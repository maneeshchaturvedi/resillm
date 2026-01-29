package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/resillm/resillm/internal/budget"
	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/metrics"
	"github.com/resillm/resillm/internal/providers"
	"github.com/resillm/resillm/internal/resilience"
	"github.com/resillm/resillm/internal/router"
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

// MockProvider for testing
type MockProvider struct {
	name      string
	response  openai.ChatCompletionResponse
	err       error
	callCount int
	mu        sync.Mutex
	latency   time.Duration
	failUntil int
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

func (m *MockProvider) GetCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
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

// MockProviderError implements the provider error interface
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

// Test helpers

func createTestServer(registry *MockRegistry, models map[string]config.ModelConfig) *Server {
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

	r := router.New(models, registry, circuitBreakers, retryConfig, nil, 200)

	cfg := &config.Config{
		Budget: config.BudgetConfig{
			Enabled:          true,
			MaxCostPerHour:   100.0,
			MaxCostPerDay:    1000.0,
			AlertThreshold:   0.8,
			ActionOnExceeded: "reject",
		},
		Logging: config.LoggingConfig{
			LogRequests: false,
		},
		Metrics: config.MetricsConfig{
			Enabled: false,
		},
	}

	budgetTracker := budget.NewTracker(budget.Config{
		Enabled:          cfg.Budget.Enabled,
		MaxCostPerHour:   cfg.Budget.MaxCostPerHour,
		MaxCostPerDay:    cfg.Budget.MaxCostPerDay,
		AlertThreshold:   cfg.Budget.AlertThreshold,
		ActionOnExceeded: cfg.Budget.ActionOnExceeded,
	})

	return &Server{
		cfg:     cfg,
		router:  r,
		metrics: metrics.NewCollector(cfg.Metrics),
		budget:  budgetTracker,
	}
}

// Tests

func TestHandleChatCompletions_Success(t *testing.T) {
	provider := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", provider)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	server := createTestServer(registry, models)

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var chatResp openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if chatResp.ID == "" {
		t.Error("expected non-empty ID")
	}

	if len(chatResp.Choices) != 1 {
		t.Errorf("expected 1 choice, got %d", len(chatResp.Choices))
	}

	if chatResp.Choices[0].Message.Content != "test response" {
		t.Errorf("expected 'test response', got '%s'", chatResp.Choices[0].Message.Content)
	}

	// Check resillm metadata in headers
	if resp.Header.Get("X-Resillm-Provider") != "openai" {
		t.Errorf("expected X-Resillm-Provider 'openai', got '%s'", resp.Header.Get("X-Resillm-Provider"))
	}

	if resp.Header.Get("X-Resillm-Fallback") != "false" {
		t.Errorf("expected X-Resillm-Fallback 'false', got '%s'", resp.Header.Get("X-Resillm-Fallback"))
	}
}

func TestHandleChatCompletions_MissingModel(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	reqBody := openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var errResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	errDetail := errResp["error"].(map[string]interface{})
	if errDetail["type"] != "invalid_request" {
		t.Errorf("expected error type 'invalid_request', got '%s'", errDetail["type"])
	}
}

func TestHandleChatCompletions_MissingMessages(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}
}

func TestHandleChatCompletions_InvalidJSON(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}
}

func TestHandleChatCompletions_UnknownModel(t *testing.T) {
	provider := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", provider)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	server := createTestServer(registry, models)

	reqBody := openai.ChatCompletionRequest{
		Model: "unknown-model",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", resp.StatusCode)
	}
}

func TestHandleChatCompletions_FallbackProvider(t *testing.T) {
	primary := NewMockProvider("openai")
	primary.SetError(&MockProviderError{Code: 500, Msg: "server error"})

	fallback := NewMockProvider("anthropic")
	fallback.response = openai.ChatCompletionResponse{
		ID:      "fallback-id",
		Object:  "chat.completion",
		Model:   "claude-3",
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: "fallback response"}}},
		Usage:   openai.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}

	registry := NewMockRegistry()
	registry.Add("openai", primary)
	registry.Add("anthropic", fallback)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary:   config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			Fallbacks: []config.EndpointConfig{{Provider: "anthropic", Model: "claude-3"}},
		},
	}

	server := createTestServer(registry, models)

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Errorf("expected status 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		return
	}

	var chatResp openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if chatResp.Choices[0].Message.Content != "fallback response" {
		t.Errorf("expected 'fallback response', got '%s'", chatResp.Choices[0].Message.Content)
	}

	// Check resillm metadata in headers
	if resp.Header.Get("X-Resillm-Provider") != "anthropic" {
		t.Errorf("expected X-Resillm-Provider 'anthropic', got '%s'", resp.Header.Get("X-Resillm-Provider"))
	}

	if resp.Header.Get("X-Resillm-Fallback") != "true" {
		t.Errorf("expected X-Resillm-Fallback 'true', got '%s'", resp.Header.Get("X-Resillm-Fallback"))
	}
}

func TestHandleChatCompletions_RequestID(t *testing.T) {
	provider := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", provider)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	server := createTestServer(registry, models)

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "custom-request-id")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.Header.Get("X-Request-ID") != "custom-request-id" {
		t.Errorf("expected X-Request-ID 'custom-request-id', got '%s'", resp.Header.Get("X-Request-ID"))
	}
}

func TestHandleHealth(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	server.handleHealth(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var healthResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&healthResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if healthResp["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got '%v'", healthResp["status"])
	}

	if _, ok := healthResp["uptime"]; !ok {
		t.Error("expected uptime field")
	}
}

func TestHandleProviders(t *testing.T) {
	provider := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", provider)

	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	req := httptest.NewRequest("GET", "/v1/providers", nil)
	w := httptest.NewRecorder()

	server.handleProviders(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var providerResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&providerResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := providerResp["providers"]; !ok {
		t.Error("expected providers field")
	}

	providers, ok := providerResp["providers"].([]interface{})
	if !ok {
		t.Fatal("expected providers to be an array")
	}

	if len(providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(providers))
	}
}

func TestHandleBudget(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	req := httptest.NewRequest("GET", "/v1/budget", nil)
	w := httptest.NewRecorder()

	server.handleBudget(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var budgetResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&budgetResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := budgetResp["current_hour"]; !ok {
		t.Error("expected current_hour field")
	}

	if _, ok := budgetResp["current_day"]; !ok {
		t.Error("expected current_day field")
	}
}

func TestHandleCompletions_NotImplemented(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	req := httptest.NewRequest("POST", "/v1/completions", nil)
	w := httptest.NewRecorder()

	server.handleCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected status 501, got %d", resp.StatusCode)
	}
}

func TestHandleEmbeddings_NotImplemented(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	req := httptest.NewRequest("POST", "/v1/embeddings", nil)
	w := httptest.NewRecorder()

	server.handleEmbeddings(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected status 501, got %d", resp.StatusCode)
	}
}

func TestHandleReload(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	// Create a temp config file for reload testing
	tmpDir := t.TempDir()
	configPath := tmpDir + "/config.yaml"
	configContent := `
providers:
  openai:
    api_key: "test-key"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	server.cfgPath = configPath

	req := httptest.NewRequest("POST", "/admin/reload", nil)
	w := httptest.NewRecorder()

	server.handleReload(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var reloadResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&reloadResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if reloadResp["status"] != "reloaded" {
		t.Errorf("expected status 'reloaded', got '%v'", reloadResp["status"])
	}
}

func TestHandleReload_NoConfigPath(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)
	// No cfgPath set

	req := httptest.NewRequest("POST", "/admin/reload", nil)
	w := httptest.NewRecorder()

	server.handleReload(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Should fail because no config path is configured
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", resp.StatusCode)
	}
}

func TestHandleChatCompletions_BudgetExceeded(t *testing.T) {
	provider := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", provider)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	server := createTestServer(registry, models)

	// Exhaust the budget
	server.budget.Record(100.0) // Hourly limit is 100.0

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Should be rejected due to budget exceeded
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", resp.StatusCode)
	}

	var errResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	errDetail := errResp["error"].(map[string]interface{})
	if errDetail["type"] != "budget_exceeded" {
		t.Errorf("expected error type 'budget_exceeded', got '%s'", errDetail["type"])
	}
}

func TestHandleChatCompletions_BudgetWarning(t *testing.T) {
	provider := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", provider)

	models := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
		},
	}

	// Create server with allow_with_warning action
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

	r := router.New(models, registry, circuitBreakers, retryConfig, nil, 200)

	cfg := &config.Config{
		Budget: config.BudgetConfig{
			Enabled:          true,
			MaxCostPerHour:   100.0,
			MaxCostPerDay:    1000.0,
			AlertThreshold:   0.8,
			ActionOnExceeded: "allow_with_warning", // Allow but warn
		},
		Logging: config.LoggingConfig{
			LogRequests: false,
		},
		Metrics: config.MetricsConfig{
			Enabled: false,
		},
	}

	budgetTracker := budget.NewTracker(budget.Config{
		Enabled:          cfg.Budget.Enabled,
		MaxCostPerHour:   cfg.Budget.MaxCostPerHour,
		MaxCostPerDay:    cfg.Budget.MaxCostPerDay,
		AlertThreshold:   cfg.Budget.AlertThreshold,
		ActionOnExceeded: cfg.Budget.ActionOnExceeded,
	})

	server := &Server{
		cfg:     cfg,
		router:  r,
		metrics: metrics.NewCollector(cfg.Metrics),
		budget:  budgetTracker,
	}

	// Exhaust the budget
	server.budget.Record(100.0)

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.handleChatCompletions(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Should be allowed with warning
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check for warning header
	if resp.Header.Get("X-Resillm-Budget-Warning") == "" {
		t.Error("expected X-Resillm-Budget-Warning header")
	}
}

func TestHandleBudget_WithTracking(t *testing.T) {
	registry := NewMockRegistry()
	models := map[string]config.ModelConfig{}
	server := createTestServer(registry, models)

	// Record some costs
	server.budget.Record(25.0)

	req := httptest.NewRequest("GET", "/v1/budget", nil)
	w := httptest.NewRecorder()

	server.handleBudget(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var budgetResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&budgetResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if budgetResp["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", budgetResp["enabled"])
	}

	currentHour := budgetResp["current_hour"].(map[string]interface{})
	if currentHour["spent"].(float64) != 25.0 {
		t.Errorf("expected spent=25.0, got %v", currentHour["spent"])
	}

	if currentHour["remaining"].(float64) != 75.0 {
		t.Errorf("expected remaining=75.0, got %v", currentHour["remaining"])
	}
}
