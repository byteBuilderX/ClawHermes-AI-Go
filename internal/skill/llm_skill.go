package skill

import (
	"context"
	"fmt"

	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/llmgateway"
	"go.uber.org/zap"
)

type LLMSkill struct {
	*BaseSkill
	gateway *llmgateway.Gateway
	logger  *zap.Logger
}

func NewLLMSkill(id, name, description string, gateway *llmgateway.Gateway, logger *zap.Logger) *LLMSkill {
	return &LLMSkill{
		BaseSkill: &BaseSkill{
			ID:          id,
			Name:        name,
			Description: description,
			Type:        "llm",
		},
		gateway: gateway,
		logger:  logger,
	}
}

func (ls *LLMSkill) Execute(input interface{}) (interface{}, error) {
	ctx := context.Background()

	// 解析输入
	inputMap, ok := input.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid input format")
	}

	model, ok := inputMap["model"].(string)
	if !ok {
		return nil, fmt.Errorf("model not specified")
	}

	prompt, ok := inputMap["prompt"].(string)
	if !ok {
		return nil, fmt.Errorf("prompt not specified")
	}

	// 构建请求
	req := &llmgateway.CompletionRequest{
		Model: model,
		Messages: []llmgateway.Message{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	// 设置可选参数
	if temp, ok := inputMap["temperature"].(float32); ok {
		req.Temperature = temp
	}
	if maxTokens, ok := inputMap["max_tokens"].(int); ok {
		req.MaxTokens = maxTokens
	}

	// 调用 LLM
	resp, err := ls.gateway.Complete(ctx, req)
	if err != nil {
		ls.logger.Error("LLM call failed", zap.Error(err))
		return nil, err
	}

	ls.logger.Info("LLM call success", zap.String("model", model), zap.Int("tokens", resp.Usage.TotalTokens))

	return map[string]interface{}{
		"content": resp.Content,
		"model":   resp.Model,
		"usage": map[string]int{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		},
	}, nil
}
