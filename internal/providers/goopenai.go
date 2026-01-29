package providers

import (
	"context"
	"errors"
	"fmt"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/pricing"
	openai "github.com/sashabaranov/go-openai"
)

// GoOpenAIProvider is a unified provider using the go-openai library.
// It supports OpenAI, Anthropic, and Azure through different APIType configurations.
type GoOpenAIProvider struct {
	client     *openai.Client
	name       string
	calculator *pricing.Calculator
}

// NewGoOpenAIProvider creates a provider based on the provider name.
// Supported names: "openai", "anthropic", "azure", "azure-openai"
func NewGoOpenAIProvider(name string, cfg config.ProviderConfig) (*GoOpenAIProvider, error) {
	var clientConfig openai.ClientConfig

	switch name {
	case "openai":
		clientConfig = openai.DefaultConfig(cfg.APIKey)
		if cfg.BaseURL != "" {
			clientConfig.BaseURL = cfg.BaseURL
		}
	case "anthropic":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.anthropic.com/v1"
		}
		clientConfig = openai.DefaultAnthropicConfig(cfg.APIKey, baseURL)
	case "azure", "azure-openai":
		clientConfig = openai.DefaultAzureConfig(cfg.APIKey, cfg.BaseURL)
		if cfg.APIVersion != "" {
			clientConfig.APIVersion = cfg.APIVersion
		}
	default:
		return nil, fmt.Errorf("unsupported provider: %s", name)
	}

	return &GoOpenAIProvider{
		client:     openai.NewClientWithConfig(clientConfig),
		name:       name,
		calculator: pricing.NewCalculator(),
	}, nil
}

func (p *GoOpenAIProvider) Name() string {
	return p.name
}

func (p *GoOpenAIProvider) ExecuteChat(ctx context.Context, req openai.ChatCompletionRequest, model string) (openai.ChatCompletionResponse, error) {
	if model != "" {
		req.Model = model
	}
	req.Stream = false

	resp, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return openai.ChatCompletionResponse{}, p.wrapError(err)
	}

	return resp, nil
}

func (p *GoOpenAIProvider) ExecuteChatStream(ctx context.Context, req openai.ChatCompletionRequest, model string) (ChatStream, error) {
	if model != "" {
		req.Model = model
	}

	stream, err := p.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return nil, p.wrapError(err)
	}

	return WrapOpenAIStream(stream), nil
}

func (p *GoOpenAIProvider) CalculateCost(model string, usage openai.Usage) float64 {
	return p.calculator.Calculate(model, usage)
}

func (p *GoOpenAIProvider) HealthCheck(ctx context.Context) error {
	// For OpenAI, list models. For others, just return nil (no easy health check)
	if p.name == "openai" {
		_, err := p.client.ListModels(ctx)
		return err
	}
	return nil
}

func (p *GoOpenAIProvider) wrapError(err error) error {
	// Check if it's an OpenAI API error
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return &ProviderError{
			Code:     apiErr.HTTPStatusCode,
			Body:     apiErr.Message,
			Provider: p.name,
		}
	}
	return err
}
