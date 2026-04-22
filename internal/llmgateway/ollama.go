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

type OllamaClient struct {
	endpoint string
	logger   *zap.Logger
	client   *http.Client
}

func NewOllamaClient(endpoint string, logger *zap.Logger) *OllamaClient {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	return &OllamaClient{
		endpoint: endpoint,
		logger:   logger,
		client:   &http.Client{},
	}
}

func (c *OllamaClient) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	ollamaReq := map[string]interface{}{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   false,
	}

	if req.Temperature > 0 {
		ollamaReq["temperature"] = req.Temperature
	}

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		c.logger.Error("failed to marshal request", zap.Error(err))
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		c.logger.Error("failed to create request", zap.Error(err))
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		c.logger.Error("failed to call Ollama API", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.logger.Error("Ollama API error", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("Ollama API error: %d", resp.StatusCode)
	}

	var ollamaResp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Model string `json:"model"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		c.logger.Error("failed to decode response", zap.Error(err))
		return nil, err
	}

	result := &CompletionResponse{
		Content: ollamaResp.Message.Content,
		Model:   ollamaResp.Model,
	}

	c.logger.Info("Ollama completion success", zap.String("model", req.Model))
	return result, nil
}

func (c *OllamaClient) Health(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+"/api/tags", nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama health check failed: %d", resp.StatusCode)
	}

	return nil
}
