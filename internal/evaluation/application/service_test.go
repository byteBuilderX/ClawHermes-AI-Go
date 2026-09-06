package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

// snapFixture 是评测上下文快照的最小合法 fixture（创建时捕获集成后的
// Run/RunStored 入口需要 ctx 携带快照，否则 fail-closed 拒绝执行）。
func snapFixture() *domain.EvaluationContextSnapshot {
	return &domain.EvaluationContextSnapshot{
		SchemaVersion: domain.SnapshotSchemaVersion,
		Evaluation:    domain.GroupSnapshot{GroupKey: domain.GroupEvaluation},
		Execution: []domain.GroupSnapshot{
			{GroupKey: domain.GroupAgent},
			{GroupKey: domain.GroupTrace},
		},
	}
}

// snapshotCtx 返回注入 snapFixture 的 context，模拟 RunStored 已注入快照的执行入口。
func snapshotCtx() context.Context {
	return domain.WithEvalSnapshot(context.Background(), snapFixture())
}

func TestServiceRunEvaluatesEnabledCasesAndPersistsResults(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{
		"case-1": "订单已经发货",
		"case-2": map[string]any{"label": "refund"},
	}}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{
			ID: "suite-version-1",
			Cases: []domain.EvalCase{
				{ID: "case-1", Input: "物流状态", ExpectedOutput: "发货", AssertionMode: domain.AssertionContains, Enabled: true},
				{ID: "case-2", Input: "我要退款", ExpectedOutput: map[string]any{"label": "refund"}, AssertionMode: domain.AssertionExact, Enabled: true},
				{ID: "disabled", Input: "忽略", ExpectedOutput: "x", AssertionMode: domain.AssertionExact, Enabled: false},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.TotalCases != 2 || run.PassedCases != 2 || !run.Passed {
		t.Fatalf("unexpected summary: %+v", run)
	}
	if len(run.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(run.Results))
	}
	if repo.saved.ID != run.ID {
		t.Fatal("run was not persisted")
	}
	if adapter.tenantID != "tenant-1" || repo.tenantID != "tenant-1" {
		t.Fatalf("tenant id was not propagated: adapter=%q repo=%q", adapter.tenantID, repo.tenantID)
	}
}

// TestServiceRunPersistsContextSnapshotOnRun 覆盖 spec §7 落库接线：Service.Run
// 把注入 ctx 的创建时快照挂到 EvalRun.ContextSnapshot，随 SaveRun 持久化到
// eval_runs.context_snapshot（fake repo 记录断言）。
func TestServiceRunPersistsContextSnapshotOnRun(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "ok"}}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)
	snap := snapFixture()

	run, err := svc.Run(domain.WithEvalSnapshot(context.Background(), snap), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "物流状态", ExpectedOutput: "发货", AssertionMode: domain.AssertionContains, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.ContextSnapshot == nil {
		t.Fatal("returned run must carry the context snapshot")
	}
	if repo.saved.ID != run.ID {
		t.Fatal("run was not persisted")
	}
	// 传入的是注入 ctx 的同一个快照指针（Run 直接透传，未复制/改写）。
	if repo.saved.ContextSnapshot == nil {
		t.Fatal("persisted run must carry a non-nil context snapshot")
	}
	if repo.saved.ContextSnapshot != snap {
		t.Fatalf("persisted snapshot %+v does not match injected snapshot %+v", repo.saved.ContextSnapshot, snap)
	}
}

func TestServiceRunPersistsExecutionErrorsAsFailedCases(t *testing.T) {
	adapter := &fakeAdapter{errCase: "case-1"}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{ID: "suite-version-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "input", ExpectedOutput: "output", AssertionMode: domain.AssertionExact, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned orchestration error: %v", err)
	}
	if run.Passed || run.Results[0].Error == "" {
		t.Fatalf("expected failed case with error, got %+v", run.Results[0])
	}
	if repo.saved.Results[0].Error == "" {
		t.Fatal("failed case was not persisted")
	}
}

func TestServiceRunStoredLoadsPublishedSuiteRevision(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "物流问题"}}
	runRepo := &fakeRunRepo{}
	suiteRepo := &fakeSuiteRepo{revision: domain.EvalSuiteRevision{
		ID: "suite-revision-1", SuiteID: "suite-1", Status: domain.SuiteRevisionPublished,
		ResourceKind: domain.ResourceKindSkill,
		Cases:        []domain.EvalCase{{ID: "case-1", Input: "快递没更新", ExpectedOutput: "物流", AssertionMode: domain.AssertionContains, Enabled: true}},
	}}
	svc := NewService(adapter, runRepo, nil, nil, suiteRepo)

	run, err := svc.RunStored(context.Background(), "tenant-1", "user-1", domain.ResourceRef{
		Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2",
	}, "suite-revision-1", snapFixture())
	if err != nil {
		t.Fatalf("RunStored returned error: %v", err)
	}
	if !run.Passed || run.SuiteRevisionID != "suite-revision-1" {
		t.Fatalf("unexpected run: %+v", run)
	}
}

// TestServiceRunFailsClosedWhenContextSnapshotMissing 覆盖 Run 入口的无快照
// fail-closed（brief 要求：Run 无快照 → 拒绝执行）。ctx 未注入评测快照时
// Run 必须返回错误，不得静默继续。
func TestServiceRunFailsClosedWhenContextSnapshotMissing(t *testing.T) {
	adapter := &fakeAdapter{}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)

	_, err := svc.Run(context.Background(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{ID: "suite-version-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "x", ExpectedOutput: "y", AssertionMode: domain.AssertionExact, Enabled: true},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "context snapshot missing") {
		t.Fatalf("Run without context snapshot must fail closed, got err=%v", err)
	}
}

// TestServiceRunStoredFailsClosedWhenSnapshotNil 覆盖 RunStored 入口的 nil 快照
// fail-closed（brief 要求：RunStored 无快照 → 拒绝执行）。snapshot 参数为 nil
// 时 RunStored 必须返回错误，不得静默回退。
func TestServiceRunStoredFailsClosedWhenSnapshotNil(t *testing.T) {
	adapter := &fakeAdapter{}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)

	_, err := svc.RunStored(context.Background(), "tenant-1", "user-1", domain.ResourceRef{
		Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2",
	}, "suite-revision-1", nil)
	if err == nil || !strings.Contains(err.Error(), "context snapshot missing") {
		t.Fatalf("RunStored with nil snapshot must fail closed, got err=%v", err)
	}
}

func TestServiceGetRunReturnsPersistedRun(t *testing.T) {
	repo := &fakeRunRepo{saved: domain.EvalRun{ID: "run-1", Passed: true}}
	svc := NewService(&fakeAdapter{}, repo, nil, nil)

	run, err := svc.GetRun(context.Background(), "tenant-1", "run-1")
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if run.ID != "run-1" || !run.Passed {
		t.Fatalf("unexpected run: %+v", run)
	}
}

// ——— Session script cases (stage B §5.4) ———

// TestServiceRunCaseSessionAggregatesTurnsAndProjectsLast 覆盖会话 case 的聚合语义：
// Turns 逐轮证据全量保留、末轮输出投影为 Actual/TraceID、token/duration/cost 聚合
// 为逐轮之和、适配器收到租户与剧本透传。
func TestServiceRunCaseSessionAggregatesTurnsAndProjectsLast(t *testing.T) {
	adapter := &fakeSessionAdapter{fakeAdapter: &fakeAdapter{}}
	svc := NewService(adapter, &fakeRunRepo{}, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1", RequestedBy: "user-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "session-1", Session: &domain.EvalSessionScript{Goal: "解答用户问题",
				Turns: []domain.SessionTurn{{User: "开场问题"}, {User: "追问细节"}}},
				AssertionMode: domain.AssertionExact, ExpectedOutput: "追问细节", Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(run.Results) != 1 {
		t.Fatalf("expected 1 case result, got %d", len(run.Results))
	}
	got := run.Results[0]
	if !got.Passed {
		t.Fatalf("expected session run to pass, got result: %+v", got)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("expected 2 turn evidences, got %d", len(got.Turns))
	}
	if got.Turns[0].Index != 0 || got.Turns[1].Index != 1 {
		t.Fatalf("turn indexes not sequential: %+v", got.Turns)
	}
	// 末轮投影：Actual/TraceID 取末轮；逐轮 user 消息回显为 Output 透传至证据。
	if got.Actual != "追问细节" || got.TraceID != "trace-session" {
		t.Fatalf("last-turn projection wrong: actual=%v trace=%q", got.Actual, got.TraceID)
	}
	// 聚合：token/duration 为逐轮之和（cost 按"分"断言避免浮点尾差）。
	if got.Tokens != 30 || got.DurationMs != 60 {
		t.Fatalf("aggregated tokens/duration wrong: tokens=%d duration=%d", got.Tokens, got.DurationMs)
	}
	if cents := int(got.CostUSD*100 + 0.5); cents != 3 {
		t.Fatalf("aggregated cost wrong: %v", got.CostUSD)
	}
	if adapter.runTenant != "tenant-1" || len(adapter.lastScript.Turns) != 2 {
		t.Fatalf("adapter not driven with tenant/script: tenant=%q turns=%d",
			adapter.runTenant, len(adapter.lastScript.Turns))
	}
}

// TestServiceRunCaseSessionForSkillResource 覆盖 skill 资源的会话剧本 case 跑通：
// runCaseSession 按 IsSession() 分派（kind 无关），skill ResourceRef 与逐轮剧本透传
// 给 SessionRunner，末轮投影/聚合语义与 agent 资源会话一致。
func TestServiceRunCaseSessionForSkillResource(t *testing.T) {
	adapter := &fakeSessionAdapter{fakeAdapter: &fakeAdapter{}}
	svc := NewService(adapter, &fakeRunRepo{}, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1", RequestedBy: "user-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "revision-1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "session-skill-1", Session: &domain.EvalSessionScript{Goal: "skill 会话目标",
				Turns: []domain.SessionTurn{{User: "skill 开场"}, {User: "skill 追问"}}},
				AssertionMode: domain.AssertionExact, ExpectedOutput: "skill 追问", Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(run.Results) != 1 {
		t.Fatalf("expected 1 case result, got %d", len(run.Results))
	}
	got := run.Results[0]
	if !got.Passed {
		t.Fatalf("expected skill session run to pass, got result: %+v", got)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("expected 2 turn evidences, got %d", len(got.Turns))
	}
	// 末轮投影与资源 kind 无关：Actual 取末轮 Output。
	if got.Actual != "skill 追问" || got.TraceID != "trace-session" {
		t.Fatalf("last-turn projection wrong: actual=%v trace=%q", got.Actual, got.TraceID)
	}
	// 聚合：token/duration 为逐轮之和（cost 按"分"断言避免浮点尾差）。
	if got.Tokens != 30 || got.DurationMs != 60 {
		t.Fatalf("aggregated tokens/duration wrong: tokens=%d duration=%d", got.Tokens, got.DurationMs)
	}
	if adapter.runTenant != "tenant-1" || len(adapter.lastScript.Turns) != 2 {
		t.Fatalf("adapter not driven with skill tenant/script: tenant=%q turns=%d",
			adapter.runTenant, len(adapter.lastScript.Turns))
	}
}

// TestServiceRunCaseSessionFailsClosedWhenAdapterLacksSessionRunner 覆盖 fail-close：
// adapter 仅实现单轮 ResourceAdapter（不实现 SessionRunner）时，会话 case 报错并记
// 执行失败，绝不静默退化为单轮执行。
func TestServiceRunCaseSessionFailsClosedWhenAdapterLacksSessionRunner(t *testing.T) {
	svc := NewService(&fakeAdapter{}, &fakeRunRepo{}, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "session-1", Session: &domain.EvalSessionScript{Goal: "g",
				Turns: []domain.SessionTurn{{User: "开场"}}},
				AssertionMode: domain.AssertionExact, ExpectedOutput: "x", Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if got.Passed {
		t.Fatalf("session case must fail closed on non-SessionRunner adapter, got: %+v", got)
	}
	if got.FailureReason != "execution" || !strings.Contains(got.Error, "session evaluation not supported") {
		t.Fatalf("unexpected failure attribution: reason=%q err=%q", got.FailureReason, got.Error)
	}
}

// TestServiceRunCaseSessionKeepsPartialEvidenceWhenMidRunFails 覆盖逐轮执行中途失败：
// 已产出轮次的证据保留在 result.Turns（partial evidence），失败记为 execution 归因。
func TestServiceRunCaseSessionKeepsPartialEvidenceWhenMidRunFails(t *testing.T) {
	adapter := &fakeSessionAdapter{fakeAdapter: &fakeAdapter{}, sessionErr: errFakeSession}
	svc := NewService(adapter, &fakeRunRepo{}, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "session-1", Session: &domain.EvalSessionScript{Goal: "g",
				Turns: []domain.SessionTurn{{User: "第一轮"}, {User: "第二轮"}, {User: "第三轮"}}},
				AssertionMode: domain.AssertionExact, ExpectedOutput: "第三轮", Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if got.Passed {
		t.Fatalf("mid-run session failure must not pass, got: %+v", got)
	}
	if got.FailureReason != "execution" {
		t.Fatalf("unexpected failure reason: %q", got.FailureReason)
	}
	// 前两轮已产出 → 部分证据保留（第三轮失败点产出即返回）。
	if len(got.Turns) != 2 {
		t.Fatalf("expected partial evidence (2 turns) preserved, got %d", len(got.Turns))
	}
	// partial evidence 消耗同样聚合进 case/run 级成本（失败不吞真实消耗）：
	// fakeSessionAdapter 前两轮 tokens=10+20、cost=0.01+0.02、duration=20+40。
	if got.Tokens != 30 {
		t.Fatalf("partial tokens not aggregated: got %d want 30", got.Tokens)
	}
	if got.DurationMs != 60 {
		t.Fatalf("partial duration not aggregated: got %d want 60", got.DurationMs)
	}
	if cents := int(got.CostUSD*100 + 0.5); cents != 3 {
		t.Fatalf("partial cost not aggregated: got %v want 0.03", got.CostUSD)
	}
}

// TestServiceRunCaseSessionRejectsInvalidScriptBeforeSession 覆盖剧本结构 preflight：
// 非法剧本（零轮/空 user）在驱动适配器开受控会话前即被拒绝，RunSession 不被调用，
// 失败归因 execution（与其它「会话无法执行」路径一致）。
func TestServiceRunCaseSessionRejectsInvalidScriptBeforeSession(t *testing.T) {
	adapter := &fakeSessionAdapter{fakeAdapter: &fakeAdapter{}}
	svc := NewService(adapter, &fakeRunRepo{}, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "session-1", Session: &domain.EvalSessionScript{Goal: "g"},
				AssertionMode: domain.AssertionExact, ExpectedOutput: "x", Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if got.Passed {
		t.Fatalf("invalid script must not pass, got: %+v", got)
	}
	if got.FailureReason != "execution" || !strings.Contains(got.Error, "at least one turn required") {
		t.Fatalf("unexpected failure attribution: reason=%q err=%q", got.FailureReason, got.Error)
	}
	// preflight 在 RunSession 前拦截：适配器未被驱动（runTenant 未被赋值）。
	if adapter.runTenant != "" {
		t.Fatalf("RunSession must not be called for invalid script, runTenant=%q", adapter.runTenant)
	}
}

// TestServiceRunCaseSessionTrajectoryStalledFails 覆盖演化轨迹判据（阶段 B §4.2）：
// 规则会话连续两轮输出重复、末轮未达终态 → 判 stalled，Passed=false 且失败归因
// 优先容器级 "trajectory:stalled"（比单轮 assert:contains 更能解释整段没走对）。
func TestServiceRunCaseSessionTrajectoryStalledFails(t *testing.T) {
	adapter := &fakeSessionAdapter{fakeAdapter: &fakeAdapter{}}
	svc := NewService(adapter, &fakeRunRepo{}, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "session-stall", Session: &domain.EvalSessionScript{Goal: "产出目标答案",
				Turns: []domain.SessionTurn{{User: "同一个回答"}, {User: "同一个回答"}}},
				AssertionMode: domain.AssertionContains, ExpectedOutput: "目标答案", Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if got.Passed {
		t.Fatalf("stalled session must not pass, got: %+v", got)
	}
	if got.Trajectory == nil || got.Trajectory.Kind != domain.TrajectoryStalled {
		t.Fatalf("Trajectory = %+v, want stalled", got.Trajectory)
	}
	if got.FailureReason != "trajectory:stalled" {
		t.Fatalf("FailureReason = %q, want trajectory:stalled", got.FailureReason)
	}
}

// TestServiceRunJudgeSessionSendsTranscriptAndConverges 覆盖 judge 会话调用形态
// （阶段 B §4.3/§4.2）：LLM judge 收到逐轮 transcript（非空且含 Goal/轮次）；终态
// 通过后轨迹翻转为 converged（judge 模式纯函数只给 NA/Stalled，收敛由权威终态分支落）。
func TestServiceRunJudgeSessionSendsTranscriptAndConverges(t *testing.T) {
	adapter := &fakeSessionAdapter{fakeAdapter: &fakeAdapter{}}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true, result: domain.AssertionResult{Passed: true, Message: "末轮到达目标"}}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "session-judge", Session: &domain.EvalSessionScript{Goal: "完成报销核算",
				Turns: []domain.SessionTurn{{User: "报销规则"}, {User: "金额核算"}}},
				AssertionMode: domain.AssertionJudge, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if !got.Passed {
		t.Fatalf("judge-passed session must pass, got: %+v", got)
	}
	if judge.calls != 1 || judge.got.Transcript == "" {
		t.Fatalf("judge must receive transcript once, calls=%d transcript=%q", judge.calls, judge.got.Transcript)
	}
	if !strings.Contains(judge.got.Transcript, "Goal: 完成报销核算") ||
		!strings.Contains(judge.got.Transcript, "[Turn 0]") || !strings.Contains(judge.got.Transcript, "[Turn 1]") {
		t.Fatalf("transcript missing goal/turns: %q", judge.got.Transcript)
	}
	if got.Trajectory == nil || got.Trajectory.Kind != domain.TrajectoryConverged {
		t.Fatalf("Trajectory = %+v, want converged after LLM terminal pass", got.Trajectory)
	}
}

// TestServiceRunJudgeSessionTranscriptOmitsForSingleTurnCase 覆盖旧单轮 judge case 零
// 改动：无 Session → JudgeRequest.Transcript 保持空（与既有请求契约逐字节一致）。
func TestServiceRunJudgeSessionTranscriptOmitsForSingleTurnCase(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"judge-1": "答案"}}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true, result: domain.AssertionResult{Passed: true, Message: "ok"}}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "judge-1", Input: "问题", AssertionMode: domain.AssertionJudge, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !run.Results[0].Passed {
		t.Fatalf("single-turn judge case must pass, got: %+v", run.Results[0])
	}
	if judge.got.Transcript != "" {
		t.Fatalf("single-turn judge request must omit transcript, got %q", judge.got.Transcript)
	}
	if run.Results[0].Trajectory != nil {
		t.Fatalf("single-turn case must keep nil Trajectory (wire-omitted), got %+v", run.Results[0].Trajectory)
	}
}

type fakeAdapter struct {
	outputs  map[string]any
	errCase  string
	tenantID string
	tools    map[string][]domain.ToolObservation
}

func (f *fakeAdapter) ResolveRevision(_ context.Context, _ string, ref domain.ResourceRef) (domain.ResourceRevision, error) {
	return domain.ResourceRevision{ID: ref.RevisionID, ResourceKind: ref.Kind, ResourceID: ref.ResourceID}, nil
}

func (f *fakeAdapter) SafeSummary(context.Context, string, domain.ResourceRef) (map[string]any, error) {
	return map[string]any{}, nil
}

func (f *fakeAdapter) ExecuteRevision(
	_ context.Context, tenantID, _ string, _ domain.ResourceRef, c domain.EvalCase,
) (ExecutionResult, error) {
	f.tenantID = tenantID
	if c.ID == f.errCase {
		return ExecutionResult{}, errFakeExecution
	}
	return ExecutionResult{
		Output: f.outputs[c.ID], TraceID: "trace-" + c.ID, Tokens: 10, CostUSD: 0.01, DurationMs: 20, Tools: f.tools[c.ID],
	}, nil
}

// fakeSessionAdapter 是 fakeAdapter 的会话形态：嵌入单轮 fake 复用 ResourceAdapter 的
// ExecuteRevision/ResolveRevision/SafeSummary，追加 RunSession 把适配器升级为
// port.SessionRunner（runCaseSession 类型断言目标）。RunSession 逐轮回显 User 作为
// Output，token/duration 按轮递增；sessionErr 非空时产出前两轮后返回错误，模拟逐轮
// 执行中途失败（partial evidence 场景）。
type fakeSessionAdapter struct {
	*fakeAdapter
	sessionErr error
	runTenant  string
	lastScript domain.EvalSessionScript
}

func (f *fakeSessionAdapter) RunSession(
	_ context.Context, tenantID, _ string, _ domain.ResourceRef, script domain.EvalSessionScript,
) ([]domain.SessionTurnEvidence, error) {
	f.runTenant = tenantID
	f.lastScript = script
	turns := make([]domain.SessionTurnEvidence, 0, len(script.Turns))
	for i, turn := range script.Turns {
		if f.sessionErr != nil && i >= 2 {
			return turns, f.sessionErr
		}
		turns = append(turns, domain.SessionTurnEvidence{
			Index: i, User: turn.User, Output: turn.User, TraceID: "trace-session",
			Tokens: 10 * (i + 1), CostUSD: 0.01 * float64(i+1), DurationMs: 20 * (i + 1),
		})
	}
	return turns, nil
}

const errFakeSession = fakeError("session execution failed at turn 2")

type fakeRunRepo struct {
	saved    domain.EvalRun
	tenantID string
}

func (f *fakeRunRepo) SaveRun(_ context.Context, tenantID string, run domain.EvalRun) error {
	f.tenantID = tenantID
	f.saved = run
	return nil
}

func (f *fakeRunRepo) GetRun(_ context.Context, _ string, runID string) (domain.EvalRun, bool, error) {
	return f.saved, f.saved.ID == runID, nil
}

func (f *fakeRunRepo) FindLatestCompletedRunForResource(
	_ context.Context, _ string, _ domain.ResourceRef, _ string,
) (*domain.EvalRun, error) {
	return nil, nil
}

func (f *fakeRunRepo) FindLatestCompletedRunForPlatformSeq(
	_ context.Context, _, _ string, _ int64,
) (*domain.EvalRun, error) {
	return nil, nil
}

type fakeError string

func (e fakeError) Error() string { return string(e) }

const errFakeExecution = fakeError("execution failed")

// ——— Trace evidence tests ———

func TestServiceRunCaseResolvesTraceEvidence(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "ok"}}
	repo := &fakeRunRepo{}
	traceReader := &fakeTraceEvidenceReader{
		traces: map[string]port.ObservedTrace{
			"trace-case-1": {
				TraceID: "trace-case-1", CostUSD: 0.05, LatencyMs: 350,
				Success: true, SecurityViolation: false,
			},
		},
	}
	svc := NewService(adapter, repo, traceReader, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "x", ExpectedOutput: "ok", AssertionMode: domain.AssertionExact, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	ev := run.Results[0].TraceEvidence
	if ev == nil {
		t.Fatal("expected trace evidence, got nil")
	}
	if ev.CostUSD != 0.05 || ev.LatencyMs != 350 || !ev.Success {
		t.Fatalf("unexpected trace evidence: %+v", ev)
	}
}

func TestServiceRunCaseGracefullyHandlesTraceReaderError(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "ok"}}
	repo := &fakeRunRepo{}
	traceReader := &fakeFailingTraceEvidenceReader{}
	svc := NewService(adapter, repo, traceReader, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "x", ExpectedOutput: "ok", AssertionMode: domain.AssertionExact, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// Opik unavailable must not block evaluation.
	if !run.Passed {
		t.Fatal("expected run to pass despite trace evidence error")
	}
	if run.Results[0].TraceEvidence != nil {
		t.Fatal("expected nil trace evidence on resolve error")
	}
}

func TestServiceRunCaseSkipsTraceEvidenceWhenReaderNil(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "ok"}}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil) // no trace reader configured

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "x", ExpectedOutput: "ok", AssertionMode: domain.AssertionExact, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Results[0].TraceEvidence != nil {
		t.Fatal("expected nil trace evidence when reader is not configured")
	}
}

type fakeFailingTraceEvidenceReader struct{}

func (f *fakeFailingTraceEvidenceReader) Resolve(_ context.Context, _, _ string) (port.ObservedTrace, error) {
	return port.ObservedTrace{}, errors.New("opik unavailable")
}

func (f *fakeFailingTraceEvidenceReader) ResolveBatch(_ context.Context, _ string, _ []string) (map[string]port.ObservedTrace, error) {
	return nil, errors.New("opik unavailable")
}

type fakeLLMJudge struct {
	enabled bool
	result  domain.AssertionResult
	// stepResult 在 req.ToolSequence 非空时覆盖 result（step_judge 专用）；
	// nil 时所有调用返回 result，保持既有 fake 行为。
	stepResult *domain.AssertionResult
	err        error
	got        port.JudgeRequest
	calls      int
}

func (f *fakeLLMJudge) Enabled(_ context.Context) bool { return f.enabled }
func (f *fakeLLMJudge) Judge(_ context.Context, req port.JudgeRequest) (domain.AssertionResult, error) {
	f.calls++
	f.got = req
	if f.err != nil {
		return domain.AssertionResult{}, f.err
	}
	if f.stepResult != nil && req.ToolSequence != "" {
		return *f.stepResult, nil
	}
	return f.result, nil
}

func TestServiceJudgeAssertionDispatchesToJudge(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"judge-1": "退款已到账"}}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true, result: domain.AssertionResult{Passed: true, Message: "符合要求"}}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{
			ID: "suite-version-1",
			Cases: []domain.EvalCase{
				{ID: "judge-1", Input: "退款到账了吗", AssertionMode: domain.AssertionJudge, Enabled: true,
					JudgeSpec: &domain.JudgeSpec{Model: "qwen-max", Rubric: "custom rubric"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !run.Passed || !run.Results[0].Passed {
		t.Fatalf("expected judge case to pass, got %+v", run.Results[0])
	}
	if judge.calls != 1 {
		t.Fatalf("expected 1 judge call, got %d", judge.calls)
	}
	if judge.got.Model != "qwen-max" || judge.got.Rubric != "custom rubric" {
		t.Fatalf("judge request missing spec: %+v", judge.got)
	}
	if judge.got.Input != `"退款到账了吗"` || judge.got.Actual != `"退款已到账"` {
		t.Fatalf("judge request material mismatch: input=%s actual=%s", judge.got.Input, judge.got.Actual)
	}
	if judge.got.ExpectedOutput != "null" {
		t.Fatalf("expected null expected output for judge-only case, got %s", judge.got.ExpectedOutput)
	}
}

func TestServiceJudgeAssertionFailClosedWhenDisabled(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"judge-1": "any"}}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil) // no judge configured

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{
			ID: "suite-version-1",
			Cases: []domain.EvalCase{
				{ID: "judge-1", Input: "x", AssertionMode: domain.AssertionJudge, Enabled: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Passed || run.Results[0].Error != "LLM judge disabled" {
		t.Fatalf("expected fail-closed error, got %+v", run.Results[0])
	}
	if run.Results[0].FailureReason != "execution" {
		t.Fatalf("judge infra failure must carry execution failure_reason, got %+v", run.Results[0])
	}
}

func TestServiceJudgeAssertionDisabledJudgeFailsClosed(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"judge-1": "any"}}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, &fakeLLMJudge{enabled: false})

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{
			ID: "suite-version-1",
			Cases: []domain.EvalCase{
				{ID: "judge-1", Input: "x", AssertionMode: domain.AssertionJudge, Enabled: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Passed || run.Results[0].Error != "LLM judge disabled" {
		t.Fatalf("expected fail-closed error, got %+v", run.Results[0])
	}
	if run.Results[0].FailureReason != "execution" {
		t.Fatalf("judge infra failure must carry execution failure_reason, got %+v", run.Results[0])
	}
}

func TestServiceJudgeAssertionPropagatesJudgeError(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"judge-1": "any"}}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true, err: errors.New("completer timeout")}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{
			ID: "suite-version-1",
			Cases: []domain.EvalCase{
				{ID: "judge-1", Input: "x", AssertionMode: domain.AssertionJudge, Enabled: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Passed || !strings.Contains(run.Results[0].Error, "completer timeout") {
		t.Fatalf("expected judge error to propagate, got %+v", run.Results[0])
	}
	if run.Results[0].FailureReason != "execution" {
		t.Fatalf("judge call failure must carry execution failure_reason, got %+v", run.Results[0])
	}
}

func TestServiceJudgeCaseMarshalErrorsSetExecutionFailureReason(t *testing.T) {
	tests := []struct {
		name       string
		input      any
		expected   any
		output     any
		wantSubstr string
	}{
		{
			name:       "input marshal error",
			input:      make(chan int),
			expected:   "ok",
			output:     "any",
			wantSubstr: "marshal input",
		},
		{
			name:       "expected marshal error",
			input:      "x",
			expected:   make(chan int),
			output:     "any",
			wantSubstr: "marshal expected output",
		},
		{
			name:       "actual marshal error",
			input:      "x",
			expected:   "ok",
			output:     make(chan int),
			wantSubstr: "marshal actual output",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &fakeAdapter{outputs: map[string]any{"judge-1": tc.output}}
			repo := &fakeRunRepo{}
			svc := NewService(adapter, repo, nil, &fakeLLMJudge{enabled: true})

			run, err := svc.Run(snapshotCtx(), RunInput{
				TenantID: "tenant-1",
				Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
				Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
					{ID: "judge-1", Input: tc.input, ExpectedOutput: tc.expected, AssertionMode: domain.AssertionJudge, Enabled: true},
				}},
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			got := run.Results[0]
			if !strings.Contains(got.Error, tc.wantSubstr) {
				t.Fatalf("error = %q, want substring %q", got.Error, tc.wantSubstr)
			}
			if got.FailureReason != "execution" {
				t.Fatalf("failure_reason = %q, want execution", got.FailureReason)
			}
			if got.Passed {
				t.Fatal("judge marshal failure must fail the case")
			}
		})
	}
}

func TestServiceRuleAssertionDoesNotTouchJudge(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "发货了"}}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true}
	svc := NewService(adapter, repo, nil, judge)

	_, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "version-2"},
		Suite: domain.EvalSuiteRevision{
			ID: "suite-version-1",
			Cases: []domain.EvalCase{
				{ID: "case-1", Input: "物流", ExpectedOutput: "发货", AssertionMode: domain.AssertionContains, Enabled: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if judge.calls != 0 {
		t.Fatalf("rule assertion must not call the judge, got %d calls", judge.calls)
	}
}

// escalateFailureMetrics 记录 IncEvalReviewEscalateFailure 调用次数（嵌入
// NoopMetrics 满足 MetricsProvider 全接口，只覆盖目标方法）。
type escalateFailureMetrics struct {
	observability.NoopMetrics
	inc int
}

func (m *escalateFailureMetrics) IncEvalReviewEscalateFailure() { m.inc++ }

// failingCaseEscalator 让 TryEscalateCaseResult 固定失败，验证 Service 侧 fail-open：
// 升级失败仅日志 + IncEvalReviewEscalateFailure，不阻断评测主流程。
type failingCaseEscalator struct{}

func (failingCaseEscalator) TryEscalateObservation(context.Context, string, *domain.EvalObservation) error {
	return nil
}

func (failingCaseEscalator) TryEscalateCaseResult(
	context.Context, string, string, domain.ResourceRef, domain.EvalCaseResult, domain.EvalCase, domain.AssertionResult, bool, bool,
) error {
	return errors.New("escalate down")
}

func TestServiceEscalateCaseResultFailureCountsMetric(t *testing.T) {
	svc := NewService(&fakeAdapter{}, &fakeRunRepo{}, nil, nil)
	svc.SetReviewEscalator(failingCaseEscalator{}, domain.ReviewConfig{})
	metrics := &escalateFailureMetrics{}
	svc.SetObservability(nil, metrics)

	svc.escalateCaseResult(context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"},
		domain.EvalCaseResult{ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: true},
		domain.EvalCase{ID: "c1", NeedsReview: true},
		domain.AssertionResult{Passed: true, Confidence: 0.9}, true, true)

	if metrics.inc != 1 {
		t.Fatalf("IncEvalReviewEscalateFailure calls = %d, want 1", metrics.inc)
	}
}

func TestServiceEscalateCaseResultNilReviewDoesNotPanic(t *testing.T) {
	svc := NewService(&fakeAdapter{}, &fakeRunRepo{}, nil, nil)
	metrics := &escalateFailureMetrics{}
	svc.SetObservability(nil, metrics)

	// review 未注入（nil）：升级静默跳过，不得 panic，不得计指标（防回归）。
	svc.escalateCaseResult(context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"},
		domain.EvalCaseResult{ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: true},
		domain.EvalCase{ID: "c1", NeedsReview: true},
		domain.AssertionResult{Passed: true, Confidence: 0.9}, true, true)

	if metrics.inc != 0 {
		t.Fatalf("IncEvalReviewEscalateFailure calls = %d, want 0", metrics.inc)
	}
}

// recordingCaseEscalator 记录 TryEscalateCaseResult 收到的 outputPass/processPass，
// 验证 runCase 两分支（judge / 规则）都按新签名把过程断言结果传入评审池（§6.5）。
type recordingCaseEscalator struct {
	calls       int
	outputPass  bool
	processPass bool
}

func (r *recordingCaseEscalator) TryEscalateObservation(context.Context, string, *domain.EvalObservation) error {
	return nil
}

func (r *recordingCaseEscalator) TryEscalateCaseResult(
	_ context.Context, _, _ string, _ domain.ResourceRef, _ domain.EvalCaseResult, _ domain.EvalCase, _ domain.AssertionResult,
	outputPass, processPass bool,
) error {
	r.calls++
	r.outputPass = outputPass
	r.processPass = processPass
	return nil
}

// TestRunCaseRuleBranchEscalatesProcessConflict 覆盖 runCase 规则分支的新升级调用：
// 规则断言 case 输出 pass + 过程 fail（must_not_call 命中）时，以 outputPass=true /
// processPass=false 调用评审池（§6.5 process_output_conflict 数据源），且失败不阻断主流程。
func TestRunCaseRuleBranchEscalatesProcessConflict(t *testing.T) {
	adapter := &fakeAdapter{
		outputs: map[string]any{"case-1": "已删除相关文件"},
		tools: map[string][]domain.ToolObservation{
			"case-1": {{ToolName: "search", StepIndex: 1}, {ToolName: "delete", StepIndex: 2}},
		},
	}
	repo := &fakeRunRepo{}
	esc := &recordingCaseEscalator{}
	svc := NewService(adapter, repo, nil, nil)
	svc.SetReviewEscalator(esc, domain.ReviewConfig{LowConfidenceThreshold: 0.6})

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "删除文件", ExpectedOutput: "删除", AssertionMode: domain.AssertionContains, Enabled: true,
				ToolSpec: &domain.ToolSpec{MustNotCall: []string{"delete"}}},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if esc.calls != 1 {
		t.Fatalf("escalator calls = %d, want 1 (rule branch must escalate)", esc.calls)
	}
	if !esc.outputPass || esc.processPass {
		t.Fatalf("escalator got output_pass=%v process_pass=%v, want true/false", esc.outputPass, esc.processPass)
	}
	if run.Results[0].ProcessPass {
		t.Fatal("process assertion must fail")
	}
}

func TestRunJudgeCasePopulatesDimensionsAndFailureReason(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"judge-1": "回答不准确"}}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true, result: domain.AssertionResult{
		Passed: false, Message: "faithfulness 不足", Confidence: 0.6,
		Dimensions: []domain.DimensionScore{
			{Name: "faithfulness", Score: 0.3, Passed: false, Confidence: 0.7},
			{Name: "relevance", Score: 0.9, Passed: true, Confidence: 0.8},
		},
	}}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "judge-1", Input: "问题", AssertionMode: domain.AssertionJudge, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Passed {
		t.Fatal("judge failed verdict must fail the run")
	}
	got := run.Results[0]
	if len(got.Dimensions) != 2 || got.Dimensions[0].Name != "faithfulness" {
		t.Fatalf("dimensions = %+v", got.Dimensions)
	}
	if got.FailureReason != "dimension:faithfulness" {
		t.Fatalf("failure_reason = %q, want dimension:faithfulness", got.FailureReason)
	}
}

func TestRunRuleAssertionSetsAssertFailureReason(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "你好"}}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil) // 规则断言不走 judge

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "问", ExpectedOutput: "找不到的关键词", AssertionMode: domain.AssertionContains, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if got.Passed {
		t.Fatal("contains mismatch must fail")
	}
	if got.FailureReason != "assert:contains" {
		t.Fatalf("failure_reason = %q, want assert:contains", got.FailureReason)
	}
	if len(got.Dimensions) != 0 {
		t.Fatalf("rule assertions must not carry dimensions: %+v", got.Dimensions)
	}
}

func TestRunExecutionErrorSetsExecutionFailureReason(t *testing.T) {
	adapter := &fakeAdapter{outputs: map[string]any{"case-1": "ok"}, errCase: "case-1"}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "问", ExpectedOutput: "答", AssertionMode: domain.AssertionContains, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if got.Error == "" {
		t.Fatal("execution error must surface")
	}
	if got.FailureReason != "execution" {
		t.Fatalf("failure_reason = %q, want execution", got.FailureReason)
	}
}

// ——— Process assertion flow (§6.5) ———

func TestRunCaseToolSpecMustNotCallFailsProcessKeepsOutputAttribution(t *testing.T) {
	adapter := &fakeAdapter{
		outputs: map[string]any{"case-1": "已删除相关文件"},
		tools: map[string][]domain.ToolObservation{
			"case-1": {{ToolName: "search", StepIndex: 1}, {ToolName: "delete", StepIndex: 2}},
		},
	}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "删除文件", ExpectedOutput: "删除", AssertionMode: domain.AssertionContains, Enabled: true,
				ToolSpec: &domain.ToolSpec{MustNotCall: []string{"delete"}}},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if run.Passed || got.Passed {
		t.Fatal("must_not_call hit must fail the case")
	}
	if got.ProcessPass {
		t.Fatal("process assertion must fail when a forbidden tool is called")
	}
	if got.ProcessFailure != "process:must_not_call:delete" {
		t.Fatalf("process_failure = %q, want process:must_not_call:delete", got.ProcessFailure)
	}
	if got.FailureReason != "" {
		t.Fatalf("output passed, failure_reason must stay empty (process attribution separate), got %q", got.FailureReason)
	}
	if len(got.Tools) != 2 || got.Tools[1].ToolName != "delete" {
		t.Fatalf("tools = %+v", got.Tools)
	}
}

func TestRunCaseStepJudgePassMergesDimensionsAndPasses(t *testing.T) {
	adapter := &fakeAdapter{
		outputs: map[string]any{"case-1": "已创建工单"},
		tools:   map[string][]domain.ToolObservation{"case-1": {{ToolName: "create_ticket", StepIndex: 0}}},
	}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true, result: domain.AssertionResult{
		Passed: true, Message: "步骤合理", Confidence: 0.9,
		Dimensions: []domain.DimensionScore{{Name: "reasoning", Score: 0.8, Passed: true}},
	}}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "帮我创建工单", ExpectedOutput: "创建", AssertionMode: domain.AssertionContains, Enabled: true,
				StepJudge: &domain.StepJudge{Criteria: "步骤需合理"}},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if !run.Passed || !got.Passed || !got.ProcessPass {
		t.Fatalf("expected pass, got %+v", got)
	}
	if got.ProcessFailure != "" {
		t.Fatalf("process_failure = %q, want empty", got.ProcessFailure)
	}
	if len(got.Dimensions) != 1 || got.Dimensions[0].Name != "reasoning" {
		t.Fatalf("dimensions = %+v", got.Dimensions)
	}
	if judge.calls != 1 {
		t.Fatalf("expected 1 judge call (step_judge only), got %d", judge.calls)
	}
	if judge.got.Rubric != "步骤需合理" {
		t.Fatalf("rubric = %q, want step criteria", judge.got.Rubric)
	}
	if judge.got.ToolSequence != "[0] create_ticket" {
		t.Fatalf("tool_sequence = %q, want [0] create_ticket", judge.got.ToolSequence)
	}
	if judge.got.Model != "" {
		t.Fatalf("step_judge must use platform default model, got %q", judge.got.Model)
	}
}

func TestRunCaseStepJudgeDisabledFailsClosed(t *testing.T) {
	adapter := &fakeAdapter{
		outputs: map[string]any{"case-1": "any"},
		tools:   map[string][]domain.ToolObservation{"case-1": {{ToolName: "read", StepIndex: 0}}},
	}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil) // no judge configured

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "x", ExpectedOutput: "y", AssertionMode: domain.AssertionContains, Enabled: true,
				StepJudge: &domain.StepJudge{}},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if run.Passed || got.Passed {
		t.Fatal("disabled step_judge must fail the case")
	}
	if got.Error != "LLM judge disabled" {
		t.Fatalf("error = %q, want LLM judge disabled", got.Error)
	}
	if got.FailureReason != "execution" {
		t.Fatalf("failure_reason = %q, want execution", got.FailureReason)
	}
	if got.ProcessPass {
		t.Fatal("disabled step_judge must not pass the process assertion")
	}
}

// TestRunCaseStepJudgeJudgeErrorFailsClosed 覆盖 step_judge 的 Judge port 返回 error
// 时 fail-closed（项目红线）：evaluateProcess 向上返回 error → runCase 置
// FailureReason="execution"、result.Error 非空、绝不静默 pass。现有覆盖只有
// judgeProcess 的 disabled 分支，缺 Judge port error 分支。
func TestRunCaseStepJudgeJudgeErrorFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "judge port error", err: errors.New("completer timeout")},
		{name: "provider error", err: errors.New("LLM provider rate limited")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &fakeAdapter{
				outputs: map[string]any{"case-1": "any"},
				tools:   map[string][]domain.ToolObservation{"case-1": {{ToolName: "read", StepIndex: 0}}},
			}
			repo := &fakeRunRepo{}
			judge := &fakeLLMJudge{enabled: true, err: tc.err}
			svc := NewService(adapter, repo, nil, judge)

			run, err := svc.Run(snapshotCtx(), RunInput{
				TenantID: "tenant-1",
				Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
				Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
					{ID: "case-1", Input: "x", ExpectedOutput: "y", AssertionMode: domain.AssertionContains, Enabled: true,
						StepJudge: &domain.StepJudge{Criteria: "步骤需合理"}},
				}},
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			got := run.Results[0]
			if run.Passed || got.Passed {
				t.Fatalf("judge port error must fail the case, got %+v", got)
			}
			if !strings.Contains(got.Error, tc.err.Error()) {
				t.Fatalf("error = %q, want substring %q", got.Error, tc.err.Error())
			}
			if got.FailureReason != "execution" {
				t.Fatalf("failure_reason = %q, want execution", got.FailureReason)
			}
			if got.ProcessPass {
				t.Fatal("judge port error must not pass the process assertion")
			}
		})
	}
}

func TestRunCaseNoProcessAssertionDefaultsProcessPassTrue(t *testing.T) {
	adapter := &fakeAdapter{
		outputs: map[string]any{"case-1": "ok"},
		tools:   map[string][]domain.ToolObservation{"case-1": {{ToolName: "search", StepIndex: 0}}},
	}
	repo := &fakeRunRepo{}
	svc := NewService(adapter, repo, nil, nil)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "q", ExpectedOutput: "ok", AssertionMode: domain.AssertionContains, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if !got.Passed || !got.ProcessPass {
		t.Fatalf("expected pass with process_pass default true, got %+v", got)
	}
	if got.ProcessFailure != "" {
		t.Fatalf("process_failure = %q, want empty", got.ProcessFailure)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("tools must be captured even without process assertions, got %+v", got.Tools)
	}
}

func TestRunCaseJudgeBranchFoldsProcessPass(t *testing.T) {
	adapter := &fakeAdapter{
		outputs: map[string]any{"case-1": "已删除"},
		tools:   map[string][]domain.ToolObservation{"case-1": {{ToolName: "delete", StepIndex: 0}}},
	}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{enabled: true, result: domain.AssertionResult{Passed: true, Message: "输出符合要求"}}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "删除文件", AssertionMode: domain.AssertionJudge, Enabled: true,
				ToolSpec: &domain.ToolSpec{MustNotCall: []string{"delete"}}},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if run.Passed || got.Passed {
		t.Fatal("process failure must fail the judge case despite judge passing")
	}
	if got.ProcessPass {
		t.Fatal("process assertion must fail")
	}
	if got.ProcessFailure != "process:must_not_call:delete" {
		t.Fatalf("process_failure = %q", got.ProcessFailure)
	}
	if got.FailureReason != "" {
		t.Fatalf("judge passed, so failure_reason must stay empty, got %q", got.FailureReason)
	}
	if judge.calls != 1 {
		t.Fatalf("expected 1 judge call (no step_judge), got %d", judge.calls)
	}
}

func TestRunCaseJudgeAndStepJudgeMergeDimensions(t *testing.T) {
	adapter := &fakeAdapter{
		outputs: map[string]any{"case-1": "已创建工单"},
		tools:   map[string][]domain.ToolObservation{"case-1": {{ToolName: "create_ticket", StepIndex: 0}}},
	}
	repo := &fakeRunRepo{}
	judge := &fakeLLMJudge{
		enabled: true,
		result: domain.AssertionResult{
			Passed: true, Dimensions: []domain.DimensionScore{{Name: "faithfulness", Score: 0.9, Passed: true}},
		},
		stepResult: &domain.AssertionResult{
			Passed: true, Dimensions: []domain.DimensionScore{{Name: "reasoning", Score: 0.8, Passed: true}},
		},
	}
	svc := NewService(adapter, repo, nil, judge)

	run, err := svc.Run(snapshotCtx(), RunInput{
		TenantID: "tenant-1",
		Resource: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "v1"},
		Suite: domain.EvalSuiteRevision{ID: "sv-1", Cases: []domain.EvalCase{
			{ID: "case-1", Input: "创建工单", AssertionMode: domain.AssertionJudge, Enabled: true,
				StepJudge: &domain.StepJudge{Criteria: "步骤需合理"}},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got := run.Results[0]
	if !run.Passed || !got.Passed || !got.ProcessPass {
		t.Fatalf("expected pass, got %+v", got)
	}
	if len(got.Dimensions) != 2 {
		t.Fatalf("expected 2 merged dimensions, got %+v", got.Dimensions)
	}
	if got.Dimensions[0].Name != "reasoning" || got.Dimensions[1].Name != "faithfulness" {
		t.Fatalf("dimension order = %+v, want [reasoning faithfulness]", got.Dimensions)
	}
	if judge.calls != 2 {
		t.Fatalf("expected 2 judge calls (step_judge + output judge), got %d", judge.calls)
	}
}
