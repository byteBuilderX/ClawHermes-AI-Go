package llmgateway

import (
	"context"
	"fmt"
)

type ModelProvider string

const (
	ProviderOpenAI ModelProvider = "openai"
	ProviderClaude ModelProvider = "claude"
	ProviderGemini ModelProvider = "gemini"
	ProviderOllama ModelProvider = "ollama"
	ProviderLLaMA  ModelProvider = "llama"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float32   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	TopP        float32   `json:"top_p,omitempty"`
}

type CompletionResponse struct {
	Content string `json:"content"`
	Model   string `json:"model"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type LLMClient interface {
	Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
	Health(ctx context.Context) error
}

type Gateway struct {
	clients         map[ModelProvider]LLMClient
	defaultProvider ModelProvider
}

func NewGateway() *Gateway {
	return &Gateway{
		clients: make(map[ModelProvider]LLMClient),
	}
}

func (g *Gateway) RegisterClient(provider ModelProvider, client LLMClient) {
	g.clients[provider] = client
}

func (g *Gateway) SetDefault(provider ModelProvider) {
	g.defaultProvider = provider
}

func (g *Gateway) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	provider := g.defaultProvider
	if req.Model != "" {
		// 从 model 字符串中提取 provider
		provider = g.parseProvider(req.Model)
	}

	client, ok := g.clients[provider]
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", provider)
	}

	return client.Complete(ctx, req)
}

func (g *Gateway) Health(ctx context.Context) error {
	for provider, client := range g.clients {
		if err := client.Health(ctx); err != nil {
			return fmt.Errorf("provider %s health check failed: %w", provider, err)
		}
	}
	return nil
}

func (g *Gateway) parseProvider(model string) ModelProvider {
	switch model {
	case "gpt-4", "gpt-3.5-turbo":
		return ProviderOpenAI
	case "claude-3-opus", "claude-3-sonnet":
		return ProviderClaude
	case "gemini-pro":
		return ProviderGemini
	case "ollama":
		return ProviderOllama
	default:
		return g.defaultProvider
	}
}
