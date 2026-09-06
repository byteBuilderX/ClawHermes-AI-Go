package wiring

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agent "github.com/byteBuilderX/stratum/internal/agent/application"
	"github.com/byteBuilderX/stratum/internal/agent/application/factcheck"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	capgateway "github.com/byteBuilderX/stratum/internal/agent/infrastructure/capability"
	agentobjects "github.com/byteBuilderX/stratum/internal/agent/infrastructure/objectstore"
	"github.com/byteBuilderX/stratum/internal/agent/infrastructure/officialdocs"
	agentopik "github.com/byteBuilderX/stratum/internal/agent/infrastructure/opik"
	persistence "github.com/byteBuilderX/stratum/internal/agent/infrastructure/persistence"
	iampersistence "github.com/byteBuilderX/stratum/internal/iam/infrastructure/persistence"
	knowledge "github.com/byteBuilderX/stratum/internal/knowledge/application"
	llmgatewaydomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	llmgateway "github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	memapp "github.com/byteBuilderX/stratum/internal/memory/application"
	pipeline "github.com/byteBuilderX/stratum/internal/memory/infrastructure/pipeline"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	versioningpersistence "github.com/byteBuilderX/stratum/internal/versioning/infrastructure/persistence"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	pkgobjectstore "github.com/byteBuilderX/stratum/pkg/storage/objectstore"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// agentCheckpointTenantLister adapts *pgxpool.Pool to the
// func(ctx) ([]string, error) signature consumed by
// CheckpointCleanupWorker.
type agentCheckpointTenantLister struct {
	pool *pgxpool.Pool
}

func (l agentCheckpointTenantLister) list(ctx context.Context) ([]string, error) {
	schemas, err := postgres.ListTenantSchemas(ctx, l.pool)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		ids = append(ids, strings.TrimPrefix(schema, "tenant_"))
	}
	return ids, nil
}

// Agent groups the agent persistence/registry services and execution
// stores. The Registry is wired with CapabilityGateway and MemoryInjector
// so agents resolved from DB inherit those capabilities at construction
// time. Service is the orchestration façade handlers consume.
type Agent struct {
	Registry *agent.Registry
	Service  *agent.AgentService
	// AgentRepo exposes the pg agent repository for cross-context consumers
	// built earlier than buildAgent (memory pipeline). It is read lazily via
	// closure so the nil window during startup is never dereferenced.
	AgentRepo           agentport.AgentRepo
	ChatStore           agent.ChatStore
	EvidenceProvider    agentport.TraceEvidenceProvider
	TracePayloadStore   agentport.TracePayloadStore
	RevisionObjectStore pkgobjectstore.Store
	CheckpointStore     agent.CheckpointStore
	// CompactionStore 跨轮复用压缩摘要存储（模型 A 累计覆盖）。nil 时组装侧
	// 保持无复用行为。
	CompactionStore   agentport.CompactionStore
	CheckpointCleanup *agent.CheckpointCleanupWorker
	TaskCleanup       *agent.TaskCleanupWorker
	ApprovalCleanup   *agent.ApprovalCleanupWorker
	ApprovalStore     agentport.ToolApprovalRepo
	TaskStore         agentport.TaskRepo
	ApprovalService   *agent.ToolApprovalService
	// ActionExecutor 执行审批通过后的动作（D4/D5），由 buildEvaluation 在评测
	// 组件就绪后装配；nil 时执行端点 fail closed。
	ActionExecutor agentport.ApprovalActionExecutor
	// RoleResolver 现查 actor 的租户角色（单事实源）。injectTenantRoleResolvers
	// 在完整 resource stack 装配时设置；nil 时消费方回退 JWT role claim（仅测试路径）。
	RoleResolver         agentport.TenantRoleResolver
	TenantResolver       agentport.TenantCapabilityResolver
	SkillLookup          agentport.SkillLookup
	DiagnosticProvider   agentport.DiagnosticEvidenceProvider
	ProposalService      *agent.ResourceChangeProposalService
	OperationGateService *agent.OperationGateService
	OperationProposalSvc *agent.OperationProposalService
}

// ragSearchAdapter wraps *knowledge.RAGService to satisfy
// agentport.RAGSearchProvider. Lives in wiring (the composition root)
// so neither agent/application nor knowledge/application has to know
// about the other.
type ragSearchAdapter struct {
	rag *knowledge.RAGService
}

type tenantMemberRoleService interface {
	GetMemberRole(ctx context.Context, tenantID, userID string) (string, error)
}

type agentToolUserScopeResolver struct {
	members tenantMemberRoleService
}

func (r agentToolUserScopeResolver) ResolveToolUserScope(
	ctx context.Context,
	tenantID, userID, _, _ string,
) (agentport.ToolUserScope, error) {
	if r.members == nil {
		return agentport.ToolUserScope{}, fmt.Errorf("resolve agent tool user scope: tenant membership service unavailable")
	}
	role, err := r.members.GetMemberRole(ctx, tenantID, userID)
	if err != nil {
		return agentport.ToolUserScope{}, fmt.Errorf("resolve agent tool user scope: %w", err)
	}
	switch role {
	case "member", "admin", "owner":
		return agentport.ToolUserScope{UserActive: true, AllowsTool: true}, nil
	default:
		return agentport.ToolUserScope{}, fmt.Errorf("resolve agent tool user scope: unsupported tenant role")
	}
}

func (a ragSearchAdapter) SearchKnowledge(
	ctx context.Context, tenantID string, workspaceIDs []string, query string, topK int, viewerID string,
) (string, error) {
	return knowledge.NewRAGSearchFn(a.rag, tenantID, viewerID)(ctx, workspaceIDs, query, topK)
}

// agentPlatformPromptResolver 装配 agent 侧平台提示词解析器（复用参数注册表
// 平台解析器，与 memory 侧同源）。参数服务不可用时返回 nil——生产 wiring 恒
// 注入，nil 仅测试/降级路径出现，消费方 fail-closed。
func (c *Container) agentPlatformPromptResolver() agentport.PlatformPromptResolver {
	if c.Parameters == nil || c.Parameters.Service == nil {
		return nil
	}
	return platformParamResolver{svc: c.Parameters.Service}
}

// SearchKnowledgeWithEvidence implements agentport.RAGSearchEvidenceProvider:
// same fan-out as SearchKnowledge but retaining chunk-level provenance so
// agent tool observations can record retrieval evidence.
func (a ragSearchAdapter) SearchKnowledgeWithEvidence(
	ctx context.Context, tenantID string, workspaceIDs []string, query string, topK int, viewerID string,
) (agentport.RAGSearchEvidence, error) {
	ev, err := knowledge.NewRAGSearchEvidenceFn(a.rag, tenantID, viewerID)(ctx, workspaceIDs, query, topK)
	if err != nil {
		return agentport.RAGSearchEvidence{}, err
	}
	out := agentport.RAGSearchEvidence{Content: ev.Content, Sources: make([]agentport.RAGSearchSource, 0, len(ev.Sources))}
	for _, src := range ev.Sources {
		out.Sources = append(out.Sources, agentport.RAGSearchSource{
			WorkspaceID: src.WorkspaceID, WorkspaceName: src.WorkspaceName, ChunkID: src.ChunkID,
			DocumentID: src.DocumentID, DocumentTitle: src.DocumentTitle, Snippet: src.Snippet,
			Score: src.Score, HasScore: src.HasScore,
		})
	}
	// NoAnswer 透传：knowledge 信号 → agent 域类型（字段一一对应，reason
	// 枚举值跨 context 由 pkg/constants 单一事实源对齐）。
	if ev.NoAnswer != nil {
		out.NoAnswer = &domain.NoAnswerInfo{
			Reason:         domain.NoAnswerReason(ev.NoAnswer.Reason),
			RetrievedCount: ev.NoAnswer.RetrievedCount,
			FilteredCount:  ev.NoAnswer.FilteredCount,
			BestScore:      ev.NoAnswer.BestScore,
			Retried:        ev.NoAnswer.Retried,
			RewrittenQuery: ev.NoAnswer.RewrittenQuery,
			Detail:         ev.NoAnswer.Detail,
		}
	}
	return out, nil
}

var _ agentport.RAGSearchEvidenceProvider = ragSearchAdapter{}

func (a ragSearchAdapter) SearchKnowledgeRevision(
	ctx context.Context, tenantID string, revision agentport.KnowledgeRetrievalRevision, query string, viewerID string,
) (string, error) {
	if a.rag == nil {
		return "", fmt.Errorf("Knowledge revision search: RAG service unavailable")
	}
	ctx = reqctx.WithTenantID(ctx, tenantID)
	// D3: the revision path resolves its visible set inside RAGService.Query
	// (the snapshot carries the workspace ID); viewerID is the end-user
	// identity and the evaluator fails closed when it is missing.
	return knowledge.NewRetrievalEvaluator(a.rag).RetrieveContext(ctx, knowledge.RetrievalSnapshot{
		WorkspaceID: revision.WorkspaceID, WorkspaceName: revision.WorkspaceName,
		EmbeddingModel: revision.EmbeddingModel, QueryMode: revision.QueryMode, TopK: revision.TopK,
		ScoreThreshold: revision.ScoreThreshold, Reranking: revision.Reranking,
		QueryRewrite: revision.QueryRewrite,
	}, query, viewerID)
}

// skillVersionService returns the wired skill VersionService, or nil when the
// skill context was built without a database. The resolver treats a nil
// service as an empty catalog, so agent construction never panics on it.
func skillVersionService(c *Container) *skillapp.VersionService {
	if c.Skill == nil {
		return nil
	}
	return c.Skill.VersionService
}

// knowledgeWorkspaceService returns the wired knowledge WorkspaceService, or
// nil when the knowledge context was built without a database. Consumers
// treat a nil service as fail-closed (bindings rejected).
func knowledgeWorkspaceService(c *Container) *knowledge.WorkspaceService {
	if c.Knowledge == nil {
		return nil
	}
	return c.Knowledge.WorkspaceService
}

// workspaceBindingAdapter adapts *knowledge.WorkspaceService onto the
// consuming contexts' WorkspaceBindingValidator ports (agent and skill each
// define their own same-shaped interface; this single adapter satisfies
// both). Lives in wiring so neither context imports the other.
type workspaceBindingAdapter struct {
	ws *knowledge.WorkspaceService
}

func (a workspaceBindingAdapter) ValidateWorkspaceBindings(ctx context.Context, tenantID string, ids []string) error {
	if a.ws == nil {
		return fmt.Errorf("knowledge: workspace binding validation unavailable (workspace service not wired)")
	}
	return a.ws.ValidateWorkspaceBindings(ctx, tenantID, ids)
}

func tenantMemberService(c *Container) tenantMemberRoleService {
	if c.IAM == nil {
		return nil
	}
	return c.IAM.TenantService
}

// publishedSkillActivationResolver adapts skill/application's context-neutral
// VersionService.ResolveActivation onto agentport.SkillActivationResolver.
// The activation query (active-revision fallback, published/candidate status
// filter, contract name/description fallback) lives in the skill context; the
// composition root only maps the returned view onto the agent port's shape.
type publishedSkillActivationResolver struct {
	versions *skillapp.VersionService
}

func (r publishedSkillActivationResolver) ResolveSkills(
	ctx context.Context, _ string, refs []agentport.SkillRevisionRef,
) (map[string]agentport.SkillActivation, error) {
	catalog := make(map[string]agentport.SkillActivation, len(refs))
	if r.versions == nil || len(refs) == 0 {
		return catalog, nil
	}
	for _, ref := range refs {
		view, found, err := r.versions.ResolveActivation(ctx, ref.SkillID, ref.RevisionID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		catalog[view.SkillID] = agentport.SkillActivation{
			SkillID:      view.SkillID,
			RevisionID:   view.RevisionID,
			Name:         view.Name,
			Description:  view.Description,
			Instructions: view.Instructions,
			InputSchema:  view.InputSchema,
			OutputSchema: view.OutputSchema,
		}
	}
	return catalog, nil
}

func (c *Container) buildAgent(ctx context.Context) error {
	db := c.dbOrNil()

	var repo agentport.AgentRepo
	if db != nil {
		repo = persistence.NewPgAgentRepo(db)
	}
	registry := agent.NewRegistry(repo, c.Logger)
	if c.Memory != nil && c.Memory.Injector != nil {
		registry.SetMemoryInjector(c.Memory.Injector)
	}
	registry.SetPlatformPromptResolver(c.agentPlatformPromptResolver())
	if c.Memory != nil && c.Memory.RecallFn != nil {
		registry.SetRecallMemoryFn(c.Memory.RecallFn)
	}

	evidenceProvider := agentopik.NewClient(agentopik.Config{
		BaseURL: c.Config.Opik.URL, Project: c.Config.Opik.Project, Workspace: c.Config.Opik.Workspace,
		APIKey: c.Config.Opik.APIKey, Timeout: c.Config.Opik.Timeout,
	})
	a := &Agent{Registry: registry, EvidenceProvider: evidenceProvider, AgentRepo: repo}
	if c.Config.TracePayload.Enabled {
		store := agentobjects.NewStore(c.revisionObjectClient, c.Config.TracePayload.Bucket, c.Platform.AESKey)
		a.TracePayloadStore = store
		a.RevisionObjectStore = c.RevisionObjectStore
	}
	if db != nil {
		a.CheckpointStore = persistence.NewPgCheckpointStore(db)
		a.TaskStore = persistence.NewPgTaskRepo(db)
		// 任务持久化经 Registry 注入每个 hydrate 的 agent（persistTaskSnapshot
		// 与恢复链路依赖 BaseAgent.TaskStore，必须与仓库实例同源）。
		registry.SetTaskStore(a.TaskStore)
		a.ApprovalStore = persistence.NewPgToolApprovalStore(db)
		chatStore := persistence.NewPgChatStore(db, c.Logger)
		// D9 会话删除级联：DeleteConversation 在同一租户事务内终结关联审批。
		chatStore.SetApprovalCascade(a.ApprovalStore)
		// 生命周期解耦：会话删除只解除 task 引用，task 保留可恢复。
		chatStore.SetTaskDetach(a.TaskStore)
		a.ChatStore = chatStore
		a.CompactionStore = persistence.NewPgCompactionStore(db)
		a.ApprovalService = agent.NewToolApprovalService(a.ApprovalStore, a.CheckpointStore, c.Platform.AESKey)
		// 审批列表/详情的昵称解析（display_name > github_login > raw id），iam 全局 users 表。
		a.ApprovalService.SetActorNameResolver(iampersistence.NewPgActorNameResolver(db))
		a.CheckpointCleanup = agent.NewCheckpointCleanupWorker(
			agentCheckpointTenantLister{pool: db}.list,
			a.CheckpointStore,
			10*time.Minute,
			c.Logger,
			c.platformMetrics(),
		)
		a.CheckpointCleanup.Start(ctx)
		wireCleanupWorkers(c, a, ctx)
		a.SkillLookup = persistence.NewPgSkillLookup(db)
		var registry *llmgateway.ModelRegistry
		var gw *llmgateway.Gateway
		if c.LLMGateway != nil {
			registry, gw = c.LLMGateway.Registry, c.LLMGateway.Gateway
		}
		a.TenantResolver = newTenantCapabilityResolver(registry, gw, c.Logger)
	}

	deps := agent.AgentServiceDeps{
		Registry:                registry,
		SkillLookup:             a.SkillLookup,
		SkillActivationResolver: publishedSkillActivationResolver{versions: skillVersionService(c)},
		TenantResolver:          a.TenantResolver,
		TenantModelValidator:    tenantModelValidator(a.TenantResolver),
		TenantModelCatalog:      tenantModelCatalog(a.TenantResolver),
		ModelContextProvider:    modelContextProvider(a.TenantResolver),
		ModelDetailsProvider:    tenantModelDetailsProvider(a.TenantResolver),
		VendorWindowLookup:      llmgateway.LookupModelSpec,
		HistoryCompactorFactory: func(gw agentport.CapabilityGateway, logger *zap.Logger, compactionMaxTokens int) agentport.HistoryCompactor {
			return capgateway.NewLLMHistoryCompactor(gw, logger, compactionMaxTokens, c.agentPlatformPromptResolver())
		},
		PlatformPromptResolver:    c.agentPlatformPromptResolver(),
		ChatStore:                 a.ChatStore,
		EvidenceProvider:          a.EvidenceProvider,
		TracePayloadStore:         a.TracePayloadStore,
		CheckpointStore:           a.CheckpointStore,
		CompactionStore:           a.CompactionStore,
		ApprovalService:           a.ApprovalService,
		ToolAuthorizer:            agent.NewToolAuthorizer(agentToolUserScopeResolver{members: tenantMemberService(c)}),
		WorkspaceBindingValidator: workspaceBindingAdapter{ws: knowledgeWorkspaceService(c)},
		FailureAudit:              failureRecorderOf(c),
		Logger:                    c.Logger,
	}
	if db != nil {
		deps.ResourceEditorRepo = persistence.NewPgResourceEditorRepo(db)
		// 通用产品版本历史（read-only）+ created_by 昵称解析，未装配 fail-closed。
		deps.VersionRepo = versioningpersistence.NewPgVersionRepo(db)
		deps.ActorNameResolver = iampersistence.NewPgActorNameResolver(db)
	}
	if c.Memory != nil {
		deps.MemoryInjector = c.Memory.Injector
		deps.RecallMemory = c.Memory.RecallFn
	}
	if c.MCP != nil {
		deps.MCPTools = c.MCP.AgentToolProvider
		deps.MCPToolExecutor = agentMCPExecutor{clients: c.MCP.Manager}
		deps.MCPToolPolicy = agentMCPPolicyResolver{service: c.MCP.Service}
		deps.MCPServerLister = agentMCPServerLister{service: c.MCP.Service}
	}
	if c.Knowledge != nil && c.Knowledge.RAGService != nil {
		deps.RAGSearch = ragSearchAdapter{rag: c.Knowledge.RAGService}
	}
	deps.FactCheck = agentFactCheckSettings(c)
	if c.Platform != nil {
		deps.Metrics = c.Platform.Metrics
	}
	wireTokenLedger(registry, &deps, c.Logger)
	deps.ParametersProvider = agentParametersProvider(c)
	deps.RuleGuard = agentRuleGuard(c, deps.Metrics)
	if c.Memory != nil && c.Memory.Service != nil {
		deps.MemoryCleaner = c.Memory.Service
		deps.MemoryBuffer = memoryBufferClosure(c.Memory.Service)
		deps.TrajectoryReflection = trajectoryReflectionClosure(c)
	}
	a.DiagnosticProvider = newDiagnosticProvider(c, a)
	deps.OfficialDocsSearch = officialdocs.Search
	deps.DiagnosticProvider = a.DiagnosticProvider
	a.Service = agent.NewAgentService(deps)
	if db != nil && c.Skill != nil && c.MCP != nil && c.Knowledge != nil &&
		c.Skill.VersionService != nil && c.MCP.Service != nil && c.Knowledge.WorkspaceService != nil {
		c.injectTenantRoleResolvers(a)
		adapters := NewResourceChangeProposalAdapters(
			a.Service, c.Skill.VersionService, c.MCP.Service, c.Knowledge.WorkspaceService,
		)
		a.ProposalService = agent.NewResourceChangeProposalService(
			persistence.NewPgResourceChangeProposalRepo(db),
			proposalAuthorizer{roles: newTenantRoleAdapter(c)},
			adapters,
			map[domain.ResourceKind]agentport.ResourceChangeApplier{
				domain.ResourceAgent: adapters, domain.ResourceSkillDraft: adapters,
				domain.ResourceMCPConfig: adapters, domain.ResourceKnowledgeWorkspace: adapters,
			},
			deps.Metrics, // nil is normalized to NoopMetrics inside the constructor
		)
		a.Service.SetResourceChangeProposalService(a.ProposalService)
		a.Service.SetResourceChangeApplier(adapters.ApplyDirectFromTool)
	}
	wireAgentObservability(c, a, deps.Metrics)
	c.Agent = a
	return nil
}

// injectTenantRoleResolvers hands one DB-backed role adapter to every service
// whose ownership checks need it. Called only when the full resource stack is
// wired; otherwise each service fails closed (nil resolver).
func (c *Container) injectTenantRoleResolvers(a *Agent) {
	roles := newTenantRoleAdapter(c)
	a.RoleResolver = roles
	a.Service.SetTenantRoleResolver(roles)
	a.ApprovalService.SetTenantRoleResolver(roles)
	c.Skill.VersionService.SetTenantRoleResolver(roles)
	c.MCP.Service.SetTenantRoleResolver(roles)
	c.Knowledge.WorkspaceService.SetTenantRoleResolver(roles)
	if c.Knowledge.RAGService != nil {
		c.Knowledge.RAGService.SetTenantRoleResolver(roles)
	}
}

// wireTaskCleanup assembles the TaskCleanupWorker for the DB-backed path and
// registers its shutdown. Extracted from buildAgent to keep that wiring
// function under the line-count gate; mirrors the inline CheckpointCleanup
// construction.
func wireTaskCleanup(c *Container, a *Agent, ctx context.Context) {
	db := c.dbOrNil()
	if db == nil {
		return
	}
	a.TaskCleanup = agent.NewTaskCleanupWorker(
		agentCheckpointTenantLister{pool: db}.list,
		a.TaskStore,
		constants.TaskCleanupInterval,
		c.Logger,
		c.platformMetrics(),
	)
	a.TaskCleanup.Start(ctx)
	c.shutdown = append(c.shutdown, func(context.Context) error {
		a.CheckpointCleanup.Stop()
		a.TaskCleanup.Stop()
		return nil
	})
}

// wireApprovalCleanup assembles the ApprovalCleanupWorker for the DB-backed path
// and registers its shutdown. Mirrors wireTaskCleanup.
func wireApprovalCleanup(c *Container, a *Agent, ctx context.Context) {
	db := c.dbOrNil()
	if db == nil {
		return
	}
	a.ApprovalCleanup = agent.NewApprovalCleanupWorker(
		agentCheckpointTenantLister{pool: db}.list,
		a.ApprovalStore,
		constants.ApprovalCleanupInterval,
		c.Logger,
		c.platformMetrics(),
	)
	a.ApprovalCleanup.Start(ctx)
	c.shutdown = append(c.shutdown, func(context.Context) error {
		a.ApprovalCleanup.Stop()
		return nil
	})
}

// wireCleanupWorkers assembles the DB-backed cleanup workers (task + approval)
// in a single buildAgent call site. Mirrors the CheckpointCleanup inline
// construction.
func wireCleanupWorkers(c *Container, a *Agent, ctx context.Context) {
	wireTaskCleanup(c, a, ctx)
	wireApprovalCleanup(c, a, ctx)
}

// wireTokenLedger 注入 TokenLedger 到 Registry 与 AgentService deps：span cost
// 此前恒 0（Noop 返回 0），接线后为真实 USD。ledger 不打 Prometheus usage ——
// token 计数由网关出站唯一记账（spec §11 D2①，C1 去重）。Registry.Get
// hydrate 的 agent 同样走执行链路，两个构建点必须同源。
func wireTokenLedger(registry *agent.Registry, deps *agent.AgentServiceDeps, logger *zap.Logger) {
	ledger := agent.NewTokenLedger(logger)
	registry.SetLedger(ledger)
	deps.Ledger = ledger
}

// wireOperationGate wires the T8 operation approval chain: the gate service
// plus the reviewer-facing proposal service, and injects the gate into the
// agent service. Skipped without a database.
func wireOperationGate(c *Container, a *Agent, metrics observability.MetricsProvider) {
	db := c.dbOrNil()
	if db == nil {
		return
	}
	if metrics == nil {
		metrics = observability.NoopMetrics{}
	}
	roles := newTenantRoleAdapter(c)
	a.OperationGateService = agent.NewOperationGateService(
		persistence.NewPgOperationProposalRepo(db),
		persistence.NewPgOperationUsageRepo(db),
		metrics,
	)
	a.OperationProposalSvc = agent.NewOperationProposalService(
		persistence.NewPgOperationProposalRepo(db),
		roles,
		metrics,
	)
	// grant_editor 批准即授予：按 resourceType 分发到各模块的授予实现。skill
	// 走共享 resource_editors 表（agent 模块仓库以 kind='skill' 写入，skill
	// 模块自身读该表判断白名单，不触碰 internal/skill/**）；knowledge_doc 走
	// 文档查看白名单幂等追加。createdBy 记系统 gate actor，与
	// gated_self_modify 的 operationGateActor 对齐。
	resourceEditors := persistence.NewPgResourceEditorRepo(db)
	a.OperationProposalSvc.WithGrantEditor(func(ctx context.Context, tenantID, resourceType, resourceID, editorID string) error {
		switch resourceType {
		case "agent":
			return a.Service.GrantEditorOnApproval(ctx, tenantID, resourceID, editorID)
		case "skill":
			return resourceEditors.AddEditorForKind(ctx, tenantID, "skill", resourceID, editorID, "operation-gate")
		case "knowledge_doc":
			if c.Knowledge == nil || c.Knowledge.DocRepo == nil {
				return fmt.Errorf("grant editor: knowledge doc repo not wired")
			}
			return c.Knowledge.DocRepo.AddAllowedUser(ctx, tenantID, resourceID, editorID)
		case "mcp":
			return resourceEditors.AddEditorForKind(ctx, tenantID, "mcp", resourceID, editorID, "operation-gate")
		case "knowledge_workspace":
			workspace, err := c.Knowledge.WorkspaceService.GetWorkspace(ctx, tenantID, resourceID)
			if err != nil {
				return err
			}
			return resourceEditors.AddEditorForKind(ctx, tenantID, "knowledge", workspace.ID, editorID, "operation-gate")
		case "workflow":
			return resourceEditors.AddEditorForKind(ctx, tenantID, "workflow", resourceID, editorID, "operation-gate")
		default:
			return fmt.Errorf("grant editor: unsupported resource type %q", resourceType)
		}
	})
	a.Service.SetOperationGate(a.OperationGateService)
}

// wireAgentObservability 装配 agent 侧门控与观测能力：操作审批门 + 运行态观测
// 引用事件发布器（P1a）。观测发布 best-effort：NATS 不可用仅 Warn，不阻断
// agent 执行；操作审批门仍按原语义装配。
func wireAgentObservability(c *Container, a *Agent, metrics observability.MetricsProvider) {
	wireOperationGate(c, a, metrics)
	if c.Storage == nil || c.Storage.NATS == nil {
		return
	}
	jsm, err := pipeline.NewJetStreamManager(c.Storage.NATS, c.Logger)
	if err != nil {
		c.Logger.Warn("agent observation emitter: jetstream manager unavailable", zap.Error(err))
		return
	}
	a.Service.SetObservationEmitter(&observationEmitterAdapter{js: jsm.JS(), logger: c.Logger})
}

func tenantModelValidator(resolver agentport.TenantCapabilityResolver) agentport.TenantChatModelValidator {
	validator, _ := resolver.(agentport.TenantChatModelValidator)
	return validator
}

func tenantModelCatalog(resolver agentport.TenantCapabilityResolver) agentport.TenantChatModelCatalog {
	catalog, _ := resolver.(agentport.TenantChatModelCatalog)
	return catalog
}

func modelContextProvider(resolver agentport.TenantCapabilityResolver) agentport.ModelContextProvider {
	provider, _ := resolver.(agentport.ModelContextProvider)
	return provider
}

func tenantModelDetailsProvider(resolver agentport.TenantCapabilityResolver) agentport.TenantModelDetailsProvider {
	provider, _ := resolver.(agentport.TenantModelDetailsProvider)
	return provider
}

// memoryBufferClosure adapts the memory BufferMessage service onto the
// agentport.MemoryBufferFn shape, stamping a fresh message id per turn.
func memoryBufferClosure(svc *memapp.MemoryService) func(ctx context.Context, tenantID, userID, agentID, conversationID, scope, role, content string) error {
	return func(ctx context.Context, tenantID, userID, agentID, conversationID, scope, role, content string) error {
		return svc.BufferMessage(ctx, &memapp.BufferMessageRequest{
			TenantID:       tenantID,
			UserID:         userID,
			AgentID:        agentID,
			ConversationID: conversationID,
			Scope:          scope,
			Role:           role,
			Content:        content,
			MessageID:      uuid.Must(uuid.NewV7()).String(),
			CreatedAt:      time.Now(),
		})
	}
}

// ruleGuardEnabled 读取平台参数 evaluation.ruleguard.enabled。默认关闭（fail open
// 于规则层：参数服务不可用或未显式开启时护栏静默放行，开启后才是 fail closed）。
// 参数读取失败属安全控件失效路径，Warn 留痕（与 observationEnabled 先例一致但补日志）。
func ruleGuardEnabled(ctx context.Context, logger *zap.Logger, params *parametersapp.Service) bool {
	if params == nil {
		return false
	}
	values, err := params.PlatformValues(ctx)
	if err != nil {
		logger.Warn("ruleguard enabled read failed, guard open", zap.Error(err))
		return false
	}
	enabled, _ := values["evaluation.ruleguard.enabled"].(bool)
	return enabled
}

// ruleGuardDenylist 读取平台参数 evaluation.ruleguard.denylist（逗号分隔工具名）。
// 读取失败或空值返回空切片：denylist 为空 = 无拦截项，规则层放行。参数读取失败
// 属安全控件失效路径，Warn 留痕（同 ruleGuardEnabled）。
func ruleGuardDenylist(ctx context.Context, logger *zap.Logger, params *parametersapp.Service) []string {
	if params == nil {
		return nil
	}
	values, err := params.PlatformValues(ctx)
	if err != nil {
		logger.Warn("ruleguard denylist read failed, guard open", zap.Error(err))
		return nil
	}
	raw, _ := values["evaluation.ruleguard.denylist"].(string)
	return strings.Split(raw, ",")
}

// agentRuleGuard 装配内联规则护栏（§4.1）。Enabled/Denylist 读取平台参数
// evaluation.ruleguard.*（默认关闭）；Metrics 可 nil（NewRuleGuard 归一为 NoopMetrics）。
func agentRuleGuard(c *Container, metrics observability.MetricsProvider) *agent.RuleGuard {
	return agent.NewRuleGuard(agent.RuleGuardDeps{
		Enabled:  func(ctx context.Context) bool { return ruleGuardEnabled(ctx, c.Logger, c.Parameters.Service) },
		Denylist: func(ctx context.Context) []string { return ruleGuardDenylist(ctx, c.Logger, c.Parameters.Service) },
		Metrics:  metrics,
		Logger:   c.Logger,
	})
}

// agentFactCheckSettings 按参数注册表 + gateway 可用性装配幻觉校验依赖（fail-closed：
// 任一缺失返回 nil，collectGraphResult 不校验）。judge 模型与 prompt 来自身份
// agent.factcheck.* 平台参数，均不兜底默认值：空 = 禁用。EvidenceFn 留空，
// collectGraphResult 执行时用 RAGSearchFnWithEvidence 填充（per-execution 已带
// tenant 权限）。
func agentFactCheckSettings(c *Container) *factcheck.Settings {
	settings, err := resolveFactCheckPlatformSettings(c)
	if err != nil {
		c.Logger.Warn("factcheck: platform settings resolve failed, disabled", zap.Error(err))
		return nil
	}
	if settings == nil {
		return nil
	}
	return settings
}

// resolveFactCheckPlatformSettings 从参数注册表解析 factcheck 平台配置。
func resolveFactCheckPlatformSettings(c *Container) (*factcheck.Settings, error) {
	if c.LLMGateway == nil || c.LLMGateway.Gateway == nil {
		return nil, nil
	}
	if c.Parameters == nil || c.Parameters.Service == nil || c.Parameters.Registry == nil {
		return nil, nil
	}
	return resolveFactCheckSettings(c.LLMGateway.Gateway, c.Parameters.Service.Resolver(), c.Logger)
}

// resolveFactCheckSettings 从参数解析器装配 factcheck 配置（纯逻辑，便于单测）。
// 对账轨（citation_verify）与 judge 轨（enabled）可独立开启：仅开对账时返回无
// Judge 的 settings（对账是代码级核验，不依赖 LLM）；仅开 judge 时维持既有语义。
// 两轨全关返回 nil（fail-closed）。enabled 但 gateway 缺失或 judge model/prompt
// 空 = 整体禁用（不兜底，沿既有 fail-closed）。
func resolveFactCheckSettings(gateway llmgatewaydomain.LLMCompleter, r fcResolver, logger *zap.Logger) (*factcheck.Settings, error) {
	ctx := context.Background()
	enabled, _ := resolveFCParam[bool](r, ctx, "agent.factcheck.enabled")
	citationVerify, _ := resolveFCParam[bool](r, ctx, "agent.factcheck.citation_verify")
	if !enabled && !citationVerify {
		return nil, nil
	}
	if enabled && gateway == nil {
		return nil, nil
	}
	settings := &factcheck.Settings{
		Enabled:        enabled,
		CitationVerify: citationVerify,
		Logger:         logger,
	}
	// judge 相关字段仅在 enabled 时解析（citation-only 允许 Judge==nil）。
	if enabled {
		model, _ := resolveFCParam[string](r, ctx, "agent.factcheck.judge.model")
		if model == "" {
			return nil, nil
		}
		prompt, _ := resolveFCParam[string](r, ctx, "agent.factcheck.judge.prompt")
		if prompt == "" {
			return nil, nil
		}
		topK, _ := resolveFCInt(r, ctx, "agent.factcheck.top_k")
		maxClaims, _ := resolveFCInt(r, ctx, "agent.factcheck.max_claims")
		var temperature *float64
		if t, ok := resolveFCFloat(r, ctx, "agent.factcheck.judge.temperature"); ok && t > 0 {
			temperature = &t
		}
		settings.TopK = topK
		settings.MaxClaims = maxClaims
		settings.Judge = factCheckJudge{
			completer:   gateway,
			model:       model,
			prompt:      prompt,
			temperature: temperature,
		}
	}
	return settings, nil
}

// fcResolver is the minimal interface for the parameter registry resolver.
type fcResolver interface {
	Resolve(ctx context.Context, key string, declared map[string]any) (any, bool, error)
}

// resolveFCParam resolves a typed bool/string parameter.
func resolveFCParam[T bool | string](r fcResolver, ctx context.Context, key string) (T, bool) {
	val, present, err := r.Resolve(ctx, key, nil)
	if err != nil || !present {
		var zero T
		return zero, false
	}
	v, ok := val.(T)
	if !ok {
		var zero T
		return zero, false
	}
	return v, true
}

// resolveFCInt resolves an int parameter via parsed int64 or float64.
func resolveFCInt(r fcResolver, ctx context.Context, key string) (int, bool) {
	val, present, err := r.Resolve(ctx, key, nil)
	if err != nil || !present {
		return 0, false
	}
	switch v := val.(type) {
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// resolveFCFloat resolves a float64 parameter via parsed float64 or int64.
func resolveFCFloat(r fcResolver, ctx context.Context, key string) (float64, bool) {
	val, present, err := r.Resolve(ctx, key, nil)
	if err != nil || !present {
		return 0, false
	}
	switch v := val.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// factCheckJudge 实现 factcheck.Judge（LLM-as-Judge 幻觉判定），走 llmgateway
// completer + json_object 结构化输出。prompt 是纯规则系统提示词（无占位符），
// 程序构造的 user message 包含 claims 和 evidence 的填充。只消费 claim 聚合证据，
// 不记录原始模型输出（日志白名单：judge 失败只记模型名，不记正文）。
type factCheckJudge struct {
	completer llmgatewaydomain.LLMCompleter
	model     string
	prompt    string
	// temperature 是 judge 采样温度；nil = 平台 unset（0）用模型/Provider 默认。
	temperature *float64
}

func (j factCheckJudge) JudgeClaims(ctx context.Context, claims []string, evidence string) ([]domain.ClaimVerdict, error) {
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return nil, fmt.Errorf("factcheck judge: marshal claims: %w", err)
	}
	resp, err := j.completer.Complete(ctx, &llmgatewaydomain.CompletionRequest{
		Model:       j.model,
		Temperature: j.temperature,
		MaxTokens:   constants.AgentFactCheckJudgeMaxTokens,
		ResponseFormat: &llmgatewaydomain.ResponseFormat{
			Type: "json_object",
		},
		Messages: []llmgatewaydomain.Message{
			{Role: "system", Content: j.prompt},
			{Role: "user", Content: fmt.Sprintf(
				"Claims:\n%s\n\nEvidence:\n%s\n\n输出 JSON：{\"claims\":[{\"text\":\"<claim>\",\"verdict\":\"SUPPORTED|CONTRADICTED|UNSUPPORTED\",\"risk\":<0-5>}]}。risk 越高越可疑；证据不足判 UNSUPPORTED。",
				string(claimsJSON), evidence,
			)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("factcheck judge: %w", err)
	}
	return factcheck.ParseClaimVerdicts(resp.Content)
}
