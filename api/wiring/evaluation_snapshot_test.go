package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	parametersdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
)

// fakeAgentRevisionSvc 是 agentRevisionService 的桩：固定返回一份被测 agent
// revision payload。
type fakeAgentRevisionSvc struct {
	revision agentdomain.AgentRevision
	found    bool
	err      error
	calls    int
	createID string
}

func (f *fakeAgentRevisionSvc) Get(
	_ context.Context, _ string, _ evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, []byte, bool, error) {
	f.calls++
	if f.err != nil {
		return evaldomain.ResourceRevision{}, nil, false, f.err
	}
	if !f.found {
		return evaldomain.ResourceRevision{}, nil, false, nil
	}
	payload, err := json.Marshal(f.revision)
	if err != nil {
		return evaldomain.ResourceRevision{}, nil, false, err
	}
	return evaldomain.ResourceRevision{
		ID: "revision-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
	}, payload, true, nil
}

func (f *fakeAgentRevisionSvc) Create(context.Context, string, evalport.CreateRevisionInput) (evaldomain.ResourceRevision, bool, error) {
	return evaldomain.ResourceRevision{ID: f.createID, ResourceKind: evaldomain.ResourceKindAgent, ResourceID: f.revision.AgentID}, true, nil
}

func (f *fakeAgentRevisionSvc) Publish(context.Context, string, evaldomain.ResourceRef) (evaldomain.ResourceRevision, error) {
	return evaldomain.ResourceRevision{}, nil
}

type fakeModelCtxProvider struct{ window int }

func (f fakeModelCtxProvider) GetChatModelContextWindow(_ context.Context, _ string) (int, error) {
	return f.window, nil
}

type fakeModelDetailsProvider struct{}

func (f fakeModelDetailsProvider) ListTenantModelDetails(_ context.Context, _ string) ([]agentdomain.TenantModelDetail, error) {
	return nil, nil
}

type fakeMCPResolver struct{ revisionByServer map[string]string }

func (f *fakeMCPResolver) ResolveMCPRevision(
	_ context.Context, _ string, serverID, _ string,
) (agentport.MCPRevisionAssignment, bool, error) {
	rev, ok := f.revisionByServer[serverID]
	if !ok {
		return agentport.MCPRevisionAssignment{}, false, nil
	}
	return agentport.MCPRevisionAssignment{RevisionID: rev}, true, nil
}

type fakeKnowledgeResolver struct{ revisionByWorkspace map[string]string }

func (f *fakeKnowledgeResolver) ResolveKnowledgeRevision(
	_ context.Context, _ string, workspaceName, _ string,
) (agentport.KnowledgeRevisionAssignment, bool, error) {
	rev, ok := f.revisionByWorkspace[workspaceName]
	if !ok {
		return agentport.KnowledgeRevisionAssignment{}, false, nil
	}
	return agentport.KnowledgeRevisionAssignment{
		Revision: agentport.KnowledgeRetrievalRevision{RevisionID: rev},
	}, true, nil
}

func (f *fakeKnowledgeResolver) LoadKnowledgeRevision(
	context.Context, string, string, string,
) (agentport.KnowledgeRetrievalRevision, error) {
	return agentport.KnowledgeRetrievalRevision{}, nil
}

// newSnapshotCapturerFixture 构造一个真实 parameters 服务（fake store 注入
// 版本历史）+ 桩 revision/窗口/resolver 依赖的 capturer。
func newSnapshotCapturerFixture(t *testing.T) (*snapshotCapturer, *fakeAgentRevisionSvc) {
	t.Helper()
	versions := map[string][]port.PlatformVersion{
		evaldomain.GroupEvaluation: {
			{GroupKey: evaldomain.GroupEvaluation, VersionSeq: 3, IsCurrent: true, Snapshot: map[string]json.RawMessage{
				"evaluation.judge.enabled": json.RawMessage(`true`),
				"evaluation.max_cases":     json.RawMessage(`10`),
			}},
			{GroupKey: evaldomain.GroupEvaluation, VersionSeq: 2, Snapshot: map[string]json.RawMessage{}},
		},
		evaldomain.GroupAgent: {
			{GroupKey: evaldomain.GroupAgent, VersionSeq: 5, IsCurrent: true, Snapshot: map[string]json.RawMessage{
				"agent.max_iterations": json.RawMessage(`3`),
			}},
		},
		evaldomain.GroupTrace: {
			{GroupKey: evaldomain.GroupTrace, VersionSeq: 1, IsCurrent: true, Snapshot: map[string]json.RawMessage{}},
		},
	}
	params := parametersapp.NewService(parametersdomain.NewParametersRegistry(), &fakePlatformStore{versions: versions})
	revisions := &fakeAgentRevisionSvc{
		found: true,
		revision: agentdomain.AgentRevision{
			AgentID: "agent-1", Type: agentdomain.ReActAgent, SystemPrompt: "你是助手", Model: "qwen-max",
			MaxIterations:   3,
			ModelParameters: agentdomain.ModelParameters{MaxContextTokens: 8192, MaxTokens: 2048},
			Bindings: []agentdomain.AgentBinding{
				{Kind: agentdomain.AgentBindingMCP, ID: "mcp-1", Enabled: true},
				{Kind: agentdomain.AgentBindingKnowledge, Name: "kb-1", Enabled: true},
				{Kind: agentdomain.AgentBindingMCP, ID: "mcp-disabled", Enabled: false},
			},
		},
	}
	capturer := &snapshotCapturer{
		params:      params,
		revisions:   revisions,
		modelCtx:    fakeModelCtxProvider{window: 32768},
		details:     fakeModelDetailsProvider{},
		vendor:      func(string) (int, int) { return 0, 0 },
		mcpResolver: &fakeMCPResolver{revisionByServer: map[string]string{"mcp-1": "mcp-rev-9"}},
		knowRes:     &fakeKnowledgeResolver{revisionByWorkspace: map[string]string{"kb-1": "kb-rev-2"}},
		logger:      zap.NewNop(),
	}
	return capturer, revisions
}

func TestSnapshotCapturerCaptureGroupPicksCurrentVersion(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)

	group, err := capturer.captureGroup(context.Background(), evaldomain.GroupEvaluation)
	require.NoError(t, err)
	require.Equal(t, evaldomain.GroupEvaluation, group.GroupKey)
	require.Equal(t, int64(3), group.VersionSeq)
	require.Equal(t, map[string]any{
		"evaluation.judge.enabled": true,
		"evaluation.max_cases":     float64(10),
	}, group.Values)
}

func TestSnapshotCapturerCaptureGroupUnpublishedReturnsEmptyGroup(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)

	group, err := capturer.captureGroup(context.Background(), "unpublished")
	require.NoError(t, err)
	require.Equal(t, "unpublished", group.GroupKey)
	require.Zero(t, group.VersionSeq)
	require.Nil(t, group.Values)
}

func TestSnapshotCapturerCaptureGroupPropagatesStoreError(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)
	capturer.params = parametersapp.NewService(parametersdomain.NewParametersRegistry(),
		&fakePlatformStore{err: context.DeadlineExceeded})

	_, err := capturer.captureGroup(context.Background(), evaldomain.GroupEvaluation)
	require.Error(t, err)
	require.Contains(t, err.Error(), "capture evaluation group versions")
}

func TestSnapshotCapturerCaptureFull(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)

	snap, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1",
		},
		SuiteRevisionID: "suite-1",
		RequestedBy:     "user-1",
	})
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Equal(t, evaldomain.SnapshotSchemaVersion, snap.SchemaVersion)
	require.Equal(t, "user-1", snap.CapturedBy)
	require.False(t, snap.CapturedAt.IsZero())
	require.Equal(t, evaldomain.GroupEvaluation, snap.Evaluation.GroupKey)
	require.Len(t, snap.Execution, 2)
	require.Equal(t, evaldomain.GroupAgent, snap.Execution[0].GroupKey)
	require.Equal(t, evaldomain.GroupTrace, snap.Execution[1].GroupKey)
	// 窗口固化：modelCtx 32768 未触发 MaxContextWindowTokens 上限，agent 显式
	// MaxContextTokens=8192 → ResolveAgentWindow 比例 clamp（32768×0.85=27852）
	// 内保留显式值 8192；reserve 取显式 2048。
	require.Equal(t, evaldomain.ResolvedExecution{ContextWindow: 8192, OutputReserve: 2048}, snap.ResolvedExecution)
	require.Equal(t, map[string]string{"mcp-1": "mcp-rev-9"}, snap.PinnedAssignments.MCPRevisions)
	require.Equal(t, map[string]string{"kb-1": "kb-rev-2"}, snap.PinnedAssignments.KnowledgeRevisions)
	require.Empty(t, snap.PinnedAssignments.SkillAgentRevision)
}

func TestSnapshotCapturerCaptureFailsClosedWhenParamsNil(t *testing.T) {
	capturer := &snapshotCapturer{logger: zap.NewNop()}

	_, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parameters service unavailable")
}

func TestSnapshotCapturerCaptureFailsClosedOnRevisionNotFound(t *testing.T) {
	capturer, revisions := newSnapshotCapturerFixture(t)
	revisions.found = false

	_, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "revision not found")
}

func TestSnapshotCapturerCaptureFailsClosedWhenRevisionServiceMissing(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)
	capturer.revisions = nil

	_, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "revision service unavailable")
}

func TestSnapshotCapturerLoadSubjectRejectsUnsupportedKind(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)

	_, _, err := capturer.loadSubject(context.Background(), "tenant-1", evaldomain.ResourceRef{
		Kind: evaldomain.ResourceKind("bogus"), ResourceID: "unknown-1",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported resource kind")
}

// fakeSkillBinding 返回固定的 skill→agent 绑定，供 skill 分支 capture pin 测试。
type fakeSkillBinding struct {
	agentID string
	found   bool
	err     error
}

func (f fakeSkillBinding) FindAgentBySkill(context.Context, string) (string, bool, error) {
	return f.agentID, f.found, f.err
}

// fakeBaselineAgentExecutor 是 agentRevisionExecutor 的桩：SnapshotRevision 返回
// 可哈希的有效 revision，供 CreatePublishedBaseline 幂等锁 pin。
type fakeBaselineAgentExecutor struct {
	revision agentdomain.AgentRevision
}

func (e *fakeBaselineAgentExecutor) SnapshotRevision(
	context.Context, string, string,
) (agentdomain.AgentRevision, error) {
	return e.revision, nil
}

func (e *fakeBaselineAgentExecutor) ExecuteRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta,
) (*agentapp.AgentResult, int, error) {
	return nil, 0, nil
}

func (e *fakeBaselineAgentExecutor) ExecuteSkillScenarioRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation,
) (*agentapp.AgentResult, int, error) {
	return nil, 0, nil
}

// TestCaptureSkillPin 验证 skill 资源评测创建时锁定承载 agent 的 published
// revision（D7）：FindAgentBySkill → CreatePublishedBaseline 幂等 pin → 快照写入
// SkillAgentRevision[skillID]。
func TestCaptureSkillPin(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)
	rev := agentdomain.AgentRevision{
		AgentID: "agent-1", Type: agentdomain.ReActAgent, SystemPrompt: "你是助手", Model: "qwen-max",
		MaxIterations:   3,
		ModelParameters: agentdomain.ModelParameters{MaxContextTokens: 8192, MaxTokens: 2048},
	}
	revisions := &fakeAgentRevisionSvc{found: true, revision: rev, createID: "pinned-rev-1"}
	capturer.revisions = revisions
	capturer.bindings = fakeSkillBinding{agentID: "agent-1", found: true}
	capturer.baselines = &agentEvaluationAdapter{
		revisions: revisions,
		agents:    &fakeBaselineAgentExecutor{revision: rev},
	}

	snap, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "rev-1",
		},
		RequestedBy: "user-1",
	})
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Equal(t, "pinned-rev-1", snap.PinnedAssignments.SkillAgentRevision["skill-1"])
	require.Equal(t, evaldomain.ResolvedExecution{ContextWindow: 8192, OutputReserve: 2048}, snap.ResolvedExecution)
}

// TestCaptureSkillPinFailsClosedWhenBindingMissing 验证 skill 分支 fail-closed：
// 无绑定 agent / resolver 未配置时创建失败。
func TestCaptureSkillPinFailsClosedWhenBindingMissing(t *testing.T) {
	capturer, _ := newSnapshotCapturerFixture(t)
	rev := agentdomain.AgentRevision{
		AgentID: "agent-1", Type: agentdomain.ReActAgent, SystemPrompt: "你是助手", Model: "qwen-max",
		MaxIterations: 3,
	}
	capturer.bindings = fakeSkillBinding{found: false}
	capturer.baselines = &agentEvaluationAdapter{
		revisions: &fakeAgentRevisionSvc{createID: "pinned-rev-1"},
		agents:    &fakeBaselineAgentExecutor{revision: rev},
	}

	_, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "rev-1",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no Agent bound")
}

// fakeSkillActivationResolver 是 agentport.SkillActivationResolver 的桩：按 ref
// 返回固定的 skill→revision 目录，模拟 run 创建时点该 skill 的 active 发布版。
type fakeSkillActivationResolver struct {
	revisionBySkill map[string]string
	err             error
}

func (f *fakeSkillActivationResolver) ResolveSkills(
	_ context.Context, _ string, refs []agentport.SkillRevisionRef,
) (map[string]agentport.SkillActivation, error) {
	if f.err != nil {
		return nil, f.err
	}
	catalog := make(map[string]agentport.SkillActivation, len(refs))
	for _, ref := range refs {
		rev, ok := f.revisionBySkill[ref.SkillID]
		if !ok {
			continue
		}
		catalog[ref.SkillID] = agentport.SkillActivation{SkillID: ref.SkillID, RevisionID: rev}
	}
	return catalog, nil
}

// TestSnapshotCapturerPinsBoundSkills 验证 agent 评测创建时把被测 agent 绑定的
// enabled skill 锚定到当时生效发布版（SkillRevisions），disabled 与未发布（resolver
// 未命中）的 skill 不 pin。
func TestSnapshotCapturerPinsBoundSkills(t *testing.T) {
	capturer, revisions := newSnapshotCapturerFixture(t)
	revisions.revision.Bindings = append(revisions.revision.Bindings,
		agentdomain.AgentBinding{Kind: agentdomain.AgentBindingSkill, ID: "skill-1", Enabled: true},
		agentdomain.AgentBinding{Kind: agentdomain.AgentBindingSkill, ID: "skill-2", Enabled: true},
		agentdomain.AgentBinding{Kind: agentdomain.AgentBindingSkill, ID: "skill-disabled", Enabled: false},
	)
	capturer.skills = &fakeSkillActivationResolver{revisionBySkill: map[string]string{
		"skill-1": "skill-rev-7",
		// skill-2 有绑定但无发布版：resolver 未命中 → 不 pin（fail-open）。
	}}

	snap, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1",
		},
		RequestedBy: "user-1",
	})
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Equal(t, map[string]string{"skill-1": "skill-rev-7"}, snap.PinnedAssignments.SkillRevisions)
	// MCP/Knowledge 既有 pin 不受影响。
	require.Equal(t, map[string]string{"mcp-1": "mcp-rev-9"}, snap.PinnedAssignments.MCPRevisions)
	require.Equal(t, map[string]string{"kb-1": "kb-rev-2"}, snap.PinnedAssignments.KnowledgeRevisions)
}

// TestSnapshotCapturerSkillPinFailsOpenOnResolveError 验证 skill resolver 读取失败
// 时 warn 跳过、不阻断创建（与 MCP/Knowledge D4 fail-open 语义一致）。
func TestSnapshotCapturerSkillPinFailsOpenOnResolveError(t *testing.T) {
	capturer, revisions := newSnapshotCapturerFixture(t)
	revisions.revision.Bindings = append(revisions.revision.Bindings,
		agentdomain.AgentBinding{Kind: agentdomain.AgentBindingSkill, ID: "skill-1", Enabled: true},
	)
	capturer.skills = &fakeSkillActivationResolver{err: errors.New("db down")}

	snap, err := capturer.Capture(context.Background(), "tenant-1", evalport.CaptureInput{
		Resource: evaldomain.ResourceRef{
			Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1",
		},
		RequestedBy: "user-1",
	})
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Empty(t, snap.PinnedAssignments.SkillRevisions)
}
