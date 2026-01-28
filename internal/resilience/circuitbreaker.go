package resilience

import (
	"sync"
	"time"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/metrics"
)

// State represents the circuit breaker state
type State int

const (
	StateClosed   State = iota // Normal operation, requests allowed
	StateOpen                   // Failing, requests blocked
	StateHalfOpen              // Testing recovery
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Default limit for requests in half-open state
// Set to 50 to allow sufficient traffic for reliable recovery detection under high concurrency
const defaultHalfOpenMaxRequests = 50

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	name    string
	mu      sync.RWMutex
	state   State
	config  config.CircuitBreakerConfig
	metrics *metrics.Collector

	failureCount     int
	successCount     int
	halfOpenRequests int // Count of requests allowed in half-open state
	lastFailureTime  time.Time
	lastStateChange  time.Time
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(name string, cfg config.CircuitBreakerConfig, m *metrics.Collector) *CircuitBreaker {
	cb := &CircuitBreaker{
		name:            name,
		state:           StateClosed,
		config:          cfg,
		metrics:         m,
		lastStateChange: time.Now(),
	}

	if m != nil {
		m.SetCircuitState(name, 0) // 0 = closed
	}

	return cb
}

// Allow checks if a request should be allowed
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.RLock()
	state := cb.state
	lastFailure := cb.lastFailureTime
	cb.mu.RUnlock()

	switch state {
	case StateClosed:
		return true

	case StateOpen:
		// Check if we should transition to half-open
		if time.Since(lastFailure) > cb.config.Timeout {
			cb.mu.Lock()
			if cb.state == StateOpen { // Double-check after acquiring lock
				cb.transitionTo(StateHalfOpen)
			}
			cb.mu.Unlock()
			return true
		}
		return false

	case StateHalfOpen:
		// In half-open state, allow limited requests to test recovery
		cb.mu.Lock()
		defer cb.mu.Unlock()

		// Get max requests limit (use default if not configured)
		maxRequests := cb.config.HalfOpenMaxRequests
		if maxRequests <= 0 {
			maxRequests = defaultHalfOpenMaxRequests
		}

		if cb.halfOpenRequests >= maxRequests {
			return false // Already have enough test requests in flight
		}
		cb.halfOpenRequests++
		return true

	default:
		return false
	}
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		// Reset failure count on success
		cb.failureCount = 0

	case StateHalfOpen:
		cb.successCount++
		// If enough successes, close the circuit
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.transitionTo(StateClosed)
		}
	}

	if cb.metrics != nil {
		cb.metrics.RecordCircuitSuccess(cb.name)
	}
}

// RecordFailure records a failed request
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		cb.failureCount++
		// If enough failures, open the circuit
		if cb.failureCount >= cb.config.FailureThreshold {
			cb.transitionTo(StateOpen)
		}

	case StateHalfOpen:
		// Any failure in half-open state opens the circuit again
		cb.transitionTo(StateOpen)
	}

	if cb.metrics != nil {
		cb.metrics.RecordCircuitFailure(cb.name)
	}
}

// transitionTo changes the circuit state (must be called with lock held)
func (cb *CircuitBreaker) transitionTo(newState State) {
	if cb.state == newState {
		return
	}

	oldState := cb.state
	cb.state = newState
	cb.lastStateChange = time.Now()

	// Reset all counters on state change
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenRequests = 0

	if cb.metrics != nil {
		cb.metrics.SetCircuitState(cb.name, int(newState))
		cb.metrics.RecordCircuitStateChange(cb.name, oldState.String(), newState.String())
	}
}

// State returns the current circuit state
func (cb *CircuitBreaker) State() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Stats returns circuit breaker statistics
func (cb *CircuitBreaker) Stats() CircuitStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitStats{
		Name:            cb.name,
		State:           cb.state.String(),
		FailureCount:    cb.failureCount,
		SuccessCount:    cb.successCount,
		LastFailureTime: cb.lastFailureTime,
		LastStateChange: cb.lastStateChange,
	}
}

// CircuitStats contains circuit breaker statistics
type CircuitStats struct {
	Name            string    `json:"name"`
	State           string    `json:"state"`
	FailureCount    int       `json:"failure_count"`
	SuccessCount    int       `json:"success_count"`
	LastFailureTime time.Time `json:"last_failure_time"`
	LastStateChange time.Time `json:"last_state_change"`
}

// UpdateConfig updates the circuit breaker configuration
func (cb *CircuitBreaker) UpdateConfig(cfg config.CircuitBreakerConfig) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.config = cfg
}
