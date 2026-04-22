# ClawHermes AI Go

企业级 AI 原生应用架构底座 | AI Native Application Framework

## 定位

面向企业私有化部署的 AI 应用编排平台，融合：
- OpenClaw Skill 原子化架构
- Hermes 事件驱动异步通信
- Harness AI 可观测与灰度发布
- MCP 统一工具/模型协议
- GraphRAG 知识增强

## 技术栈

| 组件 | 技术 | 用途 |
|------|------|------|
| 语言 | Go 1.22+ | 高性能后端 |
| API 网关 | Gin | HTTP 服务框架 |
| 事件总线 | NATS | 异步事件驱动 |
| 向量数据库 | Milvus | 向量存储与检索 |
| 图数据库 | Neo4j | 知识图谱存储 |
| 日志 | Uber Zap | 结构化日志 |
| 配置 | Spf13 Viper | 配置管理 |
| 可观测 | 结构化日志 + 指标 | 链路追踪与监控 |

## 架构分层

```
┌─────────────────────────────────────────┐
│  Portal 接入层 (Gin HTTP API)           │
├─────────────────────────────────────────┤
│  Hermes 事件总线 (NATS)                 │
├─────────────────────────────────────────┤
│  Orchestrator Skill 编排 (Registry)     │
├─────────────────────────────────────────┤
│  Skill Runtime 执行环境 (Executor)      │
├─────────────────────────────────────────┤
│  GraphRAG 知识记忆 (Neo4j + Milvus)     │
├─────────────────────────────────────────┤
│  LLM Gateway + MCP                      │
├─────────────────────────────────────────┤
│  Harness AI 运维治理 (可观测)            │
└─────────────────────────────────────────┘
```

## 项目结构

```
clawhermes-ai-go/
├── cmd/server/              # 应用入口
├── api/                     # HTTP API 层
│   ├── router.go            # 路由定义
│   ├── model/               # 请求/响应模型
│   ├── handler/             # 请求处理器
│   └── middleware/          # 中间件
├── internal/                # 内部业务逻辑
│   ├── config/              # 配置管理
│   ├── hermes/              # NATS 事件总线
│   ├── skill/               # Skill 定义与执行
│   ├── orchestrator/        # Skill 编排与注册
│   ├── llmgateway/          # LLM 网关
│   └── knowledge/           # GraphRAG 知识管理
├── pkg/                     # 公共库
│   ├── mcp/                 # MCP 协议
│   └── observability/       # 日志、指标、链路追踪
├── go.mod                   # Go 模块定义
├── go.sum                   # 依赖校验和
├── Makefile                 # 构建脚本
├── docker-compose.yml       # 容器编排
├── .env.example             # 环境变量示例
├── start.sh                 # 启动脚本
├── stop.sh                  # 停止脚本
├── CLAUDE.md                # Claude Code 开发指南
└── docs/                    # 文档
    ├── LLM_INTEGRATION.md   # LLM 集成指南
    ├── QUICKSTART_LLM.md    # LLM 快速开始
    └── DEPENDENCIES.md      # 依赖集成指南
```

## 快速启动

### 前置要求

- Go 1.22+
- Docker & Docker Compose
- Make

### 1. 克隆项目

```bash
git clone https://github.com/clawhermes/clawhermes-ai-go.git
cd clawhermes-ai-go
```

### 2. 配置环境

```bash
cp .env.example .env
```

### 3. 一键启动（推荐）

```bash
# 启动所有服务（包括依赖和应用）
./start.sh
```

或者手动启动：

```bash
# 启动依赖服务
make docker-up

# 运行应用
make run
```

### 4. 验证健康状态

```bash
curl http://localhost:8080/health
# 响应: {"status":"ok"}
```

### 5. 停止服务

```bash
# 停止所有服务
./stop.sh

# 或手动停止
make docker-down
```

## API 端点

### 创建 Skill

```bash
POST /skills
Content-Type: application/json

{
  "name": "Python Calculator",
  "description": "A simple calculator skill",
  "type": "code",
  "code": "def add(a, b): return a + b",
  "language": "python"
}
```

响应：
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "Python Calculator",
  "description": "A simple calculator skill",
  "type": "code",
  "created_at": "2026-04-22T22:09:35Z"
}
```

### 获取 Skill 信息

```bash
GET /skills/{id}
```

响应：
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "Python Calculator",
  "description": "A simple calculator skill",
  "type": "code",
  "created_at": "2026-04-22T22:09:35Z"
}
```

### 执行 Skill

```bash
POST /skills/{id}/execute
Content-Type: application/json

{
  "input": {"a": 5, "b": 3}
}
```

响应：
```json
{
  "result": 8,
  "error": ""
}
```

### 健康检查

```bash
GET /health
```

响应：
```json
{
  "status": "ok"
}
```

## 常用命令

```bash
# 构建
make build

# 运行
make run

# 测试
make test

# 测试覆盖率
make test-coverage

# 代码格式化
make fmt

# 静态检查
make vet

# Lint 检查
make lint

# Docker 启动
make docker-up

# Docker 停止
make docker-down

# 查看 Docker 日志
make docker-logs

# 清理构建产物
make clean
```

## 核心概念

### Skill 系统

Skill 是 ClawHermes 的原子化能力单元，支持三种类型：

- **Builtin Skill**: 内置能力，直接实现业务逻辑
- **Code Skill**: 代码执行能力，支持 Python、JavaScript 等语言
- **LLM Skill**: 大模型调用能力，支持 OpenAI、Claude、Ollama 等

```go
// 创建 Skill
skill := skill.NewCodeSkill(
    "skill-1",
    "Calculator",
    "A simple calculator",
    "def add(a, b): return a + b",
    "python",
)

// 执行 Skill
executor := skill.NewExecutor(registry)
result := executor.Execute(skill.ExecutionContext{
    SkillID: "skill-1",
    Input:   map[string]interface{}{"a": 5, "b": 3},
    Timeout: 30 * time.Second,
})
```

### Hermes 事件总线

基于 NATS 的事件驱动异步通信框架：

```go
// 发布事件
event := &hermes.Event{
    Type:      "skill.executed",
    Timestamp: time.Now().Unix(),
    Data:      result,
    Source:    "skill-executor",
}
hermesClient.Publish(event)

// 订阅事件
hermesClient.Subscribe("skill.executed", func(event *hermes.Event) error {
    log.Printf("Skill executed: %v", event.Data)
    return nil
})
```

### Orchestrator 编排

Skill 注册与管理：

```go
registry := orchestrator.NewRegistry()

// 注册 Skill
registry.Register(skill.GetID(), skill)

// 查询 Skill
skill, ok := registry.Get(skillID)
```

### 可观测

结构化日志、指标收集、链路追踪：

```go
// 创建 Logger
logger, _ := observability.NewLogger("production")

// 记录指标
metrics := observability.NewMetrics(logger)
metrics.RecordSkillExecution("skill-1", 123.45, true)
metrics.RecordAPIRequest("POST", "/skills", 201, 45.67)
```

### LLM Gateway

支持多种大模型提供商的统一网关：

```go
// 初始化 Gateway
cfg := llmgateway.LoadConfig()
gateway := llmgateway.InitializeGateway(cfg, logger)

// 创建 LLM Skill
llmSkill := skill.NewLLMSkill("skill-1", "GPT-4", "Call GPT-4", gateway, logger)

// 执行 Skill
result, err := llmSkill.Execute(map[string]interface{}{
    "model":   "gpt-4",
    "prompt":  "What is AI?",
    "temperature": 0.7,
})
```

**支持的模型提供商**：
- OpenAI (GPT-4, GPT-3.5-turbo)
- Anthropic (Claude-3-opus, Claude-3-sonnet)
- Ollama (Llama2, Mistral, Neural-chat 等开源模型)

## 环境配置

编辑 `.env` 文件配置以下变量：

```env
# 服务配置
PORT=8080

# NATS 配置
NATS_URL=nats://localhost:4222

# Milvus 配置
MILVUS_HOST=localhost
MILVUS_PORT=19530

# Neo4j 配置
NEO4J_URI=bolt://localhost:7687
NEO4J_USER=neo4j
NEO4J_PASSWORD=password

# OpenTelemetry 配置
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317

# LLM 配置
OPENAI_API_KEY=sk-your-openai-key
ANTHROPIC_API_KEY=sk-ant-your-anthropic-key
OLLAMA_ENDPOINT=http://localhost:11434
DEFAULT_LLM_PROVIDER=openai
```

详见 [LLM 集成指南](docs/LLM_INTEGRATION.md)

## 开发指南

### 添加新的 Skill

1. 在 `internal/skill/` 中创建新的 Skill 类型
2. 实现 `Skill` 接口或继承 `BaseSkill`
3. 如需执行，实现 `SkillExecutor` 接口
4. 在 API 中注册 Skill

### 运行测试

```bash
# 运行所有测试
make test

# 运行特定包的测试
go test -v ./internal/skill

# 生成覆盖率报告
make test-coverage
```

### 代码风格

- 遵循 Go 官方代码风格指南
- 使用 `make fmt` 格式化代码
- 使用 `make vet` 进行静态检查
- 使用 `make lint` 进行 Lint 检查

## 商业化能力

- ✅ 多租户隔离
- ✅ 私有化部署
- 🔄 Skill 插件市场
- 🔄 AI 成本治理
- 🔄 灰度发布
- 🔄 安全合规

## 依赖管理

项目使用 Go Modules 管理依赖：

```bash
# 添加新依赖
go get github.com/package/name

# 更新依赖
go get -u github.com/package/name

# 清理未使用的依赖
go mod tidy

# 下载所有依赖
go mod download
```

## 故障排除

### 端口被占用

如果 8080 端口被占用，修改 `.env` 中的 `PORT` 变量：

```env
PORT=8081
```

### NATS 连接失败

确保 Docker 容器正在运行：

```bash
docker-compose ps
```

如果容器未运行，执行：

```bash
make docker-up
```

### 数据库连接失败

检查 Neo4j 和 Milvus 的连接配置：

```bash
# 查看容器日志
make docker-logs
```

## 许可证

Apache License 2.0 - 详见 [LICENSE](LICENSE)

## 贡献指南

欢迎提交 Issue 和 Pull Request！

## 联系方式

- 📧 Email: [contact@clawhermes.com](mailto:contact@clawhermes.com)
- 🐙 GitHub: [clawhermes/clawhermes-ai-go](https://github.com/clawhermes/clawhermes-ai-go)
