package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// benchResultH prevents dead code elimination (handlers)
var benchResultH interface{}

func BenchmarkGenerateRequestID(b *testing.B) {
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResultH = generateRequestID()
	}
}

func BenchmarkValidateRequest_Simple(b *testing.B) {
	b.ReportAllocs()

	req := &openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
		Temperature: 0.7,
		TopP:        0.9,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResultH = validateRequest(req)
	}
}

func BenchmarkValidateRequest_ManyMessages(b *testing.B) {
	b.ReportAllocs()

	messages := make([]openai.ChatCompletionMessage, 50)
	for i := 0; i < 50; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = openai.ChatCompletionMessage{
			Role:    role,
			Content: "This is a test message with some content.",
		}
	}

	req := &openai.ChatCompletionRequest{
		Model:       "gpt-4o",
		Messages:    messages,
		Temperature: 0.7,
		TopP:        0.9,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResultH = validateRequest(req)
	}
}

func BenchmarkValidateRequest_Invalid(b *testing.B) {
	b.ReportAllocs()

	req := &openai.ChatCompletionRequest{
		Model: "", // Invalid - empty model
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResultH = validateRequest(req)
	}
}

func BenchmarkSanitizeError_NoSensitiveData(b *testing.B) {
	b.ReportAllocs()

	err := io.EOF

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResultH = sanitizeError(err)
	}
}

func BenchmarkSanitizeError_WithAPIKey(b *testing.B) {
	b.ReportAllocs()

	// Create an error that contains an API key
	err := &testError{msg: "failed to connect with key sk-proj-1234567890abcdefghij"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResultH = sanitizeError(err)
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func BenchmarkFormatInt(b *testing.B) {
	b.ReportAllocs()

	var nums = []int64{0, 1, 100, 12345, 999999, 1234567890}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := nums[i%len(nums)]
		benchResultH = formatInt(n)
	}
}

// MockProvider for benchmark tests
type benchMockProvider struct {
	name     string
	response openai.ChatCompletionResponse
}

func (m *benchMockProvider) Name() string {
	return m.name
}

func (m *benchMockProvider) ExecuteChat(ctx context.Context, req openai.ChatCompletionRequest, model string) (openai.ChatCompletionResponse, error) {
	return m.response, nil
}

func (m *benchMockProvider) ExecuteChatStream(ctx context.Context, req openai.ChatCompletionRequest, model string) (providers.ChatStream, error) {
	return nil, nil
}

func (m *benchMockProvider) CalculateCost(model string, usage openai.Usage) float64 {
	return 0.001
}

func (m *benchMockProvider) HealthCheck(ctx context.Context) error {
	return nil
}

// benchMockRegistry for benchmark tests
type benchMockRegistry struct {
	providers map[string]providers.Provider
}

func (r *benchMockRegistry) Get(name string) (providers.Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

func (r *benchMockRegistry) List() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

func createBenchServer() *Server {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:        "localhost",
			Port:        8080,
			MetricsPort: 9090,
		},
		Models: map[string]config.ModelConfig{
			"gpt-4o": {
				Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			},
		},
		Resilience: config.ResilienceConfig{
			Retry: config.RetryConfig{
				MaxAttempts:       3,
				InitialBackoff:    time.Millisecond,
				MaxBackoff:        10 * time.Millisecond,
				BackoffMultiplier: 2.0,
				RetryableErrors:   []int{429, 500, 502, 503, 504},
			},
			CircuitBreaker: config.CircuitBreakerConfig{
				FailureThreshold: 5,
				SuccessThreshold: 2,
				Timeout:          100 * time.Millisecond,
			},
		},
		Logging: config.LoggingConfig{
			Level:        "warn", // Suppress logging during benchmarks
			LogRequests:  false,
			LogResponses: false,
		},
	}

	// Create mock provider
	mockProvider := &benchMockProvider{
		name: "openai",
		response: openai.ChatCompletionResponse{
			ID:      "test-id",
			Object:  "chat.completion",
			Model:   "gpt-4o",
			Created: time.Now().Unix(),
			Choices: []openai.ChatCompletionChoice{
				{
					Message: openai.ChatCompletionMessage{
						Role:    "assistant",
						Content: "Hello! How can I help you today?",
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		},
	}

	registry := &benchMockRegistry{
		providers: map[string]providers.Provider{
			"openai": mockProvider,
		},
	}

	circuitBreakers := map[string]*resilience.CircuitBreaker{
		"openai": resilience.NewCircuitBreaker("openai", cfg.Resilience.CircuitBreaker, nil),
	}

	r := router.New(
		cfg.Models,
		registry,
		circuitBreakers,
		cfg.Resilience.Retry,
		nil,
		200,
	)

	return &Server{
		cfg:             cfg,
		router:          r,
		metrics:         metrics.NewCollector(config.MetricsConfig{}),
		budget:          budget.NewTracker(budget.Config{}),
		circuitBreakers: circuitBreakers,
	}
}

func BenchmarkHandleChatCompletions_Simple(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	body, _ := json.Marshal(reqBody)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		s.handleChatCompletions(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", w.Code)
		}
	}
}

func BenchmarkHandleChatCompletions_LargePayload(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	// Create a larger request with more messages
	messages := make([]openai.ChatCompletionMessage, 20)
	for i := 0; i < 20; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = openai.ChatCompletionMessage{
			Role:    role,
			Content: strings.Repeat("This is a longer message with more content. ", 10),
		}
	}

	reqBody := openai.ChatCompletionRequest{
		Model:       "gpt-4o",
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   1000,
	}
	body, _ := json.Marshal(reqBody)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		s.handleChatCompletions(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", w.Code)
		}
	}
}

func BenchmarkHandleHealth(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()

		s.handleHealth(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", w.Code)
		}
	}
}

func BenchmarkHandleProviders(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/v1/providers", nil)
		w := httptest.NewRecorder()

		s.handleProviders(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", w.Code)
		}
	}
}

func BenchmarkHandleBudget(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/v1/budget", nil)
		w := httptest.NewRecorder()

		s.handleBudget(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", w.Code)
		}
	}
}

func BenchmarkHandleModels(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		w := httptest.NewRecorder()

		s.handleModels(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", w.Code)
		}
	}
}

func BenchmarkWriteError(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		s.writeError(w, http.StatusBadRequest, "invalid_request", "Test error message")
	}
}

func BenchmarkJSONMarshal_ChatResponse(b *testing.B) {
	b.ReportAllocs()

	resp := openai.ChatCompletionResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Model:   "gpt-4o",
		Created: time.Now().Unix(),
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Role:    "assistant",
					Content: "Hello! How can I help you today?",
				},
				FinishReason: "stop",
			},
		},
		Usage: openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(resp)
		benchResultH = data
	}
}

func BenchmarkJSONUnmarshal_ChatRequest(b *testing.B) {
	b.ReportAllocs()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}],"temperature":0.7}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var req openai.ChatCompletionRequest
		json.Unmarshal([]byte(reqBody), &req)
		benchResultH = req
	}
}

func BenchmarkHandleChatCompletions_Concurrent(b *testing.B) {
	b.ReportAllocs()

	s := createBenchServer()

	reqBody := openai.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	body, _ := json.Marshal(reqBody)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.handleChatCompletions(w, req)
		}
	})
}
