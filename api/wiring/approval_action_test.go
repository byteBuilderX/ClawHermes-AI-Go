package wiring

import (
	"context"
	"errors"
	"testing"

	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	mcpapp "github.com/byteBuilderX/stratum/internal/mcp/application"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
	mcp "github.com/byteBuilderX/stratum/internal/mcp/infrastructure"
	paramport "github.com/byteBuilderX/stratum/internal/parameters/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// notExecutedError 断言 err 是 ApprovalActionNotExecutedError（预执行失败 → 审批释放回
// approved 可重试，而非烧成 unknown_outcome）。
func notExecutedError(t *testing.T, err error) {
	t.Helper()
	var notExec *port.ApprovalActionNotExecutedError
	require.ErrorAs(t, err, &notExec, "expected ApprovalActionNotExecutedError, got %v", err)
}

func TestApprovalActionExecutorSubjectDispatch(t *testing.T) {
	e := &approvalActionExecutor{}
	ctx := context.Background()

	t.Run("evaluation subject not configured fails closed", func(t *testing.T) {
		_, err := e.ExecuteApprovalAction(ctx, port.ApprovalActionRequest{
			TenantID: "t1", SubjectKind: agentdomain.SubjectKindEvaluationAction,
			Arguments: map[string]any{"operation": "pause_experiment"},
		})
		notExecutedError(t, err)
	})

	t.Run("mcp config subjects degrade to not-executed", func(t *testing.T) {
		for _, kind := range []string{agentdomain.SubjectKindMCPPolicy, agentdomain.SubjectKindMCPServer} {
			_, err := e.ExecuteApprovalAction(ctx, port.ApprovalActionRequest{
				TenantID: "t1", SubjectKind: kind, Arguments: map[string]any{},
			})
			notExecutedError(t, err)
		}
	})

	t.Run("unknown subject fails closed", func(t *testing.T) {
		_, err := e.ExecuteApprovalAction(ctx, port.ApprovalActionRequest{
			TenantID: "t1", SubjectKind: "unknown_kind", Arguments: map[string]any{},
		})
		notExecutedError(t, err)
	})
}

// TestEvaluationOperationsComplete 防 dispatch table 遗漏：14 个写 op 必须全部注册，
// 否则审批执行与直接执行路径分叉（D4 核心不变量）。
func TestEvaluationOperationsComplete(t *testing.T) {
	ops := []string{
		"create_suite", "publish_suite", "generate_suite_cases", "enqueue_run",
		"create_experiment", "pause_experiment", "promote_experiment", "rollback_experiment",
		"rollback_platform", "rollback_resource", "publish_platform_version",
		"reject_candidate", "create_baseline", "generate_optimization",
	}
	require.Len(t, evaluationOperations, len(ops), "dispatch table must cover exactly the 14 evaluation write operations")
	for _, op := range ops {
		require.NotNil(t, evaluationOperations[op], "operation %q missing from dispatch table", op)
	}
}

func TestExecuteEvaluationUnsupportedOperation(t *testing.T) {
	e := &approvalActionExecutor{}
	_, err := e.executeEvaluation(context.Background(), port.ApprovalActionRequest{
		TenantID: "t1", SubjectKind: agentdomain.SubjectKindEvaluationAction,
		Arguments: map[string]any{"operation": "rm -rf"},
	})
	notExecutedError(t, err)
}

func TestNotExecutedWrapsCause(t *testing.T) {
	err := notExecuted(errors.New("provider down"))
	notExecutedError(t, err)
	require.Contains(t, err.Error(), "provider down")
}

func TestApprovalActionArgHelpers(t *testing.T) {
	args := map[string]any{
		"name":     "suite-a",
		"maxCases": float64(42),
		"tags":     []any{"a", "b"},
		"enabled":  true,
	}
	require.Equal(t, "suite-a", asString(args, "name"))
	require.Empty(t, asString(args, "missing"))
	require.Equal(t, 42, asInt(args, "maxCases"))
	require.Equal(t, 0, asInt(args, "missing"))
	require.Equal(t, []string{"a", "b"}, asStringSlice(args, "tags"))
	require.True(t, asBool(args, "enabled"))
	require.False(t, asBool(args, "missing"))
	// 缺失 key 返回 nil（而非空 slice）：OptimizationFingerprint 序列化 nil 为
	// null、[] 为空数组，跨路径必须一致，否则幂等键分叉。
	require.Nil(t, asStringSlice(args, "missing"))
}

func TestApprovalActionResourceRefHelper(t *testing.T) {
	args := map[string]any{"resource": map[string]any{
		"kind": "agent", "id": "agent-1", "revision_id": "rev-1",
	}}
	ref := asResourceRef(args, "resource")
	require.Equal(t, evaldomain.ResourceKind("agent"), ref.Kind)
	require.Equal(t, "agent-1", ref.ResourceID)
	require.Equal(t, "rev-1", ref.RevisionID)
	// 缺失字段返回零值，不 panic。
	require.Equal(t, evaldomain.ResourceRef{}, asResourceRef(map[string]any{}, "missing"))
}

func TestApprovalActionEvalCasesHelper(t *testing.T) {
	args := map[string]any{"cases": []any{
		map[string]any{"name": "c1", "input": "x", "expected_output": "y", "assertion_mode": "exact", "enabled": true},
		map[string]any{"name": "c2"}, // 仅必填字段
		nil,                          // 异常项跳过
	}}
	cases := asEvalCases(args, "cases")
	require.Len(t, cases, 2)
	require.Equal(t, "c1", cases[0].Name)
	require.Equal(t, evaldomain.AssertionMode("exact"), cases[0].AssertionMode)
	require.True(t, cases[0].Enabled)
	require.Equal(t, "c2", cases[1].Name)
}

func TestApprovalActionSearchSpaceHelper(t *testing.T) {
	// JSONB 反序列化形态：map[string]any + []any。
	jsonb := map[string]any{"searchSpace": map[string]any{
		"temperature": []any{float64(0.1), float64(0.7)},
	}}
	space := asSearchSpace(jsonb, "searchSpace")
	require.Len(t, space["temperature"], 2)

	// 内存直传形态：map[string][]any。
	direct := map[string]any{"searchSpace": map[string][]any{"topK": {float64(3)}}}
	require.Len(t, asSearchSpace(direct, "searchSpace")["topK"], 1)

	// 缺失返回空 map，不 panic。
	require.Empty(t, asSearchSpace(map[string]any{}, "missing"))
}

func TestApprovalActionExperimentCommandHelper(t *testing.T) {
	req := port.ApprovalActionRequest{
		DecidedBy: "user-admin",
		Arguments: map[string]any{
			"reason": "reviewed", "idempotencyKey": "idem-1",
			"expectedStateVersion": float64(3),
		},
	}
	cmd := asExperimentCommand(req, "reason")
	require.Equal(t, "user-admin", cmd.ActorID)
	require.Equal(t, "reviewed", cmd.Reason)
	require.Equal(t, "idem-1", cmd.IdempotencyKey)
	require.Equal(t, int64(3), cmd.ExpectedStateVersion)
}

// toolPolicyRecorder 记录 SetToolPolicy Upsert，模拟 ToolPolicyRepo。
type toolPolicyRecorder struct {
	upserts []mcpdomain.ToolPolicy
}

func (r *toolPolicyRecorder) Get(context.Context, string, string) (mcpdomain.ToolPolicy, bool, error) {
	return mcpdomain.ToolPolicy{}, false, nil
}
func (r *toolPolicyRecorder) List(context.Context) ([]mcpdomain.ToolPolicy, error) { return nil, nil }
func (r *toolPolicyRecorder) Upsert(_ context.Context, p mcpdomain.ToolPolicy) error {
	r.upserts = append(r.upserts, p)
	return nil
}

// newMCPExecutorSvc 构造带 fake ToolPolicyRepo 的 MCPService，供 MCP 审批执行器分发测试。
func newMCPExecutorSvc(t *testing.T) (*mcpapp.MCPService, *toolPolicyRecorder) {
	t.Helper()
	logger := zap.NewNop()
	manager := mcp.NewClientManager(logger, nil, nil)
	registry := mcp.NewMCPToolRegistry(manager, logger)
	svc := mcpapp.NewMCPService(mcp.ToolRegistryAsPort(registry), mcp.ServerManagerAsPort(manager), logger)
	repo := &toolPolicyRecorder{}
	svc.SetToolPolicyRepo(repo)
	return svc, repo
}

// D5：MCP 策略/服务器配置审批执行分发。set_tool_policy 用 DecidedBy 作为 actor
// （审批人以自身 admin/owner 权限执行，member 发起的 connect 审批通过后 ownership 校验可过）。
func TestApprovalActionExecutorMCPDispatch(t *testing.T) {
	svc, repo := newMCPExecutorSvc(t)
	e := &approvalActionExecutor{mcpSvc: svc}

	t.Run("set_tool_policy uses DecidedBy as actor", func(t *testing.T) {
		_, err := e.ExecuteApprovalAction(context.Background(), port.ApprovalActionRequest{
			TenantID: "t1", SubjectKind: agentdomain.SubjectKindMCPPolicy, DecidedBy: "admin-1",
			Arguments: map[string]any{"operation": "set_tool_policy", "serverId": "srv", "toolName": "tool", "riskLevel": "write_reversible"},
		})
		require.NoError(t, err)
		require.Len(t, repo.upserts, 1)
		require.Equal(t, "admin-1", repo.upserts[0].UpdatedBy)
		require.Equal(t, mcpdomain.ToolRiskWriteReversible, repo.upserts[0].RiskLevel)
	})

	t.Run("unsupported mcp operation fails closed", func(t *testing.T) {
		_, err := e.ExecuteApprovalAction(context.Background(), port.ApprovalActionRequest{
			TenantID: "t1", SubjectKind: agentdomain.SubjectKindMCPServer, DecidedBy: "admin-1",
			Arguments: map[string]any{"operation": "rm -rf"},
		})
		notExecutedError(t, err)
	})

	t.Run("delete_server dispatches to DeleteServer", func(t *testing.T) {
		_, err := e.ExecuteApprovalAction(context.Background(), port.ApprovalActionRequest{
			TenantID: "t1", SubjectKind: agentdomain.SubjectKindMCPServer, DecidedBy: "admin-1",
			Arguments: map[string]any{"operation": "delete_server", "serverId": "missing"},
		})
		// 真实 manager 对不存在 server fail closed；断言错误路径已传播而非静默成功。
		require.Error(t, err)
	})
}

// system_key 已随「所有 MCP server 一视同仁」从契约删除：外部提交的 system_key 由
// json.Unmarshal 静默忽略（列同时删除、无处落库），decodeServerConfig 不再归一化、
// 不再拒绝。测试锚定「忽略」语义，防止回退到旧的拒绝行为。
func TestDecodeServerConfigIgnoresSystemKey(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{name: "literal null", args: map[string]any{"config": map[string]any{
			"name": "svr", "transport": "streamable-http", "url": "http://localhost:9000",
			"system_key": nil,
		}}},
		{name: "no system_key key", args: map[string]any{"config": map[string]any{
			"name": "svr", "transport": "streamable-http", "url": "http://localhost:9000",
		}}},
		{name: "foreign system_key value ignored", args: map[string]any{"config": map[string]any{
			"name": "svr", "transport": "streamable-http", "url": "http://localhost:9000", "system_key": "stratum.platform_mcp",
		}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := decodeServerConfig(tc.args)
			require.NoError(t, err, "config with system_key must decode and ignore it")
			require.Equal(t, "svr", cfg.Name)
			require.Equal(t, "http://localhost:9000", cfg.URL)
		})
	}
}

// asStringSlice 兼容 JSON 持久化形态 []any 与内存直传形态 []string（评审建议）。
func TestAsStringSliceBothForms(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{name: "json round-tripped []any", args: map[string]any{"editors": []any{"e1", "e2"}}, want: []string{"e1", "e2"}},
		{name: "in-memory []string", args: map[string]any{"editors": []string{"e1"}}, want: []string{"e1"}},
		{name: "missing key returns nil", args: map[string]any{}, want: nil},
		{name: "nil slice returns nil", args: map[string]any{"editors": nil}, want: nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, asStringSlice(tc.args, "editors"))
		})
	}
}

// gateActionRecorder 记录 IncEvalGateAction，供 auto_refused 计数断言（R28）。
// 内嵌 NoopMetrics 免实现 MetricsProvider 全接口；指针接收者覆盖计数方法。
type gateActionRecorder struct {
	observability.NoopMetrics
	actions []string
}

func (r *gateActionRecorder) IncEvalGateAction(layer, action string) {
	r.actions = append(r.actions, layer+":"+action)
}

// fakePlatformOps 是 platformVersionOps 的最小内存实现：记录 Rollback/Publish/
// UpdateEvalState 收到的参数，Versions 返回预置版本列表，供平台三分支 happy/failure 单测。
type fakePlatformOps struct {
	versions     []paramport.PlatformVersion
	rollbackID   int64
	publishID    int64
	lastState    string
	lastStateSeq int64
	err          error // 非 nil 时所有方法返回该错误（fail-closed 上抛）
}

func (f *fakePlatformOps) Versions(context.Context, string) ([]paramport.PlatformVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.versions, nil
}

func (f *fakePlatformOps) Publish(_ context.Context, _ string, versionID int64, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.publishID = versionID
	return nil
}

func (f *fakePlatformOps) Rollback(_ context.Context, _ string, versionID int64, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.rollbackID = versionID
	return nil
}

func (f *fakePlatformOps) UpdateEvalState(_ context.Context, _ string, versionSeq int64, state, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.lastState, f.lastStateSeq = state, versionSeq
	return nil
}

// fakeResourceRollback 记录 GateTarget，供 rollback_resource 分派断言；err 非 nil 时返回。
type fakeResourceRollback struct {
	calls []evaldomain.GateTarget
	err   error
}

func (f *fakeResourceRollback) Rollback(_ context.Context, _ string, target evaldomain.GateTarget, _, _, _ string) error {
	f.calls = append(f.calls, target)
	return f.err
}

// R28 auto 拒绝不变量：Arguments auto=true → 类型化 sentinel + l3_platform:auto_refused
// 计数；guard 在 paramSvc 任何调用之前（paramSvc 存在时 Rollback 也不得发生）。
func TestPlatformRollbackAutoRefused(t *testing.T) {
	var rec gateActionRecorder
	stub := &fakePlatformOps{}
	e := &approvalActionExecutor{paramSvc: stub, metrics: &rec}
	_, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1",
		Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2), "auto": true},
	})
	require.ErrorIs(t, err, evalport.ErrAutoRollbackForbidden)
	require.Equal(t, int64(0), stub.rollbackID, "auto rollback must not reach paramSvc")
	require.Equal(t, []string{constants.GateLayerL3Platform + ":" + constants.GateActionAutoRefused}, rec.actions)
}

func TestPlatformRollbackHappy(t *testing.T) {
	stub := &fakePlatformOps{}
	e := &approvalActionExecutor{paramSvc: stub}
	out, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1", Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)},
	})
	require.NoError(t, err)
	require.Equal(t, "rolled_back", out["status"])
	require.Equal(t, int64(2), stub.rollbackID)
}

func TestPlatformRollbackMissingArgsFailsClosed(t *testing.T) {
	e := &approvalActionExecutor{paramSvc: &fakePlatformOps{}}
	_, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{
		Arguments: map[string]any{"group_key": "", "version_id": float64(0)},
	})
	notExecutedError(t, err)
}

// publish 前置哨兵断言：eval_state != sentinel_passed → notExecuted（fail-closed），Publish 未发生。
func TestPlatformPublishRequiresSentinelPassed(t *testing.T) {
	stub := &fakePlatformOps{versions: []paramport.PlatformVersion{
		{ID: 2, VersionSeq: 3, Status: "draft", EvalState: "unknown"},
	}}
	e := &approvalActionExecutor{paramSvc: stub}
	_, err := executePlatformPublishGated(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1", Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)},
	})
	notExecutedError(t, err)
	require.Equal(t, int64(0), stub.publishID)
}

// publish happy：id→seq 桥（Publish 收行 PK id=2，UpdateEvalState 收 seq=3）+ 发布后回写 sentinel_passed。
func TestPlatformPublishHappy(t *testing.T) {
	stub := &fakePlatformOps{versions: []paramport.PlatformVersion{
		{ID: 2, VersionSeq: 3, Status: "draft", EvalState: constants.PlatformEvalStateSentinelPassed},
	}}
	e := &approvalActionExecutor{paramSvc: stub}
	out, err := executePlatformPublishGated(context.Background(), e, port.ApprovalActionRequest{
		DecidedBy: "admin-1", Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), stub.publishID)
	require.Equal(t, constants.PlatformEvalStateSentinelPassed, stub.lastState)
	require.Equal(t, int64(3), stub.lastStateSeq)
	require.Equal(t, 3, out["version_seq"]) // VersionSeq 为 int；分支返回 int
}

func TestResourceRollbackDispatchToExecutor(t *testing.T) {
	stub := &fakeResourceRollback{}
	e := &approvalActionExecutor{resourceRollback: stub}
	out, err := executeResourceRollback(context.Background(), e, port.ApprovalActionRequest{
		TenantID: "t1", DecidedBy: "admin-1",
		Arguments: map[string]any{"resource_kind": "agent", "resource_id": "agent-1", "target_revision_id": "rev-9"},
	})
	require.NoError(t, err)
	require.Equal(t, "rolled_back", out["status"])
	require.Len(t, stub.calls, 1)
	require.Equal(t, evaldomain.ScopeResource, stub.calls[0].Scope)
	require.Equal(t, "agent", stub.calls[0].Kind)
	require.Equal(t, "agent-1", stub.calls[0].ResourceID)
	require.Equal(t, "rev-9", stub.calls[0].RevisionID)
}

// rollback_resource 失败（含 Task 3 ErrRollbackUnsupported）单事务无副作用 → notExecuted 可重试。
func TestResourceRollbackFailureNotExecuted(t *testing.T) {
	e := &approvalActionExecutor{resourceRollback: &fakeResourceRollback{err: evalport.ErrRollbackUnsupported}}
	_, err := executeResourceRollback(context.Background(), e, port.ApprovalActionRequest{
		TenantID: "t1", DecidedBy: "admin-1",
		Arguments: map[string]any{"resource_kind": "mcp", "resource_id": "srv-1", "target_revision_id": "rev-9"},
	})
	notExecutedError(t, err)
}

func TestPlatformBranchesFailClosedWhenUnconfigured(t *testing.T) {
	e := &approvalActionExecutor{}
	_, err := executePlatformRollback(context.Background(), e, port.ApprovalActionRequest{Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)}})
	notExecutedError(t, err)
	_, err = executePlatformPublishGated(context.Background(), e, port.ApprovalActionRequest{Arguments: map[string]any{"group_key": "evaluation", "version_id": float64(2)}})
	notExecutedError(t, err)
	_, err = executeResourceRollback(context.Background(), e, port.ApprovalActionRequest{TenantID: "t1", Arguments: map[string]any{}})
	notExecutedError(t, err)
}
