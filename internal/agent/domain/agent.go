// Package domain holds canonical agent context types and sentinels.
//
// This file is the single source of truth for agent / chat / execution
// data shapes shared across application + infrastructure layers.
// Application keeps thin type aliases (`type X = domain.X`) so existing
// call-sites remain source-compatible after the layering refactor.

package domain

import (
	"encoding/json"
	"time"
)

// AgentType enumerates supported agent architectures.
type AgentType string

const (
	ReActAgent       AgentType = "react"
	CoTAgent         AgentType = "cot"
	PlanningAgent    AgentType = "planning"
	ToolCallingAgent AgentType = "tool_calling"
	RAGAgent         AgentType = "rag"
	SwarmAgent       AgentType = "swarm"
)

// AgentCapability declares what an agent can do.
type AgentCapability struct {
	Name        string
	Description string
	CanUseTools bool
	CanPlan     bool
	CanReason   bool
}

// AgentConfig holds the persisted shape of an agent.
type AgentConfig struct {
	ID                             string
	Name                           string
	Type                           AgentType
	Description                    string
	SystemPrompt                   string
	LLMModel                       string
	MaxIterations                  int
	AllowedSkills                  []string
	MCPToolIDs                     []string
	Capabilities                   []AgentCapability
	KnowledgeWorkspaceIDs          []string
	KnowledgeWorkspaceNames        []string
	KnowledgeWorkspaceDescriptions []string
	MaxContextTokens               int
	// Temperature 0 means unset: the gateway/provider default applies.
	Temperature float32
	// ReasoningEffort is the sampling effort tier: "" (unset) | low | medium |
	// high. Empty means unset: the gateway/provider default applies. Unlike
	// Temperature, "" is a *sentinel* not a zero — merge paths skip it so an
	// old client PUT never erases a persisted effort.
	ReasoningEffort string
	// MaxTokens 0 means unset: no explicit output cap.
	MaxTokens int
	// MaxTokensPerExecution is the execution-wide LLM token budget. 0 = unlimited.
	MaxTokensPerExecution int
	// MemoryParameters holds the memory.* resource-scope registry keys
	// (dotted form, e.g. "memory.max_facts_per_extraction"). Unlike the bare
	// sampling keys above they are stored as-is and consumed by the memory
	// pipeline per agent. Absent key = unset (definition default applies).
	MemoryParameters map[string]any
	MemoryScope      string
	// StuckThreshold > 0 enables lazy planning: after this many LLM rounds with
	// no final answer the agent transitions to Reflect→Plan→Execute.
	// 0 disables the feature (pure ReAct).
	StuckThreshold int
	// CreatedBy is the user who created the agent ("" for historical/platform rows).
	CreatedBy string
	// ---- stratum_delegate sub-agent dispatch (Step 0) ----
	// DelegateEnabled 是否允许该 agent 将子任务委托给隔离子 agent。DB 默认 false：
	// 委托是显式能力，存量 agent 默认关闭，管理员在编辑页按 agent 开启，避免
	// 未评估风险的 agent 静默获得子 agent 派发能力。
	DelegateEnabled bool
	// DelegateMaxDepth 委托深度上限（0=unset → 回落 DefaultDelegateMaxDepth，再
	// clamp 到 MaxDelegateDepth 全局硬上限）。默认 1 = "仅主→子一层"。
	DelegateMaxDepth int
	// DelegateDefaultMaxSteps 子循环未显式传 max_steps 时的默认推理步数（0=unset
	// → 回落 DefaultDelegateMaxLLMSteps）。
	DelegateDefaultMaxSteps int
}

// PlanStep is a single goal inside an agent execution plan.
type PlanStep struct {
	Goal      string   `json:"goal"`
	HintTools []string `json:"hint_tools,omitempty"`
	// DependsOn lists the zero-based indices of steps that must complete before
	// this step starts. Empty means the step can run in the first wave (parallel).
	DependsOn []int `json:"depends_on,omitempty"`
}

// StepResult captures the outcome of executing one PlanStep.
type StepResult struct {
	StepIndex int    `json:"step_index"`
	Goal      string `json:"goal"`
	Summary   string `json:"summary"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// PlanRuntimeState is serialised into AgentExecutionCheckpoint.RuntimeStateJSON.
type PlanRuntimeState struct {
	Phase             string       `json:"phase"`
	ReflectionSummary string       `json:"reflection_summary"`
	Plan              []PlanStep   `json:"plan"`
	PlanTemplateID    string       `json:"plan_template_id,omitempty"`
	CurrentStepIndex  int          `json:"current_step_index"`
	StepResults       []StepResult `json:"step_results"`
}

// ChatConversation is a named conversation thread between a user and an agent.
type ChatConversation struct {
	ID        string
	AgentID   string
	UserID    string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
	DeletedAt *time.Time
}

// ChatMessage is a single message in a conversation.
type ChatMessage struct {
	ID             string
	ConversationID string
	Role           string // "user" | "assistant"
	Content        string
	StepsJSON      json.RawMessage
	IsError        bool
	CreatedAt      time.Time
	UserID         string
	AgentID        string
	MemoryScope    string
	SkipOutbox     bool
	Visibility     string
	Artifacts      []ExecutionArtifact
	// Sources are the RAG citation sources an assistant answer was grounded on,
	// persisted to chat_messages.sources_json and replayed on history load.
	// Serialized camelCase via RAGSearchSource json tags — identical to the live
	// SSE done frame's sources, so live and replay render the same cards.
	Sources []RAGSearchSource
	// TraceID links the message to its agent execution trace. Persisted so
	// the evaluation case generator can pair (query, response) with
	// evaluation_feedback rows; empty for manual messages.
	TraceID string
}

const (
	ChatMessageVisibilityUser     = "user"
	ChatMessageVisibilityInternal = "internal"
)

// CompactionCoverage 是一次组装侧加载时从共享压缩摘要存储读到的覆盖信息：
// 该会话已有多少历史被压成摘要、覆盖游标落在哪条 chat_messages.id 之后。
// covered_until 为空串表示尚无覆盖（首次压缩）。
type CompactionCoverage struct {
	CoveredUntil string // 已压缩段最后一条消息 id（chat_messages.id，UUID v7 时间有序）
	Summary      string // 覆盖段的结构化摘要正文（§3.4 格式）
	Version      int
}

// CompactionSegment 是一次组装侧压缩后要回写共享存储的段信息。
type CompactionSegment struct {
	ConversationID string
	CoveredUntil   string // 本次被压掉的最后一条消息 id（单调推进）
	Summary        string // 结构化摘要正文
	SourceStart    string
	SourceEnd      string
	TokenCount     int
}

const (
	ExecStatusSuccess         = "success"
	ExecStatusError           = "error"
	ExecStatusWaitingApproval = "waiting_approval"
)

// ExecutionRecord is an agent execution history entry.
type ExecutionRecord struct {
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
	CostUSD       float64
	DurationMs    int
	CreatedAt     time.Time
}

// ListOptions controls pagination for execution history queries.
type ListOptions struct {
	Page     int
	PageSize int
	UserID   string
}

// Message represents a single message in an agent's in-memory conversation history.
type Message struct {
	Role       string
	Content    string
	Timestamp  time.Time
	Metadata   map[string]interface{}
	TokenCount int
}

// Thought represents a single reasoning step in Chain-of-Thought execution.
type Thought struct {
	Step        int
	Observation string
	Thought     string
}

// ToolCall represents a structured tool invocation and its result.
type ToolCall struct {
	ToolName string
	Input    map[string]interface{}
	Output   interface{}
	Error    error
	Duration time.Duration
}

const (
	ToolTraceStatusSuccess = "success"
	ToolTraceStatusError   = "error"

	ToolTypeReasoning     = "reasoning"
	ToolTypeBuiltinRAG    = "builtin_rag"
	ToolTypeBuiltinMemory = "builtin_memory"
	ToolTypeSkill         = "skill"
	ToolTypeMCP           = "mcp"
	ToolTypeInternal      = "internal"

	RunTypeAgent         = "agent"
	RunTypeWorkflow      = "workflow"
	RunTypeSkillTest     = "skill_test"
	RunTypeScheduledTask = "scheduled_task"

	ObservationTypeAgent      = "agent"
	ObservationTypeLLM        = "llm"
	ObservationTypeTool       = "tool"
	ObservationTypeMCP        = "mcp"
	ObservationTypeSkill      = "skill"
	ObservationTypeRetriever  = "retriever"
	ObservationTypeMemory     = "memory"
	ObservationTypeWorkflow   = "workflow"
	ObservationTypeCheckpoint = "checkpoint"
	ObservationTypeCustom     = "custom"

	ProviderTypeSkill    = "skill"
	ProviderTypeMCP      = "mcp"
	ProviderTypeLLM      = "llm"
	ProviderTypeBuiltin  = "builtin"
	ProviderTypeInternal = "internal"
	ProviderTypeHTTP     = "http"
	ProviderTypeBrowser  = "browser"
	ProviderTypeShell    = "shell"

	TraceEventAgentStarted  = "agent.execution_started"
	TraceEventLLMRequest    = "llm.request"
	TraceEventLLMResponse   = "llm.response"
	TraceEventToolStarted   = "tool.call_started"
	TraceEventToolFinished  = "tool.call_finished"
	TraceEventToolFailed    = "tool.call_failed"
	TraceEventFinalAnswer   = "agent.final_answer"
	TraceEventAgentFinished = "agent.execution_finished"
	TraceEventAgentFailed   = "agent.execution_failed"
)

// ToolObservation captures a single tool invocation for audit/debug storage
// and for producing a compact context summary for the next conversation turn.
type ToolObservation struct {
	ID             string         `json:"id"`
	TraceID        string         `json:"trace_id"`
	ExecutionID    string         `json:"execution_id"`
	ConversationID string         `json:"conversation_id"`
	AgentID        string         `json:"agent_id"`
	UserID         string         `json:"user_id"`
	StepIndex      int            `json:"step_index"`
	ToolCallID     string         `json:"tool_call_id"`
	ToolName       string         `json:"tool_name"`
	ToolType       string         `json:"tool_type"`
	ProviderType   string         `json:"provider_type"`
	ProviderID     string         `json:"provider_id"`
	ServerID       string         `json:"server_id"`
	CapabilityID   string         `json:"capability_id"`
	Arguments      map[string]any `json:"arguments"`
	RawResult      any            `json:"raw_result"`
	RawText        string         `json:"raw_text"`
	Summary        string         `json:"summary"`
	Status         string         `json:"status"`
	// Outcome 携带 MCP 执行结果分类（"" / not_sent / definite_failure /
	// outcome_unknown），供幻觉防护对账区分"确凿失败"与"传输结果未知"；
	// trace 回放路径不填充，omitempty 保证旧序列化零差异。
	Outcome      string         `json:"outcome,omitempty"`
	ErrorMessage string         `json:"error_message"`
	LatencyMs    int64          `json:"latency_ms"`
	RawTruncated bool           `json:"raw_truncated"`
	Metadata     map[string]any `json:"metadata"`
	StartedAt    time.Time      `json:"started_at"`
	EndedAt      time.Time      `json:"ended_at"`
	CreatedAt    time.Time      `json:"created_at"`
}

// AgentTraceEvent is an append-only execution trajectory event. Large tool raw
// IO is linked through ToolTraceID instead of duplicated here.
type AgentTraceEvent struct {
	ID               string    `json:"id"`
	TraceID          string    `json:"trace_id"`
	ExecutionID      string    `json:"execution_id"`
	ConversationID   string    `json:"conversation_id"`
	AgentID          string    `json:"agent_id"`
	UserID           string    `json:"user_id"`
	RunType          string    `json:"run_type"`
	ObservationType  string    `json:"observation_type"`
	EventType        string    `json:"event_type"`
	StepIndex        int       `json:"step_index"`
	SpanName         string    `json:"span_name"`
	ParentEventID    string    `json:"parent_event_id"`
	Status           string    `json:"status"`
	Input            any       `json:"input"`
	Output           any       `json:"output"`
	Summary          string    `json:"summary"`
	ErrorMessage     string    `json:"error_message"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	LatencyMs        int64     `json:"latency_ms"`
	ToolTraceID      string    `json:"tool_trace_id"`
	ProviderType     string    `json:"provider_type"`
	ProviderID       string    `json:"provider_id"`
	NodeID           string    `json:"node_id"`
	NodeType         string    `json:"node_type"`
	WorkflowID       string    `json:"workflow_id"`
	WorkflowVersion  string    `json:"workflow_version"`
	SequenceNo       int64     `json:"sequence_no"`
	Metadata         any       `json:"metadata"`
	OTelTraceID      string    `json:"otel_trace_id"`
	OTelSpanID       string    `json:"otel_span_id"`
	StartedAt        time.Time `json:"started_at"`
	EndedAt          time.Time `json:"ended_at"`
	CreatedAt        time.Time `json:"created_at"`
}

// AgentExecutionCheckpoint is the resumable runtime snapshot for a long-running
// agent execution. It is not used as audit history; trace events remain
// append-only history.
type AgentExecutionCheckpoint struct {
	ID                     string          `json:"id"`
	ExecutionID            string          `json:"execution_id"`
	TraceID                string          `json:"trace_id"`
	ConversationID         string          `json:"conversation_id"`
	AgentID                string          `json:"agent_id"`
	UserID                 string          `json:"user_id"`
	CurrentNode            string          `json:"current_node"`
	StepIndex              int             `json:"step_index"`
	MessagesSnapshotJSON   json.RawMessage `json:"messages_snapshot_json"`
	PendingToolCallsJSON   json.RawMessage `json:"pending_tool_calls_json"`
	CompletedToolCallsJSON json.RawMessage `json:"completed_tool_calls_json"`
	RuntimeStateJSON       json.RawMessage `json:"runtime_state_json"`
	Status                 string          `json:"status"`
	ResumeReason           string          `json:"resume_reason"`
	// UserQuery is the user query of the current execution round. Written once
	// by ensureInitialCheckpoint (and by approval wait rows via Upsert ON
	// CONFLICT retention) so a session with no first-step checkpoint yet can
	// still be discovered by GetActiveExecution and resumed verbatim. It is
	// never updated per-step — plan checkpoints preserve the original round.
	UserQuery string `json:"user_query"`
	// RunGeneration is the resume-generation fence. Every continuation entry
	// atomically increments it (AdvanceRunGeneration) so two tabs/devices
	// racing to resume the same execution cannot both win — the loser's CAS
	// fails and it reports "already running elsewhere".
	RunGeneration int       `json:"run_generation"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// RuleBlock 记录一次规则护栏命中（拦截或仅检测，O4 检测恒开：denylist 命中不论 enabled
// 均写入 ctx 累积器供观测产出 rule 信号）。
type RuleBlock struct {
	Rule    string `json:"rule"`
	Tool    string `json:"tool"`
	Message string `json:"message"`
}

// AgentResult holds the output of a completed agent execution.
type AgentResult struct {
	AgentID                string
	Input                  string
	Output                 string
	Thoughts               []Thought
	ToolCalls              []ToolCall
	ToolObservations       []ToolObservation
	TraceEvents            []AgentTraceEvent
	Steps                  int
	TokensUsed             int
	CostUSD                float64
	Duration               time.Duration
	Error                  error
	Metadata               map[string]interface{}
	AssistantToolArtifacts []SystemAssistantToolArtifact
	Artifacts              []ExecutionArtifact
	// ModelResolved 是本次执行最后一次 LLM 调用实际成功的模型名（fallback
	// 降级后与配置模型不同）；ModelRoutedVia 是实际尝试过的模型链。
	ModelResolved  string
	ModelRoutedVia []string
	// Sources are the citation sources surfaced in the chat UI, aggregated
	// from retrieval evidence during execution (deduplicated by chunk ID,
	// capped at constants.MaxAgentResultSources). Empty when no knowledge
	// search ran or nothing matched.
	Sources []RAGSearchSource
	// TerminatedBy 记录业务终止原因（如 cost_budget）；空 = 正常完成。
	// 业务终止仍返回已产出部分结果，不进入错误路径。
	TerminatedBy string
	// Degraded 标记本次执行因工具连续校验失败进入降级：最终回答已按降级
	// 指令生成，不得声称完成了未验证的操作。子节点止损经共享 map 传播到
	// 父图，collectGraphResult 从非空 StopLossTools 推导。
	Degraded bool
	// DegradeReason 是降级原因的固定枚举（如 "tool_stop_loss:<tool>"）；
	// 永不含内部标识或原始错误正文，可安全透出到前端。
	DegradeReason string
	// FactCheck 是幻觉校验的展示型报告（advisory）。开关关或校验失败/超时/无
	// 证据时为 nil，handler 透出 fact_check 时 omitempty 不出现。只展示，不进
	// 工具决策、不写库为 ground truth。
	FactCheck *FactCheckReport
	// NoAnswer 是知识检索无答案的结构化信号（SSE done payload noAnswer）。
	// 工具路径检索全空时填充，nil = 本次执行未触发无答案（无知识检索或至少
	// 一个 workspace 有命中）。
	NoAnswer *NoAnswerInfo
}

// NoAnswerReason 是知识检索无答案信号的固定枚举（值与 knowledge 侧一一
// 对应，定义在 pkg/constants 单一事实源；agent 侧仅透传）。
type NoAnswerReason string

// NoAnswerInfo 是知识检索无答案的结构化信号，序列化为 done payload 的
// noAnswer 字段（PascalCase，与 Sources 一致）。
type NoAnswerInfo struct {
	Reason         NoAnswerReason
	RetrievedCount int     // 阈值过滤前候选数
	FilteredCount  int     // 阈值过滤掉的条数
	BestScore      float32 // 池内最高分（阈值过滤前采集）
	Retried        bool    // 触底自动重试标记（P3 使用）
	RewrittenQuery string  // 重试改写后的 query（P3 使用）
	Detail         string  // 人读摘要，仅固定模板
}

// FactCheckInput 是幻觉校验的输入：agent 最终输出 + 可检索的 knowledge
// workspaces + 请求者。ViewerID 空 = 不校验（RAGService 的 SkipAccessCheck
// 对 SystemActor 整体旁路 D2 门控，必须显式 fail-closed 守卫）。
type FactCheckInput struct {
	Output     string
	Workspaces []string
	ViewerID   string
	// ToolObservations 是本次执行的内存工具调用记录（by ToolCallID），对账器
	// 据其核验最终输出中的 <tool_ref:ID> 声称；trace 回放路径不填充。
	ToolObservations []ToolObservation
}

// FactCheckReport 是幻觉校验结果（advisory，只展示）。Checked 表示本次确实
// 执行了校验；IsValid 由 verdict 推导（存在 CONTRADICTED/UNSUPPORTED 即无效）；
// RiskPoints 是风险 claim 计数。
type FactCheckReport struct {
	Checked    bool           `json:"checked"`
	Claims     []ClaimVerdict `json:"claims"`
	IsValid    bool           `json:"isValid"`
	RiskPoints int            `json:"riskPoints"`
	// ToolReferences 是对账结果：最终输出中的每个 <tool_ref:ID> 引用 vs
	// ToolObservation 记录的核验判定。Unverified 字段标记含副作用声称但未带
	// 引用的句子（advisory 软标记，不硬判假话）。omitempty 保持旧序列化零差异。
	ToolReferences   []ToolReferenceVerdict `json:"toolReferences,omitempty"`
	UnverifiedCount  int                    `json:"unverifiedCount,omitempty"`
	UnverifiedClaims []string               `json:"unverifiedClaims,omitempty"`
}

// ToolReferenceVerdict 是单个工具引用声称的对账判定。Classification 为五态枚举
// verified / verification_failed / outcome_unknown / invalid_reference / unverified；
// Risk ∈ [0,5]，verification_failed 与 invalid_reference 会使整体 IsValid=false。
type ToolReferenceVerdict struct {
	ClaimText      string `json:"claimText,omitempty"`
	ToolName       string `json:"toolName,omitempty"`
	ToolCallID     string `json:"toolCallId,omitempty"`
	Reference      string `json:"reference,omitempty"`
	Status         string `json:"status,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	Classification string `json:"classification"`
	Risk           int    `json:"risk"`
}

// ClaimVerdict 是单个 claim 的 LLM-as-Judge 判定。Verdict 为固定枚举
// SUPPORTED|CONTRADICTED|UNSUPPORTED；Risk ∈ [0,5]，越高越可疑。
type ClaimVerdict struct {
	Text    string `json:"text"`
	Verdict string `json:"verdict"`
	Risk    int    `json:"risk"`
}

// RAGSearchSource is per-chunk retrieval provenance for the chat UI. Score
// is only meaningful when HasScore is true (vector retrieval); keyword-mode
// results carry no score. DocumentTitle and Snippet are display metadata for
// citations; the knowledge side has already applied the viewer's document
// whitelist before emitting any source. JSON tags serialize the chat-source
// payload (SSE done event) in camelCase.
type RAGSearchSource struct {
	WorkspaceID   string  `json:"workspaceId"`
	WorkspaceName string  `json:"workspaceName"`
	ChunkID       string  `json:"chunkId"`
	DocumentID    string  `json:"documentId"`
	DocumentTitle string  `json:"documentTitle"`
	Snippet       string  `json:"snippet"`
	ParentContent string  `json:"parentContent,omitempty"` // whole enclosing section (Parent-Child); empty when leaf has no parent
	Score         float64 `json:"score,omitempty"`
	HasScore      bool    `json:"hasScore,omitempty"`
}

// AgentState tracks mutable execution progress during a single run.
type AgentState struct {
	StepsTaken int
	Thoughts   []Thought
	ToolCalls  []ToolCall
	TokensUsed int
}
