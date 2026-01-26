package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad_ValidConfig(t *testing.T) {
	configContent := `
server:
  host: "0.0.0.0"
  port: 8080

providers:
  openai:
    api_key: "test-key"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
	cfg := createTempConfig(t, configContent)

	loaded, err := Load(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if loaded.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", loaded.Server.Port)
	}

	if loaded.Providers["openai"].APIKey != "test-key" {
		t.Errorf("expected api_key 'test-key', got '%s'", loaded.Providers["openai"].APIKey)
	}
}

func TestLoad_ExpandsEnvVars(t *testing.T) {
	os.Setenv("TEST_API_KEY", "secret-key-from-env")
	defer os.Unsetenv("TEST_API_KEY")

	configContent := `
providers:
  openai:
    api_key: ${TEST_API_KEY}

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
	cfg := createTempConfig(t, configContent)

	loaded, err := Load(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if loaded.Providers["openai"].APIKey != "secret-key-from-env" {
		t.Errorf("expected api_key 'secret-key-from-env', got '%s'", loaded.Providers["openai"].APIKey)
	}
}

func TestLoad_EnvVarNotSet_RejectsUnexpanded(t *testing.T) {
	// Make sure the env var is not set
	os.Unsetenv("NONEXISTENT_VAR")

	configContent := `
providers:
  openai:
    api_key: ${NONEXISTENT_VAR}

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
	cfg := createTempConfig(t, configContent)

	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected error for unexpanded environment variable, got nil")
	}

	// Should contain helpful error message
	if !strings.Contains(err.Error(), "unexpanded variable") {
		t.Errorf("expected error to mention 'unexpanded variable', got: %v", err)
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	configContent := `
providers:
  openai:
    api_key: "test-key"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
	cfg := createTempConfig(t, configContent)

	loaded, err := Load(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Server defaults
	if loaded.Server.Host != "0.0.0.0" {
		t.Errorf("expected default host '0.0.0.0', got '%s'", loaded.Server.Host)
	}
	if loaded.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", loaded.Server.Port)
	}
	if loaded.Server.MetricsPort != 9090 {
		t.Errorf("expected default metrics port 9090, got %d", loaded.Server.MetricsPort)
	}

	// Resilience defaults
	if loaded.Resilience.Retry.MaxAttempts != 3 {
		t.Errorf("expected default max_attempts 3, got %d", loaded.Resilience.Retry.MaxAttempts)
	}
	if loaded.Resilience.Retry.InitialBackoff != 100*time.Millisecond {
		t.Errorf("expected default initial_backoff 100ms, got %v", loaded.Resilience.Retry.InitialBackoff)
	}
	if loaded.Resilience.CircuitBreaker.FailureThreshold != 5 {
		t.Errorf("expected default failure_threshold 5, got %d", loaded.Resilience.CircuitBreaker.FailureThreshold)
	}

	// Logging defaults
	if loaded.Logging.Level != "info" {
		t.Errorf("expected default log level 'info', got '%s'", loaded.Logging.Level)
	}
}

func TestLoad_ValidatesRequiredFields_NoProviders(t *testing.T) {
	configContent := `
models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
`
	cfg := createTempConfig(t, configContent)

	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected error for missing providers")
	}

	if !contains(err.Error(), "at least one provider") {
		t.Errorf("expected 'at least one provider' error, got: %v", err)
	}
}

func TestLoad_ValidatesRequiredFields_NoModels(t *testing.T) {
	configContent := `
providers:
  openai:
    api_key: "test-key"
`
	cfg := createTempConfig(t, configContent)

	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected error for missing models")
	}

	if !contains(err.Error(), "at least one model") {
		t.Errorf("expected 'at least one model' error, got: %v", err)
	}
}

func TestLoad_ValidatesProviderReferences(t *testing.T) {
	configContent := `
providers:
  openai:
    api_key: "test-key"

models:
  gpt-4o:
    primary:
      provider: nonexistent
      model: gpt-4o
`
	cfg := createTempConfig(t, configContent)

	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected error for invalid provider reference")
	}

	if !contains(err.Error(), "unknown provider") {
		t.Errorf("expected 'unknown provider' error, got: %v", err)
	}
}

func TestLoad_ValidatesFallbackProviderReferences(t *testing.T) {
	configContent := `
providers:
  openai:
    api_key: "test-key"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
    fallbacks:
      - provider: nonexistent
        model: claude-3
`
	cfg := createTempConfig(t, configContent)

	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected error for invalid fallback provider reference")
	}

	if !contains(err.Error(), "unknown provider") {
		t.Errorf("expected 'unknown provider' error, got: %v", err)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	configContent := `
providers:
  openai:
    api_key: "test-key"
    this is not valid yaml
`
	cfg := createTempConfig(t, configContent)

	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_FullConfig(t *testing.T) {
	configContent := `
server:
  host: "127.0.0.1"
  port: 9000
  metrics_port: 9100

providers:
  openai:
    api_key: "sk-openai"
    base_url: "https://api.openai.com/v1"
  anthropic:
    api_key: "sk-anthropic"

models:
  gpt-4o:
    primary:
      provider: openai
      model: gpt-4o
    fallbacks:
      - provider: anthropic
        model: claude-3
  default:
    primary:
      provider: openai
      model: gpt-4o-mini

resilience:
  retry:
    max_attempts: 5
    initial_backoff: 200ms
    max_backoff: 30s
    backoff_multiplier: 3.0
    retryable_errors:
      - 429
      - 503
  circuit_breaker:
    failure_threshold: 10
    success_threshold: 5
    timeout: 60s
  timeout:
    connect: 10s
    request: 180s

budget:
  enabled: true
  max_cost_per_hour: 100.00
  max_cost_per_day: 1000.00
  alert_threshold: 0.9
  action_on_exceeded: allow_with_warning

metrics:
  enabled: true
  prometheus:
    enabled: true
    path: /custom/metrics

logging:
  level: debug
  format: text
  log_requests: true
  log_responses: true
`
	cfg := createTempConfig(t, configContent)

	loaded, err := Load(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Server
	if loaded.Server.Host != "127.0.0.1" {
		t.Errorf("expected host '127.0.0.1', got '%s'", loaded.Server.Host)
	}
	if loaded.Server.Port != 9000 {
		t.Errorf("expected port 9000, got %d", loaded.Server.Port)
	}

	// Providers
	if len(loaded.Providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(loaded.Providers))
	}

	// Models
	if len(loaded.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(loaded.Models))
	}
	if len(loaded.Models["gpt-4o"].Fallbacks) != 1 {
		t.Errorf("expected 1 fallback, got %d", len(loaded.Models["gpt-4o"].Fallbacks))
	}

	// Resilience
	if loaded.Resilience.Retry.MaxAttempts != 5 {
		t.Errorf("expected max_attempts 5, got %d", loaded.Resilience.Retry.MaxAttempts)
	}
	if loaded.Resilience.Retry.InitialBackoff != 200*time.Millisecond {
		t.Errorf("expected initial_backoff 200ms, got %v", loaded.Resilience.Retry.InitialBackoff)
	}
	if loaded.Resilience.CircuitBreaker.Timeout != 60*time.Second {
		t.Errorf("expected circuit timeout 60s, got %v", loaded.Resilience.CircuitBreaker.Timeout)
	}

	// Budget
	if !loaded.Budget.Enabled {
		t.Error("expected budget enabled")
	}
	if loaded.Budget.MaxCostPerHour != 100.00 {
		t.Errorf("expected max_cost_per_hour 100.00, got %f", loaded.Budget.MaxCostPerHour)
	}

	// Metrics
	if loaded.Metrics.Prometheus.Path != "/custom/metrics" {
		t.Errorf("expected prometheus path '/custom/metrics', got '%s'", loaded.Metrics.Prometheus.Path)
	}

	// Logging
	if loaded.Logging.Level != "debug" {
		t.Errorf("expected log level 'debug', got '%s'", loaded.Logging.Level)
	}
}

func TestExpandEnvVars(t *testing.T) {
	os.Setenv("VAR1", "value1")
	os.Setenv("VAR2", "value2")
	defer os.Unsetenv("VAR1")
	defer os.Unsetenv("VAR2")

	tests := []struct {
		input    string
		expected string
	}{
		{"${VAR1}", "value1"},
		{"prefix_${VAR1}_suffix", "prefix_value1_suffix"},
		{"${VAR1}_${VAR2}", "value1_value2"},
		{"no vars here", "no vars here"},
		{"${NONEXISTENT}", "${NONEXISTENT}"}, // Keeps original if not found
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := expandEnvVars(tt.input)
			if result != tt.expected {
				t.Errorf("expandEnvVars(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// Helper functions

func createTempConfig(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp config: %v", err)
	}

	return path
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
