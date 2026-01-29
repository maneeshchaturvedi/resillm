package pricing

import (
	openai "github.com/sashabaranov/go-openai"
)

// ModelPricing holds pricing per 1M tokens for a model
type ModelPricing struct {
	InputPer1M  float64
	OutputPer1M float64
}

// Calculator calculates costs for LLM API usage
type Calculator struct {
	prices         map[string]ModelPricing
	defaultPricing ModelPricing
}

// NewCalculator creates a new pricing calculator with default pricing
func NewCalculator() *Calculator {
	return &Calculator{
		prices: map[string]ModelPricing{
			// OpenAI models
			"gpt-4o":            {2.50, 10.00},
			"gpt-4o-mini":       {0.15, 0.60},
			"gpt-4-turbo":       {10.00, 30.00},
			"gpt-4":             {30.00, 60.00},
			"gpt-3.5-turbo":     {0.50, 1.50},
			"gpt-4o-2024-08-06": {2.50, 10.00},
			// Anthropic models
			"claude-3-5-sonnet-20241022": {3.00, 15.00},
			"claude-sonnet-4-20250514":   {3.00, 15.00},
			"claude-3-5-haiku-20241022":  {0.80, 4.00},
			"claude-3-opus-20240229":     {15.00, 75.00},
			"claude-3-sonnet-20240229":   {3.00, 15.00},
			"claude-3-haiku-20240307":    {0.25, 1.25},
		},
		defaultPricing: ModelPricing{2.50, 10.00},
	}
}

// NewCalculatorWithPricing creates a calculator with custom pricing
func NewCalculatorWithPricing(prices map[string]ModelPricing, defaultPricing ModelPricing) *Calculator {
	return &Calculator{
		prices:         prices,
		defaultPricing: defaultPricing,
	}
}

// Calculate returns the cost in USD for the given model and usage
func (c *Calculator) Calculate(model string, usage openai.Usage) float64 {
	pricing, ok := c.prices[model]
	if !ok {
		pricing = c.defaultPricing
	}

	inputCost := float64(usage.PromptTokens) / 1_000_000 * pricing.InputPer1M
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * pricing.OutputPer1M

	return inputCost + outputCost
}

// GetPricing returns the pricing for a model, or default if not found
func (c *Calculator) GetPricing(model string) ModelPricing {
	if pricing, ok := c.prices[model]; ok {
		return pricing
	}
	return c.defaultPricing
}

// SetPricing sets the pricing for a model
func (c *Calculator) SetPricing(model string, pricing ModelPricing) {
	c.prices[model] = pricing
}

// ListModels returns all models with configured pricing
func (c *Calculator) ListModels() []string {
	models := make([]string, 0, len(c.prices))
	for model := range c.prices {
		models = append(models, model)
	}
	return models
}
