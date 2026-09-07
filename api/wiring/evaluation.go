package wiring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	agentpersist "github.com/byteBuilderX/stratum/internal/agent/infrastructure/persistence"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/internal/evaluation/infrastructure/observation"
	evalpersist "github.com/byteBuilderX/stratum/internal/evaluation/infrastructure/persistence"
	iampersistence "github.com/byteBuilderX/stratum/internal/iam/infrastructure/persistence"
	knowledgeapp "github.com/byteBuilderX/stratum/internal/knowledge/application"
	llmgatewaydomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	llmgateway "github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
	pipeline "github.com/byteBuilderX/stratum/internal/memory/infrastructure/pipeline"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	skilldomain "github.com/byteBuilderX/stratum/internal/skill/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type Evaluation struct {
	Service              *evalapp.Service
	SuiteService         *evalapp.SuiteService
	JobService           *evalapp.JobService
	Worker               *evalapp.Worker
	OptimizationService  *evalapp.OptimizationService
	ExperimentService    *evalapp.ExperimentService
	FeedbackService      *evalapp.FeedbackService
	QueryService         *evalapp.QueryService
	CandidateService     *evalapp.CandidateCommandService
	AgentProvider        evalport.AgentRevisionProvider
	MCPProvider          evalport.ResourceRevisionProvider
	KnowledgeProvider    evalport.ResourceRevisionProvider
	BaselineService      *evalapp.BaselineService
	AgentRevisionApplier evalport.AgentRevisionApplier
	TestCaseGenerator    *evalapp.TestCaseGenerator
	ObservationService   *evalapp.ObservationService
	ReviewService        *evalapp.ReviewService
	DeleteService        *evalapp.DeleteService
	// ResourceRollbackExecutor L3 资源回滚执行器（agent/knowledge/skill/canary）。
	// nil = 无任何可回滚后端（未装配）；Task 4 executeResourceRollback / T13 gate auto 消费。
	ResourceRollbackExecutor evalport.ResourceRollbackExecutor
	// ResourceNamer 评测中心资源行被测资源的跨模块真名解析器（仅展示富化：agent/skill
	// 产品名 + mcp server 名 + knowledge 恒等）。nil 时 handler 富化为空操作。
	ResourceNamer evalport.CenterResourceNamer
}

type evaluationResourceRouter struct {
	adapters map[evaldomain.ResourceKind]evalport.ResourceAdapter
}

func (r evaluationResourceRouter) adapter(kind evaldomain.ResourceKind) (evalport.ResourceAdapter, error) {
	adapter := r.adapters[kind]
	if adapter == nil {
		return nil, fmt.Errorf("evaluation resource adapter unavailable for %q", kind)
	}
	return adapter, nil
}

func (r evaluationResourceRouter) ExecuteRevision(
	ctx context.Context, tenantID, requestedBy string, ref evaldomain.ResourceRef, testCase evaldomain.EvalCase,
) (evalport.ExecutionResult, error) {
	adapter, err := r.adapter(ref.Kind)
	if err != nil {
		return evalport.ExecutionResult{}, err
	}
	return adapter.ExecuteRevision(ctx, tenantID, requestedBy, ref, testCase)
}

func (r evaluationResourceRouter) ResolveRevision(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, error) {
	adapter, err := r.adapter(ref.Kind)
	if err != nil {
		return evaldomain.ResourceRevision{}, err
	}
	return adapter.ResolveRevision(ctx, tenantID, ref)
}

func (r evaluationResourceRouter) SafeSummary(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (map[string]any, error) {
	adapter, err := r.adapter(ref.Kind)
	if err != nil {
		return nil, err
	}
	return adapter.SafeSummary(ctx, tenantID, ref)
}

// RunSession 分派会话剧本执行（阶段 B §5.4）。adapter 未实现 evalport.SessionRunner
// （单轮知识检索 / MCP 等）时 fail-close 报错，绝不静默退化为单轮执行。
func (r evaluationResourceRouter) RunSession(
	ctx context.Context, tenantID, requestedBy string, ref evaldomain.ResourceRef,
	script evaldomain.EvalSessionScript,
) ([]evaldomain.SessionTurnEvidence, error) {
	adapter, err := r.adapter(ref.Kind)
	if err != nil {
		return nil, err
	}
	runner, ok := adapter.(evalport.SessionRunner)
	if !ok {
		return nil, fmt.Errorf("session evaluation not supported for %q resource", ref.Kind)
	}
	return runner.RunSession(ctx, tenantID, requestedBy, ref, script)
}

var _ evalport.SessionRunner = evaluationResourceRouter{}

type evaluationCandidateRouter struct {
	creators map[evaldomain.ResourceKind]evalport.CandidateCreator
}

type evaluationBaselineRouter struct {
	providers map[evaldomain.ResourceKind]evalport.ResourceRevisionProvider
}

func newEvaluationBaselineService(
	skillProvider evalport.ResourceRevisionProvider,
	agentProvider evalport.ResourceRevisionProvider,
	mcpProvider evalport.ResourceRevisionProvider,
	knowledgeProvider evalport.ResourceRevisionProvider,
) *evalapp.BaselineService {
	providers := make(map[evaldomain.ResourceKind]evalport.ResourceRevisionProvider, 4)
	for kind, provider := range map[evaldomain.ResourceKind]evalport.ResourceRevisionProvider{
		evaldomain.ResourceKindSkill:     skillProvider,
		evaldomain.ResourceKindAgent:     agentProvider,
		evaldomain.ResourceKindMCP:       mcpProvider,
		evaldomain.ResourceKindKnowledge: knowledgeProvider,
	} {
		if provider != nil {
			providers[kind] = provider
		}
	}
	return evalapp.NewBaselineService(evaluationBaselineRouter{providers: providers})
}

func (r evaluationBaselineRouter) CreatePublishedBaseline(
	ctx context.Context, tenantID string, kind evaldomain.ResourceKind, resourceID string,
) (evaldomain.ResourceRef, error) {
	provider := r.providers[kind]
	if provider == nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation baseline provider unavailable for %q", kind)
	}
	return provider.CreatePublishedBaseline(ctx, tenantID, resourceID)
}

func (r evaluationCandidateRouter) creator(kind evaldomain.ResourceKind) (evalport.CandidateCreator, error) {
	creator := r.creators[kind]
	if creator == nil {
		return nil, fmt.Errorf("evaluation candidate creator unavailable for %q", kind)
	}
	return creator, nil
}

func (r evaluationCandidateRouter) LoadOptimizableSnapshot(
	ctx context.Context, tenantID string, baseline evaldomain.ResourceRef,
) (map[string]any, error) {
	creator, err := r.creator(baseline.Kind)
	if err != nil {
		return nil, err
	}
	return creator.LoadOptimizableSnapshot(ctx, tenantID, baseline)
}

func (r evaluationCandidateRouter) CreateCandidate(
	ctx context.Context, tenantID string, baseline evaldomain.ResourceRef, patch evaldomain.CandidatePatch,
) (evaldomain.ResourceRef, error) {
	creator, err := r.creator(baseline.Kind)
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	return creator.CreateCandidate(ctx, tenantID, baseline, patch)
}

type skillEvaluationRepositoryAdapter struct {
	repo evalport.ExperimentRepository
}

func (a skillEvaluationRepositoryAdapter) ResolveSkillEvaluation(
	ctx context.Context, tenantID, skillID string,
) (skillEvaluationStatus, error) {
	deployment, found, err := a.repo.ResolveDeployment(ctx, tenantID, string(evaldomain.ResourceKindSkill), skillID)
	if err != nil || !found || deployment.ExperimentID == "" {
		return skillEvaluationStatus{}, err
	}
	experiment, found, err := a.repo.Get(ctx, tenantID, deployment.ExperimentID)
	if err != nil || !found {
		return skillEvaluationStatus{}, err
	}
	return skillEvaluationStatus{ExperimentID: experiment.ID, Status: string(experiment.Status)}, nil
}

type skillCandidateManager struct {
	versions  *skillapp.VersionService
	revisions evalport.RevisionRepository
}

type experimentSkillRevisionResolver struct {
	service *evalapp.ExperimentService
}

type experimentAgentRevisionResolver struct {
	service *evalapp.ExperimentService
	adapter agentEvaluationAdapter
}

type experimentMCPRevisionResolver struct {
	service *evalapp.ExperimentService
	adapter mcpEvaluationAdapter
}

type experimentKnowledgeRevisionResolver struct {
	service *evalapp.ExperimentService
	adapter knowledgeEvaluationAdapter
}

func (m skillCandidateManager) CreatePublishedBaseline(
	ctx context.Context, tenantID, skillID string,
) (evaldomain.ResourceRef, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(skillID) == "" {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Skill adapter: tenant and skill IDs required")
	}
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: "evaluation-worker", Role: postgres.RoleTenantAdmin,
	})
	revision, err := m.versions.ResolveActivePublishedRevision(ctx, skillID)
	if err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Skill adapter: resolve active baseline: %w", err)
	}
	if m.revisions == nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Skill adapter: revision index unavailable")
	}
	summary, err := m.versions.PublishedRevisionSafeSummary(ctx, skillID, revision.ID)
	if err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Skill adapter: summarize baseline: %w", err)
	}
	indexed := evaldomain.ResourceRevision{
		ID: revision.ID, ResourceKind: evaldomain.ResourceKindSkill, ResourceID: skillID,
		Source: evaldomain.RevisionSourceManual, Status: evaldomain.RevisionStatusPublished,
		ContentHash: revision.ContentHash, PayloadRef: "skill://" + revision.ID, PayloadHash: revision.ContentHash,
		SafeSummary: summary, CreatedBy: "evaluation-worker", CreatedAt: time.Now().UTC(),
	}
	if err := indexed.Validate(); err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Skill adapter: validate baseline index: %w", err)
	}
	_, _, err = m.revisions.Create(ctx, tenantID, indexed, "skill-baseline-"+stableTenantFingerprint(
		tenantID, strings.Join([]string{skillID, revision.ID, revision.ContentHash}, "\x00"),
	))
	if err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Skill adapter: register baseline: %w", err)
	}
	return evaldomain.ResourceRef{
		Kind: evaldomain.ResourceKindSkill, ResourceID: skillID, RevisionID: revision.ID,
	}, nil
}

func (r experimentSkillRevisionResolver) ResolveSkillRevision(
	ctx context.Context,
	tenantID, skillID, subjectID string,
) (agentport.SkillRevisionAssignment, bool, error) {
	assignment, found, err := r.service.ResolveAssignment(ctx, tenantID, evaldomain.ResourceKindSkill, skillID, subjectID)
	return agentport.SkillRevisionAssignment{
		RevisionID: assignment.RevisionID, ExperimentID: assignment.ExperimentID, Variant: assignment.Variant,
	}, found, err
}

func (r experimentAgentRevisionResolver) ResolveAgentRevision(
	ctx context.Context,
	tenantID, agentID, subjectID string,
) (agentport.AgentRevisionAssignment, bool, error) {
	assignment, found, err := r.service.ResolveAssignment(
		ctx, tenantID, evaldomain.ResourceKindAgent, agentID, subjectID,
	)
	if err != nil || !found {
		return agentport.AgentRevisionAssignment{}, found, err
	}
	_, snapshot, revisionFound, err := r.adapter.get(ctx, tenantID, evaldomain.ResourceRef{
		Kind: evaldomain.ResourceKindAgent, ResourceID: agentID, RevisionID: assignment.RevisionID,
	})
	if err != nil {
		return agentport.AgentRevisionAssignment{}, false, err
	}
	if !revisionFound {
		return agentport.AgentRevisionAssignment{}, false, evalport.ErrCenterResourceNotFound
	}
	return agentport.AgentRevisionAssignment{
		Revision: snapshot, RevisionID: assignment.RevisionID,
		ExperimentID: assignment.ExperimentID, Variant: assignment.Variant,
	}, true, nil
}

func (r experimentMCPRevisionResolver) ResolveMCPRevision(
	ctx context.Context,
	tenantID, serverID, subjectID string,
) (agentport.MCPRevisionAssignment, bool, error) {
	assignment, found, err := r.service.ResolveAssignment(
		ctx, tenantID, evaldomain.ResourceKindMCP, serverID, subjectID,
	)
	return agentport.MCPRevisionAssignment{
		RevisionID: assignment.RevisionID, ExperimentID: assignment.ExperimentID, Variant: assignment.Variant,
	}, found, err
}

func (r experimentMCPRevisionResolver) LoadMCPRuntimeRevision(
	ctx context.Context, tenantID, serverID, revisionID string,
) (mcpRuntimeRevision, error) {
	_, snapshot, err := r.adapter.loadRevision(ctx, tenantID, evaldomain.ResourceRef{
		Kind: evaldomain.ResourceKindMCP, ResourceID: serverID, RevisionID: revisionID,
	}, true)
	if err != nil {
		return mcpRuntimeRevision{}, err
	}
	config, err := r.adapter.loadRuntimeConfig(ctx, tenantID, snapshot)
	if err != nil {
		return mcpRuntimeRevision{}, err
	}
	config.Timeout = time.Duration(snapshot.TimeoutMS) * time.Millisecond
	if config.Retry == nil {
		config.Retry = &mcpdomain.RetryConfig{}
	}
	config.Retry.Enabled = snapshot.MaxRetries > 0
	config.Retry.MaxRetries = snapshot.MaxRetries
	return mcpRuntimeRevision{
		Config: config, EnabledTools: append([]string(nil), snapshot.EnabledTools...),
		Timeout: config.Timeout, MaxRetries: snapshot.MaxRetries,
	}, nil
}

func (r experimentKnowledgeRevisionResolver) ResolveKnowledgeRevision(
	ctx context.Context, tenantID, workspaceName, subjectID string,
) (agentport.KnowledgeRevisionAssignment, bool, error) {
	assignment, found, err := r.service.ResolveAssignment(
		ctx, tenantID, evaldomain.ResourceKindKnowledge, workspaceName, subjectID,
	)
	if err != nil || !found {
		return agentport.KnowledgeRevisionAssignment{}, found, err
	}
	revision, err := r.LoadKnowledgeRevision(ctx, tenantID, workspaceName, assignment.RevisionID)
	if err != nil {
		return agentport.KnowledgeRevisionAssignment{}, false, err
	}
	return agentport.KnowledgeRevisionAssignment{
		Revision: revision, ExperimentID: assignment.ExperimentID, Variant: assignment.Variant,
	}, true, nil
}

func (r experimentKnowledgeRevisionResolver) LoadKnowledgeRevision(
	ctx context.Context, tenantID, workspaceName, revisionID string,
) (agentport.KnowledgeRetrievalRevision, error) {
	_, snapshot, err := r.adapter.loadRevision(ctx, tenantID, evaldomain.ResourceRef{
		Kind: evaldomain.ResourceKindKnowledge, ResourceID: workspaceName, RevisionID: revisionID,
	}, true)
	if err != nil {
		return agentport.KnowledgeRetrievalRevision{}, err
	}
	documents, err := r.adapter.source.ListSnapshotDocuments(ctx, tenantID, snapshot.WorkspaceID)
	if err != nil {
		return agentport.KnowledgeRetrievalRevision{}, fmt.Errorf("load Knowledge runtime documents: %w", err)
	}
	documentSetHash, err := knowledgeDocumentSetHash(documents)
	if err != nil {
		return agentport.KnowledgeRetrievalRevision{}, err
	}
	if documentSetHash != snapshot.DocumentSetHash {
		return agentport.KnowledgeRetrievalRevision{}, errors.New("Knowledge runtime document set changed")
	}
	return agentport.KnowledgeRetrievalRevision{
		RevisionID: revisionID, WorkspaceID: snapshot.WorkspaceID, WorkspaceName: snapshot.WorkspaceName,
		EmbeddingModel: snapshot.EmbeddingIdentity, QueryMode: snapshot.QueryMode, TopK: snapshot.TopK,
		ScoreThreshold: snapshot.ScoreThreshold, Reranking: legacyRerankingValue(snapshot.Reranking),
		QueryRewrite: snapshot.QueryRewrite,
	}, nil
}

func (m skillCandidateManager) LoadOptimizableSnapshot(
	ctx context.Context, tenantID string, baseline evaldomain.ResourceRef,
) (map[string]any, error) {
	ctx, err := evaluationSkillContext(ctx, tenantID, baseline)
	if err != nil {
		return nil, err
	}
	version, err := m.versions.ResolvePublishedRevision(ctx, baseline.ResourceID, baseline.RevisionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"instructions": version.Instructions,
	}, nil
}

func (m skillCandidateManager) CreateCandidate(
	ctx context.Context, tenantID string, baseline evaldomain.ResourceRef, patch evaldomain.CandidatePatch,
) (evaldomain.ResourceRef, error) {
	ctx, err := evaluationSkillContext(ctx, tenantID, baseline)
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	if _, err := m.versions.ResolvePublishedRevision(ctx, baseline.ResourceID, baseline.RevisionID); err != nil {
		return evaldomain.ResourceRef{}, err
	}
	version, err := m.versions.CreateCandidate(ctx, baseline.ResourceID, baseline.RevisionID, skillapp.CandidateInput{
		Source: patch.Source, PromptPatch: patch.PromptPatch,
		GenerationMetadata: map[string]any{"rationale": patch.Rationale},
	})
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	return evaldomain.ResourceRef{
		Kind: baseline.Kind, ResourceID: baseline.ResourceID, RevisionID: version.ID,
	}, nil
}

func (m skillCandidateManager) ResolveRevision(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, error) {
	ctx, err := evaluationSkillContext(ctx, tenantID, ref)
	if err != nil {
		return evaldomain.ResourceRevision{}, err
	}
	version, err := m.versions.ResolveEvaluableRevision(ctx, ref.ResourceID, ref.RevisionID)
	if err != nil {
		return evaldomain.ResourceRevision{}, err
	}
	summary, err := m.versions.EvaluableRevisionSafeSummary(ctx, ref.ResourceID, ref.RevisionID)
	if err != nil {
		return evaldomain.ResourceRevision{}, err
	}
	return evaldomain.ResourceRevision{
		ID: version.ID, ResourceKind: evaldomain.ResourceKindSkill, ResourceID: version.SkillID,
		Source: skillRevisionSource(version.Status), Status: skillRevisionStatus(version.Status),
		ContentHash: version.ContentHash, PayloadRef: "skill://" + version.ID, PayloadHash: version.ContentHash,
		SafeSummary: summary,
	}, nil
}

func (m skillCandidateManager) SafeSummary(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (map[string]any, error) {
	ctx, err := evaluationSkillContext(ctx, tenantID, ref)
	if err != nil {
		return nil, err
	}
	return m.versions.EvaluableRevisionSafeSummary(ctx, ref.ResourceID, ref.RevisionID)
}

func skillRevisionSource(status skilldomain.VersionStatus) evaldomain.RevisionSource {
	if status == skilldomain.VersionStatusCandidate {
		return evaldomain.RevisionSourceOptimization
	}
	return evaldomain.RevisionSourceManual
}

func skillRevisionStatus(status skilldomain.VersionStatus) evaldomain.RevisionStatus {
	if status == skilldomain.VersionStatusPublished {
		return evaldomain.RevisionStatusPublished
	}
	return evaldomain.RevisionStatusDraft
}

func evaluationSkillContext(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (context.Context, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("evaluation Skill adapter: tenant ID required")
	}
	if ref.Kind != evaldomain.ResourceKindSkill {
		return nil, fmt.Errorf("evaluation Skill adapter: unsupported resource kind %q", ref.Kind)
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("evaluation Skill adapter: %w", err)
	}
	return postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: "evaluation-worker", Role: postgres.RoleTenantAdmin,
	}), nil
}

type gatewayPromptRewriter struct {
	resolver agentport.TenantCapabilityResolver
	// params feeds evaluation.optimizer.* platform parameters into the rewrite
	// request (model / temperature / max_tokens)。模型默认值为空：代码内不写死
	// 兜底模型，空模型交由 llmgateway 从模型目录解析默认。
	params *parametersapp.Service
}

// optimizerLLM picks the effective optimizer LLM spec from platform
// parameters. 模型未配置（空）时交由 llmgateway 解析默认，绝不写死模型名。
func (r gatewayPromptRewriter) optimizerLLM(
	ctx context.Context,
) (model string, temperature float32, maxTokens int) {
	model, temperature, maxTokens = "", 0.2, 2048
	if r.params == nil {
		return model, temperature, maxTokens
	}
	values, err := r.params.PlatformValues(ctx)
	if err != nil {
		return model, temperature, maxTokens
	}
	if v, ok := values["evaluation.optimizer.model"].(string); ok && v != "" {
		model = v
	}
	if v, ok := values["evaluation.optimizer.temperature"].(float64); ok {
		temperature = float32(v)
	}
	switch v := values["evaluation.optimizer.max_tokens"].(type) {
	case float64:
		maxTokens = int(v)
	case int64:
		maxTokens = int(v)
	}
	return model, temperature, maxTokens
}

// optimizerSystemPrompt 解析优化器系统提示词：平台值 string 且非空用之，否则
// 内置常量兜底（空/缺键不产生空 system，parsePromptRewritePatches 契约不漂移）。
func (r gatewayPromptRewriter) optimizerSystemPrompt(ctx context.Context) string {
	if r.params == nil {
		return constants.EvaluationOptimizerSystemPrompt
	}
	values, err := r.params.PlatformValues(ctx)
	if err == nil {
		if s, ok := values["evaluation.optimizer.system_prompt"].(string); ok && s != "" {
			return s
		}
	}
	return constants.EvaluationOptimizerSystemPrompt
}

// optimizerMessages 组装优化器 LLM 消息：system 取平台配置（空回退常量）；
// user 模板保持代码固定并追加 JSON 数组防御契约行——admin 自由改 system 也不会
// 破坏 parsePromptRewritePatches 对「整段合法 JSON 数组」的强制。
func (r gatewayPromptRewriter) optimizerMessages(ctx context.Context, snapshotJSON, failuresJSON []byte) []agentport.LLMMessage {
	return []agentport.LLMMessage{
		{Role: "system", Content: r.optimizerSystemPrompt(ctx)},
		{Role: "user", Content: fmt.Sprintf(
			"基线配置：%s\n失败摘要：%s\n输出最多3项，每项格式：{\"prompt_patch\":{\"instructions\":\"...\"},\"rationale\":\"...\"}。不得修改 requirements、权限、密钥或网络配置。\n整段回复必须为合法 JSON 数组，禁止任何解释、Markdown、代码围栏或前后缀。",
			string(snapshotJSON), string(failuresJSON),
		)},
	}
}

func (r gatewayPromptRewriter) Rewrite(
	ctx context.Context, request evalapp.PromptRewriteRequest,
) ([]evaldomain.CandidatePatch, error) {
	gateway, ok := r.resolver.Resolve(ctx, request.TenantID)
	if !ok || gateway == nil {
		return nil, fmt.Errorf("prompt optimizer: tenant has no LLM provider configured")
	}
	model, temperature, maxTokens := r.optimizerLLM(ctx)
	snapshotJSON, err := json.Marshal(request.BaselineSnapshot)
	if err != nil {
		return nil, err
	}
	failuresJSON, err := json.Marshal(request.FailureSummaries)
	if err != nil {
		return nil, err
	}
	response, err := gateway.Route(ctx, agentport.CapabilityRequest{
		TenantID: request.TenantID,
		Type:     agentport.CapLLM,
		Timeout:  60 * time.Second,
		LLM: &agentport.LLMCapRequest{
			Model: model, Temperature: temperature, MaxTokens: maxTokens,
			Messages: r.optimizerMessages(ctx, snapshotJSON, failuresJSON),
		},
	})
	if err != nil {
		return nil, err
	}
	return parsePromptRewritePatches(response.Content)
}

// buildEvaluationJudge wires the optional LLM judge. It degrades to a
// disabled judge when the gateway is unavailable (db not configured),
// keeping rule assertions working.
func buildEvaluationJudge(c *Container) evalport.LLMJudge {
	if c.LLMGateway == nil || c.LLMGateway.Gateway == nil {
		return nil
	}
	return judgeAdapter{
		completer: c.LLMGateway.Gateway,
		params:    c.Parameters.Service,
	}
}

// buildReviewService 装配评审池服务（P1c §6.6）：复用 suite 仓库做 promote 沉淀、
// trace evidence reader 解析观测 trace。必须在 Service / ObservationService 注入前装配。
func buildReviewService(
	c *Container, db *pgxpool.Pool, suites evalport.SuiteRepository, traceReader evalport.TraceEvidenceReader,
) *evalapp.ReviewService {
	return evalapp.NewReviewService(evalapp.ReviewServiceDeps{
		Repo:     evalpersist.NewPgReviewRepository(db),
		Suites:   suites,
		Evidence: traceReader,
		Metrics:  c.platformMetrics(),
		Logger:   c.Logger,
		Cfg: evaldomain.ReviewConfig{
			LowConfidenceThreshold: constants.ReviewLowConfidenceThreshold,
			JudgePassThreshold:     constants.JudgeBelowThreshold,
		},
		// TenantIDs 供跨租户求和刷新 eval_review_backlog（spec §8.3）。IAM 未
		// 装配时返回 nil（nil → ReviewService 退化为当前租户单租户语义）。
		TenantIDs: func(ctx context.Context) ([]string, error) {
			if c.IAM == nil || c.IAM.TenantRepo == nil {
				return nil, nil // 无租户注册表：退化为当前租户（fail-open）
			}
			return c.IAM.TenantRepo.ListActiveTenantIDs(ctx)
		},
	})
}

// buildObservationService 装配运行态观测服务（P1b §4.2 路 A）：FeedbackService 依赖它
// 作为行为信号 writer，NATS 消费 worker 复用同一实例（wireObservationPipeline），故
// 必须在 FeedbackService 之前装配。review 为评审池升级器（P1c §6.6 内联触发），
// 与 Service 共用同一 reviewSvc 实例。
func buildObservationService(
	c *Container, db *pgxpool.Pool, traceReader evalport.TraceEvidenceReader, judge evalport.LLMJudge,
	review evalport.ReviewEscalator,
) *evalapp.ObservationService {
	return evalapp.NewObservationService(evalapp.ObservationServiceDeps{
		Enabled:    func(ctx context.Context) bool { return observationEnabled(ctx, c.Parameters.Service) },
		SampleRate: func(ctx context.Context) float64 { return observationSampleRate(ctx, c.Parameters.Service) },
		// Phase 2 §4.3：平台层版本锚点绑定 evaluation 配置组当前生效版本序号。
		PlatformVersion: func(ctx context.Context) (int64, bool, error) {
			return observationPlatformVersion(ctx, c.Parameters.Service)
		},
		Evidence:   traceReader,
		Judge:      judge,
		Repo:       evalpersist.NewPgObservationRepository(db),
		Metrics:    c.platformMetrics(),
		Logger:     c.Logger,
		TenantTier: tenantTierAdapter{repo: iampersistence.NewAdminTenantRepo(db)},
		Review:     review,
	})
}

// newEvaluationServiceWithReview 构造评测 Service 并把评审池升级器注入（P1c §6.6）：
// 评审池服务先行装配（buildReviewService），NewService 后注入升级器与阈值配置。
// 返回 reviewSvc 供观测服务与 Evaluation 结构复用同一实例。独立成函数保持
// buildEvaluation 行数在质量门禁基线内。
func newEvaluationServiceWithReview(
	c *Container, db *pgxpool.Pool, adapter evalport.ResourceAdapter, repo evalport.RunRepository,
	traceReader evalport.TraceEvidenceReader, judge evalport.LLMJudge, suiteRepo evalport.SuiteRepository,
) (*evalapp.Service, *evalapp.ReviewService) {
	reviewSvc := buildReviewService(c, db, suiteRepo, traceReader)
	service := evalapp.NewService(adapter, repo, traceReader, judge, suiteRepo)
	service.SetReviewEscalator(reviewSvc, evaldomain.ReviewConfig{
		LowConfidenceThreshold: constants.ReviewLowConfidenceThreshold,
		JudgePassThreshold:     constants.JudgeBelowThreshold,
	})
	service.SetObservability(c.Logger, c.platformMetrics())
	service.SetPlatformVersion(func(ctx context.Context) (int64, bool, error) {
		return observationPlatformVersion(ctx, c.Parameters.Service)
	})
	return service, reviewSvc
}

// judgeAdapter implements evalport.LLMJudge over llmgateway's LLMCompleter.
// The runtime switch and model/temperature come from platform parameters;
// the rubric is the built-in default unless the case declares one
// explicitly. nil dependencies degrade conservatively: disabled judge and
// built-in defaults, never a silent pass.
type judgeAdapter struct {
	completer llmgatewaydomain.LLMCompleter
	params    *parametersapp.Service
}

// Enabled reports the evaluation.judge.enabled platform parameter, preferring
// the run-scoped evaluation snapshot (D2/D6: 全链路版本快照) when present. Fail
// closed when the parameters service is unavailable.
func (j judgeAdapter) Enabled(ctx context.Context) bool {
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		enabled, _ := snap.Evaluation.Values["evaluation.judge.enabled"].(bool)
		return enabled
	}
	if j.params == nil {
		return false
	}
	values, err := j.params.PlatformValues(ctx)
	if err != nil {
		return false
	}
	enabled, _ := values["evaluation.judge.enabled"].(bool)
	return enabled
}

func (j judgeAdapter) judgeModel(ctx context.Context, requested string) string {
	if requested != "" {
		return requested
	}
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		model, _ := snap.Evaluation.Values["evaluation.judge.model"].(string)
		return model
	}
	if j.params == nil {
		return ""
	}
	values, err := j.params.PlatformValues(ctx)
	if err != nil {
		return ""
	}
	if model, ok := values["evaluation.judge.model"].(string); ok && model != "" {
		return model
	}
	return ""
}

func (j judgeAdapter) judgeTemperature(ctx context.Context) float32 {
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		if temperature, ok := snap.Evaluation.Values["evaluation.judge.temperature"].(float64); ok {
			return float32(temperature)
		}
		return 0
	}
	if j.params == nil {
		return 0
	}
	values, err := j.params.PlatformValues(ctx)
	if err != nil {
		return 0
	}
	if temperature, ok := values["evaluation.judge.temperature"].(float64); ok {
		return float32(temperature)
	}
	return 0
}

// judgeRubric 解析法官判定准则，优先级：用例自声明 > run 版本快照 > 当前平台
// 值 > 内置常量兜底（D2/D7：默认=内置全文，空/缺键不漂移）。params 与快照任一
// 缺失均降级到下一层，绝不空态返回。
func (j judgeAdapter) judgeRubric(ctx context.Context, requested string) string {
	if requested != "" {
		return requested
	}
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		if s, ok := snap.Evaluation.Values["evaluation.judge.rubric"].(string); ok && s != "" {
			return s
		}
	}
	if j.params != nil {
		if values, err := j.params.PlatformValues(ctx); err == nil {
			if s, ok := values["evaluation.judge.rubric"].(string); ok && s != "" {
				return s
			}
		}
	}
	return constants.EvaluationJudgeDefaultRubric
}

func (j judgeAdapter) Judge(ctx context.Context, req evalport.JudgeRequest) (evaldomain.AssertionResult, error) {
	if j.completer == nil {
		return evaldomain.AssertionResult{}, errors.New("LLM judge: no LLM completer configured")
	}
	userContent := fmt.Sprintf(
		"Rubric:\n%s\n\nInput:\n%s\n\nExpected output:\n%s\n\nActual output:\n%s",
		j.judgeRubric(ctx, req.Rubric), req.Input, req.ExpectedOutput, req.Actual,
	)
	if req.ToolSequence != "" {
		userContent += "\n\nTool sequence:\n" + req.ToolSequence
	}
	// 会话剧本 case 追加 conversation transcript（阶段 B §4.3）：judge 据此评
	// 「末轮是否到达目标/守住探针」。空 = 单轮 case 无 transcript，拼接与既有
	// 契约逐字节一致（回归测试守护）。
	if req.Transcript != "" {
		userContent += "\n\nConversation transcript:\n" + req.Transcript
	}
	response, err := j.completer.Complete(ctx, &llmgatewaydomain.CompletionRequest{
		Model:       j.judgeModel(ctx, req.Model),
		Temperature: temperaturePtrOrNil(j.judgeTemperature(ctx)),
		MaxTokens:   constants.JudgeMaxTokens,
		Messages: []llmgatewaydomain.Message{
			{Role: "system", Content: "你是评测法官。只输出 JSON，不输出其他内容。"},
			{Role: "user", Content: userContent},
		},
	})
	if err != nil {
		return evaldomain.AssertionResult{}, fmt.Errorf("LLM judge: %w", err)
	}
	return parseJudgeResponse(response.Content)
}

// buildEvaluationCaseGen wires the LLM-backed eval-case generator. Returns
// nil when the gateway is unavailable; TestCaseGenerator rejects the whole
// request then instead of silently producing no cases.
// buildEvaluationRuntime resolves the baseline service and agent revision
// applier used by the runtime evaluation entry points.
func buildEvaluationRuntime(
	manager skillCandidateManager,
	agentProvider, mcpProvider, knowledgeProvider evalport.ResourceRevisionProvider,
	runtimeAgentAdapter *agentEvaluationAdapter,
) (*evalapp.BaselineService, evalport.AgentRevisionApplier) {
	baseline := newEvaluationBaselineService(manager, agentProvider, mcpProvider, knowledgeProvider)
	var applier evalport.AgentRevisionApplier
	if runtimeAgentAdapter != nil {
		applier = *runtimeAgentAdapter
	}
	return baseline, applier
}

// buildTestCaseGenerator wires sample source, LLM generator and suite repo
// into the eval-case generation service.
func buildTestCaseGenerator(c *Container, suites evalport.SuiteRepository, db *pgxpool.Pool) *evalapp.TestCaseGenerator {
	return evalapp.NewTestCaseGenerator(
		evalpersist.NewPgCaseSampleSource(db),
		buildEvaluationCaseGen(c),
		suites,
	)
}

func buildEvaluationCaseGen(c *Container) evalport.CaseGenerator {
	if c.LLMGateway == nil || c.LLMGateway.Gateway == nil {
		return nil
	}
	var params *parametersapp.Service
	if c.Parameters != nil {
		params = c.Parameters.Service
	}
	return casegenAdapter{completer: c.LLMGateway.Gateway, params: params}
}

// casegenAdapter implements evalport.CaseGenerator over llmgateway's
// LLMCompleter, the same channel as the LLM judge. The model falls back to
// the platform optimizer model; a generation failure returns a
// Valid=false GeneratedCase so the caller can report the per-sample reason
// without aborting the whole pass.
type casegenAdapter struct {
	completer llmgatewaydomain.LLMCompleter
	params    *parametersapp.Service
}

const caseGenSystemPrompt = `你是一名评测用例生成器。给定一条真实生产交互样本（用户查询、实际回答、用户反馈信号），生成一条评测用例。
规则：
1. input：保留或轻度改写原用户查询，不得改变语义；
2. expected_output：基于实际回答推导期望输出；回答明显错误时可给出修正后的期望；
3. assertion_mode：从 exact（精确匹配）、contains（包含）、regex（正则）、judge（LLM 判断）中选择最能验证该意图的模式；
4. reason：一句话说明该用例的来源与生成依据。
只输出 JSON：{"name": "...", "input": ..., "expected_output": ..., "assertion_mode": "...", "reason": "..."}，不要输出其他内容。`

func (a casegenAdapter) Generate(ctx context.Context, req evalport.CaseGenRequest) (evaldomain.GeneratedCase, error) {
	if a.completer == nil {
		return evaldomain.GeneratedCase{Valid: false, Reason: "case generator: no LLM completer configured"}, nil
	}
	response, err := a.completer.Complete(ctx, &llmgatewaydomain.CompletionRequest{
		Model:       a.genModel(ctx),
		Temperature: temperaturePtrOrNil(0.2),
		MaxTokens:   constants.CaseGenMaxTokens,
		Messages: []llmgatewaydomain.Message{
			{Role: "system", Content: caseGenSystemPrompt},
			{Role: "user", Content: caseGenUserContent(req)},
		},
	})
	if err != nil {
		return evaldomain.GeneratedCase{}, fmt.Errorf("case generator: %w", err)
	}
	return parseCaseGenResponse(response.Content)
}

func (a casegenAdapter) genModel(ctx context.Context) string {
	if a.params != nil {
		if values, err := a.params.PlatformValues(ctx); err == nil {
			if model, ok := values["evaluation.optimizer.model"].(string); ok && model != "" {
				return model
			}
		}
	}
	// 模型未配置（空）时交由 llmgateway 从模型目录解析默认，代码内不写死
	// 兜底模型。
	return ""
}

// caseGenUserContent renders one sample for the generator, including the
// feedback signal when present.
func caseGenUserContent(req evalport.CaseGenRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "资源类型：%s\n", req.ResourceKind)
	fmt.Fprintf(&b, "用户查询：%s\n", req.Sample.Query)
	fmt.Fprintf(&b, "实际回答：%s\n", req.Sample.Response)
	if req.Sample.Score != nil {
		fmt.Fprintf(&b, "反馈分数：%.2f\n", *req.Sample.Score)
	} else {
		b.WriteString("反馈分数：无\n")
	}
	if len(req.Sample.Outcome) > 0 {
		outcomeJSON, _ := json.Marshal(req.Sample.Outcome)
		fmt.Fprintf(&b, "反馈标签：%s\n", outcomeJSON)
	}
	return b.String()
}

// parseCaseGenResponse extracts one eval-case JSON from the generator
// output, tolerating a markdown code fence. Unparseable output becomes a
// Valid=false GeneratedCase: the sample is rejected with a reason, never
// silently dropped.
func parseCaseGenResponse(content string) (evaldomain.GeneratedCase, error) {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	}
	var generated struct {
		Name           string                   `json:"name"`
		Input          any                      `json:"input"`
		ExpectedOutput any                      `json:"expected_output"`
		AssertionMode  evaldomain.AssertionMode `json:"assertion_mode"`
		Reason         string                   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(trimmed)), &generated); err != nil {
		return evaldomain.GeneratedCase{}, fmt.Errorf("case generator: parse generated case: %w", err)
	}
	return evaldomain.GeneratedCase{
		Name:           generated.Name,
		Input:          generated.Input,
		ExpectedOutput: generated.ExpectedOutput,
		AssertionMode:  generated.AssertionMode,
		GenerateReason: generated.Reason,
		Valid:          true,
	}, nil
}

// parseJudgeResponse extracts {"passed","reason","confidence","dimensions"}
// from the judge output, tolerating a markdown code fence around the JSON.
// Confidence and per-dimension scores are optional: missing, non-numeric or
// out-of-[0,1] values default to 1.0 / are dropped (fail-open, spec §6.2).
func parseJudgeResponse(content string) (evaldomain.AssertionResult, error) {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	}
	var verdict struct {
		Passed     bool              `json:"passed"`
		Reason     string            `json:"reason"`
		Confidence json.RawMessage   `json:"confidence"`
		Dimensions []json.RawMessage `json:"dimensions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(trimmed)), &verdict); err != nil {
		return evaldomain.AssertionResult{}, fmt.Errorf("LLM judge: parse verdict: %w", err)
	}
	confidence := 1.0
	if score, ok := parseScore01(verdict.Confidence); ok {
		confidence = score
	}
	return evaldomain.AssertionResult{
		Passed:     verdict.Passed,
		Message:    verdict.Reason,
		Confidence: confidence,
		Dimensions: parseJudgeDimensions(verdict.Dimensions),
	}, nil
}

// parseJudgeDimensions 解析 judge 返回的多维分数。单维度非法（name 空、score
// 越界、JSON 无法解析）时丢弃该维度而非使整次判定失败（fail-open）；完全无
// 合法维度返回 nil，聚合层自动跳过。confidence 缺失/越界回退 1.0。
func parseJudgeDimensions(raw []json.RawMessage) []evaldomain.DimensionScore {
	out := make([]evaldomain.DimensionScore, 0, len(raw))
	for _, item := range raw {
		var d struct {
			Name       string          `json:"name"`
			Score      json.RawMessage `json:"score"`
			Passed     bool            `json:"passed"`
			Reason     string          `json:"reason"`
			Confidence json.RawMessage `json:"confidence"`
		}
		if err := json.Unmarshal(item, &d); err != nil || strings.TrimSpace(d.Name) == "" {
			continue
		}
		score, ok := parseScore01(d.Score)
		if !ok {
			continue
		}
		confidence := 1.0
		if c, ok := parseScore01(d.Confidence); ok {
			confidence = c
		}
		out = append(out, evaldomain.DimensionScore{
			Name: d.Name, Score: score, Passed: d.Passed, Reason: d.Reason, Confidence: confidence,
		})
	}
	return out
}

// parseScore01 解析 [0,1] 内的 number；缺失/null/非数字/越界返回 ok=false。
func parseScore01(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || v < 0 || v > 1 {
		return 0, false
	}
	return v, true
}

func parsePromptRewritePatches(content string) ([]evaldomain.CandidatePatch, error) {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	}
	var patches []evaldomain.CandidatePatch
	if err := json.Unmarshal([]byte(strings.TrimSpace(trimmed)), &patches); err != nil {
		return nil, fmt.Errorf("prompt optimizer: parse candidate patches: %w", err)
	}
	if len(patches) == 0 || len(patches) > 3 {
		return nil, fmt.Errorf("prompt optimizer: expected 1-3 candidate patches")
	}
	for i := range patches {
		if err := evaldomain.ValidatePromptPatch(patches[i].PromptPatch); err != nil {
			return nil, err
		}
		patches[i].Source = "llm_rewrite"
	}
	return patches, nil
}

type evaluationTenantLister struct {
	pool *pgxpool.Pool
}

// skillScenarioExecutor 是 skill 场景评测的注入面：单轮场景执行
// (ExecuteSkillScenarioRevision) + 评测受控会话打开 (OpenEvalConversation)。
// *agentapp.AgentService 天然满足；窄接口注入让单测可用 fake 替代完整 AgentService，
// 与 agentEvaluationAdapter 的 agentRevisionExecutor/evalConversationOpener 同模式。
type skillScenarioExecutor interface {
	ExecuteSkillScenarioRevision(context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation) (*agentapp.AgentResult, int, error)
	OpenEvalConversation(ctx context.Context, tenantID, agentID, userID string) (string, error)
}

type agentScenarioEvaluationAdapter struct {
	agents    skillScenarioExecutor
	revisions agentRevisionService
	skills    agentport.SkillActivationResolver
	bindings  agentport.AgentSkillBinding
	resources skillCandidateManager
}

var _ evalport.SessionRunner = agentScenarioEvaluationAdapter{}

func (a agentScenarioEvaluationAdapter) ResolveRevision(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, error) {
	return a.resources.ResolveRevision(ctx, tenantID, ref)
}

func (a agentScenarioEvaluationAdapter) SafeSummary(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (map[string]any, error) {
	return a.resources.SafeSummary(ctx, tenantID, ref)
}

// validateSkillRef rejects non-skill refs. 内置 skill 与普通 skill 走同一评测链路
// (FindAgentBySkill 解析到系统助手),不再做 builtin 前缀特判。
func (a agentScenarioEvaluationAdapter) validateSkillRef(ref evaldomain.ResourceRef) error {
	if ref.Kind != evaldomain.ResourceKindSkill {
		return fmt.Errorf("agent scenario evaluation: unsupported resource kind %q", ref.Kind)
	}
	return nil
}

func (a agentScenarioEvaluationAdapter) ExecuteRevision(
	ctx context.Context, tenantID, requestedBy string, ref evaldomain.ResourceRef, testCase evaldomain.EvalCase,
) (evalport.ExecutionResult, error) {
	// D7：执行时用 run 创建时锁定的承载 agent revision，不读 Registry 当前生产
	// 配置，可重放。快照缺失/pin 缺失 → fail-closed，提示 recreate run。
	snap := evaldomain.EvalSnapshotFromCtx(ctx)
	ctx, agentRev, agentID, activation, err := a.resolveScenarioExecution(ctx, tenantID, requestedBy, ref, snap)
	if err != nil {
		return evalport.ExecutionResult{}, err
	}
	query, err := evaluationCaseQuery(testCase.Input)
	if err != nil {
		return evalport.ExecutionResult{}, err
	}
	meta := a.scenarioExecutionMeta(tenantID, ref, agentID, snap.PinnedAssignments.SkillAgentRevision[ref.ResourceID])
	result, duration, traceID, err := a.executeScenarioTurn(ctx, agentRev,
		agentapp.ExecRequest{Query: query, UserID: requestedBy}, activation, meta)
	if err != nil {
		return evalport.ExecutionResult{}, err
	}
	return evalport.ExecutionResult{Output: result.Output, TraceID: traceID, Tokens: result.TokensUsed,
		CostUSD: result.CostUSD, DurationMs: duration, Tools: mapToolObservations(result.ToolObservations)}, nil
}

// resolveScenarioExecution 解析并锁定一次 skill 场景评测（D7）：单轮 ExecuteRevision
// 与会话 RunSession 共用的前置解析——校验 ref、要求 ctx 快照、一次性包裹租户 ctx、
// 解析锁定承载 agent + skill 激活。任一步骤失败 fail-closed（pin 缺失/绑定 agent 缺失/
// skill 不可用一律拒绝，绝不落到 Registry 当前生产配置）。评测后台 worker 路径 ctx 本无
// tenant：此处包裹贯穿 bindings / ResolveSkills / OpenEvalConversation / 执行调用
// (publishedSkillActivationResolver 与 skill repo 只从 ctx 读 tenant，缺失即 fail-closed)。
// resolvePinnedAgent 内的局部包裹是幂等双保险，两处重复无害。
func (a agentScenarioEvaluationAdapter) resolveScenarioExecution(
	ctx context.Context, tenantID, requestedBy string, ref evaldomain.ResourceRef, snap *evaldomain.EvaluationContextSnapshot,
) (context.Context, agentdomain.AgentRevision, string, agentport.SkillActivation, error) {
	if err := a.validateSkillRef(ref); err != nil {
		return nil, agentdomain.AgentRevision{}, "", agentport.SkillActivation{}, err
	}
	if a.revisions == nil {
		return nil, agentdomain.AgentRevision{}, "", agentport.SkillActivation{}, errors.New("agent scenario evaluation: revision service unavailable")
	}
	if snap == nil {
		return nil, agentdomain.AgentRevision{}, "", agentport.SkillActivation{}, errors.New("agent scenario evaluation: evaluation context snapshot required")
	}
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: requestedBy, Role: postgres.RoleTenantAdmin,
	})
	agentRev, agentID, err := a.resolvePinnedAgent(ctx, tenantID, requestedBy, snap, ref)
	if err != nil {
		return nil, agentdomain.AgentRevision{}, "", agentport.SkillActivation{}, err
	}
	activation, err := a.resolveSkillActivation(ctx, tenantID, ref)
	if err != nil {
		return nil, agentdomain.AgentRevision{}, "", agentport.SkillActivation{}, err
	}
	if a.agents == nil {
		return nil, agentdomain.AgentRevision{}, "", agentport.SkillActivation{}, errors.New("agent scenario evaluation: skill executor unavailable")
	}
	return ctx, agentRev, agentID, activation, nil
}

// scenarioExecutionMeta 构造 skill 场景评测共用的路由元数据：TraceID 每次执行在
// executeScenarioTurn 内重新生成；EvolutionTrace 标注被测 skill revision 与锁定的
// 承载 agent revision（manifest 键 agent:<agentID>，值来自 resolvePinnedAgent 已
// fail-closed 保证非空的 pin）。
func (a agentScenarioEvaluationAdapter) scenarioExecutionMeta(tenantID string, ref evaldomain.ResourceRef, agentID, pinnedAgentRev string) agentapp.ExecMeta {
	return agentapp.ExecMeta{
		TenantID: tenantID,
		EvolutionTrace: agentapp.EvolutionTraceMetadata{
			Evaluation: true,
			ResourceManifest: map[string]string{
				"skill:" + ref.ResourceID: ref.RevisionID,
				"agent:" + agentID:        pinnedAgentRev,
			},
		},
	}
}

// executeScenarioTurn 执行一次 skill 场景评测调用（单轮 case 与会话剧本逐轮共用）。
// req.ConversationID 为空 = 一次性单轮；非空 = 以 ExecRequest.ConversationID 续跑同一
// source='evaluation' 受控会话——assembleOptions 映射 WithConversationID +
// loadConversationHistory 重载真实历史 → skill 场景逐轮与 agent 执行同语义真实多轮。
// 每次调用生成独立 trace（逐轮证据各自可追溯）；执行错误与 nil result 都显式上抛。
func (a agentScenarioEvaluationAdapter) executeScenarioTurn(
	ctx context.Context, agentRev agentdomain.AgentRevision, req agentapp.ExecRequest,
	activation agentport.SkillActivation, meta agentapp.ExecMeta,
) (*agentapp.AgentResult, int, string, error) {
	meta.TraceID = uuid.Must(uuid.NewV7()).String()
	result, duration, err := a.agents.ExecuteSkillScenarioRevision(ctx, agentRev,
		req, meta, []agentport.SkillActivation{activation})
	if err != nil {
		return nil, 0, "", fmt.Errorf("agent scenario evaluation: execute skill scenario revision: %w", err)
	}
	if result == nil {
		return nil, 0, "", errors.New("agent scenario evaluation: provider returned no result")
	}
	return result, duration, meta.TraceID, nil
}

// RunSession 实现 evalport.SessionRunner（阶段 B §5.4）：skill 场景会话剧本与 agent
// 会话同语义——先经 OpenEvalConversation 开一条 source='evaluation' 受控会话，再逐轮
// 以 ExecuteSkillScenarioRevision 续跑同一会话（query=turn.User，历史自动重载）。任一轮
// 失败返回已收集 partial evidence + error，绝不吞错；Output 是 AgentResult.Output
// (string)，投影为逐轮证据供应用层末轮终态断言与 turns 落库。
//
// 离线审批默认策略：会话逐轮在评测 worker 中离线同步执行，无人工审批环。任一剧本轮次
// 触发 RequireApproval 工具时，执行链在 tool guard 返回 ToolApprovalRequiredError 后以该
// 轮 error 终止——runCaseSession 记该轮失败、case 失败（fail-close），绝不自动放行，也不
// 把审批偷偷转成真人待办路径。显式「先批准再执行」剧本（需先人工放行再续跑验证批准路径）
// 需要独立交互续跑机制，属后续子步，不在本阶段实现。
func (a agentScenarioEvaluationAdapter) RunSession(
	ctx context.Context, tenantID, requestedBy string, ref evaldomain.ResourceRef,
	script evaldomain.EvalSessionScript,
) ([]evaldomain.SessionTurnEvidence, error) {
	if a.agents == nil {
		return nil, errors.New("agent scenario evaluation: skill executor unavailable")
	}
	snap := evaldomain.EvalSnapshotFromCtx(ctx)
	ctx, agentRev, agentID, activation, err := a.resolveScenarioExecution(ctx, tenantID, requestedBy, ref, snap)
	if err != nil {
		return nil, err
	}
	convID, err := a.agents.OpenEvalConversation(ctx, tenantID, agentID, requestedBy)
	if err != nil {
		return nil, fmt.Errorf("agent scenario evaluation: open evaluation conversation: %w", err)
	}
	meta := a.scenarioExecutionMeta(tenantID, ref, agentID, snap.PinnedAssignments.SkillAgentRevision[ref.ResourceID])
	evidences := make([]evaldomain.SessionTurnEvidence, 0, len(script.Turns))
	for i, turn := range script.Turns {
		result, duration, traceID, err := a.executeScenarioTurn(ctx, agentRev,
			agentapp.ExecRequest{Query: turn.User, UserID: requestedBy, ConversationID: convID}, activation, meta)
		if err != nil {
			return evidences, err
		}
		evidences = append(evidences, evaldomain.SessionTurnEvidence{
			Index: i, User: turn.User, Output: result.Output,
			TraceID: traceID, Tokens: result.TokensUsed, CostUSD: result.CostUSD,
			DurationMs: duration, Tools: mapToolObservations(result.ToolObservations),
		})
	}
	return evidences, nil
}

// resolvePinnedAgent 加载并解码 run 快照锁定的承载 agent revision（D7）：
// pin 缺失 → fail-closed；FindAgentBySkill 解析承载 agent；revisions.Get 读锁定
// revision payload 并解码为被测 agent。执行绝不落到 Registry 当前生产配置。
func (a agentScenarioEvaluationAdapter) resolvePinnedAgent(
	ctx context.Context, tenantID, requestedBy string, snap *evaldomain.EvaluationContextSnapshot, ref evaldomain.ResourceRef,
) (agentdomain.AgentRevision, string, error) {
	pinnedID := snap.PinnedAssignments.SkillAgentRevision[ref.ResourceID]
	if pinnedID == "" {
		return agentdomain.AgentRevision{}, "", fmt.Errorf("agent scenario evaluation: no pinned agent revision for Skill %s; recreate the run", ref.ResourceID)
	}
	// ExecuteRevision 顶层已包裹 tenant；此处局部包裹为幂等双保险（WithTenant 覆盖
	// 同值 TenantContext），保证本 helper 单独被调用时 bindings/revisions 仍带租户。
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: requestedBy, Role: postgres.RoleTenantAdmin,
	})
	agentID, found, err := a.bindings.FindAgentBySkill(ctx, ref.ResourceID)
	if err != nil {
		return agentdomain.AgentRevision{}, "", fmt.Errorf("agent scenario evaluation: resolve agent for Skill %s: %w", ref.ResourceID, err)
	}
	if !found {
		return agentdomain.AgentRevision{}, "", fmt.Errorf("agent scenario evaluation requires an Agent bound to Skill %s", ref.ResourceID)
	}
	_, payload, found, err := a.revisions.Get(ctx, tenantID, evaldomain.ResourceRef{
		Kind: evaldomain.ResourceKindAgent, ResourceID: agentID, RevisionID: pinnedID,
	})
	if err != nil {
		return agentdomain.AgentRevision{}, "", fmt.Errorf("agent scenario evaluation: load pinned agent revision %s: %w", pinnedID, err)
	}
	if !found {
		return agentdomain.AgentRevision{}, "", fmt.Errorf("agent scenario evaluation: pinned agent revision %s not found; recreate the run", pinnedID)
	}
	var agentRev agentdomain.AgentRevision
	if err := json.Unmarshal(payload, &agentRev); err != nil {
		return agentdomain.AgentRevision{}, "", fmt.Errorf("agent scenario evaluation: decode pinned agent revision: %w", err)
	}
	return agentRev, agentID, nil
}

// resolveSkillActivation 解析被测 skill revision 的激活信息：缺失 → 报不可用。
func (a agentScenarioEvaluationAdapter) resolveSkillActivation(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (agentport.SkillActivation, error) {
	catalog, err := a.skills.ResolveSkills(ctx, tenantID, []agentport.SkillRevisionRef{{SkillID: ref.ResourceID, RevisionID: ref.RevisionID}})
	if err != nil {
		return agentport.SkillActivation{}, err
	}
	activation, ok := catalog[ref.ResourceID]
	if !ok {
		return agentport.SkillActivation{}, fmt.Errorf("Skill revision %s is not available", ref.RevisionID)
	}
	return activation, nil
}

func (l evaluationTenantLister) ListTenantIDs(ctx context.Context) ([]string, error) {
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

type evaluationTraceEvidenceAdapter struct {
	provider agentport.TraceEvidenceProvider
}

func (a evaluationTraceEvidenceAdapter) Resolve(
	ctx context.Context, tenantID, traceID string,
) (evalport.ObservedTrace, error) {
	evidence, err := a.provider.Resolve(ctx, tenantID, traceID)
	if err != nil {
		return evalport.ObservedTrace{}, err
	}
	return mapEvaluationEvidence(evidence), nil
}

func (a evaluationTraceEvidenceAdapter) ResolveBatch(
	ctx context.Context, tenantID string, traceIDs []string,
) (map[string]evalport.ObservedTrace, error) {
	evidence, err := a.provider.ResolveBatch(ctx, tenantID, traceIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]evalport.ObservedTrace, len(evidence))
	for traceID, trace := range evidence {
		out[traceID] = mapEvaluationEvidence(trace)
	}
	return out, nil
}

func mapEvaluationEvidence(evidence agentdomain.TraceEvidence) evalport.ObservedTrace {
	assignments := make(map[string]evalport.ObservedResourceAssignment, len(evidence.ResourceAssignments))
	for resource, assignment := range evidence.ResourceAssignments {
		assignments[resource] = evalport.ObservedResourceAssignment{
			RevisionID: assignment.RevisionID, ExperimentID: assignment.ExperimentID, Variant: assignment.Variant,
		}
	}
	tools := mapToolObservations(evidence.Tools)
	return evalport.ObservedTrace{
		TraceID: evidence.TraceID, UserID: evidence.UserID, CostUSD: evidence.CostUSD, LatencyMs: evidence.LatencyMs,
		Input: evidence.Input, Output: evidence.Output, TotalTokens: int64(evidence.TotalTokens), // TraceEvidence.TotalTokens(int) → ObservedTrace.TotalTokens(int64)
		Success: evidence.Status == agentdomain.ExecStatusSuccess, SecurityViolation: evidence.SecurityViolation,
		Assignments: assignments, Tools: tools,
	}
}

// mapToolObservations 把执行链路工具调用序列（agent domain.ToolObservation）
// 投影为评测域的 ToolObservation 摘要，逐字段拷贝并保持顺序。Arguments 原样
// 透传（脱敏在评测结果落库层处理）。
func mapToolObservations(tools []agentdomain.ToolObservation) []evalport.ToolObservation {
	mapped := make([]evalport.ToolObservation, 0, len(tools))
	for _, tool := range tools {
		mapped = append(mapped, evalport.ToolObservation{
			ToolName: tool.ToolName, ToolType: tool.ToolType, StepIndex: tool.StepIndex,
			ProviderType: tool.ProviderType, CapabilityID: tool.CapabilityID,
			Arguments: tool.Arguments, RawText: tool.RawText,
		})
	}
	return mapped
}

// buildEvaluationCenterResourceNamer 装配评测中心资源行真名解析器（仅展示富化）：
// agent/skill 由 buildEvaluation 的 guard 保证已装配；mcp 仅在 client manager 就绪
// 时挂载（nil → mcp 行真名缺席、前端占位，读查询不受影响）。
func (c *Container) buildEvaluationCenterResourceNamer() evalport.CenterResourceNamer {
	namer := &centerResourceNamer{agents: c.Agent.Service, skills: c.Skill.VersionService}
	if c.MCP != nil {
		namer.mcp = c.MCP.Manager
	}
	return namer
}

// attachEvaluationCenterNamer 是独立 build step（排在 buildEvaluation 之后）：把中心
// 资源行真名解析器装配进 c.Evaluation。buildEvaluation 行数被质量门禁基线锁定，不再
// 向函数体增行，故装配独立成 step（同 publish-gate/platform-verify 先例）。guard 兜底
// Evaluation 组件缺失或 agent/skill 未装配的 fallback 形态（此时 namer 保持 nil，读查询
// 照常、仅 resource_name 不富化）。
func (c *Container) attachEvaluationCenterNamer(ctx context.Context) error {
	if c.Evaluation == nil || c.Agent == nil || c.Agent.Service == nil || c.Skill == nil || c.Skill.VersionService == nil {
		return nil
	}
	c.Evaluation.ResourceNamer = c.buildEvaluationCenterResourceNamer()
	return nil
}

func (c *Container) buildEvaluation(ctx context.Context) error {
	db := c.dbOrNil()
	if db == nil || c.Skill == nil || c.Skill.VersionService == nil || c.Agent == nil || c.Agent.Service == nil {
		c.Evaluation = &Evaluation{}
		return nil
	}
	suiteRepo := evalpersist.NewPgSuiteRepository(db)
	runRepo := evalpersist.NewPgRunRepository(db)
	jobRepo := evalpersist.NewPgJobRepository(db)
	optimizationRepo := evalpersist.NewPgOptimizationRepository(db)
	experimentRepo := evalpersist.NewPgExperimentRepository(db)
	feedbackRepo := evalpersist.NewPgFeedbackRepository(db)
	queryRepo := evalpersist.NewPgCenterQueryRepository(db)
	candidateRepo := evalpersist.NewPgCandidateCommandRepository(db)
	suiteService := evalapp.NewSuiteService(suiteRepo)
	activationResolver := publishedSkillActivationResolver{versions: c.Skill.VersionService}
	revisionRepo := evalpersist.NewPgRevisionRepository(db)
	manager := skillCandidateManager{versions: c.Skill.VersionService, revisions: revisionRepo}
	var sharedRevisionService *evalapp.RevisionService
	if c.RevisionObjectStore != nil {
		sharedRevisionService = evalapp.NewRevisionService(
			evalpersist.RevisionObjectStoreAdapter{Store: c.RevisionObjectStore},
			revisionRepo,
		)
	}
	skillAdapter := agentScenarioEvaluationAdapter{
		agents:    c.Agent.Service,
		revisions: sharedRevisionService,
		skills:    activationResolver,
		bindings:  agentpersist.NewPgAgentRepo(db),
		resources: manager,
	}
	resourceAdapters := map[evaldomain.ResourceKind]evalport.ResourceAdapter{
		evaldomain.ResourceKindSkill: skillAdapter,
	}
	candidateCreators := map[evaldomain.ResourceKind]evalport.CandidateCreator{
		evaldomain.ResourceKindSkill: manager,
	}
	var agentProvider evalport.AgentRevisionProvider
	var runtimeAgentAdapter *agentEvaluationAdapter
	var mcpProvider evalport.ResourceRevisionProvider
	var runtimeMCPAdapter *mcpEvaluationAdapter
	var knowledgeProvider evalport.ResourceRevisionProvider
	var runtimeKnowledgeAdapter *knowledgeEvaluationAdapter
	if c.Agent != nil && sharedRevisionService != nil {
		agentAdapter := agentEvaluationAdapter{
			revisions: sharedRevisionService, agents: c.Agent.Service, modelValidator: tenantModelValidator(c.Agent.TenantResolver),
			agentUpdater: c.Agent.Service, parameters: c.Parameters.Registry, conversations: c.Agent.Service,
		}
		resourceAdapters[evaldomain.ResourceKindAgent] = agentAdapter
		candidateCreators[evaldomain.ResourceKindAgent] = agentAdapter
		agentProvider = agentAdapter
		runtimeAgentAdapter = &agentAdapter
	}
	if c.MCP != nil && c.MCP.Manager != nil && sharedRevisionService != nil {
		mcpAdapter := mcpEvaluationAdapter{
			runtime: c.MCP.Manager, revisions: sharedRevisionService,
			runtimeStore: c.RevisionObjectStore, actorID: "evaluation-worker", parameters: c.Parameters.Registry,
		}
		resourceAdapters[evaldomain.ResourceKindMCP] = mcpAdapter
		candidateCreators[evaldomain.ResourceKindMCP] = mcpAdapter
		mcpProvider = mcpAdapter
		runtimeMCPAdapter = &mcpAdapter
	}
	if c.Knowledge != nil && c.Knowledge.WorkspaceService != nil && c.Knowledge.RAGService != nil &&
		sharedRevisionService != nil {
		knowledgeAdapter := knowledgeEvaluationAdapter{
			revisions: sharedRevisionService, source: c.Knowledge.WorkspaceService, rerankAvailable: c.Config.RerankConfigured,
			evaluator: knowledgeapp.NewRetrievalEvaluator(c.Knowledge.RAGService), actorID: "evaluation-worker", parameters: c.Parameters.Registry,
		}
		resourceAdapters[evaldomain.ResourceKindKnowledge] = knowledgeAdapter
		candidateCreators[evaldomain.ResourceKindKnowledge] = knowledgeAdapter
		knowledgeProvider = knowledgeAdapter
		runtimeKnowledgeAdapter = &knowledgeAdapter
	}
	var traceReader evalport.TraceEvidenceReader
	if c.Agent != nil {
		traceReader = evaluationTraceEvidenceAdapter{provider: c.Agent.EvidenceProvider}
	}
	judge := buildEvaluationJudge(c)
	service, reviewSvc := newEvaluationServiceWithReview(
		c, db, evaluationResourceRouter{adapters: resourceAdapters}, runRepo, traceReader, judge, suiteRepo,
	)
	experimentService := evalapp.NewExperimentService(experimentRepo)
	jobService := evalapp.NewJobService(jobRepo, service, c.buildSnapshotCapturer(
		experimentService, runtimeMCPAdapter, runtimeKnowledgeAdapter, sharedRevisionService,
		agentpersist.NewPgAgentRepo(db), runtimeAgentAdapter,
	))
	optimizationService := evalapp.NewOptimizationService(
		evaluationCandidateRouter{creators: candidateCreators}, buildEvaluationPromptRewriter(c), optimizationRepo,
	)
	observationSvc := buildObservationService(c, db, traceReader, judge, reviewSvc)
	feedbackService := buildEvaluationFeedbackService(c, feedbackRepo, experimentService, observationSvc)
	worker := c.newEvaluationWorker(ctx, db, jobService, experimentService, experimentRepo, feedbackRepo)
	baselineService, agentRevisionApplier := buildEvaluationRuntime(manager, agentProvider, mcpProvider, knowledgeProvider, runtimeAgentAdapter)
	deleteService := buildEvaluationDeleteService(c, db, suiteRepo, runRepo, jobRepo, experimentRepo, optimizationRepo, feedbackRepo)
	c.Evaluation = &Evaluation{
		Service:              service,
		SuiteService:         suiteService,
		JobService:           jobService,
		Worker:               worker,
		OptimizationService:  optimizationService,
		ExperimentService:    experimentService,
		FeedbackService:      feedbackService,
		QueryService:         evalapp.NewQueryService(queryRepo),
		CandidateService:     evalapp.NewCandidateCommandService(candidateRepo),
		AgentProvider:        agentProvider,
		MCPProvider:          mcpProvider,
		KnowledgeProvider:    knowledgeProvider,
		BaselineService:      baselineService,
		AgentRevisionApplier: agentRevisionApplier,
		TestCaseGenerator:    buildTestCaseGenerator(c, suiteRepo, db),
		ObservationService:   observationSvc,
		ReviewService:        reviewSvc,
		DeleteService:        deleteService,
	}
	if err := c.wireObservationPipeline(ctx, observationSvc); err != nil {
		return err
	}
	c.applyAgentRevisionResolvers(experimentService, runtimeAgentAdapter, runtimeMCPAdapter, runtimeKnowledgeAdapter)
	c.applySkillEvaluationReader(experimentRepo)
	c.Evaluation.ResourceRollbackExecutor = c.buildResourceRollbackExecutor(experimentRepo) // L3 资源回滚执行器（Task 3/T11；Task 4/T13 消费）
	c.buildApprovalActionExecutor()
	return nil
}

// buildSnapshotCapturer 装配创建时快照捕获器：从既有 wiring 组件取 parameters、
// revision 读取、窗口解析与 MCP/Knowledge 分流 resolver。revision 服务或
// parameters/agent 组件缺失时返回 nil（EnqueueRun fail-closed：capturer 未配置
// 即拒绝创建）。bindings/baselines 供 skill 场景锁承载 agent pin（D7）。
func (c *Container) buildSnapshotCapturer(
	experimentService *evalapp.ExperimentService,
	runtimeMCPAdapter *mcpEvaluationAdapter,
	runtimeKnowledgeAdapter *knowledgeEvaluationAdapter,
	revisions agentRevisionService,
	bindings agentport.AgentSkillBinding,
	baselines *agentEvaluationAdapter,
) evalport.SnapshotCapturer {
	if c.Parameters == nil || c.Parameters.Service == nil || c.Agent == nil {
		return nil
	}
	capturer := &snapshotCapturer{
		params:    c.Parameters.Service,
		revisions: revisions,
		bindings:  bindings,
		baselines: baselines,
		modelCtx:  modelContextProvider(c.Agent.TenantResolver),
		details:   tenantModelDetailsProvider(c.Agent.TenantResolver),
		vendor:    llmgateway.LookupModelSpec,
		// skills 复用 agent 装配同源 resolver：评测执行期只依赖 ResolveSkills 的
		// active 发布版解析，不受 skill 自身 canary 实验分流影响。
		skills: publishedSkillActivationResolver{versions: skillVersionService(c)},
		logger: c.Logger,
	}
	if runtimeMCPAdapter != nil && c.MCP != nil && c.MCP.Manager != nil {
		capturer.mcpResolver = experimentMCPRevisionResolver{service: experimentService, adapter: *runtimeMCPAdapter}
	}
	if runtimeKnowledgeAdapter != nil {
		capturer.knowRes = experimentKnowledgeRevisionResolver{service: experimentService, adapter: *runtimeKnowledgeAdapter}
	}
	return capturer
}

// buildEvaluationDeleteService 装配删除服务：owner-or-creator 门禁（fail-closed）。
// roles 复用 c.Agent.RoleResolver（agentport.TenantRoleResolver 与评测本地 port 结构
// 兼容）；reviewRepo 同池重建实例（仓储无状态，复用 buildReviewService 内部实例需改签名）。
func buildEvaluationDeleteService(c *Container, db *pgxpool.Pool, suites evalport.DeleteSuiteRepository,
	runs evalport.DeleteRunRepository, jobs evalport.DeleteJobRepository, experiments evalport.DeleteExperimentRepository,
	candidates evalport.DeleteCandidateRepository, feedback evalport.DeleteFeedbackRepository) *evalapp.DeleteService {
	return evalapp.NewDeleteService(
		c.Agent.RoleResolver,
		suites, runs, jobs, experiments, candidates,
		evalpersist.NewPgReviewRepository(db), feedback,
	)
}

// buildEvaluationPromptRewriter 仅在 agent 租户解析器就绪时装配提示词重写器，否则 nil。
func buildEvaluationPromptRewriter(c *Container) evalapp.PromptRewriter {
	if c.Agent != nil && c.Agent.TenantResolver != nil {
		return gatewayPromptRewriter{resolver: c.Agent.TenantResolver, params: c.Parameters.Service}
	}
	return nil
}

// buildEvaluationFeedbackService 装配反馈服务：证据适配器固定绑定 agent 的
// EvidenceProvider（与 main 内联构造等价），writer 为观测信号接收器。
func buildEvaluationFeedbackService(c *Container, repo evalport.FeedbackRepository, experiments *evalapp.ExperimentService,
	writer evalport.BehaviorSignalWriter) *evalapp.FeedbackService {
	return evalapp.NewFeedbackService(repo, experiments,
		evaluationTraceEvidenceAdapter{provider: c.Agent.EvidenceProvider}, writer)
}

// buildApprovalActionExecutor 评测组件就绪后装配审批动作执行器。paramSvc 注入前做
// typed-nil 判定：nil *parametersapp.Service 装箱进接口后非 nil，会绕过分支的
// nil 检查并在 nil 接收者方法上 panic；Parameters 未装配/Service nil 时显式传 nil
// 接口（平台分支 fail closed）。ResourceRollbackExecutor 由 Task 3 在
// Evaluation struct 装配（未装配 → nil，rollback_resource 分支 fail closed）。
func (c *Container) buildApprovalActionExecutor() {
	if c.Agent != nil {
		var paramSvc platformVersionOps
		if c.Parameters != nil && c.Parameters.Service != nil {
			paramSvc = c.Parameters.Service
		}
		c.Agent.ActionExecutor = newApprovalActionExecutor(
			c.Evaluation,
			paramSvc,
			c.Evaluation.ResourceRollbackExecutor, // Task 3 seam：Evaluation struct 装配的 ACL 适配器
			c.MCP,
			c.platformMetrics(),
			c.Logger,
		)
	}
}

func (c *Container) applyAgentRevisionResolvers(
	experimentService *evalapp.ExperimentService,
	runtimeAgentAdapter *agentEvaluationAdapter,
	runtimeMCPAdapter *mcpEvaluationAdapter,
	runtimeKnowledgeAdapter *knowledgeEvaluationAdapter,
) {
	if c.Agent == nil || c.Agent.Service == nil {
		return
	}
	c.Agent.Service.SetSkillRevisionResolver(experimentSkillRevisionResolver{service: experimentService})
	if runtimeAgentAdapter != nil {
		c.Agent.Service.SetAgentRevisionResolver(experimentAgentRevisionResolver{
			service: experimentService, adapter: *runtimeAgentAdapter,
		})
	}
	if runtimeMCPAdapter != nil && c.MCP != nil && c.MCP.Manager != nil {
		resolver := experimentMCPRevisionResolver{service: experimentService, adapter: *runtimeMCPAdapter}
		c.Agent.Service.SetMCPRevisionResolver(resolver)
		c.Agent.Service.SetMCPToolExecutor(agentMCPExecutor{
			clients: c.MCP.Manager, revisionRuntime: c.MCP.Manager, revisions: resolver,
		})
	}
	if runtimeKnowledgeAdapter != nil {
		c.Agent.Service.SetKnowledgeRevisionResolver(experimentKnowledgeRevisionResolver{
			service: experimentService, adapter: *runtimeKnowledgeAdapter,
		})
	}
}

func (c *Container) applySkillEvaluationReader(experimentRepo evalport.ExperimentRepository) {
	if c.Agent == nil || c.Skill == nil || c.Skill.VersionService == nil {
		return
	}
	if diagnostics, ok := c.Agent.DiagnosticProvider.(*systemAssistantDiagnosticAdapter); ok {
		diagnostics.setSkillEvaluationReader(
			c.Skill.VersionService, skillEvaluationRepositoryAdapter{repo: experimentRepo},
			traceAgentBindingResolver{evidence: c.Agent.EvidenceProvider, registry: c.Agent.Registry},
		)
	}
}

// observationEnabled 读取平台参数 evaluation.observe.enabled，快照优先（评测
// run 创建时点固化的参数值）。默认关闭（fail closed：参数服务不可用时禁用观测链路）。
func observationEnabled(ctx context.Context, params *parametersapp.Service) bool {
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		enabled, _ := snap.Evaluation.Values["evaluation.observe.enabled"].(bool)
		return enabled
	}
	if params == nil {
		return false
	}
	values, err := params.PlatformValues(ctx)
	if err != nil {
		return false
	}
	enabled, _ := values["evaluation.observe.enabled"].(bool)
	return enabled
}

// observationSampleRate 读取平台参数 evaluation.observe.sample_rate，快照优先；
// 未配置或非法时回退常量默认采样率。
func observationSampleRate(ctx context.Context, params *parametersapp.Service) float64 {
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		if rate, ok := snap.Evaluation.Values["evaluation.observe.sample_rate"].(float64); ok && rate >= 0 && rate <= 1 {
			return rate
		}
		return constants.ObservationSampleRateDefault
	}
	if params == nil {
		return constants.ObservationSampleRateDefault
	}
	values, err := params.PlatformValues(ctx)
	if err != nil {
		return constants.ObservationSampleRateDefault
	}
	if rate, ok := values["evaluation.observe.sample_rate"].(float64); ok && rate >= 0 && rate <= 1 {
		return rate
	}
	return constants.ObservationSampleRateDefault
}

// observationPlatformVersion 解析 evaluation 配置组当前生效版本序号（Phase 2
// §4.3 版本锚点），快照优先：快照已有创建时点固化的 VersionSeq，直接返回（比运行时
// IsCurrent 更准确地锚定 run 创建时点）。参数服务未装配 / 无已发布版本时返回
// (0,false) fail-open：观测版本锚点标记 unknown，不阻断落库；DB 读取失败原样返回
// 错误（service 层降级为 unknown + warn）。IsCurrent 由 platform_config_labels
// 生产 label 服务端推导，过滤即得当前生效版本。
func observationPlatformVersion(ctx context.Context, params *parametersapp.Service) (int64, bool, error) {
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		return snap.Evaluation.VersionSeq, snap.Evaluation.VersionSeq > 0, nil
	}
	if params == nil {
		return 0, false, nil
	}
	versions, err := params.Versions(ctx, constants.PlatformGroupEvaluation)
	if err != nil {
		return 0, false, err
	}
	for _, v := range versions {
		if v.IsCurrent {
			return int64(v.VersionSeq), true, nil
		}
	}
	return 0, false, nil
}

// newEvaluationWorker 构建评测周期 worker 并启动 + 注册关闭。独立成函数
// 保持 buildEvaluation 复杂度在质量门禁基线内。
func (c *Container) newEvaluationWorker(ctx context.Context, db *pgxpool.Pool, jobService *evalapp.JobService,
	experimentService *evalapp.ExperimentService, experimentRepo evalport.ExperimentRepository, feedbackRepo evalport.FeedbackRepository,
) *evalapp.Worker {
	experimentRunner := evalapp.NewExperimentRunner(experimentService, experimentRepo, feedbackRepo)
	worker := evalapp.NewWorker(
		evaluationTenantLister{pool: db},
		evalapp.NewMultiRunner(jobService, experimentRunner),
		constants.EvaluationIdleInterval,
		c.platformMetrics(),
	)
	worker.Start(ctx)
	c.shutdown = append(c.shutdown, func(context.Context) error { worker.Stop(); return nil })
	return worker
}

// wireObservationPipeline 装配运行态观测链路（P1a）：ObservationService 已由
// buildEvaluation 先行装配（FeedbackService 复用为行为信号 writer），此处仅挂接
// JetStream 消费 worker。观测消费为 best-effort：NATS 缺失或 JetStream 装配失败
// 仅降级跳过 worker，查询 API 与 agent 执行不受影响（§14 评估器不阻断执行）。
func (c *Container) wireObservationPipeline(ctx context.Context, observationSvc *evalapp.ObservationService) error {
	if c.Storage == nil || c.Storage.NATS == nil {
		return nil
	}
	jsm, err := pipeline.NewJetStreamManager(c.Storage.NATS, c.Logger)
	if err != nil {
		c.Logger.Warn("observation pipeline degraded: jetstream manager unavailable", zap.Error(err))
		return nil
	}
	if err := observation.EnsureStreams(ctx, jsm.JS()); err != nil {
		c.Logger.Warn("observation pipeline degraded: ensure streams failed", zap.Error(err))
		return nil
	}
	consumer, err := jsm.CreateConsumer(ctx, constants.ObservationStream,
		constants.ObservationConsumerName, constants.ObservationSubjectPrefix+".>",
		constants.ObservationAckWait, constants.ObservationMaxDeliver)
	if err != nil {
		c.Logger.Warn("observation pipeline degraded: ensure consumer failed", zap.Error(err))
		return nil
	}
	observationWorker := observation.NewObservationConsumerWorker(consumer, jsm.JS(),
		observationSvc, c.platformMetrics(), c.Logger,
		constants.ObservationAckWait, constants.ObservationMaxDeliver)
	if err := observationWorker.Start(ctx); err != nil {
		c.Logger.Warn("observation pipeline degraded: consumer start failed", zap.Error(err))
		return nil
	}
	c.shutdown = append(c.shutdown, func(context.Context) error {
		observationWorker.Stop()
		return nil
	})
	return nil
}
