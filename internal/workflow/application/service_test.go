package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/workflow/application"
	"github.com/byteBuilderX/stratum/internal/workflow/domain"
	"github.com/byteBuilderX/stratum/internal/workflow/domain/port"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type memoryStore struct {
	mu          sync.Mutex
	definitions map[string]*domain.Definition
	versions    map[string]*domain.Version
	runs        map[string]*domain.Run
	attempts    map[string][]domain.NodeAttempt
	auditEvents []*auditdomain.ResourceChangeAuditEvent
	// lastEditorActor 记录最近一次 UpdateDefinition 的 editorActor 参数，
	// 供白名单 member 写路径行为测试断言 store 收到了非空 editorActor。
	lastEditorActor string
}

func (s *memoryStore) DeleteDefinition(_ context.Context, _, id string, ev *auditdomain.ResourceChangeAuditEvent) error {
	delete(s.definitions, id)
	s.auditEvents = append(s.auditEvents, ev)
	return nil
}

func newMemoryStore() *memoryStore {
	return &memoryStore{definitions: map[string]*domain.Definition{}, versions: map[string]*domain.Version{}, runs: map[string]*domain.Run{}, attempts: map[string][]domain.NodeAttempt{}}
}

func (s *memoryStore) CreateDefinition(_ context.Context, _ string, definition *domain.Definition, ev *auditdomain.ResourceChangeAuditEvent) error {
	s.definitions[definition.ID] = definition
	s.auditEvents = append(s.auditEvents, ev)
	return nil
}
func (s *memoryStore) GetDefinition(_ context.Context, _, id string) (*domain.Definition, error) {
	row := s.definitions[id]
	if row == nil {
		return nil, domain.ErrNotFound
	}
	copy := *row
	return &copy, nil
}
func (s *memoryStore) UpdateDefinition(_ context.Context, _ string, definition *domain.Definition, expected int64, editorActor string, ev *auditdomain.ResourceChangeAuditEvent) error {
	s.lastEditorActor = editorActor
	if s.definitions[definition.ID].Revision != expected {
		return domain.ErrRevisionConflict
	}
	s.definitions[definition.ID] = definition
	s.auditEvents = append(s.auditEvents, ev)
	return nil
}
func (s *memoryStore) CreateVersion(_ context.Context, _ string, version *domain.Version, ev *auditdomain.ResourceChangeAuditEvent) error {
	s.versions[version.ID] = version
	s.auditEvents = append(s.auditEvents, ev)
	return nil
}
func (s *memoryStore) GetVersion(_ context.Context, _, id string) (*domain.Version, error) {
	row := s.versions[id]
	if row == nil {
		return nil, domain.ErrNotFound
	}
	copy := *row
	return &copy, nil
}
func (s *memoryStore) NextVersionNumber(context.Context, string, string) (int64, error) {
	return int64(len(s.versions) + 1), nil
}
func (s *memoryStore) SetActiveVersion(_ context.Context, _, definitionID, versionID string, ev *auditdomain.ResourceChangeAuditEvent) error {
	row := s.definitions[definitionID]
	if row == nil {
		return domain.ErrNotFound
	}
	row.ActiveVersionID = versionID
	s.auditEvents = append(s.auditEvents, ev)
	return nil
}
func (s *memoryStore) FindRunByIdempotency(_ context.Context, _, key string) (*domain.Run, error) {
	for _, run := range s.runs {
		if run.IdempotencyKey == key {
			copy := *run
			return &copy, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (s *memoryStore) CreateRun(_ context.Context, _ string, run *domain.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *run
	s.runs[run.ID] = &copy
	return nil
}
func (s *memoryStore) GetRun(_ context.Context, _, id string) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.runs[id]
	if row == nil {
		return nil, domain.ErrNotFound
	}
	copy := *row
	return &copy, nil
}
func (s *memoryStore) UpdateRun(_ context.Context, _ string, run *domain.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *run
	s.runs[run.ID] = &copy
	return nil
}
func (s *memoryStore) SaveAttempt(_ context.Context, _ string, attempt domain.NodeAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.attempts[attempt.RunID]
	for i := range rows {
		if rows[i].NodeID == attempt.NodeID && rows[i].AttemptNo == attempt.AttemptNo {
			rows[i] = attempt
			s.attempts[attempt.RunID] = rows
			return nil
		}
	}
	s.attempts[attempt.RunID] = append(rows, attempt)
	return nil
}
func (s *memoryStore) ListAttempts(_ context.Context, _, runID string) ([]domain.NodeAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.NodeAttempt(nil), s.attempts[runID]...), nil
}

type ids struct{ n int }

func (i *ids) NewID() string { i.n++; return "id-" + string(rune('0'+i.n)) }

// stubTenantRole 固定角色解析器：非授权用例注入 owner 放行所有权矩阵。
type stubTenantRole struct{ role string }

func (s stubTenantRole) ResolveTenantRole(context.Context, string, string) (string, error) {
	return s.role, nil
}

// defVersionStore 组合 DefinitionRepository 与 VersionRepository，供
// newOwnerDefinitionService 接受任意嵌入 memoryStore 的测试 store（dagStore、
// executeStore 等），同一值同时充当 definitions 与 versions 仓库。
type defVersionStore interface {
	port.DefinitionRepository
	port.VersionRepository
}

// newOwnerDefinitionService 构造注入 owner 角色的服务，用于非授权相关测试。
// newID 抽象为 func 以便同时覆盖 idgen 与固定 ID 两类构造点（机械替换全部
// NewDefinitionService 站点，保证 Update/Publish/Delete 既有用例经 owner stub 放行）。
func newOwnerDefinitionService(store defVersionStore, newID func() string) *application.DefinitionService {
	svc := application.NewDefinitionService(store, store, newID)
	svc.SetTenantRoleResolver(stubTenantRole{role: "owner"})
	return svc
}

func adminActor() application.Actor {
	return application.Actor{UserID: "admin", Role: "admin"}
}

type agentStub struct {
	calls []string
	fail  string
}

func (s *agentStub) ExecuteAgent(_ context.Context, _, agentID, _, _, input string) (string, string, error) {
	s.calls = append(s.calls, agentID+":"+input)
	if agentID == s.fail {
		return "", "trace-fail", errors.New("agent failed")
	}
	return "output-" + agentID, "trace-" + agentID, nil
}

func workflowSpec() domain.Spec {
	return domain.Spec{Nodes: []domain.Node{{ID: "one", Type: domain.NodeTypeAgent, AgentID: "agent-1"}, {ID: "two", Type: domain.NodeTypeAgent, AgentID: "agent-2"}}, Edges: []domain.Edge{{From: "one", To: "two"}}}
}

func TestDefinitionServicePublishesVersion(t *testing.T) {
	store, idgen := newMemoryStore(), &ids{}
	svc := newOwnerDefinitionService(store, idgen.NewID)
	def, err := svc.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "Research", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)
	version, err := svc.Publish(context.Background(), "tenant-1", def.ID, "u-1")
	require.NoError(t, err)
	require.Equal(t, def.ID, version.DefinitionID)
	require.Equal(t, int64(1), version.Number)
	// 发布者记为版本 created_by（fallback 路径 service 在入库前打戳），使版本历史
	// 「操作者」可溯源；store 收到的版本已带操作者。
	require.Equal(t, "u-1", version.CreatedBy)
	stored := store.versions[version.ID]
	require.NotNil(t, stored)
	require.Equal(t, "u-1", stored.CreatedBy)
}

func TestDefinitionService_Rollback_MovesActivePointer(t *testing.T) {
	store, idgen := newMemoryStore(), &ids{}
	svc := newOwnerDefinitionService(store, idgen.NewID)
	ctx := context.Background()
	def, err := svc.Create(ctx, "tenant-1", application.CreateDefinitionCommand{Name: "Research", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)
	v1, err := svc.Publish(ctx, "tenant-1", def.ID, "u-1")
	require.NoError(t, err)
	v2, err := svc.Publish(ctx, "tenant-1", def.ID, "u-1")
	require.NoError(t, err)
	// 模拟发布后 active 指向最新版 v2（真实库 CreateNextVersion 事务内写入）。
	store.definitions[def.ID].ActiveVersionID = v2.ID

	rolled, err := svc.Rollback(ctx, "tenant-1", def.ID, v1.ID, "u-1")
	require.NoError(t, err)
	require.Equal(t, v1.ID, rolled.ActiveVersionID)
	// 回退不产生新版本。
	require.Len(t, store.versions, 2)
	last := store.auditEvents[len(store.auditEvents)-1]
	require.Equal(t, auditdomain.ChangeOpRollback, last.Operation)
	require.Equal(t, "u-1", last.ActorID)
	require.JSONEq(t, `{"id":"`+def.ID+`","name":"Research","description":"","active_version_id":"`+v1.ID+`"}`, string(last.After))
}

func TestDefinitionService_Rollback_RejectsForeignVersion(t *testing.T) {
	store, idgen := newMemoryStore(), &ids{}
	svc := newOwnerDefinitionService(store, idgen.NewID)
	ctx := context.Background()
	def, err := svc.Create(ctx, "tenant-1", application.CreateDefinitionCommand{Name: "A", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)
	other, err := svc.Create(ctx, "tenant-1", application.CreateDefinitionCommand{Name: "B", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)
	otherVersion, err := svc.Publish(ctx, "tenant-1", other.ID, "u-1")
	require.NoError(t, err)

	_, err = svc.Rollback(ctx, "tenant-1", def.ID, otherVersion.ID, "u-1")
	require.ErrorIs(t, err, domain.ErrNotFound)
	// 归属不符时生效指针不得被移动（fail-closed）。
	require.Empty(t, store.definitions[def.ID].ActiveVersionID)
}

func TestDefinitionService_Rollback_MissingVersion(t *testing.T) {
	store, idgen := newMemoryStore(), &ids{}
	svc := newOwnerDefinitionService(store, idgen.NewID)
	ctx := context.Background()
	def, err := svc.Create(ctx, "tenant-1", application.CreateDefinitionCommand{Name: "A", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)

	_, err = svc.Rollback(ctx, "tenant-1", def.ID, "no-such-version", "u-1")
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.Empty(t, store.definitions[def.ID].ActiveVersionID)
}

// TestDefinitionService_Rollback_RequiresOwnerOrAdmin pins the Rollback ownership
// semantics: owner 放行、admin 无需本人为 creator 放行、member 一律拒绝，且被拒后
// 生效指针不得被移动（fail-closed）。
func TestDefinitionService_Rollback_RequiresOwnerOrAdmin(t *testing.T) {
	cases := []struct {
		name string
		role string
		want error
	}{
		{"owner passes", "owner", nil},
		{"admin non-creator passes", "admin", nil},
		{"member forbidden", "member", domain.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, idgen := newMemoryStore(), &ids{}
			svc := newOwnerDefinitionService(store, idgen.NewID)
			ctx := context.Background()
			def, err := svc.Create(ctx, "tenant-1", application.CreateDefinitionCommand{Name: "Research", Spec: workflowSpec()}, "u-1")
			require.NoError(t, err)
			version, err := svc.Publish(ctx, "tenant-1", def.ID, "u-1")
			require.NoError(t, err)
			// 模拟发布后 active 指向最新版本。
			store.definitions[def.ID].ActiveVersionID = version.ID

			svc.SetTenantRoleResolver(stubTenantRole{role: tc.role})
			before := store.definitions[def.ID].ActiveVersionID
			_, err = svc.Rollback(ctx, "tenant-1", def.ID, version.ID, "actor-1")
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.Equal(t, before, store.definitions[def.ID].ActiveVersionID,
					"未授权回退不得移动生效指针")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestDefinitionServiceDeletesDraft(t *testing.T) {
	store, idgen := newMemoryStore(), &ids{}
	svc := newOwnerDefinitionService(store, idgen.NewID)
	definition, err := svc.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{
		Name: "Disposable", Spec: workflowSpec(),
	}, "u-1")
	require.NoError(t, err)

	require.NoError(t, svc.Delete(context.Background(), "tenant-1", definition.ID, "u-1"))
	_, exists := store.definitions[definition.ID]
	require.False(t, exists)
}

func TestRunServiceIdempotencyAndSequentialExecution(t *testing.T) {
	store, idgen, agents := newMemoryStore(), &ids{}, &agentStub{}
	defs := newOwnerDefinitionService(store, idgen.NewID)
	def, err := defs.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "Research", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)
	version, err := defs.Publish(context.Background(), "tenant-1", def.ID, "u-1")
	require.NoError(t, err)
	runs := application.NewRunService(store, store, agents, idgen.NewID)

	run, created, err := runs.Start(context.Background(), "tenant-1", application.StartRunCommand{VersionID: version.ID, Input: map[string]any{"task": "hello"}, IdempotencyKey: "same-key"})
	require.NoError(t, err)
	require.True(t, created)
	same, created, err := runs.Start(context.Background(), "tenant-1", application.StartRunCommand{VersionID: version.ID, Input: map[string]any{"task": "hello"}, IdempotencyKey: "same-key"})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, run.ID, same.ID)
	_, _, err = runs.Start(context.Background(), "tenant-1", application.StartRunCommand{VersionID: version.ID, Input: map[string]any{"task": "different"}, IdempotencyKey: "same-key"})
	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)

	require.NoError(t, runs.Execute(context.Background(), "tenant-1", run.ID))
	got, attempts, err := runs.Get(context.Background(), "tenant-1", run.ID, adminActor())
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusCompleted, got.Status)
	require.Equal(t, "output-agent-2", got.Output)
	require.Equal(t, []string{"agent-1:{\"task\":\"hello\"}", "agent-2:output-agent-1"}, agents.calls)
	require.Len(t, attempts, 2)
	require.Equal(t, "trace-agent-2", attempts[1].TraceID)
}

func TestRunServiceStopsAfterUpstreamFailure(t *testing.T) {
	store, idgen, agents := newMemoryStore(), &ids{}, &agentStub{fail: "agent-1"}
	defs := newOwnerDefinitionService(store, idgen.NewID)
	def, _ := defs.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "Research", Spec: workflowSpec()}, "u-1")
	version, _ := defs.Publish(context.Background(), "tenant-1", def.ID, "u-1")
	runs := application.NewRunService(store, store, agents, idgen.NewID)
	run, _, err := runs.Start(context.Background(), "tenant-1", application.StartRunCommand{VersionID: version.ID, Input: map[string]any{"task": "hello"}, IdempotencyKey: "failure"})
	require.NoError(t, err)
	require.Error(t, runs.Execute(context.Background(), "tenant-1", run.ID))
	got, attempts, err := runs.Get(context.Background(), "tenant-1", run.ID, adminActor())
	require.NoError(t, err)
	require.Equal(t, domain.RunStatusFailed, got.Status)
	require.Len(t, attempts, 1)
	require.Len(t, agents.calls, 1)
}

func TestRunServiceStartAsyncOnlyPersistsQueuedRun(t *testing.T) {
	store, idgen, agents := newMemoryStore(), &ids{}, &agentStub{}
	defs := newOwnerDefinitionService(store, idgen.NewID)
	def, _ := defs.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "Research", Spec: workflowSpec()}, "u-1")
	version, _ := defs.Publish(context.Background(), "tenant-1", def.ID, "u-1")
	runs := application.NewRunService(store, store, agents, idgen.NewID)

	run, created, err := runs.StartAsync(context.Background(), "tenant-1", application.StartRunCommand{VersionID: version.ID, Input: map[string]any{"task": "hello"}, IdempotencyKey: "async", CreatedBy: "user-a"})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "user-a", run.CreatedBy)
	time.Sleep(30 * time.Millisecond)
	got, _, getErr := runs.Get(context.Background(), "tenant-1", run.ID, adminActor())
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusQueued, got.Status)
	require.Empty(t, agents.calls)
}

// persistFailStore 在 run 进入 failed 状态时让持久化失败,用于验证 failRun
// 不得吞掉失败状态写回错误(项目原则:持久化失败必须向上传播)。
type persistFailStore struct {
	*memoryStore
	events      []domain.Event
	failPersist error
}

func (s *persistFailStore) UpdateRun(ctx context.Context, tenantID string, run *domain.Run) error {
	if run.Status == domain.RunStatusFailed {
		return s.failPersist
	}
	return s.memoryStore.UpdateRun(ctx, tenantID, run)
}

func (s *persistFailStore) AppendEvent(_ context.Context, _ string, event domain.Event) (domain.Event, error) {
	s.events = append(s.events, event)
	return event, nil
}

func (s *persistFailStore) ListEvents(context.Context, string, string, int64, int) ([]domain.Event, error) {
	return nil, nil
}

func TestRunServiceFailRunPropagatesCheckpointFailure(t *testing.T) {
	store, idgen := newMemoryStore(), &ids{}
	persistErr := errors.New("checkpoint write failed")
	persistStore := &persistFailStore{memoryStore: store, failPersist: persistErr}
	agentErr := errors.New("agent two exploded")
	agents := &scriptedFailAgent{err: agentErr}
	defs := newOwnerDefinitionService(store, idgen.NewID)
	def, err := defs.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "Research", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)
	version, err := defs.Publish(context.Background(), "tenant-1", def.ID, "u-1")
	require.NoError(t, err)
	core, observed := observer.New(zapcore.ErrorLevel)
	runs := application.NewRunServiceWithRegistry(store, persistStore, agents, idgen.NewID, zap.New(core))
	run, _, err := runs.Start(context.Background(), "tenant-1", application.StartRunCommand{VersionID: version.ID, Input: map[string]any{"task": "hello"}, IdempotencyKey: "fail-persist"})
	require.NoError(t, err)

	// failRun 路径:节点执行失败 → run 标记失败 → 失败状态写回失败。
	// 持久化错误必须随返回值向上传播,且记录 ERROR 日志(含 run id)。
	err = runs.Execute(context.Background(), "tenant-1", run.ID)
	require.ErrorIs(t, err, agentErr, "原始失败原因必须保留")
	require.ErrorIs(t, err, persistErr, "失败状态写回错误必须向上传播,禁止吞掉")

	entries := observed.FilterMessage("workflow.run_failed_persist").All()
	require.Len(t, entries, 1, "失败状态写回失败必须记录 ERROR 日志")
	require.Equal(t, run.ID, entries[0].ContextMap()["run_id"])
	// 失败状态未落库:库内 run 仍停留在 running,等待租约回收——这与静默吞错
	// 的区别在于错误已经显式暴露,运维可见。
	got, _, getErr := runs.Get(context.Background(), "tenant-1", run.ID, adminActor())
	require.NoError(t, getErr)
	require.Equal(t, domain.RunStatusRunning, got.Status)
}

// scriptedFailAgent 对特定 node 返回捕获的 sentinel 错误,便于 errors.Is 断言。
type scriptedFailAgent struct{ err error }

func (s *scriptedFailAgent) Execute(_ context.Context, request port.NodeExecutionRequest) (port.NodeExecutionResult, error) {
	if request.Node.AgentID == "agent-2" {
		return port.NodeExecutionResult{}, s.err
	}
	return port.NodeExecutionResult{
		Output:  "output-" + request.Node.AgentID,
		TraceID: "trace-" + request.Node.AgentID,
	}, nil
}

func TestDefinitionService_Create_WritesChangeAudit(t *testing.T) {
	store := newMemoryStore()
	svc := newOwnerDefinitionService(store, func() string { return "def-1" })
	created, err := svc.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{
		Name: "n", Description: "d", Spec: workflowSpec(),
	}, "u-1")
	require.NoError(t, err)
	require.Equal(t, "def-1", created.ID)
	require.Len(t, store.auditEvents, 1)
	ev := store.auditEvents[0]
	require.Equal(t, auditdomain.ResourceKindWorkflow, ev.ResourceKind)
	require.Equal(t, "def-1", ev.ResourceID)
	require.Equal(t, auditdomain.ChangeOpCreate, ev.Operation)
	require.Equal(t, "u-1", ev.ActorID)
	require.JSONEq(t, `{"id":"def-1","name":"n","description":"d"}`, string(ev.After))
}

// skillBindingStub 是 SkillBindingResolver 的测试桩：按 agentID 返回允许列表，
// err 非空时查询失败（用于 fail-closed 验证）。
type skillBindingStub struct {
	allowed map[string][]string
	err     error
}

func (s *skillBindingStub) AgentAllowedSkills(_ context.Context, _, agentID string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.allowed[agentID], nil
}

func skillSpec(skillID string) domain.Spec {
	return domain.Spec{Nodes: []domain.Node{{ID: "s", Type: domain.NodeTypeSkill, AgentID: "agent-1", SkillID: skillID}}}
}

func TestDefinitionService_Create_RejectsCyclicSpec(t *testing.T) {
	store := newMemoryStore()
	svc := newOwnerDefinitionService(store, func() string { return "def-1" })
	cyclic := workflowSpec()
	cyclic.Edges = append(cyclic.Edges, domain.Edge{From: "two", To: "one"})
	_, err := svc.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "n", Spec: cyclic}, "u-1")
	require.ErrorIs(t, err, domain.ErrInvalidSpec)
}

func TestDefinitionService_Update_RejectsCyclicSpec(t *testing.T) {
	store := newMemoryStore()
	svc := newOwnerDefinitionService(store, func() string { return "def-1" })
	created, err := svc.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "n", Spec: workflowSpec()}, "u-1")
	require.NoError(t, err)
	cyclic := workflowSpec()
	cyclic.Edges = append(cyclic.Edges, domain.Edge{From: "two", To: "one"})
	_, err = svc.Update(context.Background(), "tenant-1", created.ID, application.UpdateDefinitionCommand{Name: "n", Spec: cyclic, ExpectedRevision: created.Revision}, "u-1")
	require.ErrorIs(t, err, domain.ErrInvalidSpec)
}

func TestDefinitionService_SkipsBindingCheckWithoutResolver(t *testing.T) {
	store := newMemoryStore()
	svc := newOwnerDefinitionService(store, func() string { return "def-1" })
	// resolver 未注入（nil）时绑定校验跳过，skill 草稿可保存。
	_, err := svc.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "n", Spec: skillSpec("skill-1")}, "u-1")
	require.NoError(t, err)
}

func TestDefinitionService_ValidateSkillBindings(t *testing.T) {
	tests := []struct {
		name        string
		allowed     map[string][]string
		resolverErr error
		wantErr     bool
	}{
		{name: "enabled skill passes", allowed: map[string][]string{"agent-1": {"skill-1"}}},
		{name: "empty allowed skills rejects", allowed: map[string][]string{"agent-1": {}}, wantErr: true},
		{name: "skill not enabled rejects", allowed: map[string][]string{"agent-1": {"skill-2"}}, wantErr: true},
		{name: "resolver failure propagates", allowed: map[string][]string{}, resolverErr: errors.New("agent query failed"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			svc := newOwnerDefinitionService(store, func() string { return "def-1" })
			svc.SetSkillBindingResolver(&skillBindingStub{allowed: tc.allowed, err: tc.resolverErr})
			_, err := svc.Create(context.Background(), "tenant-1", application.CreateDefinitionCommand{Name: "n", Spec: skillSpec("skill-1")}, "u-1")
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
