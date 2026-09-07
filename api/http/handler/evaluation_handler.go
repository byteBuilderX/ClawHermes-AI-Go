package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/byteBuilderX/stratum/api/http/dto/gen"
	"github.com/byteBuilderX/stratum/api/middleware"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type evaluationSuiteService interface {
	Create(ctx context.Context, tenantID string, input evalapp.CreateSuiteInput) (domain.EvalSuite, domain.EvalSuiteRevision, error)
	Publish(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error)
	GetDraft(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error)
	GetRevision(ctx context.Context, tenantID, revisionID string) (domain.EvalSuiteRevision, error)
	UpdateDraftCase(ctx context.Context, tenantID, suiteID, caseID string, testCase domain.EvalCase) (domain.EvalCase, error)
	GetSuiteDetail(ctx context.Context, tenantID, suiteID string) (domain.SuiteDetail, error)
	ListVersions(ctx context.Context, tenantID, suiteID string) ([]domain.SuiteRevisionMeta, error)
	AddDraftCase(ctx context.Context, tenantID, suiteID string, testCase domain.EvalCase) (domain.EvalCase, error)
	DeleteDraftCase(ctx context.Context, tenantID, suiteID, caseID string) error
	StartNextDraft(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error)
}

type evaluationCaseGenerator interface {
	Generate(ctx context.Context, input evalapp.GenerateInput) (evalapp.GenerateResult, error)
}

type evaluationJobService interface {
	EnqueueRun(ctx context.Context, tenantID string, input evalapp.EnqueueRunInput) (domain.EvaluationJob, error)
	Get(ctx context.Context, tenantID, jobID string) (domain.EvaluationJob, error)
}

type evaluationRunService interface {
	GetRun(ctx context.Context, tenantID, runID string) (domain.EvalRun, error)
}

type evaluationOptimizationService interface {
	Generate(
		ctx context.Context,
		tenantID string,
		input evalapp.GenerateCandidatesInput,
	) (domain.OptimizationJob, []domain.OptimizationCandidate, error)
}

type evaluationExperimentService interface {
	Create(
		ctx context.Context,
		tenantID string,
		input evalapp.CreateExperimentInput,
	) (domain.Experiment, domain.Deployment, error)
	EvaluateStageIdempotent(context.Context, string, string, evalapp.EvaluateStageInput) (domain.Experiment, domain.Decision, error)
	Pause(context.Context, string, string, evalapp.ExperimentCommandInput) (domain.Experiment, error)
	Promote(context.Context, string, string, evalapp.ExperimentCommandInput) (domain.Experiment, error)
	Rollback(context.Context, string, string, evalapp.ExperimentCommandInput) (domain.Experiment, error)
}

type evaluationQueryService interface {
	Overview(context.Context, string) (domain.CenterOverview, error)
	ListResources(context.Context, string, port.CenterFilter) (domain.ResourcePage, error)
	ListSuites(context.Context, string, port.CenterFilter) (domain.SuitePage, error)
	ListRuns(context.Context, string, port.CenterFilter) (domain.RunPage, error)
	ListCandidates(context.Context, string, port.CenterFilter) (domain.CandidatePage, error)
	ListExperiments(context.Context, string, port.CenterFilter) (domain.ExperimentPage, error)
	Timeline(context.Context, string, port.CenterFilter) (domain.TimelinePage, error)
	MonitorResources(context.Context, string, port.MonitorFilter) (domain.MonitorResourcesPage, error)
	MonitorTrend(context.Context, string, port.MonitorFilter) (domain.MonitorTrendSeries, error)
	// ListRevisions (0) 返回单被测资源的 eval 版本表（含零引用版本）。
	ListRevisions(context.Context, string, port.CenterFilter) (domain.RevisionPage, error)
	// RevisionReferences (c) 返回单 eval 版本的引用方账本。
	RevisionReferences(context.Context, string, domain.ResourceRef) (domain.RevisionReferences, error)
	// RevisionPassRate (d) 返回单 eval 版本通过率摘要。
	RevisionPassRate(context.Context, string, domain.ResourceRef) (domain.RevisionPassRate, error)
}

type evaluationCandidateCommandService interface {
	Reject(context.Context, string, string, evalapp.CandidateCommandInput) (domain.CandidateSummary, error)
}

type evaluationFeedbackService interface {
	Record(
		ctx context.Context,
		tenantID string,
		input evalapp.RecordFeedbackInput,
	) (evalapp.FeedbackResult, error)
}

// evaluationDeleteService 全实体删除服务（owner-or-creator 门禁，fail-closed）。
type evaluationDeleteService interface {
	DeleteSuite(ctx context.Context, tenantID, suiteID, actorID string) error
	DeleteRun(ctx context.Context, tenantID, runID, actorID string) error
	DeleteJob(ctx context.Context, tenantID, jobID, actorID string) error
	DeleteExperiment(ctx context.Context, tenantID, experimentID, actorID string) error
	DeleteCandidate(ctx context.Context, tenantID, candidateID, actorID string) error
	DeleteReviewItem(ctx context.Context, tenantID, reviewID, actorID string) error
	DeleteFeedback(ctx context.Context, tenantID, feedbackID, actorID string) error
}

type evaluationBaselineService interface {
	CreatePublishedBaseline(
		ctx context.Context, tenantID string, kind domain.ResourceKind, resourceID string,
	) (domain.ResourceRef, error)
}

type evaluationAgentRevisionApplier interface {
	ApplyPublishedRevision(ctx context.Context, tenantID, agentID, revisionID string) error
}

// evaluationObservationQueryService 运行态观测查询服务（P1a 查询 API）。
type evaluationObservationQueryService interface {
	ListObservations(ctx context.Context, tenantID, resourceKind, resourceID string,
		from, to *time.Time, limit, offset int) ([]domain.EvalObservation, error)
	GetObservation(ctx context.Context, tenantID, id string) (*domain.EvalObservation, error)
}

// evaluationReviewService 评审池查询与人工决策服务（P1c）。*evalapp.ReviewService 满足该接口。
type evaluationReviewService interface {
	List(ctx context.Context, tenantID string, f port.ReviewFilter) ([]domain.ReviewItem, int64, error)
	Get(ctx context.Context, tenantID, id string) (*domain.ReviewItem, error)
	Decide(ctx context.Context, tenantID, id, actor string, verdict domain.HumanVerdict,
		reason string) (*domain.ReviewItem, error)
}

type EvaluationHandler struct {
	suites       evaluationSuiteService
	jobs         evaluationJobService
	runs         evaluationRunService
	optimization evaluationOptimizationService
	experiments  evaluationExperimentService
	feedback     evaluationFeedbackService
	queries      evaluationQueryService
	candidates   evaluationCandidateCommandService
	baselines    evaluationBaselineService
	agentApplier evaluationAgentRevisionApplier
	casegen      evaluationCaseGenerator
	observations evaluationObservationQueryService
	review       evaluationReviewService
	deletes      evaluationDeleteService
	logger       *zap.Logger
}

func NewEvaluationHandler(
	suites evaluationSuiteService,
	jobs evaluationJobService,
	runs evaluationRunService,
	optimization evaluationOptimizationService,
	experiments evaluationExperimentService,
	feedback evaluationFeedbackService,
	queries evaluationQueryService,
	candidates evaluationCandidateCommandService,
	logger *zap.Logger,
) *EvaluationHandler {
	return &EvaluationHandler{
		suites: suites, jobs: jobs, runs: runs, optimization: optimization,
		experiments: experiments, feedback: feedback, queries: queries, candidates: candidates, logger: logger,
	}
}

func (h *EvaluationHandler) WithBaselineService(service evaluationBaselineService) *EvaluationHandler {
	h.baselines = service
	return h
}

func (h *EvaluationHandler) WithAgentRevisionApplier(applier evaluationAgentRevisionApplier) *EvaluationHandler {
	h.agentApplier = applier
	return h
}

func (h *EvaluationHandler) WithTestCaseGenerator(generator evaluationCaseGenerator) *EvaluationHandler {
	h.casegen = generator
	return h
}

// WithObservationService 注入运行态观测查询服务（P1a 查询 API）。
func (h *EvaluationHandler) WithObservationService(service evaluationObservationQueryService) *EvaluationHandler {
	h.observations = service
	return h
}

// WithReviewService 注入评审池查询与决策服务（P1c）。
func (h *EvaluationHandler) WithReviewService(service evaluationReviewService) *EvaluationHandler {
	h.review = service
	return h
}

// WithDeleteService 注入全实体删除服务（owner-or-creator 门禁）。
func (h *EvaluationHandler) WithDeleteService(service evaluationDeleteService) *EvaluationHandler {
	h.deletes = service
	return h
}

func (h *EvaluationHandler) CreateBaseline(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if h.baselines == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("evaluation baseline unavailable")))
		return
	}
	kind := domain.ResourceKind(c.Param("kind"))
	if err := kind.Validate(); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	// 被测收敛：建档入口仅保留 agent 与 knowledge 两轨；skill/mcp 退出独立评测
	// （skill 仅作被测 agent 绑定资源，其历史 baseline 由既有内部路径/读回承载，
	// 这里禁止新建档，避免中心默认视图出现 skill/mcp 新行）。
	switch kind {
	case domain.ResourceKindAgent, domain.ResourceKindKnowledge:
	default:
		_ = c.Error(middleware.NewHTTPError(http.StatusForbidden,
			errors.New("resource kind 不再支持建档：被测仅支持 agent 与 knowledge")))
		return
	}
	ref, err := h.baselines.CreatePublishedBaseline(c.Request.Context(), tenantID, kind, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, ref)
}

func (h *EvaluationHandler) CreateSuite(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.CreateEvaluationSuiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	cases := make([]domain.EvalCase, 0, len(req.Cases))
	for _, item := range req.Cases {
		cases = append(cases, toDomainCase(item))
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	suite, revision, err := h.suites.Create(c.Request.Context(), tenantID, evalapp.CreateSuiteInput{
		Name: req.Name, Description: req.Description, ResourceKind: domain.ResourceKind(req.ResourceKind), Cases: cases,
		ActorID: actorID,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"suite": suite, "revision": revision})
}

// toDomainCase converts one create-suite request case into a domain.EvalCase,
// copying the assertion config (judge_spec / tool_spec / step_judge) and the
// session script (阶段 B §5.4) verbatim so the payloads bind to the domain
// shapes. Session cases carry no single-turn input (input is meaningless under
// the multi-turn runner); the shape validation happens in the application layer.
func toDomainCase(item gen.EvaluationCaseRequest) domain.EvalCase {
	enabled := true
	if item.Enabled != nil {
		enabled = *item.Enabled
	}
	testCase := domain.EvalCase{
		Name: item.Name, Input: item.Input, ExpectedOutput: item.ExpectedOutput,
		AssertionMode: domain.AssertionMode(item.AssertionMode), Enabled: enabled,
		Session: toSessionScript(item.Session),
	}
	if item.JudgeSpec != nil {
		testCase.JudgeSpec = &domain.JudgeSpec{Model: item.JudgeSpec.Model, Rubric: item.JudgeSpec.Rubric}
	}
	if item.ToolSpec != nil {
		testCase.ToolSpec = &domain.ToolSpec{
			MustCall: item.ToolSpec.MustCall, MustNotCall: item.ToolSpec.MustNotCall,
			Order: item.ToolSpec.Order, MaxCalls: int(item.ToolSpec.MaxCalls),
		}
	}
	if item.StepJudge != nil {
		testCase.StepJudge = &domain.StepJudge{Criteria: item.StepJudge.Criteria}
	}
	return testCase
}

// toSessionScript converts the request-layer session script into the domain
// shape, mapping each turn's optional tool_spec the same way the case-level
// tool_spec is mapped. A nil request script yields nil (old single-turn case).
func toSessionScript(script *gen.EvalSessionScript) *domain.EvalSessionScript {
	if script == nil {
		return nil
	}
	turns := make([]domain.SessionTurn, 0, len(script.Turns))
	for _, turn := range script.Turns {
		t := domain.SessionTurn{User: turn.User, Probe: turn.Probe}
		if turn.ToolSpec != nil {
			t.ToolSpec = &domain.ToolSpec{
				MustCall: turn.ToolSpec.MustCall, MustNotCall: turn.ToolSpec.MustNotCall,
				Order: turn.ToolSpec.Order, MaxCalls: int(turn.ToolSpec.MaxCalls),
			}
		}
		turns = append(turns, t)
	}
	return &domain.EvalSessionScript{Goal: script.Goal, Turns: turns}
}

func (h *EvaluationHandler) PublishSuite(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	revision, err := h.suites.Publish(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, revision)
}

// GenerateSuiteCases samples production interactions for the suite's
// resource kind and writes generated cases into its draft revision for
// human review. The generator never publishes automatically.
func (h *EvaluationHandler) GenerateSuiteCases(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if h.casegen == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("case generator unavailable")))
		return
	}
	var req gen.GenerateSuiteCasesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	limit := constants.DefaultCaseSampleLimit
	if req.MaxCases > 0 {
		limit = int(req.MaxCases)
	}
	result, err := h.casegen.Generate(c.Request.Context(), evalapp.GenerateInput{
		TenantID: tenantID,
		SuiteID:  c.Param("id"),
		Policy:   domain.SamplePolicy(req.SamplePolicy),
		MaxCases: limit,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *EvaluationHandler) GetSuiteDraft(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	revision, err := h.suites.GetDraft(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, revision)
}

// UpdateDraftCase approves (enabled=true), edits (full replacement) or
// rejects (enabled=false) one case in the suite's draft revision.
func (h *EvaluationHandler) UpdateDraftCase(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.UpdateDraftCaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updated, err := h.suites.UpdateDraftCase(c.Request.Context(), tenantID, c.Param("id"), c.Param("caseId"), domain.EvalCase{
		ID:             c.Param("caseId"),
		Name:           req.Name,
		Input:          req.Input,
		ExpectedOutput: req.ExpectedOutput,
		AssertionMode:  domain.AssertionMode(req.AssertionMode),
		Enabled:        enabled,
		Session:        toSessionScript(req.Session),
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// GetSuiteDetail returns the suite header meta for the detail page: base fields
// plus the current active/draft version numbers, enabled case counts, resource
// kind and status aggregated from the revision chain. member 可读。
func (h *EvaluationHandler) GetSuiteDetail(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	detail, err := h.suites.GetSuiteDetail(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// ListSuiteVersions returns the lightweight revision chain (published newest
// first, draft last) without case bodies. member 可读。
func (h *EvaluationHandler) ListSuiteVersions(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	metas, err := h.suites.ListVersions(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	if metas == nil {
		metas = []domain.SuiteRevisionMeta{}
	}
	c.JSON(http.StatusOK, metas)
}

// GetSuiteRevision returns one revision's full case bodies (single-turn cases
// with assertion config, session scripts included) for read-only display.
// member 可读。
func (h *EvaluationHandler) GetSuiteRevision(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	revision, err := h.suites.GetRevision(c.Request.Context(), tenantID, c.Param("revisionId"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, revision)
}

// StartNextDraft guarantees an editable draft for the suite: returns the
// existing one, or on a legacy suite (published, no draft) inherits the active
// revision's cases into a new draft. admin。成功 200 返回（可能带 cases 的）草稿。
func (h *EvaluationHandler) StartNextDraft(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	draft, err := h.suites.StartNextDraft(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, draft)
}

// AddDraftCase appends one hand-authored case to the suite's draft revision.
// admin；请求体复用 create-suite 的单 case 结构（gen.EvaluationCaseRequest），
// 经 toDomainCase 转换后走与 Create/UpdateDraftCase 一致的 authoring 校验。
func (h *EvaluationHandler) AddDraftCase(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.EvaluationCaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	testCase, err := h.suites.AddDraftCase(c.Request.Context(), tenantID, c.Param("id"), toDomainCase(req))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, testCase)
}

// DeleteDraftCase removes one case from the suite's draft revision. admin；
// 成功 204 空体，失败（case 不在当前草稿/无草稿/套件不存在）交给统一错误中间件。
func (h *EvaluationHandler) DeleteDraftCase(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if err := h.suites.DeleteDraftCase(c.Request.Context(), tenantID, c.Param("id"), c.Param("caseId")); err != nil {
		_ = c.Error(err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *EvaluationHandler) EnqueueRun(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	requestedBy, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	var req gen.EnqueueEvaluationRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	job, err := h.jobs.EnqueueRun(c.Request.Context(), tenantID, evalapp.EnqueueRunInput{
		Resource: domain.ResourceRef{
			Kind: domain.ResourceKind(req.Resource.Kind), ResourceID: req.Resource.ResourceID, RevisionID: req.Resource.RevisionID,
		},
		SuiteRevisionID:      req.SuiteRevisionID,
		IdempotencyKey:       req.IdempotencyKey,
		RequestedBy:          requestedBy,
		PlatformSeqOverrides: req.PlatformSeqOverrides,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusAccepted, gen.EvaluationJobResponse{JobID: job.ID, Status: string(job.Status)})
}

func (h *EvaluationHandler) GetJob(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	job, err := h.jobs.Get(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gen.EvaluationJobResponse{
		JobID: job.ID, Status: string(job.Status), ErrorMessage: job.ErrorMessage, ResultID: job.ResultID,
	})
}

func (h *EvaluationHandler) GetRun(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	run, err := h.runs.GetRun(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, runDetailResponse{EvalRun: run, Anchors: domain.ResolveRunAnchors(run)})
}

// runDetailResponse 是 run 详情展示信封：嵌入 EvalRun 全字段并附一份平铺的
// 锚定资源清单（anchors），供前端显式展示评测锚定的资源版本。旧 run 无快照
// pin 时 anchors 仅含被测主体。
type runDetailResponse struct {
	domain.EvalRun
	Anchors []domain.RunResourceAnchor `json:"anchors"`
}

func (h *EvaluationHandler) GenerateOptimization(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.GenerateOptimizationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	job, candidates, err := h.optimization.Generate(c.Request.Context(), tenantID, evalapp.GenerateCandidatesInput{
		IdempotencyKey: firstNonEmpty(req.IdempotencyKey, c.GetHeader("Idempotency-Key")),
		Baseline: domain.ResourceRef{
			Kind: domain.ResourceKind(req.Baseline.Kind), ResourceID: req.Baseline.ResourceID,
			RevisionID: req.Baseline.RevisionID,
		},
		SuiteRevisionID: req.SuiteRevisionID, SearchSpace: req.SearchSpace,
		FailureSummaries: req.FailureSummaries, ActorID: actorID,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"job": job, "candidates": candidates})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (h *EvaluationHandler) CreateExperiment(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.CreateEvaluationExperimentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	toRef := func(ref gen.EvaluationResourceRef) domain.ResourceRef {
		return domain.ResourceRef{Kind: domain.ResourceKind(ref.Kind), ResourceID: ref.ResourceID, RevisionID: ref.RevisionID}
	}
	stable, canary := toRef(req.Stable), toRef(req.Canary)
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	experiment, deployment, err := h.experiments.Create(c.Request.Context(), tenantID, evalapp.CreateExperimentInput{
		Stable: stable, Canary: canary, SuiteRevisionID: req.SuiteRevisionID, ActorID: actorID,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"experiment": experiment, "deployment": deployment})
}

func (h *EvaluationHandler) Overview(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	result, err := h.queries.Overview(c.Request.Context(), tenantID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func centerFilter(c *gin.Context, kind, id string) (port.CenterFilter, error) {
	var req gen.EvaluationCenterQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		return port.CenterFilter{}, err
	}
	if kind != "" {
		req.ResourceKind = kind
	}
	if id != "" {
		req.ResourceID = id
	}
	// 被测收敛：筛选放行四类单值（skill/mcp 历史只读）与默认双轨 CSV
	// 'agent,knowledge'；非法 token 在此统一 400，替代被移除的 oneof tag。
	if err := validateCenterResourceKind(req.ResourceKind); err != nil {
		return port.CenterFilter{}, err
	}
	return port.CenterFilter{ResourceKind: req.ResourceKind, ResourceID: req.ResourceID, RevisionID: req.RevisionID,
		Status: req.Status, Cursor: req.Cursor, Limit: req.Limit}, nil
}

// validateCenterResourceKind 校验评测中心被测类型筛选值：空串=全部；每个逗号分隔
// token 都必须是对被测类型的合法取值（skill/agent/mcp/knowledge，逐 token 复用
// domain.ResourceKind.Validate，避免在 handler 复制允许集）。
func validateCenterResourceKind(kind string) error {
	if kind == "" {
		return nil
	}
	for _, token := range strings.Split(kind, ",") {
		if token == "" {
			return errors.New("resource_kind 含空的逗号分隔 token")
		}
		if err := domain.ResourceKind(token).Validate(); err != nil {
			return fmt.Errorf("resource_kind %q 不支持：被测筛选仅支持 skill/agent/mcp/knowledge 或其逗号组合", token)
		}
	}
	return nil
}

func queryPage[T any](c *gin.Context, call func(string, port.CenterFilter) (T, error), kind, id string) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	filter, err := centerFilter(c, kind, id)
	if err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	page, err := call(tenantID, filter)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (h *EvaluationHandler) ListResources(c *gin.Context) {
	queryPage(c, func(t string, f port.CenterFilter) (domain.ResourcePage, error) {
		return h.queries.ListResources(c.Request.Context(), t, f)
	}, "", "")
}
func (h *EvaluationHandler) ListSuites(c *gin.Context) {
	queryPage(c, func(t string, f port.CenterFilter) (domain.SuitePage, error) {
		return h.queries.ListSuites(c.Request.Context(), t, f)
	}, "", "")
}
func (h *EvaluationHandler) ListRuns(c *gin.Context) {
	queryPage(c, func(t string, f port.CenterFilter) (domain.RunPage, error) {
		return h.queries.ListRuns(c.Request.Context(), t, f)
	}, "", "")
}
func (h *EvaluationHandler) ListCandidates(c *gin.Context) {
	queryPage(c, func(t string, f port.CenterFilter) (domain.CandidatePage, error) {
		return h.queries.ListCandidates(c.Request.Context(), t, f)
	}, "", "")
}
func (h *EvaluationHandler) ListExperiments(c *gin.Context) {
	queryPage(c, func(t string, f port.CenterFilter) (domain.ExperimentPage, error) {
		return h.queries.ListExperiments(c.Request.Context(), t, f)
	}, "", "")
}
func (h *EvaluationHandler) Timeline(c *gin.Context) {
	queryPage(c, func(t string, f port.CenterFilter) (domain.TimelinePage, error) {
		return h.queries.Timeline(c.Request.Context(), t, f)
	}, c.Param("kind"), c.Param("id"))
}

// ListRevisions (0) 被测资源 eval 版本表。kind/id 来自路径参数；单资源约束
// （非 CSV、resource_id 必填）由 service 校验为 400。
func (h *EvaluationHandler) ListRevisions(c *gin.Context) {
	queryPage(c, func(t string, f port.CenterFilter) (domain.RevisionPage, error) {
		return h.queries.ListRevisions(c.Request.Context(), t, f)
	}, c.Param("kind"), c.Param("id"))
}

// revisionScoped 组装并校验版本作用域引用（kind/id/revisionId 路径参数必填）。
// 路径参数非法 → 400；资源/版本不存在由 service 经 error middleware 映射 404。
func (h *EvaluationHandler) revisionScoped(c *gin.Context) (tenantID string, ref domain.ResourceRef, ok bool) {
	tenantID, ok = tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	ref = domain.ResourceRef{Kind: domain.ResourceKind(c.Param("kind")),
		ResourceID: c.Param("id"), RevisionID: c.Param("revisionId")}
	if err := ref.Validate(); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		ok = false
		return
	}
	return tenantID, ref, true
}

func (h *EvaluationHandler) RevisionReferences(c *gin.Context) {
	tenantID, ref, ok := h.revisionScoped(c)
	if !ok {
		return
	}
	result, err := h.queries.RevisionReferences(c.Request.Context(), tenantID, ref)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *EvaluationHandler) RevisionPassRate(c *gin.Context) {
	tenantID, ref, ok := h.revisionScoped(c)
	if !ok {
		return
	}
	result, err := h.queries.RevisionPassRate(c.Request.Context(), tenantID, ref)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func commandInput(c *gin.Context) (evalapp.ExperimentCommandInput, bool) {
	var req gen.EvaluationCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return evalapp.ExperimentCommandInput{}, false
	}
	actorID, ok := userIDFromCtx(c)
	if !ok || actorID == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusUnauthorized, errors.New("authenticated actor required")))
		return evalapp.ExperimentCommandInput{}, false
	}
	return evalapp.ExperimentCommandInput{ActorID: actorID, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey,
		ExpectedStateVersion: req.ExpectedStateVersion}, true
}

// experimentCommand 解析命令入参后按角色分流（member → 审批，admin/owner → 直接执行）。
// commandInput 已消费请求体并校验 actor，因此 args 中的 reason 等字段直接取自解析结果。
func (h *EvaluationHandler) experimentCommand(c *gin.Context, operation string, call func(context.Context, string, string, evalapp.ExperimentCommandInput) (domain.Experiment, error)) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	input, ok := commandInput(c)
	if !ok {
		return
	}
	result, err := call(c.Request.Context(), tenantID, c.Param("id"), input)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}
func (h *EvaluationHandler) PauseExperiment(c *gin.Context) {
	h.experimentCommand(c, "pause_experiment", h.experiments.Pause)
}

// PromoteExperiment promotes an experiment's canary to stable.  For Agent
// resources it additionally writes the optimized revision payload back to the
// agents table, closing the evaluation → production loop.
func (h *EvaluationHandler) PromoteExperiment(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	input, ok := commandInput(c)
	if !ok {
		return
	}
	result, err := h.experiments.Promote(c.Request.Context(), tenantID, c.Param("id"), input)
	if err != nil {
		_ = c.Error(err)
		return
	}
	// Write optimized Agent revision back to the production agents table.
	if result.ResourceKind == domain.ResourceKindAgent && h.agentApplier != nil {
		if applyErr := h.agentApplier.ApplyPublishedRevision(
			c.Request.Context(), tenantID, result.ResourceID, result.CanaryRevisionID,
		); applyErr != nil {
			h.logger.Warn("promote experiment: agent write-back failed",
				zap.String("agent_id", result.ResourceID),
				zap.String("revision_id", result.CanaryRevisionID),
				zap.Error(applyErr),
			)
		}
	}
	c.JSON(http.StatusOK, result)
}
func (h *EvaluationHandler) RollbackExperiment(c *gin.Context) {
	h.experimentCommand(c, "rollback_experiment", h.experiments.Rollback)
}

func (h *EvaluationHandler) RejectCandidate(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	input, ok := commandInput(c)
	if !ok {
		return
	}
	result, err := h.candidates.Reject(c.Request.Context(), tenantID, c.Param("id"), evalapp.CandidateCommandInput(input))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *EvaluationHandler) RecordFeedback(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	var req gen.RecordEvaluationFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	result, err := h.feedback.Record(c.Request.Context(), tenantID, evalapp.RecordFeedbackInput{
		ActorID: actorID, TraceID: req.TraceID, ResourceKind: domain.ResourceKind(req.ResourceKind), ResourceID: req.ResourceID,
		Score: req.Score, Outcome: req.Outcome, IdempotencyKey: req.IdempotencyKey,
		SecurityViolation: req.SecurityViolation,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

// ListObservationsQuery 观测分页查询参数（from/to 可选，RFC3339）。
type ListObservationsQuery struct {
	ResourceKind string     `form:"resource_kind"`
	ResourceID   string     `form:"resource_id"`
	From         *time.Time `form:"from" time_format:"2006-01-02T15:04:05Z07:00"`
	To           *time.Time `form:"to" time_format:"2006-01-02T15:04:05Z07:00"`
	Page         int        `form:"page"`
	PageSize     int        `form:"page_size"`
}

// ListObservations 返回运行态观测明细分页（规格 §10.1 数据源）。
func (h *EvaluationHandler) ListObservations(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if h.observations == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("evaluation observation unavailable")))
		return
	}
	var req ListObservationsQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize <= 0 || req.PageSize > constants.MaxPageSize {
		req.PageSize = constants.DefaultPageSize
	}
	limit, offset := req.PageSize, (req.Page-1)*req.PageSize
	items, err := h.observations.ListObservations(c.Request.Context(), tenantID,
		req.ResourceKind, req.ResourceID, req.From, req.To, limit, offset)
	if err != nil {
		_ = c.Error(err)
		return
	}
	if items == nil {
		items = []domain.EvalObservation{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// GetObservation 返回单条运行态观测明细。
func (h *EvaluationHandler) GetObservation(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if h.observations == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("evaluation observation unavailable")))
		return
	}
	obs, err := h.observations.GetObservation(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	if obs == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "observation not found"})
		return
	}
	c.JSON(http.StatusOK, obs)
}

// MonitorQuery 监控聚合查询参数（spec 2026-09-03 §4.2）。from/to 可选（RFC3339，
// 缺省由 service 兜底近 EvalMonitorWindowDays 天）；端点 1 resource_kind/resource_id
// 可选、limit 可选（默认/上限走 pkg/constants）；端点 2 trend 复用同 DTO，kind/id
// 必填由 handler 校验。
type MonitorQuery struct {
	ResourceKind string     `form:"resource_kind"`
	ResourceID   string     `form:"resource_id"`
	From         *time.Time `form:"from" time_format:"2006-01-02T15:04:05Z07:00"`
	To           *time.Time `form:"to" time_format:"2006-01-02T15:04:05Z07:00"`
	Limit        int        `form:"limit"`
}

// ListMonitorResources 返回窗口内资源行四区摘要（spec §4.2 端点 1，member 可读）。
func (h *EvaluationHandler) ListMonitorResources(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req MonitorQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	if err := validateMonitorResourcesQuery(req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	page, err := h.queries.MonitorResources(c.Request.Context(), tenantID, port.MonitorFilter{
		ResourceKind: req.ResourceKind, ResourceID: req.ResourceID, From: req.From, To: req.To, Limit: req.Limit,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// GetMonitorTrend 返回单资源四区时间趋势（spec §4.2 端点 2，member 可读）。
func (h *EvaluationHandler) GetMonitorTrend(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req MonitorQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	if err := validateMonitorTrendQuery(req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	series, err := h.queries.MonitorTrend(c.Request.Context(), tenantID, port.MonitorFilter{
		ResourceKind: req.ResourceKind, ResourceID: req.ResourceID, From: req.From, To: req.To,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, series)
}

// validateMonitorResourcesQuery 端点 1：kind 提供时须在资源白名单内；单传
// resource_id 而无 kind 拒绝；from/to 同时提供时顺序合法。kind/id 均可不传
// （整租户窗口汇总）。
func validateMonitorResourcesQuery(req MonitorQuery) error {
	if err := validateMonitorWindowQuery(req); err != nil {
		return err
	}
	if req.ResourceID != "" && req.ResourceKind == "" {
		return errors.New("resource_id requires resource_kind")
	}
	return nil
}

// validateMonitorTrendQuery 端点 2：在窗口/白名单校验基础上强制 kind+id 成对必填。
func validateMonitorTrendQuery(req MonitorQuery) error {
	if err := validateMonitorWindowQuery(req); err != nil {
		return err
	}
	if req.ResourceKind == "" {
		return errors.New("resource kind required")
	}
	if req.ResourceID == "" {
		return errors.New("resource id required")
	}
	return nil
}

// validateMonitorWindowQuery 两端点共享：kind 若提供须为 skill|agent|mcp|knowledge
// （复用 domain.ResourceKind.Validate）；from/to 同时提供时不得倒置。
func validateMonitorWindowQuery(req MonitorQuery) error {
	if req.ResourceKind != "" {
		if err := domain.ResourceKind(req.ResourceKind).Validate(); err != nil {
			return err
		}
	}
	if req.From != nil && req.To != nil && req.From.After(*req.To) {
		return errors.New("from must not be after to")
	}
	return nil
}

// ReviewListQuery 评审池分页查询参数（status/trigger_reason 为原始字符串，边界在
// handler 内收敛为领域类型，避免绑定层自定义类型解析依赖）。
type ReviewListQuery struct {
	Status        string `form:"status"`
	TriggerReason string `form:"trigger_reason"`
	ResourceKind  string `form:"resource_kind"`
	ResourceID    string `form:"resource_id"`
	Page          int    `form:"page"`
	PageSize      int    `form:"page_size"`
}

// ListReviewItems 返回评审池分页明细（spec §9 数据源）。
func (h *EvaluationHandler) ListReviewItems(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if h.review == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("evaluation review unavailable")))
		return
	}
	var req ReviewListQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize <= 0 || req.PageSize > constants.MaxPageSize {
		req.PageSize = constants.DefaultPageSize
	}
	items, total, err := h.review.List(c.Request.Context(), tenantID, port.ReviewFilter{
		Status:        domain.ReviewItemStatus(req.Status),
		TriggerReason: domain.ReviewTriggerReason(req.TriggerReason),
		ResourceKind:  req.ResourceKind,
		ResourceID:    req.ResourceID,
		Limit:         req.PageSize,
		Offset:        (req.Page - 1) * req.PageSize,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	if items == nil {
		items = []domain.ReviewItem{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// GetReviewItem 返回单条评审明细。
func (h *EvaluationHandler) GetReviewItem(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if h.review == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("evaluation review unavailable")))
		return
	}
	item, err := h.review.Get(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil {
		if errors.Is(err, evalapp.ErrReviewItemNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "review item not found"})
			return
		}
		_ = c.Error(err)
		return
	}
	if item == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "review item not found"})
		return
	}
	c.JSON(http.StatusOK, item)
}

// DecideReviewItem 人工评审结论回写（spec §9 状态机：pending → reviewed）。
func (h *EvaluationHandler) DecideReviewItem(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	if h.review == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("evaluation review unavailable")))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok || actorID == "" {
		respondMissingUser(c)
		return
	}
	var req gen.ReviewItemDecisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	item, err := h.review.Decide(c.Request.Context(), tenantID, c.Param("id"), actorID,
		domain.HumanVerdict(req.Verdict), req.Reason)
	if err != nil {
		if errors.Is(err, evalapp.ErrReviewItemNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "review item not found"})
			return
		}
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, item)
}

// deleteEntity 统一删除 handler 形状：tenant→actor→service；服务未装配时
// 503 fail-closed；成功 204 空体；失败交给统一错误中间件映射。
func (h *EvaluationHandler) deleteEntity(c *gin.Context, del func(context.Context, string, string, string) error) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if h.deletes == nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusServiceUnavailable, errors.New("evaluation delete unavailable")))
		return
	}
	if err := del(c.Request.Context(), tenantID, c.Param("id"), actorID); err != nil {
		_ = c.Error(err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *EvaluationHandler) DeleteSuite(c *gin.Context) {
	// 闭包内才解引用 h.deletes：若直接传 h.deletes.DeleteSuite 方法值，
	// nil 接口在参数求值阶段就 panic，deleteEntity 的 nil 检查形同虚设
	// （503 fail-closed 无法生效）。
	h.deleteEntity(c, func(ctx context.Context, tenantID, id, actorID string) error {
		return h.deletes.DeleteSuite(ctx, tenantID, id, actorID)
	})
}

func (h *EvaluationHandler) DeleteRun(c *gin.Context) {
	h.deleteEntity(c, func(ctx context.Context, tenantID, id, actorID string) error {
		return h.deletes.DeleteRun(ctx, tenantID, id, actorID)
	})
}

func (h *EvaluationHandler) DeleteJob(c *gin.Context) {
	h.deleteEntity(c, func(ctx context.Context, tenantID, id, actorID string) error {
		return h.deletes.DeleteJob(ctx, tenantID, id, actorID)
	})
}

func (h *EvaluationHandler) DeleteExperiment(c *gin.Context) {
	h.deleteEntity(c, func(ctx context.Context, tenantID, id, actorID string) error {
		return h.deletes.DeleteExperiment(ctx, tenantID, id, actorID)
	})
}

func (h *EvaluationHandler) DeleteCandidate(c *gin.Context) {
	h.deleteEntity(c, func(ctx context.Context, tenantID, id, actorID string) error {
		return h.deletes.DeleteCandidate(ctx, tenantID, id, actorID)
	})
}

func (h *EvaluationHandler) DeleteReviewItem(c *gin.Context) {
	h.deleteEntity(c, func(ctx context.Context, tenantID, id, actorID string) error {
		return h.deletes.DeleteReviewItem(ctx, tenantID, id, actorID)
	})
}

func (h *EvaluationHandler) DeleteFeedback(c *gin.Context) {
	h.deleteEntity(c, func(ctx context.Context, tenantID, id, actorID string) error {
		return h.deletes.DeleteFeedback(ctx, tenantID, id, actorID)
	})
}
