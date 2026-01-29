package router

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/providers"
	"github.com/resillm/resillm/internal/resilience"
	openai "github.com/sashabaranov/go-openai"
)

// MockChatStream implements providers.ChatStream for testing
type MockChatStream struct {
	chunks []openai.ChatCompletionStreamResponse
	index  int
	err    error
	mu     sync.Mutex
}

func NewMockChatStream(chunks []openai.ChatCompletionStreamResponse) *MockChatStream {
	return &MockChatStream{chunks: chunks}
}

func (m *MockChatStream) Recv() (openai.ChatCompletionStreamResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return openai.ChatCompletionStreamResponse{}, m.err
	}
	if m.index >= len(m.chunks) {
		return openai.ChatCompletionStreamResponse{}, io.EOF
	}
	chunk := m.chunks[m.index]
	m.index++
	return chunk, nil
}

func (m *MockChatStream) Close() {
	// No-op for mock
}

func (m *MockChatStream) SetError(err error) {
	m.mu.Lock()
	m.err = err
	m.mu.Unlock()
}

// MockProvider for testing
type MockProvider struct {
	name      string
	response  openai.ChatCompletionResponse
	err       error
	callCount int
	mu        sync.Mutex
	latency   time.Duration
	failUntil int // Fail until this many calls
}

func NewMockProvider(name string) *MockProvider {
	return &MockProvider{
		name: name,
		response: openai.ChatCompletionResponse{
			ID:      "test-id",
			Object:  "chat.completion",
			Model:   "test-model",
			Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: "test response"}}},
			Usage:   openai.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		},
	}
}

func (m *MockProvider) Name() string {
	return m.name
}

func (m *MockProvider) ExecuteChat(ctx context.Context, req openai.ChatCompletionRequest, model string) (openai.ChatCompletionResponse, error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
			return openai.ChatCompletionResponse{}, ctx.Err()
		}
	}

	if m.failUntil > 0 && count <= m.failUntil {
		return openai.ChatCompletionResponse{}, m.err
	}

	if m.err != nil && m.failUntil == 0 {
		return openai.ChatCompletionResponse{}, m.err
	}

	return m.response, nil
}

func (m *MockProvider) ExecuteChatStream(ctx context.Context, req openai.ChatCompletionRequest, model string) (providers.ChatStream, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Return a mock stream with sample chunks
	chunks := []openai.ChatCompletionStreamResponse{
		{ID: "test-stream", Choices: []openai.ChatCompletionStreamChoice{{Delta: openai.ChatCompletionStreamChoiceDelta{Role: "assistant"}}}},
		{ID: "test-stream", Choices: []openai.ChatCompletionStreamChoice{{Delta: openai.ChatCompletionStreamChoiceDelta{Content: "Hello"}}}},
		{ID: "test-stream", Choices: []openai.ChatCompletionStreamChoice{{Delta: openai.ChatCompletionStreamChoiceDelta{Content: " world"}}}},
	}
	return NewMockChatStream(chunks), nil
}

func (m *MockProvider) CalculateCost(model string, usage openai.Usage) float64 {
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

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID == "" {
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

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID == "" {
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

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	_, _, err := router.ExecuteChat(context.Background(), req)

	if err == nil {
		t.Fatal("expected error when all providers fail")
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

	// Create circuit breaker that's already open for primary
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

	// Create router with custom circuit breakers
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
		semaphores:      providers.NewProviderSemaphores(200),
		providers:       &mockRegistryWrapper{registry: registry},
	}

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	resp, meta, err := r.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID == "" {
		t.Fatal("expected response")
	}

	// Primary should be skipped due to open circuit, fallback should be used
	if meta.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic' (fallback), got '%s'", meta.Provider)
	}

	if !meta.Fallback {
		t.Error("expected fallback=true since primary was skipped")
	}

	// Primary should NOT have been called
	if primary.GetCallCount() != 0 {
		t.Errorf("expected primary to not be called (circuit open), got %d calls", primary.GetCallCount())
	}

	// Fallback should have been called
	if fallback.GetCallCount() != 1 {
		t.Errorf("expected fallback to be called once, got %d", fallback.GetCallCount())
	}
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

	req := openai.ChatCompletionRequest{
		Model:    "unknown-model",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
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

	req := openai.ChatCompletionRequest{
		Model:    "unknown-model",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID == "" {
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

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	resp, meta, err := router.ExecuteChat(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID == "" {
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

func TestRouter_ExecuteChatStream_Success(t *testing.T) {
	primary := NewMockProvider("openai")

	registry := NewMockRegistry()
	registry.Add("openai", primary)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	result, meta, err := router.ExecuteChatStream(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected stream result")
	}

	if result.Stream == nil {
		t.Fatal("expected stream")
	}

	if meta.Provider != "openai" {
		t.Errorf("expected provider 'openai', got '%s'", meta.Provider)
	}

	if meta.Fallback {
		t.Error("expected fallback=false")
	}

	// Read chunks from the stream
	var chunks []openai.ChatCompletionStreamResponse
	for {
		chunk, err := result.Stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected stream error: %v", err)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(chunks))
	}

	// Cleanup
	result.Stream.Close()
	result.Sem.Release()
}

func TestRouter_ExecuteChatStream_Fallback(t *testing.T) {
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

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	result, meta, err := router.ExecuteChatStream(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected stream result")
	}

	if meta.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got '%s'", meta.Provider)
	}

	if !meta.Fallback {
		t.Error("expected fallback=true")
	}

	// Cleanup
	result.Stream.Close()
	result.Sem.Release()
}

func TestRouter_ExecuteChatStream_AllProvidersFail(t *testing.T) {
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

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	_, _, err := router.ExecuteChatStream(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when all providers fail")
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
		semaphores:      providers.NewProviderSemaphores(200),
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

// Integration and concurrent request tests

func TestRouter_ConcurrentRequests(t *testing.T) {
	primary := NewMockProvider("openai")

	registry := NewMockRegistry()
	registry.Add("openai", primary)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	// Launch multiple concurrent requests
	numRequests := 50
	var wg sync.WaitGroup
	errors := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := openai.ChatCompletionRequest{
				Model:    "gpt-4o",
				Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
			}

			_, _, err := router.ExecuteChat(context.Background(), req)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("concurrent request failed: %v", err)
	}

	// All requests should have been processed
	if primary.GetCallCount() != numRequests {
		t.Errorf("expected %d calls, got %d", numRequests, primary.GetCallCount())
	}
}

func TestRouter_ConcurrentRequestsWithFallback(t *testing.T) {
	primary := NewMockProvider("openai")
	// Make primary fail intermittently
	primary.SetError(&MockProviderError{Code: 500, Msg: "server error"})
	primary.SetFailUntil(25) // First 25 attempts will fail

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

	numRequests := 30
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := openai.ChatCompletionRequest{
				Model:    "gpt-4o",
				Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
			}

			_, _, err := router.ExecuteChat(context.Background(), req)
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// All requests should eventually succeed (via primary retry or fallback)
	if successCount != numRequests {
		t.Errorf("expected %d successes, got %d", numRequests, successCount)
	}
}

func TestRouter_SemaphoreLimiting(t *testing.T) {
	slowProvider := NewMockProvider("openai")
	slowProvider.latency = 50 * time.Millisecond

	registry := NewMockRegistry()
	registry.Add("openai", slowProvider)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	// Create router with very low semaphore limit
	maxConcurrent := 3
	circuitBreakers := make(map[string]*resilience.CircuitBreaker)
	circuitBreakers["openai"] = resilience.NewCircuitBreaker("openai", config.CircuitBreakerConfig{
		FailureThreshold: 10,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}, nil)

	retryConfig := config.RetryConfig{
		MaxAttempts:       1,
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
		semaphores:      providers.NewProviderSemaphores(maxConcurrent),
		providers:       &mockRegistryWrapper{registry: registry},
	}

	// Launch more concurrent requests than the semaphore allows
	numRequests := 10
	var wg sync.WaitGroup
	startTime := time.Now()

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := openai.ChatCompletionRequest{
				Model:    "gpt-4o",
				Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
			}

			_, _, _ = r.ExecuteChat(context.Background(), req)
		}()
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	// With 10 requests, 50ms each, and max 3 concurrent,
	// minimum time is ceil(10/3) * 50ms = 4 * 50ms = 200ms
	// Allow some slack for scheduling
	minExpected := 150 * time.Millisecond
	if elapsed < minExpected {
		t.Errorf("expected at least %v (semaphore limiting), but took %v", minExpected, elapsed)
	}
}

func TestRouter_ContextCancellation(t *testing.T) {
	slowProvider := NewMockProvider("openai")
	slowProvider.latency = 500 * time.Millisecond

	registry := NewMockRegistry()
	registry.Add("openai", slowProvider)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	start := time.Now()
	_, _, err := router.ExecuteChat(ctx, req)
	elapsed := time.Since(start)

	// Should have cancelled quickly
	if elapsed > 200*time.Millisecond {
		t.Errorf("expected quick cancellation, but took %v", elapsed)
	}

	// Should have an error
	if err == nil {
		t.Error("expected error from context cancellation")
	}
}

func TestRouter_StreamConcurrent(t *testing.T) {
	primary := NewMockProvider("openai")

	registry := NewMockRegistry()
	registry.Add("openai", primary)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	router := createTestRouterWithRegistry(registry, models)

	// Launch multiple concurrent streaming requests
	numRequests := 20
	var wg sync.WaitGroup
	errors := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := openai.ChatCompletionRequest{
				Model:    "gpt-4o",
				Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
			}

			result, _, err := router.ExecuteChatStream(context.Background(), req)
			if err != nil {
				errors <- err
				return
			}

			// Consume the stream
			for {
				_, err := result.Stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					errors <- err
					break
				}
			}

			result.Stream.Close()
			result.Sem.Release()
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("concurrent stream request failed: %v", err)
	}
}

func TestRouter_CircuitBreakerOpensAfterFailures(t *testing.T) {
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

	// Create circuit breaker with low threshold
	circuitBreakers := make(map[string]*resilience.CircuitBreaker)
	circuitBreakers["openai"] = resilience.NewCircuitBreaker("openai", config.CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          1 * time.Hour,
	}, nil)
	circuitBreakers["anthropic"] = resilience.NewCircuitBreaker("anthropic", config.CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}, nil)

	retryConfig := config.RetryConfig{
		MaxAttempts:       1, // No retries for this test
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
		semaphores:      providers.NewProviderSemaphores(200),
		providers:       &mockRegistryWrapper{registry: registry},
	}

	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	// First few requests: primary fails, falls back
	for i := 0; i < 3; i++ {
		_, meta, err := r.ExecuteChat(context.Background(), req)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		// Fallback should be used
		if meta.Provider != "anthropic" {
			t.Errorf("request %d: expected fallback provider, got %s", i, meta.Provider)
		}
	}

	// Circuit breaker should now be open
	if circuitBreakers["openai"].Allow() {
		t.Error("expected openai circuit to be open after failures")
	}

	// Reset primary call count
	primary.Reset()

	// Next request: primary should be skipped entirely
	_, meta, err := r.ExecuteChat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.Provider != "anthropic" {
		t.Errorf("expected anthropic (circuit open), got %s", meta.Provider)
	}

	// Primary should NOT have been called (circuit is open)
	if primary.GetCallCount() != 0 {
		t.Errorf("expected primary to not be called (circuit open), got %d calls", primary.GetCallCount())
	}
}
