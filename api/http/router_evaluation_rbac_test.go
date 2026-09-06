package http

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/api/middleware"
	"github.com/byteBuilderX/stratum/api/wiring"
	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/application"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	iamdomain "github.com/byteBuilderX/stratum/internal/iam/domain"
	iamport "github.com/byteBuilderX/stratum/internal/iam/domain/port"
	iamtoken "github.com/byteBuilderX/stratum/internal/iam/infrastructure/token"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestEvaluationEvolutionRoutesRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokens := iamtoken.NewJWTService(key)
	queryRepo := &evaluationQueryRepoFake{}
	experimentRepo := &evaluationExperimentRepoFake{}
	candidateRepo := &evaluationCandidateRepoFake{}
	deleteRepo := &evaluationDeleteRepoFake{createdBy: map[string]string{"suite-1": "user-1"}}
	deleteSvc := application.NewDeleteService(
		&evaluationRoleFake{roles: map[string]string{"user-1": "admin", "owner-1": "owner"}},
		deleteRepo, deleteRepo, deleteRepo, deleteRepo, deleteRepo, deleteRepo, deleteRepo,
	)
	c := &wiring.Container{Logger: zap.NewNop(), Platform: &wiring.Platform{JWTService: tokens}, Evaluation: &wiring.Evaluation{
		SuiteService: application.NewSuiteService(nil), JobService: application.NewJobService(nil, nil, nil),
		QueryService: application.NewQueryService(queryRepo), ExperimentService: application.NewExperimentService(experimentRepo),
		CandidateService: application.NewCandidateCommandService(candidateRepo), DeleteService: deleteSvc,
	}}
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	requireActive := func(c *gin.Context) {
		if c.GetHeader("X-Tenant-Status") == "inactive" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "tenant is not active"})
			return
		}
		c.Next()
	}
	registerEvaluations(r, c, requireActive)

	member := signEvaluationToken(t, tokens, "tenant-1", "member")
	for _, path := range []string{"/evaluations/resources", "/evaluations/suites",
		"/evaluations/experiments",
		"/evaluations/resources/skill/skill-1/timeline"} {
		rec := performEvaluationRequest(r, http.MethodGet, path, member, "", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("member GET %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	// 读放开：overview/runs/candidates 对 member 同样可见（推翻 D6）。
	for _, path := range []string{"/evaluations/overview", "/evaluations/runs", "/evaluations/candidates"} {
		rec := performEvaluationRequest(r, http.MethodGet, path, member, "", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("member GET %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	// 写收紧：member 写操作被 requireAdmin 拒绝 403，repo 不执行。
	commandBody := `{"reason":"reviewed","idempotency_key":"request-1","expected_state_version":1}`
	for _, path := range []string{"/evaluations/candidates/candidate-1/reject", "/evaluations/experiments/experiment-1/pause",
		"/evaluations/experiments/experiment-1/promote", "/evaluations/experiments/experiment-1/rollback"} {
		rec := performEvaluationRequest(r, http.MethodPost, path, member, "", strings.NewReader(commandBody))
		if rec.Code != http.StatusForbidden {
			t.Errorf("member POST %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if candidateRepo.actorID != "" || len(experimentRepo.actors) != 0 {
		t.Fatalf("member write must not execute: candidate=%q experiments=%v", candidateRepo.actorID, experimentRepo.actors)
	}
	admin := signEvaluationToken(t, tokens, "tenant-1", "admin")
	// Admin retains read access to the moved endpoints.
	for _, path := range []string{"/evaluations/overview", "/evaluations/runs", "/evaluations/candidates"} {
		rec := performEvaluationRequest(r, http.MethodGet, path, admin, "", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("admin GET %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	for _, route := range r.Routes() {
		if route.Method == http.MethodPost && route.Path == "/evaluations/experiments/:id/evaluate" {
			t.Fatal("client-reported experiment metrics route must not be registered")
		}
	}
	rec := performEvaluationRequest(r, http.MethodPost, "/evaluations/experiments/experiment-1/pause", admin, "inactive", strings.NewReader(`{}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("inactive admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/evaluations/candidates/candidate-1/reject",
		"/evaluations/experiments/experiment-1/pause", "/evaluations/experiments/experiment-1/promote",
		"/evaluations/experiments/experiment-1/rollback"} {
		rec = performEvaluationRequest(r, http.MethodPost, path, admin, "", strings.NewReader(commandBody))
		if rec.Code != http.StatusOK {
			t.Errorf("admin POST %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if candidateRepo.actorID != "user-1" || len(experimentRepo.actors) != 3 {
		t.Fatalf("authenticated actors not propagated: candidate=%q experiments=%v", candidateRepo.actorID, experimentRepo.actors)
	}

	// owner：读放开依旧可见；写命令与 admin 同档放行（requireAdmin rank owner>admin）。
	owner := signEvaluationTokenAs(t, tokens, "tenant-1", "owner-1", "owner")
	rec = performEvaluationRequest(r, http.MethodGet, "/evaluations/overview", owner, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner GET overview: status=%d body=%s", rec.Code, rec.Body.String())
	}
	adminActorCount := len(experimentRepo.actors)
	rec = performEvaluationRequest(r, http.MethodPost, "/evaluations/experiments/experiment-1/pause", owner, "",
		strings.NewReader(commandBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner POST pause: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(experimentRepo.actors) != adminActorCount+1 {
		t.Fatalf("owner command actor not propagated: experiments=%v", experimentRepo.actors)
	}

	// DELETE 端点 RBAC：member 被 requireAdmin 拦截 403 且 repo 不执行；
	// owner 恒 204；admin 仅创建者可删（suite-1 由 user-1 创建），非创建者
	// （suite-other，createdBy=stranger）服务层 fail-closed 403。
	deletePaths := []string{"/evaluations/suites/suite-1", "/evaluations/runs/run-1",
		"/evaluations/jobs/job-1", "/evaluations/experiments/experiment-1",
		"/evaluations/candidates/candidate-1", "/evaluations/review/review-1",
		"/evaluations/feedback/feedback-1"}
	for _, path := range deletePaths {
		rec = performEvaluationRequest(r, http.MethodDelete, path, member, "", nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("member DELETE %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if len(deleteRepo.deleted) != 0 {
		t.Fatalf("member DELETE must not reach service: deleted=%v", deleteRepo.deleted)
	}
	for _, path := range deletePaths {
		rec = performEvaluationRequest(r, http.MethodDelete, path, owner, "", nil)
		if rec.Code != http.StatusNoContent {
			t.Errorf("owner DELETE %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if len(deleteRepo.deleted) != len(deletePaths) {
		t.Fatalf("owner DELETE must reach service: deleted=%v", deleteRepo.deleted)
	}
	rec = performEvaluationRequest(r, http.MethodDelete, "/evaluations/suites/suite-1", admin, "", nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("creator DELETE suite-1: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performEvaluationRequest(r, http.MethodDelete, "/evaluations/suites/suite-other", admin, "", nil)
	if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
		t.Fatalf("admin non-creator DELETE: status=%d body=%s", rec.Code, rec.Body.String())
	}

	other := signEvaluationToken(t, tokens, "tenant-2", "member")
	rec = performEvaluationRequest(r, http.MethodGet, "/evaluations/resources/skill/skill-1/timeline", other, "", nil)
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
		t.Fatalf("cross tenant status=%d body=%s", rec.Code, rec.Body.String())
	}
	otherAdmin := signEvaluationToken(t, tokens, "tenant-2", "admin")
	rec = performEvaluationRequest(r, http.MethodPost, "/evaluations/experiments/missing/pause", otherAdmin, "",
		strings.NewReader(commandBody))
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
		t.Fatalf("cross tenant command status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performEvaluationRequest(r, http.MethodPost, "/evaluations/experiments/conflict/pause", admin, "",
		strings.NewReader(commandBody))
	if rec.Code != http.StatusConflict || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
		t.Fatalf("conflict status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type evaluationCandidateRepoFake struct{ actorID string }

func (r *evaluationCandidateRepoFake) Reject(_ context.Context, tenantID, candidateID string,
	command domain.CandidateCommand) (domain.CandidateSummary, error) {
	if tenantID != "tenant-1" {
		return domain.CandidateSummary{}, domain.ErrCandidateNotFound
	}
	r.actorID = command.ActorID
	return domain.CandidateSummary{ID: candidateID, Status: "rejected"}, nil
}

type evaluationExperimentRepoFake struct{ actors []string }

func (*evaluationExperimentRepoFake) ValidatePrerequisites(context.Context, string, domain.ResourceRef,
	domain.ResourceRef, string) error {
	return nil
}
func (*evaluationExperimentRepoFake) Create(context.Context, string, domain.Experiment, domain.Deployment,
	*auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (*evaluationExperimentRepoFake) Get(context.Context, string, string) (domain.Experiment, bool, error) {
	return domain.Experiment{}, false, nil
}
func (*evaluationExperimentRepoFake) SaveDecision(context.Context, string, domain.Experiment, domain.Decision,
	domain.StageMetrics, string, string) (domain.Experiment, domain.Decision, error) {
	return domain.Experiment{}, domain.DecisionHold, nil
}
func (r *evaluationExperimentRepoFake) ApplyCommand(_ context.Context, tenantID, experimentID string,
	_ domain.ExperimentCommandAction, command domain.ExperimentCommand) (domain.Experiment, error) {
	if tenantID != "tenant-1" || experimentID == "missing" {
		return domain.Experiment{}, application.ErrExperimentNotFound
	}
	if experimentID == "conflict" {
		return domain.Experiment{}, domain.ErrExperimentStateConflict
	}
	r.actors = append(r.actors, command.ActorID)
	return domain.Experiment{ID: experimentID}, nil
}
func (*evaluationExperimentRepoFake) ResolveDeployment(context.Context, string, string, string) (domain.Deployment, bool, error) {
	return domain.Deployment{}, false, nil
}
func (*evaluationExperimentRepoFake) HasRunningExperiment(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (*evaluationExperimentRepoFake) ListPendingExperiments(context.Context, string, string, string) ([]domain.Experiment, error) {
	return nil, nil
}
func (*evaluationExperimentRepoFake) ListRunningExperiments(context.Context, string) ([]domain.Experiment, error) {
	return nil, nil
}

// evaluationRoleFake 按 actorID 返回固定角色；未注册 actor 返回空串（fail-closed）。
type evaluationRoleFake struct{ roles map[string]string }

func (f *evaluationRoleFake) ResolveTenantRole(_ context.Context, _, actorID string) (string, error) {
	return f.roles[actorID], nil
}

// evaluationDeleteRepoFake 同时满足 7 个删除仓库接口：Get*CreatedBy 默认存在
// （owner 兜底可删），Delete* 记录 resourceID。route 级 RBAC 断言聚焦门禁与
// 中间件，不校验仓储细节。
type evaluationDeleteRepoFake struct {
	createdBy map[string]string
	deleted   []string
}

func (f *evaluationDeleteRepoFake) getCreatedBy(id string) (string, bool, error) {
	cb, ok := f.createdBy[id]
	if !ok {
		cb = ""
	}
	return cb, true, nil
}

func (f *evaluationDeleteRepoFake) GetSuiteCreatedBy(ctx context.Context, _, id string) (string, bool, error) {
	return f.getCreatedBy(id)
}
func (f *evaluationDeleteRepoFake) GetRunCreatedBy(ctx context.Context, _, id string) (string, bool, error) {
	return f.getCreatedBy(id)
}
func (f *evaluationDeleteRepoFake) GetJobCreatedBy(ctx context.Context, _, id string) (string, bool, error) {
	return f.getCreatedBy(id)
}
func (f *evaluationDeleteRepoFake) GetExperimentCreatedBy(ctx context.Context, _, id string) (string, bool, error) {
	return f.getCreatedBy(id)
}
func (f *evaluationDeleteRepoFake) GetCandidateCreatedBy(ctx context.Context, _, id string) (string, bool, error) {
	return f.getCreatedBy(id)
}
func (f *evaluationDeleteRepoFake) GetReviewItemCreatedBy(ctx context.Context, _, id string) (string, bool, error) {
	return f.getCreatedBy(id)
}
func (f *evaluationDeleteRepoFake) GetFeedbackCreatedBy(ctx context.Context, _, id string) (string, bool, error) {
	return f.getCreatedBy(id)
}

func (f *evaluationDeleteRepoFake) DeleteSuite(_ context.Context, _, id string, _ *auditdomain.ResourceChangeAuditEvent) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *evaluationDeleteRepoFake) DeleteRun(_ context.Context, _, id string, _ *auditdomain.ResourceChangeAuditEvent) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *evaluationDeleteRepoFake) DeleteJob(_ context.Context, _, id string, _ *auditdomain.ResourceChangeAuditEvent) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *evaluationDeleteRepoFake) DeleteExperiment(_ context.Context, _, id string, _ *auditdomain.ResourceChangeAuditEvent) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *evaluationDeleteRepoFake) DeleteCandidate(_ context.Context, _, id string, _ *auditdomain.ResourceChangeAuditEvent) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *evaluationDeleteRepoFake) DeleteReviewItem(_ context.Context, _, id string, _ *auditdomain.ResourceChangeAuditEvent) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *evaluationDeleteRepoFake) DeleteFeedback(_ context.Context, _, id string, _ *auditdomain.ResourceChangeAuditEvent) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func signEvaluationToken(t *testing.T, svc iamport.TokenService, tenantID, role string) string {
	t.Helper()
	return signEvaluationTokenAs(t, svc, tenantID, "user-1", role)
}

// signEvaluationTokenAs 以指定 Sub 签发 token，用于区分 owner/创建者等 actor 语义。
func signEvaluationTokenAs(t *testing.T, svc iamport.TokenService, tenantID, sub, role string) string {
	t.Helper()
	token, err := svc.Sign(iamport.TokenClaims{Sub: sub, TenantID: tenantID, Role: role,
		SystemRole: iamdomain.SystemRoleUser, JTI: tenantID + role + sub}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func performEvaluationRequest(r http.Handler, method, path, token, status string, body *strings.Reader) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, body)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if status != "" {
		req.Header.Set("X-Tenant-Status", status)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

type evaluationQueryRepoFake struct{}

func (*evaluationQueryRepoFake) Overview(context.Context, string) (domain.CenterOverview, error) {
	return domain.CenterOverview{}, nil
}
func (*evaluationQueryRepoFake) ListResources(context.Context, string, port.CenterFilter) (domain.ResourcePage, error) {
	return domain.ResourcePage{}, nil
}
func (*evaluationQueryRepoFake) ListSuites(context.Context, string, port.CenterFilter) (domain.SuitePage, error) {
	return domain.SuitePage{}, nil
}
func (*evaluationQueryRepoFake) ListRuns(context.Context, string, port.CenterFilter) (domain.RunPage, error) {
	return domain.RunPage{}, nil
}
func (*evaluationQueryRepoFake) ListCandidates(context.Context, string, port.CenterFilter) (domain.CandidatePage, error) {
	return domain.CandidatePage{}, nil
}
func (*evaluationQueryRepoFake) ListExperiments(context.Context, string, port.CenterFilter) (domain.ExperimentPage, error) {
	return domain.ExperimentPage{}, nil
}
func (*evaluationQueryRepoFake) Timeline(_ context.Context, tenantID string, _ port.CenterFilter) (domain.TimelinePage, error) {
	if tenantID != "tenant-1" {
		return domain.TimelinePage{}, port.ErrCenterResourceNotFound
	}
	return domain.TimelinePage{}, nil
}
func (*evaluationQueryRepoFake) MonitorResources(context.Context, string, port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	return domain.MonitorResourcesPage{}, nil
}
func (*evaluationQueryRepoFake) MonitorTrend(context.Context, string, port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	return domain.MonitorTrendSeries{}, nil
}
