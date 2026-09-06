package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

type stubEvidenceReader struct {
	trace port.ObservedTrace
	err   error
}

func (s *stubEvidenceReader) Resolve(ctx context.Context, tenantID, traceID string) (port.ObservedTrace, error) {
	if s.err != nil {
		return port.ObservedTrace{}, s.err
	}
	return s.trace, nil
}

func (s *stubEvidenceReader) ResolveBatch(ctx context.Context, tenantID string, traceIDs []string) (map[string]port.ObservedTrace, error) {
	return nil, errors.New("not used")
}

type stubJudge struct {
	result  domain.AssertionResult
	err     error
	enabled bool
	calls   int
}

func (j *stubJudge) Enabled(ctx context.Context) bool { return j.enabled }
func (j *stubJudge) Judge(ctx context.Context, req port.JudgeRequest) (domain.AssertionResult, error) {
	j.calls++
	if j.err != nil {
		return domain.AssertionResult{}, j.err
	}
	return j.result, nil
}

type stubObservationRepo struct {
	saved     []domain.EvalObservation
	err       error
	latest    *domain.EvalObservation
	latestErr error
	updates   []domain.BehaviorSignals
}

func (s *stubObservationRepo) Save(ctx context.Context, tenantID string, obs *domain.EvalObservation) error {
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, *obs)
	return nil
}
func (s *stubObservationRepo) Get(ctx context.Context, tenantID, id string) (*domain.EvalObservation, error) {
	return nil, errors.New("not used")
}
func (s *stubObservationRepo) QueryByResource(ctx context.Context, tenantID, resourceKind, resourceID string,
	from, to *time.Time, limit, offset int,
) ([]domain.EvalObservation, error) {
	return nil, errors.New("not used")
}
func (s *stubObservationRepo) FindLatestByTrace(
	ctx context.Context, tenantID, traceID string,
) (*domain.EvalObservation, error) {
	if s.latestErr != nil {
		return nil, s.latestErr
	}
	return s.latest, nil
}
func (s *stubObservationRepo) UpdateBehaviorSignals(
	ctx context.Context, tenantID, observationID string, signals domain.BehaviorSignals,
) error {
	if s.err != nil {
		return s.err
	}
	s.updates = append(s.updates, signals)
	return nil
}

type stubTierReader struct {
	tier string
	err  error
}

func (s *stubTierReader) GetTenantTier(ctx context.Context, tenantID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.tier, nil
}

// coverageCall 记录一次 eval_sample_coverage 写入。
type coverageCall struct {
	resource string
	ratio    float64
}

// stubMetrics 记录 P1b 判异/覆盖率指标调用（嵌入 NoopMetrics 满足 MetricsProvider 全接口）。
type stubMetrics struct {
	observability.NoopMetrics
	sampleCoverages []coverageCall // RecordEvalSampleCoverage 调用序列
	behaviorSignals []string       // IncEvalBehaviorAnomaly 调用（"resource/signal"）
	gateActions     []string       // IncEvalGateAction 调用（"layer/action"）
}

func (m *stubMetrics) RecordEvalSampleCoverage(resource string, ratio float64) {
	m.sampleCoverages = append(m.sampleCoverages, coverageCall{resource: resource, ratio: ratio})
}

func (m *stubMetrics) IncEvalBehaviorAnomaly(resource, signal string) {
	m.behaviorSignals = append(m.behaviorSignals, resource+"/"+signal)
}

func (m *stubMetrics) IncEvalGateAction(layer, action string) {
	m.gateActions = append(m.gateActions, layer+"/"+action)
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func newTestObservationService(repo *stubObservationRepo, reader *stubEvidenceReader, judge *stubJudge) *ObservationService {
	return NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
	})
}

func TestObservationServiceProcessJudgesAndSaves(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{
		TraceID: "trace-1", Input: "用户问题", Output: "助手回答",
		CostUSD: 0.01, LatencyMs: 800, Success: true,
	}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	svc := newTestObservationService(repo, reader, judge)

	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-1", ExecutionID: "exec-1",
		AgentID: "agent-1", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	saved := repo.saved[0]
	if saved.TraceID != "trace-1" || saved.Resource.Kind != "agent" || saved.Resource.ResourceID != "agent-1" {
		t.Fatalf("saved identity mismatch: %+v", saved)
	}
	if len(saved.Signals.Judge) != 3 {
		t.Fatalf("judge signals = %d, want 3 dimensions", len(saved.Signals.Judge))
	}
	if saved.Verdict != domain.VerdictPass {
		t.Fatalf("verdict = %s, want pass", saved.Verdict)
	}
	if judge.calls != 3 {
		t.Fatalf("judge calls = %d, want 3", judge.calls)
	}
}

func TestObservationServiceProcessDisabledSkips(t *testing.T) {
	repo := &stubObservationRepo{}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:  func(context.Context) bool { return false },
		Evidence: &stubEvidenceReader{}, Judge: &stubJudge{},
		Repo: repo, Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
	})
	if err := svc.Process(context.Background(), domain.ObservationReferenceEvent{TraceID: "trace-1"}); err != nil {
		t.Fatalf("Process disabled: %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("disabled must not save, got %d", len(repo.saved))
	}
}

func TestObservationServiceProcessEvidenceErrorPropagates(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{err: errors.New("opik down")}
	svc := newTestObservationService(repo, reader, &stubJudge{enabled: true})
	err := svc.Process(context.Background(), domain.ObservationReferenceEvent{TraceID: "trace-1", ResourceKind: "agent", ResourceID: "a1"})
	if err == nil {
		t.Fatal("evidence error must propagate for NATS redelivery")
	}
	if len(repo.saved) != 0 {
		t.Fatalf("must not save on evidence error, got %d", len(repo.saved))
	}
}

func TestObservationServiceProcessJudgeFailureDegrades(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-1", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, err: errors.New("judge down")}
	svc := newTestObservationService(repo, reader, judge)
	if err := svc.Process(context.Background(), domain.ObservationReferenceEvent{TraceID: "trace-1", ResourceKind: "agent", ResourceID: "a1"}); err != nil {
		t.Fatalf("Process with judge failure should degrade without error: %v", err)
	}
	// judge 故障 §14 采样降级跳过：不落库、不重投、不伪造零信号 pass 观察。
	if len(repo.saved) != 0 {
		t.Fatalf("judge failure must skip observation, got %d saved", len(repo.saved))
	}
}

func TestObservationServiceProcessJudgeDisabledSkips(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-1", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: false} // judge 关闭（配置态）
	svc := newTestObservationService(repo, reader, judge)
	if err := svc.Process(context.Background(), domain.ObservationReferenceEvent{
		TraceID: "trace-1", ResourceKind: "agent", ResourceID: "a1",
	}); err != nil {
		t.Fatalf("Process with judge disabled should skip without error: %v", err)
	}
	// judge 关闭时跳过本次观测：不落零信号 pass 观测（§14 精神），非故障降级。
	if len(repo.saved) != 0 {
		t.Fatalf("judge disabled must not save zero-signal observation, got %d", len(repo.saved))
	}
}

func TestObservationServiceProcessInvalidObservationDrops(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-1", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	svc := newTestObservationService(repo, reader, judge)
	// ResourceID 为空 → buildObservation 后 obs.Validate 触发「resource id required」。
	evt := domain.ObservationReferenceEvent{
		TraceID: "trace-1", ResourceKind: "agent", ResourceID: "",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("invalid observation must drop without error (no redelivery): %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("invalid observation must not save, got %d", len(repo.saved))
	}
}

func TestObservationServiceProcessSaveFailureDrops(t *testing.T) {
	repo := &stubObservationRepo{err: errors.New("repo down")} // Save 失败
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-1", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	svc := newTestObservationService(repo, reader, judge)
	if err := svc.Process(context.Background(), domain.ObservationReferenceEvent{
		TraceID: "trace-1", ResourceKind: "agent", ResourceID: "a1",
	}); err != nil {
		t.Fatalf("save failure must drop without error (no redelivery): %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("save failure must not save, got %d", len(repo.saved))
	}
}

func TestObservationServiceProcessStratumFilledFromTenantTier(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{
		TraceID: "trace-1", Input: "用户问题", Output: "助手回答",
		CostUSD: 0.01, LatencyMs: 800, Success: true,
	}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
		TenantTier: &stubTierReader{tier: "pro"},
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-1", ExecutionID: "exec-1",
		AgentID: "agent-1", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	if got := repo.saved[0].Stratum; got != "pro" {
		t.Fatalf("stratum = %q, want %q", got, "pro")
	}
}

func TestObservationServiceProcessStratumEmptyOnTierResolveFailure(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-1", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
		TenantTier: &stubTierReader{err: errors.New("tier repo down")},
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-1", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("tier resolve failure must not block observation: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	if got := repo.saved[0].Stratum; got != "" {
		t.Fatalf("stratum = %q, want empty on tier resolve failure", got)
	}
}

// newObservationServiceWithPlatformVersion 构造带 PlatformVersion 读取器的观测服务。
func newObservationServiceWithPlatformVersion(
	seq int64, ok bool, err error,
) (*ObservationService, *stubObservationRepo) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{
		TraceID: "trace-1", Input: "q", Output: "a", Success: true,
	}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
		PlatformVersion: func(context.Context) (int64, bool, error) { return seq, ok, err },
	})
	return svc, repo
}

func TestObservationServiceProcessPlatformVersionBound(t *testing.T) {
	svc, repo := newObservationServiceWithPlatformVersion(3, true, nil)
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-1", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	got := repo.saved[0].Param.Platform
	if got.VersionSeq != 3 {
		t.Fatalf("platform version_seq = %d, want 3", got.VersionSeq)
	}
	if got.GroupKey != constants.PlatformGroupEvaluation {
		t.Fatalf("platform group_key = %q, want %q", got.GroupKey, constants.PlatformGroupEvaluation)
	}
}

func TestObservationServiceProcessPlatformVersionUnknownWhenNil(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-1", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	// PlatformVersion 未装配（nil）：fail-open 标记 unknown，不阻断落库。
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-1", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("nil platform version reader must not block observation: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	if got := repo.saved[0].Param.Platform; got.VersionSeq != 0 || got.GroupKey != "" {
		t.Fatalf("platform anchor = %+v, want unknown (seq 0, empty group)", got)
	}
}

func TestObservationServiceProcessPlatformVersionUnknownOnResolveError(t *testing.T) {
	svc, repo := newObservationServiceWithPlatformVersion(0, false, errors.New("config store down"))
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-1", ResourceKind: "agent", ResourceID: "agent-1",
	}
	// 读取失败 fail-open：标记 unknown，不阻断落库（同 resolveStratum 语义）。
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("platform version resolve failure must not block observation: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	if got := repo.saved[0].Param.Platform; got.VersionSeq != 0 || got.GroupKey != "" {
		t.Fatalf("platform anchor = %+v, want unknown on resolve error", got)
	}
}

func TestObservationServiceProcessPlatformVersionUnknownNoPublished(t *testing.T) {
	// 无已发布版本（(0,false,nil)）：版本锚点 unknown，观测照常落库。
	svc, repo := newObservationServiceWithPlatformVersion(0, false, nil)
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-1", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("no published version must not block observation: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	if got := repo.saved[0].Param.Platform; got.VersionSeq != 0 || got.GroupKey != "" {
		t.Fatalf("platform anchor = %+v, want unknown (seq 0)", got)
	}
}

func TestObservationServiceApplyBehaviorSignalsNoObservation(t *testing.T) {
	repo := &stubObservationRepo{} // latest 为 nil：采样未覆盖该 trace
	svc := newTestObservationService(repo, &stubEvidenceReader{}, &stubJudge{})
	if err := svc.ApplyBehaviorSignals(context.Background(), "t1", "trace-missing",
		domain.BehaviorSignals{Abandonment: true}); err != nil {
		t.Fatalf("ApplyBehaviorSignals: %v", err)
	}
	if len(repo.updates) != 0 {
		t.Fatalf("UpdateBehaviorSignals called %d times, want 0", len(repo.updates))
	}
}

func TestObservationServiceApplyBehaviorSignalsMergesIntoLatest(t *testing.T) {
	repo := &stubObservationRepo{latest: &domain.EvalObservation{
		ID: "obs-1", TraceID: "trace-1",
		Signals: domain.ObservationSignals{Behavior: domain.BehaviorSignals{Retry: true}},
	}}
	svc := newTestObservationService(repo, &stubEvidenceReader{}, &stubJudge{})
	if err := svc.ApplyBehaviorSignals(context.Background(), "t1", "trace-1",
		domain.BehaviorSignals{Abandonment: true, Escalation: true}); err != nil {
		t.Fatalf("ApplyBehaviorSignals: %v", err)
	}
	if len(repo.updates) != 1 {
		t.Fatalf("UpdateBehaviorSignals called %d times, want 1", len(repo.updates))
	}
	got := repo.updates[0]
	if !got.Retry || !got.Abandonment || !got.Escalation {
		t.Fatalf("merged behavior = %+v, want all true", got)
	}
}

func TestObservationServiceApplyBehaviorSignalsNoChangeShortCircuits(t *testing.T) {
	repo := &stubObservationRepo{latest: &domain.EvalObservation{
		ID: "obs-1", TraceID: "trace-1",
		Signals: domain.ObservationSignals{Behavior: domain.BehaviorSignals{Retry: true, Abandonment: true}},
	}}
	svc := newTestObservationService(repo, &stubEvidenceReader{}, &stubJudge{})
	// 输入信号已包含于现有行为：合并无变化 → 幂等短路，不写库。
	if err := svc.ApplyBehaviorSignals(context.Background(), "t1", "trace-1",
		domain.BehaviorSignals{Retry: true, Abandonment: true}); err != nil {
		t.Fatalf("ApplyBehaviorSignals: %v", err)
	}
	if len(repo.updates) != 0 {
		t.Fatalf("UpdateBehaviorSignals called %d times, want 0 (no change)", len(repo.updates))
	}
}

func TestObservationServiceApplyBehaviorSignalsRepoErrorPropagates(t *testing.T) {
	repo := &stubObservationRepo{latestErr: errors.New("repo down")}
	svc := newTestObservationService(repo, &stubEvidenceReader{}, &stubJudge{})
	if err := svc.ApplyBehaviorSignals(context.Background(), "t1", "trace-1",
		domain.BehaviorSignals{Abandonment: true}); err == nil {
		t.Fatal("repo error must propagate to caller (best-effort 调用方忽略)")
	}
}

func TestObservationServiceApplyBehaviorSignalsNilRepoNoop(t *testing.T) {
	svc := NewObservationService(ObservationServiceDeps{Logger: zap.NewNop()})
	if err := svc.ApplyBehaviorSignals(context.Background(), "t1", "trace-1",
		domain.BehaviorSignals{Abandonment: true}); err != nil {
		t.Fatalf("nil repo must noop: %v", err)
	}
}

func TestObservationServiceAnomalyVerdict(t *testing.T) {
	t.Run("rule hit verdict block", func(t *testing.T) {
		repo := &stubObservationRepo{}
		reader := &stubEvidenceReader{trace: port.ObservedTrace{
			TraceID: "trace-rule", Input: "q", Output: "a",
		}}
		judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
		metrics := &stubMetrics{}
		svc := NewObservationService(ObservationServiceDeps{
			Enabled:    func(context.Context) bool { return true },
			SampleRate: func(context.Context) float64 { return 1.0 },
			Evidence:   reader, Judge: judge, Repo: repo,
			Metrics: metrics, Logger: zap.NewNop(),
		})
		evt := domain.ObservationReferenceEvent{
			TenantID: "t1", TraceID: "trace-rule", ResourceKind: "agent", ResourceID: "agent-1",
			RuleSignals: []domain.RuleSignalPayload{{
				Rule: "tool_denylist", Message: `tool "x" blocked by platform rule`,
			}},
		}
		if err := svc.Process(context.Background(), evt); err != nil {
			t.Fatalf("Process: %v", err)
		}
		if len(repo.saved) != 1 {
			t.Fatalf("saved %d, want 1", len(repo.saved))
		}
		saved := repo.saved[0]
		if saved.Verdict != domain.VerdictBlock {
			t.Fatalf("verdict = %s, want block", saved.Verdict)
		}
		if len(saved.Signals.Rule) != 1 || saved.Signals.Rule[0].Rule != "tool_denylist" {
			t.Fatalf("rule signals = %+v, want [tool_denylist]", saved.Signals.Rule)
		}
		if !contains(metrics.behaviorSignals, "agent/rule_block") {
			t.Fatalf("behavior anomaly metrics = %v, want agent/rule_block", metrics.behaviorSignals)
		}
		if !contains(metrics.gateActions, "detect/block") {
			t.Fatalf("gate action metrics = %v, want detect/block", metrics.gateActions)
		}
	})

	t.Run("behavior abandonment verdict flag", func(t *testing.T) {
		repo := &stubObservationRepo{}
		reader := &stubEvidenceReader{trace: port.ObservedTrace{
			TraceID: "trace-abandon", Input: "q", Output: "a",
		}}
		judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
		metrics := &stubMetrics{}
		svc := NewObservationService(ObservationServiceDeps{
			Enabled:    func(context.Context) bool { return true },
			SampleRate: func(context.Context) float64 { return 1.0 },
			Evidence:   reader, Judge: judge, Repo: repo,
			Metrics: metrics, Logger: zap.NewNop(),
		})
		evt := domain.ObservationReferenceEvent{
			TenantID: "t1", TraceID: "trace-abandon", ResourceKind: "agent", ResourceID: "agent-1",
			Behavior: &domain.BehaviorSignalPayload{Abandonment: true},
		}
		if err := svc.Process(context.Background(), evt); err != nil {
			t.Fatalf("Process: %v", err)
		}
		if len(repo.saved) != 1 {
			t.Fatalf("saved %d, want 1", len(repo.saved))
		}
		saved := repo.saved[0]
		if saved.Verdict != domain.VerdictFlag {
			t.Fatalf("verdict = %s, want flag", saved.Verdict)
		}
		if !saved.Signals.Behavior.Abandonment {
			t.Fatal("behavior abandonment not persisted in observation")
		}
		if !contains(metrics.behaviorSignals, "agent/behavior_abandonment") {
			t.Fatalf("behavior anomaly metrics = %v, want agent/behavior_abandonment", metrics.behaviorSignals)
		}
		if !contains(metrics.gateActions, "detect/flag") {
			t.Fatalf("gate action metrics = %v, want detect/flag", metrics.gateActions)
		}
	})

	t.Run("nil behavior event does not panic and passes", func(t *testing.T) {
		// Behavior==nil（全 false 事件，agent 侧 Go omitempty 语义下不发送行为段）：
		// behaviorFromEvent 必须安全返回全 false，不得 panic（§14 不阻断执行）。
		repo := &stubObservationRepo{}
		reader := &stubEvidenceReader{trace: port.ObservedTrace{
			TraceID: "trace-nil-behavior", Input: "q", Output: "a",
		}}
		judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
		svc := newTestObservationService(repo, reader, judge)
		evt := domain.ObservationReferenceEvent{
			TenantID: "t1", TraceID: "trace-nil-behavior", ResourceKind: "agent", ResourceID: "agent-1",
		}
		if err := svc.Process(context.Background(), evt); err != nil {
			t.Fatalf("Process with nil behavior: %v", err)
		}
		if len(repo.saved) != 1 {
			t.Fatalf("saved %d, want 1", len(repo.saved))
		}
		if got := repo.saved[0].Verdict; got != domain.VerdictPass {
			t.Fatalf("verdict = %s, want pass", got)
		}
	})

	t.Run("rule hit + judge below threshold → final verdict block", func(t *testing.T) {
		// 回归（review fix）：applyJudge 先置 flag，applyAnomalyVerdict 后置 block——
		// block > flag 优先级必须保持，且 `verdict != block` 守卫抑制
		// judge_below_threshold 计数（不把 rule-block 降级为 flag）。
		repo := &stubObservationRepo{}
		reader := &stubEvidenceReader{trace: port.ObservedTrace{
			TraceID: "trace-rule-judge", Input: "q", Output: "a",
		}}
		judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: false}}
		metrics := &stubMetrics{}
		svc := NewObservationService(ObservationServiceDeps{
			Enabled:    func(context.Context) bool { return true },
			SampleRate: func(context.Context) float64 { return 1.0 },
			Evidence:   reader, Judge: judge, Repo: repo,
			Metrics: metrics, Logger: zap.NewNop(),
		})
		evt := domain.ObservationReferenceEvent{
			TenantID: "t1", TraceID: "trace-rule-judge", ResourceKind: "agent", ResourceID: "agent-1",
			RuleSignals: []domain.RuleSignalPayload{{
				Rule: "tool_denylist", Message: `tool "x" blocked by platform rule`,
			}},
		}
		if err := svc.Process(context.Background(), evt); err != nil {
			t.Fatalf("Process: %v", err)
		}
		if len(repo.saved) != 1 {
			t.Fatalf("saved %d, want 1", len(repo.saved))
		}
		saved := repo.saved[0]
		if saved.Verdict != domain.VerdictBlock {
			t.Fatalf("verdict = %s, want block (block > flag)", saved.Verdict)
		}
		if len(saved.Signals.Judge) != 3 {
			t.Fatalf("judge signals = %d, want 3 (judge must run before anomaly verdict)", len(saved.Signals.Judge))
		}
		if !contains(metrics.behaviorSignals, "agent/rule_block") {
			t.Fatalf("behavior anomaly metrics = %v, want agent/rule_block", metrics.behaviorSignals)
		}
		if !contains(metrics.gateActions, "detect/block") {
			t.Fatalf("gate action metrics = %v, want detect/block", metrics.gateActions)
		}
		if contains(metrics.behaviorSignals, "agent/judge_below_threshold") {
			t.Fatalf("block verdict must suppress judge_below_threshold, got %v", metrics.behaviorSignals)
		}
	})

	t.Run("judge below threshold verdict flag and metric", func(t *testing.T) {
		// 回归（review fix）：applyAnomalyVerdict 在 applyJudge 之后运行，judge 信号
		// 已填充——纯 judge 跌阈事件必须触发 judge_below_threshold 判异指标 + detect/flag。
		repo := &stubObservationRepo{}
		reader := &stubEvidenceReader{trace: port.ObservedTrace{
			TraceID: "trace-judge-low", Input: "q", Output: "a",
		}}
		judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: false}}
		metrics := &stubMetrics{}
		svc := NewObservationService(ObservationServiceDeps{
			Enabled:    func(context.Context) bool { return true },
			SampleRate: func(context.Context) float64 { return 1.0 },
			Evidence:   reader, Judge: judge, Repo: repo,
			Metrics: metrics, Logger: zap.NewNop(),
		})
		evt := domain.ObservationReferenceEvent{
			TenantID: "t1", TraceID: "trace-judge-low", ResourceKind: "agent", ResourceID: "agent-1",
		}
		if err := svc.Process(context.Background(), evt); err != nil {
			t.Fatalf("Process: %v", err)
		}
		if len(repo.saved) != 1 {
			t.Fatalf("saved %d, want 1", len(repo.saved))
		}
		if saved := repo.saved[0]; saved.Verdict != domain.VerdictFlag {
			t.Fatalf("verdict = %s, want flag", saved.Verdict)
		}
		if !contains(metrics.behaviorSignals, "agent/judge_below_threshold") {
			t.Fatalf("behavior anomaly metrics = %v, want agent/judge_below_threshold", metrics.behaviorSignals)
		}
		if !contains(metrics.gateActions, "detect/flag") {
			t.Fatalf("gate action metrics = %v, want detect/flag", metrics.gateActions)
		}
	})
}

func TestObservationServiceSampleCoverageRecorded(t *testing.T) {
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{
		TraceID: "trace-cov", Input: "q", Output: "a",
	}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	metrics := &stubMetrics{}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: metrics, Logger: zap.NewNop(),
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-cov", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// I-1 新语义：分母 = 采样候选（采样通过且 judge 开启），分子 = 落库。
	// 到达刷新 ratio=0/1=0.0；落库后刷新 ratio=1/1=1.0。健康稳态最终 Gauge 应为 1.0。
	if len(metrics.sampleCoverages) == 0 {
		t.Fatal("RecordEvalSampleCoverage not called")
	}
	last := metrics.sampleCoverages[len(metrics.sampleCoverages)-1]
	if last.resource != "agent" || last.ratio != 1.0 {
		t.Fatalf("last sample coverage = %+v, want agent/1.0", last)
	}
}

func TestObservationServiceSampleCoverageJudgeDisabledNotCounted(t *testing.T) {
	// I-1 回归：judge 配置关闭（主动停观测）不计入分母——不写覆盖率、不因此告警。
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-cov-off", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: false}
	metrics := &stubMetrics{}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: metrics, Logger: zap.NewNop(),
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-cov-off", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(metrics.sampleCoverages) != 0 {
		t.Fatalf("sample coverages = %+v, want none (judge disabled must not count arrival)", metrics.sampleCoverages)
	}
}

func TestObservationServiceSampleCoverageJudgeFailureDropsRatio(t *testing.T) {
	// I-1 回归：judge 故障降级（真正的静默跳过）计入分母但不计入分子 → 覆盖率掉低。
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-cov-degrade", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, err: errors.New("judge down")}
	metrics := &stubMetrics{}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: metrics, Logger: zap.NewNop(),
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-cov-degrade", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(metrics.sampleCoverages) == 0 {
		t.Fatal("RecordEvalSampleCoverage not called")
	}
	last := metrics.sampleCoverages[len(metrics.sampleCoverages)-1]
	if last.resource != "agent" || last.ratio != 0.0 {
		t.Fatalf("last sample coverage = %+v, want agent/0.0 (arrival counted, nothing saved)", last)
	}
}

// stubGateSink 记录 HandleObservation 调用（观察 hook 用例）；err 非 nil 模拟门禁内部失败。
type stubGateSink struct {
	calls int
	err   error
}

func (s *stubGateSink) HandleObservation(_ context.Context, _ string, _ domain.EvalObservation) error {
	s.calls++
	return s.err
}

func TestObservationServiceProcessGateNilSkips(t *testing.T) {
	// Gate 未装配（nil）→ 门禁评估静默跳过（fail-open），观测照常落库。
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-gate-nil", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	svc := newTestObservationService(repo, reader, judge)
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-gate-nil", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process with nil gate: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
}

func TestObservationServiceProcessCallsGateSink(t *testing.T) {
	// Gate 装配 + 正常 Process → handleGateObservation 触发一次 HandleObservation。
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-gate", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	gate := &stubGateSink{}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
		Gate: gate,
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-gate", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	if gate.calls != 1 {
		t.Fatalf("gate sink calls = %d, want 1", gate.calls)
	}
}

func TestObservationServiceProcessGateFailureDoesNotBlock(t *testing.T) {
	// 门禁内部失败（Gate.HandleObservation 返回 err）→ fail-open：观测主流程
	// 不阻断、不报错，观测照常落库（与 escalateToReview 同哲学）。
	repo := &stubObservationRepo{}
	reader := &stubEvidenceReader{trace: port.ObservedTrace{TraceID: "trace-gate-err", Input: "q", Output: "a"}}
	judge := &stubJudge{enabled: true, result: domain.AssertionResult{Passed: true}}
	gate := &stubGateSink{err: errors.New("gate down")}
	svc := NewObservationService(ObservationServiceDeps{
		Enabled:    func(context.Context) bool { return true },
		SampleRate: func(context.Context) float64 { return 1.0 },
		Evidence:   reader, Judge: judge, Repo: repo,
		Metrics: observability.NoopMetrics{}, Logger: zap.NewNop(),
		Gate: gate,
	})
	evt := domain.ObservationReferenceEvent{
		TenantID: "t1", TraceID: "trace-gate-err", ResourceKind: "agent", ResourceID: "agent-1",
	}
	if err := svc.Process(context.Background(), evt); err != nil {
		t.Fatalf("gate failure must not block observation: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d observations, want 1", len(repo.saved))
	}
	if gate.calls != 1 {
		t.Fatalf("gate sink calls = %d, want 1", gate.calls)
	}
}
