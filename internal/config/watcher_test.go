package config

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		return nil
	})

	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	if watcher.path != configPath {
		t.Errorf("expected path %s, got %s", configPath, watcher.path)
	}

	if watcher.debounce != 500*time.Millisecond {
		t.Errorf("expected default debounce 500ms, got %v", watcher.debounce)
	}
}

func TestWatcher_WithDebounce(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		return nil
	}, WithDebounce(100*time.Millisecond))

	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	if watcher.debounce != 100*time.Millisecond {
		t.Errorf("expected debounce 100ms, got %v", watcher.debounce)
	}
}

func TestWatcher_Start(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}

	// Starting again should be a no-op
	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("second start should not fail: %v", err)
	}
}

func TestWatcher_DetectsFileChange(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	var reloadCount int32

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		atomic.AddInt32(&reloadCount, 1)
		return nil
	}, WithDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}

	// Give the watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Modify the config file
	newContent := `
providers:
  openai:
    api_key: "new-key"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o-new
`
	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Wait for the debounce and reload
	time.Sleep(200 * time.Millisecond)

	if atomic.LoadInt32(&reloadCount) != 1 {
		t.Errorf("expected 1 reload, got %d", reloadCount)
	}
}

func TestWatcher_DebouncesRapidChanges(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	var reloadCount int32

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		atomic.AddInt32(&reloadCount, 1)
		return nil
	}, WithDebounce(100*time.Millisecond))
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Make multiple rapid changes
	for i := 0; i < 5; i++ {
		content := `
providers:
  openai:
    api_key: "key-` + string(rune('0'+i)) + `"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // Rapid changes
	}

	// Wait for debounce
	time.Sleep(200 * time.Millisecond)

	// Should only have reloaded once due to debouncing
	count := atomic.LoadInt32(&reloadCount)
	if count > 2 {
		t.Errorf("expected at most 2 reloads due to debouncing, got %d", count)
	}
}

func TestWatcher_ManualReload(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	var reloadCount int32

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		atomic.AddInt32(&reloadCount, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	// Manual reload without starting watcher
	if err := watcher.Reload(); err != nil {
		t.Fatalf("failed to reload: %v", err)
	}

	if atomic.LoadInt32(&reloadCount) != 1 {
		t.Errorf("expected 1 reload, got %d", reloadCount)
	}
}

func TestWatcher_ReloadWithInvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	// Write invalid config
	if err := os.WriteFile(configPath, []byte("invalid: yaml: content:"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Reload should fail
	if err := watcher.Reload(); err == nil {
		t.Error("expected error for invalid config")
	}
}

func TestWatcher_Stop(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	createTestConfigFile(t, configPath)

	watcher, err := NewWatcher(configPath, func(cfg *Config) error {
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}

	// Stop should work
	if err := watcher.Stop(); err != nil {
		t.Fatalf("failed to stop watcher: %v", err)
	}

	// Stopping again should be safe
	if err := watcher.Stop(); err != nil {
		t.Fatalf("second stop should not fail: %v", err)
	}
}

func TestDiff_NoChanges(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"gpt-4o": {
				Primary: EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			},
		},
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "key"},
		},
	}

	diff := Diff(cfg, cfg)

	if diff.ModelsChanged {
		t.Error("expected no model changes")
	}
	if diff.ProvidersChanged {
		t.Error("expected no provider changes")
	}
}

func TestDiff_ModelsChanged(t *testing.T) {
	oldCfg := &Config{
		Models: map[string]ModelConfig{
			"gpt-4o": {
				Primary: EndpointConfig{Provider: "openai", Model: "gpt-4o"},
			},
		},
	}

	newCfg := &Config{
		Models: map[string]ModelConfig{
			"gpt-4o": {
				Primary: EndpointConfig{Provider: "openai", Model: "gpt-4o-new"},
			},
		},
	}

	diff := Diff(oldCfg, newCfg)

	if !diff.ModelsChanged {
		t.Error("expected models changed")
	}
}

func TestDiff_FallbacksChanged(t *testing.T) {
	oldCfg := &Config{
		Models: map[string]ModelConfig{
			"gpt-4o": {
				Primary:   EndpointConfig{Provider: "openai", Model: "gpt-4o"},
				Fallbacks: []EndpointConfig{{Provider: "anthropic", Model: "claude-3"}},
			},
		},
	}

	newCfg := &Config{
		Models: map[string]ModelConfig{
			"gpt-4o": {
				Primary:   EndpointConfig{Provider: "openai", Model: "gpt-4o"},
				Fallbacks: []EndpointConfig{{Provider: "anthropic", Model: "claude-3.5"}},
			},
		},
	}

	diff := Diff(oldCfg, newCfg)

	if !diff.ModelsChanged {
		t.Error("expected models changed due to fallback change")
	}
}

func TestDiff_ProvidersChanged(t *testing.T) {
	oldCfg := &Config{
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "key"},
		},
	}

	newCfg := &Config{
		Providers: map[string]ProviderConfig{
			"openai":    {APIKey: "key"},
			"anthropic": {APIKey: "key2"},
		},
	}

	diff := Diff(oldCfg, newCfg)

	if !diff.ProvidersChanged {
		t.Error("expected providers changed")
	}
}

func TestDiff_ResilienceChanged(t *testing.T) {
	oldCfg := &Config{
		Resilience: ResilienceConfig{
			Retry: RetryConfig{MaxAttempts: 3},
		},
	}

	newCfg := &Config{
		Resilience: ResilienceConfig{
			Retry: RetryConfig{MaxAttempts: 5},
		},
	}

	diff := Diff(oldCfg, newCfg)

	if !diff.ResilienceChanged {
		t.Error("expected resilience changed")
	}
}

func TestDiff_BudgetChanged(t *testing.T) {
	oldCfg := &Config{
		Budget: BudgetConfig{
			Enabled:        true,
			MaxCostPerHour: 100.0,
		},
	}

	newCfg := &Config{
		Budget: BudgetConfig{
			Enabled:        true,
			MaxCostPerHour: 200.0,
		},
	}

	diff := Diff(oldCfg, newCfg)

	if !diff.BudgetChanged {
		t.Error("expected budget changed")
	}
}

func TestDiff_LoggingChanged(t *testing.T) {
	oldCfg := &Config{
		Logging: LoggingConfig{
			Level: "info",
		},
	}

	newCfg := &Config{
		Logging: LoggingConfig{
			Level: "debug",
		},
	}

	diff := Diff(oldCfg, newCfg)

	if !diff.LoggingChanged {
		t.Error("expected logging changed")
	}
}

// Helper function
func createTestConfigFile(t *testing.T, path string) {
	t.Helper()

	content := `
providers:
  openai:
    api_key: "test-key"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}
}
