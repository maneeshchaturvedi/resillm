package providers

import (
	"context"
	"fmt"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/types"
)

// Provider defines the interface for LLM providers
type Provider interface {
	// Name returns the provider name
	Name() string

	// ExecuteChat sends a chat completion request
	ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest, model string) (*types.ChatCompletionResponse, error)

	// ExecuteChatStream sends a streaming chat completion request
	ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest, model string) (<-chan types.StreamChunk, error)

	// CalculateCost calculates the cost for a request
	CalculateCost(model string, usage types.Usage) float64

	// HealthCheck checks if the provider is healthy
	HealthCheck(ctx context.Context) error
}

// Registry manages all configured providers
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a new provider registry
func NewRegistry(configs map[string]config.ProviderConfig, timeout config.TimeoutConfig) (*Registry, error) {
	registry := &Registry{
		providers: make(map[string]Provider),
	}

	// Create HTTP client with proper timeout configuration
	// Uses connect timeout for dial and TLS handshake, not just request timeout
	httpClientCfg := DefaultHTTPClientConfig(timeout)
	httpClient := NewHTTPClient(httpClientCfg)

	for name, cfg := range configs {
		var provider Provider
		var err error

		switch name {
		case "openai":
			provider, err = NewOpenAIProvider(cfg, httpClient)
		case "anthropic":
			provider, err = NewAnthropicProvider(cfg, httpClient)
		case "azure-openai", "azure":
			provider, err = NewAzureOpenAIProvider(cfg, httpClient)
		case "ollama":
			provider, err = NewOllamaProvider(cfg, httpClient)
		default:
			// Try to determine from base URL or use generic OpenAI-compatible
			provider, err = NewOpenAICompatibleProvider(name, cfg, httpClient)
		}

		if err != nil {
			return nil, fmt.Errorf("initializing provider %s: %w", name, err)
		}

		registry.providers[name] = provider
	}

	return registry, nil
}

// Get returns a provider by name
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// List returns all provider names
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
