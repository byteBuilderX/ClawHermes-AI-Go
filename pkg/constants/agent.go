package constants

import "time"

const (
	// DefaultAgentContextTokens 是"模型窗口未知 + explicit=0"时的上下文预算兜底
	// （ResolveAgentWindow default 分支）。0 = 自动按模型窗口解析：窗口 known 时走
	// 0.85×window，未知时回落本常量。32768 保证兜底不至于像旧 8000 那样与
	// outputReserve(4096) 形成预算账本矛盾（usable 远小于输出预留 → 智谱 400）。
	DefaultAgentContextTokens = 32768
	MinSystemPromptTokens     = 200
	// MinAgentMaxIterations / MaxAgentMaxIterations bound the per-agent max
	// iteration count. Single source of truth shared by the frontend slider,
	// AgentRevision.Validate, the HTTP create/update validation, the parameter
	// registry, and the system assistant's propose/apply payload schema.
	MinAgentMaxIterations                        = 1
	MaxAgentMaxIterations                        = 90
	DefaultInitHistoryWindow                     = 20  // BuildInitMessages fallback window
	DefaultContextHistoryWindow                  = 50  // BuildContextMessages fallback window
	MemoryBudgetRatio                            = 0.3 // fraction of remaining budget reserved for memory context
	MaxRAGTopK                                   = 20  // hard cap on RAG search top-k
	AgentToolTraceMaxRawJSONBytes                = 256 * 1024
	AgentToolTraceMaxRawTextBytes                = 64 * 1024
	SystemAssistantToolMaxJSONBytes              = 32 * 1024
	SystemAssistantQueryMaxRunes                 = 500
	SystemAssistantAreasMaxCount                 = 6
	SystemAssistantFailureAuditMaxRows           = 20
	SystemAssistantCitationMaxCount              = 5
	SystemAssistantDiagnosticFactsMaxCount       = 100
	SystemAssistantDiagnosticGapsMaxCount        = 20
	SystemAssistantDiagnosticAreaResultsMaxCount = 5
	SystemAssistantEvidenceFieldMaxRunes         = 500
	// MaxAgentResultSources caps the citation sources attached to an agent
	// result for chat display (deduplicated by chunk ID, newest wins).
	MaxAgentResultSources = 10

	// Lazy planning: K consecutive LLM rounds with no Output triggers Reflect→Plan.
	DefaultStuckThreshold = 3
	// MaxPlanSteps caps the number of steps a single Plan may contain.
	MaxPlanSteps = 10
	// DefaultStepMaxLLMSteps is the LLM budget for each sub-step ReAct execution.
	DefaultStepMaxLLMSteps = 3

	// ---- stratum_delegate sub-agent dispatch ----
	// MaxDelegateDepth is the global hard cap on delegation depth (clamped at
	// runtime); per-agent DelegateMaxDepth defaults to 1 and may be raised to 2.
	MaxDelegateDepth = 2
	// DefaultDelegateMaxDepth is the runtime fallback for a per-agent
	// delegate_max_depth of 0 (unset): a single main→child hop.
	DefaultDelegateMaxDepth = 1
	// DefaultDelegateMaxLLMSteps is the runtime fallback for a per-agent
	// delegate_default_max_steps of 0 (unset). Slightly more headroom than the
	// plan-node default (DefaultStepMaxLLMSteps) since a sub-agent owns the whole
	// task, not a single node.
	DefaultDelegateMaxLLMSteps = 5
	// MaxDelegateMaxLLMSteps is the hard ceiling for the delegate max_steps
	// argument and the per-agent default (schema maximum + clamp).
	MaxDelegateMaxLLMSteps = 10
	// DelegateMaxGoalRunes bounds the delegate goal length; an oversized goal
	// would blow up the child context window.
	DelegateMaxGoalRunes = 2000
	// DelegateSummaryMaxRunes truncates the child final output summary before it
	// is returned, then ResultGuard's 32KB cap applies as a backstop.
	DelegateSummaryMaxRunes = 4000

	// AgentToolStopLossThreshold 是同一工具连续（同错指纹）失败触发止损的
	// 阈值：达阈值后该工具不再执行，模型收到观察后换路。
	AgentToolStopLossThreshold = 3
	// AgentDegradeReasonStopLossPrefix 是止损降级原因枚举的前缀。固定枚举
	// （"tool_stop_loss:<tool>"），禁止拼接 err.Error()——错误正文含
	// plan_id/revision 等内部标识，透出前端违反「错误不落下游错误正文」。
	AgentDegradeReasonStopLossPrefix = "tool_stop_loss:"
	// AgentToolStopLossObservation 是止损后返回给模型的观察文案（%s = 工具名）。
	AgentToolStopLossObservation = "Tool %s has been stopped after repeated validation failures. Use an alternative approach."
	// AgentNoProgressNudgeThreshold 是「同工具+同参+同归一化结果」的连续完成回合
	// 数达到该值时，向模型注入一次换路提示（nudge-then-cut 的 nudge 档）。提示
	// 只进本轮请求、不落持久会话，给模型一次换用不同工具/参数或直接作答的转机。
	AgentNoProgressNudgeThreshold = 3
	// AgentNoProgressTerminateThreshold 是连续同指纹回合数达到该值时，以业务终止
	// （reason=no_progress，非错误）结束执行。比 nudge 档大 1：每次入口 run 至多
	// 递增 1，进程内 run 能达到本值必然已在 nudge 档提示过一轮，无需额外记账；
	// 3/4 保证 MaxLLMSteps≤4 的短循环（plan 子步默认 3）优先走强制收尾而非误杀。
	AgentNoProgressTerminateThreshold = 4
	// AgentFinalAnswerInstruction 是 ReAct 达步数上限强制收尾的 system 指令：
	// 基于已有分析和工具结果总结已做到的事，并明确告知用户已达最大迭代次数。
	AgentFinalAnswerInstruction = "You have reached the maximum reasoning steps. Summarize what has been accomplished so far based on your analysis and tool results, and explicitly inform the user that the maximum number of steps has been reached. Do not call any tools."
	// AgentDegradedFinalAnswerInstruction 是降级执行的强制收尾指令：只基于已
	// 确认事实总结已做到的事并告知用户已达上限，禁止声称完成了未验证的操作。
	AgentDegradedFinalAnswerInstruction = "You have reached the maximum reasoning steps. Based on confirmed facts only, summarize what has been accomplished so far and explicitly inform the user that the maximum number of steps has been reached. Do not claim operations that were not verified successfully. Do not call any tools."

	// AgentFactCheckMaxClaims 是幻觉校验最多拆分的 claim 数（控成本，超出截断）。
	// 一次 judge 调用批量判定全部 claim，claim 过多会拉长单次生成 → 超时降级；
	// 4 条 + 30s 预算在 LLM-as-Judge 常见输出量下留足余量。
	AgentFactCheckMaxClaims = 4
	// AgentFactCheckTopK 是幻觉校验每个 claim 的 RAG 检索 topK。
	AgentFactCheckTopK = 4
	// AgentFactCheckTimeout 是单次幻觉校验的整体时间预算；judge/检索失败或超时
	// 降级为「不校验」（nil），不阻塞 agent 执行。
	AgentFactCheckTimeout = 30 * time.Second
	// AgentFactCheckJudgeMaxTokens 是 judge 单次输出预算。1024 会被批量
	// claim 判定截断（finish_reason=length → JSON 半截 → 解析失败降级）；
	// 2048 覆盖 4 claims 的完整 verdict JSON 输出。
	AgentFactCheckJudgeMaxTokens = 2048
	// AgentFactCheckEvidenceConcurrency 是幻觉校验证据检索的并发上限（有界并发，
	// 控制检索突发对下游 RAG 的压力；索引写入按 claim 顺序保序）。
	AgentFactCheckEvidenceConcurrency = 3
	// AgentFactCheckMaxUnverified 是未验证副作用声称的上限：无引用但命中
	// accomplishment 白名单的句子记入 UnverifiedClaims，超出截断（控报告噪音）。
	AgentFactCheckMaxUnverified = 5
	// AgentCitationReferenceInstruction 是"声称带引用"约束：模型声称操作/工具
	// 调用/副作用已完成时必须紧接该陈述输出引用标记 <tool_ref:ID>（ID = 本次
	// 执行真实的 tool_call_id），由收尾对账代码级核验；未真实调用或结果不确定
	// 时禁止声称成功。主注入常驻 base context（锚头压缩不逐出），收尾再强化。
	AgentCitationReferenceInstruction = "When you state that an operation, tool call, or side effect has been completed " +
		"(such as created, deleted, updated, sent, enabled, or disabled), you MUST immediately follow that statement " +
		"with a citation marker <tool_ref:ID>, where ID is the tool_call_id of the tool call you actually executed. " +
		"Never claim an operation succeeded unless you actually invoked the tool and received a successful result. " +
		"If you are unsure whether the call succeeded, say so explicitly and do not attach a citation. " +
		"If you did not make the relevant tool call, do not make the claim."
	// AgentKnowledgeNoResultText 是知识检索空结果的拒答观察模板（%s = NoAnswer
	// reason，固定枚举无注入风险）。空内容不能当成功结果喂给模型——模型看到
	// "没找到"而非空串，避免靠训练记忆编造答案。
	AgentKnowledgeNoResultText = "Knowledge search returned no relevant results (reason: %s). " +
		"Do not fabricate an answer; state clearly that no relevant information was found, " +
		"or answer from general knowledge without claiming the knowledge base as a source."
	// AgentSearchKnowledgeRefusalInstruction 是 stratum_search_knowledge 工具
	// 描述的拒答指令后缀：检索无结果/证据不足时必须明说，禁止编造来源。
	AgentSearchKnowledgeRefusalInstruction = " If the search returns no relevant results or the " +
		"evidence is insufficient, say so explicitly; never fabricate an answer or invent sources."

	// ---- agent task persistence (cross-session goal progress) ----

	// TaskLeaseDuration 是 task 的 claim lease：推进一次刷新一次；无 heartbeat，
	// 会话崩溃后 30 分钟自动释放，新会话可接管（复用 workflow claim 模式）。
	TaskLeaseDuration = 30 * time.Minute
	// TaskExpiresAt 是 task 自身保留窗口：30 天未推进则 CleanupExpired 回收。
	TaskExpiresAt = 30 * 24 * time.Hour
	// TaskFailThreshold 是恢复提示阈值：fail_count 达到后注入"上次多次失败，
	// 是否继续"提示（不自动改状态）。
	TaskFailThreshold = 3
	// TaskCleanupInterval 是 TaskCleanupWorker 的清理周期。
	TaskCleanupInterval = 10 * time.Minute
	// ApprovalCleanupInterval 是 ApprovalCleanupWorker 的清理周期。
	ApprovalCleanupInterval = 10 * time.Minute
	// TaskSemanticSimilarityThreshold 是恢复注入的语义相关阈值：新消息的
	// bigram 覆盖 goal bigram 的比例达到该值才注入（0.25 = 每 4 个 bigram 至少
	// 命中 1 个，中文 2 字词粒度）。
	TaskSemanticSimilarityThreshold = 0.25
	// TaskMetadataKey 是 AgentResult.Metadata 中 task snapshot 的透出键
	// （白名单：仅此键 + TaskMetadataCompleteKey 透出前端，禁止透出其他 Metadata）。
	TaskMetadataKey = "stratum_task_snapshot"
	// TaskMetadataCompleteKey 标记本次执行中 LLM 调用了 stratum_complete_task。
	TaskMetadataCompleteKey = "stratum_task_complete"

	DefaultPlanMaxNodes           = 10
	DefaultPlanMaxRevisions       = 20
	DefaultPlanMaxAttemptsPerNode = 3
	DefaultPlanMaxConcurrentNodes = 4

	// LoopCompactionRecentGroups is the number of most-recent message groups
	// (a group = one assistant turn plus its paired tool results) kept verbatim
	// during in-loop compaction. Older groups are summarized or dropped.
	LoopCompactionRecentGroups = 3
	// DefaultCompactionCooldown 是一次执行内压缩触发后的冷却窗口（Spec 第 4 节，
	// 建议默认 10s，实现时按压测验证）。registry 参数 agent.compaction_cooldown_sec
	// 覆盖它（0 = 本常量）。
	DefaultCompactionCooldown = 10 * time.Second
	// CompactionDefaultTemperature 是上下文压缩的默认温度（agent 配置 0 = 未设置
	// 时的兜底）。Qwen/Zhipu 拒收 >1 的温度，运行时必须钳制 [0,1]。
	CompactionDefaultTemperature = 0.3
	// CompactionTemperatureMin/Max 是压缩温度的合法区间。0 = unset（回退
	// CompactionDefaultTemperature），并非表达"温度=0"的合法值——这是 0=unset
	// 语义的已知限制，见计划决策记录。
	CompactionTemperatureMin = 0.0
	CompactionTemperatureMax = 1.0
	// LoopCompactionSafetyRatio triggers compaction before the hard token ceiling,
	// leaving margin for the EstimateText heuristic error (<20%).
	LoopCompactionSafetyRatio = 0.8
	// ContextSafetyReserveRatio 是执行级预算账本的安全余量默认比例（Spec 第 2 节
	// usable = window − safetyReserve − outputReserve）。独立于
	// LoopCompactionSafetyRatio（"80% 满即压缩"的触发语义）：扣减 80% 会让
	// 默认配置下 usable 归零（0.8×window + outputReserve > window），system 模板
	// 塞满 headCap、memory 注入与压缩全部失效。默认 20% 余量在窗口利用率与
	// 自修正兜底间取中。
	ContextSafetyReserveRatio = 0.2

	// CompactionBudgetTotal 是压缩路径一次执行的总体时间预算（Spec 第 4 节）：
	// 按 剩余/剩余尝试数 分摊为逐次独立的时间片，链内所有尝试合计不放大
	// 用户可感知时延。
	CompactionBudgetTotal = 5 * time.Second
	// CompactionMaxCandidates 是压缩路径 fallback 候选模型数量上限（不含主模型）。
	CompactionMaxCandidates = 2
	// CompactionMinSlice 是单次尝试时间片下限：剩余预算耗尽（≤0）时的兜底
	// slice，保证每次尝试仍有最小执行窗口。
	CompactionMinSlice = 1 * time.Millisecond

	// DefaultContextWindowRatio is the fraction of a model's context window
	// used as the agent's MaxContextTokens when the user does not set one explicitly.
	DefaultContextWindowRatio = 0.85

	// MaxContextWindowTokens is the hard ceiling of a resolved window (Spec 第 1 节),
	// replacing the model-independent DefaultAgentContextTokensCeiling(32768).
	MaxContextWindowTokens = 1_048_576
	// MinContextWindowTokens is the lower bound an explicit MaxContextTokens
	// is clamped to when the model window is known.
	MinContextWindowTokens = 2_000

	// DefaultFixedHeadRatio 是 system+memory 的预算配额比例（Spec 第 2 节）。
	DefaultFixedHeadRatio = 0.2
	// DefaultToolsBudgetRatio 是工具定义的预算配额比例（Spec 第 2 节）。
	DefaultToolsBudgetRatio = 0.2
	// DefaultOutputReserveTokens 是主模型输出预留的保守默认（无显式 max_tokens
	// 且 vendor 表未知时的兜底）。
	DefaultOutputReserveTokens = 4_096

	// ---- adaptive compaction thresholds (Context Phase 3) ----

	// CompactionRecentGroupsSmall is the number of recent message groups kept
	// verbatim when the context window is below 16K tokens.
	CompactionRecentGroupsSmall = 2
	// CompactionRecentGroupsLarge is the number of recent message groups kept
	// verbatim when the context window exceeds 64K tokens.
	CompactionRecentGroupsLarge = 5

	// CompactionRecentGroupsThresholdSmall is the window size below which the
	// small recent-groups count applies.
	CompactionRecentGroupsThresholdSmall = 16_000
	// CompactionRecentGroupsThresholdLarge is the window size above which the
	// large recent-groups count applies.
	CompactionRecentGroupsThresholdLarge = 64_000

	// CompactionSummaryReserveRatio is the fraction of the context budget
	// reserved for the history compaction summary (5%).
	CompactionSummaryReserveRatio = 0.05
	// CompactionSummaryReserveFloor is the minimum summary reserve in tokens.
	CompactionSummaryReserveFloor = 200

	// CompactionMaxTokensRatio is the fraction of MaxContextTokens used to cap
	// the compaction LLM's output (10%). See DynamicCompactionMaxTokens.
	CompactionMaxTokensRatio = 0.10
	// CompactionMaxTokensFloor is the minimum output budget for compaction.
	CompactionMaxTokensFloor = 400
	// CompactionMaxTokensCeiling caps the compaction output regardless of window.
	CompactionMaxTokensCeiling = 800

	// MaxFingerprintPayloadBytes caps the serialised ExecutionFingerprint
	// before it is truncated in span attributes (F1).
	MaxFingerprintPayloadBytes = 4096

	// OperationApprovalTTL bounds an approved operation proposal before its
	// single-use replay expires. Lives here (not in the agent application
	// package) because the tenant schema repository must parameterise the
	// Approve UPDATE's expires_at interval without importing application.
	OperationApprovalTTL = 24 * time.Hour
	// MaxPendingApprovalsPerActor caps how many unexpired pending approvals a
	// single user may hold (D4 放宽后 member 可触发审批，须防存储 DoS）。
	MaxPendingApprovalsPerActor = 50
	// TokenCorrectionAlpha is the EMA smoothing factor for the compaction
	// token-correction loop: correction = α·ratio + (1−α)·correction.
	TokenCorrectionAlpha = 0.1
	// TokenCorrectionMin/Max clamp the correction factor. 0.5 halves the
	// effective budget (compacts earlier); 2.0 doubles it (compacts later).
	TokenCorrectionMin = 0.5
	TokenCorrectionMax = 2.0

	// MinimalRetryReserveBytes 是最终请求 context_length_exceeded 降级最小
	// 请求（Spec D4）的字节预算余量：len() 字节数是 token 的保守上界
	// （CJK 每字符 3 字节），从窗口扣除该余量保证最小请求必然小于原请求。
	MinimalRetryReserveBytes = 64
)

// ChatConversationSourceEvaluation 标记评测驱动的受控会话
// （chat_conversations.source 列，阶段 B §5.4）。评测会话与手动/工作流会话
// 同表同协议（真历史、逐轮续跑），仅来源隔离；生产侧默认会话列表隐藏
// （ListConversations 过滤），避免污染用户工作台。
const ChatConversationSourceEvaluation = "evaluation"

// DynamicRecentGroups returns the number of recent message groups to preserve
// during in-loop compaction, scaled by the agent's MaxContextTokens.
//
//	< 16K → 2 groups (tight budget)
//	16K–64K → 3 groups (default)
//	> 64K → 5 groups (ample budget)
func DynamicRecentGroups(maxContextTokens int) int {
	if maxContextTokens <= 0 {
		return LoopCompactionRecentGroups
	}
	switch {
	case maxContextTokens < CompactionRecentGroupsThresholdSmall:
		return CompactionRecentGroupsSmall
	case maxContextTokens > CompactionRecentGroupsThresholdLarge:
		return CompactionRecentGroupsLarge
	default:
		return LoopCompactionRecentGroups
	}
}

// DynamicSummaryReserve returns the token budget reserved for a conversation
// history compaction summary. It scales as 5% of budget with a 200-token floor,
// replacing the fixed budget/4 previously used.
func DynamicSummaryReserve(budget int) int {
	reserve := int(float64(budget) * CompactionSummaryReserveRatio)
	if reserve < CompactionSummaryReserveFloor {
		return CompactionSummaryReserveFloor
	}
	return reserve
}

// DynamicCompactionMaxTokens returns the max output tokens for a compaction
// LLM call. It scales as ~10% of the agent's MaxContextTokens, bounded to
// [CompactionMaxTokensFloor, CompactionMaxTokensCeiling].
//
//	4K → 400 (floor)
//	8K → 800 (ceiling)
//	32K → 800 (ceiling)
func DynamicCompactionMaxTokens(maxContextTokens int) int {
	derived := int(float64(maxContextTokens) * CompactionMaxTokensRatio)
	if derived < CompactionMaxTokensFloor {
		return CompactionMaxTokensFloor
	}
	if derived > CompactionMaxTokensCeiling {
		return CompactionMaxTokensCeiling
	}
	return derived
}
