package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v2"
	"github.com/stretchr/testify/require"
)

func TestPgRunRepository_SaveRun_success(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}
	now := time.Now()
	run := domain.EvalRun{
		ID:              "run-1",
		Resource:        domain.ResourceRef{Kind: "prompt", ResourceID: "r-1", RevisionID: "rev-1"},
		SuiteRevisionID: "suite-rev-1",
		Passed:          true,
		TotalCases:      2,
		PassedCases:     2,
		Metrics:         map[string]any{"pass_rate": 1.0, "total_tokens": 10},
		CreatedAt:       now,
		Results: []domain.EvalCaseResult{
			{CaseID: "case-1", Passed: true, Actual: map[string]any{"token": "leak"}, Message: "ok"},
			{CaseID: "case-2", Passed: true, Actual: "plain", Tokens: 10, CostUSD: 0.5, DurationMs: 3,
				Dimensions:    []domain.DimensionScore{{Name: "correctness", Score: 1, Passed: true, Reason: "ok"}},
				FailureReason: "assert failed",
				TraceEvidence: &domain.ObservedTraceEvidence{
					CostUSD: 0.2, LatencyMs: 150, Success: true, ToolCallCount: 3, ToolErrorCount: 1,
				}},
		},
	}

	expectTenantTx(mock)
	mock.ExpectExec("INSERT INTO eval_runs").
		WithArgs("run-1", "prompt", "r-1", "rev-1", "suite-rev-1", true, 2, 2,
			`{"pass_rate":1,"total_tokens":10}`, "{}", "", now).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO eval_case_results").
		WithArgs(pgxmock.AnyArg(), "run-1", "case-1", true, `{"token":"[REDACTED]"}`, "ok", "", "",
			0, 0.0, 0, "[]", "", "null", false, "", "[]", "[]").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO eval_case_results").
		WithArgs(pgxmock.AnyArg(), "run-1", "case-2", true, `"plain"`, "", "", "", 10, 0.5, 3,
			`[{"name":"correctness","score":1,"passed":true,"reason":"ok"}]`, "assert failed",
			`{"cost_usd":0.2,"latency_ms":150,"success":true,`+
				`"security_violation":false,"tool_call_count":3,"tool_error_count":1}`,
			false, "", "[]", "[]").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, repo.SaveRun(context.Background(), "t1", run))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgRunRepository_SaveRun_insertRunFails(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}
	run := domain.EvalRun{ID: "run-1", CreatedAt: time.Now()}

	expectTenantTx(mock)
	mock.ExpectExec("INSERT INTO eval_runs").
		WithArgs("run-1", "prompt", "r-1", "rev-1", "s-1", false, 0, 0, pgxmock.AnyArg(), pgxmock.AnyArg(), "", pgxmock.AnyArg()).
		WillReturnError(errors.New("duplicate key"))
	mock.ExpectRollback()

	err := repo.SaveRun(context.Background(), "t1", run)
	require.Error(t, err)
	require.ErrorContains(t, err, "insert run")
}

func TestPgRunRepository_SaveRun_marshalResultFails(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}
	run := domain.EvalRun{
		ID:              "run-1",
		Resource:        domain.ResourceRef{Kind: "prompt", ResourceID: "r-1", RevisionID: "rev-1"},
		SuiteRevisionID: "s-1",
		CreatedAt:       time.Now(),
		Results: []domain.EvalCaseResult{
			{CaseID: "case-1", Actual: make(chan int)}, // not JSON-marshalable
		},
	}

	expectTenantTx(mock)
	mock.ExpectExec("INSERT INTO eval_runs").
		WithArgs("run-1", "prompt", "r-1", "rev-1", "s-1", false, 0, 0, pgxmock.AnyArg(), pgxmock.AnyArg(), "", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectRollback()

	err := repo.SaveRun(context.Background(), "t1", run)
	require.Error(t, err)
	require.ErrorContains(t, err, "marshal actual output")
}

func TestPgRunRepository_SaveRun_insertResultFails(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}
	run := domain.EvalRun{
		ID:              "run-1",
		Resource:        domain.ResourceRef{Kind: "prompt", ResourceID: "r-1", RevisionID: "rev-1"},
		SuiteRevisionID: "s-1",
		CreatedAt:       time.Now(),
		Results:         []domain.EvalCaseResult{{CaseID: "case-1", Actual: "x"}},
	}

	expectTenantTx(mock)
	mock.ExpectExec("INSERT INTO eval_runs").
		WithArgs("run-1", "prompt", "r-1", "rev-1", "s-1", false, 0, 0, pgxmock.AnyArg(), pgxmock.AnyArg(), "", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO eval_case_results").
		WithArgs(pgxmock.AnyArg(), "run-1", "case-1", false, `"x"`, "", "", "", 0, 0.0, 0, "[]", "", "null",
			false, "", "[]", "[]").
		WillReturnError(errors.New("foreign key violation"))
	mock.ExpectRollback()

	err := repo.SaveRun(context.Background(), "t1", run)
	require.Error(t, err)
	require.ErrorContains(t, err, "insert case result")
}

func TestPgRunRepository_GetRun_found(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("run-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "suite_revision_id",
			"passed", "total_cases", "passed_cases", "metrics", "context_snapshot", "created_by", "created_at",
		}).AddRow("run-1", "prompt", "r-1", "rev-1", "s-1", true, 1, 1, []byte(`{"pass_rate":1.0}`), []byte("{}"), "creator-1", now))
	mock.ExpectQuery("SELECT case_id, passed, actual_output").
		WithArgs("run-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"case_id", "passed", "actual_output", "message", "error_message", "trace_id",
			"tokens", "cost_usd", "duration_ms", "dimensions", "failure_reason", "trace_evidence",
			"process_pass", "process_failure", "tool_sequence", "turns",
		}).AddRow("case-1", true, []byte(`{"ok":true}`), "m", "e", "tr-1", 5, 0.1, 2,
			[]byte(`[{"name":"faithfulness","score":0.9,"passed":true}]`), "assert failed", []byte("null"),
			false, "", []byte("[]"), []byte("[]")))
	mock.ExpectCommit()

	run, found, err := repo.GetRun(context.Background(), "t1", "run-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "r-1", run.Resource.ResourceID)
	require.Equal(t, domain.ResourceKind("prompt"), run.Resource.Kind)
	require.Len(t, run.Results, 1)
	require.Equal(t, "tr-1", run.Results[0].TraceID)
	require.Equal(t, []domain.DimensionScore{{Name: "faithfulness", Score: 0.9, Passed: true}},
		run.Results[0].Dimensions)
	require.Equal(t, "assert failed", run.Results[0].FailureReason)
	require.Nil(t, run.Results[0].TraceEvidence)
	require.Nil(t, run.Results[0].Turns) // 旧行 turns='[]'（未捕获）解码保持 nil
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgRunRepository_GetRun_notFound(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("missing").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectCommit()

	run, found, err := repo.GetRun(context.Background(), "t1", "missing")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, run.Results)
	require.NoError(t, mock.ExpectationsWereMet())
}

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
