package providers

import (
	"context"
	"fmt"
	"io"

	"github.com/resillm/resillm/internal/config"
	openai "github.com/sashabaranov/go-openai"
)

// ChatStream is an interface for streaming chat completion responses.
// This abstracts away the concrete *openai.ChatCompletionStream type,
// enabling testing with mocks and supporting alternative implementations.
type ChatStream interface {
	// Recv returns the next chunk from the stream.
	// Returns io.EOF when the stream is complete.
	Recv() (openai.ChatCompletionStreamResponse, error)

	// Close closes the stream and releases resources.
	Close()
}

// Provider defines the interface for LLM providers
type Provider interface {
	// Name returns the provider name
	Name() string

	// ExecuteChat sends a chat completion request
	ExecuteChat(ctx context.Context, req openai.ChatCompletionRequest, model string) (openai.ChatCompletionResponse, error)

	// ExecuteChatStream sends a streaming chat completion request
	ExecuteChatStream(ctx context.Context, req openai.ChatCompletionRequest, model string) (ChatStream, error)

	// CalculateCost calculates the cost for a request
	CalculateCost(model string, usage openai.Usage) float64

	// HealthCheck checks if the provider is healthy
	HealthCheck(ctx context.Context) error
}

// openaiStreamWrapper wraps *openai.ChatCompletionStream to implement ChatStream
type openaiStreamWrapper struct {
	stream *openai.ChatCompletionStream
}

// WrapOpenAIStream wraps an openai.ChatCompletionStream to implement the ChatStream interface
func WrapOpenAIStream(stream *openai.ChatCompletionStream) ChatStream {
	return &openaiStreamWrapper{stream: stream}
}

func (w *openaiStreamWrapper) Recv() (openai.ChatCompletionStreamResponse, error) {
	return w.stream.Recv()
}

func (w *openaiStreamWrapper) Close() {
	w.stream.Close()
}

// Ensure io.EOF is available for stream completion checking
var _ = io.EOF

// ProviderError represents an error from a provider.
// Implements both resilience.ProviderError (StatusCode()) and
// resilience.RetryableError (IsRetryable()) interfaces.
type ProviderError struct {
	Code     int
	Body     string
	Provider string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s error (status %d): %s", e.Provider, e.Code, e.Body)
}

// StatusCode returns the HTTP status code, implementing resilience.ProviderError
func (e *ProviderError) StatusCode() int {
	return e.Code
}

// IsRetryable returns true if the error is retryable, implementing resilience.RetryableError
func (e *ProviderError) IsRetryable() bool {
	switch e.Code {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
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

	for name, cfg := range configs {
		provider, err := NewGoOpenAIProvider(name, cfg)
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
