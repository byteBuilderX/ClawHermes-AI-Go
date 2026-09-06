//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/workflow/domain"
	"github.com/byteBuilderX/stratum/internal/workflow/domain/port"
	workflowpersist "github.com/byteBuilderX/stratum/internal/workflow/infrastructure/persistence"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/byteBuilderX/stratum/pkg/tenantdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPgStoreStage1ALifecycleAndTenantIsolation(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantA, tenantB := "workflow_"+uuid.NewString()[:8], "workflow_"+uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantA))
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantB))
	defer func() {
		_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantA+`" CASCADE`)
		_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantB+`" CASCADE`)
	}()
	store := workflowpersist.NewPgStore(pool)
	ctxA := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantA})
	ctxB := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantB})

	spec := domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "agent-1"}}}
	inputSchema := domain.InputSchema{TaskLabel: "任务", Fields: []domain.InputField{{
		Key: "region", Label: "区域", Type: domain.InputFieldShortText,
	}}}
	def, err := domain.NewDefinition(uuid.NewString(), "Research", "desc", spec, inputSchema)
	require.NoError(t, err)
	require.NoError(t, store.CreateDefinition(ctxA, tenantA, def, nil))
	loaded, err := store.GetDefinition(ctxA, tenantA, def.ID)
	require.NoError(t, err)
	require.Equal(t, def.Spec, loaded.Spec)
	require.Equal(t, inputSchema, loaded.InputSchema)
	require.False(t, loaded.CreatedAt.IsZero())
	require.False(t, loaded.UpdatedAt.IsZero())
	require.NoError(t, loaded.UpdateDraft("Research v2", "changed", spec, 1, inputSchema))
	require.NoError(t, store.UpdateDefinition(ctxA, tenantA, loaded, 1, "", nil))
	require.ErrorIs(t, store.UpdateDefinition(ctxA, tenantA, loaded, 1, "", nil), domain.ErrRevisionConflict)
	_, err = store.GetDefinition(ctxB, tenantB, def.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)

	version, err := def.Publish(uuid.NewString(), 1)
	require.NoError(t, err)
	// 发布者记为版本 created_by，落库后单版读回应保留该操作者（版本历史「操作者」）。
	version.CreatedBy = "user-a"
	require.NoError(t, store.CreateVersion(ctxA, tenantA, version, nil))
	loadedVersion, err := store.GetVersion(ctxA, tenantA, version.ID)
	require.NoError(t, err)
	require.Equal(t, inputSchema, loadedVersion.InputSchema)
	require.Equal(t, "user-a", loadedVersion.CreatedBy)
	require.False(t, loadedVersion.CreatedAt.IsZero())
	run, err := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "hello", "region": "east"}, "key-1", "hash-1")
	require.NoError(t, err)
	run.CreatedBy = "user-a"
	require.NoError(t, store.CreateRun(ctxA, tenantA, run))
	found, err := store.FindRunByIdempotency(ctxA, tenantA, "key-1")
	require.NoError(t, err)
	require.Equal(t, run.ID, found.ID)
	require.Equal(t, "user-a", found.CreatedBy)
	require.False(t, found.CreatedAt.IsZero())
	require.False(t, found.UpdatedAt.IsZero())
	_, err = store.GetRun(ctxB, tenantB, run.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)

	definitions, definitionTotal, err := store.ListDefinitions(ctxA, tenantA, port.DefinitionListQuery{
		Query: "search", Offset: 0, Limit: 20,
	})
	require.NoError(t, err)
	require.Equal(t, 1, definitionTotal)
	require.Equal(t, def.ID, definitions[0].ID)
	versions, versionTotal, err := store.ListVersions(ctxA, tenantA, def.ID, port.VersionListQuery{Offset: 0, Limit: 20})
	require.NoError(t, err)
	require.Equal(t, 1, versionTotal)
	require.Equal(t, version.ID, versions[0].ID)

	secondRun, err := domain.NewRun(uuid.NewString(), version, map[string]any{
		"task": "second", "region": "west",
	}, "key-2", "hash-2")
	require.NoError(t, err)
	secondRun.CreatedBy = "user-b"
	require.NoError(t, store.CreateRun(ctxA, tenantA, secondRun))
	memberRuns, memberTotal, err := store.ListRuns(ctxA, tenantA, port.RunListQuery{
		CreatedBy: "user-a", DefinitionID: def.ID, Status: domain.RunStatusQueued, Offset: 0, Limit: 20,
	})
	require.NoError(t, err)
	require.Equal(t, 1, memberTotal)
	require.Equal(t, run.ID, memberRuns[0].ID)
	adminRuns, adminTotal, err := store.ListRuns(ctxA, tenantA, port.RunListQuery{Offset: 0, Limit: 20})
	require.NoError(t, err)
	require.Equal(t, 2, adminTotal)
	require.Equal(t, secondRun.ID, adminRuns[0].ID)
	otherTenantRuns, otherTenantTotal, err := store.ListRuns(ctxB, tenantB, port.RunListQuery{Offset: 0, Limit: 20})
	require.NoError(t, err)
	require.Zero(t, otherTenantTotal)
	require.Empty(t, otherTenantRuns)

	attempt := domain.NodeAttempt{ID: uuid.NewString(), RunID: run.ID, NodeID: "one", AttemptNo: 1, Status: domain.AttemptStatusRunning, Input: "hello"}
	require.NoError(t, store.SaveAttempt(ctxA, tenantA, attempt))
	retryAt := time.Now().UTC().Add(time.Minute)
	attempt.Status = domain.AttemptStatusRetryWait
	attempt.ErrorCode = "temporary"
	attempt.RetryAt = &retryAt
	attempt.EffectClass = domain.EffectClassIdempotent
	require.NoError(t, store.SaveAttempt(ctxA, tenantA, attempt))
	attempts, err := store.ListAttempts(ctxA, tenantA, run.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, "temporary", attempts[0].ErrorCode)
	require.WithinDuration(t, retryAt, *attempts[0].RetryAt, time.Second)

	first, err := store.AppendEvent(ctxA, tenantA, domain.Event{ID: uuid.NewString(), RunID: run.ID, Type: "workflow.run_started", ActorType: "human", ActorID: "admin-1", OccurredAt: time.Now().UTC()})
	require.NoError(t, err)
	second, err := store.AppendEvent(ctxA, tenantA, domain.Event{ID: uuid.NewString(), RunID: run.ID, Type: "workflow.node_started", NodeID: "one", OccurredAt: time.Now().UTC()})
	require.NoError(t, err)
	require.Equal(t, int64(1), first.SequenceNo)
	require.Equal(t, int64(2), second.SequenceNo)
	events, err := store.ListEvents(ctxA, tenantA, run.ID, 1, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, second.ID, events[0].ID)
	require.Equal(t, second.SequenceNo, events[0].SequenceNo)
	require.Equal(t, second.Type, events[0].Type)
	allEvents, err := store.ListEvents(ctxA, tenantA, run.ID, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "human", allEvents[0].ActorType)
	require.Equal(t, "admin-1", allEvents[0].ActorID)
	require.WithinDuration(t, second.OccurredAt, events[0].OccurredAt, time.Millisecond)
	_, err = store.ListEvents(ctxB, tenantB, run.ID, 0, 100)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPgStoreJSONBWorksWithProductionSimpleProtocol(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	wrapped, err := postgres.New(ctx, url, zap.NewNop())
	require.NoError(t, err)
	defer wrapped.Close()
	pool := wrapped.DB()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := "workflow_" + uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantID+`" CASCADE`) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, err := domain.NewDefinition(uuid.NewString(), "Simple Protocol", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "a"}}})
	require.NoError(t, err)
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	loaded, err := store.GetDefinition(tenantCtx, tenantID, definition.ID)
	require.NoError(t, err)
	require.Equal(t, definition.Spec, loaded.Spec)
}

func TestPgStoreClaimsRunOnceAndRejectsStaleRelease(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO tenants (id,name,slug,status) VALUES ($1,$2,$3,'active')`, tenantID, "Workflow Runtime", "workflow-"+tenantID[:8])
	require.NoError(t, err)
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	spec := domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "agent-1"}}}
	definition, _ := domain.NewDefinition(uuid.NewString(), "Durable", "", spec)
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "hello"}, "claim-key", "hash")
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))

	claimedTenant, first, claimed, err := store.ClaimRun(ctx, "worker-a", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, tenantID, claimedTenant)
	_, _, claimed, err = store.ClaimRun(ctx, "worker-b", time.Minute)
	require.NoError(t, err)
	require.False(t, claimed)
	require.ErrorIs(t, store.ReleaseRun(ctx, tenantID, first.ID, "worker-a", first.Generation-1), domain.ErrFenceConflict)
	require.NoError(t, store.ReleaseRun(ctx, tenantID, first.ID, "worker-a", first.Generation))

	first.Status = domain.RunStatusRunning
	require.NoError(t, store.UpdateRun(tenantCtx, tenantID, first))
	for i := 1; i < domain.MaxTenantConcurrentRuns; i++ {
		active, newErr := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "active"}, fmt.Sprintf("active-%d", i), fmt.Sprintf("hash-%d", i))
		require.NoError(t, newErr)
		require.NoError(t, active.Start())
		require.NoError(t, store.CreateRun(tenantCtx, tenantID, active))
	}
	queued, newErr := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "queued"}, "over-limit", "over-limit-hash")
	require.NoError(t, newErr)
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, queued))
	_, _, claimed, err = store.ClaimRun(ctx, "worker-over-limit", time.Minute)
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestPgStoreClaimRunSkipsActiveTenantWhoseSchemaIsNotYetProvisioned(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))

	unprovisionedTenantID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO tenants (id,name,slug,status) VALUES ($1,$2,$3,'active')`, unprovisionedTenantID, "Provisioning", "provisioning-"+unprovisionedTenantID[:8])
	require.NoError(t, err)
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, unprovisionedTenantID) }()

	readyTenantID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO tenants (id,name,slug,status) VALUES ($1,$2,$3,'active')`, readyTenantID, "Ready", "ready-"+readyTenantID[:8])
	require.NoError(t, err)
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, readyTenantID))
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, readyTenantID) }()

	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: readyTenantID})
	definition, err := domain.NewDefinition(uuid.NewString(), "Ready", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "agent"}}})
	require.NoError(t, err)
	require.NoError(t, store.CreateDefinition(tenantCtx, readyTenantID, definition, nil))
	version, err := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, err)
	require.NoError(t, store.CreateVersion(tenantCtx, readyTenantID, version, nil))
	run, err := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "schema race"}, "schema-race", "hash")
	require.NoError(t, err)
	require.NoError(t, store.CreateRun(tenantCtx, readyTenantID, run))

	claimedTenant, _, claimed, err := store.ClaimRun(ctx, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, readyTenantID, claimedTenant)
}

func TestPgStoreRejectsLateAttemptCompletionFence(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := "workflow_" + uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantID+`" CASCADE`) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	spec := domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "agent"}}}
	definition, _ := domain.NewDefinition(uuid.NewString(), "Fence", "", spec)
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "fence"}, "fence-key", "hash")
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
	attempt := domain.NodeAttempt{ID: uuid.NewString(), RunID: run.ID, NodeID: "one", AttemptNo: 1, Status: domain.AttemptStatusSucceeded, OutputSummary: "new", FenceToken: 2}
	require.NoError(t, store.SaveAttempt(tenantCtx, tenantID, attempt))
	attempt.Status, attempt.OutputSummary, attempt.FenceToken = domain.AttemptStatusFailed, "late", 1
	require.ErrorIs(t, store.SaveAttempt(tenantCtx, tenantID, attempt), domain.ErrFenceConflict)
	attempt.Status, attempt.OutputSummary, attempt.FenceToken = domain.AttemptStatusFailed, "same-fence-late", 2
	require.ErrorIs(t, store.SaveAttempt(tenantCtx, tenantID, attempt), domain.ErrFenceConflict)
	rows, err := store.ListAttempts(tenantCtx, tenantID, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AttemptStatusSucceeded, rows[0].Status)
}

func TestPgStorePhase1CControlApprovalAndEffectAreTenantScopedAndFenced(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantA, tenantB := "workflow_"+uuid.NewString()[:8], "workflow_"+uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantA))
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantB))
	defer func() {
		_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantA+`" CASCADE`)
		_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantB+`" CASCADE`)
	}()
	store := workflowpersist.NewPgStore(pool)
	ctxA := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantA})
	ctxB := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantB})
	spec := domain.Spec{Nodes: []domain.Node{{ID: "approve", Type: domain.NodeTypeApproval}}}
	definition, _ := domain.NewDefinition(uuid.NewString(), "Controlled", "", spec)
	require.NoError(t, store.CreateDefinition(ctxA, tenantA, definition, nil))
	version, err := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, err)
	require.NoError(t, store.CreateVersion(ctxA, tenantA, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "control"}, "control-key", "hash")
	require.NoError(t, store.CreateRun(ctxA, tenantA, run))

	require.NoError(t, store.ControlRun(ctxA, tenantA, run.ID, run.Generation, domain.RunStatusPauseRequested, "maintenance", domain.Event{ID: uuid.NewString(), Type: "workflow.pause_requested", OccurredAt: time.Now().UTC()}))
	require.ErrorIs(t, store.ControlRun(ctxA, tenantA, run.ID, run.Generation, domain.RunStatusCancelRequested, "stale", domain.Event{ID: uuid.NewString(), Type: "workflow.cancel_requested", OccurredAt: time.Now().UTC()}), domain.ErrGenerationConflict)

	attemptID := uuid.NewString()
	require.NoError(t, store.SaveAttempt(ctxA, tenantA, domain.NodeAttempt{ID: attemptID, RunID: run.ID, NodeID: "approve", AttemptNo: 1, Status: domain.AttemptStatusPaused, FenceToken: run.Generation + 1, RunGeneration: run.Generation + 1}))
	controlled, err := store.GetRun(ctxA, tenantA, run.ID)
	require.NoError(t, err)
	approval := domain.NewApproval(uuid.NewString(), run.ID, "approve", attemptID, controlled.Generation+1, "human gate", "high", "redacted")
	require.NoError(t, store.CreateApproval(ctxA, tenantA, approval, domain.Event{ID: uuid.NewString(), Type: "workflow.approval_requested", OccurredAt: time.Now().UTC()}))
	paused, err := store.GetRun(ctxA, tenantA, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusPaused, paused.Status)
	require.Equal(t, approval.RunGeneration, paused.Generation)
	require.ErrorIs(t, store.DecideApproval(ctxA, tenantA, approval.ID, approval.RunGeneration, approval.AttemptID, "approve ", "admin", "bad", domain.Event{ID: uuid.NewString(), Type: "workflow.approval_decided", OccurredAt: time.Now().UTC()}), domain.ErrInvalidSpec)
	stillPending, err := store.GetApproval(ctxA, tenantA, approval.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ApprovalStatusPending, stillPending.Status)
	_, err = store.GetApproval(ctxB, tenantB, approval.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, store.DecideApproval(ctxA, tenantA, approval.ID, approval.RunGeneration, approval.AttemptID, domain.ApprovalDecisionApprove, "admin", "ok", domain.Event{ID: uuid.NewString(), Type: "workflow.approval_decided", OccurredAt: time.Now().UTC()}))
	require.ErrorIs(t, store.DecideApproval(ctxA, tenantA, approval.ID, approval.RunGeneration, approval.AttemptID, domain.ApprovalDecisionReject, "admin", "again", domain.Event{ID: uuid.NewString(), Type: "workflow.approval_decided", OccurredAt: time.Now().UTC()}), domain.ErrDecisionConflict)
	run2, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "reject"}, "reject-key", "reject-hash")
	require.NoError(t, run2.Start())
	require.NoError(t, store.CreateRun(ctxA, tenantA, run2))
	attempt2 := domain.NodeAttempt{ID: uuid.NewString(), RunID: run2.ID, NodeID: "approve", AttemptNo: 1, Status: domain.AttemptStatusPaused, FenceToken: run2.Generation, RunGeneration: run2.Generation}
	require.NoError(t, store.SaveAttempt(ctxA, tenantA, attempt2))
	approval2 := domain.NewApproval(uuid.NewString(), run2.ID, "approve", attempt2.ID, run2.Generation+1, "gate", "high", "safe")
	require.NoError(t, store.CreateApproval(ctxA, tenantA, approval2, domain.Event{ID: uuid.NewString(), Type: "workflow.approval_requested", OccurredAt: time.Now().UTC()}))
	require.NoError(t, store.DecideApproval(ctxA, tenantA, approval2.ID, approval2.RunGeneration, approval2.AttemptID, domain.ApprovalDecisionReject, "admin", "no", domain.Event{ID: uuid.NewString(), Type: "workflow.approval_decided", OccurredAt: time.Now().UTC()}))
	rejected, err := store.GetRun(ctxA, tenantA, run2.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusFailed, rejected.Status)

	intent := domain.NewEffectIntent(uuid.NewString(), run.ID, "tool", attemptID, approval.RunGeneration, domain.EffectClassNonIdempotent, "effect-key")
	require.NoError(t, store.CreateEffectIntent(ctxA, tenantA, intent))
	require.NoError(t, intent.Start(intent.RunGeneration))
	require.NoError(t, store.UpdateEffectIntent(ctxA, tenantA, intent, domain.EffectIntentStatusPrepared))
	require.NoError(t, intent.MarkUnknown("lost", intent.RunGeneration))
	require.NoError(t, store.UpdateEffectIntent(ctxA, tenantA, intent, domain.EffectIntentStatusStarted))
	intents, err := store.ListEffectIntents(ctxA, tenantA, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.EffectIntentStatusUnknown, intents[0].Status)
}

// 同批并行 approval:首个 approval 提交把 run 置为 paused 后,同批其余 approval 必须幂等成功,
// 否则右分支 approval 静默丢失,run 卡死在 paused(run_failed 因状态非 running 不落库)
func TestPgStoreParallelApprovalBatchBothRequestsSucceed(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenant := "workflow_" + uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenant))
	defer func() {
		_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenant+`" CASCADE`)
	}()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenant})
	spec := domain.Spec{Nodes: []domain.Node{{ID: "left-2", Type: domain.NodeTypeApproval}, {ID: "right-3", Type: domain.NodeTypeApproval}}, Edges: []domain.Edge{{ID: "e1", From: "left-2", To: "right-3"}}}
	definition, _ := domain.NewDefinition(uuid.NewString(), "ParallelApproval", "", spec)
	require.NoError(t, store.CreateDefinition(tenantCtx, tenant, definition, nil))
	version, err := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, err)
	require.NoError(t, store.CreateVersion(tenantCtx, tenant, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "parallel"}, "parallel-key", "parallel-hash")
	require.NoError(t, run.Start())
	require.NoError(t, store.CreateRun(tenantCtx, tenant, run))

	// 同一批(批基准 generation=run.Generation)创建两个并行 approval
	batchGeneration := run.Generation
	leftAttemptID, rightAttemptID := uuid.NewString(), uuid.NewString()
	require.NoError(t, store.SaveAttempt(tenantCtx, tenant, domain.NodeAttempt{ID: leftAttemptID, RunID: run.ID, NodeID: "left-2", AttemptNo: 1, Status: domain.AttemptStatusRunning, FenceToken: batchGeneration, RunGeneration: batchGeneration}))
	require.NoError(t, store.SaveAttempt(tenantCtx, tenant, domain.NodeAttempt{ID: rightAttemptID, RunID: run.ID, NodeID: "right-3", AttemptNo: 1, Status: domain.AttemptStatusRunning, FenceToken: batchGeneration, RunGeneration: batchGeneration}))
	left := domain.NewApproval(uuid.NewString(), run.ID, "left-2", leftAttemptID, batchGeneration+1, "human approval required", "high", "left")
	right := domain.NewApproval(uuid.NewString(), run.ID, "right-3", rightAttemptID, batchGeneration+1, "human approval required", "high", "right")
	require.NoError(t, store.CreateApproval(tenantCtx, tenant, left, domain.Event{ID: uuid.NewString(), Type: "workflow.approval_requested", OccurredAt: time.Now().UTC()}))
	// 第二个 approval 必须幂等成功,而不是 ErrGenerationConflict
	require.NoError(t, store.CreateApproval(tenantCtx, tenant, right, domain.Event{ID: uuid.NewString(), Type: "workflow.approval_requested", OccurredAt: time.Now().UTC()}))

	paused, err := store.GetRun(tenantCtx, tenant, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusPaused, paused.Status)
	require.Equal(t, batchGeneration+1, paused.Generation)
	approvals, err := store.ListApprovals(tenantCtx, tenant, run.ID, true)
	require.NoError(t, err)
	require.Len(t, approvals, 2)
	// 两个 approval 期望的 run generation 一致,decision 与 resume 均可用同一 expected_generation
	require.Equal(t, left.RunGeneration, right.RunGeneration)
}

func TestPgStoreReclaimsExpiredRunAndRenewsLease(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO tenants (id,name,slug,status) VALUES ($1,$2,$3,'active')`, tenantID, "Recovery", "recovery-"+tenantID[:8])
	require.NoError(t, err)
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Recovery", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "agent"}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "recovery"}, "recovery-key", "hash")
	require.NoError(t, run.Start())
	run.SchedulerOwner = "dead-worker"
	expired := time.Now().Add(-time.Minute)
	run.LeaseExpiresAt = &expired
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))

	claimedTenant, claimedRun, claimed, err := store.ClaimRun(ctx, "new-worker", time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, tenantID, claimedTenant)
	require.Greater(t, claimedRun.Generation, run.Generation)
	require.NoError(t, store.RenewRunLease(ctx, tenantID, claimedRun.ID, "new-worker", claimedRun.Generation, time.Minute))
	require.ErrorIs(t, store.RenewRunLease(ctx, tenantID, claimedRun.ID, "dead-worker", run.Generation, time.Minute), domain.ErrFenceConflict)
}

func TestPgStoreClaimsPersistentCancelWithoutOverwritingControlState(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO tenants (id,name,slug,status) VALUES ($1,$2,$3,'active')`, tenantID, "Cancel", "cancel-"+tenantID[:8])
	require.NoError(t, err)
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Cancel", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "a"}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "cancel"}, "cancel-key", "hash")
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
	require.NoError(t, store.ControlRun(tenantCtx, tenantID, run.ID, run.Generation, domain.RunStatusCancelRequested, "stop", domain.Event{ID: uuid.NewString(), Type: "workflow.cancel_requested", OccurredAt: time.Now().UTC()}))
	claimedTenant, claimedRun, claimed, err := store.ClaimRun(ctx, "worker", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, tenantID, claimedTenant)
	require.Equal(t, domain.RunStatusCancelRequested, claimedRun.Status)
}

func TestPgStoreAttemptCheckpointRollsBackWhenEventInsertFails(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := "workflow_" + uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantID+`" CASCADE`) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Atomic", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "a"}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "atomic"}, "atomic-key", "hash")
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
	attempt := domain.NodeAttempt{ID: uuid.NewString(), RunID: run.ID, NodeID: "one", AttemptNo: 1, Status: domain.AttemptStatusRunning, FenceToken: 1, RunGeneration: 1}
	eventID := uuid.NewString()
	require.NoError(t, store.CheckpointAttempt(tenantCtx, tenantID, attempt, domain.Event{ID: eventID, RunID: run.ID, Type: "workflow.node_started", OccurredAt: time.Now().UTC()}))
	attempt.Status = domain.AttemptStatusSucceeded
	attempt.OutputSummary = "done"
	require.Error(t, store.CheckpointAttempt(tenantCtx, tenantID, attempt, domain.Event{ID: eventID, RunID: run.ID, Type: "workflow.node_completed", OccurredAt: time.Now().UTC()}))
	rows, err := store.ListAttempts(tenantCtx, tenantID, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AttemptStatusRunning, rows[0].Status)
}

func TestPgStoreRunCheckpointRollsBackWhenEventInsertFails(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := "workflow_" + uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantID+`" CASCADE`) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Run Atomic", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "a"}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "run atomic"}, "run-atomic", "hash")
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
	eventID := uuid.NewString()
	_, err = store.AppendEvent(tenantCtx, tenantID, domain.Event{ID: eventID, RunID: run.ID, Type: "seed", OccurredAt: time.Now().UTC()})
	require.NoError(t, err)
	run.Status = domain.RunStatusCompleted
	run.Output = "done"
	require.Error(t, store.CheckpointRun(tenantCtx, tenantID, run, domain.Event{ID: eventID, RunID: run.ID, Type: "workflow.run_completed", OccurredAt: time.Now().UTC()}))
	loaded, err := store.GetRun(tenantCtx, tenantID, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusQueued, loaded.Status)
}

func TestPgStoreManualInterventionActionsAndConcurrentDecision(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := "workflow_" + uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantID+`" CASCADE`) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Manual", "", domain.Spec{Nodes: []domain.Node{{ID: "tool", Type: domain.NodeTypeMCPTool, MCPServerID: "crm", MCPToolName: "create", EffectClass: domain.EffectClassNonIdempotent}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	for _, tc := range []struct {
		action domain.ManualAction
		want   domain.RunStatus
	}{{domain.ManualActionMarkSucceeded, domain.RunStatusQueued}, {domain.ManualActionRetry, domain.RunStatusQueued}, {domain.ManualActionTerminate, domain.RunStatusFailed}} {
		run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "manual"}, "manual-"+string(tc.action), "hash-"+string(tc.action))
		require.NoError(t, run.Start())
		require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
		require.NoError(t, store.ControlRun(tenantCtx, tenantID, run.ID, run.Generation, domain.RunStatusManualIntervention, "unknown", domain.Event{ID: uuid.NewString(), Type: "workflow.manual_intervention", OccurredAt: time.Now().UTC()}))
		fresh, err := store.GetRun(tenantCtx, tenantID, run.ID)
		require.NoError(t, err)
		executionGeneration := fresh.Generation - 1
		attempt := domain.NodeAttempt{ID: uuid.NewString(), RunID: run.ID, NodeID: "tool", AttemptNo: 1, Status: domain.AttemptStatusManualIntervention, FenceToken: executionGeneration, RunGeneration: executionGeneration, EffectClass: domain.EffectClassNonIdempotent}
		require.NoError(t, store.SaveAttempt(tenantCtx, tenantID, attempt))
		intent := domain.NewEffectIntent(uuid.NewString(), run.ID, "tool", attempt.ID, executionGeneration, domain.EffectClassNonIdempotent, "effect-"+string(tc.action))
		require.NoError(t, store.CreateEffectIntent(tenantCtx, tenantID, intent))
		require.NoError(t, intent.Start(executionGeneration))
		require.NoError(t, store.UpdateEffectIntent(tenantCtx, tenantID, intent, domain.EffectIntentStatusPrepared))
		require.NoError(t, intent.MarkUnknown("lost", executionGeneration))
		require.NoError(t, store.UpdateEffectIntent(tenantCtx, tenantID, intent, domain.EffectIntentStatusStarted))
		require.NoError(t, store.ResolveEffect(tenantCtx, tenantID, intent.ID, fresh.Generation, tc.action, "reviewed", "admin", domain.Event{ID: uuid.NewString(), Type: "workflow.manual_intervention_resolved", OccurredAt: time.Now().UTC()}))
		resolved, err := store.GetRun(tenantCtx, tenantID, run.ID)
		require.NoError(t, err)
		require.Equal(t, tc.want, resolved.Status)
		if tc.action == domain.ManualActionRetry {
			attempt2 := domain.NodeAttempt{ID: uuid.NewString(), RunID: run.ID, NodeID: "tool", AttemptNo: 2, Status: domain.AttemptStatusRunning, FenceToken: resolved.Generation, RunGeneration: resolved.Generation, EffectClass: domain.EffectClassNonIdempotent}
			require.NoError(t, store.SaveAttempt(tenantCtx, tenantID, attempt2))
			replacement := domain.NewEffectIntent(uuid.NewString(), run.ID, "tool", attempt2.ID, resolved.Generation, domain.EffectClassNonIdempotent, intent.IdempotencyKey)
			require.NoError(t, store.CreateEffectIntent(tenantCtx, tenantID, replacement))
			require.Equal(t, intent.ID, replacement.ID)
		}
	}

	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "decision race"}, "decision-race", "hash-race")
	require.NoError(t, run.Start())
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
	attempt := domain.NodeAttempt{ID: uuid.NewString(), RunID: run.ID, NodeID: "tool", AttemptNo: 1, Status: domain.AttemptStatusPaused, FenceToken: run.Generation, RunGeneration: run.Generation}
	require.NoError(t, store.SaveAttempt(tenantCtx, tenantID, attempt))
	approval := domain.NewApproval(uuid.NewString(), run.ID, "tool", attempt.ID, run.Generation+1, "gate", "high", "safe")
	require.NoError(t, store.CreateApproval(tenantCtx, tenantID, approval, domain.Event{ID: uuid.NewString(), Type: "workflow.approval_requested", OccurredAt: time.Now().UTC()}))
	errs := make(chan error, 2)
	for _, decision := range []domain.ApprovalDecision{domain.ApprovalDecisionApprove, domain.ApprovalDecisionReject} {
		go func(d domain.ApprovalDecision) {
			errs <- store.DecideApproval(tenantCtx, tenantID, approval.ID, approval.RunGeneration, approval.AttemptID, d, "admin", "race", domain.Event{ID: uuid.NewString(), Type: "workflow.approval_decided", OccurredAt: time.Now().UTC()})
		}(decision)
	}
	success, conflict := 0, 0
	for range 2 {
		err := <-errs
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrDecisionConflict) || errors.Is(err, domain.ErrGenerationConflict) {
			conflict++
		} else {
			t.Fatalf("unexpected decision error: %v", err)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
}

func TestPgStoreTerminalRunsCannotBeResurrectedByControlCAS(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := "workflow_" + uuid.NewString()[:8]
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS "tenant_`+tenantID+`" CASCADE`) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Terminal", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "a"}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	for _, status := range []domain.RunStatus{domain.RunStatusCompleted, domain.RunStatusFailed, domain.RunStatusCanceled} {
		run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "terminal"}, "terminal-"+string(status), "hash-"+string(status))
		run.Status = status
		require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
		for _, target := range []domain.RunStatus{domain.RunStatusPauseRequested, domain.RunStatusCancelRequested} {
			err := store.ControlRun(tenantCtx, tenantID, run.ID, run.Generation, target, "late", domain.Event{ID: uuid.NewString(), Type: "workflow.control", OccurredAt: time.Now().UTC()})
			require.ErrorIs(t, err, domain.ErrInvalidTransition)
			loaded, getErr := store.GetRun(tenantCtx, tenantID, run.ID)
			require.NoError(t, getErr)
			require.Equal(t, status, loaded.Status)
		}
	}
}

func TestPgStoreReclaimsExpiredRunAtTenantConcurrencyLimit(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO tenants (id,name,slug,status) VALUES ($1,$2,$3,'active')`, tenantID, "Lease Limit", "lease-limit-"+tenantID[:8])
	require.NoError(t, err)
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Lease Limit", "", domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "a"}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	expired := time.Now().Add(-time.Minute)
	for i := 0; i < domain.MaxTenantConcurrentRuns; i++ {
		run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "expired"}, fmt.Sprintf("expired-%d", i), fmt.Sprintf("hash-%d", i))
		require.NoError(t, run.Start())
		run.SchedulerOwner, run.LeaseExpiresAt = "dead", &expired
		require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
	}
	claimedTenant, _, claimed, err := store.ClaimRun(ctx, "recovery-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, tenantID, claimedTenant)
}

func TestPgStoreExternalEffectFenceRejectsStaleWorkerBeforeCall(t *testing.T) {
	url := os.Getenv("STRATUM_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("STRATUM_TEST_POSTGRES_URL is required; this integration test must not skip")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, postgres.ProvisionPublicSchema(ctx, pool, zap.NewNop()))
	tenantID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO tenants (id,name,slug,status) VALUES ($1,$2,$3,'active')`, tenantID, "Effect Fence", "effect-fence-"+tenantID[:8])
	require.NoError(t, err)
	require.NoError(t, postgres.ProvisionTenantSchema(ctx, pool, tenantID))
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID) }()
	store := workflowpersist.NewPgStore(pool)
	tenantCtx := tenantdb.WithTenant(ctx, &tenantdb.TenantContext{TenantID: tenantID})
	definition, _ := domain.NewDefinition(uuid.NewString(), "Fence", "", domain.Spec{Nodes: []domain.Node{{ID: "tool", Type: domain.NodeTypeMCPTool, MCPServerID: "crm", MCPToolName: "write", EffectClass: domain.EffectClassNonIdempotent}}})
	require.NoError(t, store.CreateDefinition(tenantCtx, tenantID, definition, nil))
	version, _ := definition.Publish(uuid.NewString(), 1)
	require.NoError(t, store.CreateVersion(tenantCtx, tenantID, version, nil))
	run, _ := domain.NewRun(uuid.NewString(), version, map[string]any{"task": "fence effect"}, "fence-effect", "hash")
	require.NoError(t, store.CreateRun(tenantCtx, tenantID, run))
	_, claimedA, ok, err := store.ClaimRun(ctx, "worker-a", 25*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok)
	attempt := domain.NodeAttempt{ID: uuid.NewString(), RunID: run.ID, NodeID: "tool", AttemptNo: 1, Status: domain.AttemptStatusRunning, FenceToken: claimedA.Generation, RunGeneration: claimedA.Generation, EffectClass: domain.EffectClassNonIdempotent}
	require.NoError(t, store.SaveAttempt(tenantCtx, tenantID, attempt))
	time.Sleep(35 * time.Millisecond)
	_, claimedB, ok, err := store.ClaimRun(ctx, "worker-b", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Greater(t, claimedB.Generation, claimedA.Generation)
	intent := domain.NewEffectIntent(uuid.NewString(), run.ID, "tool", attempt.ID, claimedA.Generation, domain.EffectClassNonIdempotent, "stale-effect")
	require.ErrorIs(t, store.StartExternalEffect(tenantCtx, tenantID, intent, "worker-a", claimedA.Generation), domain.ErrFenceConflict)
}
