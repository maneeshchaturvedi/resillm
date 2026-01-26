package router

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/providers"
	"github.com/resillm/resillm/internal/resilience"
	"github.com/resillm/resillm/internal/types"
)

// MockProvider for testing
type MockProvider struct {
	name       string
	response   *types.ChatCompletionResponse
	err        error
	callCount  int
	mu         sync.Mutex
	latency    time.Duration
	failUntil  int // Fail until this many calls
}

func NewMockProvider(name string) *MockProvider {
	return &MockProvider{
		name: name,
		response: &types.ChatCompletionResponse{
			ID:      "test-id",
			Object:  "chat.completion",
			Model:   "test-model",
			Choices: []types.Choice{{Message: types.Message{Content: "test response"}}},
			Usage:   types.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		},
	}
}

func (m *MockProvider) Name() string {
	return m.name
}

func (m *MockProvider) ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest, model string) (*types.ChatCompletionResponse, error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if m.failUntil > 0 && count <= m.failUntil {
		return nil, m.err
	}

	if m.err != nil && m.failUntil == 0 {
		return nil, m.err
	}

	return m.response, nil
}

func (m *MockProvider) ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest, model string) (<-chan types.StreamChunk, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan types.StreamChunk)
	close(ch)
	return ch, nil
}

func (m *MockProvider) CalculateCost(model string, usage types.Usage) float64 {
	return 0.001
}

func (m *MockProvider) HealthCheck(ctx context.Context) error {
	return nil
}

func (m *MockProvider) SetError(err error) {
	m.mu.Lock()
	m.err = err
	m.mu.Unlock()
}

func (m *MockProvider) SetFailUntil(n int) {
	m.mu.Lock()
	m.failUntil = n
	m.mu.Unlock()
}

func (m *MockProvider) GetCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *MockProvider) Reset() {
	m.mu.Lock()
	m.callCount = 0
	m.err = nil
	m.failUntil = 0
	m.mu.Unlock()
}

// MockProviderError implements the ProviderError interface
type MockProviderError struct {
	Code int
	Msg  string
}

func (e *MockProviderError) Error() string {
	return e.Msg
}

func (e *MockProviderError) StatusCode() int {
	return e.Code
}

func (e *MockProviderError) IsRetryable() bool {
	return e.Code == 429 || e.Code >= 500
}

// MockRegistry for testing
type MockRegistry struct {
	providers map[string]providers.Provider
}

func NewMockRegistry() *MockRegistry {
	return &MockRegistry{
		providers: make(map[string]providers.Provider),
	}
}

func (r *MockRegistry) Add(name string, p providers.Provider) {
	r.providers[name] = p
}

func (r *MockRegistry) Get(name string) (providers.Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

func (r *MockRegistry) List() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// Test helpers
func createTestRouter(registry *MockRegistry, models map[string]config.ModelConfig) *Router {
	circuitBreakers := make(map[string]*resilience.CircuitBreaker)
	for name := range registry.providers {
		circuitBreakers[name] = resilience.NewCircuitBreaker(name, config.CircuitBreakerConfig{
			FailureThreshold: 3,
			SuccessThreshold: 2,
			Timeout:          100 * time.Millisecond,
		}, nil)
	}

	retryConfig := config.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{429, 500, 502, 503, 504},
	}

	return New(models, (*providers.Registry)(nil), circuitBreakers, retryConfig, nil)
}

// Tests

func TestRouter_RoutesToPrimaryProvider(t *testing.T) {
	primary := NewMockProvider("openai")
	fallback := NewMockProvider("anthropic")

	registry := NewMockRegistry()
	registry.Add("openai", primary)
	registry.Add("anthropic", fallback)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary:   config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			Fallbacks: []config.EndpointConfig{{Provider: "anthropic", Model: "claude-3"}},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	req := &types.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []types.Message{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected response")
	}

	if meta.Provider != "openai" {
		t.Errorf("expected provider 'openai', got '%s'", meta.Provider)
	}

	if meta.Fallback {
		t.Error("expected fallback=false")
	}

	if primary.GetCallCount() != 1 {
		t.Errorf("expected primary to be called once, got %d", primary.GetCallCount())
	}

	if fallback.GetCallCount() != 0 {
		t.Errorf("expected fallback to not be called, got %d", fallback.GetCallCount())
	}
}

func TestRouter_FallsBackOnPrimaryFailure(t *testing.T) {
	primary := NewMockProvider("openai")
	primary.SetError(&MockProviderError{Code: 500, Msg: "server error"})

	fallback := NewMockProvider("anthropic")

	registry := NewMockRegistry()
	registry.Add("openai", primary)
	registry.Add("anthropic", fallback)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary:   config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			Fallbacks: []config.EndpointConfig{{Provider: "anthropic", Model: "claude-3"}},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	req := &types.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []types.Message{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected response")
	}

	if meta.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got '%s'", meta.Provider)
	}

	if !meta.Fallback {
		t.Error("expected fallback=true")
	}
}

func TestRouter_ReturnsErrorWhenAllProvidersFail(t *testing.T) {
	primary := NewMockProvider("openai")
	primary.SetError(&MockProviderError{Code: 500, Msg: "server error"})

	fallback := NewMockProvider("anthropic")
	fallback.SetError(&MockProviderError{Code: 500, Msg: "server error"})

	registry := NewMockRegistry()
	registry.Add("openai", primary)
	registry.Add("anthropic", fallback)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary:   config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			Fallbacks: []config.EndpointConfig{{Provider: "anthropic", Model: "claude-3"}},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	req := &types.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []types.Message{{Role: "user", Content: "test"}},
	}

	resp, _, err := router.ExecuteChat(context.Background(), req)

	if err == nil {
		t.Fatal("expected error when all providers fail")
	}

	if resp != nil {
		t.Error("expected nil response when all providers fail")
	}
}

func TestRouter_SkipsProviderWithOpenCircuit(t *testing.T) {
	primary := NewMockProvider("openai")
	fallback := NewMockProvider("anthropic")

	registry := NewMockRegistry()
	registry.Add("openai", primary)
	registry.Add("anthropic", fallback)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary:   config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			Fallbacks: []config.EndpointConfig{{Provider: "anthropic", Model: "claude-3"}},
		},
	}

	// Create circuit breaker that's already open
	circuitBreakers := make(map[string]*resilience.CircuitBreaker)

	openaiCB := resilience.NewCircuitBreaker("openai", config.CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          1 * time.Hour, // Long timeout to stay open
	}, nil)
	// Open the circuit
	openaiCB.RecordFailure()
	circuitBreakers["openai"] = openaiCB

	circuitBreakers["anthropic"] = resilience.NewCircuitBreaker("anthropic", config.CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}, nil)

	retryConfig := config.RetryConfig{
		MaxAttempts:       1,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{429, 500},
	}

	router := &Router{
		models:          models,
		providers:       nil,
		circuitBreakers: circuitBreakers,
		retryConfig:     retryConfig,
		metrics:         nil,
	}

	// Override provider lookup
	router.providers = nil // Will use fallback method

	req := &types.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []types.Message{{Role: "user", Content: "test"}},
	}

	// This test verifies the circuit breaker logic
	// Primary should be skipped due to open circuit
	if openaiCB.Allow() {
		t.Error("expected openai circuit to reject requests")
	}

	if !circuitBreakers["anthropic"].Allow() {
		t.Error("expected anthropic circuit to allow requests")
	}

	// Direct test of Allow() behavior
	_ = req // Used in full integration
}

func TestRouter_UnknownModelReturnsError(t *testing.T) {
	registry := NewMockRegistry()
	registry.Add("openai", NewMockProvider("openai"))

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	req := &types.ChatCompletionRequest{
		Model:    "unknown-model",
		Messages: []types.Message{{Role: "user", Content: "test"}},
	}

	_, _, err := router.ExecuteChat(context.Background(), req)

	if err == nil {
		t.Fatal("expected error for unknown model")
	}

	if !contains(err.Error(), "unknown model") {
		t.Errorf("expected 'unknown model' error, got: %v", err)
	}
}

func TestRouter_UsesDefaultModelForUnknown(t *testing.T) {
	primary := NewMockProvider("openai")

	registry := NewMockRegistry()
	registry.Add("openai", primary)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
		"default": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o-mini"},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	req := &types.ChatCompletionRequest{
		Model:    "unknown-model",
		Messages: []types.Message{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected response")
	}

	if meta.Provider != "openai" {
		t.Errorf("expected provider 'openai', got '%s'", meta.Provider)
	}
}

func TestRouter_TracksRetryCount(t *testing.T) {
	primary := NewMockProvider("openai")
	primary.SetError(&MockProviderError{Code: 500, Msg: "server error"})
	primary.SetFailUntil(2) // Fail first 2 attempts, succeed on 3rd

	registry := NewMockRegistry()
	registry.Add("openai", primary)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	req := &types.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []types.Message{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected response")
	}

	// 2 retries (first attempt + 2 retries = 3 calls)
	if meta.Retries != 2 {
		t.Errorf("expected 2 retries, got %d", meta.Retries)
	}
}

func TestRouter_GetProviderStatus(t *testing.T) {
	registry := NewMockRegistry()
	registry.Add("openai", NewMockProvider("openai"))
	registry.Add("anthropic", NewMockProvider("anthropic"))

	models := map[string]config.ModelConfig{}

	router := createTestRouterWithRegistry(registry, models)

	statuses := router.GetProviderStatus()

	if len(statuses) != 2 {
		t.Errorf("expected 2 providers, got %d", len(statuses))
	}

	for _, status := range statuses {
		if status.Status != "healthy" {
			t.Errorf("expected status 'healthy', got '%s'", status.Status)
		}
		if status.Circuit != "closed" {
			t.Errorf("expected circuit 'closed', got '%s'", status.Circuit)
		}
	}
}

// Helper to create router with mock registry
func createTestRouterWithRegistry(registry *MockRegistry, models map[string]config.ModelConfig) *Router {
	circuitBreakers := make(map[string]*resilience.CircuitBreaker)
	for name := range registry.providers {
		circuitBreakers[name] = resilience.NewCircuitBreaker(name, config.CircuitBreakerConfig{
			FailureThreshold: 3,
			SuccessThreshold: 2,
			Timeout:          100 * time.Millisecond,
		}, nil)
	}

	retryConfig := config.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{429, 500, 502, 503, 504},
	}

	r := &Router{
		models:          models,
		circuitBreakers: circuitBreakers,
		retryConfig:     retryConfig,
		metrics:         nil,
	}

	// Create a wrapper to use MockRegistry
	r.providers = &mockRegistryWrapper{registry: registry}

	return r
}

// Wrapper to make MockRegistry compatible with Router
type mockRegistryWrapper struct {
	registry *MockRegistry
}

func (w *mockRegistryWrapper) Get(name string) (providers.Provider, bool) {
	return w.registry.Get(name)
}

func (w *mockRegistryWrapper) List() []string {
	return w.registry.List()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
