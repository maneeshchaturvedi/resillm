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

// DefaultHTTPClientConfig returns sensible defaults for HTTP client configuration.
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
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		MaxConnsPerHost:     50,
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
