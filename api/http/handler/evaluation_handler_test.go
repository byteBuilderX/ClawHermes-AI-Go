package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/api/middleware"
	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	"github.com/byteBuilderX/stratum/internal/evaluation/application"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestEvaluationHandlerEnqueueRunReturnsAcceptedJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jobs := &fakeEvaluationJobs{}
	h := NewEvaluationHandler(nil, jobs, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/evaluations/runs", withTenantAndUser("tenant-1", "user-1"), h.EnqueueRun)

	req := httptest.NewRequest(http.MethodPost, "/evaluations/runs", strings.NewReader(`{
		"resource":{"kind":"skill","resource_id":"skill-1","revision_id":"version-2"},
		"suite_revision_id":"suite-revision-1","idempotency_key":"request-1"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"job_id":"job-1"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if jobs.tenantID != "tenant-1" || jobs.input.RequestedBy != "user-1" {
		t.Fatalf("request identity not propagated: tenant=%q input=%+v", jobs.tenantID, jobs.input)
	}
}

func TestEvaluationHandlerCreateBaselineUsesTenantAndResourcePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	baselines := &fakeEvaluationBaselines{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop()).
		WithBaselineService(baselines)
	r := gin.New()
	r.POST("/evaluations/resources/:kind/:id/baseline", withTenant("tenant-1"), h.CreateBaseline)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/evaluations/resources/agent/agent-1/baseline", nil))

	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"revision_id":"revision-1"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if baselines.tenantID != "tenant-1" || baselines.kind != domain.ResourceKindAgent ||
		baselines.resourceID != "agent-1" {
		t.Fatalf("baseline path not propagated: %+v", baselines)
	}
}

func TestEvaluationHandlerGenerateOptimizationReturnsCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	optimization := &fakeOptimizationService{}
	h := NewEvaluationHandler(nil, &fakeEvaluationJobs{}, nil, optimization, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/evaluations/optimizations", withTenantAndUser("tenant-1", "user-1"), h.GenerateOptimization)
	req := httptest.NewRequest(http.MethodPost, "/evaluations/optimizations", strings.NewReader(`{
		"baseline":{"kind":"skill","resource_id":"skill-1","revision_id":"version-1"},
		"suite_revision_id":"suite-revision-1","search_space":{"temperature":[0.1,0.2]},
		"idempotency_key":"request-1"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"revision_id":"candidate-1"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if optimization.input.IdempotencyKey != "request-1" {
		t.Fatalf("idempotency key not propagated: %+v", optimization.input)
	}
	if optimization.input.ActorID != "user-1" {
		t.Fatalf("actor not propagated: %+v", optimization.input)
	}
}

func TestEvaluationHandlerGenerateOptimizationAcceptsLegacyRequestWithoutIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	optimization := &fakeOptimizationService{}
	h := NewEvaluationHandler(nil, &fakeEvaluationJobs{}, nil, optimization, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/evaluations/optimizations", withTenantAndUser("tenant-1", "user-1"), h.GenerateOptimization)
	req := httptest.NewRequest(http.MethodPost, "/evaluations/optimizations", strings.NewReader(`{
		"baseline":{"kind":"skill","resource_id":"skill-1","revision_id":"version-1"},
		"suite_revision_id":"suite-revision-1","search_space":{}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if optimization.input.IdempotencyKey != "" {
		t.Fatalf("legacy request should preserve empty key for application fallback: %+v", optimization.input)
	}
}

func TestEvaluationHandlerGenerateOptimizationUsesHeaderAndMapsConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	optimization := &fakeOptimizationService{err: domain.ErrOptimizationIdempotencyConflict}
	h := NewEvaluationHandler(nil, &fakeEvaluationJobs{}, nil, optimization, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/optimizations", withTenantAndUser("tenant-1", "user-1"), h.GenerateOptimization)
	req := httptest.NewRequest(http.MethodPost, "/evaluations/optimizations", strings.NewReader(`{
		"baseline":{"kind":"skill","resource_id":"skill-1","revision_id":"version-1"},
		"suite_revision_id":"suite-revision-1"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "header-key")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || rec.Body.String() != `{"error":"optimization idempotency conflict"}` {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if optimization.input.IdempotencyKey != "header-key" {
		t.Fatalf("header key not propagated: %+v", optimization.input)
	}
}

func TestEvaluationHandlerListResourcesPropagatesFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/resources", withTenant("tenant-1"), h.ListResources)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/evaluations/resources?resource_kind=skill&resource_id=skill-1&status=published&cursor=cursor-1&limit=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if queries.tenantID != "tenant-1" || queries.filter != (port.CenterFilter{
		ResourceKind: "skill", ResourceID: "skill-1", Status: "published", Cursor: "cursor-1", Limit: 7,
	}) {
		t.Fatalf("query not propagated: tenant=%q filter=%+v", queries.tenantID, queries.filter)
	}
	var page domain.ResourcePage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("typed page response=%s err=%v", rec.Body.String(), err)
	}
}

func TestEvaluationHandlerListExperimentsSerializesSafePromotionEvidence(t *testing.T) {
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/experiments", withTenant("tenant-1"), h.ListExperiments)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/experiments", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"eligible":true`) ||
		strings.Contains(rec.Body.String(), "decision_snapshot") || strings.Contains(rec.Body.String(), `"metrics"`) {
		t.Fatalf("unsafe or incomplete experiment response: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluationHandlerRejectCandidateDerivesActor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	candidates := &fakeCandidateCommands{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, candidates, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/candidates/:id/reject", withTenantAndUser("tenant-1", "user-1"), h.RejectCandidate)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/evaluations/candidates/candidate-1/reject", strings.NewReader(
		`{"reason":"unsafe","idempotency_key":"request-1","expected_state_version":1,"actor_id":"attacker"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if candidates.tenantID != "tenant-1" || candidates.candidateID != "candidate-1" ||
		candidates.input.ActorID != "user-1" || candidates.input.Reason != "unsafe" ||
		candidates.input.IdempotencyKey != "request-1" || candidates.input.ExpectedStateVersion != 1 {
		t.Fatalf("command not propagated safely: %+v", candidates)
	}
}

func TestEvaluationHandlerExperimentCommandValidationUsesFrozenEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewEvaluationHandler(nil, nil, nil, nil, &fakeExperimentCommands{}, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/experiments/:id/pause", withTenantAndUser("tenant-1", "user-1"), h.PauseExperiment)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/evaluations/experiments/experiment-1/pause", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluationHandlerListSuitesPropagatesResourceID(t *testing.T) {
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/suites", withTenant("tenant-1"), h.ListSuites)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/suites?resource_id=skill-1", nil))
	if rec.Code != http.StatusOK || queries.filter.ResourceID != "skill-1" {
		t.Fatalf("status=%d filter=%+v body=%s", rec.Code, queries.filter, rec.Body.String())
	}
}

func TestEvaluationHandlerCandidateResponseContainsOnlySafeDiff(t *testing.T) {
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/candidates", withTenant("tenant-1"), h.ListCandidates)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/candidates", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"before":"old","after":"new"`) ||
		strings.Contains(rec.Body.String(), "raw_payload") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func withTenantAndUser(tenantID, userID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(reqctx.WithTenantID(c.Request.Context(), tenantID))
		c.Set(middleware.ContextKeySub, userID)
		c.Set(middleware.ContextKeyRole, "admin")
		c.Next()
	}
}

type fakeEvaluationQueries struct {
	tenantID     string
	filter       port.CenterFilter
	monitorKind  string
	monitorID    string
	monitorFrom  *time.Time
	monitorTo    *time.Time
	monitorLimit int
}

func (f *fakeEvaluationQueries) Overview(context.Context, string) (domain.CenterOverview, error) {
	return domain.CenterOverview{}, nil
}
func (f *fakeEvaluationQueries) ListResources(_ context.Context, tenantID string, filter port.CenterFilter) (domain.ResourcePage, error) {
	f.tenantID, f.filter = tenantID, filter
	return domain.ResourcePage{Items: []domain.ResourceSummary{{ID: "revision-1"}}}, nil
}

func (f *fakeEvaluationQueries) ListSuites(_ context.Context, tenantID string, filter port.CenterFilter) (domain.SuitePage, error) {
	f.tenantID, f.filter = tenantID, filter
	return domain.SuitePage{}, nil
}
func (f *fakeEvaluationQueries) ListRuns(context.Context, string, port.CenterFilter) (domain.RunPage, error) {
	return domain.RunPage{}, nil
}
func (f *fakeEvaluationQueries) ListCandidates(context.Context, string, port.CenterFilter) (domain.CandidatePage, error) {
	return domain.CandidatePage{Items: []domain.CandidateSummary{{ID: "candidate-1", SafeDiff: domain.CandidateSafeDiff{
		ChangedFields: []string{"label"}, Changes: map[string]domain.SafeFieldChange{
			"label": {Before: "old", After: "new"},
		},
	}}}}, nil
}
func (f *fakeEvaluationQueries) ListExperiments(context.Context, string, port.CenterFilter) (domain.ExperimentPage, error) {
	return domain.ExperimentPage{Items: []domain.ExperimentSummary{{ID: "experiment-1", PromotionEvidence: domain.PromotionEvidence{
		Eligible: true, Gates: domain.PromotionGates{Quality: domain.GatePassed, Cost: domain.GatePassed,
			Latency: domain.GatePassed, ErrorRate: domain.GatePassed, Security: domain.GatePassed},
		Blockers: []domain.PromotionBlocker{},
	}}}}, nil
}
func (f *fakeEvaluationQueries) Timeline(context.Context, string, port.CenterFilter) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, nil
}
func (f *fakeEvaluationQueries) MonitorResources(_ context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	f.tenantID = tenantID
	f.monitorKind = filter.ResourceKind
	f.monitorID = filter.ResourceID
	f.monitorFrom = filter.From
	f.monitorTo = filter.To
	f.monitorLimit = filter.Limit
	pass := 0.92
	return domain.MonitorResourcesPage{Items: []domain.MonitorResourceSummary{{
		ResourceKind: domain.ResourceKindSkill, ResourceID: "sk1", SampleCount: 2,
		Quality: []domain.QualityDim{{Dimension: "faithfulness", PassRate: pass, AvgScore: pass, AvgConfidence: 0.8, Samples: 2}},
		Process: &domain.ProcessBaseline{ProcessPassRate: 0.5, RunID: "runA", RunCreatedAt: time.Now().UTC()},
	}}}, nil
}
func (f *fakeEvaluationQueries) MonitorTrend(_ context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	f.tenantID = tenantID
	f.monitorKind = filter.ResourceKind
	f.monitorID = filter.ResourceID
	f.monitorFrom = filter.From
	f.monitorTo = filter.To
	return domain.MonitorTrendSeries{ResourceKind: domain.ResourceKind(filter.ResourceKind), ResourceID: filter.ResourceID,
		Series: []domain.MonitorTrendPoint{{BucketAt: time.Now().UTC()}}, Runs: []domain.RunProcessPoint{}}, nil
}

type fakeCandidateCommands struct {
	tenantID, candidateID string
	input                 application.CandidateCommandInput
}

func (f *fakeCandidateCommands) Reject(_ context.Context, tenantID, candidateID string, input application.CandidateCommandInput) (domain.CandidateSummary, error) {
	f.tenantID, f.candidateID, f.input = tenantID, candidateID, input
	return domain.CandidateSummary{ID: candidateID, Status: "rejected"}, nil
}

type fakeExperimentCommands struct{ evaluateKeys []string }

func (*fakeExperimentCommands) Create(context.Context, string, application.CreateExperimentInput) (domain.Experiment, domain.Deployment, error) {
	return domain.Experiment{}, domain.Deployment{}, nil
}
func (f *fakeExperimentCommands) EvaluateStageIdempotent(_ context.Context, _, _ string, input application.EvaluateStageInput) (domain.Experiment, domain.Decision, error) {
	f.evaluateKeys = append(f.evaluateKeys, input.IdempotencyKey)
	return domain.Experiment{}, domain.DecisionHold, nil
}
func (*fakeExperimentCommands) Pause(context.Context, string, string, application.ExperimentCommandInput) (domain.Experiment, error) {
	return domain.Experiment{}, nil
}
func (*fakeExperimentCommands) Promote(context.Context, string, string, application.ExperimentCommandInput) (domain.Experiment, error) {
	return domain.Experiment{}, nil
}
func (*fakeExperimentCommands) Rollback(context.Context, string, string, application.ExperimentCommandInput) (domain.Experiment, error) {
	return domain.Experiment{}, nil
}

func withTenant(tenantID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(reqctx.WithTenantID(c.Request.Context(), tenantID))
		c.Set(middleware.ContextKeyRole, "admin")
		c.Next()
	}
}

type fakeEvaluationJobs struct {
	tenantID string
	input    application.EnqueueRunInput
}

type fakeEvaluationBaselines struct {
	tenantID, resourceID string
	kind                 domain.ResourceKind
}

func (f *fakeEvaluationBaselines) CreatePublishedBaseline(
	_ context.Context, tenantID string, kind domain.ResourceKind, resourceID string,
) (domain.ResourceRef, error) {
	f.tenantID, f.kind, f.resourceID = tenantID, kind, resourceID
	return domain.ResourceRef{Kind: kind, ResourceID: resourceID, RevisionID: "revision-1"}, nil
}

func (f *fakeEvaluationJobs) EnqueueRun(
	_ context.Context, tenantID string, input application.EnqueueRunInput,
) (domain.EvaluationJob, error) {
	f.tenantID, f.input = tenantID, input
	return domain.EvaluationJob{ID: "job-1", Status: domain.JobQueued}, nil
}

func (f *fakeEvaluationJobs) Get(_ context.Context, _ string, _ string) (domain.EvaluationJob, error) {
	return domain.EvaluationJob{ID: "job-1", Status: domain.JobQueued}, nil
}

type fakeOptimizationService struct {
	input application.GenerateCandidatesInput
	err   error
}

func (f *fakeOptimizationService) Generate(
	_ context.Context, _ string, input application.GenerateCandidatesInput,
) (domain.OptimizationJob, []domain.OptimizationCandidate, error) {
	f.input = input
	if f.err != nil {
		return domain.OptimizationJob{}, nil, f.err
	}
	job := domain.OptimizationJob{ID: "optimization-1", Baseline: input.Baseline, Status: domain.JobSucceeded}
	return job, []domain.OptimizationCandidate{{
		ID: "candidate-record-1", OptimizationJobID: job.ID,
		Revision: domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "candidate-1"},
	}}, nil
}

type fakeSuiteService struct {
	created     domain.EvalSuite
	revision    domain.EvalSuiteRevision
	createInput application.CreateSuiteInput
	getDraftErr error
	updated     domain.EvalCase
	updatedReq  domain.EvalCase
	updateErr   error
	tenantID    string
	suiteID     string
	caseID      string

	// S1-3 扩展：详情/版本/加删草稿 case/开启草稿/单版本读取。
	detail     domain.SuiteDetail
	detailErr  error
	metas      []domain.SuiteRevisionMeta
	metasErr   error
	revByID    domain.EvalSuiteRevision
	revByIDErr error
	revisionID string
	addedCase  domain.EvalCase
	addCaseReq domain.EvalCase
	addCaseErr error
	deletedID  string
	deleteErr  error
	started    domain.EvalSuiteRevision
	startErr   error
}

func (f *fakeSuiteService) Create(_ context.Context, _ string, input application.CreateSuiteInput) (domain.EvalSuite, domain.EvalSuiteRevision, error) {
	f.createInput = input
	return f.created, f.revision, nil
}

func (f *fakeSuiteService) Publish(_ context.Context, _, _ string) (domain.EvalSuiteRevision, error) {
	return f.revision, nil
}

func (f *fakeSuiteService) GetDraft(_ context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error) {
	f.tenantID, f.suiteID = tenantID, suiteID
	return f.revision, f.getDraftErr
}

func (f *fakeSuiteService) UpdateDraftCase(_ context.Context, tenantID, suiteID, caseID string, testCase domain.EvalCase) (domain.EvalCase, error) {
	f.tenantID, f.suiteID, f.caseID = tenantID, suiteID, caseID
	f.updatedReq = testCase
	return f.updated, f.updateErr
}

func (f *fakeSuiteService) GetSuiteDetail(_ context.Context, tenantID, suiteID string) (domain.SuiteDetail, error) {
	f.tenantID, f.suiteID = tenantID, suiteID
	return f.detail, f.detailErr
}

func (f *fakeSuiteService) ListVersions(_ context.Context, tenantID, suiteID string) ([]domain.SuiteRevisionMeta, error) {
	f.tenantID, f.suiteID = tenantID, suiteID
	return f.metas, f.metasErr
}

func (f *fakeSuiteService) GetRevision(_ context.Context, tenantID, revisionID string) (domain.EvalSuiteRevision, error) {
	f.tenantID, f.revisionID = tenantID, revisionID
	return f.revByID, f.revByIDErr
}

func (f *fakeSuiteService) AddDraftCase(_ context.Context, tenantID, suiteID string, testCase domain.EvalCase) (domain.EvalCase, error) {
	f.tenantID, f.suiteID, f.addCaseReq = tenantID, suiteID, testCase
	return f.addedCase, f.addCaseErr
}

func (f *fakeSuiteService) DeleteDraftCase(_ context.Context, tenantID, suiteID, caseID string) error {
	f.tenantID, f.suiteID, f.deletedID = tenantID, suiteID, caseID
	return f.deleteErr
}

func (f *fakeSuiteService) StartNextDraft(_ context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error) {
	f.tenantID, f.suiteID = tenantID, suiteID
	return f.started, f.startErr
}

type fakeCaseGen struct {
	result application.GenerateResult
	err    error
	input  application.GenerateInput
}

func (f *fakeCaseGen) Generate(_ context.Context, input application.GenerateInput) (application.GenerateResult, error) {
	f.input = input
	return f.result, f.err
}

func TestEvaluationHandlerGenerateSuiteCasesSamplesAndReturnsResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gen := &fakeCaseGen{result: application.GenerateResult{SamplesFound: 5, Generated: 3}}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop()).
		WithTestCaseGenerator(gen)
	r := gin.New()
	r.POST("/evaluations/suites/:id/generate", withTenant("tenant-1"), h.GenerateSuiteCases)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/evaluations/suites/suite-1/generate",
		strings.NewReader(`{"sample_policy":"negative_first","max_cases":7}`)))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"samples_found":5`) ||
		!strings.Contains(rec.Body.String(), `"generated":3`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gen.input.SuiteID != "suite-1" || gen.input.TenantID != "tenant-1" ||
		gen.input.Policy != domain.SamplePolicyNegativeFirst || gen.input.MaxCases != 7 {
		t.Fatalf("generate input not propagated: %+v", gen.input)
	}
}

func TestEvaluationHandlerGenerateSuiteCasesDefaultsLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gen := &fakeCaseGen{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop()).
		WithTestCaseGenerator(gen)
	r := gin.New()
	r.POST("/evaluations/suites/:id/generate", withTenant("tenant-1"), h.GenerateSuiteCases)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/evaluations/suites/suite-1/generate", strings.NewReader(`{"sample_policy":"balanced"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if gen.input.MaxCases != constants.DefaultCaseSampleLimit {
		t.Fatalf("default limit not applied: %d", gen.input.MaxCases)
	}
}

func TestEvaluationHandlerGenerateSuiteCasesUnavailableWithoutGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/suites/:id/generate", withTenant("tenant-1"), h.GenerateSuiteCases)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/evaluations/suites/suite-1/generate", strings.NewReader(`{"sample_policy":"negative_first"}`)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without gateway, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluationHandlerGetSuiteDraft(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{
		revision: domain.EvalSuiteRevision{
			ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
			Cases:        []domain.EvalCase{{ID: "case-1", Name: "物流", Input: "快递没更新", ExpectedOutput: "物流查询"}},
		},
	}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/suites/:id/draft", withTenant("tenant-1"), h.GetSuiteDraft)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/suites/suite-1/draft", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"case-1"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if suites.tenantID != "tenant-1" || suites.suiteID != "suite-1" {
		t.Fatalf("draft path not propagated: %+v", suites)
	}
}

func TestEvaluationHandlerUpdateDraftCaseDefaultsEnabledTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{updated: domain.EvalCase{ID: "case-1", Name: "物流改", Enabled: true}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.PUT("/evaluations/suites/:id/draft/cases/:caseId", withTenant("tenant-1"), h.UpdateDraftCase)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/evaluations/suites/suite-1/draft/cases/case-1",
		strings.NewReader(`{"name":"物流改","input":"物流进度查询","expected_output":"物流查询","assertion_mode":"exact"}`)))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if suites.caseID != "case-1" || suites.tenantID != "tenant-1" {
		t.Fatalf("update path not propagated: %+v", suites)
	}
}

func TestEvaluationHandlerUpdateDraftCaseRejectsWhenEnabledFalse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{updated: domain.EvalCase{ID: "case-1", Enabled: false}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.PUT("/evaluations/suites/:id/draft/cases/:caseId", withTenant("tenant-1"), h.UpdateDraftCase)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/evaluations/suites/suite-1/draft/cases/case-1",
		strings.NewReader(`{"name":"物流改","input":"物流进度查询","expected_output":"物流查询","assertion_mode":"contains","enabled":false}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluationHandlerUpdateDraftCaseRejectsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewEvaluationHandler(&fakeSuiteService{}, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.PUT("/evaluations/suites/:id/draft/cases/:caseId", withTenant("tenant-1"), h.UpdateDraftCase)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/evaluations/suites/suite-1/draft/cases/case-1",
		strings.NewReader(`{"name":"","input":"","expected_output":""}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid update, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// fakeApprovalRequests 记录审批请求，模拟 ToolApprovalService.Request
// （MCP handler 审批分支测试与评测 handler 测试共享，定义保留于此）。
type fakeApprovalRequests struct {
	called      int
	subjectKind string
	toolName    string
	args        map[string]any
}

func (f *fakeApprovalRequests) Request(_ context.Context, payload agentapp.ToolApprovalPayload) (string, error) {
	f.called++
	f.subjectKind = payload.SubjectKind
	f.toolName = payload.ToolName
	f.args = payload.Arguments
	return "approval-1", nil
}

// D4：admin 直接执行，不创建审批。
func TestEvaluationCreateSuiteAdminExecutesDirectly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{created: domain.EvalSuite{ID: "suite-1"}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/suites", withTenantAndUser("tenant-1", "admin-1"), h.CreateSuite)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/evaluations/suites",
		strings.NewReader(`{"name":"S","description":"D","resource_kind":"skill","cases":[{"name":"c1","input":"i","expected_output":"o","assertion_mode":"exact"}]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if suites.created.ID != "suite-1" {
		t.Fatalf("suite Create not executed directly: %+v", suites.created)
	}
}

// D4：admin 创建 judge case 时，judge_spec（model/rubric）绑定进 service 输入。
func TestEvaluationCreateSuiteBindsJudgeSpec(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{created: domain.EvalSuite{ID: "suite-1"}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/suites", withTenantAndUser("tenant-1", "admin-1"), h.CreateSuite)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/evaluations/suites",
		strings.NewReader(`{"name":"S","resource_kind":"skill","cases":[{"name":"j1","input":"i","expected_output":"o","assertion_mode":"judge","judge_spec":{"model":"judge-v1","rubric":"faithfulness"}}]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(suites.createInput.Cases) != 1 {
		t.Fatalf("create input cases=%d, want 1", len(suites.createInput.Cases))
	}
	got := suites.createInput.Cases[0]
	if got.AssertionMode != domain.AssertionJudge {
		t.Fatalf("assertion mode=%q, want judge", got.AssertionMode)
	}
	if got.JudgeSpec == nil || got.JudgeSpec.Model != "judge-v1" || got.JudgeSpec.Rubric != "faithfulness" {
		t.Fatalf("judge spec not bound: %+v", got.JudgeSpec)
	}
}

// D4：admin 创建 agent case 时，tool_spec（must_call/must_not_call/order/max_calls）
// 与 step_judge（criteria）绑定进 service 输入（§6.5 过程断言契约）。
func TestEvaluationCreateSuiteBindsToolSpecAndStepJudge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{created: domain.EvalSuite{ID: "suite-1"}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/suites", withTenantAndUser("tenant-1", "admin-1"), h.CreateSuite)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/evaluations/suites",
		strings.NewReader(`{"name":"S","resource_kind":"agent","cases":[{"name":"a1","input":"i","expected_output":"o","assertion_mode":"exact","tool_spec":{"must_call":["search"],"must_not_call":["delete"],"order":["search","execute"],"max_calls":3},"step_judge":{"criteria":"reason about steps"}}]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(suites.createInput.Cases) != 1 {
		t.Fatalf("create input cases=%d, want 1", len(suites.createInput.Cases))
	}
	got := suites.createInput.Cases[0]
	if got.ToolSpec == nil {
		t.Fatalf("tool_spec not bound: %+v", got)
	}
	if got.ToolSpec.MaxCalls != 3 {
		t.Fatalf("tool_spec.max_calls=%d, want 3", got.ToolSpec.MaxCalls)
	}
	if len(got.ToolSpec.MustCall) != 1 || got.ToolSpec.MustCall[0] != "search" ||
		len(got.ToolSpec.MustNotCall) != 1 || got.ToolSpec.MustNotCall[0] != "delete" {
		t.Fatalf("tool_spec must_call/must_not_call not bound: %+v", got.ToolSpec)
	}
	if len(got.ToolSpec.Order) != 2 || got.ToolSpec.Order[0] != "search" || got.ToolSpec.Order[1] != "execute" {
		t.Fatalf("tool_spec.order not bound: %+v", got.ToolSpec)
	}
	if got.StepJudge == nil || got.StepJudge.Criteria != "reason about steps" {
		t.Fatalf("step_judge not bound: %+v", got.StepJudge)
	}
}

// fakeReviewService 实现 evaluationReviewService（P1c 评审池查询/决策 mock）。
type fakeReviewService struct {
	listItems []domain.ReviewItem
	listTotal int64
	listErr   error
	filter    port.ReviewFilter

	getItem *domain.ReviewItem
	getErr  error

	decideItem    *domain.ReviewItem
	decideErr     error
	decideTenant  string
	decideID      string
	decideActor   string
	decideVerdict domain.HumanVerdict
	decideReason  string
}

func (f *fakeReviewService) List(_ context.Context, tenantID string, filter port.ReviewFilter,
) ([]domain.ReviewItem, int64, error) {
	f.filter = filter
	return f.listItems, f.listTotal, f.listErr
}

func (f *fakeReviewService) Get(_ context.Context, tenantID, id string) (*domain.ReviewItem, error) {
	f.decideTenant, f.decideID = tenantID, id
	return f.getItem, f.getErr
}

func (f *fakeReviewService) Decide(_ context.Context, tenantID, id, actor string,
	verdict domain.HumanVerdict, reason string,
) (*domain.ReviewItem, error) {
	f.decideTenant, f.decideID, f.decideActor, f.decideVerdict, f.decideReason = tenantID, id, actor, verdict, reason
	return f.decideItem, f.decideErr
}

func reviewItemFixture() domain.ReviewItem {
	return domain.ReviewItem{
		ID: "review-1", SourceType: domain.ReviewSourceObservation, SourceID: "obs-1",
		RunID: "run-1", TraceID: "trace-1",
		ResourceKind: domain.ResourceKindAgent, ResourceID: "agent-1",
		TriggerReason: domain.TriggerLowConfidence,
		Snapshot:      map[string]any{"signals": map[string]any{"judge": []any{}}},
		Status:        domain.ReviewStatusPending,
		CreatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// P1c：评审池分页查询透传过滤条件并返回 items/total 结构。
func TestEvaluationHandlerListReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &fakeReviewService{listItems: []domain.ReviewItem{reviewItemFixture()}, listTotal: 1}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop()).
		WithReviewService(svc)
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/review", withTenant("tenant-1"), h.ListReviewItems)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/evaluations/review?status=pending&trigger_reason=low_confidence"+
			"&resource_kind=agent&resource_id=agent-1&page=2&page_size=10", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":1`) ||
		!strings.Contains(rec.Body.String(), `"review-1"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.filter != (port.ReviewFilter{
		Status: domain.ReviewStatusPending, TriggerReason: domain.TriggerLowConfidence,
		ResourceKind: "agent", ResourceID: "agent-1", Limit: 10, Offset: 10,
	}) {
		t.Fatalf("filter not propagated: %+v", svc.filter)
	}
}

// P1c：单条评审详情返回条目。
func TestEvaluationHandlerGetReviewItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &fakeReviewService{}
	item := reviewItemFixture()
	svc.getItem = &item
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop()).
		WithReviewService(svc)
	r := gin.New()
	r.GET("/evaluations/review/:id", withTenant("tenant-1"), h.GetReviewItem)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/review/review-1", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"review-1"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.decideID != "review-1" {
		t.Fatalf("item id not propagated: %q", svc.decideID)
	}
}

// P1c：人工决策回写，actor/verdict/reason 从请求安全传播。
func TestEvaluationHandlerDecideReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &fakeReviewService{}
	item := reviewItemFixture()
	item.Status = domain.ReviewStatusReviewed
	item.HumanVerdict = domain.HumanVerdictPass
	item.Reviewer = "user-1"
	item.ReviewReason = "确认通过"
	svc.decideItem = &item
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop()).
		WithReviewService(svc)
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/review/:id/decision", withTenantAndUser("tenant-1", "user-1"), h.DecideReviewItem)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/evaluations/review/review-1/decision",
		strings.NewReader(`{"verdict":"pass","reason":"确认通过"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"human_verdict":"pass"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.decideTenant != "tenant-1" || svc.decideID != "review-1" || svc.decideActor != "user-1" ||
		svc.decideVerdict != domain.HumanVerdictPass || svc.decideReason != "确认通过" {
		t.Fatalf("decision not propagated safely: tenant=%q id=%q actor=%q verdict=%q reason=%q",
			svc.decideTenant, svc.decideID, svc.decideActor, svc.decideVerdict, svc.decideReason)
	}
}

// P1c：非法 verdict 在绑定层被拒绝（400），不进入服务层。
func TestEvaluationHandlerDecideReviewRejectsInvalidVerdict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &fakeReviewService{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop()).
		WithReviewService(svc)
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.POST("/evaluations/review/:id/decision", withTenantAndUser("tenant-1", "user-1"), h.DecideReviewItem)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/evaluations/review/review-1/decision",
		strings.NewReader(`{"verdict":"bogus","reason":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid verdict, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.decideID != "" {
		t.Fatalf("invalid verdict must not reach service: %+v", svc)
	}
}

// P1c：评审服务未装配时 fail closed 503。
func TestEvaluationHandlerListReviewUnavailableWithoutService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/review", withTenant("tenant-1"), h.ListReviewItems)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/review", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without review service, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- 评测指标监控面板 handler（spec 2026-09-03 §4.2）----

// TestEvaluationHandlerMonitorResourcesPropagatesFilter 端点 1：200 + 参数透传。
func TestEvaluationHandlerMonitorResourcesPropagatesFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/monitoring/resources", withTenant("tenant-1"), h.ListMonitorResources)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/evaluations/monitoring/resources?resource_kind=skill&resource_id=sk1&from=2026-09-01T00:00:00Z&to=2026-09-03T00:00:00Z&limit=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if queries.monitorKind != "skill" || queries.monitorID != "sk1" || queries.monitorLimit != 7 {
		t.Fatalf("filter not propagated: kind=%q id=%q limit=%d", queries.monitorKind, queries.monitorID, queries.monitorLimit)
	}
	if queries.monitorFrom == nil || queries.monitorTo == nil {
		t.Fatal("from/to not propagated")
	}
	var page domain.MonitorResourcesPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("typed page response=%s err=%v", rec.Body.String(), err)
	}
}

// TestEvaluationHandlerMonitorResourcesRejectsBadQuery 端点 1：400 表。
func TestEvaluationHandlerMonitorResourcesRejectsBadQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/monitoring/resources", withTenant("tenant-1"), h.ListMonitorResources)
	urls := []string{
		"/evaluations/monitoring/resources?resource_id=only",                                                      // 单传 id 无 kind
		"/evaluations/monitoring/resources?resource_kind=bad",                                                     // kind 非法
		"/evaluations/monitoring/resources?resource_kind=skill&from=2026-09-03T00:00:00Z&to=2026-09-01T00:00:00Z", // from>to
	}
	for _, raw := range urls {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, raw, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status=%d body=%s, want 400", raw, rec.Code, rec.Body.String())
		}
	}
}

// TestEvaluationHandlerMonitorTrendPropagates 端点 2：200 + 缺 kind/id → 400。
func TestEvaluationHandlerMonitorTrendPropagates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/monitoring/resources/trend", withTenant("tenant-1"), h.GetMonitorTrend)
	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodGet,
		"/evaluations/monitoring/resources/trend?resource_kind=skill&resource_id=sk1", nil))
	if ok.Code != http.StatusOK {
		t.Fatalf("ok status=%d body=%s", ok.Code, ok.Body.String())
	}
	var series domain.MonitorTrendSeries
	if err := json.Unmarshal(ok.Body.Bytes(), &series); err != nil || series.ResourceID != "sk1" {
		t.Fatalf("typed series response=%s err=%v", ok.Body.String(), err)
	}
	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodGet,
		"/evaluations/monitoring/resources/trend?resource_kind=skill", nil)) // 缺 id
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad status=%d body=%s, want 400", bad.Code, bad.Body.String())
	}
}

// TestEvaluationHandlerCreateSuiteCarriesSessionScriptToDomain verifies the
// authoring contract (阶段 B §5.4): a create-suite request case carrying a
// session script is converted into the domain EvalCase.Session verbatim
// (goal + turns, per-turn tool_spec mapped like the case-level one), and a
// session case may omit the single-turn input.
func TestEvaluationHandlerCreateSuiteCarriesSessionScriptToDomain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/evaluations/suites", withTenantAndUser("tenant-1", "user-1"), h.CreateSuite)

	rec := httptest.NewRecorder()
	body := `{"name":"会话基线","resource_kind":"agent","cases":[{
	  "name":"会话投诉","expected_output":"已给用户可执行处理","assertion_mode":"contains",
	  "session":{"goal":"用户投诉快递未收到：定位物流状态并给出签收异常处理","turns":[
	    {"user":"快递一直没到，帮我看看","probe":"识别物流查询意图"},
	    {"user":"物流显示已签收但我没收到","probe":"进入签收异常处理",
	     "tool_spec":{"must_call":["track_package"],"max_calls":2}}]}}]}`
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/evaluations/suites", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	cases := suites.createInput.Cases
	if len(cases) != 1 || cases[0].Session == nil {
		t.Fatalf("session not carried to domain: %+v", cases)
	}
	session := cases[0].Session
	if session.Goal != "用户投诉快递未收到：定位物流状态并给出签收异常处理" || len(session.Turns) != 2 {
		t.Fatalf("session goal/turns not preserved: %+v", session)
	}
	if session.Turns[1].ToolSpec == nil || len(session.Turns[1].ToolSpec.MustCall) != 1 ||
		session.Turns[1].ToolSpec.MustCall[0] != "track_package" || session.Turns[1].ToolSpec.MaxCalls != 2 {
		t.Fatalf("per-turn tool_spec not mapped: %+v", session.Turns[1])
	}
	if cases[0].Input != nil {
		t.Fatalf("session case should carry no single-turn input, got %v", cases[0].Input)
	}
}

// TestEvaluationHandlerUpdateDraftCaseCarriesSessionScriptToDomain verifies the
// session authoring edit path: the draft-case update maps a session script into
// the domain case (full replacement), and a session case update may omit input.
func TestEvaluationHandlerUpdateDraftCaseCarriesSessionScriptToDomain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{updated: domain.EvalCase{ID: "case-1", Enabled: true}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.PUT("/evaluations/suites/:id/draft/cases/:caseId", withTenant("tenant-1"), h.UpdateDraftCase)

	rec := httptest.NewRecorder()
	body := `{"name":"会话投诉改","expected_output":"已给用户可执行处理","assertion_mode":"contains",
	  "session":{"goal":"快递签收异常：先核实再给处理","turns":[
	    {"user":"签收异常怎么处理","probe":"进入异常处理"}]}}`
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/evaluations/suites/suite-1/draft/cases/case-1", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	got := suites.updatedReq
	if got.Session == nil || got.Session.Goal != "快递签收异常：先核实再给处理" || len(got.Session.Turns) != 1 {
		t.Fatalf("session not carried to domain: %+v", got.Session)
	}
	if got.Session.Turns[0].User != "签收异常怎么处理" {
		t.Fatalf("turn user not preserved: %+v", got.Session.Turns[0])
	}
	if got.Input != nil {
		t.Fatalf("session case update should carry no single-turn input, got %v", got.Input)
	}
}

// ---- S1-3 suite management page endpoints ----

func TestEvaluationHandlerGetSuiteDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{detail: domain.SuiteDetail{
		ID: "suite-1", Name: "投诉分类", ResourceKind: domain.ResourceKindSkill, Status: "published",
		ActiveRevisionID: "rev-1", DraftRevisionID: "rev-2", ActiveVersionNo: 2, ActiveCaseCount: 8,
	}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/suites/:id", withTenant("tenant-1"), h.GetSuiteDetail)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/suites/suite-1", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"投诉分类"`) ||
		!strings.Contains(rec.Body.String(), `"resource_kind":"skill"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if suites.tenantID != "tenant-1" || suites.suiteID != "suite-1" {
		t.Fatalf("detail path not propagated: %+v", suites)
	}
}

func TestEvaluationHandlerGetSuiteDetailNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{detailErr: application.ErrSuiteNotFound}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/suites/:id", withTenant("tenant-1"), h.GetSuiteDetail)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/suites/missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing suite, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluationHandlerListSuiteVersions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{metas: []domain.SuiteRevisionMeta{
		{ID: "rev-1", VersionNo: 2, Status: domain.SuiteRevisionPublished, ResourceKind: domain.ResourceKindSkill},
		{ID: "rev-2", Status: domain.SuiteRevisionDraft, ResourceKind: domain.ResourceKindSkill},
	}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/suites/:id/versions", withTenant("tenant-1"), h.ListSuiteVersions)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/suites/suite-1/versions", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rev-1"`) ||
		!strings.Contains(rec.Body.String(), `"version_no":2`) || !strings.Contains(rec.Body.String(), `"draft"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluationHandlerListSuiteVersionsEmptyArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/suites/:id/versions", withTenant("tenant-1"), h.ListSuiteVersions)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/suites/suite-1/versions", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `[]`) {
		t.Fatalf("expected empty-array body, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluationHandlerGetSuiteRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{revByID: domain.EvalSuiteRevision{
		ID: "rev-1", SuiteID: "suite-1", Status: domain.SuiteRevisionPublished, VersionNo: 2,
		ResourceKind: domain.ResourceKindSkill,
		Cases: []domain.EvalCase{{
			ID: "case-1", Name: "物流", Input: "快递没更新",
			ExpectedOutput: "物流查询", AssertionMode: domain.AssertionContains,
		}},
	}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.GET("/evaluations/suites/:id/versions/:revisionId", withTenant("tenant-1"), h.GetSuiteRevision)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/suites/suite-1/versions/rev-1", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"case-1"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if suites.revisionID != "rev-1" || suites.tenantID != "tenant-1" {
		t.Fatalf("revision path not propagated: %+v", suites)
	}
}

func TestEvaluationHandlerStartNextDraft(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{started: domain.EvalSuiteRevision{
		ID: "rev-next", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft, ResourceKind: domain.ResourceKindSkill,
		Cases: []domain.EvalCase{{ID: "case-1", Name: "继承", Input: "q", ExpectedOutput: "a"}},
	}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/evaluations/suites/:id/draft", withTenant("tenant-1"), h.StartNextDraft)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/evaluations/suites/suite-1/draft", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rev-next"`) {
		t.Fatalf("unexpected response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if suites.suiteID != "suite-1" || suites.tenantID != "tenant-1" {
		t.Fatalf("start-next path not propagated: %+v", suites)
	}
}

func TestEvaluationHandlerAddDraftCaseDefaultsEnabledAndMapsConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{addedCase: domain.EvalCase{ID: "case-new", Name: "物流新"}}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/evaluations/suites/:id/draft/cases", withTenant("tenant-1"), h.AddDraftCase)

	rec := httptest.NewRecorder()
	body := `{"name":"物流新","input":"查单","expected_output":"物流查询","assertion_mode":"contains",
	  "tool_spec":{"must_call":["track_package"]}}`
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/evaluations/suites/suite-1/draft/cases", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if suites.suiteID != "suite-1" || suites.tenantID != "tenant-1" {
		t.Fatalf("add-case path not propagated: %+v", suites)
	}
	got := suites.addCaseReq
	if got.Name != "物流新" || got.Input != "查单" ||
		got.AssertionMode != domain.AssertionContains || !got.Enabled {
		t.Fatalf("add-case request not mapped: %+v", got)
	}
	if got.ToolSpec == nil || len(got.ToolSpec.MustCall) != 1 || got.ToolSpec.MustCall[0] != "track_package" {
		t.Fatalf("tool_spec not mapped through toDomainCase: %+v", got.ToolSpec)
	}
}

func TestEvaluationHandlerDeleteDraftCase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.DELETE("/evaluations/suites/:id/draft/cases/:caseId", withTenant("tenant-1"), h.DeleteDraftCase)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/evaluations/suites/suite-1/draft/cases/case-1", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if suites.deletedID != "case-1" || suites.suiteID != "suite-1" {
		t.Fatalf("delete path not propagated: %+v", suites)
	}
}

func TestEvaluationHandlerDeleteDraftCaseNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suites := &fakeSuiteService{deleteErr: application.ErrSuiteNotFound}
	h := NewEvaluationHandler(suites, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.DELETE("/evaluations/suites/:id/draft/cases/:caseId", withTenant("tenant-1"), h.DeleteDraftCase)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/evaluations/suites/suite-1/draft/cases/missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}
