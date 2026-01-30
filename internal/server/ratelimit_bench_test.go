package server

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// benchResult prevents dead code elimination
var benchResult interface{}

func BenchmarkShardedRateLimiter_GetLimiter_ExistingIP(b *testing.B) {
	b.ReportAllocs()

	rl := NewShardedRateLimiter(100, 200)
	defer rl.Stop()

	// Pre-populate with the IP we'll query
	ip := "192.168.1.1"
	rl.GetLimiter(ip)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = rl.GetLimiter(ip)
	}
}

func BenchmarkShardedRateLimiter_GetLimiter_NewIP(b *testing.B) {
	b.ReportAllocs()

	rl := NewShardedRateLimiter(100, 200)
	defer rl.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		benchResult = rl.GetLimiter(ip)
	}
}

func BenchmarkShardedRateLimiter_Allow_ExistingIP(b *testing.B) {
	b.ReportAllocs()

	rl := NewShardedRateLimiter(1000000, 2000000) // High limits so Allow always succeeds
	defer rl.Stop()

	// Pre-populate
	ip := "192.168.1.1"
	rl.GetLimiter(ip)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = rl.Allow(ip)
	}
}

func BenchmarkShardedRateLimiter_Concurrent_SameIP(b *testing.B) {
	b.ReportAllocs()

	rl := NewShardedRateLimiter(1000000, 2000000)
	defer rl.Stop()

	ip := "192.168.1.1"
	rl.GetLimiter(ip)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rl.GetLimiter(ip)
		}
	})
}

func BenchmarkShardedRateLimiter_Concurrent_DifferentIPs(b *testing.B) {
	b.ReportAllocs()

	rl := NewShardedRateLimiter(1000000, 2000000)
	defer rl.Stop()

	var counter atomic.Int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			ip := fmt.Sprintf("10.%d.%d.%d", (n/65536)%256, (n/256)%256, n%256)
			rl.GetLimiter(ip)
		}
	})
}

func BenchmarkShardedRateLimiter_ShardDistribution(b *testing.B) {
	// This benchmark tests the distribution of IPs across shards
	// by measuring access patterns with many unique IPs
	b.ReportAllocs()

	rl := NewShardedRateLimiter(100, 200)
	defer rl.Stop()

	// Generate a pool of unique IPs
	numIPs := 10000
	ips := make([]string, numIPs)
	for i := 0; i < numIPs; i++ {
		ips[i] = fmt.Sprintf("172.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256)
	}

	// Pre-populate all limiters
	for _, ip := range ips {
		rl.GetLimiter(ip)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ip := ips[i%numIPs]
		benchResult = rl.GetLimiter(ip)
	}
}

func BenchmarkShardedRateLimiter_Stats(b *testing.B) {
	b.ReportAllocs()

	rl := NewShardedRateLimiter(100, 200)
	defer rl.Stop()

	// Add some entries
	for i := 0; i < 100; i++ {
		rl.GetLimiter(fmt.Sprintf("10.0.0.%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = rl.Stats()
	}
}
