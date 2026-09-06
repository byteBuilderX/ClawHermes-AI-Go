// Package http builds the HTTP router from a wiring.Container.
package http

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"golang.org/x/time/rate"

	"github.com/byteBuilderX/stratum/api/http/handler"
	"github.com/byteBuilderX/stratum/api/middleware"
	"github.com/byteBuilderX/stratum/api/wiring"
	"github.com/byteBuilderX/stratum/internal/iam/application"
	pipeline "github.com/byteBuilderX/stratum/internal/memory/infrastructure/pipeline"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// NewRouter assembles the HTTP gin engine from an already-built Container.
// Route registration mirrors the legacy api.SetupRouter exactly so the
// recorded contract goldens continue to PASS.
func NewRouter(c *wiring.Container) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.BodyLimit(constants.MaxRequestBodyBytes))

	// Trace wraps error rendering so its access log observes the final status.
	r.Use(otelgin.Middleware("stratum-ai"))
	r.Use(middleware.TraceMiddleware(c.Logger))
	r.Use(middleware.ErrorHandler(c.Logger))
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.CORSMiddleware(c.Config.FrontendURL))
	r.Use(middleware.MetricsMiddleware(c.Platform.Metrics))

	requireActive := middleware.RequireActiveTenant(c.DB())

	registerAuth(r, c, requireActive)
	registerModelCatalogue(r, c)
	registerDashboard(r, c)
	registerHealth(r, c)
	// 路由 dump 仅测试态暴露(STRATUM_E2E_MODE=true),生产不注册 /e2e/routes。
	if os.Getenv("STRATUM_E2E_MODE") == "true" {
		registerE2ERoutes(r)
	}
	registerSkills(r, c, requireActive)
	registerEvaluations(r, c, requireActive)
	registerAgents(r, c, requireActive)
	registerResourceChangeProposals(r, c, requireActive)
	registerOperationProposals(r, c, requireActive)
	registerWorkflows(r, c, requireActive)
	registerCollab(r, c, requireActive)
	registerScheduledTasks(r, c, requireActive)
	registerKnowledge(r, c, requireActive)
	registerMCP(r, c, requireActive)
	registerMemory(r, c, requireActive)
	registerAudit(r, c, requireActive)
	registerLLMAdmin(r, c, requireActive)
	if c.Config.AvatarDir != "" {
		r.GET("/avatars/:filename", func(ctx *gin.Context) {
			ctx.File(filepath.Join(c.Config.AvatarDir, ctx.Param("filename")))
		})
	}
	return r
}

func registerDashboard(r *gin.Engine, c *wiring.Container) {
	if c.Platform == nil || c.Platform.DashboardService == nil {
		return
	}
	h := handler.NewDashboardHandler(c.Platform.DashboardService)
	dashboard := r.Group("/dashboard", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	dashboard.GET("/overview", h.Overview)
}

func registerOperationProposals(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Agent == nil || c.Agent.OperationProposalSvc == nil || c.Agent.OperationGateService == nil {
		return
	}
	h := handler.NewOperationProposalHandler(c.Agent.OperationProposalSvc, c.Agent.Service)
	member := protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))
	admin := protectedTenantMiddleware(c, middleware.RequireTenantRole("admin"))
	routes := r.Group("/operation-proposals", admin...)
	routes.Use(requireActive)
	routes.GET("", h.List)
	routes.GET("/:id", h.Get)
	routes.POST("/:id/review", h.Review)
	routes.POST("/:id/approve", h.Approve)
	routes.POST("/:id/reject", h.Reject)
	// The member-facing gated mutation channel sits on the agent resource;
	// the operation gate always proposes for self-modify, so no budget or
	// delegation fields are accepted here.
	agents := r.Group("/agents/:id/self-modify", member...)
	agents.Use(requireActive)
	agents.POST("", h.SelfModify)
	// member 自助查看自己的提案（权限审批 tab「我的申请」）。GET /mine 是静态
	// 段，gin 路由树静态优先于 /:id 参数段，两者可共存。
	memberOps := r.Group("/operation-proposals", member...)
	memberOps.Use(requireActive)
	memberOps.GET("/mine", h.ListMine)
	// member 组同时服务 admin 与 member（admin ⊃ member）：history 静态段优先于
	// /:id；ListHistory 按角色现查过滤全租户/本人，Cancel 按归属校验自撤/代撤。
	memberOps.GET("/history", h.ListHistory)
	memberOps.POST("/:id/cancel", h.Cancel)
	// member 自助申请白名单（grant_editor）：agent / skill 编辑权申请、
	// knowledge 文档查看权申请，批准即授予。同一 handler 按 resourceType 落库。
	grant := r.Group("", member...)
	grant.Use(requireActive)
	grant.POST("/agents/:id/request-editor", h.RequestEditorAccess)
	grant.POST("/skills/:id/request-editor", h.RequestEditorAccess)
	grant.POST("/knowledge/workspaces/:name/documents/:documentID/request-access", h.RequestEditorAccess)
	grant.POST("/mcp/servers/:id/request-editor", h.RequestEditorAccess)
	grant.POST("/knowledge/workspaces/:name/request-editor", h.RequestEditorAccess)
	grant.POST("/workflows/:id/request-editor", h.RequestEditorAccess)
}

func registerResourceChangeProposals(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Agent == nil || c.Agent.ProposalService == nil {
		return
	}
	h := handler.NewResourceChangeProposalHandler(c.Agent.ProposalService)
	routes := r.Group("/resource-change-proposals",
		protectedTenantMiddleware(c, middleware.RequireTenantRole("admin"))...)
	routes.Use(requireActive)
	routes.GET("/:id", h.Get)
	routes.PATCH("/:id", h.Update)
	routes.POST("/:id/cancel", h.Cancel)
	routes.POST("/:id/confirm", h.Confirm)
}

func registerWorkflows(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Workflow == nil || c.Workflow.DefinitionService == nil || c.Workflow.RunService == nil {
		return
	}
	h := handler.NewWorkflowHandlerWithControl(c.Workflow.DefinitionService, c.Workflow.RunService, c.Workflow.ControlService)
	member := protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))
	admin := middleware.RequireTenantRole("admin")
	definitions := r.Group("/workflows", member...)
	edit := append(append([]gin.HandlerFunc{}, member...), requireActive)
	definitions.GET("", h.ListDefinitions)
	definitions.GET("/:id", h.GetDefinition)
	definitions.GET("/:id/versions", h.ListVersions)
	definitions.GET("/:id/versions/:versionID", h.GetVersion)
	definitions.POST("", append(edit, h.CreateDefinition)...)
	definitions.PUT("/:id/draft", append(edit, h.UpdateDefinition)...)
	definitions.POST("/:id/publish", append(edit, h.PublishDefinition)...)
	definitions.DELETE("/:id", admin, requireActive, h.DeleteDefinition)
	definitions.POST("/:id/validate", admin, requireActive, h.ValidateDefinition)
	definitions.POST("/:id/rollback", admin, requireActive, h.RollbackDefinition)
	definitions.PUT("/:id/editors", admin, requireActive, h.SetWorkflowEditors)
	startRuns := r.Group("/workflow-runs", member...)
	startRuns.POST("", requireActive, h.StartRun)
	runs := r.Group("/workflow-runs", member...)
	runs.GET("", h.ListRuns)
	runs.GET("/:id", h.GetRun)
	runs.GET("/:id/events", h.GetEvents)
	runs.GET("/:id/events/stream", h.StreamEvents)
	runs.POST("/:id/cancel", requireActive, h.CancelRun)
	// pause/resume 放开给执行人（发起人）：鉴权在 application 层 authorizeRun 完成，
	// 执行人可暂停/继续自己发起的运行，非发起人 member 被拒绝。
	runs.POST("/:id/pause", requireActive, h.PauseRun)
	runs.POST("/:id/resume", requireActive, h.ResumeRun)
	runs.POST("/:id/manual-interventions/:effectID/resolve", admin, requireActive, h.ResolveManual)
	approvals := r.Group("/workflow-approvals", member...)
	approvals.GET("", admin, h.ListApprovals)
	approvals.POST("/:id/decision", admin, requireActive, h.DecideApproval)
}

func registerEvaluations(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Evaluation == nil || c.Evaluation.SuiteService == nil || c.Evaluation.JobService == nil {
		return
	}
	h := handler.NewEvaluationHandler(
		c.Evaluation.SuiteService, c.Evaluation.JobService, c.Evaluation.Service,
		c.Evaluation.OptimizationService, c.Evaluation.ExperimentService,
		c.Evaluation.FeedbackService, c.Evaluation.QueryService, c.Evaluation.CandidateService,
		c.Logger,
	).WithBaselineService(c.Evaluation.BaselineService).WithAgentRevisionApplier(c.Evaluation.AgentRevisionApplier).
		WithTestCaseGenerator(c.Evaluation.TestCaseGenerator).WithObservationService(c.Evaluation.ObservationService).
		WithReviewService(c.Evaluation.ReviewService).WithDeleteService(c.Evaluation.DeleteService)
	requireAdmin := middleware.RequireTenantRole("admin")
	evaluations := r.Group("/evaluations", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	{
		// 读放开：评测相关内容对租户全部用户可见（推翻 D6），不再 requireAdmin；
		// 写操作由 requireAdmin 门禁（member 审批分流 D4 已整体移除）。
		evaluations.GET("/overview", h.Overview)
		evaluations.GET("/resources", h.ListResources)
		evaluations.GET("/suites", h.ListSuites)
		evaluations.GET("/runs", h.ListRuns)
		evaluations.GET("/candidates", h.ListCandidates)
		evaluations.GET("/experiments", h.ListExperiments)
		evaluations.GET("/resources/:kind/:id/timeline", h.Timeline)
		// P1a 运行态观测查询：租户自有运行数据，member 可读（无需 requireAdmin）。
		// handler 内部在观测服务未装配时 fail closed 503（Task 12 wiring 注入）。
		evaluations.GET("/observations", h.ListObservations)
		evaluations.GET("/observations/:id", h.GetObservation)
		// 评测指标监控（spec 2026-09-03 §4.2）：租户自有观测/评测聚合，member 可读。
		evaluations.GET("/monitoring/resources", h.ListMonitorResources)
		evaluations.GET("/monitoring/resources/trend", h.GetMonitorTrend)
		// P1c 评审池：读对 member 放开；评审决策回写仍 requireAdmin。
		evaluations.GET("/review", h.ListReviewItems)
		evaluations.GET("/review/:id", h.GetReviewItem)
		evaluations.POST("/review/:id/decision", requireAdmin, h.DecideReviewItem)
		// 写收紧：评测写端点一律 requireAdmin（D4 member→evaluation_action 分流已移除）。
		evaluations.POST("/resources/:kind/:id/baseline", requireAdmin, requireActive, h.CreateBaseline)
		evaluations.POST("/suites", requireAdmin, requireActive, h.CreateSuite)
		evaluations.POST("/suites/:id/publish", requireAdmin, requireActive, h.PublishSuite)
		evaluations.POST("/suites/:id/generate", requireAdmin, requireActive, h.GenerateSuiteCases)
		// S1-3 suite 管理页读写端点：读放开（member 只读展示版本/cases），
		// 草稿开启/加删 case 是写操作 requireAdmin。
		evaluations.GET("/suites/:id", requireActive, h.GetSuiteDetail)
		evaluations.GET("/suites/:id/draft", requireActive, h.GetSuiteDraft)
		evaluations.GET("/suites/:id/versions", requireActive, h.ListSuiteVersions)
		evaluations.GET("/suites/:id/versions/:revisionId", requireActive, h.GetSuiteRevision)
		evaluations.POST("/suites/:id/draft", requireAdmin, requireActive, h.StartNextDraft)
		evaluations.POST("/suites/:id/draft/cases", requireAdmin, requireActive, h.AddDraftCase)
		evaluations.DELETE("/suites/:id/draft/cases/:caseId", requireAdmin, requireActive, h.DeleteDraftCase)
		evaluations.PUT("/suites/:id/draft/cases/:caseId", requireAdmin, requireActive, h.UpdateDraftCase)
		evaluations.POST("/runs", requireAdmin, requireActive, h.EnqueueRun)
		evaluations.GET("/runs/:id", h.GetRun)
		evaluations.GET("/jobs/:id", h.GetJob)
		evaluations.POST("/optimizations", requireAdmin, requireActive, h.GenerateOptimization)
		evaluations.POST("/experiments", requireAdmin, requireActive, h.CreateExperiment)
		evaluations.POST("/candidates/:id/reject", requireAdmin, requireActive, h.RejectCandidate)
		evaluations.POST("/experiments/:id/pause", requireAdmin, requireActive, h.PauseExperiment)
		evaluations.POST("/experiments/:id/promote", requireAdmin, requireActive, h.PromoteExperiment)
		evaluations.POST("/experiments/:id/rollback", requireAdmin, requireActive, h.RollbackExperiment)
		evaluations.POST("/feedback", requireAdmin, requireActive, h.RecordFeedback)
		// 删除：owner-or-creator 门禁（应用层 fail-closed），admin+ 才可触发。
		evaluations.DELETE("/suites/:id", requireAdmin, requireActive, h.DeleteSuite)
		evaluations.DELETE("/runs/:id", requireAdmin, requireActive, h.DeleteRun)
		evaluations.DELETE("/jobs/:id", requireAdmin, requireActive, h.DeleteJob)
		evaluations.DELETE("/experiments/:id", requireAdmin, requireActive, h.DeleteExperiment)
		evaluations.DELETE("/candidates/:id", requireAdmin, requireActive, h.DeleteCandidate)
		evaluations.DELETE("/review/:id", requireAdmin, requireActive, h.DeleteReviewItem)
		evaluations.DELETE("/feedback/:id", requireAdmin, requireActive, h.DeleteFeedback)
	}
}

// registerAuth wires /auth, /admin/*, /tenant/* routes. JWT-gated groups
// only register when a usable RSA key was provided (Platform.JWTService
// non-nil).
func registerAuth(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	cfg := c.Config
	if c.Platform.JWTService == nil {
		return
	}
	jwtSvc := c.Platform.JWTService
	var invitationSvc *application.InvitationService
	if c.IAM != nil {
		invitationSvc = c.IAM.InvitationService
	}

	authHandler := handler.NewAuthHandler(handler.AuthHandlerDeps{
		GitHubClient:       c.Platform.GitHubClient,
		SchemaProvisioner:  c.Platform.SchemaProvisioner,
		JWTService:         jwtSvc,
		TokenStore:         c.Platform.TokenStore,
		OAuthExchangeStore: c.Platform.OAuthExchangeStore,
		OnboardSvc:         c.Platform.OnboardSvc,
		InvitationSvc:      invitationSvc,
		Logger:             c.Logger,
		GitHubAuthorizeURL: cfg.GitHubAuthorizeURL,
		CallbackURL:        cfg.GitHubCallbackURL,
		FrontendURL:        cfg.FrontendURL,
		GlobalAdmin:        cfg.GlobalAdminGitHubLogin,
		SecureCookies:      cfg.SecureCookies,
		GuestAuthEnabled:   cfg.GuestAuthEnabled,
		AvatarStore:        c.Platform.AvatarStore,
	})
	authLimiter := newRateLimiterStore(c, middleware.AuthRate, middleware.AuthBurst)
	authRoutes := r.Group("/auth")
	{
		if cfg.GitHubClientID != "" && c.Platform.GitHubClient != nil {
			authRoutes.GET("/github", authHandler.GitHubLogin)
			authRoutes.GET("/github/callback", middleware.RateLimit(authLimiter), authHandler.GitHubCallback)
		}
		authRoutes.POST("/register", middleware.RateLimit(authLimiter), authHandler.Register)
		if cfg.PasswordAuthEnabled {
			authRoutes.POST("/password/register", middleware.RateLimit(authLimiter), authHandler.UsernameRegister)
			authRoutes.POST("/password/login", middleware.RateLimit(authLimiter), authHandler.UsernameLogin)
		}
		authRoutes.POST("/oauth/exchange", middleware.RateLimit(authLimiter), authHandler.OAuthExchange)
		authRoutes.POST("/guest", middleware.RateLimit(authLimiter), authHandler.GuestLogin)
		authRoutes.POST("/refresh", middleware.RateLimit(authLimiter), authHandler.Refresh)
		authRoutes.POST("/logout", authHandler.Logout)
		authRoutes.GET("/me", authHandler.Me)
		authRoutes.PATCH("/me", authHandler.UpdateProfile)
		authRoutes.POST("/me/avatar", authHandler.UploadAvatar)
		authRoutes.POST("/switch-tenant", authHandler.SwitchTenant)
		authRoutes.POST("/create-tenant", authHandler.CreateUserTenant)
	}

	if c.IAM == nil || c.IAM.AdminService == nil || c.IAM.TenantService == nil {
		return
	}
	jwtMW := middleware.JWTMiddleware(jwtSvc, c.Platform.Metrics)
	adminHandler := handler.NewAdminHandler(c.IAM.AdminService, c.Logger)
	tenantHandler := handler.NewTenantHandler(c.IAM.TenantService, c.IAM.InvitationService, c.IAM.AdminService, c.Logger)

	// 平台管理只读接口：所有登录租户成员可读（租户/参数/管理员名单），写接口见 adminGroup。
	platformRead := r.Group("/admin", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	{
		platformRead.GET("/tenants", adminHandler.ListTenants)
		platformRead.GET("/tenants/:id", adminHandler.GetTenant)
		registerParameterReadRoutes(platformRead, c)
		platformRead.GET("/admins", adminHandler.ListAdmins)
	}

	// /admin 常规后台写接口：system_admin 及以上（租户管理、参数、memory DLQ）。
	adminGroup := r.Group("/admin", jwtMW, middleware.RequireSystemAdmin())
	{
		adminGroup.POST("/tenants", adminHandler.CreateTenant)
		adminGroup.PATCH("/tenants/:id", adminHandler.UpdateTenant)
		// 高敏感：删除租户仅 global_admin。
		adminGroup.DELETE("/tenants/:id", middleware.RequireGlobalAdmin(), adminHandler.DeleteTenant)
		registerParameterWriteRoutes(adminGroup, c)
		registerMemoryDLQAdminRoutes(adminGroup, c)

		// 平台管理员管理：仅 global_admin（system_admin 不可自我管理或管理同级）。
		adminAdmins := adminGroup.Group("/admins", middleware.RequireGlobalAdmin())
		{
			adminAdmins.POST("", adminHandler.SetAdminRole)
			adminAdmins.DELETE("/:user_id", adminHandler.RemoveAdminRole)
		}

		// 用户搜索：供提升选择候选，system_admin 可见（候选不含管理员）。
		adminGroup.GET("/users", adminHandler.SearchUsers)
	}

	tenantGroup := r.Group("/tenant", jwtMW, middleware.InjectTenantContext(), middleware.RequireTenantRole("member"))
	{
		tenantGroup.GET("/members", tenantHandler.ListMembers)
		tenantGroup.POST("/members/invite", requireActive, tenantHandler.InviteMember)
		tenantGroup.POST("/join", tenantHandler.JoinTenant)
		tenantGroup.PATCH("/members/:user_id/role", tenantHandler.UpdateMemberRole)
		tenantGroup.DELETE("/members/:user_id", tenantHandler.RemoveMember)
		tenantGroup.GET("/settings", tenantHandler.GetSettings)
		tenantGroup.PATCH("/settings", requireActive, tenantHandler.UpdateSettings)
		tenantGroup.DELETE("", middleware.RequireTenantRole("owner"), tenantHandler.DeleteSelf)
	}
	r.GET("/tenant/list", jwtMW, tenantHandler.ListUserTenants)
}

// registerParameterReadRoutes wires the unified parameter registry read-only
// endpoints (schema + platform values) for all tenant members.
func registerParameterReadRoutes(readGroup *gin.RouterGroup, c *wiring.Container) {
	if c.Parameters == nil || c.Parameters.Service == nil {
		return
	}
	paramHandler := handler.NewParameterHandler(c.Parameters.Service, c.Logger)
	readGroup.GET("/parameters/schema", paramHandler.Schema)
	readGroup.GET("/parameters", paramHandler.List)
	readGroup.GET("/parameters/versions/:groupKey", paramHandler.Versions)
}

// registerParameterWriteRoutes wires the unified parameter registry write
// endpoints, which remain gated by the parent group's system_admin middleware.
// R29/O2：Publish/Rollback 移动 production label（public 平台参数影响全租户），请求
// 的 reqctx 宿主租户必须 = default(host) tenant：InjectTenantContext 由 auth.tenant_id
// 填充 reqctx → RequireDefaultTenant 非 default 一律 403 fail-closed。
func registerParameterWriteRoutes(adminGroup *gin.RouterGroup, c *wiring.Container) {
	if c.Parameters == nil || c.Parameters.Service == nil {
		return
	}
	paramHandler := handler.NewParameterHandler(c.Parameters.Service, c.Logger)
	// Task 5 Sub-commit B：装配发布闸协调器 seam（wiring.PublishGateFunc → handler
	// PublishGateFunc 显式转换）。nil（未装配/参数服务缺失）→ handler 保持裸发布语义。
	paramHandler.SetPublishGate(handler.PublishGateFunc(c.PublishGate))
	adminGroup.PUT("/parameters", paramHandler.Update)
	adminGroup.POST("/parameters/versions/:groupKey", paramHandler.CreateDraft)
	hostWrite := adminGroup.Group("", middleware.InjectTenantContext(), middleware.RequireDefaultTenant())
	hostWrite.POST("/parameters/versions/:groupKey/:versionID/publish", paramHandler.Publish)
	hostWrite.POST("/parameters/versions/:groupKey/:versionID/rollback", paramHandler.Rollback)
}

// dlqReplayAdapter 把 pipeline.ReplayService 适配到 handler 的消费方接口
// （router 层是 wiring 之外的唯一允许适配点，避免 wiring import handler）。
type dlqReplayAdapter struct {
	svc *pipeline.ReplayService
}

func (a dlqReplayAdapter) ReplayByErrorCode(ctx context.Context, errorCode string) (handler.MemoryDLQReplayResult, error) {
	result, err := a.svc.ReplayByErrorCode(ctx, errorCode)
	return handler.MemoryDLQReplayResult{
		Total: result.Total, Replayed: result.Replayed, Skipped: result.Skipped, Failed: result.Failed,
	}, err
}

// registerMemoryDLQAdminRoutes wires POST /admin/memory/dlq/replay on the
// global admin group when the memory pipeline (and its NATS connection) is up.
func registerMemoryDLQAdminRoutes(adminGroup *gin.RouterGroup, c *wiring.Container) {
	if c.Memory == nil || c.Memory.DLQReplay == nil {
		return
	}
	h := handler.NewMemoryDlqReplayHandler(dlqReplayAdapter{svc: c.Memory.DLQReplay})
	adminGroup.POST("/memory/dlq/replay", h.Replay)
}

func registerModelCatalogue(r *gin.Engine, c *wiring.Container) {
	if c.LLMGateway == nil {
		return
	}
	modelHandler := handler.NewModelHandler(c.LLMGateway.ModelService)
	models := r.Group("/models", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	models.GET("", modelHandler.ListModels)
}

// registerE2ERoutes exposes a read-only route inventory for the stateful
// E2E coverage report. Paths are gin template form (e.g. /agents/:id).
// Unauthenticated: it carries no business data, only route shapes.
func registerE2ERoutes(r *gin.Engine) {
	r.GET("/e2e/routes", func(ctx *gin.Context) {
		type routeEntry struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		}
		routes := make([]routeEntry, 0)
		for _, route := range r.Routes() {
			routes = append(routes, routeEntry{Method: route.Method, Path: route.Path})
		}
		ctx.JSON(http.StatusOK, gin.H{"routes": routes})
	})
}

// registerHealth wires unauthenticated observability and health endpoints.
func registerHealth(r *gin.Engine, c *wiring.Container) {
	r.GET("/metrics", gin.WrapH(c.Platform.Metrics.GetHandler()))
	r.GET("/livez", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/readyz", readinessHandler(c.ReadinessCheck))
	r.GET("/health", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"status": "ok", "service": "Stratum"})
	})
}

func readinessHandler(check func(context.Context) map[string]error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if check == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), constants.RouterHealthTimeout)
		defer cancel()
		for _, err := range check(ctx) {
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func protectedTenantMiddleware(c *wiring.Container, extra ...gin.HandlerFunc) []gin.HandlerFunc {
	if c.Platform == nil || c.Platform.JWTService == nil {
		return []gin.HandlerFunc{func(ctx *gin.Context) {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
		}}
	}
	mw := []gin.HandlerFunc{middleware.JWTMiddleware(c.Platform.JWTService, c.Platform.Metrics), middleware.InjectTenantContext()}
	return append(mw, extra...)
}

// registerSkills wires versioned instruction bundles. Skills are activated by
// the Agent loop; they are never executed directly through an HTTP endpoint.
func registerSkills(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Skill == nil || c.Skill.VersionService == nil {
		return
	}
	skillHandler := handler.NewSkillHandler(c.Skill.VersionService, c.Logger)

	skills := r.Group("/skills", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	{
		skills.GET("", skillHandler.GetAllSkills)
		skills.GET("/:id/workspace", skillHandler.GetSkillWorkspace)
		skills.GET("/:id", skillHandler.GetSkill)

		adminMW := []gin.HandlerFunc{middleware.RequireTenantRole("admin")}
		skills.POST("", append(adminMW, requireActive, skillHandler.CreateSkill)...)
		// 编辑与回滚向白名单成员开放：组级别 RequireTenantRole("member") +
		// service 层 resolveUpdateActor 白名单校验，不在此再叠加 admin 门槛。
		skills.PATCH("/:id", requireActive, skillHandler.UpdateSkill)
		// 草稿流转：保存/发布/撤销草稿与编辑同级(member 级 requireActive)，
		// 编辑人经 service 白名单校验；发布/保存共用乐观并发基线。
		skills.POST("/:id/draft", requireActive, skillHandler.SaveSkillDraft)
		skills.POST("/:id/publish", requireActive, skillHandler.PublishSkillDraft)
		skills.DELETE("/:id/draft", requireActive, skillHandler.DiscardSkillDraft)
		skills.POST("/:id/rollback", requireActive, skillHandler.RollbackSkill)
		skills.GET("/:id/revisions", skillHandler.ListSkillRevisions)
		skills.DELETE("/:id", append(adminMW, requireActive, skillHandler.DeleteSkill)...)
		skills.PUT("/:id/editors", append(adminMW, requireActive, skillHandler.SetSkillEditors)...)
	}
}

// registerAgents wires /agents/* and /conversations/* under JWT + tenant
// context. Agent + chat handlers share middleware. Read + execute + chat
// stay open to members; create/update/delete require admin so ordinary
// tenant members can only use agents, not modify them.
func registerAgents(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Agent == nil || c.Agent.Service == nil {
		return
	}
	agentHandler := handler.NewAgentHandler(c.Agent.Service, c.Logger).WithActionExecutor(c.Agent.ActionExecutor)
	chatHandler := handler.NewChatHandler(c.Agent.ChatStore, c.Logger)

	requireAdmin := middleware.RequireTenantRole("admin")

	agents := r.Group("/agents", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	{
		agents.GET("", agentHandler.GetAllAgents)
		agents.POST("", requireAdmin, requireActive, agentHandler.CreateAgent)
		agents.GET("/executions", agentHandler.ListExecutions)
		agents.GET("/executions/:traceID/tool-traces", agentHandler.ListExecutionToolTraces)
		agents.GET("/executions/:traceID/trace-events", agentHandler.ListExecutionTraceEvents)
		agents.GET("/tool-approvals", agentHandler.ListToolApprovals)
		agents.POST("/tool-approvals/:approvalID/decision", requireAdmin, requireActive, agentHandler.DecideToolApproval)
		agents.POST("/tool-approvals/:approvalID/resume", requireAdmin, requireActive, agentHandler.ResumeToolApproval)
		// D4：审批工作台——历史/详情/执行/指定审批人。
		// history/detail 对 member 开放（M4/D4）：service 内做"发起人或 admin/owner"归属校验，
		// member 仅看自己发起的、详情归属自己，非归属 404（关闭 oracle）。decision/execute/assignee 仍 requireAdmin。
		agents.GET("/tool-approvals/history", requireActive, agentHandler.ListApprovalHistory)
		agents.GET("/tool-approvals/:approvalID", requireActive, agentHandler.GetApprovalDetail)
		agents.POST("/tool-approvals/:approvalID/execute", requireAdmin, requireActive, agentHandler.ExecuteApproval)
		// 取消待批审批：requireActive 而非 requireAdmin——发起人（member）要能取消自己的
		// 审批；越权由 service 层 row.UserID 归属校验兜底（ErrApprovalNotFound 关闭 oracle）。
		agents.POST("/tool-approvals/:approvalID/cancel", requireActive, agentHandler.CancelToolApproval)
		agents.PUT("/tool-approvals/:approvalID/assignee", requireAdmin, requireActive, agentHandler.SetApprovalAssignee)
		agents.GET("/:id", agentHandler.GetAgent)
		execLimiter := newRateLimiterStore(c, middleware.LLMExecRate, middleware.LLMExecBurst)
		execRateLimit := middleware.RateLimitByKey(execLimiter, func(c *gin.Context) string {
			tid, _ := c.Get("auth.tenant_id")
			uid, _ := c.Get("auth.sub")
			return fmt.Sprintf("%v:%v", tid, uid)
		})
		agents.POST("/:id/execute", requireActive, execRateLimit, agentHandler.ExecuteAgent)
		agents.POST("/:id/execute/stream", requireActive, execRateLimit, agentHandler.ExecuteAgentStream)
		agents.POST("/:id/executions/:executionID/pause", requireActive, agentHandler.PauseExecution)
		agents.POST("/:id/executions/:executionID/resume", requireActive, agentHandler.ResumeExecution)
		// P1/P2：白名单成员可编辑——update 门控放宽到 member+，真实鉴权由 service
		// ownership 矩阵完成（owner/admin/creator/白名单 editor 放行，其余 ErrForbidden）；
		// editors 管理同样放宽，SetEditors 内部仍限 creator/owner（editors=nil 拒编辑人委托）。
		agents.PUT("/:id", requireActive, agentHandler.UpdateAgent)
		agents.PUT("/:id/editors", requireActive, agentHandler.SetAgentEditors)
		// 版本历史/回滚：与 skill 语义一致——member 级，归属/白名单鉴权在 service
		// ownership 矩阵内完成（owner/admin/creator/白名单 editor 放行，其余 ErrForbidden）。
		agents.GET("/:id/versions", requireActive, agentHandler.ListAgentVersions)
		// 单版本内容：返回该版整份 payload + safeSummary + parentVersionId，供「详情」
		// Drawer 以其直父版本 payload 为基线现算字段前后值。
		agents.GET("/:id/versions/:versionID", requireActive, agentHandler.GetAgentVersion)
		agents.POST("/:id/rollback", requireActive, agentHandler.RollbackAgent)
		agents.DELETE("/:id", requireAdmin, requireActive, agentHandler.DeleteAgent)
		agents.POST("/:id/conversations", chatHandler.CreateConversation)
		agents.GET("/:id/conversations", chatHandler.ListConversations)
	}
	conversations := r.Group("/conversations", protectedTenantMiddleware(c)...)
	{
		conversations.PATCH("/:convID", chatHandler.RenameConversation)
		conversations.DELETE("/:convID", chatHandler.DeleteConversation)
		conversations.GET("/:convID/messages", chatHandler.ListMessages)
		conversations.POST("/:convID/messages", chatHandler.AddMessage)
		// 会话刷新恢复：返回该会话的进行中执行（running/paused/waiting_approval）
		// 及其恢复键 execution_id；member 级，服务内做会话归属校验。
		conversations.GET("/:convID/active-execution", agentHandler.GetActiveExecution)
	}
}

func newRateLimiterStore(c *wiring.Container, limit rate.Limit, burst int) *middleware.RateLimiterStore {
	if c.Storage != nil && c.Storage.Redis != nil {
		return middleware.NewRedisRateLimiterStore(c.Storage.Redis.Client(), limit, burst)
	}
	return middleware.NewRateLimiterStore(limit, burst)
}

// registerCollab wires /collaborations/* for all members. Start/cancel
// authorization (creator vs admin/owner) is enforced by the service.
func registerCollab(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Collab == nil || c.Collab.Service == nil {
		return
	}
	h := handler.NewCollaborationHandler(c.Collab.Service)
	member := protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))
	routes := r.Group("/collaborations", member...)
	routes.Use(requireActive)
	routes.GET("", h.List)
	routes.POST("", h.Create)
	routes.GET("/:id", h.Get)
	routes.POST("/:id/start", h.Start)
	routes.POST("/:id/cancel", h.Cancel)
}

// registerScheduledTasks wires /scheduled-tasks/*: reads are member-level,
// writes (create/update/delete/enable) require admin plus an active tenant.
// Params use :id so scripts/record-contracts.go resolves them as named paths.
func registerScheduledTasks(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Scheduler == nil || c.Scheduler.Service == nil {
		return
	}
	h := handler.NewScheduledTaskHandler(c.Scheduler.Service)
	member := protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))
	admin := append(protectedTenantMiddleware(c, middleware.RequireTenantRole("admin")), requireActive)
	group := r.Group("/scheduled-tasks", member...)
	{
		group.GET("", h.List)
		group.GET("/:id", h.Get)
		group.POST("", append(admin, h.Create)...)
		group.PUT("/:id", append(admin, h.Update)...)
		group.DELETE("/:id", append(admin, h.Delete)...)
		group.PATCH("/:id/enabled", append(admin, h.SetEnabled)...)
	}
}

// registerKnowledge wires /knowledge/* under JWT + tenant context with
// member/admin role split for read vs write.
func registerKnowledge(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Knowledge == nil || c.Knowledge.RAGService == nil {
		return
	}
	ragHandler := handler.NewRAGHandler(c.Knowledge.RAGService, c.Knowledge.WorkspaceService, c.Logger)

	knowledgeGroup := r.Group("/knowledge", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	{
		knowledgeGroup.GET("/workspaces", ragHandler.ListWorkspaces)
		knowledgeGroup.GET("/workspaces/:name/stats", ragHandler.GetWorkspaceStats)
		knowledgeGroup.GET("/workspaces/:name/documents", ragHandler.ListDocuments)
		knowledgeGroup.GET("/workspaces/:name/documents/:documentID/preview", requireActive, ragHandler.PreviewDocument)
		knowledgeGroup.POST("/query", requireActive, ragHandler.Query)

		adminMW := []gin.HandlerFunc{middleware.RequireTenantRole("admin")}
		knowledgeGroup.POST("/workspaces", append(adminMW, requireActive, ragHandler.CreateWorkspace)...)
		// PATCH workspace 对白名单 member 开放（service UpdateWorkspace 所有权矩阵
		// fail-closed：owner/admin 放行，白名单 member 放行，其余 403）。白名单 member
		// 可改 name/description/检索参数（config 中 embedding/chunk 等不可变字段由
		// domain applyImmutableSettings 兜底）；upload（POST /ingest）同样对白名单
		// member 开放。doc/access/delete/rollback/editors/create 保持 admin 门禁。
		knowledgeGroup.PATCH("/workspaces/:name", requireActive, ragHandler.UpdateWorkspace)
		// 版本历史/回滚：历史 GET member 级（对齐 agent/skill），回滚写 admin
		// （spec：入口仅 isAdmin 可见）。
		knowledgeGroup.GET("/workspaces/:name/versions", requireActive, ragHandler.ListWorkspaceVersions)
		// 单版本内容 GET（member 级，同列表）：详情 Drawer 取点击版与直父版两次内容。
		knowledgeGroup.GET("/workspaces/:name/versions/:versionID", requireActive, ragHandler.GetWorkspaceVersion)
		knowledgeGroup.POST("/workspaces/:name/rollback", append(adminMW, requireActive, ragHandler.RollbackWorkspace)...)
		knowledgeGroup.DELETE("/workspaces/:name", append(adminMW, requireActive, ragHandler.DeleteWorkspace)...)
		knowledgeGroup.PUT("/workspaces/:name/editors", append(adminMW, requireActive, ragHandler.SetWorkspaceEditors)...)
		knowledgeGroup.DELETE("/workspaces/:name/documents/:documentID", append(adminMW, requireActive, ragHandler.DeleteDocument)...)
		knowledgeGroup.PUT("/workspaces/:name/documents/:documentID/access",
			append(adminMW, requireActive, ragHandler.SetDocumentAccess)...)
		knowledgeGroup.POST("/ingest", requireActive, middleware.BodyLimit(constants.MaxUploadBytes), ragHandler.UploadDocument)
	}
}

// registerMCP wires /mcp/* via the handler's RegisterRoutes.
//   - base:  JWT + tenant context + member 底线（所有路由，含读取与工具执行）。
//   - write: member 可执行的运行时操作追加 requireActive（工具执行）。
//   - admin: 服务器管理类操作（连接/更新/断开/删除配置/重连/刷新技能）要求 admin+。
func registerMCP(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.MCP == nil || c.MCP.Service == nil {
		return
	}
	mcpHandler := handler.NewMCPHandler(c.MCP.Service, c.Logger)
	if c.Agent != nil && c.Agent.ApprovalService != nil {
		// D5：member 配置写操作创建审批（缺装配时 handler 内部 fail closed 503）。
		mcpHandler = mcpHandler.WithApprovalService(c.Agent.ApprovalService)
		// 角色分流现查（单事实源）：不信任 JWT role claim 的陈旧窗口。
		if roles := c.Agent.RoleResolver; roles != nil {
			mcpHandler = mcpHandler.WithRoleResolver(roles)
		}
	}

	base := protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))
	writeMW := []gin.HandlerFunc{requireActive}
	adminMW := []gin.HandlerFunc{middleware.RequireTenantRole("admin"), requireActive}

	mcpHandler.RegisterRoutes(r, base, writeMW, adminMW)
}

func registerMemory(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Memory == nil || c.Platform.JWTService == nil {
		return
	}

	// LLMGateway 可能未构建（DB 不可用），handler 内部对 nil resolver fail-closed。
	var embedSvc handler.MemoryEmbeddingModelResolver
	if c.LLMGateway != nil {
		embedSvc = c.LLMGateway.TenantEmbeddingResolver
	}
	userHandler := handler.NewUserMemoryHandler(c.Memory.Service, c.Memory.Manager, embedSvc)
	g := r.Group("/memory", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	g.Use(requireActive)
	g.DELETE("/clear", userHandler.ClearMemories)
	g.GET("", userHandler.ListMemories)
	g.POST("/sessions", userHandler.ListSessions)
	g.GET("/stats", userHandler.GetStats)
	g.GET("/entities", userHandler.GetEntities)
	g.GET("/summary/:session_id", userHandler.GetSummary)
	g.DELETE("/session/:session_id", userHandler.ClearSession)
	g.GET("/facts", userHandler.ListFacts)
	g.GET("/facts/:id", userHandler.GetFact)
	g.PATCH("/facts/:id", userHandler.UpdateFact)
	g.DELETE("/facts/:id", userHandler.DeleteFact)
	g.DELETE("/entities/:id", userHandler.DeleteEntity)
	g.GET("/summaries", userHandler.ListSummaries)
	g.DELETE("/summaries/:id", userHandler.DeleteSummary)
	g.GET("/snapshots", userHandler.ListSnapshots)
	g.PATCH("/snapshots/:agent_id", userHandler.UpdateSnapshot)
	g.DELETE("/snapshots/:agent_id", userHandler.DeleteSnapshot)
	g.GET("/entries", userHandler.ListEntries)
	g.DELETE("/entries/:id", userHandler.DeleteEntry)
}

// registerLLMAdmin wires /admin/providers and /admin/models under JWT + tenant
// context with the admin role. These routes are only registered when the
// LLMGateway is fully built (DB available).
func registerAudit(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.Audit == nil || c.Audit.QueryService == nil {
		return
	}
	h := handler.NewAuditHandler(c.Audit.QueryService, c.Logger)
	// 审计日志:租户内 admin/owner 可见(owner 经 admin gate 通过)。
	auditGroup := r.Group("/audit", protectedTenantMiddleware(c, middleware.RequireTenantRole("admin"))...)
	auditGroup.Use(requireActive)
	auditGroup.GET("/events", h.ListEvents)
	auditGroup.GET("/events/:id", h.GetEvent)
	if c.Audit.PlatformQueryService != nil {
		platformHandler := handler.NewPlatformAuditHandler(c.Audit.PlatformQueryService)
		if c.Platform == nil || c.Platform.JWTService == nil {
			return
		}
		platformAudit := r.Group("/admin/audit/platform",
			protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
		platformAudit.GET("/events", platformHandler.List)
		platformAudit.GET("/events/:id", platformHandler.Get)
	}
}

func registerLLMAdmin(r *gin.Engine, c *wiring.Container, requireActive gin.HandlerFunc) {
	if c.LLMGateway == nil || c.LLMGateway.ProviderService == nil || c.LLMGateway.ModelMgmtService == nil {
		return
	}
	providerH := handler.NewProviderHandler(c.LLMGateway.ProviderService)
	modelMgmtH := handler.NewModelMgmtHandler(c.LLMGateway.ModelMgmtService)
	// The catalog is public/platform-scoped. Tenant administrators may read it,
	// but every mutation must be authorized by the system-admin claim (or above).
	adminMW := middleware.RequireSystemAdmin()

	// Providers: list is readable by any tenant member; write ops require admin.
	providers := r.Group("/admin/providers", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	{
		providers.GET("", providerH.List)
		providers.POST("", adminMW, requireActive, providerH.Create)
		providers.PUT("/:id", adminMW, requireActive, providerH.Update)
		providers.DELETE("/:id", adminMW, requireActive, providerH.Delete)
		providers.POST("/:id/discover", adminMW, requireActive, providerH.Discover)
		providers.POST("/:id/health", adminMW, requireActive, providerH.HealthCheck)
	}

	// Models: list and get are readable by any tenant member; write ops require admin.
	models := r.Group("/admin/models", protectedTenantMiddleware(c, middleware.RequireTenantRole("member"))...)
	{
		models.GET("", modelMgmtH.List)
		models.POST("", adminMW, modelMgmtH.Create)
		models.GET("/:id", modelMgmtH.Get)
		models.PUT("/:id", adminMW, modelMgmtH.Update)
		models.PATCH("/:id/policy", adminMW, requireActive, modelMgmtH.UpdatePolicy)
		models.PATCH("/:id/toggle", adminMW, modelMgmtH.Toggle)
		models.DELETE("/:id", adminMW, modelMgmtH.Delete)
	}
}
