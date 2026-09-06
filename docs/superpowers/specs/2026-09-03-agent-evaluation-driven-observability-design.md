# Agent 运行指标与监控设计（评测判定驱动）：证据保真 · 结论导出 · 能力健康

> 内部卡：agent 运行观测指标「以评测判定为优先级锚点」设计（讨论收敛稿）
> 类型：**设计 spec**（现状 reality 映射 + 优先级模型 + 四块设计 + 分期落地；实现待后续 writing-plan 逐任务落地）
> 日期：2026-09-03
> 状态：设计（待 review）
> 前置文档 / 事实源：
>
> - `docs/superpowers/specs/2026-08-28-evaluation-metrics-design.md`（下称「主 spec」；评测判定指标体系与 §11 能力健康指标为本次消费端）
> - `docs/superpowers/specs/2026-09-03-evaluation-layered-gates-design.md`（下称「门禁 spec」；§8 L1-L3+ 分层门禁落地，P1 结论导出的上游）
> - `docs/agent/observability.md`（观测权威：三支柱 + Opik 证据专线、采样、本地/远端分离）
> - `docs/agent/agent.md`（Agent 执行模型：ReasoningEffort 成本语义、上下文预算、MCP 审批）

## 0. 结论先行

### 0.1 核心论点

**评测判定是 Agent 运行指标的第一消费者；评测判定消费的是「证据链」（Opik trace/span re-pull + NATS 观测事件 + rule/behavior 信号），不是 Prometheus 指标。**

因此「按评测需求构建 agent 指标」的落点不是新增一批 Prometheus 曲线，而是依评测各维度对证据的依赖紧密度，从上到下排一个优先级金字塔：**先把评测要的判定证据在运行侧补保真（P0a/P0b），再把评测结论稳定导成可监控的 agent 质量指标（P1），最后为评测与 agent 能力健康接线监控与告警（P1b/P2）。**

### 0.2 三条红线原则（obsidian + 仓库事实共同约束）

1. **Opik 是 agent 执行证据的唯一权威源。** PostgreSQL 不复制执行证据（`agent_executions/agent_tool_traces/agent_trace_events` 已从 tenant DDL 移除，observability.md）。**已否决「观测事件带 cost/token 快照、绕过 Opik 单点」的方向**——那会平行制造第二证据源。正确路径是**把喂给 Opik 的数据补完整**（span 属性 / mapper 投影 / 消费端透传），并把 Opik/Collector 纳入依赖健康监控，而非绕开。
2. **证据保真是运行观测的第一职责。** Agent 五维评测里过程与成本维度**判定依赖轨迹证据**：「结果层感知不到过程链——过程不稳定→不可复现→无法规模化→成本与体验失控」。Prometheus 结果类指标永远感知不到它；所以观测侧优先保证评测重拉证据的完整性与可达性。
3. **No false green。** 证据缺失 / 成本缺失时必须显式表达为 unknown / null 或失败传播，禁止记 0 制造假健康（统一 Evidence Plane 原则；评测 spec 亦有 fail-closed 约束）。

### 0.3 优先级金字塔（评测依赖紧密度自上而下）

| 级 | 主题 | 评测依赖 | 现状 | 本节 |
|---|---|---|---|---|
| **P0a** | 单 run 成本账本（per-model / part / step） | cost_perf 维度：成本归因需结构，只看总量会失真 | ObservedTrace 只投影总 token/成本；agent mapper 有 per-span 明细但消费端丢弃 | §3 |
| **P0b** | 过程证据可达（工具序列正文 / step 事件） | process 维度：tool_pass / tool_cycle / step_reasoning | mapper 无工具正文；ObservedTrace 丢 Events；Opik 拉取单页截断 | §4 |
| **P0c** | Opik/Collector 依赖健康 | 所有 re-pull 判定的可用性前提 | 作为外部依赖应已纳监控，但缺「评测判定专用」健康视角 | §6 |
| **P1** | 评测结论导出（`agent_eval_score` 写者） | 质量结论 → 稳定可监控的 agent 指标 | GaugeVec 已注册、0 次调用，语义未定 | §5 |
| **P1b** | `eval_*` 能力健康接线 | 评测系统自身运行产出（§11） | 接口 + 注册已有，调用点部分缺失 | §6 |
| **P2** | 监控大盘与告警、计数/标签修复 | 长期承载与租户级对比 | remote 告警 0 命中 agent/llm | §6 |

### 0.4 范围边界（用户已裁）

- **仅评测驱动**：本 spec 覆盖评测判定链的上游证据供给、下游结论导出、旁路能力健康。
- **明确排除**：通用 agent/LLM 运行 SLO 大盘与告警（执行成功率 / TTFT / 错误率告警体系）不纳入，另立 spec；本 spec 只在需要时引用既有 `llm_*` 指标。

---

## 1. 现状代码基座（reality 映射）

> 全部行号取自当前 `origin/main`（HEAD `00fa73b6`）；跨仓库事实以代码、测试与运行结果为准。

### 1.1 已具备的运行观测（无需重建）

- **Agent 执行收尾统一上报** `recordFingerprintAndKPI`（`internal/agent/application/agent.go:773`）：记 `IncAgentTaskCompleted`、`RecordAgentTaskLatency`、`RecordAgentCostPerTask`、`RecordAgentConversationTurn`；指纹属性（解析模型 / 路由链 / 版本）写回 exec + request 两个 span。
- **规则护栏** `RuleGuard.Check` 内联 fail-closed（`internal/agent/application/rule_guard.go:55`）：denylist → `RuleBlockedError`，命中打 `IncEvalRuleHit("tool_denylist", …)` + `IncEvalGateAction`。
- **观测事件发射** `emitObservation`（`internal/agent/application/agent_service.go:124`）：只带 trace 标识 + resource 锚点 + `RuleSignals` + `Behavior`，证据由评测服务从 Opik 拉取（**不复制证据**）。调用点见 §1.2A。
- **判定输入挂回 trace** `evalSpanAttrs`（`internal/agent/application/agent.go:613`）：把 `eval_rule_hits` / `eval_behavior_retry|escalation|abandonment` / `eval_emitted` 以 `opik.metadata.stratum.eval_*` 挂回原 span——执行侧已在「喂 Opik」上做了一部分。
- **评测消费端已装配**：`evaluationTraceEvidenceAdapter`（`api/wiring/evaluation.go:1136`）+ `buildObservationService`（含 judge、平台版本锚点、review 升级器）+ 门禁 spec 落地中。

### 1.2 证据链缺口（评测消费侧丢数据）

| # | 缺口 | 证据（file:line） | 影响 |
|---|---|---|---|
| G1 | **step 级成本/事件被丢弃**：agent mapper 已把每个 span 填成 `AgentTraceEvent`（含 `PromptTokens/CompletionTokens/CostUSD/Model`、`SequenceNo`），但 wiring 层 `mapEvaluationEvidence` 只投影 Tools 摘要 + 总 token，**Events 整个不落 ObservedTrace** | `opik/mapper.go:44-49, 80-95`（已填）→ `api/wiring/evaluation.go:1164-1178`（丢弃） | cost_perf 判定只能看 run 总量，无法按 model / role / step 归因；process 的 step_reasoning 无正文可判 |
| G2 | **工具无正文**：agent 侧 `mapTool` 只投影摘要（name/status/latency/metadata），**无 Arguments / 结果正文** | `opik/mapper.go:65-78`；`domain.ToolObservation` 无正文字段 | 工具参数/结果质量 judge 无上下文；process 只能数序列，不能看质量 |
| G3 | **Opik 拉取单页截断**：`Resolve` 用 `size=100` 一次取 span，长 ReAct（>100 spans）**静默截断** | `internal/agent/infrastructure/opik/client.go:48-49` | 长任务过程/成本判定缺尾部证据，且不报错 → 潜在 false green |
| G4 | **无 result 的异常终止不产观测事件**：`emitObservation` 依赖 `result != nil`；超时强杀/panic 等无 `AgentResult` 路径不发事件 | `agent_execution.go:300,361` → `agent_service.go:125` | 可靠性 / 失败率样本缺口——最该判异的失败被跳过 |
| G5 | **行为信号推导面窄**：`behaviorFromResult` 只覆盖 `NoAnswer.Retried→Retry`、`Degraded→Abandonment`；Escalation 靠 feedback 侧补 | `agent_service.go:162-174` | 异常行为召回有限（设计不做扩大，属评测 spec 判定语义） |

### 1.3 计数与标签失真

| # | 失真 | 证据 | 影响 |
|---|---|---|---|
| C1 | **token 双重计数（坐实）**：同一次 agent LLM 调用，gateway `invokeComplete`（非流式）打 `IncLLMTokenUsage + RecordLLMTokenHistogram`，`invokeStream`（流式）只打 `IncLLMTokenUsage`（histogram 缺口此前被 ledger 覆盖）；agent 层 `react_llm.go:386` 经 `ledger.Record` **再打一次** | `gateway.go:407-412`（complete 双打）、`:467-474`（stream 双打——Fix B 对称补齐）；`react_llm.go:386`；`token_ledger.go:37-70` | counter 对 agent 发起调用偏高约 2×；流式 `llm_token_count` 若仅去 ledger 将归零（Fix B 补网关双侧对称，覆盖流式+非流式）；用量告警与成本对账失真 |
| C2 | **`tenant_id` 标签空串**：`agent_task_completed_total` 有 `tenant_id` label 但实现写死空串（`prometheus.go:734-736`），非注册缺位 | `prometheus.go:435-437`（注册含 tenant_id）、`:734-736`（写死 `""`） | 平台租户级用量/成本不可分——多租户评测对比（平台参数多租户效应）无数据支撑 |
| C3 | **KPI 形参名误导**：`recordFingerprintAndKPI` 形参名 `taskKind` 实收 `string(snap.agentType)`，`IncAgentTaskCompleted(agentID, taskKind, taskKind, status)` 使 agent_type 与 task_kind 两槽同值为 agentType（命名错位，非值污染；平台暂无独立 task-kind 维度） | `agent.go:782`（旧）→ 拆分 `recordAgentKPI`；注册 `:435-437` 五标签 | task_kind 语义悬空，读者误以为存在独立 task-kind 分类 |

> **P0 修复状态（2026-09-03）**：C1 已去重（ledger 撤回 usage 打点，见 §11 D2①）；流式 histogram 由网关 `invokeStream` 对称补齐（Fix B）；C2 已接线（tenant 取自 `ExecutionConfig.TenantID`）；C3 已通过拆分 `recordAgentKPI` 正名。实现细节见计划 `2026-09-03-agent-eval-obs-p0-metrics-fix.md`。

### 1.4 监控与结论导出空白

- **`agent_eval_score` GaugeVec 已注册、0 次调用**，指标语义未定义（`prometheus.go:446-449`）。
- **`eval_*` 能力健康**：接口已进 `MetricsProvider`（`provider.go:132-142`：`IncEvalRuleHit / IncEvalBehaviorAnomaly / IncEvalGateAction / RecordEvalSampleCoverage`），`registerEvalObservationMetrics` 已注册（`prometheus.go:339-386`），但**调用点不齐**：`IncEvalRuleHit` 仅 tool_denylist 一处；`IncEvalBehaviorAnomaly` 在 `applyAnomalyVerdict`（`observation_service.go:304`）；`RecordEvalSampleCoverage` 为 TODO。
- **远端监控告警 0 命中 agent/llm/eval**：`monitoring/remote/dashboards|rules/` 4 张 dashboard 全为 HTTP/依赖/资源；`observability.md:284-286` 明示当前告警「不覆盖 LLM、Agent、Token 或业务指标」。
- **Opik 依赖健康**：监控体系对已验证依赖 target 有 blackbox；但「Opik 作为评测 re-pull 判定的依赖」缺专用健康视角（503/不可用时的评测降级可见性）。

---

## 2. 优先级模型：评测依赖金字塔

```
评测判定（消费端：observation_service / 分层门禁 / 评审池 / feedback）
   │  re-pull：TraceEvidenceReader.Resolve/ResolveBatch（Opik）
   │  push：NATS observation event（轻量引用 + rule/behavior 信号）
   ▼
[ P0a 成本账本 ]  [ P0b 过程证据 ]  [ P0c Opik 依赖健康 ]   ← 证据保真/可达（§3/§4/§6）
   ▼
[ P1 结论导出 agent_eval_score ]                             ← 质量→稳定指标（§5）
   ▼
[ P1b eval_* 能力健康接线 ] [ P2 大盘/告警/计数修复 ]        ← 健康与承载（§6）
```

推理链：

1. 评测 judge（faithfulness/relevance/completeness）、行为（retry/escalation/abandonment）、过程（tool_pass/tool_cycle）、成本（latency/tokens/cost）**全部消费 Opik 证据**；判定所需字段一旦在消费端被丢（G1-G3），judge 只能降级用总 token 与首尾正文，维度判不实。
2. 因此**最高优先工作是「喂给 Opik 的数据 + 从 Opik 拉回的投影」保真**，不是加 Prometheus 曲线。
3. 评测系统自身运行健康（eval_* §11）与质量结论导出（agent_eval_score）是下游，依赖门禁 spec / 观测聚合先可用。

---

## 3. 设计 A：单 run 成本账本（P0a，证据保真）

### 3.1 目标与判定需求

评测 cost_perf 维度需要**在 run 粒度把成本拆出结构**：

- per **model**（配置模型 vs fallback 后实际模型）、per **provider**；
- per **part**（prompt / completion / cache_read，缺失则用 input:output 比近似并标 unknown）；
- per **role / step**（think/reflect/plan/act，或多轮 ReAct 的 step 序列）。

只看 run 总 token/成本（现状 `ObservedTrace.TotalTokens/CostUSD`）会让成本归因失效：ReAct 轮次累积与系统提示固定开销、工具回填、反射重试各自占比不可见——而这正是成本治理要暴露的四个黑洞结构。

### 3.2 设计落点（不复制证据，只透传与补全）

| # | 改动 | 现状 → 目标 |
|---|---|---|
| A1 | **ObservedTrace 保留 step 事件** | wiring `mapEvaluationEvidence` 丢弃 `Events` → 透传 agent `TraceEvidence.Events`（已含每 span prompt/completion/cost/model/provider/sequence，mapper.go:80-95） |
| A2 | **评测聚合**：run 级成本 = Σ events cost；`TraceEvidence.CostUSD/TotalTokens` 只作总账对账 | observation_service / Service 聚合时提供 per-model / per-part 结构 |
| A3 | **part 拆分**：cache_read 无则暴露 input:output 与 `cache_unknown` 标记 | no-false-green：缺缓存字段不记 0 |
| A4 | **计数去重裁决**（修 C1）：`llm_token_usage_total` 归属**网关出站唯一记账**；`TokenLedger.Record` 移除 metrics `Inc*/Histogram` 打点，只保留 cost 计算 + span 属性 + 日志（span 属性已写入，见 token_ledger.go:51-57） | 消除 2× 双计；agent 侧 cost 语义由 span/证据承载，与 Opik 账本一致 |

### 3.3 成功标准与验证

- 一条真实多轮 ReAct agent 任务 → 观测聚合能给出 per-model / per-step token 明细，且 Σ step cost ≈ trace 总 cost（±usage 边界误差，误差须显式而非吞没）。
- `llm_token_usage_total` 在单任务前后净增 = 网关实际出站 token 一次，不再 2×。
- 单测：mock `TraceEvidenceReader` 喂多 span trace → 断言聚合结构与对账一致性。

---

## 4. 设计 B：过程证据可达（P0b，工具序列 / step 正文）

### 4.1 目标与判定需求

process 维度（tool_pass 通过率、tool_cycle 多余循环、step_reasoning、失败降级路径）**必须基于轨迹**——主 spec §6.5 已定义轨迹断言。运行侧职责是保证评测能拿到：

- 完整工具调用序列（顺序、name、status、latency、error 已可拿，mapper.go:65-78）；
- **每步工具参数与结果正文**（当前缺失，G2）；
- step 级 LLM 事件（与 P0a A1 共用，G1）；
- 长任务不截断（G3）。

### 4.2 设计落点

| # | 改动 | 约束 |
|---|---|---|
| B1 | **mapper 补工具正文**：`mapTool` / `domain.ToolObservation` 增加 `Arguments`/`ResultText`（来源 span.Input/Output，经既有脱敏链），评测侧 `mapToolObservations` 透传 | 正文上限（如 4KB/工具）与脱敏沿用 payload 既有策略；缺正文标 `unknown`，不冒充 |
| B2 | **Opik 拉取分页**：`Resolve` 去掉单页 `size=100` 硬截断，按 trace span 数分页拉全；截断时返回显式错误而非静默 | 避免 G3 false green；分页是 Opik REST `spans?trace_id=…&page=N` 既有接口 |
| B3 | **step 事件透传**：与 A1 同一处 Events 透传落地后，process 断言在评测侧基于完整序列执行 | 观测事件本身不加正文（保持轻量引用，红线 1） |
| B4 | **process 信号不下沉为 Prometheus 新指标**：多余率/循环等是评测侧聚合断言（主 spec §6.5），非运行侧新曲线 | 范围纪律：本 spec 只保证证据可达，不做重复指标 |
| B5 | **失败样本可达（修 G4）**：无 `AgentResult` 的异常终止（超时强杀/panic）路径也保证 Opik 可重拉到 error trace（exec span `status` 落盘），并在可行处补发 status=error 的观测事件 | emitObservation 维持 best-effort；评测侧以 span status 兜底抓失败，缺失样本显式标记而非静默跳过 |

### 4.3 成功标准

- 长任务（构造 >100 spans 的 ReAct）证据 re-pull 完整，不再截断；
- 工具正文可达，`tool_pass / tool_cycle` 断言可基于正文+状态给出判定；
- 缺正文样本显式标记，评测报告不计为 pass；
- 失败/异常终止任务的 error trace 可在 Opik 重拉（G4 闭合），观测面不含因无 result 造成的系统性漏采。

---

## 5. 设计 C：评测结论导出（P1，`agent_eval_score` 写者）

### 5.1 现状与问题

`agent_eval_score`（`{agent_id, …}` GaugeVec，`prometheus.go:446-449`）**已注册、0 次调用**。直接写会有两个悬而未决：**写什么语义**、**何时更新**，且 `agent_id` 单 label 无法表达多维结论。

### 5.2 结论来源与语义（开放决策 D1，推荐默认值如下）

- **来源**：评测结论（分层门禁/评测集结论，门禁 spec 落地后可得；观测 run 聚合先行提供过渡口径）。
- **语义**：`score = 该评测口径下 resource 的维度分`（overall 通过率 + by-dimension）。
- **label**：沿用已注册 `{agent_id, metric}`（`prometheus.go:446-449`，P0 不改 schema——同名字段 label 集合变更会触发 Prometheus 拒收）。metric ∈ `overall|faithfulness|relevance|completeness|safety|format|cost_perf|process|behavior`。agent 量级（平台托管 + 租户自建）有限，基数可控。
- **更新时点**：评测集/门禁判定收敛后写一次（事件驱动），**禁止 run 级高频写**，避免 gauge 语义模糊成瞬时值。
- **无新结论时**：不更新旧值（保留上次判定），与 no-false-green 一致。

### 5.3 写者落点

| # | 改动 |
|---|---|
| C-a | 复用已注册 `RecordAgentEvalScore(agentID, metric string, score float64)`（`provider.go`，0 调用）与 `{agent_id, metric}` label（`prometheus.go:446-449`），只补写者接线；**不新增 `SetAgentEvalScore`、不改 label** |
| C-b | 写者注入评测结论收敛点（门禁判定/评测集 run 收尾，参照 review_service / experiment_runner 事件位），非 agent 运行路径 |
| C-c | **依赖次序**：门禁 spec（§8 落地）先于本设计全量接线；过渡期可用观测 run 聚合口径先行验证语义 |

### 5.4 成功标准

- 评测集跑完 → `agent_eval_score{agent_id, metric}` 出现，维度完整，Dashboard 可查；
- 代码评审/生产对账能回答「某 agent 当前质量结论是什么、基于哪个评测口径、何时更新」。

---

## 6. 设计 D：能力健康与依赖接线（P1b/P2）

### 6.1 `eval_*` 能力健康接线（P1b，引用主 spec §11，不重复定义）

现状：接口 + 注册已有，调用点不齐。补齐：

- `RecordEvalSampleCoverage(resource, ratio)`：观测采样器决策处写（当前未接线）；
- `IncEvalRuleHit` 除 tool_denylist 外，随规则护栏扩展（门禁 spec L1）逐规则接线；
- `IncEvalGateAction` / `IncEvalBehaviorAnomaly`：随门禁判定/观测判异接线；
- `SetEvalReviewBacklog` / `IncEvalReviewEscalateFailure`：评审池处（review_service 已具备触发点）。

### 6.2 agent KPI 修复（P0 已落地）

- C2 `tenant_id` 空串 → 已接线：`IncAgentTaskCompleted` 增 `tenantID` 参贯通 interface/双实现，Execute 收尾取 `cfg.TenantID`（`ExecutionConfig.TenantID`，与内层 `newReActExecContext` 重注入同源），解锁租户级成本对比；
- C3 形参误导 → 已收敛：`recordFingerprintAndKPI` 拆分出 `recordAgentKPI`（单一职责），形参正名 `agentType`；task_kind 槽镜像 agent_type，平台暂无独立 task-kind 维度（注释声明，预留接入）；
- `agent_executions_total`（无 tenant，schema 锁）与 `agent_task_completed_total`（含 tenant）**口径分工**：前者是执行事件计数（不含租户，避免改已注册 schema），后者是租户级任务 KPI；跨租户评测对比统一查 `agent_task_completed_total`。

### 6.3 监控大盘与告警（P2）

> 范围条款：只做评测驱动的 agent/评测健康与结论展示，不建通用 SLO 大盘。

- **Dashboard（远端 `monitoring/remote/dashboards/`）新增 1 张**：`Agent · 评测驱动健康`，含：
  - 质量结论：`agent_eval_score` by dimension；
  - 能力健康：`eval_rule_hit_total / eval_gate_action_total / eval_behavior_anomaly_total / eval_sample_coverage / eval_review_backlog`；
  - 用量对账：修复后的 `llm_token_usage_total`、`agent_cost_per_task_usd` by tenant。
- **告警（远端 `monitoring/remote/rules/`）**：评测驱动的最小告警集（4 条，`runbook_url` 指向 `docs/operations/alerts/` 锚点）：
  1. `eval_sample_coverage` 长期 < 阈值（观测面收缩）；
  2. `eval_review_backlog` 持续堆积（判定堵住）；
  3. `eval_judge_failure` 高（judge 通道故障）；
  4. Opik re-pull 失败率 / 评测降级（P0c 依赖健康专用视角）。
- **不做**：通用执行成功率/TTFT 告警（范围外）。

---

## 7. 分期落地

| 期 | 内容 | 依赖 | 出口判据 |
|---|---|---|---|
| **P0（计数与语义收敛，已落地 2026-09-03）** | C1 计数去重（D2①）；C2/C3 标签修复；D1 `agent_eval_score` label 对齐已注册 `{agent_id, metric}`（语义写作留 P2） | 无 | usage 双计消除；tenant_id 落真实租户；task_kind 命名收敛；score label 与代码一致 |
| **P1（证据保真）** | A1-A3 成本账本 + B1-B3 过程证据（Events 透传、工具正文、Opik 分页） | P0 | cost_perf/process 判定有据（§3.3/§4.3 成功标准过） |
| **P2（结论导出）** | 设计 C：`RecordAgentEvalScore` 写者接线（复用已注册，不新增） | 门禁 spec（卡 C）判定可用或观测聚合口径 | `agent_eval_score` 有值、Dashboard 可查 |
| **P3（能力健康）** | 设计 D：eval_* 调用点补齐 + Dashboard + 最小告警集 | P1/P2 指标真实存在 | 远端 Dashboard 常青、告警有效（monitoring-config-test 过） |

> P0 独立可先行；P1 是评测判定质量的主干；P2/P3 依赖上游结论/指标真实出现，避免空接线。

---

## 8. 错误处理与 no-false-green

- **证据缺失**：span 截断、Opik 不可用、usage 缺失一律显式 `unknown`/传播；Opik `Resolve` 截断改为显式错误（G3），杜绝静默假健康。
- **观测发射**：`emitObservation` 保持 best-effort（评估器不阻断执行铁律）；Opik 不可用只使 re-pull 判定 503，NATS 事件不丢，支持后续补拉。
- **计数去重**：修复后 usage 单归属（网关），ledger 只算 cost 与 span 归属，幂等无重复。
- **score 写失败**：不落旧值、不伪造；写者失败只告警，不阻断评测主链路。

---

## 9. 测试策略

| 层 | 手段 |
|---|---|
| mapper / wiring 投影 | 补 `mapEvidence`/`mapEvaluationEvidence` 透传 golden 测试（Events 完整、工具正文有界、缺正文标 unknown） |
| 计数去重 | mock `MetricsProvider` 断言单任务 `IncLLMTokenUsage` 调用次数（gateway 1× / ledger 0× 或等价裁决） |
| 分页 | Opik `Resolve` 分页测试：>100 spans trace 全量返回；翻页失败显式错误 |
| 观测聚合 | mock `TraceEvidenceReader` 多 span → 断言 Σ step cost ≈ trace 总账 |
| 合同/回归 | `api/http/contract_test.go` 守护既有契约不变；`make code-quality` 新函数不超门禁 |

外部依赖（Opik）一律 mock；全量用 `-race`。

---

## 10. 交付项与成功标准

**git 交付项**（后续 writing-plan 分解）：

1. 计数与标签修复（P0）：`token_ledger.go`、`gateway.go`（如裁决去 ledger）、`agent.go` `recordFingerprintAndKPI`、调用点、`prometheus.go` 标签。
2. 证据透传（P1）：`ObservedTrace`（port）、wiring `mapEvaluationEvidence`、opik `mapper.go`/`client.go`（分页）、聚合单测。
3. 结论导出（P2）：`RecordAgentEvalScore` 写者接线。
4. 健康接线 + 监控（P3）：eval_* 调用点、dashboard JSON、告警规则 + runbook、`monitoring-config-test` 通过。

**成功标准**：

- 评测对真实 agent 任务可给出 per-model/step 成本结构与完整工具过程证据（判得实）；
- `llm_token_usage_total` 去重后与网关出站一致；
- `agent_eval_score` 承载「某 agent 某维度当前质量结论」，Dashboard 可查；
- 远端 Dashboard/告警经 `monitoring/remote` 部署常青，0 条空指标曲线。

---

## 11. 开放决策（待 review 裁决）

| # | 决策 | 候选 | 推荐默认 |
|---|---|---|---|
| D1 | `agent_eval_score` 语义/维度/label | ①run 级写 ②评测集/门禁结论写 ③两者分层 | **②为主，①仅过渡口径**；label 沿用已注册 `{agent_id, metric}`（P0 定稿，语义写者留 P2） |
| D2 | 计数归属 | ①去 ledger 留 gateway ②去 gateway 留 ledger | **①**（网关出站为 token 唯一事实源）——**P0 已落地** |
| D3 | 工具正文上限与脱敏边界 | 4KB / 8KB / 引用截断策略 | **4KB + 既有脱敏链**，超限截断标 unknown |
| D4 | cache_read 缺失策略 | ①暴露 input:output ②全标 unknown | **①为主、缺失显式标注**，不记 0 |
| D5 | P0-P3 是否四期并行度 | 串行 / 部分并行 | **P0 先独立，P1 主干，P2/P3 后置**（依赖真实数据） |

---

## 附：证据索引（file:line 速查）

- agent 收尾上报：`agent.go:773-797`（`recordFingerprintAndKPI`）；`agent.go:613-626`（`evalSpanAttrs` 挂 eval_*）
- 观测事件：`agent_service.go:124-143`（`emitObservation`）；`:147-157`（`ruleSignalsFromBlocks`）；`:162-174`（`behaviorFromResult`）；`agent_execution.go:300,361`（调用点）
- token 账本 / 计数：`token_ledger.go:37-70`（Record：cost+span 属性+日志，**无 usage 打点**——C1 后）；`graph/react_llm.go:386`（ledger.Record 调用）；`gateway.go:407-412,467-474`（usage 唯一记账：counter+histogram，complete 与 stream 双侧对称）；`:403,460`（duration）；`api/wiring/agent.go`（`wireTokenLedger` 不注入 metrics）
- agent 指标注册：`prometheus.go:202-216`（executions/duration/step，无 tenant）；`:435-437`（task_completed 五标签含 tenant_id）；`:734-736`（实现落真实 tenant）；`:446-449`（agent_eval_score `{agent_id, metric}` 0 调用）；`agent.go`（`recordAgentKPI` 打点 + `recordFingerprintAndKPI` 指纹）
- eval_* 接口与注册：`provider.go:132-142`；`prometheus.go:339-386`（registerEvalObservationMetrics）
- Opik mapper/client：`opik/mapper.go:15-51`（mapEvidence）、`:65-78`（mapTool 无正文）、`:80-95`（mapEvent 有明细）、`:175`（textOf）；`opik/client.go:42-54`（Resolve size=100 单页）、`:56`（ResolveBatch）
- wiring 投影：`api/wiring/evaluation.go:1136-1178`（adapter + mapEvaluationEvidence 丢 Events）、`:1180`（mapToolObservations）
- 规则护栏：`rule_guard.go:55-80`
- 远端监控：`monitoring/remote/dashboards|rules/`（0 命中 agent/llm/eval）；`observability.md:284-286`
