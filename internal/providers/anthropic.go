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
	"time"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/types"
)

// AnthropicProvider implements the Provider interface for Anthropic
type AnthropicProvider struct {
	apiKey           string
	baseURL          string
	httpClient       *http.Client
	maxTokensDefault int
	streamBufferSize int
}

// Anthropic model max output token limits
var anthropicModelMaxTokens = map[string]int{
	"claude-3-5-haiku-20241022":  8192,
	"claude-3-haiku-20240307":    4096,
	"claude-3-sonnet-20240229":   4096,
	"claude-3-5-sonnet-20241022": 8192,
	"claude-sonnet-4-20250514":   16384,
	"claude-3-opus-20240229":     4096,
	"claude-opus-4-20250514":     32000,
}

// DefaultAnthropicMaxTokens is the default max_tokens if not specified
const DefaultAnthropicMaxTokens = 4096

// Anthropic-specific types
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	StopSequences []string         `json:"stop_sequences,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []anthropicContent
}

type anthropicContent struct {
	Type   string         `json:"type"`
	Text   string         `json:"text,omitempty"`
	Source *anthropicImage `json:"source,omitempty"`
}

type anthropicImage struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema"`
}

type anthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []anthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        anthropicUsage     `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicStreamEvent struct {
	Type         string            `json:"type"`
	Index        int               `json:"index,omitempty"`
	ContentBlock *anthropicContent `json:"content_block,omitempty"`
	Delta        *anthropicDelta   `json:"delta,omitempty"`
	Message      *anthropicResponse `json:"message,omitempty"`
	Usage        *anthropicUsage   `json:"usage,omitempty"`
}

type anthropicDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// NewAnthropicProvider creates a new Anthropic provider
func NewAnthropicProvider(cfg config.ProviderConfig, httpClient *http.Client) (*AnthropicProvider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}

	maxTokens := cfg.MaxTokensDefault
	if maxTokens <= 0 {
		maxTokens = DefaultAnthropicMaxTokens
	}

	bufferSize := cfg.StreamBufferSize
	if bufferSize <= 0 {
		bufferSize = DefaultStreamBufferSize
	}

	return &AnthropicProvider{
		apiKey:           cfg.APIKey,
		baseURL:          strings.TrimSuffix(baseURL, "/"),
		httpClient:       httpClient,
		maxTokensDefault: maxTokens,
		streamBufferSize: bufferSize,
	}, nil
}

func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

func (p *AnthropicProvider) ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest, model string) (*types.ChatCompletionResponse, error) {
	// Convert OpenAI format to Anthropic format
	anthropicReq, err := p.convertRequest(req, model)
	if err != nil {
		return nil, fmt.Errorf("converting request: %w", err)
	}
	anthropicReq.Stream = false

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

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
			Provider:   "anthropic",
		}
	}

	var anthropicResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	// Convert Anthropic response to OpenAI format
	return p.convertResponse(&anthropicResp, req.Model), nil
}

func (p *AnthropicProvider) ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest, model string) (<-chan types.StreamChunk, error) {
	anthropicReq, err := p.convertRequest(req, model)
	if err != nil {
		return nil, fmt.Errorf("converting request: %w", err)
	}
	anthropicReq.Stream = true

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
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
			Provider:   "anthropic",
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

			var event anthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				chunkChan <- types.StreamChunk{Error: fmt.Errorf("malformed stream chunk: %w", err)}
				return
			}

			// Convert to OpenAI chunk format
			chunk := p.convertStreamEvent(&event, req.Model)
			if chunk != nil {
				chunkChan <- types.StreamChunk{Data: chunk}
			}

			if event.Type == "message_stop" {
				return
			}
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

func (p *AnthropicProvider) convertRequest(req *types.ChatCompletionRequest, model string) (*anthropicRequest, error) {
	if model == "" {
		model = req.Model
	}

	// Determine max_tokens: use request value, then config default, then model-specific limit
	maxTokens := p.maxTokensDefault
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	// Apply model-specific limit if known
	if modelLimit, ok := anthropicModelMaxTokens[model]; ok {
		if maxTokens > modelLimit {
			maxTokens = modelLimit
		}
	}

	anthropicReq := &anthropicRequest{
		Model:       model,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	// Extract system message and convert other messages
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			// Anthropic uses a separate system field
			if content, ok := msg.Content.(string); ok {
				anthropicReq.System = content
			}
			continue
		}

		anthropicMsg := anthropicMessage{
			Role: msg.Role,
		}

		// Handle content (string or multimodal)
		switch c := msg.Content.(type) {
		case string:
			anthropicMsg.Content = c
		case []interface{}:
			// Multimodal content
			var contents []anthropicContent
			for _, part := range c {
				if partMap, ok := part.(map[string]interface{}); ok {
					content := anthropicContent{}
					if t, ok := partMap["type"].(string); ok {
						content.Type = t
						if t == "text" {
							if text, ok := partMap["text"].(string); ok {
								content.Text = text
							}
						}
						// Handle images if needed
					}
					contents = append(contents, content)
				}
			}
			anthropicMsg.Content = contents
		}

		// Map assistant to assistant, user to user
		if msg.Role == "assistant" || msg.Role == "user" {
			anthropicReq.Messages = append(anthropicReq.Messages, anthropicMsg)
		}
	}

	// Convert stop sequences
	if req.Stop != nil {
		switch s := req.Stop.(type) {
		case string:
			anthropicReq.StopSequences = []string{s}
		case []interface{}:
			for _, v := range s {
				if str, ok := v.(string); ok {
					anthropicReq.StopSequences = append(anthropicReq.StopSequences, str)
				}
			}
		}
	}

	// Convert tools
	for _, tool := range req.Tools {
		anthropicReq.Tools = append(anthropicReq.Tools, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}

	return anthropicReq, nil
}

func (p *AnthropicProvider) convertResponse(resp *anthropicResponse, requestedModel string) *types.ChatCompletionResponse {
	// Extract text content
	var content string
	for _, c := range resp.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}

	// Map stop reason
	finishReason := "stop"
	switch resp.StopReason {
	case "end_turn":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	}

	return &types.ChatCompletionResponse{
		ID:      "chatcmpl-" + resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   requestedModel, // Return what was requested
		Choices: []types.Choice{
			{
				Index: 0,
				Message: types.Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: finishReason,
			},
		},
		Usage: types.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}

func (p *AnthropicProvider) convertStreamEvent(event *anthropicStreamEvent, requestedModel string) *types.ChatCompletionChunk {
	switch event.Type {
	case "content_block_delta":
		if event.Delta != nil && event.Delta.Type == "text_delta" {
			return &types.ChatCompletionChunk{
				ID:                "chatcmpl-stream",
				Object:            "chat.completion.chunk",
				Created:           time.Now().Unix(),
				Model:             requestedModel,
				SystemFingerprint: "fp_resillm",
				Choices: []types.ChunkChoice{
					{
						Index: 0,
						Delta: types.Delta{
							Content: event.Delta.Text,
						},
						FinishReason: "",
					},
				},
			}
		}
	case "message_start":
		return &types.ChatCompletionChunk{
			ID:                "chatcmpl-stream",
			Object:            "chat.completion.chunk",
			Created:           time.Now().Unix(),
			Model:             requestedModel,
			SystemFingerprint: "fp_resillm",
			Choices: []types.ChunkChoice{
				{
					Index: 0,
					Delta: types.Delta{
						Role: "assistant",
					},
					FinishReason: "",
				},
			},
		}
	case "message_delta":
		if event.Delta != nil {
			return &types.ChatCompletionChunk{
				ID:                "chatcmpl-stream",
				Object:            "chat.completion.chunk",
				Created:           time.Now().Unix(),
				Model:             requestedModel,
				SystemFingerprint: "fp_resillm",
				Choices: []types.ChunkChoice{
					{
						Index:        0,
						Delta:        types.Delta{},
						FinishReason: "stop",
					},
				},
			}
		}
	}

	return nil
}

func (p *AnthropicProvider) CalculateCost(model string, usage types.Usage) float64 {
	// Pricing per 1M tokens (as of 2024)
	pricing := map[string]struct{ input, output float64 }{
		"claude-3-5-sonnet-20241022": {3.00, 15.00},
		"claude-sonnet-4-20250514":   {3.00, 15.00},
		"claude-3-5-haiku-20241022":  {0.80, 4.00},
		"claude-3-opus-20240229":     {15.00, 75.00},
		"claude-3-sonnet-20240229":   {3.00, 15.00},
		"claude-3-haiku-20240307":    {0.25, 1.25},
	}

	price, ok := pricing[model]
	if !ok {
		// Default to Sonnet pricing
		price = pricing["claude-sonnet-4-20250514"]
	}

	inputCost := float64(usage.PromptTokens) / 1_000_000 * price.input
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * price.output

	return inputCost + outputCost
}

func (p *AnthropicProvider) HealthCheck(ctx context.Context) error {
	// Anthropic doesn't have a models endpoint, so we'll just check connectivity
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL, nil)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Any response means the API is reachable
	return nil
}
