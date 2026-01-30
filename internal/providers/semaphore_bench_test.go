package providers

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// benchResult prevents dead code elimination
var benchResult interface{}

func BenchmarkSemaphore_Acquire_NoContention(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 1000)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sem.Acquire(ctx)
		sem.Release()
	}
}

func BenchmarkSemaphore_TryAcquire_NoContention(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sem.TryAcquire() {
			sem.Release()
		}
	}
}

func BenchmarkSemaphore_TryAcquire_Full(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 10)
	ctx := context.Background()

	// Fill up the semaphore
	for i := 0; i < 10; i++ {
		sem.Acquire(ctx)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = sem.TryAcquire() // Should always return false
	}
}

func BenchmarkSemaphore_Active(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 100)
	ctx := context.Background()

	// Acquire some slots
	for i := 0; i < 50; i++ {
		sem.Acquire(ctx)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = sem.Active()
	}
}

func BenchmarkSemaphore_Capacity(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = sem.Capacity()
	}
}

func BenchmarkSemaphore_Available(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 100)
	ctx := context.Background()

	// Acquire some slots
	for i := 0; i < 30; i++ {
		sem.Acquire(ctx)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = sem.Available()
	}
}

func BenchmarkSemaphore_Concurrent_AcquireRelease(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 100)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sem.Acquire(ctx)
			sem.Release()
		}
	})
}

func BenchmarkSemaphore_Concurrent_TryAcquireRelease(b *testing.B) {
	b.ReportAllocs()

	sem := NewSemaphore("test", 100)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if sem.TryAcquire() {
				sem.Release()
			}
		}
	})
}

func BenchmarkSemaphore_Concurrent_HighContention(b *testing.B) {
	b.ReportAllocs()

	// Small capacity to create contention
	sem := NewSemaphore("test", 4)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sem.Acquire(ctx)
			sem.Release()
		}
	})
}

func BenchmarkProviderSemaphores_Get_ExistingProvider(b *testing.B) {
	b.ReportAllocs()

	ps := NewProviderSemaphores(200)
	// Pre-create the semaphore
	ps.Get("openai")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = ps.Get("openai")
	}
}

func BenchmarkProviderSemaphores_Get_NewProvider(b *testing.B) {
	b.ReportAllocs()

	ps := NewProviderSemaphores(200)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := fmt.Sprintf("provider-%d", i)
		benchResult = ps.Get(name)
	}
}

func BenchmarkProviderSemaphores_Get_Concurrent_SameProvider(b *testing.B) {
	b.ReportAllocs()

	ps := NewProviderSemaphores(200)
	ps.Get("openai") // Pre-create

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ps.Get("openai")
		}
	})
}

func BenchmarkProviderSemaphores_Get_Concurrent_DifferentProviders(b *testing.B) {
	b.ReportAllocs()

	ps := NewProviderSemaphores(200)
	providers := []string{"openai", "anthropic", "azure", "bedrock", "cohere"}

	// Pre-create all semaphores
	for _, p := range providers {
		ps.Get(p)
	}

	var counter atomic.Int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			provider := providers[n%int64(len(providers))]
			ps.Get(provider)
		}
	})
}

func BenchmarkProviderSemaphores_Stats(b *testing.B) {
	b.ReportAllocs()

	ps := NewProviderSemaphores(200)
	ctx := context.Background()

	// Create some providers with active semaphores
	providers := []string{"openai", "anthropic", "azure", "bedrock", "cohere"}
	for i, p := range providers {
		sem := ps.Get(p)
		for j := 0; j < (i+1)*10; j++ {
			sem.Acquire(ctx)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResult = ps.Stats()
	}
}

func BenchmarkProviderSemaphores_FullWorkflow(b *testing.B) {
	// This benchmark simulates a realistic workflow:
	// 1. Get semaphore for a provider
	// 2. Acquire
	// 3. Do work (simulated)
	// 4. Release
	b.ReportAllocs()

	ps := NewProviderSemaphores(200)
	ctx := context.Background()
	providers := []string{"openai", "anthropic", "azure"}

	// Pre-create
	for _, p := range providers {
		ps.Get(p)
	}

	var counter atomic.Int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			provider := providers[n%int64(len(providers))]

			sem := ps.Get(provider)
			if err := sem.Acquire(ctx); err == nil {
				// Simulate minimal work
				_ = sem.Active()
				sem.Release()
			}
		}
	})
}
