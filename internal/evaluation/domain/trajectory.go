package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// TrajectoryKind 是会话演化轨迹判定结果（阶段 B §4.2）：判定会话逐轮是否「一路收敛
// 到目标」，而非绕圈无进展（停滞）或到达后又推翻（漂移）。
type TrajectoryKind string

const (
	// TrajectoryConverged 末轮输出命中终态期望：会话收敛到达目标。
	TrajectoryConverged TrajectoryKind = "converged"
	// TrajectoryStalled 多轮未到达终态且无实质进展（连续轮输出/工具序列重复，或
	// 规则会话始终未接近目标）。
	TrajectoryStalled TrajectoryKind = "stalled"
	// TrajectoryDrifted 前轮曾命中终态期望但后续轮次推翻（到达后漂移离开）。
	TrajectoryDrifted TrajectoryKind = "drifted"
	// TrajectoryNotApplicable 无轨迹可判：会话轮次不足 2 无「轮间演化」；或 judge
	// 会话终态由 LLM 权威判定，确定性规则不臆断收敛/漂移。
	TrajectoryNotApplicable TrajectoryKind = "not_applicable"
)

// Failed 报告轨迹是否判负（停滞/漂移）。会话容器级轨迹判负必须强制进入人工评审池
// （spec §4.5：整段演化没走对、逐轮单看挑不出独立错误的盲区样本），归集为单一谓词
// 供 application/review 消费。
func (k TrajectoryKind) Failed() bool {
	return k == TrajectoryStalled || k == TrajectoryDrifted
}

// TrajectoryVerdict 是演化轨迹判据的产出：Kind + 人类可读 Reason。挂在
// EvalCaseResult.Trajectory（*指针 + omitempty：旧单轮 case 无轨迹维度，JSON 省略以
// 保持既有 wire 兼容）。
type TrajectoryVerdict struct {
	Kind   TrajectoryKind `json:"kind"`
	Reason string         `json:"reason,omitempty"`
}

// EvaluateTrajectory 是演化轨迹判据纯函数（无 IO、无副作用）：确定性硬编码规则，
// AI 不做控制决策。入参 turns=会话逐轮执行证据、expected/mode=终态期望与断言方式、
// goal=会话目标（仅计入停滞归因文案，方便评审池快照定位被测意图）。判定次序
// （前一档命中即返回）：
//
//  1. 轮次 < 2 → NotApplicable：单轮没有「轮间演化」可判，交终态断言单独裁决。
//  2. 末轮命中终态期望 → Converged：规则模式用 EvaluateAssertion 判命中。
//  3. 前轮命中而末轮未守住 → Drifted：到达后被推翻是信息量最重的轨迹判负。
//  4. judge 模式 → 仅确定性信号：存在连续轮输出/工具序列重复 → Stalled；否则
//     NotApplicable——收敛/漂移是语义判定，EvaluateAssertion 不支持 judge，
//     留给 LLM judge + transcript（§4.3）。
//  5. 规则模式仍未到终态 → Stalled：多轮未到达目标即停滞归因（容器级判负）。
//
// 规则会话里「末轮命中」与「轨迹判负」互斥：末轮命中必 Converged，故 Passed 判定
// 无需再乘 Kind==Converged；judge 会话的收敛改写由终态分支在 LLM 通过后落
// （见 judgeCaseResult）。
func EvaluateTrajectory(turns []SessionTurnEvidence, expected any, mode AssertionMode, goal string) TrajectoryVerdict {
	if len(turns) < 2 {
		return TrajectoryVerdict{Kind: TrajectoryNotApplicable, Reason: "会话轮次不足 2，无演化轨迹可判"}
	}
	last := turns[len(turns)-1]
	if trajectoryHitTerminal(last.Output, expected, mode) {
		return TrajectoryVerdict{Kind: TrajectoryConverged, Reason: "末轮输出命中终态期望（收敛）"}
	}
	if mode == AssertionJudge {
		if trajectoryStalled(turns) {
			return TrajectoryVerdict{Kind: TrajectoryStalled, Reason: "连续轮次输出/工具序列重复（停滞）"}
		}
		return TrajectoryVerdict{Kind: TrajectoryNotApplicable, Reason: "judge 会话终态由 LLM 判定，轨迹判据不臆断收敛/漂移"}
	}
	for i := 0; i < len(turns)-1; i++ {
		if trajectoryHitTerminal(turns[i].Output, expected, mode) {
			return TrajectoryVerdict{Kind: TrajectoryDrifted, Reason: fmt.Sprintf("第 %d 轮曾命中终态期望但末轮未守住（漂移）", i)}
		}
	}
	if trajectoryStalled(turns) {
		return TrajectoryVerdict{Kind: TrajectoryStalled, Reason: "多轮未达终态且连续轮次输出/工具序列重复（停滞）"}
	}
	return TrajectoryVerdict{Kind: TrajectoryStalled, Reason: stalledFallbackReason(len(turns), goal)}
}

// trajectoryHitTerminal 判定单轮输出是否命中终态期望：规则模式复用 EvaluateAssertion
// （exact/contains/regex）；judge 模式 EvaluateAssertion 不支持（返回 error），恒
// false——终态到达由 LLM judge 权威裁决（§4.3），纯函数不臆断命中。
func trajectoryHitTerminal(output string, expected any, mode AssertionMode) bool {
	if mode == AssertionJudge {
		return false
	}
	res, err := EvaluateAssertion(mode, output, expected)
	return err == nil && res.Passed
}

// trajectoryStalled 判定是否存在「无进展」证据：连续两轮归一化输出相同，或连续两轮
// 工具序列相同。工具序列比较要求前轮确有工具调用——两轮都无工具调用不算停滞信号：
// 输出仍在变化说明会话仍在推进，仅「重复/空转」才是停滞。
func trajectoryStalled(turns []SessionTurnEvidence) bool {
	for i := 1; i < len(turns); i++ {
		prev, cur := turns[i-1], turns[i]
		if strings.TrimSpace(prev.Output) == strings.TrimSpace(cur.Output) {
			return true
		}
		if len(prev.Tools) > 0 && sameToolSequence(prev.Tools, cur.Tools) {
			return true
		}
	}
	return false
}

// sameToolSequence 比较两轮工具序列（按调用顺序的工具名全等）。
func sameToolSequence(a, b []ToolObservation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ToolName != b[i].ToolName {
			return false
		}
	}
	return true
}

// stalledFallbackReason 组装规则模式「多轮未到达终态」的停滞归因文案：goal 非空时
// 附带目标描述（无敏感数据，goal 是评测作者编写的被测意图），便于评审池快照定位。
func stalledFallbackReason(turnCount int, goal string) string {
	if goal == "" {
		return fmt.Sprintf("会话 %d 轮未到达终态目标（停滞）", turnCount)
	}
	return fmt.Sprintf("会话 %d 轮未到达目标 %q（停滞）", turnCount, goal)
}

// FormatTranscript 把会话逐轮证据渲染成纯文本 transcript 交给 LLM judge（阶段 B
// §4.3 judge 会话调用形态：LLM 只评「末轮是否到达目标 / 守住探针」）。结构 =
// Goal 头 + 逐轮 User/Assistant 文本；不渲染工具序列/敏感负载（工具行为属过程断言
// 通道，§6.5 step_judge 单独收）。截断预算复用 pkg/constants 渲染常量：单轮
// user/assistant 文本按 rune 截断至 SessionTurnTextMaxRunes；保留最近
// SessionTranscriptMaxTurns 轮；总长超 SessionTranscriptMaxRunes 时自最旧轮逐轮
// 丢弃——judge 判末端，优先保留最新内容。
func FormatTranscript(turns []SessionTurnEvidence, goal string) string {
	var sb strings.Builder
	sb.WriteString("Goal: ")
	sb.WriteString(goal)

	budget := constants.SessionTranscriptMaxRunes - utf8.RuneCountInString(sb.String())
	if budget < 0 {
		budget = 0
	}
	// 自最新轮向最旧轮收集（预算内尽量多留，超总预算即停），再倒序拼成时间序。
	var blocks []string
	for i := len(turns) - 1; i >= 0; i-- {
		if len(blocks) >= constants.SessionTranscriptMaxTurns {
			break
		}
		block := formatTranscriptTurn(turns[i])
		blockRunes := utf8.RuneCountInString(block)
		if blockRunes > budget {
			if len(blocks) == 0 {
				blocks = append(blocks, block) // 最新一轮必须保留：judge 至少要看到末端
			}
			break
		}
		blocks = append(blocks, block)
		budget -= blockRunes
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		sb.WriteString("\n\n")
		sb.WriteString(blocks[i])
	}
	return sb.String()
}

// formatTranscriptTurn 渲染单轮 transcript 块："[Turn N]\nUser: ...\nAssistant: ..."。
// user/assistant 文本按 rune 截断（truncateRunes 超限追加省略号），空文本轮省略对应行。
func formatTranscriptTurn(turn SessionTurnEvidence) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Turn %d]", turn.Index)
	if user := strings.TrimSpace(turn.User); user != "" {
		sb.WriteString("\nUser: ")
		sb.WriteString(truncateRunes(user, constants.SessionTurnTextMaxRunes))
	}
	if out := strings.TrimSpace(turn.Output); out != "" {
		sb.WriteString("\nAssistant: ")
		sb.WriteString(truncateRunes(out, constants.SessionTurnTextMaxRunes))
	}
	return sb.String()
}
