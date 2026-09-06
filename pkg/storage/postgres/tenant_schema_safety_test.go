package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTenantSchemaQuarantinesUnmappedKnowledgeChunksWithoutDeletingThem(t *testing.T) {
	ddl, err := os.ReadFile("tenant_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ddl)
	if strings.Contains(text, "DELETE FROM knowledge_chunks WHERE workspace_id IS NULL") {
		t.Fatal("tenant startup DDL still deletes unmapped knowledge chunks")
	}
	if !strings.Contains(text, "knowledge_chunks_quarantine") {
		t.Fatal("tenant startup DDL does not preserve unmapped chunks in quarantine")
	}
}

func TestTenantSchemaRevisionAndDecisionSafetyAvoidsPlaintextPayloads(t *testing.T) {
	ddl, err := os.ReadFile("tenant_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ddl)

	for _, table := range []string{"resource_revisions", "experiment_decisions"} {
		start := strings.Index(text, "CREATE TABLE IF NOT EXISTS "+table)
		if start == -1 {
			t.Fatalf("tenant schema missing %s", table)
		}
		end := strings.Index(text[start:], ");")
		if end == -1 {
			t.Fatalf("tenant schema has unterminated %s DDL", table)
		}
		body := strings.ToLower(text[start : start+end])
		if strings.Contains(body, "payload jsonb") || strings.Contains(body, "payload_json jsonb") {
			t.Fatalf("%s must not store plaintext payload JSONB", table)
		}
	}

	for _, table := range []string{
		"skills",
		"skill_versions",
		"skill_test_cases",
		"skill_eval_runs",
		"agent_skill_links",
		"eval_suites",
		"eval_suite_revisions",
		"eval_runs",
		"evaluation_experiments",
		"evaluation_deployments",
		"evaluation_feedback",
	} {
		if strings.Contains(text, "DROP TABLE IF EXISTS "+table) {
			t.Fatalf("tenant upgrade must not drop existing Skill evaluation table %s", table)
		}
	}
}

func TestTenantSchemaAuditProjectionsAreCredentialFreeColumns(t *testing.T) {
	ddl, err := os.ReadFile("tenant_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ddl)
	start := strings.Index(text, "CREATE TABLE IF NOT EXISTS resource_change_audits")
	if start == -1 {
		t.Fatal("tenant schema missing resource_change_audits")
	}
	end := strings.Index(text[start:], ");")
	if end == -1 {
		t.Fatal("tenant schema has unterminated resource_change_audits DDL")
	}
	body := strings.ToLower(text[start : start+end])
	for _, col := range []string{"before_projection", "after_projection", "actor_type", "source", "proposal_id"} {
		if !strings.Contains(body, col+" ") && !strings.Contains(body, col+"\n") {
			t.Fatalf("resource_change_audits missing %s column", col)
		}
	}
	// Projections are marshalled from Go safe projections; the DDL must not
	// hint at storing raw credential-bearing config blobs.
	for _, forbidden := range []string{"auth_config", "headers", "\"env\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("resource_change_audits DDL references credential-bearing field %q", forbidden)
		}
	}
}

func TestTenantSchemaHasReviewPoolTables(t *testing.T) {
	ddl, err := os.ReadFile("tenant_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ddl)
	for _, table := range []string{
		"eval_review_items",
		"eval_calibration_samples",
		"eval_attribution_entries",
	} {
		if !strings.Contains(text, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("tenant schema missing %s", table)
		}
		// 幂等：不允许裸 CREATE（无 IF NOT EXISTS）。
		if strings.Contains(text, "CREATE TABLE "+table) {
			t.Fatalf("tenant schema has non-idempotent %s DDL", table)
		}
	}
}

func TestTenantSchemaWorkflowEditorsAndCreatedBy(t *testing.T) {
	ddl, err := os.ReadFile("tenant_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ddl)

	// workflow_definitions 必须带 created_by（所有权矩阵 creator 语义的存储基线）。
	if !strings.Contains(text, "workflow_definitions") {
		t.Fatal("tenant schema missing workflow_definitions")
	}
	if !strings.Contains(text, "created_by TEXT NOT NULL DEFAULT ''") {
		t.Fatal("workflow_definitions must carry created_by TEXT NOT NULL DEFAULT ''")
	}
	// 幂等 ALTER 用于升级历史租户。
	if !strings.Contains(text, "ALTER TABLE workflow_definitions ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';") {
		t.Fatal("workflow_definitions must idempotently add created_by for historical tenants")
	}
	// resource_editors 注释声明 workflow kind（可申请编辑权的新资源类型）。
	if !strings.Contains(text, "agent|skill|mcp|knowledge|workflow") {
		t.Fatal("resource_editors kind comment must include workflow")
	}
	// workflow_versions.created_by 是版本历史「操作者」列的存储基线：CREATE 段必须带列，
	// 历史租户由幂等 ALTER 升级，两者任一缺失都会让 store 写路径与 DDL 静默分叉。
	if !strings.Contains(text, "ALTER TABLE workflow_versions ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';") {
		t.Fatal("workflow_versions must idempotently add created_by for historical tenants")
	}
	// 存量版本 created_by 幂等回填必须存在（WHERE created_by='' 只回填一次）。
	if !strings.Contains(text, "WHERE v.created_by = '';") {
		t.Fatal("workflow_versions must idempotently backfill created_by for existing versions")
	}
}

// TestTenantSchemaEvaluationDeleteCreatedByColumns 守护评测删除门禁的创建者列：每个删除目标表
// 必须在 CREATE TABLE 段携带 created_by，且带幂等 ALTER 升级历史租户（” 表示存量行仅租户 owner 可删）。
func TestTenantSchemaEvaluationDeleteCreatedByColumns(t *testing.T) {
	ddl, err := os.ReadFile("tenant_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ddl)

	tables := []string{
		"eval_suites",
		"eval_runs",
		"eval_review_items",
		"evaluation_feedback",
		"evaluation_jobs",
	}
	for _, table := range tables {
		createStart := strings.Index(text, "CREATE TABLE IF NOT EXISTS "+table+" (")
		if createStart < 0 {
			t.Fatalf("tenant schema missing CREATE TABLE for %s", table)
		}
		createEnd := strings.Index(text[createStart:], ");")
		createBlock := text[createStart : createStart+createEnd]
		// SQL 列对齐用多空格（如 created_by         TEXT），以正则容忍空白差异匹配列定义。
		colRe := regexp.MustCompile(`created_by\s+TEXT\s+NOT NULL DEFAULT ''`)
		if !colRe.MatchString(createBlock) {
			t.Fatalf("%s CREATE TABLE must carry created_by TEXT NOT NULL DEFAULT ''", table)
		}
		alter := "ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';"
		if !strings.Contains(text, alter) {
			t.Fatalf("%s must idempotently add created_by for historical tenants", table)
		}
	}
}

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
