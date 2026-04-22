# LLM 集成指南

ClawHermes AI Go 支持多种大模型调用方式，包括云端 API 和本地部署模型。

## 支持的模型提供商

### 1. OpenAI (GPT-4, GPT-3.5-turbo)

**配置环境变量**：
```bash
export OPENAI_API_KEY=sk-your-api-key
```

**API 调用示例**：
```bash
curl -X POST http://localhost:8080/skills \
  -H "Content-Type: application/json" \
  -d '{
    "name": "GPT-4 Skill",
    "description": "Call GPT-4 model",
    "type": "llm"
  }'

# 获取 skill ID 后，执行 skill
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "model": "gpt-4",
      "prompt": "What is the capital of France?",
      "temperature": 0.7,
      "max_tokens": 100
    }
  }'
```

**响应示例**：
```json
{
  "result": {
    "content": "The capital of France is Paris.",
    "model": "gpt-4",
    "usage": {
      "prompt_tokens": 10,
      "completion_tokens": 8,
      "total_tokens": 18
    }
  }
}
```

### 2. Anthropic (Claude-3-opus, Claude-3-sonnet)

**配置环境变量**：
```bash
export ANTHROPIC_API_KEY=sk-ant-your-api-key
```

**API 调用示例**：
```bash
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "model": "claude-3-opus",
      "prompt": "Explain quantum computing in simple terms",
      "temperature": 0.5,
      "max_tokens": 200
    }
  }'
```

### 3. Ollama (本地开源模型)

**安装 Ollama**：
```bash
# macOS
brew install ollama

# Linux
curl https://ollama.ai/install.sh | sh

# 启动 Ollama 服务
ollama serve
```

**拉取模型**：
```bash
ollama pull llama2
ollama pull mistral
ollama pull neural-chat
```

**配置环境变量**：
```bash
export OLLAMA_ENDPOINT=http://localhost:11434
```

**API 调用示例**：
```bash
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "model": "llama2",
      "prompt": "Write a Python function to calculate factorial",
      "temperature": 0.7
    }
  }'
```

## 混合方案配置

同时支持多个模型提供商：

```bash
# .env 文件
OPENAI_API_KEY=sk-your-openai-key
ANTHROPIC_API_KEY=sk-ant-your-anthropic-key
OLLAMA_ENDPOINT=http://localhost:11434
DEFAULT_LLM_PROVIDER=openai
```

**自动路由示例**：
```bash
# 使用 OpenAI
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -d '{"input": {"model": "gpt-4", "prompt": "..."}}'

# 使用 Claude
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -d '{"input": {"model": "claude-3-opus", "prompt": "..."}}'

# 使用 Ollama
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -d '{"input": {"model": "llama2", "prompt": "..."}}'
```

## 代码集成示例

### 创建 LLM Skill

```go
package main

import (
	"clawhermes-ai-go/internal/llmgateway"
	"clawhermes-ai-go/internal/skill"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	
	// 初始化 LLM Gateway
	cfg := llmgateway.LoadConfig()
	gateway := llmgateway.InitializeGateway(cfg, logger)
	
	// 创建 LLM Skill
	llmSkill := skill.NewLLMSkill(
		"skill-1",
		"GPT-4 Skill",
		"Call GPT-4 model",
		gateway,
		logger,
	)
	
	// 执行 Skill
	result, err := llmSkill.Execute(map[string]interface{}{
		"model":   "gpt-4",
		"prompt":  "What is AI?",
		"temperature": 0.7,
		"max_tokens": 100,
	})
	
	if err != nil {
		logger.Error("execution failed", zap.Error(err))
		return
	}
	
	logger.Info("result", zap.Any("output", result))
}
```

### 自定义 LLM 客户端

```go
package llmgateway

import (
	"context"
	"go.uber.org/zap"
)

type CustomClient struct {
	endpoint string
	logger   *zap.Logger
}

func NewCustomClient(endpoint string, logger *zap.Logger) *CustomClient {
	return &CustomClient{
		endpoint: endpoint,
		logger:   logger,
	}
}

func (c *CustomClient) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	// 实现自定义逻辑
	return nil, nil
}

func (c *CustomClient) Health(ctx context.Context) error {
	// 实现健康检查
	return nil
}

// 注册到 Gateway
gateway := NewGateway()
customClient := NewCustomClient("http://custom-llm:8000", logger)
gateway.RegisterClient("custom", customClient)
```

## 模型对比

| 模型 | 提供商 | 优势 | 成本 | 延迟 |
|------|--------|------|------|------|
| GPT-4 | OpenAI | 最强能力 | 高 | 中 |
| Claude-3-Opus | Anthropic | 长上下文 | 高 | 中 |
| Llama2 | Meta (Ollama) | 开源免费 | 低 | 低 |
| Mistral | Mistral (Ollama) | 快速推理 | 低 | 低 |

## 故障排除

### OpenAI 连接失败

```bash
# 检查 API Key
echo $OPENAI_API_KEY

# 测试连接
curl https://api.openai.com/v1/models \
  -H "Authorization: Bearer $OPENAI_API_KEY"
```

### Ollama 连接失败

```bash
# 检查 Ollama 服务
curl http://localhost:11434/api/tags

# 重启 Ollama
ollama serve
```

### 模型不存在

```bash
# 列出可用模型
ollama list

# 拉取模型
ollama pull llama2
```

## 性能优化

### 1. 连接池

Gateway 自动管理 HTTP 连接池，支持并发请求。

### 2. 超时控制

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

resp, err := gateway.Complete(ctx, req)
```

### 3. 缓存

可在 Skill 层实现缓存：

```go
type CachedLLMSkill struct {
	*LLMSkill
	cache map[string]interface{}
}
```

## 最佳实践

1. **使用环境变量**：不要在代码中硬编码 API Key
2. **错误处理**：总是检查 API 响应错误
3. **超时设置**：为长时间运行的请求设置合理超时
4. **日志记录**：记录所有 API 调用用于调试和监控
5. **成本控制**：监控 token 使用量，设置速率限制
