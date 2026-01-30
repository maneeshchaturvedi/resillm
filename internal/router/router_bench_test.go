package router

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/providers"
	"github.com/resillm/resillm/internal/resilience"
	openai "github.com/sashabaranov/go-openai"
)

// benchResult prevents dead code elimination
var benchResult interface{}

func createBenchRouter() *Router {
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

	return createTestRouterWithRegistry(registry, models)
}

func createBenchRouterWithFallback() *Router {
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

	return createTestRouterWithRegistry(registry, models)
}

func BenchmarkRouter_ExecuteChat_Success(b *testing.B) {
	b.ReportAllocs()

	router := createBenchRouter()
	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, _, _ := router.ExecuteChat(context.Background(), req)
		benchResult = resp
	}
}

func BenchmarkRouter_ExecuteChat_WithFallback_PrimaryFails(b *testing.B) {
	b.ReportAllocs()

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

	// Use minimal retry config to speed up benchmark
	circuitBreakers := make(map[string]*resilience.CircuitBreaker)
	circuitBreakers["openai"] = resilience.NewCircuitBreaker("openai", config.CircuitBreakerConfig{
		FailureThreshold:    1000000, // High threshold to avoid circuit opening
		SuccessThreshold:    1,
		Timeout:             100 * time.Millisecond,
		HalfOpenMaxRequests: 100,
	}, nil)
	circuitBreakers["anthropic"] = resilience.NewCircuitBreaker("anthropic", config.CircuitBreakerConfig{
		FailureThreshold:    1000000,
		SuccessThreshold:    1,
		Timeout:             100 * time.Millisecond,
		HalfOpenMaxRequests: 100,
	}, nil)

	retryConfig := config.RetryConfig{
		MaxAttempts:       1, // No retries - just fallback
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        1 * time.Millisecond,
		BackoffMultiplier: 1.0,
		RetryableErrors:   []int{429, 500, 502, 503, 504},
	}

	router := &Router{
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

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, _, _ := router.ExecuteChat(context.Background(), req)
		benchResult = resp
	}
}

func BenchmarkRouter_ExecuteChat_Concurrent(b *testing.B) {
	b.ReportAllocs()

	router := createBenchRouter()
	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			router.ExecuteChat(context.Background(), req)
		}
	})
}

func BenchmarkRouter_ModelLookup_KnownModel(b *testing.B) {
	b.ReportAllocs()

	primary := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", primary)

	// Create many models to simulate realistic lookup
	models := make(map[string]config.ModelConfig)
	for i := 0; i < 50; i++ {
		name := "model-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		models[name] = config.ModelConfig{
			Primary: config.EndpointConfig{Provider: "openai", Model: name},
		}
	}
	models["gpt-4o"] = config.ModelConfig{
		Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o"},
	}

	router := &Router{
		models:          models,
		circuitBreakers: make(map[string]*resilience.CircuitBreaker),
		retryConfig:     config.RetryConfig{MaxAttempts: 1},
		semaphores:      providers.NewProviderSemaphores(200),
		providers:       &mockRegistryWrapper{registry: registry},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Direct map lookup simulation
		_, ok := router.models["gpt-4o"]
		benchResult = ok
	}
}

func BenchmarkRouter_ModelLookup_DefaultFallback(b *testing.B) {
	b.ReportAllocs()

	primary := NewMockProvider("openai")
	registry := NewMockRegistry()
	registry.Add("openai", primary)

	models := make(map[string]config.ModelConfig)
	for i := 0; i < 50; i++ {
		name := "model-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		models[name] = config.ModelConfig{
			Primary: config.EndpointConfig{Provider: "openai", Model: name},
		}
	}
	models["default"] = config.ModelConfig{
		Primary: config.EndpointConfig{Provider: "openai", Model: "default-model"},
	}

	router := &Router{
		models:          models,
		circuitBreakers: make(map[string]*resilience.CircuitBreaker),
		retryConfig:     config.RetryConfig{MaxAttempts: 1},
		semaphores:      providers.NewProviderSemaphores(200),
		providers:       &mockRegistryWrapper{registry: registry},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate fallback to default lookup
		_, ok := router.models["unknown-model"]
		if !ok {
			_, ok = router.models["default"]
		}
		benchResult = ok
	}
}

func BenchmarkRouter_GetProviderStatus(b *testing.B) {
	b.ReportAllocs()

	router := createBenchRouterWithFallback()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = router.GetProviderStatus()
	}
}

func BenchmarkRouter_UpdateModels(b *testing.B) {
	b.ReportAllocs()

	router := createBenchRouter()
	newModels := map[string]config.ModelConfig{
		"gpt-4o": {
			Primary: config.EndpointConfig{Provider: "openai", Model: "gpt-4o-turbo"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		router.UpdateModels(newModels)
	}
}

func BenchmarkRouter_UpdateRetryConfig(b *testing.B) {
	b.ReportAllocs()

	router := createBenchRouter()
	newConfig := config.RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.5,
		RetryableErrors:   []int{429, 500, 502, 503, 504},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		router.UpdateRetryConfig(newConfig)
	}
}

func BenchmarkRouter_ExecuteChatStream_Success(b *testing.B) {
	b.ReportAllocs()

	router := createBenchRouter()
	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, _, err := router.ExecuteChatStream(context.Background(), req)
		if err == nil {
			result.Stream.Close()
			result.Sem.Release()
		}
		benchResult = result
	}
}

func BenchmarkRouter_ExecuteChatStream_Concurrent(b *testing.B) {
	b.ReportAllocs()

	router := createBenchRouter()
	req := openai.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			result, _, err := router.ExecuteChatStream(context.Background(), req)
			if err == nil {
				result.Stream.Close()
				result.Sem.Release()
			}
		}
	})
}

func BenchmarkRouter_CircuitBreakerCheck(b *testing.B) {
	b.ReportAllocs()

	cb := resilience.NewCircuitBreaker("openai", config.CircuitBreakerConfig{
		FailureThreshold:    5,
		SuccessThreshold:    3,
		Timeout:             30 * time.Second,
		HalfOpenMaxRequests: 50,
	}, nil)

	circuitBreakers := map[string]*resilience.CircuitBreaker{
		"openai": cb,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb, ok := circuitBreakers["openai"]
		if ok {
			benchResult = cb.Allow()
		}
	}
}

func BenchmarkRouter_SemaphoreAcquireRelease(b *testing.B) {
	b.ReportAllocs()

	semaphores := providers.NewProviderSemaphores(200)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sem := semaphores.Get("openai")
		sem.Acquire(ctx)
		sem.Release()
	}
}

func BenchmarkRouter_SemaphoreAcquireRelease_Concurrent(b *testing.B) {
	b.ReportAllocs()

	semaphores := providers.NewProviderSemaphores(200)
	ctx := context.Background()
	var counter atomic.Int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			provider := "provider-" + string(rune('a'+n%10))
			sem := semaphores.Get(provider)
			sem.Acquire(ctx)
			sem.Release()
		}
	})
}
