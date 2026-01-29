package metrics

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/resillm/resillm/internal/config"
	openai "github.com/sashabaranov/go-openai"
)

// Collector collects and exposes metrics
type Collector struct {
	config   config.MetricsConfig
	registry *prometheus.Registry

	// Request metrics
	requestsTotal    *prometheus.CounterVec
	requestLatency   *prometheus.HistogramVec
	requestsInFlight *prometheus.GaugeVec

	// Token metrics
	tokensTotal *prometheus.CounterVec

	// Cost metrics
	costTotal *prometheus.CounterVec

	// Error metrics
	errorsTotal *prometheus.CounterVec
	skippedTotal *prometheus.CounterVec

	// Circuit breaker metrics
	circuitState       *prometheus.GaugeVec
	circuitFailures    *prometheus.CounterVec
	circuitSuccesses   *prometheus.CounterVec
	circuitStateChanges *prometheus.CounterVec

	// Fallback metrics
	fallbacksTotal *prometheus.CounterVec

	// HTTP server metrics
	httpRequestsTotal   *prometheus.CounterVec
	httpRequestLatency  *prometheus.HistogramVec

	// Latency tracking for provider status
	latencyMu     sync.RWMutex
	latencyWindow map[string][]float64
}

// NewCollector creates a new metrics collector
func NewCollector(cfg config.MetricsConfig) *Collector {
	registry := prometheus.NewRegistry()

	c := &Collector{
		config:        cfg,
		registry:      registry,
		latencyWindow: make(map[string][]float64),
	}

	// Register default Go metrics
	registry.MustRegister(prometheus.NewGoCollector())

	// Request metrics
	c.requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_requests_total",
			Help: "Total number of requests by provider and model",
		},
		[]string{"provider", "model", "status"},
	)
	registry.MustRegister(c.requestsTotal)

	c.requestLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "resillm_request_latency_seconds",
			Help:    "Request latency in seconds",
			Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0, 120.0},
		},
		[]string{"provider", "model"},
	)
	registry.MustRegister(c.requestLatency)

	c.requestsInFlight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "resillm_requests_in_flight",
			Help: "Number of requests currently in flight",
		},
		[]string{"provider"},
	)
	registry.MustRegister(c.requestsInFlight)

	// Token metrics
	c.tokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_tokens_total",
			Help: "Total tokens processed by provider, model, and type",
		},
		[]string{"provider", "model", "type"},
	)
	registry.MustRegister(c.tokensTotal)

	// Cost metrics
	c.costTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_cost_dollars_total",
			Help: "Total cost in USD by provider and model",
		},
		[]string{"provider", "model"},
	)
	registry.MustRegister(c.costTotal)

	// Error metrics
	c.errorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_errors_total",
			Help: "Total errors by provider and error type",
		},
		[]string{"provider", "error_type"},
	)
	registry.MustRegister(c.errorsTotal)

	c.skippedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_skipped_total",
			Help: "Total skipped requests by provider and reason",
		},
		[]string{"provider", "reason"},
	)
	registry.MustRegister(c.skippedTotal)

	// Circuit breaker metrics
	c.circuitState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "resillm_circuit_state",
			Help: "Circuit breaker state (0=closed, 1=open, 2=half-open)",
		},
		[]string{"provider"},
	)
	registry.MustRegister(c.circuitState)

	c.circuitFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_circuit_failures_total",
			Help: "Total circuit breaker failures by provider",
		},
		[]string{"provider"},
	)
	registry.MustRegister(c.circuitFailures)

	c.circuitSuccesses = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_circuit_successes_total",
			Help: "Total circuit breaker successes by provider",
		},
		[]string{"provider"},
	)
	registry.MustRegister(c.circuitSuccesses)

	c.circuitStateChanges = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_circuit_state_changes_total",
			Help: "Total circuit breaker state changes",
		},
		[]string{"provider", "from", "to"},
	)
	registry.MustRegister(c.circuitStateChanges)

	// Fallback metrics
	c.fallbacksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_fallbacks_total",
			Help: "Total fallbacks by from/to provider",
		},
		[]string{"from_provider", "to_provider"},
	)
	registry.MustRegister(c.fallbacksTotal)

	// HTTP metrics
	c.httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "resillm_http_requests_total",
			Help: "Total HTTP requests by method, path, and status",
		},
		[]string{"method", "path", "status"},
	)
	registry.MustRegister(c.httpRequestsTotal)

	c.httpRequestLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "resillm_http_request_latency_seconds",
			Help:    "HTTP request latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
	registry.MustRegister(c.httpRequestLatency)

	return c
}

// Handler returns an HTTP handler for metrics
func (c *Collector) Handler() http.Handler {
	return promhttp.HandlerFor(c.registry, promhttp.HandlerOpts{})
}

// RecordSuccess records a successful request
func (c *Collector) RecordSuccess(provider, model string, usage openai.Usage, cost float64) {
	c.requestsTotal.WithLabelValues(provider, model, "success").Inc()
	c.tokensTotal.WithLabelValues(provider, model, "input").Add(float64(usage.PromptTokens))
	c.tokensTotal.WithLabelValues(provider, model, "output").Add(float64(usage.CompletionTokens))
	c.costTotal.WithLabelValues(provider, model).Add(cost)
}

// RecordFailure records a failed request
func (c *Collector) RecordFailure(provider, model string, err error) {
	c.requestsTotal.WithLabelValues(provider, model, "error").Inc()
	errorType := classifyError(err)
	c.errorsTotal.WithLabelValues(provider, errorType).Inc()
}

// RecordLatency records request latency
func (c *Collector) RecordLatency(provider, model string, latency time.Duration) {
	c.requestLatency.WithLabelValues(provider, model).Observe(latency.Seconds())

	// Store for p99 calculation
	c.latencyMu.Lock()
	window := c.latencyWindow[provider]
	window = append(window, float64(latency.Milliseconds()))
	// Keep last 100 samples
	if len(window) > 100 {
		window = window[len(window)-100:]
	}
	c.latencyWindow[provider] = window
	c.latencyMu.Unlock()
}

// RecordSkipped records a skipped provider
func (c *Collector) RecordSkipped(provider, reason string) {
	c.skippedTotal.WithLabelValues(provider, reason).Inc()
}

// SetCircuitState sets the circuit breaker state gauge
func (c *Collector) SetCircuitState(provider string, state int) {
	c.circuitState.WithLabelValues(provider).Set(float64(state))
}

// RecordCircuitSuccess records a circuit breaker success
func (c *Collector) RecordCircuitSuccess(provider string) {
	c.circuitSuccesses.WithLabelValues(provider).Inc()
}

// RecordCircuitFailure records a circuit breaker failure
func (c *Collector) RecordCircuitFailure(provider string) {
	c.circuitFailures.WithLabelValues(provider).Inc()
}

// RecordCircuitStateChange records a circuit breaker state change
func (c *Collector) RecordCircuitStateChange(provider, from, to string) {
	c.circuitStateChanges.WithLabelValues(provider, from, to).Inc()
}

// RecordFallback records a fallback
func (c *Collector) RecordFallback(fromProvider, toProvider string) {
	c.fallbacksTotal.WithLabelValues(fromProvider, toProvider).Inc()
}

// RecordHTTPRequest records an HTTP request
func (c *Collector) RecordHTTPRequest(method, path string, status int, latency time.Duration) {
	statusStr := classifyHTTPStatus(status)
	c.httpRequestsTotal.WithLabelValues(method, path, statusStr).Inc()
	c.httpRequestLatency.WithLabelValues(method, path).Observe(latency.Seconds())
}

// GetLatencyP99 returns the p99 latency for a provider (from recent samples)
func (c *Collector) GetLatencyP99(provider string) float64 {
	c.latencyMu.RLock()
	defer c.latencyMu.RUnlock()

	window := c.latencyWindow[provider]
	if len(window) == 0 {
		return 0
	}

	// Simple p99 calculation (for small windows)
	sorted := make([]float64, len(window))
	copy(sorted, window)

	// Bubble sort (fine for 100 elements)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	idx := int(float64(len(sorted)) * 0.99)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}

	return sorted[idx]
}

// Helper functions

func classifyError(err error) string {
	if err == nil {
		return "none"
	}

	errStr := err.Error()
	switch {
	case contains(errStr, "timeout"):
		return "timeout"
	case contains(errStr, "429"):
		return "rate_limit"
	case contains(errStr, "500", "502", "503", "504"):
		return "server_error"
	case contains(errStr, "connection"):
		return "connection"
	default:
		return "unknown"
	}
}

func contains(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if len(substr) > 0 && len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

func classifyHTTPStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "unknown"
	}
}
