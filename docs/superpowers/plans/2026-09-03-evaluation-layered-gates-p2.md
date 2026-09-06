# 评测分层门禁 P2 深化（spec T8/T9/T11/T10/T12）实现计划 —— master 组装单

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现本计划。Steps 用 `- [ ]` 跟踪。本文件是 P2 唯一权威实现计划，由五个独立 section（Task 1-5）按编译次序 **T8→T9→T11→T10→T12**（即 Task 1→2→3→4→5）组装而成；跨节冲突已由协调者按真实代码 seam 逐项裁决并就地统一（见「任务依赖与次序」的跨任务绑定、各 Task 内 `> 主会话裁决` 注释、末尾「主要风险与对策 / 开放问题」）。任务在隔离 worktree `/home/yang/go-projects/stratum-card-c-gate-p2`（分支 `feat/card-c-gate-p2`）内执行，每 Task 一个独立 commit（标题 `type(scope): description`，body 带 `Co-Authored-By: Claude <noreply@anthropic.com>`），禁止 main 直提交。**硬红线**：零 DDL / 零 proto / 零新 metric family（R32）；禁止 magic string（共享字面量进既有 domain 常量或 `pkg/constants/evaluation.go`）；`domain` 仅 stdlib + `pkg/constants`；`application` 不 import pgx/Redis/NATS/Gin/兄弟 context；改 port 后立即 grep 同步全部 test mock/stub；**监控规则单一源 = `monitoring/remote/rules/*.yaml`**（environment: remote-test），`monitoring/local/rules/*.yml`（production）与 `monitoring/remote/generated/stratum-prometheus-rules.yaml`（CRD）是渲染产物、禁止手编、必须同 commit，改源后两端重渲 + `--check` 防漂移。

**Goal:** 在 P1 地基（门禁 domain `Decide`/`GateTarget`/`port.GateStore` 契约/eval_state 列/常量）之上落地分层门禁 P2 深化：L2 run 级回归对照纯函数 + 评审池 behavior 判异分支 + 两条 L2 告警（Task 1）；L1 检测/执行分离（O4，检测恒开）+ `StratumEvalRuleDisabled`（Task 2）；L3 资源回滚 planner/executor + 窄 port + wiring ACL（agent/knowledge/skill/experiment/canary）（Task 3）；审批执行器三操作分支（rollback_platform / rollback_resource / publish_platform_version）+ platform auto 拒绝不变量 + eval_state 读路径（Task 4）；L3+ 发布哨兵协调 + memory 组纳入快照 capture（R30，用户 09-03 决定：P2 扩范围无条件并入 Execution）+ 多租户验证 worker + 分化/未恢复告警（Task 5）。

**Architecture:** 门禁判定继续是硬编码确定性规则阶梯（禁 LLM），本次把判定收敛到**纯函数/纯编排 + wiring 薄装配**：`regression_compare.go` 是零 IO 的 run 级回归纯函数（Task 1 定义、Task 5 哨兵与验证 worker 经 deps 注入复用）；`RuleGuard.Check` 把「检测」与「执行」解耦为同一热路径内两个判定点（只改 agent 侧，evaluation 侧 R21/E8 零改动）；L3 资源回滚以 planner 纯函数 + executor 分派（`domain/port/rollback.go` 窄面 + `api/wiring/resource_rollback.go` ACL 适配器，经 `Evaluation.ResourceRollbackExecutor` seam 供 Task 4/T13 复用）；审批执行器在 `evaluationOperations` map 上新增三个写 op，`publish_platform_version` 消费 Task 4 接线的 `eval_state` 读路径并前置断言 `sentinel_passed`；L3+ 发布哨兵是编排层产物——`publishGateCoordinator` 不调低层 `store.Publish`，只产出决策（直通/待审批/拒发/未接线拒发——memory 组与 agent/evaluation/trace 同路径，无特判），HTTP handler 据决策渲染，fail-closed 绝不静默直发。监控告警统一走 `monitoring/remote/rules/*.yaml` 单一源 → render 出本地 `.yml` 与远端 CRD 双产物（同 commit，`--check` 防漂移）；runbook anchor 同 commit 进 `docs/operations/alerts/stratum-evaluation.md`。

**Tech Stack:** Go 1.25.12（module `github.com/byteBuilderX/stratum`）、pgx v5、Gin、zap、go-redis / NATS / Milvus 仅既有消费、`observability.MetricsProvider`（CounterVec label 开放）、`promtool`（告警规则测试）、`scripts/quality/render-monitoring-rules.sh` + `monitoring-runbook-test.go`（监控质量闸）。本 P2 **零 DDL、零 proto、零新 metric family**。

## P2 追加裁决记录表（R17-R32，先前逐字裁决，实现必须遵守）

| # | 裁决（2026-09-03 修订定稿） |
|---|---|
| R17 | P2 plan Task 顺序 = T8→T9→T11→T10→T12；T11 先于 T10（rollback_resource 审批分支复用 T11 executor）。T8/T9 相互独立、可与 T11 并行起。 |
| R18 | T8 两条告警信号已核实（spec §3.2-① L206）：`StratumEvalJudgeBelowThreshold` 对 `eval_gate_action_total{layer="detect",action="flag"}`（judge 维）速率 **加** `eval_judge_score` 直方图尾部占比（现网即燃——judge 跌阈 emit detect/flag 已存在）；`StratumEvalRunRegression` 选 `eval_gate_action_total{layer="l2",action="regression"}`（新 label 值；emit 点 = T12 哨兵判劣，T13 确认 run 复用；rule 先落、runbook 注明 P2 仅哨兵源）。两条 severity 均 **warning**。T8 只写规则+runbook，不实现 emit。 |
| R19 | behavior_anomaly：`TriggerBehaviorAnomaly ReviewTriggerReason="behavior_anomaly"` 并入 `Valid()`；`RiskLevel()` = **medium** → persistence `reviewRiskOrderSQL()` 同步加 `WHEN 'behavior_anomaly' THEN 1`（review_repository.go 两端镜像，改一侧必须同步另一侧）。DDL CHECK P1 T2 已含 → **P2 零 DDL**。`TriggersForObservation` 加 behavior 分支：`Signals.Behavior.Abandonment||Escalation` 且 verdict flag 时触发（保留 judge early-return 语义——无 judge 的 rule-block-only 观测仍不进池，勿破坏现状）。 |
| R20 | L1 metric 判别（核实 guard 现码 L72-73 写 `IncEvalRuleHit("tool_denylist","agent","block")` + `IncEvalGateAction("rule_guard","block")`；spec §3.1 L198 计数目标是 `layer="l1_rule",action="block"`）：任一 denylist 命中（不论 enabled）→ `IncEvalRuleHit("tool_denylist","agent", verdict)`，verdict = enabled ? `"block"` : `"detected"`（block 匹配既有 StratumEvalRuleBlocked、detected 匹配新 StratumEvalRuleDisabled）；**仅** enabled && hit → `IncEvalGateAction("l1_rule","block")` + 返回 `RuleBlockedError`（拦截）。`eval_rule_hit_total` 每命中 +1，判别由 verdict label 承担；gate_action 仅拦截 +1。guard 现写 `layer="rule_guard"` → 改 `"l1_rule"`（动手前 grep `monitoring/` `grafana/` 中 `rule_guard`，预期零消费；找到则同步改）。`enabled==nil` 视为 false。 |
| R21 | **O4 观测恒产、零契约改动**（已核实：collector 现仅 enabled&&hit 填 → 这正是 P2 要改变的行为；evaluation 侧 `applyAnomalyVerdict` rule→VerdictBlock L310-314 无条件、是规范意图，**不改**）：`RuleGuard.Check` 命中即写 ctx 累积器（`ruleBlockCollectorKey`），使 disabled 命中同样产出含 rule 信号的观测（spec §3.1 O4「检测恒开：命中即产 block 观测，不依赖执行开关」）。agent port / evaluation 双份契约镜像 / golden **零改动**，`RuleSignalPayload` 不加字段。副作用（写进本 master 风险节，供 reviewer/用户知情）：enabled=false 且 denylist 非空时命中工具的 run 观测变 VerdictBlock、`eval_behavior_anomaly_total{signal="rule_block"}` 与 `eval_gate_action_total{layer="detect",action="block"}` 上涨——spec O4 接受的显式新行为（告警判别不受污染：RuleBlocked 匹配 verdict=block，disabled 命中 verdict=detected 只触 RuleDisabled）。 |
| R22 | `StratumEvalRuleDisabled`（T9）：expr 见 dict F；severity warning；runbook 指向开启 `evaluation.ruleguard.enabled`（registry `internal/parameters/domain/registry.go` 平台级 low risk_tier）。 |
| R23 | T9 `RuleGuard.Check` 新契约（**签名不变** `(*RuleBlockedError, bool)`；唯一调用点 `tool_execution_guard.go` L44-52 语义不变）：denylist 空/空列表→`(nil,false)` 零命中；命中判定沿用 `strings.EqualFold(strings.TrimSpace(...))`（L62-66）；命中→(a) 恒写 ctx 累积器；(b) `IncEvalRuleHit` 判别 verdict；(c) enabled && hit → `IncEvalGateAction("l1_rule","block")` + 返回拦截错误；否则 `(nil,false)` 放行（但检测/观测照常）。`RuleGuardDeps` 结构、参数注册 `registry.go:490-508` 与 wiring `api/wiring/agent.go:617-644` 不动。`emitObservation`（agent_service.go）不改（数据源=ctx 累积器，已恒产）。 |
| R24 | T11 分层：planner（纯函数）+ executor（实现 `port.ResourceRollbackExecutor`）落 `internal/evaluation/application/resource_rollback.go`；窄 port 新增于 `internal/evaluation/domain/port/`（新文件 `rollback.go`）；禁止 import agent/knowledge/skill/mcp infra。上一好 = 非当前 active 的最高序已发布版本（各 kind 语义按 E3）。 |
| R25 | T11 executor 按 `target.Scope==ScopeResource` + `Kind` 分派 agent/knowledge/skill/experiment（真实入口签名 E3，作者照抄）；mcp 或未知 kind → 返回 `port.ErrRollbackUnsupported`（与 `ErrAutoRollbackForbidden` 一并放 `domain/port/gate.go`）。窄 port 由 `api/wiring` ACL 适配（`api/wiring/resource_rollback.go`，装配到 `Evaluation.ResourceRollbackExecutor` seam，可被 T10/T13 复用）。返回错误统一带上下文。 |
| R26 | T10 `evaluationOperations` map（`api/wiring/approval_action.go:74-86`）新增三 key：`rollback_platform`→`executePlatformRollback`（→ parameters `Service.Rollback`；首行 guardNoAutoRollback）；`rollback_resource`→`executeResourceRollback`（→ T11 `ResourceRollbackExecutor.Rollback`）；`publish_platform_version`→`executePlatformPublishGated`（→ parameters `Service.Publish` + `UpdateEvalState(sentinel_passed)`）。分支函数仿 `executeRollbackExperiment`（L209 附近）。失败 `notExecuted`（单事务无副作用可重试）；平台 actor=`req.DecidedBy`、不带 tenant。Arguments 契约：`rollback_platform`/`publish_platform_version` 带 `group_key` + `version_id`（**行 PK id，与 HTTP path `:versionID` 同语义**，非 version_seq）；`rollback_resource` 带 `resource_kind` + `resource_id` + `target_revision_id`（+ 可选 `version_id`）。 |
| R27 | T10 wiring：`newApprovalActionExecutor`（L36-53）注入 parameters `*parametersapp.Service`（public，无 tenant）+ T11 resource rollback executor（`c.Evaluation.ResourceRollbackExecutor` seam）；装配点 `api/wiring/evaluation.go:1386-1390`（parameters 先就绪），paramSvc 注入前做 typed-nil 判定。 |
| R28 | auto 不变量（spec §3.4 L255）：`ErrAutoRollbackForbidden` 新增于 `domain/port/gate.go`。`executePlatformRollback` 首行 guard：请求意图为 auto（Arguments 显式 `auto=true`）→ 返回 sentinel + `IncEvalGateAction("l3_platform","auto_refused")`；测试守护；wiring 不提供平台 auto 分支。 |
| R29 | T12 宿主租户（O2）：参数 `/admin` 写链（Publish/Rollback）补 `InjectTenantContext` + `RequireDefaultTenant` → reqctx tenantID = host tenant，固化到哨兵 suite 载荷与后续验证 job 载荷。宿主租户解析失败 → 拒绝发布（fail-closed）。 |
| R30 | **memory 组偏差（用户 09-03 已决：P2 扩范围，无条件纳入 memory 组入快照 capture；不再 fail-closed 拒发、不再归 T13）**：代码现实 = capture 原只覆盖 evaluation/agent/trace、evaldomain 快照常量无 `GroupMemory`（parameters registry 有全部 4 组）。决定：`internal/evaluation/domain/snapshot.go` 加 `GroupMemory="memory"`；`api/wiring/evaluation_snapshot.go` Capture 无条件捕获 memory 组、后置追加进 `Execution`（恒 `[agent, trace, memory]` 三组）；`publishGateCoordinator` 对 memory 组**不再特判**（删 `GroupMemoryPlatform`/`DecisionRefusedMemory`/`ErrMemoryGroupSentinelUnsupported`），与 agent/evaluation/trace 同路径——enabled=false 直通（默认，行为不变）；enabled=true 时经 ResolveVersion→draft 检查→SentinelSpec nil → `refused_not_wired`（全组一致）。零 DDL（memory 组 + 22 个 memory.* 平台键迁移 043 已 seed；context_snapshot JSONB 追加合法）。哨兵对 draft 的真实执行消费属 T13 完成环（所有组一致），P2 只建归因基座 + 门骨架 + fail-closed。 |
| R31 | T12 验证 worker：复用 review `RefreshBacklog` 的租户枚举模式（`TenantIDs` = IAM `ListActiveTenantIDs`，wiring evaluation.go:585-590；IAM nil→仅宿主租户）+ dedupe + per-tenant fail-open + bounded worker 骨架（`worker.go` `NewWorker/PollOnce` + 租户 lister `evaluationTenantLister`）。宿主租户来自动作载荷。计数：哨兵 `eval_gate_action_total{layer="l3_sentinel",action="block"|"pass"}`；门`{layer="l3_platform",action="publish_gated"|"publish_blocked"|"auto_refused"|"rollback_manual"}`；多租户`{layer="l3_multitenant_verify",action="queued"|"recovered"|"not_recovered"}`；run 级劣化`{layer="l2",action="regression"}`（哨兵判劣时与 l3_sentinel block 同发）。 |
| R32 | 指标纪律（dict B）：全 P2 **不新增 metric family**（不加 prometheus.go 注册）；全消费现有 `eval_rule_hit_total`/`eval_gate_action_total`/`eval_judge_score`/`eval_behavior_anomaly_total` + 新 label 值（CounterVec label 开放，无需注册）。 |

## Global Constraints（逐字，自 P2 共享契约字典 B）

- 禁止 main 直提交/直推送：P2 在 `feat/card-c-gate-p2`（worktree `/home/yang/go-projects/stratum-card-c-gate-p2`），每 Task 一个独立 commit。
- 决策确定性：门禁判定硬编码常量/阈值/纯函数，禁 LLM。新行为数字进 `pkg/constants/evaluation.go`，禁止内联。**禁 magic string**：layer/action/trigger_reason 等共享字面量集中命名常量（既有 `domain` 常量或新增 const 块），跨包引用禁止散写。
- Go 质量门禁：圈复杂度 ≤10、认知 ≤15、函数 ≤120 行、嵌套 ≤4、行宽 ≤120；写码前 `bash scripts/quality/risk-regression-guard.sh --explain`，PR 前 `make risk-guardrails`。
- DDD 分层：`domain` 仅 stdlib + `pkg/constants`；`application` 不 import pgx/Redis/NATS/Gin，也**不 import 兄弟 context 的 application/infrastructure**。evaluation 新增跨 context 接口一律 `internal/evaluation/domain/port/`（consumer 定义），provider 由 `api/wiring` ACL 适配。evaluation domain 自带 `Scope` 类型，禁 import parameters domain.Scope。
- fail-open 纪律：门禁 hook / 审批执行失败只日志 + 指标 + 分类，不阻断主流程。审批执行器失败分类沿用 `notExecuted`/`unknown_outcome`。
- tenant-scoped 存储：访问 tenant 表 repository 必须经 `execTenant` 且 port 方法显式 `tenantID`；public 存储用 schema-qualified 名。修改既有 port 接口后**立即 grep 同步所有 test mock/stub**。
- Commit：标题 `type(scope): description` + body `Co-Authored-By: Claude <noreply@anthropic.com>`。
- 告警质量闸：`monitoring/local/rules/stratum-evaluation.yml` 每条新规则恰 1 个可解析 `runbook_url`，runbook anchor 同 commit 进 `docs/operations/alerts/stratum-evaluation.md`（`scripts/quality/monitoring-runbook-test.go` 守卫）。yml 转义沿用既有（`&gt;`）写法。
  - > **A1 澄清（master 定稿，优先于上述字面）**：仓库**规则单一事实源 = `monitoring/remote/rules/*.yaml`（environment: remote-test）**；`monitoring/local/rules/*.yml`（environment: production）与 `monitoring/remote/generated/stratum-prometheus-rules.yaml`（PrometheusRule CRD）均为 `scripts/quality/render-monitoring-rules.sh remote-test|local` 的渲染产物，禁止手编。改源后必须两端重渲并**同 commit**，再跑 `render-monitoring-rules.sh <remote-test|local> --check` 防漂移。runbook guard 用法：`go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .`（两参）。
- 验证门槛：PR 前系统验收由 `stratum-e2e-tester`（stratum-e2e-development）按 `.test/verification.yaml` 定级；E2E 仅无头。P2 无 DDL、无 proto 改动。
- 指标纪律：P2 **不新增 metric family**（不加 prometheus.go 注册）；全消费现有 `eval_rule_hit_total`/`eval_gate_action_total`/`eval_judge_score`/`eval_behavior_anomaly_total` + 新 label 值（CounterVec label 开放，无需注册）。
- 模块路径 `github.com/byteBuilderX/stratum`；Go 以 go.mod 为准（1.25.12）。

## 任务依赖与次序

| scratch 文件 | master Task | 任务 | 属 spec | 主要裁决 |
|---|---|---|---|---|
| `.superpowers/plans-scratch/task-T8.md` | **Task 1** | L2 run 级回归 + 评审池 behavior 分支 + 两条告警 | §3.2、§4.3.5/4.3.6 | R17-R19 + A5 |
| `.superpowers/plans-scratch/task-T9.md` | **Task 2** | L1 检测/执行分离 + StratumEvalRuleDisabled | §3.1（O4） | R20-R23 + A2/A3/A4 |
| `.superpowers/plans-scratch/task-T11.md` | **Task 3** | L3 资源回滚 planner/executor + wiring ACL | §3.3、§4.3.3 | R24/R25 + A8 |
| `.superpowers/plans-scratch/task-T10.md` | **Task 4** | 审批执行器三分支 + platform auto 拒绝 + eval_state 读路径 | §3.4、§4.4 | R26-R28 + A6 |
| `.superpowers/plans-scratch/task-T12.md` | **Task 5** | L3+ 发布哨兵协调 + 多租户验证 worker + 分化告警 | §3.4 | R29-R31 + A7/A1 修正 |

**执行顺序 = 上述 Task 1→5 串行（每 Task 独立 commit；Task 1 与 Task 2、Task 3 相互独立，可在各自分支并行起，但按序合入本分支）。** 依赖链：

- Task 1（T8）零前置依赖；Task 2（T9）零前置依赖（agent 侧，不与 Task 1/3 共享写文件）。
- Task 3（T11）零前置依赖；但其 Produces（6 参 `Rollback` + 2 sentinel + `Evaluation.ResourceRollbackExecutor` seam）是 Task 4 的硬前置。
- Task 4（T10）依赖 Task 3 合并后状态；本 Task 自含 `PlatformVersion.EvalState` 读路径 + `pkg/constants` 三个共享常量（见「跨任务绑定 B」）。
- Task 5（T12）依赖 Task 1（`CompareRunRegression` + `FindLatestCompletedRunForResource`）+ Task 4（`eval_state` 读路径 + `sentinel_passed` 写回语义 + 常量）合并后状态。

**跨任务绑定（master 统一，实现时必须遵守）：**

- **A. `Compare` 消费签名 vs Task 1 纯函数导出（裁决保留两边、wiring 适配器衔接）**：Task 1 产出纯函数 `func CompareRunRegression(baseline, current *domain.EvalRun) *domain.RunComparison`（永不为 nil，无 error）；Task 5 两个 deps（`PublishGateDeps.Compare`、`MultiTenantVerifyDeps.Compare`）签名均为 `func(baseline, current *domain.EvalRun) (domain.RunComparison, error)`。**不重写 Task 1 函数语义**；Task 5 的 wiring 绑定点（B4 的 `newPublishGateCoordinator` 与 C5 的验证 worker）用统一适配器衔接：

  ```go
  // runCompareAdapter 把 Task 1 纯函数（*RunComparison，永不为 nil）适配为 deps 的
  // (RunComparison, error) 签名。定义于 api/wiring/publish_gate.go，B4 与 C5 复用。
  func runCompareAdapter(baseline, current *evaldomain.EvalRun) (evaldomain.RunComparison, error) {
      return *evalapp.CompareRunRegression(baseline, current), nil
  }
  ```

- **B. `l3_platform` / `auto_refused` / `sentinel_passed` Go 常量单一归属 = `pkg/constants/evaluation.go`（Task 4 定义；Task 5 消费，不重复定义）**：compile order 决定 Task 4（T10）先于 Task 5（T12）合入，故这三个值由 Task 4 Step 4 在 `pkg/constants/evaluation.go` 定义（`GateLayerL3Platform`/`GateActionAutoRefused`/`PlatformEvalStateSentinelPassed`）；Task 5 的 `domain/publish_gate_const.go` **不重复定义** `LayerL3Platform` 与 `EvalStateSentinelPassed`，其编排代码引用 `constants.GateLayerL3Platform` / `constants.PlatformEvalStateSentinelPassed`（见 Task 5 B1/B2 inline 修正）。Task 5 独有的 `l2`/`l3_sentinel`/`l3_multitenant_verify`/全部 action/`sentinel_failed` 仍在 domain const 文件。此点**与裁决 A7.2「T12 拥有常量」的偏差是 compile order 强制**，已在「开放问题」标注。另：`GroupMemoryPlatform` 已删除——memory 组常量唯一 home = `internal/evaluation/domain/snapshot.go` `GroupMemory`（Sub-commit A0 定义，与 parameters 域同值）。
- **C. `FindLatestCompletedRunForResource` 过滤语义（A5.2，Task 1 定义、Task 5 消费）**：按 `Kind + ResourceID + suiteRevisionID` 过滤、**不含 `ref.RevisionID`**，取最近一条 `status='succeeded'` run（无 → nil,nil）。Task 5 哨兵比较的是「当前 published seq run vs 草案 seq run」——跨 `version_seq`、同 resource+suite revision，而非跨资源 revision。
- **D. 多租户验证入队调用点（开放问题 #2）**：spec §3.4-3「平台回滚成功后自动入队 `EnqueueMultiTenantVerify`」的调用点本应在 Task 4 `executePlatformRollback` 成功路径，但 Task 4 先于 Task 5 合入、无法引用 Task 5 交付的函数；且生产 wiring 中 `executePlatformRollback` 仅经审批流（T13 `GateApprovalRequester` 创建）可达。**裁决：入队调用点延迟到 T13**；Task 5 交付幂等 enqueue 函数 + worker + 判定 + 计数供 T13 接线，P2 无调用者属安全（无副作用、无生产者）。
- **E. 告警规则单一源 = remote yaml（A1）**：Task 1/2/5 三节中所有「改 yml」表述一律以「改 `monitoring/remote/rules/stratum-evaluation.yaml`（environment: remote-test）+ render 双产物」为准；渲染产物（`monitoring/remote/generated/stratum-prometheus-rules.yaml`、`monitoring/local/rules/stratum-evaluation.yml`）同 commit，禁止手编。

---

## Task 1（spec T8）: L2 确认 run 回归判定（regression_compare）+ 评审池 behavior 判异分支 + StratumEvalRunRegression/JudgeBelowThreshold 告警

> spec §3.2 + §4.3.5/4.3.6

本 Task = master P2 Task 1（spec T8）。落点裁决：R17（次序）/R18（两条告警 severity warning，T8 只写规则+runbook，不实现 emit）/R19（behavior_anomaly medium + reviewRiskOrderSQL 镜像 + TriggersForObservation behavior 分支保留 judge early-return）；范围见 recon T8 R-E/R-F。零 DDL（`eval_review_items.trigger_reason` CHECK 已含 `'behavior_anomaly'`，P1 T2 已加，见 `pkg/storage/postgres/tenant_schema.sql` CREATE 与 DROP/ADD 幂等升级块）；零 proto；零新增 metric family（alert expr 只消费现有 label 值）。

**Files:**

- Create: `internal/evaluation/application/regression_compare.go`（run 级回归纯函数，spec §4.3.6）
- Create: `internal/evaluation/application/regression_compare_test.go`
- Modify: `internal/evaluation/domain/review_pool.go`（`TriggerBehaviorAnomaly` + `Valid()` + `RiskLevel()`=medium + `TriggersForObservation` behavior 分支，§4.3.5）
- Modify: `internal/evaluation/domain/review_pool_test.go`（behavior 分支子用例 + RiskLevel 表行）
- Modify: `internal/evaluation/infrastructure/persistence/review_repository.go`（`reviewRiskOrderSQL()` 加 `WHEN 'behavior_anomaly' THEN 1`，与 domain `RiskLevel()` 两端镜像）
- Modify: `internal/evaluation/infrastructure/persistence/review_repository_test.go`（`TestPgReviewRepositoryListItems` 的精确 SQL 期望串加同一 WHEN，见 R19 镜像）
- Modify: `internal/evaluation/domain/port/evaluation.go`（`RunRepository` 加 E6 `FindLatestCompletedRunForResource`）
- Modify: `internal/evaluation/infrastructure/persistence/run_repository.go`（`PgRunRepository` 实现该查询）
- Modify: `internal/evaluation/application/service_test.go`（`fakeRunRepo` 补方法满足 port，编译强制同步）
- Modify: `internal/evaluation/infrastructure/persistence/run_repository_mock_test.go`（新方法 found/notFound 用例）
- Modify: `monitoring/remote/rules/stratum-evaluation.yaml`（两条告警；**仓库渲染架构事实：规则源 = `monitoring/remote/rules/*.yaml`（environment: remote-test）；两个 commit 产物均由 renderer 生成：`monitoring/remote/generated/stratum-prometheus-rules.yaml`（remote-test → PrometheusRule CRD）与 `monitoring/local/rules/*.yml`（local → standalone，remote-test 替换为 production）。改源后必须两端重渲并同 commit**；dict B 所述本地 yml 即渲染产物之一）
- Modify: `monitoring/remote/generated/stratum-prometheus-rules.yaml`（6b `render-monitoring-rules.sh remote-test` 生成的 CRD 产物，同 commit）
- Modify: `docs/operations/alerts/stratum-evaluation.md`（两条 runbook section + anchor，恰 1 个 `runbook_url` 可解析，`monitoring-runbook-test.go` 守卫）

**Interfaces:**

- Consumes：
  - E1 `domain.RunComparison{Regressed bool; BaselineSeq, ConfirmedSeq int64; DimensionDeltas map[string]float64}`（`internal/evaluation/domain/gate.go` L61-66）；常量 `pkg/constants/evaluation.go` `RunRegressionDeltaThreshold = -0.05`（L148）。
  - run 落库指标结构：`aggregateRunMetrics` 输出 `metrics.by_dimension` → 每维 `map[string]any{avg_score, pass_rate, samples}`（`internal/evaluation/application/metrics.go` `aggregateByDimension` L95-125；`avg_score` 为维度 score 均值，本次判异基准，见「主会话裁决」①）。
  - E7 `domain.EvalObservation.Verdict`/`VerdictFlag`/`BehaviorSignals{Retry,Escalation,Abandonment}`（`internal/evaluation/domain/observation.go`）；`TriggersForObservation(obs *EvalObservation, cfg ReviewConfig) []ReviewTriggerReason` 现形态（`review_pool.go` L163-179）。
  - E6 `RunRepository` 现契约 `SaveRun/GetRun`（`internal/evaluation/domain/port/evaluation.go` L73-76）；`domain.ResourceRef{Kind ResourceKind; ResourceID, RevisionID string}`。
  - 观测判异 emit 现状（只读核对，不改）：`applyAnomalyVerdict`（`observation_service.go` L312-332）已 `IncEvalGateAction("detect", "block"|"flag")` —— R18 第一条告警的第一腿 `eval_gate_action_total{layer="detect",action="flag"}` 现网即燃。
- Produces：
  - application 纯函数 `func CompareRunRegression(baseline, current *domain.EvalRun) *domain.RunComparison`（回归判定；永不为 nil。Task 5 哨兵/T13 确认 run 消费——wiring 侧以 `(RunComparison, error)` 适配器衔接，见「任务依赖与次序」跨任务绑定 A。T8 不 emit）。
  - E6（Task 5 消费）：`RunRepository.FindLatestCompletedRunForResource(ctx context.Context, tenantID string, ref domain.ResourceRef, suiteRevisionID string) (*domain.EvalRun, error)`，无 → `(nil, nil)`。
  - E7 `TriggerBehaviorAnomaly ReviewTriggerReason = "behavior_anomaly"`，并入 `Valid()`；`RiskLevel()` medium；`reviewRiskOrderSQL()` `WHEN 'behavior_anomaly' THEN 1`。
  - F 表两条告警（expr 逐字）：
    - `StratumEvalJudgeBelowThreshold`（warning）＝ `increase(eval_gate_action_total{layer="detect",action="flag"}[15m]) > 0 OR (sum(rate(eval_judge_score_bucket{le="0.5"}[10m])) / clamp_min(sum(rate(eval_judge_score_count[10m])), 1)) > 0.3`
    - `StratumEvalRunRegression`（warning）＝ `increase(eval_gate_action_total{layer="l2",action="regression"}[15m]) > 0`（`{layer="l2",action="regression"}` 为 P2 新增 label 值，emit 点 = Task 5 哨兵判劣，T13 确认 run 复用；rule 先落、runbook 注明 P2 仅哨兵源）。

---

- [ ] **Step 1（test-first）: `regression_compare_test.go` 先行 → 预期 FAIL（undefined: CompareRunRegression）**

Create `internal/evaluation/application/regression_compare_test.go`：

```go
package application

import (
	"math"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// dimScores 把每维 avg_score 构造成 run.Metrics["by_dimension"] 的真实形态
// （metrics.go aggregateByDimension：{avg_score, pass_rate, samples}）。
func dimScores(m map[string]float64) map[string]any {
	out := make(map[string]any, len(m))
	for name, score := range m {
		out[name] = map[string]any{"avg_score": score, "pass_rate": 1.0, "samples": 1}
	}
	return map[string]any{"by_dimension": out}
}

func runWithMetrics(m map[string]float64) *domain.EvalRun {
	return &domain.EvalRun{ID: "r", Metrics: dimScores(m)}
}

func TestCompareRunRegression(t *testing.T) {
	cases := []struct {
		name       string
		baseline   *domain.EvalRun
		current    *domain.EvalRun
		wantReg    bool
		wantDeltas map[string]float64
	}{
		{
			name:       "degradation below threshold flags regression",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.9, "relevance": 0.8}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.84, "relevance": 0.8}),
			wantReg:    true,
			wantDeltas: map[string]float64{"faithfulness": -0.06, "relevance": 0.0},
		},
		{
			name:       "delta at threshold boundary is not regression",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.9}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.85}),
			wantReg:    false,
			wantDeltas: map[string]float64{"faithfulness": constants.RunRegressionDeltaThreshold},
		},
		{
			name:       "improvement and flat deltas are not regression",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.8}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.95}),
			wantReg:    false,
			wantDeltas: map[string]float64{"faithfulness": 0.15},
		},
		{
			name:       "dimension missing in baseline is skipped",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.9}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.8, "relevance": 0.1}),
			wantReg:    true,
			wantDeltas: map[string]float64{"faithfulness": -0.1},
		},
		{
			name:       "nil run yields empty comparison",
			baseline:   nil,
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.8}),
			wantReg:    false,
			wantDeltas: map[string]float64{},
		},
		{
			name:       "missing by_dimension node yields empty comparison",
			baseline:   &domain.EvalRun{ID: "a"},
			current:    &domain.EvalRun{ID: "b", Metrics: map[string]any{"pass_rate": 0.5}},
			wantReg:    false,
			wantDeltas: map[string]float64{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareRunRegression(tc.baseline, tc.current)
			if got.Regressed != tc.wantReg {
				t.Fatalf("Regressed = %v, want %v", got.Regressed, tc.wantReg)
			}
			if len(got.DimensionDeltas) != len(tc.wantDeltas) {
				t.Fatalf("DimensionDeltas = %v, want %v", got.DimensionDeltas, tc.wantDeltas)
			}
			for dim, want := range tc.wantDeltas {
				gotDelta, ok := got.DimensionDeltas[dim]
				if !ok {
					t.Fatalf("DimensionDeltas missing dim %q in %v", dim, got.DimensionDeltas)
				}
				if math.Abs(gotDelta-want) > 1e-9 {
					t.Fatalf("delta[%q] = %v, want %v", dim, gotDelta, want)
				}
			}
		})
	}
}
```

Run: `go test ./internal/evaluation/application/ -run TestCompareRunRegression`
Expected: **FAIL / compile error** `undefined: CompareRunRegression`（符号未定义即为测试先行的 FAIL 预期）。

- [ ] **Step 2: 实现 `regression_compare.go` → PASS**

Create `internal/evaluation/application/regression_compare.go`：

```go
package application

import (
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// CompareRunRegression 比较同 suite 两个 run（baseline vs current）的 run 级回归
// （spec §3.2-② / §4.3.6）：对两 run 各自 metrics.by_dimension 的每维 avg_score 求
// current − baseline 的 delta，任一 delta 跌破 RunRegressionDeltaThreshold 即判劣化
// （Regressed=true）。纯函数 + 硬编码阈值，供确认 run 对照与发布哨兵 verdict 复用
// （Task 5/T13 emit，T8 不 emit）。
// 仅两 run 都出现的维度参与（基版缺某维度 = 该维度无从对比，跳过，避免误判）；delta
// 恒为 current − baseline，负值 = 劣化。BaselineSeq/ConfirmedSeq 由调用方（Task 5/T13
// 哨兵）按 run 实际平台版本锚点填充，本函数不解析 metrics.version（保持纯函数不依赖
// JSON 取数；seq 零值由调用方覆盖）。返回永不为 nil（空比较亦为 non-nil 结构）。
func CompareRunRegression(baseline, current *domain.EvalRun) *domain.RunComparison {
	comp := &domain.RunComparison{DimensionDeltas: map[string]float64{}}
	if baseline == nil || current == nil {
		return comp
	}
	base := dimensionAvgScores(baseline.Metrics["by_dimension"])
	cur := dimensionAvgScores(current.Metrics["by_dimension"])
	for dim, curScore := range cur {
		baseScore, ok := base[dim]
		if !ok {
			continue
		}
		delta := curScore - baseScore
		comp.DimensionDeltas[dim] = delta
		if delta < constants.RunRegressionDeltaThreshold {
			comp.Regressed = true
		}
	}
	return comp
}

// dimensionAvgScores 从 run metrics 的 by_dimension 节点提取每维 avg_score（结构见
// metrics.go aggregateByDimension 输出 {avg_score, pass_rate, samples}）。非预期类型或
// 缺 avg_score 的维度跳过（数据缺失不参与对比）。JSONB round-trip 后数字为 float64。
func dimensionAvgScores(v any) map[string]float64 {
	byDim, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]float64, len(byDim))
	for name, entry := range byDim {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if score, ok := m["avg_score"].(float64); ok {
			out[name] = score
		}
	}
	return out
}
```

Run: `go test ./internal/evaluation/application/ -run TestCompareRunRegression`
Expected: **PASS**（6/6 子用例）。

- [ ] **Step 3（test-first）: 评审池 behavior 分支 — 测试先行 → 预期 FAIL**

在 `internal/evaluation/domain/review_pool_test.go` 的 `TestTriggersForObservation` 内、`t.Run("no judge signals yields no triggers", ...)` 之后插入（`TriggerBehaviorAnomaly` 尚未定义时本文件与现有全部 domain 用例编译 FAIL，即 FAIL 预期）：

```go
	t.Run("behavior abandonment with flag verdict triggers behavior_anomaly", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Abandonment = true
		if got := TriggersForObservation(o, cfg); len(got) != 1 || got[0] != TriggerBehaviorAnomaly {
			t.Fatalf("got %v, want [behavior_anomaly]", got)
		}
	})

	t.Run("behavior escalation with flag verdict triggers behavior_anomaly", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Escalation = true
		if got := TriggersForObservation(o, cfg); len(got) != 1 || got[0] != TriggerBehaviorAnomaly {
			t.Fatalf("got %v, want [behavior_anomaly]", got)
		}
	})

	t.Run("behavior signals without flag verdict do not trigger", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictBlock
		o.Signals.Behavior.Abandonment = true
		if got := TriggersForObservation(o, cfg); containsReason(got, TriggerBehaviorAnomaly) {
			t.Fatalf("got %v, want no behavior_anomaly (block 无 judge 仍不进池)", got)
		}
	})

	t.Run("retry signal alone does not trigger behavior_anomaly", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Retry = true
		if got := TriggersForObservation(o, cfg); containsReason(got, TriggerBehaviorAnomaly) {
			t.Fatalf("got %v, want no behavior_anomaly", got)
		}
	})

	t.Run("behavior_anomaly accumulates with judge triggers when both present", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Abandonment = true
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 1.0, Confidence: 0.4}}
		got := TriggersForObservation(o, cfg)
		if !containsReason(got, TriggerBehaviorAnomaly) || !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want [behavior_anomaly low_confidence]", got)
		}
	})
```

在 `TestReviewTriggerReasonRiskLevel` 的 `cases` 中 `{TriggerNeedsReview, ReviewRiskMedium},` 行后插入：

```go
		{TriggerBehaviorAnomaly, ReviewRiskMedium},
```

Run: `go test ./internal/evaluation/domain/ -run 'TestTriggersForObservation|TestReviewTriggerReasonRiskLevel'`
Expected: **FAIL / compile error** `undefined: TriggerBehaviorAnomaly`。

- [ ] **Step 4: 实现 `review_pool.go` + `reviewRiskOrderSQL()` 镜像 + 修正 SQL 期望串 → PASS**

**4a.** `internal/evaluation/domain/review_pool.go` — 按序做以下 5 处精确替换（old = 当前文件原样，逐字）：

① 常量组（在现有 5 个 trigger 常量之后追加 `TriggerBehaviorAnomaly` 与注释；`TriggerProcessOutputConflict` 行保留不动）：

```go
	// TriggerBehaviorAnomaly 行为判异入池：Signals.Behavior 含 abandonment/escalation 且
	// Verdict=flag（spec §3.2-③）。trigger_reason 枚举 DDL P1 T2 已含，P2 零 DDL。
	TriggerBehaviorAnomaly ReviewTriggerReason = "behavior_anomaly"
```

② `Valid()` switch 的 case 行 替换为（`TriggerProcessOutputConflict` 后追加）：

```go
	case TriggerLowConfidence, TriggerDimensionSplit, TriggerJudgeRuleConflict, TriggerNeedsReview,
		TriggerProcessOutputConflict, TriggerBehaviorAnomaly:
```

③ `RiskLevel()` doc 注释的 medium bullet 行 替换为：

```go
//   - medium：low_confidence、dimension_split、needs_review、behavior_anomaly；
```

④ `RiskLevel()` switch 的 medium case 行 替换为（追加 `TriggerBehaviorAnomaly`；两端镜像注释指 `reviewRiskOrderSQL()`，见 4b）：

```go
	case TriggerLowConfidence, TriggerDimensionSplit, TriggerNeedsReview, TriggerBehaviorAnomaly:
		return ReviewRiskMedium
```

⑤ `TriggersForObservation` 整体替换（见下方独立代码块）。

`TriggersForObservation` 整体替换为（保留 rule-block-only 不进池语义：verdict 守卫使无 judge 的 `VerdictBlock` 观测 triggers 恒空；judge early-return 从「直接 nil」改成「返回已累积的 behavior 触发」，judge 分支行为不变）：

```go
// TriggersForObservation 计算观测应入池的触发原因（空 = 不进池）。纯函数，硬编码规则。
// 规则（spec §6.6 + §3.2-③）：
//  1. low_confidence：任一 judge 维度 Confidence < cfg.LowConfidenceThreshold，
//     或 Confidence 落在边界区间 [ConfidenceBoundaryLow, ConfidenceBoundaryHigh]，
//     或打分理由含糊（hasVagueReason：为空/过短 <VagueReasonMinRunes/含不确定性措辞）；
//  2. dimension_split：存在 Score >= JudgePassThreshold 且存在 Score < JudgePassThreshold；
//  3. judge_rule_conflict：规则命中（Signals.Rule 非空）+ Verdict == block + 全部维度 pass；
//  4. behavior_anomaly：Signals.Behavior 含 abandonment/escalation 且 Verdict == flag
//     （无 judge 的行为判异也入池；但无 judge 的 rule-block-only 观测 Verdict=block，
//     不满足 flag 守卫，仍不进池，勿破坏现状）。
func TriggersForObservation(obs *EvalObservation, cfg ReviewConfig) []ReviewTriggerReason {
	if obs == nil {
		return nil
	}
	var triggers []ReviewTriggerReason
	if (obs.Signals.Behavior.Abandonment || obs.Signals.Behavior.Escalation) && obs.Verdict == VerdictFlag {
		triggers = append(triggers, TriggerBehaviorAnomaly)
	}
	if len(obs.Signals.Judge) == 0 {
		return triggers
	}
	if hasLowConfidence(obs.Signals.Judge, cfg.LowConfidenceThreshold) {
		triggers = append(triggers, TriggerLowConfidence)
	}
	below, above := splitExists(obs.Signals.Judge, cfg.JudgePassThreshold)
	if below && above {
		triggers = append(triggers, TriggerDimensionSplit)
	}
	if len(obs.Signals.Rule) > 0 && obs.Verdict == VerdictBlock && !below {
		triggers = append(triggers, TriggerJudgeRuleConflict)
	}
	return triggers
}
```

（`hasLowConfidence`/`splitExists`/`TriggersForProcessConflict`/`TriggersForCaseResult` 等其余函数不动。）

**4b.** `internal/evaluation/infrastructure/persistence/review_repository.go` `reviewRiskOrderSQL()`（与 domain `RiskLevel()` 镜像；改一侧必须同步另一侧）：

```go
func reviewRiskOrderSQL() string {
	return `CASE trigger_reason WHEN 'judge_rule_conflict' THEN 0 WHEN 'process_output_conflict' THEN 0 WHEN 'low_confidence' THEN 1 WHEN 'dimension_split' THEN 1 WHEN 'needs_review' THEN 1 WHEN 'behavior_anomaly' THEN 1 ELSE 2 END`
}
```

**4c.** `internal/evaluation/infrastructure/persistence/review_repository_test.go` `TestPgReviewRepositoryListItems`（L166）用 `regexp.QuoteMeta` 断言精确 SQL，必须同步加 WHEN（否则该既有用例 FAIL）：

```go
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, source_type, source_id, run_id, trace_id, resource_kind, resource_id, trigger_reason, snapshot, status, human_verdict, reviewer, review_reason, created_at, reviewed_at FROM eval_review_items WHERE 1=1 ORDER BY CASE trigger_reason WHEN 'judge_rule_conflict' THEN 0 WHEN 'process_output_conflict' THEN 0 WHEN 'low_confidence' THEN 1 WHEN 'dimension_split' THEN 1 WHEN 'needs_review' THEN 1 WHEN 'behavior_anomaly' THEN 1 ELSE 2 END, created_at DESC LIMIT $1 OFFSET $2`)).
```

Run: `go test ./internal/evaluation/domain/ ./internal/evaluation/infrastructure/persistence/ -run 'TestTriggersForObservation|TestReviewTriggerReasonRiskLevel|TestPgReviewRepositoryListItems'`
Expected: **PASS**。旧子用例全保绿（"no judge signals yields no triggers"：无 judge 无 behavior → 空；"nil observation yields no triggers"：nil → nil；"rule conflict suppressed when judge below threshold" 等 judge 分支行为不变）。

- [ ] **Step 5: E6 run 基线查询 — port + PgRunRepository 实现 + fake 同步 + pgxmock 用例 → PASS**

**5a.** `internal/evaluation/domain/port/evaluation.go` `RunRepository` 加方法（E6 精确签名）：

```go
type RunRepository interface {
	SaveRun(ctx context.Context, tenantID string, run domain.EvalRun) error
	GetRun(ctx context.Context, tenantID, runID string) (domain.EvalRun, bool, error)
	// FindLatestCompletedRunForResource 返回该 resource（kind+id）+ suite revision 最近一条
	// 已完成（status='succeeded'）run；无 → (nil, nil)。供 run 级回归对照与发布哨兵定位基线
	// run（T8 定义、T12 消费）。
	FindLatestCompletedRunForResource(
		ctx context.Context, tenantID string, ref domain.ResourceRef, suiteRevisionID string,
	) (*domain.EvalRun, error)
}
```

> 注：`ref` 仅取 `Kind` + `ResourceID` 作过滤键，**不含 `ref.RevisionID`**（基线 = 同资源同 suite revision 的最近一条已完成 run，其资源 revision 通常异于当前确认 run；若按 ref.RevisionID 精确匹配将永远找不到前序版本的基线）。该语义取舍见「任务依赖与次序」跨任务绑定 C（Task 5 哨兵消费同语义）。`eval_runs` 顶层确有 `suite_revision_id` 列（DDL + SaveRun INSERT 均含），故无需 `metrics->'version'` JSON path 兜底。

**5b.** `internal/evaluation/infrastructure/persistence/run_repository.go` 追加实现（镜像 `GetRun` 的 `execTenantTx` + row-scan 风格；不加载 case results——基线对照只消费 `metrics.by_dimension`，明细属 `GetRun` 语义，使该方法保持单查询 O(1)）：

```go
// FindLatestCompletedRunForResource 返回该 resource（kind+id）+ suite revision 最近一条
// 已完成（succeeded）run（无 → (nil, nil)）。供 run 级回归对照与发布哨兵定位基线 run。
// 只读 run 行（Metrics/ContextSnapshot/版本锚点），不加载 case results——基线对照只消费
// metrics.by_dimension，明细查询属 GetRun 语义。按 created_at DESC, id DESC 取最近。
func (r *PgRunRepository) FindLatestCompletedRunForResource(
	ctx context.Context,
	tenantID string,
	ref domain.ResourceRef,
	suiteRevisionID string,
) (*domain.EvalRun, error) {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	var run *domain.EvalRun
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		run = &domain.EvalRun{Resource: ref}
		var kind string
		var metricsJSON []byte
		var snapshotJSON []byte
		err := tx.QueryRow(ctx,
			`SELECT id, resource_kind, resource_id, revision_id, suite_revision_id,
			        passed, total_cases, passed_cases, metrics, context_snapshot, created_by, created_at
			 FROM eval_runs
			 WHERE resource_kind=$1 AND resource_id=$2 AND suite_revision_id=$3 AND status='succeeded'
			 ORDER BY created_at DESC, id DESC LIMIT 1`,
			string(ref.Kind), ref.ResourceID, suiteRevisionID,
		).Scan(&run.ID, &kind, &run.Resource.ResourceID, &run.Resource.RevisionID,
			&run.SuiteRevisionID, &run.Passed, &run.TotalCases, &run.PassedCases, &metricsJSON,
			&snapshotJSON, &run.CreatedBy, &run.CreatedAt)
		if err == pgx.ErrNoRows {
			run = nil
			return nil
		}
		if err != nil {
			return err
		}
		run.Resource.Kind = domain.ResourceKind(kind)
		if len(metricsJSON) > 0 {
			_ = json.Unmarshal(metricsJSON, &run.Metrics)
		}
		snap, derr := decodeContextSnapshot(snapshotJSON)
		if derr != nil {
			return derr
		}
		run.ContextSnapshot = snap
		return nil
	})
	return run, err
}
```

（`run_repository.go` 现 imports：`encoding/json`、`pgx` 均已存在，无需新增。）

**5c.** `internal/evaluation/application/service_test.go` `fakeRunRepo` 补方法（port 扩展的编译强制同步；默认无基线，保持 nil,nil）：

```go
func (f *fakeRunRepo) FindLatestCompletedRunForResource(
	_ context.Context, _ string, _ domain.ResourceRef, _ string,
) (*domain.EvalRun, error) {
	return nil, nil
}
```

**5d.** `internal/evaluation/infrastructure/persistence/run_repository_mock_test.go` 追加两个用例（仿 `TestPgRunRepository_GetRun_found/notFound` 的 pgxmock 风格）：

```go
func TestPgRunRepository_FindLatestCompletedRunForResource_found(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("agent", "r-1", "s-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "suite_revision_id",
			"passed", "total_cases", "passed_cases", "metrics", "context_snapshot", "created_by", "created_at",
		}).AddRow("run-9", "agent", "r-1", "rev-0", "s-1", true, 1, 1,
			[]byte(`{"by_dimension":{"faithfulness":{"avg_score":0.8,"pass_rate":1.0,"samples":1}}}`),
			[]byte("{}"), "creator-1", now))
	mock.ExpectCommit()

	got, err := repo.FindLatestCompletedRunForResource(context.Background(), "t1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "r-1"}, "s-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "run-9", got.ID)
	require.Equal(t, domain.ResourceKindAgent, got.Resource.Kind)
	require.Equal(t, "rev-0", got.Resource.RevisionID)
	require.Nil(t, got.ContextSnapshot)
	require.Equal(t, 0.8, got.Metrics["by_dimension"].(map[string]any)["faithfulness"].(map[string]any)["avg_score"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgRunRepository_FindLatestCompletedRunForResource_notFound(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("agent", "r-1", "s-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectCommit()

	got, err := repo.FindLatestCompletedRunForResource(context.Background(), "t1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "r-1"}, "s-1")
	require.NoError(t, err)
	require.Nil(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
```

Run: `go build ./internal/evaluation/... && go test ./internal/evaluation/infrastructure/persistence/ -run 'TestPgRunRepository_FindLatestCompletedRunForResource' ./internal/evaluation/application/`
Expected: **PASS**（build 通过证明 port 扩展后全部实现/桩已同步）。

- [ ] **Step 6: 两条告警规则 + runbook（同 commit）**

**6a.** 规则源 `monitoring/remote/rules/stratum-evaluation.yaml`，在文件末（`StratumEvalReviewBacklogHigh` 条目之后、同一 `rules:` 序列内、缩进对齐 `- alert:`）追加（severity 均 warning，expr 逐字取自 dict F/R18）：

```yaml
      - alert: StratumEvalJudgeBelowThreshold
        expr: |
          increase(eval_gate_action_total{layer="detect",action="flag"}[15m]) > 0 OR (sum(rate(eval_judge_score_bucket{le="0.5"}[10m])) / clamp_min(sum(rate(eval_judge_score_count[10m])), 1)) > 0.3
        for: 10m
        labels:
          severity: warning
          service: evaluation
          component: runtime-observation
          environment: remote-test
        annotations:
          summary: judge 跌阈判定升高（低分尾部占比超阈值）
          description: >-
            judge 单维度低于 JudgeBelowThreshold 的判异升高：detect/flag 计数 15 分钟
            increase &gt; 0，或 eval_judge_score 直方图 &lt;0.5 尾部占比 &gt; 30%（10 分钟
            窗口）。第一腿信号现网即燃（applyAnomalyVerdict emit layer=detect action=flag）。
            到评测中心下钻对应 trace，人工确认真退化后走参数/版本调整。
          dashboard_url: https://stratum.grafana/d/evaluation
          runbook_url: /docs/operations/alerts/stratum-evaluation.md#stratum-eval-judge-below-threshold
      - alert: StratumEvalRunRegression
        expr: |
          increase(eval_gate_action_total{layer="l2",action="regression"}[15m]) > 0
        for: 10m
        labels:
          severity: warning
          service: evaluation
          component: runtime-observation
          environment: remote-test
        annotations:
          summary: run 级回归劣化判异
          description: >-
            run 级回归判异出现（layer=l2 action=regression，15 分钟 increase &gt; 0）。
            P2 该 label 值仅由发布哨兵判劣 emit（Task 5），T13 确认 run 复用；规则先落、当前
            只读待命。到评测中心对比 base vs current run 的 by_dimension 并人工确认。
          dashboard_url: https://stratum.grafana/d/evaluation
          runbook_url: /docs/operations/alerts/stratum-evaluation.md#stratum-eval-run-regression
```

**6b.** 重渲两个 commit 产物（源 `monitoring/remote/rules/*.yaml` 零改动首行 groups:，双栈产物必须同 commit 防漂移）：

```bash
bash scripts/quality/render-monitoring-rules.sh remote-test
bash scripts/quality/render-monitoring-rules.sh local
```

Expected: 更新 `monitoring/remote/generated/stratum-prometheus-rules.yaml`（PrometheusRule CRD，environment: remote-test）与 `monitoring/local/rules/stratum-evaluation.yml`（environment: production）。两者规则文本与 remote 源一致。

**6c.** `docs/operations/alerts/stratum-evaluation.md` 文件尾追加两条 section（字节序须为 `<a id="{anchor}"></a>\n\n## {AlertName}\n`，`monitoring-runbook-test.go` 精确匹配）：

```markdown
<a id="stratum-eval-judge-below-threshold"></a>

## StratumEvalJudgeBelowThreshold

judge 单维度低于阈值（score < JudgeBelowThreshold）的跌阈判异升高。

- 语义：`eval_gate_action_total{layer="detect",action="flag"}` 15 分钟 increase > 0，或
  `eval_judge_score` 直方图 <0.5 尾部占比 > 30%（10 分钟窗口）触发；第一腿信号现网即燃
  （applyAnomalyVerdict emit detect/flag，§3.2-①）。
- 定位：查询 `eval_gate_action_total{layer="detect",action="flag"}` 与 `eval_judge_score`
  直方图尾部，定位低分维度与 resource；到评测中心按 trace 下钻核对 judge 分数。
- 处置：真能力退化 → 走参数/版本调整并回归验证；误报 → 复核 judge 阈值与评测量纲后调整
  `evaluation.judge.*` 阈值。确认路径 = 评审池人工确认（§3.2-①）。

<a id="stratum-eval-run-regression"></a>

## StratumEvalRunRegression

run 级回归劣化判异（相对基线 run 的维度 delta 跌破 `RunRegressionDeltaThreshold`）。

- 语义：`eval_gate_action_total{layer="l2",action="regression"}` 15 分钟 increase > 0。
  P2 该 label 值仅由发布哨兵判劣 emit（emit 点 Task 5），T13 确认 run 复用——规则先落、当前
  只读待命；`{layer="l2",action="regression"}` 是 P2 新增 label 值，不新增 metric family。
- 定位：查询该 counter 按 resource/suite_revision 分组定位劣化 run；对比 base vs current
  run 的 `metrics.by_dimension`（纯函数 `application.CompareRunRegression`，同 suite revision
  + 同 resource 的最近 completed run 为基线）。
- 处置：真回归 → 走门禁流程（人工确认/回滚候选）；误报 → 核对基线选择与维度 delta 阈值。
```

**6d.** 质量闸验证：

```bash
go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .
bash scripts/quality/render-monitoring-rules.sh remote-test --check
bash scripts/quality/render-monitoring-rules.sh local --check
```

Expected: 全绿（每条新 alert 恰 1 个可解析 `runbook_url`，anchor 在 runbook 文件可解析；双栈渲染与 commit 产物一致）。

- [ ] **Step 7: Commit**

```bash
git add internal/evaluation/application/regression_compare.go internal/evaluation/application/regression_compare_test.go internal/evaluation/domain/review_pool.go internal/evaluation/domain/review_pool_test.go internal/evaluation/infrastructure/persistence/review_repository.go internal/evaluation/infrastructure/persistence/review_repository_test.go internal/evaluation/domain/port/evaluation.go internal/evaluation/infrastructure/persistence/run_repository.go internal/evaluation/infrastructure/persistence/run_repository_mock_test.go internal/evaluation/application/service_test.go monitoring/remote/rules/stratum-evaluation.yaml monitoring/remote/generated/stratum-prometheus-rules.yaml monitoring/local/rules/stratum-evaluation.yml docs/operations/alerts/stratum-evaluation.md
git commit -m "feat(evaluation): 卡 C 分层门禁 P2 run 级回归对照 + 评审池 behavior 判异 + 两条 L2 告警

Co-Authored-By: Claude <noreply@anthropic.com>"
```

**验证命令**

```bash
# Step 1/2：regression_compare 纯函数（表驱动）
go test ./internal/evaluation/application/ -run TestCompareRunRegression -v
# Step 3/4：评审池 behavior 分支 + RiskLevel 表 + reviewRiskOrderSQL 镜像 SQL 期望
go test ./internal/evaluation/domain/ ./internal/evaluation/infrastructure/persistence/ \
  -run 'TestTriggersForObservation|TestReviewTriggerReasonRiskLevel|TestPgReviewRepositoryListItems' -v
# Step 5：E6 基线查询实现 + pgxmock 用例
go test ./internal/evaluation/infrastructure/persistence/ -run 'TestPgRunRepository_FindLatestCompletedRunForResource' -v
# 全量（含 mock 同步编译证明）：evaluation 应用/领域/持久化 + contract 无变化
go vet ./internal/evaluation/...
go test ./internal/evaluation/application/ ./internal/evaluation/domain/ ./internal/evaluation/infrastructure/persistence/ ./api/http/
# Step 6：告警质量闸
go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .
bash scripts/quality/render-monitoring-rules.sh local --check
bash scripts/quality/render-monitoring-rules.sh remote-test --check
# PR 前按仓库门槛补：go test -v -race -timeout 30s ./... 与 make risk-guardrails（系统验收归 stratum-e2e-tester）
```

**主会话裁决（原「需协调者定夺」；协调者已定稿）**

1. **run 级回归的维度比较基准**：spec §3.2-②/§4.3.6 只说「比较 `by_dimension` 的维度 delta」，而 `by_dimension` 每维同时有 `avg_score` 与 `pass_rate`。→ **master 定稿：采用 `avg_score`**（维度 score 均值，度量更平滑、与 `RunRegressionDeltaThreshold=-0.05` 同量纲；`pass_rate` 步进离散、样本少时易抖动）。Task 5/T13 消费同一 `CompareRunRegression`，比较基准全局唯一（A5.1）。
2. **`FindLatestCompletedRunForResource` 是否过滤 `ref.RevisionID`**：→ **master 定稿：只按 `Kind + ResourceID + suiteRevisionID` 过滤（不含 RevisionID）**，取最近 `status='succeeded'` run（A5.2 + 跨任务绑定 C）。Task 5 哨兵比「当前 published seq run vs 草案 seq run」，不需同 revision 基线。
3. **`eval_judge_score` 直方图尾部腿**：已核实 `pkg/observability/prometheus.go:345` `LinearBuckets(0, 0.1, 11)` → 存在 `le="0.5"` 边界，dict F expr 不需改动（仅记录，无行动项）。

**Spec 覆盖**：§3.2-①②（run 级回归判定纯函数 + 两条 L2 告警规则/runbook）、§3.2-③ 与 §4.3.5（`TriggersForObservation` behavior 分支 + `behavior_anomaly` 枚举/Valid/RiskLevel=medium + `reviewRiskOrderSQL` 镜像）、§4.3.6（`regression_compare.go` + E6 基线 run 查询）。有意跳过并标注：告警 emit 点（`eval_gate_action_total{layer="l2",action="regression"}`）属 Task 5 哨兵、T13 确认 run（R18，T8 只写规则+runbook）；`review_service.go`/`TryEscalateObservation` 零改动（已确认其遍历 `TriggersForObservation` 输出逐条 UpsertItem，behavior 分支产出后自动入池）；P2 零 DDL/零 proto/零 metric family。

---

## Task 2（spec T9）: L1 检测/执行分离（O4）+ StratumEvalRuleDisabled 告警

> spec §3.1（O4）

**Goal:** 落地 O4 已裁的 L1 语义——检测恒开 + 执行受控。`RuleGuard.Check` 把「检测」与「执行(拦截)」解耦（签名不变 `(*RuleBlockedError, bool)`）：denylist 命中（不论 `evaluation.ruleguard.enabled`）恒写 ctx 累积器、恒记 `eval_rule_hit_total`（verdict=block|detected）；**仅** `enabled && hit` 追加 `eval_gate_action_total{layer="l1_rule",action="block"}` 并返回 `RuleBlockedError`。由此 disabled 命中同样经 `emitObservation` 产出含 rule 信号的评测观测（spec §3.1 L195「命中即产 block 观测，不依赖执行开关」）。同时新增 warning 告警 `StratumEvalRuleDisabled`（expr 见 dict F/R22）+ 配套 runbook，提示「denylist 非空但 enabled=false 命中未拦」的误配置。

**Architecture:** 变更全部收敛在 agent 侧 `rule_guard.go` 单一热路径函数 + 观测/监控产物，不触碰 evaluation 侧任何代码（R21/E8 零改动）。检测语义落在 `Check` 内部：先把「enabled 短路总闸」从函数最前移除，改为只计算一个 bool 判别 verdict；命中循环保持 `strings.EqualFold(strings.TrimSpace(denied), toolID)`（R23）。观测旁路 `emitObservation`（数据源=ctx 累积器）零改动——它本就不读 enabled，之前之所以 disabled 不产观测，是因为累积器只在 enabled 分支被填充；本次让累积器恒填即可。`RuleGuardDeps` 结构、参数注册（`registry.go:490-508`）、wiring（`agent.go:617-644`）与唯一调用点 `tool_execution_guard.go:44-52` 语义全部不动。监控产物走 `monitoring/remote/rules/*.yaml` 单一源 → render 出本地 `.yml` 与远端 generated CRD（渲染器 `scripts/quality/render-monitoring-rules.sh`），runbook anchor 同 commit（`scripts/quality/monitoring-runbook-test.go` 守卫）。

**Files:**

- Modify: `internal/agent/application/rule_guard.go`（常量块 + `RuleGuard` doc + `Check` 重写）
- Modify: `internal/agent/application/rule_guard_test.go`（重写为带 metrics spy 的表驱动 8 用例）
- Modify: `internal/agent/application/observation_emit_test.go`（追加 disabled 命中→rule 信号集成用例，复用 `stubEmitter`/`newTestServiceWithEmitter`）
- Modify: `internal/agent/domain/agent.go`（doc-only 一行，A4：`RuleBlock` L386 注释「即时拦截」→「规则护栏命中（拦截或仅检测）」；无契约/运行影响，非 evaluation 契约镜像，R21 不涉）
- Modify: `monitoring/remote/rules/stratum-evaluation.yaml`（规则单一源，新增 `StratumEvalRuleDisabled`）
- Modify: `docs/operations/alerts/stratum-evaluation.md`（追加 runbook section，anchor `stratum-eval-rule-disabled`）
- Modify: `monitoring/remote/tests/stratum-rules.test.yaml`（新增 promtool test block）
- Regenerate（render 产物，随 commit 落库，禁止手编）: `monitoring/local/rules/stratum-evaluation.yml`、`monitoring/remote/generated/stratum-prometheus-rules.yaml`

不修改（有意保持零改动）: `internal/agent/application/tool_execution_guard.go`、`internal/agent/application/agent_service.go`（emitObservation/ruleSignalsFromBlocks）、`internal/agent/application/agent_execution.go`（累积器注入点 L286/L347）、`internal/agent/domain/port/observation.go`、`internal/evaluation/**`、`internal/parameters/domain/registry.go`、`api/wiring/agent.go`。

**Interfaces:**

- Consumes（签名精确，全部只读不改）:
  - `RuleGuardDeps{Enabled func(ctx context.Context) bool; Denylist func(ctx context.Context) []string; Metrics observability.MetricsProvider; Logger *zap.Logger}`（`rule_guard.go` L30-35）
  - `MetricsProvider.IncEvalRuleHit(rule, resource, verdict string)`（`pkg/observability/provider.go` L135）、`IncEvalGateAction(layer, action string)`（L139）——只传新 label 值，无新方法、无新 metric family（R32）
  - ctx 累积器 `ctx.Value(ruleBlockCollectorKey{}).(*[]domain.RuleBlock)`（注入点 `agent_execution.go` L286/L347）
  - `emitObservation`（`agent_service.go` L124-158）与 `ruleSignalsFromBlocks` L147-157（数据源=ctx 累积器，只读）
  - 唯一调用点 `ToolExecutionGuard.Execute`（`tool_execution_guard.go` L44-52）：`if block, blocked := g.deps.RuleGuard.Check(ctx, req.Tool.Name); blocked { return nil, block }`
  - 平台参数 `evaluation.ruleguard.enabled`(bool 默认 false) / `evaluation.ruleguard.denylist`(string 默认 "")（`registerRuleGuardParams` `registry.go:490-508`，risk_tier 平台 low）
- Produces:
  - `Check` 新语义（签名不变）：denylist 空/nil → `(nil,false)` 零命中零观测；命中恒写累积器 + `IncEvalRuleHit("tool_denylist","agent",verdict)`；`enabled && hit` → `IncEvalGateAction("l1_rule","block")` + `RuleBlockedError`；否则 `(nil,false)`（检测/观测照常）；`enabled==nil` 视为 false
  - 观测副作用（R21）：disabled+denylist 命中 → emitObservation 产 rule 信号 → evaluation `applyAnomalyVerdict`（L309-334 只读不改）判 `VerdictBlock`；`eval_behavior_anomaly_total{signal="rule_block"}` 与 `eval_gate_action_total{layer="detect",action="block"}` 上涨
  - 指标 label 值：`eval_rule_hit_total{rule="tool_denylist",resource="agent",verdict="block"|"detected"}`、`eval_gate_action_total{layer="l1_rule",action="block"}`
  - Alert `StratumEvalRuleDisabled`（warning）+ runbook + render 产物 + promtool test

---

- [ ] **Step 0: 开工前风险与事实核对（非 TDD）**

在 worktree 根执行：

```bash
bash scripts/quality/risk-regression-guard.sh --explain
grep -rn "rule_guard" monitoring/ grafana/ || true   # R20 裁决：改 l1_rule 前确认零消费，预期无输出
grep -rn "ruleGuardVerdictDetected\|ruleGuardLayerL1\|ruleGuardKindDenylist" internal/agent/application/ || true   # 常量名无冲突，预期无输出
```

Expected: risk guard 正常输出说明；两个 grep 均零命中（监控/grafana 无 `rule_guard` layer 消费 → 可安全更名 `l1_rule`，dict R20）。

- [ ] **Step 1: 先写失败测试（O4 行为契约）**

重写 `internal/agent/application/rule_guard_test.go` 为带 metrics spy 的表驱动 8 用例（复用既有 `domain`/`observability` import；新增本地 spy 类型锁 label 契约）：

```go
package application

import (
	"context"
	"testing"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

// ruleGuardMetricsSpy 记录 IncEvalRuleHit/IncEvalGateAction 调用，锁 R20 label 契约。
type ruleGuardMetricsSpy struct {
	observability.NoopMetrics
	ruleHits    []ruleHitCall
	gateActions []gateActionCall
}

type ruleHitCall struct{ rule, resource, verdict string }
type gateActionCall struct{ layer, action string }

func (s *ruleGuardMetricsSpy) IncEvalRuleHit(rule, resource, verdict string) {
	s.ruleHits = append(s.ruleHits, ruleHitCall{rule, resource, verdict})
}

func (s *ruleGuardMetricsSpy) IncEvalGateAction(layer, action string) {
	s.gateActions = append(s.gateActions, gateActionCall{layer, action})
}

func TestRuleGuardCheck(t *testing.T) {
	always := func(context.Context) bool { return true }
	never := func(context.Context) bool { return false }
	noDenylist := func(context.Context) []string { return nil }
	cases := []struct {
		name           string
		enabled        func(context.Context) bool
		denylist       func(context.Context) []string
		toolID         string
		skipGuard      bool // nil guard 用例
		wantBlocked    bool
		wantVerdict    string // "" = 不应产生 hit
		wantGateAction bool
		wantCollector  int
	}{
		{name: "enabled hit blocks and observes", enabled: always,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "danger_tool",
			wantBlocked: true, wantVerdict: "block", wantGateAction: true, wantCollector: 1},
		{name: "not listed allows with zero observation", enabled: always,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "safe_tool",
			wantCollector: 0},
		{name: "disabled hit detects but does not block", enabled: never,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "danger_tool",
			wantVerdict: "detected", wantCollector: 1},
		{name: "nil enabled treated as disabled", enabled: nil,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "danger_tool",
			wantVerdict: "detected", wantCollector: 1},
		{name: "case-insensitive match blocks", enabled: always,
			denylist: func(context.Context) []string { return []string{"DANGER_Tool"} }, toolID: "danger_tool",
			wantBlocked: true, wantVerdict: "block", wantGateAction: true, wantCollector: 1},
		{name: "empty denylist entries skipped", enabled: always,
			denylist: func(context.Context) []string { return []string{"", "   "} }, toolID: "danger_tool",
			wantCollector: 0},
		{name: "nil denylist zero hit zero observation", enabled: always, denylist: noDenylist,
			toolID: "danger_tool", wantCollector: 0},
		{name: "nil guard allows", toolID: "danger_tool", skipGuard: true, wantCollector: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &ruleGuardMetricsSpy{}
			var g *RuleGuard
			if !tc.skipGuard {
				g = NewRuleGuard(RuleGuardDeps{Enabled: tc.enabled, Denylist: tc.denylist, Metrics: spy})
			}
			blocks := &[]domain.RuleBlock{}
			ctx := context.WithValue(context.Background(), ruleBlockCollectorKey{}, blocks)
			block, blocked := g.Check(ctx, tc.toolID)
			if blocked != tc.wantBlocked || (block != nil) != tc.wantBlocked {
				t.Fatalf("blocked=%v block=%v, want blocked=%v", blocked, block, tc.wantBlocked)
			}
			if len(*blocks) != tc.wantCollector {
				t.Fatalf("collector len = %d, want %d", len(*blocks), tc.wantCollector)
			}
			wantHits := 0
			if tc.wantVerdict != "" {
				wantHits = 1
			}
			if len(spy.ruleHits) != wantHits {
				t.Fatalf("rule hits = %v, want %d", spy.ruleHits, wantHits)
			}
			if tc.wantVerdict != "" {
				if hit := spy.ruleHits[0]; hit.rule != "tool_denylist" || hit.resource != "agent" || hit.verdict != tc.wantVerdict {
					t.Fatalf("rule hit mismatch: %+v", hit)
				}
			}
			if tc.wantGateAction {
				if len(spy.gateActions) != 1 {
					t.Fatalf("gate actions = %v, want 1", spy.gateActions)
				}
				if act := spy.gateActions[0]; act.layer != "l1_rule" || act.action != "block" {
					t.Fatalf("gate action mismatch: %+v", act)
				}
			} else if len(spy.gateActions) != 0 {
				t.Fatalf("unexpected gate action: %v", spy.gateActions)
			}
		})
	}
}
```

在 `internal/agent/application/observation_emit_test.go` 末尾追加 disabled 命中→观测信号的集成用例（O4 检测恒开的端到端证明，复用 `stubEmitter`/`newTestServiceWithEmitter`/`ExecMeta`/`AgentResult`）。该文件现 import 组（stdlib → third-party → internal）缺 `observability`，在 `"github.com/byteBuilderX/stratum/internal/agent/domain/port"` 之后补一行 `"github.com/byteBuilderX/stratum/pkg/observability"`：

```go
func TestEmitObservationDisabledGuardHitStillYieldsRuleSignal(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	blocks := &[]domain.RuleBlock{}
	ctx := context.WithValue(context.Background(), ruleBlockCollectorKey{}, blocks)
	g := NewRuleGuard(RuleGuardDeps{
		Enabled:  func(context.Context) bool { return false },
		Denylist: func(context.Context) []string { return []string{"danger_tool"} },
		Metrics:  observability.NoopMetrics{},
	})
	if block, blocked := g.Check(ctx, "danger_tool"); blocked || block != nil {
		t.Fatalf("disabled guard must not block, got blocked=%v block=%v", blocked, block)
	}
	if len(*blocks) != 1 {
		t.Fatalf("collector len = %d, want 1（O4：disabled 命中恒填累积器）", len(*blocks))
	}
	s.emitObservation(ctx, ExecMeta{TenantID: "t1", TraceID: "trace-1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"})
	if emitter.called != 1 {
		t.Fatalf("Emit called %d times, want 1", emitter.called)
	}
	if len(emitter.last.RuleSignals) != 1 {
		t.Fatalf("rule_signals len = %d, want 1", len(emitter.last.RuleSignals))
	}
	if sig := emitter.last.RuleSignals[0]; sig.Rule != "tool_denylist" {
		t.Fatalf("rule signal mismatch: %+v", sig)
	}
}
```

Run:

```bash
go test ./internal/agent/application/ -run 'TestRuleGuardCheck|TestEmitObservationDisabledGuardHitStillYieldsRuleSignal' -count=1
```

Expected: **FAIL**（旧实现 enabled 总闸在 L56 短路，disabled/nil-enabled 命中零累积零指标；enabled 命中的 gate layer 仍是 `rule_guard` 非 `l1_rule`）。典型失败输出：
`TestRuleGuardCheck/enabled_hit_blocks_and_observes: gate action mismatch: {rule_guard block}`、`TestRuleGuardCheck/disabled_hit_detects_but_does_not_block: collector len = 0, want 1`、`TestEmitObservationDisabledGuardHitStillYieldsRuleSignal: collector len = 0, want 1`。其余既有锁定用例（not listed / empty entries / nil denylist / nil guard）仍 PASS，证明不引入误拦截回归。

- [ ] **Step 2: 实现 Check（检测恒开 + 执行受控）**

`internal/agent/application/rule_guard.go`：在 `NewRuleGuard` 返回后、`Check` 前插入常量块；替换 `RuleGuard` struct doc（L37）与整个 `Check`（L52-80）。常量块：

```go
// 规则护栏指标标签常量（spec §3.1 L198 / dict R20）：检测与执行分离后 hit 判别由
// verdict 承担（block=真拦截 / detected=检测未拦截）；guard 层计数落 l1_rule
// （原 rule_guard label 全仓零消费，R20 裁决更名）。enabled==nil 视为 false。
const (
	ruleGuardKindDenylist    = "tool_denylist"
	ruleGuardResourceAgent   = "agent"
	ruleGuardLayerL1         = "l1_rule"
	ruleGuardVerdictBlock    = "block"
	ruleGuardVerdictDetected = "detected"
)
```

struct doc 替换为：

```go
// RuleGuard 是内联 L1 规则护栏（spec §3.1 快路径，O4：检测恒开 + 执行受控）：
// denylist 命中恒检测/恒观测，仅 enabled 时真拦截。零 LLM、零额外延迟。
```

`Check` 整体替换为（签名不变 `(*RuleBlockedError, bool)`，R23）：

```go
// Check 对单个工具名执行 L1 规则护栏（spec §3.1 / O4）。检测与执行分离：
//   - denylist 为空/nil → (nil,false)，零命中零观测；
//   - 命中判定保留 strings.EqualFold(strings.TrimSpace(denied), toolID)（大小写不敏感）；
//   - 任一命中（不论 enabled）→ (a) 恒写 ctx 累积器（ruleBlockCollectorKey，供
//     emitObservation 产出 rule 信号）+ (b) IncEvalRuleHit("tool_denylist","agent",verdict)，
//     verdict = enabled ? "block" : "detected"（判别由 verdict 承担，R20）；
//   - 仅 enabled && hit → (c) IncEvalGateAction("l1_rule","block") + 返回
//     RuleBlockedError（真拦截，fail closed）；否则 (nil,false) 放行（检测/观测照常）。
// enabled==nil 视为 false；RuleGuardDeps 结构与调用点（tool_execution_guard.go）语义不变。
func (g *RuleGuard) Check(ctx context.Context, toolID string) (*RuleBlockedError, bool) {
	if g == nil {
		return nil, false
	}
	enabled := g.deps.Enabled != nil && g.deps.Enabled(ctx)
	if g.deps.Denylist == nil {
		return nil, false
	}
	for _, denied := range g.deps.Denylist(ctx) {
		denied = strings.TrimSpace(denied)
		if denied == "" || !strings.EqualFold(denied, toolID) {
			continue
		}
		message := fmt.Sprintf("tool %q blocked by platform rule", toolID)
		verdict := ruleGuardVerdictDetected
		if enabled {
			verdict = ruleGuardVerdictBlock
		}
		// (a)+(b) 检测恒开：命中恒记 hit、恒填累积器（O4：disabled 命中同样产观测）。
		g.deps.Metrics.IncEvalRuleHit(ruleGuardKindDenylist, ruleGuardResourceAgent, verdict)
		if collector, ok := ctx.Value(ruleBlockCollectorKey{}).(*[]domain.RuleBlock); ok {
			*collector = append(*collector, domain.RuleBlock{Rule: ruleGuardKindDenylist, Tool: toolID, Message: message})
		}
		// (c) 执行受控：仅 enabled && hit 真拦截（fail closed）。
		if enabled {
			g.deps.Metrics.IncEvalGateAction(ruleGuardLayerL1, ruleGuardVerdictBlock)
			return &RuleBlockedError{Rule: ruleGuardKindDenylist, Tool: toolID, Message: message}, true
		}
		return nil, false
	}
	return nil, false
}
```

Run:

```bash
go test ./internal/agent/application/ -run 'TestRuleGuardCheck|TestEmitObservationDisabledGuardHitStillYieldsRuleSignal|TestEmitObservation' -count=1
```

Expected: **PASS**。Step 1 所有红用例转绿；既存 emit/execution 观测用例（`TestEmitObservationPopulatesRuleSignalsFromCollector`、`TestToolExecutionGuard*`）不受影响。

- [ ] **Step 3: agent 包回归（确认零破坏）**

Run:

```bash
go vet ./internal/agent/...
go test ./internal/agent/application/ -count=1
```

Expected: 全 PASS，无 vet 告警。`tool_execution_guard_test.go`（enabled=true 命中→RuleBlockedError 仍成立）与 `observation_emit_test.go` 其余用例不受新语义影响。

- [ ] **Step 4: evaluation 侧零改动回归（R21/E8 验证）**

不加任何代码，跑全量证明只读约束成立：

```bash
go test ./internal/evaluation/... ./internal/agent/domain/... -count=1
```

Expected: 全 PASS（evaluation `applyAnomalyVerdict`/契约镜像/golden/parity 未动）。注释说明：disabled 命中现在也会让 `Signals.Rule>0 → VerdictBlock`，这是 O4 显式接受的规范行为，见本 task 风险节，不由本步代码承担。

- [ ] **Step 5: 新增 StratumEvalRuleDisabled 告警（远程单一源）**

在 `monitoring/remote/rules/stratum-evaluation.yaml` 的 `StratumEvalRuleBlocked` 块之后（`runbook_url: /docs/operations/alerts/stratum-evaluation.md#stratum-eval-rule-blocked` 下一行）、`StratumEvalJudgeDegraded` 之前插入（dict F/R22，缩进 6 空格对齐既有条目）：

```yaml
      - alert: StratumEvalRuleDisabled
        expr: |
          increase(eval_rule_hit_total{verdict="detected"}[15m]) > 0
        for: 5m
        labels:
          severity: warning
          service: evaluation
          component: runtime-observation
          environment: remote-test
        annotations:
          summary: 规则护栏命中但未拦截
          description: >-
            检测到规则护栏命中但未拦截：evaluation.ruleguard.enabled=false 但 denylist 非空，
            命中 verdict=detected 持续 5 分钟。护栏当前只检测不拦截，命中工具照常执行。若该
            工具应禁用，请开启 evaluation.ruleguard.enabled；否则收紧 denylist 移除误报。
          dashboard_url: https://stratum.grafana/d/evaluation
          runbook_url: /docs/operations/alerts/stratum-evaluation.md#stratum-eval-rule-disabled
```

> 说明：规则唯一事实源是 `monitoring/remote/rules/*.yaml`（见 `render-monitoring-rules.sh` 头注释）。本步只改远程源，本地 `.yml`/远端 CRD 由 Step 7 渲染生成，禁止手编（dict B 告警质量闸按渲染产物校验）。

- [ ] **Step 6: 追加 runbook section + `RuleBlock` doc 一行修正（anchor 同 commit）**

`docs/operations/alerts/stratum-evaluation.md` 文末追加（runbook anchor 必须精确匹配 `<a id="{anchor}"></a>\n\n## {AlertName}\n`，`monitoring-runbook-test.go` 守卫）：

```markdown
<a id="stratum-eval-rule-disabled"></a>

## StratumEvalRuleDisabled

规则护栏命中但未拦截：`evaluation.ruleguard.enabled=false` 但 `denylist` 非空（O4 检测恒开 + 执行受控的提示告警）。

- 语义：任一 15 分钟窗口内 `eval_rule_hit_total{verdict="detected"}` 命中且持续 5 分钟触发。
  verdict=detected 表示护栏「检测到但未拦截」；`StratumEvalRuleBlocked`（critical，verdict=block）
  不受污染——disabled 命中不会误触 critical。
- 定位：查询 `eval_rule_hit_total{verdict="detected"}` 按 `rule`/`resource` 分组，确认命中工具
  是否本应禁用；比对平台参数 `evaluation.ruleguard.enabled` 与 `evaluation.ruleguard.denylist`
  当前值（registry 平台级 low risk_tier）。
- 确认：enabled=false 时 denylist 命中只产观测（评测侧判 VerdictBlock，属显式接受副作用），
  不拦截执行——这与 O4「未启用规则 = 无规则可命中」语义一致，非执行故障。
- 处置：命中工具应禁用 → 平台参数开启 `evaluation.ruleguard.enabled=true`，走平台参数发布审批，
  随后回归验证命中即拦截；误报 → 收紧 `evaluation.ruleguard.denylist`。禁止远端手改（变更走
  操作台/CD 流程）。
```

`internal/agent/domain/agent.go` `RuleBlock` 结构 doc 一行修正（A4，随本 task 同 commit；非契约镜像）：

```go
// RuleBlock 记录一次规则护栏命中（拦截或仅检测，O4 检测恒开：denylist 命中不论 enabled
// 均写入 ctx 累积器供观测产出 rule 信号）。
```

- [ ] **Step 7: 渲染监控产物 + 质量闸**

Run:

```bash
bash scripts/quality/render-monitoring-rules.sh remote-test
bash scripts/quality/render-monitoring-rules.sh local
go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .
promtool check rules monitoring/remote/rules/*.yaml
promtool test rules monitoring/remote/tests/stratum-rules.test.yaml
bash scripts/quality/render-monitoring-rules.sh remote-test --check
bash scripts/quality/render-monitoring-rules.sh local --check
```

Expected: render 落盘 `monitoring/local/rules/stratum-evaluation.yml`（`remote-test`→`production`）与 `monitoring/remote/generated/stratum-prometheus-rules.yaml`；runbook guard 通过（新 anchor 精确命中）；`promtool check rules` 语法通过；`promtool test rules` 通过（含 Step 8 新增用例）；两 render `--check` 一致（无漂移）。

- [ ] **Step 8: 新增 promtool 断言（同 commit）**

在 `monitoring/remote/tests/stratum-rules.test.yaml` 的 evaluation 观测 test block（`name: evaluation observability alerts fire after their for durations`，L1284 结束）之后追加独立 test block（series 只含 verdict=detected，不与 RuleBlocked/Behavior 交叉）：

```yaml
  - name: evaluation rule disabled alert fires after its for duration
    interval: 1m
    input_series:
      - series: 'eval_rule_hit_total{rule="tool_denylist", resource="agent", verdict="detected"}'
        values: '0+30x25'
    alert_rule_test:
      - eval_time: 2m
        alertname: StratumEvalRuleDisabled
        exp_alerts: []
      - eval_time: 5m
        alertname: StratumEvalRuleDisabled
        exp_alerts: []
      - eval_time: 6m
        alertname: StratumEvalRuleDisabled
        exp_alerts:
          - exp_labels:
              severity: warning
              service: evaluation
              environment: remote-test
              component: runtime-observation
              rule: tool_denylist
              resource: agent
              verdict: detected
            exp_annotations:
              summary: 规则护栏命中但未拦截
              description: >-
                检测到规则护栏命中但未拦截：evaluation.ruleguard.enabled=false 但 denylist 非空，
                命中 verdict=detected 持续 5 分钟。护栏当前只检测不拦截，命中工具照常执行。若该
                工具应禁用，请开启 evaluation.ruleguard.enabled；否则收紧 denylist 移除误报。
              dashboard_url: https://stratum.grafana/d/evaluation
              runbook_url: /docs/operations/alerts/stratum-evaluation.md#stratum-eval-rule-disabled
```

再跑 `promtool test rules monitoring/remote/tests/stratum-rules.test.yaml`，Expected: PASS（for:5m → 2m/5m pending 空、6m 触发，沿 RuleBlocked 既有模式）。

- [ ] **Step 9: 全量门禁相关验证 + Commit（每 Task 一个独立 commit）**

Run:

```bash
go vet ./internal/agent/... ./internal/evaluation/...
go test ./internal/agent/application/ ./internal/evaluation/... -count=1
promtool check rules monitoring/remote/rules/*.yaml && promtool test rules monitoring/remote/tests/stratum-rules.test.yaml
go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .
bash scripts/quality/render-monitoring-rules.sh remote-test --check && bash scripts/quality/render-monitoring-rules.sh local --check
```

Expected: 全绿、无漂移、无新超标函数（改动仅重写一个 ≤80 行函数，满足 Go 门禁：CC/认知/行数/嵌套）。

```bash
git add internal/agent/application/rule_guard.go internal/agent/application/rule_guard_test.go internal/agent/application/observation_emit_test.go internal/agent/domain/agent.go monitoring/remote/rules/stratum-evaluation.yaml monitoring/remote/tests/stratum-rules.test.yaml docs/operations/alerts/stratum-evaluation.md monitoring/local/rules/stratum-evaluation.yml monitoring/remote/generated/stratum-prometheus-rules.yaml
git commit -m "feat(agent): L1 规则护栏检测/执行分离（O4）+ StratumEvalRuleDisabled 告警

- RuleGuard.Check 检测恒开/执行受控（签名不变）：denylist 命中恒写 ctx 累积器 +
  IncEvalRuleHit（verdict=block|detected），仅 enabled&&hit 才 IncEvalGateAction(\"l1_rule\",\"block\")
  并返回 RuleBlockedError；enabled==nil 视为 false；guard layer label rule_guard→l1_rule
- domain/agent.go RuleBlock doc 同步（拦截或仅检测，A4）
- evaluation 侧零改动（R21/E8）：applyAnomalyVerdict/契约镜像/golden/parity 不动，
  disabled 命中产 VerdictBlock 观测为 O4 显式接受副作用
- 新增 warning 告警 StratumEvalRuleDisabled（increase(eval_rule_hit_total{verdict=\"detected\"}[15m])>0）
  + runbook，提示开启 evaluation.ruleguard.enabled 或收紧 denylist（单一源 remote rules → render 产物）

Co-Authored-By: Claude <noreply@anthropic.com>"
```

**验证命令**

```bash
go vet ./internal/agent/... ./internal/evaluation/...
go test -short ./internal/agent/... ./internal/evaluation/... ./internal/parameters/... -count=1
promtool test rules monitoring/remote/tests/stratum-rules.test.yaml
go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .
bash scripts/quality/render-monitoring-rules.sh local --check
```

**主要风险与对策**

1. **disabled 命中观测变 VerdictBlock（R21 显式副作用，供 reviewer 知情）**：enabled=false 且 denylist 非空时，命中工具的 run 观测在 evaluation 侧判 `VerdictBlock`，`eval_behavior_anomaly_total{signal="rule_block"}` 与 `eval_gate_action_total{layer="detect",action="block"}` 上涨。这是 spec O4 L195「检测恒开：命中即产 block 观测」的规范意图，非缺陷；告警判别不受污染（`StratumEvalRuleBlocked` 只匹配 verdict=block，disabled 命中 verdict=detected 只触 `StratumEvalRuleDisabled`，两条独立）。
2. **语义改写不引入误拦截回归**：detect-only 分支返回 `(nil,false)` 放行，`tool_execution_guard.go` 调用点语义不变（仅 blocked=true 返回错误）；`enabled=true` 的拦截行为与既有测试逐条对齐（case-insensitive/未命中/空 denylist/nil guard 全部保留锁定）。
3. **监控单一源纪律**：只改 `monitoring/remote/rules/*.yaml`，本地 `.yml` 与远端 CRD 由渲染器生成；`--check` 守卫漂移。
4. **promtool for 语义**：expr 为 15m 窗口 increase，for:5m 时单次命中会保持触发约 15 分钟（hit 滚出窗口后自动恢复）；连续命中则持续告警。符合「误配置提示」意图。

Spec 覆盖：§3.1（L187-198 全量：缺口②检测/执行分离 + StratumEvalRuleDisabled；L198 计数目标 `eval_gate_action_total{layer="l1_rule",action="block"}`）、O4 裁决（⑦检测恒开 + 执行受控 + `StratumEvalRuleDisabled` 提示）、§5 T9。有意跳过（按 dict 边界）: 缺口① `RuleKind` 小枚举为 spec §3.1 L193 设计但 dict R20-R23/recon T9 均未列入本 task——本实现保留 `tool_denylist` 字符串，未做枚举扩张；规则内容多类型（安全/格式/PII/注入）属 §6 不做。

**主会话裁决（原「需协调者定夺」；协调者已定稿）**

1. **告警规则事实源**：→ **master 定稿（A1）**：只改 `monitoring/remote/rules/*.yaml` 单一源再 render 双产物；本 Task 已按此实现，与 dict B 第 23 行质量闸通过渲染产物一致。
2. **StratumEvalRuleDisabled 的 `for:` 取值**：→ **master 定稿（A2）：`for: 5m`** + labels 对齐既有 sibling（RuleBlocked 同族），promtool 沿 2m/5m pending → 6m fire。
3. **commit scope**：→ **master 定稿（A3）：单 commit `feat(agent): …`**（agent 代码 + remote 告警 + runbook + render 产物同 commit；行为代码全在 agent 上下文）。
4. **`domain/agent.go` L386 `RuleBlock` doc 注释**：→ **master 定稿（A4）：采纳，随本 task 同 commit 一行修正**（已并入本 Task Files 与 Step 6/9）。

---

## Task 3（spec T11）: L3 资源回滚 planner 纯函数 + ResourceRollbackExecutor 分派（agent/knowledge/skill/experiment）

> spec §3.3 + §4.3.3
>
> dict 契约：E2 门禁 port（6 参签名 + 2 sentinel）、E3 资源回滚真实入口、E4 审批执行器（Task 4 消费）、R24/R25；recon T11（Rollback 入口签名 + 上一好语义 + R-K/R-L）。mcp 无产品回滚链 → `ErrRollbackUnsupported`；skill 不在 `pkg/versioning`。
>
> 本任务只实现 executor 分派与 planner 纯函数、窄 port 与 ACL 适配器、wiring 构造器；**生产 wiring（GateService auto 装配、平台 manual 走 Task 4 审批）归 T13/R-M**；Task 4 在本节 Produces 之上接 `rollback_resource` 审批分支。

**Files:**

- Modify: `internal/evaluation/domain/port/gate.go`（`ResourceRollbackExecutor.Rollback` 3 参 → 6 参对齐 E2；追加 `ErrRollbackUnsupported` / `ErrAutoRollbackForbidden` sentinel）
- Modify: `internal/evaluation/application/gate_service.go`（`execAutoRollback` 调用点传 6 参，L175-183）
- Modify: `internal/evaluation/application/gate_service_test.go`（`stubResourceRollback` 改 6 参，L78-90）
- Create: `internal/evaluation/domain/port/rollback.go`（T11 窄 port + 候选值类型）
- Create: `internal/evaluation/application/resource_rollback.go`（planner 纯函数 + `ResourceRollbackExecutor`，实现 E2 6 参签名）
- Create: `internal/evaluation/application/resource_rollback_test.go`（planner + 分派矩阵表驱动）
- Create: `api/wiring/resource_rollback.go`（agent/knowledge/skill 产品 ACL 适配器 + canary 适配器 + `buildResourceRollbackExecutor`）
- Modify: `api/wiring/evaluation.go`（`Evaluation` struct 增 `ResourceRollbackExecutor` 字段；`buildEvaluation` 在 `c.Evaluation = ...` 后装配并赋值）
- Create: `api/wiring/resource_rollback_test.go`（wiring 构造器 nil-safety 测试）

**Interfaces:**

- Consumes（照抄 dict，不改签名）：
  - E1 `domain.GateTarget{Scope, Kind string, ResourceID, RevisionID string}`、`ScopeResource/ScopePlatform`、`domain.ResourceKind` 与 `ResourceKindAgent/Knowledge/Skill/MCP`（`internal/evaluation/domain/resource.go`；**字段名 `Kind` 为权威，已核 gate.go L22-29**）。
  - E2 `port.ResourceRollbackExecutor.Rollback(ctx, tenantID string, target domain.GateTarget, actor, decidedBy, approvalID string) error`（本任务扩展到该目标签名）。
  - E3 真实入口（**不在 evaluation 内直接 import**，经 wiring ACL 适配器消费）：
    - agent `ListVersions(ctx, id) ([]VersionDTO, error)` / `Rollback(ctx, id string, in RollbackAgentInput{ActorID, VersionID}) (AgentDTO, error)`；
    - knowledge `ListWorkspaceVersions(ctx, tenantID, name) ([]WorkspaceVersionDTO, error)` / `RollbackWorkspace(ctx, tenantID, name string, in RollbackWorkspaceInput{ActorID, VersionID}) (*domain.Workspace, error)`；
    - skill `ListRevisions(ctx, skillID) ([]domain.SkillRevision, error)` / `RollbackRevision(ctx, skillID, targetRevisionID, actorID string) error`；
    - experiment `ExperimentService.Rollback(ctx, tenantID, experimentID string, command ExperimentCommandInput{ActorID,Reason,IdempotencyKey,ExpectedStateVersion}) (domain.Experiment, error)`（experiment_service.go L146-150）；部署解析 `evalport.ExperimentRepository.ResolveDeployment(ctx, tenantID, resourceKind, resourceID string) (domain.Deployment, bool, error)`（port/evaluation.go L215）。
  - tenant/审计注入基建：`pkg/reqctx.WithTenantID` / `WithSystemActor`；`pkg/storage/postgres.WithTenant` + `TenantContext{TenantID, UserID, Role}` + `RoleTenantAdmin`（复用 P1 ACL 适配器模式，evaluation_agent_adapter.go L79-80）。
  - 状态常量：`versioningdomain.VersionStatusDeprecated`（agent/knowledge 的 DTO Status string 值）、`skilldomain.VersionStatusDeprecated`（skill）。
  - `observability.NewLogger`/`zap.Logger`（executor 日志）。

- Produces（下游 wiring / Task 4 / T13 消费）：
  - `port.ErrRollbackUnsupported`、`port.ErrAutoRollbackForbidden`（`domain/port/gate.go`）。**Task 4 消费**：`executePlatformRollback` 首行 guard 用 `ErrAutoRollbackForbidden`；executor mcp/未知 kind 返回 `ErrRollbackUnsupported`。
  - `port.RollbackCandidate{ID string; RevisionNo int; IsCurrent bool; Rollbackable bool}`。
  - `port.ProductRollbackBackend{ ListCandidates(ctx, tenantID, resourceID string) ([]RollbackCandidate, error); RollbackProduct(ctx, tenantID, resourceID, candidateID, actorID string) error }`（agent/knowledge/skill 各一实例）。
  - `port.CanaryRollbackBackend{ ResolveDeployment(ctx, tenantID string, kind domain.ResourceKind, resourceID string) (domain.Deployment, bool, error); ClearCanary(ctx, tenantID, experimentID, actorID, reason string) error }`（可为 nil）。
  - `application.NewResourceRollbackExecutor(deps ResourceRollbackExecutorDeps) *ResourceRollbackExecutor`，并 `var _ port.ResourceRollbackExecutor = (*ResourceRollbackExecutor)(nil)`。
  - wiring `func (c *Container) buildResourceRollbackExecutor(experimentRepo evalport.ExperimentRepository) evalport.ResourceRollbackExecutor`（返回 nil = 无任何可回滚后端，gate auto 保持 skip）。**Task 4 消费**（R27）：`newApprovalActionExecutor` 在 api/wiring/evaluation.go 装配时改接 `c.Evaluation.ResourceRollbackExecutor`。
  - `api/wiring/evaluation.go` `Evaluation` struct 增 `ResourceRollbackExecutor evalport.ResourceRollbackExecutor` 字段。

- [ ] **Step 1: `port/gate.go` 对齐 E2 6 参签名 + 追加 2 sentinel；同步 gate_service 与 stub**

`internal/evaluation/domain/port/gate.go` 现有 `ResourceRollbackExecutor`（L41-43）整体替换为 E2 目标签名；文件尾追加两个 sentinel。P1 合并代码仅 2 处引用（gate_service.go L179 调用点 + gate_service_test.go stub），改后全仓库同步：

```go
// ResourceRollbackExecutor 执行资源自动回滚。auto 动作由 GateService 装配（T13 生产
// wiring）；manual 动作由审批执行器 executeResourceRollback 调用（Task 4）。分派见
// application/resource_rollback.go：ScopeResource + Kind → 产品后端 / canary 后端；
// mcp / 未知 kind / 非资源 scope → ErrRollbackUnsupported。执行日限 GateAutoRollbackMaxPerDay
// 由调用方（gate service / T13 wiring 按台账聚合）保障。
type ResourceRollbackExecutor interface {
	// actor = 动作执行者（auto 传 "gate"）；decidedBy = 审批人（manual 传审批 row 的
	// DecidedBy，auto 空）；approvalID = 审批 id（manual 传，auto 空）。
	Rollback(ctx context.Context, tenantID string, target domain.GateTarget,
		actor, decidedBy, approvalID string) error
}

// ErrRollbackUnsupported 表示该资源无回滚链路（mcp / 未知 kind / 非资源 scope）。
var ErrRollbackUnsupported = errors.New("evaluation: rollback unsupported for target")

// ErrAutoRollbackForbidden 表示该目标禁止自动回滚（auto 意图被策略拒绝；spec §3.4 平台恒
// 人工、资源默认 AutoRollbackAllowed=false，Task 4 executePlatformRollback 首行 guard 消费）。
var ErrAutoRollbackForbidden = errors.New("evaluation: auto rollback forbidden for target")
```

`gate.go` 顶部 import 块需加入 `"errors"`。

`internal/evaluation/application/gate_service.go` `execAutoRollback` 调用点（L175-183）改为 6 参（auto 无审批人：actor="gate"，decidedBy/approvalID 空）：

```go
func (s *GateService) execAutoRollback(ctx context.Context, tenantID string, target domain.GateTarget) {
	if s.deps.ResourceRollback == nil {
		return
	}
	// auto 路径无审批人：actor 记为 gate（与台账 rec.Actor 同值，见 route()），
	// decidedBy/approvalID 空串由 executor 语义消费。
	if err := s.deps.ResourceRollback.Rollback(ctx, tenantID, target, "gate", "", ""); err != nil {
		s.warn("gate resource auto rollback failed", zap.Error(err), zap.String("target", target.Key()))
	}
}
```

`gate_service_test.go` stub（L78-90）同步 6 参（既有 `rollbacks []domain.GateTarget` 断言不变，附加参数忽略）：

```go
func (s *stubResourceRollback) Rollback(_ context.Context, _ string, target domain.GateTarget, _, _, _ string) error {
	s.rollbacks = append(s.rollbacks, target)
	if s.err != nil {
		return s.err
	}
	return nil
}
```

Run: `go test ./internal/evaluation/application/ -run 'TestHandleObservationRoutesResourceScopeRollbackActions' ./internal/evaluation/domain/`
Expected: PASS（stub 签名编译 + auto 分发断言仍 1 次调用）。

Commit:

```bash
git add internal/evaluation/domain/port/gate.go internal/evaluation/application/gate_service.go internal/evaluation/application/gate_service_test.go
git commit -m "feat(evaluation): 卡 C P2 gate port 对齐 E2 6 参 Rollback + rollback sentinel

Co-Authored-By: Claude <noreply@anthropic.com>"
```

- [ ] **Step 2: 新建窄 port `domain/port/rollback.go`**

R24：窄 port 新增于 `internal/evaluation/domain/port/`，禁止把 agent/knowledge/skill/mcp infra 引回 evaluation 内部。放**新文件** `rollback.go`：`gate.go` 是门禁主 port（GateStore/GatePolicy 等平台与资源判定面），回滚桥接面（产品后端 + canary 后端 + 候选值类型）职责独立、且同时被 executor（application）与 wiring ACL 适配器引用，拆文件避免 gate.go 膨胀并降低 T13/Task 4 合入冲突面。

```go
package port

import (
	"context"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

// RollbackCandidate 是产品回滚入口的「可回滚历史版本」归一化候选。ACL 适配器按
// kind 语义填充（agent/knowledge 取 versioningdomain.VersionStatusDeprecated；
// skill 取 skilldomain.VersionStatusDeprecated），executor 纯函数在其上选上一好。
type RollbackCandidate struct {
	ID           string // 服务层版本/修订行主键（agent/knowledge version id；skill revision id）
	RevisionNo   int
	IsCurrent    bool
	Rollbackable bool // deprecated 历史版本（可被 Rollback/RollbackRevision 接受）
}

// ProductRollbackBackend 是单个资源 kind 的产品回滚适配面（spec §3.3 path 2）。
// provider：api/wiring 中基于真实 service 的 ACL 适配器（agent/knowledge/skill 各一），
// 由 executor 按 target.Kind 选取。禁止在 evaluation 内部 import 兄弟 context infra。
type ProductRollbackBackend interface {
	// ListCandidates 返回该资源全部可回滚候选（deprecated 历史版本，newest-first）。
	// knowledge 的 resourceID = workspace name（与 evaluation 资源锚点一致）。
	ListCandidates(ctx context.Context, tenantID, resourceID string) ([]RollbackCandidate, error)
	// RollbackProduct 把 resourceID 回滚到 candidateID（服务层内部单事务 + 自带
	// ChangeOpRollback 审计；仅 deprecated 目标可回滚）。
	RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error
}

// CanaryRollbackBackend 是金丝雀坏状态的判定与清除适配面（spec §3.3 path 1）。
// provider：api/wiring 适配器（experimentRepo.ResolveDeployment + ExperimentService.Rollback）。
type CanaryRollbackBackend interface {
	// ResolveDeployment 返回资源当前 deployment（无实验 → ok=false）。
	ResolveDeployment(ctx context.Context, tenantID string, kind domain.ResourceKind,
		resourceID string) (domain.Deployment, bool, error)
	// ClearCanary 通过 ExperimentService.Rollback(CommandRollback) 清 canary，流量回 stable。
	ClearCanary(ctx context.Context, tenantID, experimentID, actorID, reason string) error
}
```

Run: `go vet ./internal/evaluation/domain/port/`
Expected: 无输出（编译通过）。

- [ ] **Step 3: planner 纯函数 + executor（`application/resource_rollback.go`）**

R24：planner（纯函数）与 executor（实现 `port.ResourceRollbackExecutor`）落 `internal/evaluation/application/resource_rollback.go`。上一好语义（recon T11 / E3）：agent/knowledge/skill = 候选里「非 IsCurrent 且 deprecated」的最高 RevisionNo；experiment 目标 = `Deployment.StableRevisionID`（即金丝雀坏 → 清 canary，不留产品变更）。纯函数与执行器分离：函数零 IO、可表驱动；执行器只编排 IO + 调用纯函数 + 调窄 port。

```go
package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"go.uber.org/zap"
)

// errNoRollbackCandidate 产品路径找不到上一好版本（如唯一 published 版本即坏版）。
var errNoRollbackCandidate = errors.New("resource rollback: no previous good version")

// ---------------------------------------------------------------------------
// Planner 纯函数（零 IO；输入由 executor 经窄 port 拉取）
// ---------------------------------------------------------------------------

// previousGoodVersion 返回候选里可回滚的上一好版本：非 IsCurrent、Rollbackable、
// RevisionNo 最高者（对输入顺序不敏感，表驱动覆盖乱序）。
func previousGoodVersion(candidates []port.RollbackCandidate) (port.RollbackCandidate, bool) {
	best, ok := port.RollbackCandidate{}, false
	for _, c := range candidates {
		if c.IsCurrent || !c.Rollbackable {
			continue
		}
		if !ok || c.RevisionNo > best.RevisionNo {
			best, ok = c, true
		}
	}
	return best, ok
}

// isCanaryBadState 判定观测锚定的坏版本是否为金丝雀版本：有 deployment、canary 非空、
// 且 observedRevisionID 命中 canary。命中 → 走 spec §3.3 path 1（清 canary）；否则 path 2。
func isCanaryBadState(dep domain.Deployment, found bool, observedRevisionID string) bool {
	if !found || dep.CanaryRevisionID == "" || observedRevisionID == "" {
		return false
	}
	return observedRevisionID == dep.CanaryRevisionID
}

// ---------------------------------------------------------------------------
// Executor：实现 port.ResourceRollbackExecutor（E2 6 参签名）
// ---------------------------------------------------------------------------

// ResourceRollbackExecutorDeps 是 executor 的窄依赖。products 至少含一个 kind 时
// executor 才有实际可回滚能力；canary 可为 nil（跳过金丝雀判定，只走产品路径）。
type ResourceRollbackExecutorDeps struct {
	Logger   *zap.Logger
	Products map[domain.ResourceKind]port.ProductRollbackBackend // agent/knowledge/skill
	Canary   port.CanaryRollbackBackend                          // 可选
}

// ResourceRollbackExecutor 是无状态执行器（goroutine-safe），按 target 分派到
// canary 后端或对应 kind 产品后端。mcp / 未知 kind / 非资源 scope → ErrRollbackUnsupported。
type ResourceRollbackExecutor struct {
	deps ResourceRollbackExecutorDeps
}

var _ port.ResourceRollbackExecutor = (*ResourceRollbackExecutor)(nil)

// NewResourceRollbackExecutor 构造执行器；Logger 缺省 zap.NewNop()。
func NewResourceRollbackExecutor(deps ResourceRollbackExecutorDeps) *ResourceRollbackExecutor {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	return &ResourceRollbackExecutor{deps: deps}
}

// Rollback 实现 port.ResourceRollbackExecutor。auto 路径 actor="gate"/decidedBy=""/approvalID=""
// （gate_service.execAutoRollback）；manual 路径 decidedBy=审批人/approvalID=审批 id（Task 4
// executeResourceRollback）。有效执行者 = decidedBy（有则取）否则 actor，作为审计 actor 透传。
func (e *ResourceRollbackExecutor) Rollback(ctx context.Context, tenantID string, target domain.GateTarget,
	actor, decidedBy, approvalID string) error {
	if target.Scope != domain.ScopeResource {
		return fmt.Errorf("resource rollback: unsupported scope %q: %w", target.Scope, port.ErrRollbackUnsupported)
	}
	kind := domain.ResourceKind(target.Kind)
	backend, ok := e.deps.Products[kind]
	if !ok {
		// mcp 无产品链；未知 kind 亦不支持（fail-closed，不静默降级）。
		return fmt.Errorf("resource rollback: kind %q has no product rollback backend: %w", kind, port.ErrRollbackUnsupported)
	}
	actingUser := decidedBy
	if actingUser == "" {
		actingUser = actor
	}

	// path 1：金丝雀坏（observedRevision 命中 canary）→ 清 canary 回 stable，全部 kind 通用。
	if e.deps.Canary != nil {
		dep, found, err := e.deps.Canary.ResolveDeployment(ctx, tenantID, kind, target.ResourceID)
		if err != nil {
			return fmt.Errorf("resource rollback: resolve deployment %s/%s: %w", kind, target.ResourceID, err)
		}
		if isCanaryBadState(dep, found, target.RevisionID) {
			reason := fmt.Sprintf("gate rollback: revision %s judged bad (approval %s)", target.RevisionID, approvalID)
			if err := e.deps.Canary.ClearCanary(ctx, tenantID, dep.ExperimentID, actingUser, reason); err != nil {
				return fmt.Errorf("resource rollback: clear canary %s/%s: %w", kind, target.ResourceID, err)
			}
			e.deps.Logger.Info("resource rollback: canary cleared",
				zap.String("tenant", tenantID), zap.String("kind", string(kind)),
				zap.String("resource", target.ResourceID), zap.String("experiment", dep.ExperimentID))
			return nil
		}
	}

	// path 2：产品生效版本坏 → 回滚到上一好版本（deprecated 历史版本）。
	candidates, err := backend.ListCandidates(ctx, tenantID, target.ResourceID)
	if err != nil {
		return fmt.Errorf("resource rollback: list candidates %s/%s: %w", kind, target.ResourceID, err)
	}
	good, ok := previousGoodVersion(candidates)
	if !ok {
		return fmt.Errorf("resource rollback: %s/%s: %w", kind, target.ResourceID, errNoRollbackCandidate)
	}
	if err := backend.RollbackProduct(ctx, tenantID, target.ResourceID, good.ID, actingUser); err != nil {
		return fmt.Errorf("resource rollback: %s/%s to %s: %w", kind, target.ResourceID, good.ID, err)
	}
	e.deps.Logger.Info("resource rollback: product rolled back to previous good",
		zap.String("tenant", tenantID), zap.String("kind", string(kind)),
		zap.String("resource", target.ResourceID), zap.String("version", good.ID),
		zap.String("actor", actingUser))
	return nil
}
```

Run: `go vet ./internal/evaluation/application/`
Expected: 无输出。

- [ ] **Step 4: executor/planner 表驱动单测（`application/resource_rollback_test.go`）**

stub 依赖实现窄 port，测试只测 executor + 纯函数（mock 依赖，不 mock 领域逻辑）；测试意图即文档。

```go
package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

// stubProductBackend 记录调用；err 模拟失败。
type stubProductBackend struct {
	candidates []port.RollbackCandidate
	listErr    error
	rollErr    error
	listCalls  int
	rollCalls  []string // "resourceID->candidateID(actor)"
}

func (s *stubProductBackend) ListCandidates(_ context.Context, _, resourceID string) ([]port.RollbackCandidate, error) {
	s.listCalls++
	return s.candidates, s.listErr
}

func (s *stubProductBackend) RollbackProduct(_ context.Context, _, resourceID, candidateID, actorID string) error {
	s.rollCalls = append(s.rollCalls, resourceID+"->"+candidateID+"("+actorID+")")
	return s.rollErr
}

// stubCanaryBackend 记录判定与清除调用。
type stubCanaryBackend struct {
	dep        domain.Deployment
	found      bool
	resolveErr error
	clearErr   error
	resolves   int
	cleared    []string // "experimentID(actor)"
}

func (s *stubCanaryBackend) ResolveDeployment(_ context.Context, _ string, _ domain.ResourceKind, _ string) (domain.Deployment, bool, error) {
	s.resolves++
	return s.dep, s.found, s.resolveErr
}

func (s *stubCanaryBackend) ClearCanary(_ context.Context, _, experimentID, actorID, _ string) error {
	s.cleared = append(s.cleared, experimentID+"("+actorID+")")
	return s.clearErr
}

func resourceTarget(kind, resourceID, revisionID string) domain.GateTarget {
	return domain.GateTarget{Scope: domain.ScopeResource, Kind: kind, ResourceID: resourceID, RevisionID: revisionID}
}

func TestPreviousGoodVersion(t *testing.T) {
	candidates := []port.RollbackCandidate{
		{ID: "v1", RevisionNo: 1, IsCurrent: false, Rollbackable: true},
		{ID: "v3", RevisionNo: 3, IsCurrent: true, Rollbackable: false},
		{ID: "v2", RevisionNo: 2, IsCurrent: false, Rollbackable: true},
	}
	got, ok := previousGoodVersion(candidates)
	if !ok || got.ID != "v2" {
		t.Fatalf("previousGoodVersion = (%+v, %v), want v2", got, ok)
	}
	if _, ok := previousGoodVersion([]port.RollbackCandidate{{ID: "c", RevisionNo: 1, IsCurrent: true, Rollbackable: false}}); ok {
		t.Fatal("previousGoodVersion should be false when no deprecated non-current candidate")
	}
}

func TestIsCanaryBadState(t *testing.T) {
	dep := domain.Deployment{ExperimentID: "exp-1", StableRevisionID: "rev-s", CanaryRevisionID: "rev-c", CanaryPercent: 10}
	if !isCanaryBadState(dep, true, "rev-c") {
		t.Fatal("observed canary should be judged canary-bad")
	}
	if isCanaryBadState(dep, true, "rev-s") {
		t.Fatal("observed stable revision must NOT be treated as canary-bad")
	}
	if isCanaryBadState(dep, false, "rev-c") {
		t.Fatal("missing deployment must not be treated as canary-bad")
	}
	if isCanaryBadState(domain.Deployment{ExperimentID: "exp-1"}, true, "rev-c") {
		t.Fatal("empty canary revision must not be treated as canary-bad")
	}
}

func TestResourceRollbackExecutorDispatch(t *testing.T) {
	ctx := context.Background()
	t.Run("mcp kind returns ErrRollbackUnsupported", func(t *testing.T) {
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{},
		})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindMCP), "m1", "r1"), "gate", "", "")
		if !errors.Is(err, port.ErrRollbackUnsupported) {
			t.Fatalf("err = %v, want ErrRollbackUnsupported", err)
		}
	})
	t.Run("platform scope returns ErrRollbackUnsupported", func(t *testing.T) {
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{})
		err := ex.Rollback(ctx, "t1", domain.GateTarget{Scope: domain.ScopePlatform, GroupKey: "agent", VersionSeq: 1}, "gate", "", "")
		if !errors.Is(err, port.ErrRollbackUnsupported) {
			t.Fatalf("err = %v, want ErrRollbackUnsupported", err)
		}
	})
	t.Run("canary bad clears canary and skips product rollback", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v2", RevisionNo: 2, Rollbackable: true}}}
		can := &stubCanaryBackend{dep: domain.Deployment{ExperimentID: "exp-1", CanaryRevisionID: "rev-c"}, found: true}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{Logger: nil,
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}, Canary: can})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-c"), "gate", "u-admin", "ap-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(can.cleared) != 1 || can.cleared[0] != "exp-1(u-admin)" {
			t.Fatalf("cleared = %#v, want exp-1(u-admin)", can.cleared)
		}
		if prod.listCalls != 0 || len(prod.rollCalls) != 0 {
			t.Fatalf("product backend must not be called on canary path, list=%d roll=%#v", prod.listCalls, prod.rollCalls)
		}
	})
	t.Run("product path rolls back to previous good with decidedBy actor", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{
			{ID: "v3", RevisionNo: 3, IsCurrent: true},
			{ID: "v2", RevisionNo: 2, Rollbackable: true},
			{ID: "v1", RevisionNo: 1, Rollbackable: true},
		}}
		can := &stubCanaryBackend{dep: domain.Deployment{CanaryRevisionID: "rev-c"}, found: true} // observed != canary
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}, Canary: can})
		if err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-s"), "gate", "u-admin", ""); err != nil {
			t.Fatal(err)
		}
		if len(prod.rollCalls) != 1 || prod.rollCalls[0] != "a1->v2(u-admin)" {
			t.Fatalf("roll calls = %#v, want a1->v2(u-admin)", prod.rollCalls)
		}
	})
	t.Run("auto path falls back actor when decidedBy empty", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v2", RevisionNo: 2, Rollbackable: true}}}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}})
		if err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-s"), "gate", "", ""); err != nil {
			t.Fatal(err)
		}
		if len(prod.rollCalls) != 1 || prod.rollCalls[0] != "a1->v2(gate)" {
			t.Fatalf("roll calls = %#v, want a1->v2(gate)", prod.rollCalls)
		}
	})
	t.Run("no rollback candidate returns wrapped errNoRollbackCandidate", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v3", RevisionNo: 3, IsCurrent: true}}}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindSkill: prod}})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindSkill), "s1", "v3"), "gate", "", "")
		if err == nil || !strings.Contains(err.Error(), errNoRollbackCandidate.Error()) {
			t.Fatalf("err = %v, want errNoRollbackCandidate", err)
		}
	})
	t.Run("list failure propagates", func(t *testing.T) {
		boom := errors.New("boom")
		prod := &stubProductBackend{listErr: boom}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindKnowledge: prod}})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindKnowledge), "k1", "v2"), "gate", "", "")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
	t.Run("canary resolve failure propagates before product path", func(t *testing.T) {
		boom := errors.New("resolve boom")
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v2", RevisionNo: 2, Rollbackable: true}}}
		can := &stubCanaryBackend{resolveErr: boom}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}, Canary: can})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-s"), "gate", "", "")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}
```

Run: `go test ./internal/evaluation/application/ -run 'TestPreviousGoodVersion|TestIsCanaryBadState|TestResourceRollbackExecutorDispatch'`
Expected: PASS。

Commit:

```bash
git add internal/evaluation/domain/port/rollback.go internal/evaluation/application/resource_rollback.go internal/evaluation/application/resource_rollback_test.go
git commit -m "feat(evaluation): 卡 C P2 L3 资源回滚 planner 纯函数 + ResourceRollbackExecutor 分派

Co-Authored-By: Claude <noreply@anthropic.com>"
```

- [ ] **Step 5: ACL 适配器 + wiring 构造器（`api/wiring/resource_rollback.go`）**

R25/R27：窄 port 由 `api/wiring` ACL 适配（agent/knowledge/skill/experiment 真实入口 E3），装配方式可被 Task 4/T13 复用。放**新文件** `api/wiring/resource_rollback.go`，并在 `Evaluation` struct + `buildEvaluation` 装配（Task 4 于 `buildApprovalActionExecutor` 直接消费 `c.Evaluation.ResourceRollbackExecutor`，T13 gate auto 同源）。适配器复用 P1 ACL 模式（evaluation_agent_adapter.go L79-80）：`reqctx.WithTenantID` + `postgres.WithTenant(TenantContext{TenantID, UserID, RoleTenantAdmin})`；写路径叠加 `reqctx.WithSystemActor`（agent/knowledge/skill 的 ownership 均以 system actor 旁路，见各自 ownership.go；审计 actor 透传 actingUser）。

```go
package wiring

import (
	"context"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	knowledgeapp "github.com/byteBuilderX/stratum/internal/knowledge/application"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	skilldomain "github.com/byteBuilderX/stratum/internal/skill/domain"
	versioningdomain "github.com/byteBuilderX/stratum/internal/versioning/domain"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
)

// rollbackWorkerActor 是读路径（版本列表）的租户 ctx 用户标识，语义同 P1
// evaluation adapters 的 "evaluation-worker"（只读列表不产生审计）。
const rollbackWorkerActor = "evaluation-worker"

// rollbackTenantCtx 注入租户上下文：reqctx tenant + postgres schema tenant。
func rollbackTenantCtx(ctx context.Context, tenantID, actorID string) context.Context {
	ctx = reqctx.WithTenantID(ctx, tenantID)
	return postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: actorID, Role: postgres.RoleTenantAdmin,
	})
}

// ---------------------------------------------------------------------------
// agent 产品回滚适配器
// ---------------------------------------------------------------------------

type resourceRollbackAgentAdapter struct {
	agents *agentapp.AgentService
}

func (a *resourceRollbackAgentAdapter) ListCandidates(ctx context.Context, tenantID, resourceID string) ([]evalport.RollbackCandidate, error) {
	versions, err := a.agents.ListVersions(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), resourceID)
	if err != nil {
		return nil, err
	}
	out := make([]evalport.RollbackCandidate, 0, len(versions))
	for _, v := range versions {
		out = append(out, evalport.RollbackCandidate{
			ID: v.ID, RevisionNo: v.VersionNo, IsCurrent: v.IsCurrent,
			Rollbackable: v.Status == string(versioningdomain.VersionStatusDeprecated),
		})
	}
	return out, nil
}

func (a *resourceRollbackAgentAdapter) RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	_, err := a.agents.Rollback(ctx, resourceID, agentapp.RollbackAgentInput{ActorID: actorID, VersionID: candidateID})
	return err
}

// ---------------------------------------------------------------------------
// knowledge 产品回滚适配器（resourceID = workspace name，与 evaluation 资源锚点一致）
// ---------------------------------------------------------------------------

type resourceRollbackKnowledgeAdapter struct {
	svc *knowledgeapp.WorkspaceService
}

func (a *resourceRollbackKnowledgeAdapter) ListCandidates(ctx context.Context, tenantID, resourceID string) ([]evalport.RollbackCandidate, error) {
	versions, err := a.svc.ListWorkspaceVersions(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), tenantID, resourceID)
	if err != nil {
		return nil, err
	}
	out := make([]evalport.RollbackCandidate, 0, len(versions))
	for _, v := range versions {
		out = append(out, evalport.RollbackCandidate{
			ID: v.ID, RevisionNo: v.VersionNo, IsCurrent: v.IsCurrent,
			Rollbackable: v.Status == string(versioningdomain.VersionStatusDeprecated),
		})
	}
	return out, nil
}

func (a *resourceRollbackKnowledgeAdapter) RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	_, err := a.svc.RollbackWorkspace(ctx, tenantID, resourceID, knowledgeapp.RollbackWorkspaceInput{
		ActorID: actorID, VersionID: candidateID,
	})
	return err
}

// ---------------------------------------------------------------------------
// skill 产品回滚适配器（revisionID 语义，自有版本机制）
// ---------------------------------------------------------------------------

type resourceRollbackSkillAdapter struct {
	versions *skillapp.VersionService
}

func (a *resourceRollbackSkillAdapter) ListCandidates(ctx context.Context, tenantID, resourceID string) ([]evalport.RollbackCandidate, error) {
	revs, err := a.versions.ListRevisions(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), resourceID)
	if err != nil {
		return nil, err
	}
	out := make([]evalport.RollbackCandidate, 0, len(revs))
	for _, r := range revs {
		out = append(out, evalport.RollbackCandidate{
			ID: r.ID, RevisionNo: r.RevisionNo, IsCurrent: r.IsCurrent,
			Rollbackable: r.Status == skilldomain.VersionStatusDeprecated,
		})
	}
	return out, nil
}

func (a *resourceRollbackSkillAdapter) RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	return a.versions.RollbackRevision(ctx, resourceID, candidateID, actorID)
}

// ---------------------------------------------------------------------------
// canary 适配器：deployment 判定（E3 repo） + 清 canary（E3 ExperimentService）
// ---------------------------------------------------------------------------

type resourceRollbackCanaryAdapter struct {
	experiments *evalapp.ExperimentService
	deployments evalport.ExperimentRepository
}

func (a *resourceRollbackCanaryAdapter) ResolveDeployment(ctx context.Context, tenantID string, kind evaldomain.ResourceKind, resourceID string) (evaldomain.Deployment, bool, error) {
	return a.deployments.ResolveDeployment(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), tenantID, string(kind), resourceID)
}

func (a *resourceRollbackCanaryAdapter) ClearCanary(ctx context.Context, tenantID, experimentID, actorID, reason string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	_, err := a.experiments.Rollback(ctx, tenantID, experimentID, evalapp.ExperimentCommandInput{
		ActorID: actorID, Reason: reason, IdempotencyKey: "gate-rollback-" + experimentID,
	})
	return err
}

// ---------------------------------------------------------------------------
// 构造器：只装配已就绪的真实 service；Task 4/T13 复用同一来源。
// ---------------------------------------------------------------------------

// buildResourceRollbackExecutor 组装 L3 资源回滚执行器。mcp 恒不入 map（无产品回滚链）。
// 无任何可回滚后端（产品 map 空且 canary nil）→ 返回 nil，GateService auto 保持 skip
// （fail-open，语义同 P1 未装配）。
func (c *Container) buildResourceRollbackExecutor(experimentRepo evalport.ExperimentRepository) evalport.ResourceRollbackExecutor {
	products := map[evaldomain.ResourceKind]evalport.ProductRollbackBackend{}
	if c.Agent != nil && c.Agent.Service != nil {
		products[evaldomain.ResourceKindAgent] = &resourceRollbackAgentAdapter{agents: c.Agent.Service}
	}
	if c.Knowledge != nil && c.Knowledge.WorkspaceService != nil {
		products[evaldomain.ResourceKindKnowledge] = &resourceRollbackKnowledgeAdapter{svc: c.Knowledge.WorkspaceService}
	}
	if c.Skill != nil && c.Skill.VersionService != nil {
		products[evaldomain.ResourceKindSkill] = &resourceRollbackSkillAdapter{versions: c.Skill.VersionService}
	}
	var canary evalport.CanaryRollbackBackend
	if experimentRepo != nil && c.Evaluation != nil && c.Evaluation.ExperimentService != nil {
		canary = &resourceRollbackCanaryAdapter{
			experiments: c.Evaluation.ExperimentService, deployments: experimentRepo,
		}
	}
	if len(products) == 0 && canary == nil {
		return nil
	}
	return evalapp.NewResourceRollbackExecutor(evalapp.ResourceRollbackExecutorDeps{
		Logger:   c.Logger,
		Products: products,
		Canary:   canary,
	})
}
```

注：`api/wiring/evaluation.go` 顶部已 import `evalapp/evaldomain/evalport/evalpersist`；新文件另需 `agentapp/knowledgeapp/skillapp/skilldomain/versioningdomain/postgres/reqctx/zap`（原 `evaluation.go` 已含大部分，作者按需并入或保持文件内自足 import）。

- [ ] **Step 6: `Evaluation` struct + `buildEvaluation` 装配**

`api/wiring/evaluation.go` `type Evaluation struct`（L37-52）加字段：

```go
type Evaluation struct {
	// 既有字段不变...
	// ResourceRollbackExecutor L3 资源回滚执行器（agent/knowledge/skill/canary）。
	// nil = 无任何可回滚后端（未装配）；Task 4 executeResourceRollback / T13 gate auto 消费。
	ResourceRollbackExecutor evalport.ResourceRollbackExecutor
}
```

`buildEvaluation` 中 `c.Evaluation = &Evaluation{...}` 之后、`c.buildApprovalActionExecutor()` 之前，追加：

```go
	// L3 资源回滚执行器（Task 3/T11）：窄 port 绑真实 service 的 ACL 适配器。
	c.Evaluation.ResourceRollbackExecutor = c.buildResourceRollbackExecutor(experimentRepo)
```

Run: `go build ./... && go vet ./api/wiring/`
Expected: 编译通过、vet 无输出。

Commit:

```bash
git add api/wiring/resource_rollback.go api/wiring/evaluation.go
git commit -m "feat(evaluation): 卡 C P2 L3 资源回滚 wiring 构造器 + Evaluation 装配

Co-Authored-By: Claude <noreply@anthropic.com>"
```

- [ ] **Step 7: wiring 构造器 nil-safety 测试（`api/wiring/resource_rollback_test.go`）**

复用 wiring 测试轻量风格（参照 `evaluation_skill_adapter_test.go` 直接构造组件，不依赖 DB 容器）。构造器在兄弟 service 全缺失时返回 nil（gate auto 保持 skip）：

```go
package wiring

import "testing"

func TestBuildResourceRollbackExecutorNilWhenUnwired(t *testing.T) {
	c := &Container{}
	if got := c.buildResourceRollbackExecutor(nil); got != nil {
		t.Fatalf("expected nil executor when no sibling service wired, got %#v", got)
	}
}
```

Run: `go test ./api/wiring/ -run TestBuildResourceRollbackExecutorNilWhenUnwired`
Expected: PASS。

- [ ] **Step 8: 跑门禁相关全量测试 + vet**

Run: `go vet ./internal/evaluation/... ./api/wiring/... && go test -short ./internal/evaluation/application/ ./internal/evaluation/domain/... ./api/wiring/`
Expected: PASS。

Commit:

```bash
git add api/wiring/resource_rollback_test.go
git commit -m "test(evaluation): 卡 C P2 L3 资源回滚 wiring 构造器 nil-safety

Co-Authored-By: Claude <noreply@anthropic.com>"
```

**验证命令**

```bash
# 1. gate port/服务同步编译（Step 1）
go test ./internal/evaluation/application/ -run 'TestHandleObservationRoutesResourceScopeRollbackActions' ./internal/evaluation/domain/
# 2. 窄 port 编译（Step 2）
go vet ./internal/evaluation/domain/port/
# 3. executor/planner 单测（Step 4）
go test ./internal/evaluation/application/ -run 'TestPreviousGoodVersion|TestIsCanaryBadState|TestResourceRollbackExecutorDispatch' -v
# 4. 全仓编译 + wiring 构造器测试（Step 5-7）
go build ./... && go vet ./api/wiring/ && go test ./api/wiring/ -run TestBuildResourceRollbackExecutorNilWhenUnwired
# 5. 门禁域全量（Step 8）
go test -short ./internal/evaluation/application/ ./internal/evaluation/domain/... ./api/wiring/
```

**主会话裁决（原「需协调者定夺」；协调者已定稿）**

1. **executor 语义假设：`target.RevisionID` 视为「观测当前坏版本」**（不是预解析好的回滚目标）。executor 在内部做 IO（`ResolveDeployment` + `ListCandidates`）后，用 planner 纯函数判定（a）是否金丝雀坏 → 清 canary（experiment 入口），否则（b）产品坏 → 回滚到上一好（agent/knowledge/skill 入口）。→ **master 定稿（A8.1）：维持本方案**（E2 签名无第二 id 位，且 spec 明言上一好由 planner 解析）。
2. **窄 port 文件位置**：→ **master 定稿（A8.2）：新文件 `internal/evaluation/domain/port/rollback.go`**（非并入 gate.go；gate.go 只放门禁判定主 port + 两个 sentinel）。
3. **wiring 适配器/构造器文件位置**：→ **master 定稿（A8.3）：新文件 `api/wiring/resource_rollback.go`(+_test)**，seam = `c.Evaluation.ResourceRollbackExecutor`，与 Task 4 装配点一致。
4. **canary 路径对全部产品 kind 通用**：→ **master 定稿（A8.4）：维持**——无 deployment 或 observedRevision 未命中 canary → 产品路径（`isCanaryBadState` 对 `found=false` 返回 false）。
5. **6 参签名扩展触碰 P1 合并代码**（gate_service.go L179 调用点 + gate_service_test.go L78-84 stub），属 E2 既定目标、跨 Task 有意为之；Step 1 一次性同步。→ **master 定稿（A8.5）：维持**；executor auto 语义（actor="gate"、decidedBy=""、approvalID=""）与台账 `rec.Actor:"gate"` 一致。

**Spec 覆盖：§3.3（L3-资源三路径：金丝雀坏→ExperimentService.Rollback、产品坏→agent/knowledge/skill 原生回滚、mcp→不支持；上一好 = 非当前 active 最高序 deprecated / experiment StableRevisionID；auto 默认关闭由策略 + 调用方保障；硬编码确定性映射表——kind→后端、坏状态类型→入口）、§4.3.3（`ResourceRollbackExecutor.Rollback(ctx, tenantID, target, actor, decidedBy, approvalID)` 消费端端口 + mcp→ErrRollbackUnsupported）。有意跳过：GateService 生产 wiring 与 auto 日限（T13/R-M）、审批 `rollback_resource` 分支与 `ErrAutoRollbackForbidden` 消费（Task 4）、eval_gate_actions 台账写回（T13）、mcp 产品版本链（§6 不做）。**

---

## Task 4（spec T10）: 审批执行器三操作分支（rollback_platform/rollback_resource/publish_platform_version）+ platform auto 拒绝不变量

> spec §3.4 + §4.4

**Files:**

- Modify: `internal/parameters/domain/port/store.go` — `PlatformVersion` 补 `EvalState string`（dict E5 读路径接线；migration 044 列已在，**无 DDL**）。
- Modify: `internal/parameters/infrastructure/persistence/platform_repo.go` — `ListVersions`（L408-450）与 `GetVersion`（L454-479）SELECT + Scan 加 `eval_state`。
- Modify: `internal/parameters/application/application_test.go` — memStore `UpdateEvalState`（L177-190）写真实字段 + `TestServiceGateVersionOps` 断言（L771-773）同步（mock/stub 同步，dict B）。
- Modify: `api/wiring/embedding_model_test.go` — fake `UpdateEvalState`（L96-110）Snapshot 占位改真实字段。
- Modify: `api/http/handler/parameter_handler_test.go` — fake `UpdateEvalState`（L175-187）Snapshot 占位改真实字段。
- Modify: `pkg/constants/evaluation.go` — 追加共享 label/state 常量（`GateLayerL3Platform`/`GateActionAutoRefused`/`PlatformEvalStateSentinelPassed`；禁 magic string，dict B；不新增 metric family，R32）。**此三常量为跨任务单一归属 home（compile order：Task 4 先于 Task 5；Task 5 消费 `constants.*`，见「任务依赖与次序」跨任务绑定 B）。**
- Modify: `api/wiring/approval_action.go` — `approvalActionExecutor` 三依赖 + `platformVersionOps` 消费方窄接口 + `newApprovalActionExecutor` 签名 + `evaluationOperations` 三 key + 三分支函数 + auto guard + import/编译期断言。
- Modify: `api/wiring/evaluation.go` — `buildApprovalActionExecutor` 注入 `paramSvc`（typed-nil 安全）、`c.Evaluation.ResourceRollbackExecutor`（Task 3 seam）、`c.platformMetrics()`（R27）。
- Modify: `api/wiring/approval_action_test.go` — dispatch 表 11→14 + 三分支 happy/失败/auto_refused 用例 + fake。

不改：`internal/evaluation/domain/port/gate.go`（`ErrAutoRollbackForbidden`/`ErrRollbackUnsupported` + 6 参 `ResourceRollbackExecutor` 由 **Task 3** 按 dict E2 一并落，T10 只消费）、`internal/evaluation/domain/gate.go`（`GateTarget.Kind` 现名即权威，见「跨任务依赖」#4）、`internal/parameters/application/service.go`（薄转发已满足消费面）、无 DDL、无 proto、无 metric family。

**Interfaces:**

- Consumes（跨 task 引用 dict 契约名）：
  - `E5` parameters `Service.Publish(ctx, groupKey string, versionID int64, actor string) error`、`Service.Rollback(ctx, groupKey string, versionID int64, actor string) error`、`Service.Versions(ctx, groupKey string) ([]port.PlatformVersion, error)`、`Service.UpdateEvalState(ctx, groupKey string, versionSeq int64, state, actor string) error`（实码已核：Publish/Rollback 按**行 PK id**、UpdateEvalState 按 **version_seq** → 分支内经 `Versions()` 做 id→seq 桥）。
  - `E5` `port.PlatformVersion`（读回 `EvalState`；`VersionSeq int`、`ID int64`）；`E4` `port.ApprovalActionRequest{TenantID, SubjectKind, Arguments, ActorID, DecidedBy}`、`ApprovalActionNotExecutedError`；subject_kind 沿用 `agentdomain.SubjectKindEvaluationAction`。
  - `E2`（**Task 3 合并后状态**）`port.ResourceRollbackExecutor.Rollback(ctx, tenantID string, target domain.GateTarget, actor, decidedBy, approvalID string) error`；sentinels `port.ErrAutoRollbackForbidden`、`port.ErrRollbackUnsupported`（Task 3 落 `internal/evaluation/domain/port/gate.go`）。
  - `E1` `evaldomain.ScopeResource`、`evaldomain.GateTarget{Scope, GroupKey, Kind string, ResourceID, RevisionID string, VersionSeq int64}`（合并代码字段 `Kind` 为权威，已核 gate.go L22-29）。
  - `observability.MetricsProvider.IncEvalGateAction(layer, action string)`（无新 family）；`pkg/constants` 本 task 追加常量。
- Produces：
  - `evaluationOperations` 新增 `rollback_platform`→`executePlatformRollback`、`rollback_resource`→`executeResourceRollback`、`publish_platform_version`→`executePlatformPublishGated`（dispatch 表 11→14，`TestEvaluationOperationsComplete` 同步）。
  - `PlatformVersion.EvalState` 经 `ListVersions`/`GetVersion` 读路径对外可见（/admin 版本历史 JSON 增量字段，Task 5 消费；handler 直接返回 `[]port.PlatformVersion`、无 gen DTO/golden 钉死，已核 api/http/testdata 无 `eval_state`/versions 全量断言）。
  - 本地消费方窄接口 `platformVersionOps`（4 方法，`*parametersapp.Service` 满足，`var _ platformVersionOps = (*parametersapp.Service)(nil)` 编译期断言）。

- [ ] **Step 1（前置）**：`bash scripts/quality/risk-regression-guard.sh --explain`，通读 dict R26/R27/R28 + E2/E4/E5 与 recon T10（R-H/R-I/R-J）已核。本 task 依赖 Task 3(T11) 先合（sentinels/6 参 executor/`Evaluation.ResourceRollbackExecutor` seam）。

- [ ] **Step 2: parameters `PlatformVersion` 读路径接线（E5 R-I；无 DDL）**

`internal/parameters/domain/port/store.go` `PlatformVersion` 字段尾（`CreatedAt` 之后）追加：

```go
	CreatedAt time.Time `json:"created_at"`
	// EvalState 是平台门禁对该版本的评测结论（spec §4.1.1：unknown|sentinel_failed|
	// sentinel_passed|anomaly_flag|anomaly_block|rollback_recommended|rollback_executed）。
	// 044 迁移已建列，P2 只接读路径；写路径 UpdateEvalState 已存在。JSON tag 无
	// omitempty：DB 列 NOT NULL，读回恒有值（未过门禁的历史行 = 'unknown'）。
	EvalState string `json:"eval_state"`
}
```

`internal/parameters/infrastructure/persistence/platform_repo.go` `ListVersions` SELECT 在 `v.status` 后加 `v.eval_state`，Scan 同步加 `&v.EvalState`：

```go
	rows, err := r.pool.Query(ctx,
		`SELECT v.id, v.group_key, v.version_seq, v.status, v.eval_state, v.snapshot, v.base_version_id,
		        v.message, v.created_by, v.created_at,
		        (prod.version_id IS NOT NULL) AS is_current
		 FROM public.platform_config_versions v
		 LEFT JOIN public.platform_config_labels prod
		   ON prod.group_key = v.group_key AND prod.label = 'production' AND prod.version_id = v.id
		 WHERE v.group_key = $1
		 ORDER BY v.version_seq DESC`,
		groupKey,
	)
	...
		if err := rows.Scan(&v.ID, &v.GroupKey, &v.VersionSeq, &v.Status, &v.EvalState,
			&snapshot, &base, &v.Message, &v.CreatedBy, &createdAt, &v.IsCurrent); err != nil {
```

`GetVersion` SELECT/Scan 同步：

```go
	const q = `SELECT id, group_key, version_seq, status, eval_state, snapshot
		FROM public.platform_config_versions WHERE group_key = $1 AND version_seq = $2`
	...
	if err := r.pool.QueryRow(ctx, q, groupKey, versionSeq).
		Scan(&v.ID, &v.GroupKey, &v.VersionSeq, &v.Status, &v.EvalState, &snapshot); err != nil {
```

（改动后对文件跑 `gofmt` 对齐列。）

- [ ] **Step 3: mock/stub 同步（grep 驱动，dict B）**

`grep -rn 'Snapshot\["eval_state"\]' internal/parameters api/wiring api/http/handler` 应只命中下列三处 fake，逐处把 Snapshot 占位换成真实 `EvalState` 字段写（与 DB 独立列语义一致），并同步其上方注释。

`internal/parameters/application/application_test.go` memStore（`versions` 为 `map[int64]*port.PlatformVersion`，v 为指针）`UpdateEvalState` L177-190 替换为：

```go
// UpdateEvalState 写平台版本真实 EvalState 字段（与 DB 独立列语义一致）；actor 单独
// 记录到 lastEvalActor，供 service 空 actor 默认 "api" 路径断言。
func (m *memStore) UpdateEvalState(_ context.Context, groupKey string, versionSeq int64, state, actor string) error {
	m.lastEvalActor = actor
	g := m.group(groupKey)
	for _, v := range g.versions {
		if int64(v.VersionSeq) == versionSeq {
			v.EvalState = state
			return nil
		}
	}
	return domain.ErrVersionNotFound
}
```

同文件 `TestServiceGateVersionOps` "UpdateEvalState hit records state" 断言 L771-773 改字段断言（删除 `Snapshot["eval_state"]` 引用）：

```go
		if got.EvalState != "rollback_recommended" {
			t.Fatalf("eval_state = %q, want %q", got.EvalState, "rollback_recommended")
		}
```

`api/wiring/embedding_model_test.go` fake（`versions` 为 `map[string][]port.PlatformVersion`，按索引写切片元素）`UpdateEvalState` L96-110 内 Snapshot 占位分支替换为 `s.versions[groupKey][i].EvalState = state`，删除 `Snapshot` nil-init 三行；保留顶部 `if s.err != nil` 与 `ErrVersionNotFound` 分支。其上方注释同步为「桩用真实 EvalState 字段（与 DB 独立列语义一致）」。

`api/http/handler/parameter_handler_test.go` fake（`f.group(groupKey).versions` 为 `map[int64]*port.PlatformVersion`，v 为指针）`UpdateEvalState` L175-187 内 Snapshot 占位分支替换为 `v.EvalState = state`，删除 `Snapshot` nil-init 三行；其上方注释同步。若删除分支后某 fake 不再使用 `json`，由 goimports 收敛该文件 import，勿动共享引用。

验证：`go test ./internal/parameters/... ./api/http/handler/... ./api/wiring/... -short -count=1`（integration 用真实 DB 的用例无 `STRATUM_TEST_POSTGRES_URL` 时自动 skip）。

- [ ] **Step 4: 共享常量（禁 magic string，dict B；不新增 metric family，R32）**

`pkg/constants/evaluation.go` 文件尾追加（本 task 只用其中 3 个；Task 1/2/5 各自的 label/state 值在同一文件扩展，禁止他处散写）：

```go
// 分层门禁计数 label 与平台版本门禁状态（spec §3.4/§4.1.1；P2 只消费
// eval_gate_action_total 开放 label 与 eval_state 状态文本，不新增 metric family）。
const (
	// GateLayerL3Platform 是平台参数门禁动作层（rollback_manual/auto_refused/publish_gated/…）。
	GateLayerL3Platform = "l3_platform"
	// GateActionAutoRefused 是平台 auto 回滚被拒计数动作（§3.4 无自动不变量）。
	GateActionAutoRefused = "auto_refused"
)

const (
	// PlatformEvalStateSentinelPassed 表示草案已过发布前置哨兵，允许人工确认发布。
	PlatformEvalStateSentinelPassed = "sentinel_passed"
)
```

- [ ] **Step 5: `api/wiring/approval_action.go` — import + 窄接口 + 依赖注入 + dispatch 三 key**

import 块（stdlib/third-party/internal 分组）补四行（`parametersapp` 别名与 evaluation.go L27 一致）：

```go
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	paramport "github.com/byteBuilderX/stratum/internal/parameters/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
```

struct 上方加消费方窄接口，struct 尾（`mcpSvc` 与 `logger` 之间）加三依赖：

```go
// platformVersionOps 是审批执行器对 parameters public 版本操作的最小消费面
// （consumer 定义窄接口；*parametersapp.Service 天然满足，见文件底部编译期断言）。
// 平台参数是 public scope、无 tenant；Publish/Rollback 按行 PK id 寻址，Versions
// 提供 id→seq 桥（E5）。窄接口使 wiring 单测可用 stub 注入 happy path。
type platformVersionOps interface {
	Publish(ctx context.Context, groupKey string, versionID int64, actor string) error
	Rollback(ctx context.Context, groupKey string, versionID int64, actor string) error
	Versions(ctx context.Context, groupKey string) ([]paramport.PlatformVersion, error)
	UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error
}

type approvalActionExecutor struct {
	suiteSvc         *evalapp.SuiteService
	jobSvc           *evalapp.JobService
	baselineSvc      *evalapp.BaselineService
	experimentSvc    *evalapp.ExperimentService
	optimizationSvc  *evalapp.OptimizationService
	casegen          *evalapp.TestCaseGenerator
	candidateSvc     *evalapp.CandidateCommandService
	agentApplier     evalport.AgentRevisionApplier
	mcpSvc           *mcpapp.MCPService
	paramSvc         platformVersionOps                // Task 4：平台版本操作（public；nil → 平台分支 fail closed）
	resourceRollback evalport.ResourceRollbackExecutor // Task 4：L3 资源回滚（Task 3 装配的 ACL 适配器）
	metrics          observability.MetricsProvider     // Task 4：门禁计数（auto_refused）
	logger           *zap.Logger
}
```

`newApprovalActionExecutor` 签名扩展并接线（参数顺序即装配点使用顺序）：

```go
func newApprovalActionExecutor(
	evalComp *Evaluation,
	paramSvc platformVersionOps,
	resourceRollback evalport.ResourceRollbackExecutor,
	mcpComp *MCP,
	metrics observability.MetricsProvider,
	logger *zap.Logger,
) *approvalActionExecutor {
	var mcpSvc *mcpapp.MCPService
	if mcpComp != nil {
		mcpSvc = mcpComp.Service
	}
	return &approvalActionExecutor{
		suiteSvc:         evalComp.SuiteService,
		jobSvc:           evalComp.JobService,
		baselineSvc:      evalComp.BaselineService,
		experimentSvc:    evalComp.ExperimentService,
		optimizationSvc:  evalComp.OptimizationService,
		casegen:          evalComp.TestCaseGenerator,
		candidateSvc:     evalComp.CandidateService,
		agentApplier:     evalComp.AgentRevisionApplier,
		mcpSvc:           mcpSvc,
		paramSvc:         paramSvc,
		resourceRollback: resourceRollback,
		metrics:          metrics,
		logger:           logger,
	}
}
```

`evaluationOperations` 整体替换为 14 键（三新 key 插在 `rollback_experiment` 与 `reject_candidate` 之间；注释同步「14 个写 op」）：

```go
var evaluationOperations = map[string]evaluationActionFunc{
	"create_suite":             executeCreateSuite,
	"publish_suite":            executePublishSuite,
	"generate_suite_cases":     executeGenerateSuiteCases,
	"enqueue_run":              executeEnqueueRun,
	"create_experiment":        executeCreateExperiment,
	"pause_experiment":         executePauseExperiment,
	"promote_experiment":       executePromoteExperiment,
	"rollback_experiment":      executeRollbackExperiment,
	"rollback_platform":        executePlatformRollback,
	"rollback_resource":        executeResourceRollback,
	"publish_platform_version": executePlatformPublishGated,
	"reject_candidate":         executeRejectCandidate,
	"create_baseline":          executeCreateBaseline,
	"generate_optimization":    executeGenerateOptimization,
}
```

文件底部加编译期断言：

```go
// compile-time：parametersapp.Service 满足平台版本操作消费面（R27 wiring 注入具体类型）。
var _ platformVersionOps = (*parametersapp.Service)(nil)
```

- [ ] **Step 6: 三分支函数 + auto guard**

在 `executeRollbackExperiment` 之后、`executeRejectCandidate` 之前追加（包级函数，签名与既有分支一致）：

```go
// guardNoAutoRollback 实现 §3.4「无自动不变量」（L255）：平台 Scope 回滚执行器入口
// 首行断言请求意图非 auto。auto 在编译/接线层面不存在（wiring 不提供平台 auto 分支），
// Arguments 显式 auto=true 属策略违例：返回类型化 sentinel + auto_refused 计数。
// 返回原始错误（终态 unknown_outcome 烧审批，不释放回 approved），避免非法意图在
// 自动化循环里反复重试刷计数；正确 wiring 下恒不触发。
func (e *approvalActionExecutor) guardNoAutoRollback(req port.ApprovalActionRequest) error {
	if !asBool(req.Arguments, "auto") {
		return nil
	}
	if e.metrics != nil {
		e.metrics.IncEvalGateAction(constants.GateLayerL3Platform, constants.GateActionAutoRefused)
	}
	return evalport.ErrAutoRollbackForbidden
}

// executePlatformRollback 执行平台组人工回滚（rollback_platform，R26）：Arguments
// group_key + version_id（行 PK id，与 HTTP path :versionID 同语义）。parameters
// public scope、无 tenant；actor = 审批人 DecidedBy。单事务「错误=无副作用」→ 失败
// notExecuted（审批释放回 approved 可重试）；auto 意图由 guardNoAutoRollback 拒绝。
// 【跨任务：spec §3.4-3 平台回滚成功后的 EnqueueMultiTenantVerify 调用点延迟到 T13——本
// Task 先于 Task 5 合入无法引用其交付函数，且生产仅 T13 审批流可达；见「开放问题 #2」。】
func executePlatformRollback(ctx context.Context, e *approvalActionExecutor, req port.ApprovalActionRequest) (map[string]any, error) {
	if err := e.guardNoAutoRollback(req); err != nil {
		return nil, err
	}
	if e.paramSvc == nil {
		return nil, notExecuted(fmt.Errorf("platform approval executor not configured"))
	}
	groupKey := asString(req.Arguments, "group_key")
	versionID := int64(asInt(req.Arguments, "version_id"))
	if groupKey == "" || versionID <= 0 {
		return nil, notExecuted(fmt.Errorf("rollback_platform: group_key and version_id are required"))
	}
	if err := e.paramSvc.Rollback(ctx, groupKey, versionID, req.DecidedBy); err != nil {
		return nil, notExecuted(err)
	}
	return map[string]any{"status": "rolled_back", "group_key": groupKey}, nil
}

// executeResourceRollback 执行 L3 资源人工回滚（rollback_resource，R26 → Task 3 executor）：
// Arguments resource_kind + resource_id + target_revision_id（+可选 version_id）。目标
// = 回滚到的上一好版本；resourceRollback 是 Task 3 装配的 ACL 适配器，按 Kind 分派
// agent/knowledge/skill/experiment（mcp/未知 → ErrRollbackUnsupported）。各资源回滚单事务
// 「错误=无副作用」→ 失败 notExecuted（含 ErrRollbackUnsupported）。actor/decidedBy =
// 审批人（执行者代表审批人意志，与 executeRejectCandidate 同一 doctrine）；
// approvalID 来源见「跨任务依赖」#2。
func executeResourceRollback(ctx context.Context, e *approvalActionExecutor, req port.ApprovalActionRequest) (map[string]any, error) {
	if e.resourceRollback == nil {
		return nil, notExecuted(fmt.Errorf("resource rollback executor not configured"))
	}
	target := evaldomain.GateTarget{
		Scope:      evaldomain.ScopeResource,
		Kind:       asString(req.Arguments, "resource_kind"),
		ResourceID: asString(req.Arguments, "resource_id"),
		RevisionID: asString(req.Arguments, "target_revision_id"),
		VersionSeq: int64(asInt(req.Arguments, "version_id")),
	}
	if target.Kind == "" || target.ResourceID == "" || target.RevisionID == "" {
		return nil, notExecuted(fmt.Errorf("rollback_resource: resource_kind, resource_id and target_revision_id are required"))
	}
	if err := e.resourceRollback.Rollback(ctx, req.TenantID, target, req.DecidedBy, req.DecidedBy, ""); err != nil {
		return nil, notExecuted(err)
	}
	return map[string]any{"status": "rolled_back", "kind": target.Kind, "resource_id": target.ResourceID}, nil
}

// executePlatformPublishGated 执行平台组人工发布（publish_platform_version，R26 → E5）：
// Arguments group_key + version_id（行 PK id）。前置断言目标版本 eval_state ==
// sentinel_passed（发布哨兵门：未过哨兵的版本禁止发布，fail-closed）；通过后
// Service.Publish + 回写 eval_state=sentinel_passed（§3.4 事1：approve 后系统 actor 调
// store.Publish + 写 eval_state）。单事务「错误=无副作用」→ 失败 notExecuted。
// Task 4 落点无生产者（发布审批由 Task 5 哨兵流创建），本分支天然 fail-closed 待命至 Task 5。
func executePlatformPublishGated(ctx context.Context, e *approvalActionExecutor, req port.ApprovalActionRequest) (map[string]any, error) {
	if e.paramSvc == nil {
		return nil, notExecuted(fmt.Errorf("platform approval executor not configured"))
	}
	groupKey := asString(req.Arguments, "group_key")
	versionID := int64(asInt(req.Arguments, "version_id"))
	if groupKey == "" || versionID <= 0 {
		return nil, notExecuted(fmt.Errorf("publish_platform_version: group_key and version_id are required"))
	}
	versions, err := e.paramSvc.Versions(ctx, groupKey)
	if err != nil {
		return nil, notExecuted(err)
	}
	// version_id 是行 PK id，UpdateEvalState 按 version_seq 寻址 → 经 Versions() 做 id→seq 桥（E5）。
	var target *paramport.PlatformVersion
	for i := range versions {
		if versions[i].ID == versionID {
			target = &versions[i]
			break
		}
	}
	if target == nil {
		return nil, notExecuted(fmt.Errorf("publish_platform_version: version %d not found in group %q", versionID, groupKey))
	}
	if target.EvalState != constants.PlatformEvalStateSentinelPassed {
		return nil, notExecuted(fmt.Errorf("publish_platform_version: version %d (seq %d) eval_state=%q, want %q",
			versionID, target.VersionSeq, target.EvalState, constants.PlatformEvalStateSentinelPassed))
	}
	if err := e.paramSvc.Publish(ctx, groupKey, versionID, req.DecidedBy); err != nil {
		return nil, notExecuted(err)
	}
	// 发布后保持状态标签（§3.4：approve 后写 eval_state=sentinel_passed）。
	if err := e.paramSvc.UpdateEvalState(ctx, groupKey, int64(target.VersionSeq), constants.PlatformEvalStateSentinelPassed, req.DecidedBy); err != nil {
		return nil, notExecuted(err)
	}
	return map[string]any{"status": "published", "group_key": groupKey, "version_seq": target.VersionSeq}, nil
}
```

（分支均 ≤32 行、CC≤3、嵌套≤2，满足门禁；`asBool`/`asString`/`asInt`/`notExecuted`/`fmt`/`evaldomain`/`evalport`/`constants` 复用既有或本 task import。）

- [ ] **Step 7: wiring 装配点（R27；Task 3 seam 消费）**

`api/wiring/evaluation.go` `buildApprovalActionExecutor` 改为：

```go
// buildApprovalActionExecutor 评测组件就绪后装配审批动作执行器。paramSvc 注入前做
// typed-nil 判定：nil *parametersapp.Service 装箱进接口后非 nil，会绕过分支的
// nil 检查并在 nil 接收者方法上 panic；Parameters 未装配/Service nil 时显式传 nil
// 接口（平台分支 fail closed）。ResourceRollbackExecutor 由 Task 3 在
// Evaluation struct 装配（未装配 → nil，rollback_resource 分支 fail closed）。
func (c *Container) buildApprovalActionExecutor() {
	if c.Agent != nil {
		var paramSvc platformVersionOps
		if c.Parameters != nil && c.Parameters.Service != nil {
			paramSvc = c.Parameters.Service
		}
		c.Agent.ActionExecutor = newApprovalActionExecutor(
			c.Evaluation,
			paramSvc,
			c.Evaluation.ResourceRollbackExecutor, // Task 3 seam：Evaluation struct 装配的 ACL 适配器
			c.MCP,
			c.platformMetrics(),
			c.Logger,
		)
	}
}
```

前置依赖（由 Task 3 提供，本 task 编译依赖存在）：`Evaluation` struct 补字段 `ResourceRollbackExecutor evalport.ResourceRollbackExecutor`，并在 `buildEvaluation` 的 `c.Evaluation = &Evaluation{...}` 字面量赋值后装配。`parameters` 先于 `evaluation` 构建（wiring.go L78/L89），`c.platformMetrics()` 已存在（wiring.go L103-108，Platform 未装配时返回 NoopMetrics 非 nil）。

- [ ] **Step 8: dispatch 表完整性测试同步（11→14）**

`api/wiring/approval_action_test.go` `TestEvaluationOperationsComplete`（L57-67）整体替换（含函数上方注释）：

```go
// TestEvaluationOperationsComplete 防 dispatch table 遗漏：14 个写 op 必须全部注册，
// 否则审批执行与直接执行路径分叉（D4 核心不变量）。
func TestEvaluationOperationsComplete(t *testing.T) {
	ops := []string{
		"create_suite", "publish_suite", "generate_suite_cases", "enqueue_run",
		"create_experiment", "pause_experiment", "promote_experiment", "rollback_experiment",
		"rollback_platform", "rollback_resource", "publish_platform_version",
		"reject_candidate", "create_baseline", "generate_optimization",
	}
	require.Len(t, evaluationOperations, len(ops), "dispatch table must cover exactly the 14 evaluation write operations")
	for _, op := range ops {
		require.NotNil(t, evaluationOperations[op], "operation %q missing from dispatch table", op)
	}
}
```

- [ ] **Step 9: 三分支 TDD 用例（追加到 `api/wiring/approval_action_test.go`）**

import 补齐（现有已含 `evaldomain`/`require`/`context`/`port`/`agentdomain`；下列仅缺少才追加，避免重复 import）：

```go
	paramport "github.com/byteBuilderX/stratum/internal/parameters/domain/port"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
```

fake 与 recorder（文件内追加；`fakeResourceRollback.Rollback` 六参签名须与 Task 3 合并后的 `evalport.ResourceRollbackExecutor`（dict E2）一致）：

```go
// gateActionRecorder 记录 IncEvalGateAction，供 auto_refused 计数断言（R28）。
// 内嵌 NoopMetrics 免实现 MetricsProvider 全接口；指针接收者覆盖计数方法。
type gateActionRecorder struct {
	observability.NoopMetrics
	actions []string
}

func (r *gateActionRecorder) IncEvalGateAction(layer, action string) {
	r.actions = append(r.actions, layer+":"+action)
}

// fakePlatformOps 是 platformVersionOps 的最小内存实现：记录 Rollback/Publish/
// UpdateEvalState 收到的参数，Versions 返回预置版本列表，供平台三分支 happy/failure 单测。
type fakePlatformOps struct {
	versions     []paramport.PlatformVersion
	rollbackID   int64
	publishID    int64
	lastState    string
	lastStateSeq int64
	err          error // 非 nil 时所有方法返回该错误（fail-closed 上抛）
}

func (f *fakePlatformOps) Versions(context.Context, string) ([]paramport.PlatformVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.versions, nil
}

func (f *fakePlatformOps) Publish(_ context.Context, _ string, versionID int64, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.publishID = versionID
	return nil
}

func (f *fakePlatformOps) Rollback(_ context.Context, _ string, versionID int64, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.rollbackID = versionID
	return nil
}

func (f *fakePlatformOps) UpdateEvalState(_ context.Context, _ string, versionSeq int64, state, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.lastState, f.lastStateSeq = state, versionSeq
	return nil
}

// fakeResourceRollback 记录 GateTarget，供 rollback_resource 分派断言；err 非 nil 时返回。
type fakeResourceRollback struct {
	calls []evaldomain.GateTarget
	err   error
}

func (f *fakeResourceRollback) Rollback(_ context.Context, _ string, target evaldomain.GateTarget, _, _, _ string) error {
	f.calls = append(f.calls, target)
	return f.err
}
```

用例（分支为包级函数 `fn(ctx, e, req)`，与 `evaluationOperations` 值同形态；不要写成 `e.fn(...)` 方法调用）：

```go
// R28 auto 拒绝不变量：Arguments auto=true → 类型化 sentinel + l3_platform:auto_refused
// 计数；guard 在 paramSvc 任何调用之前（paramSvc 存在时 Rollback 也不得发生）。
func TestPlatformRollbackAutoRefused(t *testing.T) {
	var rec gateActionRecorder
	stub := &fakePlatformOps{}
	e := &approvalActionExecutor{paramSvc: stub, metrics: &rec}
	_, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1",
		Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2), "auto": true},
	})
	require.ErrorIs(t, err, evalport.ErrAutoRollbackForbidden)
	require.Equal(t, int64(0), stub.rollbackID, "auto rollback must not reach paramSvc")
	require.Equal(t, []string{constants.GateLayerL3Platform + ":" + constants.GateActionAutoRefused}, rec.actions)
}

func TestPlatformRollbackHappy(t *testing.T) {
	stub := &fakePlatformOps{}
	e := &approvalActionExecutor{paramSvc: stub}
	out, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1", Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)},
	})
	require.NoError(t, err)
	require.Equal(t, "rolled_back", out["status"])
	require.Equal(t, int64(2), stub.rollbackID)
}

func TestPlatformRollbackMissingArgsFailsClosed(t *testing.T) {
	e := &approvalActionExecutor{paramSvc: &fakePlatformOps{}}
	_, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{
		Arguments: map[string]any{"group_key": "", "version_id": float64(0)},
	})
	notExecutedError(t, err)
}

// publish 前置哨兵断言：eval_state != sentinel_passed → notExecuted（fail-closed），Publish 未发生。
func TestPlatformPublishRequiresSentinelPassed(t *testing.T) {
	stub := &fakePlatformOps{versions: []paramport.PlatformVersion{
		{ID: 2, VersionSeq: 3, Status: "draft", EvalState: "unknown"},
	}}
	e := &approvalActionExecutor{paramSvc: stub}
	_, err := executePlatformPublishGated(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1", Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)},
	})
	notExecutedError(t, err)
	require.Equal(t, int64(0), stub.publishID)
}

// publish happy：id→seq 桥（Publish 收行 PK id=2，UpdateEvalState 收 seq=3）+ 发布后回写 sentinel_passed。
func TestPlatformPublishHappy(t *testing.T) {
	stub := &fakePlatformOps{versions: []paramport.PlatformVersion{
		{ID: 2, VersionSeq: 3, Status: "draft", EvalState: constants.PlatformEvalStateSentinelPassed},
	}}
	e := &approvalActionExecutor{paramSvc: stub}
	out, err := executePlatformPublishGated(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1", Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), stub.publishID)
	require.Equal(t, constants.PlatformEvalStateSentinelPassed, stub.lastState)
	require.Equal(t, int64(3), stub.lastStateSeq)
	require.Equal(t, 3, out["version_seq"]) // VersionSeq 为 int；分支返回 int
}

func TestResourceRollbackDispatchToExecutor(t *testing.T) {
	stub := &fakeResourceRollback{}
	e := &approvalActionExecutor{resourceRollback: stub}
	out, err := executeResourceRollback(context.Background(), e, port.ApprovalActionRequest{
		TenantID: "t1", DecidedBy: "admin-1",
		Arguments: map[string]any{"resource_kind": "agent", "resource_id": "agent-1", "target_revision_id": "rev-9"},
	})
	require.NoError(t, err)
	require.Equal(t, "rolled_back", out["status"])
	require.Len(t, stub.calls, 1)
	require.Equal(t, evaldomain.ScopeResource, stub.calls[0].Scope)
	require.Equal(t, "agent", stub.calls[0].Kind)
	require.Equal(t, "agent-1", stub.calls[0].ResourceID)
	require.Equal(t, "rev-9", stub.calls[0].RevisionID)
}

// rollback_resource 失败（含 Task 3 ErrRollbackUnsupported）单事务无副作用 → notExecuted 可重试。
func TestResourceRollbackFailureNotExecuted(t *testing.T) {
	e := &approvalActionExecutor{resourceRollback: &fakeResourceRollback{err: evalport.ErrRollbackUnsupported}}
	_, err := executeResourceRollback(context.Background(), e, port.ApprovalActionRequest{
		TenantID: "t1", DecidedBy: "admin-1",
		Arguments: map[string]any{"resource_kind": "mcp", "resource_id": "srv-1", "target_revision_id": "rev-9"},
	})
	notExecutedError(t, err)
}

func TestPlatformBranchesFailClosedWhenUnconfigured(t *testing.T) {
	e := &approvalActionExecutor{}
	_, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)}})
	notExecutedError(t, err)
	_, err = executePlatformPublishGated(context.Background(), e, port.ApprovalActionRequest{Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)}})
	notExecutedError(t, err)
	_, err = executeResourceRollback(context.Background(), e, port.ApprovalActionRequest{TenantID: "t1", Arguments: map[string]any{}})
	notExecutedError(t, err)
}
```

- [ ] **Step 10: 验证 + 质量门禁 + commit**

```bash
gofmt -l api/wiring/approval_action.go api/wiring/approval_action_test.go api/wiring/evaluation.go \
  internal/parameters/domain/port/store.go internal/parameters/infrastructure/persistence/platform_repo.go \
  internal/parameters/application/application_test.go api/wiring/embedding_model_test.go \
  api/http/handler/parameter_handler_test.go pkg/constants/evaluation.go   # 期望空输出
go vet ./api/wiring/... ./internal/parameters/... ./api/http/handler/...
go test ./api/wiring/... -run 'TestEvaluationOperationsComplete|TestPlatformRollback|TestPlatformPublish|TestResourceRollback|TestPlatformBranchesFailClosed' -count=1
go test ./internal/parameters/... -run 'TestServiceGateVersionOps' -count=1
go test -race -short ./...   # PR 前完整
```

commit（单 task 独立 commit，标题含 type/scope）：`feat(evaluation): 卡 C P2 审批执行器平台三操作分支 + eval_state 读路径`（body 含 `Co-Authored-By: Claude <noreply@anthropic.com>`）。

**验证命令**（汇总）：`go vet ./api/wiring/... ./internal/parameters/... ./api/http/handler/...`；`go test ./api/wiring/... ./internal/parameters/... ./api/http/handler/... -race -short`；PR 前 `go test -v -race -timeout 30s ./...` 与 `make code-quality`。本 task 无 DDL/proto/新 metric family，不触发 migration/contract/告警质量闸；contract golden 不受影响（已核 api/http/testdata 无 eval_state/versions 全量断言）。

**跨任务依赖与主会话裁决（原「需协调者定夺」；协调者已定稿）**

1. **Task 3 seam 命名**：本 Task 装配行读 `c.Evaluation.ResourceRollbackExecutor`。→ **master 定稿（A6.1）**：Task 3 section 以同名 `ResourceRollbackExecutor evalport.ResourceRollbackExecutor` 字段 + `buildResourceRollbackExecutor` 在 `buildEvaluation` 装配暴露（已对齐）。
2. **`Rollback` 6 参与 `approvalID` 来源（dict E2 6 参 vs P1 现 3 参）**：→ **master 定稿（A6.2）**：Task 3 已把 port 扩为 6 参；`ApprovalActionRequest` 现无 approval id 字段，Task 4 分支传 `approvalID=""`（`decidedBy` 即审批人、`actor` 复用同值）；`GateActionRecord.ApprovalID` 占位，真实 id 透传归 T13（Task 4 不扩 E4 契约）。
3. **auto guard 返回形态（R28 语义细化）**：→ **master 定稿（A6.3）**：`ErrAutoRollbackForbidden` **原样返回（终态 unknown_outcome 烧审批）**而非包 `notExecuted`——auto 在正确 wiring 下不可能出现，烧审批防自动重试刷计数；R28 显式策略违例 carve-out。
4. **`GateTarget` 字段名（dict E1 `ResourceKind` vs 合并代码 `Kind`）**：→ **master 定稿（A6.4）**：Task 3/4 均按现字段 `Kind`（已核 gate.go L22-29），dict E1 已修正为 `Kind`。
5. **`paramSvc` 字段类型**：→ **master 定稿（A6.5）**：消费方窄接口 `platformVersionOps` + wiring 注入具体 `*parametersapp.Service`，装配点 typed-nil 判定。
6. **publish 分支状态机时序**：master 顺序 T11→T10→T12，故 Task 4 合入后到 Task 5 前 `publish_platform_version` 恒因 `sentinel_passed` 未写而 fail-closed（无生产者、无副作用），属预期安全的中间态（Task 5 哨兵流落位后才可放行）。→ **master 定稿（A6.6）**：按 dict E5 + 本 master 采用 sentinel 前置断言。

**Spec 覆盖：§3.4（无自动不变量 L255、发布哨兵人工确认分支的消费端、回滚_manual 审批执行、eval_state 读路径）+ §4.4 平台三操作（rollback_platform/rollback_resource/publish_platform_version）行；§4.1.1 eval_state 只读不新增 DDL；§5 T10 全部验收项。有意跳过：平台发布审批的创建/哨兵编排（Task 5）、eval_gate_actions 台账投影（T13）、resource rollback executor 实现（Task 3）、任何 UI/proto。多租户验证入队调用点延迟到 T13（见「开放问题 #2」）。**

---

## Task 5（spec T12）: L3+ 发布哨兵协调（publishGateCoordinator/RunSentinelForDraft）+ 多租户验证 worker + 分化告警

> spec §3.4
>
> 属 spec：§3.4（四件事 1/3/4 的编排层 + R29/R30/R31）；§5 T12。
> 裁决：R29-R32 + recon T12（R-N/R-O/R-P）；Consumes dict E5/E6/Task 1(E1 RunComparison + `regression_compare.go`)/Task 4(`executePlatformPublishGated` 的 eval_state 写回语义)。
> 目的：把 §3.4「发布哨兵前置门 → 判异人工回滚 → 事后多租户验证 → 分化告警」四件事中**可由 P2 组件化交付的部分**落到 HTTP/wiring 编排层：宿主租户固化（O2/R29）、memory 组纳入快照 capture（R30 用户 09-03 已决：Sub-commit A0 无条件并入 Execution）、哨兵编排器与判定、多租户验证 bounded worker（R31/E9）、两条分化/未恢复告警（F）。低层 `store.Publish/Rollback` 内部、`GateStore` DB 投影、`buildGateService` 生产 wiring、门禁操作台 UI/proto 一律不碰（dict A，归 T13）。

**需用户复核的偏差（用户 09-03 已复核 R30，处置为已解决记录，见下；其余偏差见末尾「开放问题」）**

> ✅ **R30 memory 组偏差（用户 09-03 已决：P2 扩范围，无条件纳入 memory capture；处置完结）**：spec O1「严格全组」（§3.4 L239）vs 代码现实（capture 原只覆盖 evaluation/agent/trace、eval 域快照常量原无 `GroupMemory`）的偏差，用户选择**扩 P2 范围**：把 memory 组纳入快照 capture、**无条件并入 `Execution`**（恒 `[agent, trace, memory]` 三组），不再 fail-closed 拒发、不再归 T13。落地见 **Sub-commit A0**（`snapshot.go` 加 `GroupMemory="memory"`；`api/wiring/evaluation_snapshot.go` Capture 捕获 memory 组并后置追加；测试 len 2→3 + memory 用例）。据此删除原拒发方案全部产物（`GroupMemoryPlatform`/`DecisionRefusedMemory`/`ErrMemoryGroupSentinelUnsupported`/memory guard/`refused_memory` 映射），memory 组与 agent/evaluation/trace 同路径：enabled=false 直通（默认，行为不变）；enabled=true 时 ResolveVersion→draft 检查→SentinelSpec nil → `refused_not_wired`（全组一致，A7.6 前提）。零 DDL（memory 组 + `memory.*` 平台键迁移 043 已 seed；context_snapshot JSONB 追加合法）。哨兵对 draft 的真实执行消费仍属 T13 完成环（所有组一致）。

---

**Goal / Architecture（分层与责任）**

哨兵门是**编排层**产物：`publishGateCoordinator` 不调 `store.Publish`（低层版本机制原样），只产出决策；HTTP handler 据决策决定「直通裸发布 / 拒发 / 待审批」。决策所需 IO 全部经窄 deps func/interface 注入（wiring ACL 组装），使编排逻辑为纯编排、可单测、fail-closed。多租户验证复用既有 worker 骨架（`worker.go` `NewWorker`/`PollOnce`）+ 租户枚举 lister（`evaluationTenantLister`，wiring `evaluation.go` L1505；底层走 IAM `ListActiveTenantIDs`，L585-590），per-tenant fail-open + `evaluation_jobs` 幂等键去重。

责任归属（与 Task 4/T13 的边界）：

- **Task 5 新产**：/admin Publish/Rollback 宿主租户加固（R29）；**Sub-commit A0：快照 capture 纳入 memory 组（R30 用户 09-03 已决，前置改动）**；`publishGateCoordinator`（编排 + 哨兵判定，memory 组与全平台组同路径）；`RunSentinelForDraft`/`DecideSentinel`；多租户验证 worker（enqueue/claim/判定/计数）；两条告警（remote 单一源）+ runbook；`FindLatestCompletedRunForPlatformSeq` run 查询（哨兵基线与验证共用）。
- **Consumes（不重实现）**：Task 1(T8) run 级回归对照（`regression_compare.go` 纯函数，dict E1 `RunComparison`）+ `FindLatestCompletedRunForResource`（E6）；Task 4(T10) 平台发布/回滚审批分支的 eval_state 语义（`sentinel_passed`，Go 常量在 `pkg/constants`）；parameters E5 `Versions/GetVersion/UpdateEvalState`（经 wiring ACL，见 Interfaces）。
- **明确留给 T13**：handler 侧「哨兵通过 → 建 `ToolApproval(publish_platform_version)` → approve 后由系统 actor 调 `store.Publish`」的完整 gated 流、`GateStore` DB 投影/台账、`buildGateService` 生产 wiring、哨兵 suite 真实解析源（§3.6 tier）、门禁台 UI/proto。Task 5 只做「编排器 + 判定 + 拒发 + 验证 worker + 告警」，`gate.enabled=true` 在 P2 的通过路径返回 **待审批/待接线** 语义（见下），不静默直发。

---

**Files**

- Modify `api/wiring/evaluation_snapshot.go`（Sub-commit A0：Capture 捕获 memory 组并入 Execution）
- Modify `internal/evaluation/domain/snapshot.go`（Sub-commit A0：加 `GroupMemory` const + Execution 注释）
- Modify `api/wiring/evaluation_snapshot_test.go`（Sub-commit A0：`TestSnapshotCapturerCaptureFull` len 2→3 + memory 断言；新增 memory 用例）
- Modify `api/http/router.go`（`registerParameterWriteRoutes`：Publish/Rollback 挂 `InjectTenantContext`+`RequireDefaultTenant`）
- Modify `api/http/handler/parameter_handler.go`（nil-safe 发布闸 seam + 决策渲染）
- Create `internal/evaluation/application/publish_gate.go`（编排器 + 哨兵判定；纯编排，memory 组与全平台组同路径）
- Create `internal/evaluation/application/publish_gate_test.go`
- Create `internal/evaluation/domain/publish_gate_const.go`（本 task 独有 layer/action/eval_state 共享常量；**`l3_platform`/`sentinel_passed` 不在此定义**——见跨任务常量单一归属 B1；memory 组常量唯一 home = `snapshot.go` `GroupMemory`，Sub-commit A0）
- Create `internal/evaluation/application/multitenant_verify.go`（验证 payload + enqueue + runner）
- Create `internal/evaluation/application/multitenant_verify_test.go`
- Modify `internal/evaluation/infrastructure/persistence/run_repository.go`（新增 `FindLatestCompletedRunForPlatformSeq`）
- Modify `internal/evaluation/domain/evaluation.go`（新增 `JobTypePlatformVerify` 常量，与 `JobTypeEvalRun` 并列；app/infra 两侧经 `domain` 引用，避免 infra→application 依赖）
- Modify `internal/evaluation/domain/port/evaluation.go`（`RunRepository` 增方法 + `JobPlatformVerifyRepo` 窄面）+ grep 同步全部 test mock/stub
- Modify `internal/evaluation/infrastructure/persistence/job_repository.go`（`EnqueuePlatformVerify`/`ClaimPlatformVerify` 两方法；**tenant-scoped 访问必须经 `execTenant`**，与既有 `Enqueue`/`Claim` 同纪律）
- Modify `api/wiring/evaluation.go`（装配验证 worker `Start`/`Stop` + `TenantLister`；暴露 publish gate 依赖组装函数）
- Create `api/wiring/publish_gate.go`（wiring 依赖组装 + `runCompareAdapter` 跨任务适配器）
- Create `api/wiring/publish_gate_test.go`（wiring 层依赖拼装单测，栅栏默认直通 + gate.enabled=true 拒发）
- Modify `pkg/constants/evaluation.go`（验证窗口/未恢复门槛常量；`GateLayerL3Platform`/`PlatformEvalStateSentinelPassed` 已由 Task 4 定义，**本 task 不重复**）
- Modify `monitoring/remote/rules/stratum-evaluation.yaml`（2 条新规则，**environment: remote-test 单一源**）
- Modify `monitoring/remote/generated/stratum-prometheus-rules.yaml` + `monitoring/local/rules/stratum-evaluation.yml`（render 产物，随 commit 落库，禁止手编）
- Modify `docs/operations/alerts/stratum-evaluation.md`（2 个 runbook section + anchor）

---

**Interfaces**

**Consumes（dict 契约，验证后照抄）**

- Task 1(T8) `internal/evaluation/application/regression_compare.go`：run 级回归纯函数 → `domain.RunComparison{Regressed bool, BaselineSeq, ConfirmedSeq int64, DimensionDeltas map[string]float64}`（E1）。导出名 = `CompareRunRegression`（Task 1 section 已定稿）；本编排器经 deps 注入避免硬依赖（见「任务依赖与次序」跨任务绑定 A——wiring 用 `runCompareAdapter` 衔接）。
- Task 1(T8) `RunRepository.FindLatestCompletedRunForResource(ctx, tenantID string, ref domain.ResourceRef, suiteRevisionID string) (*domain.EvalRun, error)`（E6；nil,nil = 无基线；过滤语义 = Kind+ResourceID+suiteRevisionID，见跨任务绑定 C）。
- Task 4(T10) eval_state 写回语义（`sentinel_passed` 常量 = `constants.PlatformEvalStateSentinelPassed`，随 `executePlatformPublishGated` 使用；Task 5 写 `domain.EvalStateSentinelFailed` 于 block 路径、`constants.PlatformEvalStateSentinelPassed` 于 pass 路径）。
- parameters E5：`Service.Versions(ctx, groupKey) ([]port.PlatformVersion, error)`、`Service.UpdateEvalState(ctx, groupKey string, versionSeq int64, state, actor string) error`（actor 空 → "api"）。**仅经 wiring ACL，eval 应用不 import parameters。**
- E9：`TenantLister.ListTenantIDs(ctx) ([]string, error)`（wiring `evaluationTenantLister{pool: db}`）；worker 骨架 `NewWorker(lister, runner, idleInterval, metrics)`/`TenantJobRunner.RunOnce`。
- `observability.MetricsProvider.IncEvalGateAction(layer, action string)`（CounterVec label 开放，无需注册；R32）。

**Produces（下游/自己测试消费）**

- 宿主租户加固：Publish/Rollback 路由补 `middleware.InjectTenantContext()` + `middleware.RequireDefaultTenant()`（R29；宿主 = default tenant；解析失败 403 fail-closed）。
- `internal/evaluation/domain/publish_gate_const.go`（**跨任务常量单一归属：`l3_platform` 与 `sentinel_passed` 由 Task 4 定义于 `pkg/constants/evaluation.go`（`GateLayerL3Platform`/`PlatformEvalStateSentinelPassed`），本文件不重复定义，引用方统一写 `constants.*`**；下列为本文件保留常量）：
  - layer/action：`LayerL2="l2"`、`LayerL3Sentinel="l3_sentinel"`、`LayerL3MultiTenantVerify="l3_multitenant_verify"`；`ActionRegression="regression"`、`ActionBlock="block"`、`ActionPass="pass"`、`ActionPublishGated="publish_gated"`、`ActionPublishBlocked="publish_blocked"`、`ActionQueued="queued"`、`ActionRecovered="recovered"`、`ActionNotRecovered="not_recovered"`（R31 精确值）。`LayerL3Platform`（= `l3_platform`）引用 `constants.GateLayerL3Platform`。
  - eval_state：`EvalStateSentinelFailed="sentinel_failed"`（block 写回）；`EvalStateSentinelPassed`（= `sentinel_passed`）引用 `constants.PlatformEvalStateSentinelPassed`。
- `internal/evaluation/application/publish_gate.go`：

  ```go
  type SentinelTarget struct {
      Resource        domain.ResourceRef
      SuiteRevisionID string
  }
  // SentinelVerdict 哨兵判定结论（run 级回归 pass/block，§3.2-②同判据）。
  type SentinelVerdict string

  const (
      SentinelVerdictPass  SentinelVerdict = "pass"
      SentinelVerdictBlock SentinelVerdict = "block"
  )

  type SentinelDecision struct {
      Verdict      SentinelVerdict    // pass | block
      BaselineSeq  int64              // 生产基线 run 锚 seq（无基线 0）
      ConfirmedSeq int64              // 哨兵 run 锚 seq（= 草案 seq）
      Deltas       map[string]float64 // 维度 delta（哨兵 vs 基线；基线 nil 空）
  }

  type PublishGateRequest struct {
      GroupKey  string
      VersionID int64 // 行 PK id（HTTP path 语义，E5）
      Actor     string
  }

  type PublishGateDecision int

  const (
      DecisionPassThrough    PublishGateDecision = iota // gate 关闭 → 调用方直通裸发布（默认）
      DecisionApprovalPending                            // 哨兵通过 → 待人工审批（T13 完成）
      DecisionBlocked                                    // 哨兵 block/失败 → 拒发（eval_state=sentinel_failed）
      DecisionRefusedNotWired                            // 哨兵 suite 解析源未接线（T13）→ fail-closed 拒发
  )

  type PublishGateResult struct {
      Decision PublishGateDecision
      Message  string
      RunID    string // 哨兵 run（DecisionBlocked/ApprovalPending 时）
  }

  type PublishGateDeps struct {
      Logger  *zap.Logger
      Metrics observability.MetricsProvider // nil-safe
      // GateEnabled 实时读 evaluation.gate.enabled（nil → false）。默认 false → 直通。
      GateEnabled func(ctx context.Context) bool
      // UpdateEvalState 平台版本写回（wiring → parameters Service.UpdateEvalState，E5）。
      UpdateEvalState func(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error
      // ResolveVersion id→seq 桥（wiring → parameters Service.Versions 匹配 ID，E5）。
      ResolveVersion func(ctx context.Context, groupKey string, versionID int64) (seq int, status string, isCurrent bool, ok bool, err error)
      // SentinelSpec 解析哨兵目标（resource+suite，宿主租户维度；P2 wiring 恒 nil → 拒发，T13 接真实 suite 源）。
      SentinelSpec func(ctx context.Context, hostTenantID, groupKey string, draftSeq int) (SentinelTarget, error)
      // EnqueueSentinel 入队哨兵 run（wiring 绑 application.JobService.EnqueueRun，入参携带 PlatformSeqOverrides{groupKey:draftSeq}）。
      EnqueueSentinel func(ctx context.Context, tenantID string, in EnqueueRunInput) (string, error)
      // BaselineRun 返回该哨兵目标的最近 completed 基线 run（wiring 包 Task 1 FindLatestCompletedRunForResource，
      // 排除哨兵自身 runID；nil,nil = 无基线 → 无回归信号，判定 pass）。
      BaselineRun func(ctx context.Context, tenantID string, target SentinelTarget, excludeRunID string) (*domain.EvalRun, error)
      // Compare run 级回归（wiring 经 runCompareAdapter 绑 Task 1 CompareRunRegression；
      // nil → DecideSentinel 返回错误 fail-closed）。
      Compare func(baseline, current *domain.EvalRun) (domain.RunComparison, error)
  }

  func NewPublishGateCoordinator(deps PublishGateDeps) *PublishGateCoordinator
  // GatePublish 是 handler 调用的单一入口：关闭→PassThrough；enabled=true 下→
  // ResolveVersion 失败拒发 / SentinelSpec 未接线→RefusedNotWired / 有 spec→
  // RunSentinelForDraft 入队（run 未完成前返回 DecisionApprovalPending，携带 RunID）。
  func (c *PublishGateCoordinator) GatePublish(ctx context.Context, hostTenantID string, req PublishGateRequest) (PublishGateResult, error)
  // RunSentinelForDraft 对草案 seq 入队哨兵 run（EnqueueRun + PlatformSeqOverrides），返回 runID。
  // draftSeq 为 ResolveVersion 已解析的草案 version_seq（调用方传入，避免二次解析）。
  func (c *PublishGateCoordinator) RunSentinelForDraft(ctx context.Context, hostTenantID string, req PublishGateRequest, draftSeq int, spec SentinelTarget) (string, error)
  // DecideSentinel 在哨兵 run 完成后判定：sentinel nil（未完成/未找到）→ Blocked（fail-closed）；
  // 基线 nil → Pass（无回归信号）；Compare(基线, 哨兵).Regressed → Blocked 否则 Pass。计数/写回副作用
  // 在此方法内：pass → {l3_sentinel,pass}+{l3_platform,publish_gated}+UpdateEvalState(sentinel_passed)；
  // block → {l3_sentinel,block}+{l2,regression}+UpdateEvalState(sentinel_failed)。
  func (c *PublishGateCoordinator) DecideSentinel(ctx context.Context, hostTenantID string, groupKey string, baseline, sentinel *domain.EvalRun) (SentinelDecision, error)
  ```

- `api/http/handler/parameter_handler.go` seam（handler 自持最小接口，防 wiring/handler 环依赖；**类型导出**，因 wiring 需命名它做 Container 字段/返回类型）：

  ```go
  // PublishGateFunc 是 Publish 前置发布闸（nil = 未装配，保持裸发布；Task 5 装配编排器，
  // 行为默认不变——gate.enabled=false 返回 passthrough）。
  // decision ∈ {"passthrough","approval_pending","blocked","refused_not_wired"}
  type PublishGateFunc func(ctx context.Context, groupKey string, versionID int64, actor string) (decision, message, runID string, err error)
  func (h *ParameterHandler) SetPublishGate(g PublishGateFunc)
  ```

- `internal/evaluation/domain/evaluation.go`（Modify）：`JobTypeEvalRun` 旁加 job 类型常量 + 平台验证 payload/job 实体（domain 单一 home，port/app/infra 共用；`evaluation_jobs.job_type` 无 CHECK，零 DDL）：

  ```go
  const JobTypePlatformVerify = "platform_verify"

  // PlatformVerifyPayload 持久化于 evaluation_jobs.payload（JSONB）；host 租户来自动作载荷（O2/R29）。
  type PlatformVerifyPayload struct {
      GroupKey string `json:"group_key"`
      FromSeq  int64  `json:"from_seq"` // 回滚离开的坏版本 seq（曾为 production）
      ToSeq    int64  `json:"to_seq"`   // 回滚到的目标 seq（当前 production）
      Actor    string `json:"actor"`
  }

  // PlatformVerifyJob 是 ClaimPlatformVerify 返回的本地 job 视图（DB 行 → runner 消费）。
  type PlatformVerifyJob struct {
      ID       string
      TenantID string
      Payload  PlatformVerifyPayload
  }
  ```

- `internal/evaluation/application/multitenant_verify.go`（Create；payload 类型用 `domain.PlatformVerifyPayload`）：

  ```go
  // VerifyIdempotencyKey 生成确定性幂等键（tenant 表 UNIQUE(idempotency_key) 去重；同组同 seq 只跑一次）。
  func VerifyIdempotencyKey(groupKey string, fromSeq, toSeq int64) string
  // MultiTenantVerifyRunner 实现 TenantJobRunner.RunOnce：Claim 一条本租户 platform_verify job →
  // 找 from/to seq 锚定 run → Compare → recovered/not_recovered 计数（R31）。per-tenant fail-open。
  type MultiTenantVerifyRunner struct { deps MultiTenantVerifyDeps }
  type MultiTenantVerifyDeps struct {
      Logger  *zap.Logger
      Metrics observability.MetricsProvider
      Repo    port.JobPlatformVerifyRepo
      Runs    port.RunRepository
      Compare func(baseline, current *domain.EvalRun) (domain.RunComparison, error) // Task 1 对照（runCompareAdapter）
  }
  func NewMultiTenantVerifyRunner(deps MultiTenantVerifyDeps) *MultiTenantVerifyRunner
  func (r *MultiTenantVerifyRunner) RunOnce(ctx context.Context, tenantID, workerID string, lease time.Duration) (bool, error)
  // EnqueueMultiTenantVerify 供平台回滚执行器（T13 在 executePlatformRollback 成功路径调用——
  // Task 4 先于 Task 5 合入无法引用本函数，P2 无调用者属安全；见「开放问题 #2」）；
  // enqueue 成功 IncEvalGateAction(l3_multitenant_verify, queued)。幂等键冲突 → inserted=false → 不重复 +queued（去重）。
  func EnqueueMultiTenantVerify(ctx context.Context, repo port.JobPlatformVerifyRepo, tenantID string, p domain.PlatformVerifyPayload, createdBy string, metrics observability.MetricsProvider) error
  ```

- `internal/evaluation/domain/port/evaluation.go` 增 RunRepository 方法 + 平台验证窄面；`run_repository.go` 落实现（**execTenant + postgres.WithTenant 纪律同 Task 1**）：

  ```go
  // RunRepository 增：
  // FindLatestCompletedRunForPlatformSeq 返回 tenant 下最近一条 completed run，其 context_snapshot
  // 中 groupKey 组 version_seq == seq（在指定平台配置版本下执行的最近 run）；无 → nil,nil。
  FindLatestCompletedRunForPlatformSeq(ctx context.Context, tenantID, groupKey string, seq int64) (*domain.EvalRun, error)

  // JobPlatformVerifyRepo 复用既有 evaluation_jobs 表（无 DDL；job_type 列无 CHECK）。
  type JobPlatformVerifyRepo interface {
      // EnqueuePlatformVerify 幂等插入（job_type=domain.JobTypePlatformVerify，ON CONFLICT
      // (idempotency_key) DO NOTHING）；返回是否新插入（已存在 → false，调用方据此不重复 +queued）。
      EnqueuePlatformVerify(ctx context.Context, tenantID string, p domain.PlatformVerifyPayload, idempotencyKey, createdBy string) (bool, error)
      // ClaimPlatformVerify 只取本租户 job_type='platform_verify' 的一条（queued/running 过期）。
      ClaimPlatformVerify(ctx context.Context, tenantID, workerID string, lease time.Duration) (*domain.PlatformVerifyJob, error)
  }
  ```

  （两方法实现落 `internal/evaluation/infrastructure/persistence/job_repository.go` + wiring ACL 组装；不改既有 `EvaluationJob`/`Enqueue`/`Claim` 语义。payload JSONB 按 pgx v5 规则先 `json.Marshal` 再传 string。）

- 告警（F 定稿 + 本 section 微调 for/阈值；**remote 单一源，environment: remote-test**）：
  见 TDD 步骤 yml 全文 + runbook section。

---

### Sub-commit A0：快照 capture 纳入 memory 组（R30 扩展前置，用户 09-03 已决）

> 作用：把 memory 平台组**无条件并入**评测上下文快照 `Execution`（恒 `[agent, trace, memory]` 三组，与 agent/trace 现状一致），为 Task 5 发布哨兵/归因提供 memory 组固定值基座，coordinator 对 memory 不再特判（删 `GroupMemoryPlatform` 拒发路径，见 B1/B2）。**零 DDL、spec 零改动**：memory 组 + 22 个 `memory.*` 平台键迁移 043 已 seed `platform_config_groups('memory','Memory')`（parameters registry 全 4 组，spec §3.4 L241 同集合）；`context_snapshot` JSONB 追加一组合法，consumer 按 GroupKey 匹配（`injectExecutionSnapshot`/`projectExecutionSnapshot`），后置追加不破 `Execution[0..1]` 索引。哨兵对 draft 的真实执行消费仍属 T13 完成环（所有组一致）。

- [ ] **Step A0-1：写失败测试**

Modify `api/wiring/evaluation_snapshot_test.go` `TestSnapshotCapturerCaptureFull`（L198-201；fixture versions 无 memory 发布 → 空组 `{GroupKey:"memory"}` 后置追加，captureGroup 已覆盖）：

```go
	require.Equal(t, evaldomain.GroupEvaluation, snap.Evaluation.GroupKey)
	require.Len(t, snap.Execution, 3)
	require.Equal(t, evaldomain.GroupAgent, snap.Execution[0].GroupKey)
	require.Equal(t, evaldomain.GroupTrace, snap.Execution[1].GroupKey)
	require.Equal(t, evaldomain.GroupMemory, snap.Execution[2].GroupKey)
```

同文件新增用例（memory 组带 seq 历史，仿既有 agent override 用例 L211-251 结构；Snapshot 用空 map 仿 trace，不臆造 memory 键名）：

```go
func TestSnapshotCapturerCaptureOverridePinsHistoricalMemoryVersion(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)
	capturer.params = parametersapp.NewService(parametersdomain.NewParametersRegistry(),
		&fakePlatformStore{versions: map[string][]port.PlatformVersion{
			evaldomain.GroupEvaluation: {
				{GroupKey: evaldomain.GroupEvaluation, VersionSeq: 3, IsCurrent: true, Snapshot: map[string]json.RawMessage{
					"evaluation.judge.enabled": json.RawMessage(`true`),
				}},
			},
			evaldomain.GroupAgent: {
				{GroupKey: evaldomain.GroupAgent, VersionSeq: 5, IsCurrent: true, Snapshot: map[string]json.RawMessage{}},
			},
			evaldomain.GroupTrace: {
				{GroupKey: evaldomain.GroupTrace, VersionSeq: 1, IsCurrent: true, Snapshot: map[string]json.RawMessage{}},
			},
			evaldomain.GroupMemory: {
				{GroupKey: evaldomain.GroupMemory, VersionSeq: 1, Snapshot: map[string]json.RawMessage{}},
				{GroupKey: evaldomain.GroupMemory, VersionSeq: 2, IsCurrent: true, Snapshot: map[string]json.RawMessage{}},
			},
		}})

	snap, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1",
		},
		SuiteRevisionID:      "suite-1",
		RequestedBy:          "user-1",
		PlatformSeqOverrides: map[string]int64{evaldomain.GroupMemory: 1},
	})
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Len(t, snap.Execution, 3)
	require.Equal(t, evaldomain.GroupAgent, snap.Execution[0].GroupKey)
	require.Equal(t, evaldomain.GroupTrace, snap.Execution[1].GroupKey)
	require.Equal(t, evaldomain.GroupMemory, snap.Execution[2].GroupKey)
	require.Equal(t, int64(1), snap.Execution[2].VersionSeq) // override 命中历史 seq 1，非 IsCurrent 2
}
```

Run: `go test ./api/wiring/ -run 'TestSnapshotCapturerCaptureFull|TestSnapshotCapturerCaptureOverridePinsHistorical' -v`
Expected: FAIL（编译错误 `evaldomain.GroupMemory undefined` —— const 未加）。

- [ ] **Step A0-2：实现（domain const + Capture append）**

Modify `internal/evaluation/domain/snapshot.go` const 块（L8-13）：

```go
const (
	SnapshotSchemaVersion = 1
	GroupEvaluation       = "evaluation" // 与 parameters 域 PlatformGroupEvaluation 值一致
	GroupAgent            = "agent"
	GroupTrace            = "trace"
	GroupMemory           = "memory" // 与 parameters 域 GroupMemory 值一致（R30 用户 09-03：无条件并入评测快照 capture）
)
```

Modify `snapshot.go` L39 `Execution` 字段注释（`// agent + trace 组（被测启用 memory 时追加 memory 组）` →）：

```go
	Execution []GroupSnapshot // agent + trace + memory 组（全部注册平台组；R30 用户 09-03：无条件并入，恒三组）
```

Modify `api/wiring/evaluation_snapshot.go` `Capture()`（L71-75，traceGroup 后追加 memoryGroup）：

```go
	traceGroup, err := c.captureGroup(ctx, evaldomain.GroupTrace, overrideSeq(input, evaldomain.GroupTrace))
	if err != nil {
		return nil, err
	}
	memoryGroup, err := c.captureGroup(ctx, evaldomain.GroupMemory, overrideSeq(input, evaldomain.GroupMemory))
	if err != nil {
		return nil, err
	}
	snap.Execution = []evaldomain.GroupSnapshot{agentGroup, traceGroup, memoryGroup}
```

Run: `go build ./...`
Expected: PASS

- [ ] **Step A0-3：跑相关测试 + 全仓索引核对**

Run: `go test ./api/wiring/ ./internal/evaluation/domain/ -run 'Capture|Snapshot' -v`
Expected: 全 PASS。既有 `TestSnapshotCapturerCaptureOverridePinsHistoricalAgentVersion` 依赖 `Execution[0]` 索引稳定——后置追加不破；`TestSnapshotCapturerCaptureGroupUnpublishedReturnsEmptyGroup` 用 `"unpublished"` 裸串组，不受影响。

随后核对无其它对 `Execution` 长度/索引的假设：
Run: `grep -rn "snap.Execution\|Execution\[" api/ internal/ | grep -v _test.go`
Expected: 生产代码仅 `evaluation_snapshot.go` 组装处构造 `snap.Execution`；消费侧（`injectExecutionSnapshot`/`projectExecutionSnapshot`）按 GroupKey 匹配，不按位置取第三组。

- [ ] **Step A0-4：Commit**

```bash
git add internal/evaluation/domain/snapshot.go api/wiring/evaluation_snapshot.go api/wiring/evaluation_snapshot_test.go
git commit -m "feat(evaluation): 卡C R30 扩展——评测快照 capture 无条件纳入 memory 组（Execution 恒 [agent, trace, memory]）

Co-Authored-By: Claude <noreply@anthropic.com>"
```

Run: `go test ./api/wiring/ ./internal/evaluation/domain/ -short`
Expected: PASS

---

### Sub-commit A：宿主租户加固（R29）+ handler 发布闸 seam

- [ ] **Step A1：路由挂宿主租户中间件**

Modify `api/http/router.go` `registerParameterWriteRoutes`——Publish/Rollback 移到带 `InjectTenantContext`+`RequireDefaultTenant` 的子组；PUT/CreateDraft 保持原样（不挪生产 label，不需宿主租户）：

```go
// registerParameterWriteRoutes wires the unified parameter registry write
// endpoints, which remain gated by the parent group's system_admin middleware.
// R29/O2：Publish/Rollback 移动 production label（public 平台参数影响全租户），请求
// 的 reqctx 宿主租户必须 = default(host) tenant：InjectTenantContext 由 auth.tenant_id
// 填充 reqctx → RequireDefaultTenant 非 default 一律 403 fail-closed。
func registerParameterWriteRoutes(adminGroup *gin.RouterGroup, c *wiring.Container) {
	if c.Parameters == nil || c.Parameters.Service == nil {
		return
	}
	paramHandler := handler.NewParameterHandler(c.Parameters.Service, c.Logger)
	adminGroup.PUT("/parameters", paramHandler.Update)
	adminGroup.POST("/parameters/versions/:groupKey", paramHandler.CreateDraft)
	hostWrite := adminGroup.Group("", middleware.InjectTenantContext(), middleware.RequireDefaultTenant())
	hostWrite.POST("/parameters/versions/:groupKey/:versionID/publish", paramHandler.Publish)
	hostWrite.POST("/parameters/versions/:groupKey/:versionID/rollback", paramHandler.Rollback)
}
```

Run: `go build ./...`
Expected: PASS（middleware 包名已在 router.go import 集合；若缺 `InjectTenantContext` import 由 gofmt 补全）。

- [ ] **Step A2：handler 加 nil-safe 发布闸 seam**

Modify `api/http/handler/parameter_handler.go`：`ParameterHandler` 加字段 + setter，`Publish` 在裸发布前询问闸；非 `passthrough` 一律不触 `h.svc.Publish`（fail-closed，不静默直发）：

```go
// PublishGateFunc 见 Interfaces。nil（默认）= 未装配 → 维持现状裸发布。
type PublishGateFunc func(ctx context.Context, groupKey string, versionID int64, actor string) (decision, message, runID string, err error)

type ParameterHandler struct {
	svc         *paramapp.Service
	logger      *zap.Logger
	publishGate PublishGateFunc
}

func (h *ParameterHandler) SetPublishGate(g PublishGateFunc) { h.publishGate = g }
```

`Publish` 改为：

```go
func (h *ParameterHandler) Publish(c *gin.Context) {
	versionID, ok := parseVersionID(c)
	if !ok {
		return
	}
	groupKey := c.Param("groupKey")
	actor := c.GetString(middleware.ContextKeySub)
	if h.publishGate != nil {
		decision, message, runID, err := h.publishGate(c.Request.Context(), groupKey, versionID, actor)
		if err != nil {
			_ = c.Error(err) // 编排器内部错误 → 统一 500；不直发
			return
		}
		switch decision {
		case "passthrough": // gate 关闭：落回裸发布（默认语义，行为与现状一致）
		case "approval_pending":
			c.JSON(http.StatusAccepted, gin.H{"status": "sentinel_pending", "run_id": runID, "message": message})
			return
		case "blocked", "refused_not_wired":
			c.JSON(http.StatusConflict, gin.H{"error": message})
			return
		default:
			_ = c.Error(fmt.Errorf("unknown publish gate decision %q", decision))
			return
		}
	}
	if err := h.svc.Publish(c.Request.Context(), groupKey, versionID, actor); err != nil {
		h.renderVersionError(c, err)
		return
	}
	values, err := h.svc.PlatformValues(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, values)
}
```

Run: `go build ./...`
Expected: PASS（`fmt` 若未 import 由编译器指出后补）。

- [ ] **Step A3：路由级宿主租户测试**

Create `api/http/router_test.go`（若已有同包测试文件则并入）：用 gin test 模式 + 伪造 JWT gin keys 验证 Publish 非 default 租户被 RequireDefaultTenant 403。先确认测试文件是否存在：

Run: `ls api/http/*_test.go`
按结果创建/并入 `TestParameterWriteRoutesRequireHostTenant`：构造 `adminGroup` 链，注入 `auth.tenant_id="tenant-not-default"`，请求 `POST /admin/parameters/versions/evaluation/1/publish`，断言 403 且未达 handler（用 stub service 或仅断言中间件短路——router_test 通常只测中间件组合，handler 依赖 `Container.Parameters` 为 nil 时 registerParameterWriteRoutes 早退，故直接以真实 `middleware.RequireDefaultTenant` 单测为主）。

若 router_test 不便构造 Container，则降级为 middleware 直测（Step A4）并在此记录「路由挂载由 A1 代码 + code review 守护」。

- [ ] **Step A4：RequireDefaultTenant 行为直测（防回归）**

Run: `go test ./api/middleware/ -run TestRequireDefaultTenant -v`
若该测试已存在：Expected PASS（既有覆盖 non-default→403）；若不存在：补用例 `auth.tenant_id` 非 default → 403、default → next。此为 R29 fail-closed 的守护点。

- [ ] **Step A5：Commit（子段 A）**

```bash
git add api/http/router.go api/http/handler/parameter_handler.go api/http/router_test.go api/middleware/
git commit -m "feat(evaluation): 卡C L3+ 宿主租户加固——参数 /admin Publish/Rollback 挂 InjectTenantContext+RequireDefaultTenant（R29/O2）

Co-Authored-By: Claude <noreply@anthropic.com>"
```

Run: `go test ./api/http/ ./api/middleware/ -run 'Publish|HostTenant|RequireDefaultTenant' -short`
Expected: PASS

### Sub-commit B：publishGateCoordinator + RunSentinelForDraft + DecideSentinel

- [ ] **Step B1：共享字面量常量（跨任务常量单一归属修正）**

Create `internal/evaluation/domain/publish_gate_const.go`：

```go
package domain

// 平台门禁分层/动作字面量（spec §3.4 计数、R31 精确值；跨包共享单一归属）。
// 跨任务常量单一归属（compile order 强制）：`l3_platform` 与 `sentinel_passed` 由
// Task 4 定义于 pkg/constants/evaluation.go（GateLayerL3Platform / PlatformEvalStateSentinelPassed），
// 本文件不重复定义；编排代码引用 constants.GateLayerL3Platform / constants.PlatformEvalStateSentinelPassed。
// 本文件只保留 Task 5 独有常量（l2/l3_sentinel/l3_multitenant_verify/全部 action/sentinel_failed）。
// 组常量唯一 home = internal/evaluation/domain/snapshot.go GroupMemory（Sub-commit A0 定义，
// 与 parameters 域同值；本文件不再定义组字面量）。
const (
	LayerL2                  = "l2"
	LayerL3Sentinel          = "l3_sentinel"
	LayerL3MultiTenantVerify = "l3_multitenant_verify"

	ActionRegression     = "regression"
	ActionBlock          = "block"
	ActionPass           = "pass"
	ActionPublishGated   = "publish_gated"
	ActionPublishBlocked = "publish_blocked"
	ActionQueued         = "queued"
	ActionRecovered      = "recovered"
	ActionNotRecovered   = "not_recovered"
)

// 平台版本 eval_state（spec §4.1.1 值域子集；parameters 侧 UPDATE 消费）。Task 5 只写
// sentinel_failed（本文件）/ sentinel_passed（constants.PlatformEvalStateSentinelPassed）。
const (
	EvalStateSentinelFailed = "sentinel_failed"
)

```

Run: `go build ./internal/evaluation/...`
Expected: PASS

- [ ] **Step B2：编排器核心**

Create `internal/evaluation/application/publish_gate.go`（关键判定方法；复杂度受门禁约束，判定拆纯函数）：

```go
package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

// 类型 PublishGateDeps / SentinelTarget / SentinelDecision / PublishGateRequest /
// PublishGateDecision / PublishGateResult / NewPublishGateCoordinator 见 Interfaces 节，
// 此处为关键方法实现。


// gateEnabled 判总开关：关闭（默认）→ 直通，不改动任何现状。
func (c *PublishGateCoordinator) gateEnabled(ctx context.Context) bool {
	return c.deps.GateEnabled != nil && c.deps.GateEnabled(ctx)
}

// GatePublish 编排入口。任何无法证明「走哨兵且通过」的路径一律拒发（fail-closed），
// 绝不静默直发（O5/§3.4）。
func (c *PublishGateCoordinator) GatePublish(ctx context.Context, hostTenantID string, req PublishGateRequest) (PublishGateResult, error) {
	if !c.gateEnabled(ctx) {
		return PublishGateResult{Decision: DecisionPassThrough}, nil
	}
	seq, status, _, ok, err := c.deps.ResolveVersion(ctx, req.GroupKey, req.VersionID)
	if err != nil {
		// infra/查询错误 → 编排层透传 error → handler 统一 500；不直发（fail-closed）。
		return PublishGateResult{Decision: DecisionBlocked, Message: "解析版本失败：" + err.Error()}, err
	}
	if !ok {
		return PublishGateResult{Decision: DecisionBlocked, Message: fmt.Sprintf("版本不存在：version_id=%d", req.VersionID)}, nil
	}
	if status != "draft" {
		return PublishGateResult{Decision: DecisionBlocked, Message: fmt.Sprintf("仅 draft 可发布哨兵，当前 status=%s", status)}, nil
	}
	if c.deps.SentinelSpec == nil {
		// T13 接线哨兵 suite 源前，enabled=true 一律 fail-closed 拒发（无静默直发、无假通过）。
		return PublishGateResult{Decision: DecisionRefusedNotWired,
			Message: "发布哨兵 suite 解析源未接线（SentinelSpec nil）：gate.enabled=true 下 P2 拒绝发布，待 T13 接入哨兵 suite + 人工审批环"}, nil
	}
	spec, err := c.deps.SentinelSpec(ctx, hostTenantID, req.GroupKey, seq)
	if err != nil {
		return PublishGateResult{Decision: DecisionBlocked, Message: "哨兵目标解析失败：" + err.Error()}, nil
	}
	runID, err := c.RunSentinelForDraft(ctx, hostTenantID, req, seq, spec)
	if err != nil {
		return PublishGateResult{Decision: DecisionBlocked, Message: "哨兵 run 入队失败：" + err.Error()}, nil
	}
	// O5 阻断式：哨兵 run 为异步执行；run 完成 → T13 完成「DecideSentinel → 人工审批 →
	// store.Publish」。P2 返回待审批/待接线态，不直发。
	return PublishGateResult{Decision: DecisionApprovalPending, RunID: runID,
		Message: "哨兵 run 已入队，待完成判定与人工审批（T13 完成发布环）"}, nil
}

// RunSentinelForDraft 对草案 seq 入队哨兵 run（CaptureInput.PlatformSeqOverrides，E6）。
func (c *PublishGateCoordinator) RunSentinelForDraft(ctx context.Context, hostTenantID string, req PublishGateRequest, draftSeq int, spec SentinelTarget) (string, error) {
	if c.deps.EnqueueSentinel == nil {
		return "", errors.New("EnqueueSentinel 未注入")
	}
	return c.deps.EnqueueSentinel(ctx, hostTenantID, EnqueueRunInput{
		Resource:             spec.Resource,
		SuiteRevisionID:      spec.SuiteRevisionID,
		IdempotencyKey:       fmt.Sprintf("sentinel:%s:%d", req.GroupKey, req.VersionID),
		RequestedBy:          req.Actor,
		PlatformSeqOverrides: map[string]int64{req.GroupKey: int64(draftSeq)},
	})
}

// DecideSentinel 哨兵 run 完成后的判定：仅消费两次 run 与 Compare，无 IO（可单测）。
// sentinel nil = 哨兵 run 未完成/未找到 → block（fail-closed，无法证明安全）。
func (c *PublishGateCoordinator) DecideSentinel(ctx context.Context, hostTenantID, groupKey string, baseline, sentinel *domain.EvalRun) (SentinelDecision, error) {
	decision := SentinelDecision{Verdict: SentinelVerdictPass, Deltas: map[string]float64{}}
	if sentinel == nil {
		decision.Verdict = SentinelVerdictBlock
		c.emitGate(ctx, domain.LayerL3Sentinel, domain.ActionBlock)
		c.emitGate(ctx, domain.LayerL2, domain.ActionRegression)
		return decision, nil
	}
	decision.ConfirmedSeq = sentinelSeq(groupKey, sentinel)
	if baseline != nil {
		decision.BaselineSeq = sentinelSeq(groupKey, baseline)
		if c.deps.Compare == nil {
			return decision, errors.New("Compare 未注入：无法完成哨兵回归判定（fail-closed，不直发）")
		}
		comparison, err := c.deps.Compare(baseline, sentinel)
		if err != nil {
			return decision, fmt.Errorf("哨兵回归对照失败: %w", err) // 编排层透传 → handler 500，不直发
		}
		decision.Deltas = comparison.DimensionDeltas
		if comparison.Regressed {
			decision.Verdict = SentinelVerdictBlock
			c.emitGate(ctx, domain.LayerL3Sentinel, domain.ActionBlock)
			c.emitGate(ctx, domain.LayerL2, domain.ActionRegression)
			_ = c.updateEvalState(ctx, groupKey, decision.ConfirmedSeq, domain.EvalStateSentinelFailed, hostTenantID)
			return decision, nil
		}
	}
	// pass：记录通过 + 门计数 + eval_state=sentinel_passed（Task 4 executePlatformPublishGated 依赖此前置）。
	decision.Verdict = SentinelVerdictPass
	c.emitGate(ctx, domain.LayerL3Sentinel, domain.ActionPass)
	c.emitGate(ctx, constants.GateLayerL3Platform, domain.ActionPublishGated)
	_ = c.updateEvalState(ctx, groupKey, decision.ConfirmedSeq, constants.PlatformEvalStateSentinelPassed, hostTenantID)
	return decision, nil
}
```

> 说明：eval run 仅在完成时落库（`SaveRun` 于 run 完成路径调用），故 `GetRun` 返回非 nil 即视为 completed 哨兵 run；`sentinel == nil` 视为未完成 → block。`sentinelSeq(groupKey, run)` 从 `run.ContextSnapshot` 取组 seq（helper 写于本文件；`ContextSnapshot.Evaluation.GroupKey` 与 `Execution[]` 各组的 GroupKey+VersionSeq 匹配 groupKey，nil 快照回退 0）。`emitGate`/`updateEvalState` 为私有 helper（`emitGate` 走 `deps.Metrics.IncEvalGateAction` nil-safe）。`Compare` nil 时 `DecideSentinel` 返回错误（fail-closed，不直发）。

- [ ] **Step B3：判定单测（表驱动，覆盖 pass/block/无基线/哨兵失败/memory 组与全组同路径/关闭直通）**

Create `internal/evaluation/application/publish_gate_test.go`：stub `Compare`/`SentinelSpec`/`ResolveVersion`/`UpdateEvalState`/`EnqueueSentinel`/`Metrics`（用 `observability.NoopMetrics` 或包内 metrics 桩）。用例名描述行为：

```go
func TestPublishGateCoordinator_GateDisabled_PassThrough(t *testing.T)
func TestPublishGateCoordinator_MemoryGroup_UniformRefusedNotWired(t *testing.T) // R30 memory 组不再特判：enabled=true & SentinelSpec nil → RefusedNotWired，与全组一致
func TestPublishGateCoordinator_SentinelBlock_RefusesAndWritesFailed(t *testing.T) // Compare.Regressed=true → 断言 UpdateEvalState 收到 sentinel_failed + l3_sentinel block + l2 regression
func TestPublishGateCoordinator_SentinelPass_WritesPassed(t *testing.T)            // Regressed=false → sentinel_passed + l3_sentinel pass + l3_platform publish_gated
func TestPublishGateCoordinator_SentinelRunFailed_Blocks(t *testing.T)             // sentinel 非 completed → block，不 Compare
func TestPublishGateCoordinator_NoBaseline_Passes(t *testing.T)                    // baseline nil → pass（无回归信号）
func TestPublishGateCoordinator_SentinelSpecNil_RefusedNotWired(t *testing.T)      // gate.enabled=true & SentinelSpec nil → 拒发（不直发、不 Enqueue）
```

Run: `go test ./internal/evaluation/application/ -run TestPublishGateCoordinator -v`
Expected: 全 PASS；block 用例断言 `store.Publish` 从未被调用（编排器无 publish 依赖，编译层保证）。

- [ ] **Step B4：wiring 依赖组装 + handler 注入 + 跨任务 Compare 适配器**

Create `api/wiring/publish_gate.go`：

```go
package wiring

import (
	"context"

	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

// runCompareAdapter 把 Task 1 纯函数（*RunComparison，永不为 nil）适配为 deps 的
// (RunComparison, error) 签名。B4（哨兵）与 C5（验证 worker）复用，见「任务依赖与次序」跨任务绑定 A。
func runCompareAdapter(baseline, current *evaldomain.EvalRun) (evaldomain.RunComparison, error) {
	return *evalapp.CompareRunRegression(baseline, current), nil
}
```

`func (c *Container) newPublishGateCoordinator(ctx) handler.PublishGateFunc` 组装真实 deps（`GateEnabled` 读平台键 `evaluation.gate.enabled`，仿 `observationEnabled`；`ResolveVersion` 用 `c.Parameters.Service.Versions` 匹配 ID；`UpdateEvalState` 直连 parameters Service；`Compare` 绑 `runCompareAdapter`；`SentinelSpec`/`EnqueueSentinel`/`BaselineRun` 在 P2 绑 nil/占位——enabled=true 由 B2 拒发兜底），并在 `registerParameterWriteRoutes`（router.go）构造 handler 后 `paramHandler.SetPublishGate(...)`。router.go 组装处需要 gate func：Container 增字段 `PublishGate handler.PublishGateFunc`，wiring build 时经 `newPublishGateCoordinator` 赋值（参数 handler 构造在 router.go，经 container 取 `c.PublishGate`）。适配器把 `PublishGateResult.Decision` 整数决策翻译为 handler seam 字符串集合并封进 `(decision, message, runID string, err error)`：`DecisionPassThrough→"passthrough"`、`DecisionApprovalPending→"approval_pending"`（RunID 一并带出）、`DecisionBlocked→"blocked"`、`DecisionRefusedNotWired→"refused_not_wired"`；`err!=nil` 时返回原始 error（handler 走 500），否则 `err=nil`（handler 按 decision 渲染 409/202）。

Run: `go build ./... && go vet ./api/wiring/`
Expected: PASS（wiring→handler 方向若已有依赖环，将 `PublishGate` 的 func 类型改为 wiring 本地定义、router 再适配，见既有 `dlqReplayAdapter` 同款 fallback）。

- [ ] **Step B5：Commit（子段 B）**

```bash
git add internal/evaluation/domain/publish_gate_const.go internal/evaluation/application/publish_gate.go internal/evaluation/application/publish_gate_test.go api/wiring/publish_gate.go api/http/router.go
git commit -m "feat(evaluation): 卡C L3+ 发布哨兵协调器 publishGateCoordinator + RunSentinelForDraft（memory 组与全平台组同路径 / R31 计数）

Co-Authored-By: Claude <noreply@anthropic.com>"
```

Run: `go test ./internal/evaluation/application/ ./api/wiring/ -run 'PublishGate|Sentinel' -short`
Expected: PASS

### Sub-commit C：多租户验证 worker + 两条分化/未恢复告警

- [ ] **Step C1：run 平台 seq 锚查询 + port 扩展 + mock 同步**

Modify `internal/evaluation/domain/port/evaluation.go` `RunRepository` 增方法；实现于 `run_repository.go`（eval_runs.context_snapshot JSONB，`group_key`/`version_seq` 判定；无则 `nil,nil`；**沿用 Task 1 的 `postgres.WithTenant` + `execTenantTx` + `decodeContextSnapshot` 模式**）：

```go
FindLatestCompletedRunForPlatformSeq(ctx context.Context, tenantID, groupKey string, seq int64) (*domain.EvalRun, error)
```

Run: `go build ./...`
随后 grep 全部 `RunRepository` stub/mock：
Run: `grep -rln "SaveRun(ctx" internal/ api/ | head -30`
逐文件补该方法（mock 返回 nil,nil 或按测试注入）。预期新增编译错误清零。

- [ ] **Step C2：验证 payload 实体（domain）+ 幂等键 + enqueue**

Modify `internal/evaluation/domain/evaluation.go`：`JobTypeEvalRun` 旁加 `const JobTypePlatformVerify = "platform_verify"` + `PlatformVerifyPayload` + `PlatformVerifyJob`（定义见 Interfaces 节，domain 单一 home；`evaluation_jobs.job_type` 无 CHECK，零 DDL）。

Create `internal/evaluation/application/multitenant_verify.go`：

```go
package application

import (
	"context"
	"fmt"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

// job_type 常量 domain.JobTypePlatformVerify + payload 类型 domain.PlatformVerifyPayload
// 定义于 domain/evaluation.go（JobTypeEvalRun 旁），本文件只消费不重定义。

func VerifyIdempotencyKey(groupKey string, fromSeq, toSeq int64) string {
	return fmt.Sprintf("platform_verify:%s:%d:%d", groupKey, fromSeq, toSeq)
}

// EnqueueMultiTenantVerify 平台回滚成功后调用（调用点延迟 T13，见「开放问题 #2」；
// Task 4 executePlatformRollback 成功路径由 T13 接线）。幂等键冲突（已存在）→
// inserted=false → 去重不重复 +queued。
func EnqueueMultiTenantVerify(ctx context.Context, repo port.JobPlatformVerifyRepo, tenantID string, p domain.PlatformVerifyPayload, createdBy string, metrics observability.MetricsProvider) error {
	inserted, err := repo.EnqueuePlatformVerify(ctx, tenantID, p, VerifyIdempotencyKey(p.GroupKey, p.FromSeq, p.ToSeq), createdBy)
	if err != nil {
		return fmt.Errorf("enqueue platform verify: %w", err)
	}
	if inserted && metrics != nil {
		metrics.IncEvalGateAction(domain.LayerL3MultiTenantVerify, domain.ActionQueued)
	}
	return nil
}
```

Run: `go build ./internal/evaluation/...`
Expected: PASS

- [ ] **Step C3：repo 两方法 + 常量**

Modify `internal/evaluation/infrastructure/persistence/job_repository.go` 增 `EnqueuePlatformVerify`（`INSERT ... ON CONFLICT (idempotency_key) DO NOTHING`，执行后取 `cmd.RowsAffected()>0` 作为 `inserted` 返回；job_type 列写 `domain.JobTypePlatformVerify`；payload JSONB 由 `json.Marshal` 后以 string 传，pgx v5 规则）与 `ClaimPlatformVerify`（`WHERE job_type=$1 ...`，参数 `domain.JobTypePlatformVerify`，条件 `(status='queued' OR (status='running' AND lease_until<NOW())) ... FOR UPDATE SKIP LOCKED LIMIT 1`，行反序列化为 `domain.PlatformVerifyJob`（原始 payload JSONB `json.Unmarshal` 到 `domain.PlatformVerifyPayload` 填入 `Payload` 字段））。两方法均为 tenant-scoped 访问，**必须经 `execTenant`（与既有 `Enqueue`/`Claim` 同纪律）**；port 定义 `JobPlatformVerifyRepo`（`EnqueuePlatformVerify` 返回 `(bool, error)`）放 `internal/evaluation/domain/port/evaluation.go`（见 Interfaces 节）。先 `go build ./...` 确认 domain 常量/类型引用无编译错。

`pkg/constants/evaluation.go` 追加：

```go
// 多租户验证（spec §3.4-3）：回滚后验证窗口与未恢复判定门槛。
const (
	PlatformVerifyWindowMinutes   = 30 // 验证对比取 run 的窗口（分钟）
	PlatformVerifyNotRecoveredMin = 1  // ≥1 租户未恢复即触发 StratumEvalMultiTenantVerifyNotRecovered
)
```

Run: `go build ./... && go test ./internal/evaluation/infrastructure/persistence/ -run TestPgJobRepository -short`
Expected: PASS（若该包测试依赖真实 DB，按仓库惯例改跑 `-short` 跳过项或本步骤在 CI 数据库 profile 验证；本地先用 mock 单测覆盖 repo 语义——沿用 `job_repository_mock_test.go` 模式）。

- [ ] **Step C4：验证 runner（per-tenant fail-open）**

`multitenant_verify.go` 增 runner：

```go
// MultiTenantVerifyRunner 实现 TenantJobRunner：每租户 Claim 一条 platform_verify →
// 用 FindLatestCompletedRunForPlatformSeq 取 from/to 锚定 run → Compare(坏, 好) →
// 好不劣于坏 = recovered，否则 not_recovered（R31 计数）。run 缺失 = 无信号 → 跳过不发计数。
func (r *MultiTenantVerifyRunner) RunOnce(ctx context.Context, tenantID, workerID string, lease time.Duration) (bool, error) {
	job, err := r.deps.Repo.ClaimPlatformVerify(ctx, tenantID, workerID, lease)
	if err != nil {
		r.deps.Metrics.IncEvaluationJob("platform_verify_error") // 既有 eval 工作计数维度，不加新 family
		return false, err
	}
	if job == nil {
		return false, nil
	}
	fromRun, err := r.deps.Runs.FindLatestCompletedRunForPlatformSeq(ctx, tenantID, job.Payload.GroupKey, job.Payload.FromSeq)
	if err != nil {
		return true, fmt.Errorf("verify from-seq lookup: %w", err) // 单租户失败：job 留 running 可重试（fail-open 于整体）
	}
	toRun, err := r.deps.Runs.FindLatestCompletedRunForPlatformSeq(ctx, tenantID, job.Payload.GroupKey, job.Payload.ToSeq)
	if err != nil {
		return true, fmt.Errorf("verify to-seq lookup: %w", err)
	}
	if fromRun == nil || toRun == nil {
		return true, nil // 无信号租户：跳过（不产生 recovered/not_recovered 计数）
	}
	cmp, err := r.deps.Compare(fromRun, toRun)
	if err != nil {
		return true, fmt.Errorf("verify compare: %w", err)
	}
	if cmp.Regressed {
		r.deps.Metrics.IncEvalGateAction(domain.LayerL3MultiTenantVerify, domain.ActionNotRecovered)
	} else {
		r.deps.Metrics.IncEvalGateAction(domain.LayerL3MultiTenantVerify, domain.ActionRecovered)
	}
	return true, nil
}
```

> 语义说明：`Compare(坏版本 run, 好版本 run)` 的 Regressed=true 表示「回滚后（好）仍劣于回滚前（坏）」= 未恢复；Regressed=false = 已恢复/改善。这一定义与哨兵判定方向相反（哨兵是草案 vs 基线），注释已写明防混用。

- [ ] **Step C5：worker 装配（bounded，复用 NewWorker；Compare 绑 runCompareAdapter）**

Modify `api/wiring/evaluation.go`：`newEvaluationWorker` 之后增第二个 worker（同一 `evaluationTenantLister{pool: db}` + 验证 runner，独立 interval `constants.EvaluationIdleInterval`），注册 `Start`/`Stop`；`MultiTenantVerifyRunner` 的 `Compare` 绑 `runCompareAdapter`（见 B4，同一文件 api/wiring/publish_gate.go）、`Repo` 用 `evalpersist.NewPgJobRepository(db)` 的窄接口（`port.JobPlatformVerifyRepo`）、`Runs` 用 run repo。

Run: `go build ./... && go vet ./api/wiring/`
Expected: PASS

- [ ] **Step C6：worker 单测**

Create `internal/evaluation/application/multitenant_verify_test.go`：stub `JobPlatformVerifyRepo`/`RunRepository`/`Compare`；用例：`RecoveredWhenRollbackRestores`、`NotRecoveredWhenStillRegressed`、`SkipWhenNoSignal`、`EnqueueDedupesByIdempotencyKey`（二次 enqueue 冲突 → 不 +queued）。

Run: `go test ./internal/evaluation/application/ -run 'MultiTenantVerify|PlatformVerify' -v`
Expected: PASS

- [ ] **Step C7：告警规则（remote 单一源）+ render + runbook**

**C7a.** 规则单一源 `monitoring/remote/rules/stratum-evaluation.yaml`（追加两条，格式对齐既有 summary/description/`>-`/runbook_url；**environment: remote-test**）：

```yaml
      - alert: StratumEvalMultiTenantVerifyNotRecovered
        expr: |
          increase(eval_gate_action_total{layer="l3_multitenant_verify",action="not_recovered"}[30m]) >= 1
        for: 5m
        labels:
          severity: critical
          service: evaluation
          component: l3-plus-platform
          environment: remote-test
        annotations:
          summary: 平台回滚后多租户验证存在未恢复租户（当前 {{ $value }}）
          description: >-
            平台版本回滚后 multi-tenant verify 判定 ≥ 1 个租户 not_recovered（回滚后
            该租户表现仍劣于回滚前），持续 5 分钟。平台参数影响全租户，属 T4 红线级；
            按 runbook 立即人工确认并处置。
          dashboard_url: https://stratum.grafana/d/evaluation
          runbook_url: /docs/operations/alerts/stratum-evaluation.md#stratum-eval-multitenant-verify-not-recovered
      - alert: StratumEvalPlatformMultiTenantDivergence
        expr: |
          increase(eval_gate_action_total{layer="l3_multitenant_verify",action="not_recovered"}[30m]) > 0
            AND increase(eval_gate_action_total{layer="l3_multitenant_verify",action="recovered"}[30m]) > 0
        for: 10m
        labels:
          severity: warning
          service: evaluation
          component: l3-plus-platform
          environment: remote-test
        annotations:
          summary: 平台版本多租户验证出现分化（多数恢复 / 少数未恢复）
          description: >-
            同一平台版本窗口内 recovered 与 not_recovered 并存（多数改善少数劣化分布效应，
            spec §3.4-4 / §9.4.2）。仅信号告警不做自动处置；劣化租户名单供人工在归因视图下钻。
          dashboard_url: https://stratum.grafana/d/evaluation
          runbook_url: /docs/operations/alerts/stratum-evaluation.md#stratum-eval-platform-multitenant-divergence
```

> 规则唯一事实源 = `monitoring/remote/rules/*.yaml`；本地 `.yml` 与远端 CRD 由下述 render 生成，禁止手编。

**C7b.** 重渲两个 commit 产物（同 commit 防漂移）：

```bash
bash scripts/quality/render-monitoring-rules.sh remote-test
bash scripts/quality/render-monitoring-rules.sh local
```

Expected: 更新 `monitoring/remote/generated/stratum-prometheus-rules.yaml`（CRD，environment: remote-test）与 `monitoring/local/rules/stratum-evaluation.yml`（environment: production）。规则文本与 remote 源一致。

**C7c.** `docs/operations/alerts/stratum-evaluation.md` 文件尾追加（anchor 与规则 `runbook_url` fragment 一致，且满足 `scripts/quality/monitoring-runbook-test.go` 的 `<a id="{fragment}"></a>\n\n## {AlertName}\n` 格式）：

```markdown
<a id="stratum-eval-multitenant-verify-not-recovered"></a>

## StratumEvalMultiTenantVerifyNotRecovered

平台版本回滚后，multi-tenant verify 判定存在未恢复租户（critical，T4 红线级）。

- 定位：查询 `eval_gate_action_total{layer="l3_multitenant_verify",action="not_recovered"}` 按租户维度下钻；
  确认回滚动作（group_key/from_seq/to_seq）与受影响租户。
- 确认：到该租户核对回滚目标 seq 下的 run 表现（`FindLatestCompletedRunForPlatformSeq` 锚定 to_seq）；
  not_recovered = 回滚后（好版本）run 仍劣于回滚前（坏版本）run（run 级回归 Regressed=true）。
- 处置：平台参数影响全租户，恢复不达标需人工介入——复核回滚目标是否为真「上一好版本」，必要时继续回滚到更早
  版本或调整配置；处置动作走参数操作台与 CD，禁止远端手改。恢复后 not_recovered 计数停止增长即自动消除。

<a id="stratum-eval-platform-multitenant-divergence"></a>

## StratumEvalPlatformMultiTenantDivergence

平台版本多租户验证分化（多数恢复 / 少数未恢复，warning，仅信号不自动处置）。

- 语义：同一验证窗口内 recovered 与 not_recovered 并存 = 分布效应（多数改善、少数劣化），
  可能源于租户规模/tier/流量差异（防辛普森悖论需分层归因）。
- 定位：把 not_recovered 的租户名单按 tier/行业/流量规模分层下钻，找出劣化集中段。
- 处置：仅告警，不自动回滚；人工在归因视图确认劣化是否真实能力退化，走参数调整/定向回滚流程。
```

- [ ] **Step C8：监控质量闸 + 全量验证**

Run: `go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .`（两参，与 Task 1/2 同形式；或仓库对应 make 目标 `make monitoring-config-test`）
Expected: PASS（每条新规则恰 1 个可解析 runbook_url；anchor 匹配 runbook section）

```bash
bash scripts/quality/render-monitoring-rules.sh remote-test --check
bash scripts/quality/render-monitoring-rules.sh local --check
go test -short ./internal/evaluation/... ./api/wiring/ ./api/middleware/
```

Expected: 全绿、无渲染漂移。

- [ ] **Step C9：Commit（子段 C）**

```bash
git add internal/evaluation/domain/evaluation.go internal/evaluation/domain/port/evaluation.go internal/evaluation/infrastructure/persistence/job_repository.go internal/evaluation/infrastructure/persistence/run_repository.go internal/evaluation/application/multitenant_verify.go internal/evaluation/application/multitenant_verify_test.go api/wiring/evaluation.go api/wiring/publish_gate.go pkg/constants/evaluation.go monitoring/remote/rules/stratum-evaluation.yaml monitoring/remote/generated/stratum-prometheus-rules.yaml monitoring/local/rules/stratum-evaluation.yml docs/operations/alerts/stratum-evaluation.md
git commit -m "feat(evaluation): 卡C L3+ 多租户验证 worker + 分化告警（R31/E9 计数，StratumEvalMultiTenantVerifyNotRecovered critical / StratumEvalPlatformMultiTenantDivergence warning）

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

**主会话裁决（原「需协调者定夺」；协调者已定稿；未决项见末尾「开放问题」）**

1. **R30 memory 组偏差（用户 09-03 已复核并决定，处置完结）**：spec O1「合一、全平台组哨兵」与代码现实（capture 仅 evaluation/agent/trace，eval 域快照无 `GroupMemory`）冲突，原按 fail-closed 拒发（`ErrMemoryGroupSentinelUnsupported`）处置。→ **用户 09-03 复核后定案：P2 扩范围，无条件纳入 memory capture**（Execution 恒 `[agent, trace, memory]`，落地见 Sub-commit A0）；据此删除拒发全部产物（`GroupMemoryPlatform`/`DecisionRefusedMemory`/`ErrMemoryGroupSentinelUnsupported`/memory guard/`refused_memory` 映射），memory 组与 agent/evaluation/trace 同路径。原「开放问题 #1」已归档为已解决，无待决项。
2. **跨任务常量单一归属**：→ **master 定稿（compile order 强制，见「任务依赖与次序」跨任务绑定 B）**：`l3_platform`/`auto_refused`/`sentinel_passed` Go 常量单一 home = `pkg/constants/evaluation.go`（Task 4 Step 4 定义）；本 Task B1 已**删除** `domain/publish_gate_const.go` 中 `LayerL3Platform` 与 `EvalStateSentinelPassed` 重复定义，B2 引用 `constants.GateLayerL3Platform`/`constants.PlatformEvalStateSentinelPassed` 并加 `pkg/constants` import。此点**与裁决 A7.2「T12 拥有常量」的偏差为 compile order 强制**（见「开放问题 #3」）。
3. **Task 1 regression_compare 导出名绑定**：→ **master 定稿（跨任务绑定 A）**：导出名 = `CompareRunRegression`（`func(baseline, current *domain.EvalRun) *domain.RunComparison`）；wiring 用 `runCompareAdapter` 包成 deps 的 `(RunComparison, error)` 签名，B4/C5 复用。
4. **编排器装配位置 vs handler 环依赖**：→ **master 定稿**：`SetPublishGate` seam + wiring 把编排器适配成 handler 的 func 类型；若 `api/wiring → api/http/handler` 已存在依赖环，wiring 本地定义 func 类型、router.go 再适配（`dlqReplayAdapter` 同款 fallback）。
5. **Task 4 跨任务 enqueue 调用点**：spec §3.4-3「每次平台回滚后自动入队」调用点本应在 Task 4 `executePlatformRollback` 成功路径，但 compile order（Task 4 先于 Task 5）使 Task 4 无法引用 Task 5 交付的 `EnqueueMultiTenantVerify`，且生产 wiring 中平台回滚仅经 T13 审批流可达。→ **master 定稿：调用点延迟 T13**（本 Task 交付 enqueue 函数 + worker + 幂等去重 + 判定 + 计数供 T13 接线；发布成功后是否入队由 T13 决策环补，A7.5）。见「开放问题 #2」。
6. **gate.enabled=true 的 P2 通过路径**：哨兵 suite 源（SentinelSpec）真实接线属 T13；P2 enabled=true 时所有平台组返回 `refused_not_wired`/`approval_pending` 且无完成环。→ **master 风险节 + 开放问题显著标注（A7.6）**：P2 合入后**不得开启 `evaluation.gate.enabled`**（默认 false 不受影响），到 T13 全链路 wiring 后才可开。
7. **`DecideSentinel` 的 completed 判定（已核实消解，非开放项）**：实景 `EvalRun` **无 `Status` 字段**；run 仅在完成时落库 → `GetRun`/`FindLatestCompletedRunForPlatformSeq` 返回非 nil 即视为 completed 哨兵 run；`sentinel == nil`（未完成/未找到）→ block（fail-closed）。已按此内嵌进 `DecideSentinel` 步骤与注释，不再依赖 `EvalRun.Status`。

---

**验证命令**

```bash
# 子段 A：宿主租户加固
go build ./...
go test ./api/http/ ./api/middleware/ -run 'Publish|HostTenant|RequireDefaultTenant' -short

# 子段 B：哨兵协调器
go test ./internal/evaluation/application/ ./api/wiring/ -run 'PublishGate|Sentinel' -v -short

# 子段 C：多租户验证 worker + repo 查询
go test ./internal/evaluation/application/ -run 'MultiTenantVerify|PlatformVerify' -v -short
go test ./internal/evaluation/infrastructure/persistence/ -short
go run scripts/quality/monitoring-runbook-test.go monitoring/remote/rules .
bash scripts/quality/render-monitoring-rules.sh remote-test --check
bash scripts/quality/render-monitoring-rules.sh local --check

# PR 前全量（沿用仓库门槛）
go vet ./... && go test -short ./...
make code-quality && make risk-guardrails
```

Expected：全绿；`make code-quality` 无新超标函数（编排器/runner 拆纯函数保 CC ≤10/认知 ≤15/行 ≤120/嵌套 ≤4）。

---

**Spec 覆盖：§3.4（四件事 1/3/4 的编排组件 + R29-R32）；有意跳过/留 T13：**

- §3.4-1 的「人工确认发布（建 ToolApproval → approve → store.Publish）」handler gated 完成环 → T13（本 section 交付编排器 + 拒发 + 待审批态 + 判定）。
- §3.4-2 运行时判异强制人工（rollback_manual 审批流）→ Task 4/T13（`GateApprovalRequester` 生产 wiring）。
- §3.4-3 回滚后自动入队的**调用点** → T13（本 section 交付 enqueue 函数 + worker + 判定 + 计数；见「开放问题 #2」）。
- §3.4-4 分化检测的**租户分层聚合分析/归因视图**（防辛普森完整分层）→ T13+（P2 禁新 metric family + 无 DB 投影，分化告警由 recovered/not_recovered 并存信号近似）。
- `GateStore` DB 台账、`buildGateService`、门禁台 UI/proto → T13（dict A）。memory capture 已随 R30 决定（用户 09-03）纳入 P2 的 Sub-commit A0，不再归 T13。

---

## Spec 覆盖自检（writing-plans）

| spec 节 | 实现任务 | 备注 |
|---|---|---|
| §3.1（O4：L1 检测/执行分离）+ StratumEvalRuleDisabled | Task 2 | R20-R23；evaluation 侧零改动（R21/E8） |
| §3.2-①② run 级回归纯函数 + 两条 L2 告警规则/runbook | Task 1 | R18；emit 点归 Task 5/T13，rule 先落 |
| §3.2-③ 评审池 behavior 分支（enum/Valid/RiskLevel/TriggersForObservation） | Task 1 | R19；`reviewRiskOrderSQL` 两端镜像 |
| §3.3 L3 资源回滚三路径 planner/executor/port/wiring | Task 3 | R24/R25；mcp→ErrRollbackUnsupported |
| §3.4 无自动不变量 + 平台三操作审批执行 + eval_state 读路径 | Task 4 | R26-R28 + E5 读路径 |
| §3.4 L3+ 发布哨兵编排 + memory 组入快照 capture + 多租户验证 worker | Task 5 | R29-R32（R30 已决：用户 09-03，无条件并入 Sub-commit A0）；enqueue 调用点 T13 |
| §4.1.1 eval_state（只读接线，无 DDL） | Task 4 | migration 044 列已在 |
| §4.3.5 / §4.3.6（behavior 判异 + run 级回归 + E6 基线 run 查询） | Task 1 | `avg_score` 基准（A5.1）；不过滤 RevisionID（A5.2） |
| §4.4 平台三操作行（rollback_platform/rollback_resource/publish_platform_version） | Task 4 | 消费 Task 3 executor + Task 5 哨兵语义 |
| §5 T13/T14（台账 DB 投影、buildGateService 生产 wiring、门禁台 UI/proto） | 不在 P2 | 全部留 T13/T14，P2 仅 stub/占位/拒发（memory capture 不在此列——已随 R30 决定入 P2 Sub-commit A0） |

无占位符、无 TBD、无「see scratch / same as Task N」。跨任务共享符号全文一致：`CompareRunRegression`（Task 1 产出，Task 5 wiring 经 `runCompareAdapter` 消费）、`FindLatestCompletedRunForResource`（Task 1 定义、Task 5 消费）、`ResourceRollbackExecutor`/`ErrRollbackUnsupported`/`ErrAutoRollbackForbidden`（Task 3 定义、Task 4 消费）、`GateTarget.Kind`（Task 3/4 统一）、`snapshot.go` `GroupMemory`（Task 5 Sub-commit A0 单一定义，与 parameters 域同值）、`constants.GateLayerL3Platform`/`GateActionAutoRefused`/`PlatformEvalStateSentinelPassed`（Task 4 定义、Task 4/5 引用）、`monitoring-runbook-test.go` 一律两参、监控规则单一源 remote yaml + 双栈 render 产物。

## 主要风险与对策

1. **R30 memory 组偏差（已解决，用户 09-03 已复核，非开放项）**：原 fail-closed 拒发 = memory 组发布在 P2 被整体关停到 T13 的产品级关停，已随用户决定解除——**扩 P2 范围，无条件纳入 memory capture**（Sub-commit A0，见「开放问题 #1」归档）。memory 组与 agent/evaluation/trace 同路径（enabled=false 直通默认不变；enabled=true SentinelSpec nil → `refused_not_wired` 全组一致，A7.6 前提不破）；`publishGateCoordinator` 不再特判，无残留风险。
2. **P2 合入后不得开启 `evaluation.gate.enabled`（A7.6）**：gate.enabled=true 的 P2 通过路径 = `refused_not_wired`/`approval_pending` 且无完成环（SentinelSpec 未接线、人工审批环未建），任何平台组发布都会被拒或悬挂。**registry 默认 false 不受影响**；T13 全链路 wiring（哨兵 suite 源 + GateApprovalRequester 生产接线）完成前禁止开启。风险节显著标注，runbook 亦注明。
3. **Task 4 `publish_platform_version` 在 Task 5 合入前恒 fail-closed（预期安全中间态）**：master 顺序 T11→T10→T12；Task 4 合入后 `sentinel_passed` 无生产者写入，`executePlatformPublishGated` 恒 notExecuted。无副作用、无发布发生；Task 5 哨兵流落位后才放行（A6.6）。
4. **`pkg/constants` 常量单一归属 vs 裁决 A7.2（compile order 偏差）**：A7.2 原本裁给「T12 拥有 layer/action 常量」，但 Task 4（T10）先于 Task 5（T12）合入且 Task 4 已 emit `{l3_platform,auto_refused}` → 这三个值的 Go home 必须是 `pkg/constants`（Task 4 定义）。Task 5 只消费、不重复定义。实现者若发现其它共享字面量需跨 Task 引用，一律进 `pkg/constants/evaluation.go` 或既有 domain 常量，禁止散写。见「开放问题 #3」。
5. **Compare 消费签名（Task 5 deps `(RunComparison, error)`）vs Task 1 纯函数（`*RunComparison`）**：两边签名都保留，wiring `runCompareAdapter` deref 永不为 nil 的指针统一衔接；`Compare==nil` 时 `DecideSentinel`/`RunOnce` 返回错误 fail-closed。不会出现 nil deref。
6. **disabled 命中观测变 VerdictBlock（Task 2，R21 显式副作用）**：enabled=false 且 denylist 非空时命中工具 → 观测判 VerdictBlock、`eval_behavior_anomaly_total{signal="rule_block"}`/`eval_gate_action_total{layer="detect",action="block"}` 上涨。这是 O4 规范意图；告警判别不受污染（RuleBlocked 匹配 verdict=block、RuleDisabled 匹配 verdict=detected）。
7. **平台回滚后多租户验证入队调用点缺口（spec §3.4-3）**：P2 交付 enqueue 函数与 worker，但无调用者（compile order + 生产仅 T13 审批流可达）。P2 期间平台回滚审批在真实 wiring 中不可达，故无「回滚后未入队」的实际副作用；T13 必须接线，否则验证 worker 恒空转。见「开放问题 #2」。
8. **repo 扩展连锁编译**：Task 1/5 均扩展 `RunRepository` port → 每次 `go build ./...` 找齐 `fakeRunRepo`/pgxmock stub/mock 补方法（编译期强制，不会漏）；新 repo 方法一律 `execTenant` + `postgres.WithTenant`（tenant-scoped 纪律）。

## 开放问题（需用户/协调者复核；非静默决定）

1. **R30 memory 组偏差（用户 09-03 已决，归档为已解决）**：spec O1「发布哨兵对全部平台组一律生效」与代码现实冲突——快照 capture 原只覆盖 evaluation/agent/trace、eval 域快照常量无 `GroupMemory`。**用户 09-03 复核后决定：P2 扩范围，无条件纳入 memory capture**（Execution 恒 `[agent, trace, memory]`，落地见 Sub-commit A0：`snapshot.go` 加 `GroupMemory`；`evaluation_snapshot.go` Capture 捕获 memory 组并后置追加；测试 len 2→3）。据此删除原拒发方案全部产物（`GroupMemoryPlatform`/`DecisionRefusedMemory`/`ErrMemoryGroupSentinelUnsupported`/memory guard/`refused_memory` 映射），memory 组不再 fail-closed 拒发、不再归 T13；哨兵对 draft 的真实执行消费仍属 T13 完成环（所有组一致）。零 DDL 边界不破。
2. **平台回滚/发布成功后的多租户验证入队调用点（spec §3.4-3）**：`EnqueueMultiTenantVerify` 调用点应在 Task 4 `executePlatformRollback` 成功路径（回滚后）/ 发布 gate 通过后（T13 决策环）。compile order 使 Task 4 无法引用 Task 5 交付函数，且生产 wiring 中平台回滚仅经 T13 审批流可达。**master 裁决：调用点延迟到 T13**（P2 无调用者 = 安全中间态）。发布成功后是否也入队验证由 T13 决策环补（A7.5 倾向默认仅回滚后入队）。T13 若需真实 approval id 落 `GateActionRecord.ApprovalID`，亦在 ExecuteApprovedAction 透传请求字段。
3. **常量单一归属与裁决 A7.2 的偏差（compile order 强制）**：A7.2 原裁「layer/action Go 常量由 T12（唯一 emit 侧）拥有」，但 Task 4（T10）先于 Task 5 合入且需 emit `{l3_platform, auto_refused}`、消费 `sentinel_passed` → 这三个值 home = `pkg/constants/evaluation.go`（Task 4 Step 4）。Task 5 删除 domain 文件中的重复定义并引用 `constants.*`。**请用户知悉此对 A7.2 的有意修正**；若坚持 T12 拥有，则需把 Task 4 中三个常量的引用改为等值字符串 + 常量迁移到 Task 5（不推荐，破坏禁 magic string）。
4. **`evaluation.gate.enabled` 在 P2 的开启路径不完备（A7.6）**：P2 enabled=true 时所有平台组发布返回 `refused_not_wired`/`approval_pending` 且无完成环。**P2 合入后不得开启该开关**（默认 false 不受影响）；待 T13 全链路 wiring（哨兵 suite 源 + GateApprovalRequester + 人工审批环）完成后方可开启。此项已显著标注于风险节与本计划头部，非静默收编。

---
