package llmgateway

import (
	"os"

	"go.uber.org/zap"
)

type Config struct {
	OpenAIKey      string
	AnthropicKey   string
	OllamaEndpoint string
	DefaultProvider ModelProvider
}

func LoadConfig() *Config {
	return &Config{
		OpenAIKey:       os.Getenv("OPENAI_API_KEY"),
		AnthropicKey:    os.Getenv("ANTHROPIC_API_KEY"),
		OllamaEndpoint:  os.Getenv("OLLAMA_ENDPOINT"),
		DefaultProvider: ModelProvider(os.Getenv("DEFAULT_LLM_PROVIDER")),
	}
}

func InitializeGateway(cfg *Config, logger *zap.Logger) *Gateway {
	gateway := NewGateway()

	// 注册 OpenAI 客户端
	if cfg.OpenAIKey != "" {
		openaiClient := NewOpenAIClient(cfg.OpenAIKey, "", logger)
		gateway.RegisterClient(ProviderOpenAI, openaiClient)
	}

	// 注册 Anthropic 客户端
	if cfg.AnthropicKey != "" {
		anthropicClient := NewAnthropicClient(cfg.AnthropicKey, "", logger)
		gateway.RegisterClient(ProviderClaude, anthropicClient)
	}

	// 注册 Ollama 客户端
	if cfg.OllamaEndpoint != "" {
		ollamaClient := NewOllamaClient(cfg.OllamaEndpoint, logger)
		gateway.RegisterClient(ProviderOllama, ollamaClient)
	}

	// 设置默认 provider
	if cfg.DefaultProvider != "" {
		gateway.SetDefault(cfg.DefaultProvider)
	} else {
		gateway.SetDefault(ProviderOpenAI)
	}

	return gateway
}
