package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/types"
)

func TestNewAnthropicProvider_DefaultBaseURL(t *testing.T) {
	cfg := config.ProviderConfig{
		APIKey: "test-key",
	}

	provider, err := NewAnthropicProvider(cfg, http.DefaultClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider.baseURL != "https://api.anthropic.com/v1" {
		t.Errorf("expected default base URL, got %s", provider.baseURL)
	}
}

func TestNewAnthropicProvider_CustomBaseURL(t *testing.T) {
	cfg := config.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: "https://custom.anthropic.com/v1/",
	}

	provider, err := NewAnthropicProvider(cfg, http.DefaultClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should strip trailing slash
	if provider.baseURL != "https://custom.anthropic.com/v1" {
		t.Errorf("expected trimmed URL, got %s", provider.baseURL)
	}
}

func TestAnthropicProvider_Name(t *testing.T) {
	provider := &AnthropicProvider{}

	if provider.Name() != "anthropic" {
		t.Errorf("expected 'anthropic', got '%s'", provider.Name())
	}
}

func TestAnthropicProvider_ConvertRequest_Basic(t *testing.T) {
	provider := &AnthropicProvider{}

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Model != "claude-3-sonnet-20240229" {
		t.Errorf("expected model 'claude-3-sonnet-20240229', got '%s'", result.Model)
	}

	if result.MaxTokens != 4096 {
		t.Errorf("expected default max_tokens 4096, got %d", result.MaxTokens)
	}

	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(result.Messages))
	}

	if result.Messages[0].Role != "user" {
		t.Errorf("expected role 'user', got '%s'", result.Messages[0].Role)
	}
}

func TestAnthropicProvider_ConvertRequest_SystemMessage(t *testing.T) {
	provider := &AnthropicProvider{}

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello"},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// System message should be extracted
	if result.System != "You are a helpful assistant." {
		t.Errorf("expected system message, got '%s'", result.System)
	}

	// Only user message should be in messages array
	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message (system extracted), got %d", len(result.Messages))
	}
}

func TestAnthropicProvider_ConvertRequest_MaxTokens(t *testing.T) {
	provider := &AnthropicProvider{}

	maxTokens := 2048
	req := &types.ChatCompletionRequest{
		Model:     "gpt-4o",
		MaxTokens: &maxTokens,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MaxTokens != 2048 {
		t.Errorf("expected max_tokens 2048, got %d", result.MaxTokens)
	}
}

func TestAnthropicProvider_ConvertRequest_Temperature(t *testing.T) {
	provider := &AnthropicProvider{}

	temp := 0.7
	req := &types.ChatCompletionRequest{
		Model:       "gpt-4o",
		Temperature: &temp,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Temperature == nil || *result.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", result.Temperature)
	}
}

func TestAnthropicProvider_ConvertRequest_StopSequences_String(t *testing.T) {
	provider := &AnthropicProvider{}

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Stop:  "END",
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.StopSequences) != 1 || result.StopSequences[0] != "END" {
		t.Errorf("expected stop sequence 'END', got %v", result.StopSequences)
	}
}

func TestAnthropicProvider_ConvertRequest_StopSequences_Array(t *testing.T) {
	provider := &AnthropicProvider{}

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Stop:  []interface{}{"END", "STOP"},
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.StopSequences) != 2 {
		t.Errorf("expected 2 stop sequences, got %d", len(result.StopSequences))
	}
}

func TestAnthropicProvider_ConvertRequest_Tools(t *testing.T) {
	provider := &AnthropicProvider{}

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
		Tools: []types.Tool{
			{
				Type: "function",
				Function: types.Function{
					Name:        "get_weather",
					Description: "Get the current weather",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "The city name",
							},
						},
					},
				},
			},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(result.Tools))
	}

	if result.Tools[0].Name != "get_weather" {
		t.Errorf("expected tool name 'get_weather', got '%s'", result.Tools[0].Name)
	}

	if result.Tools[0].Description != "Get the current weather" {
		t.Errorf("expected tool description, got '%s'", result.Tools[0].Description)
	}
}

func TestAnthropicProvider_ConvertRequest_MultimodalContent(t *testing.T) {
	provider := &AnthropicProvider{}

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []types.Message{
			{
				Role: "user",
				Content: []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "What's in this image?",
					},
				},
			},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}

	// Content should be converted to []anthropicContent
	contents, ok := result.Messages[0].Content.([]anthropicContent)
	if !ok {
		t.Fatalf("expected content to be []anthropicContent")
	}

	if len(contents) != 1 {
		t.Errorf("expected 1 content block, got %d", len(contents))
	}

	if contents[0].Type != "text" {
		t.Errorf("expected type 'text', got '%s'", contents[0].Type)
	}

	if contents[0].Text != "What's in this image?" {
		t.Errorf("expected text content, got '%s'", contents[0].Text)
	}
}

func TestAnthropicProvider_ConvertRequest_Conversation(t *testing.T) {
	provider := &AnthropicProvider{}

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "How are you?"},
		},
	}

	result, err := provider.convertRequest(req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.System != "You are helpful." {
		t.Errorf("expected system message extracted")
	}

	// Should have 3 messages (user, assistant, user)
	if len(result.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result.Messages))
	}

	expectedRoles := []string{"user", "assistant", "user"}
	for i, msg := range result.Messages {
		if msg.Role != expectedRoles[i] {
			t.Errorf("message %d: expected role '%s', got '%s'", i, expectedRoles[i], msg.Role)
		}
	}
}

func TestAnthropicProvider_ConvertResponse_Basic(t *testing.T) {
	provider := &AnthropicProvider{}

	resp := &anthropicResponse{
		ID:   "msg_12345",
		Type: "message",
		Role: "assistant",
		Content: []anthropicContent{
			{Type: "text", Text: "Hello! How can I help you?"},
		},
		Model:      "claude-3-sonnet-20240229",
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:  10,
			OutputTokens: 15,
		},
	}

	result := provider.convertResponse(resp, "gpt-4o")

	if result.ID != "chatcmpl-msg_12345" {
		t.Errorf("expected ID 'chatcmpl-msg_12345', got '%s'", result.ID)
	}

	if result.Object != "chat.completion" {
		t.Errorf("expected object 'chat.completion', got '%s'", result.Object)
	}

	if result.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got '%s'", result.Model)
	}

	if len(result.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(result.Choices))
	}

	if result.Choices[0].Message.Content != "Hello! How can I help you?" {
		t.Errorf("expected content, got '%s'", result.Choices[0].Message.Content)
	}

	if result.Choices[0].Message.Role != "assistant" {
		t.Errorf("expected role 'assistant', got '%s'", result.Choices[0].Message.Role)
	}

	if result.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got '%s'", result.Choices[0].FinishReason)
	}
}

func TestAnthropicProvider_ConvertResponse_MultipleContentBlocks(t *testing.T) {
	provider := &AnthropicProvider{}

	resp := &anthropicResponse{
		ID:   "msg_12345",
		Type: "message",
		Role: "assistant",
		Content: []anthropicContent{
			{Type: "text", Text: "Here's the answer: "},
			{Type: "text", Text: "42"},
		},
		Model:      "claude-3-sonnet-20240229",
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:  10,
			OutputTokens: 5,
		},
	}

	result := provider.convertResponse(resp, "gpt-4o")

	// Multiple text blocks should be concatenated
	if result.Choices[0].Message.Content != "Here's the answer: 42" {
		t.Errorf("expected concatenated content, got '%s'", result.Choices[0].Message.Content)
	}
}

func TestAnthropicProvider_ConvertResponse_StopReasons(t *testing.T) {
	provider := &AnthropicProvider{}

	tests := []struct {
		stopReason     string
		expectedFinish string
	}{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"tool_use", "tool_calls"},
	}

	for _, tt := range tests {
		t.Run(tt.stopReason, func(t *testing.T) {
			resp := &anthropicResponse{
				ID:      "msg_12345",
				Content: []anthropicContent{{Type: "text", Text: "test"}},
				StopReason: tt.stopReason,
				Usage:   anthropicUsage{},
			}

			result := provider.convertResponse(resp, "gpt-4o")

			if result.Choices[0].FinishReason != tt.expectedFinish {
				t.Errorf("expected finish_reason '%s', got '%s'", tt.expectedFinish, result.Choices[0].FinishReason)
			}
		})
	}
}

func TestAnthropicProvider_ConvertResponse_Usage(t *testing.T) {
	provider := &AnthropicProvider{}

	resp := &anthropicResponse{
		ID:      "msg_12345",
		Content: []anthropicContent{{Type: "text", Text: "test"}},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:  100,
			OutputTokens: 200,
		},
	}

	result := provider.convertResponse(resp, "gpt-4o")

	if result.Usage.PromptTokens != 100 {
		t.Errorf("expected prompt_tokens 100, got %d", result.Usage.PromptTokens)
	}

	if result.Usage.CompletionTokens != 200 {
		t.Errorf("expected completion_tokens 200, got %d", result.Usage.CompletionTokens)
	}

	if result.Usage.TotalTokens != 300 {
		t.Errorf("expected total_tokens 300, got %d", result.Usage.TotalTokens)
	}
}

func TestAnthropicProvider_ConvertStreamEvent_ContentBlockDelta(t *testing.T) {
	provider := &AnthropicProvider{}

	event := &anthropicStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &anthropicDelta{
			Type: "text_delta",
			Text: "Hello",
		},
	}

	result := provider.convertStreamEvent(event, "gpt-4o")

	if result == nil {
		t.Fatal("expected chunk, got nil")
	}

	if result.Object != "chat.completion.chunk" {
		t.Errorf("expected object 'chat.completion.chunk', got '%s'", result.Object)
	}

	if result.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got '%s'", result.Model)
	}

	if len(result.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(result.Choices))
	}

	if result.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected delta content 'Hello', got '%s'", result.Choices[0].Delta.Content)
	}
}

func TestAnthropicProvider_ConvertStreamEvent_MessageStart(t *testing.T) {
	provider := &AnthropicProvider{}

	event := &anthropicStreamEvent{
		Type: "message_start",
	}

	result := provider.convertStreamEvent(event, "gpt-4o")

	if result == nil {
		t.Fatal("expected chunk, got nil")
	}

	if result.Choices[0].Delta.Role != "assistant" {
		t.Errorf("expected delta role 'assistant', got '%s'", result.Choices[0].Delta.Role)
	}
}

func TestAnthropicProvider_ConvertStreamEvent_MessageDelta(t *testing.T) {
	provider := &AnthropicProvider{}

	event := &anthropicStreamEvent{
		Type: "message_delta",
		Delta: &anthropicDelta{
			Type: "message_delta",
		},
	}

	result := provider.convertStreamEvent(event, "gpt-4o")

	if result == nil {
		t.Fatal("expected chunk, got nil")
	}

	if result.Choices[0].FinishReason == nil {
		t.Error("expected finish_reason to be set")
	} else if *result.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got '%s'", *result.Choices[0].FinishReason)
	}
}

func TestAnthropicProvider_ConvertStreamEvent_IgnoresUnknownType(t *testing.T) {
	provider := &AnthropicProvider{}

	event := &anthropicStreamEvent{
		Type: "ping",
	}

	result := provider.convertStreamEvent(event, "gpt-4o")

	if result != nil {
		t.Errorf("expected nil for unknown event type, got %v", result)
	}
}

func TestAnthropicProvider_CalculateCost(t *testing.T) {
	provider := &AnthropicProvider{}

	tests := []struct {
		model    string
		usage    types.Usage
		expected float64
	}{
		{
			model: "claude-3-5-sonnet-20241022",
			usage: types.Usage{
				PromptTokens:     1000000, // 1M tokens
				CompletionTokens: 1000000, // 1M tokens
			},
			expected: 3.00 + 15.00, // $3 input + $15 output
		},
		{
			model: "claude-3-haiku-20240307",
			usage: types.Usage{
				PromptTokens:     1000000,
				CompletionTokens: 1000000,
			},
			expected: 0.25 + 1.25, // $0.25 input + $1.25 output
		},
		{
			model: "claude-3-opus-20240229",
			usage: types.Usage{
				PromptTokens:     1000000,
				CompletionTokens: 1000000,
			},
			expected: 15.00 + 75.00, // $15 input + $75 output
		},
		{
			model: "unknown-model",
			usage: types.Usage{
				PromptTokens:     1000000,
				CompletionTokens: 1000000,
			},
			expected: 3.00 + 15.00, // Falls back to Sonnet pricing
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			cost := provider.CalculateCost(tt.model, tt.usage)

			// Allow for floating point comparison
			if cost < tt.expected-0.01 || cost > tt.expected+0.01 {
				t.Errorf("expected cost %.2f, got %.2f", tt.expected, cost)
			}
		})
	}
}

func TestAnthropicProvider_CalculateCost_SmallUsage(t *testing.T) {
	provider := &AnthropicProvider{}

	usage := types.Usage{
		PromptTokens:     1000,  // 1K tokens
		CompletionTokens: 500,   // 500 tokens
	}

	cost := provider.CalculateCost("claude-3-5-sonnet-20241022", usage)

	// $3 per 1M input = $0.003 per 1K
	// $15 per 1M output = $0.0075 per 500
	expectedInput := 0.003
	expectedOutput := 0.0075
	expected := expectedInput + expectedOutput

	if cost < expected-0.0001 || cost > expected+0.0001 {
		t.Errorf("expected cost %.6f, got %.6f", expected, cost)
	}
}

func TestAnthropicProvider_ExecuteChat_Integration(t *testing.T) {
	// Create a mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type header")
		}

		// Verify request body
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			return
		}

		if req.Model != "claude-3-sonnet-20240229" {
			t.Errorf("expected model 'claude-3-sonnet-20240229', got '%s'", req.Model)
		}

		if req.System != "You are helpful." {
			t.Errorf("expected system message, got '%s'", req.System)
		}

		// Return mock response
		resp := anthropicResponse{
			ID:   "msg_test123",
			Type: "message",
			Role: "assistant",
			Content: []anthropicContent{
				{Type: "text", Text: "Hello! I'm here to help."},
			},
			Model:      "claude-3-sonnet-20240229",
			StopReason: "end_turn",
			Usage: anthropicUsage{
				InputTokens:  25,
				OutputTokens: 10,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create provider with mock server
	cfg := config.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}

	provider, err := NewAnthropicProvider(cfg, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	// Execute request
	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
	}

	resp, err := provider.ExecuteChat(context.Background(), req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "chatcmpl-msg_test123" {
		t.Errorf("expected ID 'chatcmpl-msg_test123', got '%s'", resp.ID)
	}

	if resp.Choices[0].Message.Content != "Hello! I'm here to help." {
		t.Errorf("unexpected response content: %s", resp.Choices[0].Message.Content)
	}
}

func TestAnthropicProvider_ExecuteChat_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error": {"message": "Rate limit exceeded"}}`))
	}))
	defer server.Close()

	cfg := config.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}

	provider, _ := NewAnthropicProvider(cfg, http.DefaultClient)

	req := &types.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	_, err := provider.ExecuteChat(context.Background(), req, "claude-3-sonnet-20240229")

	if err == nil {
		t.Fatal("expected error")
	}

	providerErr, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}

	if providerErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", providerErr.StatusCode)
	}
}

func TestAnthropicProvider_ExecuteChatStream_Integration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify streaming request
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("expected Accept header 'text/event-stream'")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send SSE events
		events := []string{
			`{"type": "message_start", "message": {"id": "msg_123"}}`,
			`{"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}`,
			`{"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": " world"}}`,
			`{"type": "message_delta", "delta": {"stop_reason": "end_turn"}}`,
			`{"type": "message_stop"}`,
		}

		for _, event := range events {
			w.Write([]byte("data: " + event + "\n\n"))
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()

	cfg := config.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}

	provider, _ := NewAnthropicProvider(cfg, http.DefaultClient)

	req := &types.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: true,
		Messages: []types.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	chunkChan, err := provider.ExecuteChatStream(context.Background(), req, "claude-3-sonnet-20240229")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var chunks []*types.ChatCompletionChunk
	for chunk := range chunkChan {
		if chunk.Error != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Error)
		}
		if chunk.Data != nil {
			chunks = append(chunks, chunk.Data)
		}
	}

	// Should have received multiple chunks
	if len(chunks) < 3 {
		t.Errorf("expected at least 3 chunks, got %d", len(chunks))
	}

	// First chunk should have role
	if len(chunks) > 0 && chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Errorf("expected first chunk to have role 'assistant'")
	}
}

func TestAnthropicProvider_HealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}

	provider, _ := NewAnthropicProvider(cfg, http.DefaultClient)

	err := provider.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
