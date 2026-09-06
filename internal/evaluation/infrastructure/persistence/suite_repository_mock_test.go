package persistence

import (
	"context"
	"errors"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v2"
	"github.com/stretchr/testify/require"
)

func newSuiteRevision() domain.EvalSuiteRevision {
	return domain.EvalSuiteRevision{
		ID:           "rev-1",
		SuiteID:      "suite-1",
		ParentID:     "parent-1",
		VersionNo:    2,
		Status:       domain.SuiteRevisionDraft,
		ResourceKind: domain.ResourceKind("prompt"),
		Cases: []domain.EvalCase{
			{ID: "case-1", Name: "c1", Input: map[string]any{"q": "hi"}, ExpectedOutput: "ok", AssertionMode: "exact", Enabled: true},
		},
	}
}

func TestPgSuiteRepository_CreateSuite_success(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}
	revision := newSuiteRevision()

	expectTenantTx(mock)
	mock.ExpectExec("INSERT INTO eval_suites").
		WithArgs("suite-1", "My Suite", "desc", "rev-1", "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO eval_suite_revisions").
		WithArgs("rev-1", "suite-1", "parent-1", 2, "draft", "prompt", "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO eval_cases").
		WithArgs("case-1", "rev-1", "c1", `{"q":"hi"}`, `"ok"`, "exact", true, `{}`, `{}`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, repo.CreateSuite(context.Background(), "t1", domain.EvalSuite{ID: "suite-1", Name: "My Suite", Description: "desc"}, revision))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSuiteRepository_CreateSuite_insertSuiteFails(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectExec("INSERT INTO eval_suites").
		WithArgs("suite-1", "", "", "", "").
		WillReturnError(errors.New("duplicate key"))
	mock.ExpectRollback()

	err := repo.CreateSuite(context.Background(), "t1", domain.EvalSuite{ID: "suite-1"}, domain.EvalSuiteRevision{})
	require.Error(t, err)
	require.ErrorContains(t, err, "insert suite")
}

func TestPgSuiteRepository_CreateSuite_unmarshalableCaseInputFails(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}
	revision := newSuiteRevision()
	revision.Cases[0].Input = make(chan int) // not JSON-marshalable

	expectTenantTx(mock)
	mock.ExpectExec("INSERT INTO eval_suites").
		WithArgs("suite-1", "", "", "rev-1", "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO eval_suite_revisions").
		WithArgs("rev-1", "suite-1", "parent-1", 2, "draft", "prompt", "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectRollback()

	err := repo.CreateSuite(context.Background(), "t1", domain.EvalSuite{ID: "suite-1"}, revision)
	require.Error(t, err)
	require.ErrorContains(t, err, "marshal input")
}

func TestPgSuiteRepository_GetDraftRevision_found(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, suite_id, COALESCE\\(parent_id").
		WithArgs("suite-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "suite_id", "parent_id", "version_no", "status", "resource_kind", "created_by",
		}).AddRow("rev-1", "suite-1", "", 3, "draft", "prompt", ""))
	mock.ExpectQuery("SELECT id, name, input, expected_output, assertion_mode, enabled, session, evaluator_config").
		WithArgs("rev-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "input", "expected_output", "assertion_mode", "enabled", "session", "evaluator_config",
		}).AddRow("case-1", "c1", []byte(`{"q":1}`), []byte(`"ok"`), "contains", true, []byte(`{}`), []byte(nil)))
	mock.ExpectCommit()

	revision, found, err := repo.GetDraftRevision(context.Background(), "t1", "suite-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "rev-1", revision.ID)
	require.Len(t, revision.Cases, 1)
	require.Equal(t, "contains", string(revision.Cases[0].AssertionMode))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSuiteRepository_GetDraftRevision_notFound(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, suite_id, COALESCE\\(parent_id").
		WithArgs("missing").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectCommit()

	revision, found, err := repo.GetDraftRevision(context.Background(), "t1", "missing")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, revision.Cases)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSuiteRepository_GetRevision_invalidCaseJSON(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, suite_id, COALESCE\\(parent_id").
		WithArgs("rev-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "suite_id", "parent_id", "version_no", "status", "resource_kind", "created_by",
		}).AddRow("rev-1", "suite-1", "", 3, "published", "prompt", ""))
	mock.ExpectQuery("SELECT id, name, input, expected_output, assertion_mode, enabled, session, evaluator_config").
		WithArgs("rev-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "input", "expected_output", "assertion_mode", "enabled", "session", "evaluator_config",
		}).AddRow("case-1", "c1", []byte(`{bad`), []byte(`"ok"`), "contains", true, []byte(`{}`), []byte(nil)))
	mock.ExpectCommit()

	// loadSuiteRevision ignores per-case decode errors; revision is still returned.
	revision, found, err := repo.GetRevision(context.Background(), "t1", "rev-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.SuiteRevisionPublished, revision.Status)
	require.Len(t, revision.Cases, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSuiteRepository_NextVersionNo(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(version_no\\)").
		WithArgs("suite-1").
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(4))
	mock.ExpectCommit()

	next, err := repo.NextVersionNo(context.Background(), "t1", "suite-1")
	require.NoError(t, err)
	require.Equal(t, 4, next)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSuiteRepository_PublishRevision_success(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}

	expectTenantTx(mock)
	// S1-1：事务首行按 suite 加 advisory 锁串行化并发 publish。
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("suite-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	mock.ExpectExec("UPDATE eval_suite_revisions").
		WithArgs("rev-1", "suite-1", 5).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// 发布后 reload 刚置为 published 的 revision 及其 cases，用于返回 + 播种继承草稿。
	mock.ExpectQuery("SELECT id, suite_id, COALESCE\\(parent_id").
		WithArgs("rev-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "suite_id", "parent_id", "version_no", "status", "resource_kind", "created_by",
		}).AddRow("rev-1", "suite-1", "", 5, "published", "prompt", ""))
	mock.ExpectQuery("SELECT id, name, input, expected_output, assertion_mode, enabled, session, evaluator_config").
		WithArgs("rev-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "input", "expected_output", "assertion_mode", "enabled", "session", "evaluator_config",
		}).AddRow("case-1", "c1", []byte(`{"q":1}`), []byte(`"ok"`), "contains", true, []byte(`{}`), []byte(`{}`)))
	// 自动开启继承草稿：id 随机（新 revision），kind/created_by 继承自刚发布 revision。
	mock.ExpectExec("INSERT INTO eval_suite_revisions").
		WithArgs(pgxmock.AnyArg(), "suite-1", "rev-1", "draft", "prompt", "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// 继承草稿把 published revision 的全部 case 以全新 uuid 拷贝。手写规则 case 的
	// evaluator_config 存的是 '{}'；读回时 ApplyConfig 的 bare-JudgeSpec 兜底把 '{}'
	// 解析成空 JudgeSpec，重写后即为 {'judge_spec':{}}（内容无差别的既有行为）。
	mock.ExpectExec("INSERT INTO eval_cases").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "c1", `{"q":1}`, `"ok"`, "contains", true, `{}`, `{"judge_spec":{}}`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE eval_suites SET active_revision_id").
		WithArgs("suite-1", "rev-1", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	revision, err := repo.PublishRevision(context.Background(), "t1", "suite-1", "rev-1", 5)
	require.NoError(t, err)
	require.Equal(t, "rev-1", revision.ID)
	require.Equal(t, domain.SuiteRevisionPublished, revision.Status)
	require.Len(t, revision.Cases, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSuiteRepository_PublishRevision_draftMissing(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgSuiteRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("suite-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	mock.ExpectExec("UPDATE eval_suite_revisions").
		WithArgs("rev-1", "suite-1", 5).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	_, err := repo.PublishRevision(context.Background(), "t1", "suite-1", "rev-1", 5)
	require.Error(t, err)
	require.ErrorContains(t, err, "draft revision not found")
}
