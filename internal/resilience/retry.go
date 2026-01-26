package resilience

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/resillm/resillm/internal/config"
)

// Retrier handles retry logic with exponential backoff
type Retrier struct {
	config config.RetryConfig
}

// NewRetrier creates a new retrier
func NewRetrier(cfg config.RetryConfig) *Retrier {
	return &Retrier{config: cfg}
}

// Execute runs a function with retry logic
func (r *Retrier) Execute(ctx context.Context, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt < r.config.MaxAttempts; attempt++ {
		// Check context before each attempt
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !r.isRetryable(err) {
			return err
		}

		// Don't wait after the last attempt
		if attempt < r.config.MaxAttempts-1 {
			backoff := r.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				// Continue to next attempt
			}
		}
	}

	return lastErr
}

// ExecuteWithResult runs a function that returns a result with retry logic
func (r *Retrier) ExecuteWithResult(ctx context.Context, fn func() (interface{}, error)) (interface{}, int, error) {
	var lastErr error
	attempts := 0

	for attempt := 0; attempt < r.config.MaxAttempts; attempt++ {
		attempts++

		// Check context before each attempt
		if ctx.Err() != nil {
			return nil, attempts, ctx.Err()
		}

		result, err := fn()
		if err == nil {
			return result, attempts, nil
		}

		lastErr = err

		// Check if error is retryable
		if !r.isRetryable(err) {
			return nil, attempts, err
		}

		// Don't wait after the last attempt
		if attempt < r.config.MaxAttempts-1 {
			backoff := r.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				return nil, attempts, ctx.Err()
			case <-time.After(backoff):
				// Continue to next attempt
			}
		}
	}

	return nil, attempts, lastErr
}

// calculateBackoff calculates the backoff duration for an attempt
func (r *Retrier) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: initial * multiplier^attempt
	backoff := float64(r.config.InitialBackoff) * math.Pow(r.config.BackoffMultiplier, float64(attempt))

	// Cap at max backoff
	if backoff > float64(r.config.MaxBackoff) {
		backoff = float64(r.config.MaxBackoff)
	}

	// Add jitter (±25%)
	jitter := backoff * 0.25 * (rand.Float64()*2 - 1)
	backoff += jitter

	// Ensure backoff never goes below initial backoff (jitter could make it negative)
	minBackoff := float64(r.config.InitialBackoff)
	if backoff < minBackoff {
		backoff = minBackoff
	}

	return time.Duration(backoff)
}

// isRetryable checks if an error is retryable based on configuration
func (r *Retrier) isRetryable(err error) bool {
	// Check if error implements retryable interface
	if re, ok := err.(RetryableError); ok {
		return re.IsRetryable()
	}

	// Check if error has a status code we can inspect
	if pe, ok := err.(ProviderError); ok {
		statusCode := pe.StatusCode()
		for _, code := range r.config.RetryableErrors {
			if statusCode == code {
				return true
			}
		}
		return false
	}

	// Default to retrying on unknown errors
	return true
}

// RetryableError is an interface for errors that know if they're retryable
type RetryableError interface {
	IsRetryable() bool
}

// ProviderError is an interface for errors with status codes
type ProviderError interface {
	StatusCode() int
}
