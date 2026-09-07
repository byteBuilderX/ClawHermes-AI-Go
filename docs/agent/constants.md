# 常量规范（业务/配置数字）

> 魔法数字（timeout / TTL / pageSize / topK / chunkSize / poolSize / retries）**禁止内联**。
> 纯 UI 样式数字（spacing、border-radius 等）不在此范围。

## 后端（Go）

| 作用域 | 存放位置 | 命名 |
|--------|----------|------|
| 跨包共享（超时/TTL/分页/重试/Pool） | `pkg/constants/<domain>.go` | `Default*` / `Max*` / `Min*` / `*Timeout` / `*TTL` |
| 包内共享（≥2 个文件使用） | `internal/<pkg>/defaults.go` | 同上，包级 unexported 即可 |
| 单文件内使用 | 原文件 `const` 块 | 同上 |

规则：

- `pkg/constants/` 禁止 import `internal/`（单向依赖）
- 禁止在函数签名 / 结构体字面量中直接写魔法数字

Agent 上下文相关常量位于 `pkg/constants/agent.go`：

| 常量 | 当前值 | 用途 |
|------|--------|------|
| `DefaultAgentContextTokens` | 32768 | 模型窗口未知 + `MaxContextTokens`=0 时的兜底预算（窗口 known 时自动 `0.85×window`，0=自动） |
| `DefaultInitHistoryWindow` | 20 | AgentService 执行时加载的初始历史窗口 |
| `DefaultContextHistoryWindow` | 50 | `BuildContextMessages` 的直接调用兜底窗口 |
| `MemoryBudgetRatio` | 0.3 | memory context 最多占剩余预算的比例 |
| `LoopCompactionRecentGroups` | 3 | 循环压缩时优先保留的最近完整消息组数 |
| `LoopCompactionSafetyRatio` | 0.8 | 触发循环内压缩的预算安全阈值（固定平台默认，不暴露用户配置；2026-08-17 产品裁决） |
| `MaxDelegateDepth` | 2 | 委托深度全局硬上限（clamp 兜底）；per-agent 默认 1 可放宽到 2 |
| `DefaultDelegateMaxDepth` | 1 | per-agent `delegate_max_depth` 0=unset 回落；默认"仅主→子一层" |
| `DefaultDelegateMaxLLMSteps` | 5 | per-agent `delegate_default_max_steps` 0=unset 回落 |
| `MaxDelegateMaxLLMSteps` | 10 | delegate max_steps 参数硬上限（schema maximum + clamp） |
| `DelegateMaxGoalRunes` | 2000 | delegate goal 长度上限 |
| `DelegateSummaryMaxRunes` | 4000 | 子 agent 摘要回传前截断 |
| `DelegateExecutionTimeout` | 3min | 单次 delegate 整体 wall-clock 上限 |

无进展停滞检测（nudge-then-cut）阈值位于 `pkg/constants/agent.go`，判定逻辑在
`internal/agent/application/graph/no_progress.go`（无状态派生：每次 LLM 入口从完整
Messages 重算，不新增回合计数器）：

| 常量 | 当前值 | 用途 |
|------|--------|------|
| `AgentNoProgressNudgeThreshold` | 3 | 同「工具+参数+归一化结果」指纹连续 ≥3 轮：本轮请求尾部注入一次换路提示（给模型转机），只进本轮、不落持久会话 |
| `AgentNoProgressTerminateThreshold` | 4 | 提示后仍重复一轮：连续 run ≥4 即业务终止 `no_progress`（进程内 run 到 4 必已在 3 提示过，无需记账） |
| `AgentNoProgressWindow` | 6 | 振荡停滞回溯窗口（最近 N 个已完成成功工具回合）；窗口 6 + 阈值 3 ⇒ 至少 5 个成功回合才可能命中 |
| `AgentNoProgressOscillationThreshold` | 3 | 振荡触发：窗口内去重指纹数 ∈ [2, 3] 且最高频指纹重复 ≥ 3（A→B→A→B→A→B）；指纹种类更多 = 系统性换路尝试，不算振荡 |

振荡采用 nudge-then-cut：首次命中注入换路提示并记录锚点（`ReActState.NoProgressOscillationResetAt`
= 提示时已完成回合数），此后判定只看锚点后新增回合——模型拿到提示后立即换全新指纹即活，
不因提示前窗口里的旧振荡惯性被误杀；提示后仍在少量指纹间振荡并在锚点后重新累积满窗口才
终止（一次转机，不给第二次）。`NoProgressOscillationResetAt` / `NoProgressOscillationNudged`
是执行内存态（不入 checkpoint，plan/delegate 子循环构造时重置），不属常量范围。

> **死配置声明**：既有 `DefaultStuckThreshold`（=3）、`ReActState.StuckThreshold` /
> `PlanTriggered` / `ReflectionSummary` 是**死配置**——全仓库零读取方，其注释描述的
> 「连续 K 轮无 Output → Reflect → Plan」lazy-planning 流程从未接线。该语义与本次
> no-progress 检测（对已完成工具回合的停滞判定）不同，**不在本改动范围、未接线、未删除**。

## 前端（TypeScript / TSX）

所有行为常量集中在 `web/src/constants/index.ts`，按前缀分组：

```js
// API / 网络
API_DEFAULT_TIMEOUT_MS   AUTH_REGISTER_TIMEOUT_MS
// 分页
DEFAULT_PAGE_SIZE   COMPACT_PAGE_SIZE   PAGE_SIZE_OPTIONS
// MCP
MCP_DEFAULT_TIMEOUT_SEC   MCP_MAX_TIMEOUT_SEC
// Skill
SKILL_DEFAULT_TEMPERATURE   SKILL_DEFAULT_MAX_TOKENS   SKILL_DEFAULT_TIMEOUT_SEC
// Evaluation
EVALUATION_JOB_POLL_INTERVAL_MS   EVALUATION_JOB_MAX_WAIT_MS
// Memory
MEMORY_SEARCH_LIMIT
```

规则：

- 所有页面通过 `import { ... } from '../constants'` 引用，禁止页面内直接硬编码上述数字
- 常量名全大写下划线，值加单位后缀（`_MS` / `_SEC` / `_SIZE`）
