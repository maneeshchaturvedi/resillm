package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/types"
)

// OpenAIProvider implements the Provider interface for OpenAI
type OpenAIProvider struct {
	apiKey           string
	baseURL          string
	httpClient       *http.Client
	streamBufferSize int
}

// NewOpenAIProvider creates a new OpenAI provider
func NewOpenAIProvider(cfg config.ProviderConfig, httpClient *http.Client) (*OpenAIProvider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	bufferSize := cfg.StreamBufferSize
	if bufferSize <= 0 {
		bufferSize = DefaultStreamBufferSize
	}

	return &OpenAIProvider{
		apiKey:           cfg.APIKey,
		baseURL:          strings.TrimSuffix(baseURL, "/"),
		httpClient:       httpClient,
		streamBufferSize: bufferSize,
	}, nil
}

func (p *OpenAIProvider) Name() string {
	return "openai"
}

func (p *OpenAIProvider) ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest, model string) (*types.ChatCompletionResponse, error) {
	// Override model if specified
	reqCopy := *req
	if model != "" {
		reqCopy.Model = model
	}
	reqCopy.Stream = false

	body, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
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
			Provider:   "openai",
		}
	}

	var result types.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	return &result, nil
}

func (p *OpenAIProvider) ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest, model string) (<-chan types.StreamChunk, error) {
	// Override model if specified
	reqCopy := *req
	if model != "" {
		reqCopy.Model = model
	}
	reqCopy.Stream = true

	body, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &ProviderError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			Provider:   "openai",
		}
	}

	// Use buffered channel to prevent goroutine blocking when consumer is slow
	chunkChan := make(chan types.StreamChunk, p.streamBufferSize)

	go func() {
		defer close(chunkChan)
		defer resp.Body.Close()

		// Use bounded scanner to prevent memory exhaustion from malicious/buggy upstreams
		// Memory usage bounded to 512KB per stream (configurable)
		buf := getBuffer()
		defer putBuffer(buf)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(buf, DefaultScannerMaxBuf)

		for scanner.Scan() {
			lineStr := strings.TrimSpace(scanner.Text())

			// Skip empty lines
			if lineStr == "" {
				continue
			}

			// Check for data prefix
			if !strings.HasPrefix(lineStr, "data: ") {
				continue
			}

			data := strings.TrimPrefix(lineStr, "data: ")

			// Check for done marker
			if data == "[DONE]" {
				return
			}

			var chunk types.ChatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				chunkChan <- types.StreamChunk{Error: err}
				return
			}

			chunkChan <- types.StreamChunk{Data: &chunk}
		}

		if err := scanner.Err(); err != nil {
			if err == bufio.ErrTooLong {
				chunkChan <- types.StreamChunk{Error: fmt.Errorf("stream line exceeded maximum size of %d bytes", DefaultScannerMaxBuf)}
			} else {
				chunkChan <- types.StreamChunk{Error: err}
			}
		}
	}()

	return chunkChan, nil
}

func (p *OpenAIProvider) CalculateCost(model string, usage types.Usage) float64 {
	// Pricing per 1M tokens (as of 2024)
	pricing := map[string]struct{ input, output float64 }{
		"gpt-4o":            {2.50, 10.00},
		"gpt-4o-mini":       {0.15, 0.60},
		"gpt-4-turbo":       {10.00, 30.00},
		"gpt-4":             {30.00, 60.00},
		"gpt-3.5-turbo":     {0.50, 1.50},
		"gpt-4o-2024-08-06": {2.50, 10.00},
	}

	price, ok := pricing[model]
	if !ok {
		// Default to gpt-4o pricing
		price = pricing["gpt-4o"]
	}

	inputCost := float64(usage.PromptTokens) / 1_000_000 * price.input
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * price.output

	return inputCost + outputCost
}

func (p *OpenAIProvider) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: status %d", resp.StatusCode)
	}

	return nil
}

// NewOpenAICompatibleProvider creates a provider for OpenAI-compatible APIs
func NewOpenAICompatibleProvider(name string, cfg config.ProviderConfig, httpClient *http.Client) (*OpenAIProvider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required for OpenAI-compatible provider %s", name)
	}

	bufferSize := cfg.StreamBufferSize
	if bufferSize <= 0 {
		bufferSize = DefaultStreamBufferSize
	}

	return &OpenAIProvider{
		apiKey:           cfg.APIKey,
		baseURL:          strings.TrimSuffix(cfg.BaseURL, "/"),
		httpClient:       httpClient,
		streamBufferSize: bufferSize,
	}, nil
}

// ProviderError represents an error from a provider
type ProviderError struct {
	StatusCode int
	Body       string
	Provider   string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s error (status %d): %s", e.Provider, e.StatusCode, e.Body)
}

func (e *ProviderError) IsRetryable() bool {
	switch e.StatusCode {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
