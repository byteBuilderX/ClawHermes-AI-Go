package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeReviewRepo struct {
	inserted []domain.ReviewItem
	marked   map[string]domain.HumanVerdict
	err      error
}

func (f *fakeReviewRepo) UpsertItem(_ context.Context, _ string, item *domain.ReviewItem) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.inserted = append(f.inserted, *item)
	return true, nil
}
func (f *fakeReviewRepo) GetItem(_ context.Context, _, id string) (*domain.ReviewItem, error) {
	for i := range f.inserted {
		if f.inserted[i].ID == id {
			return &f.inserted[i], nil
		}
	}
	return nil, nil
}
func (f *fakeReviewRepo) ListItems(
	_ context.Context, _ string, _ port.ReviewFilter,
) ([]domain.ReviewItem, int64, error) {
	return f.inserted, int64(len(f.inserted)), nil
}
func (f *fakeReviewRepo) MarkReviewed(_ context.Context, _, id string, v domain.HumanVerdict, _, _ string) error {
	if f.err != nil {
		return f.err
	}
	if f.marked == nil {
		f.marked = map[string]domain.HumanVerdict{}
	}
	f.marked[id] = v
	return nil
}
func (f *fakeReviewRepo) CreateCalibrationSample(_ context.Context, _ string, _ *domain.CalibrationSample) error {
	if f.err != nil {
		return f.err
	}
	return nil
}
func (f *fakeReviewRepo) CreateAttributionEntry(_ context.Context, _ string, _ *domain.AttributionEntry) error {
	if f.err != nil {
		return f.err
	}
	return nil
}
func (f *fakeReviewRepo) CountPending(_ context.Context, _ string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return int64(len(f.inserted)), nil
}

// raceReviewRepo 模拟并发评审竞态：Decide 首次 GetItem 读到 pending，MarkReviewed
// 返回 PgReviewRepository 的普通错误签名（非 sentinel，条目已非 pending），再次
// GetItem 返回已被另一评审者置为 reviewed 的最新条目。用于验证 Decide 对真实 Pg
// 错误签名的幂等降级。
type raceReviewRepo struct {
	*fakeReviewRepo
	getCalls int
}

func (r *raceReviewRepo) GetItem(ctx context.Context, tenantID, id string) (*domain.ReviewItem, error) {
	r.getCalls++
	item, err := r.fakeReviewRepo.GetItem(ctx, tenantID, id)
	if err != nil || item == nil {
		return item, err
	}
	if r.getCalls > 1 {
		reviewed := *item
		reviewed.Status = domain.ReviewStatusReviewed
		reviewed.HumanVerdict = domain.HumanVerdictPass
		return &reviewed, nil
	}
	return item, nil
}

func (r *raceReviewRepo) MarkReviewed(_ context.Context, _, id string, _ domain.HumanVerdict, _, _ string) error {
	// 模拟 PgReviewRepository.MarkReviewed：条目已非 pending 时返回普通错误。
	return fmt.Errorf("eval review item %s not pending (or missing)", id)
}

func newTestReviewService(repo port.ReviewRepository) *ReviewService {
	return newTestReviewServiceWithMetrics(repo, observability.NoopMetrics{})
}

func newTestReviewServiceWithMetrics(repo port.ReviewRepository, metrics observability.MetricsProvider) *ReviewService {
	return NewReviewService(ReviewServiceDeps{
		Repo:    repo,
		Metrics: metrics,
		Logger:  zap.NewNop(),
		Cfg:     domain.ReviewConfig{LowConfidenceThreshold: 0.6, JudgePassThreshold: 0.5},
	})
}

// stubReviewMetrics 记录 SetEvalReviewBacklog 调用序列（嵌入 NoopMetrics 满足
// MetricsProvider 全接口，只覆盖积压指标）。
type stubReviewMetrics struct {
	observability.NoopMetrics
	backlog []int64
}

func (m *stubReviewMetrics) SetEvalReviewBacklog(count int64) {
	m.backlog = append(m.backlog, count)
}

// countPendingErrRepo 让 CountPending 独立失败，用于验证积压刷新失败 fail-open。
type countPendingErrRepo struct {
	*fakeReviewRepo
}

func (r *countPendingErrRepo) CountPending(_ context.Context, _ string) (int64, error) {
	return 0, errors.New("count pending down")
}

// perTenantPendingRepo 让 CountPending 按租户返回不同值/错误，用于跨租户求和刷新
// eval_review_backlog（spec §8.3）。外层方法遮蔽嵌入 fakeReviewRepo 的 CountPending。
type perTenantPendingRepo struct {
	*fakeReviewRepo
	pending map[string]int64
	errOn   map[string]error
}

func (r *perTenantPendingRepo) CountPending(_ context.Context, tenantID string) (int64, error) {
	if err := r.errOn[tenantID]; err != nil {
		return 0, err
	}
	return r.pending[tenantID], nil
}

// newTestReviewServiceWithTenants 构造注入 TenantIDs 的 ReviewService（nil 时走
// 单租户路径）。TenantIDs 枚举活动租户供跨租户求和。
func newTestReviewServiceWithTenants(
	repo port.ReviewRepository, metrics observability.MetricsProvider, tenantIDs []string,
) *ReviewService {
	return NewReviewService(ReviewServiceDeps{
		Repo:    repo,
		Metrics: metrics,
		Logger:  zap.NewNop(),
		Cfg:     domain.ReviewConfig{LowConfidenceThreshold: 0.6, JudgePassThreshold: 0.5},
		TenantIDs: func(ctx context.Context) ([]string, error) {
			return tenantIDs, nil
		},
	})
}

// promoteSuiteRepo 遮蔽 fakeSuiteRepo.AddDraftCases 记录 promote 沉淀的草稿
// case（fake 原实现返回 nil 不记录，无法断言 promote 调用）。其余方法嵌入继承，
// 其 CreateSuite 在惰性创建路径下会把 suite/revision 存进 fake 字段供断言。
type promoteSuiteRepo struct {
	*fakeSuiteRepo
	added           []domain.EvalCase
	addedRevisionID string
}

func (f *promoteSuiteRepo) AddDraftCases(_ context.Context, _ string, revisionID string, cases []domain.EvalCase) error {
	f.addedRevisionID = revisionID
	f.added = append(f.added, cases...)
	return nil
}

// stubTraceEvidence 实现 port.TraceEvidenceReader，供 promote 的 observation
// 来源解析 trace 构造 case（ResolveBatch 未被 promote 使用）。
type stubTraceEvidence struct {
	trace port.ObservedTrace
}

func (s *stubTraceEvidence) Resolve(_ context.Context, _, _ string) (port.ObservedTrace, error) {
	return s.trace, nil
}

func (s *stubTraceEvidence) ResolveBatch(context.Context, string, []string) (map[string]port.ObservedTrace, error) {
	return nil, nil
}

// newTestReviewServiceWithPromote 构造注入 Suites/Evidence 的 ReviewService，
// 让 judge_misjudgment 决策触发 promote 沉淀评测集草稿（Suites 为 nil 时跳过）。
func newTestReviewServiceWithPromote(
	repo port.ReviewRepository, suites port.SuiteRepository, evidence port.TraceEvidenceReader,
) *ReviewService {
	return NewReviewService(ReviewServiceDeps{
		Repo:     repo,
		Suites:   suites,
		Evidence: evidence,
		Metrics:  observability.NoopMetrics{},
		Logger:   zap.NewNop(),
		Cfg:      domain.ReviewConfig{LowConfidenceThreshold: 0.6, JudgePassThreshold: 0.5},
	})
}

func observationForTest() *domain.EvalObservation {
	return &domain.EvalObservation{
		ID: "obs-1", TraceID: "t-1",
		Resource: domain.ObservationResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "s1"},
		Verdict:  domain.VerdictPass,
		Signals: domain.ObservationSignals{Judge: []domain.JudgeSignal{
			// 新置信度语义（§6.6）：空/过短理由 = 含糊 = 触发 low_confidence。默认夹具
			// 须带实质 Reason，供"无触发"类测试保持不触发；低置信用例再显式改 Confidence。
			{Dimension: "faithfulness", Score: 1.0, Confidence: 0.9, Reason: "理由充分，结果符合预期"},
		}},
	}
}

func TestTryEscalateObservationFiresOnLowConfidence(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	obs := observationForTest()
	obs.Signals.Judge[0].Confidence = 0.3
	if err := svc.TryEscalateObservation(context.Background(), "t1", obs); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(repo.inserted))
	}
	got := repo.inserted[0]
	if got.TriggerReason != domain.TriggerLowConfidence || got.SourceType != domain.ReviewSourceObservation {
		t.Fatalf("unexpected item: %+v", got)
	}
	if got.ResourceKind != domain.ResourceKindSkill || got.ResourceID != "s1" {
		t.Fatalf("resource mismatch: %+v", got)
	}
}

func TestTryEscalateObservationNoTriggerNoInsert(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	if err := svc.TryEscalateObservation(context.Background(), "t1", observationForTest()); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 0 {
		t.Fatalf("inserted = %d, want 0", len(repo.inserted))
	}
}

func TestTryEscalateCaseResultFiresOnNeedsReview(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	result := domain.EvalCaseResult{ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: true, ProcessPass: true}
	c := domain.EvalCase{ID: "c1", NeedsReview: true, AssertionMode: domain.AssertionJudge}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.9, Message: "输出完全符合预期"}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, true, true,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 1 || repo.inserted[0].TriggerReason != domain.TriggerNeedsReview {
		t.Fatalf("unexpected: %+v", repo.inserted)
	}
	if repo.inserted[0].RunID != "run-1" || repo.inserted[0].SourceID != "cr-1" {
		t.Fatalf("run/source mismatch: %+v", repo.inserted[0])
	}
}

// TestTryEscalateCaseResultRuleCaseOnlyConflict 覆盖规则断言 case 的评审池触发
// （§6.5 §6.6）：输出 pass + 过程 fail → 仅 process_output_conflict。规则 case 无
// judge 信号，低置信（assertion.Confidence 0.3 < 0.6）与 needs_review 都不得在
// 规则分支误触发（需求红线：规则 case 不产生 judge 信号）。
func TestTryEscalateCaseResultRuleCaseOnlyConflict(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	result := domain.EvalCaseResult{
		ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: true,
		ProcessPass: false, ProcessFailure: "process:must_not_call:delete",
	}
	c := domain.EvalCase{ID: "c1", AssertionMode: domain.AssertionContains, NeedsReview: true}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.3}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, true, false,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(repo.inserted))
	}
	if repo.inserted[0].TriggerReason != domain.TriggerProcessOutputConflict {
		t.Fatalf("trigger = %q, want process_output_conflict", repo.inserted[0].TriggerReason)
	}
}

// TestTryEscalateCaseResultRuleCaseNoConflictNoInsert 覆盖规则 case 无过程冲突
// （输出 pass + 过程 pass）时 rule 分支不进池（TriggersForProcessConflict 空）。
func TestTryEscalateCaseResultRuleCaseNoConflictNoInsert(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	result := domain.EvalCaseResult{ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: true, ProcessPass: true}
	c := domain.EvalCase{ID: "c1", AssertionMode: domain.AssertionContains}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.9}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, true, true,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 0 {
		t.Fatalf("inserted = %d, want 0", len(repo.inserted))
	}
}

// TestTryEscalateCaseResultJudgeConflict 覆盖 judge case 的评审池触发
// （§6.5 §6.6）：输出 pass + 过程 fail → 实际产生 process_output_conflict 条目。
// 现有覆盖只有规则分支与 TriggersForProcessConflict 纯函数，缺 judge 分支到
// UpsertItem 的落条断言（judge 分支还叠加 needs_review / low_confidence 判定）。
func TestTryEscalateCaseResultJudgeConflict(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	result := domain.EvalCaseResult{
		ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: false,
		ProcessPass: false, ProcessFailure: "process:must_not_call:delete",
	}
	c := domain.EvalCase{ID: "c1", AssertionMode: domain.AssertionJudge}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.9, Message: "输出完全符合预期"}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, true, false,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(repo.inserted))
	}
	if repo.inserted[0].TriggerReason != domain.TriggerProcessOutputConflict {
		t.Fatalf("trigger = %q, want process_output_conflict", repo.inserted[0].TriggerReason)
	}
	if repo.inserted[0].SourceType != domain.ReviewSourceCaseResult {
		t.Fatalf("source_type = %q, want case_result", repo.inserted[0].SourceType)
	}
}

// TestTryEscalateCaseResultTrajectoryFailedForcesInsert 覆盖会话容器级轨迹判负强制入池
// （spec 阶段 B §4.5 盲点）：规则 case 输出失败（outputPass=false）本无任何触发——但
// 轨迹 drifted 是「曾到达又推翻」的容器级负例，必须追加 trajectory_failed 入池，绝不
// 因逐轮信号为空而漏检。快照同时携带 trajectory 归因供评审员复核。
func TestTryEscalateCaseResultTrajectoryFailedForcesInsert(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	result := domain.EvalCaseResult{
		ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: false, ProcessPass: true,
		Trajectory: &domain.TrajectoryVerdict{Kind: domain.TrajectoryDrifted, Reason: "第 0 轮曾命中终态期望但末轮未守住（漂移）"},
	}
	c := domain.EvalCase{ID: "c1", AssertionMode: domain.AssertionContains}
	assertion := domain.AssertionResult{Passed: false, Confidence: 0.9}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, false, true,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 1 || repo.inserted[0].TriggerReason != domain.TriggerTrajectoryFailed {
		t.Fatalf("inserted = %+v, want single trajectory_failed item", repo.inserted)
	}
	snap, ok := repo.inserted[0].Snapshot.(map[string]any)
	if !ok {
		t.Fatalf("snapshot type = %T, want map[string]any", repo.inserted[0].Snapshot)
	}
	traj, ok := snap["trajectory"].(*domain.TrajectoryVerdict)
	if !ok || traj.Kind != domain.TrajectoryDrifted {
		t.Fatalf("snapshot trajectory = %v, want drifted verdict", snap["trajectory"])
	}
}

// TestTryEscalateCaseResultTrajectoryConvergedNoTrigger 覆盖轨迹非判负（converged）时
// trajectory_failed 不触发：通过即收敛，评审池不被噪声条目淹没。
func TestTryEscalateCaseResultTrajectoryConvergedNoTrigger(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	result := domain.EvalCaseResult{
		ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: true, ProcessPass: true,
		Trajectory: &domain.TrajectoryVerdict{Kind: domain.TrajectoryConverged, Reason: "末轮命中终态"},
	}
	c := domain.EvalCase{ID: "c1", AssertionMode: domain.AssertionContains}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.9}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, true, true,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 0 {
		t.Fatalf("inserted = %d, want 0 (converged must not trigger)", len(repo.inserted))
	}
}

// TestTryEscalateCaseResultSnapshotSanitized 覆盖评审池快照脱敏（安全红线，spec §6.5）：
// case_result 评审条目的 actual 与 tool_sequence 入快照前必须经 domain 脱敏，与
// eval_case_results 路径行为一致。敏感 Arguments 键（api_key/token/password）整体
// 替换为 [REDACTED]，RawText 中 `Authorization=Bearer <token>` 键值对被正则脱敏，
// actual 敏感键同样替换；普通字段原样保留，不得凭据明文落库/经 API 外泄。
func TestTryEscalateCaseResultSnapshotSanitized(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	result := domain.EvalCaseResult{
		ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: false, ProcessPass: false,
		ProcessFailure: "process:must_not_call:delete",
		Actual: map[string]any{
			"token":  "secret-token",
			"nested": map[string]any{"api_key": "secret-key"},
			"result": "ok",
		},
		Tools: []domain.ToolObservation{{
			ToolName:  "web_search",
			StepIndex: 1,
			Arguments: map[string]any{
				"query":    "stratum",
				"api_key":  "secret-key",
				"token":    "tok123",
				"password": "hunter2",
			},
			RawText: "web_search(query='stratum', api_key='secret-key', Authorization=Bearer tok123)",
		}},
	}
	c := domain.EvalCase{ID: "c1", Name: "搜索", AssertionMode: domain.AssertionJudge}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.9, Message: "输出完全符合预期"}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, true, false,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(repo.inserted))
	}
	snap, ok := repo.inserted[0].Snapshot.(map[string]any)
	if !ok {
		t.Fatalf("snapshot type = %T, want map[string]any", repo.inserted[0].Snapshot)
	}

	// actual：敏感键整体替换，普通键原样。
	actual, ok := snap["actual"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot actual type = %T", snap["actual"])
	}
	if actual["token"] != "[REDACTED]" {
		t.Fatalf("actual token = %v, want [REDACTED]", actual["token"])
	}
	nested, ok := actual["nested"].(map[string]any)
	if !ok || nested["api_key"] != "[REDACTED]" {
		t.Fatalf("actual nested = %#v, want api_key=[REDACTED]", actual["nested"])
	}
	if actual["result"] != "ok" {
		t.Fatalf("actual result = %v, want ok", actual["result"])
	}

	// tool_sequence：Arguments 敏感键替换，RawText 键值对正则脱敏。
	tools, ok := snap["tool_sequence"].([]domain.ToolObservation)
	if !ok || len(tools) != 1 {
		t.Fatalf("tool_sequence = %#v, want sanitized tool observations", snap["tool_sequence"])
	}
	tool := tools[0]
	for _, key := range []string{"api_key", "token", "password"} {
		if tool.Arguments[key] != "[REDACTED]" {
			t.Fatalf("tool arguments[%s] = %v, want [REDACTED]", key, tool.Arguments[key])
		}
	}
	if tool.Arguments["query"] != "stratum" {
		t.Fatalf("tool arguments[query] = %v, want stratum", tool.Arguments["query"])
	}
	if strings.Contains(tool.RawText, "tok123") || strings.Contains(tool.RawText, "secret-key") {
		t.Fatalf("tool raw_text leaked sensitive value: %q", tool.RawText)
	}
	if !strings.Contains(tool.RawText, "[REDACTED]") {
		t.Fatalf("tool raw_text must contain [REDACTED], got %q", tool.RawText)
	}
}

// TestCaseSnapshotIncludesProcessFields 覆盖评审池快照新字段（§6.5）：过程断言结果
// process_pass/process_failure 与工具序列 tool_sequence（ToolObservation 切片，与
// eval_case_results.tool_sequence 列同构）进入 case_result 评审条目快照，供人工
// 评审详情展示。
func TestCaseSnapshotIncludesProcessFields(t *testing.T) {
	result := domain.EvalCaseResult{
		ID: "cr-1", CaseID: "c1", Passed: false, Actual: "已删除",
		ProcessPass: false, ProcessFailure: "process:must_not_call:delete",
		Tools: []domain.ToolObservation{{ToolName: "search", StepIndex: 1}, {ToolName: "delete", StepIndex: 2}},
	}
	c := domain.EvalCase{
		ID: "c1", Name: "删除文件", Input: "删除", ExpectedOutput: "删除",
		AssertionMode: domain.AssertionContains,
	}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.9}
	snap := caseSnapshot(result, c, assertion)
	if snap["process_pass"] != false {
		t.Fatalf("process_pass = %v, want false", snap["process_pass"])
	}
	if snap["process_failure"] != "process:must_not_call:delete" {
		t.Fatalf("process_failure = %v, want process:must_not_call:delete", snap["process_failure"])
	}
	tools, ok := snap["tool_sequence"].([]domain.ToolObservation)
	if !ok || len(tools) != 2 || tools[1].ToolName != "delete" {
		t.Fatalf("tool_sequence = %#v, want tool observations slice", snap["tool_sequence"])
	}
}

func TestTryEscalatePropagatesRepoError(t *testing.T) {
	repo := &fakeReviewRepo{err: errors.New("db down")}
	svc := newTestReviewService(repo)
	obs := observationForTest()
	obs.Signals.Judge[0].Confidence = 0.3
	// fail-open：错误原样返回，主流程侧忽略（TryEscalate 不 panic 不吞错）。
	if err := svc.TryEscalateObservation(context.Background(), "t1", obs); err == nil {
		t.Fatal("want error propagated")
	}
}

func TestDecideStateMachine(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	obs := observationForTest()
	obs.Signals.Judge[0].Confidence = 0.3
	if err := svc.TryEscalateObservation(context.Background(), "t1", obs); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	id := repo.inserted[0].ID
	item, err := svc.Decide(context.Background(), "t1", id, "reviewer@x", domain.HumanVerdictFail, "实际输出与上下文冲突")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if item.Status != domain.ReviewStatusReviewed || item.HumanVerdict != domain.HumanVerdictFail {
		t.Fatalf("unexpected item: %+v", item)
	}
	if repo.marked[id] != domain.HumanVerdictFail {
		t.Fatalf("mark reviewed not recorded: %+v", repo.marked)
	}
}

func TestDecideRejectsInvalidVerdict(t *testing.T) {
	svc := newTestReviewService(&fakeReviewRepo{})
	_, err := svc.Decide(context.Background(), "t1", "ri-x", "reviewer@x", domain.HumanVerdict("bogus"), "")
	if err == nil {
		t.Fatal("want error for invalid verdict")
	}
}

// TestDecidePromoteSkippedWhenSuitesNil 覆盖 promote 分支：judge_misjudgment
// 时经 suites.AddDraftCases 沉淀（Suites/Evidence 为 nil 时跳过 promote，仅落
// calibration sample）。
func TestDecidePromoteSkippedWhenSuitesNil(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	item := domain.ReviewItem{
		ID: uuid.NewString(), SourceType: domain.ReviewSourceObservation,
		SourceID: "obs-1", ResourceKind: domain.ResourceKindSkill, ResourceID: "s1",
		TriggerReason: domain.TriggerLowConfidence, Status: domain.ReviewStatusPending,
		Snapshot: map[string]any{"signals": observationForTest().Signals},
	}
	repo.inserted = append(repo.inserted, item)
	_, err := svc.Decide(context.Background(), "t1", item.ID, "reviewer@x",
		domain.HumanVerdictJudgeMisjudgment, "judge 判错")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if repo.marked[item.ID] != domain.HumanVerdictJudgeMisjudgment {
		t.Fatalf("not marked: %+v", repo.marked)
	}
}

// TestDecidePromotesObservationToDraftSuite 覆盖 promote 成功路径（observation
// 来源）：judge_misjudgment 决策后经 TraceEvidenceReader 解析 trace 构造
// judge-mode 草稿 case 落专用评测集 review-promote-<tenant>（spec §9）。
func TestDecidePromotesObservationToDraftSuite(t *testing.T) {
	repo := &fakeReviewRepo{}
	suites := &promoteSuiteRepo{fakeSuiteRepo: &fakeSuiteRepo{
		revision: domain.EvalSuiteRevision{
			ID: "rev-1", SuiteID: "review-promote-t1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
		},
	}}
	evidence := &stubTraceEvidence{trace: port.ObservedTrace{Input: "快递没更新", Output: "客服已回复"}}
	svc := newTestReviewServiceWithPromote(repo, suites, evidence)
	item := domain.ReviewItem{
		ID: uuid.NewString(), SourceType: domain.ReviewSourceObservation,
		SourceID: "obs-1", TraceID: "t-1", ResourceKind: domain.ResourceKindSkill, ResourceID: "s1",
		TriggerReason: domain.TriggerLowConfidence, Status: domain.ReviewStatusPending,
		Snapshot: map[string]any{"signals": observationForTest().Signals},
	}
	repo.inserted = append(repo.inserted, item)

	if _, err := svc.Decide(context.Background(), "t1", item.ID, "reviewer@x",
		domain.HumanVerdictJudgeMisjudgment, "judge 判错"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if repo.marked[item.ID] != domain.HumanVerdictJudgeMisjudgment {
		t.Fatalf("not marked: %+v", repo.marked)
	}
	if len(suites.added) != 1 {
		t.Fatalf("promoted cases = %d, want 1", len(suites.added))
	}
	c := suites.added[0]
	if c.Name != "review-observation-obs-1" || c.Input != "快递没更新" {
		t.Fatalf("case name/input mismatch: %+v", c)
	}
	if c.AssertionMode != domain.AssertionJudge || c.Enabled {
		t.Fatalf("case must be judge-mode and disabled in draft: %+v", c)
	}
	if suites.addedRevisionID != "rev-1" {
		t.Fatalf("revision = %q, want rev-1", suites.addedRevisionID)
	}
}

// TestDecidePromotesObservationCreatesSuiteLazily 覆盖 promote 目标套件
// review-promote-<tenant> 不存在时 draftRevisionForSuite 的惰性创建分支：
// CreateSuite 后再 GetDraftRevision 取回新草稿 revision 落 case。
func TestDecidePromotesObservationCreatesSuiteLazily(t *testing.T) {
	repo := &fakeReviewRepo{}
	suites := &promoteSuiteRepo{fakeSuiteRepo: &fakeSuiteRepo{}}
	svc := newTestReviewServiceWithPromote(repo, suites, &stubTraceEvidence{
		trace: port.ObservedTrace{Input: "订单被取消", Output: "已退款"},
	})
	item := domain.ReviewItem{
		ID: uuid.NewString(), SourceType: domain.ReviewSourceObservation,
		SourceID: "obs-2", TraceID: "t-2", ResourceKind: domain.ResourceKindSkill, ResourceID: "s1",
		TriggerReason: domain.TriggerLowConfidence, Status: domain.ReviewStatusPending,
		Snapshot: map[string]any{"signals": observationForTest().Signals},
	}
	repo.inserted = append(repo.inserted, item)

	if _, err := svc.Decide(context.Background(), "t1", item.ID, "reviewer@x",
		domain.HumanVerdictJudgeMisjudgment, "judge 判错"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if suites.suite.ID != "review-promote-t1" || suites.suite.Name != "Review Promote Pool" {
		t.Fatalf("suite not lazily created: %+v", suites.suite)
	}
	if len(suites.added) != 1 || suites.added[0].Name != "review-observation-obs-2" {
		t.Fatalf("promoted cases = %+v, want observation-obs-2", suites.added)
	}
	if suites.addedRevisionID == "" {
		t.Fatal("revision id empty, promote must write into a real draft revision")
	}
}

// TestDecidePromotesCaseResultToDraftSuite 覆盖 promote 成功路径（case_result
// 来源）：从评审条目快照平铺字段重建 judge-mode 草稿 case（rebuiltCase）落
// 专用评测集。
func TestDecidePromotesCaseResultToDraftSuite(t *testing.T) {
	repo := &fakeReviewRepo{}
	suites := &promoteSuiteRepo{fakeSuiteRepo: &fakeSuiteRepo{
		revision: domain.EvalSuiteRevision{
			ID: "rev-1", SuiteID: "review-promote-t1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
		},
	}}
	svc := newTestReviewServiceWithPromote(repo, suites, nil)
	item := domain.ReviewItem{
		ID: uuid.NewString(), SourceType: domain.ReviewSourceCaseResult,
		SourceID: "cr-1", ResourceKind: domain.ResourceKindSkill, ResourceID: "s1",
		TriggerReason: domain.TriggerNeedsReview, Status: domain.ReviewStatusPending,
		Snapshot: map[string]any{
			"case_name": "物流分类", "input": "快递没更新", "expected": "物流",
			"signals": observationForTest().Signals,
		},
	}
	repo.inserted = append(repo.inserted, item)

	if _, err := svc.Decide(context.Background(), "t1", item.ID, "reviewer@x",
		domain.HumanVerdictJudgeMisjudgment, "judge 判错"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if len(suites.added) != 1 {
		t.Fatalf("promoted cases = %d, want 1", len(suites.added))
	}
	c := suites.added[0]
	if c.Name != "物流分类" || c.Input != "快递没更新" || c.ExpectedOutput != "物流" {
		t.Fatalf("rebuilt case mismatch: %+v", c)
	}
	if c.AssertionMode != domain.AssertionJudge || c.Enabled {
		t.Fatalf("case must be judge-mode and disabled in draft: %+v", c)
	}
}

// TestDecideConcurrentRaceReturnsLatest 验证对 PgReviewRepository.MarkReviewed
// 普通错误签名（非 sentinel）的降级：条目已被另一评审者置为 reviewed 时返回最新
// 条目（幂等/并发竞态语义），而非报错。
func TestDecideConcurrentRaceReturnsLatest(t *testing.T) {
	base := &fakeReviewRepo{}
	repo := &raceReviewRepo{fakeReviewRepo: base}
	svc := newTestReviewService(repo)
	item := domain.ReviewItem{
		ID: uuid.NewString(), SourceType: domain.ReviewSourceObservation,
		SourceID: "obs-1", ResourceKind: domain.ResourceKindSkill, ResourceID: "s1",
		TriggerReason: domain.TriggerLowConfidence, Status: domain.ReviewStatusPending,
		Snapshot: map[string]any{"signals": observationForTest().Signals},
	}
	base.inserted = append(base.inserted, item)
	got, err := svc.Decide(context.Background(), "t1", item.ID, "reviewer@x", domain.HumanVerdictFail, "并发竞态")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got.Status != domain.ReviewStatusReviewed {
		t.Fatalf("status = %s, want reviewed", got.Status)
	}
	if got.HumanVerdict != domain.HumanVerdictPass {
		t.Fatalf("verdict = %s, want pass（另一评审者结论）", got.HumanVerdict)
	}
}

// TestDecideMarkReviewedErrorPropagated 验证 MarkReviewed 返回错误且条目仍为
// pending（非并发竞态）时，错误原样返回。
func TestDecideMarkReviewedErrorPropagated(t *testing.T) {
	repo := &fakeReviewRepo{err: errors.New("db down")}
	svc := newTestReviewService(repo)
	item := domain.ReviewItem{
		ID: uuid.NewString(), SourceType: domain.ReviewSourceObservation,
		SourceID: "obs-1", ResourceKind: domain.ResourceKindSkill, ResourceID: "s1",
		TriggerReason: domain.TriggerLowConfidence, Status: domain.ReviewStatusPending,
	}
	repo.inserted = append(repo.inserted, item)
	if _, err := svc.Decide(context.Background(), "t1", item.ID, "reviewer@x", domain.HumanVerdictFail, "x"); err == nil {
		t.Fatal("want error propagated")
	}
}

// TestDecideIdempotentAlreadyReviewed 验证对已 reviewed 条目重复决策直接返回
// 现状，不重复写副作用（repo.marked 保持 nil 表示 MarkReviewed 未被再次调用）。
func TestDecideIdempotentAlreadyReviewed(t *testing.T) {
	repo := &fakeReviewRepo{}
	svc := newTestReviewService(repo)
	item := domain.ReviewItem{
		ID: uuid.NewString(), SourceType: domain.ReviewSourceObservation,
		SourceID: "obs-1", ResourceKind: domain.ResourceKindSkill, ResourceID: "s1",
		TriggerReason: domain.TriggerLowConfidence, Status: domain.ReviewStatusReviewed,
		HumanVerdict: domain.HumanVerdictPass,
	}
	repo.inserted = append(repo.inserted, item)
	got, err := svc.Decide(context.Background(), "t1", item.ID, "another@x", domain.HumanVerdictFail, "重复")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got.Status != domain.ReviewStatusReviewed || got.HumanVerdict != domain.HumanVerdictPass {
		t.Fatalf("unexpected item: %+v", got)
	}
	if repo.marked != nil {
		t.Fatalf("must not re-mark: %+v", repo.marked)
	}
}

func TestGetNotFound(t *testing.T) {
	svc := newTestReviewService(&fakeReviewRepo{})
	if _, err := svc.Get(context.Background(), "t1", "nope"); !errors.Is(err, ErrReviewItemNotFound) {
		t.Fatalf("err = %v, want ErrReviewItemNotFound", err)
	}
}

func TestListDelegates(t *testing.T) {
	repo := &fakeReviewRepo{}
	repo.inserted = []domain.ReviewItem{{ID: "i1"}}
	svc := newTestReviewService(repo)
	items, total, err := svc.List(context.Background(), "t1", port.ReviewFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("unexpected: items=%d total=%d", len(items), total)
	}
}

func TestRefreshBacklog(t *testing.T) {
	svc := newTestReviewService(&fakeReviewRepo{})
	if err := svc.RefreshBacklog(context.Background(), "t1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

func TestRefreshBacklogPropagatesError(t *testing.T) {
	svc := newTestReviewService(&fakeReviewRepo{err: errors.New("db down")})
	if err := svc.RefreshBacklog(context.Background(), "t1"); err == nil {
		t.Fatal("want error propagated")
	}
}

func TestRefreshBacklogSumsAcrossTenants(t *testing.T) {
	repo := &perTenantPendingRepo{
		fakeReviewRepo: &fakeReviewRepo{},
		pending:        map[string]int64{"t1": 3, "t2": 5, "t3": 2},
	}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithTenants(repo, metrics, []string{"t2", "t3"})
	if err := svc.RefreshBacklog(context.Background(), "t1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(metrics.backlog) != 1 || metrics.backlog[0] != 10 {
		t.Fatalf("backlog = %v, want [10] (3+5+2 跨租户求和)", metrics.backlog)
	}
}

func TestRefreshBacklogCurrentTenantErrorPropagates(t *testing.T) {
	repo := &perTenantPendingRepo{
		fakeReviewRepo: &fakeReviewRepo{},
		pending:        map[string]int64{"t1": 3},
		errOn:          map[string]error{"t1": errors.New("db down")},
	}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithTenants(repo, metrics, []string{"t2", "t3"})
	if err := svc.RefreshBacklog(context.Background(), "t1"); err == nil {
		t.Fatal("want current-tenant error propagated")
	}
	if len(metrics.backlog) != 0 {
		t.Fatalf("backlog = %v, want no Set on current-tenant error", metrics.backlog)
	}
}

func TestRefreshBacklogSkipsOtherTenantFailure(t *testing.T) {
	repo := &perTenantPendingRepo{
		fakeReviewRepo: &fakeReviewRepo{},
		pending:        map[string]int64{"t1": 3, "t2": 2},
		errOn:          map[string]error{"t2": errors.New("no schema")},
	}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithTenants(repo, metrics, []string{"t2"})
	if err := svc.RefreshBacklog(context.Background(), "t1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(metrics.backlog) != 1 || metrics.backlog[0] != 3 {
		t.Fatalf("backlog = %v, want [3] (t2 失败跳过计 0)", metrics.backlog)
	}
}

func TestRefreshBacklogDeduplicatesCurrentTenant(t *testing.T) {
	repo := &perTenantPendingRepo{
		fakeReviewRepo: &fakeReviewRepo{},
		pending:        map[string]int64{"t1": 3, "t2": 2},
	}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithTenants(repo, metrics, []string{"t1", "t2"})
	if err := svc.RefreshBacklog(context.Background(), "t1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(metrics.backlog) != 1 || metrics.backlog[0] != 5 {
		t.Fatalf("backlog = %v, want [5] (t1 不重复 count)", metrics.backlog)
	}
}

func TestEscalateObservationRefreshesBacklog(t *testing.T) {
	repo := &fakeReviewRepo{}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithMetrics(repo, metrics)
	obs := observationForTest()
	obs.Signals.Judge[0].Confidence = 0.3
	if err := svc.TryEscalateObservation(context.Background(), "t1", obs); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(metrics.backlog) != 1 || metrics.backlog[0] != 1 {
		t.Fatalf("backlog = %v, want [1]", metrics.backlog)
	}
}

func TestEscalateCaseResultRefreshesBacklog(t *testing.T) {
	repo := &fakeReviewRepo{}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithMetrics(repo, metrics)
	result := domain.EvalCaseResult{ID: "cr-1", CaseID: "c1", TraceID: "t-1", Passed: true, ProcessPass: true}
	c := domain.EvalCase{ID: "c1", NeedsReview: true, AssertionMode: domain.AssertionJudge}
	assertion := domain.AssertionResult{Passed: true, Confidence: 0.9, Message: "输出完全符合预期"}
	if err := svc.TryEscalateCaseResult(
		context.Background(), "t1", "run-1",
		domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"}, result, c, assertion, true, true,
	); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(metrics.backlog) != 1 || metrics.backlog[0] != 1 {
		t.Fatalf("backlog = %v, want [1]", metrics.backlog)
	}
}

func TestDecideRefreshesBacklog(t *testing.T) {
	repo := &fakeReviewRepo{inserted: []domain.ReviewItem{{ID: "i1", Status: domain.ReviewStatusPending}}}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithMetrics(repo, metrics)
	if _, err := svc.Decide(context.Background(), "t1", "i1", "admin", domain.HumanVerdictFail, "bad"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if len(metrics.backlog) != 1 {
		t.Fatalf("backlog calls = %v, want 1 refresh after decision", metrics.backlog)
	}
}

func TestEscalateBacklogRefreshFailureIsFailOpen(t *testing.T) {
	repo := &countPendingErrRepo{fakeReviewRepo: &fakeReviewRepo{}}
	svc := newTestReviewService(repo)
	obs := observationForTest()
	obs.Signals.Judge[0].Confidence = 0.3
	// 积压刷新失败不得阻断升级主流程（fail-open）。
	if err := svc.TryEscalateObservation(context.Background(), "t1", obs); err != nil {
		t.Fatalf("escalate should succeed despite backlog refresh failure: %v", err)
	}
}

func TestEscalateNoTriggerSkipsBacklogRefresh(t *testing.T) {
	repo := &fakeReviewRepo{}
	metrics := &stubReviewMetrics{}
	svc := newTestReviewServiceWithMetrics(repo, metrics)
	if err := svc.TryEscalateObservation(context.Background(), "t1", observationForTest()); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(metrics.backlog) != 0 {
		t.Fatalf("backlog calls = %v, want none", metrics.backlog)
	}
}

// TestDecideNilMetricsDoesNotPanic 覆盖构造时未注入 Metrics（如 contract test /
// record-contracts wiring）的场景：Decide 触发的积压刷新不得因 nil Metrics 空指针
// panic，否则契约测试 500。回归 P1c 积压指标修复引入的缺陷。
func TestDecideNilMetricsDoesNotPanic(t *testing.T) {
	repo := &fakeReviewRepo{
		inserted: []domain.ReviewItem{{ID: "i1", Status: domain.ReviewStatusPending}},
	}
	svc := NewReviewService(ReviewServiceDeps{
		Repo:   repo,
		Logger: zap.NewNop(),
		// 刻意不注入 Metrics，复现 contract_test.go / record-contracts.go 的装配。
	})
	got, err := svc.Decide(context.Background(), "t1", "i1", "admin", domain.HumanVerdictFail, "bad")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got.Status != domain.ReviewStatusReviewed || got.HumanVerdict != domain.HumanVerdictFail {
		t.Fatalf("unexpected item: %+v", got)
	}
}
