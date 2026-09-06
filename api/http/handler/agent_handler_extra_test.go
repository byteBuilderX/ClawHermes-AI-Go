package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/api/middleware"
	agent "github.com/byteBuilderX/stratum/internal/agent/application"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	parametersdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	versioningdomain "github.com/byteBuilderX/stratum/internal/versioning/domain"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockAgentRepo is a scriptable AgentRepo stub for handler tests.
type mockAgentRepo struct {
	agents      []*domain.AgentConfig
	err         error
	removeErr   error
	registerErr error
	updateErr   error
}

func (m *mockAgentRepo) Register(_ context.Context, _ *domain.AgentConfig, _ *auditdomain.ResourceChangeAuditEvent, _ []string) error {
	return m.registerErr
}
func (m *mockAgentRepo) Get(_ context.Context, id string) (*domain.AgentConfig, bool, error) {
	if m.err != nil {
		return nil, false, m.err
	}
	for _, a := range m.agents {
		if a.ID == id {
			return a, true, nil
		}
	}
	return nil, false, nil
}
func (m *mockAgentRepo) GetAll(context.Context) ([]*domain.AgentConfig, error) {
	return m.agents, m.err
}
func (m *mockAgentRepo) Remove(_ context.Context, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return m.removeErr
}
func (m *mockAgentRepo) Update(_ context.Context, cfg *domain.AgentConfig, _ *auditdomain.ResourceChangeAuditEvent, _ string, _ bool, _ *versioningdomain.Version) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	// 真实写回:service 在 Update 成功后经 repo 回读,若 mock 是 no-op,
	// 回读结果与内存 DTO 不一致,API 断言会假绿。
	for i, a := range m.agents {
		if a.ID == cfg.ID {
			m.agents[i] = cfg
			return nil
		}
	}
	return nil
}

func (m *mockAgentRepo) Rollback(_ context.Context, cfg *domain.AgentConfig, _ *auditdomain.ResourceChangeAuditEvent, _, _ string) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	for i, a := range m.agents {
		if a.ID == cfg.ID {
			m.agents[i] = cfg
			return nil
		}
	}
	return nil
}

// fakeEvidence 是 TraceEvidenceProvider 的可脚本化 stub。
type fakeEvidence struct {
	records []domain.ExecutionRecord
	total   int64
	userID  string
	err     error
}

func (f *fakeEvidence) ListExecutions(context.Context, string, domain.ListOptions) ([]domain.ExecutionRecord, int64, error) {
	return f.records, f.total, f.err
}
func (f *fakeEvidence) ToolObservations(context.Context, string, string) ([]domain.ToolObservation, error) {
	return nil, f.err
}
func (f *fakeEvidence) TraceEvents(context.Context, string, string) ([]domain.AgentTraceEvent, error) {
	return nil, f.err
}
func (f *fakeEvidence) Resolve(context.Context, string, string) (domain.TraceEvidence, error) {
	if f.err != nil {
		return domain.TraceEvidence{}, f.err
	}
	return domain.TraceEvidence{UserID: f.userID}, nil
}
func (f *fakeEvidence) ResolveBatch(context.Context, string, []string) (map[string]domain.TraceEvidence, error) {
	return nil, f.err
}

// fakeCheckpointStore 让 Pause/Resume 的失败路径可测。
type fakeCheckpointStore struct {
	updateErr error
}

func (f fakeCheckpointStore) Upsert(context.Context, string, domain.AgentExecutionCheckpoint) error {
	return nil
}
func (f fakeCheckpointStore) GetLatest(context.Context, string, string) (*domain.AgentExecutionCheckpoint, error) {
	return nil, nil
}
func (f fakeCheckpointStore) MarkCompleted(context.Context, string, string) error { return nil }
func (f fakeCheckpointStore) UpdateStatus(context.Context, string, string, string) error {
	return f.updateErr
}
func (f fakeCheckpointStore) DeleteExpired(context.Context, string) (int64, error) { return 0, nil }
func (f fakeCheckpointStore) GetLatestActiveByConversation(context.Context, string, string) (*domain.AgentExecutionCheckpoint, error) {
	return nil, nil
}
func (f fakeCheckpointStore) UpdateStatusFrom(context.Context, string, string, string, string) error {
	return nil
}
func (f fakeCheckpointStore) AdvanceRunGeneration(context.Context, string, string, int) error {
	return nil
}
func (f fakeCheckpointStore) Terminate(context.Context, string, string, string) error { return nil }

func newTestAgentHandler(t *testing.T, repo *mockAgentRepo, evidence port.TraceEvidenceProvider, mut func(*agent.AgentServiceDeps)) *AgentHandler {
	t.Helper()
	deps := agent.AgentServiceDeps{
		Registry:         agent.NewRegistry(repo, zap.NewNop()),
		EvidenceProvider: evidence,
		Logger:           zap.NewNop(),
	}
	if mut != nil {
		mut(&deps)
	}
	svc := agent.NewAgentService(deps)
	svc.SetTenantRoleResolver(fixedTenantRole{role: "owner"})
	return NewAgentHandler(svc, zap.NewNop())
}

// agentRoutes 注册本文件被测的全部路由，统一挂 ErrorHandler；
// auth 中间件可选（传 withAuth(...) 注入 tenant/user）。
func agentRoutes(h *AgentHandler, auth ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	for _, a := range auth {
		r.Use(a)
	}
	g := r.Group("/agents")
	g.GET("", h.GetAllAgents)
	g.GET("/:id", h.GetAgent)
	g.POST("", h.CreateAgent)
	g.PUT("/:id", h.UpdateAgent)
	g.DELETE("/:id", h.DeleteAgent)
	g.GET("/:id/versions", h.ListAgentVersions)
	g.GET("/:id/versions/:versionID", h.GetAgentVersion)
	g.POST("/:id/rollback", h.RollbackAgent)
	g.GET("/:id/executions", h.ListExecutions)
	g.GET("/:id/executions/:traceID/tool-traces", h.ListExecutionToolTraces)
	g.GET("/:id/executions/:traceID/trace-events", h.ListExecutionTraceEvents)
	g.GET("/:id/approvals", h.ListToolApprovals)
	g.POST("/:id/approvals/:approvalID/decide", h.DecideToolApproval)
	g.POST("/:id/approvals/:approvalID/resume", h.ResumeToolApproval)
	g.POST("/:id/pause/:executionID", h.PauseExecution)
	g.POST("/:id/resume/:executionID", h.ResumeExecution)
	return r
}

func withAuth(tenantID, userID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := reqctx.WithTenantID(c.Request.Context(), tenantID)
		c.Request = c.Request.WithContext(ctx)
		c.Set(middleware.ContextKeySub, userID)
		c.Next()
	}
}

// withTenantOnly 只注入 tenant、不设置 user，用于缺 user 的 401 用例。
func withTenantOnly(tenantID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := reqctx.WithTenantID(c.Request.Context(), tenantID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// authedRoutes 注入 t1/u1 的默认认证 router。
func authedRoutes(h *AgentHandler) *gin.Engine {
	return agentRoutes(h, withAuth("t1", "u1"))
}

func doAgentReq(t *testing.T, r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil) //nolint:noctx
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body)) //nolint:noctx
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAgentHandlerGetAllAgents(t *testing.T) {
	repo := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}, {ID: "a2", Name: "Beta"}}}
	h := newTestAgentHandler(t, repo, nil, nil)

	w := doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"agents"`)
	require.Contains(t, w.Body.String(), "Alpha")

	// 极端情况：缺 tenant → 401。
	w = doAgentReq(t, agentRoutes(h), http.MethodGet, "/agents", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 极端情况：repo 失败 → 500。
	h = newTestAgentHandler(t, &mockAgentRepo{err: errors.New("db down")}, nil, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAgentHandlerGetAgent(t *testing.T) {
	repo := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}
	h := newTestAgentHandler(t, repo, nil, nil)

	w := doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "Alpha")

	// 极端情况：不存在 → 404。
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/missing", "")
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：repo 失败 → 500。
	h = newTestAgentHandler(t, &mockAgentRepo{err: errors.New("db down")}, nil, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAgentHandlerCreateAgent(t *testing.T) {
	h := newTestAgentHandler(t, &mockAgentRepo{}, nil, nil)
	valid := `{"name":"N","llmModel":"qwen-max","maxIterations":5,"memoryScope":"user"}`

	// 缺 tenant → 401。
	w := doAgentReq(t, agentRoutes(h), http.MethodPost, "/agents", valid)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 极端情况：非法 JSON → 400。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", "{")
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 极端情况：缺少 required 字段 → 400。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 记忆范围必选：缺省或空串 → 400（平台助手与普通 Agent 等同后创建接口必选）。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", `{"name":"N","llmModel":"qwen-max","maxIterations":5}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", `{"name":"N","llmModel":"qwen-max","maxIterations":5,"memoryScope":""}`)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 成功 → 201。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", valid)
	require.Equal(t, http.StatusCreated, w.Code)
	require.Contains(t, w.Body.String(), `"id"`)

	// 极端情况：持久化失败 → 500。
	h = newTestAgentHandler(t, &mockAgentRepo{registerErr: errors.New("write failed")}, nil, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", valid)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAgentHandlerUpdateAgent(t *testing.T) {
	repo := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha", LLMModel: "qwen-max"}}}
	h := newTestAgentHandler(t, repo, nil, nil)
	valid := `{"name":"Renamed","llmModel":"qwen-max","maxIterations":3,"memoryScope":"user"}`

	// 极端情况：不存在 → 404。
	w := doAgentReq(t, authedRoutes(h), http.MethodPut, "/agents/missing", valid)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：非法 JSON → 400。
	w = doAgentReq(t, authedRoutes(h), http.MethodPut, "/agents/a1", "{")
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 记忆范围必选：缺省 → 400。
	w = doAgentReq(t, authedRoutes(h), http.MethodPut, "/agents/a1", `{"name":"Renamed","llmModel":"qwen-max","maxIterations":3}`)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 成功 → 200。
	w = doAgentReq(t, authedRoutes(h), http.MethodPut, "/agents/a1", valid)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "Renamed")

	// 极端情况：持久化失败 → 500。
	repo.updateErr = errors.New("write failed")
	w = doAgentReq(t, authedRoutes(h), http.MethodPut, "/agents/a1", valid)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

// registryParamsProvider adapts the real parameters registry validator to the
// agent ParametersProvider port so handler tests exercise the actual enum
// rejection (the frontend hidden field is UX, not a security boundary). Resolve
// stubs are unused by the create/update write paths.
type registryParamsProvider struct {
	svc *parametersapp.Service
}

func (p registryParamsProvider) ResolveForResource(context.Context, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (p registryParamsProvider) Resolve(context.Context, string, map[string]any) (any, bool, error) {
	return nil, false, nil
}
func (p registryParamsProvider) ValidateResource(_ context.Context, declared map[string]any) error {
	return p.svc.ValidateResourceValues(declared)
}
func (p registryParamsProvider) ValidateResourceKey(_ context.Context, key string, value any) error {
	def, ok := p.svc.Registry().Get(key)
	if !ok {
		return fmt.Errorf("unknown parameter %s", key)
	}
	return def.Validate(value)
}

// TestAgentHandlerReasoningEffortEcho 守护 reasoning_effort 在 Create/Update 请求
// 与响应间往返回显，并拒绝枚举外非法值（fail-closed，防绕过前端传非法档位）。
func TestAgentHandlerReasoningEffortEcho(t *testing.T) {
	paramsProvider := registryParamsProvider{svc: parametersapp.NewService(parametersdomain.NewParametersRegistry(), nil)}
	newHandler := func(repo *mockAgentRepo) *AgentHandler {
		return newTestAgentHandler(t, repo, nil, func(deps *agent.AgentServiceDeps) {
			deps.ParametersProvider = paramsProvider
		})
	}

	// Create 成功回显高档位。
	h := newHandler(&mockAgentRepo{})
	body := `{"name":"N","llmModel":"qwen-max","maxIterations":5,"memoryScope":"user","reasoning_effort":"high"}`
	w := doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", body)
	require.Equal(t, http.StatusCreated, w.Code)
	require.Contains(t, w.Body.String(), `"reasoning_effort":"high"`)

	// Update 成功回显中档位。
	repo := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha", LLMModel: "qwen-max"}}}
	h = newHandler(repo)
	body = `{"name":"Renamed","llmModel":"qwen-max","maxIterations":3,"memoryScope":"user","reasoning_effort":"medium"}`
	w = doAgentReq(t, authedRoutes(h), http.MethodPut, "/agents/a1", body)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"reasoning_effort":"medium"`)

	// 极端情况：枚举外值被拒 → 400（绕过前端直调 API 也拦得住）。
	h = newHandler(&mockAgentRepo{})
	body = `{"name":"N","llmModel":"qwen-max","maxIterations":5,"reasoning_effort":"deep"}`
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", body)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAgentHandlerDeleteAgent(t *testing.T) {
	repo := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}
	h := newTestAgentHandler(t, repo, nil, nil)

	// 成功 → 200。
	w := doAgentReq(t, authedRoutes(h), http.MethodDelete, "/agents/a1", "")
	require.Equal(t, http.StatusOK, w.Code)

	// 极端情况：不存在 → 404。
	w = doAgentReq(t, authedRoutes(h), http.MethodDelete, "/agents/missing", "")
	require.Equal(t, http.StatusNotFound, w.Code)

	// 等同化后 system assistant 与普通 agent 一致可删除 → 200。
	sys := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "sys-1"}}}
	h = newTestAgentHandler(t, sys, nil, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodDelete, "/agents/sys-1", "")
	require.Equal(t, http.StatusOK, w.Code)

	// 极端情况：删除失败 → 500。
	h = newTestAgentHandler(t, repo, nil, nil)
	repo.removeErr = errors.New("delete failed")
	w = doAgentReq(t, authedRoutes(h), http.MethodDelete, "/agents/a1", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAgentHandlerPauseResumeExecution(t *testing.T) {
	// 极端情况：checkpoint store 未配置 → Pause 500。
	h := newTestAgentHandler(t, &mockAgentRepo{}, nil, nil)
	w := doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/pause/exec-1", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：checkpoint 状态写失败 → Pause/Resume 均 500。
	h = newTestAgentHandler(t, &mockAgentRepo{}, nil, func(deps *agent.AgentServiceDeps) {
		deps.CheckpointStore = fakeCheckpointStore{updateErr: errors.New("db down")}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/pause/exec-1", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/resume/exec-1", `{"query":"q"}`)
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：缺 tenant → 401。
	w = doAgentReq(t, agentRoutes(h), http.MethodPost, "/agents/a1/pause/exec-1", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAgentHandlerListExecutions(t *testing.T) {
	created := time.Now()
	evidence := &fakeEvidence{
		records: []domain.ExecutionRecord{{
			ID: "e1", TraceID: "t1", AgentID: "a1", AgentName: "Research", UserID: "u1",
			Status: "completed", InputPreview: "in", OutputPreview: "out", TotalTokens: 42,
			DurationMs: 100, CreatedAt: created,
		}},
		total: 1,
	}
	h := newTestAgentHandler(t, &mockAgentRepo{}, evidence, nil)

	w := doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "Research")
	require.Contains(t, w.Body.String(), `"total":1`)

	// 极端情况：非法 page/page_size → 解析为 0 但请求成功。
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions?page=abc&page_size=-1", "")
	require.Equal(t, http.StatusOK, w.Code)

	// 极端情况：空记录 → 200 + 空数组。
	h = newTestAgentHandler(t, &mockAgentRepo{}, &fakeEvidence{}, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"executions":[]`)

	// 极端情况：provider 失败 → 500。
	h = newTestAgentHandler(t, &mockAgentRepo{}, &fakeEvidence{err: errors.New("db down")}, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：缺 user → 401。
	w = doAgentReq(t, agentRoutes(h, withTenantOnly("t1")), http.MethodGet, "/agents/a1/executions", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAgentHandlerListExecutionToolTracesAndEvents(t *testing.T) {
	// 本人 → 200。
	evidence := &fakeEvidence{userID: "u1"}
	h := newTestAgentHandler(t, &mockAgentRepo{}, evidence, nil)
	w := doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions/trace-1/tool-traces", "")
	require.Equal(t, http.StatusOK, w.Code)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions/trace-1/trace-events", "")
	require.Equal(t, http.StatusOK, w.Code)

	// 极端情况：非本人（偷窥）→ 404。
	h = newTestAgentHandler(t, &mockAgentRepo{}, &fakeEvidence{userID: "other"}, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions/trace-1/tool-traces", "")
	require.Equal(t, http.StatusNotFound, w.Code)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions/trace-1/trace-events", "")
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：provider 失败 → 500。
	h = newTestAgentHandler(t, &mockAgentRepo{}, &fakeEvidence{userID: "u1", err: errors.New("db down")}, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/executions/trace-1/tool-traces", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：缺 user → 401。
	w = doAgentReq(t, agentRoutes(h, withTenantOnly("t1")), http.MethodGet, "/agents/a1/executions/trace-1/tool-traces", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAgentHandlerListToolApprovals(t *testing.T) {
	// ApprovalService 未配置 → fail closed：500（配置错误显式暴露，禁止静默空列表）。
	h := newTestAgentHandler(t, &mockAgentRepo{}, nil, nil)
	w := doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/approvals", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：缺 tenant → 401。
	w = doAgentReq(t, agentRoutes(h), http.MethodGet, "/agents/a1/approvals", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAgentHandlerDecideToolApproval(t *testing.T) {
	h := newTestAgentHandler(t, &mockAgentRepo{}, nil, nil)

	// 极端情况：缺 tenant → 401。
	w := doAgentReq(t, agentRoutes(h), http.MethodPost, "/agents/a1/approvals/app-1/decide", `{"decision":"approve"}`)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 极端情况：非法 JSON → 400。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/approvals/app-1/decide", "{")
	require.Equal(t, http.StatusBadRequest, w.Code)

	// ApprovalService 未配置 → 500。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/approvals/app-1/decide", `{"decision":"approve"}`)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAgentHandlerResumeToolApproval(t *testing.T) {
	h := newTestAgentHandler(t, &mockAgentRepo{}, nil, nil)

	// 极端情况：缺 tenant → 401。
	w := doAgentReq(t, agentRoutes(h), http.MethodPost, "/agents/a1/approvals/app-1/resume", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// Approval runtime 未配置 → 500。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/approvals/app-1/resume", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

// fixedTenantRole resolves every actor as owner so handler tests reach the
// service write path; ownership specifics are covered in application tests.
type fixedTenantRole struct{ role string }

func (r fixedTenantRole) ResolveTenantRole(context.Context, string, string) (string, error) {
	return r.role, nil
}

// TestAgentHandlerMemoryParametersEcho 守护 memory.* 参数经
// UpdateAgentRequest.parameters 落库（agents.parameters JSONB 的 dotted 键，
// 非采样字段），并在 Update/GET 响应回显供编辑页表单预填；越界值 fail-closed。
func TestAgentHandlerMemoryParametersEcho(t *testing.T) {
	paramsProvider := registryParamsProvider{svc: parametersapp.NewService(parametersdomain.NewParametersRegistry(), nil)}
	newHandler := func(repo *mockAgentRepo) *AgentHandler {
		return newTestAgentHandler(t, repo, nil, func(deps *agent.AgentServiceDeps) {
			deps.ParametersProvider = paramsProvider
		})
	}

	// Update 携带 memory.* dotted 键 → 落库并在响应回显。
	repo := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha", LLMModel: "qwen-max"}}}
	h := newHandler(repo)
	body := `{"name":"Alpha","llmModel":"qwen-max","maxIterations":5,"memoryScope":"user","parameters":{"memory.max_facts_per_extraction":8,"memory.fact_injection_top_n":10}}`
	w := doAgentReq(t, authedRoutes(h), http.MethodPut, "/agents/a1", body)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"memory.max_facts_per_extraction":8`)
	require.Contains(t, w.Body.String(), `"memory.fact_injection_top_n":10`)

	// GET 回读同一份 parameters（编辑页预填来源）。
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"memory.max_facts_per_extraction":8`)

	// 越界 memory.* 值被 registry 校验拒绝 → 400（绕过前端直调 API 也拦得住）。
	h = newHandler(&mockAgentRepo{})
	body = `{"name":"N","llmModel":"qwen-max","maxIterations":5,"parameters":{"memory.max_facts_per_extraction":999}}`
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents", body)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// mockHandlerVersionRepo 是通用版本基座只读 port（resource_versions）的
// 脚本化实现，按 versionID 区分 GetVersion 返回值，供 handler 级
// ListAgentVersions/RollbackAgent 测试覆盖成功与错误路径。
type mockHandlerVersionRepo struct {
	versions []versioningdomain.Version
	getByID  map[string]versioningdomain.Version
	listErr  error
	getErr   error
}

func (m *mockHandlerVersionRepo) ListVersions(_ context.Context, _ string, _ versioningdomain.ResourceKind, _ string) ([]versioningdomain.Version, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.versions, nil
}

func (m *mockHandlerVersionRepo) GetVersion(_ context.Context, _ string, _ versioningdomain.ResourceKind, _, versionID string) (versioningdomain.Version, bool, error) {
	if m.getErr != nil {
		return versioningdomain.Version{}, false, m.getErr
	}
	v, ok := m.getByID[versionID]
	return v, ok, nil
}

// fakeHandlerActorNames 固定返回预置昵称映射，实现 agent port.ActorNameResolver。
type fakeHandlerActorNames struct {
	names map[string]string
}

func (f *fakeHandlerActorNames) ResolveActorNames(_ context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if n, ok := f.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func TestAgentHandlerListAgentVersions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	vrepo := &mockHandlerVersionRepo{versions: []versioningdomain.Version{
		{ID: "v2", RevisionNo: 2, ParentVersionID: "v1", Status: versioningdomain.VersionStatusPublished, Source: versioningdomain.VersionSourceManual,
			ContentHash: "h2", CreatedBy: "u1", CreatedAt: now, IsCurrent: true},
		{ID: "v1", RevisionNo: 1, Status: versioningdomain.VersionStatusDeprecated, Source: versioningdomain.VersionSourceRollback,
			ContentHash: "h1", CreatedBy: "u2", CreatedAt: now.Add(-time.Hour)},
	}}
	h := newTestAgentHandler(t, &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = vrepo
		deps.ActorNameResolver = &fakeHandlerActorNames{names: map[string]string{"u1": "Alice", "u2": "Bob"}}
	})

	// 成功 → 200，版本数组含昵称与生效标记。
	w := doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/versions", "")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.Contains(t, body, `"versions"`)
	require.Contains(t, body, `"versionNo":2`)
	require.Contains(t, body, `"isCurrent":true`)
	require.Contains(t, body, `"createdByName":"Alice"`)
	require.Contains(t, body, `"createdByName":"Bob"`)
	require.Contains(t, body, `"source":"rollback"`)
	// parentVersionId：v2 自链到 v1；首版 v1 为空串。
	require.Contains(t, body, `"parentVersionId":"v1"`)
	require.Contains(t, body, `"parentVersionId":""`)

	// 极端情况：缺 tenant → 401。
	w = doAgentReq(t, agentRoutes(h), http.MethodGet, "/agents/a1/versions", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 极端情况：版本基座未装配（nil）→ fail-closed 500。
	h = newTestAgentHandler(t, &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}, nil, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/versions", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：版本基座查询失败 → 500。
	h = newTestAgentHandler(t, &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{listErr: errors.New("db down")}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/versions", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAgentHandlerGetAgentVersion(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// 整份编辑面 payload（snake_case 键），由「详情」Drawer 与直父版本 diff。
	v2 := versioningdomain.Version{
		ID: "v2", RevisionNo: 2, ParentVersionID: "v1", Status: versioningdomain.VersionStatusPublished,
		Source: versioningdomain.VersionSourceManual, ContentHash: "h2", CreatedBy: "u1", CreatedAt: now,
		IsCurrent: true, SafeSummary: map[string]any{"name": "Alpha"}, Payload: map[string]any{
			"name": "Alpha", "description": "desc", "system_prompt": "prompt", "llm_model": "qwen-plus",
			"max_iterations": 5,
		},
	}
	h := newTestAgentHandler(t, &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getByID: map[string]versioningdomain.Version{"v2": v2}}
		deps.ActorNameResolver = &fakeHandlerActorNames{names: map[string]string{"u1": "Alice"}}
	})

	// 成功 → 200：整份 payload + parentVersionId + 昵称 + safeSummary。
	w := doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/versions/v2", "")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.Contains(t, body, `"id":"v2"`)
	require.Contains(t, body, `"parentVersionId":"v1"`)
	require.Contains(t, body, `"createdByName":"Alice"`)
	require.Contains(t, body, `"safeSummary":{"name":"Alpha"}`)
	require.Contains(t, body, `"payload":{`)
	require.Contains(t, body, `"system_prompt":"prompt"`)
	require.Contains(t, body, `"llm_model":"qwen-plus"`)
	require.Contains(t, body, `"max_iterations":5`)

	// 极端情况：缺 tenant → 401。
	w = doAgentReq(t, agentRoutes(h), http.MethodGet, "/agents/a1/versions/v2", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 极端情况：版本不存在 → 404（ErrVersionNotFound → middleware）。
	h = newTestAgentHandler(t, &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getByID: map[string]versioningdomain.Version{}}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/versions/nope", "")
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：版本基座查询失败 → 500。
	h = newTestAgentHandler(t, &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getErr: errors.New("db down")}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/versions/v2", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：版本基座未装配（nil）→ fail-closed 500。
	h = newTestAgentHandler(t, &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "Alpha"}}}, nil, nil)
	w = doAgentReq(t, authedRoutes(h), http.MethodGet, "/agents/a1/versions/v2", "")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAgentHandlerRollbackAgent(t *testing.T) {
	historical := &domain.AgentConfig{ID: "a1", Name: "historical", Description: "desc", Type: domain.ReActAgent,
		SystemPrompt: "old prompt", LLMModel: "qwen-plus", MaxIterations: 5, CreatedBy: "u1"}
	deprecatedTarget := versioningdomain.Version{
		ID: "v1", RevisionNo: 1, Status: versioningdomain.VersionStatusDeprecated,
		Source: versioningdomain.VersionSourceManual, Payload: domain.SnapshotFromConfig(historical).Map(),
	}
	repo := &mockAgentRepo{agents: []*domain.AgentConfig{{ID: "a1", Name: "current", Type: domain.ReActAgent, CreatedBy: "u1"}}}
	h := newTestAgentHandler(t, repo, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getByID: map[string]versioningdomain.Version{"v1": deprecatedTarget}}
	})

	// 成功 → 200，返回重建后的 agent（name 回滚到历史值）。
	w := doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/rollback", `{"versionId":"v1"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "historical")
	require.Contains(t, w.Body.String(), `"systemPrompt":"old prompt"`)

	// 极端情况：缺 tenant → 401。
	w = doAgentReq(t, agentRoutes(h), http.MethodPost, "/agents/a1/rollback", `{"versionId":"v1"}`)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 极端情况：非法 JSON / 缺 versionId → 400。
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/rollback", `{`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/rollback", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 极端情况：目标是 published（非可回滚历史版本）→ 404。
	h = newTestAgentHandler(t, repo, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getByID: map[string]versioningdomain.Version{"v1": {
			ID: "v1", Status: versioningdomain.VersionStatusPublished,
		}}}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/rollback", `{"versionId":"v1"}`)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：目标不存在 → 404。
	h = newTestAgentHandler(t, repo, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getByID: map[string]versioningdomain.Version{}}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/rollback", `{"versionId":"nope"}`)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：agent 不存在 → 404。
	h = newTestAgentHandler(t, &mockAgentRepo{}, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getByID: map[string]versioningdomain.Version{"v1": deprecatedTarget}}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/missing/rollback", `{"versionId":"v1"}`)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：持久化失败 → 500。
	repo.updateErr = errors.New("write failed")
	h = newTestAgentHandler(t, repo, nil, func(deps *agent.AgentServiceDeps) {
		deps.VersionRepo = &mockHandlerVersionRepo{getByID: map[string]versioningdomain.Version{"v1": deprecatedTarget}}
	})
	w = doAgentReq(t, authedRoutes(h), http.MethodPost, "/agents/a1/rollback", `{"versionId":"v1"}`)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
