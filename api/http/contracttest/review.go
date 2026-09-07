package contracttest

import (
	"context"
	"time"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

type contractQueryRepo struct{}

func (contractQueryRepo) Overview(context.Context, string) (domain.CenterOverview, error) {
	return domain.CenterOverview{}, nil
}
func (contractQueryRepo) ListResources(context.Context, string, port.CenterFilter) (domain.ResourcePage, error) {
	return domain.ResourcePage{Items: []domain.ResourceSummary{}}, nil
}
func (contractQueryRepo) ListSuites(context.Context, string, port.CenterFilter) (domain.SuitePage, error) {
	return domain.SuitePage{Items: []domain.SuiteSummary{}}, nil
}
func (contractQueryRepo) ListRuns(context.Context, string, port.CenterFilter) (domain.RunPage, error) {
	return domain.RunPage{Items: []domain.RunSummary{}}, nil
}
func (contractQueryRepo) ListCandidates(context.Context, string, port.CenterFilter) (domain.CandidatePage, error) {
	return domain.CandidatePage{Items: []domain.CandidateSummary{}}, nil
}
func (contractQueryRepo) ListExperiments(context.Context, string, port.CenterFilter) (domain.ExperimentPage, error) {
	return domain.ExperimentPage{Items: []domain.ExperimentSummary{}}, nil
}
func (contractQueryRepo) Timeline(context.Context, string, port.CenterFilter) (domain.TimelinePage, error) {
	return domain.TimelinePage{Items: []domain.TimelineEvent{}}, nil
}
func (contractQueryRepo) ListRevisions(context.Context, string, port.CenterFilter) (domain.RevisionPage, error) {
	return domain.RevisionPage{Items: []domain.RevisionSummary{
		{
			ID: "rev-1", ResourceKind: domain.ResourceKindSkill, ResourceID: "resource-1",
			Source: string(domain.RevisionSourceManual), Status: "published",
			SafeSummary: map[string]any{"version_label": "v1"}, CreatedBy: "user-1",
			CreatedAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC),
		},
		{
			ID: "rev-2", ResourceKind: domain.ResourceKindSkill, ResourceID: "resource-1",
			ParentRevisionID: "rev-1", Source: string(domain.RevisionSourceOptimization), Status: "draft",
			SafeSummary: map[string]any{"version_label": "v2"}, CreatedBy: "user-2",
			CreatedAt: time.Date(2026, 9, 3, 9, 30, 0, 0, time.UTC),
		},
	}}, nil
}

func (contractQueryRepo) RevisionReferences(context.Context, string, domain.ResourceRef) (domain.RevisionReferences, error) {
	pr := 1
	return domain.RevisionReferences{
		Deployment: &domain.RevisionDeployment{Role: "stable", StableRevisionID: "rev-1", CanaryPercent: 0},
		SubjectRuns: []domain.RunSummary{{
			ID: "run-subj-1", ResourceID: "resource-1", RevisionID: "rev-1", Status: "succeeded",
			ResourceKind: domain.ResourceKindSkill, Passed: true, TotalCases: 12, PassedCases: 11,
			CreatedBy: "user-1", CreatedAt: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
		}},
		PinnedRuns: []domain.RevisionPinnedRun{{
			RunID: "run-pin-1", ResourceKind: domain.ResourceKindAgent, ResourceID: "agent-1",
			Status: "succeeded", Passed: true, TotalCases: 5, PassedCases: 5,
			CreatedAt: time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC),
		}},
		Candidates: []domain.RevisionCandidateRef{{
			ID: "cand-1", RevisionID: "rev-9", ParentRevisionID: "rev-1", Role: "baseline",
			Source: string(domain.RevisionSourceOptimization), Status: "proposed", Rank: &pr,
			CreatedAt: time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC),
		}},
		Experiments: []domain.RevisionExperimentRef{{
			ID: "exp-1", Role: "stable", StableRevisionID: "rev-1", CanaryRevisionID: "rev-5",
			Status: "running", StagePercent: 10, Recommendation: "hold",
			CreatedAt: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC),
		}},
	}, nil
}

func (contractQueryRepo) RevisionPassRate(context.Context, string, domain.ResourceRef) (domain.RevisionPassRate, error) {
	rate := 0.9166666666666666
	return domain.RevisionPassRate{
		SucceededRuns: 1, TotalRuns: 2, PassedCases: 11, TotalCases: 12, PassRate: &rate,
		RecentRuns: []domain.RunSummary{
			{
				ID: "run-subj-1", ResourceID: "resource-1", RevisionID: "rev-1", Status: "succeeded",
				ResourceKind: domain.ResourceKindSkill, Passed: true, TotalCases: 12, PassedCases: 11,
				CreatedBy: "user-1", CreatedAt: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
			},
			{
				ID: "run-fail-1", ResourceID: "resource-1", RevisionID: "rev-1", Status: "failed",
				ResourceKind: domain.ResourceKindSkill, Passed: false, TotalCases: 12, PassedCases: 0,
				CreatedBy: "user-3", CreatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
			},
		},
	}, nil
}

func (contractQueryRepo) MonitorResources(context.Context, string, port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	return domain.MonitorResourcesPage{
		Items: []domain.MonitorResourceSummary{
			{
				ResourceKind: domain.ResourceKindSkill, ResourceID: "resource-1", SampleCount: 128,
				Quality:  []domain.QualityDim{{Dimension: "faithfulness", PassRate: 0.92, AvgScore: 0.92, AvgConfidence: 0.87, Samples: 128}},
				Behavior: domain.BehaviorStats{RuleHits: 15, RetryCount: 3, EscalationCount: 1, Verdict: domain.VerdictDistribution{Pass: 120, Flag: 6, Block: 2}},
				Cost:     costPtr(154000, 0.42, 1800, 5200),
				Process: &domain.ProcessBaseline{ProcessPassRate: 0.67, RunID: "run-9",
					RunCreatedAt: time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)},
			},
			{
				// 窗口内无 succeeded run 的资源行：process=null，且 latency 无有效样本 → null。
				ResourceKind: domain.ResourceKindAgent, ResourceID: "agent-2", SampleCount: 64,
				Quality: []domain.QualityDim{
					{Dimension: "relevance", PassRate: 0.9, AvgScore: 0.9, AvgConfidence: 0.8, Samples: 64},
					{Dimension: "helpfulness", PassRate: 0.83, AvgScore: 0.83, AvgConfidence: 0.76, Samples: 64},
				},
				Behavior: domain.BehaviorStats{RuleHits: 4, AbandonmentCount: 1,
					Verdict: domain.VerdictDistribution{Pass: 60, Flag: 4}},
				Cost: domain.CostStats{TotalTokens: 82000, TotalCostUSD: 0.21},
			},
		},
		Window: domain.MonitorWindow{From: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)},
	}, nil
}

func (contractQueryRepo) MonitorTrend(context.Context, string, port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	return domain.MonitorTrendSeries{
		ResourceKind: domain.ResourceKindSkill, ResourceID: "resource-1",
		Series: []domain.MonitorTrendPoint{
			{
				BucketAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), SampleCount: 12,
				Quality: []domain.QualityDim{
					{Dimension: "relevance", PassRate: 0.83, AvgScore: 0.83, AvgConfidence: 0.75, Samples: 12},
					{Dimension: "faithfulness", PassRate: 1, AvgScore: 1, AvgConfidence: 0.9, Samples: 12},
				},
				Behavior: domain.BehaviorStats{Verdict: domain.VerdictDistribution{Pass: 10, Flag: 2}},
				Cost:     domain.CostStats{TotalTokens: 15000, TotalCostUSD: 0.04},
			},
			{
				BucketAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), SampleCount: 20,
				Quality:  []domain.QualityDim{{Dimension: "relevance", PassRate: 0.9, AvgScore: 0.9, AvgConfidence: 0.8, Samples: 20}},
				Behavior: domain.BehaviorStats{RuleHits: 2, Verdict: domain.VerdictDistribution{Pass: 19, Flag: 1}},
				Cost:     costPtr(24000, 0.06, 1600, 4100),
			},
		},
		Runs: []domain.RunProcessPoint{{RunID: "run-9", ProcessPassRate: 0.67, RunCreatedAt: time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)}},
	}, nil
}

// costPtr 便捷构造（nil-safe 延迟指针）。放 contracttest 包级。
func costPtr(tokens int64, costUSD, avg, p95 float64) domain.CostStats {
	return domain.CostStats{TotalTokens: tokens, TotalCostUSD: costUSD, AvgLatencyMS: &avg, P95LatencyMS: &p95}
}

type contractExperimentRepo struct{}

func (contractExperimentRepo) ValidatePrerequisites(context.Context, string, domain.ResourceRef,
	domain.ResourceRef, string) error {
	return nil
}
func (contractExperimentRepo) Create(context.Context, string, domain.Experiment, domain.Deployment, *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractExperimentRepo) Get(context.Context, string, string) (domain.Experiment, bool, error) {
	return domain.Experiment{}, false, nil
}
func (contractExperimentRepo) SaveDecision(context.Context, string, domain.Experiment, domain.Decision, domain.StageMetrics, string, string) (domain.Experiment, domain.Decision, error) {
	return domain.Experiment{}, domain.DecisionHold, nil
}
func (contractExperimentRepo) ApplyCommand(context.Context, string, string, domain.ExperimentCommandAction, domain.ExperimentCommand) (domain.Experiment, error) {
	return domain.Experiment{}, domain.ErrExperimentStateConflict
}
func (contractExperimentRepo) ResolveDeployment(context.Context, string, string, string) (domain.Deployment, bool, error) {
	return domain.Deployment{}, false, nil
}
func (contractExperimentRepo) HasRunningExperiment(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (contractExperimentRepo) ListPendingExperiments(context.Context, string, string, string) ([]domain.Experiment, error) {
	return nil, nil
}
func (contractExperimentRepo) ListRunningExperiments(context.Context, string) ([]domain.Experiment, error) {
	return nil, nil
}

type contractCandidateRepo struct{}

func (contractCandidateRepo) Reject(context.Context, string, string, domain.CandidateCommand) (domain.CandidateSummary, error) {
	return domain.CandidateSummary{}, domain.ErrCandidateCommandConflict
}

// contractObservationRepo 为运行态观测查询 API 提供确定性单条/分页响应
// （P1a；golden 文件与此 stub 的返回一一对应）。
type contractObservationRepo struct{}

func (contractObservationRepo) Save(_ context.Context, _ string, _ *domain.EvalObservation) error {
	return nil
}

func (contractObservationRepo) Get(_ context.Context, _, _ string) (*domain.EvalObservation, error) {
	return &domain.EvalObservation{
		ID: "obs-1", TraceID: "trace-1",
		Resource:  domain.ObservationResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"},
		Verdict:   domain.VerdictPass,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (contractObservationRepo) QueryByResource(_ context.Context, _, _, _ string,
	_, _ *time.Time, _, _ int,
) ([]domain.EvalObservation, error) {
	return []domain.EvalObservation{{
		ID: "obs-1", TraceID: "trace-1",
		Resource:  domain.ObservationResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-1"},
		Verdict:   domain.VerdictPass,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}}, nil
}

func (contractObservationRepo) FindLatestByTrace(_ context.Context, _, _ string) (*domain.EvalObservation, error) {
	return nil, nil
}

func (contractObservationRepo) UpdateBehaviorSignals(_ context.Context, _, _ string, _ domain.BehaviorSignals) error {
	return nil
}

// reviewItem 是评审池 golden 的确定性条目：pending 状态使 Decide 走真实
// pending → reviewed 转换（reviewed_at 为 live 时间戳，由 WantBodyRE 容错）。
func reviewItem() *domain.ReviewItem {
	return &domain.ReviewItem{
		ID:            "review-1",
		SourceType:    domain.ReviewSourceObservation,
		SourceID:      "obs-1",
		RunID:         "run-1",
		TraceID:       "trace-1",
		ResourceKind:  domain.ResourceKindAgent,
		ResourceID:    "agent-1",
		TriggerReason: domain.TriggerLowConfidence,
		RiskLevel:     domain.ReviewRiskMedium,
		Snapshot:      map[string]any{"signals": map[string]any{"judge": []any{}}},
		Status:        domain.ReviewStatusPending,
		CreatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// contractReviewRepo 为评审池查询/决策 API 提供确定性响应（P1c；golden 文件与
// 此 stub 的返回一一对应）。
type contractReviewRepo struct{}

func (contractReviewRepo) UpsertItem(_ context.Context, _ string, _ *domain.ReviewItem) (bool, error) {
	return true, nil
}

func (contractReviewRepo) GetItem(_ context.Context, _, _ string) (*domain.ReviewItem, error) {
	return reviewItem(), nil
}

func (contractReviewRepo) ListItems(_ context.Context, _ string, _ port.ReviewFilter,
) ([]domain.ReviewItem, int64, error) {
	return []domain.ReviewItem{*reviewItem()}, 1, nil
}

func (contractReviewRepo) MarkReviewed(_ context.Context, _, _ string, _ domain.HumanVerdict, _, _ string) error {
	return nil
}

func (contractReviewRepo) CreateCalibrationSample(_ context.Context, _ string, _ *domain.CalibrationSample) error {
	return nil
}

func (contractReviewRepo) CreateAttributionEntry(_ context.Context, _ string, _ *domain.AttributionEntry) error {
	return nil
}

func (contractReviewRepo) CountPending(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
