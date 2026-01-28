package providers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/resillm/resillm/internal/config"
)

// HTTPClientConfig holds configuration for creating HTTP clients.
type HTTPClientConfig struct {
	ConnectTimeout      time.Duration
	RequestTimeout      time.Duration
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int
	IdleConnTimeout     time.Duration
}

// Connection pool defaults optimized for high concurrency (1000+ requests)
const (
	// DefaultMaxIdleConns is the maximum number of idle connections across all hosts.
	// Set high enough to handle concurrent requests to multiple providers.
	DefaultMaxIdleConns = 500

	// DefaultMaxIdleConnsPerHost is the maximum number of idle connections per host.
	// Should be high enough to avoid connection churn under load.
	DefaultMaxIdleConnsPerHost = 100

	// DefaultMaxConnsPerHost is the maximum total connections per host.
	// At 1000 concurrent requests across 3-4 providers, each host needs ~250-300.
	DefaultMaxConnsPerHost = 250
)

// DefaultHTTPClientConfig returns sensible defaults for HTTP client configuration.
// Defaults are optimized for high concurrency scenarios (1000+ concurrent requests).
func DefaultHTTPClientConfig(timeout config.TimeoutConfig) HTTPClientConfig {
	connectTimeout := timeout.Connect
	if connectTimeout == 0 {
		connectTimeout = 5 * time.Second
	}

	requestTimeout := timeout.Request
	if requestTimeout == 0 {
		requestTimeout = 120 * time.Second
	}

	return HTTPClientConfig{
		ConnectTimeout:      connectTimeout,
		RequestTimeout:      requestTimeout,
		MaxIdleConns:        DefaultMaxIdleConns,
		MaxIdleConnsPerHost: DefaultMaxIdleConnsPerHost,
		MaxConnsPerHost:     DefaultMaxConnsPerHost,
		IdleConnTimeout:     90 * time.Second,
	}
}

// NewHTTPClient creates an HTTP client with proper timeout configuration.
// Unlike the previous implementation, this actually uses the connect timeout
// for dial and TLS handshake operations.
func NewHTTPClient(cfg HTTPClientConfig) *http.Client {
	return &http.Client{
		Timeout: cfg.RequestTimeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   cfg.ConnectTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			TLSHandshakeTimeout:   cfg.ConnectTimeout,
			MaxIdleConns:          cfg.MaxIdleConns,
			MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
			MaxConnsPerHost:       cfg.MaxConnsPerHost,
			IdleConnTimeout:       cfg.IdleConnTimeout,
			ResponseHeaderTimeout: cfg.ConnectTimeout, // Timeout for reading response headers
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// RequestConfig holds configuration for a single HTTP request.
type RequestConfig struct {
	Method   string
	URL      string
	Body     interface{} // Will be JSON marshaled if not nil
	Headers  map[string]string
	Provider string // Provider name for error messages
}

// ExecuteJSONRequest executes an HTTP request with JSON body and returns the response body.
// This consolidates the duplicated HTTP request logic across providers.
func ExecuteJSONRequest(ctx context.Context, client *http.Client, cfg RequestConfig) ([]byte, error) {
	var bodyReader io.Reader
	if cfg.Body != nil {
		bodyBytes, err := json.Marshal(cfg.Body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	httpReq, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	for key, value := range cfg.Headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			Provider:   cfg.Provider,
		}
	}

	return respBody, nil
}

// ExecuteStreamRequest executes an HTTP request and returns the response body for streaming.
// The caller is responsible for closing the response body.
func ExecuteStreamRequest(ctx context.Context, client *http.Client, cfg RequestConfig) (io.ReadCloser, error) {
	var bodyReader io.Reader
	if cfg.Body != nil {
		bodyBytes, err := json.Marshal(cfg.Body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	httpReq, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for key, value := range cfg.Headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &ProviderError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			Provider:   cfg.Provider,
		}
	}

	return resp.Body, nil
}
