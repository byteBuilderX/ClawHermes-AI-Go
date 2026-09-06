# 评测指标监控面板（四区 × 资源行下钻）设计

> 日期：2026-09-03 · 状态：spec（待评审） · 落点 worktree：`feat/evaluation-monitoring-panel`
> 本文档是 brainstorming→spec 流程的产出，编码此前逐轮确认的产品决策（见 §1.1 决策来源）。评审通过后由 writing-plans 派生实现计划，在此之前不进入实现。

## 0. 结论先行（TL;DR）

在评测中心（`EvaluationCenterPage`）内新增一个**租户级「监控」视图**：以**被测资源为行**展示当前时间窗（默认近 7 天，可调）内的四区指标摘要 —— **质量**（judge 三维 through-rate）· **行为**（rule 命中 + retry/escalation/abandonment + verdict 分布）· **成本与延迟**（cost_perf）· **过程**（评测集 run 的 `process_pass_rate`）；点击任一行下钻该资源的四区时间趋势。

数据口径为「**观测线为主 + 评测集补过程**」：

- 质量 / 行为 / 成本 / 延迟来自 **`eval_observations`**（真实运行快照，含 `signals.judge/rule/behavior` 与 `cost_perf`）。
- 过程区只能来自**评测集** `eval_runs.metrics.process_pass_rate`（观测信号**没有** process 字段）。

范围外（保留在 monitoring/alerting 侧，不进租户前端面板）：评测系统自身运行健康指标 —— judge latency/failure、queue backlog、sample_coverage、calibration、review backlog（2026-08-28 spec §11.2 的 B 类）。面板展示对象是**被测资源的质量表现**，不是**评测系统自身的健康**。

实现核心缺口：现有 `GET /evaluations/observations` 只是分页明细（`QueryByResource` 无 count/group/aggregate），前端拿不到「每资源四区摘要」与「时间趋势」。因此**需新增后端租户级聚合查询能力**，并把结果接入评测中心（详见 §4）。

## 1. 目标与非目标

### 1.1 已确认的决策来源（评审锚点）

| # | 决策 | 内容 |
|---|------|------|
| D1 | 落点 | 评测中心 `EvaluationCenterPage` 升级 —— 新增视图（tab），不是新页面，不接 Grafana/Prometheus 仪表盘 |
| D2 | 形态 | 租户级概览页，指标与「系统已总结的评测指标清单」（2026-08-28 spec §11）语义对齐 |
| D3 | 展示区 | 四区 = 质量 × 行为 × 成本 × 过程，全部放租户级前端面板 |
| D4 | 排除项 | 评测系统自身运行健康（§11.2 B 类）留在 monitoring/alerting 侧 |
| D5 | 口径 | 观测线为主 + 评测集补过程（质量/行为/成本/延迟来自 `eval_observations`；过程只能来自 run.metrics） |
| D6 | 面板形态 | 资源行 × 下钻趋势：主表列每资源四区摘要 + 点击下钻单资源时间趋势 |

### 1.2 目标

1. 让租户在一个页面看清「近一周各被测资源跑得怎么样」：质量在哪些语义维度下滑、行为异常/拦截多不多、烧了多少 token/钱、延迟走势、离线评测的过程通过率基线。
2. 复用评测中心已有的资源口径与交互基座（`ResourceKind` 四枚举、kind/resource_id 过滤、tab 布局、native SVG 趋势图），不新建平行概念。
3. 只读聚合，不改任何观测/评测写入路径，无 DDL 迁移（纯查询扩展）。

### 1.3 非目标（显式排除，防 scope creep）

- 不做**系统健康**（B 类）面板：judge 延迟/失败率、queue backlog、sample_coverage、calibration、review backlog 等留在 Prometheus/告警侧（D4）。
- 不接 Prometheus / 不做跨租户聚合（面板数据源是本租户 DB 观测线）。
- 不做按 `param_version` / `stratum` 分层展示（观测的 `param_version` 多为 unknown 平台序；列为扩展点，见 §9）。
- 不做告警 / 阈值 / 主动推送。
- 不做资源间对比图、不做导出、不做自定义报表。
- 不改 `eval_observations` 写入方（现有 Agent 完成链路）的任何语义。
- 本版本不含新增列/表 —— 无需 migration 与 tenant schema 变更。

## 2. 现状基座与关键约束（证据）

### 2.1 两条数据线

| 线 | 表 | 来源 | 时间轴 | 一条 = |
|----|----|----|--------|--------|
| 观测线 | `eval_observations` | 真实 agent 完成（运行时快照） | `created_at`（连续） | 单 trace 单行 |
| 评测线 | `eval_runs` | 离线评测集批量 | `created_at` / `completed_at`（离散批次） | 单次 run（`metrics` JSONB） |

**`eval_observations`** DDL：`pkg/storage/postgres/tenant_schema.sql:549-566`
`id, trace_id, resource_kind, resource_id, param_version, signals, cost_perf, stratum, verdict, created_at`；索引 `(resource_kind, resource_id, created_at DESC)`、`(trace_id)`、`(verdict, created_at DESC)`。

**`eval_runs`** DDL：`pkg/storage/postgres/tenant_schema.sql:488-514`
`…, status (queued/running/succeeded/failed/cancelled), passed, metrics JSONB, created_at, started_at, completed_at`；索引 `(resource_kind, resource_id, created_at DESC)`、唯一部分索引 `idempotency_key`。

### 2.2 观测信号结构（`internal/evaluation/domain/observation.go`）

- `signals` JSONB = `ObservationSignals{Rule []RuleSignal, Judge []JudgeSignal{dimension, score, confidence, reason}, Behavior{retry, escalation, abandonment}}`。
- `cost_perf` JSONB = `{latency_ms, tokens, cost_usd}`。
- `verdict ∈ pass | flag | block`；`stratum` = 分层 tier。
- **观测没有 process 字段** —— 这是「过程区必须接评测线」的结构性原因（D5）。
- Judge 维度硬编码 `["faithfulness","relevance","completeness"]`（`internal/evaluation/application/observation_service.go:18`）。score 归一为 0.0/1.0（`res.Passed→1.0`）；`anyJudgeBelow(score, JudgeBelowThreshold=0.5)` → `verdict=flag`。
- 空态前提：judge 未装配（disabled / nil）时 `signals.judge` 为空 → 质量区无样本；同理行为区依赖装配。

### 2.3 run.metrics 结构（`internal/evaluation/application/metrics.go:23-90` aggregateRunMetrics）

顶层 key：`pass_rate, overall_pass_rate, process_pass_rate, total_cases`，以及有结果时的 `total_tokens, total_cost_usd, avg_tokens_per_case, avg_latency_ms, p95_latency_ms, cost{total_usd, avg_usd}, latency{p50_ms, p95_ms, max_ms}, by_dimension{dim:{avg_score, pass_rate, samples}}, version{...}`。

- `process_pass_rate` 语义（metrics.go 注释）：过程断言通过比例，分母为全部已评测结果；**未配置过程断言的 case 其 `ProcessPass=true` 计入分子** → 对纯功能用例，process_pass_rate 恒 = 1.0，只有配了 `tool_spec`/`step_criteria` 的评测才有区分度。面板须如实呈现，前端需能识别「本 run 未做过程断言」而非把它当 1.0 完美率（见 §4.4 展示说明）。
- `RunSummary`（`domain/query.go:62`）**不带 metrics**；metrics 仅在单 run detail 暴露（`EvalRun.Metrics map[string]any`，`domain/evaluation.go:361`）。面板的 run 过程基线统一取**查询窗口内**按 `(resource_kind, resource_id)` 的最近一条 `status='succeeded'` run 的 `metrics.process_pass_rate`（窗口口径见 §4.4）。

### 2.4 现有查询能力与缺口

- `GET /evaluations/observations`（`api/http/router.go:198`）→ `ListObservations`（application/observation_service.go:354-360）→ `PgObservationRepository.QueryByResource`（kind AND id 必填、时间窗可选、仅明细分页）→ `{"items", "total": len(items)}`（handler evaluation_handler.go:705-737）。**无 count/group/aggregate 能力。**
- 评测中心查询走 `CenterQueryRepository`（port/evaluation.go）：`Overview/ListResources/ListSuites/ListRuns/ListCandidates/ListExperiments/Timeline`，实现 `PgCenterQueryRepository`（persistence/query_repository.go，`tenant()` helper + `execTenantTx`），经 `QueryService` 注入 handler 的 `evaluationQueryService` 接口（handler evaluation_handler.go:60-68）。这些查询已能 join `eval_runs`（ListResources 取每资源 latest run 状态）。
- **缺口**：没有任何「按资源分组聚合观测 + 关联最近 run 过程基线」的查询。新增能力需一次补齐。

### 2.5 租户隔离约束（仓库红线）

`eval_observations` 与 `eval_runs` 均为 tenant-scoped：所有访问方法必须经 `postgres.WithTenant` + `execTenantTx`；port 方法显式携带 `tenantID string`；新增聚合查询同样必须走 `execTenantTx` 内的租户事务，禁止绕过。

### 2.6 前端现状（`web/src/modules/evaluation/`）

- `pages/EvaluationCenterPage.tsx`：顶部 kind/resource_id/status 过滤来自 URL searchParams；`Tabs` 已有 runs/candidates/experiments/suites/**health**/review；health tab（行 139-140）挂 `RuntimeHealthTrendPanel`（run 通过率趋势，Prom 侧 §10.1 backlog 尚未接入）。**新「监控」tab 加在 health 之后**，接同源 `defaultKind`/`filterResourceId` 并加 key 使过滤变化重挂载。
- `components/RuntimeHealthTrendPanel.tsx`：接受 `defaultKind`/`defaultResourceId` 的既有面板模式参考。
- `components/HealthTrendChart.tsx`：native SVG 折线组件（`HealthTrendPoint`），多区趋势可扩展为多段（多维度线）或复用多次；仓库 **无 recharts import**（package.json 虽有），沿用 native SVG 约定。
- `api/evaluation.api.ts`：现有 `evaluationApi` 对象；**无 observation/聚合 API**，需新增。
- `model/evaluation.ts`：`resourceKindSchema` 四枚举；run schemas；**无观测/监控类型**，需新增。
- 行为数字集中 `web/src/constants/index.ts`（如 `EVALUATION_TREND_RUN_LIMIT=100`）。

## 3. 概念模型：四区 × 数据来源 × 指标对齐

面板「与评测指标对齐」指**语义对齐** 2026-08-28 spec §11.1 A 类对象能力指标（不是去查 Prometheus —— 面板读租户 DB 观测线）。对齐关系：

| 面板区 | 展示内容 | 租户 DB 数据源 | 对齐 §11.1 A 类 |
|--------|----------|----------------|-----------------|
| 质量 | 每 judge 语义维度：通过率、均分、平均置信、样本数 | `eval_observations.signals.judge[]` | `eval_judge_score{resource, dimension}` |
| 行为 | rule 命中数、retry/escalation/abandonment 计数、verdict 分布 | `signals.rule` 长度 + `signals.behavior` + `verdict` | `eval_rule_hit_total`、`eval_behavior_anomaly_total` |
| 成本/延迟 | token 总量、成本 USD、平均/ P95 延迟、样本数 | `cost_perf.{tokens, cost_usd, latency_ms}` | ——（成本无 A 类标签，取 cost_perf 真实值） |
| 过程 | 评测集过程通过率基线 | `eval_runs.metrics.process_pass_rate`（最近 succeeded run） | `eval_sample_coverage` 评测侧 + §6.2 过程断言 |

**口径澄清（D5）**：面板「成本」= 真实运行观测成本（观测线），**不含**评测集 run 消耗 —— run 是离线开销，不是被测资源运行质量。run 只贡献「过程」区的 `process_pass_rate` 基线。

### 3.1 各区的空态规则（不做假装为零）

- 质量区：窗口内某维度 `signals.judge` 无样本（judge 未装配）→ 该维度不出现；全部维度无样本 → 显示「无 judge 装配」。
- 行为区：rule/behavior 未装配时计数为 0 属正常（规则系统按资源开/关），但要与「装配了但全过」区分 —— 本版本通过样本数与 rule 是否被观测携带的分布呈现，不引入装配态信号。
- 过程区：窗口内无 succeeded run → `process: null`（显示「窗口内无评测」）；有 run 但该 run 无过程断言 → 前端据 §2.3 语义展示「该 run 未做过程断言」，不把恒 1.0 当完美率。后端**只回传 run 的原始 `process_pass_rate` 与 run 元信息**，不做解释性推断。

## 4. 后端设计

### 4.1 总体落点

新增租户级聚合查询属「评测中心查询」的自然扩展（一次 SQL 即可 join 观测与 run 两表、复用 `tenant()` 租户路径与既有 wiring）—— 因此落在 **`PgCenterQueryRepository` 扩展**，而非塞进 `PgObservationRepository`（后者的单一职责是观测明细读写）。

链路：`CenterQueryRepository` 增方法 → `QueryService` 透传 → handler 的 `evaluationQueryService` 接口增方法（handler evaluation_handler.go:60-68 同步）→ 新 `GET` 路由 → handler DTO（手写 struct + `form:` tags，沿用 `ListObservationsQuery` 模式，非 proto-gen）。port 改动的**全部 test mock/stub 必须同步**（仓库红线，见 §7.3）。

> 备选（不取）：把聚合塞进 `ObservationRepository` —— 会把它拖成跨 run 的巨型查询仓库，且观测 repo 的 `observationColumns` 与 run join 语义混在一起，职责不清。

### 4.2 新增端点契约

沿用现有 JSON snake_case、`{"items":…}` 分页风格与 `from/to`（RFC3339）时间窗语义。窗口为空时后端兜底默认近 7 天（常量见 §4.3）。

**端点 1 —— 资源行四区摘要**

`GET /evaluations/monitoring/resources?resource_kind=&resource_id=&from=&to=`

- `resource_kind`（可选，合法值见 `ResourceKind.Validate`）；`resource_id` 仅在带 `resource_kind` 时有意义（可选）。不传 = 全部资源。若只传 `resource_id` 不传 kind → 400。
- 响应：

```jsonc
{
  "items": [{
    "resource_kind": "skill", "resource_id": "skill-a", "sample_count": 128,
    "quality": [   // 窗口内实际出现过的维度；无样本维度不出现
      { "dimension": "faithfulness", "pass_rate": 0.92, "avg_score": 0.92, "avg_confidence": 0.87, "samples": 128 }
    ],
    "behavior": { "rule_hits": 15, "retry_count": 3, "escalation_count": 1, "abandonment_count": 0,
                  "verdict": { "pass": 120, "flag": 6, "block": 2 } },
    "cost": { "total_tokens": 154000, "total_cost_usd": 0.42, "avg_latency_ms": 1800, "p95_latency_ms": 5200 },
    "process": { "process_pass_rate": 0.67, "run_id": "run-9", "run_created_at": "2026-09-02T08:00:00Z" }  // 可为 null
  }],
  "window": { "from": "…", "to": "…" }
}
```

- `items` 按 `sample_count` 降序，最多 `limit`（默认/上限常量见 §4.3）。MVP 不分页（资源行 = 窗口内有观测的资源，量级可控；超限截断在前端明确标注「仅显示观测最多的 N 个资源」）。

**端点 2 —— 单资源四区时间趋势（下钻）**

`GET /evaluations/monitoring/resources/trend?resource_kind=&resource_id=&from=&to=`

- kind 与 id 走 query 参数（与现有 `GET /evaluations/observations` 的 `QueryByResource` 约定一致，规避 `resource_id` 含 `/` 时路径段截断风险）；两者都必填并各自校验（kind 用 `ResourceKind.Validate`）。时间窗可选（后端默认近 7 天）。
- 响应：

```jsonc
{
  "resource_kind": "skill", "resource_id": "skill-a",
  "series": [ // 按天桶（MVP 粒度=日；桶内无观测则空态——见 4.4）
    { "bucket_at": "2026-09-01T00:00:00Z", "sample_count": 20,
      "quality": [ { "dimension": "relevance", "pass_rate": 0.9, "avg_score": 0.9, "avg_confidence": 0.8, "samples": 20 } ],
      "behavior": { "rule_hits": 2, "retry_count": 0, "escalation_count": 0, "abandonment_count": 0,
                    "verdict": { "pass": 19, "flag": 1, "block": 0 } },
      "cost": { "total_tokens": 24000, "total_cost_usd": 0.06, "avg_latency_ms": 1600, "p95_latency_ms": 4100 } }
  ],
  "runs": [ // 该资源窗口内 succeeded run 的过程基线点（离散、独立于 series 时间轴）
    { "run_id": "run-9", "process_pass_rate": 0.67, "run_created_at": "2026-09-02T08:00:00Z" }
  ]
}
```

- `runs` 为空数组（无 succeeded run）时前端渲染「该窗口无离线评测过程数据」，而不是伪造点。

### 4.3 领域类型与常量

- `domain/query.go` 新增页面摘要类型：`MonitorResourceSummary`（含内嵌 `QualityDim`、`BehaviorStats`、`CostStats`、`*ProcessBaseline`）、`MonitorResourcesPage{Items, Window}`、`MonitorTrendPoint`、`MonitorTrendSeries{ResourceRef, Series, Runs}`。字段名与 §4.2 JSON 一一对应，沿用现有 struct 风格（`json` tag、`omitempty`/指针表达可空）。
- 数值常量（行为数字禁止内联，仓库红线）：默认监控窗口天数、资源行 `limit` 上限，放 `pkg/constants/evaluation.go`（跨 application 与前端契约边界不共享 —— 前端默认近 7 天走 `web/src/constants/index.ts`，两端各持有默认值并在 UI 明示）。

### 4.4 聚合语义与实现方向（plan 阶段定稿 SQL）

在 `execTenantTx` 内、经 `tenant()` 设租户后执行。方向性要点（供实现计划细化并在仓库集成测试用真实 fixture 校验）：

- **范围过滤**：`WHERE created_at >= $from AND created_at <= $to`（窗口）+ 可选 `resource_kind` / `resource_id`。窗口若后端兜底则 from=now-7d、to=now。
- **质量区**：`signals.judge` 是数组 → 以 `jsonb_array_elements(signals->'judge')` 展开按 `(resource, dimension)` 分组。`score` 已 0/1 归一，故 `avg(score)=pass_rate`；同步 `avg(confidence)`、`count(*)`。与 run 侧 `by_dimension`「未出现维度不在结果」语义保持一致。
- **行为区**：rule 命中 = 每观测 `jsonb_array_length(signals->'rule')` 求和；behavior 三维 = `count(*) filter (signals->'behavior'->>'retry' = 'true')`（escalation/abandonment 同构）；verdict 分布 = 按 `verdict` 分组 count。
- **成本区**：`sum((cost_perf->>'tokens')::int)`、`sum((cost_perf->>'cost_usd')::numeric)`、`avg((cost_perf->>'latency_ms')::numeric)`、P95 用 `percentile_cont(0.95) within group (order by (cost_perf->>'latency_ms')::numeric)`（或窗口内聚合后在 Go 计算；观测量可控）。
- **过程区**（窗口口径统一）：对每个资源取**窗口内**最近一条 `status='succeeded' AND created_at BETWEEN $from AND $to` 的 `metrics.process_pass_rate` + `run_id` + `created_at`；窗口内无 succeeded run → `process: null`。端点 1 用 `LEFT JOIN LATERAL`（按资源取窗口内最近 run），端点 2 的 `runs` 即窗口内该资源全部 succeeded run 点 —— 两端的「过程」都以窗口为界，空态语义一致（「该窗口无离线评测」），避免把窗口外陈旧 run 基线显示在近 7 天观测旁。
- 端点 1 可一次 SQL 分组完成；端点 2 的 series 按 `date_trunc('day', created_at)` 分组 + runs 独立小查询，在 application 组装。SQL 细节留给 plan，用**仓库既有真实 fixture/集成测试**（见 §7）验证数值，不依赖推测。

实现时逐条核对：多租户（两租户 fixture）、judge 空数组、窗口边界含端点、verdict 未知值防御、无观测资源不出现（不返回 sample_count=0 的假行）。

### 4.5 错误映射

新增端点沿用现有 handler 约定：bind 失败/参数非法 → 400；仅路由层 404（路径资源非法值由校验 400 处理）；查询失败 → `c.Error(err)` 交给统一错误中间件映射（不吞错、不伪成功）。`resource_kind` 非法、`resource_id` 单传、`from > to` 均 400。新增 contract golden 覆盖正反例。

## 5. 前端设计

### 5.1 页面结构与组件

在 `EvaluationCenterPage` 的 `Tabs` 中、`health` tab 之后新增 `monitor` tab（label：`监控`），children 为 `<EvaluationMonitorPanel>`；复用父级 `kind`/`filterResourceId`（来自 URL searchParams），加 `key={`monitor-${kind ?? 'all'}-${filterResourceId ?? 'none'}`}` 使过滤变化重挂载（对齐 health tab 行 139-140 的做法）。

组件拆分为（`web/src/modules/evaluation/components/`）：

- `EvaluationMonitorPanel` —— 视图编排：默认近 7 天窗口（`RangePicker`），拉取资源行摘要；上「资源行主表」，行点击开下钻抽屉；空态处理。可接受 `defaultKind`/`defaultResourceId` props。
- `MonitorResourceTable`（或并入 Panel）—— 主表：列 = 资源（kind + id 徽标，复用 `displayLabel`/`StatusTag`）、质量（三维迷你条/数字）、行为（rule/retry/flag+block）、成本（token + USD + P95 延迟）、过程（process_pass_rate + run 时点）。行数多时在表头明示「窗口仅显示观测最多的前 N 资源」截断提示。
- `MonitorResourceDrawer` —— 下钻：质量三维随时间（native SVG 多线，可复用/扩展 `HealthTrendChart` 或按需新增多序列组件）、行为计数/verdict 分布趋势、成本与延迟趋势、以及 run 过程基线离散点（无 run 时给空态文案）。宽度用既有 `drawerWidth`。
- model / api 层：`model/evaluation.ts` 新增监控 schema（对齐 §4.2 JSON；维度动态数组）；`api/evaluation.api.ts` 新增 `listMonitorResources` / `getMonitorTrend`。

### 5.2 交互与展示约定

- 时间窗：默认近 7 天常量入 `web/src/constants/index.ts`（如 `EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS`）。查询传 `from/to` RFC3339；**禁止把"近 7 天"等行为数字硬编码在页面组件**。
- 趋势图沿用 native SVG（仓库无 recharts import 的事实约定），多线需区分色 + legend；不做 hover tooltip 的自定义重型图表（够用即可）。
- 空态文案明确区分：窗口无观测 / 该资源某维度无 judge 装配 / 无离线评测过程基线（§3.1、§4.2）。质量通过率用百分比、成本用 USD（千分位）、延迟用 ms、置信用 0-1 数字，量纲在表头/图例注明。
- 文案中文；错误通知 `message.error({content: err.response?.data?.error || '操作失败', duration: 3})`；成功/信息提示时长 ≤2；不用 `alert/confirm`、不提交 `console.log`；页面组件超 200 行时拆 hook/组件。
- kind 过滤与资源行联动沿用 `resourceOptions`（`displayLabel`）。

## 6. 错误处理与边界（合并 §4.5 后）

- 后端只读，无写路径、无状态变更、无不可逆操作。
- 观测量大时窗口内聚合力求在 DB 完成，避免把窗口明细拉到应用层求和（§4.4 已列 P95 方向）。
- 面板任何区数据缺失都以「空态」表达，禁止以 0 或默认值伪装无数据（§3.1）；这同时服务于诚实呈现 —— 质量区无样本 ≠ 质量差。
- `process_pass_rate` 对未做过程断言的 run 恒为 1.0（§2.3）—— 后端回传原始值，前端展示需带 run 元信息语境，避免误导为「过程全对」。

## 7. 测试策略

本改动为**前后端联调 + 数据库聚合查询**能力，走完整测试门槛（R3，`make test-verify-before-pr` 级，含 stateful e2e），不适用字段/单 bug 的最小验证。

### 7.1 后端（internal/evaluation）

- **aggregation repo 集成测试**（`persistence/query_repository_*_test.go` 同风格，真实/容器 PG + 双租户 fixture）：窗口边界、kind/resource_id 过滤、judge 数组展开正确性、rule/behavior/verdict 计数、cost P95 数值、`process:null` 分支、跨租户隔离（另一租户数据不串）。
- **application service**：QueryService 对 port 返回的透传与窗口/limit 兜底（mock repo，不 mock domain）。
- **handler**：参数校验 400 表（kind 非法、单传 resource_id、from>to）、200 形状。契约守护由 contract golden 扩展（见下）。

### 7.2 契约守护

新端点追加 `api/http/testdata/contracts/` golden（正例 + 校验失败反例），`api/http/contract_test.go` 同步挂新用例 —— 守护 HTTP JSON 形状与前端期望一致。

### 7.3 port/mock 同步

`port.CenterQueryRepository` 增方法 → 同步所有 test mock/stub（领域 mock + handler 接口 stub）；`evaluationQueryService` 接口增方法同步 handler 测试替身。仓库红线：改 port 后立即搜索并同步，禁止遗漏。

### 7.4 前端

- model schema 测试（解析 §4.2 响应样例、缺字段容错、资源行截断语义）。
- 组件行为测试（mock api）：主表渲染/空态、时间窗默认与变更触发重查、下钻抽屉数据加载与空态、维度动态渲染。
- 质量门禁：`make fe-lint`、`make fe-build`；无 `console.log/alert`。

### 7.5 系统验收（PR 前）

按 `.test/verification.yaml` 由 `stratum-e2e-tester`（封装 `stratum-e2e-development`）在 clean commit 上执行：构造含多资源、多观测（含 judge/rule/behavior/verdict 多样值）、至少一个 succeeded 评测 run 的租户数据 → 断言监控视图主表数字与后端 SQL 抽查一致、kind/时间窗过滤生效、下钻趋势与空态正确、跨租户不可见。所有登录/验证走无头浏览器。

## 8. 成功标准

1. `EvaluationCenterPage` 可见「监控」tab，默认近 7 天窗口渲染每被测资源四区摘要；kind / resource_id 过滤与父级联动生效。
2. 点击资源行下钻该资源四区时间趋势（质量三维通过率、行为计数、成本/延迟、run 过程基线点）。
3. 数字与「同一窗口直接 SQL 抽查」一致；judge 未装配、无评测 run 等场景呈现 §3.1 空态而非 0 假象。
4. 双租户 fixture 证明隔离（B 租户看不到 A 租户观测）。
5. 新端点 contract golden 守护；后端 integration / service / handler 测试、前端 model / 组件测试全绿。
6. 无 DDL / migration / 观测写入语义变更；只读聚合。

## 9. 分期与扩展点（本版本不做）

- 按 `param_version` / `stratum` 分层（观测 `param_version` 多 unknown 平台序，分层价值需先清洗口径）。
- 趋势桶粒度自适应（>30 天窗口自动放宽桶）；当前固定日桶。
- 资源行分页 / 虚拟滚动 / 更多排序键。
- 过程基线的「最近 N 次 run」迷你趋势替代「最近一次」单值。
- 面板指标与 Prometheus A 类 label 的对账视图 / 导出。
- 面板空态里引入「judge/rule 装配态」信号（当前无此存储）。

## 附：证据索引

| 证据 | 位置 |
|------|------|
| eval_observations DDL + 索引 | `pkg/storage/postgres/tenant_schema.sql:549-566` |
| eval_runs DDL + 索引 | `pkg/storage/postgres/tenant_schema.sql:488-514` |
| 观测信号类型 | `internal/evaluation/domain/observation.go` |
| judge 维度硬编码 | `internal/evaluation/application/observation_service.go:18` |
| applyJudge score 0/1 + flag 阈值 | `internal/evaluation/application/observation_service.go:263-334` |
| aggregateRunMetrics keys | `internal/evaluation/application/metrics.go:23-90`（process_pass_rate 语义注释） |
| RunSummary 无 metrics / Page 类型 | `internal/evaluation/domain/query.go:62-148` |
| EvalRun.Metrics map | `internal/evaluation/domain/evaluation.go:361` |
| ResourceKind 四枚举 | `internal/evaluation/domain/resource.go:13-32` |
| CenterQueryRepository port | `internal/evaluation/domain/port/evaluation.go` |
| evaluationQueryService 接口 | `api/http/handler/evaluation_handler.go:60-68` |
| observations 手写 query DTO + 明细端点 | `api/http/handler/evaluation_handler.go:695-737`、`api/http/router.go:198-199` |
| QueryService 装配 | `api/wiring/evaluation.go:45,1299` |
| EvaluationCenterPage tabs / health | `web/src/modules/evaluation/pages/EvaluationCenterPage.tsx:119-142` |
| 前端常量 | `web/src/constants/index.ts:91`（`EVALUATION_TREND_RUN_LIMIT` 模式） |
| A/B 类指标权威清单 | `docs/superpowers/specs/2026-08-28-evaluation-metrics-design.md` §10/§11 |
