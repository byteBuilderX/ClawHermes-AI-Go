package persistence

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/pashagolub/pgxmock/v2"
)

// reviewItem 是测试用评审条目（不变字段集中定义，各测试只覆盖关注点）。
func reviewItem(id, sourceType, sourceID string, reason domain.ReviewTriggerReason) *domain.ReviewItem {
	return &domain.ReviewItem{
		ID:            id,
		SourceType:    domain.ReviewSourceType(sourceType),
		SourceID:      sourceID,
		RunID:         "run-" + sourceID,
		TraceID:       "t-" + sourceID,
		ResourceKind:  domain.ResourceKindSkill,
		ResourceID:    "s1",
		TriggerReason: reason,
		Snapshot:      map[string]any{"note": "x"},
		Status:        domain.ReviewStatusPending,
		CreatedAt:     time.Now().UTC(),
	}
}

func TestPgReviewRepositoryUpsertItemIdempotent(t *testing.T) {
	mock := newMockRepo(t)
	repo := NewPgReviewRepository(mock)
	item := reviewItem("ri-1", "observation", "obs-1", domain.TriggerLowConfidence)

	// 第一次：同 key 无冲突，RowsAffected=1 → inserted=true。
	expectTenantTx(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO eval_review_items`)).
		WithArgs("ri-1", "observation", "obs-1", "run-obs-1", "t-obs-1", "skill", "s1",
			"low_confidence", pgxmock.AnyArg(), "pending", item.CreatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	inserted, err := repo.UpsertItem(context.Background(), "t1", item)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !inserted {
		t.Fatal("want inserted on first upsert")
	}

	// 第二次：DB 对同 (source_type, source_id, trigger_reason) ON CONFLICT DO NOTHING
	// 返回 RowsAffected=0 → inserted=false（幂等）。
	expectTenantTx(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO eval_review_items`)).
		WithArgs("ri-1", "observation", "obs-1", "run-obs-1", "t-obs-1", "skill", "s1",
			"low_confidence", pgxmock.AnyArg(), "pending", item.CreatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectCommit()
	again, err := repo.UpsertItem(context.Background(), "t1", item)
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if again {
		t.Fatal("want no insert on duplicate key")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestPgReviewRepositoryGetItem(t *testing.T) {
	mock := newMockRepo(t)
	repo := NewPgReviewRepository(mock)
	expectTenantTx(mock)
	rows := pgxmock.NewRows([]string{"id", "source_type", "source_id", "run_id", "trace_id",
		"resource_kind", "resource_id", "trigger_reason", "snapshot", "status",
		"human_verdict", "reviewer", "review_reason", "created_at", "reviewed_at"}).
		AddRow("ri-1", "observation", "obs-1", "run-obs-1", "t-obs-1", "skill", "s1",
			"low_confidence", `{"note":"x"}`, "pending", "", "", "", time.Now().UTC(), nil)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, source_type, source_id, run_id, trace_id, resource_kind, resource_id, trigger_reason, snapshot, status, human_verdict, reviewer, review_reason, created_at, reviewed_at FROM eval_review_items WHERE id = $1`)).
		WithArgs("ri-1").
		WillReturnRows(rows)
	mock.ExpectCommit()

	got, err := repo.GetItem(context.Background(), "t1", "ri-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.TriggerReason != domain.TriggerLowConfidence || got.Status != domain.ReviewStatusPending {
		t.Fatalf("unexpected item: %+v", got)
	}
	if got.RiskLevel != domain.ReviewRiskMedium {
		t.Fatalf("item risk = %v, want medium (low_confidence)", got.RiskLevel)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestPgReviewRepositoryMarkReviewedAndCountPending(t *testing.T) {
	mock := newMockRepo(t)
	repo := NewPgReviewRepository(mock)
	item := reviewItem("ri-2", "case_result", "cr-2", domain.TriggerNeedsReview)

	expectTenantTx(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO eval_review_items`)).
		WithArgs("ri-2", "case_result", "cr-2", "run-cr-2", "t-cr-2", "skill", "s1",
			"needs_review", pgxmock.AnyArg(), "pending", item.CreatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	if _, err := repo.UpsertItem(context.Background(), "t1", item); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	expectTenantTx(mock)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM eval_review_items WHERE status = 'pending'`)).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectCommit()
	n, err := repo.CountPending(context.Background(), "t1")
	if err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}

	expectTenantTx(mock)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE eval_review_items`)).
		WithArgs("ri-2", "fail", "reviewer@x", "错误输出").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	if err := repo.MarkReviewed(context.Background(), "t1", item.ID, domain.HumanVerdictFail, "reviewer@x", "错误输出"); err != nil {
		t.Fatalf("mark reviewed: %v", err)
	}

	expectTenantTx(mock)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM eval_review_items WHERE status = 'pending'`)).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectCommit()
	n, err = repo.CountPending(context.Background(), "t1")
	if err != nil {
		t.Fatalf("count pending after review: %v", err)
	}
	if n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestPgReviewRepositoryListItems(t *testing.T) {
	mock := newMockRepo(t)
	repo := NewPgReviewRepository(mock)
	now := time.Now().UTC()

	// ListItems 无 filter：list 查询 + count 查询。created_at DESC 排序由 SQL 负责。
	expectTenantTx(mock)
	listRows := pgxmock.NewRows([]string{"id", "source_type", "source_id", "run_id", "trace_id",
		"resource_kind", "resource_id", "trigger_reason", "snapshot", "status",
		"human_verdict", "reviewer", "review_reason", "created_at", "reviewed_at"}).
		AddRow("ri-1", "observation", "obs-1", "run-obs-1", "t-obs-1", "skill", "s1",
			"low_confidence", `{"note":"x"}`, "pending", "", "", "", now, nil).
		AddRow("ri-2", "case_result", "cr-2", "run-cr-2", "t-cr-2", "skill", "s1",
			"dimension_split", `{"note":"x"}`, "pending", "", "", "", now, nil)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, source_type, source_id, run_id, trace_id, resource_kind, resource_id, trigger_reason, snapshot, status, human_verdict, reviewer, review_reason, created_at, reviewed_at FROM eval_review_items WHERE 1=1 ORDER BY CASE trigger_reason WHEN 'judge_rule_conflict' THEN 0 WHEN 'process_output_conflict' THEN 0 WHEN 'low_confidence' THEN 1 WHEN 'dimension_split' THEN 1 WHEN 'needs_review' THEN 1 WHEN 'behavior_anomaly' THEN 1 WHEN 'trajectory_failed' THEN 1 ELSE 2 END, created_at DESC LIMIT $1 OFFSET $2`)).
		WithArgs(10, 0).
		WillReturnRows(listRows)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM eval_review_items WHERE 1=1`)).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectCommit()

	items, total, err := repo.ListItems(context.Background(), "t1", port.ReviewFilter{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2", total, len(items))
	}
	if items[0].TriggerReason != domain.TriggerLowConfidence {
		t.Fatalf("first item reason = %v, want low_confidence", items[0].TriggerReason)
	}
	if items[0].RiskLevel != domain.ReviewRiskMedium {
		t.Fatalf("first item risk = %v, want medium (low_confidence)", items[0].RiskLevel)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestPgReviewRepositoryCreateCalibrationSample(t *testing.T) {
	mock := newMockRepo(t)
	repo := NewPgReviewRepository(mock)
	sample := &domain.CalibrationSample{
		ID:           "cs-1",
		ReviewItemID: "ri-1",
		SourceType:   domain.ReviewSourceObservation,
		SourceID:     "obs-1",
		JudgeModel:   "judge-v3",
		Signals:      map[string]any{"judge": []map[string]any{{"dimension": "faithfulness", "score": 0.4}}},
		HumanVerdict: domain.HumanVerdictJudgeMisjudgment,
		Reviewer:     "reviewer@x",
		Reason:       "误判",
		CreatedAt:    time.Now().UTC(),
	}

	expectTenantTx(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO eval_calibration_samples`)).
		WithArgs("cs-1", "ri-1", "observation", "obs-1", "judge-v3", pgxmock.AnyArg(),
			"judge_misjudgment", "reviewer@x", "误判", sample.CreatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	if err := repo.CreateCalibrationSample(context.Background(), "t1", sample); err != nil {
		t.Fatalf("create calibration sample: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestPgReviewRepositoryCreateAttributionEntry(t *testing.T) {
	mock := newMockRepo(t)
	repo := NewPgReviewRepository(mock)
	entry := &domain.AttributionEntry{
		ID:           "ae-1",
		ReviewItemID: "ri-1",
		SourceType:   domain.ReviewSourceCaseResult,
		SourceID:     "cr-1",
		ResourceKind: domain.ResourceKindSkill,
		ResourceID:   "s1",
		Dimension:    "faithfulness",
		Snapshot:     map[string]any{"note": "x"},
		Status:       "open",
		Reviewer:     "reviewer@x",
		Reason:       "输出错误",
		CreatedAt:    time.Now().UTC(),
	}

	expectTenantTx(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO eval_attribution_entries`)).
		WithArgs("ae-1", "ri-1", "case_result", "cr-1", "skill", "s1",
			"faithfulness", pgxmock.AnyArg(), "open", "reviewer@x", "输出错误", entry.CreatedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	if err := repo.CreateAttributionEntry(context.Background(), "t1", entry); err != nil {
		t.Fatalf("create attribution entry: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}
