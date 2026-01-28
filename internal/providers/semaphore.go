package providers

import (
	"context"
	"sync"
)

// DefaultMaxConcurrentPerProvider is the default maximum concurrent requests per provider.
// This provides backpressure when a provider is slow or overloaded.
const DefaultMaxConcurrentPerProvider = 200

// Semaphore provides a counting semaphore for limiting concurrent operations.
// It's safe for concurrent use from multiple goroutines.
type Semaphore struct {
	ch     chan struct{}
	name   string
	mu     sync.RWMutex
	active int
}

// NewSemaphore creates a new semaphore with the specified capacity.
func NewSemaphore(name string, capacity int) *Semaphore {
	if capacity <= 0 {
		capacity = DefaultMaxConcurrentPerProvider
	}
	return &Semaphore{
		ch:   make(chan struct{}, capacity),
		name: name,
	}
}

// Acquire acquires a slot from the semaphore, blocking if necessary.
// Returns an error if the context is cancelled while waiting.
func (s *Semaphore) Acquire(ctx context.Context) error {
	select {
	case s.ch <- struct{}{}:
		s.mu.Lock()
		s.active++
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryAcquire attempts to acquire a slot without blocking.
// Returns true if successful, false if no slots available.
func (s *Semaphore) TryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		s.mu.Lock()
		s.active++
		s.mu.Unlock()
		return true
	default:
		return false
	}
}

// Release releases a slot back to the semaphore.
func (s *Semaphore) Release() {
	select {
	case <-s.ch:
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	default:
		// Should not happen - means Release was called without Acquire
		panic("semaphore: release without acquire")
	}
}

// Active returns the number of currently held slots.
func (s *Semaphore) Active() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// Capacity returns the total capacity of the semaphore.
func (s *Semaphore) Capacity() int {
	return cap(s.ch)
}

// Available returns the number of available slots.
func (s *Semaphore) Available() int {
	return s.Capacity() - len(s.ch)
}

// ProviderSemaphores manages semaphores for all providers.
type ProviderSemaphores struct {
	semaphores map[string]*Semaphore
	mu         sync.RWMutex
	capacity   int
}

// NewProviderSemaphores creates a new ProviderSemaphores manager.
func NewProviderSemaphores(capacity int) *ProviderSemaphores {
	if capacity <= 0 {
		capacity = DefaultMaxConcurrentPerProvider
	}
	return &ProviderSemaphores{
		semaphores: make(map[string]*Semaphore),
		capacity:   capacity,
	}
}

// Get returns the semaphore for a provider, creating one if necessary.
func (ps *ProviderSemaphores) Get(providerName string) *Semaphore {
	ps.mu.RLock()
	sem, exists := ps.semaphores[providerName]
	ps.mu.RUnlock()

	if exists {
		return sem
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Double-check after acquiring write lock
	if sem, exists = ps.semaphores[providerName]; exists {
		return sem
	}

	sem = NewSemaphore(providerName, ps.capacity)
	ps.semaphores[providerName] = sem
	return sem
}

// Stats returns concurrency statistics for all providers.
func (ps *ProviderSemaphores) Stats() map[string]SemaphoreStats {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	stats := make(map[string]SemaphoreStats)
	for name, sem := range ps.semaphores {
		stats[name] = SemaphoreStats{
			Active:    sem.Active(),
			Capacity:  sem.Capacity(),
			Available: sem.Available(),
		}
	}
	return stats
}

// SemaphoreStats holds statistics for a semaphore.
type SemaphoreStats struct {
	Active    int `json:"active"`
	Capacity  int `json:"capacity"`
	Available int `json:"available"`
}
