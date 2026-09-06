package application

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

// gateMetricsSpy 记录 IncEvalGateAction(layer, action) 调用（嵌入 NoopMetrics 满足
// MetricsProvider 其余方法；PublishGateCoordinator 只需 gate 动作计数）。
type gateMetricsSpy struct {
	observability.NoopMetrics
	mu      sync.Mutex
	actions []string // "layer:action" 保序
}

func (s *gateMetricsSpy) IncEvalGateAction(layer, action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, layer+":"+action)
}

func (s *gateMetricsSpy) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.actions...)
}

// stateRecorder 记录 UpdateEvalState 调用（哨兵判定写回 eval_state 断言）。
type stateRecorder struct {
	calls []evalStateCall
}

type evalStateCall struct {
	groupKey string
	seq      int64
	state    string
	actor    string
}

func (r *stateRecorder) record(_ context.Context, groupKey string, seq int64, state, actor string) error {
	r.calls = append(r.calls, evalStateCall{groupKey: groupKey, seq: seq, state: state, actor: actor})
	return nil
}

type enqueueRecorder struct {
	calls int
}

func (e *enqueueRecorder) enqueue(_ context.Context, _ string, _ EnqueueRunInput) (string, error) {
	e.calls++
	return "run-sentinel", nil
}

// runAnchored 构造 ContextSnapshot.Execution 含 groupKey 组 seq 的 run（DecideSentinel
// 只消费 ContextSnapshot；哨兵/基线对比用相同 groupKey，memory 组与全平台组同路径）。
func runAnchored(groupKey string, seq int64) *domain.EvalRun {
	return &domain.EvalRun{
		ID: fmt.Sprintf("run-%s-%d", groupKey, seq),
		ContextSnapshot: &domain.EvaluationContextSnapshot{
			Execution: []domain.GroupSnapshot{
				{GroupKey: domain.GroupAgent, VersionSeq: 3},
				{GroupKey: groupKey, VersionSeq: seq},
			},
		},
	}
}

// testCoordinator 建默认 deps 的协调器：GateEnabled=false、Metrics spy、UpdateEvalState
// 记录器；mutate 覆盖各用例 stub。
func testCoordinator(mutate func(*PublishGateDeps)) (*PublishGateCoordinator, *gateMetricsSpy, *stateRecorder, *enqueueRecorder) {
	spy := &gateMetricsSpy{}
	recorder := &stateRecorder{}
	enqueuer := &enqueueRecorder{}
	deps := PublishGateDeps{
		Metrics:         spy,
		GateEnabled:     func(context.Context) bool { return false },
		UpdateEvalState: recorder.record,
		EnqueueSentinel: enqueuer.enqueue,
	}
	if mutate != nil {
		mutate(&deps)
	}
	return NewPublishGateCoordinator(deps), spy, recorder, enqueuer
}

func compareStub(regressed bool, deltas map[string]float64) func(*domain.EvalRun, *domain.EvalRun) (domain.RunComparison, error) {
	return func(_, _ *domain.EvalRun) (domain.RunComparison, error) {
		return domain.RunComparison{Regressed: regressed, DimensionDeltas: deltas}, nil
	}
}

func wantActions(t *testing.T, spy *gateMetricsSpy, want ...string) {
	t.Helper()
	if got := spy.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("gate actions = %v, want %v", got, want)
	}
}

func wantStateCalls(t *testing.T, recorder *stateRecorder, want ...evalStateCall) {
	t.Helper()
	if !reflect.DeepEqual(recorder.calls, want) {
		t.Fatalf("updateEvalState calls = %+v, want %+v", recorder.calls, want)
	}
}

func TestPublishGateCoordinator_GateDisabled_PassThrough(t *testing.T) {
	coordinator, spy, recorder, enqueuer := testCoordinator(nil)
	// ResolveVersion 留 nil：GateEnabled=false 必须短路，绝不触碰版本解析/入队。
	result, err := coordinator.GatePublish(context.Background(), "host", PublishGateRequest{
		GroupKey: "evaluation", VersionID: 9, Actor: "tester",
	})
	if err != nil {
		t.Fatalf("GatePublish err = %v, want nil", err)
	}
	if result.Decision != DecisionPassThrough {
		t.Fatalf("Decision = %v, want PassThrough", result.Decision)
	}
	wantActions(t, spy)
	wantStateCalls(t, recorder)
	if enqueuer.calls != 0 {
		t.Fatalf("EnqueueSentinel called %d times, want 0", enqueuer.calls)
	}
}

func TestPublishGateCoordinator_MemoryGroup_UniformRefusedNotWired(t *testing.T) {
	// R30：memory 组不再特判——enabled=true & SentinelSpec nil → RefusedNotWired，与全组一致。
	coordinator, spy, recorder, enqueuer := testCoordinator(func(deps *PublishGateDeps) {
		deps.GateEnabled = func(context.Context) bool { return true }
		deps.ResolveVersion = func(_ context.Context, _ string, _ int64) (int, string, bool, bool, error) {
			return 2, "draft", false, true, nil
		}
	})
	result, err := coordinator.GatePublish(context.Background(), "host", PublishGateRequest{
		GroupKey: domain.GroupMemory, VersionID: 11, Actor: "tester",
	})
	if err != nil {
		t.Fatalf("GatePublish err = %v, want nil", err)
	}
	if result.Decision != DecisionRefusedNotWired {
		t.Fatalf("Decision = %v, want RefusedNotWired", result.Decision)
	}
	if enqueuer.calls != 0 {
		t.Fatalf("EnqueueSentinel called %d times, want 0", enqueuer.calls)
	}
	wantActions(t, spy)
	wantStateCalls(t, recorder)
}

func TestPublishGateCoordinator_SentinelSpecNil_RefusedNotWired(t *testing.T) {
	// enabled=true & SentinelSpec nil → 拒发（不直发、不 Enqueue）——非 memory 组通用路径。
	coordinator, spy, recorder, enqueuer := testCoordinator(func(deps *PublishGateDeps) {
		deps.GateEnabled = func(context.Context) bool { return true }
		deps.ResolveVersion = func(_ context.Context, _ string, _ int64) (int, string, bool, bool, error) {
			return 2, "draft", false, true, nil
		}
	})
	result, err := coordinator.GatePublish(context.Background(), "host", PublishGateRequest{
		GroupKey: "agent", VersionID: 12, Actor: "tester",
	})
	if err != nil {
		t.Fatalf("GatePublish err = %v, want nil", err)
	}
	if result.Decision != DecisionRefusedNotWired {
		t.Fatalf("Decision = %v, want RefusedNotWired", result.Decision)
	}
	if enqueuer.calls != 0 {
		t.Fatalf("EnqueueSentinel called %d times, want 0", enqueuer.calls)
	}
	wantActions(t, spy)
	wantStateCalls(t, recorder)
}

func TestPublishGateCoordinator_SentinelBlock_RefusesAndWritesFailed(t *testing.T) {
	// Compare.Regressed=true → 断言 eval_state=sentinel_failed + l3_sentinel block + l2 regression。
	coordinator, spy, recorder, _ := testCoordinator(func(deps *PublishGateDeps) {
		deps.Compare = compareStub(true, map[string]float64{"faithfulness": -0.1})
	})
	baseline := runAnchored(domain.GroupMemory, 5)
	sentinel := runAnchored(domain.GroupMemory, 7)
	decision, err := coordinator.DecideSentinel(context.Background(), "host", domain.GroupMemory, baseline, sentinel)
	if err != nil {
		t.Fatalf("DecideSentinel err = %v, want nil", err)
	}
	if decision.Verdict != SentinelVerdictBlock {
		t.Fatalf("Verdict = %v, want block", decision.Verdict)
	}
	if decision.BaselineSeq != 5 || decision.ConfirmedSeq != 7 {
		t.Fatalf("seqs = baseline %d / confirmed %d, want 5 / 7", decision.BaselineSeq, decision.ConfirmedSeq)
	}
	if got := decision.Deltas["faithfulness"]; got != -0.1 {
		t.Fatalf("Deltas[faithfulness] = %v, want -0.1", got)
	}
	wantActions(t, spy, domain.LayerL3Sentinel+":block", domain.LayerL2+":regression")
	wantStateCalls(t, recorder, evalStateCall{
		groupKey: domain.GroupMemory, seq: 7, state: domain.EvalStateSentinelFailed, actor: "host",
	})
}

func TestPublishGateCoordinator_SentinelPass_WritesPassed(t *testing.T) {
	// Regressed=false → sentinel_passed + l3_sentinel pass + l3_platform publish_gated。
	coordinator, spy, recorder, _ := testCoordinator(func(deps *PublishGateDeps) {
		deps.Compare = compareStub(false, map[string]float64{"faithfulness": 0.02})
	})
	baseline := runAnchored(domain.GroupMemory, 5)
	sentinel := runAnchored(domain.GroupMemory, 7)
	decision, err := coordinator.DecideSentinel(context.Background(), "host", domain.GroupMemory, baseline, sentinel)
	if err != nil {
		t.Fatalf("DecideSentinel err = %v, want nil", err)
	}
	if decision.Verdict != SentinelVerdictPass {
		t.Fatalf("Verdict = %v, want pass", decision.Verdict)
	}
	if decision.BaselineSeq != 5 || decision.ConfirmedSeq != 7 {
		t.Fatalf("seqs = baseline %d / confirmed %d, want 5 / 7", decision.BaselineSeq, decision.ConfirmedSeq)
	}
	wantActions(t, spy, domain.LayerL3Sentinel+":pass", constants.GateLayerL3Platform+":"+domain.ActionPublishGated)
	wantStateCalls(t, recorder, evalStateCall{
		groupKey: domain.GroupMemory, seq: 7, state: constants.PlatformEvalStateSentinelPassed, actor: "host",
	})
}

func TestPublishGateCoordinator_SentinelRunFailed_Blocks(t *testing.T) {
	// sentinel 非 completed（nil）→ block，不 Compare、不写 eval_state（fail-closed）。
	compareCalls := 0
	coordinator, spy, recorder, _ := testCoordinator(func(deps *PublishGateDeps) {
		deps.Compare = func(_, _ *domain.EvalRun) (domain.RunComparison, error) {
			compareCalls++
			return domain.RunComparison{Regressed: true}, nil
		}
	})
	baseline := runAnchored(domain.GroupMemory, 5)
	decision, err := coordinator.DecideSentinel(context.Background(), "host", domain.GroupMemory, baseline, nil)
	if err != nil {
		t.Fatalf("DecideSentinel err = %v, want nil", err)
	}
	if decision.Verdict != SentinelVerdictBlock {
		t.Fatalf("Verdict = %v, want block", decision.Verdict)
	}
	if compareCalls != 0 {
		t.Fatalf("Compare called %d times, want 0", compareCalls)
	}
	wantActions(t, spy, domain.LayerL3Sentinel+":block", domain.LayerL2+":regression")
	wantStateCalls(t, recorder)
}

func TestPublishGateCoordinator_NoBaseline_Passes(t *testing.T) {
	// 无基线 run（nil）→ pass（无回归信号）；不 Compare；ConfirmedSeq 仍锚定哨兵 run seq。
	compareCalls := 0
	coordinator, spy, recorder, _ := testCoordinator(func(deps *PublishGateDeps) {
		deps.Compare = func(_, _ *domain.EvalRun) (domain.RunComparison, error) {
			compareCalls++
			return domain.RunComparison{Regressed: true}, nil
		}
	})
	sentinel := runAnchored(domain.GroupMemory, 7)
	decision, err := coordinator.DecideSentinel(context.Background(), "host", domain.GroupMemory, nil, sentinel)
	if err != nil {
		t.Fatalf("DecideSentinel err = %v, want nil", err)
	}
	if decision.Verdict != SentinelVerdictPass {
		t.Fatalf("Verdict = %v, want pass", decision.Verdict)
	}
	if decision.BaselineSeq != 0 || decision.ConfirmedSeq != 7 {
		t.Fatalf("seqs = baseline %d / confirmed %d, want 0 / 7", decision.BaselineSeq, decision.ConfirmedSeq)
	}
	if compareCalls != 0 {
		t.Fatalf("Compare called %d times, want 0", compareCalls)
	}
	wantActions(t, spy, domain.LayerL3Sentinel+":pass", constants.GateLayerL3Platform+":"+domain.ActionPublishGated)
	wantStateCalls(t, recorder, evalStateCall{
		groupKey: domain.GroupMemory, seq: 7, state: constants.PlatformEvalStateSentinelPassed, actor: "host",
	})
}
