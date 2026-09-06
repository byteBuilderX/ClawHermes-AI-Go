package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// HumanVerdict 人工评审结论 4 分类（spec §6.6 回写动作）。
// 常量以 HumanVerdict 前缀命名：同包已有 ObservationVerdict.VerdictPass，
// 裸名 VerdictPass 已被占用，故按 Go 惯例用类型前缀消除歧义。
type HumanVerdict string

const (
	HumanVerdictPass             HumanVerdict = "pass"
	HumanVerdictFail             HumanVerdict = "fail"
	HumanVerdictJudgeMisjudgment HumanVerdict = "judge_misjudgment"
	HumanVerdictCaseRevision     HumanVerdict = "case_revision"
)

// Valid 校验人工结论枚举。
func (v HumanVerdict) Valid() bool {
	switch v {
	case HumanVerdictPass, HumanVerdictFail, HumanVerdictJudgeMisjudgment, HumanVerdictCaseRevision:
		return true
	default:
		return false
	}
}

// ReviewTriggerReason 入池原因（硬编码规则，AI 不做控制决策）。
type ReviewTriggerReason string

const (
	TriggerLowConfidence         ReviewTriggerReason = "low_confidence"
	TriggerDimensionSplit        ReviewTriggerReason = "dimension_split"
	TriggerJudgeRuleConflict     ReviewTriggerReason = "judge_rule_conflict"
	TriggerNeedsReview           ReviewTriggerReason = "needs_review"
	TriggerProcessOutputConflict ReviewTriggerReason = "process_output_conflict"
	// TriggerBehaviorAnomaly 行为判异入池：Signals.Behavior 含 abandonment/escalation 且
	// Verdict=flag（spec §3.2-③）。trigger_reason 枚举 DDL P1 T2 已含，P2 零 DDL。
	TriggerBehaviorAnomaly ReviewTriggerReason = "behavior_anomaly"
	// TriggerTrajectoryFailed 会话演化轨迹判负入池（阶段 B §4.5 盲点：容器级轨迹
	// 负例——整段会话停滞/漂移、逐轮单看无独立错误的样本——必须强制人工复核）。
	TriggerTrajectoryFailed ReviewTriggerReason = "trajectory_failed"
)

// Valid 校验入池原因枚举。
func (r ReviewTriggerReason) Valid() bool {
	switch r {
	case TriggerLowConfidence, TriggerDimensionSplit, TriggerJudgeRuleConflict, TriggerNeedsReview,
		TriggerProcessOutputConflict, TriggerBehaviorAnomaly, TriggerTrajectoryFailed:
		return true
	default:
		return false
	}
}

// ReviewRiskLevel 评审优先级（spec §6.6 规模控制：评审池按风险排序，安全/写操作/高危资源优先）。
type ReviewRiskLevel string

const (
	ReviewRiskHigh   ReviewRiskLevel = "high"
	ReviewRiskMedium ReviewRiskLevel = "medium"
	ReviewRiskLow    ReviewRiskLevel = "low"
)

// RiskLevel 把入池原因映射为评审优先级。硬编码规则，与 persistence 的 reviewRiskOrderSQL
// 保持镜像（两端注释互指，修改必须同步）：
//   - high：judge_rule_conflict（规则护栏命中 = 安全类）、process_output_conflict（副作用/写操作越界）；
//   - medium：low_confidence、dimension_split、needs_review、behavior_anomaly、trajectory_failed
//     （会话整段停滞/漂移需人工复核归因）；
//   - low：其余（未来新增触发默认低，人工可随时介入）。
func (r ReviewTriggerReason) RiskLevel() ReviewRiskLevel {
	switch r {
	case TriggerJudgeRuleConflict, TriggerProcessOutputConflict:
		return ReviewRiskHigh
	case TriggerLowConfidence, TriggerDimensionSplit, TriggerNeedsReview, TriggerBehaviorAnomaly,
		TriggerTrajectoryFailed:
		return ReviewRiskMedium
	default:
		return ReviewRiskLow
	}
}

// ReviewSourceType 评审条目来源。
type ReviewSourceType string

const (
	ReviewSourceObservation ReviewSourceType = "observation"
	ReviewSourceCaseResult  ReviewSourceType = "case_result"
)

// ReviewItemStatus 评审条目状态。
type ReviewItemStatus string

const (
	ReviewStatusPending  ReviewItemStatus = "pending"
	ReviewStatusReviewed ReviewItemStatus = "reviewed"
)

// ReviewConfig 评审池触发配置。默认值在 pkg/constants（Task 1），wiring 组装。
type ReviewConfig struct {
	// LowConfidenceThreshold 是 low_confidence 触发阈值（默认 constants.ReviewLowConfidenceThreshold）。
	LowConfidenceThreshold float64
	// JudgePassThreshold 是维度通过/跌阈分界（沿用 constants.JudgeBelowThreshold）。
	JudgePassThreshold float64
}

// ReviewItem 评审池条目（对应 eval_review_items）。
type ReviewItem struct {
	ID            string              `json:"id"`
	SourceType    ReviewSourceType    `json:"source_type"`
	SourceID      string              `json:"source_id"`
	RunID         string              `json:"run_id,omitempty"`
	TraceID       string              `json:"trace_id,omitempty"`
	ResourceKind  ResourceKind        `json:"resource_kind"`
	ResourceID    string              `json:"resource_id"`
	TriggerReason ReviewTriggerReason `json:"trigger_reason"`
	// RiskLevel 评审优先级（派生自 trigger_reason；不落库，repository 读取后填充，
	// JSON 透出供前端展示排序依据）。
	RiskLevel    ReviewRiskLevel  `json:"risk_level"`
	Snapshot     any              `json:"snapshot"`
	Status       ReviewItemStatus `json:"status"`
	HumanVerdict HumanVerdict     `json:"human_verdict,omitempty"`
	Reviewer     string           `json:"reviewer,omitempty"`
	ReviewReason string           `json:"review_reason,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	ReviewedAt   *time.Time       `json:"reviewed_at,omitempty"`
}

// CalibrationSample judge 误判校准样本（对应 eval_calibration_samples）。
type CalibrationSample struct {
	ID           string           `json:"id"`
	ReviewItemID string           `json:"review_item_id"`
	SourceType   ReviewSourceType `json:"source_type"`
	SourceID     string           `json:"source_id"`
	JudgeModel   string           `json:"judge_model,omitempty"`
	Signals      any              `json:"signals"`
	HumanVerdict HumanVerdict     `json:"human_verdict"`
	Reviewer     string           `json:"reviewer"`
	Reason       string           `json:"reason,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
}

// AttributionEntry 产品缺陷归因条目（对应 eval_attribution_entries，轻量记录）。
type AttributionEntry struct {
	ID           string           `json:"id"`
	ReviewItemID string           `json:"review_item_id"`
	SourceType   ReviewSourceType `json:"source_type"`
	SourceID     string           `json:"source_id"`
	ResourceKind ResourceKind     `json:"resource_kind"`
	ResourceID   string           `json:"resource_id"`
	Dimension    string           `json:"dimension,omitempty"`
	Snapshot     any              `json:"snapshot"`
	Status       string           `json:"status"`
	Reviewer     string           `json:"reviewer"`
	Reason       string           `json:"reason,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
}

// TriggersForObservation 计算观测应入池的触发原因（空 = 不进池）。纯函数，硬编码规则。
// 规则（spec §6.6 + §3.2-③）：
//  1. low_confidence：任一 judge 维度 Confidence < cfg.LowConfidenceThreshold，
//     或 Confidence 落在边界区间 [ConfidenceBoundaryLow, ConfidenceBoundaryHigh]，
//     或打分理由含糊（hasVagueReason：为空/过短 <VagueReasonMinRunes/含不确定性措辞）；
//  2. dimension_split：存在 Score >= JudgePassThreshold 且存在 Score < JudgePassThreshold；
//  3. judge_rule_conflict：规则命中（Signals.Rule 非空）+ Verdict == block + 全部维度 pass；
//  4. behavior_anomaly：Signals.Behavior 含 abandonment/escalation 且 Verdict == flag
//     （无 judge 的行为判异也入池；但无 judge 的 rule-block-only 观测 Verdict=block，
//     不满足 flag 守卫，仍不进池，勿破坏现状）。
func TriggersForObservation(obs *EvalObservation, cfg ReviewConfig) []ReviewTriggerReason {
	if obs == nil {
		return nil
	}
	var triggers []ReviewTriggerReason
	if isBehaviorAnomaly(obs) {
		triggers = append(triggers, TriggerBehaviorAnomaly)
	}
	if len(obs.Signals.Judge) == 0 {
		return triggers
	}
	if hasLowConfidence(obs.Signals.Judge, cfg.LowConfidenceThreshold) {
		triggers = append(triggers, TriggerLowConfidence)
	}
	below, above := splitExists(obs.Signals.Judge, cfg.JudgePassThreshold)
	if below && above {
		triggers = append(triggers, TriggerDimensionSplit)
	}
	if len(obs.Signals.Rule) > 0 && obs.Verdict == VerdictBlock && !below {
		triggers = append(triggers, TriggerJudgeRuleConflict)
	}
	return triggers
}

// isBehaviorAnomaly 判定行为判异：Signals.Behavior 含 abandonment/escalation 且 Verdict == flag
// （spec §3.2-③）。复合布尔独立成小函数，保持 TriggersForObservation 圈复杂度 ≤10。
func isBehaviorAnomaly(obs *EvalObservation) bool {
	if obs.Verdict != VerdictFlag {
		return false
	}
	return obs.Signals.Behavior.Abandonment || obs.Signals.Behavior.Escalation
}

// hasLowConfidence 返回任一 judge 维度满足低置信：Confidence < threshold、落在边界区间，
// 或打分理由含糊（spec §6.6 置信度机制）。
func hasLowConfidence(judge []JudgeSignal, threshold float64) bool {
	for _, j := range judge {
		if j.Confidence < threshold || isBoundaryConfidence(j.Confidence) || hasVagueReason(j.Reason) {
			return true
		}
	}
	return false
}

// vagueReasonKeywords 是打分理由含糊的硬编码判据（spec §6.6：理由含不确定性措辞视为含糊）。
// 规则断言天然确定，不参与——本判定仅作用于 judge 信号。
var vagueReasonKeywords = []string{
	"不确定", "无法确定", "无法判断", "不能判断", "不清楚", "可能", "也许", "大概", "似乎",
}

// isBoundaryConfidence 判定 confidence 是否落在边界区间 [ConfidenceBoundaryLow, ConfidenceBoundaryHigh]
// （spec §6.6：分数落在边界视为低置信）。
func isBoundaryConfidence(c float64) bool {
	return c >= constants.ConfidenceBoundaryLow && c <= constants.ConfidenceBoundaryHigh
}

// hasVagueReason 判定打分理由是否含糊：为空/过短（< VagueReasonMinRunes rune），或含不确定性措辞。
// spec §6.6「打分理由含糊也视为低置信」。
func hasVagueReason(reason string) bool {
	if strings.TrimSpace(reason) == "" {
		return true
	}
	if utf8.RuneCountInString(reason) < constants.VagueReasonMinRunes {
		return true
	}
	for _, kw := range vagueReasonKeywords {
		if strings.Contains(reason, kw) {
			return true
		}
	}
	return false
}

// splitExists 返回是否存在 Score 低于 threshold（below）与不低于 threshold（above）。
func splitExists(judge []JudgeSignal, threshold float64) (below, above bool) {
	for _, j := range judge {
		if j.Score < threshold {
			below = true
		} else {
			above = true
		}
	}
	return below, above
}

// TriggersForProcessConflict 计算输出断言与过程断言不一致的入池原因（§6.5 §6.6）：
// 仅输出通过但过程失败（outputPass=true, processPass=false）时触发
// process_output_conflict；其余组合（一致或输出已失败）不构成冲突，不进池。
// 纯函数，硬编码规则。规则断言 case 也复用本函数（规则 case 无 judge 信号，
// 不能走完整 TriggersForCaseResult 以免 low_confidence 误触发）。
func TriggersForProcessConflict(outputPass, processPass bool) []ReviewTriggerReason {
	if outputPass && !processPass {
		return []ReviewTriggerReason{TriggerProcessOutputConflict}
	}
	return nil
}

// TriggersForCaseResult 计算评测集 judge 判定的入池原因（空 = 不进池）。
// 规则（spec §6.6）：
//  1. needs_review：EvalCase.NeedsReview == true（assertion_mode 分支由调用方强制，本函数不检查）；
//  2. low_confidence：assertion.Confidence < cfg.LowConfidenceThreshold，
//     或 Confidence 落在边界区间 [ConfidenceBoundaryLow, ConfidenceBoundaryHigh]，
//     或打分理由含糊（hasVagueReason：为空/过短 <VagueReasonMinRunes/含不确定性措辞）；
//  3. process_output_conflict：输出断言通过但过程断言失败（§6.5）。
func TriggersForCaseResult(
	needsReview bool, outputPass, processPass bool, assertion AssertionResult, cfg ReviewConfig,
) []ReviewTriggerReason {
	var triggers []ReviewTriggerReason
	if needsReview {
		triggers = append(triggers, TriggerNeedsReview)
	}
	if assertion.Confidence < cfg.LowConfidenceThreshold ||
		isBoundaryConfidence(assertion.Confidence) ||
		hasVagueReason(assertion.Message) {
		triggers = append(triggers, TriggerLowConfidence)
	}
	return append(triggers, TriggersForProcessConflict(outputPass, processPass)...)
}
