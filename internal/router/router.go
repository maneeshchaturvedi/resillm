package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/metrics"
	"github.com/resillm/resillm/internal/providers"
	"github.com/resillm/resillm/internal/resilience"
	"github.com/resillm/resillm/internal/types"
	"github.com/rs/zerolog/log"
)

// ProviderRegistry is the interface for provider lookups
type ProviderRegistry interface {
	Get(name string) (providers.Provider, bool)
	List() []string
}

// Router handles request routing with fallbacks and resilience
type Router struct {
	models          map[string]config.ModelConfig
	providers       ProviderRegistry
	circuitBreakers map[string]*resilience.CircuitBreaker
	retryConfig     config.RetryConfig
	metrics         *metrics.Collector

	mu sync.RWMutex
}

// New creates a new router
func New(
	models map[string]config.ModelConfig,
	providerRegistry ProviderRegistry,
	circuitBreakers map[string]*resilience.CircuitBreaker,
	retryConfig config.RetryConfig,
	metricsCollector *metrics.Collector,
) *Router {
	return &Router{
		models:          models,
		providers:       providerRegistry,
		circuitBreakers: circuitBreakers,
		retryConfig:     retryConfig,
		metrics:         metricsCollector,
	}
}

// ExecuteChat routes a chat completion request with fallback handling
func (r *Router) ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest) (*types.ChatCompletionResponse, *types.ExecutionMeta, error) {
	// Look up model config
	modelConfig, ok := r.models[req.Model]
	if !ok {
		// Try default if available
		modelConfig, ok = r.models["default"]
		if !ok {
			return nil, nil, fmt.Errorf("unknown model: %s", req.Model)
		}
	}

	// Build endpoint chain: primary + fallbacks
	endpoints := []config.EndpointConfig{modelConfig.Primary}
	endpoints = append(endpoints, modelConfig.Fallbacks...)

	meta := &types.ExecutionMeta{
		Retries:  0,
		Fallback: false,
	}

	var lastErr error

	for i, endpoint := range endpoints {
		// Check circuit breaker
		cb, ok := r.circuitBreakers[endpoint.Provider]
		if ok && !cb.Allow() {
			log.Debug().
				Str("provider", endpoint.Provider).
				Msg("Circuit breaker open, skipping provider")
			if r.metrics != nil {
				r.metrics.RecordSkipped(endpoint.Provider, "circuit_open")
			}
			continue
		}

		// Get provider
		provider, ok := r.providers.Get(endpoint.Provider)
		if !ok {
			log.Warn().
				Str("provider", endpoint.Provider).
				Msg("Provider not found, skipping")
			continue
		}

		// Execute with retry
		retrier := resilience.NewRetrier(r.retryConfig)
		result, attempts, err := retrier.ExecuteWithResult(ctx, func() (interface{}, error) {
			return provider.ExecuteChat(ctx, req, endpoint.Model)
		})

		meta.Retries += attempts - 1 // Don't count first attempt as retry

		if err == nil {
			resp := result.(*types.ChatCompletionResponse)

			// Record success
			if cb != nil {
				cb.RecordSuccess()
			}

			meta.Provider = endpoint.Provider
			meta.ActualModel = endpoint.Model
			meta.Fallback = i > 0
			meta.Cost = provider.CalculateCost(endpoint.Model, resp.Usage)

			if r.metrics != nil {
				r.metrics.RecordSuccess(endpoint.Provider, endpoint.Model, resp.Usage, meta.Cost)
			}

			return resp, meta, nil
		}

		// Record failure
		lastErr = err
		if cb != nil {
			cb.RecordFailure()
		}

		if r.metrics != nil {
			r.metrics.RecordFailure(endpoint.Provider, endpoint.Model, err)
		}

		log.Warn().
			Err(err).
			Str("provider", endpoint.Provider).
			Str("model", endpoint.Model).
			Int("attempts", attempts).
			Msg("Provider failed, trying fallback")
	}

	return nil, meta, fmt.Errorf("all providers failed: %w", lastErr)
}

// ExecuteChatStream routes a streaming chat completion request
func (r *Router) ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest) (<-chan types.StreamChunk, *types.ExecutionMeta, error) {
	// Look up model config
	modelConfig, ok := r.models[req.Model]
	if !ok {
		modelConfig, ok = r.models["default"]
		if !ok {
			return nil, nil, fmt.Errorf("unknown model: %s", req.Model)
		}
	}

	// Build endpoint chain
	endpoints := []config.EndpointConfig{modelConfig.Primary}
	endpoints = append(endpoints, modelConfig.Fallbacks...)

	meta := &types.ExecutionMeta{
		Retries:  0,
		Fallback: false,
	}

	var lastErr error

	for i, endpoint := range endpoints {
		// Check circuit breaker
		cb, ok := r.circuitBreakers[endpoint.Provider]
		if ok && !cb.Allow() {
			continue
		}

		// Get provider
		provider, ok := r.providers.Get(endpoint.Provider)
		if !ok {
			continue
		}

		// For streaming, we don't retry at the request level
		// (would need to buffer, which defeats the purpose)
		streamChan, err := provider.ExecuteChatStream(ctx, req, endpoint.Model)
		if err != nil {
			lastErr = err
			if cb != nil {
				cb.RecordFailure()
			}
			continue
		}

		// Success
		if cb != nil {
			cb.RecordSuccess()
		}

		meta.Provider = endpoint.Provider
		meta.ActualModel = endpoint.Model
		meta.Fallback = i > 0

		return streamChan, meta, nil
	}

	return nil, meta, fmt.Errorf("all providers failed: %w", lastErr)
}

// GetProviderStatus returns status of all providers
func (r *Router) GetProviderStatus() []ProviderStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var statuses []ProviderStatus

	for _, name := range r.providers.List() {
		status := ProviderStatus{
			Name:   name,
			Status: "healthy",
		}

		if cb, ok := r.circuitBreakers[name]; ok {
			stats := cb.Stats()
			status.Circuit = stats.State
			if stats.State != "closed" {
				status.Status = "degraded"
			}
		}

		// Get latency from metrics if available
		if r.metrics != nil {
			status.LatencyP99 = r.metrics.GetLatencyP99(name)
		}

		statuses = append(statuses, status)
	}

	return statuses
}

// ProviderStatus represents the status of a provider
type ProviderStatus struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Circuit    string  `json:"circuit"`
	LatencyP99 float64 `json:"latency_p99_ms,omitempty"`
}

// UpdateModels atomically updates the model configuration
func (r *Router) UpdateModels(models map[string]config.ModelConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models = models
}

// UpdateRetryConfig atomically updates the retry configuration
func (r *Router) UpdateRetryConfig(cfg config.RetryConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryConfig = cfg
}
