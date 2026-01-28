package server

import (
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const (
	// NumShards is the number of shards for the rate limiter.
	// Using 256 shards reduces lock contention significantly.
	NumShards = 256

	// DefaultCleanupInterval is how often stale entries are cleaned up.
	DefaultCleanupInterval = 5 * time.Minute

	// DefaultEntryTTL is how long an unused rate limiter entry lives.
	DefaultEntryTTL = 10 * time.Minute
)

// rateLimitEntry holds a rate limiter and its last access time.
type rateLimitEntry struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

// rateLimiterShard is a single shard of the rate limiter.
type rateLimiterShard struct {
	mu       sync.RWMutex
	limiters map[string]*rateLimitEntry
}

// ShardedRateLimiter implements per-IP rate limiting with sharding
// to reduce lock contention under high concurrency.
type ShardedRateLimiter struct {
	shards       [NumShards]*rateLimiterShard
	rate         rate.Limit
	burst        int
	cleanupStop  chan struct{}
	totalEntries atomic.Int64
}

// NewShardedRateLimiter creates a new sharded rate limiter.
// It automatically starts a cleanup goroutine to remove stale entries.
func NewShardedRateLimiter(rps float64, burst int) *ShardedRateLimiter {
	rl := &ShardedRateLimiter{
		rate:        rate.Limit(rps),
		burst:       burst,
		cleanupStop: make(chan struct{}),
	}

	// Initialize all shards
	for i := 0; i < NumShards; i++ {
		rl.shards[i] = &rateLimiterShard{
			limiters: make(map[string]*rateLimitEntry),
		}
	}

	// Start cleanup goroutine
	go rl.cleanupLoop()

	return rl
}

// getShard returns the shard for a given key using FNV hash.
func (rl *ShardedRateLimiter) getShard(key string) *rateLimiterShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	return rl.shards[h.Sum32()%NumShards]
}

// GetLimiter returns the rate limiter for a given IP.
// This is the hot path - optimized for minimal lock contention.
func (rl *ShardedRateLimiter) GetLimiter(ip string) *rate.Limiter {
	shard := rl.getShard(ip)

	// Fast path: check if limiter exists with read lock
	shard.mu.RLock()
	entry, exists := shard.limiters[ip]
	if exists {
		entry.lastAccess = time.Now() // Update access time (slight race is OK)
		shard.mu.RUnlock()
		return entry.limiter
	}
	shard.mu.RUnlock()

	// Slow path: create new limiter with write lock
	shard.mu.Lock()
	defer shard.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, exists = shard.limiters[ip]; exists {
		entry.lastAccess = time.Now()
		return entry.limiter
	}

	// Create new entry
	entry = &rateLimitEntry{
		limiter:    rate.NewLimiter(rl.rate, rl.burst),
		lastAccess: time.Now(),
	}
	shard.limiters[ip] = entry
	rl.totalEntries.Add(1)

	return entry.limiter
}

// Allow checks if a request from the given IP is allowed.
func (rl *ShardedRateLimiter) Allow(ip string) bool {
	return rl.GetLimiter(ip).Allow()
}

// cleanupLoop periodically removes stale entries to prevent memory leaks.
func (rl *ShardedRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(DefaultCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.cleanup(DefaultEntryTTL)
		case <-rl.cleanupStop:
			return
		}
	}
}

// cleanup removes entries that haven't been accessed within the TTL.
func (rl *ShardedRateLimiter) cleanup(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	var removed int64

	for _, shard := range rl.shards {
		shard.mu.Lock()
		for ip, entry := range shard.limiters {
			if entry.lastAccess.Before(cutoff) {
				delete(shard.limiters, ip)
				removed++
			}
		}
		shard.mu.Unlock()
	}

	if removed > 0 {
		rl.totalEntries.Add(-removed)
	}
}

// Stop stops the cleanup goroutine. Should be called when the rate limiter
// is no longer needed.
func (rl *ShardedRateLimiter) Stop() {
	close(rl.cleanupStop)
}

// Stats returns statistics about the rate limiter.
func (rl *ShardedRateLimiter) Stats() RateLimiterStats {
	return RateLimiterStats{
		TotalEntries: rl.totalEntries.Load(),
		NumShards:    NumShards,
		Rate:         float64(rl.rate),
		Burst:        rl.burst,
	}
}

// RateLimiterStats holds statistics about the rate limiter.
type RateLimiterStats struct {
	TotalEntries int64   `json:"total_entries"`
	NumShards    int     `json:"num_shards"`
	Rate         float64 `json:"rate"`
	Burst        int     `json:"burst"`
}
