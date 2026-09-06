# 工具轨迹反思式长期记忆摄取（Trajectory Reflection Memory）

> 状态：implemented（2026-08-21 评审确认；2026-08-21 修订：配置平台化、传输层 NATS 化、PG 队列退役随本特性一并落地）
> 范围：长期记忆链路新增与 fact 提取**并列**的工具轨迹反思链路；任务传输统一收口到 NATS JetStream；PG 退出队列职责。

## 1. 目标与三条纪律

让长期记忆链路把工具调用轨迹当作一等提取素材，同时严格遵守：

1. **原始 tool steps 不直接入库**——只作为反思模型的输入，入库的是结构化记忆条目；
2. **不逐调用写记忆**——任务结束时做一次批量反思；
3. **反思模型负责过滤与摘要**——临时查询、试错、动态临时数据在反思阶段被滤掉。

## 2. 现状与差距（证据）

| 现状 | 证据 |
|---|---|
| 记忆管道只吃 user/assistant 文本 | `internal/agent/application/agent_execution.go` `bufferMemoryTurn` 仅缓冲 `user`/`assistant`，工具轨迹从未进入记忆管道 |
| 工具调用有记录，但都是运行/观测数据 | 图状态 `AllToolCalls` + tool 消息 → `agent_execution_checkpoints`（TTL 回收）→ Opik trace；不是记忆素材 |
| 纯工具批次被主动丢弃 | `internal/memory/application/message_buffer.go` `discardLowValueBatch`：非 tool 内容 <50 runes 直接删除 |
| NATS 富化路径明确排除工具消息 | `internal/agent/infrastructure/persistence/chat_store.go` `memoryOutboxContent`：role 必须为 user/assistant，"system/tool messages are internal signals" |
| 提取任务队列是 PG 表 | `memory_extraction_queue`（`pkg/storage/postgres/tenant_schema.sql`），`internal/memory/infrastructure/persistence/extraction_queue.go` 提供 Enqueue/Dequeue/Claim/Mark 语义 |
| 记忆配置解析分散在 agent 级与平台级两套 | agent 级 `ResourceParamResolver` 仅被 memory 使用：`llm_extractor.go`（`memory.max_facts_per_extraction`/`memory.extraction_prompt`/`memory.extraction_model`）、`injector.go`、`recall_tool.go`（`memory.recall_top_k`）；enricher/superseder/history 已是平台级 `PlatformParamResolver` |
| 已有可复用资产 | `ExtractFacts` 质量链（置信度门禁、per-round cap、supersede、entity 归一化）、`CompleteStructured` 带错重试管线、NATS JetStream/DLQ（`memory.raw`/`memory.enriched`/`memory.dlq`）、`AgentResult.ToolCalls`（含 Input/Output/Error/Duration） |

结论：工具轨迹走一条**独立的 task-end 通道**（链路 B），不塞进现有 chat 文本链路。

## 3. 总体架构：三条链路

### 链路 A · 对话事实提取（现有，传输层改造）

```
user/assistant 消息 → Redis buffer（K=5/8KB/2min，保留）
  → NATS memory.extraction.{tenant}（替换 PG queue）
  → extraction consumer worker
  → LLM 直提（memory.extraction_prompt/model，agent 级）
  → 事实质量门禁（置信度/cap/supersede/entity）
  → memory_entries + memory_facts 入库
```

### 链路 B · 工具轨迹反思（新增，与 A 并列）

```
任务结束图终态（AgentResult.ToolCalls）
  → 轨迹骨架压缩（Go 确定性规则，一次）
  → 反思触发 gate（规则，非 LLM）
  → NATS memory.reflection.{tenant}
  → reflection consumer worker
  → 反思模型 LLM（memory.reflection_prompt/model，agent 级）
  → 反思质量门禁（证据门 + 重要性/来源校验 + supersede）
  → memory_entries + memory_facts 入库（source=trajectory_reflection）
```

### 链路 C · NATS 富化（现有 memory_outbox 路径，平台级配置）

```
chat 消息 → memory_outbox（PG，仅 user/assistant）
  → NATS memory.raw.{tenant} → embedder → memory.enriched.{tenant} → enricher
  → memory_entries/entities/active snapshot/summaries
```

## 4. 链路 B 详细设计

### 4.1 轨迹骨架压缩（确定性 Go，非 LLM）

输入：任务结束时 `AgentResult.ToolCalls`（`domain.ToolCall`：ToolName/Input/Output/Error/Duration）+ `Steps` + `Output` + `Error` + `TerminatedBy`。

输出：`TrajectorySkeleton` 值对象：

```go
type TrajectoryStep struct {
    ToolName         string  `json:"tool_name"`
    ArgsSummary      string  `json:"args_summary,omitempty"` // 截断/脱敏后摘要
    Status           string  `json:"status"`                 // success|error
    ErrorFingerprint string  `json:"error_fingerprint,omitempty"`
    DurationMS       int64   `json:"duration_ms,omitempty"`
}

type TrajectorySkeleton struct {
    ExecutionID   string             `json:"execution_id"`
    TaskGoal      string             `json:"task_goal,omitempty"`      // user query 摘要
    Steps         []TrajectoryStep   `json:"steps"`
    ToolStats     map[string]ToolStat `json:"tool_stats"`               // 按工具聚合
    ResultSummary string             `json:"result_summary,omitempty"` // 最终回答/错误摘要
    TerminatedBy  string             `json:"terminated_by,omitempty"`
}

type ToolStat struct {
    Count, ErrorCount, RetryCount int
}
```

规则（对应知识输入「轨迹三层压缩」）：

- 层 1：工具名 + 参数摘要（只保留可序列化标量，复用 `SafeTracePayload` 截断/脱敏策略）；
- 层 2：按工具聚合统计（次数/error/retry）；
- 层 3：结果交叉验证（`ResultSummary` + `TerminatedBy`）。

**明确丢弃**：原始参数值、工具返回体、动态临时数据、token/密钥/PII（走现有脱敏规则）。skeleton 大小上限 `MemoryReflectionSkeletonMaxBytes`（8KB），超出按工具截断。

### 4.2 反思触发 gate（规则硬编码）

- 触发：`tool_count >= MemoryReflectionMinToolCalls`（初值 3，可调）或 存在 error/retry 或 用户消息含明确记忆指令；
- 不触发：纯问答无工具、单次只读查询且无失败无纠正（过滤临时查询）、超时无工具；
- gate 在任务结束后评估一次，不满足即丢弃，不产生任何写入。

### 4.3 反思模型与提示词

与 fact 提取**不同模型、不同提示词**，均为**平台级**配置（经 `PlatformParamResolver.ResolvePlatform(ctx, key)` 读取，不与 agent 绑定；agent 数量多，无 per-agent 配置必要，统一走通用任务系统参数）：

| 配置项 | 链路 A | 链路 B |
|---|---|---|
| prompt | `memory.extraction_prompt` | `memory.reflection_prompt` |
| model | `memory.extraction_model` | `memory.reflection_model` |
| 数量上限 | `memory.max_facts_per_extraction` | `MemoryReflectionMaxEntries`（常量初值） |
| 占位符 | `{user_id}/{agent_id}/{max_facts}` | `{skeleton}/{task_goal}/{existing_facts}` |
| 失败语义 | 未配置 fail-closed | 同模式 fail-closed |

`memory.recall_top_k`、`memory.inject_*` 等记忆相关参数同样收敛为平台级。反思 prompt 承担三类指令：过滤准则（一次性探索/试错噪声/动态临时数据/可从源码推导的通用知识 → 拒绝）、证据要求（候选必须携带 `execution_id` + ≥1 工具引用）、"从行动中学"的提炼视角（失败教训、工具选择经验、流程洞察）。

输出：`ReflectionEntry`，复用 `ExtractedFact` 形状（content/importance/confidence/fact_type/entities）+ 轨迹 provenance。JSON 契约由 Go 解析器 + 复用 `Validate` 强制，不依赖 prompt 文本。

### 4.4 入库

- 复用 `memory_entries` + `memory_facts`，`source=trajectory_reflection`（扩展 `domain/fact.go` allowlist，纯 Go 改动，无需迁移）；
- provenance 只存 `execution_id`（落 `memory_facts.source_message_id` 列，幂等 writer 身份），**不存原始步骤**；step 区间与工具名集合只存在于（不持久化的）骨架中；
- 与链路 A 的事实共用 supersede 链：来源只是 provenance，不是冲突自动赢家；
- 反思候选同样受 per-round cap 与注入预算约束。

### 4.5 幂等与失败语义

- NATS Msg-Id = `execution_id`，JetStream 去重，重复入队 no-op；
- at-least-once：consumer 失败按 AckWait/MaxDeliver 重投，耗尽走 DLQ（复用 `memory.dlq`）；
- 聊天主路径 fail-open：骨架压缩/入队失败只日志+指标（与 `bufferMemoryTurn` 同模式）；consumer 内持久化失败必须传播；
- 原始轨迹不落盘，反思失败有损可接受，但记录 `execution_id` 便于人工补录。
- 用户显式"记住"指令由 agent 侧关键词检测（`constants.MemoryExplicitRememberKeywords`），随任务 payload 的 `explicit_memory` 进入触发 gate。

## 5. 传输层统一（Redis 保留，PG 退出队列）

### 5.1 Redis 保留的职责

- 消息聚合：K=5 / 8KB / 2min 阈值攒批（NATS 不做聚合）；
- 单飞 flush 锁 + meta 计数 + 值精确清理 + buffer key TTL（`message_buffer.go` 全保留）；
- `discardLowValueBatch` 保留：过滤纯工具/短确认批次（链路 A 语义不变）。

### 5.2 NATS 替换 PG 队列

语义映射：

| 现状（PG `memory_extraction_queue`） | 改后（NATS JetStream） |
|---|---|
| `Enqueue` | `js.Publish` → `memory.extraction.{tenant}`，Msg-Id=message_id |
| `Dequeue`（claim） | per-tenant consumer（filter subject），AckWait + MaxDeliver |
| `MarkCompleted` | Ack |
| `MarkFailed` | Ack + DLQ（现有 `dead_letter.go`） |

链路 B 同样走 NATS：`memory.reflection.{tenant}`，Msg-Id=execution_id。

新增常量（`pkg/constants/memory.go`）：

```go
MemoryExtractionSubject = "memory.extraction"
MemoryReflectionSubject = "memory.reflection"
MemoryReflectionMinToolCalls     = 3
MemoryReflectionSkeletonMaxBytes = 8 * 1024
MemoryReflectionMaxEntries       = 5 // 每任务反思候选上限
ExtractionAckWait                = 60s // extraction consumer ack 预算
ExtractionMaxDeliver             = 3  // 与 PG 队列 retry_count<2（共 3 次尝试）一致
ReflectionAckWait                = 60s
ReflectionMaxDeliver             = 5
```

### 5.3 PG 队列退役

按仓库迁移规则（功能替代旧存储）：

1. Redis buffer flush 改发布到 `memory.extraction.{tenant}`（NATS publisher 实现 `port.ExtractionQueue.Enqueue`）；
2. 提取消费由 pipeline 内 `ExtractionConsumerWorker`（JetStream consumer）承担，取代 PG Dequeue/Mark 语义；
3. Go 引用清零：`memory_extraction_queue` 的 repo/worker/port 全部移除（GC worker 不再清理队列）；
4. `tenant_schema.sql` 移除 `memory_extraction_queue` DDL，替换为存量租户幂等清理语句；
5. `pkg/migration/sql/042_memory_pg_queue_retirement` 为 public 标记迁移（down 保留数据策略）。

## 6. 记忆配置统一平台化（取消 agent 绑定）

现状：agent 级 `ResourceParamResolver` 仅被 memory 管线使用（extractor / injector / recall）；enricher / superseder / history 已是平台级。

改后：

- `llm_extractor.go` 的 `memory.max_facts_per_extraction` / `memory.extraction_prompt` / `memory.extraction_model`、`injector.go` 的注入参数、`recall_tool.go` 的 `memory.recall_top_k` 全部改为 `PlatformParamResolver.ResolvePlatform(ctx, key)`；
- 删除 memory 侧的 `agentResourceParamResolver` / `memoryResourceParamResolver` 装配点；引用清零后删除 `ResourceParamResolver` port；
- 链路 B 的 `memory.reflection_prompt` / `memory.reflection_model` 从第一天就是平台级，无 agent 绑定；
- 平台参数源是现有 `c.Parameters` 注册表（Nacos 桥接热更新），天然适配"通用的任务系统参数"；
- 工具调用对**不进** outbox（保持 user/assistant 语义，不改 `memoryOutboxContent`）。

注意：记忆**配置**平台化不影响记忆**数据**的 scope——条目仍按 user/agent scope 存取与注入，agentID 继续出现在任务 payload 与 provenance 中，只是不再用于解析模型/提示词。

## 7. 分层接口（DDD 合规）

消费方 port（`internal/agent/domain/port/memory.go`）：

```go
// EnqueueTrajectoryReflectionFn 任务结束时把工具调用摘要异步入队反思。
type EnqueueTrajectoryReflectionFn func(
    ctx context.Context, tenantID, userID, agentID, conversationID, scope, executionID string,
    taskGoal, resultSummary, terminatedBy string,
    calls []TrajectoryToolCallVO,
    explicitMemory bool,
) error
```

`internal/memory/domain/port` 新增：

- `TrajectorySkeleton` / `TrajectoryStep` / `ToolStat`（domain 值对象，校验方法）；
- `TrajectoryReflector`：`Reflect(ctx, tenantID string, skeleton TrajectorySkeleton, existing string) ([]ReflectionEntry, error)`（配置平台级，无 agentID；agentID 仍在任务 payload 中）；
- `ReflectionEntry`：`ExtractedFact` 内嵌 + `Evidence{ExecutionID string; StepRange [2]int; ToolNames []string}`；
- 队列 port 复用现有 `Publisher`（`publish.go`），不再新增 PG queue port。

`internal/memory/application` 新增：

- `ReflectionService`（`MemoryService.ReflectAndPersist`）：编排骨架校验 → gate → 反思 → 证据门 + Validate + supersede + cap → 复用事实持久化链；
- `SkeletonBuilder`（纯函数，输入 `AgentResult` 工具调用，输出骨架；单测覆盖截断/脱敏/统计）。

`internal/memory/infrastructure` 新增：

- `trajectory_reflector.go`：LLM 实现，复用 `CompleteStructured` 带错重试管线；
- `reflection_worker.go`：JetStream consumer，复用 embedder/enricher 消费模式；
- `extraction_publisher.go`：NATS publisher 替代 PG `Enqueue`。

`api/wiring` 新增：`makeTrajectoryReflectorResolver`（复用 `memoryPlatformParamResolver`）、`trajectoryReflectionClosure`、`attachMemoryTaskPublishers`（NATS 发布器 + consumer service 装配）；删除 memory 侧 agent resolver 装配点。

## 8. 不做的事

- 不做原始轨迹持久化/回放（checkpoint 的 TTL 运行快照不升级为记忆）；
- 不改 `memoryOutboxContent`，工具对不进 outbox/富化链路；
- 不做 skill 自动修改（属 self-evolve 范畴）；
- Phase 1 只接 agent 图执行终态；workflow/evaluation 轨迹留扩展点（`TerminatedBy`/`RunType` 字段已具备区分能力）。

## 9. 风险与验收

风险与对策：

- 反思 LLM 输出漂移 → 全部走确定性校验（Validate 枚举/区间/长度 + 证据门），坏输出 Ack + DLQ，不降级；
- 记忆污染 → 触发 gate + 过滤准则 + importance/confidence 阈值 + supersede 链；
- 敏感泄漏 → 骨架压缩复用脱敏策略，provenance 不含原始步骤；
- 队列切换风险 → 双跑验证 + 存量清理单独评审；
- 配置平台化回归 → extractor/injector/recall 切平台解析后，行为等价性由现有参数解析单测 + 双跑观测守护。

验收（`stratum-e2e-development` skill 主导）：

1. 工具失败教训被记住（构造失败任务，反思后事实可召回）；
2. 临时查询被过滤（单次只读查询不产生记忆）；
3. 原始步骤不入库（skeleton/事实中无工具返回原文）；
4. 平台配置生效（修改平台参数后所有 agent 生效，无 agent 级覆盖）；
5. PG queue 零引用（`rg memory_extraction_queue` 无 Go 命中，tenant_schema 无 DDL）。

## 10. 分阶段实施

- **Phase 0（已完成）**：骨架压缩器 + 触发 gate + NATS 传输（extraction/reflection subject）+ 反思 worker/模型 + source 枚举扩展 + 单测；
- **Phase 1（已完成）**：记忆配置平台化收敛（extractor/injector/recall 切 `ResolvePlatform`，删除 `ResourceParamResolver` port 与装配点）；
- **Phase 2（已完成）**：PG `memory_extraction_queue` 退役（Go 引用清零 → tenant_schema 清理 → 042 标记迁移）；
- **Phase 3（进行中）**：端到端系统验收（skill 主导）+ CI/CD。
