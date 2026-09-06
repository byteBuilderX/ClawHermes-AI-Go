# 评测分层门禁落地设计：主规格 §8「分层门禁」L1/L2/L3/L3+ 全量落地

> 文件建议名：`2026-09-03-evaluation-layered-gates-design.md`
> 内部卡：#84「主规格 §8 分层门禁 L1/L2/L3/L3+ 集成」（卡 C）
> 类型：**落地设计 spec**（§8 四层与既有代码基座 reality 映射 + 精确改动清单 + 数据模型/接口草案；实现待后续 writing-plan 逐任务落地）
> 日期：2026-09-02（初稿）；**2026-09-03 修订版**（落主会话裁决 O1-O5，见 §7.2）
> 状态：**定稿（主会话已裁 O1-O5，待提交）**
> 前置文档：
>
> - `docs/superpowers/specs/2026-08-28-evaluation-metrics-design.md`（下称「主 spec」；§8 分层门禁为本次落地对象）
> - `docs/superpowers/specs/2026-08-30-evaluation-review-pool-design.md`（下称「评审池 spec」）
> - `docs/superpowers/specs/2026-09-02-evaluation-two-track-versioning-design.md`（下称「双轨 spec」）
> - `/tmp/card-c-recon.md`（代码基座与缺口盘点，2026-09-02；本 spec 的 file:line 均相对仓库根）

---

## 0. 结论先行

- 主规格 §8 的 4 行在代码现实里落地为 **4 个实现段**（L1/L2/L3-资源/L3+ 平台），因为代码存在**两条互不相通的版本链**：平台参数（public `platform_config_versions`，挪 label 回滚）与租户资源配置（`resource_revisions` / `resource_versions` / `skill_revisions` 各自的原生链）。§8 措辞「租户级走 `parameters.Rollback`」在代码中不存在对应物——`internal/parameters` 只管理 public 平台组。
- **门禁判定是控制逻辑**：全部判定过程 = 确定性常量/阈值/策略表 + 纯函数，**零 LLM**（judge 只产信号，不产门禁动作）。
- **动作永远通过既有版本机制 + 审计**：平台回滚 = `parameters.Rollback`（挪 label，同事务审计）；租户资源回滚 = 该资源原生回滚入口（agent/knowledge 产品版本、skill 原生链、experiment 清 canary）；评测自身绝不直接写生产配置。
- **平台参数无任何自动回滚路径**（O1 合一后仍成立），代码层以类型化错误 `ErrAutoRollbackForbidden` 拒绝，并有测试守护。
- **平台段统一最严仪式（O1 已裁：合一，无红线/普通之分）**：所有平台组每次 Publish 都强制「事前哨兵（草案 seq）+ 人工审批」；判异强制人工 `parameters.Rollback`；每次回滚后自动多租户验证 + 分化告警。不存在「低风险平台组可直发」的快路径。
- 落地最小改动面集中在：一个 public 迁移（044）+ 一组 tenant 表/列、parameters registry/服务少量扩展、evaluation 新增 `gate` 领域与应用服务、快照 `CaptureInput` 增加历史 seq 覆盖、审批执行器增加回滚动作。见 §4 精确清单。

---

## 1. 现状代码基座与 §8 的 reality 映射

### 1.1 两条版本链（reality）

**A. 平台参数（public，全租户生效）**

- `internal/parameters` 只管理 **ScopePlatform** 键（`internal/parameters/domain/parameter.go:19-26`；registry 各 `register*Params` 把键登记为 platform/resource 两级）。
- 表：`platform_config_groups` / `platform_config_versions` / `platform_config_labels`（`pkg/migration/sql/043_platform_config_versions.up.sql:15-46`）。生产生效 = `production` label 所指版本（`label='production'`，`platform_repo.go:114-120, 355-375`）。
- `Publish`：置 published + 记录 `base_version_id` + 挪 `production/latest` label + 同事务审计（`platform_repo.go:202-261`，审计 L246-255，`Operation=ChangeOpPublish`）。
- `Rollback`：把 label 挪到历史 published 版本，**不产新版本**，同事务审计（`platform_repo.go:265-312`，审计 L297-306，`Operation=ChangeOpRollback`）。审计写 public `platform_resource_change_audits`（`internal/audit/domain/change_audit.go:119-122`；`PlatformChangeAuditInsertSQL` 列契约）。
- 服务层是薄转发：`parameters/application/service.go` `Publish L186-191` / `Rollback L195-200` / `Versions L205-207`。
- 版本行除生命周期 `status`/自由文本 `message` 外**没有任何评测结论列**；store API（`internal/parameters/domain/port/store.go:45-76`）没有按 seq 读单版、没有写评测结论的方法（探明：`platform_config_versions` 无 eval 列，无写结论 API）。

**B. 租户资源配置（tenant，单租户/单资源）**

- `ScopeResource` 键存在 registry，但值不落 parameters 存储：落在租户资源行（`agents.parameters` JSONB / skill / mcp / knowledge config）。生效版本由该资源的原生链承载：
  - **agent / knowledge**：产品版本基座 `resource_versions` + `agents.active_version_id`/`rag_workspaces.active_version_id`（`tenant_schema.sql` agents 指针 L56-58；`internal/agent/application/agent_crud.go:192-254` 每次 `Update` 产新 manual 版本并重指 active，是产品写唯一漏斗；回滚 `AgentService.Rollback` `internal/agent/application/agent_version.go:120-160`，仓库 `PgAgentRepo.Rollback` `agent_repo.go:836-868`）。
  - **skill**：原生 `skill_revisions` + `skills.active_revision_id`（`tenant_schema.sql:340,321-380`）；回滚 `VersionService.RollbackRevision`（`internal/skill/application/version_service.go:779`）。
  - **mcp**：无产品版本链（`pkg/versioning/version_tx.go:30-33` 只注册 agent/knowledge）；只有评测控制面 `resource_revisions`(kind=mcp)，**无回滚入口**。
- 评测/优化控制面链：`resource_revisions`（`tenant_schema.sql:386-407`）`source IN ('manual','optimization','rollback')`、`status draft|published`、`parent_revision_id` 自引用。`RevisionService` 只有 `Create/Get/Publish`（`internal/evaluation/application/revision_service.go:34-149`），**无 RollbackToRevision、无「翻回历史 published」**（探明确认）。`resource_revisions` 的 Create/Publish **不写** `resource_change_audits`。
- experiment 锁 stable/canary `revision_id`（`evaluation_service.go:15-20`、DDL `experiment_repository.go:100-108`）；`Promote`=翻 revision 状态 + 挪 `evaluation_deployments` 指针 + 标记 candidate（`experiment_repository.go:488-530`），agent 内容写回生产在 HTTP handler 层调 `ApplyPublishedRevision`→`AgentService.Update`→产新 manual 产品版本（`api/http/handler/evaluation_handler.go:614-642`；`api/wiring/evaluation_agent_adapter.go:116-172`）；`ExperimentService.Rollback` **只清 canary**，不恢复 promote 前生产内容（`experiment_repository.go:415-418`，探明确认）。

### 1.2 §8 表 → 4 实现段的映射（O1 合一后）

| 本设计段 | 对应 §8 行 | 目标实体 | 回滚载体 | 自动/人工 |
|---|---|---|---|---|
| **L1** | L1 | agent 执行期工具调用 | 内联拦截（无回滚） | 自动 fail-closed |
| **L2** | L2 | 采样观测（judge/行为/规则） | 无回滚，告警 + 评审池确认 | 告警自动，确认人工 |
| **L3-资源** | L3 | 租户资源配置（某 kind 的生效版本） | 该 kind 原生回滚入口（见 §3.3） | 策略可配，默认人工；auto 仅对确定性安全 kind |
| **L3+ 平台** | L3+ | 已发布平台版本（全部平台组，无红线/普通之分） | `parameters.Rollback`（唯一） | **强制人工** + 事前发布哨兵 + 事后多租户验证；auto 被代码拒绝 |

> 层定义说明（O1 已裁 = 合一）：§8 只有 4 行；平台段**不拆 L3-平台/L3+ 两级**。§8 L3+ 的平台参数处置统一为**一套最严仪式**：所有平台组每次 Publish 前置哨兵 + 人工审批（阻断式），判异强制人工 `parameters.Rollback`，回滚后自动多租户验证 + 分化告警。不存在「红线组 vs 普通组」之分，`RedLine` 概念删除（本设计无此字段/键）。

### 1.3 判定链路现状（代码事实）

- 观测判定：`internal/evaluation/application/observation_service.go` `Process L67-120`；`buildObservation L167-196`（`verdict=pass` 默认于 L195）；`applyJudge L261-299`（faithfulness/relevance/completeness，任一维度 < `constants.JudgeBelowThreshold=0.5` → `VerdictFlag`，L294-297）；`applyAnomalyVerdict L304-329`（rule→block；行为 abandonment/escalation、judge 跌阈→flag；同时 `IncEvalGateAction("detect","block"|"flag")`）。
- 观测落库后唯一回调：`escalateToReview L124-135`（`deps.Review.TryEscalateObservation`，fail-open）。纯 block 无 judge 信号不进评审池（`review_pool.go:163-179` `len(Judge)==0 → nil`）。
- 评审池：`ReviewServiceDeps`（`review_service.go:26-36`）只有 `ReviewRepository/SuiteRepository/TraceEvidenceReader/Metrics/Cfg/TenantIDs`；`Decide L148-187` 后 `applySideEffects L191-204`（judge_misjudgment→calibration；fail/case_revision→attribution + promote draft），副作用全在 eval 模块表内。`eval_review_items` source_type 仅 `observation|case_result`、trigger_reason 5 值（`tenant_schema.sql:568-604` 及随后的 DROP/ADD CHECK 模式；`review_pool.go:34-41` 常量）。
- 门禁指标：`eval_gate_action_total{layer,action}` 定义 `pkg/observability/prometheus.go:371-375`；生产 layer 值仅 `rule_guard`/`detect`（`rule_guard.go:73`；`observation_service.go:308,317,322,327`）。
- 审批载体：通用 `agent_tool_approvals` + `subject_kind`（`internal/agent/domain/tool_approval.go:28-33`）+ 加密 payload Arguments + 状态机；`ToolApprovalService.ExecuteApprovedAction`（`internal/agent/application/tool_approval_service.go:830-866`）→ wiring `approvalActionExecutor.ExecuteApprovalAction`（`api/wiring/approval_action.go:56-67`）→ `evaluationOperations` 字符串 map（`:74-86`，如 publish_suite/promote_experiment/rollback_experiment）→ 同步执行，失败分类 `notExecuted`/`unknown_outcome`。live 创建点现仅 MCP 成员审批 + agent 工具门控（`router.go:204` 评测写端点一律 requireAdmin，`evaluation_action` 无 live 创建点）。
- 快照：run 创建时 `snapshotCapturer.Capture`（`api/wiring/evaluation_snapshot.go:48-92`）；`captureGroup L96-119` 只取 `IsCurrent` 版本（`:104-107`），无历史 seq 选择。`CaptureInput{Resource, SuiteRevisionID, RequestedBy}`（`internal/evaluation/domain/port/evaluation.go:275-280`）无平台 seq 覆盖字段；`EnqueueRunInput`（`job_service.go:24-29`）同构。执行期快照随 ctx（`RunStored service.go:110-135` 注入；`runCase` 从 ctx 读，不查 DB）。平台 seq 锚点=观测 `param_version.platform.version_seq`（`eval_observations.param_version` JSONB，`tenant_schema.sql:554`；`resolvePlatformVersion observation_service.go:244-257`；`constants.PlatformGroupEvaluation` `pkg/constants/evaluation.go:124`）。

### 1.4 裁决记录（2026-09-03 全量闭合）

已裁（不可回退）：① §8 全量四层；② L3 资源级回滚 = 桥接既有资源 revision/产品版本/experiment-baseline 机制，不新建租户资源参数平台链；③ 平台参数一律 `parameters.Rollback` + 审计、禁止任何自动回滚、强制人工；④ **O1 合一**：平台段不拆红线/普通，全部平台组 Publish 前置哨兵 + 人工审批（阻断式）、判异强制人工、回滚后多租户验证；⑤ **O2 宿主租户**：平台动作（发布哨兵/验证 job）宿主 = 触发动作的 HTTP 请求所在租户（参数 admin 台所在 default/租户上下文），经 reqctx 捕获并固化到动作/job 载荷；⑥ **O3 RiskTier 取值**：先出草案（§7.2 O3）供 writing-plan 前核对，控制逻辑不依赖具体取值；⑦ **O4 L1 语义**：检测恒开 + 执行受控（enabled=false=无规则可命中=放行，命中检测不依赖 enabled）+ `StratumEvalRuleDisabled` 提示；⑧ **O5 哨兵阻断式**：Publish 必须等哨兵 verdict + 人工审批才生效。

---

## 2. 门禁编排（总控）

### 2.1 四段模型

每个门禁事件走「**触发 → 证据 → 决策（硬编码） → 动作（版本机制+审计）**」四段，动作结果写回「门禁结论」（§8 L432）。

```
触发源（版本变更/判异）
  ├─ 平台 Publish（草案→生产）…… L3+（每发布都走事前哨兵，§3.4）
  ├─ 平台生产后运行态观测 verdict（flag/block，锚 platform seq）…… L3+/L2
  ├─ 租户资源 publish/promote/编辑生效（agent Update→版本、experiment promote）…… L3-资源
  └─ 运行态观测 verdict（锚 resource revision/生产版本）…… L3-资源/L2
        │
        ▼
证据聚合（窗口内，DB 聚合 + 确认 run）
  ├─ eval_observations 按 (目标实体版本 × resource × stratum) 聚合 verdict/维度得分
  ├─ run 级：同 suite 新版本 vs base_version 的 by_dimension delta（确认 run / 发布哨兵 run）
  └─ review 人工结论（金标准）
        │
        ▼
决策（纯函数 Decide，确定性阈值/策略，禁 LLM）→ GateDecision
  ├─ none                      → 无事（可选结论回写 pass）
  ├─ l2_escalate               → 告警 + 评审池入池（人工确认判异）
  ├─ rollback_manual           → 建审批（ToolApproval，operation=rollback_*）→ 人工 approve → 执行回滚
  └─ rollback_auto             → 仅 L3-资源且策略 auto=true 且 kind 有确定性安全通道 → 执行回滚
        │
        ▼
动作执行（全部走版本机制 + 审计）
  ├─ 平台：parameters.Rollback（挪 label；审计在 store 同事务内）→ 结论回写版本行
  ├─ 资源：ExperimentService.Rollback 清 canary | AgentService.Rollback | VersionService.RollbackRevision | WorkspaceService.RollbackWorkspace（各自带原生审计）→ 写 eval_gate_actions 台账
  └─ L1：内联拦截（已存在）
        │
        ▼
结论写回
  ├─ 平台版本行 eval_state（public 044）
  ├─ eval_gate_actions 台账（tenant）
  └─ eval_gate_action_total{layer,action} 计数
```

### 2.2 领域类型（新增，`internal/evaluation/domain/gate.go`）

```go
// GateTarget 唯一标识一个可被门禁动作的已发布、有版本实体。
type GateTarget struct {
    Scope    Scope    // platform | resource（复用参数 Scope 语义）
    GroupKey string   // Scope==platform 时必填（如 "evaluation"）
    VersionSeq int64  // Scope==platform 时必填（已发布 seq）
    ResourceKind ResourceKind // Scope==resource 时必填
    ResourceID  string        // Scope==resource 时必填
    RevisionID  string        // resource 目标 revision（可为 product version id / revision id）
}

// GateDecision 是门禁决策结果（确定性产出）。
type GateDecision string
const (
    GateNone             GateDecision = "none"
    GateL2Escalate       GateDecision = "l2_escalate"         // 告警 + 评审池
    GateRollbackManual   GateDecision = "rollback_manual"     // 建审批
    GateRollbackAuto     GateDecision = "rollback_auto"       // 仅 L3-资源 auto
)

// GateVerdict 是门禁对某个目标版本的结论（写回版本行的形态）。
type GateVerdict string // unknown|pass|flag|block|rollback_recommended|rollback_executed|sentinel_passed|sentinel_failed

// GateEvidence 是窗口内证据的不可变摘要（喂给 Decide 的输入）。
type GateEvidence struct {
    AnomalyCount    int
    RuleBlockCount  int      // 确定性信号
    JudgeFlagCount  int
    ConfirmationRun *RunComparison // 确认 run 版本对比（可空）
    ReviewVerdict   string         // 人工结论（可空，金标准优先）
}

// GatePolicy 是决策输入之一：目标是否进回滚候选 / 是否允许 auto / 是否支持回滚。
// （O1 合一后无 RedLine 字段——平台段不区分红线/普通，一律 rollback_manual。）
type GatePolicy struct {
    RiskTierHigh        bool // 目标受影响参数在 registry 声明 RiskTier=high
    AutoRollbackAllowed bool // 仅 L3-资源语义化；平台段恒 false
    RollbackSupported   bool // mcp=false（无产品回滚链）；其余 kind=true
}
```

### 2.3 决策逻辑 = 硬编码确定性（禁 LLM）

`Decide(policy GatePolicy, ev GateEvidence) GateDecision` 是**纯函数**（表驱动测试），规则固定如下（阈值全进 `pkg/constants/evaluation.go`，见 §4.2.4）：

1. 若有评审池人工结论 `confirm_regression` 或 `confirm_rollback` → 直接 `rollback_*`（人工金标准优先，§6.6 语义）。
2. `RuleBlockCount ≥ GateRuleBlockRollbackMin`（确定性规则命中）→ 该目标进入回滚候选（但平台目标仍走 `rollback_manual`，见 §3.4）。
3. `AnomalyCount ≥ GateAnomalyRollbackMin` **且** `ConfirmationRun` 显示受维度劣化超阈值 → 进入回滚候选。
4. 平台 Scope → 候选一律映射 `rollback_manual`（auto 被 §3.4 拒绝）；资源 Scope → 按 `policy.AutoRollbackAllowed` 映射 `rollback_auto` 或 `rollback_manual`。
5. 以上皆不满足但有 flag/block → `l2_escalate`。
6. 窗口内 `AnomalyCount < GateAnomalyAlertMin` 且无 run 级劣化 → `none`。

> 任何一条分支都不调用 LLM；judge/行为/规则只负责把信号写进 `GateEvidence`。策略来自 registry 声明 + 平台运行键（§3.3/§4.2.2），策略不是代码内 if-else 堆砌，而是数据表（registry `RiskTier`），但**判定流程本身固定写死**。

### 2.4 窗口与聚合

- 每个 `(GateTarget)` 维护一个门禁观察窗口 `GateWindow`：窗口时长 `GateObservationWindow`（默认常量，如 10min）、触发后置 `GateCooldown` 冷却，防止抖动重复回滚。
- 窗口内从 `eval_observations` 聚合（SQL：`WHERE param_version->'platform'->>'version_seq'=$seq` 或 `resource` 锚点匹配 + `created_at > window_start`），新增索引见 §4.1.2。
- 确认 run（L3 auto 前置 / L3+ 判异证据）：对受影响点跑哨兵/标准集确认（§3.6 tier），与 base 版本同 suite 对比；新增 run 级对比服务（§3.2）。

### 2.5 集成点（不侵入热路径）

- **观测判定后回调**：`observation_service.go` `Process` 在 `Save`（L108）与 `escalateToReview`（L118）之间新增 `gate.HandleObservation(ctx, tenantID, obs)`（fail-open，与 escalateToReview 同风格，`deps.Gate==nil` 跳过）。实现（`gate_service.go`）做窗口聚合+决策；决策动作**不内联执行回滚**——回滚走审批/执行器（人工）或 bounded 异步 job（auto），避免阻塞观测 worker。
- **平台 Publish 门**：见 §3.4（发布前置哨兵全量）。低层 `store.Publish/Rollback` 保持原样；gated 流程在 wiring/HTTP 层组合。
- **experiment promote / 资源发布门**：`evaluation_handler.PromoteExperiment`（`evaluation_handler.go:614-642`）与 agent 产品 `Update`（`agent_crud.go:192`）成功后发"版本生效事件"（在各自审计点旁）→ gate 开窗。设计上**不在** promote/Update 的同步事务内跑门禁（不阻塞产品写），只挂异步通知。

---

## 3. 各层设计

### 3.1 L1 规则护栏（内联 fail-closed；O4 已裁 = 检测恒开 + 执行受控）

**目标**：§8 L1。agent 执行期确定性规则命中即即时拦截（fail-closed），命中同时产出一条评测观察。
**现状**：`internal/agent/application/rule_guard.go` `RuleGuard.Check L55-80`（命中→`RuleBlockedError`，fail-closed L13-22；`IncEvalGateAction("rule_guard","block")` L73）；调用点 `tool_execution_guard.go L44-52`；观测旁路 `agent_service.go emitObservation L124-158`；登记平台键 `evaluation.ruleguard.enabled` + `evaluation.ruleguard.denylist`（registry `registerRuleGuardParams` `registry.go:488-507`，`enabled` 默认 false）。
**Recon 缺口与设计**：

- 缺口①规则类型单一（仅 `tool_denylist`）。设计：**本卡不做规则内容扩张**（安全/格式/PII/注入多类型 = 独立功能，列入 §6 不做）。保留现有规则记录模型；在 `RuleGuard` 结果中把「命中类别」从字符串改为小枚举 `RuleKind`（现仅 `tool_denylist`），为后续扩展留缝，不新增规则引擎。
- 缺口②`enabled` 默认 false = 默认 fail-open（与 §14「规则护栏命中 fail closed」表面冲突）。设计（**O4 已裁**）：**检测与执行分离**——
  - **检测恒开**：`emitObservation` 侧的规则命中检测（denylist 匹配）独立于 `enabled`，命中即产 block 观测 + 计数（L2/L3 信号可用），**不依赖执行开关**。
  - **执行（拦截）受控**：真正 `RuleBlockedError` 拦截仍以 `evaluation.ruleguard.enabled` 为准（默认 false = 未配置任何规则时零命中、不误伤；管理员启用 denylist 后命中即拦截）。「默认 fail-open」从「命中被忽略」修正为「未启用规则 = 无规则可命中」，消除与 §14 的表观矛盾；新增告警 `StratumEvalRuleDisabled`（denylist 非空但 enabled=false 时提示），避免管理员误以为已拦截。
**硬编码点**：命中→block 是确定性逻辑；`enabled` 布尔 + denylist 列表是平台运行键（config，非 LLM）。
**动作**：拦截 = 既有 fail-closed；拦截同时产 block 观测；计数进 `eval_rule_hit_total` / `eval_gate_action_total{layer="l1_rule",action="block"}`。

### 3.2 L2 判异告警 + 人工确认

**目标**：§8 L2。judge 跌阈 / 行为异常 → 告警（飞书）+ 人工确认。
**现状**：verdict flag/block 产生于 `applyJudge`/`applyAnomalyVerdict`（`observation_service.go:261-329`）；flag 观测进评审池走 `escalateToReview`→`TryEscalateObservation`；评审池 `Decide` 金标准；指标 `eval_gate_action_total{layer="detect",...}`。阈值常量 `pkg/constants/evaluation.go:89-116`。告警规则文件 `monitoring/local/rules/stratum-evaluation.yml`（StratumEvalBehaviorAnomaly / StratumEvalReviewBacklogHigh 等）。
**Recon 缺口与设计**：

- 缺口①judge 跌阈无独立告警。设计：新增告警规则 `StratumEvalJudgeBelowThreshold`，对 `eval_gate_action_total{layer="detect",action="flag"}`（judge 维）速率 + `eval_judge_score` 直方图尾部（<0.5 占比）告警；runbook 指向评审池操作台。纯告警规则，挂既有 Alertmanager→飞书 route（`docs/agent/observability.md` 适配器，不新建通道）。
- 缺口②无 run 级 pass_rate / 版本对比跌阈告警。设计：新增 **run 级回归对比**（确认 run 的输入，L3 也复用）：对同一 `EvalSuiteRevision`、同一目标资源跑 base 版本与新版本的 run，比较 `run.metrics.by_dimension`（已有结构，`spec §6.2 L267-286`），任受声明影响维度 delta 跌破 `RunRegressionDeltaThreshold`（常量，默认如 -0.05）→ 判 run 级 flag → `gate` 消费 + 告警 `StratumEvalRunRegression`。落点：新增 `internal/evaluation/application/regression_compare.go`（纯对比 + 阈值），依赖现有 run 存储查询（run 表已存 `metrics` + 双版本锚点）。这同时服务 §7.2.4「历史版本重跑对比」与 §3.4 发布哨兵 verdict。
- 缺口③评审池 source 仅 observation/case_result，block 无 judge 信号不进池。设计：`review_pool.go` `TriggersForObservation`（`:163-179`）增加行为分支：`Signals.Behavior` 有 abandonment/escalation 且 verdict=flag → 触发 `behavior_anomaly`；`eval_review_items.trigger_reason` CHECK 需加 `'behavior_anomaly'`（沿用该表已有的 DROP/ADD CONSTRAINT 幂等模式，`tenant_schema.sql` 紧随表定义）。
- 缺口④单条飞书直达 reviewer：复用现有告警（积压/新增待审）+ 顶栏铃铛（`web/src/modules/approvals/ApprovalNotificationBell.tsx`）。本卡把 L2「待确认队列」接到评审池现有 UI；不做单条飞书直发（进 §6 不做）。
**动作**：告警自动、确认人工（评审池 `Decide`）；结论回写观测/评审项（既有）。门禁计数 `eval_gate_action_total{layer="l2",action="alert"|"review"}`。

### 3.3 L3-资源（租户资源高风险参数判异 → 资源回滚桥接）

**目标**：§8 L3 + 用户裁决②。租户资源自身配置（resource-scope 高风险参数）判异时，回滚 = 桥接该资源**既有**回滚机制，不新建租户资源参数平台版本链。
**触发与目标判定**：观测 verdict（flag/block）锚定资源生效版本（`param_version.resource` / `PinnedAssignments` / `evaluation_deployments` stable/canary）。门禁把目标解析为三种「当前坏状态」之一（见下），并取「上一好状态」= base/稳定版本/上个 active 产品版本。
**回滚三路径（按坏状态类型）**：

1. **金丝雀流量坏（canary revision 判异，尚未 promote 全量）** → `ExperimentService.Rollback`（`internal/evaluation/application/experiment_service.go:146-150` → 清 `evaluation_deployments.canary_*`，`experiment_repository.go:415-418`）→ 流量回 stable。改动最小，全部 kind 通用。
2. **产品生效版本坏（promote 已写回 / 用户编辑已生效，active_version_id 指向坏版）** → 该 kind 原生产品回滚：
   - agent：`AgentService.Rollback(ctx, id, RollbackAgentInput{ActorID, VersionID})`（`agent_version.go:120-160`，仅允许 deprecated 历史版本，`loadRollbackTarget :165-178`）。
   - knowledge：`WorkspaceService.RollbackWorkspace`（`internal/knowledge/application/workspace_version.go:111-…`）。
   - skill：`VersionService.RollbackRevision(ctx, skillID, targetRevisionID, actorID)`（`version_service.go:779`）。
   - 三者仓库层均为「降级当前 + 目标回 published + 重指 active，不产新行」（`pkg/versioning.RollbackVersionTx` `version_tx.go:140-156`；skill `skill_version_repo.go:457-478`）。回滚审计走各自既有 change audit（`ChangeOpRollback`）。
3. **mcp（无产品链、无 eval 回滚入口）** → 不支持自动/手动一键回滚。L3 对 mcp 降级为「告警 + 评审池/人工在操作台手动重配」；在门禁策略里标记 `rollback_supported=false`（避免 UI 给误导按钮）。列入 §6 不做（产品版本链接入 mcp 是独立工程）。

> 判定「上一好版本」：金丝雀路径 = deployment.stable_revision_id；产品路径 = 当前 active 版本在版本链上的前一个 published/deprecated 版本（产品版本列表 `agents.active_version_id` 的上一非当前版本；skill `active_revision_id` 链）。由新增 `ResourceRollbackPlanner` 解析（§4.3.3）。
**策略（GatePolicy，资源侧）**：

- `RiskTier`：取自受影响资源参数在 registry 的 `RiskTier`（§4.2.1）最高档。仅 `high`（对应 spec ImpactMajor）进入回滚候选；`medium` 最多 L2；`low` 仅记录。
- `AutoRollbackAllowed`：默认 false（§8 L433「自动回滚默认关闭」）。显式策略开启且目标为「确定性安全 kind + 产品/金丝雀路径」才允许 auto；auto 动作必须走执行器 + 审计 + 写 `eval_gate_actions`。
- 策略存储：平台运行键 + registry 声明（§4.2.2）。资源级策略**不**按单个资源配置（避免页面膨胀），按 `(resource_kind × risk_tier)` 全局档位，管理员可整档关闭。
**动作链（默认人工）**：`rollback_manual` → 门禁建 `ToolApproval`（`subject_kind=evaluation_action`，`Arguments.operation="rollback_resource"`，Arguments 携带 target/坏版本/好版本/planner 输出）→ 审批人 approve → wiring `approvalActionExecutor` 新分支 `executeResourceRollback` 调对应资源 service → 成功 `MarkExecuted`（unknown_outcome 语义沿用）。**审批 = 复用现有工作台/铃铛/权限**（`web/src/modules/approvals/`；admin/owner 白名单 `useApprovalsPage.ts:16,91-101`）。
**硬编码点**：路径选择（坏状态类型→回滚入口映射）是确定性映射表；`Decide` 见 §2.3；auto 允许性由策略档位决定，执行器再校验一次（defense in depth）。
**审计**：资源原生回滚自带 `resource_change_audits`（`ChangeOpRollback`）；另写 `eval_gate_actions` 台账（谁在何时因哪个 verdict 触发）。
**计数**：`eval_gate_action_total{layer="l3_resource",action="rollback_manual"|"rollback_auto"|"rollback_refused"|"rollback_unsupported"}`。

### 3.4 L3+ 平台参数（统一最严仪式：发布哨兵 + 判异人工回滚 + 事后多租户验证）
>
> **O1 已裁 = 合一**：平台段无红线/普通之分。本节的四件事（发布前置哨兵、判异强制人工、事后多租户验证、分化告警）**对全部平台组一律生效**；`RedLine` 字段/键删除；不存在「非红线组直发」快路径。§8 L3+ L434 表注「平台参数影响全租户，永远人工、禁止自动」是本节底线，auto 回滚在编译/接线层面不存在。

**目标**：§8 L3+。任何平台组版本变更（Publish）与生产后判异，统一走「事前哨兵 → 人工审批发布 / 判异人工回滚 → 事后多租户验证」完整仪式。平台组 = registry 全部 `ScopePlatform` 键的所属 group（recon：agent 组 system_prompt/compaction、evaluation 组 optimizer/judge/observe/ruleguard、agent.factcheck 组、trace、memory 组，见 §7.2 O3 全键盘点）。

**四件事（全部叠加、不分支）**：

1. **事前哨兵前置门（每次 Publish 必过，O5 阻断式已裁）**：平台组的 Publish 不再直通 `store.Publish`。新编排 `publishGateCoordinator`（wiring，参数 admin handler 使用）：
   - 对**草案版本**（有 seq、status=draft、快照不可变）以 seq 覆盖跑哨兵集（5-15 case，§3.6 tier）。哨兵 run 需要快照 = 草案 seq —— 由 §4.3.4 的 `CaptureInput.PlatformSeqOverrides` 支持（`ListVersions` 已返回含 draft 的不可变快照，`platform_repo.go:408-450`，故无需发布即可对草案跑）。
   - 哨兵 verdict（run 级回归对比 + 维度阈值，§3.2-②同一判定）：`pass` → 进入「人工确认发布」；`flag/block` → 草案标 `eval_state=sentinel_failed`，Publish 拒绝（fail-closed），通知平台管理员。
   - 人工确认：每次平台发布需 `ToolApproval`（`operation="publish_platform_version"`，Arguments 带哨兵结果）。approve 后由系统 actor 调 `store.Publish`（原审计不变）+ 写 `eval_state=sentinel_passed`。
   - **宿主租户（O2 已裁）**：哨兵 run/suite 是 tenant-scoped，而平台发布是 public 操作。发布/回滚请求来自参数 admin 台（requireAdmin 的租户上下文），handler 把该请求的 tenantID（reqctx）作为**宿主租户**透传给哨兵（在该租户的哨兵 suite revision 上跑）并固化到后续验证 job 载荷；动作链内宿主稳定，不随异步执行变化。
   - **发布延迟是已接受的代价**（O5 阻断式）；启用节奏由 `evaluation.gate.enabled` 总开关控制（默认 false，见 §4.2.2——开启即全平台组发布前置哨兵）。
2. **运行时判异强制人工**：平台版本 `vN` 成为 production 后，观测 verdict 锚 `vN`（`param_version.platform.version_seq`）。`gate` 按 groupKey+seq 建 `GateTarget`，累积窗口证据 → `Decide` 候选 rollback → 因 `Scope==platform` 一律 `rollback_manual`（硬编码规则 §2.3-4）→ 建 `ToolApproval`（`subject_kind=evaluation_action`，`operation="rollback_platform"`，Arguments 带 groupKey + targetVersionID = 回滚到的历史 seq）→ admin/owner approve → 执行。**红线组 / 普通组无差别——本就不允许 auto**。
3. **事后多租户验证 job（每次平台回滚后自动入队）**：回滚执行成功后入队 `multi-tenant verify`：遍历活跃租户（复用评审积压的 `TenantIDs` = IAM `ListActiveTenantIDs`，`api/wiring/evaluation.go` buildReviewService `:570-592` 已注入），对受影响点重跑确认/哨兵，或对比回滚前后全租户聚合 delta（分层：tier/行业/流量规模，防辛普森）。产出报告 + 指标；恢复不达标 → 告警 `StratumEvalMultiTenantVerifyNotRecovered`。实现为 eval 应用内的一个 bounded worker（不在观测热路径）；宿主租户（O2）来自回滚动作时固化到 job payload 的 tenantID。查询跨租户用既有 per-tenant 轮询/聚合模式（评审积压已这么做），**不建 public 租户数据表**。
4. **多租户分化检测（§9.4.2）**：平台版本窗口内按租户聚合观测，检测「多数改善少数劣化」的分布效应 → 告警 `StratumEvalPlatformMultiTenantDivergence`，劣化租户名单下钻。本卡只做**信号告警**（不做自动处置），劣化名单供人工在归因视图下钻。

**无自动不变量（代码级）**：平台 Scope 的 rollback 执行器入口第一行断言 `policy.AutoRollbackAllowed==false`，若调用方传 true → 返回类型化错误 `port.ErrAutoRollbackForbidden`（新），计数器 `eval_gate_action_total{layer="l3_platform",action="auto_refused"}` +1。auto 执行路径在**编译/接线层面就不存在**（wiring 不提供「平台自动回滚」分支），配合测试守护。

**结论写回**：平台版本行 `eval_state` 全生命周期可追踪（`unknown|sentinel_failed|sentinel_passed|anomaly_flag|anomaly_block|rollback_recommended|rollback_executed`，配合 §8 L432 的"该版本评测结论"要求）。回滚后观测锚回旧 seq（既有 `resolvePlatformVersion` 自动跟随 label）。

**计数**：`eval_gate_action_total{layer="l3_platform",action="rollback_manual"|"auto_refused"|"publish_gated"|"publish_blocked"}`、`{layer="l3_sentinel",action="block"|"pass"}`、`{layer="l3_multitenant_verify",action="queued"|"recovered"|"not_recovered"}`。

**硬编码点**：哨兵 verdict→放行/阻断是确定性阈值（§2.3 同一 `Decide` 的 run 级变体）；发布/回滚均人工（审批）；无自动路径同不变量。

---

## 4. 数据模型与接口改动清单（精确位置）

> DDL 规则：编号迁移只动 public；tenant-only DDL 只进 `pkg/storage/postgres/tenant_schema.sql` 且幂等（IF NOT EXISTS / ADD COLUMN IF NOT EXISTS / DROP+ADD CONSTRAINT 模式）。下一编号迁移 = **044**（043 为最新，`pkg/migration/sql/043_*.up.sql` 存在）。

### 4.1 DDL

#### 4.1.1 public 迁移 `pkg/migration/sql/044_platform_gate_eval_state.up.sql`（新）

平台版本行的「评测结论」回写（§8 L432）。只动 public schema：

```sql
ALTER TABLE public.platform_config_versions
    ADD COLUMN IF NOT EXISTS eval_state TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE public.platform_config_versions
    ADD COLUMN IF NOT EXISTS eval_state_updated_at TIMESTAMPTZ;
ALTER TABLE public.platform_config_versions
    ADD COLUMN IF NOT EXISTS eval_state_updated_by TEXT NOT NULL DEFAULT '';
```

- `eval_state` 值域（应用层约束，DB 不强加 CHECK 以免历史租户约束重排）：`unknown|sentinel_failed|sentinel_passed|anomaly_flag|anomaly_block|rollback_recommended|rollback_executed`。
- 幂等：`ADD COLUMN IF NOT EXISTS`（升级存量 public 表，符合 `docs/agent/migration-tenant.md`）。
- 下游索引/查询必须排在该迁移之后（无新依赖列索引）。

#### 4.1.2 tenant schema `pkg/storage/postgres/tenant_schema.sql`（追加，幂等）

- **`eval_gate_actions`（台账，append-only）**：

```sql
CREATE TABLE IF NOT EXISTS eval_gate_actions (
    id             TEXT PRIMARY KEY,
    scope          TEXT NOT NULL CHECK (scope IN ('platform','resource')),
    target         JSONB NOT NULL,            -- GateTarget 序列化（group/version_seq 或 resource ref）
    layer          TEXT NOT NULL,             -- l1_rule|l2|l3_resource|l3_platform|l3_sentinel|l3_multitenant_verify
    decision       TEXT NOT NULL,             -- GateDecision
    action         TEXT NOT NULL DEFAULT '',  -- block|alert|review|rollback_manual|rollback_auto|refused|unsupported|publish_gated
    evidence       JSONB NOT NULL DEFAULT '{}'::jsonb,  -- GateEvidence 摘要快照
    actor          TEXT NOT NULL DEFAULT '',
    approval_id    TEXT NOT NULL DEFAULT '',  -- 关联 agent_tool_approvals.id（人工路径）
    host_tenant_id TEXT NOT NULL DEFAULT '',  -- O2：平台动作宿主租户（发布/回滚请求 reqctx tenantID，job 固化）
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_eval_gate_actions_target_time
    ON eval_gate_actions (scope, target, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_eval_gate_actions_decision
    ON eval_gate_actions (decision, created_at DESC);
```

- **`eval_observations` 追加索引**（门禁窗口聚合用，不含新列）：

```sql
CREATE INDEX IF NOT EXISTS idx_eval_observations_verdict_time
    ON eval_observations (verdict, created_at DESC);
```

（平台 seq 锚点已存在 `param_version` JSONB，按需以 JSON path 查询；如聚合频度高，writing-plan 可评估新增生成列 `platform_seq`，本设计先不加，避免与 `resolvePlatformVersion` 双写漂移。）

- **`eval_review_items.trigger_reason` CHECK 扩展**：加 `'behavior_anomaly'`，沿用该表现在 DROP/ADD CONSTRAINT 的幂等模式（`tenant_schema.sql` 紧随 `eval_review_items` 表定义已有先例）。
- 备注：L3 人工回滚审批与 L3+ 发布/回滚审批走 `agent_tool_approvals`（agent 模块既有表），**不新增审批表**。

### 4.2 parameters 侧

#### 4.2.1 registry 风险档声明

- `internal/parameters/domain/parameter.go` `ParameterDefinition`（L68-90）增加字段：
  `RiskTier RiskTier`（值 `none|low|medium|high`，`high` 对应 spec ImpactMajor）。枚举 `type RiskTier string` 与本文件 `Scope` 并列定义。
- `internal/parameters/domain/registry.go` 各 `register*Params` 为键声明档位（默认 `low`）。registry 不变量测试（`registry_test.go` 已有大量守卫模式）补：每个键必须声明 `RiskTier`。权威取值草案见 §7.2 O3（writing-plan 前核对）。
- 语义衔接：evolution 域已有 `ChangeImpact`（`internal/evaluation/domain/evolution_diff.go:3-11`）与 `classifyImpact`（`tunable_registry.go` 相关），本卡**不重构**它们；仅把 registry `RiskTier` 作为门禁权威源，`classifyImpact` 归因侧后续（Phase 3 §9.2）再统一（列入 §6 不做）。

#### 4.2.2 平台门禁策略键（public platform key，registry 追加）

在 `registerObservationParams`/新 `registerGateParams`（`registry.go` 仿 `registerRuleGuardParams:488-507`）增加：

- `evaluation.gate.enabled`（bool，默认 false：总开关，全层观测/动作；关闭 = 只保留 L1/L2 现有告警 + 指标，不触发 L3 动作、平台发布不走哨兵门）。
- `evaluation.gate.auto_rollback_resources`（bool，默认 false：L3-资源 auto 总开关，§8 L433 默认关闭）。
（这些键都走既有 platform 快照版本化 + 审计，配置变更本身可审计。O1 合一后无 redline_groups 之类分层键——平台处置不区分层档，总开关开启即全量。）

#### 4.2.3 store/服务 方法扩展

- `internal/parameters/domain/port/store.go` `PlatformStore`（L45-76）增加：
  - `GetVersion(ctx, groupKey string, versionSeq int64) (PlatformVersion, error)`（按 seq 读单版，含 draft，供哨兵/结论查询；`platform_repo.go` 现 `ListVersions` 已返回全量，可加 SQL 单版方法）。
  - `UpdateEvalState(ctx, groupKey string, versionSeq int64, state, actor string) error`（`UPDATE public.platform_config_versions SET eval_state=..., eval_state_updated_at=NOW(), eval_state_updated_by=$x WHERE group_key=$1 AND version_seq=$2`，public；幂等）。
- `internal/parameters/infrastructure/persistence/platform_repo.go` 实现上述两方法（锁语义非必需，单行 UPDATE 幂等）。
- `internal/parameters/application/service.go` 暴露 `GetVersion` / `UpdateEvalState` 薄转发。
- `Publish`/`Rollback` 本体不改（发布前置哨兵在 wiring/HTTP 组合，§3.4）。

#### 4.2.4 常量

`pkg/constants/evaluation.go`（在 L89-135 现有阈值区后追加门禁常量块）：
`GateObservationWindow`、`GateCooldown`、`GateRuleBlockRollbackMin`（如 3）、`GateAnomalyRollbackMin`（如 10）、`GateAnomalyAlertMin`（如 3）、`RunRegressionDeltaThreshold`（如 -0.05）、`GateAutoRollbackMaxPerDay`（防抖上限）——均带单位/语义后缀。跨包命名规范见 `docs/agent/constants.md`。

### 4.3 evaluation 侧

#### 4.3.1 新领域文件 `internal/evaluation/domain/gate.go`

§2.2 的 `GateTarget/GateDecision/GateVerdict/GateEvidence/GatePolicy` + 纯函数 `Decide(policy, evidence)`（表驱动单测）。

#### 4.3.2 新应用服务 `internal/evaluation/application/gate_service.go`

- `GateServiceDeps`：`Logger/Metrics/Cfg(domain.GateConfig)/Repo(port.GateStore)/Policy(port.GatePolicySource)/Approvals(port.GateApprovalRequester)/Platform(port.PlatformGateOps)/ResourceRollback(port.ResourceRollbackExecutor)`。
- `HandleObservation(ctx, tenantID, obs)`：窗口聚合（§2.4）→ `Decide` → 按决策路由：`l2_escalate`→告警/评审池；`rollback_manual`→`Approvals.Request`（建 ToolApproval）；`rollback_auto`→auto executor（带冷却/日限）。
- 平台结论写回经 `Platform.UpdateEvalState`。
- 平台发布哨兵协调 `RunSentinelForDraft(ctx, hostTenantID, target, suiteRevisionID)`、多租户验证 job `RunMultiTenantVerify(ctx, hostTenantID, ...)`（§3.4）。
- 复杂度约束：每个方法 ≤10 CC / ≤15 认知 / ≤120 行（门禁判定逻辑拆纯函数，防超限）。

#### 4.3.3 端口（consumer 定义，provider 在 wiring/外部上下文实现）

新增 `internal/evaluation/domain/port/gate.go`：

- `GateStore`：`AppendAction(ctx, tenantID, row) error` + `QueryWindow(ctx, tenantID, target, since) ([]domain.ObservationSummary, error)`。
- `GatePolicySource`：`Resolve(ctx, tenantID, target) (domain.GatePolicy, error)`（provider：wiring 适配器读 parameters registry/平台键）。
- `GateApprovalRequester`：`RequestRollback(ctx, tenantID, kind, args map[string]any) (approvalID string, err error)`（provider：wiring → `ToolApprovalService.Request`，subject_kind=evaluation_action）。
- `PlatformGateOps`：`GetVersion / UpdateEvalState`（provider：wiring → parameters Service）。
- `ResourceRollbackExecutor`：`Rollback(ctx, tenantID, target domain.GateTarget, actor, decidedBy string, approvalID string) error`（provider：wiring；按 §3.3 分派到 experiment/agent/skill/knowledge；mcp → `ErrRollbackUnsupported`）。
- `ObservationServiceDeps`（`observation_service.go:23-37`）增加可选 `Gate GateSink`（`HandleObservation`），`nil` 跳过（fail-open），`Process` 在 `Save`（L108）后、`escalateToReview`（L118）前调用。

#### 4.3.4 快照历史 seq 覆盖（§3.4 发布哨兵 + §7.2.4 历史版本重跑）

- `internal/evaluation/domain/port/evaluation.go` `CaptureInput`（L275-280）增加 `PlatformSeqOverrides map[string]int64`（groupKey→version_seq，空=现 IsCurrent 语义，兼容存量）。
- `api/wiring/evaluation_snapshot.go` `captureGroup`（L96-119）：命中 override 时按 `v.VersionSeq` 精确匹配取代 `:104-107` 的 `IsCurrent` 过滤；无命中/无该 seq → 沿用 IsCurrent 或空组。
- 链路透传：`job_service.go` `EnqueueRunInput`（L24-29）与 `EnqueueRun`（L41-76，构造 CaptureInput 处 L57-59）增加同字段；HTTP DTO 与 proto 依契约规则经 `proto/evaluation/evaluation.proto`（含 `:69-74` 区）扩展（生成物走 `make proto-gen`，不入 git）。
- 资源侧「重跑任意历史 revision」无需新字段：`Resource.RevisionID` 已是 CaptureInput 字段（探明确认），把目标 revision id 传入即可。

#### 4.3.5 评审池触发扩展

`internal/evaluation/application/review_service.go` / `internal/evaluation/domain/review_pool.go` `TriggersForObservation`（`:163-179`）加 behavior 分支（§3.2-③）。`TenantIDs` 依赖已注入，复用。

#### 4.3.6 run 级回归对比

新 `internal/evaluation/application/regression_compare.go`（§3.2-②）：读同 suite 两个 run（base vs current）`metrics.by_dimension`（run 表已存），纯函数输出维度 delta + 是否跌破阈值。判异确认 run 与发布哨兵 verdict 复用。

### 4.4 agent / 审批侧

- `internal/agent/domain/tool_approval.go`：无需改 SubjectKind（沿用 `evaluation_action`，L28-33）；如语义更清晰可加 `SubjectKindPlatformConfig`，但**非必需**（payload Arguments 已带 operation）。本设计沿用 `evaluation_action` + 新 operation 字符串，减少状态机枚举扩散。
- `api/wiring/approval_action.go` `evaluationOperations`（`:74-86`）增加：
  - `"rollback_platform"` → `executePlatformRollback`（→ parameters Service.Rollback；**首行断言平台禁止 auto**，见 §3.4）。
  - `"rollback_resource"` → `executeResourceRollback`（→ §3.3 分派）。
  - `"publish_platform_version"` → `executePlatformPublishGated`（L3+ 平台发布审批，→ store.Publish + eval_state 写回；全平台组适用）。
  执行器同步调用，失败语义沿用 `notExecuted`/`unknown_outcome` 分类（`approval_action.go:106-108`；`tool_approval_service.go:868-879`）。
- `api/wiring/evaluation_agent_adapter.go` 的 `agentRevisionService`/`agentReadWriter` 供资源回滚读取「上一好版本」复用（Get/List 版本 → Rollback）。不新增 agent 模块领域改动（回滚入口已存在）。

### 4.5 wiring / handler / 路由 / 监控 / 前端

- **wiring**（`api/wiring/`）：
  - `evaluation.go` 附近新增 `buildGateService`（组装 deps：GateStore→新 eval gate repo；GatePolicySource→parameters registry+键；GateApprovalRequester→ToolApprovalService；PlatformGateOps→parameters Service；ResourceRollbackExecutor→组合 experiment/agent/skill/knowledge，参照 `approval_action.go:21-32` 已注入的依赖集合）。
  - `evaluation.go` `buildObservationService`（L598-617）注入 `Gate`。
  - `approval_action.go` `approvalActionExecutor` 构造处（L21-32）注入 parameters Service（如未注入）以支持 `rollback_platform`。
  - 新增 `publishGateCoordinator`（§3.4）：参数 admin Publish 端点注入，宿主租户自 handler reqctx。
- **handler/路由**：
  - `api/http/handler/parameter_handler.go` Publish/Rollback（`POST /admin/parameters/versions/:groupKey/:versionID/publish|rollback`，`router.go:358-359`）：平台组 Publish 改走 gated 协调器（§3.4：草案 seq 哨兵 → 人工审批 → store.Publish）；Rollback 增加「人工审批」态（未审批返回 pending 语义），或由门禁审批流触发。前端版本化配置页相应支持门禁状态展示。
  - `evaluation_handler.go`（含 promote `:614-642`）在 promote/资源发布后发「版本生效」门禁通知（异步）。
- **监控**：`monitoring/local/rules/stratum-evaluation.yml` 新增告警：`StratumEvalJudgeBelowThreshold`、`StratumEvalRunRegression`、`StratumEvalRuleDisabled`、`StratumEvalMultiTenantVerifyNotRecovered`、`StratumEvalPlatformMultiTenantDivergence`；均带 runbook_url（`scripts/quality/monitoring-config-test.sh` 守卫）。
- **前端**（`web/src/modules/evaluation/`、`web/src/modules/approvals/`、`web/src/modules/parameters/`）：分层门禁操作台（告警/待确认/回滚记录/结论状态）接入审批工作台 + 评审池 UI；版本化配置页展示每版 `eval_state` 与「待哨兵/待审批/已阻断」发布状态。详细组件划分留给对应 writing-plan（本卡只定后端契约）。
- **proto**：`proto/evaluation/evaluation.proto` 增补历史 seq 覆盖参数（`make proto-gen` 再前后端联编）。生成物不入 git。

---

## 5. 任务分解（后续 writing-plan 粒度）

> 每项 = 一个可独立规划/评审的 commit 单元。依赖序自上而下；各 writing-plan 再拆实现细节。

- **T1（public 迁移）**：`044_platform_gate_eval_state.up.sql`（4.1.1）+ schema 顺序测试（历史租户幂等）。
- **T2（tenant DDL）**：`eval_gate_actions` 表 + 观测索引 + `eval_review_items` trigger_reason `behavior_anomaly`（DROP/ADD CHECK 幂等，4.1.2）。
- **T3（parameters registry 风险档）**：`RiskTier` 字段 + 枚举 + 各 register* 声明（按 §7.2 O3 草案档位）+ registry 不变量测试（4.2.1）。
- **T4（parameters 平台键 + store 扩展）**：`evaluation.gate.*` 键（4.2.2）；`GetVersion`/`UpdateEvalState` store 实现 + 服务薄转发 + 单测（4.2.3）。
- **T5（门禁领域）**：`domain/gate.go`（类型 + `Decide` 纯函数）+ 表驱动单测（§2.2/§2.3、4.3.1）。
- **T6（门禁应用）**：`gate_service.go` 窗口聚合 + 决策路由 + 冷却 + 结论写回 + 指标（4.3.2）；`port/gate.go`（4.3.3）。
- **T7（快照历史 seq）**：`CaptureInput.PlatformSeqOverrides` + `captureGroup` override + job/handler/DTO/proto 透传 + 单测（4.3.4，发布哨兵的地基）。
- **T8（L2 run 级回归 + 评审池扩展）**：`regression_compare.go` + `TriggersForObservation` behavior 分支 + 告警规则（3.2、4.3.5/4.3.6）。
- **T9（L1 检测/执行分离）**：`RuleGuard`/`emitObservation` 解耦 + `StratumEvalRuleDisabled`（3.1，O4）。
- **T10（审批执行器扩展）**：`rollback_platform`/`rollback_resource`/`publish_platform_version` 三分支 + 平台 auto 拒绝错误（3.4、4.4）。
- **T11（L3-资源回滚 planner/executor）**：`ResourceRollbackPlanner` + `ResourceRollbackExecutor`（agent/knowledge/skill/experiment 分派；mcp unsupported）（3.3、4.3.3）。
- **T12（L3+ 平台发布哨兵协调 + 多租户验证，全量）**：`publishGateCoordinator`（宿主租户经 reqctx）+ `RunSentinelForDraft` + `RunMultiTenantVerify` worker + 分化告警（3.4）。
- **T13（wiring/路由/前端门禁台）**：`buildGateService` + 参数 handler gated 发布流程 + 门禁状态展示 + proto-gen（4.5）。
- **T14（测试与验收）**：每层判定/动作/审计测试（沿用 §15 与「门禁：L1 即时拦截 / L2 告警 / L3 租户回滚策略 / L3+ 平台永不自动 + 发布哨兵阻断」）；`stratum-e2e-development` 系统验收（R3 级，按 `.test/verification.yaml`）。

依赖：T1/T2/T3/T4 并行可起；T5 依赖 T3；T6 依赖 T5+T2+T4；T7 独立；T10/T11 依赖 T6；T12 依赖 T7+T10；T13 收束；T14 最后。

---

## 6. 明确不做（not-doing）

1. **不新建「租户资源参数平台版本链」**（用户裁决②：不造第三个版本系统）。
2. **mcp 资源产品回滚链**：不给 mcp 建 `resource_versions` 接入/回滚入口（独立工程，超出 §8 门禁本意）；L3 对 mcp 仅告警 + 人工重配（`rollback_unsupported` 计数）。
3. **不把门禁判定交给 LLM**；judge 只产信号，门禁动作永远确定性 + 版本机制。
4. **平台参数任何自动回滚路径**（编译/接线层面不存在；`ErrAutoRollbackForbidden` 守护）。
5. **平台发布快路径**：合一后所有平台组 Publish 一律前置哨兵 + 人工审批，**不提供**「低风险组直发」例外（这是已裁行为，非可配置项）。
6. **规则内容扩张**（安全/格式/PII/注入多规则类型）与**规则类型枚举**之外的新规则引擎——本卡只分离检测/执行。
7. **归因重构**：不统一 `classifyImpact`/`TunableEvalProfile`（Phase 3 §9.2）；本卡用 registry `RiskTier` 作门禁权威源。
8. **单条飞书直达 reviewer**、**评审池大改**：L2/L3 人工确认复用评审池 + 审批工作台 + 既有铃铛。
9. **新公共租户数据表 / 平行证据存储**：门禁台账在 tenant；跨租户聚合沿用 per-tenant 轮询 + IAM `TenantIDs`（评审积压模式）。
10. **展示层重做**：门禁操作台复用评审池/审批/版本化配置页组件，不新建大页面框架。
11. **改动 `store.Publish/Rollback` 内部**：发布前置哨兵在 wiring/HTTP 组合，低层版本机制原样。

---

## 7. 自检与开放问题

### 7.1 自检

- 无 TBD/TODO/「待实现」占位：所有缺口均给了机制 + 位置；确需产品定的取值表列在 §7.2 O3，不落在正文。
- 与 §8 表逐行核对：L1（拦截）→§3.1；L2（告警+人工）→§3.2；L3（租户高风险，可配自动回滚默认人工）→§3.3（默认 manual、auto 显式策略）；L3+（平台禁自动+发布哨兵+事后多租户验证）→§3.4。§8 L432 结论写回 → §4.1.1/§4.2.3；L433 自动默认关 → §4.2.2 默认 false；L434 平台永不自动 → §3.4 无自动不变量 + §6.4。
- 签名一致性：所有新 port 含 `tenantID`（租户表方法）；`execTenant` 约束适用 gate repo（tenant 表走既有 execTenant 封装模式）；public `GetVersion/UpdateEvalState` 走 public schema 方法（`public.platform_config_versions` 全限定），不碰 tenant 表——符合 `docs/agent/migration-tenant.md`。
- 语义矛盾检查：检测/执行分离消除了 §14「规则命中 fail closed」与 enabled 默认 false 的表观冲突（O4 已裁接受）；观测仍 `param_version=unknown` 时归因排除、不进回滚候选（沿用 §14 L570 语义）；O1 合一后全文件无 `RedLine` 残留引用（字段/键/表述已清）。
- 复杂度/质量：`Decide` 纯函数表驱动；`gate_service` 方法拆小防超 CC/行数门禁；端口全部在消费方 `domain/port/`（evaluation 不 import parameters/agent infra）。

### 7.2 开放问题裁决记录（2026-09-03 全部闭合）

- **O1（L3-平台 vs L3+ 边界）**：**已裁 = 合一**。平台段不拆两级；§3.4 对全部平台组生效（发布哨兵 + 判异人工回滚 + 事后多租户验证 + 分化告警），删 `RedLine` 字段/键。§8 L3+ → §3.4 单段实现。
- **O1-范围（哨兵发布范围）**：**已裁 = 严格合一**。每个平台组每次 Publish（哪怕小调）都强制前置哨兵（草案 seq）+ 人工审批，阻断式，无快路径；启用由 `evaluation.gate.enabled` 总开关控制。
- **O2（平台动作宿主租户）**：**已裁 = 发布 admin 所在租户**。发布/回滚请求的 reqctx tenantID 作为宿主租户，透传给哨兵 suite 并固化到 `eval_gate_actions.host_tenant_id` + 验证 job 载荷；动作链内稳定。
- **O4（L1 默认语义）**：**已裁 = 检测恒开 + 执行受控**（enabled=false=无规则可命中=放行；命中检测独立于 enabled 恒产 block 观测 + `StratumEvalRuleDisabled` 提示）。初始规则集为空。
- **O5（哨兵阻断式 vs 快路径）**：**已裁 = 阻断式**。Publish 必须等哨兵 verdict（pass 放行 / flag·block 阻断 + `sentinel_failed`）+ 人工审批才生效。
- **O3（RiskTier 权威取值）**：**已裁 = 先出草案供核对**（控制逻辑不依赖具体取值，只依赖"已声明"）。基于 registry 全键盘点（§4.2.1 来源，recon registry.go 各行）的 **high 候选草案**：

**RiskTier=high 候选草案（writing-plan 前与用户核对）**

| 档位 | ScopePlatform 键（全租户） | ScopeResource 键（租户资源） | 依据 |
|---|---|---|---|
| **high（判异升级回滚候选；资源侧可进 auto，平台恒 manual）** | `agent.system_prompt`、`agent.compaction_model`、`evaluation.judge.model`、`evaluation.optimizer.model`、`agent.factcheck.judge.model`、`memory.embedding_model`、`memory.extraction_model`、`memory.reflection_model` | `agent.model`、`mcp.enabled_tools`（判异升级，但 mcp 处置=人工重配，`rollback_unsupported`） | 跨租户生效 / 直接决定模型行为与判分基座 / 安全边界；变更影响面最大 |
| **medium（判异 L2 升级，不进回滚候选）** | 其余 *_model、*_prompt、*_temperature（如 judge.temperature、agent.factcheck.judge.prompt、memory.summary_model 等） | `rag.*`（top_k/score_threshold/reranking/query_rewrite）、`agent.reasoning_effort`、`agent.max_iterations`、`memory.long_term_top_k` | 影响质量但非直接致命 / 变更可观测收敛 |
| **low（仅记录）** | 开关与数值型（enabled、sample_rate、top_k、max_tokens、cooldown_sec、max_claims、recent_groups 等） | `agent.temperature`、`agent.max_tokens/max_context_tokens/max_tokens_per_execution`、`agent.bindings`、`mcp.timeout_ms/max_retries`、`memory.max_facts_per_extraction` | 有默认安全值 / 变更影响局部且可逆 |

> 核对点：high 名单是否过宽/过窄；medium/low 归并可后调（T3 实现时按表声明，核对只需盯 high）。

---

## 附：主规格关键行速查

§8 表 L423-434；§7 六绑定点 L392-421（绑点4「历史版本重跑」L407、绑点5「门禁回滚走版本机制」L408）；§9.4 L478-489（平台多租户效应/回滚高门槛 L489）；§4.5 T1-T4 L213-231；§14 fail-closed 表 L564-575；§17 成功标准 L614-621。
