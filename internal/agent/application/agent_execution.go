// Agent execution path: revision execution, Execute/ExecuteStream,
// logging and memory-buffer side effects.

package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentgraph "github.com/byteBuilderX/stratum/internal/agent/application/graph"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func (s *AgentService) ExecuteRevision(
	ctx context.Context, revision domain.AgentRevision, req ExecRequest, meta ExecMeta,
) (*AgentResult, int, error) {
	if strings.TrimSpace(meta.TenantID) == "" {
		return nil, 0, fmt.Errorf("agent service: tenant id required")
	}
	if err := revision.Validate(); err != nil {
		return nil, 0, fmt.Errorf("agent service: validate revision: %w", err)
	}
	a, err := s.buildRevisionAgent(revision)
	if err != nil {
		return nil, 0, err
	}
	if s.deps.Metrics != nil {
		a = a.WithMetrics(s.deps.Metrics)
	}
	if s.deps.Ledger != nil {
		a = a.WithLedger(s.deps.Ledger)
	}
	executionID := uuid.Must(uuid.NewV7()).String()
	_, options, err := s.assembleOptions(ctx, a, req, meta, executionID)
	if err != nil {
		return nil, 0, err
	}
	options = append(options, WithExecutionID(executionID))
	start := time.Now()
	execCtx, cancel := revisionExecutionContext(ctx)
	defer cancel()
	result, err := a.Execute(execCtx, req.Query, options...)
	return result, int(time.Since(start).Milliseconds()), err
}

func revisionExecutionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(ctx))
}

func (s *AgentService) buildRevisionAgent(revision domain.AgentRevision) (*BaseAgent, error) {
	if revision.MemoryInjectorRequired && s.deps.MemoryInjector == nil {
		return nil, fmt.Errorf("agent service: revision requires memory injector")
	}
	if revision.RecallMemoryRequired && s.deps.RecallMemory == nil {
		return nil, fmt.Errorf("agent service: revision requires recall memory")
	}
	a := NewBaseAgent(revisionConfig(revision), s.deps.Logger)
	a.GlobalSystemSuffix = revision.GlobalSystemSuffix
	a.PlatformPromptResolver = s.deps.PlatformPromptResolver
	if revision.MemoryInjectorRequired {
		a.MemoryInjector = s.deps.MemoryInjector
	}
	if revision.RecallMemoryRequired {
		a.RecallMemoryFn = s.deps.RecallMemory
	}
	return a, nil
}

func revisionConfig(revision domain.AgentRevision) *domain.AgentConfig {
	cfg := &domain.AgentConfig{
		ID: revision.AgentID, Type: revision.Type, SystemPrompt: revision.SystemPrompt,
		LLMModel: revision.Model, MaxIterations: revision.MaxIterations,
		MaxContextTokens: revision.ModelParameters.MaxContextTokens,
		Temperature:      revision.ModelParameters.Temperature,
		MaxTokens:        revision.ModelParameters.MaxTokens,
		MemoryScope:      revision.MemoryScope,
		StuckThreshold:   revision.StuckThreshold,
	}
	for _, binding := range revision.Bindings {
		if !binding.Enabled {
			continue
		}
		switch binding.Kind {
		case domain.AgentBindingSkill:
			cfg.AllowedSkills = append(cfg.AllowedSkills, binding.ID)
		case domain.AgentBindingMCP:
			cfg.MCPToolIDs = append(cfg.MCPToolIDs, binding.ID)
		case domain.AgentBindingKnowledge:
			cfg.KnowledgeWorkspaceIDs = append(cfg.KnowledgeWorkspaceIDs, binding.ID)
			cfg.KnowledgeWorkspaceNames = append(cfg.KnowledgeWorkspaceNames, binding.Name)
			cfg.KnowledgeWorkspaceDescriptions = append(cfg.KnowledgeWorkspaceDescriptions, binding.Description)
		}
	}
	return cfg
}

// List returns all agents in the tenant schema.

type ExecRequest struct {
	Query          string
	ConversationID string
	UserID         string
	MaxSteps       int
	Timeout        time.Duration
	// ConversationSource 标记自动会话的来源（manual/workflow 等）。空值按 manual
	// 处理；workflow 自动会话带 source 标记，会话列表过滤隐藏，避免污染执行人列表。
	ConversationSource string
}

// ExecMeta carries per-call routing metadata sourced from middleware
// (tenant, trace) — never inferred from request body.

type ExecMeta struct {
	TenantID                   string
	TraceID                    string
	Stream                     bool
	ExecutionID                string // optional; generated if empty, used for resume
	EvolutionTrace             EvolutionTraceMetadata
	KnowledgeAssignmentsPinned bool
	PinnedKnowledgeRevisions   map[string]port.KnowledgeRevisionPin
	PinnedMCPRevisions         map[string]port.MCPRevisionPin
	// DelegateEventCb 在委托子 agent 进入/结束时回调（SSE delegate_status 帧
	// 出口）。仅流式路径由 handler 注入；nil = 不推送委托进度。
	DelegateEventCb func(agentgraph.DelegateEvent)
}

// ExecutionRowDTO is the wire shape emitted by ListExecutions.

type ExecutionRowDTO struct {
	ID            string
	TraceID       string
	AgentID       string
	AgentName     string
	UserID        string
	Status        string
	InputPreview  string
	OutputPreview string
	ErrorMessage  string
	TotalTokens   int
	DurationMs    int
	CreatedAt     string
}

// Execute runs an agent synchronously, persisting an execution record
// on completion. The returned context is for streaming callers — it is
// nil here. Callers receive (*AgentResult, durationMs, error) so the
// transport can render Duration uniformly.

func (s *AgentService) ensureConversation(ctx context.Context, tenantID, agentID, userID, source string, req *ExecRequest) {
	if req.ConversationID != "" || s.deps.ChatStore == nil {
		return
	}
	if source == "" {
		source = "manual"
	}
	createCtx, createCancel := context.WithTimeout(ctx, constants.AgentDBQueryTimeout)
	conv, err := s.deps.ChatStore.CreateConversation(createCtx, tenantID, agentID, userID, "新会话", source)
	createCancel()
	if err != nil {
		s.deps.Logger.Warn("agent: auto-create conversation failed", zap.Error(err))
		return
	}
	req.ConversationID = conv.ID
}

// OpenEvalConversation 打开一条评测驱动的受控会话（source=evaluation），属主为
// userID（评测 requestedBy）。评测会话与手动/工作流会话同表同协议（真历史、
// 逐轮续跑、按 user_id 加载历史），生产默认会话列表对其隐藏，避免污染用户工作台。
// 返回会话 ID，供评测 wiring 在每次评测会话启动时写入并逐轮续跑。ChatStore 未装配时
// fail-closed 报错，绝不静默返回空串。
func (s *AgentService) OpenEvalConversation(ctx context.Context, tenantID, agentID, userID string) (string, error) {
	if s.deps.ChatStore == nil {
		return "", fmt.Errorf("agent service: open eval conversation: chat store not configured")
	}
	createCtx, createCancel := context.WithTimeout(ctx, constants.AgentDBQueryTimeout)
	conv, err := s.deps.ChatStore.CreateConversation(
		createCtx, tenantID, agentID, userID, "评测会话", constants.ChatConversationSourceEvaluation,
	)
	createCancel()
	if err != nil {
		return "", fmt.Errorf("agent service: open eval conversation: %w", err)
	}
	return conv.ID, nil
}

func executionSubject(req ExecRequest, meta ExecMeta) string {
	if req.ConversationID != "" {
		return req.ConversationID
	}
	return meta.TraceID
}

func (s *AgentService) resolveExecutionAgent(
	ctx context.Context,
	current Agent,
	tenantID, agentID, subjectID string,
) (Agent, port.AgentRevisionAssignment, error) {
	if s.deps.AgentRevisionResolver == nil {
		return current, port.AgentRevisionAssignment{}, nil
	}
	assignment, found, err := s.deps.AgentRevisionResolver.ResolveAgentRevision(
		ctx, tenantID, agentID, subjectID,
	)
	if err != nil {
		return nil, port.AgentRevisionAssignment{}, fmt.Errorf("resolve Agent experiment assignment: %w", err)
	}
	if !found {
		return current, port.AgentRevisionAssignment{}, nil
	}
	if assignment.Revision.AgentID != agentID || assignment.RevisionID == "" {
		return nil, port.AgentRevisionAssignment{}, errors.New("resolve Agent experiment assignment: invalid revision")
	}
	resolved, err := s.buildRevisionAgent(assignment.Revision)
	if err != nil {
		return nil, port.AgentRevisionAssignment{}, fmt.Errorf("resolve Agent experiment revision: %w", err)
	}
	if s.deps.Metrics != nil {
		resolved = resolved.WithMetrics(s.deps.Metrics)
	}
	if s.deps.Ledger != nil {
		resolved = resolved.WithLedger(s.deps.Ledger)
	}
	resolved.Name = current.GetConfig().Name
	return resolved, assignment, nil
}

func applyAgentAssignment(meta *ExecMeta, agentID string, assignment port.AgentRevisionAssignment) {
	if assignment.RevisionID == "" {
		return
	}
	if meta.EvolutionTrace.ResourceManifest == nil {
		meta.EvolutionTrace.ResourceManifest = make(map[string]string)
	}
	key := "agent:" + agentID
	meta.EvolutionTrace.ResourceManifest[key] = assignment.RevisionID
	if assignment.ExperimentID == "" {
		return
	}
	if meta.EvolutionTrace.ExperimentAssignments == nil {
		meta.EvolutionTrace.ExperimentAssignments = make(map[string]ExperimentAssignment)
	}
	meta.EvolutionTrace.ExperimentAssignments[key] = ExperimentAssignment{
		ExperimentID: assignment.ExperimentID,
		Variant:      assignment.Variant,
	}
	if meta.EvolutionTrace.ExperimentID == "" {
		meta.EvolutionTrace.ExperimentID = assignment.ExperimentID
		meta.EvolutionTrace.Variant = assignment.Variant
	}
}

func recordExecutionPreparation(
	ctx context.Context, a Agent, req ExecRequest, meta ExecMeta, executionID string,
) {
	cfg := ExecutionConfig{
		TenantID:       meta.TenantID,
		TraceID:        meta.TraceID,
		ExecutionID:    executionID,
		ConversationID: req.ConversationID,
		UserID:         req.UserID,
		EvolutionTrace: meta.EvolutionTrace,
	}
	config := a.GetConfig()
	oteltrace.SpanFromContext(ctx).SetAttributes(
		agentExecutionAttributes(config.ID, config.Name, domain.ReActAgent, cfg, config.MaxContextTokens)...,
	)
}

func recordExecutionPreparationFailure(ctx context.Context, start time.Time, stage string) {
	span := oteltrace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("stratum.error.category", "resource_preparation_failed"),
		attribute.String("stratum.failure.stage", stage),
		attribute.String("opik.metadata.stratum.status", domain.ExecStatusError),
		attribute.String("opik.metadata.stratum.error_category", "resource_preparation_failed"),
		attribute.String("opik.metadata.stratum.failure_stage", stage),
		attribute.Int64("opik.metadata.stratum.duration_ms", time.Since(start).Milliseconds()),
	)
	span.SetStatus(codes.Error, "agent resource preparation failed")
}

func (s *AgentService) Execute(ctx context.Context, agentID string, req ExecRequest, meta ExecMeta) (*AgentResult, int, error) {
	executionID := executionIDOrNew(meta.ExecutionID)
	a, req, meta, _, options, cfg, resuming, terminal, consumedApproval, err := s.prepareAgentExecution(ctx, agentID, req, meta, executionID)
	if err != nil {
		return nil, 0, err
	}
	s.logAgentExecutionDebug("agent.execute", agentID, meta, req)
	execCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	// P1b：执行上下文注入规则拦截累积器，RuleGuard 命中时追加，emitObservation
	// 读取转观测事件 rule_signals（§4.1）。
	blocks := &[]domain.RuleBlock{}
	execCtx = context.WithValue(execCtx, ruleBlockCollectorKey{}, blocks)

	start := time.Now()
	result, err := a.Execute(execCtx, req.Query, options...)
	durationMs := int(time.Since(start).Milliseconds())
	s.recordSystemAssistantExecution(cfg, result, err)
	s.logAgentExecution("agent.execute", agentID, meta, req, durationMs, err)
	if err == nil && result != nil {
		// MemoryBuffer 是执行成功后的旁路异步摄取（Redis buffer，供后台记忆
		// 提取）。答案已交付，缓冲失败不阻断响应——降级决策，但错误必须显式
		// 处理并记录，禁止静默吞掉。
		scope := a.GetConfig().MemoryScope
		s.bufferMemoryTurn(ctx, meta, req, agentID, scope, "user", req.Query)
		s.bufferMemoryTurn(ctx, meta, req, agentID, scope, "assistant", result.Output)
		s.emitObservation(execCtx, meta, agentID, executionID, result)
	}
	// 任务结束轨迹反思：与 fact 提取并列的链路，fail-open 显式降级。
	s.enqueueTrajectoryReflection(ctx, meta, req, agentID, a.GetConfig().MemoryScope, executionID, result)
	if resuming {
		err = s.finishApprovalResume(ctx, meta.TenantID, executionID, consumedApproval, terminal, err)
	}
	return result, durationMs, err
}

// ExecuteStream runs an agent with token streaming. tokenCb is invoked
// per LLM token; it must be safe for concurrent use with this call's
// goroutine. The returned context carries the per-tenant LLM completer
// (for inner streaming RAG / tool calls) — transport must use it for
// the SSE write loop. cancel() releases the per-call deadline.

func (s *AgentService) ExecuteStream(
	ctx context.Context, agentID string, req ExecRequest, meta ExecMeta, tokenCb func(string),
) (execCtx context.Context, cancel context.CancelFunc, run func() (*AgentResult, int, error), executionID string, err error) {
	// 复用调用方传入的 execution_id（断线续接的恢复键）：非空则沿用同一执行
	// 供 resumeFromCheckpoint 定位 checkpoint，空则生成新 ID（此前无条件新建，
	// 导致流式路径即使带 execution_id 也永远无法续接）。
	executionID = executionIDOrNew(meta.ExecutionID)
	a, req, meta, streamCtx, options, cfg, resuming, terminal, consumedApproval, err := s.prepareAgentExecution(ctx, agentID, req, meta, executionID)
	if err != nil {
		return nil, nil, nil, "", err
	}
	var firstToken sync.Once
	var streamStarted time.Time
	wrappedTokenCb := tokenCb
	if s.deps.Metrics != nil {
		wrappedTokenCb = func(token string) {
			firstToken.Do(func() {
				s.deps.Metrics.RecordSystemAssistantTTFT(cfg.AssistantRoleClass,
					"", time.Since(streamStarted).Seconds())
			})
			if tokenCb != nil {
				tokenCb(token)
			}
		}
	}
	// 总是追加 token callback：run 闭包延迟执行，回调捕获 cfg 在调用期已确定。
	options = append(options, WithTokenCallback(wrappedTokenCb), WithDelegateEventCallback(meta.DelegateEventCb), WithExecutionID(executionID))

	execCtx, cancel = context.WithCancel(context.WithoutCancel(streamCtx))
	// P1b：与 Execute 路径一致的规则拦截累积器注入（§4.1）。
	blocks := &[]domain.RuleBlock{}
	execCtx = context.WithValue(execCtx, ruleBlockCollectorKey{}, blocks)
	run = func() (*AgentResult, int, error) {
		s.logAgentExecutionDebug("agent.execute_stream", agentID, meta, req)
		start := time.Now()
		streamStarted = start
		res, runErr := a.Execute(execCtx, req.Query, options...)
		durationMs := int(time.Since(start).Milliseconds())
		s.recordSystemAssistantExecution(cfg, res, runErr)
		s.logAgentExecution("agent.execute_stream", agentID, meta, req, durationMs, runErr)
		if runErr == nil && res != nil {
			// 降级决策与 Execute 路径一致：答案已交付，旁路记忆缓冲失败只记日志。
			scope := a.GetConfig().MemoryScope
			s.bufferMemoryTurn(ctx, meta, req, agentID, scope, "user", req.Query)
			s.bufferMemoryTurn(ctx, meta, req, agentID, scope, "assistant", res.Output)
			s.emitObservation(execCtx, meta, agentID, executionID, res)
		}
		s.enqueueTrajectoryReflection(ctx, meta, req, agentID, a.GetConfig().MemoryScope, executionID, res)
		if resuming {
			// 审批续跑收尾：成功/消费标记推进 checkpoint；失败且未消费批准时
			// 回滚 running→waiting_approval，让 member 可重试同一批准。
			runErr = s.finishApprovalResume(ctx, meta.TenantID, executionID, consumedApproval, terminal, runErr)
		}
		return res, durationMs, runErr
	}
	return execCtx, cancel, run, executionID, nil
}

// prepareAgentExecution 是 Execute/ExecuteStream 的公共准备链：Registry 解析
// Agent → ensure 会话与 init checkpoint → 实验 revision 解析 → 审批续跑抢占并
// 重写 req/meta → assembleOptions → 追加恢复选项。返回重写后的 req/meta、
// streamCtx（仅流式使用，供携带 per-tenant LLM completer）、options、cfg、
// 续跑标记与 consumed 判定。错误已在链内 wrap 到统一语义，调用方原样上抛。

func (s *AgentService) prepareAgentExecution(
	ctx context.Context, agentID string, req ExecRequest, meta ExecMeta, executionID string,
) (a Agent, outReq ExecRequest, outMeta ExecMeta, streamCtx context.Context, options []ExecutionOption, cfg *ExecutionConfig, resuming bool, terminal bool, consumed func() bool, err error) {
	a, ok, err := s.deps.Registry.Get(ctx, agentID)
	if err != nil {
		return nil, req, meta, nil, nil, nil, false, false, nil, fmt.Errorf("get agent: %w", err)
	}
	if !ok {
		return nil, req, meta, nil, nil, nil, false, false, nil, ErrNotFound
	}
	s.ensureConversation(ctx, meta.TenantID, agentID, req.UserID, req.ConversationSource, &req)
	s.ensureInitialCheckpoint(ctx, meta, req, agentID, executionID)
	preparationStart := time.Now()
	recordExecutionPreparation(ctx, a, req, meta, executionID)
	a, assignment, err := s.resolveExecutionAgent(ctx, a, meta.TenantID, agentID, executionSubject(req, meta))
	if err != nil {
		recordExecutionPreparationFailure(ctx, preparationStart, "resolve_agent_revision")
		return nil, req, meta, nil, nil, nil, false, false, nil, fmt.Errorf("resolve revision: %w", err)
	}
	applyAgentAssignment(&meta, agentID, assignment)
	recordExecutionPreparation(ctx, a, req, meta, executionID)
	// 审批续跑：命中 waiting_approval checkpoint 则抢占并把 req/meta 重写为整批
	// 批准载荷快照（首条为准，同 executionID 共享发起人/会话）；任一审批仍 pending
	// 整批 202 等待。buildApprovalResumeOptions 追加在 assembleOptions 之后。
	entries, resuming, req, meta, err := s.maybeResumeApproval(ctx, agentID, req, meta, executionID)
	if err != nil {
		recordExecutionPreparationFailure(ctx, preparationStart, "assemble_options")
		return nil, req, meta, nil, nil, nil, false, false, nil, fmt.Errorf("resume approval: %w", err)
	}
	streamCtx, options, err = s.assembleOptions(ctx, a, req, meta, executionID)
	if err != nil {
		s.recordSystemAssistantRequest(a, "unknown", "error")
		recordExecutionPreparationFailure(ctx, preparationStart, "assemble_options")
		return nil, req, meta, nil, nil, nil, false, false, nil, fmt.Errorf("assemble options: %w", err)
	}
	if resuming {
		var resumeOpts []ExecutionOption
		resumeOpts, consumed, err = s.buildApprovalResumeOptions(ctx, meta.TenantID, a, entries)
		if err != nil {
			recordExecutionPreparationFailure(ctx, preparationStart, "assemble_options")
			return nil, req, meta, nil, nil, nil, false, false, nil, fmt.Errorf("resume approval options: %w", err)
		}
		// 纯终态批次：finalizeReActCheckpoint 按普通执行收尾写终态，finishApprovalResume
		// 走 finishTerminalApprovalResume（不回滚不二次 Terminate）。
		terminal = allTerminal(entries)
		options = append(options, resumeOpts...)
	}
	// 用户消息即时持久化:首跑(meta.ExecutionID 为空)在 Execute 开头落库;
	// 续跑/重连(meta.ExecutionID 非空,如审批批准自动续跑、断线恢复)跳过,
	// 避免同一 query 在恢复执行时重复入库。
	options = append(options, WithExecutionID(executionID), WithSkipUserMessageSave(meta.ExecutionID != ""))
	cfg = &ExecutionConfig{}
	cfg.ApplyOptions(options)
	return a, req, meta, streamCtx, options, cfg, resuming, terminal, consumed, nil
}

// logAgentExecutionDebug 记录执行前 Debug 日志（含会话维度，供恢复排查）。

func (s *AgentService) logAgentExecutionDebug(operation, agentID string, meta ExecMeta, req ExecRequest) {
	s.deps.Logger.Debug(operation,
		zap.String("agent_id", agentID),
		zap.String("trace_id", meta.TraceID),
		zap.String("tenant_id", meta.TenantID),
		zap.String("user_id", req.UserID),
		zap.String("conversation_id", req.ConversationID),
	)
}

// recordSystemAssistantExecution 记录系统助手执行结果指标（fail-open 侧通道）。

func (s *AgentService) recordSystemAssistantExecution(cfg *ExecutionConfig, result *AgentResult, err error) {
	if s.deps.Metrics == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	} else if hasFailedAssistantArtifact(result) {
		outcome = "evidence_error"
	}
	s.deps.Metrics.IncSystemAssistantRequest(cfg.AssistantRoleClass, "", outcome)
}

// logAgentExecution 统一记录执行结果日志（错误 ERROR / 成功 INFO）。

func (s *AgentService) logAgentExecution(operation, agentID string, meta ExecMeta, req ExecRequest, durationMs int, err error) {
	if err != nil {
		s.deps.Logger.Error(operation,
			zap.String("agent_id", agentID),
			zap.String("trace_id", meta.TraceID),
			zap.String("tenant_id", meta.TenantID),
			zap.String("user_id", req.UserID),
			zap.Int("duration_ms", durationMs),
			zap.Error(err),
		)
		return
	}
	s.deps.Logger.Info(operation,
		zap.String("agent_id", agentID),
		zap.String("trace_id", meta.TraceID),
		zap.String("tenant_id", meta.TenantID),
		zap.String("user_id", req.UserID),
		zap.Int("duration_ms", durationMs),
	)
}

// bufferMemoryTurn feeds one turn into the async memory-extraction buffer.
// The answer is already delivered, so a buffering failure is a degradable
// side channel and must never fail the response — but the error is handled
// explicitly and logged instead of being swallowed (no `_ =`).

func (s *AgentService) bufferMemoryTurn(ctx context.Context, meta ExecMeta, req ExecRequest, agentID, scope, role, content string) {
	if s.deps.MemoryBuffer == nil {
		return
	}
	if err := s.deps.MemoryBuffer(ctx, meta.TenantID, req.UserID, agentID, req.ConversationID, scope, role, content); err != nil {
		s.deps.Logger.Warn("agent.memory_buffer_failed",
			zap.String("tenant_id", meta.TenantID),
			zap.String("conversation_id", req.ConversationID),
			zap.String("role", role),
			zap.Error(err))
	}
}

// enqueueTrajectoryReflection 任务结束时把工具调用摘要异步入队轨迹反思
// （与 fact 提取并列的链路 B）。原始 tool steps 不进入记忆：只传截断/脱敏
// 后的参数摘要与错误指纹。失败 fail-open 显式降级，不阻断已交付响应。

func (s *AgentService) enqueueTrajectoryReflection(
	ctx context.Context,
	meta ExecMeta,
	req ExecRequest,
	agentID, scope, executionID string,
	result *domain.AgentResult,
) {
	if s.deps.TrajectoryReflection == nil || result == nil || executionID == "" {
		return
	}
	if len(result.ToolCalls) == 0 {
		return
	}
	calls := make([]port.TrajectoryToolCallVO, 0, len(result.ToolCalls))
	for _, tc := range result.ToolCalls {
		status := domain.ToolTraceStatusSuccess
		var errMsg string
		if tc.Error != nil {
			status = domain.ToolTraceStatusError
			errMsg = tc.Error.Error()
		}
		var argsPreview string
		if tc.Input != nil {
			argsPreview = observability.SafeTracePayload(tc.Input, constants.MemoryReflectionArgsSummaryMaxRunes).Preview
		}
		calls = append(calls, port.TrajectoryToolCallVO{
			ToolName:    tc.ToolName,
			ArgsSummary: argsPreview,
			Status:      status,
			ErrorMsg:    errMsg,
			DurationMS:  tc.Duration.Milliseconds(),
		})
	}
	explicit := containsRememberKeyword(req.Query)
	if err := s.deps.TrajectoryReflection(
		ctx,
		meta.TenantID, req.UserID, agentID, req.ConversationID, scope, executionID,
		req.Query, result.Output, result.TerminatedBy, calls, explicit,
	); err != nil {
		s.deps.Logger.Warn("agent.trajectory_reflection_failed",
			zap.String("tenant_id", meta.TenantID),
			zap.String("conversation_id", req.ConversationID),
			zap.String("execution_id", executionID),
			zap.Error(err))
	}
}

// containsRememberKeyword 检测用户显式"记住"指令，作为反思触发 gate 的
// 显式档位（关键词常量集中管理）。

func containsRememberKeyword(query string) bool {
	for _, kw := range constants.MemoryExplicitRememberKeywords {
		if strings.Contains(query, kw) {
			return true
		}
	}
	return false
}

func (s *AgentService) recordSystemAssistantRequest(a Agent, roleClass, outcome string) {
	if a == nil || s.deps.Metrics == nil {
		return
	}
	s.deps.Metrics.IncSystemAssistantRequest(roleClass, "", outcome)
}

func hasFailedAssistantArtifact(result *AgentResult) bool {
	if result == nil {
		return false
	}
	for _, artifact := range result.AssistantToolArtifacts {
		if artifact.Outcome != "success" {
			return true
		}
	}
	return false
}

func boundedAssistantRoleClass(role string) string {
	switch role {
	case "member", "admin", "owner":
		return role
	default:
		return "unknown"
	}
}

func boundedAssistantOutcome(outcome string) string {
	switch outcome {
	case "success", "gap", "error", "evidence_error", "matched":
		return outcome
	default:
		return "unknown"
	}
}

// ListExecutions paginates the per-tenant execution history.

func (s *AgentService) ListExecutions(
	ctx context.Context, tenantID, userID string, page, pageSize int,
) ([]ExecutionRowDTO, int64, error) {
	if s.deps.EvidenceProvider == nil {
		return nil, 0, domain.ErrEvidenceUnavailable
	}
	records, total, err := s.deps.EvidenceProvider.ListExecutions(
		ctx, tenantID, ListOptions{Page: page, PageSize: pageSize, UserID: userID},
	)
	if err != nil {
		return nil, 0, err
	}
	out := make([]ExecutionRowDTO, 0, len(records))
	for _, r := range records {
		out = append(out, ExecutionRowDTO{
			ID:            r.ID,
			TraceID:       r.TraceID,
			AgentID:       r.AgentID,
			AgentName:     r.AgentName,
			UserID:        r.UserID,
			Status:        r.Status,
			InputPreview:  r.InputPreview,
			OutputPreview: r.OutputPreview,
			ErrorMessage:  r.ErrorMessage,
			TotalTokens:   r.TotalTokens,
			DurationMs:    r.DurationMs,
			CreatedAt:     r.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, total, nil
}

func (s *AgentService) PauseExecution(ctx context.Context, tenantID, executionID string) error {
	if s.deps.CheckpointStore == nil {
		return fmt.Errorf("pause execution: checkpoint store not configured")
	}
	return s.deps.CheckpointStore.UpdateStatus(ctx, tenantID, executionID, "paused")
}

// ResumeExecution restarts a paused execution from its last checkpoint.
// The executionID must refer to a paused checkpoint.

func (s *AgentService) ResumeExecution(ctx context.Context, agentID string, req ExecRequest, meta ExecMeta, executionID string) (*AgentResult, int, error) {
	if s.deps.CheckpointStore != nil {
		if err := s.deps.CheckpointStore.UpdateStatus(ctx, meta.TenantID, executionID, "running"); err != nil {
			return nil, 0, fmt.Errorf("resume execution: %w", err)
		}
	}
	meta.ExecutionID = executionID
	return s.Execute(ctx, agentID, req, meta)
}

// ExecuteSkillScenario 是普通生产路径的 skill 场景评测：经 Registry 读 agent 当前
// 生产配置，activations 显式注入固定 skill revision（与 registry 漂移无关）。
func (s *AgentService) ExecuteSkillScenario(ctx context.Context, agentID string, req ExecRequest, meta ExecMeta, activations []port.SkillActivation) (*AgentResult, int, error) {
	a, ok, err := s.deps.Registry.Get(ctx, agentID)
	if err != nil {
		return nil, 0, fmt.Errorf("execute skill scenario: get agent: %w", err)
	}
	if !ok {
		return nil, 0, ErrNotFound
	}
	return s.executeSkillScenarioAgent(ctx, a, req, meta, activations)
}

// ExecuteSkillScenarioRevision 是评测 skill 场景的 revision 变体（D7）：用锁定
// 的承载 agent revision 构建（buildRevisionAgent），不读 Registry 当前生产配置，
// 可重放。activations 仍显式注入固定 skill revision（与 ExecuteSkillScenario 同
// 语义），revision.Bindings 承载 skill/mcp/knowledge 绑定。
func (s *AgentService) ExecuteSkillScenarioRevision(ctx context.Context, revision domain.AgentRevision, req ExecRequest, meta ExecMeta, activations []port.SkillActivation) (*AgentResult, int, error) {
	if err := revision.Validate(); err != nil {
		return nil, 0, fmt.Errorf("execute skill scenario revision: validate: %w", err)
	}
	a, err := s.buildRevisionAgent(revision)
	if err != nil {
		return nil, 0, fmt.Errorf("execute skill scenario revision: %w", err)
	}
	return s.executeSkillScenarioAgent(ctx, a, req, meta, activations)
}

// executeSkillScenarioAgent 是 skill 场景评测的共享执行体：挂载 metrics/ledger、
// 组装执行选项（固定激活技能目录）并执行。Registry 生产路径与 revision 锁定
// 路径共用，保证两种入口执行语义一致。
func (s *AgentService) executeSkillScenarioAgent(ctx context.Context, a Agent, req ExecRequest, meta ExecMeta, activations []port.SkillActivation) (*AgentResult, int, error) {
	if base, ok := a.(*BaseAgent); ok {
		if s.deps.Metrics != nil {
			a = base.WithMetrics(s.deps.Metrics)
		}
		if s.deps.Ledger != nil {
			a = base.WithLedger(s.deps.Ledger)
		}
	}
	executionID := uuid.Must(uuid.NewV7()).String()
	_, options, err := s.assembleOptions(ctx, a, req, meta, executionID)
	if err != nil {
		return nil, 0, fmt.Errorf("execute skill scenario: assemble options: %w", err)
	}
	options = append(options,
		WithExecutionID(executionID),
		WithSkillCatalog(catalogFromActivations(activations)),
		WithActiveSkills(activations),
	)
	start := time.Now()
	result, err := a.Execute(context.WithoutCancel(ctx), req.Query, options...)
	duration := int(time.Since(start).Milliseconds())
	return result, duration, err
}

// catalogFromActivations 从 scenario 固定激活列表构建 run 级 SkillCatalog。
// 空 SkillID 跳过，重复 SkillID 后者覆盖；返回 map 供 WithSkillCatalog 使用。

func catalogFromActivations(activations []port.SkillActivation) map[string]port.SkillActivation {
	catalog := make(map[string]port.SkillActivation, len(activations))
	for _, activation := range activations {
		if activation.SkillID == "" {
			continue
		}
		catalog[activation.SkillID] = activation
	}
	return catalog
}

// assembleOptions builds the ExecutionOption slice and resolves the
// per-tenant CapabilityGateway. When meta.Stream is true, the returned
// ctx carries the per-tenant LLM completer for streaming inner calls.
