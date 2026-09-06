package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/workflow/domain"
	"github.com/byteBuilderX/stratum/internal/workflow/domain/port"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v2"
	"github.com/stretchr/testify/require"
)

func newStoreMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	return mock
}

// beginTenantTx starts the transaction and switches search_path like
// ExecTenantWith does. Every tenant-scoped SQL test begins with these two.
func beginTenantTx(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL search_path").
		WillReturnResult(pgxmock.NewResult("SET", 0))
}

var runColumns = []string{
	"id", "definition_id", "version_id", "version_no", "status", "snapshot_json", "input_json",
	"output_text", "error_message", "idempotency_key", "request_hash", "generation",
	"scheduler_owner", "lease_expires_at", "pause_reason", "cancel_reason", "manual_reason",
	"created_by", "created_at", "updated_at", "started_at", "finished_at",
}

func runRow(id string) []any {
	return []any{
		id, "d1", "v1", int64(2), domain.RunStatus("queued"), []byte(`{"nodes":[]}`), []byte(`{}`),
		"", "", "ik-1", "rh", int64(3), "owner", nil, "", "", "", "user:1",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil, nil,
	}
}

func TestPgStore_tenantContextMismatchFailsClosed(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	ctx := postgres.WithTenant(context.Background(), &postgres.TenantContext{TenantID: "other"})

	err := store.CreateDefinition(ctx, "t1", &domain.Definition{}, nil)
	require.ErrorContains(t, err, "tenant context mismatch")
	require.NoError(t, mock.ExpectationsWereMet(), "no SQL may run on a mismatched tenant")
}

func TestPgStore_CreateDefinition_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	d := &domain.Definition{ID: "d1", Name: "n", Description: "desc", Revision: 3}

	beginTenantTx(mock)
	mock.ExpectExec("INSERT INTO workflow_definitions").
		WithArgs("d1", "n", "desc", "", int64(3), `{"nodes":null,"edges":null}`, `{"task_label":""}`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, store.CreateDefinition(context.Background(), "t1", d, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateDefinition_fails(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("INSERT INTO workflow_definitions").
		WithArgs("", "", "", "", int64(0), `{"nodes":null,"edges":null}`, `{"task_label":""}`).
		WillReturnError(pgx.ErrTxClosed)
	mock.ExpectRollback()

	err := store.CreateDefinition(context.Background(), "t1", &domain.Definition{}, nil)
	require.ErrorIs(t, err, pgx.ErrTxClosed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateDefinition_autoGrantsCreatorEditor(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	// INSERT workflow_definitions（含 created_by 列，共 7 参数）。
	mock.ExpectExec("INSERT INTO workflow_definitions \\(id,name,description,created_by,draft_revision,draft_spec_json,draft_input_schema_json\\)").
		WithArgs("d1", "Research", "", "u-1", int64(1), `{"nodes":null,"edges":null}`, `{"task_label":"任务"}`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// insertEditors → EditorEligible 检查 creator 为租户成员。
	mock.ExpectQuery("SELECT EXISTS\\(").
		WithArgs("t1", "u-1", []string{"admin", "owner", "member"}).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	// insertEditors → INSERT resource_editors。
	mock.ExpectExec("INSERT INTO resource_editors \\(resource_kind, resource_id, editor_id, created_by\\)").
		WithArgs("workflow", "d1", "u-1", "u-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// ev=nil → 无变更审计。
	mock.ExpectCommit()

	err := store.CreateDefinition(context.Background(), "t1", &domain.Definition{
		ID: "d1", Name: "Research", CreatedBy: "u-1", Revision: 1,
		Spec: domain.Spec{}, InputSchema: domain.InputSchema{TaskLabel: "任务"},
	}, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

var definitionColumns = []string{
	"id", "name", "description", "created_by", "draft_revision", "active_version_id",
	"draft_spec_json", "draft_input_schema_json", "created_at", "updated_at",
}

func TestPgStore_GetDefinition_found(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_definitions WHERE id=\\$1").
		WithArgs("d1").
		WillReturnRows(pgxmock.NewRows(definitionColumns).AddRow(
			"d1", "n", "desc", "", int64(3), "v9", []byte(`{"nodes":[]}`), []byte(`{}`),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		))
	mock.ExpectCommit()

	d, err := store.GetDefinition(context.Background(), "t1", "d1")
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, "d1", d.ID)
	require.Equal(t, int64(3), d.Revision)
	require.Equal(t, "v9", d.ActiveVersionID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_GetDefinition_notFound(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_definitions WHERE id=\\$1").
		WithArgs("nope").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	d, err := store.GetDefinition(context.Background(), "t1", "nope")
	require.Nil(t, d)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_GetDefinition_unmarshalFails(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_definitions WHERE id=\\$1").
		WithArgs("d1").
		WillReturnRows(pgxmock.NewRows(definitionColumns).AddRow(
			"d1", "n", "desc", "", int64(3), "v9", []byte(`{invalid`), []byte(`{}`),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		))
	mock.ExpectCommit()

	_, err := store.GetDefinition(context.Background(), "t1", "d1")
	require.ErrorContains(t, err, "invalid character")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_ListDefinitions_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("COUNT\\(\\*\\) FROM workflow_definitions").
		WithArgs("q").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("FROM workflow_definitions WHERE").WithArgs("q", 10, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "description", "draft_revision", "active_version_id", "created_at", "updated_at",
		}).AddRow(
			"d1", "n", "desc", int64(2), "v2",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		))
	mock.ExpectCommit()

	rows, total, err := store.ListDefinitions(context.Background(), "t1", port.DefinitionListQuery{Query: "q", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, rows, 1)
	require.Equal(t, "d1", rows[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_ListDefinitions_countFails(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("COUNT\\(\\*\\) FROM workflow_definitions").
		WithArgs("").
		WillReturnError(pgx.ErrTxClosed)
	mock.ExpectRollback()

	_, _, err := store.ListDefinitions(context.Background(), "t1", port.DefinitionListQuery{})
	require.ErrorIs(t, err, pgx.ErrTxClosed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_UpdateDefinition_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	d := &domain.Definition{ID: "d1", Name: "n2", Description: "d2", Revision: 4}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_definitions").
		WithArgs("n2", "d2", int64(4), `{"nodes":null,"edges":null}`, `{"task_label":""}`, "d1", int64(3)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	require.NoError(t, store.UpdateDefinition(context.Background(), "t1", d, 3, "", nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_UpdateDefinition_staleRevision(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_definitions").
		WithArgs("", "", int64(1), `{"nodes":null,"edges":null}`, `{"task_label":""}`, "d1", int64(9)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	err := store.UpdateDefinition(context.Background(), "t1", &domain.Definition{ID: "d1", Revision: 1}, 9, "", nil)
	require.ErrorIs(t, err, domain.ErrRevisionConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_DeleteDefinition_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("DELETE FROM resource_editors WHERE resource_kind=\\$1 AND resource_id=\\$2").
		WithArgs("workflow", "d1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectExec("DELETE FROM workflow_definitions").
		WithArgs("d1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	require.NoError(t, store.DeleteDefinition(context.Background(), "t1", "d1", nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_DeleteDefinition_notFound(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("DELETE FROM resource_editors WHERE resource_kind=\\$1 AND resource_id=\\$2").
		WithArgs("workflow", "nope").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectExec("DELETE FROM workflow_definitions").
		WithArgs("nope").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectRollback()

	err := store.DeleteDefinition(context.Background(), "t1", "nope", nil)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateVersion_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	v := &domain.Version{ID: "v1", DefinitionID: "d1", Number: 2, Name: "n", Description: "d", CreatedBy: "actor-1"}

	beginTenantTx(mock)
	mock.ExpectExec("INSERT INTO workflow_versions").
		WithArgs("v1", "d1", int64(2), "n", "d", "actor-1", `{"nodes":null,"edges":null}`, `{"task_label":""}`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, store.CreateVersion(context.Background(), "t1", v, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

var versionColumns = []string{
	"id", "definition_id", "version_no", "name", "description",
	"created_by", "spec_json", "input_schema_json", "created_at",
}

func TestPgStore_GetVersion_found(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_versions WHERE id=\\$1").
		WithArgs("v1").
		WillReturnRows(pgxmock.NewRows(versionColumns).AddRow(
			"v1", "d1", int64(2), "n", "d", "user-1", []byte(`{"nodes":[]}`), []byte(`{}`),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		))
	mock.ExpectCommit()

	v, err := store.GetVersion(context.Background(), "t1", "v1")
	require.NoError(t, err)
	require.NotNil(t, v)
	require.Equal(t, int64(2), v.Number)
	require.Equal(t, "user-1", v.CreatedBy)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_GetVersion_notFound(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_versions WHERE id=\\$1").
		WithArgs("nope").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	v, err := store.GetVersion(context.Background(), "t1", "nope")
	require.Nil(t, v)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_ListVersions_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("COUNT\\(\\*\\) FROM workflow_versions").
		WithArgs("d1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("FROM workflow_versions WHERE definition_id=\\$1").
		WithArgs("d1", 10, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "definition_id", "version_no", "name", "description", "created_by", "created_at",
		}).AddRow(
			"v2", "d1", int64(2), "n2", "d2", "user-a",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		).AddRow(
			"v1", "d1", int64(1), "n1", "d1", "user-b",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		))
	mock.ExpectCommit()

	versions, total, err := store.ListVersions(context.Background(), "t1", "d1", port.VersionListQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, versions, 2)
	require.Equal(t, "v2", versions[0].ID)
	require.Equal(t, "user-a", versions[0].CreatedBy)
	require.Equal(t, "user-b", versions[1].CreatedBy)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_NextVersionNumber(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("COALESCE\\(MAX\\(version_no").
		WithArgs("d1").
		WillReturnRows(pgxmock.NewRows([]string{"next"}).AddRow(int64(4)))
	mock.ExpectCommit()

	number, err := store.NextVersionNumber(context.Background(), "t1", "d1")
	require.NoError(t, err)
	require.Equal(t, int64(4), number)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateNextVersion_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	definition := &domain.Definition{
		ID: "d1", Name: "n", Revision: 1,
		Spec:        domain.Spec{Nodes: []domain.Node{{ID: "n1", Type: domain.NodeTypeApproval}}},
		InputSchema: domain.InputSchema{TaskLabel: "task"},
	}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_definitions WHERE id=\\$1 FOR UPDATE").
		WithArgs("d1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("d1"))
	mock.ExpectQuery("COALESCE\\(MAX\\(version_no").
		WithArgs("d1").
		WillReturnRows(pgxmock.NewRows([]string{"next"}).AddRow(int64(2)))
	mock.ExpectExec("INSERT INTO workflow_versions").
		WithArgs("v-new", "d1", int64(2), "n", "", "", `{"nodes":[{"id":"n1","type":"approval","agent_id":"","retry":{}}],"edges":null}`, `{"task_label":"task"}`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE workflow_definitions SET active_version_id").
		WithArgs("v-new", "d1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	created, err := store.CreateNextVersion(context.Background(), "t1", definition, "v-new", "", nil)
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, int64(2), created.Number)
	// ev=nil（非发布写路径/测试）→ created_by 保持 ''；publish 路径由 ev.ActorID 注入。
	require.Empty(t, created.CreatedBy)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateNextVersion_invalidSpec(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	// Empty spec fails validation before any SQL runs.
	_, err := store.CreateNextVersion(context.Background(), "t1", &domain.Definition{}, "v-new", "", nil)
	require.ErrorIs(t, err, domain.ErrInvalidSpec)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateNextVersion_definitionMissing(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_definitions WHERE id=\\$1 FOR UPDATE").
		WithArgs("nope").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := store.CreateNextVersion(context.Background(), "t1", &domain.Definition{
		ID:          "nope",
		Spec:        domain.Spec{Nodes: []domain.Node{{ID: "n1", Type: domain.NodeTypeApproval}}},
		InputSchema: domain.InputSchema{TaskLabel: "task"},
	}, "v-new", "", nil)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_SetActiveVersion_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_definitions SET active_version_id").
		WithArgs("v-old", "d1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	require.NoError(t, store.SetActiveVersion(context.Background(), "t1", "d1", "v-old", nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_SetActiveVersion_definitionMissing(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_definitions SET active_version_id").
		WithArgs("v-old", "nope").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	err := store.SetActiveVersion(context.Background(), "t1", "nope", "v-old", nil)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateRun_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	r := &domain.Run{
		ID: "r1", DefinitionID: "d1", VersionID: "v1", VersionNumber: 2,
		Status: domain.RunStatusQueued, IdempotencyKey: "ik", RequestHash: "rh",
		Generation: 1, CreatedBy: "user:1",
	}

	beginTenantTx(mock)
	mock.ExpectExec("INSERT INTO workflow_runs").
		WithArgs("r1", "d1", "v1", int64(2), domain.RunStatus("queued"), `{"nodes":null,"edges":null}`, `null`,
			"", "", "ik", "rh", int64(1), "", (*time.Time)(nil), "", "", "", "user:1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, store.CreateRun(context.Background(), "t1", r))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateRunIdempotent_created(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	r := &domain.Run{
		ID: "r1", DefinitionID: "d1", VersionID: "v1", VersionNumber: 2,
		Status: domain.RunStatusQueued, IdempotencyKey: "ik", RequestHash: "rh",
		Generation: 1, CreatedBy: "user:1",
	}

	beginTenantTx(mock)
	mock.ExpectExec("ON CONFLICT \\(idempotency_key\\)").
		WithArgs("r1", "d1", "v1", int64(2), domain.RunStatus("queued"), `{"nodes":null,"edges":null}`, `null`,
			"", "", "ik", "rh", int64(1), "", (*time.Time)(nil), "", "", "", "user:1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	existing, created, err := store.CreateRunIdempotent(context.Background(), "t1", r)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "r1", existing.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateRunIdempotent_conflictMatchesHash(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	r := &domain.Run{
		ID: "r1", DefinitionID: "d1", VersionID: "v1", VersionNumber: 2,
		Status: domain.RunStatusQueued, IdempotencyKey: "ik", RequestHash: "rh",
		Generation: 1, CreatedBy: "user:1",
	}

	beginTenantTx(mock)
	mock.ExpectExec("ON CONFLICT \\(idempotency_key\\)").
		WithArgs("r1", "d1", "v1", int64(2), domain.RunStatus("queued"), `{"nodes":null,"edges":null}`, `null`,
			"", "", "ik", "rh", int64(1), "", (*time.Time)(nil), "", "", "", "user:1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("FROM workflow_runs WHERE idempotency_key=\\$1").
		WithArgs("ik").
		WillReturnRows(pgxmock.NewRows(runColumns).AddRow(runRow("r-old")...))
	mock.ExpectCommit()

	existing, created, err := store.CreateRunIdempotent(context.Background(), "t1", r)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "r-old", existing.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_CreateRunIdempotent_hashMismatch(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	r := &domain.Run{
		ID: "r1", DefinitionID: "d1", VersionID: "v1", VersionNumber: 2,
		Status: domain.RunStatusQueued, IdempotencyKey: "ik", RequestHash: "rh",
		Generation: 1, CreatedBy: "user:1",
	}

	beginTenantTx(mock)
	mock.ExpectExec("ON CONFLICT \\(idempotency_key\\)").
		WithArgs("r1", "d1", "v1", int64(2), domain.RunStatus("queued"), `{"nodes":null,"edges":null}`, `null`,
			"", "", "ik", "rh", int64(1), "", (*time.Time)(nil), "", "", "", "user:1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	row := runRow("r-old")
	row[10] = "different-hash" // request_hash column
	mock.ExpectQuery("FROM workflow_runs WHERE idempotency_key=\\$1").
		WithArgs("ik").
		WillReturnRows(pgxmock.NewRows(runColumns).AddRow(row...))
	mock.ExpectRollback()

	_, _, err := store.CreateRunIdempotent(context.Background(), "t1", r)
	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_GetRun_found(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_runs WHERE id=\\$1").
		WithArgs("r1").
		WillReturnRows(pgxmock.NewRows(runColumns).AddRow(runRow("r1")...))
	mock.ExpectCommit()

	r, err := store.GetRun(context.Background(), "t1", "r1")
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Equal(t, "r1", r.ID)
	require.Equal(t, domain.RunStatusQueued, r.Status)
	require.Equal(t, int64(3), r.Generation)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_GetRun_notFound(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_runs WHERE id=\\$1").
		WithArgs("nope").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	r, err := store.GetRun(context.Background(), "t1", "nope")
	require.Nil(t, r)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_FindRunByIdempotency_found(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("FROM workflow_runs WHERE idempotency_key=\\$1").
		WithArgs("ik-1").
		WillReturnRows(pgxmock.NewRows(runColumns).AddRow(runRow("r1")...))
	mock.ExpectCommit()

	r, err := store.FindRunByIdempotency(context.Background(), "t1", "ik-1")
	require.NoError(t, err)
	require.Equal(t, "r1", r.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_ListRuns_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("COUNT\\(\\*\\) FROM workflow_runs").
		WithArgs("", "", domain.RunStatus("")).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("FROM workflow_runs LEFT JOIN workflow_versions").
		WithArgs("", "", domain.RunStatus(""), 10, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "definition_id", "name", "version_id", "version_no", "status", "created_by",
			"created_at", "updated_at", "started_at", "finished_at",
		}).AddRow(
			"r1", "d1", "my-workflow", "v1", int64(2), domain.RunStatus("queued"), "user:1",
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil, nil,
		))
	mock.ExpectCommit()

	runs, total, err := store.ListRuns(context.Background(), "t1", port.RunListQuery{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, runs, 1)
	require.Equal(t, "r1", runs[0].ID)
	require.Equal(t, "my-workflow", runs[0].Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_ListRuns_filterArgs(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	query := port.RunListQuery{CreatedBy: "u1", DefinitionID: "d1", Status: "running", Limit: 5, Offset: 2}

	beginTenantTx(mock)
	mock.ExpectQuery("COUNT\\(\\*\\) FROM workflow_runs").
		WithArgs("u1", "d1", domain.RunStatus("running")).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM workflow_runs LEFT JOIN workflow_versions").
		WithArgs("u1", "d1", domain.RunStatus("running"), 5, 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "definition_id", "name", "version_id", "version_no", "status", "created_by",
			"created_at", "updated_at", "started_at", "finished_at",
		}))
	mock.ExpectCommit()

	_, total, err := store.ListRuns(context.Background(), "t1", query)
	require.NoError(t, err)
	require.Equal(t, 0, total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_UpdateRun_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}
	r := &domain.Run{
		ID: "r1", Status: domain.RunStatusRunning, Output: "out", Generation: 4,
	}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_runs SET").
		WithArgs(domain.RunStatus("running"), "out", "", "", "", "", "r1", int64(4)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	require.NoError(t, store.UpdateRun(context.Background(), "t1", r))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_UpdateRun_generationCAS(t *testing.T) {
	tests := []struct {
		name string
		run  *domain.Run
	}{
		{
			name: "stale generation rejected",
			run:  &domain.Run{ID: "r1", Status: domain.RunStatusRunning, Generation: 3},
		},
		{
			name: "second concurrent update loses CAS",
			run:  &domain.Run{ID: "r1", Status: domain.RunStatusRunning, Generation: 4},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := newStoreMock(t)
			store := &PgStore{pool: mock}

			beginTenantTx(mock)
			mock.ExpectExec("UPDATE workflow_runs SET").
				WithArgs(domain.RunStatus("running"), "", "", "", "", "", "r1", int64(tc.run.Generation)).
				WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			mock.ExpectRollback()

			// 乐观锁失败:行未命中(generation 不匹配或被并发更新先行提交),
			// 必须返回冲突错误并回滚,由调用方决定重试或放弃,禁止静默覆盖。
			err := store.UpdateRun(context.Background(), "t1", tc.run)
			require.ErrorIs(t, err, domain.ErrGenerationConflict)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPgStore_RenewRunLease_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_runs SET lease_expires_at").
		WithArgs("30s", "r1", "owner", int64(3)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	require.NoError(t, store.RenewRunLease(context.Background(), "t1", "r1", "owner", 3, 30*time.Second))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_RenewRunLease_fenceConflict(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_runs SET lease_expires_at").
		WithArgs("30s", "r1", "other", int64(3)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	err := store.RenewRunLease(context.Background(), "t1", "r1", "other", 3, 30*time.Second)
	require.ErrorIs(t, err, domain.ErrFenceConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_RunControlState_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("SELECT status FROM workflow_runs WHERE id=\\$1 AND generation=\\$2").
		WithArgs("r1", int64(3)).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(domain.RunStatus("running")))
	mock.ExpectCommit()

	status, err := store.RunControlState(context.Background(), "t1", "r1", 3)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusRunning, status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_RunControlState_generationConflict(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectQuery("SELECT status FROM workflow_runs WHERE id=\\$1 AND generation=\\$2").
		WithArgs("r1", int64(9)).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := store.RunControlState(context.Background(), "t1", "r1", 9)
	require.ErrorIs(t, err, domain.ErrGenerationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_ReleaseRun_success(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_runs SET scheduler_owner=''").
		WithArgs("r1", "owner", int64(3)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	require.NoError(t, store.ReleaseRun(context.Background(), "t1", "r1", "owner", 3))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgStore_ReleaseRun_fenceConflict(t *testing.T) {
	mock := newStoreMock(t)
	store := &PgStore{pool: mock}

	beginTenantTx(mock)
	mock.ExpectExec("UPDATE workflow_runs SET scheduler_owner=''").
		WithArgs("r1", "other", int64(3)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	err := store.ReleaseRun(context.Background(), "t1", "r1", "other", 3)
	require.ErrorIs(t, err, domain.ErrFenceConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
