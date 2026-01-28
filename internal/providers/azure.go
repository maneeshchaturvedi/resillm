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

// AzureOpenAIProvider implements the Provider interface for Azure OpenAI
type AzureOpenAIProvider struct {
	apiKey           string
	baseURL          string
	apiVersion       string
	httpClient       *http.Client
	streamBufferSize int
}

// DefaultAzureAPIVersion is the default API version for Azure OpenAI
// Update this periodically as Azure releases new stable versions
const DefaultAzureAPIVersion = "2024-10-21"

// NewAzureOpenAIProvider creates a new Azure OpenAI provider
func NewAzureOpenAIProvider(cfg config.ProviderConfig, httpClient *http.Client) (*AzureOpenAIProvider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required for Azure OpenAI")
	}

	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = DefaultAzureAPIVersion
	}

	bufferSize := cfg.StreamBufferSize
	if bufferSize <= 0 {
		bufferSize = DefaultStreamBufferSize
	}

	return &AzureOpenAIProvider{
		apiKey:           cfg.APIKey,
		baseURL:          strings.TrimSuffix(cfg.BaseURL, "/"),
		apiVersion:       apiVersion,
		httpClient:       httpClient,
		streamBufferSize: bufferSize,
	}, nil
}

func (p *AzureOpenAIProvider) Name() string {
	return "azure-openai"
}

func (p *AzureOpenAIProvider) ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest, model string) (*types.ChatCompletionResponse, error) {
	// Azure uses deployment name instead of model
	deploymentName := model
	if deploymentName == "" {
		deploymentName = req.Model
	}

	// Build Azure-specific URL
	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		p.baseURL, deploymentName, p.apiVersion)

	// Request body is same as OpenAI, but without model field
	reqCopy := *req
	reqCopy.Model = "" // Azure doesn't use model in body
	reqCopy.Stream = false

	body, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", p.apiKey)

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
			Provider:   "azure-openai",
		}
	}

	var result types.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	// Ensure model field is set to what was requested
	result.Model = req.Model

	return &result, nil
}

func (p *AzureOpenAIProvider) ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest, model string) (<-chan types.StreamChunk, error) {
	deploymentName := model
	if deploymentName == "" {
		deploymentName = req.Model
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		p.baseURL, deploymentName, p.apiVersion)

	reqCopy := *req
	reqCopy.Model = ""
	reqCopy.Stream = true

	body, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", p.apiKey)
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
			Provider:   "azure-openai",
		}
	}

	// Use buffered channel to prevent goroutine blocking when consumer is slow
	chunkChan := make(chan types.StreamChunk, p.streamBufferSize)

	go func() {
		defer close(chunkChan)
		defer resp.Body.Close()

		// Use bounded scanner to prevent memory exhaustion from malicious/buggy upstreams
		buf := getBuffer()
		defer putBuffer(buf)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(buf, DefaultScannerMaxBuf)

		for scanner.Scan() {
			lineStr := strings.TrimSpace(scanner.Text())

			if lineStr == "" || !strings.HasPrefix(lineStr, "data: ") {
				continue
			}

			data := strings.TrimPrefix(lineStr, "data: ")

			if data == "[DONE]" {
				return
			}

			var chunk types.ChatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				chunkChan <- types.StreamChunk{Error: err}
				return
			}

			// Set model to requested model
			chunk.Model = req.Model

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

func (p *AzureOpenAIProvider) CalculateCost(model string, usage types.Usage) float64 {
	// Azure pricing is similar to OpenAI but may vary by region/agreement
	// Using standard OpenAI pricing as default
	pricing := map[string]struct{ input, output float64 }{
		"gpt-4o":        {2.50, 10.00},
		"gpt-4o-mini":   {0.15, 0.60},
		"gpt-4":         {30.00, 60.00},
		"gpt-35-turbo":  {0.50, 1.50},
	}

	price, ok := pricing[model]
	if !ok {
		price = pricing["gpt-4o"]
	}

	inputCost := float64(usage.PromptTokens) / 1_000_000 * price.input
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * price.output

	return inputCost + outputCost
}

func (p *AzureOpenAIProvider) HealthCheck(ctx context.Context) error {
	// Azure doesn't have a simple health endpoint
	// We'll just check if the base URL is reachable
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL, nil)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
