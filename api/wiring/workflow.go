package wiring

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	iampersistence "github.com/byteBuilderX/stratum/internal/iam/infrastructure/persistence"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	workflowapp "github.com/byteBuilderX/stratum/internal/workflow/application"
	workflowport "github.com/byteBuilderX/stratum/internal/workflow/domain/port"
	workflowexec "github.com/byteBuilderX/stratum/internal/workflow/infrastructure/executor"
	workflowpersist "github.com/byteBuilderX/stratum/internal/workflow/infrastructure/persistence"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/byteBuilderX/stratum/pkg/textutil"
	"github.com/google/uuid"
)

type workflowAgentService interface {
	Execute(context.Context, string, agentapp.ExecRequest, agentapp.ExecMeta) (*agentapp.AgentResult, int, error)
	ExecuteStream(context.Context, string, agentapp.ExecRequest, agentapp.ExecMeta, func(string)) (
		context.Context, context.CancelFunc, func() (*agentapp.AgentResult, int, error), string, error,
	)
	ExecuteSkillScenario(context.Context, string, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation) (*agentapp.AgentResult, int, error)
	Get(context.Context, string) (agentapp.AgentDTO, error)
}

type workflowAgentExecutor struct{ agents workflowAgentService }

func (e workflowAgentExecutor) ExecuteAgent(
	ctx context.Context,
	tenantID, agentID, userID, executionID, input string,
	onOutputDelta func(string) error,
) (string, string, []workflowport.NodeToolStep, error) {
	traceID := uuid.Must(uuid.NewV7()).String()
	var callbackErr error
	var cancel context.CancelFunc
	_, streamCancel, run, _, err := e.agents.ExecuteStream(
		ctx,
		agentID,
		// UserID 透传执行人真实 user_id（run.CreatedBy），使审批请求人、自动会话、
		// 记忆与轨迹归属执行人；ConversationSource 标记 workflow 自动会话供列表隐藏。
		agentapp.ExecRequest{Query: input, UserID: userID, ConversationSource: "workflow"},
		agentapp.ExecMeta{TenantID: tenantID, TraceID: traceID, ExecutionID: executionID},
		func(delta string) {
			if callbackErr != nil || onOutputDelta == nil {
				return
			}
			if callbackErr = onOutputDelta(delta); callbackErr != nil && cancel != nil {
				cancel()
			}
		},
	)
	if err != nil {
		return "", traceID, nil, err
	}
	cancel = streamCancel
	defer cancel()
	result, _, err := run()
	if callbackErr != nil {
		return "", traceID, nil, fmt.Errorf("persist workflow output delta: %w", callbackErr)
	}
	if err != nil {
		// agent 原生工具审批待决（BatchToolApprovalRequiredError），或批准后重跑仍
		// pending（ErrApprovalNotApproved 双保险）：翻译为 workflow sentinel，由
		// executor 把节点置为 paused 等待审批，不进入失败重试。
		var batchErr *agentport.BatchToolApprovalRequiredError
		if errors.As(err, &batchErr) || errors.Is(err, agentapp.ErrApprovalNotApproved) {
			return "", traceID, nil, fmt.Errorf("agent approval pending: %w", workflowport.ErrAgentApprovalPending)
		}
		return "", traceID, nil, err
	}
	return result.Output, traceID, safeWorkflowToolSteps(result.ToolCalls), nil
}

func safeWorkflowToolSteps(toolCalls []agentapp.ToolCall) []workflowport.NodeToolStep {
	steps := make([]workflowport.NodeToolStep, 0, len(toolCalls))
	for _, call := range toolCalls {
		summary := "工具执行成功"
		if call.Error != nil {
			summary = "工具执行失败"
		}
		steps = append(steps, workflowport.NodeToolStep{
			ToolName:   textutil.TruncateRunes(call.ToolName, constants.WorkflowToolNameMaxRunes),
			DurationMS: max(call.Duration.Milliseconds(), 0),
			Summary:    textutil.TruncateRunes(summary, constants.WorkflowToolSummaryMaxRunes),
		})
	}
	return steps
}

type workflowSkillVersions interface {
	ResolveActivation(context.Context, string, string) (skillapp.SkillActivationView, bool, error)
}

type workflowSkillExecutor struct {
	agents   workflowAgentService
	versions workflowSkillVersions
}

func (e workflowSkillExecutor) ExecuteSkill(ctx context.Context, tenantID, agentID, userID, skillID, revisionID, input string) (string, string, error) {
	view, found, err := e.versions.ResolveActivation(ctx, skillID, revisionID)
	if err != nil {
		return "", "", err
	}
	if !found || view.RevisionID != revisionID {
		return "", "", fmt.Errorf("pinned skill revision not found")
	}
	activation := agentport.SkillActivation{SkillID: view.SkillID, RevisionID: view.RevisionID, Name: view.Name, Description: view.Description, Instructions: view.Instructions, InputSchema: view.InputSchema, OutputSchema: view.OutputSchema}
	traceID := uuid.Must(uuid.NewV7()).String()
	result, _, err := e.agents.ExecuteSkillScenario(ctx, agentID, agentapp.ExecRequest{Query: input, UserID: userID, ConversationSource: "workflow"}, agentapp.ExecMeta{TenantID: tenantID, TraceID: traceID}, []agentport.SkillActivation{activation})
	if err != nil {
		return "", traceID, err
	}
	return result.Output, traceID, nil
}

// workflowAgentApprovalResolver 把 agent 原生工具审批判定适配到 workflow 的
// AgentApprovalResolver port：executionID 下所有审批行均已终态（无未过期
// pending）才视为可续跑；任一 pending 即仍待决，agent 节点保持暂停。
// store 缺失时 fail-closed 返回错误，避免错误放行导致审批被跳过。
type workflowAgentApprovalResolver struct{ approvals agentport.ToolApprovalRepo }

func (r workflowAgentApprovalResolver) ResolveAgentApproval(ctx context.Context, tenantID, executionID string) (bool, error) {
	if r.approvals == nil {
		return false, fmt.Errorf("workflow agent approval store unavailable")
	}
	rows, err := r.approvals.ListByExecution(ctx, tenantID, executionID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Status == string(agentdomain.ToolApprovalPending) && row.ExpiresAt.After(time.Now()) {
			return false, nil
		}
	}
	return true, nil
}

// workflowSkillBindingResolver 把 agent 能力适配到 workflow 的 SkillBindingResolver
// port：agent 的 allowedSkills 需在 tenant 上下文中解析（AgentService.Get 依赖 ctx）。
type workflowSkillBindingResolver struct{ agents workflowAgentService }

func (r workflowSkillBindingResolver) AgentAllowedSkills(ctx context.Context, tenantID, agentID string) ([]string, error) {
	ctx, err := workflowMCPTenantContext(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	dto, err := r.agents.Get(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return dto.AllowedSkills, nil
}

type workflowMCPPolicy interface {
	GetToolRisk(context.Context, string, string) (mcpdomain.ToolRiskLevel, error)
}
type workflowMCPManager interface {
	CallTool(context.Context, string, string, interface{}) (interface{}, error)
}

type workflowMCPExecutor struct {
	policies workflowMCPPolicy
	manager  workflowMCPManager
}

func workflowMCPTenantContext(ctx context.Context, tenantID string) (context.Context, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("workflow MCP tenant is required")
	}
	return postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID, UserID: "workflow-worker", Role: postgres.RoleTenantAdmin}), nil
}

func (e workflowMCPExecutor) ToolRisk(ctx context.Context, tenantID string, serverID, toolName string) (workflowexec.ToolRisk, error) {
	ctx, err := workflowMCPTenantContext(ctx, tenantID)
	if err != nil {
		return "", err
	}
	risk, err := e.policies.GetToolRisk(ctx, serverID, toolName)
	return workflowexec.ToolRisk(risk), err
}
func (e workflowMCPExecutor) CallTool(ctx context.Context, tenantID string, serverID, toolName string, input map[string]any) (any, error) {
	ctx, err := workflowMCPTenantContext(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return e.manager.CallTool(ctx, serverID, toolName, input)
}

type Workflow struct {
	DefinitionService *workflowapp.DefinitionService
	RunService        *workflowapp.RunService
	ControlService    *workflowapp.ControlService
	Worker            interface {
		Run(context.Context, time.Duration)
	}
}

type workflowRunAdvancer struct{ runs *workflowapp.RunService }

func (a workflowRunAdvancer) Execute(ctx context.Context, tenantID, runID string) error {
	tenantCtx := postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID, UserID: "workflow-worker", Role: postgres.RoleTenantAdmin})
	return a.runs.Execute(tenantCtx, tenantID, runID)
}

func (c *Container) buildWorkflow(_ context.Context) error {
	db := c.dbOrNil()
	if db == nil || c.Agent == nil || c.Agent.Service == nil {
		return nil
	}
	store := workflowpersist.NewPgStore(db)
	newID := func() string { return uuid.Must(uuid.NewV7()).String() }
	agentExecutor := workflowAgentExecutor{agents: c.Agent.Service}
	registry := workflowexec.NewRegistry(agentExecutor, workflowSkillExecutor{agents: c.Agent.Service, versions: c.Skill.VersionService}, workflowMCPExecutor{policies: c.MCP.Service, manager: c.MCP.Manager})
	runs := workflowapp.NewRunServiceWithRegistry(store, store, registry, newID, c.Logger)
	// agent 原生工具审批判定注入 RunService（reconcile agent 暂停节点）与
	// ControlService（Resume 时挡住未决审批），共用同一 ToolApprovalRepo 实现。
	approvalResolver := workflowAgentApprovalResolver{approvals: c.Agent.ApprovalStore}
	runs.SetAgentApprovalResolver(approvalResolver)
	controlService := workflowapp.NewControlService(store, newID)
	controlService.SetAgentApprovalResolver(approvalResolver)
	defService := workflowapp.NewDefinitionService(store, store, newID)
	defService.SetFailureAuditRecorder(failureRecorderOf(c))
	defService.SetLogger(c.Logger)
	defService.SetSkillBindingResolver(workflowSkillBindingResolver{agents: c.Agent.Service})
	defService.SetTenantRoleResolver(c.Agent.RoleResolver)
	defService.SetEditorRepo(workflowpersist.NewPgWorkflowResourceEditorRepo(db))
	defService.SetActorNameResolver(iampersistence.NewPgActorNameResolver(db))
	c.Workflow = &Workflow{DefinitionService: defService, RunService: runs, ControlService: controlService}
	c.Workflow.Worker = workflowapp.NewWorker("workflow-"+newID(), store, workflowRunAdvancer{runs: runs}, 30*time.Second, c.platformMetrics())
	return nil
}
