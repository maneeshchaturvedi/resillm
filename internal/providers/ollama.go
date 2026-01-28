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

// OllamaProvider implements the Provider interface for Ollama
type OllamaProvider struct {
	baseURL          string
	httpClient       *http.Client
	streamBufferSize int
}

// Ollama-specific types
type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  ollamaOptions   `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type ollamaResponse struct {
	Model     string        `json:"model"`
	CreatedAt string        `json:"created_at"`
	Message   ollamaMessage `json:"message"`
	Done      bool          `json:"done"`
	TotalDuration     int64 `json:"total_duration,omitempty"`
	LoadDuration      int64 `json:"load_duration,omitempty"`
	PromptEvalCount   int   `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64 `json:"prompt_eval_duration,omitempty"`
	EvalCount         int   `json:"eval_count,omitempty"`
	EvalDuration      int64 `json:"eval_duration,omitempty"`
}

// NewOllamaProvider creates a new Ollama provider
func NewOllamaProvider(cfg config.ProviderConfig, httpClient *http.Client) (*OllamaProvider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	bufferSize := cfg.StreamBufferSize
	if bufferSize <= 0 {
		bufferSize = DefaultStreamBufferSize
	}

	return &OllamaProvider{
		baseURL:          strings.TrimSuffix(baseURL, "/"),
		httpClient:       httpClient,
		streamBufferSize: bufferSize,
	}, nil
}

func (p *OllamaProvider) Name() string {
	return "ollama"
}

func (p *OllamaProvider) ExecuteChat(ctx context.Context, req *types.ChatCompletionRequest, model string) (*types.ChatCompletionResponse, error) {
	if model == "" {
		model = req.Model
	}

	ollamaReq := p.convertRequest(req, model)
	ollamaReq.Stream = false

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

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
			Provider:   "ollama",
		}
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	return p.convertResponse(&ollamaResp, req.Model), nil
}

func (p *OllamaProvider) ExecuteChatStream(ctx context.Context, req *types.ChatCompletionRequest, model string) (<-chan types.StreamChunk, error) {
	if model == "" {
		model = req.Model
	}

	ollamaReq := p.convertRequest(req, model)
	ollamaReq.Stream = true

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

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
			Provider:   "ollama",
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

		isFirst := true

		for scanner.Scan() {
			lineStr := strings.TrimSpace(scanner.Text())
			if lineStr == "" {
				continue
			}

			var ollamaResp ollamaResponse
			if err := json.Unmarshal([]byte(lineStr), &ollamaResp); err != nil {
				continue
			}

			finishReason := ""
			if ollamaResp.Done {
				finishReason = "stop"
			}

			chunk := &types.ChatCompletionChunk{
				ID:                "chatcmpl-ollama",
				Object:            "chat.completion.chunk",
				Created:           time.Now().Unix(),
				Model:             req.Model,
				SystemFingerprint: "fp_resillm",
				Choices: []types.ChunkChoice{
					{
						Index: 0,
						Delta: types.Delta{
							Content: ollamaResp.Message.Content,
						},
						FinishReason: finishReason,
					},
				},
			}

			// Set role on first chunk
			if isFirst {
				chunk.Choices[0].Delta.Role = "assistant"
				isFirst = false
			}

			chunkChan <- types.StreamChunk{Data: chunk}

			if ollamaResp.Done {
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

func (p *OllamaProvider) convertRequest(req *types.ChatCompletionRequest, model string) *ollamaRequest {
	ollamaReq := &ollamaRequest{
		Model: model,
	}

	// Convert messages
	for _, msg := range req.Messages {
		content := ""
		if c, ok := msg.Content.(string); ok {
			content = c
		}

		ollamaReq.Messages = append(ollamaReq.Messages, ollamaMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	// Convert options
	if req.Temperature != nil {
		ollamaReq.Options.Temperature = *req.Temperature
	}
	if req.TopP != nil {
		ollamaReq.Options.TopP = *req.TopP
	}
	if req.MaxTokens != nil {
		ollamaReq.Options.NumPredict = *req.MaxTokens
	}

	// Convert stop sequences
	if req.Stop != nil {
		switch s := req.Stop.(type) {
		case string:
			ollamaReq.Options.Stop = []string{s}
		case []interface{}:
			for _, v := range s {
				if str, ok := v.(string); ok {
					ollamaReq.Options.Stop = append(ollamaReq.Options.Stop, str)
				}
			}
		}
	}

	return ollamaReq
}

func (p *OllamaProvider) convertResponse(resp *ollamaResponse, requestedModel string) *types.ChatCompletionResponse {
	return &types.ChatCompletionResponse{
		ID:      "chatcmpl-ollama-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   requestedModel,
		Choices: []types.Choice{
			{
				Index: 0,
				Message: types.Message{
					Role:    resp.Message.Role,
					Content: resp.Message.Content,
				},
				FinishReason: "stop",
			},
		},
		Usage: types.Usage{
			PromptTokens:     resp.PromptEvalCount,
			CompletionTokens: resp.EvalCount,
			TotalTokens:      resp.PromptEvalCount + resp.EvalCount,
		},
	}
}

func (p *OllamaProvider) CalculateCost(model string, usage types.Usage) float64 {
	// Ollama is self-hosted, no direct cost
	return 0
}

func (p *OllamaProvider) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}

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
