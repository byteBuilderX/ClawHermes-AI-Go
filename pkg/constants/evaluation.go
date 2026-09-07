// Package constants — evaluation tunable bounds.
package constants

import "time"

// Worker polling bounds.
const (
	// EvaluationIdleInterval 是评估 worker 无待办 job/实验时的空转等待：
	// 空队列时按该间隔轮询，禁止每租户每秒空查（2026-08-21 CPU 打满事故）。
	EvaluationIdleInterval = 2 * time.Second

	// 运行态观测（P1a，§4.2）引用事件与消费链路常量。
	// ObservationStream 承载运行态观测引用事件（WorkQueue 单消费语义）。
	ObservationStream = "evaluation-observe"
	// ObservationDLQStream 观测消费重投耗尽后的死信流。
	ObservationDLQStream = "evaluation-observe-dlq"
	// ObservationSubjectPrefix 引用事件 subject 前缀；完整 subject 为
	// "evaluation.observe.{tenant}"，与 memory 的 domain.action 命名同族。
	ObservationSubjectPrefix = "evaluation.observe"
	// ObservationDLQSubject 观测死信流独立 subject。独立前缀避免与观察流通配
	// "evaluation.observe.>" 重叠（否则死信消息按 subject 同时落入两流，观察
	// consumer 重消费死信导致重投死循环，仿 memory.dlq 的不相交前缀模式）。
	// DLQ 流按精确 subject 匹配，租户写入事件字段。
	ObservationDLQSubject = "evaluation.dlq"
	// ObservationConsumerName 观测 judge 消费组名。
	ObservationConsumerName = "observation-judge"
	// ObservationAckWait 消费确认窗口；ObservationMaxDeliver 重投上限，超限进 DLQ。
	ObservationAckWait    = 30 * time.Second
	ObservationMaxDeliver = 3
	// ObservationFetchMaxWait 单次 Fetch 等待窗口；ObservationFetchBackoffBase /
	// ObservationFetchBackoffMax 消费退避与重投延迟的指数区间（重投 NakWithDelay 用 Base）。
	ObservationFetchMaxWait     = 5 * time.Second
	ObservationFetchBackoffBase = 1 * time.Second
	ObservationFetchBackoffMax  = 30 * time.Second
	// ObservationStreamMaxAge / ObservationDLQMaxAge 消息保留期。
	ObservationStreamMaxAge = 7 * 24 * time.Hour
	ObservationDLQMaxAge    = 30 * 24 * time.Hour
	// ObservationSampleRateDefault 采样率默认值（registry 兜底，运行时经平台参数覆盖）。
	ObservationSampleRateDefault = 0.1
	// ObservationPublishTimeout 引用事件发布超时预算（agent 侧 best-effort，超时不阻断）。
	ObservationPublishTimeout = 3 * time.Second
	// ObservationBacklogInterval 消费积压指标采集周期。
	ObservationBacklogInterval = 30 * time.Second
)

// Tunable parameter bounds shared by the evaluation domain (tunable
// registration), the AgentRevision model validation, and the evaluation
// Agent adapter. Zero means "unset": a candidate may always express unset
// to leave the production value untouched.
const (
	// TunableTemperatureMin/Max bound the LLM temperature parameter.
	// Max is 1.0, not 2.0: the platform's OpenAI-compatible providers (Qwen /
	// Zhipu) reject temperature > 1 with a 4xx that the gateway surfaces as
	// 500 at execution. 1.0 also matches evaluation.optimizer/judge.temperature.
	TunableTemperatureMin = 0.0
	TunableTemperatureMax = 1.0

	// TunableMaxTokensMin/Max bound max_tokens per LLM request. Min is 0 (unset).
	TunableMaxTokensMin = 0
	TunableMaxTokensMax = 131072

	// TunableMaxContextTokensMin/Max/Step bound the context memory window
	// slider exposed to optimization (0 = auto-derive from the model).
	TunableMaxContextTokensMin  = 0
	TunableMaxContextTokensMax  = 32768
	TunableMaxContextTokensStep = 1024

	// TunableRecentGroupsMax caps compaction_recent_groups.
	TunableRecentGroupsMax = 5

	// JudgeMaxTokens caps a single LLM judge response; a verdict is a short
	// JSON object, so a fixed cap keeps judge cost bounded regardless of
	// provider. The judge itself is gated by evaluation.judge.enabled.
	JudgeMaxTokens = 1024

	// CaseGenMaxTokens caps a single case-generator response: one eval case
	// JSON object (name/input/expected_output/assertion_mode/reason).
	CaseGenMaxTokens = 2048

	// DefaultCaseSampleLimit bounds how many production samples one
	// generation pass samples when the caller does not request more.
	DefaultCaseSampleLimit = 20

	// MaxCaseSampleLimit caps the caller-provided sample count so one
	// request cannot fan out unbounded LLM calls.
	MaxCaseSampleLimit = 50
)

// Evaluation 运行态观测行为阈值（P1b §4.2）。
const (
	// JudgeBelowThreshold 是 judge 单维度跌阈判异的阈值（§4.2 判异触发）。
	JudgeBelowThreshold = 0.5
	// FeedbackNegativeThreshold 是 feedback 负反馈判异阈值：score 低于该值视为放弃倾向。
	FeedbackNegativeThreshold = 0.5
)

// Evaluation 人工评审池（P1c §6.6）阈值。
const (
	// ReviewLowConfidenceThreshold 是 judge 低置信触发评审池的阈值：任一维度
	// confidence < 该值入池（low_confidence）。
	ReviewLowConfidenceThreshold = 0.6
	// ReviewBacklogAlertThreshold 是评审池积压告警阈值：eval_review_backlog
	// 持续 > 该值触发 StratumEvalReviewBacklogHigh。
	ReviewBacklogAlertThreshold = 50
)

// Evaluation 人工评审池置信度机制（§6.6 置信度机制：分数落边界或理由含糊也视为低置信）。
const (
	// ConfidenceBoundaryLow/High 界定 confidence 边界区间 [0.45, 0.55]：落在此区间的分数
	// 视为低置信（spec §6.6「分数落在边界(如 0.45–0.55)…也视为低置信」）。
	ConfidenceBoundaryLow  = 0.45
	ConfidenceBoundaryHigh = 0.55
	// VagueReasonMinRunes 打分理由视为含糊的最短有效 rune 数：理由为空或更短不足以支撑
	// 判定，视为含糊（spec §6.6「打分理由含糊也视为低置信」）。
	VagueReasonMinRunes = 8
)

// Evaluation 平台配置组（Phase 2 §4.3 版本锚点）。
const (
	// PlatformGroupEvaluation 是平台配置的 evaluation 分组 key：evaluation 配置组
	// 当前生效版本序号写入观测 param_version.platform.version_seq。取值必须与
	// internal/parameters/domain.GroupEvaluation 保持同步（后者是分组权威来源，
	// evaluation 侧因 DDD 边界只读 pkg/constants）。
	PlatformGroupEvaluation = "evaluation"
)

// Evaluation 工具序列过程断言（§6.5 多步推理与工具调用评测）行为边界。
const (
	// StepJudgeMaxTools 是 FormatToolSequence 渲染的最大工具条数：超过后截断
	// 工具序列，防止 step_judge 上下文随长链路无限膨胀。
	StepJudgeMaxTools = 20
	// StepJudgeRawTextMaxChars 是 FormatToolSequence 单条工具 RawText 的最大
	// rune 数：超过后按 rune 截断并追加省略号。
	StepJudgeRawTextMaxChars = 500
)

// 会话 transcript 渲染（阶段 B §4.3 judge 会话调用形态）行为边界：FormatTranscript
// 把逐轮证据渲染成纯文本交给 LLM judge 判「末轮是否到达目标/守住探针」。截断预算与
// FormatToolSequence 同族——防止 judge 上下文随轮次/文本无限膨胀。
const (
	// SessionTurnTextMaxRunes 是 FormatTranscript 单轮 user/assistant 文本上限（rune）：
	// 超限按 rune 截断并追加省略号（仿 StepJudgeRawTextMaxChars 语义）。
	SessionTurnTextMaxRunes = 800
	// SessionTranscriptMaxTurns 是 FormatTranscript 保留的最近轮次数：长会话超限后
	// 优先保留末端——judge 判末端终态，末端信息量最高。
	SessionTranscriptMaxTurns = 20
	// SessionTranscriptMaxRunes 是 FormatTranscript 总长度上限（rune）：超限从最旧轮
	// 逐轮丢弃直至不超，绝不截断末端内容。
	SessionTranscriptMaxRunes = 8000
)

// 分层门禁阈值（spec §4.2.4）。时间型常量沿用本文件不带 _MS 后缀的风格。
// GateRuleBlockRollbackMin 规则阻断回滚门槛、GateAnomalyRollbackMin 行为异常回滚门槛、
// GateAnomalyAlertMin 告警门槛、GateObservationWindow 证据窗口、GateCooldown 决策冷却、
// GateAutoRollbackMaxPerDay 自动回滚日限、RunRegressionDeltaThreshold 对照 run 劣化阈值。
const (
	GateObservationWindow       = 10 * time.Minute
	GateCooldown                = 10 * time.Minute
	GateRuleBlockRollbackMin    = 3
	GateAnomalyRollbackMin      = 10
	GateAnomalyAlertMin         = 3
	GateAutoRollbackMaxPerDay   = 3
	RunRegressionDeltaThreshold = -0.05
)

// 评测指标监控面板（EvaluationCenterPage「监控」tab，spec 2026-09-03 §4.3）行为边界：
// 默认监控窗口天数与资源行 limit 默认/上限。前端 web/src/constants 各持默认窗口天数
// （spec §4.3：两端各持有默认值并在 UI 明示，后端为权威兜底）。
const (
	// EvalMonitorWindowDays 面板默认监控窗口（近 N 天，含端点）。
	EvalMonitorWindowDays = 7
	// EvalMonitorResourceLimitDefault 资源行摘要默认返回条数（按观测样本数降序）。
	EvalMonitorResourceLimitDefault = 20
	// EvalMonitorResourceLimitMax 资源行 limit 上限。
	EvalMonitorResourceLimitMax = 100
)

// 分层门禁计数 label 与平台版本门禁状态（spec §3.4/§4.1.1；P2 只消费
// eval_gate_action_total 开放 label 与 eval_state 状态文本，不新增 metric family）。
const (
	// GateLayerL3Platform 是平台参数门禁动作层（rollback_manual/auto_refused/publish_gated/…）。
	GateLayerL3Platform = "l3_platform"
	// GateActionAutoRefused 是平台 auto 回滚被拒计数动作（§3.4 无自动不变量）。
	GateActionAutoRefused = "auto_refused"
)

const (
	// PlatformEvalStateSentinelPassed 表示草案已过发布前置哨兵，允许人工确认发布。
	PlatformEvalStateSentinelPassed = "sentinel_passed"
)

// 多租户验证（spec §3.4-3）：回滚后验证窗口与未恢复判定门槛。
const (
	PlatformVerifyWindowMinutes   = 30 // 验证对比取 run 的窗口（分钟）
	PlatformVerifyNotRecoveredMin = 1  // ≥1 租户未恢复即触发 StratumEvalMultiTenantVerifyNotRecovered
)

// 版本引用账本（里程碑 7：单版本引用 usage / 通过率摘要端点）明细条数边界。引用
// 分组与 recent_runs 都是「给前端抽屉陈列明细」的只读细节，非精确计数，超限即取最近。
const (
	// EvalReferenceRunsLimit 版本引用账本 subject/pinned run 明细单组上限。
	EvalReferenceRunsLimit = 20
	// EvalReferenceCandidatesLimit 版本引用账本 candidate 引用明细上限。
	EvalReferenceCandidatesLimit = 50
	// EvalReferenceExperimentsLimit 版本引用账本 experiment 引用明细上限。
	EvalReferenceExperimentsLimit = 50
	// EvalReferenceRecentRunsLimit 版本通过率摘要 recent_runs 最近 run 条数（spec (d)）。
	EvalReferenceRecentRunsLimit = 20
)

// 评测法官默认判定准则与优化器系统提示词（提示词平台参数化，spec 2026-09-04）。
// 单一来源：internal/parameters/domain/registry.go 的平台键默认值与 api/wiring 的
// 兜底共同引用本常量，保证「开箱可见值 == 内置兜底」byte-identical、永不漂移。
const (
	// EvaluationJudgeDefaultRubric 是评测法官内置默认判定准则（assertion_mode=judge
	// 用例未单写判定标准时使用）。与平台参数 evaluation.judge.rubric 默认值镜像：
	// 改动必须同步 internal/parameters/domain/registry.go 的注册默认。
	EvaluationJudgeDefaultRubric = `你是一名严谨的评测法官。根据以下标准判断实际输出是否通过：
1. 实际输出是否直接、完整地回答了输入要求；
2. 与期望输出的一致性（期望输出为 null 或空时忽略该项）；
3. 是否存在明显的事实错误或逻辑矛盾。
只输出 JSON：{"passed": true 或 false, "reason": "一句话理由", "confidence": 0-1 之间的小数表示判定置信度,
"dimensions": [{"name": "faithfulness", "score": 0-1, "passed": true 或 false, "reason": "一句话理由", "confidence": 0-1},
{"name": "relevance", "score": 0-1, "passed": true 或 false, "reason": "一句话理由", "confidence": 0-1},
{"name": "completeness", "score": 0-1, "passed": true 或 false, "reason": "一句话理由", "confidence": 0-1}]}。`

	// EvaluationOptimizerSystemPrompt 是提示词优化器内置系统提示词。与平台参数
	// evaluation.optimizer.system_prompt 默认值镜像：改动必须同步 registry 默认值。
	EvaluationOptimizerSystemPrompt = "你是提示词优化器。只生成候选内容，不决定发布。仅输出 JSON 数组。"
)
