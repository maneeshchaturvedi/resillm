package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

// Config represents the complete configuration for resillm
type Config struct {
	Server     ServerConfig              `yaml:"server"`
	Providers  map[string]ProviderConfig `yaml:"providers"`
	Models     map[string]ModelConfig    `yaml:"models"`
	Resilience ResilienceConfig          `yaml:"resilience"`
	Budget     BudgetConfig              `yaml:"budget"`
	Metrics    MetricsConfig             `yaml:"metrics"`
	Logging    LoggingConfig             `yaml:"logging"`
}

// ServerConfig defines HTTP server settings
type ServerConfig struct {
	Host         string             `yaml:"host"`
	Port         int                `yaml:"port"`
	MetricsPort  int                `yaml:"metrics_port"`
	AdminAPIKey  string             `yaml:"admin_api_key"` // API key for admin endpoints
	RateLimit    RateLimitConfig    `yaml:"rate_limit"`
	LoadShedding LoadSheddingConfig `yaml:"load_shedding"`
}

// RateLimitConfig defines rate limiting settings
type RateLimitConfig struct {
	Enabled           bool    `yaml:"enabled"`
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

// LoadSheddingConfig defines load shedding settings
type LoadSheddingConfig struct {
	Enabled             bool  `yaml:"enabled"`
	MaxActiveRequests   int64 `yaml:"max_active_requests"`    // Max concurrent requests (0 = unlimited)
	MaxConnectionsPerIP int64 `yaml:"max_connections_per_ip"` // Max connections per IP (0 = unlimited)
}

// ProviderConfig defines settings for an LLM provider
type ProviderConfig struct {
	APIKey           string `yaml:"api_key"`
	BaseURL          string `yaml:"base_url"`
	APIVersion       string `yaml:"api_version"`        // For Azure
	Region           string `yaml:"region"`             // For Bedrock
	MaxTokensDefault int    `yaml:"max_tokens_default"` // Default max_tokens if not specified in request
	StreamBufferSize int    `yaml:"stream_buffer_size"` // Buffer size for streaming channels (default: 32)
}

// ModelConfig defines routing and fallback for a model
type ModelConfig struct {
	Primary    EndpointConfig    `yaml:"primary"`
	Fallbacks  []EndpointConfig  `yaml:"fallbacks"`
	Resilience *ResilienceConfig `yaml:"resilience"` // Per-model overrides
}

// EndpointConfig defines a specific provider/model endpoint
type EndpointConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Weight   int    `yaml:"weight"` // For load balancing
}

// ResilienceConfig defines retry, circuit breaker, and timeout settings
type ResilienceConfig struct {
	Retry          RetryConfig          `yaml:"retry"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Timeout        TimeoutConfig        `yaml:"timeout"`
}

// RetryConfig defines retry behavior
type RetryConfig struct {
	MaxAttempts       int           `yaml:"max_attempts"`
	InitialBackoff    time.Duration `yaml:"initial_backoff"`
	MaxBackoff        time.Duration `yaml:"max_backoff"`
	BackoffMultiplier float64       `yaml:"backoff_multiplier"`
	RetryableErrors   []int         `yaml:"retryable_errors"`
}

// CircuitBreakerConfig defines circuit breaker behavior
type CircuitBreakerConfig struct {
	FailureThreshold    int           `yaml:"failure_threshold"`
	SuccessThreshold    int           `yaml:"success_threshold"`
	Timeout             time.Duration `yaml:"timeout"`
	HalfOpenMaxRequests int           `yaml:"half_open_max_requests"` // Max requests allowed in half-open state
}

// TimeoutConfig defines timeout settings
type TimeoutConfig struct {
	Connect time.Duration `yaml:"connect"`
	Request time.Duration `yaml:"request"`
}

// BudgetConfig defines cost control settings
type BudgetConfig struct {
	Enabled          bool    `yaml:"enabled"`
	MaxCostPerHour   float64 `yaml:"max_cost_per_hour"`
	MaxCostPerDay    float64 `yaml:"max_cost_per_day"`
	AlertThreshold   float64 `yaml:"alert_threshold"`
	ActionOnExceeded string  `yaml:"action_on_exceeded"` // "reject" or "allow_with_warning"
}

// MetricsConfig defines metrics settings
type MetricsConfig struct {
	Enabled    bool             `yaml:"enabled"`
	Prometheus PrometheusConfig `yaml:"prometheus"`
}

// PrometheusConfig defines Prometheus settings
type PrometheusConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// LoggingConfig defines logging settings
type LoggingConfig struct {
	Level        string `yaml:"level"`
	Format       string `yaml:"format"`
	LogRequests  bool   `yaml:"log_requests"`
	LogResponses bool   `yaml:"log_responses"`
}

// Load reads and parses a configuration file
func Load(path string) (*Config, error) {
	// Check file permissions (warn if world-readable)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		log.Warn().
			Str("path", path).
			Str("permissions", fmt.Sprintf("%#o", info.Mode().Perm())).
			Msg("Config file has insecure permissions (contains API keys). Recommend chmod 600")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Expand environment variables
	expanded := expandEnvVars(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Apply defaults
	applyDefaults(&cfg)

	// Check for unexpanded environment variables in sensitive fields
	if err := validateNoUnexpandedVars(&cfg); err != nil {
		return nil, err
	}

	// Validate
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

// expandEnvVars replaces ${VAR} with environment variable values
func expandEnvVars(s string) string {
	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		varName := strings.TrimPrefix(strings.TrimSuffix(match, "}"), "${")
		if val := os.Getenv(varName); val != "" {
			return val
		}
		return match // Keep original if not found
	})
}

// applyDefaults sets default values for unspecified config options
func applyDefaults(cfg *Config) {
	// Server defaults
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.MetricsPort == 0 {
		cfg.Server.MetricsPort = 9090
	}

	// Resilience defaults
	if cfg.Resilience.Retry.MaxAttempts == 0 {
		cfg.Resilience.Retry.MaxAttempts = 3
	}
	if cfg.Resilience.Retry.InitialBackoff == 0 {
		cfg.Resilience.Retry.InitialBackoff = 100 * time.Millisecond
	}
	if cfg.Resilience.Retry.MaxBackoff == 0 {
		cfg.Resilience.Retry.MaxBackoff = 10 * time.Second
	}
	if cfg.Resilience.Retry.BackoffMultiplier == 0 {
		cfg.Resilience.Retry.BackoffMultiplier = 2.0
	}
	if len(cfg.Resilience.Retry.RetryableErrors) == 0 {
		cfg.Resilience.Retry.RetryableErrors = []int{429, 500, 502, 503, 504}
	}

	if cfg.Resilience.CircuitBreaker.FailureThreshold == 0 {
		cfg.Resilience.CircuitBreaker.FailureThreshold = 5
	}
	if cfg.Resilience.CircuitBreaker.SuccessThreshold == 0 {
		cfg.Resilience.CircuitBreaker.SuccessThreshold = 3
	}
	if cfg.Resilience.CircuitBreaker.Timeout == 0 {
		cfg.Resilience.CircuitBreaker.Timeout = 30 * time.Second
	}
	if cfg.Resilience.CircuitBreaker.HalfOpenMaxRequests == 0 {
		cfg.Resilience.CircuitBreaker.HalfOpenMaxRequests = 50
	}

	if cfg.Resilience.Timeout.Connect == 0 {
		cfg.Resilience.Timeout.Connect = 5 * time.Second
	}
	if cfg.Resilience.Timeout.Request == 0 {
		cfg.Resilience.Timeout.Request = 120 * time.Second
	}

	// Logging defaults
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}

	// Metrics defaults
	if cfg.Metrics.Prometheus.Path == "" {
		cfg.Metrics.Prometheus.Path = "/metrics"
	}

	// Budget defaults
	if cfg.Budget.AlertThreshold == 0 {
		cfg.Budget.AlertThreshold = 0.8
	}
	if cfg.Budget.ActionOnExceeded == "" {
		cfg.Budget.ActionOnExceeded = "reject"
	}

	// Rate limit defaults (applied only when enabled)
	if cfg.Server.RateLimit.Enabled {
		if cfg.Server.RateLimit.RequestsPerSecond == 0 {
			cfg.Server.RateLimit.RequestsPerSecond = 10
		}
		if cfg.Server.RateLimit.Burst == 0 {
			cfg.Server.RateLimit.Burst = 20
		}
	}

	// Load shedding defaults (applied only when enabled)
	if cfg.Server.LoadShedding.Enabled {
		if cfg.Server.LoadShedding.MaxActiveRequests == 0 {
			cfg.Server.LoadShedding.MaxActiveRequests = 1000 // Default max concurrent requests
		}
		if cfg.Server.LoadShedding.MaxConnectionsPerIP == 0 {
			cfg.Server.LoadShedding.MaxConnectionsPerIP = 100 // Default per-IP connection limit
		}
	}
}

// validateNoUnexpandedVars checks for unexpanded environment variables in sensitive fields
func validateNoUnexpandedVars(cfg *Config) error {
	envVarPattern := regexp.MustCompile(`\$\{[^}]+\}`)

	// Check provider API keys
	for name, prov := range cfg.Providers {
		if envVarPattern.MatchString(prov.APIKey) {
			return fmt.Errorf("provider %q: API key contains unexpanded variable: %s", name, prov.APIKey)
		}
	}

	// Check admin API key
	if cfg.Server.AdminAPIKey != "" && envVarPattern.MatchString(cfg.Server.AdminAPIKey) {
		return fmt.Errorf("server.admin_api_key contains unexpanded variable")
	}

	return nil
}

// validate checks the configuration for errors
func validate(cfg *Config) error {
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("at least one provider must be configured")
	}

	if len(cfg.Models) == 0 {
		return fmt.Errorf("at least one model must be configured")
	}

	// Validate each model references valid providers
	for modelName, modelCfg := range cfg.Models {
		if _, ok := cfg.Providers[modelCfg.Primary.Provider]; !ok {
			return fmt.Errorf("model %q references unknown provider %q", modelName, modelCfg.Primary.Provider)
		}
		for _, fb := range modelCfg.Fallbacks {
			if _, ok := cfg.Providers[fb.Provider]; !ok {
				return fmt.Errorf("model %q fallback references unknown provider %q", modelName, fb.Provider)
			}
		}
	}

	// Validate rate limit config
	if cfg.Server.RateLimit.Enabled {
		if cfg.Server.RateLimit.RequestsPerSecond <= 0 {
			return fmt.Errorf("rate_limit.requests_per_second must be positive when enabled")
		}
		if cfg.Server.RateLimit.Burst <= 0 {
			return fmt.Errorf("rate_limit.burst must be positive when enabled")
		}
	}

	return nil
}
