package resilience

import (
	"testing"
	"time"

	"github.com/resillm/resillm/internal/config"
)

// benchResult prevents dead code elimination
var benchResult interface{}

func newTestCircuitBreaker() *CircuitBreaker {
	return NewCircuitBreaker("test", config.CircuitBreakerConfig{
		FailureThreshold:    5,
		SuccessThreshold:    3,
		Timeout:             30 * time.Second,
		HalfOpenMaxRequests: 50,
	}, nil)
}

func BenchmarkCircuitBreaker_Allow_Closed(b *testing.B) {
	b.ReportAllocs()

	cb := newTestCircuitBreaker()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = cb.Allow()
	}
}

func BenchmarkCircuitBreaker_Allow_Open(b *testing.B) {
	b.ReportAllocs()

	cb := newTestCircuitBreaker()
	// Trigger enough failures to open the circuit
	for i := 0; i < 10; i++ {
		cb.RecordFailure()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = cb.Allow()
	}
}

func BenchmarkCircuitBreaker_Allow_HalfOpen(b *testing.B) {
	b.ReportAllocs()

	cb := NewCircuitBreaker("test", config.CircuitBreakerConfig{
		FailureThreshold:    5,
		SuccessThreshold:    3,
		Timeout:             1 * time.Nanosecond, // Very short timeout to immediately go half-open
		HalfOpenMaxRequests: 1000000,             // High limit so we don't hit cap
	}, nil)

	// Open the circuit
	for i := 0; i < 10; i++ {
		cb.RecordFailure()
	}
	// Wait for timeout to transition to half-open
	time.Sleep(2 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = cb.Allow()
	}
}

func BenchmarkCircuitBreaker_RecordSuccess(b *testing.B) {
	b.ReportAllocs()

	cb := newTestCircuitBreaker()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.RecordSuccess()
	}
}

func BenchmarkCircuitBreaker_RecordFailure(b *testing.B) {
	b.ReportAllocs()

	// Use a high threshold so we don't transition states during benchmark
	cb := NewCircuitBreaker("test", config.CircuitBreakerConfig{
		FailureThreshold:    1000000,
		SuccessThreshold:    3,
		Timeout:             30 * time.Second,
		HalfOpenMaxRequests: 50,
	}, nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.RecordFailure()
	}
}

func BenchmarkCircuitBreaker_State(b *testing.B) {
	b.ReportAllocs()

	cb := newTestCircuitBreaker()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = cb.State()
	}
}

func BenchmarkCircuitBreaker_Stats(b *testing.B) {
	b.ReportAllocs()

	cb := newTestCircuitBreaker()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = cb.Stats()
	}
}

func BenchmarkCircuitBreaker_Concurrent_Allow(b *testing.B) {
	b.ReportAllocs()

	cb := newTestCircuitBreaker()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cb.Allow()
		}
	})
}

func BenchmarkCircuitBreaker_Concurrent_Mixed(b *testing.B) {
	b.ReportAllocs()

	// Use high thresholds to avoid state transitions during test
	cb := NewCircuitBreaker("test", config.CircuitBreakerConfig{
		FailureThreshold:    1000000,
		SuccessThreshold:    1000000,
		Timeout:             30 * time.Second,
		HalfOpenMaxRequests: 1000000,
	}, nil)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			switch i % 3 {
			case 0:
				cb.Allow()
			case 1:
				cb.RecordSuccess()
			case 2:
				cb.RecordFailure()
			}
			i++
		}
	})
}

func BenchmarkCircuitBreaker_UpdateConfig(b *testing.B) {
	b.ReportAllocs()

	cb := newTestCircuitBreaker()
	newConfig := config.CircuitBreakerConfig{
		FailureThreshold:    10,
		SuccessThreshold:    5,
		Timeout:             60 * time.Second,
		HalfOpenMaxRequests: 100,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.UpdateConfig(newConfig)
	}
}
