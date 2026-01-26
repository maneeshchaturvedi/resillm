package resilience

import (
	"sync"
	"testing"
	"time"

	"github.com/resillm/resillm/internal/config"
)

func TestCircuitBreaker_StartsInClosedState(t *testing.T) {
	cb := NewCircuitBreaker("test", defaultConfig(), nil)

	if cb.State() != StateClosed {
		t.Errorf("expected state %v, got %v", StateClosed, cb.State())
	}
}

func TestCircuitBreaker_AllowsRequestsWhenClosed(t *testing.T) {
	cb := NewCircuitBreaker("test", defaultConfig(), nil)

	for i := 0; i < 10; i++ {
		if !cb.Allow() {
			t.Errorf("expected request to be allowed when circuit is closed")
		}
	}
}

func TestCircuitBreaker_OpensAfterFailureThreshold(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}
	cb := NewCircuitBreaker("test", cfg, nil)

	// Record failures up to threshold
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	if cb.State() != StateOpen {
		t.Errorf("expected state %v after %d failures, got %v", StateOpen, 3, cb.State())
	}
}

func TestCircuitBreaker_RejectsRequestsWhenOpen(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          1 * time.Hour, // Long timeout so it stays open
	}
	cb := NewCircuitBreaker("test", cfg, nil)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != StateOpen {
		t.Fatalf("expected circuit to be open")
	}

	if cb.Allow() {
		t.Errorf("expected request to be rejected when circuit is open")
	}
}

func TestCircuitBreaker_TransitionsToHalfOpenAfterTimeout(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          50 * time.Millisecond,
	}
	cb := NewCircuitBreaker("test", cfg, nil)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != StateOpen {
		t.Fatalf("expected circuit to be open")
	}

	// Wait for timeout
	time.Sleep(60 * time.Millisecond)

	// Next Allow() should transition to half-open
	if !cb.Allow() {
		t.Errorf("expected request to be allowed after timeout (half-open)")
	}

	if cb.State() != StateHalfOpen {
		t.Errorf("expected state %v after timeout, got %v", StateHalfOpen, cb.State())
	}
}

func TestCircuitBreaker_ClosesAfterSuccessThreshold(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 3,
		Timeout:          50 * time.Millisecond,
	}
	cb := NewCircuitBreaker("test", cfg, nil)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for timeout and transition to half-open
	time.Sleep(60 * time.Millisecond)
	cb.Allow()

	if cb.State() != StateHalfOpen {
		t.Fatalf("expected circuit to be half-open")
	}

	// Record successes
	for i := 0; i < 3; i++ {
		cb.RecordSuccess()
	}

	if cb.State() != StateClosed {
		t.Errorf("expected state %v after %d successes, got %v", StateClosed, 3, cb.State())
	}
}

func TestCircuitBreaker_ReopensOnFailureInHalfOpen(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 3,
		Timeout:          50 * time.Millisecond,
	}
	cb := NewCircuitBreaker("test", cfg, nil)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for timeout and transition to half-open
	time.Sleep(60 * time.Millisecond)
	cb.Allow()

	if cb.State() != StateHalfOpen {
		t.Fatalf("expected circuit to be half-open")
	}

	// Record a failure in half-open state
	cb.RecordFailure()

	if cb.State() != StateOpen {
		t.Errorf("expected state %v after failure in half-open, got %v", StateOpen, cb.State())
	}
}

func TestCircuitBreaker_ResetsFailureCountOnSuccess(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}
	cb := NewCircuitBreaker("test", cfg, nil)

	// Record 2 failures (below threshold)
	cb.RecordFailure()
	cb.RecordFailure()

	// Record a success - should reset failure count
	cb.RecordSuccess()

	// Record 2 more failures - should not open (count was reset)
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != StateClosed {
		t.Errorf("expected circuit to remain closed after reset, got %v", cb.State())
	}

	// One more failure should open it
	cb.RecordFailure()

	if cb.State() != StateOpen {
		t.Errorf("expected circuit to open after 3 consecutive failures, got %v", cb.State())
	}
}

func TestCircuitBreaker_ThreadSafety(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 100,
		SuccessThreshold: 100,
		Timeout:          100 * time.Millisecond,
	}
	cb := NewCircuitBreaker("test", cfg, nil)

	var wg sync.WaitGroup
	numGoroutines := 100
	numOperations := 100

	// Concurrent Allow() calls
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				cb.Allow()
			}
		}()
	}

	// Concurrent RecordSuccess() calls
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				cb.RecordSuccess()
			}
		}()
	}

	// Concurrent RecordFailure() calls
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				cb.RecordFailure()
			}
		}()
	}

	// Concurrent State() calls
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				_ = cb.State()
			}
		}()
	}

	wg.Wait()

	// If we got here without a race condition, the test passes
	// State may be anything due to concurrent operations
	state := cb.State()
	if state != StateClosed && state != StateOpen && state != StateHalfOpen {
		t.Errorf("unexpected state: %v", state)
	}
}

func TestCircuitBreaker_Stats(t *testing.T) {
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}
	cb := NewCircuitBreaker("test-provider", cfg, nil)

	stats := cb.Stats()

	if stats.Name != "test-provider" {
		t.Errorf("expected name 'test-provider', got '%s'", stats.Name)
	}

	if stats.State != "closed" {
		t.Errorf("expected state 'closed', got '%s'", stats.State)
	}
}

func TestState_String(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half-open"},
		{State(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("State.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// Helper function
func defaultConfig() config.CircuitBreakerConfig {
	return config.CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 3,
		Timeout:          30 * time.Second,
	}
}
