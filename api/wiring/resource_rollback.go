package wiring

import (
	"context"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	knowledgeapp "github.com/byteBuilderX/stratum/internal/knowledge/application"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	skilldomain "github.com/byteBuilderX/stratum/internal/skill/domain"
	versioningdomain "github.com/byteBuilderX/stratum/internal/versioning/domain"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
)

// rollbackWorkerActor 是读路径（版本列表）的租户 ctx 用户标识，语义同 P1
// evaluation adapters 的 "evaluation-worker"（只读列表不产生审计）。
const rollbackWorkerActor = "evaluation-worker"

// rollbackTenantCtx 注入租户上下文：reqctx tenant + postgres schema tenant。
func rollbackTenantCtx(ctx context.Context, tenantID, actorID string) context.Context {
	ctx = reqctx.WithTenantID(ctx, tenantID)
	return postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: actorID, Role: postgres.RoleTenantAdmin,
	})
}

// ---------------------------------------------------------------------------
// agent 产品回滚适配器
// ---------------------------------------------------------------------------

type resourceRollbackAgentAdapter struct {
	agents *agentapp.AgentService
}

func (a *resourceRollbackAgentAdapter) ListCandidates(ctx context.Context, tenantID, resourceID string) ([]evalport.RollbackCandidate, error) {
	versions, err := a.agents.ListVersions(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), resourceID)
	if err != nil {
		return nil, err
	}
	out := make([]evalport.RollbackCandidate, 0, len(versions))
	for _, v := range versions {
		out = append(out, evalport.RollbackCandidate{
			ID: v.ID, RevisionNo: v.VersionNo, IsCurrent: v.IsCurrent,
			Rollbackable: v.Status == string(versioningdomain.VersionStatusDeprecated),
		})
	}
	return out, nil
}

func (a *resourceRollbackAgentAdapter) RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	_, err := a.agents.Rollback(ctx, resourceID, agentapp.RollbackAgentInput{ActorID: actorID, VersionID: candidateID})
	return err
}

// ---------------------------------------------------------------------------
// knowledge 产品回滚适配器（resourceID = workspace name，与 evaluation 资源锚点一致）
// ---------------------------------------------------------------------------

type resourceRollbackKnowledgeAdapter struct {
	svc *knowledgeapp.WorkspaceService
}

func (a *resourceRollbackKnowledgeAdapter) ListCandidates(ctx context.Context, tenantID, resourceID string) ([]evalport.RollbackCandidate, error) {
	versions, err := a.svc.ListWorkspaceVersions(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), tenantID, resourceID)
	if err != nil {
		return nil, err
	}
	out := make([]evalport.RollbackCandidate, 0, len(versions))
	for _, v := range versions {
		out = append(out, evalport.RollbackCandidate{
			ID: v.ID, RevisionNo: v.VersionNo, IsCurrent: v.IsCurrent,
			Rollbackable: v.Status == string(versioningdomain.VersionStatusDeprecated),
		})
	}
	return out, nil
}

func (a *resourceRollbackKnowledgeAdapter) RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	_, err := a.svc.RollbackWorkspace(ctx, tenantID, resourceID, knowledgeapp.RollbackWorkspaceInput{
		ActorID: actorID, VersionID: candidateID,
	})
	return err
}

// ---------------------------------------------------------------------------
// skill 产品回滚适配器（revisionID 语义，自有版本机制）
// ---------------------------------------------------------------------------

type resourceRollbackSkillAdapter struct {
	versions *skillapp.VersionService
}

func (a *resourceRollbackSkillAdapter) ListCandidates(ctx context.Context, tenantID, resourceID string) ([]evalport.RollbackCandidate, error) {
	revs, err := a.versions.ListRevisions(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), resourceID)
	if err != nil {
		return nil, err
	}
	out := make([]evalport.RollbackCandidate, 0, len(revs))
	for _, r := range revs {
		out = append(out, evalport.RollbackCandidate{
			ID: r.ID, RevisionNo: r.RevisionNo, IsCurrent: r.IsCurrent,
			Rollbackable: r.Status == skilldomain.VersionStatusDeprecated,
		})
	}
	return out, nil
}

func (a *resourceRollbackSkillAdapter) RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	return a.versions.RollbackRevision(ctx, resourceID, candidateID, actorID)
}

// ---------------------------------------------------------------------------
// canary 适配器：deployment 判定（E3 repo） + 清 canary（E3 ExperimentService）
// ---------------------------------------------------------------------------

type resourceRollbackCanaryAdapter struct {
	experiments *evalapp.ExperimentService
	deployments evalport.ExperimentRepository
}

func (a *resourceRollbackCanaryAdapter) ResolveDeployment(ctx context.Context, tenantID string, kind evaldomain.ResourceKind, resourceID string) (evaldomain.Deployment, bool, error) {
	return a.deployments.ResolveDeployment(rollbackTenantCtx(ctx, tenantID, rollbackWorkerActor), tenantID, string(kind), resourceID)
}

func (a *resourceRollbackCanaryAdapter) ClearCanary(ctx context.Context, tenantID, experimentID, actorID, reason string) error {
	ctx = reqctx.WithSystemActor(rollbackTenantCtx(ctx, tenantID, actorID), actorID)
	_, err := a.experiments.Rollback(ctx, tenantID, experimentID, evalapp.ExperimentCommandInput{
		ActorID: actorID, Reason: reason, IdempotencyKey: "gate-rollback-" + experimentID,
	})
	return err
}

// ---------------------------------------------------------------------------
// 构造器：只装配已就绪的真实 service；Task 4/T13 复用同一来源。
// ---------------------------------------------------------------------------

// rollbackProductBackends 装配已就绪的 agent/knowledge/skill 产品回滚后端；mcp 恒不入 map
// （无产品回滚链）。独立成函数控制构造器圈复杂度（质量门禁 CC≤10）。
func (c *Container) rollbackProductBackends() map[evaldomain.ResourceKind]evalport.ProductRollbackBackend {
	products := map[evaldomain.ResourceKind]evalport.ProductRollbackBackend{}
	if c.Agent != nil && c.Agent.Service != nil {
		products[evaldomain.ResourceKindAgent] = &resourceRollbackAgentAdapter{agents: c.Agent.Service}
	}
	if c.Knowledge != nil && c.Knowledge.WorkspaceService != nil {
		products[evaldomain.ResourceKindKnowledge] = &resourceRollbackKnowledgeAdapter{svc: c.Knowledge.WorkspaceService}
	}
	if c.Skill != nil && c.Skill.VersionService != nil {
		products[evaldomain.ResourceKindSkill] = &resourceRollbackSkillAdapter{versions: c.Skill.VersionService}
	}
	return products
}

// rollbackCanaryBackend 装配 canary 后端；experimentRepo 或 ExperimentService 缺失 → nil。
func (c *Container) rollbackCanaryBackend(experimentRepo evalport.ExperimentRepository) evalport.CanaryRollbackBackend {
	if experimentRepo != nil && c.Evaluation != nil && c.Evaluation.ExperimentService != nil {
		return &resourceRollbackCanaryAdapter{
			experiments: c.Evaluation.ExperimentService, deployments: experimentRepo,
		}
	}
	return nil
}

// buildResourceRollbackExecutor 组装 L3 资源回滚执行器。无任何可回滚后端（产品 map 空且
// canary nil）→ 返回 nil，GateService auto 保持 skip（fail-open，语义同 P1 未装配）。
func (c *Container) buildResourceRollbackExecutor(experimentRepo evalport.ExperimentRepository) evalport.ResourceRollbackExecutor {
	products := c.rollbackProductBackends()
	canary := c.rollbackCanaryBackend(experimentRepo)
	if len(products) == 0 && canary == nil {
		return nil
	}
	return evalapp.NewResourceRollbackExecutor(evalapp.ResourceRollbackExecutorDeps{
		Logger:   c.Logger,
		Products: products,
		Canary:   canary,
	})
}
