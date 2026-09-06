package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

type AssertionMode string

const (
	AssertionExact    AssertionMode = "exact"
	AssertionContains AssertionMode = "contains"
	AssertionRegex    AssertionMode = "regex"
	// AssertionJudge delegates the verdict to an LLM judge. Dispatch happens
	// in the application layer (runCase); EvaluateAssertion stays rule-only.
	AssertionJudge AssertionMode = "judge"
)

// DimensionScore 是 judge 对一个语义维度（faithfulness/relevance/completeness）
// 的评分。Score 归一化到 [0,1]；Confidence 缺失/越界由解析层回退 1.0（与
// AssertionResult.Confidence 同语义，spec §6.2）。规则断言不产生该结构。
type DimensionScore struct {
	Name       string  `json:"name"`
	Score      float64 `json:"score"`
	Passed     bool    `json:"passed"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

type AssertionResult struct {
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
	// Confidence 是 judge 判定置信度（0-1）。规则断言不产生该值；judge 解析
	// 缺失/无效时由 parseJudgeResponse 回退 1.0（spec §6.2，本 domain 不静默改值）。
	Confidence float64 `json:"confidence,omitempty"`
	// Dimensions 是 judge 返回的语义维度分数（spec §6.2）。旧 judge 不返回
	// 维度时为空；聚合层对空维度自动跳过，不阻断判定。
	Dimensions []DimensionScore `json:"dimensions,omitempty"`
}

type EvalCase struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Input          any           `json:"input"`
	ExpectedOutput any           `json:"expected_output"`
	AssertionMode  AssertionMode `json:"assertion_mode"`
	Enabled        bool          `json:"enabled"`
	// Session 是会话剧本（spec 2026-09-04 §5.4 阶段 B）：非 nil 时 case 走多轮
	// 会话执行语义（SessionRunner），ExpectedOutput/AssertionMode 作为末轮终态断言；
	// nil = 旧单轮 case。持久化在独立 eval_cases.session JSONB 列（'{}' 解码回 nil）。
	Session *EvalSessionScript `json:"session,omitempty"`
	// JudgeSpec configures LLM judge assertion for assertion_mode=judge.
	// Both fields are optional: empty Model/Rubric fall back to platform
	// parameters and the registered global rubric respectively. Persisted in
	// the evaluator_config JSONB column (never written before Phase 3).
	JudgeSpec *JudgeSpec `json:"judge_spec,omitempty"`
	// ToolSpec 是工具序列确定性过程断言（§6.5）：must_call / must_not_call /
	// order / max_calls。nil 表示不做工具序列规则校验。
	ToolSpec *ToolSpec `json:"tool_spec,omitempty"`
	// StepJudge 是步骤级 LLM rubric（§6.5）：对工具序列逐步骤评分。nil 表示
	// 不做步骤级 judge。Criteria 空时回退平台默认步骤 rubric。
	StepJudge *StepJudge `json:"step_judge,omitempty"`
	// NeedsReview 标记该 case 判定后必须进入人工评审池（spec §6.6 触发规则 4）。
	// 仅对 assertion_mode=judge 生效；规则断言不触发评审池。
	NeedsReview bool `json:"needs_review,omitempty"`
	// Provenance fields link generated cases back to the production trace
	// and feedback signal they were sampled from (Phase 3c). Populated only
	// for LLM-generated cases; empty for hand-authored ones. Persisted in
	// the evaluator_config JSONB column alongside JudgeSpec.
	SourceTraceID  string `json:"source_trace_id,omitempty"`
	FeedbackRef    string `json:"feedback_ref,omitempty"`
	GenerateReason string `json:"generate_reason,omitempty"`
}

// EvalCaseConfig is the persisted payload of eval_cases.evaluator_config.
// It wraps the judge spec and generation provenance in one JSONB column;
// reading tolerates the pre-3c bare JudgeSpec layout for backward
// compatibility.
type EvalCaseConfig struct {
	JudgeSpec  *JudgeSpec      `json:"judge_spec,omitempty"`
	ToolSpec   *ToolSpec       `json:"tool_spec,omitempty"`
	StepJudge  *StepJudge      `json:"step_judge,omitempty"`
	Generation *GenerationMeta `json:"generation,omitempty"`
}

// GenerationMeta is the provenance block of an LLM-generated eval case.
type GenerationMeta struct {
	SourceTraceID  string `json:"source_trace_id,omitempty"`
	FeedbackRef    string `json:"feedback_ref,omitempty"`
	GenerateReason string `json:"generate_reason,omitempty"`
}

// ToConfig packs the case's judge spec, process assertions (tool_spec /
// step_judge) and provenance for persistence.
func (c EvalCase) ToConfig() *EvalCaseConfig {
	cfg := &EvalCaseConfig{JudgeSpec: c.JudgeSpec, ToolSpec: c.ToolSpec, StepJudge: c.StepJudge}
	if c.SourceTraceID != "" || c.FeedbackRef != "" || c.GenerateReason != "" {
		cfg.Generation = &GenerationMeta{
			SourceTraceID: c.SourceTraceID, FeedbackRef: c.FeedbackRef, GenerateReason: c.GenerateReason,
		}
	}
	if cfg.JudgeSpec == nil && cfg.ToolSpec == nil && cfg.StepJudge == nil && cfg.Generation == nil {
		return nil
	}
	return cfg
}

// ApplyConfig fills JudgeSpec, process assertions and provenance from the
// persisted config, accepting both the wrapped layout and the bare JudgeSpec
// written before Phase 3c.
func (c *EvalCase) ApplyConfig(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var cfg EvalCaseConfig
	if err := json.Unmarshal(raw, &cfg); err == nil && (cfg.JudgeSpec != nil || cfg.ToolSpec != nil || cfg.StepJudge != nil || cfg.Generation != nil) {
		c.JudgeSpec = cfg.JudgeSpec
		c.ToolSpec = cfg.ToolSpec
		c.StepJudge = cfg.StepJudge
		if cfg.Generation != nil {
			c.SourceTraceID = cfg.Generation.SourceTraceID
			c.FeedbackRef = cfg.Generation.FeedbackRef
			c.GenerateReason = cfg.Generation.GenerateReason
		}
		return
	}
	var spec JudgeSpec
	if err := json.Unmarshal(raw, &spec); err == nil {
		c.JudgeSpec = &spec
	}
}

// JudgeSpec is the per-case LLM judge configuration.
type JudgeSpec struct {
	Model  string `json:"model,omitempty"`
	Rubric string `json:"rubric,omitempty"`
}

// ToolSpec 是工具序列确定性过程断言（§6.5）：约束执行链路上工具调用的
// 行为。空字段不参与校验；MaxCalls <=0 表示不限次数。
type ToolSpec struct {
	// MustCall 是必须被调用的工具名；缺一即败（process:must_call:<tool>）。
	MustCall []string `json:"must_call,omitempty"`
	// MustNotCall 是禁止调用的工具名；命中即败（process:must_not_call:<tool>）。
	MustNotCall []string `json:"must_not_call,omitempty"`
	// Order 要求工具按给定顺序出现（greedy 子序列，允许跨多余调用）；
	// 违背即败（process:order）。
	Order []string `json:"order,omitempty"`
	// MaxCalls 是工具调用次数上限；超限即败（process:max_calls）。<=0 = 不限。
	MaxCalls int `json:"max_calls,omitempty"`
}

// StepJudge 是步骤级 LLM rubric（§6.5）：对工具序列逐步骤评分。Criteria
// 为空时回退平台默认步骤 rubric。
type StepJudge struct {
	Criteria string `json:"criteria,omitempty"`
}

// ProcessAssertion 是工具序列过程断言的判定结果。Passed 与 Failures 同源：
// Failures 为空即通过。Failures 逐项归因，如 "process:must_not_call:delete"。
type ProcessAssertion struct {
	Passed   bool
	Failures []string
}

// EvaluateToolSequence 是过程断言纯函数（无 IO、无副作用）：逐项检查
// must_call / must_not_call / order（greedy 子序列）/ max_calls，收集全部
// 失败而非首个失败即返回。空 spec 恒通过。
func EvaluateToolSequence(toolNames []string, spec ToolSpec) ProcessAssertion {
	var failures []string
	for _, tool := range spec.MustCall {
		if !containsTool(toolNames, tool) {
			failures = append(failures, "process:must_call:"+tool)
		}
	}
	for _, tool := range spec.MustNotCall {
		if containsTool(toolNames, tool) {
			failures = append(failures, "process:must_not_call:"+tool)
		}
	}
	if len(spec.Order) > 0 && !orderSatisfied(toolNames, spec.Order) {
		failures = append(failures, "process:order")
	}
	if spec.MaxCalls > 0 && len(toolNames) > spec.MaxCalls {
		failures = append(failures, "process:max_calls")
	}
	return ProcessAssertion{Passed: len(failures) == 0, Failures: failures}
}

// containsTool reports whether toolNames contains the target tool name.
func containsTool(toolNames []string, tool string) bool {
	for _, name := range toolNames {
		if name == tool {
			return true
		}
	}
	return false
}

// orderSatisfied 用 greedy 子序列匹配判断 order 是否被满足：顺序遍历
// toolNames，命中当前期望项即前进，允许跨多余调用；全部命中才算满足。
func orderSatisfied(toolNames, order []string) bool {
	i := 0
	for _, name := range toolNames {
		if i < len(order) && name == order[i] {
			i++
		}
	}
	return i == len(order)
}

// ToolObservation 是执行链路中一次工具调用的最小可观测摘要（评审池详情展示
// 工具序列 / step_judge 输入用；agent domain.ToolObservation 的 summary 投影，
// 见 mapEvaluationEvidence）。由 port 迁移至此（domain 不 import port）。
type ToolObservation struct {
	ToolName     string         `json:"tool_name"`
	ToolType     string         `json:"tool_type"`
	StepIndex    int            `json:"step_index"`
	ProviderType string         `json:"provider_type"`
	CapabilityID string         `json:"capability_id"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	RawText      string         `json:"raw_text,omitempty"`
}

// FormatToolSequence 把工具序列渲染成 judge 可读文本：每行 "[step] name"，
// RawText 非空时追加 ": <raw_text>"。工具数超过 constants.StepJudgeMaxTools
// 时截断序列；单条 RawText 超过 constants.StepJudgeRawTextMaxChars 时按 rune
// 截断并追加省略号。
func FormatToolSequence(tools []ToolObservation) string {
	if len(tools) > constants.StepJudgeMaxTools {
		tools = tools[:constants.StepJudgeMaxTools]
	}
	var b strings.Builder
	for i, tool := range tools {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(fmt.Sprintf("[%d] %s", tool.StepIndex, tool.ToolName))
		if tool.RawText != "" {
			b.WriteString(": ")
			b.WriteString(truncateRunes(tool.RawText, constants.StepJudgeRawTextMaxChars))
		}
	}
	return b.String()
}

// truncateRunes 按 rune 截断字符串：长度超过 max 时保留前 max 个 rune 并
// 追加省略号。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

type EvalSuiteRevision struct {
	ID           string              `json:"id"`
	SuiteID      string              `json:"suite_id"`
	ParentID     string              `json:"parent_id,omitempty"`
	VersionNo    int                 `json:"version_no,omitempty"`
	Status       SuiteRevisionStatus `json:"status"`
	ResourceKind ResourceKind        `json:"resource_kind"`
	CreatedBy    string              `json:"created_by,omitempty"`
	Cases        []EvalCase          `json:"cases"`
}

type SuiteRevisionStatus string

const (
	SuiteRevisionDraft     SuiteRevisionStatus = "draft"
	SuiteRevisionPublished SuiteRevisionStatus = "published"
)

type EvalSuite struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	ActiveRevisionID string    `json:"active_revision_id,omitempty"`
	DraftRevisionID  string    `json:"draft_revision_id,omitempty"`
	CreatedBy        string    `json:"created_by,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
}

// SuiteRevisionMeta 是套件版本列表行的轻量投影：不装载 cases，只带版本号、
// 状态、kind、创建者、发布时间与启用 case 数。草稿 revision 的 version_no 为
// NULL（未分配版本号）读回时归 0；published_at 同样可空。
type SuiteRevisionMeta struct {
	ID               string              `json:"id"`
	VersionNo        int                 `json:"version_no,omitempty"`
	Status           SuiteRevisionStatus `json:"status"`
	ResourceKind     ResourceKind        `json:"resource_kind"`
	CreatedBy        string              `json:"created_by,omitempty"`
	PublishedAt      *time.Time          `json:"published_at,omitempty"`
	EnabledCaseCount int                 `json:"enabled_case_count"`
}

type EvalCaseResult struct {
	// ID 是该结果行主键，与 eval_case_results.id 共用（评审池 source_id）。
	// runCase 内生成稳定 ID，SaveRun 持久化直接采用（为空才回退生成）。
	ID            string                 `json:"id,omitempty"`
	CaseID        string                 `json:"case_id"`
	Passed        bool                   `json:"passed"`
	Message       string                 `json:"message,omitempty"`
	Error         string                 `json:"error,omitempty"`
	Actual        any                    `json:"actual,omitempty"`
	TraceID       string                 `json:"trace_id,omitempty"`
	Tokens        int                    `json:"tokens"`
	CostUSD       float64                `json:"cost_usd"`
	DurationMs    int                    `json:"duration_ms"`
	TraceEvidence *ObservedTraceEvidence `json:"trace_evidence,omitempty"`
	// RAGEvidence carries structured retrieval metrics for knowledge
	// evaluations; nil for other resource kinds. It replaces brittle parsing
	// of the serialized Actual payload.
	RAGEvidence *RAGEvidenceInfo `json:"rag_evidence,omitempty"`
	// Dimensions 是 judge/step_judge 产出的语义维度分数（spec §6.2）；规则断言
	// case 未配置 step_judge 时为空。
	Dimensions []DimensionScore `json:"dimensions,omitempty"`
	// FailureReason 是 case 失败的主要归因（spec §6.2）：judge 失败 →
	// "dimension:<名>"；规则断言失败 → "assert:<mode>"；执行失败 → "execution"。
	// 通过 case 为空。
	FailureReason string `json:"failure_reason,omitempty"`
	// ProcessPass 是工具序列过程断言（§6.5）的判定结果：true 表示过程断言通过
	// 或未配置过程断言。与 Passed 独立：Passed 是输出归因，ProcessPass 是过程
	// 归因；最终 Passed = 输出断言 && ProcessPass。无 omitempty（仿 passed，
	// 过程判定始终在结果 JSON 可见）。
	ProcessPass bool `json:"process_pass"`
	// ProcessFailure 是过程断言失败归因（§6.5）：如 "process:must_not_call:delete"
	// 或步骤级 judge 的主要失败维度。与 FailureReason（输出归因）独立；过程通过
	// 时为空。
	ProcessFailure string `json:"process_failure,omitempty"`
	// Tools 是执行链路工具调用序列（§6.5），过程断言与评审详情展示用；未采集
	// 时为空。
	Tools []ToolObservation `json:"tools,omitempty"`
	// Turns 是会话剧本逐轮执行证据（阶段 B）：会话 case 非空、旧单轮 case 为空。
	// 落 eval_case_results.turns JSONB（'[]' 解码回 nil）。
	Turns []SessionTurnEvidence `json:"turns,omitempty"`
	// Trajectory 是会话演化轨迹判据产出（阶段 B §4.2）：判定会话是否一路收敛到
	// 目标（converged）、绕圈空转（stalled）还是到达后推翻（drifted）。指针 +
	// omitempty：旧单轮 case（无轨迹维度）保持 nil、JSON 省略，既有 wire 与 golden
	// 不受影响；会话 case 由 runCaseSession 判定后非 nil。当前为派生值，不持久化
	// 到 eval_case_results（评审快照与运行结果均可得，需落列时后续 slice 再扩展）。
	Trajectory *TrajectoryVerdict `json:"trajectory,omitempty"`
}

// RAGEvidenceInfo is the per-case retrieval signal for knowledge runs. The
// K-suffixed metrics use the rank window of knowledge/application.RetrievalK
// (constants.DefaultRAGTopK); retrieved IDs are ordered as returned.
type RAGEvidenceInfo struct {
	RetrievedDocumentIDs []string `json:"retrieved_document_ids,omitempty"`
	RelevantDocumentIDs  []string `json:"relevant_document_ids,omitempty"`
	RecallAtK            float64  `json:"recall_at_k,omitempty"`
	PrecisionAtK         float64  `json:"precision_at_k,omitempty"`
	MRR                  float64  `json:"mrr,omitempty"`
	NDCGAtK              float64  `json:"ndcg_at_k,omitempty"`
}

// ObservedTraceEvidence carries trace-level observability signals resolved
// from the authoritative Agent evidence backend (Opik). All fields are
// best-effort: a nil pointer means evidence was not available.
type ObservedTraceEvidence struct {
	CostUSD           float64 `json:"cost_usd"`
	LatencyMs         int64   `json:"latency_ms"`
	Success           bool    `json:"success"`
	SecurityViolation bool    `json:"security_violation"`
	ToolCallCount     int     `json:"tool_call_count"`
	ToolErrorCount    int     `json:"tool_error_count"`
}

type EvalRun struct {
	ID              string      `json:"id"`
	Resource        ResourceRef `json:"resource"`
	SuiteRevisionID string      `json:"suite_revision_id"`
	Passed          bool        `json:"passed"`
	TotalCases      int         `json:"total_cases"`
	PassedCases     int         `json:"passed_cases"`
	// Metrics aggregates run-level signals (pass rate, latency percentiles,
	// token/cost totals) computed after the case loop; persisted to
	// eval_runs.metrics JSONB.
	Metrics map[string]any `json:"metrics,omitempty"`
	// ContextSnapshot 是 run 创建时捕获的全链路执行上下文快照（spec §7 版本绑定），
	// 落库 eval_runs.context_snapshot JSONB；nil = 旧 run / 未捕获。
	ContextSnapshot *EvaluationContextSnapshot `json:"context_snapshot,omitempty"`
	Results         []EvalCaseResult           `json:"results"`
	CreatedBy       string                     `json:"created_by,omitempty"`
	CreatedAt       time.Time                  `json:"created_at"`
}

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

const JobTypeEvalRun = "eval_run"

// JobTypePlatformVerify 平台版本回滚后的多租户验证任务（spec §3.4-3）。
// evaluation_jobs.job_type 列无 CHECK 约束，新 job 类型零 DDL。
const JobTypePlatformVerify = "platform_verify"

// PlatformVerifyPayload 持久化于 evaluation_jobs.payload（JSONB）；host 租户来自动作
// 载荷（O2/R29）。
type PlatformVerifyPayload struct {
	GroupKey string `json:"group_key"`
	FromSeq  int64  `json:"from_seq"` // 回滚离开的坏版本 seq（曾为 production）
	ToSeq    int64  `json:"to_seq"`   // 回滚到的目标 seq（当前 production）
	Actor    string `json:"actor"`
}

// PlatformVerifyJob 是 ClaimPlatformVerify 返回的本地 job 视图（DB 行 → runner 消费）。
type PlatformVerifyJob struct {
	ID       string
	TenantID string
	Payload  PlatformVerifyPayload
}

type EvalRunJobPayload struct {
	Resource        ResourceRef `json:"resource"`
	SuiteRevisionID string      `json:"suite_revision_id"`
	RequestedBy     string      `json:"requested_by"`
	// Snapshot 是 run 创建时捕获的全链路版本快照（Task 3 创建时 fail-closed
	// 集成）。旧 job（无快照）由 RunOnce → RunStored → Run fail-closed 拒绝执行。
	Snapshot *EvaluationContextSnapshot `json:"snapshot,omitempty"`
}

type EvaluationJob struct {
	ID             string            `json:"id"`
	Type           string            `json:"type"`
	Payload        EvalRunJobPayload `json:"payload"`
	Status         JobStatus         `json:"status"`
	Attempts       int               `json:"attempts"`
	IdempotencyKey string            `json:"idempotency_key"`
	ErrorMessage   string            `json:"error_message,omitempty"`
	ResultID       string            `json:"result_id,omitempty"`
	CreatedBy      string            `json:"created_by,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
}

type CandidatePatch struct {
	Source         string         `json:"source"`
	ParameterPatch map[string]any `json:"parameter_patch,omitempty"`
	PromptPatch    map[string]any `json:"prompt_patch,omitempty"`
	Rationale      string         `json:"rationale,omitempty"`
	DiagnosisRef   string         `json:"diagnosis_ref,omitempty"`
	RiskScore      float64        `json:"risk_score"`
}

type OptimizationJob struct {
	ID               string           `json:"id"`
	Baseline         ResourceRef      `json:"baseline"`
	SuiteRevisionID  string           `json:"suite_revision_id"`
	Status           JobStatus        `json:"status"`
	SearchSpace      map[string][]any `json:"search_space"`
	FailureSummaries []string         `json:"failure_summaries,omitempty"`
	CreatedBy        string           `json:"created_by,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
}

type OptimizationCandidate struct {
	ID                 string         `json:"id"`
	OptimizationJobID  string         `json:"optimization_job_id"`
	Revision           ResourceRef    `json:"revision"`
	ParentRevisionID   string         `json:"parent_revision_id"`
	Source             string         `json:"source"`
	Rationale          string         `json:"rationale,omitempty"`
	GenerationMetadata map[string]any `json:"generation_metadata,omitempty"`
	EvalRunID          string         `json:"eval_run_id,omitempty"`
	Rank               int            `json:"rank,omitempty"`
	CreatedBy          string         `json:"created_by,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
}

type FeedbackRequest struct {
	ActorID           string
	TraceID           string
	ResourceKind      ResourceKind
	ResourceID        string
	RevisionID        string
	ExperimentID      string
	Variant           string
	Score             float64
	Outcome           map[string]any
	IdempotencyKey    string
	SecurityViolation bool
}

type EvaluationFeedback struct {
	ID             string         `json:"id"`
	TraceID        string         `json:"trace_id"`
	ResourceKind   ResourceKind   `json:"resource_kind"`
	ResourceID     string         `json:"resource_id"`
	RevisionID     string         `json:"revision_id"`
	ExperimentID   string         `json:"experiment_id,omitempty"`
	Variant        string         `json:"variant,omitempty"`
	Score          float64        `json:"score"`
	Outcome        map[string]any `json:"outcome,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
	CreatedBy      string         `json:"created_by,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// SamplePolicy controls how production (query, response) pairs are picked
// for case generation: negative_first prioritises low-score / negative
// feedback, balanced alternates between negative and non-negative samples.
type SamplePolicy string

const (
	SamplePolicyNegativeFirst SamplePolicy = "negative_first"
	SamplePolicyBalanced      SamplePolicy = "balanced"
)

// CaseSample is one sampled production interaction paired with its feedback
// signal, ready to be turned into an eval case by the LLM generator.
type CaseSample struct {
	TraceID     string
	FeedbackRef string
	Score       *float64
	Outcome     map[string]any
	Query       string
	Response    string
}

// GeneratedCase is the LLM generator's verdict for one sample. Valid cases
// carry a full eval-case shape; invalid ones carry only Reason (generation
// failure or quality-filter rejection) and never enter the draft.
type GeneratedCase struct {
	Name           string
	Input          any
	ExpectedOutput any
	AssertionMode  AssertionMode
	GenerateReason string
	Valid          bool
	Reason         string
}

// Validate checks a generated case against the assertion contract.
func (g GeneratedCase) Validate() (string, bool) {
	if !g.Valid {
		return g.Reason, false
	}
	if g.Input == nil || g.ExpectedOutput == nil {
		return "empty input or expected output", false
	}
	switch g.AssertionMode {
	case AssertionExact, AssertionContains, AssertionRegex, AssertionJudge:
	default:
		return "unsupported assertion_mode " + string(g.AssertionMode), false
	}
	return "", true
}

type OnlineObservation struct {
	Score             float64
	CostUSD           float64
	LatencyMs         int64
	Success           bool
	SecurityViolation bool
}

func EvaluateAssertion(mode AssertionMode, actual, expected any) (AssertionResult, error) {
	switch mode {
	case AssertionExact:
		actualJSON, err := json.Marshal(actual)
		if err != nil {
			return AssertionResult{}, fmt.Errorf("marshal actual output: %w", err)
		}
		expectedJSON, err := json.Marshal(expected)
		if err != nil {
			return AssertionResult{}, fmt.Errorf("marshal expected output: %w", err)
		}
		actualValue, err := decodeExactJSON(actualJSON)
		if err != nil {
			return AssertionResult{}, fmt.Errorf("normalize actual output: %w", err)
		}
		expectedValue, err := decodeExactJSON(expectedJSON)
		if err != nil {
			return AssertionResult{}, fmt.Errorf("normalize expected output: %w", err)
		}
		passed := reflect.DeepEqual(actualValue, expectedValue)
		return AssertionResult{Passed: passed, Message: mismatchMessage(passed, "values differ")}, nil
	case AssertionContains:
		actualText, ok := actual.(string)
		if !ok {
			return AssertionResult{}, errors.New("contains assertion requires string actual output")
		}
		expectedText, ok := expected.(string)
		if !ok {
			return AssertionResult{}, errors.New("contains assertion requires string expected output")
		}
		passed := strings.Contains(actualText, expectedText)
		return AssertionResult{Passed: passed, Message: mismatchMessage(passed, "expected text not found")}, nil
	case AssertionRegex:
		actualText, ok := actual.(string)
		if !ok {
			return AssertionResult{}, errors.New("regex assertion requires string actual output")
		}
		pattern, ok := expected.(string)
		if !ok {
			return AssertionResult{}, errors.New("regex assertion requires string pattern")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return AssertionResult{}, fmt.Errorf("compile assertion regex: %w", err)
		}
		passed := re.MatchString(actualText)
		return AssertionResult{Passed: passed, Message: mismatchMessage(passed, "regular expression did not match")}, nil
	default:
		return AssertionResult{}, fmt.Errorf("unsupported assertion mode: %s", mode)
	}
}

func decodeExactJSON(value []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func mismatchMessage(passed bool, message string) string {
	if passed {
		return ""
	}
	return message
}
