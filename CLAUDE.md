# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

ClawHermes AI Go 是面向企业私有化部署的 AI 应用编排平台，融合 OpenClaw Skill 原子化架构、Hermes 事件驱动异步通信、Harness AI 可观测与灰度发布、MCP 统一工具/模型协议、GraphRAG 知识增强。

## 技术栈

- **语言**: Go 1.22+
- **API 网关**: Gin
- **事件总线**: NATS
- **向量数据库**: Milvus
- **图数据库**: Neo4j
- **日志**: Uber Zap
- **配置**: Spf13 Viper
- **可观测**: 结构化日志、指标收集、链路追踪

## 架构分层

1. **Portal 接入层** (`api/`) - HTTP API 入口，基于 Gin
2. **Hermes 事件总线** (`internal/hermes/`) - NATS 事件驱动通信
3. **Orchestrator Skill 编排** (`internal/orchestrator/`) - Skill 工作流编排与注册
4. **Skill Runtime 执行环境** (`internal/skill/`) - Skill 原子化执行引擎
5. **GraphRAG 知识记忆** (`internal/knowledge/`) - Neo4j 知识图谱增强检索
6. **LLM Gateway** (`internal/llmgateway/`) - LLM 网关与 MCP 协议
7. **可观测** (`pkg/observability/`) - 日志、指标、链路追踪

## 目录结构

```
cmd/server/              - 应用入口
api/                     - HTTP API 层
  ├── router.go          - 路由定义
  ├── model/             - 请求/响应模型
  ├── handler/           - 请求处理器
  └── middleware/        - 中间件（CORS、错误处理、指标）
internal/                - 内部业务逻辑
  ├── config/            - 配置管理
  ├── hermes/            - NATS 事件总线客户端
  ├── skill/             - Skill 定义与执行
  │   ├── skill.go       - Skill 接口定义
  │   ├── code_skill.go  - CodeSkill 实现
  │   └── executor.go    - Skill 执行器
  ├── orchestrator/      - Skill 编排与注册
  ├── llmgateway/        - LLM 网关
  └── knowledge/         - GraphRAG 知识管理
pkg/                     - 公共库
  ├── mcp/               - MCP 协议类型定义
  └── observability/     - 日志、指标、链路追踪
```

## 常用命令

```bash
# 构建
make build

# 运行
make run

# 一键启动（推荐）
./start.sh

# 停止服务
./stop.sh

# 测试
make test

# 测试覆盖率
make test-coverage

# Lint
make lint

# 格式化
make fmt

# 静态检查
make vet

# Docker 环境启动
make docker-up

# Docker 环境停止
make docker-down

# 查看 Docker 日志
make docker-logs

# 清理构建产物
make clean
```

## 核心概念

### Skill 系统

- **Skill 接口**: 定义 Skill 的基本属性（ID、名称、描述、类型）
- **BaseSkill**: Skill 的基础实现
- **CodeSkill**: 支持代码执行的 Skill 子类
- **SkillExecutor**: 定义 Skill 执行方法的接口
- **Executor**: Skill 执行引擎，支持超时控制和并发执行

### Hermes 事件总线

- **Event**: 事件结构，包含类型、时间戳、数据、来源
- **EventHandler**: 事件处理函数
- **Client**: NATS 客户端，支持事件发布/订阅

### API 层

- **CreateSkillRequest**: 创建 Skill 请求
- **SkillResponse**: Skill 响应
- **ExecuteSkillRequest**: 执行 Skill 请求
- **ExecuteSkillResponse**: 执行结果响应
- **ErrorResponse**: 错误响应

### 可观测

- **Logger**: 结构化日志（生产/开发环境）
- **Tracer**: 链路追踪
- **Metrics**: 指标收集（Skill 执行、API 请求、事件发布）

## API 端点

```
POST   /skills              - 创建 Skill
GET    /skills/:id          - 获取 Skill 信息
POST   /skills/:id/execute  - 执行 Skill
GET    /health              - 健康检查
```

## 环境配置

复制 `.env.example` 为 `.env`，配置以下变量：

```
PORT=8080                                    # 服务端口
NATS_URL=nats://localhost:4222              # NATS 服务地址
MILVUS_HOST=localhost                       # Milvus 主机
MILVUS_PORT=19530                           # Milvus 端口
NEO4J_URI=bolt://localhost:7687             # Neo4j 连接 URI
NEO4J_USER=neo4j                            # Neo4j 用户名
NEO4J_PASSWORD=password                     # Neo4j 密码
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317  # OTEL 收集器地址
```

## 依赖集成

项目已集成以下底层服务，通过 Docker Compose 自动启动：

### NATS (事件总线)
- **文件**: `internal/hermes/client.go`
- **功能**: 异步事件驱动通信
- **端口**: 4222
- **持久化**: JetStream 模式，数据存储在 `nats_data` 卷
- **使用**:
  ```go
  client, err := hermes.NewClient(cfg.NatsURL, logger)
  client.Publish(event)
  client.Subscribe("event.type", handler)
  ```

### Neo4j (图数据库)
- **文件**: `internal/knowledge/graphrag.go`
- **功能**: 知识图谱存储和查询
- **端口**: 7687 (Bolt), 7474 (HTTP)
- **持久化**: 数据存储在 `neo4j_data` 卷，日志在 `neo4j_logs` 卷
- **使用**:
  ```go
  graphrag := knowledge.NewGraphRAG(uri, user, password, logger)
  graphrag.Connect(ctx)
  graphrag.CreateNode(ctx, "Skill", properties)
  ```

### Milvus (向量数据库)
- **文件**: `pkg/mcp/vector_store.go`
- **功能**: 向量存储和相似度搜索
- **端口**: 19530
- **持久化**: 元数据存储在 etcd (`etcd_data` 卷)，向量数据存储在 MinIO (`minio_data` 卷)
- **使用**:
  ```go
  vectorStore := mcp.NewVectorStore(host, port, logger)
  vectorStore.Connect(ctx)
  vectorStore.Insert(ctx, "collection", vectors)
  vectorStore.Search(ctx, "collection", query, topK)
  ```

### OpenTelemetry (可观测)
- **文件**: `pkg/observability/logger.go`
- **功能**: 日志、指标、链路追踪
- **端口**: 4317
- **配置**: `otel-collector-config.yaml`
- **使用**:
  ```go
  logger, _ := observability.NewLogger("production")
  metrics := observability.NewMetrics(logger)
  metrics.RecordSkillExecution(skillID, duration, success)
  ```

详见 [依赖集成指南](docs/DEPENDENCIES.md) 和 [数据持久化指南](docs/DATA_PERSISTENCE.md)

## 开发指南

### 添加新的 Skill

1. 在 `internal/skill/` 中创建新的 Skill 类型
2. 实现 `Skill` 接口（或继承 `BaseSkill`）
3. 如果需要执行，实现 `SkillExecutor` 接口
4. 在 API 中注册 Skill

### 发布事件

```go
event := &hermes.Event{
    Type:      "skill.executed",
    Timestamp: time.Now().Unix(),
    Data:      result,
    Source:    "skill-executor",
}
hermesClient.Publish(event)
```

### 订阅事件

```go
hermesClient.Subscribe("skill.executed", func(event *hermes.Event) error {
    // 处理事件
    return nil
})
```

## 多租户与部署

- 支持多租户隔离
- 私有化部署能力
- Skill 插件市场
- AI 成本治理与灰度发布
- 安全合规支持
