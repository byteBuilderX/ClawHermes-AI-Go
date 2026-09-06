package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v2"
	"github.com/stretchr/testify/require"
)

func TestCursorValues_empty(t *testing.T) {
	ct, cid, err := cursorValues("")
	require.NoError(t, err)
	require.Nil(t, ct)
	require.Nil(t, cid)
}

func TestCursorValues_invalid(t *testing.T) {
	_, _, err := cursorValues("not-base64!!")
	require.ErrorIs(t, err, domain.ErrInvalidCenterQuery)
}

func TestCursorValues_valid(t *testing.T) {
	raw := domain.EncodeCenterCursor(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "rev-1")
	ct, cid, err := cursorValues(raw)
	require.NoError(t, err)
	require.NotNil(t, ct)
	require.Equal(t, "rev-1", *cid)
}

func TestTimelineCursorValues_missingQualifier(t *testing.T) {
	raw := domain.EncodeCenterCursor(time.Now(), "rev-1")
	_, _, _, err := timelineCursorValues(raw)
	require.ErrorIs(t, err, domain.ErrInvalidCenterQuery)
}

func TestTimelineCursorValues_emptyParts(t *testing.T) {
	raw := domain.EncodeCenterCursor(time.Now(), "id\x00")
	_, _, _, err := timelineCursorValues(raw)
	require.ErrorIs(t, err, domain.ErrInvalidCenterQuery)
}

func TestTimelineCursorValues_valid(t *testing.T) {
	raw := domain.EncodeCenterCursor(time.Now(), "rev-1\x00revision")
	ct, id, kind, err := timelineCursorValues(raw)
	require.NoError(t, err)
	require.NotNil(t, ct)
	require.Equal(t, "rev-1", *id)
	require.Equal(t, "revision", *kind)
}

func TestPgCenterQueryRepository_Overview_success(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("COUNT\\(DISTINCT").
		WillReturnRows(pgxmock.NewRows([]string{
			"resources", "suites", "runs", "candidates", "experiments",
		}).AddRow(3, 2, 5, 1, 4))
	mock.ExpectCommit()

	overview, err := repo.Overview(context.Background(), "t1")
	require.NoError(t, err)
	require.Equal(t, domain.CenterOverview{Resources: 3, Suites: 2, Runs: 5, Candidates: 1, Experiments: 4}, overview)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_ListResources_paginated(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("WITH latest AS").
		WithArgs("", "", "", (*time.Time)(nil), (*string)(nil), 3).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "status", "safe_summary", "created_at",
			"stable_revision_id", "latest_run_status",
		}).
			AddRow("rev-3", "prompt", "r-1", "published", []byte(`{"name":"v3"}`), now.Add(-1*time.Hour), "rev-3", "succeeded").
			AddRow("rev-2", "prompt", "r-1", "published", []byte(`{"name":"v2","password":"x"}`), now.Add(-2*time.Hour), "rev-3", "").
			AddRow("rev-1", "prompt", "r-1", "draft", []byte(`{bad`), now.Add(-3*time.Hour), "", ""))
	mock.ExpectCommit()

	page, err := repo.ListResources(context.Background(), "t1", port.CenterFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.NotEmpty(t, page.NextCursor)
	require.Equal(t, "published", page.Items[0].Status)
	require.Equal(t, "succeeded", page.Items[0].LatestRunStatus)
	require.Equal(t, "v3", page.Items[0].SafeSummary["name"])
	require.Equal(t, "v2", page.Items[1].SafeSummary["name"])
	require.NotContains(t, page.Items[1].SafeSummary, "password")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_ListResources_invalidCursor(t *testing.T) {
	repo := &PgCenterQueryRepository{pool: newMockRepo(t)}

	_, err := repo.ListResources(context.Background(), "t1", port.CenterFilter{Cursor: "bad!"})
	require.ErrorIs(t, err, domain.ErrInvalidCenterQuery)
}

func TestPgCenterQueryRepository_ListSuites_paginated(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT s.id,s.name").
		WithArgs("", "", "", (*time.Time)(nil), (*string)(nil), 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "description", "resource_kind", "status", "created_by", "created_at",
			"active_revision_id", "draft_revision_id", "active_version_no", "draft_version_no",
			"active_case_count", "draft_case_count",
		}).
			// 已发布套件：active v5 + 继承草稿（无版本号→0）。
			AddRow("suite-2", "s2", "d2", "prompt", "published", "user-2", now.Add(-1*time.Hour),
				"rev-2", "rev-3", 5, 0, 8, 3).
			// 草稿-only 套件：无 active。
			AddRow("suite-1", "s1", "", "skill", "draft", "", now.Add(-2*time.Hour),
				"", "rev-1", 0, 0, 0, 1))
	mock.ExpectCommit()

	page, err := repo.ListSuites(context.Background(), "t1", port.CenterFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.NextCursor)
	first := page.Items[0]
	require.Equal(t, "s2", first.Name)
	require.Equal(t, domain.ResourceKind("prompt"), first.ResourceKind)
	require.Equal(t, "published", first.Status)
	require.Equal(t, "rev-2", first.ActiveRevisionID)
	require.Equal(t, "rev-3", first.DraftRevisionID)
	require.Equal(t, 5, first.ActiveVersionNo)
	require.Equal(t, 8, first.ActiveCaseCount)
	require.Equal(t, 3, first.DraftCaseCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_ListRuns_paginated(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id,resource_kind,resource_id,revision_id").
		WithArgs("", "", "", (*time.Time)(nil), (*string)(nil), 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "status", "passed", "total_cases", "passed_cases", "created_by", "created_at",
		}).AddRow("run-2", "prompt", "r-1", "rev-2", "succeeded", true, 10, 10, "user-2", now.Add(-1*time.Hour)).
			AddRow("run-1", "prompt", "r-1", "rev-1", "failed", false, 10, 3, "", now.Add(-2*time.Hour)))
	mock.ExpectCommit()

	page, err := repo.ListRuns(context.Background(), "t1", port.CenterFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.NextCursor)
	require.True(t, page.Items[0].Passed)
	require.Equal(t, 10, page.Items[0].PassedCases)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_ListCandidates_safeDiff(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()
	rank := 1

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT c.id,j.resource_kind").
		WithArgs("", "", "", (*time.Time)(nil), (*string)(nil), 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "parent_revision_id", "source", "status",
			"rank", "state_version", "parent", "parent_exists", "candidate", "created_by", "created_at",
		}).AddRow("cand-1", "prompt", "r-1", "rev-2", "rev-1", "optimization", "proposed",
			&rank, int64(3), []byte(`{"name":"v1","price":1}`), true, []byte(`{"name":"v1","price":2}`), "user-1", now))
	mock.ExpectCommit()

	page, err := repo.ListCandidates(context.Background(), "t1", port.CenterFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Empty(t, page.NextCursor)
	c := page.Items[0]
	require.NotNil(t, c.Rank)
	require.Equal(t, 1, *c.Rank)
	require.Equal(t, []string{"price"}, c.SafeDiff.ChangedFields)
	require.False(t, c.SafeDiff.ParentMissing)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_ListExperiments_withEvidence(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id,resource_kind,resource_id,stable_revision_id,canary_revision_id").
		WithArgs("", "", "", (*time.Time)(nil), (*string)(nil), 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "stable_revision_id", "canary_revision_id", "status",
			"stage_percent", "recommendation", "safety_stopped", "state_version", "policy", "snapshot", "created_by", "created_at",
		}).AddRow("exp-1", "prompt", "r-1", "stable-1", "canary-1", "running",
			20, "advance", false, int64(3), []byte(`{"stages":[5,20],"min_samples":100}`),
			[]byte(`{"metrics":{"samples":120,"quality_improvement":0.3}}`), "user-1", now))
	mock.ExpectCommit()

	page, err := repo.ListExperiments(context.Background(), "t1", port.CenterFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	e := page.Items[0]
	require.Equal(t, "advance", e.Recommendation)
	require.Equal(t, 20, e.StagePercent)
	require.NotEmpty(t, e.PromotionEvidence)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_ListExperiments_badPolicyJSON(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id,resource_kind,resource_id,stable_revision_id,canary_revision_id").
		WithArgs("", "", "", (*time.Time)(nil), (*string)(nil), 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "stable_revision_id", "canary_revision_id", "status",
			"stage_percent", "recommendation", "safety_stopped", "state_version", "policy", "snapshot", "created_by", "created_at",
		}).AddRow("exp-1", "prompt", "r-1", "stable-1", "canary-1", "running",
			20, "hold", false, int64(3), []byte(`{bad`), []byte(`{}`), "user-1", now))
	mock.ExpectRollback()

	_, err := repo.ListExperiments(context.Background(), "t1", port.CenterFilter{Limit: 1})
	require.ErrorContains(t, err, "decode experiment policy")
}

func TestPgCenterQueryRepository_Timeline_paginated(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM resource_revisions").
		WithArgs("prompt", "r-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("WITH events AS").
		WithArgs("prompt", "r-1", "", (*time.Time)(nil), (*string)(nil), (*string)(nil), 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "kind", "status", "summary", "safe_summary", "resource_kind", "resource_id", "created_at",
		}).AddRow("run-1", "run", "succeeded", "passed", nil, "prompt", "r-1", now.Add(-1*time.Hour)).
			AddRow("rev-1", "revision", "published", "", []byte(`{"name":"v1"}`), "prompt", "r-1", now.Add(-2*time.Hour)))
	mock.ExpectCommit()

	page, err := repo.Timeline(context.Background(), "t1", port.CenterFilter{ResourceKind: "prompt", ResourceID: "r-1", Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.NextCursor)
	require.Equal(t, "run", page.Items[0].Kind)
	require.Equal(t, "passed", page.Items[0].Summary)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_Timeline_revisionSummarySanitized(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}
	now := time.Now()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM resource_revisions").
		WithArgs("prompt", "r-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("WITH events AS").
		WithArgs("prompt", "r-1", "", (*time.Time)(nil), (*string)(nil), (*string)(nil), 3).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "kind", "status", "summary", "safe_summary", "resource_kind", "resource_id", "created_at",
		}).AddRow("rev-1", "revision", "published", "", []byte(`{"name":"v1","token":"secret"}`), "prompt", "r-1", now))
	mock.ExpectCommit()

	page, err := repo.Timeline(context.Background(), "t1", port.CenterFilter{ResourceKind: "prompt", ResourceID: "r-1", Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Empty(t, page.NextCursor)
	require.Contains(t, page.Items[0].Summary, "v1")
	require.NotContains(t, page.Items[0].Summary, "secret")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_Timeline_resourceMissing(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM resource_revisions").
		WithArgs("prompt", "r-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	_, err := repo.Timeline(context.Background(), "t1", port.CenterFilter{ResourceKind: "prompt", ResourceID: "r-1", Limit: 10})
	require.ErrorIs(t, err, port.ErrCenterResourceNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgCenterQueryRepository_Timeline_badCursor(t *testing.T) {
	repo := &PgCenterQueryRepository{pool: newMockRepo(t)}

	_, err := repo.Timeline(context.Background(), "t1", port.CenterFilter{
		ResourceKind: "prompt", ResourceID: "r-1", Cursor: "bad!", Limit: 10,
	})
	require.ErrorIs(t, err, domain.ErrInvalidCenterQuery)
}

func TestPgCenterQueryRepository_ListRuns_queryFails(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgCenterQueryRepository{pool: mock}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id,resource_kind,resource_id,revision_id").
		WithArgs("", "", "", (*time.Time)(nil), (*string)(nil), 2).
		WillReturnError(pgx.ErrTxClosed)
	mock.ExpectRollback()

	_, err := repo.ListRuns(context.Background(), "t1", port.CenterFilter{Limit: 1})
	require.ErrorContains(t, err, "list runs")
	require.NoError(t, mock.ExpectationsWereMet())
}
