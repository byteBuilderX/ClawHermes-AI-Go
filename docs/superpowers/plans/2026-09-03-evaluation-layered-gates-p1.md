# 评测分层门禁 P1 地基（T1-T7）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现本计划。Steps 用 `- [ ]` 跟踪。任务在隔离 worktree `/home/yang/go-projects/stratum-card-c-gate-p1`（分支 `feat/card-c-gate-p1`）内执行，禁止 main 直提交。

**Goal:** 落地评测分层门禁 P1 地基：平台配置版本 eval_state 列 + 门禁台账表 + registry RiskTier 分级 + gate 平台参数键与 store 新方法 + gate 领域 `Decide` 纯函数 + gate 应用服务/port 与观测 hook + 快照历史 seq override 透传。

**Architecture:** 门禁决策是硬编码确定性的规则阶梯（禁 LLM）。观测落库后由 `GateService.HandleObservation` 评估窗口证据 → `domain.Decide(policy, evidence)` → 路由（台账 + 指标 + 平台 eval_state 写回 + l2 告警），全程 fail-open 不阻断观测主流程。平台参数版本在 `platform_config_versions` 上携带 `eval_state`；被测执行快照支持按历史 version_seq override（对照重放）。scope 折叠进 policy（平台恒 manual），`Decide` 不重复判断 scope。

**Tech Stack:** Go 1.25.12、pgx v5、Gin、proto/protoc-gen-ginstruct、模块 `github.com/byteBuilderX/stratum`。

## P1 范围裁决记录表（先前的逐字裁决，实现必须遵守）

| # | 裁决 | 依据 |
|---|------|------|
| R1 | `Decide` 参数序 `(policy GatePolicy, ev GateEvidence)`，无 scope 独立参数 | spec §2.3 |
| R2 | 枚举名 `GateDecision` 已被 `internal/evaluation/domain/optimizer_gate.go:69`（`evalThresholds(...) domain.GateDecision`）占用 → 门禁枚举改名 **`GateAction`**（常量名保留 `GateNone/GateL2Escalate/GateRollbackManual/GateRollbackAuto`） | recon 冲突确认 |
| R3 | Decide 规则顺序：**rule5（flag/block → l2_escalate）必须在 rule6（none）之前**；先前的 rule6-early-none 是错误编码 | spec §2.3 |
| R4 | rule4「平台 Scope → 一律 rollback_manual；资源 → AutoRollbackAllowed ? auto : manual」通过 **scope 折叠进 policy 值**实现：平台 policy 恒 `AutoRollbackSupported=true + AutoRollbackAllowed=false`，`mapRollback` 区分；不新增 scope 分支 | spec §2.3 |
| R5 | `ReviewVerdict`/`RunComparison` 常量 T5 新建；P1 窗口证据只填计数，ReviewVerdict 恒空、ConfirmationRun 恒 nil（确认 run 对照为后续阶段） | spec §2.2 |
| R6 | 常量名：`GateObservationWindow`/`GateCooldown` 无 `_MS` 后缀（照抄 evaluation.go 现有时间型常量风格）；`RunRegressionDeltaThreshold=-0.05` | spec §4.2.4 |
| R7 | 窗口聚合直接返回 `GateEvidence`（Source 分类计数在 T13 的 QueryWindow DB 投影内实现），不单独设 ObservationSummary 传输结构 | spec §2.2 简化 |
| R8 | `ObservationSource` 分类（rule_block / behavior_anomaly / judge_flag）只出现在 T13 infra 查询注释与单测 stub；P1 不实现 DB 投影 | spec §5 T13 |
| R9 | T6 hook 插在 `observation_service.go` Process 的 `IncEvalObservation`（L116）之后、`escalateToReview`（L118）之前；`deps.Gate==nil` 跳过；决策动作不内联执行回滚 | spec §2.5 |
| R10 | T6 l2_escalate 路由只记台账 + 指标计数 + 告警日志，**不重复评审池入池**（`escalateToReview` 已在 L118 上游处理 flag/block 入池） | spec §2.5/§6 |
| R11 | 平台 eval_state 写回只实现 decision ∈ {rollback_manual, rollback_auto} → `eval_state="rollback_recommended"`（anomaly_flag/anomaly_block 等写回留给 T8 语义确定时） | spec §4.3.3 |
| R12 | O3 RiskTier high 名单（§7.2）：平台 8（agent.system_prompt、agent.compaction_model、evaluation.judge.model、evaluation.optimizer.model、agent.factcheck.judge.model、memory.embedding_model、memory.extraction_model、memory.reflection_model）+ 资源 2（agent.model、mcp.enabled_tools） | §7.2 已确认 |
| R13 | RiskTier 注册自动填充在 `Register` 的 GroupKey 推导块（registry.go L81-83）之后；显式声明与 Default 冲突时以声明为准（不变量测试要求两者一致） | recon |
| R14 | `GetVersion`/`UpdateEvalState` 以 groupKey+**version_seq**（int64）寻址；`port.PlatformVersion` 不加 eval_state 字段（UpdateEvalState 只校验存在性） | spec §4.2.3 |
| R15 | T7 override miss 语义是「沿用 IsCurrent 或空组（fail 容忍）」，**不回错误** → captureGroup 两遍扫描 | spec §4.3.4 |
| R16 | `CaptureInput.PlatformSeqOverrides map[string]int64`（groupKey→version_seq）；空 = 现 IsCurrent 语义 | spec §4.3.4 |

## Global Constraints（逐字）

- **禁止 main 直提交/直推送**：工作必须在 `feat/card-c-gate-p1`（worktree `/home/yang/go-projects/stratum-card-c-gate-p1`）。
- **多租户 DDL 唯一基线**：tenant-only 表 DDL 只落 `pkg/storage/postgres/tenant_schema.sql`（幂等，CREATE TABLE 一律带 `IF NOT EXISTS`）；新索引用 `CREATE INDEX IF NOT EXISTS`。禁止在 `pkg/migration/sql/` 复制 tenant DDL。
- **public 迁移**（`pkg/migration/sql/NNN_*.sql`）只操作 public schema（`public.platform_config_versions`），新列用 `ADD COLUMN IF NOT EXISTS`，一律带安全默认值；新文件命名 `044_platform_gate_eval_state.{up,down}.sql`（下一迁移号已确认 044）。
- **enum 扩展幂等**：`eval_review_items.trigger_reason` 加 `'behavior_anomaly'` 必须同时改 CREATE TABLE 约束体与 DROP CONSTRAINT IF EXISTS + ADD CONSTRAINT 幂等替换两处（历史租户不重建表）。
- **决策确定性**：路由/重试/门禁决策硬编码，禁 LLM。
- **常量进 `pkg/constants/evaluation.go`**：`GateObservationWindow=10*time.Minute`、`GateCooldown=10*time.Minute`、`GateRuleBlockRollbackMin=3`、`GateAnomalyRollbackMin=10`、`GateAnomalyAlertMin=3`、`GateAutoRollbackMaxPerDay=3`、`RunRegressionDeltaThreshold=-0.05`（时间型无 `_MS` 后缀）。禁止内联行为数字。
- **Go 质量门禁**：圈复杂度 ≤10、认知 ≤15、函数 ≤120 行、嵌套 ≤4、行宽 ≤120。写码前跑 `bash scripts/quality/risk-regression-guard.sh --explain`；PR 前 `make risk-guardrails`。
- **DDD 分层**：`domain` 仅依赖 stdlib + `pkg/constants`；`application` 不 import pgx/Redis/NATS/Gin；evaluation domain **自带 `Scope` 类型**（禁 import parameters domain 的 Scope）。跨 context 接口定义在消费方 `domain/port`，provider 由 infrastructure 实现，wiring 薄 ACL。
- **fail-open 纪律**：门禁 hook/escalate 失败只日志 + 指标，不阻断观测主流程；`GateServiceDeps` 各依赖 nil → 对应动作跳过。
- **tenant-scoped 存储**：访问 tenant 表的 repository 方法必须经 `execTenant`；public 存储用 schema-qualified 名。P1 的 `GateStore` 只定义 port 契约（DB 实现 T13），单测用 stub。
- **Commit 规范**：标题 `type(scope): description`（如 `feat(evaluation): …`）；body 带 `Co-Authored-By: Claude <noreply@anthropic.com>`。每任务一个独立 commit。
- **验证门槛**：本改动属 DB/能力改动 → PR 前系统验收由 `stratum-e2e-tester`（stratum-e2e-development skill）按 `.test/verification.yaml` 风险定级执行；E2E 仅无头浏览器；禁止绕过 skill 直跑 make 当系统验收。
- **行为证据**：外发前核对 spec §4.1/§4.2/§4.3 逐字；代码与测试冲突以产品意图（spec）为准。
- **模块路径** `github.com/byteBuilderX/stratum`；Go 以 `go.mod` 为准（1.25.12）。

## 任务依赖与次序

- T1（public 迁移 044）/ T2（tenant DDL）/ T3（RiskTier）/ T4（gate 平台键 + store）互不依赖，可并行；本计划按 T1→T7 顺序执行（每任务独立 commit，顺序无关紧要）。
- T5（gate 领域 `Decide`）依赖 T3（RiskTier 概念）与常量；T6（gate 应用/port/hook）依赖 T5+T2+T4；T7（快照 override）独立。
- P1 范围 = T1-T7（spec §5）。T8+（gate 台账 DB infra、策略 resolver 装配、确认 run 对照、auto 回滚执行器、告警 runbook 等）属后续卡，本计划不实现；被引用处给出 stub/预留说明。

---

## Task 1（T1）: public 迁移 044 —— platform_config_versions 门禁状态列

**Files:**

- Create: `pkg/migration/sql/044_platform_gate_eval_state.up.sql`
- Create: `pkg/migration/sql/044_platform_gate_eval_state.down.sql`
- Modify: `pkg/migration/migration_test.go`（append `TestPlatformGateEvalStateMigration`，仿既有 `TestModelEditableParamsMigration` L117-150 模式）

**Interfaces:**

- Produces: `pkg/migration/sql/044_*.sql`（下一迁移号 = 044；043 为现有最大号）。`public.platform_config_versions` 新增 3 列，下游 Task 4 的 repo `GetVersion`/`UpdateEvalState` 读写它们。

- [ ] **Step 1: 创建 up 迁移**

Create `pkg/migration/sql/044_platform_gate_eval_state.up.sql`：

```sql
-- 分层门禁 P1（spec §4.1.1）：平台参数版本携带评测门禁状态（unknown 未评估 /
-- rollback_recommended 待回滚评审 / …）。值域约束由应用层维护，本列 TEXT 放开。
ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS eval_state TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS eval_state_updated_at TIMESTAMPTZ;
ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS eval_state_updated_by TEXT NOT NULL DEFAULT '';
```

- [ ] **Step 2: 创建 down 迁移**

Create `pkg/migration/sql/044_platform_gate_eval_state.down.sql`：

```sql
ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS eval_state_updated_by;
ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS eval_state_updated_at;
ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS eval_state;
```

- [ ] **Step 3: 写失败测试**

在 `pkg/migration/migration_test.go` 末尾追加（先读 `TestModelEditableParamsMigration` 确认断言风格一致）：

```go
// TestPlatformGateEvalStateMigration 守护分层门禁 public 迁移 044：up 幂等加
// eval_state 三列，down 幂等删除（与 038 的 ADD/DROP IF EXISTS 模式一致）。
func TestPlatformGateEvalStateMigration(t *testing.T) {
	up, err := os.ReadFile("sql/044_platform_gate_eval_state.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"eval_state TEXT NOT NULL DEFAULT 'unknown'",
		"eval_state_updated_at TIMESTAMPTZ", "eval_state_updated_by TEXT NOT NULL DEFAULT ''"} {
		if !strings.Contains(string(up), "ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS "+col) {
			t.Fatalf("up migration missing idempotent ADD COLUMN IF NOT EXISTS %s", col)
		}
	}

	down, err := os.ReadFile("sql/044_platform_gate_eval_state.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"eval_state_updated_by", "eval_state_updated_at", "eval_state"} {
		if !strings.Contains(string(down), "ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS "+col) {
			t.Fatalf("down migration missing DROP COLUMN IF EXISTS %s", col)
		}
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `go test ./pkg/migration/ -run TestPlatformGateEvalStateMigration`
Expected: FAIL（`undefined: os`/`strings` 若该文件 import 缺失则按文件既有 import 补 `os`、`strings`；无该测试名时编译即失败，属预期）。若文件顶部无 os/strings import，按 `TestModelEditableParamsMigration` 所在文件现有 import 组对齐补全。

- [ ] **Step 5: 实现并跑绿**

完成 Step 1-2 后：
Run: `go test ./pkg/migration/ -run 'TestPlatformGateEvalStateMigration|TestMigrationVersionsAreUnique'`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/migration/sql/044_platform_gate_eval_state.up.sql pkg/migration/sql/044_platform_gate_eval_state.down.sql pkg/migration/migration_test.go
git commit -m "feat(evaluation): 卡 C 分层门禁 P1 平台版本 eval_state 迁移 044

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2（T2）: tenant DDL —— eval_gate_actions 台账 + verdict 时间索引 + behavior_anomaly 枚举

**Files:**

- Modify: `pkg/storage/postgres/tenant_schema.sql`（3 处：a) `idx_eval_observations_verdict_time` 追加在 `idx_eval_observations_trace` 后；b) `eval_gate_actions` 表 + 2 索引插在 eval_observations 索引之后、`-- 人工评审池` 注释之前；c) `eval_review_items.trigger_reason` 枚举加 `'behavior_anomaly'`，CREATE TABLE 约束体与 DROP/ADD 幂等块都要改）
- Modify: `pkg/storage/postgres/tenant_schema_safety_test.go`（append `TestTenantSchemaContainsGateActionDDL`）

**Interfaces:**

- Produces: tenant schema 新表 `eval_gate_actions`（列见下），被 Task 6 `GateStore`（T13 实现）落库。供 `eval_review_items` 的 trigger 枚举扩展值 `'behavior_anomaly'`（后续卡评审入池用）。

- [ ] **Step 1: 追加 verdict 时间索引 + eval_gate_actions 台账 DDL**

先读 `tenant_schema.sql` 定位锚点。在现有 `idx_eval_observations_trace` 定义（`CREATE INDEX IF NOT EXISTS idx_eval_observations_trace\n    ON eval_observations (trace_id);`）之后、`-- 人工评审池（P1c §6.6）…` 注释之前插入：

```sql
CREATE INDEX IF NOT EXISTS idx_eval_observations_verdict_time
    ON eval_observations (verdict, created_at DESC);

-- 分层门禁台账（spec §4.1.2）：每次 gate 评估决策落一行；target/evidence 为 JSONB
-- 结构化字段，由 Go json.Marshal 写入。人工审批走 agent_tool_approvals，不新增审批表，
-- approval_id 关联。action 记录决策对应的动作形态（如 rollback_recommended / escalate）。
CREATE TABLE IF NOT EXISTS eval_gate_actions (
    id             TEXT PRIMARY KEY,
    scope          TEXT NOT NULL CHECK (scope IN ('platform','resource')),
    target         JSONB NOT NULL,
    layer          TEXT NOT NULL,
    decision       TEXT NOT NULL,
    action         TEXT NOT NULL DEFAULT '',
    evidence       JSONB NOT NULL DEFAULT '{}'::jsonb,
    actor          TEXT NOT NULL DEFAULT '',
    approval_id    TEXT NOT NULL DEFAULT '',
    host_tenant_id TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_eval_gate_actions_target_time
    ON eval_gate_actions (scope, target, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_eval_gate_actions_decision
    ON eval_gate_actions (decision, created_at DESC);
```

- [ ] **Step 2: trigger_reason 枚举加 behavior_anomaly（两处）**

用 Edit 替换（该列表串在文件出现两处，分别处理）：

- 替换 1（CREATE TABLE 约束体）：
  old:

  ```
      trigger_reason TEXT NOT NULL CHECK (trigger_reason IN
          ('low_confidence','dimension_split','judge_rule_conflict','needs_review','process_output_conflict')),
  ```

  new:

  ```
      trigger_reason TEXT NOT NULL CHECK (trigger_reason IN
          ('low_confidence','dimension_split','judge_rule_conflict','needs_review','process_output_conflict','behavior_anomaly')),
  ```

- 替换 2（DROP/ADD 幂等块，升级历史租户）：
  old:

  ```
  ALTER TABLE eval_review_items ADD CONSTRAINT eval_review_items_trigger_reason_check
      CHECK (trigger_reason IN ('low_confidence','dimension_split','judge_rule_conflict','needs_review','process_output_conflict'));
  ```

  new:

  ```
  ALTER TABLE eval_review_items ADD CONSTRAINT eval_review_items_trigger_reason_check
      CHECK (trigger_reason IN ('low_confidence','dimension_split','judge_rule_conflict','needs_review','process_output_conflict','behavior_anomaly'));
  ```

  并同步更新其上方注释，说明 `'behavior_anomaly'` 由分层门禁判异信号引入（行为异常/judge 跌阈且需人工复核时入池）。

- [ ] **Step 3: 写失败测试**

在 `pkg/storage/postgres/tenant_schema_safety_test.go` 末尾追加：

```go
// TestTenantSchemaContainsGateActionDDL 守护分层门禁台账 DDL（spec §4.1.2）：
// eval_gate_actions 幂等创建 + verdict 时间索引 + 门禁双索引 + behavior_anomaly 枚举升级。
func TestTenantSchemaContainsGateActionDDL(t *testing.T) {
	ddl, err := os.ReadFile("tenant_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ddl)

	start := strings.Index(text, "CREATE TABLE IF NOT EXISTS eval_gate_actions (")
	if start == -1 {
		t.Fatal("tenant schema missing eval_gate_actions")
	}
	if strings.Contains(text, "CREATE TABLE eval_gate_actions") {
		t.Fatal("tenant schema has non-idempotent eval_gate_actions DDL")
	}
	end := strings.Index(text[start:], ");")
	if end == -1 {
		t.Fatal("tenant schema has unterminated eval_gate_actions DDL")
	}
	body := strings.ToLower(text[start : start+end])
	for _, col := range []string{
		"scope text not null check (scope in ('platform','resource'))",
		"target jsonb not null", "layer text not null", "decision text not null",
		"evidence jsonb not null", "host_tenant_id text not null",
	} {
		if !strings.Contains(body, col) {
			t.Fatalf("eval_gate_actions missing %s", col)
		}
	}

	for _, idx := range []string{
		"idx_eval_observations_verdict_time", "idx_eval_gate_actions_target_time",
		"idx_eval_gate_actions_decision",
	} {
		if !strings.Contains(text, "CREATE INDEX IF NOT EXISTS "+idx) {
			t.Fatalf("tenant schema missing idempotent index %s", idx)
		}
	}
	// behavior_anomaly 必须出现在 CREATE 约束体与 DROP/ADD 升级块（历史租户）。
	if strings.Count(text, "'behavior_anomaly'") < 2 {
		t.Fatal("behavior_anomaly must appear in both CREATE constraint and DROP/ADD upgrade")
	}
	if !strings.Contains(text, "DROP CONSTRAINT IF EXISTS eval_review_items_trigger_reason_check") {
		t.Fatal("tenant schema missing idempotent trigger_reason DROP/ADD upgrade")
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `go test ./pkg/storage/postgres/ -run TestTenantSchemaContainsGateActionDDL`
Expected: FAIL（缺 DDL/索引/枚举），实现 Step 1-2 后 PASS。

- [ ] **Step 5: 全量仓库回归（防止 tenant_schema 破坏其他 guard 测试）**

Run: `go test ./pkg/storage/postgres/`
Expected: PASS（既有 TestTenantSchema* 全部绿；若某正则被新增列触发而失败，先读失败断言再修，禁止绕过）。

- [ ] **Step 6: Commit**

```bash
git add pkg/storage/postgres/tenant_schema.sql pkg/storage/postgres/tenant_schema_safety_test.go
git commit -m "feat(evaluation): 卡 C 分层门禁 P1 tenant DDL 台账表 + verdict 索引 + behavior_anomaly

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3（T3）: parameters registry —— RiskTier 分级与自动填充

**Files:**

- Modify: `internal/parameters/domain/parameter.go`（`ParameterDefinition` 加 `RiskTier` 字段，插在 `Sensitive bool` 字段之后）
- Create: `internal/parameters/domain/risk_tier.go`
- Modify: `internal/parameters/domain/registry.go`（`Register` 内 GroupKey 推导块之后自动填充 RiskTier）
- Modify: `internal/parameters/domain/registry_test.go`（append 不变量测试）

**Interfaces:**

- Consumes: `ParameterDefinition`（parameter.go L68-90）、`Register`（registry.go L75-98）。
- Produces: `type RiskTier string` + 常量 `RiskTierHigh/Medium/Low`；`func DefaultRiskTierForKey(scope Scope, key string) RiskTier`；`ParameterDefinition.RiskTier` 自动默认。Task 5/6 的 gate 决策后续卡按 RiskTier 区分 escalation/回滚路径。

- [ ] **Step 1: ParameterDefinition 加字段**

读 `internal/parameters/domain/parameter.go` 定位 `Sensitive bool` 字段，在其后插入：

```go
	RiskTier RiskTier `json:"risk_tier,omitempty"` // O3 风险分级 high/medium/low（空 = 注册时按 DefaultRiskTierForKey 自动填充）
```

- [ ] **Step 2: 新建 risk_tier.go**

Create `internal/parameters/domain/risk_tier.go`：

```go
package domain

import "strings"

// RiskTier 是参数变更的 O3 风险分级（spec §4.2.1）：门禁/审批按它决定回滚与升级路径。
type RiskTier string

const (
	RiskTierHigh   RiskTier = "high"
	RiskTierMedium RiskTier = "medium"
	RiskTierLow    RiskTier = "low"
)

// riskTierHighKeys 是 O3 判定的 high 风险键全集：平台 8 + 资源 2（规格 §7.2 已确认）。
// 平台键影响全租户评测/judge/记忆质量；资源键 agent.model 更换推理模型、mcp.enabled_tools
// 变更能力面。
var riskTierHighKeys = map[string]struct{}{
	"agent.system_prompt": {}, "agent.compaction_model": {},
	"evaluation.judge.model": {}, "evaluation.optimizer.model": {}, "agent.factcheck.judge.model": {},
	"memory.embedding_model": {}, "memory.extraction_model": {}, "memory.reflection_model": {},
	"agent.model": {}, "mcp.enabled_tools": {},
}

// riskTierResourceMediumKeys 是资源 scope medium 键集（平台 medium 后缀规则不覆盖的资源键）。
var riskTierResourceMediumKeys = map[string]struct{}{
	"rag.top_k": {}, "rag.score_threshold": {}, "rag.reranking": {}, "rag.query_rewrite": {},
	"agent.reasoning_effort": {}, "agent.max_iterations": {}, "memory.long_term_top_k": {},
}

// riskTierPlatformSuffixes 平台 medium 后缀：模型/提示词/温度键。
var riskTierPlatformSuffixes = []string{"_model", "_prompt", "_temperature"}

// DefaultRiskTierForKey 返回键的默认风险分级。判定顺序：high 全集 → 平台 medium
// （model/prompt/temperature 后缀，已 high 的键先行短路）→ 资源 medium 集 → low。
// scope 参与判定保证互斥：agent.temperature（资源采样键）不落入平台 medium 后缀规则。
func DefaultRiskTierForKey(scope Scope, key string) RiskTier {
	if _, ok := riskTierHighKeys[key]; ok {
		return RiskTierHigh
	}
	if scope == ScopePlatform {
		for _, suffix := range riskTierPlatformSuffixes {
			if strings.HasSuffix(key, suffix) {
				return RiskTierMedium
			}
		}
		return RiskTierLow
	}
	if _, ok := riskTierResourceMediumKeys[key]; ok {
		return RiskTierMedium
	}
	return RiskTierLow
}
```

- [ ] **Step 3: Register 自动填充**

读 `internal/parameters/domain/registry.go` `Register`（L75-98）。把 GroupKey 推导块替换为（在原块后加 RiskTier 自动填充；保持 `def` 局部变量链式赋值）：

old:

```go
	// 平台参数自动推导分组归属（显式 GroupKey 可覆盖默认）；单一归属保证
	// 每个平台键恰好属于一组。资源参数无分组，不参与平台快照。
	if def.Scope == ScopePlatform && def.GroupKey == "" {
		def.GroupKey = GroupForKey(def.Key)
	}
```

new:

```go
	// 平台参数自动推导分组归属（显式 GroupKey 可覆盖默认）；单一归属保证
	// 每个平台键恰好属于一组。资源参数无分组，不参与平台快照。
	if def.Scope == ScopePlatform && def.GroupKey == "" {
		def.GroupKey = GroupForKey(def.Key)
	}
	// 风险分级自动默认（O3，spec §4.2.1）：显式声明保留，空值按键名/scope 自动填充。
	if def.RiskTier == "" {
		def.RiskTier = DefaultRiskTierForKey(def.Scope, def.Key)
	}
```

- [ ] **Step 4: 写失败测试**

读 `internal/parameters/domain/registry_test.go`（确认 package 名与既有 helper），末尾追加：

```go
// TestRegistryEveryKeyHasRiskTier 守护 O3 不变量：每个注册键都必须有非空 RiskTier，
// 且与 DefaultRiskTierForKey 一致（显式声明不允许漂移出分类表）。
func TestRegistryEveryKeyHasRiskTier(t *testing.T) {
	r := NewParametersRegistry()
	for _, def := range r.Schema() {
		switch def.RiskTier {
		case RiskTierHigh, RiskTierMedium, RiskTierLow:
		default:
			t.Fatalf("key %s risk tier %q must be one of high/medium/low", def.Key, def.RiskTier)
		}
		if want := DefaultRiskTierForKey(def.Scope, def.Key); def.RiskTier != want {
			t.Fatalf("key %s risk tier %q != DefaultRiskTierForKey %q", def.Key, def.RiskTier, want)
		}
	}
}

// TestRegistryRiskTierClassifiesGateRelevantKeys 守护 O3 high 名单与关键 medium/low 归类。
func TestRegistryRiskTierClassifiesGateRelevantKeys(t *testing.T) {
	r := NewParametersRegistry()
	cases := []struct {
		key  string
		want RiskTier
	}{
		{"agent.model", RiskTierHigh},         // 资源 high（§7.2）
		{"mcp.enabled_tools", RiskTierHigh},   // 资源 high（§7.2）
		{"agent.system_prompt", RiskTierHigh}, // 平台 high（§7.2）
		{"evaluation.judge.model", RiskTierHigh},
		{"evaluation.optimizer.model", RiskTierHigh},
		{"agent.factcheck.judge.model", RiskTierHigh},
		{"memory.embedding_model", RiskTierHigh},
		{"memory.extraction_model", RiskTierHigh},
		{"memory.reflection_model", RiskTierHigh},
		{"rag.top_k", RiskTierMedium},                 // 资源 medium
		{"agent.reasoning_effort", RiskTierMedium},    // 资源 medium
		{"agent.compaction_temperature", RiskTierMedium}, // 平台 medium（_temperature 后缀）
		{"agent.factcheck.judge.prompt", RiskTierMedium}, // 平台 medium（_prompt 后缀）
		{"agent.temperature", RiskTierLow},               // 资源采样键不落平台后缀规则
		{"evaluation.judge.enabled", RiskTierLow},        // 开关默认 low
	}
	for _, tc := range cases {
		def, ok := r.Get(tc.key)
		if !ok {
			t.Fatalf("key %s not registered", tc.key)
		}
		if def.RiskTier != tc.want {
			t.Fatalf("key %s risk tier = %q, want %q", tc.key, def.RiskTier, tc.want)
		}
	}
}
```

- [ ] **Step 5: 跑测试确认失败**

Run: `go test ./internal/parameters/domain/ -run 'RiskTier'`
Expected: FAIL（Step 1-3 未做），实现后 PASS。

- [ ] **Step 6: 全量参数域回归 + Commit**

Run: `go test ./internal/parameters/...`
Expected: PASS

```bash
git add internal/parameters/domain/parameter.go internal/parameters/domain/risk_tier.go internal/parameters/domain/registry.go internal/parameters/domain/registry_test.go
git commit -m "feat(parameters): 卡 C 分层门禁 P1 RiskTier O3 分级与注册自动填充

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4（T4）: gate 平台参数键 + PlatformStore 新方法

**Files:**

- Modify: `internal/parameters/domain/registry.go`（NewParametersRegistry 加 `r.registerGateParams()`；文件尾 `registerRuleGuardParams` 后加 `registerGateParams()`）
- Modify: `internal/parameters/domain/port/store.go`（`PlatformStore` 加 `GetVersion`/`UpdateEvalState`）
- Modify: `internal/parameters/infrastructure/persistence/platform_repo.go`（实现两方法，仿 GetSnapshot/Publish 模式）
- Modify: `internal/parameters/application/service.go`（薄转发两方法，仿 Publish/Rollback）
- Modify: `internal/parameters/application/application_test.go`（memStore/errStore 补齐两方法 + 用例；PlatformStore 接口扩展后编译强制）

**Interfaces:**

- Consumes: `PlatformStore`（store.go L45-77）、`PlatformRepository{pool}` 的 `GetSnapshot`/`Publish` 模式、Service 的 `Versions` 区（service.go L186-207）。
- Produces: 平台键 `evaluation.gate.enabled`（bool）/`evaluation.gate.auto_rollback_resources`（bool）；`port.PlatformStore.GetVersion(ctx, groupKey string, versionSeq int64) (PlatformVersion, error)`、`UpdateEvalState(ctx, groupKey string, versionSeq int64, state, actor string) error`；repo 方法：命中 0 行 → `domain.ErrVersionNotFound`；service 薄转发（actor 空 → `"api"`）。供 Task 6 `PlatformGateOps` 与 Task 7 override。

- [ ] **Step 1: 新增 registerGateParams 并挂到构造链**

`registry.go` `NewParametersRegistry` 内 `r.registerRuleGuardParams()` 之后加 `r.registerGateParams()`。文件尾（`registerRuleGuardParams` 之后）加：

```go
// registerGateParams 是分层门禁（spec §2/§4.2.2）的平台级参数。enabled 默认 false：
// 平台未显式开启时观测链路不评估门禁（fail open 于门禁层，开启后规则才生效）。
// auto_rollback_resources 只影响资源 scope 决策（平台 scope 恒 rollback_manual）。
// 仅注册不播种：PlatformValues 对快照缺失 key 回退 registry default（false）。
func (r *ParametersRegistry) registerGateParams() {
	_ = r.Register(ParameterDefinition{
		Key: "evaluation.gate.enabled", Scope: ScopePlatform, Category: "evaluation",
		DisplayName: "启用分层门禁", Description: "运行态评测门禁评估与回滚建议(默认关)",
		ValueType: TypeBool, Default: false,
		VisualHint:  VisualHint{Control: ControlToggle},
		Optimizable: true,
	})
	_ = r.Register(ParameterDefinition{
		Key: "evaluation.gate.auto_rollback_resources", Scope: ScopePlatform, Category: "evaluation",
		DisplayName: "资源自动回滚", Description: "资源 scope 劣化允许自动回滚(平台 scope 恒人工)",
		ValueType: TypeBool, Default: false,
		VisualHint:  VisualHint{Control: ControlToggle},
		Optimizable: true,
	})
}
```

- [ ] **Step 2: PlatformStore 接口加两方法**

`store.go` `ListVersions` 之后、接口闭合 `}` 前加：

```go
	// GetVersion returns one historical published version by group+version_seq
	// (the gate writes eval_state onto a version the observation anchored to).
	// Returns domain.ErrVersionNotFound when the version does not exist.
	GetVersion(ctx context.Context, groupKey string, versionSeq int64) (PlatformVersion, error)
	// UpdateEvalState records the gate's evaluation state on a version
	// (e.g. "rollback_recommended"). Returns domain.ErrVersionNotFound when the
	// version does not exist. eval_state_updated_at/by are stamped server-side.
	UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error
```

- [ ] **Step 3: repo 实现两方法**

读 `platform_repo.go`（GetSnapshot L109-133 用 schema-qualified `public.` 名 + pgx.ErrNoRows → 空值；Publish 用 RowsAffected==0 → ErrVersionNotFound）。在其后加：

```go
// GetVersion 读一个历史版本的元数据（门禁写 eval_state 前校验存在性 / 取 seq 用）。
func (r *PlatformRepository) GetVersion(ctx context.Context, groupKey string, versionSeq int64) (port.PlatformVersion, error) {
	const q = `SELECT id, group_key, version_seq, status, snapshot
		FROM public.platform_config_versions WHERE group_key = $1 AND version_seq = $2`
	var (
		v       port.PlatformVersion
		snapshot []byte
	)
	if err := r.pool.QueryRow(ctx, q, groupKey, versionSeq).
		Scan(&v.ID, &v.GroupKey, &v.VersionSeq, &v.Status, &snapshot); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PlatformVersion{}, domain.ErrVersionNotFound
		}
		return port.PlatformVersion{}, fmt.Errorf("get platform version %s seq %d: %w", groupKey, versionSeq, err)
	}
	if len(snapshot) > 0 {
		if err := json.Unmarshal(snapshot, &v.Snapshot); err != nil {
			return port.PlatformVersion{}, fmt.Errorf("get platform version %s seq %d: decode snapshot: %w", groupKey, versionSeq, err)
		}
	}
	return v, nil
}

// UpdateEvalState 写门禁状态（分层门禁 P1）：命中 0 行说明版本不存在 → ErrVersionNotFound。
func (r *PlatformRepository) UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error {
	ct, err := r.pool.Exec(ctx,
		`UPDATE public.platform_config_versions
		 SET eval_state = $3, eval_state_updated_at = NOW(), eval_state_updated_by = $4
		 WHERE group_key = $1 AND version_seq = $2`,
		groupKey, versionSeq, state, actor)
	if err != nil {
		return fmt.Errorf("update platform version %s seq %d eval_state: %w", groupKey, versionSeq, err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrVersionNotFound
	}
	return nil
}
```

若文件顶部 import 缺 `encoding/json`/`errors`/`pgx`，按文件现有 import 分组补全（stdlib → third-party → internal）。

- [ ] **Step 4: Service 薄转发**

`service.go` `Versions` 方法后加：

```go
// GetVersion 转发：按 group+version_seq 读历史版本元数据（门禁/对照链路用）。
func (s *Service) GetVersion(ctx context.Context, groupKey string, versionSeq int64) (port.PlatformVersion, error) {
	return s.store.GetVersion(ctx, groupKey, versionSeq)
}

// UpdateEvalState 转发：给平台版本写门禁状态（actor 空默认 "api"，与 Publish/Rollback 一致）。
func (s *Service) UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error {
	if actor == "" {
		actor = "api"
	}
	return s.store.UpdateEvalState(ctx, groupKey, versionSeq, state, actor)
}
```

- [ ] **Step 5: 补齐测试 stub + 用例**

`PlatformStore` 是 parameters 应用测试用接口：先 `grep -rn "GetSnapshot(ctx" internal/parameters --include=*.go` 找全部实现（memStore/errStore 等），每个为接口加两 stub 方法（errStore 两方法返回错误），例如 memStore：

```go
func (s *memStore) GetVersion(ctx context.Context, groupKey string, versionSeq int64) (port.PlatformVersion, error) {
	for _, v := range s.versions[groupKey] {
		if int64(v.VersionSeq) == versionSeq {
			return v, nil
		}
	}
	return port.PlatformVersion{}, domain.ErrVersionNotFound
}

func (s *memStore) UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error {
	for i := range s.versions[groupKey] {
		if int64(s.versions[groupKey][i].VersionSeq) == versionSeq {
			s.versions[groupKey][i].Snapshot["eval_state"] = json.RawMessage(`"`+state+`"`)
			return nil
		}
	}
	return domain.ErrVersionNotFound
}
```

（errStore 对应两方法直接 `return ..., errors.New("store failed")`；字段名/容器以文件实际为准。）再在 `application_test.go`（或既有 service 测试文件）加用例：GetVersion 命中返回 seq；UpdateEvalState 命中修改、未知 seq → `ErrVersionNotFound`（用 `errors.Is`）。

- [ ] **Step 6: 跑测试 + 全仓编译**

Run: `go test ./internal/parameters/...`
Expected: PASS
Run: `go build ./...`
Expected: 无错误（若 wiring/其他仓库还有 PlatformStore 桩实现编译失败，先 `grep -rn "PlatformStore"` 定位补齐）。

- [ ] **Step 7: Commit**

```bash
git add internal/parameters/domain/registry.go internal/parameters/domain/port/store.go internal/parameters/infrastructure/persistence/platform_repo.go internal/parameters/application/service.go internal/parameters/application/application_test.go
git commit -m "feat(parameters): 卡 C 分层门禁 P1 gate 平台键 + PlatformStore GetVersion/UpdateEvalState

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 5（T5）: gate 领域 —— Scope/GateAction/Decide 规则阶梯 + 常量

**Files:**

- Create: `internal/evaluation/domain/gate.go`
- Create: `internal/evaluation/domain/gate_test.go`
- Modify: `pkg/constants/evaluation.go`（文件尾追加 Gate 常量块，文件已有 `import "time"`）

**Interfaces:**

- Consumes: `pkg/constants`（阈值常量）。命名冲突裁决 R2：本文件不叫 `GateDecision`（已被 optimizer_gate.go 占用）。
- Produces（下游 Task 6 与未来卡消费）：
  - `type Scope string` + `ScopePlatform="platform"` / `ScopeResource="resource"`
  - `type GateTarget struct{ Scope; GroupKey, Kind, ResourceID, RevisionID string; VersionSeq int64 }` + `func (t GateTarget) Key() string`
  - `type GateAction string` + `GateNone/GateL2Escalate/GateRollbackManual/GateRollbackAuto`
  - `type ReviewVerdict string`（空 = none）+ `ReviewVerdictConfirmRegression/ReviewVerdictConfirmRollback`
  - `type RunComparison struct{ Regressed bool; BaselineSeq, ConfirmedSeq int64; DimensionDeltas map[string]float64 }`
  - `type GateEvidence struct{ RuleBlockCount, AnomalyCount, JudgeFlagCount int; ReviewVerdict ReviewVerdict; ConfirmationRun *RunComparison }`
  - `type GatePolicy struct{ Scope Scope; RollbackSupported, AutoRollbackAllowed bool }`
  - `func Decide(policy GatePolicy, ev GateEvidence) GateAction`、`func mapRollback(policy GatePolicy) GateAction`、`func runRegressed(ev GateEvidence) bool`

- [ ] **Step 1: pkg/constants/evaluation.go 追加 Gate 常量块**

读该文件（135 行，顶部已 `import "time"`），在文件尾（现有 `StepJudge` 区后）追加新 const 块：

```go
// 分层门禁阈值（spec §4.2.4）。时间型常量沿用本文件不带 _MS 后缀的风格。
// GateRuleBlockRollbackMin 规则阻断回滚门槛、GateAnomalyRollbackMin 行为异常回滚门槛、
// GateAnomalyAlertMin 告警门槛、GateObservationWindow 证据窗口、GateCooldown 决策冷却、
// GateAutoRollbackMaxPerDay 自动回滚日限、RunRegressionDeltaThreshold 对照 run 劣化阈值。
const (
	GateObservationWindow       = 10 * time.Minute
	GateCooldown                = 10 * time.Minute
	GateRuleBlockRollbackMin    = 3
	GateAnomalyRollbackMin      = 10
	GateAnomalyAlertMin         = 3
	GateAutoRollbackMaxPerDay   = 3
	RunRegressionDeltaThreshold = -0.05
)
```

- [ ] **Step 2: 写失败测试**

Create `internal/evaluation/domain/gate_test.go`：

```go
package domain

import (
	"testing"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// gateTestCase 折叠一个 Decide 输入与期望动作（表驱动）。
type gateTestCase struct {
	name   string
	policy GatePolicy
	ev     GateEvidence
	want   GateAction
}

// confirmReviewVerdict 生成一个已确认回滚的人工结论。
func confirmReviewVerdict() ReviewVerdict { return ReviewVerdictConfirmRollback }

func TestDecideRollbackCandidatesMapToActions(t *testing.T) {
	// 平台 scope 恒 manual：AutoRollbackAllowed=false 已由 policy 折叠（裁决 R4）。
	platform := GatePolicy{Scope: ScopePlatform, RollbackSupported: true, AutoRollbackAllowed: false}
	resourceAuto := GatePolicy{Scope: ScopeResource, RollbackSupported: true, AutoRollbackAllowed: true}
	resourceManual := GatePolicy{Scope: ScopeResource, RollbackSupported: true, AutoRollbackAllowed: false}
	noRollback := GatePolicy{Scope: ScopeResource, RollbackSupported: false, AutoRollbackAllowed: false}

	cases := []gateTestCase{
		{
			name: "rule1 human confirm rollback -> platform manual",
			policy: platform,
			ev: GateEvidence{ReviewVerdict: confirmReviewVerdict()},
			want: GateRollbackManual,
		},
		{
			name: "rule2 rule blocks >= min -> resource manual",
			policy: resourceManual,
			ev: GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin},
			want: GateRollbackManual,
		},
		{
			name: "rule2 rule blocks >= min -> resource auto",
			policy: resourceAuto,
			ev: GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin + 1},
			want: GateRollbackAuto,
		},
		{
			name: "rollback unsupported -> l2 escalate even when auto allowed absent",
			policy: noRollback,
			ev: GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin},
			want: GateL2Escalate,
		},
		{
			name: "rule3 anomalies >= rollback min and confirmation regressed -> resource auto",
			policy: resourceAuto,
			ev: GateEvidence{
				AnomalyCount: constants.GateAnomalyRollbackMin,
				ConfirmationRun: &RunComparison{Regressed: true},
			},
			want: GateRollbackAuto,
		},
		{
			name: "rule3 anomalies high but no confirmation run -> escalate not rollback",
			policy: resourceAuto,
			ev: GateEvidence{AnomalyCount: constants.GateAnomalyRollbackMin + 2},
			want: GateL2Escalate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.policy, tc.ev); got != tc.want {
				t.Fatalf("Decide() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideRuleOrderingFlagBeforeNone(t *testing.T) {
	// 裁决 R3：rule5（flag/block → l2_escalate）必须先于 rule6（none）。
	// 单条规则阻断（低于回滚门槛 3）仍须 escalate，不能因"低计数"被判 none。
	platform := GatePolicy{Scope: ScopePlatform, RollbackSupported: true, AutoRollbackAllowed: false}
	cases := []gateTestCase{
		{
			name:   "single rule block below rollback min -> escalate",
			policy: platform,
			ev:     GateEvidence{RuleBlockCount: 1},
			want:   GateL2Escalate,
		},
		{
			name:   "single judge flag -> escalate",
			policy: platform,
			ev:     GateEvidence{JudgeFlagCount: 1},
			want:   GateL2Escalate,
		},
		{
			name:   "clean window -> none",
			policy: platform,
			ev:     GateEvidence{},
			want:   GateNone,
		},
		{
			name: "run regressed without flag/block -> escalate (rule6 none guard)",
			policy: platform,
			ev: GateEvidence{
				AnomalyCount:   1,
				ConfirmationRun: &RunComparison{Regressed: true},
			},
			want: GateL2Escalate,
		},
		{
			name: "anomalies below alert with regression -> escalate not none",
			policy: platform,
			ev: GateEvidence{
				ConfirmationRun: &RunComparison{Regressed: true},
			},
			want: GateL2Escalate,
		},
		{
			name:   "anomalies at alert floor without flags -> escalate",
			policy: platform,
			ev:     GateEvidence{AnomalyCount: constants.GateAnomalyAlertMin},
			want:   GateL2Escalate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.policy, tc.ev); got != tc.want {
				t.Fatalf("Decide() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMapRollback(t *testing.T) {
	cases := []struct {
		name   string
		policy GatePolicy
		want   GateAction
	}{
		{"unsupported -> escalate", GatePolicy{Scope: ScopeResource, RollbackSupported: false}, GateL2Escalate},
		{"supported + auto -> auto", GatePolicy{Scope: ScopeResource, RollbackSupported: true, AutoRollbackAllowed: true}, GateRollbackAuto},
		{"supported + manual -> manual", GatePolicy{Scope: ScopePlatform, RollbackSupported: true}, GateRollbackManual},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapRollback(tc.policy); got != tc.want {
				t.Fatalf("mapRollback() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunRegressed(t *testing.T) {
	if runRegressed(GateEvidence{}) {
		t.Fatal("runRegressed on empty evidence must be false")
	}
	if runRegressed(GateEvidence{ConfirmationRun: &RunComparison{Regressed: false}}) {
		t.Fatal("runRegressed with Regressed=false must be false")
	}
	if !runRegressed(GateEvidence{ConfirmationRun: &RunComparison{Regressed: true}}) {
		t.Fatal("runRegressed with Regressed=true must be true")
	}
}

func TestGateActionValuesMatchLedgerDecisionText(t *testing.T) {
	// 台账 decision 列直接存 GateAction 文本（eval_gate_actions.decision）。
	for _, a := range []GateAction{GateNone, GateL2Escalate, GateRollbackManual, GateRollbackAuto} {
		if a == "" {
			t.Fatal("GateAction must not be empty string")
		}
	}
}

func TestRunRegressionDeltaThresholdIsNegative(t *testing.T) {
	if constants.RunRegressionDeltaThreshold >= 0 {
		t.Fatal("RunRegressionDeltaThreshold must be negative (dimension delta below baseline)")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/evaluation/domain/ -run 'Decide|MapRollback|RunRegressed|GateAction|RunRegression'`
Expected: 编译失败（gate.go 不存在）→ 属预期。

- [ ] **Step 4: 实现 domain/gate.go**

Create `internal/evaluation/domain/gate.go`：

```go
package domain

import (
	"fmt"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// Scope 表示分层门禁作用的对象层级（spec §2.2）。evaluation domain 自带类型，
// 禁止 import parameters domain 的 Scope。
type Scope string

const (
	ScopePlatform Scope = "platform" // 平台级参数（judge/observe/ruleguard/gate…）
	ScopeResource Scope = "resource" // 被测资源参数（租户/资源级 agent 等）
)

// GateTarget 标识一次门禁评估的目标参数集（平台键组或被测资源）。
// 平台：GroupKey（evaluation/agent/memory/trace）+ 生效 VersionSeq；
// 资源：Kind + ResourceID + RevisionID（obs.Param.Resource.Version 映射，裁决 R15 关联）。
type GateTarget struct {
	Scope      Scope  `json:"scope"`
	GroupKey   string `json:"group_key,omitempty"` // 平台分组；资源空
	Kind       string `json:"kind,omitempty"`       // 资源 kind（agent/skill/…）；平台空
	ResourceID string `json:"resource_id,omitempty"`
	RevisionID string `json:"revision_id,omitempty"`
	VersionSeq int64  `json:"version_seq,omitempty"` // 平台版本 seq / 资源对照锚点
}

// Key 返回目标的稳定去重键（冷却/去重用）。
func (t GateTarget) Key() string {
	if t.Scope == ScopePlatform {
		return "platform:" + t.GroupKey
	}
	return fmt.Sprintf("resource:%s:%s:%s", t.Kind, t.ResourceID, t.RevisionID)
}

// GateAction 是一次门禁评估的决策动作。常量名沿用 spec GateDecision 的值，
// 类型名改 GateAction 避免与 optimizer_gate.go 的 domain.GateDecision 冲突（裁决 R2）。
// 值即台账 decision 列文本。
type GateAction string

const (
	GateNone           GateAction = "none"
	GateL2Escalate     GateAction = "l2_escalate"
	GateRollbackManual GateAction = "rollback_manual"
	GateRollbackAuto   GateAction = "rollback_auto"
)

// ReviewVerdict 是人工评审/门禁复核结论（spec §2.2）。空值 = 无人工确认。
type ReviewVerdict string

const (
	ReviewVerdictConfirmRegression ReviewVerdict = "confirm_regression"
	ReviewVerdictConfirmRollback   ReviewVerdict = "confirm_rollback"
)

// RunComparison 描述确认 run 相对基线 run 的对照结论（T8+ 装配确认 run 后填充；
// P1 恒 nil）。
type RunComparison struct {
	Regressed       bool // 确认 run 维度劣化超过 RunRegressionDeltaThreshold
	BaselineSeq     int64
	ConfirmedSeq    int64
	DimensionDeltas map[string]float64
}

// GateEvidence 是 Decide 的输入证据（spec §2.2）：观察窗口聚合计数 + 人工/对照判定。
// 窗口计数来自 GateStore.QueryWindow（T13 按 ObservationSource 分类）；
// ReviewVerdict/ConfirmationRun 由后续阶段填充，P1 恒零/nil。
type GateEvidence struct {
	RuleBlockCount  int // 规则阻断（rule_block）观察数
	AnomalyCount    int // 行为异常（behavior_anomaly）观察数
	JudgeFlagCount  int // judge 跌阈 flag 观察数
	ReviewVerdict   ReviewVerdict
	ConfirmationRun *RunComparison
}

// GatePolicy 描述一次评估的生效策略。scope 折叠进值（裁决 R4）：平台恒
// RollbackSupported=true + AutoRollbackAllowed=false；资源按回滚能力与 auto 开关。
// Decide/mapRollback 不再重复判断 scope。
type GatePolicy struct {
	Scope               Scope
	RollbackSupported   bool
	AutoRollbackAllowed bool
}

// Decide 按规格 §2.3 规则阶梯逐条判定（硬编码、确定性，禁止 LLM）。
// 规则序不可调换：rule5（flag/block → l2_escalate）必须晚于回滚候选判定、先于
// rule6 none（早期 rule6 前置会让低计数 flag/block 被错误判 none，裁决 R3）。
func Decide(policy GatePolicy, ev GateEvidence) GateAction {
	// 规则 1：人工评审确认劣化/回滚 → 回滚候选。
	switch ev.ReviewVerdict {
	case ReviewVerdictConfirmRegression, ReviewVerdictConfirmRollback:
		return mapRollback(policy)
	}
	// 规则 2：规则阻断数 ≥ 阈值 → 回滚候选（平台仍 manual，由 mapRollback 折叠）。
	if ev.RuleBlockCount >= constants.GateRuleBlockRollbackMin {
		return mapRollback(policy)
	}
	// 规则 3：行为异常数 ≥ 阈值 且 确认 run 劣化超阈值 → 回滚候选。
	if ev.AnomalyCount >= constants.GateAnomalyRollbackMin && runRegressed(ev) {
		return mapRollback(policy)
	}
	// 规则 5（先于规则 6）：未达回滚候选但有 flag/block → l2_escalate。
	if ev.JudgeFlagCount > 0 || ev.RuleBlockCount > 0 {
		return GateL2Escalate
	}
	// 规则 6：异常低于告警阈值 且 无 run 级劣化 → none。
	if ev.AnomalyCount < constants.GateAnomalyAlertMin && !runRegressed(ev) {
		return GateNone
	}
	// 兜底：run 劣化或异常 ≥ 告警阈值但无 flag/block → l2_escalate（安全偏向人工）。
	return GateL2Escalate
}

// mapRollback 把回滚候选映射为动作：不支持回滚 → l2_escalate；
// 支持且允许自动 → rollback_auto；否则 rollback_manual（含平台 scope）。
func mapRollback(policy GatePolicy) GateAction {
	switch {
	case !policy.RollbackSupported:
		return GateL2Escalate
	case policy.AutoRollbackAllowed:
		return GateRollbackAuto
	default:
		return GateRollbackManual
	}
}

// runRegressed 报告确认 run 是否劣化：仅 ConfirmationRun 存在且 Regressed 为真。
func runRegressed(ev GateEvidence) bool {
	return ev.ConfirmationRun != nil && ev.ConfirmationRun.Regressed
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/evaluation/domain/ -run 'Decide|MapRollback|RunRegressed|GateAction|RunRegression'`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/evaluation/domain/gate.go internal/evaluation/domain/gate_test.go pkg/constants/evaluation.go
git commit -m "feat(evaluation): 卡 C 分层门禁 P1 gate 领域 Scope/GateAction/Decide 规则阶梯 + 常量

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 6（T6）: gate 应用服务 + port + 观测 hook

**Files:**

- Modify: `internal/evaluation/domain/gate.go`（追加 `GateConfig`、`GateActionRecord`、`GateTargetForObservation`）
- Create: `internal/evaluation/domain/port/gate.go`
- Create: `internal/evaluation/application/gate_service.go`
- Create: `internal/evaluation/application/gate_service_test.go`
- Modify: `internal/evaluation/application/observation_service.go`（`ObservationServiceDeps` 加 `Gate port.GateSink`；Process 插 `handleGateObservation`；追加方法）
- Modify: `internal/evaluation/application/observation_service_test.go`（补 `handleGateObservation` 用例 + stub）

**Interfaces:**

- Consumes: `domain.Decide/mapRollback/GateAction/Scope/GateTarget/GateEvidence/GatePolicy`（T5）；`ObservationService.Process`（observation_service.go L67-120）；常量（T5）。`observability.MetricsProvider.IncEvalGateAction(layer, action string)`。
- Produces（下游 wiring/T8+ 消费）：
  - `domain.GateConfig struct{ Enabled, ResourceAutoRollbackEnabled bool }`
  - `domain.GateActionRecord`（台账行，infra 补 id/created_at/host_tenant_id）
  - `func GateTargetForObservation(obs EvalObservation) (GateTarget, bool)`
  - `port.GateStore{ AppendAction(ctx, tenantID, rec) error; QueryWindow(ctx, tenantID, target, since time.Time) (domain.GateEvidence, error) }`
  - `port.GatePolicySource.Resolve(ctx, target) (domain.GatePolicy, error)`
  - `port.GateApprovalRequester.RequestRollbackApproval(ctx, tenantID, rec) (string, error)`
  - `port.PlatformGateOps.UpdateEvalState(ctx, groupKey string, versionSeq int64, state, actor string) error`
  - `port.ResourceRollbackExecutor.Rollback(ctx, tenantID, target) error`
  - `port.GateSink.HandleObservation(ctx, tenantID string, obs domain.EvalObservation) error`
  - `application.NewGateService(GateServiceDeps)`；观测 hook 在 Process 落库后调用（裁决 R9/R10/R11）。

- [ ] **Step 1: domain/gate.go 追加服务侧类型**

在 `gate.go` 文件尾追加：

```go
// GateConfig 是门禁的实时生效开关（函数型依赖每次评估读取，平台键改动能实时生效，
// 裁决：不缓存静态值）。Enabled 来自 evaluation.gate.enabled；ResourceAutoRollbackEnabled
// 来自 evaluation.gate.auto_rollback_resources（仅资源 scope 决策，平台恒 manual）。
type GateConfig struct {
	Enabled                     bool
	ResourceAutoRollbackEnabled bool
}

// GateActionRecord 是一条门禁台账行，字段映射 eval_gate_actions 列（spec §4.1.2）。
// infra 写入时补 id/created_at/host_tenant_id；approval_id 由人工审批流回填。
type GateActionRecord struct {
	Scope      Scope
	Target     GateTarget
	Layer      string         // 触发层：observation（后续 optimization/casegen）
	Decision   GateAction     // 台账 decision 列文本
	Action     string         // 动作形态：rollback_recommended / escalate / ""
	Evidence   map[string]any // 决策证据（窗口计数等），JSONB 落库
	Actor      string         // 空由 infra 落默认（gate）
	ApprovalID string         // 人工审批 agent_tool_approvals id（后续卡回填）
}

// GateTargetForObservation 从一条观测推导门禁目标。锚点判定与 buildObservation 的
// 实际填充一致（纯平台观测 Source 多为 unknown/platform，resource 锚点看
// ResourceParamVersion.Version 而非 Ref）：平台锚点（Platform.GroupKey 非空且
// VersionSeq>0）且无资源版本锚点 → 平台组目标；否则资源版本锚点存在
// （obs.Resource.ResourceID + Param.Resource.Version）→ 资源目标（RevisionID 取
// Param.Resource.Version）；两者皆无 → 不可评估（裁决 R7：mapping 只认锚点）。
func GateTargetForObservation(obs EvalObservation) (GateTarget, bool) {
	p := obs.Param
	platformAnchored := p.Platform.GroupKey != "" && p.Platform.VersionSeq > 0
	resourceAnchored := obs.Resource.ResourceID != "" && p.Resource.Version != ""
	switch {
	case platformAnchored && !resourceAnchored:
		return GateTarget{
			Scope:      ScopePlatform,
			GroupKey:   p.Platform.GroupKey,
			VersionSeq: p.Platform.VersionSeq,
		}, true
	case resourceAnchored:
		// 双锚点（Source both）也归资源：观测反映被测资源行为，回滚资源版本
		// 才可能恢复行为；平台版本写回只用于纯平台观测。
		return GateTarget{
			Scope:      ScopeResource,
			Kind:       string(obs.Resource.Kind),
			ResourceID: obs.Resource.ResourceID,
			RevisionID: p.Resource.Version,
		}, true
	default:
		return GateTarget{}, false
	}
}
```

- [ ] **Step 2: 新建 port/gate.go**

Create `internal/evaluation/domain/port/gate.go`：

```go
package port

import (
	"context"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

// GateStore 持久化分层门禁台账与窗口证据（tenant scope，eval_gate_actions）。
// DB 投影在 T13 实现（QueryWindow 按 ObservationSource 分类计数窗口内的
// rule_block/behavior_anomaly/judge_flag 观察）；P1 只定义契约，单测用 stub。
type GateStore interface {
	// AppendAction 追加一条门禁决策台账行。
	AppendAction(ctx context.Context, tenantID string, rec domain.GateActionRecord) error
	// QueryWindow 返回 since 至今、目标 target 的证据聚合（RuleBlockCount/AnomalyCount/
	// JudgeFlagCount）。since 由调用方按 GateObservationWindow 推进。
	QueryWindow(ctx context.Context, tenantID string, target domain.GateTarget, since time.Time) (domain.GateEvidence, error)
}

// GatePolicySource 解析目标当前的生效门禁策略（scope 折叠：平台恒
// RollbackSupported=true + AutoRollbackAllowed=false）。
type GatePolicySource interface {
	Resolve(ctx context.Context, target domain.GateTarget) (domain.GatePolicy, error)
}

// GateApprovalRequester 为 rollback_manual 决策请求人工审批（agent_tool_approvals）。
// 返回审批 id；失败由调用方 fail-open 处理（记录台账后跳过）。
type GateApprovalRequester interface {
	RequestRollbackApproval(ctx context.Context, tenantID string, rec domain.GateActionRecord) (string, error)
}

// PlatformGateOps 是 public platform_config_versions 的门禁写回面（actor 空 → "api"）。
type PlatformGateOps interface {
	UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error
}

// ResourceRollbackExecutor 执行资源自动回滚。P1 不装配（nil 跳过）；执行日限
// GateAutoRollbackMaxPerDay 由实现方保障（T8+ 按台账聚合）。
type ResourceRollbackExecutor interface {
	Rollback(ctx context.Context, tenantID string, target domain.GateTarget) error
}

// GateSink 是观测落库后的门禁入口（fail-open：nil / 关闭 / 失败均不阻断主流程）。
type GateSink interface {
	HandleObservation(ctx context.Context, tenantID string, obs domain.EvalObservation) error
}
```

- [ ] **Step 3: 新建 application/gate_service.go**

Create `internal/evaluation/application/gate_service.go`：

```go
package application

import (
	"context"
	"sync"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

// gateLayer 是台账 layer 列与指标维度的统一层名。
const gateLayer = "observation"

// GateServiceDeps 是分层门禁应用服务依赖。全部依赖可选（nil → 对应动作跳过，
// fail-open），保证观测主流程不被门禁链路阻断（与 escalateToReview 同哲学）。
type GateServiceDeps struct {
	Logger *zap.Logger
	// Metrics nil-safe（IncEvalGateAction 不发）；生产由 wiring 注入真实 provider。
	Metrics observability.MetricsProvider
	// Cfg 实时读平台参数（evaluation.gate.enabled / auto_rollback_resources）。
	// nil 或返回 !Enabled → 门禁整体跳过（fail open 于门禁层）。
	Cfg func(ctx context.Context) domain.GateConfig
	// Repo nil（未装配证据源）→ 无法查窗口，跳过评估（fail-open）。
	Repo port.GateStore
	// Policy nil → 跳过决策（策略未装配不评估）。
	Policy port.GatePolicySource
	// Platform 平台 eval_state 写回（decision ∈ {rollback_manual, rollback_auto}）；
	// nil 跳过写回（裁决 R11）。
	Platform port.PlatformGateOps
	// Approvals rollback_manual 人工审批请求；nil 跳过（台账已记录决策）。
	Approvals port.GateApprovalRequester
	// ResourceRollback 资源自动回滚执行器；P1 恒 nil（决策动作不内联执行回滚，
	// 裁决 R9），T8+ 装配。
	ResourceRollback port.ResourceRollbackExecutor
}

// GateService 实现 port.GateSink：观测落库后评估窗口证据并路由决策。
type GateService struct {
	deps GateServiceDeps

	now  func() time.Time
	mu   sync.Mutex
	last map[string]time.Time // target.Key() → 最近一次非 none 决策时间（GateCooldown）
}

// NewGateService 构造门禁服务。Logger 缺省 zap.NewNop。
func NewGateService(deps GateServiceDeps) *GateService {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	return &GateService{
		deps: deps,
		now:  time.Now,
		last: make(map[string]time.Time),
	}
}

var _ port.GateSink = (*GateService)(nil)

// HandleObservation 在观测落库后评估一次门禁并路由（裁决 R9：不内联执行回滚）。
// fail-open：任何依赖缺失/失败只日志，返回 nil 不阻断调用方；决策只对非 none
// 生效冷却，none 不记台账。
func (s *GateService) HandleObservation(ctx context.Context, tenantID string, obs domain.EvalObservation) error {
	target, ok := domain.GateTargetForObservation(obs)
	if !ok {
		return nil
	}
	if s.cfgEnabled(ctx) == false || s.cooldownActive(target) {
		return nil
	}
	if s.deps.Repo == nil {
		// Repo nil（未装配证据源）→ 无法查窗口，跳过评估（fail-open）。
		return nil
	}
	since := s.now().UTC().Add(-constants.GateObservationWindow)
	ev, err := s.deps.Repo.QueryWindow(ctx, tenantID, target, since)
	if err != nil {
		s.warn("gate window query failed", zap.Error(err), zap.String("target", target.Key()))
		return nil
	}
	if s.deps.Policy == nil {
		return nil
	}
	policy, err := s.deps.Policy.Resolve(ctx, target)
	if err != nil {
		s.warn("gate policy resolve failed", zap.Error(err), zap.String("target", target.Key()))
		return nil
	}
	action := domain.Decide(policy, ev)
	if action == domain.GateNone {
		return nil
	}
	s.markTriggered(target)
	s.route(ctx, tenantID, target, action, ev)
	return nil
}

// cfgEnabled 返回门禁开关：Cfg nil 视为关闭（安全默认，fail open）。
func (s *GateService) cfgEnabled(ctx context.Context) bool {
	if s.deps.Cfg == nil {
		return false
	}
	return s.deps.Cfg(ctx).Enabled
}

// route 路由非 none 决策：台账 + 指标 + 平台写回 / 审批 / 自动回滚（全 fail-open）。
func (s *GateService) route(ctx context.Context, tenantID string, target domain.GateTarget, action domain.GateAction, ev domain.GateEvidence) {
	rec := domain.GateActionRecord{
		Scope:    target.Scope,
		Target:   target,
		Layer:    gateLayer,
		Decision: action,
		Action:   actionLabel(action),
		Evidence: evidencePayload(ev),
		Actor:    "gate",
	}
	s.inc(action)
	switch action {
	case domain.GateL2Escalate:
		// 裁决 R10：l2 只记台账 + 告警日志，不重复评审池入池（上游 escalateToReview 已处理）。
		s.warn("gate l2 escalate", zap.String("target", target.Key()),
			zap.Int("rule_blocks", ev.RuleBlockCount), zap.Int("anomalies", ev.AnomalyCount),
			zap.Int("judge_flags", ev.JudgeFlagCount))
	case domain.GateRollbackManual, domain.GateRollbackAuto:
		s.applyRollbackRecommendation(ctx, tenantID, target, action, &rec)
	}
	s.appendRecord(ctx, tenantID, rec)
}

// applyRollbackRecommendation 裁决 R11：平台版本写 eval_state=rollback_recommended；
// 资源 auto 且装配执行器才真正回滚（P1 不装配）；manual 走审批（Approvals，nil 跳过）。
func (s *GateService) applyRollbackRecommendation(ctx context.Context, tenantID string, target domain.GateTarget, action domain.GateAction, rec *domain.GateActionRecord) {
	switch target.Scope {
	case domain.ScopePlatform:
		if s.deps.Platform != nil {
			if err := s.deps.Platform.UpdateEvalState(ctx, target.GroupKey, target.VersionSeq, "rollback_recommended", "gate"); err != nil {
				s.warn("gate platform eval_state writeback failed", zap.Error(err), zap.String("target", target.Key()))
			}
		}
	case domain.ScopeResource:
		if action == domain.GateRollbackAuto && s.deps.ResourceRollback != nil {
			if err := s.deps.ResourceRollback.Rollback(ctx, tenantID, target); err != nil {
				s.warn("gate resource auto rollback failed", zap.Error(err), zap.String("target", target.Key()))
			}
		}
		if action == domain.GateRollbackManual && s.deps.Approvals != nil {
			approvalID, err := s.deps.Approvals.RequestRollbackApproval(ctx, tenantID, *rec)
			if err != nil {
				s.warn("gate rollback approval request failed", zap.Error(err), zap.String("target", target.Key()))
			} else {
				rec.ApprovalID = approvalID
			}
		}
	}
}

// appendRecord 追加台账（Repo nil / 失败 → 仅日志，fail-open）。
func (s *GateService) appendRecord(ctx context.Context, tenantID string, rec domain.GateActionRecord) {
	if s.deps.Repo == nil {
		return
	}
	if err := s.deps.Repo.AppendAction(ctx, tenantID, rec); err != nil {
		s.warn("gate append action failed", zap.Error(err), zap.String("target", rec.Target.Key()))
	}
}

// inc 发门禁决策指标（Metrics nil-safe；layer=observation，action=决策文本）。
func (s *GateService) inc(action domain.GateAction) {
	if s.deps.Metrics != nil {
		s.deps.Metrics.IncEvalGateAction(gateLayer, string(action))
	}
}

// warn 记录非阻断问题（logger nil 时静默，保持 fail-open 不 panic）。
func (s *GateService) warn(msg string, fields ...zap.Field) {
	if s.deps.Logger != nil {
		s.deps.Logger.Warn(msg, fields...)
	}
}

// cooldownActive 判定目标是否处于决策冷却期（自最近非 none 决策起 GateCooldown 内）。
func (s *GateService) cooldownActive(target domain.GateTarget) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lastAt, ok := s.last[target.Key()]
	if !ok {
		return false
	}
	return s.now().Before(lastAt.Add(constants.GateCooldown))
}

// markTriggered 记录一次非 none 决策时间（冷却起点）。
func (s *GateService) markTriggered(target domain.GateTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[target.Key()] = s.now().UTC()
}

// actionLabel 返回台账 action 列文本（决策的动作形态，裁决 R11）。
func actionLabel(a domain.GateAction) string {
	switch a {
	case domain.GateRollbackAuto, domain.GateRollbackManual:
		return "rollback_recommended"
	case domain.GateL2Escalate:
		return "escalate"
	}
	return ""
}

// evidencePayload 组装证据 JSONB（窗口计数 + 人工/对照判定摘要）。
func evidencePayload(ev domain.GateEvidence) map[string]any {
	payload := map[string]any{
		"rule_blocks":   ev.RuleBlockCount,
		"anomalies":     ev.AnomalyCount,
		"judge_flags":   ev.JudgeFlagCount,
		"review_verdict": string(ev.ReviewVerdict),
	}
	if ev.ConfirmationRun != nil {
		payload["confirmation_regressed"] = ev.ConfirmationRun.Regressed
	}
	return payload
}
```

- [ ] **Step 4: observation_service.go 挂钩**

`ObservationServiceDeps` 尾部（`Review` 字段后）加：

```go
	// Gate 是分层门禁入口（spec §2.5）；nil 时门禁评估静默跳过（fail-open）。
	Gate port.GateSink
```

`Process`（observation_service.go L115-120）把 L116-118 改成下述字节精确形态（裁决 R9：门禁在评审池入池前评估——`handleGateObservation` 放在既有评审池注释之前）。以当前源为 old：

```go
	// old（现状）：
	s.recordSampled(evt.ResourceKind)
	s.deps.Metrics.IncEvalObservation(evt.ResourceKind, obs.Stratum)
	// 评审池内联触发（P1c §6.6）：落库成功后按触发规则入池（fail-open，见 escalateToReview）。
	s.escalateToReview(ctx, evt, &obs)
	return nil
}

	// new：
	s.recordSampled(evt.ResourceKind)
	s.deps.Metrics.IncEvalObservation(evt.ResourceKind, obs.Stratum)
	// 分层门禁内联评估（spec §2.5）：落库成功后评估窗口证据并路由决策（fail-open）。
	s.handleGateObservation(ctx, evt, &obs)
	// 评审池内联触发（P1c §6.6）：落库成功后按触发规则入池（fail-open，见 escalateToReview）。
	s.escalateToReview(ctx, evt, &obs)
	return nil
}
```

文件尾（`escalateToReview` 方法后）追加：

```go
// handleGateObservation 门禁内联评估（spec §2.5）：落库成功后由分层门禁评估观察
// 窗口并路由决策。fail-open——未装配（Gate nil）或门禁内部失败只日志，不阻断
// 观测主流程、不改 verdict（与 escalateToReview 同哲学）。
func (s *ObservationService) handleGateObservation(
	ctx context.Context, evt domain.ObservationReferenceEvent, obs *domain.EvalObservation,
) {
	if s.deps.Gate == nil {
		return
	}
	if err := s.deps.Gate.HandleObservation(ctx, evt.TenantID, *obs); err != nil {
		s.deps.Logger.Warn("observation gate evaluation failed", zap.Error(err),
			zap.String("trace_id", evt.TraceID))
	}
}
```

- [ ] **Step 5: 写失败测试 gate_service_test.go**

Create `internal/evaluation/application/gate_service_test.go`（package application，访问 s.now 便于冷却测试）：

```go
package application

import (
	"context"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// stubGateStore 内存版 GateStore（QueryWindow 预置证据；AppendAction 收台账）。
type stubGateStore struct {
	evidence domain.GateEvidence
	actions  []domain.GateActionRecord
	err      error
}

func (s *stubGateStore) AppendAction(_ context.Context, _ string, rec domain.GateActionRecord) error {
	if s.err != nil {
		return s.err
	}
	s.actions = append(s.actions, rec)
	return nil
}

func (s *stubGateStore) QueryWindow(context.Context, string, domain.GateTarget, time.Time) (domain.GateEvidence, error) {
	return s.evidence, s.err
}

// stubGatePolicy 固定返回策略；err 非 nil 模拟解析失败。
type stubGatePolicy struct {
	policy domain.GatePolicy
	err    error
}

func (s *stubGatePolicy) Resolve(context.Context, domain.GateTarget) (domain.GatePolicy, error) {
	return s.policy, s.err
}

// stubPlatformOps 记录平台 eval_state 写回。
type stubPlatformOps struct {
	states []string
	err    error
}

func (s *stubPlatformOps) UpdateEvalState(_ context.Context, _ string, _ int64, state, _ string) error {
	if s.err != nil {
		return s.err
	}
	s.states = append(s.states, state)
	return nil
}

// gateObs 构造一条平台源观测（组=agent，seq=2）。
func gateObs() domain.EvalObservation {
	return domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "agent-1"},
		Param: domain.ParamVersion{
			Source: domain.ParamSourcePlatform,
			Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 2},
		},
	}
}

func fixedNow() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }

func newTestGate(deps GateServiceDeps, start time.Time) *GateService {
	s := NewGateService(deps)
	s.now = func() time.Time { return start }
	return s
}

func TestHandleObservationRoutesRollbackManualToPlatformWriteback(t *testing.T) {
	ctx := context.Background()
	store := &stubGateStore{evidence: domain.GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin}}
	platform := &stubPlatformOps{}
	svc := newTestGate(GateServiceDeps{
		Cfg:      func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:     store,
		Policy:   &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true}},
		Platform: platform,
	}, fixedNow())

	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("HandleObservation error = %v", err)
	}
	if len(platform.states) != 1 || platform.states[0] != "rollback_recommended" {
		t.Fatalf("platform writeback = %v, want [rollback_recommended]", platform.states)
	}
	if len(store.actions) != 1 {
		t.Fatalf("ledger actions = %d, want 1", len(store.actions))
	}
	rec := store.actions[0]
	if rec.Decision != domain.GateRollbackManual || rec.Action != "rollback_recommended" {
		t.Fatalf("ledger decision/action = %q/%q, want rollback_manual/rollback_recommended", rec.Decision, rec.Action)
	}
	if rec.Target.Scope != domain.ScopePlatform || rec.Target.GroupKey != "agent" || rec.Target.VersionSeq != 2 {
		t.Fatalf("ledger target = %+v, want platform agent seq 2", rec.Target)
	}
}

func TestHandleObservationDisabledOrCleanSkips(t *testing.T) {
	ctx := context.Background()
	disabled := newTestGate(GateServiceDeps{
		Cfg:    func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: false} },
		Repo:   &stubGateStore{},
		Policy: &stubGatePolicy{},
	}, fixedNow())
	if err := disabled.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("disabled error = %v", err)
	}

	clean := &stubGateStore{} // evidence 全零 → none
	svc := newTestGate(GateServiceDeps{
		Cfg:    func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:   clean,
		Policy: &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true}},
	}, fixedNow())
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("clean error = %v", err)
	}
	if len(clean.actions) != 0 {
		t.Fatalf("clean window must not append ledger, got %d", len(clean.actions))
	}
}

func TestHandleObservationGateNilRepoDoesNotPanic(t *testing.T) {
	// fail-open：Repo nil（未装配证据源）→ HandleObservation 跳过评估，不 panic。
	ctx := context.Background()
	svc := newTestGate(GateServiceDeps{
		Cfg:    func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:   nil,
		Policy: &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopeResource, RollbackSupported: true}},
	}, fixedNow())
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleObservationCooldownSuppressesRapidRepeats(t *testing.T) {
	ctx := context.Background()
	store := &stubGateStore{evidence: domain.GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin}}
	platform := &stubPlatformOps{}
	svc := newTestGate(GateServiceDeps{
		Cfg:      func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:     store,
		Policy:   &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true}},
		Platform: platform,
	}, fixedNow())
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatal(err)
	}
	// 同 target 仍在冷却期内 → 跳过。
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatal(err)
	}
	if len(store.actions) != 1 {
		t.Fatalf("cooldown not applied, ledger actions = %d, want 1", len(store.actions))
	}
}

func TestGateTargetForObservation(t *testing.T) {
	// 纯平台锚点（无资源版本）→ 平台组目标（与 buildObservation 纯平台观测一致：
	// Source unknown、Platform.GroupKey+seq 已填）。
	platform := domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
		Param: domain.ParamVersion{Source: domain.ParamSourceUnknown,
			Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 3}},
	}
	tgt, ok := domain.GateTargetForObservation(platform)
	if !ok || tgt.Scope != domain.ScopePlatform || tgt.GroupKey != "agent" || tgt.VersionSeq != 3 {
		t.Fatalf("platform mapping = %+v ok=%v", tgt, ok)
	}

	// 资源版本锚点 → 资源目标（Kind+ResourceID+RevisionID=Version）。
	resource := domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "skill", ResourceID: "s1"},
		Param: domain.ParamVersion{Source: domain.ParamSourceResource,
			Resource: domain.ResourceParamVersion{Ref: "rev-9", Version: "rev-9"}},
	}
	tgt, ok = domain.GateTargetForObservation(resource)
	if !ok || tgt.Scope != domain.ScopeResource || tgt.Kind != "skill" || tgt.ResourceID != "s1" || tgt.RevisionID != "rev-9" {
		t.Fatalf("resource mapping = %+v ok=%v", tgt, ok)
	}

	// 双锚点（平台+资源都带版本，Source both）→ 资源优先（回滚被测资源以恢复行为）。
	both := domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
		Param: domain.ParamVersion{Source: domain.ParamSourceBoth,
			Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 3},
			Resource: domain.ResourceParamVersion{Ref: "rev-9", Version: "rev-9"}},
	}
	tgt, ok = domain.GateTargetForObservation(both)
	if !ok || tgt.Scope != domain.ScopeResource || tgt.ResourceID != "a1" || tgt.RevisionID != "rev-9" {
		t.Fatalf("both mapping = %+v ok=%v", tgt, ok)
	}

	// 平台组带 key 但 seq 0（未发布 unknown）→ 非锚点，不可评估。
	noSeq := domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
		Param: domain.ParamVersion{
			Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 0}},
	}
	if _, ok := domain.GateTargetForObservation(noSeq); ok {
		t.Fatal("platform anchor without seq must not map to a gate target")
	}

	// 无任何锚点 → 不可评估。
	unversioned := domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
		Param:    domain.ParamVersion{},
	}
	if _, ok := domain.GateTargetForObservation(unversioned); ok {
		t.Fatal("unversioned observation must not map to a gate target")
	}
}

func TestActionLabel(t *testing.T) {
	if got := actionLabel(domain.GateRollbackManual); got != "rollback_recommended" {
		t.Fatalf("manual label = %q", got)
	}
	if got := actionLabel(domain.GateRollbackAuto); got != "rollback_recommended" {
		t.Fatalf("auto label = %q", got)
	}
	if got := actionLabel(domain.GateL2Escalate); got != "escalate" {
		t.Fatalf("escalate label = %q", got)
	}
	if got := actionLabel(domain.GateNone); got != "" {
		t.Fatalf("none label = %q, want empty", got)
	}
}
```

注：`GateService.inc()`（gate_service.go `Metrics` nil-safe）在 `s.deps.Metrics != nil` 时才发指标，故上面所有测试构造都不带 `Metrics` 字段、文件也不 import `observability`——`GateServiceDeps` 其余字段（Repo/Policy/Platform/Cfg）都传入以便走到断言路径；只测 fail-open 分支时留对应依赖为 nil 即可。

- [ ] **Step 6: 跑测试确认失败**

Run: `go test ./internal/evaluation/application/ -run 'HandleObservation|GateTarget|ActionLabel'`
Expected: 编译失败（gate_service.go/port 不存在）→ 属预期。

- [ ] **Step 7: observation hook 用例 + stub**

读 `observation_service_test.go`，补 `handleGateObservation` 用例：构造一个记录 `HandleObservation` 调用的 stubGate：

```go
type stubGateSink struct {
	calls int
}

func (s *stubGateSink) HandleObservation(_ context.Context, tenantID string, obs domain.EvalObservation) error {
	s.calls++
	return nil
}
```

用例（放入该文件合适位置，复用文件既有构造 service 的方式）：

- Gate nil → calls 0（fail-open 未装配）。
- 有 Gate + 正常 Process（deps 复用既有观测 happy-path 构造）→ calls 1。
若文件当前 Process 测试用独立 deps 构造函数，追加一个带 Gate 的构造即可；断言 `stub.calls == 1`。

- [ ] **Step 8: 跑门禁相关全量测试**

Run: `go test ./internal/evaluation/application/ ./internal/evaluation/domain/`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/evaluation/domain/gate.go internal/evaluation/domain/port/gate.go internal/evaluation/application/gate_service.go internal/evaluation/application/gate_service_test.go internal/evaluation/application/observation_service.go internal/evaluation/application/observation_service_test.go
git commit -m "feat(evaluation): 卡 C 分层门禁 P1 gate 应用服务/port + 观测落库 hook

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 7（T7）: 快照历史 version_seq override 透传

**Files:**

- Modify: `internal/evaluation/domain/port/evaluation.go`（`CaptureInput` 加 `PlatformSeqOverrides`）
- Modify: `internal/evaluation/application/job_service.go`（`EnqueueRunInput` + `EnqueueRun` CaptureInput 透传）
- Modify: `api/wiring/evaluation_snapshot.go`（`Capture` 三处调用 + `captureGroup` 两遍扫描 + `groupFromVersion` 抽取 + `overrideSeq` helper）
- Modify: `proto/evaluation/evaluation.proto`（`EnqueueEvaluationRunRequest` 加 `map<string,int64> platform_seq_overrides = 4;`）
- Modify: `api/http/handler/evaluation_handler.go`（EnqueueRun DTO → input 透传）
- Create: `api/wiring/evaluation_snapshot_override_test.go`
- Run: `make proto-gen`

**Interfaces:**

- Consumes: `port.CaptureInput`（evaluation.go L276-280）；`snapshotCapturer.Capture(ctx, tenantID, input evalport.CaptureInput)`（evaluation_snapshot.go L48）；`captureGroup(ctx, groupKey string)`（L96-119）；`Service.Versions(ctx, groupKey) ([]port.PlatformVersion, error)`（parameters service）。
- Produces: `CaptureInput.PlatformSeqOverrides map[string]int64`（groupKey→version_seq，空 = 现 IsCurrent 语义）；`captureGroup(ctx, groupKey string, overrideSeq *int64)`；平台组按 seq 精确匹配历史版本（无命中回退 IsCurrent，裁决 R15/R16）。

- [ ] **Step 1: CaptureInput 加字段**

读 `internal/evaluation/domain/port/evaluation.go` L276-280，替换为：

```go
// CaptureInput 描述一次快照捕获：被测资源 + 评测套件 revision + 请求者。
// PlatformSeqOverrides 按平台组（evaluation/agent/trace groupKey）指定历史版本
// version_seq 覆盖（对照确认 run 重放）；空 = 现 IsCurrent 语义。
type CaptureInput struct {
	Resource             domain.ResourceRef
	SuiteRevisionID      string
	RequestedBy          string
	PlatformSeqOverrides map[string]int64
}
```

- [ ] **Step 2: job_service 透传**

`EnqueueRunInput` 加 `PlatformSeqOverrides map[string]int64`（`RequestedBy` 后）；`EnqueueRun` 的 `port.CaptureInput{...}` 字面量加字段：

```go
	snapshot, err := s.capturer.Capture(ctx, tenantID, port.CaptureInput{
		Resource: input.Resource, SuiteRevisionID: input.SuiteRevisionID, RequestedBy: input.RequestedBy,
		PlatformSeqOverrides: input.PlatformSeqOverrides,
	})
```

- [ ] **Step 3: wiring captureGroup 两遍扫描**

读 `api/wiring/evaluation_snapshot.go`（Capture L48-92 调用三组、captureGroup L96-119）。改 `Capture` 三处调用为传指针（把 `evaldomain.GroupEvaluation/GroupAgent/GroupTrace` 的三个 groupKey 常量放入 helper 参数并查 override）：

```go
	evalGroup, err := c.captureGroup(ctx, evaldomain.GroupEvaluation, overrideSeq(input, evaldomain.GroupEvaluation))
	if err != nil {
		return nil, err
	}
	snap.Evaluation = evalGroup
	agentGroup, err := c.captureGroup(ctx, evaldomain.GroupAgent, overrideSeq(input, evaldomain.GroupAgent))
	if err != nil {
		return nil, err
	}
	traceGroup, err := c.captureGroup(ctx, evaldomain.GroupTrace, overrideSeq(input, evaldomain.GroupTrace))
	if err != nil {
		return nil, err
	}
```

`captureGroup` 整体替换为（含抽取的 `groupFromVersion` 与 helper）：

```go
// overrideSeq 返回该组在 CaptureInput 中的历史版本覆盖；无覆盖 → nil（现 IsCurrent 语义）。
func overrideSeq(in evalport.CaptureInput, groupKey string) *int64 {
	seq, ok := in.PlatformSeqOverrides[groupKey]
	if !ok {
		return nil
	}
	return &seq
}

// captureGroup 读一组平台版本：overrideSeq 非 nil 时精确匹配历史版本 seq（对照重放，
// spec §4.3.4）；无命中回退 IsCurrent（版本归档/修剪后容忍，不回错误，裁决 R15）；
// 全无 → 空组（nil Values），执行时消费层默认适用。
func (c snapshotCapturer) captureGroup(ctx context.Context, groupKey string, overrideSeq *int64) (evaldomain.GroupSnapshot, error) {
	if c.params == nil {
		return evaldomain.GroupSnapshot{}, fmt.Errorf("capture %s group versions: parameters service unavailable", groupKey)
	}
	versions, err := c.params.Versions(ctx, groupKey)
	if err != nil {
		return evaldomain.GroupSnapshot{}, fmt.Errorf("capture %s group versions: %w", groupKey, err)
	}
	if overrideSeq != nil {
		for _, v := range versions {
			if int64(v.VersionSeq) == *overrideSeq {
				return c.groupFromVersion(groupKey, v.VersionSeq, v.Snapshot)
			}
		}
		c.warn("capture override version missing, fallback to current",
			zap.String("group_key", groupKey), zap.Int64("override_seq", *overrideSeq))
	}
	for _, v := range versions {
		if !v.IsCurrent {
			continue
		}
		return c.groupFromVersion(groupKey, v.VersionSeq, v.Snapshot)
	}
	return evaldomain.GroupSnapshot{GroupKey: groupKey}, nil
}

// groupFromVersion 复制单个平台版本快照为评测组快照（D1 值复制）。
// 形参取已解开的 versionSeq/snapshot 两个值，而不是 Versions 返回元素的具名类型：
// wiring 不 import parameters domain/application 具名类型（避免跨 context import 与
// 测试 stub 复制负担），调用点拆字段传入，零 import 变动。
func (c snapshotCapturer) groupFromVersion(groupKey string, versionSeq int, snapshot map[string]json.RawMessage) (evaldomain.GroupSnapshot, error) {
	values := make(map[string]any, len(snapshot))
	for k, raw := range snapshot {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return evaldomain.GroupSnapshot{}, fmt.Errorf("capture %s snapshot decode %s: %w", groupKey, k, err)
		}
		values[k] = decoded
	}
	return evaldomain.GroupSnapshot{GroupKey: groupKey, VersionSeq: int64(versionSeq), Values: values}, nil
}
```

说明：`c.params.Versions(ctx, groupKey)` 返回 `[]parametersport.PlatformVersion`（`internal/parameters/domain/port` 的 `PlatformVersion`，`VersionSeq int`、`Snapshot map[string]json.RawMessage`）。上一段代码块（`overrideSeq`/`captureGroup`/`groupFromVersion`）是**最终权威实现，无其他变体**：`captureGroup` 内调用 `groupFromVersion(groupKey, v.VersionSeq, v.Snapshot)` 拆字段传入，wiring 零新增 import（不引 parameters domain/application 具名类型）。测试 stub 的 `PlatformSeqOverrides` 与 `captureGroup` 第三参 `*int64` 与上文一致。

- [ ] **Step 4: proto + DTO + handler 透传**

`proto/evaluation/evaluation.proto` `EnqueueEvaluationRunRequest` 替换为：

```proto
message EnqueueEvaluationRunRequest {
  // @binding: required
  EvaluationResourceRef resource = 1;
  // @binding: required
  string suite_revision_id = 2;
  // @binding: required,max=255
  string idempotency_key = 3;
  // 平台组历史版本覆盖（对照确认 run 重放）；空 = 现 IsCurrent 语义。
  map<string, int64> platform_seq_overrides = 4;
}
```

Run: `make proto-gen`（生成 api/http/dto/gen/，不入 git；handler import 依赖它）。

读 `api/http/handler/evaluation_handler.go` `EnqueueRun`（L362-391），`ShouldBindJSON` 后的 `evalapp.EnqueueRunInput{...}` 字面量加字段：

```go
	evalapp.EnqueueRunInput{
		Resource:             domain.ResourceRef{Kind: domain.ResourceKind(req.Resource.Kind), ResourceID: req.Resource.ResourceID, RevisionID: req.Resource.RevisionID},
		SuiteRevisionID:      req.SuiteRevisionID,
		IdempotencyKey:       req.IdempotencyKey,
		RequestedBy:          requestedBy,
		PlatformSeqOverrides: req.PlatformSeqOverrides,
	},
```

（以文件实际字面量为准，补上字段即可。）

- [ ] **Step 5: 写 override 单元测试**

Create `api/wiring/evaluation_snapshot_override_test.go`（package wiring）：先读 `internal/parameters/application/application_test.go` 是否有可复制的内存 store（若有现成 memStore 在 application 包不可用），本测试自建最小 stub 实现 `port.PlatformStore`（`ListVersions` 返回种子版本，其余方法返回空/零值即可，接口扩展后方法清单见 Task 4 store.go）：

```go
package wiring

import (
	"context"
	"encoding/json"
	"testing"

	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	parametersdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	parametersport "github.com/byteBuilderX/stratum/internal/parameters/domain/port"
)

// seededPlatformStore 仅 ListVersions 有数据；其余方法零值（本测试只用 Versions）。
type seededPlatformStore struct {
	versions map[string][]parametersport.PlatformVersion
}

func (s *seededPlatformStore) GetValue(context.Context, string) (json.RawMessage, bool, error) {
	return nil, false, nil
}
func (s *seededPlatformStore) SetValue(context.Context, string, json.RawMessage, string) error {
	return nil
}
func (s *seededPlatformStore) GetAll(context.Context) ([]parametersport.PlatformValue, error) {
	return nil, nil
}
func (s *seededPlatformStore) GetSnapshot(context.Context, string) (map[string]json.RawMessage, error) {
	return nil, nil
}
func (s *seededPlatformStore) CreateDraft(context.Context, string, map[string]json.RawMessage, string, string) (parametersport.PlatformVersion, error) {
	return parametersport.PlatformVersion{}, nil
}
func (s *seededPlatformStore) Publish(context.Context, string, int64, string) error { return nil }
func (s *seededPlatformStore) Rollback(context.Context, string, int64, string) error {
	return nil
}
func (s *seededPlatformStore) ListVersions(_ context.Context, groupKey string) ([]parametersport.PlatformVersion, error) {
	return s.versions[groupKey], nil
}
func (s *seededPlatformStore) GetVersion(context.Context, string, int64) (parametersport.PlatformVersion, error) {
	return parametersport.PlatformVersion{}, nil
}
func (s *seededPlatformStore) UpdateEvalState(context.Context, string, int64, string, string) error {
	return nil
}

func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestCaptureGroupOverridePicksHistoricalVersion(t *testing.T) {
	svc := parametersapp.NewService(parametersdomain.NewParametersRegistry(),
		&seededPlatformStore{versions: map[string][]parametersport.PlatformVersion{
			"agent": {
				{GroupKey: "agent", VersionSeq: 1, IsCurrent: false,
					Snapshot: map[string]json.RawMessage{"agent.temperature": jsonRaw(0.2)}},
				{GroupKey: "agent", VersionSeq: 2, IsCurrent: true,
					Snapshot: map[string]json.RawMessage{"agent.temperature": jsonRaw(0.9)}},
			},
		}})
	c := snapshotCapturer{params: svc}
	ctx := context.Background()

	// 有 override → 精确命中历史 seq 1。
	seq := int64(1)
	got, err := c.captureGroup(ctx, "agent", &seq)
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionSeq != 1 {
		t.Fatalf("override capture seq = %d, want 1", got.VersionSeq)
	}
	if v, _ := got.Values["agent.temperature"].(float64); v != 0.2 {
		t.Fatalf("override captured value = %v, want 0.2", got.Values["agent.temperature"])
	}

	// override miss（seq 999 已归档修剪）→ 回退 IsCurrent seq 2，不回错误。
	miss := int64(999)
	got, err = c.captureGroup(ctx, "agent", &miss)
	if err != nil {
		t.Fatalf("override miss must fall back, got error: %v", err)
	}
	if got.VersionSeq != 2 {
		t.Fatalf("override miss fallback seq = %d, want 2", got.VersionSeq)
	}

	// 无 override → 现 IsCurrent 语义。
	got, err = c.captureGroup(ctx, "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionSeq != 2 {
		t.Fatalf("current capture seq = %d, want 2", got.VersionSeq)
	}
}

func TestCaptureOverrideFlowsThroughInput(t *testing.T) {
	in := evalport.CaptureInput{PlatformSeqOverrides: map[string]int64{"evaluation": 7}}
	if got := overrideSeq(in, "evaluation"); got == nil || *got != 7 {
		t.Fatalf("overrideSeq(evaluation) = %v, want 7", got)
	}
	if got := overrideSeq(in, "agent"); got != nil {
		t.Fatalf("overrideSeq(agent) = %v, want nil", got)
	}
}
```

（构造签名已核对：`parametersdomain.NewParametersRegistry()` 无参；`parametersapp.NewService(registry, store)`——见 `internal/parameters/domain/registry.go`、`internal/parameters/application/service.go`。若执行时参数包 API 已演进，以文件实际为准并同步本段。）

- [ ] **Step 6: 跑测试**

Run: `go build ./...`
Run: `go test ./api/wiring/ -run 'CaptureGroup|CaptureOverride' ./internal/evaluation/application/ ./internal/evaluation/domain/ ./api/http/`
Expected: PASS（handler DTO 因 proto-gen 已含 map 字段；contract golden 无变化——请求 map 为空不改变响应 JSON）。

- [ ] **Step 7: Commit**

```bash
git add internal/evaluation/domain/port/evaluation.go internal/evaluation/application/job_service.go api/wiring/evaluation_snapshot.go proto/evaluation/evaluation.proto api/http/handler/evaluation_handler.go api/wiring/evaluation_snapshot_override_test.go
git commit -m "feat(evaluation): 卡 C 分层门禁 P1 快照历史 version_seq override 透传

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 8: P1 收尾验证（不做代码改动，只跑门禁）

**Files:** 无（纯验证）

- [ ] **Step 1: 快速验证**
Run（worktree 内）：`go vet ./... && go test -short ./...`
Expected: 全绿

- [ ] **Step 2: race 全量**
Run: `go test -v -race -timeout 30s ./...`
Expected: 全绿（若 timeout 30s 太紧按仓库惯例放宽到 CI 值）

- [ ] **Step 3: quality + risk guards**
Run: `make code-quality`、`make risk-guardrails`
Expected: 无新超标函数、无风险回归

- [ ] **Step 4: 系统验收交接**
PR 前由 `stratum-e2e-tester` 依 `.test/verification.yaml` 风险级执行系统验收（禁绕过 skill 直跑 make）。验收口径：T1-T7 代码 + 测试 + DB 迁移/DDL 链路证据 + contract。评估 P1 决策链路为 R2→e2e-short 或 R3（DB + 状态机）→+e2e-soak，由 tester 按 evidence 定级。

## Spec 覆盖自检（writing-plans）

| spec 节 | 实现任务 |
|---|---|
| §4.1.1 public 迁移 044 三列 | T1 |
| §4.1.2 eval_gate_actions + 索引 + behavior_anomaly + L3 走 agent_tool_approvals（不新增表） | T2 |
| §4.2.1 RiskTier 类型 + 每键声明 + 不变量测试 | T3 |
| §4.2.2 registerGateParams 两键 + NewParametersRegistry 装配 | T4 |
| §4.2.3 PlatformStore.GetVersion/UpdateEvalState + repo + service 薄转发 | T4 |
| §4.2.4 常量块 | T5 |
| §2.2 Scope/GateTarget/GateAction/ReviewVerdict/RunComparison/GateEvidence/GatePolicy | T5 |
| §2.3 Decide 规则阶梯（rule5 先于 rule6、scope 折叠） | T5 + 裁决 R3/R4 |
| §2.5 观测落库 hook（Save 后 / escalateToReview 前，fail-open） | T6（裁决 R9） |
| §4.3.4 快照 override（map 字段 + capture 精确匹配 + miss 容忍） | T7 |
| §5 T9-T14（台账 infra、策略 resolver、确认 run、auto 执行、告警 runbook） | 不在 P1，T8+ 后续卡 |

无占位符；跨任务类型名一致（`GateAction/Scope/GateTarget/GateEvidence/GatePolicy/GateTargetForObservation/GateConfig/GateActionRecord/port.*` 由 T5→T6 单一定义并复用）。已把裁决 R1-R16 固化在裁决表与各任务注释中。

## 主要风险与对策

1. **T7 `groupFromVersion` 形参类型**：避免 wiring import parameters port 具名类型，采用 `(groupKey string, versionSeq int, snapshot map[string]json.RawMessage)` 三参拆分、调用点拆字段传入，零 import 变动（T7 Step 3 权威实现，无变体）。
2. **T4 PlatformStore 接口扩展的连锁编译**：`go build ./...` 找齐 memStore/errStore/其它桩补两方法；接口扩展属编译期强制，不会漏。
3. **observation_service_test stub 复用的差异**：先读既有文件，Metrics/构造方式跟随其模式（nil-safe 或 NoopMetrics 二选一），不引入第二套。
4. **tenant_schema guard 正则连锁**：加表后跑全 `pkg/storage/postgres` 测试；若既有 guard 对新增表断言失败，按失败断言调整，禁止绕过。
5. **gate 决策在 P1 无真实 wiring**：`ObservationServiceDeps.Gate` 保持 nil 即 fail-open（门禁默认关）；台账 DB infra（T13）与 wiring 装配属 T8+，P1 交付可测服务 + hook 就绪状态。
