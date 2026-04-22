package llmgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go.uber.org/zap"
)

type AnthropicClient struct {
	apiKey   string
	endpoint string
	logger   *zap.Logger
	client   *http.Client
}

func NewAnthropicClient(apiKey, endpoint string, logger *zap.Logger) *AnthropicClient {
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/v1"
	}
	return &AnthropicClient{
		apiKey:   apiKey,
		endpoint: endpoint,
		logger:   logger,
		client:   &http.Client{},
	}
}

func (c *AnthropicClient) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	// 转换消息格式
	var messages []map[string]string
	for _, msg := range req.Messages {
		messages = append(messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	anthropicReq := map[string]interface{}{
		"model":       req.Model,
		"messages":    messages,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
	}

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		c.logger.Error("failed to marshal request", zap.Error(err))
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/messages", bytes.NewReader(body))
	if err != nil {
		c.logger.Error("failed to create request", zap.Error(err))
		return nil, err
	}

	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		c.logger.Error("failed to call Anthropic API", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.logger.Error("Anthropic API error", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("Anthropic API error: %d", resp.StatusCode)
	}

	var anthropicResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		c.logger.Error("failed to decode response", zap.Error(err))
		return nil, err
	}

	if len(anthropicResp.Content) == 0 {
		return nil, fmt.Errorf("no content in response")
	}

	result := &CompletionResponse{
		Content: anthropicResp.Content[0].Text,
		Model:   req.Model,
	}
	result.Usage.PromptTokens = anthropicResp.Usage.InputTokens
	result.Usage.CompletionTokens = anthropicResp.Usage.OutputTokens
	result.Usage.TotalTokens = anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens

	c.logger.Info("Anthropic completion success", zap.String("model", req.Model), zap.Int("tokens", result.Usage.TotalTokens))
	return result, nil
}

func (c *AnthropicClient) Health(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+"/models", nil)
	if err != nil {
		return err
	}

	httpReq.Header.Set("x-api-key", c.apiKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Anthropic health check failed: %d", resp.StatusCode)
	}

	return nil
}
