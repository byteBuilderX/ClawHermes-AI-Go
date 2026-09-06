package application

import (
	"context"
	"errors"
	"testing"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

type stubEmitter struct {
	called int
	last   port.ObservationEvent
	err    error
}

func (s *stubEmitter) Emit(_ context.Context, evt port.ObservationEvent) error {
	s.called++
	s.last = evt
	return s.err
}

func newTestServiceWithEmitter(e port.ObservationEmitter) *AgentService {
	return NewAgentService(AgentServiceDeps{Logger: zap.NewNop(), ObservationEmitter: e})
}

func TestEmitObservationPostsEvent(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1", TraceID: "trace-1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"})

	if emitter.called != 1 {
		t.Fatalf("Emit called %d times, want 1", emitter.called)
	}
	evt := emitter.last
	if evt.TenantID != "t1" || evt.TraceID != "trace-1" || evt.ExecutionID != "exec-1" {
		t.Fatalf("event identity mismatch: %+v", evt)
	}
	if evt.AgentID != "agent-1" || evt.ResourceKind != "agent" || evt.ResourceID != "agent-1" {
		t.Fatalf("event resource mismatch: %+v", evt)
	}
	if evt.CompletedAt == "" {
		t.Fatal("completed_at must be set")
	}
}

func TestEmitObservationNilEmitterNoPanic(t *testing.T) {
	s := newTestServiceWithEmitter(nil)
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"}) // 不得 panic
}

func TestEmitObservationNilResultSkips(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", nil)
	if emitter.called != 0 {
		t.Fatalf("Emit called %d times for nil result, want 0", emitter.called)
	}
}

func TestEmitObservationFailureDoesNotPropagate(t *testing.T) {
	emitter := &stubEmitter{err: errors.New("nats down")}
	s := newTestServiceWithEmitter(emitter)
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"}) // 失败仅记日志
}

func TestEmitObservationPopulatesRuleSignalsFromCollector(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	blocks := []domain.RuleBlock{
		{Rule: "tool_denylist", Tool: "x", Message: `tool "x" blocked by platform rule`},
		{Rule: "tool_denylist", Tool: "y", Message: `tool "y" blocked by platform rule`},
	}
	ctx := context.WithValue(context.Background(), ruleBlockCollectorKey{}, &blocks)
	s.emitObservation(ctx, ExecMeta{TenantID: "t1", TraceID: "trace-1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"})

	if emitter.called != 1 {
		t.Fatalf("Emit called %d times, want 1", emitter.called)
	}
	if len(emitter.last.RuleSignals) != 2 {
		t.Fatalf("rule_signals len = %d, want 2", len(emitter.last.RuleSignals))
	}
	if sig := emitter.last.RuleSignals[0]; sig.Rule != "tool_denylist" || sig.Message != `tool "x" blocked by platform rule` {
		t.Fatalf("rule signal mismatch: %+v", sig)
	}
	if sig := emitter.last.RuleSignals[1]; sig.Rule != "tool_denylist" || sig.Message != `tool "y" blocked by platform rule` {
		t.Fatalf("rule signal mismatch: %+v", sig)
	}
}

func TestEmitObservationEmptyCollectorYieldsNilSignals(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	// 无累积器或空累积器 → 信号 nil（omitempty 不出现）。
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"})
	if emitter.last.RuleSignals != nil {
		t.Fatalf("rule_signals = %v, want nil", emitter.last.RuleSignals)
	}
	if emitter.last.Behavior != nil {
		t.Fatalf("behavior = %v, want nil", emitter.last.Behavior)
	}
	// 空累积器同样返回 nil。
	empty := &[]domain.RuleBlock{}
	ctx := context.WithValue(context.Background(), ruleBlockCollectorKey{}, empty)
	s.emitObservation(ctx, ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"})
	if emitter.last.RuleSignals != nil {
		t.Fatalf("rule_signals = %v, want nil for empty collector", emitter.last.RuleSignals)
	}
}

func TestEmitObservationPopulatesBehaviorFromResult(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	result := &AgentResult{
		Output:   "ok",
		Degraded: true,
		NoAnswer: &domain.NoAnswerInfo{Retried: true},
	}
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", result)
	if emitter.last.Behavior == nil {
		t.Fatal("behavior must be non-nil when retry/degraded present")
	}
	if !emitter.last.Behavior.Retry || emitter.last.Behavior.Escalation || !emitter.last.Behavior.Abandonment {
		t.Fatalf("behavior mismatch: %+v", emitter.last.Behavior)
	}
}

func TestEmitObservationBehaviorNilForCleanResult(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	// 无 NoAnswer（nil）且未降级 → behavior nil（omitempty 不出现）。
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"})
	if emitter.last.Behavior != nil {
		t.Fatalf("behavior = %v, want nil", emitter.last.Behavior)
	}
	// NoAnswer 存在但 Retried=false、未降级 → 仍 nil。
	s.emitObservation(context.Background(), ExecMeta{TenantID: "t1"}, "agent-1", "exec-1", &AgentResult{Output: "ok", NoAnswer: &domain.NoAnswerInfo{}})
	if emitter.last.Behavior != nil {
		t.Fatalf("behavior = %v, want nil when no signals", emitter.last.Behavior)
	}
}

func TestEmitObservationDisabledGuardHitStillYieldsRuleSignal(t *testing.T) {
	emitter := &stubEmitter{}
	s := newTestServiceWithEmitter(emitter)
	blocks := &[]domain.RuleBlock{}
	ctx := context.WithValue(context.Background(), ruleBlockCollectorKey{}, blocks)
	g := NewRuleGuard(RuleGuardDeps{
		Enabled:  func(context.Context) bool { return false },
		Denylist: func(context.Context) []string { return []string{"danger_tool"} },
		Metrics:  observability.NoopMetrics{},
	})
	if block, blocked := g.Check(ctx, "danger_tool"); blocked || block != nil {
		t.Fatalf("disabled guard must not block, got blocked=%v block=%v", blocked, block)
	}
	if len(*blocks) != 1 {
		t.Fatalf("collector len = %d, want 1（O4：disabled 命中恒填累积器）", len(*blocks))
	}
	s.emitObservation(ctx, ExecMeta{TenantID: "t1", TraceID: "trace-1"}, "agent-1", "exec-1", &AgentResult{Output: "ok"})
	if emitter.called != 1 {
		t.Fatalf("Emit called %d times, want 1", emitter.called)
	}
	if len(emitter.last.RuleSignals) != 1 {
		t.Fatalf("rule_signals len = %d, want 1", len(emitter.last.RuleSignals))
	}
	if sig := emitter.last.RuleSignals[0]; sig.Rule != "tool_denylist" {
		t.Fatalf("rule signal mismatch: %+v", sig)
	}
}
