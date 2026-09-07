package port

import (
	"context"
	"errors"
	"time"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

var ErrRevisionCommitUnknown = errors.New("revision metadata commit outcome unknown")
var ErrCenterResourceNotFound = errors.New("evaluation center resource not found")

type CenterFilter struct {
	ResourceKind, ResourceID, RevisionID, Status, Cursor string
	Limit                                                int
}

// MonitorFilter 评测监控聚合查询过滤（窗口必填由 application 兜底近 7 天）。
type MonitorFilter struct {
	ResourceKind, ResourceID string
	From, To                 *time.Time
	Limit                    int
}

type CenterQueryRepository interface {
	Overview(context.Context, string) (domain.CenterOverview, error)
	ListResources(context.Context, string, CenterFilter) (domain.ResourcePage, error)
	ListSuites(context.Context, string, CenterFilter) (domain.SuitePage, error)
	ListRuns(context.Context, string, CenterFilter) (domain.RunPage, error)
	ListCandidates(context.Context, string, CenterFilter) (domain.CandidatePage, error)
	ListExperiments(context.Context, string, CenterFilter) (domain.ExperimentPage, error)
	Timeline(context.Context, string, CenterFilter) (domain.TimelinePage, error)
	MonitorResources(context.Context, string, MonitorFilter) (domain.MonitorResourcesPage, error)
	MonitorTrend(context.Context, string, MonitorFilter) (domain.MonitorTrendSeries, error)
	// ListRevisions (0) 返回被测资源在 resource_revisions 中的 eval 版本表（含
	// 零引用版本）。资源未建档时返回 ErrCenterResourceNotFound。
	ListRevisions(context.Context, string, CenterFilter) (domain.RevisionPage, error)
	// RevisionReferences (c) 返回单 eval 版本的引用方账本：deployment 角色 +
	// 主体 run + 把它当绑定资源 pin 的其它 run + 候选/基线 + 实验臂。版本不属于
	// 该资源时返回 ErrCenterResourceNotFound。
	RevisionReferences(context.Context, string, domain.ResourceRef) (domain.RevisionReferences, error)
	// RevisionPassRate (d) 返回单 eval 版本通过率摘要（成功/总 run、用例聚合、
	// 最近 run）。版本不属于该资源时返回 ErrCenterResourceNotFound。
	RevisionPassRate(context.Context, string, domain.ResourceRef) (domain.RevisionPassRate, error)
}

type ExecutionResult struct {
	Output     any
	TraceID    string
	Tokens     int
	CostUSD    float64
	DurationMs int
	// RAGEvidence carries per-case retrieval metrics for knowledge
	// evaluations; nil for other resource kinds.
	RAGEvidence *domain.RAGEvidenceInfo
	// Tools 是执行链路工具调用序列（§6.5 过程断言数据源；无工具调用/未采集时为空）。
	Tools []ToolObservation
}

// LLMJudge evaluates free-form outputs with an LLM for assertion_mode=judge
// cases. Enabled reports the runtime switch (evaluation.judge.enabled
// platform parameter) so the application can fail closed when the judge is
// off. Wiring adapters implement it over llmgateway's LLMCompleter.
type LLMJudge interface {
	Enabled(ctx context.Context) bool
	Judge(ctx context.Context, req JudgeRequest) (domain.AssertionResult, error)
}

// JudgeRequest carries the material a judge needs. Empty Model/Rubric mean
// "use platform defaults / the registered global rubric".
type JudgeRequest struct {
	Model          string
	Rubric         string
	Input          string
	ExpectedOutput string
	Actual         string
	// ToolSequence 是执行链路工具调用序列文本（§6.5 step_judge 输入）；
	// 空 = 无需步骤级评分。
	ToolSequence string
	// Transcript 是会话剧本逐轮证据的纯文本渲染（阶段 B §4.3 judge 会话调用形态：
	// 判「末轮是否到达目标/守住探针」）；空 = 非会话（单轮）case 无需 transcript。
	Transcript string
}

type ResourceAdapter interface {
	ExecuteRevision(
		ctx context.Context, tenantID, requestedBy string, ref domain.ResourceRef, testCase domain.EvalCase,
	) (ExecutionResult, error)
	ResolveRevision(context.Context, string, domain.ResourceRef) (domain.ResourceRevision, error)
	SafeSummary(context.Context, string, domain.ResourceRef) (map[string]any, error)
}

// SessionRunner 是可选能力接口：会话剧本 case（阶段 B §5.4）的 ResourceAdapter
// 之上的能力接口。runCase 会话分支对 adapter 做类型断言分派；adapter 未实现
// （单轮知识检索等）时 fail-close 报错，绝不静默退化为单轮执行。
type SessionRunner interface {
	RunSession(
		ctx context.Context, tenantID, requestedBy string, ref domain.ResourceRef,
		script domain.EvalSessionScript,
	) ([]domain.SessionTurnEvidence, error)
}

type RunRepository interface {
	SaveRun(ctx context.Context, tenantID string, run domain.EvalRun) error
	GetRun(ctx context.Context, tenantID, runID string) (domain.EvalRun, bool, error)
	// FindLatestCompletedRunForResource 返回该 resource（kind+id）+ suite revision 最近一条
	// 已完成（status='succeeded'）run；无 → (nil, nil)。供 run 级回归对照与发布哨兵定位基线
	// run（T8 定义、T12 消费）。
	FindLatestCompletedRunForResource(
		ctx context.Context, tenantID string, ref domain.ResourceRef, suiteRevisionID string,
	) (*domain.EvalRun, error)
	// FindLatestCompletedRunForPlatformSeq 返回 tenant 下最近一条 completed run，其
	// context_snapshot 中 groupKey 组 version_seq == seq（在指定平台配置版本下执行的最近
	// run）；无 → (nil, nil)。多租户回滚验证与发布哨兵共用（spec §3.4-3）。
	FindLatestCompletedRunForPlatformSeq(
		ctx context.Context, tenantID, groupKey string, seq int64,
	) (*domain.EvalRun, error)
}

type SuiteRepository interface {
	CreateSuite(ctx context.Context, tenantID string, suite domain.EvalSuite, revision domain.EvalSuiteRevision) error
	// GetSuite 返回套件自身元信息（含 created_at 与 active/draft revision
	// 指针）；套件不存在时 found=false。
	GetSuite(ctx context.Context, tenantID, suiteID string) (domain.EvalSuite, bool, error)
	// ListSuiteRevisions 返回套件全部 revision（含当前草稿与历史已发布版本）的
	// 轻量 meta，不装载 cases；版本号降序、草稿/未编号 revision 垫底。版本列
	// 表页与详情元信息聚合共用。
	ListSuiteRevisions(ctx context.Context, tenantID, suiteID string) ([]domain.SuiteRevisionMeta, error)
	GetDraftRevision(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, bool, error)
	// GetActiveRevision 返回套件当前已发布（active）revision；从未发布的
	// 套件或套件不存在时 found=false。矩阵评测 seed 复用已发布基准集用。
	GetActiveRevision(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, bool, error)
	GetRevision(ctx context.Context, tenantID, revisionID string) (domain.EvalSuiteRevision, bool, error)
	NextVersionNo(ctx context.Context, tenantID, suiteID string) (int, error)
	PublishRevision(ctx context.Context, tenantID, suiteID, revisionID string, versionNo int) (domain.EvalSuiteRevision, error)
	// CreateDraftRevision opens a fresh draft revision for a suite whose
	// draft was cleared by publishing, and points eval_suites at it. The
	// resource kind and parent revision are inherited from the suite's
	// active revision; suites that were never published have no active
	// revision and fail.
	CreateDraftRevision(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error)
	// AddDraftCases inserts generated cases into a draft revision. All cases
	// must carry provenance; the insert is atomic.
	AddDraftCases(ctx context.Context, tenantID, revisionID string, cases []domain.EvalCase) error
	// UpdateDraftCase replaces the editable fields of one draft case
	// (approve = re-submit with Enabled=true, edit = full field replacement).
	UpdateDraftCase(ctx context.Context, tenantID, revisionID string, testCase domain.EvalCase) error
	// DeleteDraftCase removes a rejected draft case.
	DeleteDraftCase(ctx context.Context, tenantID, revisionID, caseID string) error
}

// CaseSampleSource pairs evaluation_feedback signals with the production
// (query, response) conversation rows they came from. The join is trace_id
// based; rows written before trace_id existed are unreachable by design.
type CaseSampleSource interface {
	ListSamples(ctx context.Context, tenantID string, kind domain.ResourceKind, policy domain.SamplePolicy, limit int) ([]domain.CaseSample, error)
}

// CaseGenerator turns one production sample into an eval case with an LLM.
// Implementations use llmgateway's LLMCompleter (same channel as the LLM
// judge). A failed generation returns Valid=false with a diagnostic Reason.
type CaseGenerator interface {
	Generate(ctx context.Context, req CaseGenRequest) (domain.GeneratedCase, error)
}

// CaseGenRequest carries one sample and the suite's resource kind for
// context-aware generation.
type CaseGenRequest struct {
	ResourceKind domain.ResourceKind
	Sample       domain.CaseSample
}

type JobRepository interface {
	Enqueue(ctx context.Context, tenantID string, job domain.EvaluationJob) (domain.EvaluationJob, error)
	Get(ctx context.Context, tenantID, jobID string) (domain.EvaluationJob, bool, error)
	Claim(ctx context.Context, tenantID, workerID string, lease time.Duration) (*domain.EvaluationJob, error)
	Complete(ctx context.Context, tenantID, jobID, resultID string) error
	Fail(ctx context.Context, tenantID, jobID, errorMessage string) error
}

// JobPlatformVerifyRepo 复用既有 evaluation_jobs 表（无 DDL；job_type 列无 CHECK）。
type JobPlatformVerifyRepo interface {
	// EnqueuePlatformVerify 幂等插入（job_type=domain.JobTypePlatformVerify，
	// ON CONFLICT (idempotency_key) DO NOTHING）；返回是否新插入（已存在 → false，
	// 调用方据此不重复 +queued）。
	EnqueuePlatformVerify(
		ctx context.Context, tenantID string, p domain.PlatformVerifyPayload, idempotencyKey, createdBy string,
	) (bool, error)
	// ClaimPlatformVerify 只取本租户 job_type='platform_verify' 的一条（queued/running 过期）。
	ClaimPlatformVerify(
		ctx context.Context, tenantID, workerID string, lease time.Duration,
	) (*domain.PlatformVerifyJob, error)
}

type CandidateCreator interface {
	LoadOptimizableSnapshot(ctx context.Context, tenantID string, baseline domain.ResourceRef) (map[string]any, error)
	CreateCandidate(ctx context.Context, tenantID string, baseline domain.ResourceRef, patch domain.CandidatePatch) (domain.ResourceRef, error)
}

type OptimizationRepository interface {
	WithinTransaction(context.Context, string, func(context.Context) error) error
	GetByIdempotencyKey(context.Context, string, string) (
		domain.OptimizationJob, []domain.OptimizationCandidate, string, bool, error,
	)
	SaveJobWithCandidates(
		ctx context.Context,
		tenantID string,
		job domain.OptimizationJob,
		candidates []domain.OptimizationCandidate,
		idempotencyKey, requestFingerprint string,
	) (bool, error)
}

type CandidateCommandRepository interface {
	Reject(context.Context, string, string, domain.CandidateCommand) (domain.CandidateSummary, error)
}

type CreateRevisionInput struct {
	ResourceKind                                            domain.ResourceKind
	ResourceID, ParentRevisionID, CreatedBy, IdempotencyKey string
	FingerprintPayload                                      any
	Source                                                  domain.RevisionSource
	Payload                                                 any
	SafeSummary                                             map[string]any
}

type RevisionRepository interface {
	Create(context.Context, string, domain.ResourceRevision, string) (domain.ResourceRevision, bool, error)
	Get(context.Context, string, domain.ResourceRef) (domain.ResourceRevision, bool, error)
	Publish(context.Context, string, domain.ResourceRef) (domain.ResourceRevision, error)
}

type ResourceRevisionProvider interface {
	CreatePublishedBaseline(context.Context, string, string) (domain.ResourceRef, error)
}

// AgentRevisionApplier writes an already-published optimization revision
// back to the production Agent table — closing the evaluation → production loop.
// Only Agent resources need this; Skill write-back is handled in promoteCandidateTx.
type AgentRevisionApplier interface {
	ApplyPublishedRevision(ctx context.Context, tenantID, agentID, revisionID string) error
}

type AgentRevisionProvider = ResourceRevisionProvider

type RevisionObjectStore interface {
	Put(context.Context, RevisionPayload) (RevisionPayloadRef, error)
	Get(context.Context, RevisionPayloadRef) ([]byte, error)
	Delete(context.Context, RevisionPayloadRef) error
}

type RevisionPayload struct {
	TenantID, Namespace, ID string
	Value                   any
}

type RevisionPayloadRef struct {
	URI, SHA256 string
	SizeBytes   int64
}

type ExperimentRepository interface {
	ValidatePrerequisites(ctx context.Context, tenantID string, stable, canary domain.ResourceRef,
		suiteRevisionID string) error
	Create(ctx context.Context, tenantID string, experiment domain.Experiment, deployment domain.Deployment,
		ev *auditdomain.ResourceChangeAuditEvent) error
	Get(ctx context.Context, tenantID, experimentID string) (domain.Experiment, bool, error)
	SaveDecision(
		ctx context.Context,
		tenantID string,
		experiment domain.Experiment,
		decision domain.Decision,
		metrics domain.StageMetrics,
		idempotencyKey, fingerprint string,
	) (domain.Experiment, domain.Decision, error)
	ApplyCommand(ctx context.Context, tenantID, experimentID string, action domain.ExperimentCommandAction,
		command domain.ExperimentCommand) (domain.Experiment, error)
	ResolveDeployment(ctx context.Context, tenantID, resourceKind, resourceID string) (domain.Deployment, bool, error)
	// HasRunningExperiment returns true when the resource already has
	// an active (running or paused) experiment.
	HasRunningExperiment(ctx context.Context, tenantID string, resourceKind, resourceID string) (bool, error)
	// ListPendingExperiments returns pending experiments ordered by creation time
	// for a specific resource, or all resources when resourceID is empty.
	ListPendingExperiments(ctx context.Context, tenantID, resourceKind, resourceID string) ([]domain.Experiment, error)
	// ListRunningExperiments returns all running experiments across all resources.
	ListRunningExperiments(ctx context.Context, tenantID string) ([]domain.Experiment, error)
}

type FeedbackRepository interface {
	Record(ctx context.Context, tenantID string, input domain.FeedbackRequest) (domain.EvaluationFeedback, error)
	ActiveExperiment(ctx context.Context, tenantID, resourceKind, resourceID string) (domain.Experiment, bool, error)
	StageFeedback(
		ctx context.Context,
		tenantID string,
		experiment domain.Experiment,
	) (feedback []domain.EvaluationFeedback, observedMinutes int, err error)
}

type ObservedResourceAssignment struct {
	RevisionID   string
	ExperimentID string
	Variant      string
}

// ToolObservation 是执行链路中一次工具调用的最小可观测摘要（评审池详情展示
// 工具序列用；agent domain.ToolObservation 的 summary 投影，见 mapEvaluationEvidence）。
// 结构体由 domain 定义（§6.5 迁移），此处为真实别名：现有 evalport.ToolObservation{...}
// 复合字面量与 []evalport.ToolObservation 切片照常编译（domain 不 import port）。
type ToolObservation = domain.ToolObservation

type ObservedTrace struct {
	TraceID           string
	UserID            string
	CostUSD           float64
	LatencyMs         int64
	Input             string
	Output            string
	TotalTokens       int64
	Success           bool
	SecurityViolation bool
	Assignments       map[string]ObservedResourceAssignment
	// Tools 是执行链路工具调用序列（P1c 评审详情用；证据后端不返回时为 nil）。
	Tools []ToolObservation
}

type TraceEvidenceReader interface {
	Resolve(context.Context, string, string) (ObservedTrace, error)
	ResolveBatch(context.Context, string, []string) (map[string]ObservedTrace, error)
}

// SnapshotCapturer 在评测 run 创建时捕获执行上下文版本快照（D1/D5：创建时
// fail-closed 锚定）。任何捕获失败返回 error → EnqueueRun 拒绝创建，绝不
// 入队一个无快照的 job。
type SnapshotCapturer interface {
	Capture(ctx context.Context, tenantID string, input CaptureInput) (*domain.EvaluationContextSnapshot, error)
}

// CaptureInput 描述一次快照捕获：被测资源 + 评测套件 revision + 请求者。
// PlatformSeqOverrides 按平台组（evaluation/agent/trace groupKey）指定历史版本
// version_seq 覆盖（对照确认 run 重放）；空 = 现 IsCurrent 语义。
type CaptureInput struct {
	Resource             domain.ResourceRef
	SuiteRevisionID      string
	RequestedBy          string
	PlatformSeqOverrides map[string]int64
}
