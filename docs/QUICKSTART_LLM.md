# 快速开始：调用大模型

本指南展示如何在 ClawHermes 中快速集成和调用大模型。

## 1. 配置 API Key

编辑 `.env` 文件，添加你的 API Key：

```bash
# OpenAI
OPENAI_API_KEY=sk-your-openai-key

# 或 Anthropic
ANTHROPIC_API_KEY=sk-ant-your-anthropic-key

# 或本地 Ollama
OLLAMA_ENDPOINT=http://localhost:11434
```

## 2. 启动服务

```bash
# 启动依赖服务
make docker-up

# 启动应用
make run
```

## 3. 创建 LLM Skill

```bash
curl -X POST http://localhost:8080/skills \
  -H "Content-Type: application/json" \
  -d '{
    "name": "GPT-4 Assistant",
    "description": "Call GPT-4 for general questions",
    "type": "llm"
  }'
```

响应：
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "GPT-4 Assistant",
  "description": "Call GPT-4 for general questions",
  "type": "llm",
  "created_at": "2026-04-22T22:09:35Z"
}
```

## 4. 执行 LLM Skill

```bash
curl -X POST http://localhost:8080/skills/550e8400-e29b-41d4-a716-446655440000/execute \
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

响应：
```json
{
  "result": {
    "content": "The capital of France is Paris, a city known for its art, culture, and iconic landmarks like the Eiffel Tower.",
    "model": "gpt-4",
    "usage": {
      "prompt_tokens": 10,
      "completion_tokens": 25,
      "total_tokens": 35
    }
  }
}
```

## 5. 使用不同的模型

### OpenAI GPT-3.5

```bash
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "model": "gpt-3.5-turbo",
      "prompt": "Explain quantum computing"
    }
  }'
```

### Anthropic Claude

```bash
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "model": "claude-3-opus",
      "prompt": "Write a poem about AI"
    }
  }'
```

### Ollama 本地模型

```bash
# 先拉取模型
ollama pull llama2

# 然后调用
curl -X POST http://localhost:8080/skills/{skill_id}/execute \
  -H "Content-Type: application/json" \
  -d '{
    "input": {
      "model": "llama2",
      "prompt": "What is machine learning?"
    }
  }'
```

## 6. 代码示例

### Go 代码调用

```go
package main

import (
	"context"
	"log"

	"clawhermes-ai-go/internal/llmgateway"
	"clawhermes-ai-go/internal/skill"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// 初始化 Gateway
	cfg := llmgateway.LoadConfig()
	gateway := llmgateway.InitializeGateway(cfg, logger)

	// 创建 LLM Skill
	llmSkill := skill.NewLLMSkill(
		"my-skill",
		"My LLM Skill",
		"My first LLM skill",
		gateway,
		logger,
	)

	// 执行
	result, err := llmSkill.Execute(map[string]interface{}{
		"model":       "gpt-4",
		"prompt":      "Hello, how are you?",
		"temperature": 0.7,
		"max_tokens":  100,
	})

	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Result: %v", result)
}
```

### Python 调用

```python
import requests
import json

# 创建 Skill
response = requests.post(
    "http://localhost:8080/skills",
    json={
        "name": "Python LLM Skill",
        "description": "Call LLM from Python",
        "type": "llm"
    }
)

skill_id = response.json()["id"]

# 执行 Skill
response = requests.post(
    f"http://localhost:8080/skills/{skill_id}/execute",
    json={
        "input": {
            "model": "gpt-4",
            "prompt": "What is Python?",
            "temperature": 0.7
        }
    }
)

result = response.json()
print(result["result"]["content"])
```

## 常见问题

### Q: 如何切换模型？
A: 在执行 Skill 时，通过 `model` 参数指定不同的模型。Gateway 会自动路由到对应的提供商。

### Q: 支持流式响应吗？
A: 当前版本不支持流式响应，但可以通过扩展 LLMClient 接口实现。

### Q: 如何处理 API 超时？
A: 可以通过 context 设置超时：
```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
resp, err := gateway.Complete(ctx, req)
```

### Q: 如何监控 token 使用量？
A: 响应中包含 `usage` 字段，记录了 prompt_tokens、completion_tokens 和 total_tokens。

### Q: 支持自定义模型吗？
A: 支持。实现 `LLMClient` 接口，然后通过 `gateway.RegisterClient()` 注册。

## 下一步

- 查看 [LLM 集成指南](../docs/LLM_INTEGRATION.md) 了解更多细节
- 查看 [CLAUDE.md](../CLAUDE.md) 了解项目架构
- 查看 [README.md](../README.md) 了解完整功能
