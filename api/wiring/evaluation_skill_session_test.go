package wiring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

// ——— agentScenarioEvaluationAdapter.RunSession（阶段 B §5.4 skill 场景会话逐轮执行） ———

// skillSessionAgentRevision 构造一条可解码的承载 agent revision（会话测试共用）：
// resolvePinnedAgent 经 fakeAgentRevisionSvc.Get 取回并 JSON 解码后交给执行器。
func skillSessionAgentRevision() *fakeAgentRevisionSvc {
	return &fakeAgentRevisionSvc{revision: agentdomain.AgentRevision{
		AgentID: "agent-1", Type: agentdomain.ReActAgent, SystemPrompt: "你是助手", Model: "qwen-max",
		MaxIterations:   3,
		ModelParameters: agentdomain.ModelParameters{MaxContextTokens: 8192, MaxTokens: 2048},
	}, found: true}
}

// skillSessionSnapshotCtx 构造携带 D7 skill 承载 agent pin 的 run 快照 ctx。
func skillSessionSnapshotCtx(resourceID, pinnedID string) context.Context {
	return evaldomain.WithEvalSnapshot(context.Background(), &evaldomain.EvaluationContextSnapshot{
		PinnedAssignments: evaldomain.PinnedAssignments{
			SkillAgentRevision: map[string]string{resourceID: pinnedID},
		},
	})
}

func skillSessionRef() evaldomain.ResourceRef {
	return evaldomain.ResourceRef{Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "revision-1"}
}

// skillScenarioTurnExecutor 是 skillScenarioExecutor 的 fake：记录每次
// ExecuteSkillScenarioRevision 的 ExecRequest/activations（断言会话续跑与激活透传），
// 并按调用序返回可区分的 AgentResult；failAtN>0 时第 N 次调用返回 sentinel 错误。
// OpenEvalConversation 记录调用并返回固定 convID（convErr 非空时首调即失败）。
type skillScenarioTurnExecutor struct {
	convID      string
	convErr     error
	requests    []agentapp.ExecRequest
	activations []agentport.SkillActivation
	outputs     []string
	failAtN     int
	failErr     error
	execCalls   int
	openCalls   int
	openTenant  string
	openAgent   string
	openUser    string
}

func (e *skillScenarioTurnExecutor) ExecuteSkillScenarioRevision(
	_ context.Context, _ agentdomain.AgentRevision, req agentapp.ExecRequest,
	_ agentapp.ExecMeta, activations []agentport.SkillActivation,
) (*agentapp.AgentResult, int, error) {
	e.execCalls++
	e.requests = append(e.requests, req)
	e.activations = append(e.activations, activations...)
	if e.failAtN == e.execCalls {
		return nil, 0, e.failErr
	}
	n := e.execCalls
	output := fmt.Sprintf("out-%d", n)
	if n-1 < len(e.outputs) {
		output = e.outputs[n-1]
	}
	return &agentapp.AgentResult{Output: output, TokensUsed: n * 10, CostUSD: 0.01 * float64(n)}, n * 20, nil
}

func (e *skillScenarioTurnExecutor) OpenEvalConversation(_ context.Context, tenantID, agentID, userID string) (string, error) {
	e.openCalls++
	e.openTenant, e.openAgent, e.openUser = tenantID, agentID, userID
	if e.convErr != nil {
		return "", e.convErr
	}
	return e.convID, nil
}

func TestAgentScenarioSkillAdapterRunSessionDrivesTurnsOnOneConversation(t *testing.T) {
	script := evaldomain.EvalSessionScript{Goal: "解答用户", Turns: []evaldomain.SessionTurn{
		{User: "开场问题"}, {User: "追问细节"},
	}}
	executor := &skillScenarioTurnExecutor{convID: "conv-eval-1", outputs: []string{"开场答复", "追问答复"}}
	adapter := agentScenarioEvaluationAdapter{
		agents: executor, revisions: skillSessionAgentRevision(),
		bindings: &ctxCaptureSkillBinding{agentID: "agent-1"}, skills: &ctxCaptureSkillResolver{},
	}

	evidences, err := adapter.RunSession(skillSessionSnapshotCtx("skill-1", "pinned-rev-1"), "tenant-1", "user-1",
		skillSessionRef(), script)
	require.NoError(t, err)
	require.Len(t, evidences, 2)

	if evidences[0].Index != 0 || evidences[0].User != "开场问题" || evidences[0].Output != "开场答复" {
		t.Fatalf("turn-0 evidence mismatch: %+v", evidences[0])
	}
	if evidences[1].Index != 1 || evidences[1].User != "追问细节" || evidences[1].Output != "追问答复" {
		t.Fatalf("turn-1 evidence mismatch: %+v", evidences[1])
	}
	// 每轮独立 trace（逐轮证据各自可追溯）。
	if evidences[0].TraceID == "" || evidences[0].TraceID == evidences[1].TraceID {
		t.Fatalf("per-turn trace must be non-empty and unique: %+v / %+v", evidences[0], evidences[1])
	}
	// 聚合语义：tokens=n*10、duration=n*20。
	if evidences[0].Tokens != 10 || evidences[1].Tokens != 20 ||
		evidences[0].DurationMs != 20 || evidences[1].DurationMs != 40 {
		t.Fatalf("per-turn metrics mismatch: %+v / %+v", evidences[0], evidences[1])
	}

	// 恰开一条受控会话，且以锁定承载 agent + requestedBy 透传。
	if executor.openCalls != 1 || executor.openTenant != "tenant-1" ||
		executor.openAgent != "agent-1" || executor.openUser != "user-1" {
		t.Fatalf("conversation opener driven wrong: %+v", executor)
	}
	// 逐轮以同一会话续跑：ExecRequest.ConversationID 恒为受控会话、Query 依剧本顺序。
	require.Len(t, executor.requests, 2)
	for i, req := range executor.requests {
		if req.ConversationID != "conv-eval-1" || req.UserID != "user-1" {
			t.Fatalf("turn %d did not continue evaluation conversation: %+v", i, req)
		}
		if req.Query != script.Turns[i].User {
			t.Fatalf("turn %d query = %q, want %q", i, req.Query, script.Turns[i].User)
		}
	}
	// skill 激活逐轮注入同一被测 revision。
	require.Len(t, executor.activations, 2)
	for _, act := range executor.activations {
		if act.SkillID != "skill-1" || act.RevisionID != "revision-1" {
			t.Fatalf("activation mismatch: %+v", act)
		}
	}
}

func TestAgentScenarioSkillAdapterRunSessionFailsClosedWithoutExecutor(t *testing.T) {
	adapter := agentScenarioEvaluationAdapter{}
	_, err := adapter.RunSession(context.Background(), "tenant-1", "user-1", skillSessionRef(),
		evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "x"}}})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "skill executor unavailable") {
		t.Fatalf("expected executor fail-close, got %v", err)
	}
}

func TestAgentScenarioSkillAdapterRunSessionFailsClosedWithoutSnapshot(t *testing.T) {
	executor := &skillScenarioTurnExecutor{convID: "conv-eval-1"}
	adapter := agentScenarioEvaluationAdapter{agents: executor, revisions: skillSessionAgentRevision()}

	_, err := adapter.RunSession(context.Background(), "tenant-1", "user-1", skillSessionRef(),
		evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "x"}}})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "evaluation context snapshot required") {
		t.Fatalf("expected snapshot fail-close, got %v", err)
	}
	if executor.openCalls != 0 {
		t.Fatalf("conversation must not open without snapshot: calls=%d", executor.openCalls)
	}
}

func TestAgentScenarioSkillAdapterRunSessionFailsClosedWithoutPinnedRevision(t *testing.T) {
	executor := &skillScenarioTurnExecutor{convID: "conv-eval-1"}
	adapter := agentScenarioEvaluationAdapter{
		agents: executor, revisions: skillSessionAgentRevision(),
		bindings: &ctxCaptureSkillBinding{agentID: "agent-1"}, skills: &ctxCaptureSkillResolver{},
	}

	// 快照存在但 skill 未 pin 承载 agent revision → fail-closed，提示 recreate run。
	ctx := evaldomain.WithEvalSnapshot(context.Background(), &evaldomain.EvaluationContextSnapshot{
		PinnedAssignments: evaldomain.PinnedAssignments{SkillAgentRevision: map[string]string{}},
	})
	_, err := adapter.RunSession(ctx, "tenant-1", "user-1", skillSessionRef(),
		evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "x"}}})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "no pinned agent revision") ||
		!strings.Contains(err.Error(), "recreate the run") {
		t.Fatalf("expected pinned-revision fail-close, got %v", err)
	}
	if executor.openCalls != 0 || executor.execCalls != 0 {
		t.Fatalf("no turn may run without pinned agent: open=%d exec=%d", executor.openCalls, executor.execCalls)
	}
}

// noSkillsResolver 返回空激活目录：模拟被测 skill revision 在目标租户不可用
// （fail-closed：会话开起来之前即拒绝，绝不带空激活去执行）。
type noSkillsResolver struct{}

func (noSkillsResolver) ResolveSkills(
	context.Context, string, []agentport.SkillRevisionRef,
) (map[string]agentport.SkillActivation, error) {
	return map[string]agentport.SkillActivation{}, nil
}

func TestAgentScenarioSkillAdapterRunSessionFailsClosedWhenSkillUnavailable(t *testing.T) {
	executor := &skillScenarioTurnExecutor{convID: "conv-eval-1"}
	adapter := agentScenarioEvaluationAdapter{
		agents: executor, revisions: skillSessionAgentRevision(),
		bindings: &ctxCaptureSkillBinding{agentID: "agent-1"}, skills: noSkillsResolver{},
	}

	_, err := adapter.RunSession(skillSessionSnapshotCtx("skill-1", "pinned-rev-1"), "tenant-1", "user-1",
		skillSessionRef(), evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "x"}}})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "is not available") {
		t.Fatalf("expected skill-unavailable fail-close, got %v", err)
	}
	if executor.openCalls != 0 || executor.execCalls != 0 {
		t.Fatalf("no turn may run when skill unavailable: open=%d exec=%d", executor.openCalls, executor.execCalls)
	}
}

func TestAgentScenarioSkillAdapterRunSessionKeepsPartialEvidenceOnMidTurnFailure(t *testing.T) {
	script := evaldomain.EvalSessionScript{Goal: "三轮问答", Turns: []evaldomain.SessionTurn{
		{User: "开场问题"}, {User: "追问细节"}, {User: "收尾确认"},
	}}
	wantFlake := errors.New("provider flaked at turn 2")
	executor := &skillScenarioTurnExecutor{
		convID: "conv-eval-1", outputs: []string{"第一轮答复", "第二轮答复", "第三轮答复"},
		failAtN: 2, failErr: wantFlake,
	}
	adapter := agentScenarioEvaluationAdapter{
		agents: executor, revisions: skillSessionAgentRevision(),
		bindings: &ctxCaptureSkillBinding{agentID: "agent-1"}, skills: &ctxCaptureSkillResolver{},
	}

	evidences, err := adapter.RunSession(skillSessionSnapshotCtx("skill-1", "pinned-rev-1"), "tenant-1", "user-1",
		skillSessionRef(), script)
	// 第二轮失败：保留第一轮 partial evidence，且错误绝不吞没。
	require.ErrorIs(t, err, wantFlake)
	if !strings.Contains(err.Error(), "execute skill scenario revision") {
		t.Fatalf("expected wrapped adapter error, got %v", err)
	}
	require.Len(t, evidences, 1)
	if evidences[0].Index != 0 || evidences[0].User != "开场问题" || evidences[0].Output != "第一轮答复" {
		t.Fatalf("partial evidence mismatch: %+v", evidences[0])
	}
	if executor.openCalls != 1 || executor.execCalls != 2 {
		t.Fatalf("expected one conversation and two executed turns, open=%d exec=%d",
			executor.openCalls, executor.execCalls)
	}
}

func TestAgentScenarioSkillAdapterRunSessionFailsClosedWhenConversationCannotOpen(t *testing.T) {
	wantErr := errors.New("chat store unavailable")
	executor := &skillScenarioTurnExecutor{convID: "conv-eval-1", convErr: wantErr}
	adapter := agentScenarioEvaluationAdapter{
		agents: executor, revisions: skillSessionAgentRevision(),
		bindings: &ctxCaptureSkillBinding{agentID: "agent-1"}, skills: &ctxCaptureSkillResolver{},
	}

	_, err := adapter.RunSession(skillSessionSnapshotCtx("skill-1", "pinned-rev-1"), "tenant-1", "user-1",
		skillSessionRef(), evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "x"}}})
	require.ErrorIs(t, err, wantErr)
	if !strings.Contains(err.Error(), "open evaluation conversation") {
		t.Fatalf("expected conversation-open error, got %v", err)
	}
	if executor.execCalls != 0 {
		t.Fatalf("no turn may run when conversation cannot open: exec=%d", executor.execCalls)
	}
}

// TestEvaluationRouterRunSessionDispatchesSkillAdapterToRunSession 覆盖 router 对 skill
// 资源的会话分派：buildEvaluation 中 skill 资源映射到 agentScenarioEvaluationAdapter，
// RunSession 实现后经类型断言路由到 skill 会话执行（非 fail-close 文案）。
func TestEvaluationRouterRunSessionDispatchesSkillAdapterToRunSession(t *testing.T) {
	executor := &skillScenarioTurnExecutor{convID: "conv-eval-1", outputs: []string{"skill 答复"}}
	skillAdapter := agentScenarioEvaluationAdapter{
		agents: executor, revisions: skillSessionAgentRevision(),
		bindings: &ctxCaptureSkillBinding{agentID: "agent-1"}, skills: &ctxCaptureSkillResolver{},
	}
	router := evaluationResourceRouter{adapters: map[evaldomain.ResourceKind]evalport.ResourceAdapter{
		evaldomain.ResourceKindSkill: skillAdapter,
	}}

	evidences, err := router.RunSession(skillSessionSnapshotCtx("skill-1", "pinned-rev-1"), "tenant-1", "user-1",
		skillSessionRef(), evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "skill 提问"}}})
	require.NoError(t, err)
	require.Len(t, evidences, 1)
	if evidences[0].Output != "skill 答复" {
		t.Fatalf("skill session output mismatch: %+v", evidences[0])
	}
	if executor.openCalls != 1 {
		t.Fatalf("router dispatch must reach skill RunSession, open=%d", executor.openCalls)
	}
}
