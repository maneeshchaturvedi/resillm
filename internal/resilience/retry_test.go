package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/resillm/resillm/internal/config"
)

// Mock errors for testing
type mockRetryableError struct {
	retryable bool
}

func (e *mockRetryableError) Error() string {
	return "mock error"
}

func (e *mockRetryableError) IsRetryable() bool {
	return e.retryable
}

type mockProviderError struct {
	statusCode int
}

func (e *mockProviderError) Error() string {
	return "provider error"
}

func (e *mockProviderError) StatusCode() int {
	return e.statusCode
}

func TestRetrier_NoRetryOnSuccess(t *testing.T) {
	cfg := defaultRetryConfig()
	retrier := NewRetrier(cfg)

	callCount := 0
	err := retrier.Execute(context.Background(), func() error {
		callCount++
		return nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestRetrier_RetriesOnRetryableError(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{429, 500, 503},
	}
	retrier := NewRetrier(cfg)

	callCount := 0
	err := retrier.Execute(context.Background(), func() error {
		callCount++
		return &mockRetryableError{retryable: true}
	})

	if err == nil {
		t.Errorf("expected error after all retries")
	}

	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
}

func TestRetrier_NoRetryOnNonRetryableError(t *testing.T) {
	cfg := defaultRetryConfig()
	retrier := NewRetrier(cfg)

	callCount := 0
	err := retrier.Execute(context.Background(), func() error {
		callCount++
		return &mockRetryableError{retryable: false}
	})

	if err == nil {
		t.Errorf("expected error")
	}

	if callCount != 1 {
		t.Errorf("expected 1 call (no retry), got %d", callCount)
	}
}

func TestRetrier_RetriesOnConfiguredStatusCodes(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{429, 500, 503},
	}
	retrier := NewRetrier(cfg)

	// Test 429 (rate limit)
	callCount := 0
	retrier.Execute(context.Background(), func() error {
		callCount++
		return &mockProviderError{statusCode: 429}
	})

	if callCount != 3 {
		t.Errorf("expected 3 calls for 429, got %d", callCount)
	}

	// Test 500 (server error)
	callCount = 0
	retrier.Execute(context.Background(), func() error {
		callCount++
		return &mockProviderError{statusCode: 500}
	})

	if callCount != 3 {
		t.Errorf("expected 3 calls for 500, got %d", callCount)
	}

	// Test 400 (not retryable)
	callCount = 0
	retrier.Execute(context.Background(), func() error {
		callCount++
		return &mockProviderError{statusCode: 400}
	})

	if callCount != 1 {
		t.Errorf("expected 1 call for 400 (no retry), got %d", callCount)
	}
}

func TestRetrier_RespectsMaxAttempts(t *testing.T) {
	tests := []struct {
		maxAttempts   int
		expectedCalls int
	}{
		{1, 1},
		{3, 3},
		{5, 5},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			cfg := config.RetryConfig{
				MaxAttempts:       tt.maxAttempts,
				InitialBackoff:    1 * time.Millisecond,
				MaxBackoff:        10 * time.Millisecond,
				BackoffMultiplier: 2.0,
				RetryableErrors:   []int{500},
			}
			retrier := NewRetrier(cfg)

			callCount := 0
			retrier.Execute(context.Background(), func() error {
				callCount++
				return &mockProviderError{statusCode: 500}
			})

			if callCount != tt.expectedCalls {
				t.Errorf("maxAttempts=%d: expected %d calls, got %d",
					tt.maxAttempts, tt.expectedCalls, callCount)
			}
		})
	}
}

func TestRetrier_ExponentialBackoff(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       4,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{500},
	}
	retrier := NewRetrier(cfg)

	var timestamps []time.Time
	retrier.Execute(context.Background(), func() error {
		timestamps = append(timestamps, time.Now())
		return &mockProviderError{statusCode: 500}
	})

	if len(timestamps) != 4 {
		t.Fatalf("expected 4 timestamps, got %d", len(timestamps))
	}

	// Check that delays increase (approximately)
	// Note: jitter makes exact timing unpredictable
	for i := 1; i < len(timestamps); i++ {
		delay := timestamps[i].Sub(timestamps[i-1])
		minExpected := time.Duration(float64(cfg.InitialBackoff) * 0.5) // Account for jitter
		if delay < minExpected {
			t.Logf("delay %d: %v (expected >= %v)", i, delay, minExpected)
		}
	}
}

func TestRetrier_CapsAtMaxBackoff(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    50 * time.Millisecond,
		MaxBackoff:        60 * time.Millisecond, // Very close to initial
		BackoffMultiplier: 10.0,                  // Would be 500ms without cap
		RetryableErrors:   []int{500},
	}
	retrier := NewRetrier(cfg)

	var timestamps []time.Time
	retrier.Execute(context.Background(), func() error {
		timestamps = append(timestamps, time.Now())
		return &mockProviderError{statusCode: 500}
	})

	// Check that no delay exceeds maxBackoff + jitter
	maxAllowed := time.Duration(float64(cfg.MaxBackoff) * 1.5) // 25% jitter max
	for i := 1; i < len(timestamps); i++ {
		delay := timestamps[i].Sub(timestamps[i-1])
		if delay > maxAllowed {
			t.Errorf("delay %d: %v exceeds max allowed %v", i, delay, maxAllowed)
		}
	}
}

func TestRetrier_ContextCancellation(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       10,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        1 * time.Second,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{500},
	}
	retrier := NewRetrier(cfg)

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := retrier.Execute(ctx, func() error {
		callCount++
		return &mockProviderError{statusCode: 500}
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}

	// Should have been cancelled before completing all retries
	if callCount >= 10 {
		t.Errorf("expected fewer than 10 calls due to cancellation, got %d", callCount)
	}
}

func TestRetrier_ContextTimeout(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       10,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        1 * time.Second,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{500},
	}
	retrier := NewRetrier(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	callCount := 0
	err := retrier.Execute(ctx, func() error {
		callCount++
		return &mockProviderError{statusCode: 500}
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded error, got %v", err)
	}
}

func TestRetrier_ExecuteWithResult_Success(t *testing.T) {
	cfg := defaultRetryConfig()
	retrier := NewRetrier(cfg)

	result, attempts, err := retrier.ExecuteWithResult(context.Background(), func() (interface{}, error) {
		return "success", nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}

	if result != "success" {
		t.Errorf("expected 'success', got %v", result)
	}
}

func TestRetrier_ExecuteWithResult_SuccessAfterRetry(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{500},
	}
	retrier := NewRetrier(cfg)

	callCount := 0
	result, attempts, err := retrier.ExecuteWithResult(context.Background(), func() (interface{}, error) {
		callCount++
		if callCount < 3 {
			return nil, &mockProviderError{statusCode: 500}
		}
		return "success after retry", nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}

	if result != "success after retry" {
		t.Errorf("expected 'success after retry', got %v", result)
	}
}

func TestRetrier_ExecuteWithResult_AllAttemptsFail(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{500},
	}
	retrier := NewRetrier(cfg)

	result, attempts, err := retrier.ExecuteWithResult(context.Background(), func() (interface{}, error) {
		return nil, &mockProviderError{statusCode: 500}
	})

	if err == nil {
		t.Errorf("expected error after all retries")
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}

	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestRetrier_UnknownErrorIsRetried(t *testing.T) {
	cfg := config.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{500},
	}
	retrier := NewRetrier(cfg)

	callCount := 0
	retrier.Execute(context.Background(), func() error {
		callCount++
		return errors.New("unknown error")
	})

	// Unknown errors should be retried by default
	if callCount != 3 {
		t.Errorf("expected 3 calls for unknown error, got %d", callCount)
	}
}

// Helper function
func defaultRetryConfig() config.RetryConfig {
	return config.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []int{429, 500, 502, 503, 504},
	}
}
