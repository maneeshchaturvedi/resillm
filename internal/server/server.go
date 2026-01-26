package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/resillm/resillm/internal/budget"
	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/metrics"
	"github.com/resillm/resillm/internal/providers"
	"github.com/resillm/resillm/internal/resilience"
	"github.com/resillm/resillm/internal/router"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// Server represents the resillm proxy server
type Server struct {
	cfg        *config.Config
	cfgPath    string
	httpServer *http.Server
	router     *router.Router
	metrics    *metrics.Collector
	budget     *budget.Tracker
	watcher    *config.Watcher

	// Rate limiter
	rateLimiter *RateLimiter

	// For thread-safe config updates
	mu               sync.RWMutex
	circuitBreakers  map[string]*resilience.CircuitBreaker
	providerRegistry *providers.Registry
}

// RateLimiter implements per-IP rate limiting
type RateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	rate     rate.Limit
	burst    int
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Limit(rps),
		burst:    burst,
	}
}

// GetLimiter returns the rate limiter for a given IP
func (rl *RateLimiter) GetLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limiter, exists := rl.limiters[ip]
	if !exists {
		limiter = rate.NewLimiter(rl.rate, rl.burst)
		rl.limiters[ip] = limiter
	}

	return limiter
}

// New creates a new server instance
func New(cfg *config.Config) (*Server, error) {
	return NewWithPath(cfg, "")
}

// NewWithPath creates a new server instance with config hot-reload support
func NewWithPath(cfg *config.Config, configPath string) (*Server, error) {
	// Initialize metrics collector
	metricsCollector := metrics.NewCollector(cfg.Metrics)

	// Initialize budget tracker
	budgetTracker := budget.NewTracker(budget.Config{
		Enabled:          cfg.Budget.Enabled,
		MaxCostPerHour:   cfg.Budget.MaxCostPerHour,
		MaxCostPerDay:    cfg.Budget.MaxCostPerDay,
		AlertThreshold:   cfg.Budget.AlertThreshold,
		ActionOnExceeded: cfg.Budget.ActionOnExceeded,
	})

	// Initialize providers
	providerRegistry, err := providers.NewRegistry(cfg.Providers, cfg.Resilience.Timeout)
	if err != nil {
		return nil, fmt.Errorf("initializing providers: %w", err)
	}

	// Initialize circuit breakers for each provider
	circuitBreakers := make(map[string]*resilience.CircuitBreaker)
	for name := range cfg.Providers {
		circuitBreakers[name] = resilience.NewCircuitBreaker(
			name,
			cfg.Resilience.CircuitBreaker,
			metricsCollector,
		)
	}

	// Initialize router
	r := router.New(
		cfg.Models,
		providerRegistry,
		circuitBreakers,
		cfg.Resilience.Retry,
		metricsCollector,
	)

	// Setup HTTP handlers
	mux := http.NewServeMux()

	// Initialize rate limiter if enabled
	var rateLimiter *RateLimiter
	if cfg.Server.RateLimit.Enabled {
		rateLimiter = NewRateLimiter(cfg.Server.RateLimit.RequestsPerSecond, cfg.Server.RateLimit.Burst)
	}

	s := &Server{
		cfg:              cfg,
		cfgPath:          configPath,
		router:           r,
		metrics:          metricsCollector,
		budget:           budgetTracker,
		circuitBreakers:  circuitBreakers,
		providerRegistry: providerRegistry,
		rateLimiter:      rateLimiter,
	}

	// OpenAI-compatible endpoints
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/completions", s.handleCompletions)
	mux.HandleFunc("POST /v1/embeddings", s.handleEmbeddings)

	// Health and status endpoints
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /v1/providers", s.handleProviders)
	mux.HandleFunc("GET /v1/budget", s.handleBudget)

	// Admin endpoints (protected with authentication)
	mux.HandleFunc("POST /admin/reload", s.adminAuthMiddleware(s.handleReload))

	// Wrap with middleware chain
	handler := s.securityHeadersMiddleware(mux)
	handler = s.loggingMiddleware(handler)
	handler = s.metricsMiddleware(handler)
	if rateLimiter != nil {
		handler = s.rateLimitMiddleware(handler)
	}

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: cfg.Resilience.Timeout.Request + 10*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Setup config watcher if path is provided
	if configPath != "" {
		watcher, err := config.NewWatcher(configPath, s.applyConfig)
		if err != nil {
			return nil, fmt.Errorf("creating config watcher: %w", err)
		}
		s.watcher = watcher
	}

	return s, nil
}

// applyConfig applies a new configuration to the server
func (s *Server) applyConfig(newCfg *config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldCfg := s.cfg
	diff := config.Diff(oldCfg, newCfg)

	var changes []string

	// Update models if changed
	if diff.ModelsChanged {
		s.router.UpdateModels(newCfg.Models)
		changes = append(changes, "models")
		log.Info().Msg("Models configuration updated")
	}

	// Update resilience settings if changed
	if diff.ResilienceChanged {
		s.router.UpdateRetryConfig(newCfg.Resilience.Retry)
		// Update circuit breaker configs
		for name, cb := range s.circuitBreakers {
			cb.UpdateConfig(newCfg.Resilience.CircuitBreaker)
			_ = name // Used for logging if needed
		}
		changes = append(changes, "resilience")
		log.Info().Msg("Resilience configuration updated")
	}

	// Update budget settings if changed
	if diff.BudgetChanged {
		s.budget = budget.NewTracker(budget.Config{
			Enabled:          newCfg.Budget.Enabled,
			MaxCostPerHour:   newCfg.Budget.MaxCostPerHour,
			MaxCostPerDay:    newCfg.Budget.MaxCostPerDay,
			AlertThreshold:   newCfg.Budget.AlertThreshold,
			ActionOnExceeded: newCfg.Budget.ActionOnExceeded,
		})
		changes = append(changes, "budget")
		log.Info().Msg("Budget configuration updated")
	}

	// Update logging settings if changed
	if diff.LoggingChanged {
		// Logging level change would need zerolog reconfiguration
		// For now, just update the config
		changes = append(changes, "logging")
		log.Info().Str("level", newCfg.Logging.Level).Msg("Logging configuration updated")
	}

	// Note: Provider credential changes require reconnection
	// For now, log a warning if providers changed
	if diff.ProvidersChanged {
		log.Warn().Msg("Provider configuration changed - restart required for credential updates")
	}

	// Store the new config
	s.cfg = newCfg

	if len(changes) > 0 {
		log.Info().Strs("changes", changes).Msg("Configuration reload complete")
	} else {
		log.Info().Msg("No configuration changes detected")
	}

	return nil
}

// ReloadConfig manually triggers a configuration reload
func (s *Server) ReloadConfig() error {
	if s.watcher != nil {
		return s.watcher.Reload()
	}

	// If no watcher, try to reload from path
	if s.cfgPath != "" {
		newCfg, err := config.Load(s.cfgPath)
		if err != nil {
			return err
		}
		return s.applyConfig(newCfg)
	}

	return fmt.Errorf("no config path configured for reload")
}

// Run starts the server and blocks until context is cancelled
func (s *Server) Run(ctx context.Context) error {
	// Start config watcher if configured
	if s.watcher != nil {
		if err := s.watcher.Start(ctx); err != nil {
			log.Error().Err(err).Msg("Failed to start config watcher")
		}
		defer s.watcher.Stop()
	}

	// Start metrics server if enabled
	if s.cfg.Metrics.Enabled && s.cfg.Metrics.Prometheus.Enabled {
		go s.runMetricsServer()
	}

	// Start main server
	errChan := make(chan error, 1)
	go func() {
		log.Info().
			Str("addr", s.httpServer.Addr).
			Msg("Starting HTTP server")
		if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		// Graceful shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	case err := <-errChan:
		return err
	}
}

func (s *Server) runMetricsServer() {
	mux := http.NewServeMux()
	mux.Handle(s.cfg.Metrics.Prometheus.Path, s.metrics.Handler())

	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.MetricsPort)
	log.Info().
		Str("addr", addr).
		Str("path", s.cfg.Metrics.Prometheus.Path).
		Msg("Starting metrics server")

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Error().Err(err).Msg("Metrics server error")
	}
}

// Middleware
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		if s.cfg.Logging.LogRequests {
			log.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", wrapped.statusCode).
				Dur("latency", time.Since(start)).
				Msg("Request")
		}
	})
}

func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		s.metrics.RecordHTTPRequest(r.Method, r.URL.Path, wrapped.statusCode, time.Since(start))
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// securityHeadersMiddleware adds security headers to all responses
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware implements per-IP rate limiting
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		limiter := s.rateLimiter.GetLimiter(ip)

		if !limiter.Allow() {
			http.Error(w, `{"error":{"type":"rate_limit_exceeded","message":"Too many requests"}}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// adminAuthMiddleware protects admin endpoints with API key authentication
func (s *Server) adminAuthMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If no admin key configured, allow access (for development)
		if s.cfg.Server.AdminAPIKey == "" {
			log.Warn().Msg("Admin endpoint accessed without authentication (no admin_api_key configured)")
			handler(w, r)
			return
		}

		// Check X-Admin-Key header
		providedKey := r.Header.Get("X-Admin-Key")
		if providedKey == "" {
			http.Error(w, `{"error":{"type":"unauthorized","message":"X-Admin-Key header required"}}`, http.StatusUnauthorized)
			return
		}

		if providedKey != s.cfg.Server.AdminAPIKey {
			http.Error(w, `{"error":{"type":"forbidden","message":"Invalid admin key"}}`, http.StatusForbidden)
			return
		}

		handler(w, r)
	}
}

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for proxied requests)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the chain
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}
