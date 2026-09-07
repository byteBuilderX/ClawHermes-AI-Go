// Package application provides the core agent system.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/internal/agent/application/factcheck"
	agentgraph "github.com/byteBuilderX/stratum/internal/agent/application/graph"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	jschema "github.com/byteBuilderX/stratum/pkg/jsonschema"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"github.com/byteBuilderX/stratum/pkg/textutil"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
)

// Domain type aliases — canonical definitions live in
// internal/agent/domain. Aliases preserve source-compat for the dozens
// of call-sites still spelled `application.AgentType`, etc.
type (
	AgentType       = domain.AgentType
	AgentCapability = domain.AgentCapability
	AgentConfig     = domain.AgentConfig
	AgentTraceEvent = domain.AgentTraceEvent
	Message         = domain.Message
	Thought         = domain.Thought
	ToolCall        = domain.ToolCall
	ToolObservation = domain.ToolObservation
	AgentResult     = domain.AgentResult
	AgentState      = domain.AgentState
)

// ExecutionConfig holds parameters for a single agent execution. It lives
// in the application layer because it references port.ToolDefinition and
// function types that depend on cross-context ports.
type ExecutionConfig struct {
	MaxSteps    int
	Timeout     time.Duration
	Temperature float32
	// ReasoningEffort 是本次执行的采样强度档位:""(unset)|low|medium|high。
	// 空串 = unset(与 Temperature 0 同构):网关/provider 默认生效。由
	// registry 参数 agent.reasoning_effort 经 WithReasoningEffort 注入。
	ReasoningEffort string
	MaxTokens       int
	// MaxContextTokens 是本次执行解析后的上下文窗口预算（0 = 未注入，
	// 回退到 agent 配置显式值）。执行时由 AgentService 两阶段解析后注入。
	MaxContextTokens int
	// OutputReserve 是主模型输出预留（账本 usable 的扣减项）。
	// 0 = 未注入，由调用链自动解析（显式 max_tokens > vendor maxOut > 常量）。
	OutputReserve int
	// WindowSource 记录本次执行窗口解析来源（window_source trace 用）。
	WindowSource string
	// CompactionRecentGroups overrides in-loop compaction recent groups.
	// 0 = auto-derive from MaxContextTokens.
	CompactionRecentGroups int
	// CompactionCooldownSec overrides the in-loop compaction cooldown window
	// (seconds). 0 = default constant (constants.DefaultCompactionCooldown).
	CompactionCooldownSec int
	// MaxTokensPerExecution 是本次执行的累计 LLM token 预算（Spec 第 3 节，
	// 与 Ledger 已记账 TotalTokens 对齐）。0 = 不设限。
	MaxTokensPerExecution int
	EnableTools           bool
	AvailableTools        []string
	Stream                bool
	TokenCallback         func(string)
	// DelegateEventCallback 在委托子 agent 进入/结束时回调（SSE delegate_status
	// 帧出口）。仅流式路径由 ExecuteStream 注入；nil = 不推送委托进度。
	DelegateEventCallback func(agentgraph.DelegateEvent)
	TenantID              string
	TraceID               string
	ExecutionID           string
	// SkipUserMessageSave 为真时跳过用户消息的即时持久化(首次发送在 Execute
	// 开始时已落库;续跑/断线重连复用同一 query 不再重复入库)。由 AgentService
	// prepareAgentExecution 依据 meta.ExecutionID 是否非空统一注入。
	SkipUserMessageSave bool
	// ApprovalResumeIDs 是审批续跑注入的已批准审批 ID 集合（同一 executionID 的一
	// 批多审批）。非空时 resumeFromCheckpoint 把 waiting_approval checkpoint 视为
	// 可恢复（checkpoint 无工具快照，从 chat 历史 + 本轮 query 全量重跑，语义与
	// ResumeToolApproval 一致）；空保持旧语义——waiting_approval 不恢复，重跑路径
	// 由 guard 对匹配工具重新发起审批。
	ApprovalResumeIDs []string
	// ApprovalResumePayloads 是审批续跑注入的已批准载荷集合（C2a）：非空时
	// executeReAct 在 buildReActInitState 后据此从批准参数合成一条 assistant 工具
	// 调用消息（P1，含 N 条 tool_call，覆盖非终态与终态条目）并置 SkipNextLLM，使
	// 已批准/已终态的工具调用直接进入工具节点，不再经 LLM 重新生成参数（修复审批
	// 续跑无限循环）。
	ApprovalResumePayloads []ToolApprovalPayload
	RAGSearchFn            func(ctx context.Context, workspaces []string, query string, topK int, viewerID string) (string, error)
	// RAGSearchFnWithEvidence is the evidence-capable knowledge search hook
	// (port.RAGSearchEvidenceProvider). When set, the knowledge tool prefers
	// it over RAGSearchFn so tool observations carry retrieval provenance;
	// the content contract is identical.
	RAGSearchFnWithEvidence func(ctx context.Context, workspaces []string, query string, topK int, viewerID string) (port.RAGSearchEvidence, error)
	// CaptureParameters gates recording of the effective parameter values
	// (stratum.params.* attributes) on the execution span. The parameter
	// fingerprint stratum.params.sha256 is always recorded; only the raw
	// values are privacy-gated by the platform trace.capture_parameters
	// toggle.
	CaptureParameters bool
	// SystemPromptVersion is the content fingerprint of the system prompt
	// actually applied to this execution; 0 = unset leaves it empty.
	SystemPromptVersion string
	// GlobalSystemSuffix 是本次执行解析后的全局系统提示词（平台参数
	// agent.system_prompt，执行时解析，未配置 fail-closed）。
	GlobalSystemSuffix string
	ExtraTools         []port.ToolDefinition
	SkillCatalog       map[string]port.SkillActivation
	ToolExecutionFn    port.ToolExecutionFn
	// PrecheckApprovals 是同一轮 LLM 工具调用执行前的批量审批预检钩子：任一调用
	// 需审批时一次性创建全部审批并整轮暂停。nil = 不预检（退回逐个 guard 路径）。
	PrecheckApprovals func(ctx context.Context, tools []port.ToolDefinition, calls []port.ToolCall) ([]port.ToolApprovalRequiredError, error)
	Actives           []port.SkillActivation
	TracePayloadStore port.TracePayloadStore
	ConversationID    string
	UserID            string
	HistoryWindow     int
	EvolutionTrace    EvolutionTraceMetadata
	// AssistantRoleClass 是本次执行解析的成员角色（admin/owner/member），供
	// 8 个通用内置运维工具在装配闭包与执行 metrics 中 fail-closed 门禁使用。
	AssistantRoleClass        string
	OfficialDocsSearchFn      func(context.Context, string) ([]domain.Citation, error)
	DiagnosticFn              func(context.Context, []domain.DiagnosticArea) (domain.DiagnosticEvidence, error)
	ProposalCreateFn          func(context.Context, map[string]any) (domain.ResourceChangeProposalArtifact, error)
	ResourceChangeApplyFn     func(context.Context, map[string]any) (domain.ApplyResult, error)
	ListModelsFn              func(context.Context) (map[string]any, error)
	UpdateSystemModelFn       func(_ context.Context, model, agentID string) (map[string]any, error)
	ListAgentsFn              func(context.Context) (map[string]any, error)
	ListMCPServersFn          func(context.Context) (map[string]any, error)
	InternalToolResultGuardFn func(any) (port.GuardedToolResult, error)
	// FactCheck 是幻觉校验配置（nil/Enabled=false = 关闭，fail-closed）。judge
	// 与 TopK/MaxClaims 由 wiring 装配；EvidenceFn 留空，collectGraphResult 执行
	// 时用 RAGSearchFnWithEvidence 填充（per-execution 已带 tenant 权限上下文）。
	FactCheck *factcheck.Settings
	// ---- stratum_delegate 委托参数（Step 6）----
	// 三个字段由 snapshotExecutionConfig 从 agent 配置回填：数值 0=unset（回落
	// 全局默认）；DelegateEnabled 是 bool 无 unset 哨兵，无条件拷贝 agent 配置
	// （执行级不可覆盖，忠实"复用当前 agent 配置"）。
	DelegateEnabled         bool
	DelegateMaxDepth        int
	DelegateDefaultMaxSteps int
}

// EvolutionTraceMetadata attributes an execution to evaluation and rollout evidence.
type EvolutionTraceMetadata struct {
	Evaluation            bool
	SecurityViolation     bool
	ExperimentID          string
	Variant               string
	ResourceManifest      map[string]string
	ExperimentAssignments map[string]ExperimentAssignment
}

// ExperimentAssignment identifies the rollout selected for one versioned resource.
type ExperimentAssignment struct {
	ExperimentID string `json:"experiment_id"`
	Variant      string `json:"variant"`
}

const (
	ReActAgent       = domain.ReActAgent
	CoTAgent         = domain.CoTAgent
	PlanningAgent    = domain.PlanningAgent
	ToolCallingAgent = domain.ToolCallingAgent
	RAGAgent         = domain.RAGAgent
	SwarmAgent       = domain.SwarmAgent
)

// Agent defines the interface for all agent types
type Agent interface {
	GetConfig() *AgentConfig
	Execute(ctx context.Context, input string, options ...ExecutionOption) (*AgentResult, error)
	Reset()
	GetMemory() []Message
}

// BaseAgent provides common functionality for all agent implementations
type BaseAgent struct {
	*AgentConfig
	Logger           *zap.Logger
	metrics          observability.MetricsProvider
	Ledger           agentgraph.TokenRecorder
	State            AgentState
	Memory           []Message
	mu               sync.Mutex
	CapGateway       port.CapabilityGateway
	ChatStore        ChatStore
	CheckpointStore  CheckpointStore
	TaskStore        port.TaskRepo
	MemoryInjector   port.MemoryInjector
	HistoryCompactor port.HistoryCompactor
	CompactionStore  port.CompactionStore
	RecallMemoryFn   port.RecallMemoryFn
	// GlobalSystemSuffix 是测试/无 resolver 直构路径的全局系统提示词回退；
	// 生产链路经 PlatformPromptResolver 解析 agent.system_prompt（fail-closed）。
	GlobalSystemSuffix string
	// PlatformPromptResolver 解析平台级提示词参数（agent.system_prompt /
	// agent.compaction_prompt），运行时热更新。
	PlatformPromptResolver port.PlatformPromptResolver
}

// NewBaseAgent creates a new base agent
func NewBaseAgent(config *AgentConfig, logger *zap.Logger) *BaseAgent {
	return &BaseAgent{
		AgentConfig: config,
		Logger:      logger,
		metrics:     observability.NoopMetrics{},
		Ledger:      agentgraph.NoopTokenRecorder{},
		State:       AgentState{},
		Memory:      []Message{},
		mu:          sync.Mutex{},
	}
}

// WithMetrics injects a MetricsProvider. Must be called before the agent is shared across goroutines.
func (a *BaseAgent) WithMetrics(m observability.MetricsProvider) *BaseAgent {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.metrics = m
	return a
}

// WithLedger injects a TokenRecorder for LLM token/cost accounting. Must be
// called before the agent is shared across goroutines.
func (a *BaseAgent) WithLedger(l agentgraph.TokenRecorder) *BaseAgent {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Ledger = l
	return a
}

func (a *BaseAgent) SetCapGateway(gw port.CapabilityGateway) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.CapGateway = gw
}

func (a *BaseAgent) SetHistoryCompactor(compactor port.HistoryCompactor) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.HistoryCompactor = compactor
}

// SetCompactionStore 注入跨轮复用压缩摘要存储。nil 时组装侧保持无复用行为
// （与旧 BuildContextMessagesWithCompaction 逐字节一致）。
func (a *BaseAgent) SetCompactionStore(store port.CompactionStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.CompactionStore = store
}

// assembleContextMessagesReuse 构造组装侧上下文消息。注入 CompactionStore 后
// 走游标折叠 + 增量压缩 + 回写；未注入时 reuse 为 nil，行为与旧
// BuildContextMessagesWithCompaction 一致。覆盖读取/回写失败已在 build 内部
// fail closed（降级为无复用压缩或截断兜底），主流程不阻断，仅 WARN 暴露——
// 禁止伪成功。
func (a *BaseAgent) assembleContextMessagesReuse(ctx context.Context, ec agentExecContext, maxTokens int) []port.LLMMessage {
	var reuse *CompactionReuse
	if ec.compactionStore != nil {
		reuse = &CompactionReuse{
			Store:          ec.compactionStore,
			TenantID:       ec.cfg.TenantID,
			ConversationID: ec.cfg.ConversationID,
			RecentRounds:   ec.recentRounds,
		}
	}
	initMessages, err := BuildContextMessagesWithCompactionReuse(
		ctx, ec.systemPrompt, ec.globalSuffix, ec.memCtx, ec.history, ec.input, maxTokens, ec.cfg.HistoryWindow,
		ec.cfg.OutputReserve, 0, ec.historyCompactor, reuse,
	)
	if err != nil {
		a.Logger.Warn("agent.assemble_compaction_failed",
			zap.String("agent_id", a.ID),
			zap.String("conversation_id", ec.cfg.ConversationID),
			zap.Error(err),
		)
	}
	// 对账轨开启时，把"声称带引用"规则锚入首条 system 消息之后（头部 anchor
	// 区压缩永不逐出）：所有步骤可见，无工具调用直接出答案也受约束。
	if ec.cfg.FactCheck != nil && ec.cfg.FactCheck.CitationVerify {
		initMessages = injectSystemInstruction(initMessages, constants.AgentCitationReferenceInstruction)
	}
	return initMessages
}

// injectSystemInstruction 将一条 system 指令作为块锚入首条 system 消息之后
// （头部 anchor 区压缩不逐出）；无 system 消息时置于最前。语义与 graph 包
// insertSystemBlockAfterFirstSystem 一致（不同包不可引用其未导出函数）。
func injectSystemInstruction(messages []port.LLMMessage, content string) []port.LLMMessage {
	instruction := port.LLMMessage{Role: "system", Content: content}
	if len(messages) > 0 && messages[0].Role == "system" {
		out := make([]port.LLMMessage, 0, len(messages)+1)
		out = append(out, messages[0], instruction)
		return append(out, messages[1:]...)
	}
	return append([]port.LLMMessage{instruction}, messages...)
}

// SetChatStore sets the chat store for conversation history persistence (void, for interface assertion).
func (a *BaseAgent) SetChatStore(cs ChatStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ChatStore = cs
}

// WithChatStore sets the chat store for conversation history persistence.
func (a *BaseAgent) WithChatStore(cs ChatStore) *BaseAgent {
	a.SetChatStore(cs)
	return a
}

func (a *BaseAgent) SetCheckpointStore(store CheckpointStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.CheckpointStore = store
}

func (a *BaseAgent) WithCheckpointStore(store CheckpointStore) *BaseAgent {
	a.SetCheckpointStore(store)
	return a
}

func (a *BaseAgent) SetTaskStore(store port.TaskRepo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.TaskStore = store
}

func (a *BaseAgent) WithTaskStore(store port.TaskRepo) *BaseAgent {
	a.SetTaskStore(store)
	return a
}

// GetConfig implements Agent interface
func (a *BaseAgent) GetConfig() *AgentConfig {
	return a.AgentConfig
}

// Reset implements Agent interface
func (a *BaseAgent) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.State = AgentState{}
	a.Memory = []Message{}
}

// GetMemory returns the agent's conversation memory
func (a *BaseAgent) GetMemory() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Memory
}

// AddToMemory adds a message to the in-process memory slice.
// Long-term indexing via MemoryManager is handled asynchronously in Execute().
func (a *BaseAgent) AddToMemory(msg Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	msg.Timestamp = time.Now()
	a.Memory = append(a.Memory, msg)
	if len(a.Memory) > 100 {
		a.Memory = a.Memory[len(a.Memory)-100:]
	}
}

// agentExecContext bundles immutable execution-scope values extracted under lock.
type agentExecContext struct {
	cfg              *ExecutionConfig
	tracer           oteltrace.Tracer
	agentID          string
	agentName        string
	systemPrompt     string
	globalSuffix     string
	llmModel         string
	capGW            port.CapabilityGateway
	historyCompactor port.HistoryCompactor
	compactionStore  port.CompactionStore
	recentRounds     int
	maxContextTokens int
	memoryScope      string
	workspaceNames   []string
	workspaceDescs   []string
	memCtx           string
	history          []*ChatMessage
	input            string
}

// Execute implements the Agent interface - base implementation with ReAct pattern
// agentExecSnapshot is the immutable view of the mutable agent configuration
// taken under lock at execution start, released before the long LLM call.
type agentExecSnapshot struct {
	agentID            string
	agentName          string
	agentType          domain.AgentType
	systemPrompt       string
	globalSystemSuffix string
	llmModel           string
	capGW              port.CapabilityGateway
	historyCompactor   port.HistoryCompactor
	compactionStore    port.CompactionStore
	chatStore          ChatStore
	metrics            observability.MetricsProvider
	workspaceNames     []string
	workspaceDescs     []string
	maxContextTokens   int
	memoryScope        string
}

// snapshotExecutionConfig copies the mutable configuration under lock and
// backfills unset execution options: explicit options win, agent-config values
// fill in fields the caller left at zero so the revision → execution path
// carries temperature / max_tokens / compaction through.
func (a *BaseAgent) snapshotExecutionConfig(cfg *ExecutionConfig) agentExecSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = a.MaxIterations
	}
	// cfg.Timeout stays 0 (no deadline) unless the client explicitly passes a
	// timeout option. Step limits + per-operation timeouts bound execution;
	// a wall-clock deadline is optional and client-controlled.
	if cfg.Temperature == 0 {
		cfg.Temperature = a.Temperature
	}
	// ReasoningEffort 用 "" 作 unset 哨兵:空串回填 agent 配置,非空 option 优先。
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = a.ReasoningEffort
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = a.MaxTokens
	}
	if cfg.MaxTokensPerExecution == 0 {
		cfg.MaxTokensPerExecution = a.MaxTokensPerExecution
	}
	// 执行时解析的窗口（WithMaxContextTokens 注入）优先；
	// 未注入时回退 agent 配置显式值（revision/直接执行等路径）。
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = a.MaxContextTokens
	}
	// delegate 数值参数 0=unset 回填 agent 配置；bool DelegateEnabled 无哨兵，
	// 无条件拷贝（执行级不可覆盖，见 ExecutionConfig 注释）。
	if cfg.DelegateMaxDepth == 0 {
		cfg.DelegateMaxDepth = a.DelegateMaxDepth
	}
	if cfg.DelegateDefaultMaxSteps == 0 {
		cfg.DelegateDefaultMaxSteps = a.DelegateDefaultMaxSteps
	}
	cfg.DelegateEnabled = a.DelegateEnabled
	globalSuffix := cfg.GlobalSystemSuffix
	if globalSuffix == "" {
		globalSuffix = a.GlobalSystemSuffix
	}
	return agentExecSnapshot{
		agentID:   a.ID,
		agentName: a.Name,
		agentType: domain.ReActAgent,
		// 拆分而非拼接：systemPrompt 在预算组装（FixedHeadCap）内截断，
		// globalSystemSuffix 豁免 head 配额完整注入（事实约束等治理指令
		// 不得因预算紧张被静默丢弃）。
		systemPrompt:       a.SystemPrompt,
		globalSystemSuffix: globalSuffix,
		llmModel:           a.LLMModel,
		capGW:              a.CapGateway,
		historyCompactor:   a.HistoryCompactor,
		compactionStore:    a.CompactionStore,
		chatStore:          a.ChatStore,
		metrics:            a.metrics,
		workspaceNames:     a.KnowledgeWorkspaceNames,
		workspaceDescs:     a.KnowledgeWorkspaceDescriptions,
		maxContextTokens:   cfg.MaxContextTokens,
		memoryScope:        a.MemoryScope,
	}
}

// globalSystemSuffix appends the global suffix to the agent system prompt,
// or nothing when unset.
func globalSystemSuffix(suffix string) string {
	if suffix == "" {
		return ""
	}
	return "\n\n" + suffix
}

// resolveGlobalSystemSuffix 解析平台级全局系统提示词 agent.system_prompt。
// 对所有 agent 一视同仁（含内置平台助手）：统一追加全局后缀。
// 生产链路 resolver 恒注入：未配置/空 → fail-closed 错误，禁止静默无后缀执行。
// resolver 为 nil 仅测试直构路径允许，回退 agent 字段（生产为空）。
func (a *BaseAgent) resolveGlobalSystemSuffix(ctx context.Context) (string, error) {
	if a.PlatformPromptResolver == nil {
		return a.GlobalSystemSuffix, nil
	}
	v, ok, err := a.PlatformPromptResolver.ResolvePlatform(ctx, "agent.system_prompt")
	if err != nil {
		return "", fmt.Errorf("agent: resolve global system prompt: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("agent: %w", domain.ErrSystemPromptNotConfigured)
	}
	suffix, ok := v.(string)
	if !ok || strings.TrimSpace(suffix) == "" {
		return "", fmt.Errorf("agent: %w", domain.ErrSystemPromptNotConfigured)
	}
	return suffix, nil
}

// platformExecutionParams 是执行时从平台参数解析的全局后缀与压缩行为值。
type platformExecutionParams struct {
	globalSuffix string
	recentGroups int
	cooldownSec  int
}

// resolvePlatformExecutionParams 汇总执行时平台参数：全局系统提示词
// （fail-closed）+ 压缩最近轮数/冷却（0=unset，消费端按文档默认处理）。
func (a *BaseAgent) resolvePlatformExecutionParams(ctx context.Context) (platformExecutionParams, error) {
	suffix, err := a.resolveGlobalSystemSuffix(ctx)
	if err != nil {
		return platformExecutionParams{}, err
	}
	return platformExecutionParams{
		globalSuffix: suffix,
		recentGroups: a.resolvePlatformInt(ctx, "agent.compaction_recent_groups"),
		cooldownSec:  a.resolvePlatformInt(ctx, "agent.compaction_cooldown_sec"),
	}, nil
}

// applyPlatformExecutionParams 把解析后的平台值写入执行配置：全局后缀恒写入；
// recent groups/cooldown 仅填空（option 显式值优先，0=unset 与 snapshot
// backfill 语义一致）。
func applyPlatformExecutionParams(cfg *ExecutionConfig, params platformExecutionParams) {
	cfg.GlobalSystemSuffix = params.globalSuffix
	if cfg.CompactionRecentGroups == 0 {
		cfg.CompactionRecentGroups = params.recentGroups
	}
	if cfg.CompactionCooldownSec == 0 {
		cfg.CompactionCooldownSec = params.cooldownSec
	}
}

// resolvePlatformInt 解析平台级整数参数；未配置/解析失败回退 0（0=unset 语义，
// 优化输入不是执行门禁，不回退代码常量）。
func (a *BaseAgent) resolvePlatformInt(ctx context.Context, key string) int {
	if a.PlatformPromptResolver == nil {
		return 0
	}
	v, ok, err := a.PlatformPromptResolver.ResolvePlatform(ctx, key)
	if err != nil || !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// effectiveSystemPromptVersion returns the caller-provided version when set,
// otherwise a content fingerprint of the applied system prompt (base prompt +
// global suffix)。
func effectiveSystemPromptVersion(version, systemPrompt, globalSuffix string) string {
	if version != "" || systemPrompt == "" {
		return version
	}
	return contentVersion(systemPrompt + globalSystemSuffix(globalSuffix))
}

// contentVersion returns a stable short fingerprint (first 16 hex chars of
// the SHA-256 digest) of a text blob. Used as the prompt version key so
// trace consumers can group executions by prompt without storing full
// prompt text.
func contentVersion(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

// tunableConfigVersion fingerprints the effective tunable parameter snapshot
// with the same contentVersion used for stratum.params.sha256, so the ReAct
// promptVersions["config"] and the execution-span params hash always agree.
// One source of truth for the config fingerprint — no third hash scheme
// (P1 trace-fingerprint upgrade, follows contentVersion contract).
func tunableConfigVersion(cfg *ExecutionConfig, maxContextTokens int) string {
	paramsJSON, _ := json.Marshal(tunableSnapshot(cfg, maxContextTokens))
	return contentVersion(string(paramsJSON))
}

// evalSpanAttrs 把评测观测信号投影为 span 属性（spec §12 埋点），双挂 execSpan 与
// requestSpan：评测数据以 opik.metadata.stratum.eval_* 属性挂回原 trace，不复制证据。
// ruleSignals 来自 ctx 累积器；behavior 由执行结果推导。eval_emitted=true 标记该执行
// 已进入评测采集面（区分「未采集」与「采集但无信号」）。
func evalSpanAttrs(result *AgentResult, ruleSignals []port.RuleSignalPayload) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.Int64("opik.metadata.stratum.eval_rule_hits", int64(len(ruleSignals))),
		attribute.Bool("opik.metadata.stratum.eval_emitted", true),
	}
	if b := behaviorFromResult(result); b != nil {
		attrs = append(attrs,
			attribute.Bool("opik.metadata.stratum.eval_behavior_retry", b.Retry),
			attribute.Bool("opik.metadata.stratum.eval_behavior_escalation", b.Escalation),
			attribute.Bool("opik.metadata.stratum.eval_behavior_abandonment", b.Abandonment),
		)
	}
	return attrs
}

func (a *BaseAgent) Execute(ctx context.Context, input string, options ...ExecutionOption) (*AgentResult, error) {
	startTime := time.Now()

	cfg := &ExecutionConfig{}
	cfg.ApplyOptions(options)

	// 执行时平台参数解析：全局系统提示词（未配置 fail-closed）+ 压缩最近轮数/
	// 冷却（0=unset 回退消费端默认）。所有 agent（含内置助手）同源。
	params, err := a.resolvePlatformExecutionParams(ctx)
	if err != nil {
		return nil, err
	}
	applyPlatformExecutionParams(cfg, params)

	// Snapshot mutable fields under lock, then release before the long LLM call.
	snap := a.snapshotExecutionConfig(cfg)

	// Prompt version fingerprint: derived from the actual system prompt text
	// applied to this execution (DB-loaded AgentConfig.SystemPrompt +
	// global suffix). The production path does not go through the prompt
	// registry, so the version is a content hash, not a registry revision.
	// fingerprint 覆盖拼接后的完整 prompt：全局后缀是实际发送文本的一部分。
	cfg.SystemPromptVersion = effectiveSystemPromptVersion(cfg.SystemPromptVersion, snap.systemPrompt, snap.globalSystemSuffix)

	tracer := otel.Tracer("stratum/agent")
	executionAttrs := agentExecutionAttributes(snap.agentID, snap.agentName, snap.agentType, *cfg, snap.maxContextTokens)
	requestSpan := oteltrace.SpanFromContext(ctx)
	// requestSpan 属性注入保留，供被保留的 HTTP trace 关联 agent 元数据。
	requestSpan.SetAttributes(executionAttrs...)
	// agent.execute 恒为独立根 span（WithNewRoot），理由见 startAgentExecuteSpan。
	ctx, execSpan := startAgentExecuteSpan(ctx, tracer, executionAttrs)
	defer execSpan.End()

	memCtx, memErr := a.injectMemoryContext(ctx, tracer, cfg, snap.agentID, snap.memoryScope, input)

	a.Logger.Info("agent execution started",
		zap.String("agent_id", snap.agentID),
		zap.String("trace_id", cfg.TraceID),
		zap.String("conversation_id", cfg.ConversationID),
		zap.String("type", string(snap.agentType)))

	result := &AgentResult{
		AgentID:  snap.agentID,
		Input:    input,
		Metadata: map[string]interface{}{},
	}

	history, histErr := a.loadConversationHistory(ctx, tracer, snap.chatStore, cfg)

	// 用户消息即时持久化:不等执行结束(审批等待/执行失败时 persistChatMessages
	// 不执行),保证用户发送的消息立即可见、刷新后不丢失。
	a.persistUserMessage(ctx, tracer, snap.chatStore, cfg, input, snap.agentID, snap.memoryScope)

	ec := agentExecContext{
		cfg: cfg, tracer: tracer, agentID: snap.agentID, agentName: snap.agentName,
		systemPrompt: snap.systemPrompt, globalSuffix: snap.globalSystemSuffix, llmModel: snap.llmModel, capGW: snap.capGW,
		historyCompactor: snap.historyCompactor, compactionStore: snap.compactionStore,
		recentRounds: cfg.CompactionRecentGroups, maxContextTokens: snap.maxContextTokens,
		memoryScope: snap.memoryScope, workspaceNames: snap.workspaceNames,
		workspaceDescs: snap.workspaceDescs, memCtx: memCtx, history: history,
		input: input,
	}

	var execErr error
	// Fail closed before any LLM/tool work: memory context and conversation
	// history are part of the execution context, and the execution must not
	// start when either cannot be loaded (see injectMemoryContext and
	// loadConversationHistory).
	switch {
	case memErr != nil:
		execErr = fmt.Errorf("agent: memory context preparation: %w", memErr)
	case histErr != nil:
		execErr = fmt.Errorf("agent: conversation history preparation: %w", histErr)
	default:
		switch snap.agentType {
		case ReActAgent:
			execErr = a.executeReAct(ctx, ec, result)
		case CoTAgent:
			execErr = a.executeCoT(cfg, input, result)
		case PlanningAgent:
			execErr = a.executePlanning(ctx, ec, result)

		case ToolCallingAgent, RAGAgent, SwarmAgent:
			result.Output = fmt.Sprintf("%s agent type not yet implemented", string(snap.agentType))
			execErr = fmt.Errorf("agent type %s not implemented", snap.agentType)

		default:
			result.Output = "Unknown agent type"
			execErr = fmt.Errorf("unknown agent type: %s", snap.agentType)
		}
	}
	result.Artifacts = buildExecutionArtifacts(result.AssistantToolArtifacts, domain.CurrentExecutionArtifactProfileVersion)

	a.persistChatMessages(ctx, tracer, snap.chatStore, cfg, result, snap.agentID, snap.memoryScope, execErr)

	result.Duration = time.Since(startTime)
	a.mu.Lock()
	result.Steps = a.State.StepsTaken
	a.mu.Unlock()

	status := domain.ExecStatusSuccess
	if execErr != nil {
		status = domain.ExecStatusError
	}
	completionAttrs := append([]attribute.KeyValue{
		attribute.String("opik.metadata.stratum.status", status),
		attribute.Int64("opik.metadata.stratum.duration_ms", result.Duration.Milliseconds()),
		attribute.Int64("opik.metadata.stratum.total_tokens", int64(result.TokensUsed)),
		attribute.Float64("opik.metadata.stratum.cost_usd", result.CostUSD),
	}, evalSpanAttrs(result, ruleSignalsFromBlocks(ctx))...)
	execSpan.SetAttributes(completionAttrs...)
	requestSpan.SetAttributes(completionAttrs...)
	snap.metrics.IncAgentExecution(snap.agentID, string(snap.agentType), status)
	snap.metrics.RecordAgentExecutionDuration(snap.agentID, string(snap.agentType), result.Duration.Seconds())
	snap.metrics.RecordAgentStepCount(snap.agentID, string(snap.agentType), result.Steps)

	recordFingerprintAndKPI(snap.metrics, execSpan, requestSpan, snap.agentID, string(snap.agentType), snap.llmModel, snap.systemPrompt+globalSystemSuffix(snap.globalSystemSuffix), cfg, snap.maxContextTokens, result, status, cfg.TenantID)

	return result, execErr
}

// startAgentExecuteSpan 创建 agent.execute 独立根 span（WithNewRoot）：SDK
// 采样器只对无父 root span 调用 ShouldSample（见 pkg/observability
// agentSampler），带专用属性即 100% 采样，collector tail_sampling 也按此 keep。
// HTTP/工作流等上游根 span 不加此属性、保持各自采样率，但 agent 执行本体从
// 任何入口触发都独立成根而恒采样——否则 HTTP 入口的 agent.execute 是 otelgin
// 根的子 span，ParentBased 会按父采样位短路，OTEL_SAMPLING_RATIO=0.1 下 90%
// 被 SDK 丢弃，collector 规则救不回。独立根不破坏业务关联：trace_id 来自请求
// payload（cfg.TraceID），非 OTEL span 链路。
func startAgentExecuteSpan(
	ctx context.Context,
	tracer oteltrace.Tracer,
	executionAttrs []attribute.KeyValue,
) (context.Context, oteltrace.Span) {
	// copy-then-append：避免就地修改调用方底层数组，同时让 gocritic appendAssign
	// 看到 append 结果赋回同一变量（execAttrs := append(executionAttrs, ...) 会被误报）。
	execAttrs := make([]attribute.KeyValue, 0, len(executionAttrs)+1)
	execAttrs = append(execAttrs, executionAttrs...)
	execAttrs = append(execAttrs, attribute.Bool(observability.AgentExecuteAttrKey, true))
	return tracer.Start(ctx, "agent.execute",
		oteltrace.WithNewRoot(),
		oteltrace.WithAttributes(execAttrs...),
	)
}

// recordAgentKPI 打点 agent 任务级 KPI（agent_task_completed / task_duration /
// cost_per_task / conversation_turns）。task_kind 槽当前镜像 agent_type：平台尚无
// 独立 task-kind 维度（IncAgentTaskCompleted 唯一生产调用方就是这里），预留真实
// task-kind（如审批类任务）接入时再分离。
func recordAgentKPI(metrics observability.MetricsProvider, agentID, agentType, status, tenantID string, result *AgentResult) {
	metrics.IncAgentTaskCompleted(agentID, agentType, agentType, status, tenantID)
	metrics.RecordAgentTaskLatency(agentID, agentType, result.Duration.Seconds())
	metrics.RecordAgentCostPerTask(agentID, agentType, result.CostUSD)
	metrics.RecordAgentConversationTurn(agentID, result.Steps)
}

func recordFingerprintAndKPI(
	metrics observability.MetricsProvider,
	execSpan, requestSpan oteltrace.Span,
	agentID, agentType, llmModel, systemPrompt string,
	cfg *ExecutionConfig,
	maxContextTokens int,
	result *AgentResult,
	status, tenantID string,
) {
	recordAgentKPI(metrics, agentID, agentType, status, tenantID, result)
	// 指纹记录实际解析模型与路由链：fallback 降级后 ModelResolved 为实际
	// 成功模型，ModelRoutedVia 为尝试过的模型链；未降级时保持配置模型。
	resolved := llmModel
	if result.ModelResolved != "" {
		resolved = result.ModelResolved
	}
	fp := CaptureFingerprint(resolved, result.ModelRoutedVia, systemPrompt, skillRevisionHashes(cfg.SkillCatalog),
		tunableConfigVersion(cfg, maxContextTokens), tunableSnapshot(cfg, maxContextTokens), 0)
	fpAttrs := fingerprintAttributes(fp)
	execSpan.SetAttributes(fpAttrs...)
	requestSpan.SetAttributes(fpAttrs...)
}

// tunableSnapshot records the effective tunable values applied to this
// execution so the fingerprint attributes attribute runs to their tunables.
func tunableSnapshot(cfg *ExecutionConfig, maxContextTokens int) map[string]any {
	snapshot := map[string]any{
		"temperature":              cfg.Temperature,
		"max_tokens":               cfg.MaxTokens,
		"max_context_tokens":       maxContextTokens,
		"compaction_recent_groups": cfg.CompactionRecentGroups,
	}
	// ReasoningEffort 空串 = unset，不进 fingerprint：避免未设置档位时
	// fingerprint 漂移，保持与 resolver 的 string-unset 语义一致。
	if cfg.ReasoningEffort != "" {
		snapshot["reasoning_effort"] = cfg.ReasoningEffort
	}
	return snapshot
}

// injectMemoryContext builds the memory context injected into the system
// prompt. When a MemoryInjector is configured, memory retrieval is part of the
// execution contract: a failure aborts the execution (fail closed) instead of
// silently running without memory context and producing a different answer.
func (a *BaseAgent) injectMemoryContext(ctx context.Context, tracer oteltrace.Tracer, cfg *ExecutionConfig, agentID, memoryScope, input string) (string, error) {
	if a.MemoryInjector == nil || cfg.ConversationID == "" {
		return "", nil
	}
	ic := port.InjectionContext{
		TenantID: cfg.TenantID, UserID: cfg.UserID, AgentID: agentID,
		ConversationID: cfg.ConversationID, Query: input, Scope: memoryScope,
	}
	memSpanCtx, memSpan := tracer.Start(ctx, "agent.memory_inject")
	memInjectCtx, memInjectCancel := context.WithTimeout(memSpanCtx, constants.AgentMemoryInjectTimeout)
	mctx, memInjectErr := a.MemoryInjector.BuildContext(memInjectCtx, ic)
	memInjectCancel()
	memSpan.End()
	if memInjectErr != nil {
		a.Logger.Error("agent.memory_inject_failed",
			zap.String("agent_id", agentID),
			zap.String("conversation_id", cfg.ConversationID),
			zap.Error(memInjectErr))
		return "", fmt.Errorf("inject memory context: %w", memInjectErr)
	}
	return mctx, nil
}

// loadConversationHistory loads prior turns from the chat store. History is
// part of the execution context: a load failure aborts the execution (fail
// closed) instead of running without conversation continuity.
func (a *BaseAgent) loadConversationHistory(ctx context.Context, tracer oteltrace.Tracer, chatStore ChatStore, cfg *ExecutionConfig) ([]*ChatMessage, error) {
	if chatStore == nil || cfg.ConversationID == "" {
		return nil, nil
	}
	histSpanCtx, histSpan := tracer.Start(ctx, "agent.history_load")
	histCtx, histCancel := context.WithTimeout(histSpanCtx, constants.AgentDBQueryTimeout)
	msgs, histErr := chatStore.ListMessages(histCtx, cfg.TenantID, cfg.ConversationID, cfg.UserID)
	histCancel()
	histSpan.End()
	if histErr != nil {
		a.Logger.Error("agent.history_load_failed",
			zap.String("agent_id", a.ID),
			zap.String("conversation_id", cfg.ConversationID),
			zap.Error(histErr))
		return nil, fmt.Errorf("load conversation history: %w", histErr)
	}
	return msgs, nil
}

func (a *BaseAgent) executeReAct(ctx context.Context, ec agentExecContext, result *AgentResult) error {
	if ec.capGW == nil {
		return fmt.Errorf("react: CapGateway not set")
	}
	cg, buildErr := agentgraph.BuildReActGraph(ec.capGW, a.Ledger, a.Logger)
	if buildErr != nil {
		return fmt.Errorf("react: build graph: %w", buildErr)
	}
	maxTokens := ec.maxContextTokens
	if maxTokens <= 0 {
		maxTokens = constants.DefaultAgentContextTokens
	}
	// 组装侧与循环侧同一预算账本（I1）：safetyRatio 锁定平台默认
	// （产品规格：不暴露用户配置），传 0 → ComputeBudget 回退
	// ContextSafetyReserveRatio（0.2）；循环侧 loopPolicy 锁 LoopCompactionSafetyRatio。
	// 跨轮复用（D6）：注入 CompactionStore 后走游标折叠 + 增量压缩 + 回写；
	// 未注入时 reuse 为 nil，行为与旧 BuildContextMessagesWithCompaction 一致。
	initMessages := a.assembleContextMessagesReuse(ctx, ec, maxTokens)

	// Resume from checkpoint if one exists.
	activePlan, restoredActives, initMessages := a.resumeFromCheckpoint(
		ctx, ec, initMessages,
	)
	if activePlan == nil {
		// 未恢复完整 checkpoint plan 才注入 task 摘要：两级同命中以 plan 为准。
		initMessages = a.maybeInjectTaskResume(ctx, ec, initMessages)
	}

	initState := a.buildReActInitState(ec, initMessages, maxTokens)
	// 审批续跑 C2c：从整批已批准/已终态载荷合成一条 assistant 工具调用消息（P1，
	// 含 N 条 tool_call）并置 SkipNextLLM，使工具调用直接进入工具节点，不再经 LLM
	// 重新生成参数。全部条目工具查不到（被删/改名）时回退 LLM 原路径（不构成死循环）。
	if len(ec.cfg.ApprovalResumePayloads) > 0 {
		var ok bool
		initState, ok = synthesizeApprovalResume(initState, ec.cfg.ApprovalResumePayloads)
		if !ok {
			first := ec.cfg.ApprovalResumePayloads[0]
			a.Logger.Info("agent: approval resume tool not found, falling back to llm",
				zap.String("execution_id", ec.cfg.ExecutionID),
				zap.String("server_id", first.ServerID),
				zap.String("tool", first.ToolName),
			)
		}
	}
	initState.ActivePlan = activePlan
	if len(restoredActives) > 0 {
		initState.Actives = restoredActives
	}
	initState.PlanNodeExecutor = a.buildPlanNodeExecutor(ec, ec.capGW)
	initState.DelegateExecutor = a.buildDelegateExecutor(ec, ec.capGW)
	if a.RecallMemoryFn != nil {

		fn := a.RecallMemoryFn
		initState.RecallMemoryFn = func(ctx context.Context, input map[string]any) (string, error) {
			return fn(ctx, ec.cfg.TenantID, ec.cfg.UserID, ec.agentID, ec.memoryScope, input)
		}
	}
	execCtx, cancel := newReActExecContext(ctx, ec)
	defer cancel()
	// Graph steps count both LLM and Tool node executions.
	// MaxLLMSteps (set in buildReActInitState from ec.cfg.MaxSteps)
	// counts only LLM calls and triggers forced answer at
	// s.Steps >= MaxLLMSteps-1. Double it and add one so the
	// forced-answer mechanism engages before the graph loop
	// exhausts the step budget. The plan tail reserves one wave for
	// the slots and one for the finalize join per plan step. Keep 0
	// as-is so Invoke falls back to its internal default.
	graphSteps := ec.cfg.MaxSteps
	if graphSteps > 0 {
		graphSteps = graphSteps*2 + 1 + 2*constants.MaxPlanSteps
	}
	runCfg := agentgraph.RunConfig[agentgraph.ReActState]{
		MaxSteps:    graphSteps,
		MaxParallel: initState.PlanLimits.MaxConcurrentNodes,
		MergeWave:   agentgraph.MergeReActWave,
	}
	if a.CheckpointStore != nil {
		runCfg.AfterStep = func(afterCtx context.Context, afterState agentgraph.ReActState) error {
			return agentgraph.PersistReActCheckpoint(afterCtx, a.CheckpointStore, ec.cfg.TenantID, agentgraph.PlanCheckpointIdentity{
				ExecutionID: ec.cfg.ExecutionID, TraceID: ec.cfg.TraceID, ConversationID: ec.cfg.ConversationID, AgentID: ec.agentID, UserID: ec.cfg.UserID,
			}, &afterState, "")
		}
	}
	graphCtx, reactSpan := ec.tracer.Start(execCtx, "react.graph.invoke",
		oteltrace.WithAttributes(attribute.Int("max_steps", ec.cfg.MaxSteps)),
	)
	finalState, runErr := cg.Invoke(graphCtx, initState, runCfg)
	recordTerminatedBy(reactSpan, finalState)
	reactSpan.End()
	// 最终请求 context_length_exceeded 降级（Spec D4）：循环已结束、工具成本
	// 已花，最小请求必然小于原请求，只重试一次；成功返回答案，仍失败终止。
	finalState, runErr = degradeFinalRequest(graphCtx, ec, finalState, runErr, maxTokens)
	a.finalizeReActCheckpoint(ctx, ec, runErr)
	if runErr != nil {
		return fmt.Errorf("react: %w", runErr)
	}
	a.collectReActResult(execCtx, ctx, ec, result, finalState)
	return nil
}

// synthesizeApprovalResume 从已批准载荷合成 assistant 工具调用消息（P1）追加到
// 恢复 Messages 并置 SkipNextLLM（C2c）：审批续跑时使整批已批准/已终态的工具调用
// 直接执行，不再经 LLM 重新生成参数。逐条在 AvailableTools 中按 ServerID+
// CapabilityID 查表取 Name（payload.ToolName 是 capability id，非显示名）；查不到
// （工具被删/改名）跳过该条（LLM 原路径也调不到，不构成死循环），其余照常合成。
// 合成一条 assistant 消息含 N 条 tool_call，终态条目也合成——guard digest 命中后
// 走 executeApprovedForResume(terminal=true) 返回友好错误，LLM 感知未执行后收尾。
// 全部条目查不到时返回 (state, false)，调用方回退 LLM 原路径。返回 (state, true)
// 表示已合成至少一条。
func synthesizeApprovalResume(state agentgraph.ReActState, payloads []ToolApprovalPayload) (agentgraph.ReActState, bool) {
	// 合成路径禁止预检：恢复消息的工具调用已审批/已终态，precheck 会对同一批调用
	// 重复发起审批，导致续跑再暂停死循环。
	state.PrecheckApprovals = nil
	existingIDs := make(map[string]bool)
	for _, m := range state.Messages {
		for _, tc := range m.ToolCalls {
			existingIDs[tc.ID] = true
		}
	}
	toolCalls := make([]port.ToolCall, 0, len(payloads))
	synthesized := false
	for _, payload := range payloads {
		toolName := approvalToolName(state.AvailableTools, payload)
		if toolName == "" {
			continue // 工具被删/改名：跳过该条，其余条目照常合成。
		}
		callID := uniqueApprovalCallID(payload.ToolCallID, existingIDs)
		existingIDs[callID] = true
		toolCalls = append(toolCalls, port.ToolCall{ID: callID, Name: toolName, Arguments: payload.Arguments})
		synthesized = true
	}
	if !synthesized {
		return state, false
	}
	state.Messages = append(state.Messages, port.LLMMessage{Role: "assistant", ToolCalls: toolCalls})
	state.SkipNextLLM = true
	return state, true
}

// approvalToolName 在 AvailableTools 中按 ServerID+CapabilityID 查表取显示名：
// payload.ToolName 是 capability id，非显示名。查不到返回空串（工具被删/改名），
// 由调用方跳过该条。
func approvalToolName(tools []port.ToolDefinition, payload ToolApprovalPayload) string {
	for _, td := range tools {
		if td.ServerID == payload.ServerID && td.CapabilityID == payload.ToolName {
			return td.Name
		}
	}
	return ""
}

// uniqueApprovalCallID 校验并生成唯一 tool_call ID：原 ID 为空或已被占用（恢复
// 消息已存在同 ID——含审批触发的原始 tool_call 仍留在历史；同批两载荷撞 ID）时
// 生成新唯一 ID，避免 LLM 上下文孤立重复 tool_call。
func uniqueApprovalCallID(callID string, existing map[string]bool) string {
	if callID == "" || existing[callID] {
		return uuid.NewString()
	}
	return callID
}

// newReActExecContext 构建 ReAct 执行上下文：有超时预算时用 WithTimeout，否则
// 可取消上下文；注入 trace/tenant 供内层流式调用读取。cancel 由调用方 defer。
func newReActExecContext(ctx context.Context, ec agentExecContext) (context.Context, context.CancelFunc) {
	var execCtx context.Context
	var cancel context.CancelFunc
	if ec.cfg.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, ec.cfg.Timeout)
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}
	execCtx = reqctx.WithTraceID(execCtx, ec.cfg.TraceID)
	execCtx = reqctx.WithTenantID(execCtx, ec.cfg.TenantID)
	return execCtx, cancel
}

// finalizeReActCheckpoint ReAct 循环结束后的 checkpoint 收尾：成功 MarkCompleted；
// 运行错误写终态 failed；cancelled/deadline(断线续跑)与审批等待(waiting_approval)
// 保留原状,由新鲜度窗口兜底僵尸 running checkpoint。审批续跑失败不在此写终态:
// 抢占后交给 application 层 finishApprovalResume 统一收尾(未消费批准回滚
// waiting_approval 供重试,已消费才写 failed,见 SECURITY-MEDIUM-2)。
func (a *BaseAgent) finalizeReActCheckpoint(ctx context.Context, ec agentExecContext, runErr error) {
	if a.CheckpointStore == nil {
		return
	}
	markCtx, markCancel := context.WithTimeout(ctx, constants.AgentDBQueryTimeout)
	defer markCancel()
	if runErr == nil {
		_ = a.CheckpointStore.MarkCompleted(markCtx, ec.cfg.TenantID, ec.cfg.ExecutionID)
		return
	}
	if !retainRunningError(runErr) && len(ec.cfg.ApprovalResumeIDs) == 0 {
		_ = a.CheckpointStore.Terminate(markCtx, ec.cfg.TenantID, ec.cfg.ExecutionID, "failed")
	}
}

// collectReActResult 汇总终态到 result：图结果、任务快照、最终事件、步骤计数与
// 工具调用列表。
func (a *BaseAgent) collectReActResult(execCtx, ctx context.Context, ec agentExecContext, result *AgentResult, finalState agentgraph.ReActState) {
	a.collectGraphResult(execCtx, result, finalState, ec)
	a.persistTaskSnapshot(ctx, ec, finalState, result)
	a.appendFinalAnswerEvent(result, finalState, ec)
	a.mu.Lock()
	a.State.StepsTaken = finalState.Steps
	a.mu.Unlock()
	for _, tc := range finalState.AllToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{ToolName: tc.Name, Input: tc.Arguments})
	}
}

// degradeFinalRequest 处理最终请求 context_length_exceeded 降级（Spec D4）：
// 最小请求（system + 纯截断历史 + task，剔除全部工具结果）重试一次；成功
// 返回带答案的终态，仍失败原样返回原错误（重试失败不替换错误类型）。非
// context_length 错误或非最终请求位置时也原样返回——参数校验类 400 不在此
// 路径（重试无意义，是 bug），等待工具结果位置失败不降级（否则模型会看到
// "调用了工具但没有结果"的残缺对话）。
func degradeFinalRequest(ctx context.Context, ec agentExecContext, finalState agentgraph.ReActState,
	runErr error, maxTokens int) (agentgraph.ReActState, error) {
	if runErr == nil || !agentgraph.IsContextLengthExceeded(runErr) || !isFinalRequest(finalState) {
		return finalState, runErr
	}
	// 最小请求同样携带完整全局后缀：降级重试不能丢失治理指令。
	retryMessages := agentgraph.BuildMinimalRetryMessages(ec.systemPrompt+globalSystemSuffix(ec.globalSuffix), ec.input, finalState.Messages, maxTokens)
	// 复用 routeLLM 语义：非流式单次 Route，RetryFn 对瞬态失败一层退避。
	finalResp, retryErr := retryMinimalFinalRequest(ctx, ec, retryMessages)
	if retryErr != nil {
		return finalState, runErr
	}
	finalState.Output = finalResp
	return finalState, nil
}

// isFinalRequest 报告图终止时是否处于"最终回答请求"位置：最后一条消息
// 不是等待工具调用的 assistant 消息。等待工具结果位置失败不降级——否则
// 模型会看到"调用了工具但没有结果"的残缺对话。
func isFinalRequest(s agentgraph.ReActState) bool {
	if len(s.Messages) == 0 {
		return true
	}
	last := s.Messages[len(s.Messages)-1]
	return last.Role != "assistant" || len(last.ToolCalls) == 0
}

// retryMinimalFinalRequest 以最小请求重试一次最终回答（Spec D4）：非流式
// 单次 Route，RetryFn 对瞬态失败一层退避；context_length 错误本身是永久
// 错误，RetryFn 单次尝试后终止。
func retryMinimalFinalRequest(ctx context.Context, ec agentExecContext, messages []port.LLMMessage) (string, error) {
	resp, err := agentgraph.RetryFn(ctx, agentgraph.DefaultRetry, func() (port.CapabilityResponse, error) {
		return ec.capGW.Route(ctx, port.CapabilityRequest{
			TraceID: ec.cfg.TraceID, TenantID: ec.cfg.TenantID, Type: port.CapLLM,
			LLM: &port.LLMCapRequest{
				Model: ec.llmModel, Messages: messages,
				Temperature: ec.cfg.Temperature, ReasoningEffort: ec.cfg.ReasoningEffort,
				MaxTokens: resolveMaxOutputTokens(ec.cfg.MaxTokens, ec.cfg.OutputReserve),
			},
		})
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (a *BaseAgent) executeCoT(cfg *ExecutionConfig, input string, result *AgentResult) error {
	for i := 0; i < cfg.MaxSteps; i++ {
		thought := Thought{
			Step:        i + 1,
			Observation: "Thinking about: " + input,
			Thought:     "Considering possible responses",
		}
		result.Thoughts = append(result.Thoughts, thought)
		a.mu.Lock()
		a.State.StepsTaken++
		a.mu.Unlock()
		if i >= 2 {
			result.Output = fmt.Sprintf("Response for: %s", input)
			return nil
		}
	}
	return nil
}

// effectiveStuckThreshold returns the configured stuck threshold or the
// default when unset (≤0).
func (a *BaseAgent) effectiveStuckThreshold() int {
	if a.StuckThreshold <= 0 {
		return constants.DefaultStuckThreshold
	}
	return a.StuckThreshold
}

func (a *BaseAgent) executePlanning(ctx context.Context, ec agentExecContext, result *AgentResult) error {
	if ec.capGW == nil {
		return fmt.Errorf("planning: CapGateway not set")
	}
	stuckThreshold := a.effectiveStuckThreshold()
	cg, buildErr := agentgraph.BuildReActGraph(ec.capGW, a.Ledger, a.Logger)
	if buildErr != nil {
		return fmt.Errorf("planning: build graph: %w", buildErr)
	}
	maxTokens := ec.maxContextTokens
	if maxTokens <= 0 {
		maxTokens = constants.DefaultAgentContextTokens
	}
	// 组装侧与循环侧同一预算账本（I1）：safetyRatio 锁定平台默认
	// （产品规格：不暴露用户配置），传 0 → ComputeBudget 回退
	// ContextSafetyReserveRatio（0.2）；循环侧 loopPolicy 锁 LoopCompactionSafetyRatio。
	// 跨轮复用（D6）：注入 CompactionStore 后走游标折叠 + 增量压缩 + 回写；
	// 未注入时 reuse 为 nil，行为与旧 BuildContextMessagesWithCompaction 一致。
	initMessages := a.assembleContextMessagesReuse(ctx, ec, maxTokens)
	initMessages = a.maybeInjectTaskResume(ctx, ec, initMessages)
	initState := a.buildReActInitState(ec, initMessages, maxTokens)
	initState.StuckThreshold = stuckThreshold
	initState.DelegateExecutor = a.buildDelegateExecutor(ec, ec.capGW)
	if a.RecallMemoryFn != nil {
		fn := a.RecallMemoryFn
		initState.RecallMemoryFn = func(ctx context.Context, input map[string]any) (string, error) {
			return fn(ctx, ec.cfg.TenantID, ec.cfg.UserID, ec.agentID, ec.memoryScope, input)
		}
	}
	var execCtx context.Context
	var cancel context.CancelFunc
	if ec.cfg.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, ec.cfg.Timeout)
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}
	execCtx = reqctx.WithTraceID(execCtx, ec.cfg.TraceID)
	execCtx = reqctx.WithTenantID(execCtx, ec.cfg.TenantID)
	defer cancel()
	graphCtx, planSpan := ec.tracer.Start(execCtx, "planning.graph.invoke",
		oteltrace.WithAttributes(attribute.Int("stuck_threshold", stuckThreshold)),
	)
	// 与 executeReAct 相同的波次预算：双倍 LLM 步 + 1 + plan 槽位/finalize 预留
	planSteps := ec.cfg.MaxSteps
	if planSteps > 0 {
		planSteps = planSteps*2 + 1 + 2*constants.MaxPlanSteps
	}
	finalState, runErr := cg.Invoke(graphCtx, initState, agentgraph.RunConfig[agentgraph.ReActState]{
		MaxSteps:    planSteps,
		MaxParallel: initState.PlanLimits.MaxConcurrentNodes,
		MergeWave:   agentgraph.MergeReActWave,
	})
	recordTerminatedBy(planSpan, finalState)
	planSpan.End()
	if runErr != nil {
		return fmt.Errorf("planning: %w", runErr)
	}
	a.collectGraphResult(execCtx, result, finalState, ec)
	a.persistTaskSnapshot(ctx, ec, finalState, result)
	a.appendFinalAnswerEvent(result, finalState, ec)
	a.mu.Lock()
	a.State.StepsTaken = finalState.Steps
	a.mu.Unlock()
	for _, tc := range finalState.AllToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{ToolName: tc.Name, Input: tc.Arguments})
	}
	return nil
}

// taskTokensOf 返回消息列表中最新用户消息（当前任务）的 token 估算；
// 无用户消息时返回 0。任务永不压缩，其 token 成本必须从预算账本的
// history 配额扣减（Spec 第 2 节 history = usable − fixedHead − tools − task）。
func taskTokensOf(msgs []port.LLMMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return tokenutil.EstimateText(msgs[i].Content)
		}
	}
	return 0
}

// promptVersionMap builds the prompt key → version fingerprint map carried
// into the ReAct loop. system_prompt is recorded only when a prompt revision
// is applied; config (the effective tunable snapshot fingerprint) is always
// present so the run stays attributable even with an empty system prompt.
// Never returns nil — the config key must survive the empty-prompt boundary
// (previously a nil map dropped the whole map).
func promptVersionMap(systemPromptVersion, configVersion string) map[string]string {
	m := map[string]string{"config": configVersion}
	if systemPromptVersion != "" {
		m["system_prompt"] = systemPromptVersion
	}
	return m
}

// resolveMaxOutputTokens 解析单次 LLM 请求的输出上限：显式 MaxTokens >
// 已解析 OutputReserve（显式 > vendor maxOut > 常量）> 兜底常量。
func resolveMaxOutputTokens(explicit, reserve int) int {
	if explicit > 0 {
		return explicit
	}
	if reserve > 0 {
		return reserve
	}
	return constants.DefaultOutputReserveTokens
}

func (a *BaseAgent) buildReActInitState(ec agentExecContext, initMessages []port.LLMMessage, maxTokens int) agentgraph.ReActState {
	availableTools := buildBuiltinTools(ec.workspaceNames, ec.workspaceDescs,
		len(ec.workspaceNames) > 0 && ec.cfg.RAGSearchFn != nil, a.MemoryInjector != nil)
	// delegate 启停过滤：未启用时不暴露 stratum_delegate 工具，避免模型浪费轮次
	// （execDelegateTool 的 fail-closed 兜底仍在）。mergeTools 后过滤，ExtraTools
	// 侧若混入同名也一并剔除。
	available := mergeTools(availableTools, ec.cfg.ExtraTools, a.Logger)
	if !ec.cfg.DelegateEnabled {
		available = withoutDelegateTool(available)
	}

	// 压缩冷却默认启用（Spec 第 4 节）：平台参数 agent.compaction_cooldown_sec
	// 已在 Execute 解析进 cfg；0 = constants.DefaultCompactionCooldown，option
	// 仍可覆盖。
	cooldownSec := ec.cfg.CompactionCooldownSec
	if cooldownSec <= 0 {
		cooldownSec = int(constants.DefaultCompactionCooldown.Seconds())
	}
	return agentgraph.ReActState{
		TenantID:               ec.cfg.TenantID,
		TraceID:                ec.cfg.TraceID,
		ConversationID:         ec.cfg.ConversationID,
		Model:                  ec.llmModel,
		Temperature:            ec.cfg.Temperature,
		ReasoningEffort:        ec.cfg.ReasoningEffort,
		MaxTokens:              resolveMaxOutputTokens(ec.cfg.MaxTokens, ec.cfg.OutputReserve),
		CompactionRecentGroups: ec.cfg.CompactionRecentGroups,
		CompactionCooldownSec:  cooldownSec,
		// TokenCorrection must start at 1.0: the zero value would divide the
		// compaction threshold by zero on the first step.
		TokenCorrection:            1.0,
		Messages:                   initMessages,
		OnToken:                    ec.cfg.TokenCallback,
		OnDelegateEvent:            ec.cfg.DelegateEventCallback,
		AvailableTools:             available,
		SkillCatalog:               ec.cfg.SkillCatalog,
		Actives:                    ec.cfg.Actives,
		TracePayloadStore:          ec.cfg.TracePayloadStore,
		ToolExecutionFn:            ec.cfg.ToolExecutionFn,
		PrecheckApprovals:          ec.cfg.PrecheckApprovals,
		ExecutionID:                ec.cfg.ExecutionID,
		AgentKnowledgeWorkspaceIDs: ec.workspaceNames,
		AgentMemoryScope:           ec.memoryScope,
		ViewerID:                   ec.cfg.UserID,
		RAGSearchFn:                ec.cfg.RAGSearchFn,
		RAGSearchFnWithEvidence:    ec.cfg.RAGSearchFnWithEvidence,
		PromptVersions:             promptVersionMap(ec.cfg.SystemPromptVersion, tunableConfigVersion(ec.cfg, maxTokens)),
		OfficialDocsSearchFn:       ec.cfg.OfficialDocsSearchFn,
		DiagnosticFn:               ec.cfg.DiagnosticFn,
		ProposalCreateFn:           ec.cfg.ProposalCreateFn,
		ResourceChangeApplyFn:      ec.cfg.ResourceChangeApplyFn,
		ListModelsFn:               ec.cfg.ListModelsFn,
		UpdateSystemModelFn:        ec.cfg.UpdateSystemModelFn,
		ListAgentsFn:               ec.cfg.ListAgentsFn,
		ListMCPServersFn:           ec.cfg.ListMCPServersFn,
		InternalToolResultGuardFn:  ec.cfg.InternalToolResultGuardFn,
		MaxLLMSteps:                ec.cfg.MaxSteps,
		// 声称带引用约束：对账轨开启时注入引用规则（主注入 + 收尾强化）。
		EnforceClaimCitations: ec.cfg.FactCheck != nil && ec.cfg.FactCheck.CitationVerify,
		MaxContextTokens:      maxTokens,
		// 成本预算：registry 参数 agent.max_tokens_per_execution 经
		// WithMaxTokensPerExecution 覆盖，0 = 不设限（Spec 第 3 节）。
		MaxTokensPerExecution: ec.cfg.MaxTokensPerExecution,
		// 委托参数：深度/步数 0=unset 在此归一化（回落全局默认 + clamp 硬上限）；
		// 启用状态直接取 ExecutionConfig（snapshot 已回填 agent 配置）。
		DelegateEnabled:         ec.cfg.DelegateEnabled,
		DelegateMaxDepth:        resolvedDelegateMaxDepth(ec.cfg.DelegateMaxDepth),
		DelegateDefaultMaxSteps: resolvedDelegateDefaultSteps(ec.cfg.DelegateDefaultMaxSteps),
		// Budget 账本快照：一次执行一个，初始组装与 ReAct 循环共享同一来源。
		// 循环侧任务 = 最新用户消息，经 WithTask 从 HistoryCap 扣减（I3）。
		Budget:               agentgraph.ComputeBudget(maxTokens, ec.cfg.OutputReserve, 0).WithTask(taskTokensOf(initMessages)),
		HistoryCompactor:     ec.historyCompactor,
		PlanCheckpointWriter: a.CheckpointStore,
		PlanCheckpointIdentity: agentgraph.PlanCheckpointIdentity{
			ExecutionID: ec.cfg.ExecutionID, TraceID: ec.cfg.TraceID,
			ConversationID: ec.cfg.ConversationID, AgentID: ec.agentID, UserID: ec.cfg.UserID,
		},
		PlanIDSource: uuid.NewString,
		PlanLimits: domain.PlanLimits{
			MaxNodes: constants.DefaultPlanMaxNodes, MaxRevisions: constants.DefaultPlanMaxRevisions,
			MaxAttemptsPerNode: constants.DefaultPlanMaxAttemptsPerNode, MaxConcurrentNodes: constants.DefaultPlanMaxConcurrentNodes,
		},
	}
}

func (a *BaseAgent) buildPlanNodeExecutor(ec agentExecContext, capGW port.CapabilityGateway) agentgraph.PlanNodeExecutor {
	return func(nodeCtx context.Context, parent agentgraph.ReActState, node domain.PlanNode, summaries map[string]string) (agentgraph.PlanNodeExecutionResult, error) {
		nodeGraph, graphErr := agentgraph.BuildReActGraph(capGW, a.Ledger, a.Logger)
		if graphErr != nil {
			return agentgraph.PlanNodeExecutionResult{}, graphErr
		}
		// 子循环 system 也携带完整全局后缀：plan 节点执行是独立 LLM 调用，
		// 治理指令必须同步注入。
		systemMessage := port.LLMMessage{Role: "system", Content: ec.systemPrompt + globalSystemSuffix(ec.globalSuffix)}
		goal := node.Goal
		if len(summaries) > 0 {
			encoded, _ := json.Marshal(summaries)
			goal += "\nDependency summaries: " + string(encoded)
		}
		child := parent
		child.Messages = []port.LLMMessage{systemMessage, {Role: "user", Content: goal}}
		child.ActivePlan = nil
		child.PlanToolsDisabled = true
		// 子任务是干净起点：不继承父循环的振荡换路提示状态（父可能已提示过，
		// 子任务需独立获得一次 nudge 机会）。
		child.NoProgressOscillationNudged = false
		child.NoProgressOscillationResetAt = 0
		// 子循环的任务是节点目标：预算快照按新任务重新扣减 history 配额（I3）。
		child.Budget = child.Budget.WithTask(tokenutil.EstimateText(goal))
		child.MaxLLMSteps = constants.DefaultStepMaxLLMSteps
		subSteps := constants.DefaultStepMaxLLMSteps*2 + 1
		final, invokeErr := nodeGraph.Invoke(nodeCtx, child, agentgraph.RunConfig[agentgraph.ReActState]{MaxSteps: subSteps})
		if invokeErr != nil {
			return agentgraph.PlanNodeExecutionResult{}, invokeErr
		}
		// 子循环 token 用量折回父图预算账本：child 是 parent 的结构体拷贝，
		// 继承父图 TotalTokens 基线，故 delta = final − child 即子循环自身用量，
		// 基线只计一次。子循环内已按同一 MaxTokensPerExecution 自终止，折回后
		// 父图下一次 LLM 检查点终止整次执行（Finding 1 修复）。
		return agentgraph.PlanNodeExecutionResult{
			Summary:    final.Output,
			TokensUsed: final.TotalTokens - child.TotalTokens,
		}, nil
	}
}

// buildDelegateExecutor 构造 stratum_delegate 的执行器（application 层闭包，经
// guard 的 DelegateExecutor 分支调用）。镜像 buildPlanNodeExecutor：child := parent
// 值拷贝复用同一 agent 配置，独立 Messages 隔离上下文窗口，只回传摘要+token 增量。
// 委托参数（深度/默认步数）已由 buildReActInitState 从 ExecutionConfig 填入 parent，
// 子循环继承。
func (a *BaseAgent) buildDelegateExecutor(ec agentExecContext, capGW port.CapabilityGateway) agentgraph.DelegateExecutor {
	return func(childCtx context.Context, parent *agentgraph.ReActState, input agentgraph.DelegateInput) (agentgraph.DelegateOutput, error) {
		nodeGraph, graphErr := agentgraph.BuildReActGraph(capGW, a.Ledger, a.Logger)
		if graphErr != nil {
			return agentgraph.DelegateOutput{}, graphErr
		}
		delegateID := uuid.NewString()
		// 子循环 system 携带父 system prompt + 已算好的 memCtx + 全局后缀（复用父
		// 执行记忆，零成本）。user 消息 = goal + 结构化外壳摘要指令。
		systemMessage := port.LLMMessage{Role: "system", Content: ec.systemPrompt + ec.memCtx + globalSystemSuffix(ec.globalSuffix)}
		userMessage := port.LLMMessage{Role: "user", Content: input.Goal + "\n\n" + agentgraph.DelegateSummaryInstruction}
		child := *parent
		// 步数门限按子循环自身重新计数：父循环已消耗的 Steps 不得在子循环首调即
		// 触发 MaxLLMSteps 强制收尾（R17，security review）。预算仍由 child.Budget
		// 继承，不受此重置影响。
		child.Steps = 0
		child.Messages = []port.LLMMessage{systemMessage, userMessage}
		child.DelegateDepth = parent.DelegateDepth + 1
		// 子任务是干净起点：不继承父循环的振荡换路提示状态（父可能已提示过，
		// 子任务需独立获得一次 nudge 机会）。
		child.NoProgressOscillationNudged = false
		child.NoProgressOscillationResetAt = 0
		child.ActivePlan = nil
		child.PlanToolsDisabled = true
		// 子循环的任务是委托目标：预算快照按新任务重新扣减 history 配额（I3，
		// 对齐 buildPlanNodeExecutor 的 WithTask）。
		child.Budget = child.Budget.WithTask(tokenutil.EstimateText(input.Goal))
		// 深度参数化过滤：仅当达到深度上限才移除 delegate 工具（默认 MaxDepth=1
		// 时子循环不可再委托；MaxDepth=2 时 depth=1 的子循环仍可再委托一层）。
		if child.DelegateDepth >= child.DelegateMaxDepth {
			child.AvailableTools = withoutDelegateTool(child.AvailableTools)
		}
		maxSteps := agentgraph.MaxDelegateSteps(input.MaxSteps, child.DelegateDefaultMaxSteps)
		child.MaxLLMSteps = maxSteps
		// 子循环输出经摘要回传，不向父链流式 token（OnToken 置 nil）。
		child.OnToken = nil
		subSteps := maxSteps*2 + 1
		// running/finished 帧由 graph 层 buildDelegateClosure 成对回调(SSE),此处
		// 不再发空 status 事件;delegateID 仅用于 finished 帧与日志关联。
		final, invokeErr := nodeGraph.Invoke(childCtx, child, agentgraph.RunConfig[agentgraph.ReActState]{MaxSteps: subSteps})
		if invokeErr != nil {
			// 原始错误只进日志（wrap 上抛给 guard）；观察正文由 graph 层固定模板
			// 承载，不把内部标识泄入下游错误正文。
			a.Logger.Error("delegate sub-agent failed", zap.String("agent_id", ec.agentID),
				zap.String("trace_id", ec.cfg.TraceID), zap.String("conversation_id", ec.cfg.ConversationID),
				zap.String("delegate_id", delegateID), zap.Error(invokeErr))
			// 失败路径也回传 DelegateID：graph 层 finished 失败帧据此关联 running 帧
			// 与日志链路（成功帧同源）。
			return agentgraph.DelegateOutput{DelegateID: delegateID}, fmt.Errorf("delegate sub-agent invoke: %w", invokeErr)
		}
		return agentgraph.DelegateOutput{
			Summary:    parseDelegateSummary(final.Output),
			Status:     parseDelegateStatus(final.Output),
			TokensUsed: final.TotalTokens - child.TotalTokens,
			StepsUsed:  final.Steps,
			DelegateID: delegateID,
		}, nil
	}
}

// resolvedDelegateMaxDepth 归一化 per-agent 委托深度：0=unset 回落默认，clamp 到
// 全局硬上限（MaxDelegateDepth），防误配导致深度失控。
func resolvedDelegateMaxDepth(v int) int {
	if v <= 0 {
		v = constants.DefaultDelegateMaxDepth
	}
	if v > constants.MaxDelegateDepth {
		v = constants.MaxDelegateDepth
	}
	return v
}

// resolvedDelegateDefaultSteps 归一化 per-agent 默认步数：0=unset 回落默认，clamp
// 到 MaxDelegateMaxLLMSteps（与工具 schema maximum 一致）。
func resolvedDelegateDefaultSteps(v int) int {
	if v <= 0 {
		v = constants.DefaultDelegateMaxLLMSteps
	}
	if v > constants.MaxDelegateMaxLLMSteps {
		v = constants.MaxDelegateMaxLLMSteps
	}
	return v
}

// withoutDelegateTool 从工具面移除 stratum_delegate（未启用或已达深度上限时）。
// 返回新 slice，不改写调用方 slice 底层数组。
func withoutDelegateTool(tools []port.ToolDefinition) []port.ToolDefinition {
	out := make([]port.ToolDefinition, 0, len(tools))
	for _, td := range tools {
		if td.Name == agentgraph.StratumDelegateToolName {
			continue
		}
		out = append(out, td)
	}
	return out
}

// parseDelegateStatus 从子循环 final.Output 提取结构化外壳的 status 字段
// （"success"|"partial"|"failed"）。子循环可能未按指令输出 JSON：整体解码失败时
// 扫描最后一个 '{'..'}' 子串兜底，字段非法/缺失回落 partial，绝不 fail 主循环。
// 外壳解析归属 application 层（graph 层只打包不回读）。
func parseDelegateStatus(output string) string {
	body := output
	if !json.Valid([]byte(body)) {
		if start := strings.LastIndex(body, "{"); start >= 0 {
			if end := strings.LastIndex(body[start:], "}"); end >= 0 {
				body = body[start : start+end+1]
			}
		}
	}
	var shell struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &shell); err != nil {
		return string(agentgraph.DelegateStatusPartial)
	}
	switch shell.Status {
	case string(agentgraph.DelegateStatusSuccess), string(agentgraph.DelegateStatusPartial), string(agentgraph.DelegateStatusFailed):
		return shell.Status
	default:
		return string(agentgraph.DelegateStatusPartial)
	}
}

// parseDelegateSummary 从子循环 final.Output 提取结构化外壳的 summary 字段
// （可读中文摘要），避免把子 agent 的原始 JSON 外壳原文塞给父上下文（M1，
// product review）。解析逻辑与 parseDelegateStatus 对齐：整体解码失败时扫描最后
// 一个 '{'..'}' 子串兜底；成功解析出非空 summary 才替换，否则回落原文——子循环
// 未按指令输出 JSON 时原文即自然语言，可读性不受影响。
func parseDelegateSummary(output string) string {
	body := output
	if !json.Valid([]byte(body)) {
		if start := strings.LastIndex(body, "{"); start >= 0 {
			if end := strings.LastIndex(body[start:], "}"); end >= 0 {
				body = body[start : start+end+1]
			}
		}
	}
	var shell struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(body), &shell); err != nil || shell.Summary == "" {
		return output
	}
	return shell.Summary
}

// recordTerminatedBy 在循环 span 上记录业务终止原因（Spec 第 3 节）。
// 终止标记在循环内产生，只有 Invoke 返回后才能写入；空值不写属性。
func recordTerminatedBy(span oteltrace.Span, finalState agentgraph.ReActState) {
	if finalState.TerminatedBy != "" {
		span.SetAttributes(attribute.String("terminated_by", finalState.TerminatedBy))
	}
}

func (a *BaseAgent) collectGraphResult(ctx context.Context, result *AgentResult, finalState agentgraph.ReActState, ec agentExecContext) {
	result.Output = finalState.Output
	result.Steps = finalState.Steps
	result.TokensUsed = finalState.TotalTokens
	result.CostUSD = finalState.TotalCostUSD
	result.ModelResolved = finalState.ModelResolved
	result.ModelRoutedVia = finalState.ModelRoutedVia
	result.ToolObservations = enrichToolObservations(finalState.ToolObservations, ec.cfg.TraceID, ec.cfg.ExecutionID, ec.cfg.ConversationID, ec.agentID, ec.cfg.UserID)
	result.TraceEvents = enrichTraceEvents(finalState.TraceEvents, ec.cfg.TraceID, ec.cfg.ExecutionID, ec.cfg.ConversationID, ec.agentID, ec.cfg.UserID)
	result.AssistantToolArtifacts = append([]domain.SystemAssistantToolArtifact(nil), finalState.AssistantToolArtifacts...)
	result.Sources = append([]port.RAGSearchSource(nil), finalState.CitationSources...)
	result.NoAnswer = finalState.NoAnswer
	// 幻觉校验（advisory）：judge 轨（Enabled）或对账轨（CitationVerify）任一
	// 开启即尝试；checker 内部按依赖 fail-closed——judge 轨缺证据 fn / 空 viewerID
	// 跳过，对账轨只依赖内存 ToolObservations 不依赖 RAG。失败/超时返回 nil，
	// 不阻塞执行。
	if ec.cfg.FactCheck != nil && (ec.cfg.FactCheck.Enabled || ec.cfg.FactCheck.CitationVerify) {
		settings := *ec.cfg.FactCheck
		settings.EvidenceFn = ec.cfg.RAGSearchFnWithEvidence
		result.FactCheck = factcheck.New(settings).Check(ctx, domain.FactCheckInput{
			// 校验目标是本轮最终输出（与 SSE done 透出的 output 同源）。
			Output: finalState.Output,
			// 检索侧（RAGService.resolveWorkspaceConfig）按 workspace name 解析，
			// 普通工具调用传的也是 name（buildBuiltinTools 用 KnowledgeWorkspaceNames
			// 构造工具参数枚举）。此前误传 ID 导致 GetByName 查不到 → ErrRAGDependency。
			Workspaces: a.KnowledgeWorkspaceNames,
			ViewerID:   ec.cfg.UserID,
			// 对账轨输入：内存态工具调用记录（enrich 前），by ToolCallID 对账。
			ToolObservations: finalState.ToolObservations,
		})
	}
	// 子节点降级向父图传播：StopLossTools 是跨子状态共享的 map（结构体拷贝
	// 共享引用），bool Degraded 值拷贝不传播，故从非空 map 推导整体降级。
	if len(finalState.StopLossTools) > 0 {
		result.Degraded = true
		result.DegradeReason = firstStopLossReason(finalState.StopLossTools)
	}
	// 业务终止（成本预算超限 / no_progress 停滞等）不是错误：终止原因透传，
	// 已产出部分保留。新增业务终止值必须在 agentgraph.IsBusinessTermination 登记。
	if agentgraph.IsBusinessTermination(finalState.TerminatedBy) {
		result.TerminatedBy = finalState.TerminatedBy
	}
	// 完成信号（stratum_complete_task）记录到 result.Metadata 供透出。
	if finalState.TaskCompleteRequested {
		if result.Metadata == nil {
			result.Metadata = map[string]interface{}{}
		}
		result.Metadata[constants.TaskMetadataCompleteKey] = true
	}
}

// persistTaskSnapshot 在 ReAct/Planning 循环结束后从内存 finalState.ActivePlan
// 提取 task 快照并写库。挂点必须读内存态：checkpoint 的 runtime_state 只编码
// ActiveSkills、不编码 Plan（buildReActRuntimeState），且被最后一步 AfterStep
// 覆盖。写路径旁路降级：任何失败不阻断已产出的响应（仿 MemoryBuffer 模式）。
func (a *BaseAgent) persistTaskSnapshot(ctx context.Context, ec agentExecContext, finalState agentgraph.ReActState, result *AgentResult) {
	if a.TaskStore == nil || finalState.ActivePlan == nil || finalState.ActivePlan.ID == "" {
		return
	}
	// 完成信号透出（与 collectGraphResult 中同键写入幂等）：执行期已置位即标记，
	// 即使 DB claim/save 降级失败也不丢该事实。
	if finalState.TaskCompleteRequested {
		markTaskComplete(result)
	}
	taskCtx, cancel := context.WithTimeout(ctx, constants.AgentDBQueryTimeout)
	defer cancel()
	snapshot := domain.BuildTaskSnapshot(finalState.ActivePlan, finalState.TaskCompleteRequested)

	claimed, ok, err := a.TaskStore.Claim(taskCtx, ec.cfg.TenantID, finalState.ActivePlan.ID, ec.cfg.ConversationID, constants.TaskLeaseDuration)
	if err != nil {
		a.Logger.Error("agent: task claim failed, degrade read-only",
			zap.String("agent_id", ec.agentID), zap.String("task_id", finalState.ActivePlan.ID), zap.Error(err))
		return
	}
	if ok {
		a.persistClaimedSnapshot(taskCtx, ec, claimed, snapshot, result)
		return
	}
	a.persistNewSnapshot(taskCtx, ec, snapshot, finalState, result)
}

// markTaskComplete 将完成标志写入 result.Metadata（白名单 key）。
func markTaskComplete(result *AgentResult) {
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata[constants.TaskMetadataCompleteKey] = true
}

// persistClaimedSnapshot 用本次提取覆盖已 claim 的 task 并写回；失败降级只读，
// 不阻断已产出的响应。claim 已 bump generation，此写回带该 generation 作乐观锁。
func (a *BaseAgent) persistClaimedSnapshot(ctx context.Context, ec agentExecContext, claimed *domain.Task, snapshot domain.TaskSnapshot, result *AgentResult) {
	applySnapshot(claimed, snapshot, ec.cfg.ConversationID, ec.cfg.ExecutionID)
	if err := a.TaskStore.Save(ctx, ec.cfg.TenantID, *claimed, claimed.Generation); err != nil {
		a.Logger.Error("agent: task save failed, degrade",
			zap.String("agent_id", ec.agentID), zap.String("task_id", claimed.ID), zap.Error(err))
		return
	}
	a.attachTaskSnapshot(result, snapshot)
}

// persistNewSnapshot 在 claim 无行时区分"新建"与"不可 claim"：Get 命中已完成/
// 被占 task 则降级只读；否则以 plan.ID 为稳定 task id 新建并写回。
func (a *BaseAgent) persistNewSnapshot(ctx context.Context, ec agentExecContext, snapshot domain.TaskSnapshot, finalState agentgraph.ReActState, result *AgentResult) {
	existing, getErr := a.TaskStore.Get(ctx, ec.cfg.TenantID, finalState.ActivePlan.ID)
	if getErr == nil && existing != nil {
		a.Logger.Warn("agent: task not claimable, degrade read-only",
			zap.String("task_id", existing.ID), zap.String("status", string(existing.Status)))
		return
	}
	newTask := snapshot.ToTask(finalState.ActivePlan.ID, ec.agentID, ec.cfg.UserID,
		ec.cfg.ConversationID, ec.cfg.ExecutionID)
	if err := a.TaskStore.Save(ctx, ec.cfg.TenantID, newTask, 0); err != nil {
		a.Logger.Error("agent: task create failed, degrade",
			zap.String("agent_id", ec.agentID), zap.String("task_id", newTask.ID), zap.Error(err))
		return
	}
	a.attachTaskSnapshot(result, snapshot)
}

// applySnapshot 用本次提取结果覆盖已 claim 的 task，并顺延 lease/expiry、
// 累加失败数。claim 已 bump generation，此写回带该 generation 作乐观锁。
func applySnapshot(t *domain.Task, s domain.TaskSnapshot, conversationID, executionID string) {
	now := time.Now()
	t.Goal = s.Goal
	t.CurrentPhase = s.CurrentPhase
	t.CompletedSteps = s.CompletedSteps
	t.NextAction = s.NextAction
	t.Status = s.Status
	t.ClaimedBy = conversationID
	t.LeaseExpiresAt = now.Add(constants.TaskLeaseDuration)
	t.LastConversationID = conversationID
	t.LastExecutionID = executionID
	t.FailCount += s.Failures
	t.ExpiresAt = now.Add(constants.TaskExpiresAt)
}

// attachTaskSnapshot 将 task 快照写入 result.Metadata 供 SSE done 透出（白名单
// key，见 handler）。已有 Metadata（如 complete 标志）保留。
func (a *BaseAgent) attachTaskSnapshot(result *AgentResult, snapshot domain.TaskSnapshot) {
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata[constants.TaskMetadataKey] = snapshot
}

// firstStopLossReason 从止损工具集合构造固定枚举 DegradeReason。map 迭代序
// 不稳定，排序取最小工具名保证确定性；空集合返回空串（调用方已判非空）。
func firstStopLossReason(stopLoss map[string]bool) string {
	tools := make([]string, 0, len(stopLoss))
	for name := range stopLoss {
		tools = append(tools, name)
	}
	slices.Sort(tools)
	if len(tools) == 0 {
		return ""
	}
	return constants.AgentDegradeReasonStopLossPrefix + tools[0]
}

func (a *BaseAgent) appendFinalAnswerEvent(result *AgentResult, finalState agentgraph.ReActState, ec agentExecContext) {
	finalAnswerAt := time.Now()
	result.TraceEvents = append(result.TraceEvents, domain.AgentTraceEvent{
		TraceID:         ec.cfg.TraceID,
		ExecutionID:     ec.cfg.ExecutionID,
		ConversationID:  ec.cfg.ConversationID,
		AgentID:         ec.agentID,
		UserID:          ec.cfg.UserID,
		RunType:         domain.RunTypeAgent,
		ObservationType: domain.ObservationTypeAgent,
		EventType:       domain.TraceEventFinalAnswer,
		StepIndex:       finalState.Steps,
		Status:          domain.ToolTraceStatusSuccess,
		Output:          map[string]any{"content": finalState.Output},
		Summary:         textutil.TruncateRunes(finalState.Output, 500),
		Model:           ec.llmModel,
		TotalTokens:     finalState.TotalTokens,
		CostUSD:         finalState.TotalCostUSD,
		ProviderType:    domain.ProviderTypeLLM,
		ProviderID:      ec.llmModel,
		SequenceNo:      int64(len(result.TraceEvents) + 1),
		StartedAt:       finalAnswerAt,
		EndedAt:         finalAnswerAt,
	})
}

// persistUserMessage 在 agent-loop 开始前立即持久化用户消息,不等执行结束:
// 等待审批/长时间执行期间用户消息已落库,刷新/断线后聊天记录不丢失。
// 续跑/重连(cfg.SkipUserMessageSave)跳过,避免同一 query 重复入库。
func (a *BaseAgent) persistUserMessage(ctx context.Context, tracer oteltrace.Tracer, chatStore ChatStore, cfg *ExecutionConfig, input, agentID, memoryScope string) {
	if chatStore == nil || cfg.ConversationID == "" || cfg.SkipUserMessageSave {
		return
	}
	userMsg := &ChatMessage{
		ConversationID: cfg.ConversationID, Role: "user", Content: input,
		UserID: cfg.UserID, AgentID: agentID, MemoryScope: memoryScope,
		SkipOutbox: false, Visibility: domain.ChatMessageVisibilityUser,
		TraceID: cfg.TraceID,
	}
	_, saveUserSpan := tracer.Start(ctx, "agent.chat_store.save_user")
	saveCtx, saveCancel := context.WithTimeout(ctx, constants.AgentDBQueryTimeout)
	addUserErr := chatStore.AddMessage(saveCtx, cfg.TenantID, userMsg)
	saveCancel()
	saveUserSpan.End()
	if addUserErr != nil {
		a.Logger.Warn("agent: failed to save user message", zap.String("conversation_id", cfg.ConversationID), zap.Error(addUserErr))
	}
}

func (a *BaseAgent) persistChatMessages(ctx context.Context, tracer oteltrace.Tracer, chatStore ChatStore, cfg *ExecutionConfig, result *AgentResult, agentID, memoryScope string, execErr error) {
	if chatStore == nil || cfg.ConversationID == "" || execErr != nil {
		return
	}
	agentMsg := &ChatMessage{
		ConversationID: cfg.ConversationID, Role: "assistant", Content: result.Output,
		UserID: cfg.UserID, AgentID: agentID, MemoryScope: memoryScope,
		SkipOutbox: false, Visibility: domain.ChatMessageVisibilityUser, Artifacts: result.Artifacts,
		Sources: result.Sources, TraceID: cfg.TraceID,
	}
	_, saveAgentSpan := tracer.Start(ctx, "agent.chat_store.save_assistant")
	saveCtx2, saveCancel2 := context.WithTimeout(ctx, constants.AgentDBQueryTimeout)
	addAgentErr := chatStore.AddMessage(saveCtx2, cfg.TenantID, agentMsg)
	saveCancel2()
	saveAgentSpan.End()
	if addAgentErr != nil {
		a.Logger.Warn("agent: failed to save agent message", zap.String("conversation_id", cfg.ConversationID), zap.Error(addAgentErr))
	}
	summary := buildToolObservationSummary(result.ToolObservations)
	if summary == "" {
		return
	}
	summaryMsg := &ChatMessage{
		ConversationID: cfg.ConversationID, Role: "assistant", Content: summary,
		UserID: cfg.UserID, AgentID: agentID, MemoryScope: memoryScope,
		SkipOutbox: true, Visibility: domain.ChatMessageVisibilityInternal,
		TraceID: cfg.TraceID,
	}
	_, saveSummarySpan := tracer.Start(ctx, "agent.chat_store.save_tool_summary")
	saveCtx3, saveCancel3 := context.WithTimeout(ctx, constants.AgentDBQueryTimeout)
	addSummaryErr := chatStore.AddMessage(saveCtx3, cfg.TenantID, summaryMsg)
	saveCancel3()
	saveSummarySpan.End()
	if addSummaryErr != nil {
		a.Logger.Warn("agent: failed to save tool summary message", zap.String("conversation_id", cfg.ConversationID), zap.Error(addSummaryErr))
	}
}

func enrichToolObservations(in []domain.ToolObservation, traceID, executionID, conversationID, agentID, userID string) []domain.ToolObservation {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.ToolObservation, len(in))
	for i, obs := range in {
		out[i] = obs
		if out[i].TraceID == "" {
			out[i].TraceID = traceID
		}
		if out[i].ExecutionID == "" {
			out[i].ExecutionID = executionID
		}
		if out[i].ConversationID == "" {
			out[i].ConversationID = conversationID
		}
		out[i].AgentID = agentID
		out[i].UserID = userID
		if out[i].Status == "" {
			out[i].Status = domain.ToolTraceStatusSuccess
		}
		if out[i].ProviderType == "" {
			out[i].ProviderType = domain.ProviderTypeInternal
		}
		if out[i].ProviderID == "" {
			out[i].ProviderID = out[i].ToolName
		}
		if out[i].CapabilityID == "" {
			out[i].CapabilityID = out[i].ToolName
		}
	}
	return out
}

func enrichTraceEvents(in []domain.AgentTraceEvent, traceID, executionID, conversationID, agentID, userID string) []domain.AgentTraceEvent {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.AgentTraceEvent, len(in))
	for i, ev := range in {
		out[i] = ev
		if out[i].TraceID == "" {
			out[i].TraceID = traceID
		}
		if out[i].ExecutionID == "" {
			out[i].ExecutionID = executionID
		}
		if out[i].ConversationID == "" {
			out[i].ConversationID = conversationID
		}
		out[i].AgentID = agentID
		out[i].UserID = userID
		if out[i].RunType == "" {
			out[i].RunType = domain.RunTypeAgent
		}
		if out[i].ObservationType == "" {
			out[i].ObservationType = domain.ObservationTypeCustom
		}
		if out[i].SequenceNo == 0 {
			out[i].SequenceNo = int64(i + 1)
		}
		if out[i].StartedAt.IsZero() && !out[i].EndedAt.IsZero() {
			out[i].StartedAt = out[i].EndedAt
		}
		if out[i].EndedAt.IsZero() && !out[i].StartedAt.IsZero() && out[i].LatencyMs > 0 {
			out[i].EndedAt = out[i].StartedAt.Add(time.Duration(out[i].LatencyMs) * time.Millisecond)
		}
	}
	return out
}

func buildToolObservationSummary(observations []domain.ToolObservation) string {
	if len(observations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("本轮工具观察摘要：")
	for i, obs := range observations {
		if obs.Summary == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%d. %s：%s", i+1, obs.ToolName, obs.Summary))
	}
	if b.Len() == len("本轮工具观察摘要：") {
		return ""
	}
	return textutil.TruncateRunes(b.String(), 3000)
}

// ExecutionOption configures agent execution behavior
type ExecutionOption func(*ExecutionConfig)

// WithMaxSteps sets the maximum number of steps
func WithMaxSteps(maxSteps int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.MaxSteps = maxSteps
	}
}

// WithTimeout sets the execution timeout
func WithTimeout(timeout time.Duration) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.Timeout = timeout
	}
}

// WithTemperature sets the LLM temperature
func WithTemperature(temperature float32) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.Temperature = temperature
	}
}

// WithReasoningEffort sets the LLM reasoning effort tier. "" = unset
// (gateway/provider default applies); non-empty values are gated by model
// capability at the gateway.
func WithReasoningEffort(effort string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ReasoningEffort = effort
	}
}

// WithMaxTokens sets the max output tokens for each LLM request. 0 = unset.
func WithMaxTokens(maxTokens int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.MaxTokens = maxTokens
	}
}

// WithMaxContextTokens sets the resolved execution window budget.
// 0 = unset, falls back to the agent config's explicit MaxContextTokens.
func WithMaxContextTokens(maxContextTokens int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.MaxContextTokens = maxContextTokens
	}
}

// WithOutputReserve 注入主模型输出预留（Spec 第 2 节账本 outputReserve）。
func WithOutputReserve(reserve int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.OutputReserve = reserve
	}
}

// WithWindowSource records the window resolution source for trace attributes.
func WithWindowSource(source string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.WindowSource = source
	}
}

// WithCompactionRecentGroups overrides in-loop compaction recent groups.
// 0 = auto-derive from MaxContextTokens.
func WithCompactionRecentGroups(recentGroups int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.CompactionRecentGroups = recentGroups
	}
}

// WithCompactionCooldownSec sets the in-loop compaction cooldown in seconds.
// 0 = default constant.
func WithCompactionCooldownSec(sec int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.CompactionCooldownSec = sec
	}
}

// WithMaxTokensPerExecution sets the execution-wide LLM token budget.
// 0 = unlimited (gateway/provider default).
func WithMaxTokensPerExecution(tokens int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.MaxTokensPerExecution = tokens
	}
}

// WithTools enables tool usage
func WithTools(tools []string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.AvailableTools = tools
		cfg.EnableTools = true
	}
}

// WithStream enables streaming output
func WithStream(enable bool) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.Stream = enable
	}
}

// WithTokenCallback sets a per-token callback, enabling streaming automatically.
func WithTokenCallback(cb func(string)) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.TokenCallback = cb
		cfg.Stream = true
	}
}

// WithDelegateEventCallback sets a per-delegate progress callback (running /
// finished), emitted by execDelegateTool through the ReAct state. It does not
// enable streaming by itself; pair with WithTokenCallback on streamed paths.
func WithDelegateEventCallback(cb func(agentgraph.DelegateEvent)) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.DelegateEventCallback = cb
	}
}

// WithTenantID sets the tenant ID for the execution context.
func WithTenantID(id string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.TenantID = id
	}
}

func WithTraceID(id string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.TraceID = id
	}
}

func WithExecutionID(id string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ExecutionID = id
	}
}

// WithSkipUserMessageSave 续跑/重连时跳过用户消息再次持久化(首次发送已在
// Execute 开头即时落库),避免同一 query 在恢复执行时重复入库。
func WithSkipUserMessageSave(skip bool) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.SkipUserMessageSave = skip
	}
}

// WithApprovalResume marks this execution as an approval-resume rerun for the
// given approved approval ID. It lets resumeFromCheckpoint treat a
// waiting_approval checkpoint as resumable, and pairs with the guard options
// built by buildApprovalResumeOptions that inject the approved tool call.
func WithApprovalResume(id string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ApprovalResumeIDs = []string{id}
	}
}

// WithApprovalResumes 批量版 WithApprovalResume：注入同一 executionID 的一批多审批
// ID，任一非空即把 waiting_approval checkpoint 视为可恢复（统一续跑）。
func WithApprovalResumes(ids []string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ApprovalResumeIDs = ids
	}
}

// WithApprovalResumePayload 注入已批准的 ToolApprovalPayload（C2a）：审批续跑时
// executeReAct 据此从批准参数合成 assistant 工具调用消息（P1）并置 SkipNextLLM，
// 使已批准的工具调用在审批通过后直接执行（修复续跑无限循环）。与
// WithApprovalResume 成对使用，由 buildApprovalResumeOptions 追加。
func WithApprovalResumePayload(payload ToolApprovalPayload) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ApprovalResumePayloads = []ToolApprovalPayload{payload}
	}
}

// WithApprovalResumePayloads 批量版 WithApprovalResumePayload：注入整批已批准/已
// 终态载荷（含终态条目），synthesizeApprovalResume 据此合成一条 assistant 消息含
// N 条 tool_call 直接进入工具节点。
func WithApprovalResumePayloads(payloads []ToolApprovalPayload) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ApprovalResumePayloads = payloads
	}
}

// WithPrecheckApprovals 装配批量审批预检钩子：makeToolNode 在每轮 MCP 工具调用
// 执行前调用，任一调用需审批时一次性创建全部审批并整轮暂停
// （BatchToolApprovalRequiredError，工具一个都不执行）。续跑合成消息前
// （synthesizeApprovalResume）会清空该钩子，防止对已审批工具重复发起审批。
func WithPrecheckApprovals(fn func(ctx context.Context, tools []port.ToolDefinition, calls []port.ToolCall) ([]port.ToolApprovalRequiredError, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.PrecheckApprovals = fn
	}
}

// WithRAGSearchFn injects a knowledge-base search function for the
// search_knowledge tool. viewerID is the end user scoping retrieval.
func WithRAGSearchFn(fn func(ctx context.Context, workspaces []string, query string, topK int, viewerID string) (string, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.RAGSearchFn = fn
	}
}

// WithRAGSearchFnWithEvidence injects the evidence-capable knowledge search
// function; the tool node prefers it over WithRAGSearchFn when set.
func WithRAGSearchFnWithEvidence(fn func(ctx context.Context, workspaces []string, query string, topK int, viewerID string) (port.RAGSearchEvidence, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.RAGSearchFnWithEvidence = fn
	}
}

// WithFactCheck enables hallucination checking for this execution. settings
// 的 EvidenceFn 留空：collectGraphResult 执行时用 RAGSearchFnWithEvidence
// 填充（per-execution 已带 tenant 权限上下文）。disabled/fail-closed 时
// collectGraphResult 直接跳过。
func WithFactCheck(settings *factcheck.Settings) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.FactCheck = settings
	}
}

// WithCaptureParameters toggles recording of effective parameter values on
// the execution span (stratum.params.* attributes).
func WithCaptureParameters(enabled bool) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.CaptureParameters = enabled
	}
}

// WithSystemPromptVersion records the content fingerprint of the system
// prompt applied to this execution.
func WithSystemPromptVersion(version string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.SystemPromptVersion = version
	}
}

// WithExtraTools appends extra tool definitions (from MCP servers and allowed skills) to AvailableTools.
func WithExtraTools(tools []port.ToolDefinition) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ExtraTools = tools
	}
}

// WithSkillCatalog sets immutable instruction-bundle snapshots for this run.
func WithSkillCatalog(catalog map[string]port.SkillActivation) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.SkillCatalog = catalog
	}
}

// WithActiveSkills pins the initial active skill activations for this run
// (scenario path). The slice is copied to avoid aliasing the caller's storage.
func WithActiveSkills(actives []port.SkillActivation) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.Actives = append([]port.SkillActivation(nil), actives...)
	}
}

func WithToolExecutionFn(fn port.ToolExecutionFn) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ToolExecutionFn = fn
	}
}

// WithConversationID sets the conversation ID for multi-turn history loading.
func WithConversationID(id string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.ConversationID = id
	}
}

// WithUserID sets the user ID for conversation history access control.
func WithUserID(id string) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.UserID = id
	}
}

// WithHistoryWindow sets the max number of history messages to load. n≤0 uses default (20).
func WithHistoryWindow(n int) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		if n > 0 {
			cfg.HistoryWindow = n
		}
	}
}

func WithTracePayloadStore(store port.TracePayloadStore) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.TracePayloadStore = store
	}
}

// WithEvolutionTraceMetadata attaches evaluation and rollout evidence to the root Agent span.
func WithEvolutionTraceMetadata(metadata EvolutionTraceMetadata) ExecutionOption {
	return func(cfg *ExecutionConfig) {
		cfg.EvolutionTrace = metadata
	}
}

// withAssistantRoleClass 记录本次执行解析的成员角色，供运维工具写路径
// 与执行 metrics 使用（8 工具对所有 agent 通用后的统一角色门禁来源）。
func withAssistantRoleClass(roleClass string) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.AssistantRoleClass = roleClass }
}

// WithOfficialDocsSearchFn attaches the in-process official docs search
// capability used by the system assistant tool.
func WithOfficialDocsSearchFn(fn func(context.Context, string) ([]domain.Citation, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.OfficialDocsSearchFn = fn }
}

// WithDiagnosticFn attaches the in-process tenant diagnostics capability used
// by the system assistant tool.
func WithDiagnosticFn(fn func(context.Context, []domain.DiagnosticArea) (domain.DiagnosticEvidence, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.DiagnosticFn = fn }
}

func withProposalCreateFn(fn func(context.Context, map[string]any) (domain.ResourceChangeProposalArtifact, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.ProposalCreateFn = fn }
}

// withResourceChangeApplyFn attaches the in-process direct-write capability
// (stratum_apply_resource_change) used by the system assistant tool.
func withResourceChangeApplyFn(fn func(context.Context, map[string]any) (domain.ApplyResult, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.ResourceChangeApplyFn = fn }
}

// WithListModelsFn attaches the in-process tenant model catalog capability
// (stratum_list_models) used by the system assistant tool.
func WithListModelsFn(fn func(context.Context) (map[string]any, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.ListModelsFn = fn }
}

// WithUpdateSystemModelFn attaches the in-process agent model update
// capability (stratum_update_model) shared by all agents. The role gate
// lives inside the attached closure.
func WithUpdateSystemModelFn(fn func(_ context.Context, model, agentID string) (map[string]any, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.UpdateSystemModelFn = fn }
}

// WithListAgentsFn attaches the in-process tenant agent catalog capability
// (stratum_list_agents) used by the system assistant tool.
func WithListAgentsFn(fn func(context.Context) (map[string]any, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.ListAgentsFn = fn }
}

// WithListMCPServersFn attaches the in-process MCP server catalog capability
// (stratum_list_mcp_servers) used by the system assistant tool. The wiring
// layer adapts the mcp context service through the port.MCPServerLister ACL;
// a nil lister leaves the tool fail-closed ("tool unavailable").
func WithListMCPServersFn(fn func(context.Context) (map[string]any, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.ListMCPServersFn = fn }
}

func withInternalToolResultGuard(fn func(any) (port.GuardedToolResult, error)) ExecutionOption {
	return func(cfg *ExecutionConfig) { cfg.InternalToolResultGuardFn = fn }
}

func agentExecutionAttributes(agentID, agentName string, agentType AgentType, cfg ExecutionConfig, maxContextTokens int) []attribute.KeyValue {
	resourceManifest := cfg.EvolutionTrace.ResourceManifest
	if resourceManifest == nil {
		resourceManifest = map[string]string{}
	}
	experimentAssignments := cfg.EvolutionTrace.ExperimentAssignments
	if experimentAssignments == nil {
		experimentAssignments = map[string]ExperimentAssignment{}
	}
	manifest, _ := json.Marshal(resourceManifest)
	assignments, _ := json.Marshal(experimentAssignments)
	attrs := []attribute.KeyValue{
		attribute.String("agent.id", agentID),
		attribute.String("agent.type", string(agentType)),
		attribute.String("conversation.id", cfg.ConversationID),
		attribute.String("stratum.tenant.id", cfg.TenantID),
		attribute.String("stratum.user.id", cfg.UserID),
		attribute.String("stratum.trace.id", cfg.TraceID),
		attribute.String("stratum.execution.id", cfg.ExecutionID),
		attribute.String("stratum.conversation.id", cfg.ConversationID),
		attribute.String("stratum.evaluation", fmt.Sprintf("%t", cfg.EvolutionTrace.Evaluation)),
		attribute.String("stratum.security_violation", fmt.Sprintf("%t", cfg.EvolutionTrace.SecurityViolation)),
		attribute.String("stratum.experiment.id", cfg.EvolutionTrace.ExperimentID),
		attribute.String("stratum.experiment.variant", cfg.EvolutionTrace.Variant),
		attribute.String("stratum.experiment.assignments", string(assignments)),
		attribute.String("stratum.resource.manifest", string(manifest)),
		attribute.String("opik.metadata.stratum.tenant_id", cfg.TenantID),
		attribute.String("opik.metadata.stratum.user_id", cfg.UserID),
		attribute.String("opik.metadata.stratum.trace_id", cfg.TraceID),
		attribute.String("opik.metadata.stratum.execution_id", cfg.ExecutionID),
		attribute.String("opik.metadata.stratum.conversation_id", cfg.ConversationID),
		attribute.String("opik.metadata.stratum.agent_id", agentID),
		attribute.String("opik.metadata.stratum.agent_name", agentName),
		attribute.String("opik.metadata.stratum.evaluation", fmt.Sprintf("%t", cfg.EvolutionTrace.Evaluation)),
		attribute.String("opik.metadata.stratum.security_violation", fmt.Sprintf("%t", cfg.EvolutionTrace.SecurityViolation)),
		attribute.String("opik.metadata.stratum.experiment_id", cfg.EvolutionTrace.ExperimentID),
		attribute.String("opik.metadata.stratum.experiment_variant", cfg.EvolutionTrace.Variant),
		attribute.String("opik.metadata.stratum.experiment_assignments", string(assignments)),
		attribute.String("opik.metadata.stratum.resource_manifest", string(manifest)),
		attribute.String("stratum.params.sha256", tunableConfigVersion(&cfg, maxContextTokens)),
	}
	// 窗口来源与解析值必须始终可观测（Spec 第 1 节），不随
	// CaptureParameters 门控；WindowSource 为空（preparation span）时不记录。
	if cfg.WindowSource != "" {
		attrs = append(attrs,
			attribute.String("stratum.window_source", cfg.WindowSource),
			attribute.Int("stratum.window_tokens", maxContextTokens),
		)
	}
	// Effective parameter values are privacy-gated by trace.capture_parameters;
	// the fingerprint above is always recorded so runs remain comparable.
	if cfg.CaptureParameters {
		attrs = append(attrs,
			attribute.Float64("stratum.params.temperature", float64(cfg.Temperature)),
			attribute.Int("stratum.params.max_tokens", cfg.MaxTokens),
			attribute.Int("stratum.params.max_context_tokens", maxContextTokens),
			attribute.Int("stratum.params.compaction_recent_groups", cfg.CompactionRecentGroups),
		)
		// reasoning_effort 只记录档位值（low/medium/high），不记录任何请求体。
		if cfg.ReasoningEffort != "" {
			attrs = append(attrs, attribute.String("stratum.params.reasoning_effort", cfg.ReasoningEffort))
		}
	}
	return attrs
}

// ApplyOptions applies options to the execution config
func (cfg *ExecutionConfig) ApplyOptions(opts []ExecutionOption) {
	for _, opt := range opts {
		opt(cfg)
	}
}

// BuildInitMessages constructs the initial LLM message slice from a system prompt and
// chat history. History is truncated to the most recent window messages.
// window ≤ 0 defaults to 20.
func BuildInitMessages(systemPrompt string, history []*ChatMessage, window int) []port.LLMMessage {
	if window <= 0 {
		window = constants.DefaultInitHistoryWindow
	}
	if len(history) > window {
		history = history[len(history)-window:]
	}
	msgs := make([]port.LLMMessage, 0, len(history)+1)
	if systemPrompt != "" {
		msgs = append(msgs, port.LLMMessage{Role: "system", Content: systemPrompt})
	}
	for _, m := range history {
		msgs = append(msgs, port.LLMMessage{Role: m.Role, Content: m.Content})
	}
	return msgs
}

// mergeTools combines built-in and extra tools, dropping duplicates (by name) with a warning.
// Built-in tools take priority: if an extra tool shares a name, it is silently dropped.
func mergeTools(builtins []port.ToolDefinition, extras []port.ToolDefinition, logger *zap.Logger) []port.ToolDefinition {
	seen := make(map[string]struct{}, len(builtins)+len(extras))
	out := make([]port.ToolDefinition, 0, len(builtins)+len(extras))
	for _, t := range builtins {
		seen[t.Name] = struct{}{}
		out = append(out, t)
	}
	for _, t := range extras {
		if _, dup := seen[t.Name]; dup {
			logger.Warn("tool name collision: extra tool shadowed by built-in, skipping",
				zap.String("tool_name", t.Name))
			continue
		}
		seen[t.Name] = struct{}{}
		out = append(out, t)
	}
	return out
}

// buildBuiltinTools constructs the agent's built-in tool definitions (knowledge search, memory recall).
func buildBuiltinTools(workspaceNames, workspaceDescs []string, hasRAG, hasMemory bool) []port.ToolDefinition {
	var tools []port.ToolDefinition
	if hasRAG {
		var b strings.Builder
		b.WriteString("Search one or more knowledge bases for relevant information. Available workspaces:\n")
		for i, n := range workspaceNames {
			desc := ""
			if i < len(workspaceDescs) {
				desc = workspaceDescs[i]
			}
			if desc != "" {
				b.WriteString("- " + n + ": " + desc + "\n")
			} else {
				b.WriteString("- " + n + "\n")
			}
		}
		tools = append(tools, port.ToolDefinition{
			Name:         "stratum_search_knowledge",
			Description:  strings.TrimRight(b.String(), "\n") + constants.AgentSearchKnowledgeRefusalInstruction,
			ProviderType: domain.ProviderTypeBuiltin,
			ProviderID:   "stratum_search_knowledge",
			CapabilityID: "stratum_search_knowledge",
			NodeType:     domain.ObservationTypeRetriever,
			InputSchema: jschema.Must(jschema.Object(
				jschema.RequiredProp("workspaces", jschema.Array(
					jschema.Must(jschema.Enum("", workspaceNames...)),
					1, 0, false, "Knowledge workspaces to search (one or more)",
				)),
				jschema.RequiredProp("query", jschema.String("Search query")),
				jschema.OptionalProp("top_k", jschema.Integer(nil, nil, "Number of results per workspace (1-20, default 5)")),
			)).Map(),
		})
	}
	if hasMemory {
		tools = append(tools, port.ToolDefinition{
			Name:         "stratum_recall_memory",
			Description:  "Search long-term memory for relevant past interactions, entities, and context. Use when you need to recall information from previous conversations.",
			ProviderType: domain.ProviderTypeBuiltin,
			ProviderID:   "stratum_recall_memory",
			CapabilityID: "stratum_recall_memory",
			NodeType:     domain.ObservationTypeMemory,
			InputSchema: jschema.Must(jschema.Object(
				jschema.RequiredProp("query", jschema.String("Search query to find relevant memories")),
				jschema.OptionalProp("limit", jschema.Integer(nil, nil, "Max results (1-20, default 5)")),
			)).Map(),
		})
	}
	tools = append(tools, port.ToolDefinition{
		Name:         "stratum_continue_reasoning",
		Description:  "Request another reasoning turn to continue chain-of-thought before calling other tools or producing a final answer. Use when you need more reasoning steps.",
		ProviderType: domain.ProviderTypeBuiltin,
		ProviderID:   "stratum_continue_reasoning",
		CapabilityID: "stratum_continue_reasoning",
		NodeType:     domain.ObservationTypeAgent,
		InputSchema:  jschema.Must(jschema.Object()).Map(),
	})
	// stratum_delegate：始终注册（定义恒定，不随 agent 漂移）。走 guard 全链路，
	// 故 Metadata 显式声明 risk_level=read + policy_resolved=true（与 MCP 工具同构，
	// AuthorizeTool 走无审批路径）。启停由 buildReActInitState 按 DelegateEnabled
	// 过滤工具面 + execDelegateTool fail-closed 双保险。
	tools = append(tools, port.ToolDefinition{
		Name:         agentgraph.StratumDelegateToolName,
		Description:  "Delegate a well-scoped sub-task to an isolated sub-agent that reuses your current configuration (same model, system prompt, tools, and knowledge). The sub-agent runs in a separate context window and returns a concise summary. Use it when a sub-task has clear boundaries and would benefit from isolated reasoning without polluting your context.",
		ProviderType: domain.ProviderTypeBuiltin,
		ProviderID:   agentgraph.StratumDelegateToolName,
		CapabilityID: agentgraph.StratumDelegateToolName,
		NodeType:     domain.ObservationTypeTool,
		InputSchema: jschema.Must(jschema.Object(
			jschema.RequiredProp("goal", jschema.StringRange(1, constants.DelegateMaxGoalRunes, "Self-contained goal for the sub-agent")),
			jschema.OptionalProp("max_steps", jschema.Integer(jschema.Ptr(1), jschema.Ptr(constants.MaxDelegateMaxLLMSteps), "Max reasoning steps for the sub-agent (default from agent config, capped at 10)")),
		)).Map(),
		Metadata: map[string]any{"risk_level": "read", "policy_resolved": true},
	})
	return tools
}

func buildExecutionArtifacts(toolArtifacts []domain.SystemAssistantToolArtifact, profileVersion string) []domain.ExecutionArtifact {
	if len(toolArtifacts) == 0 {
		return []domain.ExecutionArtifact{}
	}
	citations := make([]domain.Citation, 0)
	seenCitations := make(map[string]struct{})
	hasReport := false
	out := make([]domain.ExecutionArtifact, 0, 3)
	for _, artifact := range toolArtifacts {
		out = append(out, resourceChangeExecutionArtifacts(artifact, profileVersion)...)
		citations = appendUniqueExecutionCitations(citations, seenCitations, artifact.Citations)
		if artifact.Evidence != nil || artifact.Tool == "stratum_diagnose_tenant" || artifact.ErrorCode != "" {
			hasReport = true
		}
	}
	if len(citations) > 0 {
		out = append(out, domain.ExecutionArtifact{Type: "citations", ProfileVersion: profileVersion, Citations: citations})
	}
	if hasReport {
		out = append(out, domain.ExecutionArtifact{Type: "diagnostic_report", ProfileVersion: profileVersion, DiagnosticReport: domain.BuildDiagnosticReport(toolArtifacts)})
	}
	return boundExecutionArtifactsJSON(out)
}

// resourceChangeExecutionArtifacts maps proposal and direct-apply evidence
// onto their own execution artifact types.
func resourceChangeExecutionArtifacts(artifact domain.SystemAssistantToolArtifact, profileVersion string) []domain.ExecutionArtifact {
	out := make([]domain.ExecutionArtifact, 0, 2)
	if artifact.Proposal != nil {
		proposal := *artifact.Proposal
		out = append(out, domain.ExecutionArtifact{Type: "resource_change_proposal", ProfileVersion: profileVersion, ResourceChangeProposal: &proposal})
	}
	if artifact.Tool == domain.SystemAssistantToolApplyResourceChange && artifact.DirectApply != nil {
		apply := *artifact.DirectApply
		out = append(out, domain.ExecutionArtifact{Type: "resource_change_direct_apply", ProfileVersion: profileVersion, DirectApply: &apply})
	}
	return out
}

// appendUniqueExecutionCitations dedups bounded citations within the catalog cap.
func appendUniqueExecutionCitations(dst []domain.Citation, seen map[string]struct{}, values []domain.Citation) []domain.Citation {
	for _, citation := range domain.BoundCitations(values) {
		key := citation.DocumentID + "\x00" + citation.Section + "\x00" + citation.URL
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if len(dst) < constants.SystemAssistantCitationMaxCount {
			dst = append(dst, citation)
		}
	}
	return dst
}

func boundExecutionArtifactsJSON(artifacts []domain.ExecutionArtifact) []domain.ExecutionArtifact {
	for {
		raw, err := json.Marshal(artifacts)
		if err == nil && len(raw) <= constants.SystemAssistantToolMaxJSONBytes {
			return artifacts
		}
		changed := false
		for i := range artifacts {
			report := artifacts[i].DiagnosticReport
			if report == nil {
				continue
			}
			switch {
			case len(report.Facts) > 0:
				report.Facts = report.Facts[:len(report.Facts)-1]
				changed = true
			case len(report.EvidenceGaps) > 0:
				report.EvidenceGaps = report.EvidenceGaps[:len(report.EvidenceGaps)-1]
				changed = true
			case len(report.Citations) > 0:
				report.Citations = report.Citations[:len(report.Citations)-1]
				changed = true
			}
		}
		if !changed {
			return []domain.ExecutionArtifact{{Type: "diagnostic_report", ProfileVersion: artifacts[0].ProfileVersion, DiagnosticReport: &domain.DiagnosticReport{Facts: []domain.DiagnosticFact{}, Inferences: []string{}, EvidenceGaps: []domain.EvidenceGap{{Source: "artifact_aggregate", Code: "truncated"}}, RecommendedActions: []string{}, Citations: []domain.Citation{}, Steps: []domain.DiagnosticStep{{Tool: "artifact_aggregate", Outcome: "error", ErrorCode: "truncated"}}}}}
		}
	}
}

// retainRunningError reports whether a run error must keep the checkpoint in
// its current state rather than terminating it:
//   - context.Canceled / DeadlineExceeded: client disconnect or per-call timeout.
//     The execution may still be resumed via GetActiveExecution (refreshed page
//     or another device); the freshness window reclaims stale zombies later.
//   - port.ToolApprovalRequiredError: the checkpoint is already waiting_approval;
//     terminating it would orphan the in-flight approval request.
func retainRunningError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var approvalErr *port.ToolApprovalRequiredError
	if errors.As(err, &approvalErr) {
		return true
	}
	// 批量审批：整轮暂停等待人工处理，checkpoint 保留 waiting_approval。
	var batchErr *port.BatchToolApprovalRequiredError
	return errors.As(err, &batchErr)
}

// isResumableCheckpoint reports whether a checkpoint with status s can be
// resumed (running or paused).
func isResumableCheckpoint(s string) bool {
	return s == "running" || s == "paused"
}

// resumeFromCheckpoint restores execution state from the latest checkpoint.
func (a *BaseAgent) resumeFromCheckpoint(
	ctx context.Context, ec agentExecContext, msgs []port.LLMMessage,
) (*domain.Plan, []port.SkillActivation, []port.LLMMessage) {
	if a.CheckpointStore == nil || ec.cfg.ExecutionID == "" {
		return nil, nil, msgs
	}
	resumeCp, err := a.CheckpointStore.GetLatest(ctx, ec.cfg.TenantID, ec.cfg.ExecutionID)
	// waiting_approval 仅在审批续跑（WithApprovalResume 注入批准 ID）时恢复：
	// 该 checkpoint 的 messages snapshot 为空、runtime 仅存 approval_id，
	// restoreMessages/restorePlanCheckpointState 落回 base，即从 chat 历史 +
	// 本轮 query 全量重跑（H1 语义，与 ResumeToolApproval 一致）。
	if err != nil || resumeCp == nil {
		return nil, nil, msgs
	}
	if !isResumableCheckpoint(resumeCp.Status) &&
		(resumeCp.Status != domain.ExecStatusWaitingApproval || len(ec.cfg.ApprovalResumeIDs) == 0) {
		return nil, nil, msgs
	}
	a.Logger.Info("agent: resuming from checkpoint",
		zap.String("checkpoint_id", resumeCp.ID),
		zap.String("execution_id", ec.cfg.ExecutionID),
		zap.Int("step_index", resumeCp.StepIndex),
	)
	msgs = restoreMessages(resumeCp.MessagesSnapshotJSON, msgs)
	plan, actives := restorePlanCheckpointState(resumeCp.RuntimeStateJSON, ec.cfg.SkillCatalog)
	return plan, actives, msgs
}

// restoreMessages 合并 checkpoint 快照与 chat_messages 重建的 base:
// v2 增量快照(工具维度)append 到 base 末尾,旧二进制全量快照整体替换。
func restoreMessages(raw json.RawMessage, fallback []port.LLMMessage) []port.LLMMessage {
	return agentgraph.MergeToolMessagesSnapshot(raw, fallback)
}

// skillRevisionHashes extracts skillID → revision hash from the skill catalog.
func skillRevisionHashes(catalog map[string]port.SkillActivation) map[string]string {
	if len(catalog) == 0 {
		return nil
	}
	out := make(map[string]string, len(catalog))
	for id, act := range catalog {
		out[id] = act.RevisionID
	}
	return out
}

// fingerprintAttributes converts an ExecutionFingerprint into OTEL span attributes.
func fingerprintAttributes(fp *domain.ExecutionFingerprint) []attribute.KeyValue {
	if fp == nil {
		return nil
	}
	attrs := []attribute.KeyValue{
		attribute.String("stratum.fingerprint.model_resolved", fp.ModelResolved),
		attribute.String("stratum.fingerprint.prompt_version", fp.PromptVersion),
		attribute.String("stratum.fingerprint.content_hash", fp.ContentHash()),
		attribute.Int("stratum.fingerprint.ab_bucket", fp.ABBucket),
	}
	if fp.ConfigVersion != "" {
		attrs = append(attrs, attribute.String("stratum.fingerprint.config", fp.ConfigVersion))
	}
	if len(fp.ModelRoutedVia) > 0 {
		b, _ := json.Marshal(fp.ModelRoutedVia)
		attrs = append(attrs, attribute.String("stratum.fingerprint.model_routed_via", string(b)))
	}
	if len(fp.SkillRevisions) > 0 {
		b, _ := json.Marshal(fp.SkillRevisions)
		attrs = append(attrs, attribute.String("stratum.fingerprint.skill_revisions", string(b)))
	}
	if len(fp.TunableSnapshot) > 0 {
		b, _ := json.Marshal(fp.TunableSnapshot)
		attrs = append(attrs, attribute.String("stratum.fingerprint.tunable_snapshot", string(b)))
	}
	return attrs
}

func restorePlanCheckpointState(raw json.RawMessage, catalog map[string]port.SkillActivation) (*domain.Plan, []port.SkillActivation) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoded, err := agentgraph.DecodePlanCheckpoint(raw)
	if err != nil {
		return nil, nil
	}
	var plan *domain.Plan
	if decoded.Plan != nil {
		plan = decoded.Plan
	}
	if len(decoded.ActiveSkills) > 0 {
		// 数组优先；逐条校验 revision、跳过 catalog 外条目并按 SkillID 去重。
		return plan, restoreActivesFromRefs(decoded.ActiveSkills, catalog)
	}
	return plan, restoreLegacyActiveSkill(decoded, catalog)
}

// restoreActivesFromRefs 将 checkpoint 中的 skill refs 还原为 catalog 中的激活
// 快照。revision 不匹配或不在 catalog 的条目跳过；重复 SkillID 保留首个。
func restoreActivesFromRefs(refs []agentgraph.CheckpointSkillRef, catalog map[string]port.SkillActivation) []port.SkillActivation {
	seen := map[string]struct{}{}
	var actives []port.SkillActivation
	for _, ref := range refs {
		activation, ok := catalog[ref.SkillID]
		if !ok || (ref.RevisionID != "" && activation.RevisionID != ref.RevisionID) {
			continue
		}
		if _, dup := seen[activation.SkillID]; dup {
			continue
		}
		seen[activation.SkillID] = struct{}{}
		actives = append(actives, activation)
	}
	return actives
}

// restoreLegacyActiveSkill 回退旧版单条 checkpoint 字段，供旧 payload 恢复。
func restoreLegacyActiveSkill(decoded agentgraph.PlanCheckpointPayload, catalog map[string]port.SkillActivation) []port.SkillActivation {
	if decoded.ActiveSkillID == "" {
		return nil
	}
	activation, ok := catalog[decoded.ActiveSkillID]
	if !ok || (decoded.ActiveSkillRevisionID != "" && activation.RevisionID != decoded.ActiveSkillRevisionID) {
		return nil
	}
	return []port.SkillActivation{activation}
}
