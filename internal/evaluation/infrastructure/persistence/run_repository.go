package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgRunRepository struct {
	pool poolIface
}

func NewPgRunRepository(pool *pgxpool.Pool) *PgRunRepository {
	return &PgRunRepository{pool: pool}
}

func (r *PgRunRepository) SaveRun(ctx context.Context, tenantID string, run domain.EvalRun) error {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	metricsJSON, err := json.Marshal(run.Metrics)
	if err != nil {
		return fmt.Errorf("evaluation run repository: marshal metrics: %w", err)
	}
	if string(metricsJSON) == "null" {
		metricsJSON = []byte("{}")
	}
	// 快照序列化为 JSON 文本（pgx v5 JSONB 收 string）：nil = 旧 run/未捕获，
	// 落库 '{}'，GetRun 读回 nil 与 omitempty 序列化自洽。
	snapshotJSON := "{}"
	if run.ContextSnapshot != nil {
		b, err := json.Marshal(run.ContextSnapshot)
		if err != nil {
			return fmt.Errorf("evaluation run repository: marshal context snapshot: %w", err)
		}
		snapshotJSON = string(b)
	}
	return execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO eval_runs
			 (id, resource_kind, resource_id, revision_id, suite_revision_id, status, passed,
			  total_cases, passed_cases, metrics, context_snapshot, created_by, created_at, started_at, completed_at)
			 VALUES ($1,$2,$3,$4,$5,'succeeded',$6,$7,$8,$9,$10,$11,$12,$12,NOW())`,
			run.ID, string(run.Resource.Kind), run.Resource.ResourceID, run.Resource.RevisionID,
			run.SuiteRevisionID, run.Passed, run.TotalCases, run.PassedCases, string(metricsJSON),
			snapshotJSON, run.CreatedBy, run.CreatedAt,
		); err != nil {
			return fmt.Errorf("evaluation run repository: insert run: %w", err)
		}
		for _, result := range run.Results {
			if err := insertCaseResult(ctx, tx, run.ID, result); err != nil {
				return err
			}
		}
		return nil
	})
}

// insertCaseResult 在事务内写入一条 eval_case_results（spec §6.2/§6.5）：
// actual/dimensions/trace_evidence/tool_sequence 均 JSON 序列化，actual 与工具序列
// 落库前经 domain.SanitizeValue / domain.SanitizeTools 脱敏；JSON-null 分别回退到
// {} / [] 保持 round-trip 语义。
func insertCaseResult(ctx context.Context, tx pgx.Tx, runID string, result domain.EvalCaseResult) error {
	id := result.ID
	if id == "" {
		id = uuid.Must(uuid.NewV7()).String()
	}
	actualJSON, err := json.Marshal(domain.SanitizeValue(result.Actual))
	if err != nil {
		return fmt.Errorf("evaluation run repository: marshal actual output: %w", err)
	}
	dimensionsJSON, err := marshalJSONArray(result.Dimensions)
	if err != nil {
		return fmt.Errorf("evaluation run repository: marshal dimensions: %w", err)
	}
	traceJSON, err := json.Marshal(result.TraceEvidence)
	if err != nil {
		return fmt.Errorf("evaluation run repository: marshal trace evidence: %w", err)
	}
	toolSequenceJSON, err := marshalJSONArray(domain.SanitizeTools(result.Tools))
	if err != nil {
		return fmt.Errorf("evaluation run repository: marshal tool sequence: %w", err)
	}
	// 会话剧本逐轮证据（阶段 B）：nil Turns = 旧单轮 case，落库 '[]'。落库前经
	// sanitizeSessionTurns 脱敏——与单轮 actual/tool_sequence 同一道 spec §6.5 红线，
	// 防逐轮证据绕过敏感数据覆盖直落 JSONB。GetRun 读回 nil 与 omitempty 序列化自洽
	// （见 scanCaseResult 的 '[]' 守卫）。
	turnsJSON, err := marshalJSONArray(sanitizeSessionTurns(result.Turns))
	if err != nil {
		return fmt.Errorf("evaluation run repository: marshal turns: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO eval_case_results
		 (id, run_id, case_id, passed, actual_output, message, error_message, trace_id,
		  tokens, cost_usd, duration_ms, dimensions, failure_reason, trace_evidence,
		  process_pass, process_failure, tool_sequence, turns)
		 VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		id, runID, result.CaseID, result.Passed, string(actualJSON),
		result.Message, result.Error, result.TraceID, result.Tokens, result.CostUSD, result.DurationMs,
		string(dimensionsJSON), result.FailureReason, string(traceJSON),
		result.ProcessPass, result.ProcessFailure, string(toolSequenceJSON), string(turnsJSON),
	); err != nil {
		return fmt.Errorf("evaluation run repository: insert case result: %w", err)
	}
	return nil
}

// marshalJSONArray 序列化 JSON 数组列（dimensions/tool_sequence/turns）并做 nil
// 回退：json.Marshal 对 nil/空切片产出 "null"，统一落库 '[]'，与 scanCaseResult
// 读侧的 '[]' 守卫配合保持 nil ↔ '[]' round-trip 语义。
func marshalJSONArray(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return []byte("[]"), nil
	}
	return b, nil
}

// sanitizeSessionTurns 深拷贝会话逐轮证据并逐轮脱敏（Output → SanitizeValue、
// Tools → SanitizeTools），与单轮 actual/tool_sequence 落库前的脱敏保持一致；
// 不修改入参。空/ nil turns 原样返回（marshal "null" → '[]' 由调用方回退）。
func sanitizeSessionTurns(turns []domain.SessionTurnEvidence) []domain.SessionTurnEvidence {
	if len(turns) == 0 {
		return turns
	}
	out := make([]domain.SessionTurnEvidence, len(turns))
	for i, turn := range turns {
		out[i] = turn
		if sanitized, ok := domain.SanitizeValue(turn.Output).(string); ok {
			out[i].Output = sanitized
		}
		out[i].Tools = domain.SanitizeTools(turn.Tools)
	}
	return out
}

func (r *PgRunRepository) GetRun(
	ctx context.Context,
	tenantID, runID string,
) (domain.EvalRun, bool, error) {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	var run domain.EvalRun
	found := false
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var kind string
		var metricsJSON []byte
		var snapshotJSON []byte
		err := tx.QueryRow(ctx,
			`SELECT id, resource_kind, resource_id, revision_id, suite_revision_id,
			        passed, total_cases, passed_cases, metrics, context_snapshot, created_by, created_at
			 FROM eval_runs WHERE id=$1`, runID,
		).Scan(&run.ID, &kind, &run.Resource.ResourceID, &run.Resource.RevisionID,
			&run.SuiteRevisionID, &run.Passed, &run.TotalCases, &run.PassedCases, &metricsJSON,
			&snapshotJSON, &run.CreatedBy, &run.CreatedAt)
		if err == nil {
			if len(metricsJSON) > 0 {
				_ = json.Unmarshal(metricsJSON, &run.Metrics)
			}
			snap, derr := decodeContextSnapshot(snapshotJSON)
			if derr != nil {
				return derr
			}
			run.ContextSnapshot = snap
		}
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		run.Resource.Kind = domain.ResourceKind(kind)
		rows, err := tx.Query(ctx,
			`SELECT case_id, passed, actual_output, message, error_message, trace_id, tokens, cost_usd,
			        duration_ms, dimensions, failure_reason, trace_evidence,
			        process_pass, process_failure, tool_sequence, turns
			 FROM eval_case_results WHERE run_id=$1 ORDER BY created_at, id`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			result, err := scanCaseResult(rows)
			if err != nil {
				return err
			}
			run.Results = append(run.Results, result)
		}
		return rows.Err()
	})
	return run, found, err
}

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

// FindLatestCompletedRunForPlatformSeq 返回 tenant 下最近一条 completed run，其
// context_snapshot 中 groupKey 组 version_seq == seq。锚点可能在 evaluation 组
// （context_snapshot->'evaluation'）或 execution 数组（context_snapshot->'execution'，
// agent/trace/memory 组）——两处均匹配 group_key + version_seq。只读 run 行，不加载
// case results；无匹配 run → (nil, nil)。
func (r *PgRunRepository) FindLatestCompletedRunForPlatformSeq(
	ctx context.Context,
	tenantID, groupKey string,
	seq int64,
) (*domain.EvalRun, error) {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	var run *domain.EvalRun
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		run = &domain.EvalRun{}
		var kind string
		var metricsJSON []byte
		var snapshotJSON []byte
		err := tx.QueryRow(ctx,
			`SELECT id, resource_kind, resource_id, revision_id, suite_revision_id,
			        passed, total_cases, passed_cases, metrics, context_snapshot, created_by, created_at
			 FROM eval_runs
			 WHERE status='succeeded'
			   AND (
			     (context_snapshot->'evaluation'->>'group_key' = $1
			      AND (context_snapshot->'evaluation'->>'version_seq')::bigint = $2)
			     OR EXISTS (
			       SELECT 1 FROM jsonb_array_elements(context_snapshot->'execution') g
			       WHERE g->>'group_key' = $1 AND (g->>'version_seq')::bigint = $2)
			   )
			 ORDER BY created_at DESC, id DESC LIMIT 1`,
			groupKey, seq,
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

// decodeContextSnapshot 解码 eval_runs.context_snapshot JSONB：空/'{}'（旧 run
// 未捕获）→ (nil, nil)；损坏 JSON 显式报错而非静默丢快照。
func decodeContextSnapshot(raw []byte) (*domain.EvaluationContextSnapshot, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil, nil
	}
	var snap domain.EvaluationContextSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("evaluation run repository: unmarshal context snapshot: %w", err)
	}
	return &snap, nil
}

// scanCaseResult 从一行 eval_case_results 读出 case 结果，并把 JSON 列反序列化
// 到对应结构体字段（spec §6.2/§6.5/阶段 B turns）。空数组 '[]'（旧行/未捕获）跳过
// 解码，turns 保持 nil，与 omitempty 序列化自洽。
func scanCaseResult(row pgx.Row) (domain.EvalCaseResult, error) {
	var result domain.EvalCaseResult
	var actualJSON []byte
	var dimensionsJSON []byte
	var traceJSON []byte
	var toolSequenceJSON []byte
	var turnsJSON []byte
	if err := row.Scan(&result.CaseID, &result.Passed, &actualJSON, &result.Message, &result.Error,
		&result.TraceID, &result.Tokens, &result.CostUSD, &result.DurationMs,
		&dimensionsJSON, &result.FailureReason, &traceJSON,
		&result.ProcessPass, &result.ProcessFailure, &toolSequenceJSON, &turnsJSON); err != nil {
		return result, err
	}
	_ = json.Unmarshal(actualJSON, &result.Actual)
	if len(dimensionsJSON) > 0 {
		_ = json.Unmarshal(dimensionsJSON, &result.Dimensions)
	}
	if len(traceJSON) > 0 {
		_ = json.Unmarshal(traceJSON, &result.TraceEvidence)
	}
	if len(toolSequenceJSON) > 0 {
		_ = json.Unmarshal(toolSequenceJSON, &result.Tools)
	}
	if len(turnsJSON) > 0 && string(turnsJSON) != "[]" {
		_ = json.Unmarshal(turnsJSON, &result.Turns)
	}
	return result, nil
}

// GetRunCreatedBy 返回运行创建者；未命中 found=false。
func (r *PgRunRepository) GetRunCreatedBy(ctx context.Context, tenantID, runID string) (string, bool, error) {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	var createdBy string
	found := false
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT created_by FROM eval_runs WHERE id=$1`, runID).Scan(&createdBy)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return fmt.Errorf("evaluation run repository: load created by: %w", err)
		}
		found = true
		return nil
	})
	return createdBy, found, err
}

// DeleteRun 删除运行：被 optimization candidate 引用时拒绝（该 FK 为 SET NULL，
// 直接删会静默解绑候选，破坏语义）；否则事务内级联删除 case results 并写审计。
func (r *PgRunRepository) DeleteRun(
	ctx context.Context, tenantID, runID string, audit *auditdomain.ResourceChangeAuditEvent,
) error {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	return execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var referenced bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM optimization_candidates WHERE eval_run_id=$1)`, runID).Scan(&referenced); err != nil {
			return fmt.Errorf("evaluation run repository: check run references: %w", err)
		}
		if referenced {
			return domain.ErrEntityReferenced
		}
		tag, err := tx.Exec(ctx, `DELETE FROM eval_runs WHERE id=$1`, runID)
		if err != nil {
			return translateEntityReferenced(fmt.Errorf("evaluation run repository: delete run: %w", err))
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("evaluation run repository: delete run %s: not found", runID)
		}
		return insertChangeAudit(ctx, tx, audit)
	})
}
