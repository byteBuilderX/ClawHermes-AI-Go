package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5"
)

// PgReviewRepository 实现 port.ReviewRepository（tenant-scoped）。
type PgReviewRepository struct {
	pool poolIface
}

// 编译期断言：PgReviewRepository 满足 port.ReviewRepository。
var _ port.ReviewRepository = (*PgReviewRepository)(nil)

func NewPgReviewRepository(pool poolIface) *PgReviewRepository {
	return &PgReviewRepository{pool: pool}
}

func (r *PgReviewRepository) UpsertItem(ctx context.Context, tenantID string, item *domain.ReviewItem) (bool, error) {
	snapshotJSON, err := json.Marshal(item.Snapshot)
	if err != nil {
		return false, fmt.Errorf("marshal review snapshot: %w", err)
	}
	inserted := false
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err = execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx,
			`INSERT INTO eval_review_items
             (id, source_type, source_id, run_id, trace_id, resource_kind, resource_id,
              trigger_reason, snapshot, status, created_at)
             VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
             ON CONFLICT (source_type, source_id, trigger_reason) DO NOTHING`,
			item.ID, string(item.SourceType), item.SourceID, item.RunID, item.TraceID,
			string(item.ResourceKind), item.ResourceID, string(item.TriggerReason),
			string(snapshotJSON), string(item.Status), item.CreatedAt,
		)
		if execErr != nil {
			return fmt.Errorf("insert eval review item: %w", execErr)
		}
		inserted = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("upsert eval review item: %w", err)
	}
	return inserted, nil
}

func (r *PgReviewRepository) GetItem(ctx context.Context, tenantID, id string) (*domain.ReviewItem, error) {
	var (
		item                        domain.ReviewItem
		sourceType, trigger, status string
		resourceKind                string
		verdict                     string
		snapshotJSON                string
		reviewedAt                  *time.Time
	)
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, source_type, source_id, run_id, trace_id, resource_kind, resource_id,
                    trigger_reason, snapshot, status, human_verdict, reviewer, review_reason,
                    created_at, reviewed_at
             FROM eval_review_items WHERE id = $1`, id,
		).Scan(&item.ID, &sourceType, &item.SourceID, &item.RunID, &item.TraceID,
			&resourceKind, &item.ResourceID, &trigger, &snapshotJSON, &status,
			&verdict, &item.Reviewer, &item.ReviewReason, &item.CreatedAt, &reviewedAt)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get eval review item %s: %w", id, err)
	}
	item.SourceType = domain.ReviewSourceType(sourceType)
	item.ResourceKind = domain.ResourceKind(resourceKind)
	item.TriggerReason = domain.ReviewTriggerReason(trigger)
	item.RiskLevel = item.TriggerReason.RiskLevel()
	item.Status = domain.ReviewItemStatus(status)
	item.HumanVerdict = domain.HumanVerdict(verdict)
	item.ReviewedAt = reviewedAt
	if err := json.Unmarshal([]byte(snapshotJSON), &item.Snapshot); err != nil {
		return nil, fmt.Errorf("get eval review item %s: decode snapshot: %w", id, err)
	}
	return &item, nil
}

func (r *PgReviewRepository) ListItems(
	ctx context.Context, tenantID string, f port.ReviewFilter,
) ([]domain.ReviewItem, int64, error) {
	conds, args := reviewFilterConds(f)
	countSQL := `SELECT COUNT(*) FROM eval_review_items` + conds
	listSQL := `SELECT id, source_type, source_id, run_id, trace_id, resource_kind, resource_id,
                       trigger_reason, snapshot, status, human_verdict, reviewer, review_reason,
                       created_at, reviewed_at
                FROM eval_review_items` + conds +
		fmt.Sprintf(` ORDER BY %s, created_at DESC LIMIT $%d OFFSET $%d`,
			reviewRiskOrderSQL(), len(args)+1, len(args)+2)

	limit := f.Limit
	if limit <= 0 {
		limit = constants.DefaultPageSize
	}
	if limit > constants.MaxPageSize {
		limit = constants.MaxPageSize
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	queryArgs := append(append([]any{}, args...), limit, f.Offset)

	var (
		items                       []domain.ReviewItem
		total                       int64
		sourceType, trigger, status string
		resourceKind                string
		verdict                     string
		snapshotJSON                string
		reviewedAt                  *time.Time
	)
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, execErr := tx.Query(ctx, listSQL, queryArgs...)
		if execErr != nil {
			return fmt.Errorf("list eval review items: %w", execErr)
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.ReviewItem
			if scanErr := rows.Scan(&item.ID, &sourceType, &item.SourceID, &item.RunID, &item.TraceID,
				&resourceKind, &item.ResourceID, &trigger, &snapshotJSON, &status,
				&verdict, &item.Reviewer, &item.ReviewReason, &item.CreatedAt, &reviewedAt); scanErr != nil {
				return fmt.Errorf("scan eval review item: %w", scanErr)
			}
			item.SourceType = domain.ReviewSourceType(sourceType)
			item.ResourceKind = domain.ResourceKind(resourceKind)
			item.TriggerReason = domain.ReviewTriggerReason(trigger)
			item.RiskLevel = item.TriggerReason.RiskLevel()
			item.Status = domain.ReviewItemStatus(status)
			item.HumanVerdict = domain.HumanVerdict(verdict)
			item.ReviewedAt = reviewedAt
			if decodeErr := json.Unmarshal([]byte(snapshotJSON), &item.Snapshot); decodeErr != nil {
				return fmt.Errorf("decode review snapshot: %w", decodeErr)
			}
			items = append(items, item)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return fmt.Errorf("iterate eval review items: %w", rowsErr)
		}
		return tx.QueryRow(ctx, countSQL, args...).Scan(&total)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list eval review items: %w", err)
	}
	if items == nil {
		items = []domain.ReviewItem{}
	}
	return items, total, nil
}

// reviewFilterConds 把 ReviewFilter 编译为 WHERE 子句与绑定参数；list 与 count
// 两段 SQL 复用同一条件，保证总数与行集口径一致。
func reviewFilterConds(f port.ReviewFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Status != "" {
		conds = append(conds, fmt.Sprintf(" status = $%d", len(args)+1))
		args = append(args, string(f.Status))
	}
	if f.TriggerReason != "" {
		conds = append(conds, fmt.Sprintf(" trigger_reason = $%d", len(args)+1))
		args = append(args, string(f.TriggerReason))
	}
	if f.ResourceKind != "" {
		conds = append(conds, fmt.Sprintf(" resource_kind = $%d", len(args)+1))
		args = append(args, f.ResourceKind)
	}
	if f.ResourceID != "" {
		conds = append(conds, fmt.Sprintf(" resource_id = $%d", len(args)+1))
		args = append(args, f.ResourceID)
	}
	if len(conds) == 0 {
		return ` WHERE 1=1`, args
	}
	return ` WHERE` + strings.Join(conds, " AND"), args
}

// reviewRiskOrderSQL 是评审池风险优先排序表达式（spec §6.6 规模控制：评审池按风险排序，
// 安全/写操作/高危资源优先）。与 domain.ReviewTriggerReason.RiskLevel() 保持镜像：
// high=0、medium=1、low=2；同风险按 created_at DESC（维持既有最新优先）。修改 RiskLevel()
// 必须同步本表达式（两端注释互指）。
func reviewRiskOrderSQL() string {
	return `CASE trigger_reason WHEN 'judge_rule_conflict' THEN 0 WHEN 'process_output_conflict' THEN 0 WHEN 'low_confidence' THEN 1 WHEN 'dimension_split' THEN 1 WHEN 'needs_review' THEN 1 WHEN 'behavior_anomaly' THEN 1 WHEN 'trajectory_failed' THEN 1 ELSE 2 END`
}

func (r *PgReviewRepository) MarkReviewed(
	ctx context.Context, tenantID, id string, verdict domain.HumanVerdict, reviewer, reason string,
) error {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx,
			`UPDATE eval_review_items
			 SET status = 'reviewed', human_verdict = $2, reviewer = $3, review_reason = $4, reviewed_at = NOW()
			 WHERE id = $1 AND status = 'pending'`,
			id, string(verdict), reviewer, reason,
		)
		if execErr != nil {
			return fmt.Errorf("mark eval review item reviewed: %w", execErr)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("eval review item %s not pending (or missing)", id)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mark eval review item reviewed: %w", err)
	}
	return nil
}

func (r *PgReviewRepository) CreateCalibrationSample(
	ctx context.Context, tenantID string, s *domain.CalibrationSample,
) error {
	signalsJSON, err := json.Marshal(s.Signals)
	if err != nil {
		return fmt.Errorf("marshal calibration signals: %w", err)
	}
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err = execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx,
			`INSERT INTO eval_calibration_samples
			 (id, review_item_id, source_type, source_id, judge_model, signals, human_verdict, reviewer, reason, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			s.ID, s.ReviewItemID, string(s.SourceType), s.SourceID, s.JudgeModel,
			string(signalsJSON), string(s.HumanVerdict), s.Reviewer, s.Reason, s.CreatedAt,
		)
		if execErr != nil {
			return fmt.Errorf("insert calibration sample: %w", execErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create calibration sample: %w", err)
	}
	return nil
}

func (r *PgReviewRepository) CreateAttributionEntry(
	ctx context.Context, tenantID string, e *domain.AttributionEntry,
) error {
	snapshotJSON, err := json.Marshal(e.Snapshot)
	if err != nil {
		return fmt.Errorf("marshal attribution snapshot: %w", err)
	}
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err = execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx,
			`INSERT INTO eval_attribution_entries
			 (id, review_item_id, source_type, source_id, resource_kind, resource_id,
			  dimension, snapshot, status, reviewer, reason, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			e.ID, e.ReviewItemID, string(e.SourceType), e.SourceID,
			string(e.ResourceKind), e.ResourceID, e.Dimension,
			string(snapshotJSON), e.Status, e.Reviewer, e.Reason, e.CreatedAt,
		)
		if execErr != nil {
			return fmt.Errorf("insert attribution entry: %w", execErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create attribution entry: %w", err)
	}
	return nil
}

func (r *PgReviewRepository) CountPending(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM eval_review_items WHERE status = 'pending'`).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("count pending eval review items: %w", err)
	}
	return n, nil
}

// GetReviewItemCreatedBy 返回审查项创建者；系统入池项恒 ”（仅 owner 可删）。
func (r *PgReviewRepository) GetReviewItemCreatedBy(ctx context.Context, tenantID, reviewID string) (string, bool, error) {
	var createdBy string
	found := false
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT created_by FROM eval_review_items WHERE id=$1`, reviewID).Scan(&createdBy)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get eval review item: load created by: %w", err)
		}
		found = true
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("get eval review item %s: %w", reviewID, err)
	}
	return createdBy, found, nil
}

// DeleteReviewItem 删除审查项：calibration/attribution 级联删除，同事务写审计。
func (r *PgReviewRepository) DeleteReviewItem(
	ctx context.Context, tenantID, reviewID string, audit *auditdomain.ResourceChangeAuditEvent,
) error {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	err := execTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `DELETE FROM eval_review_items WHERE id=$1`, reviewID)
		if execErr != nil {
			return translateEntityReferenced(fmt.Errorf("delete eval review item: %w", execErr))
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("eval review item %s not found", reviewID)
		}
		return insertChangeAudit(ctx, tx, audit)
	})
	if err != nil {
		return fmt.Errorf("delete eval review item: %w", err)
	}
	return nil
}
