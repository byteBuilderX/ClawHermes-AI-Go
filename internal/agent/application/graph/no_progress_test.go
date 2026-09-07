package graph

import (
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/stretchr/testify/require"
)

// npToolRound 构造一个已完成工具回合的 message 序列：assistant(tool_calls) +
// 紧跟的 tool 结果（groupMessages 的配对规则）。
func npToolRound(name string, args map[string]any, result string) []port.LLMMessage {
	msgs := []port.LLMMessage{{
		Role: "assistant",
		ToolCalls: []port.ToolCall{{
			ID: "tc-1", Name: name, Arguments: args,
		}},
	}}
	if result != "" {
		msgs = append(msgs, port.LLMMessage{Role: "tool", Content: result, ToolCallID: "tc-1"})
	}
	return msgs
}

// npState 组装「user 任务 + 若干已完成回合」的 state，供纯函数判定。
func npState(rounds ...[]port.LLMMessage) ReActState {
	msgs := []port.LLMMessage{{Role: "user", Content: "task"}}
	for _, r := range rounds {
		msgs = append(msgs, r...)
	}
	return ReActState{Messages: msgs}
}

// npRoundFingerprints 取判定序列中每个回合的指纹（缺省全 ok）。
func npRoundFingerprints(rounds []noProgressRound) []string {
	fps := make([]string, 0, len(rounds))
	for _, r := range rounds {
		fps = append(fps, r.fingerprint)
	}
	return fps
}

func TestCompletedRoundsSinceTask_SameFingerprint(t *testing.T) {
	rounds := completedRoundsSinceTask(npState(
		npToolRound("calc", map[string]any{"a": 1}, "2"),
		npToolRound("calc", map[string]any{"a": 1}, "2"),
	).Messages)
	require.Len(t, rounds, 2)
	require.True(t, rounds[0].ok && rounds[1].ok)
	require.Equal(t, rounds[0].fingerprint, rounds[1].fingerprint, "同工具+同参+同结果 → 同指纹")
}

func TestCompletedRoundsSinceTask_DifferentArgsDifferentFingerprint(t *testing.T) {
	rounds := completedRoundsSinceTask(npState(
		npToolRound("calc", map[string]any{"a": 1}, "2"),
		npToolRound("calc", map[string]any{"a": 2}, "3"),
	).Messages)
	fps := npRoundFingerprints(rounds)
	require.NotEqual(t, fps[0], fps[1], "改参 → 真进展 → 指纹不同")
}

func TestCompletedRoundsSinceTask_ArgsKeyOrderIrrelevant(t *testing.T) {
	rounds := completedRoundsSinceTask(npState(
		npToolRound("calc", map[string]any{"a": 1, "b": 2}, "3"),
		npToolRound("calc", map[string]any{"b": 2, "a": 1}, "3"),
	).Messages)
	fps := npRoundFingerprints(rounds)
	require.Equal(t, fps[0], fps[1], "参数键序无关（json.Marshal 排序键）")
}

func TestCompletedRoundsSinceTask_ResultCaseWhitespaceNormalized(t *testing.T) {
	rounds := completedRoundsSinceTask(npState(
		npToolRound("read", nil, "OK"),
		npToolRound("read", nil, " ok \n\n"),
	).Messages)
	fps := npRoundFingerprints(rounds)
	require.Equal(t, fps[0], fps[1], "结果大小写/空白差异不算结果变化")
}

func TestCompletedRoundsSinceTask_SameArgsDifferentResultNotStalled(t *testing.T) {
	// 同工具同参但结果在变（分页/轮询）→ 摘要不同 → 判有进展。
	rounds := completedRoundsSinceTask(npState(
		npToolRound("page", map[string]any{"n": 1}, "page-1"),
		npToolRound("page", map[string]any{"n": 1}, "page-2"),
	).Messages)
	fps := npRoundFingerprints(rounds)
	require.NotEqual(t, fps[0], fps[1])
}

func TestCompletedRoundsSinceTask_AccumulatingResultIsProgress(t *testing.T) {
	// 结果尾部增长（builder 累积型返回）也应算结果变化，防长而有效的任务被误杀。
	rounds := completedRoundsSinceTask(npState(
		npToolRound("append", nil, strings.Repeat("x", 50)),
		npToolRound("append", nil, strings.Repeat("x", 50)+"tail"),
	).Messages)
	fps := npRoundFingerprints(rounds)
	require.NotEqual(t, fps[0], fps[1], "尾部累积增长 → 结果不同 → 不算停滞")
}

func TestCompletedRoundsSinceTask_ErrorRoundFlaggedAndBreaksRun(t *testing.T) {
	state := npState(
		npToolRound("calc", map[string]any{"a": 1}, "2"),
		npToolRound("calc", map[string]any{"a": 1}, "error: boom"),
		npToolRound("calc", map[string]any{"a": 1}, "2"),
	)
	rounds := completedRoundsSinceTask(state.Messages)
	require.Len(t, rounds, 3)
	require.False(t, rounds[1].ok, "错误结果回合 ok=false")
	require.True(t, rounds[0].ok && rounds[2].ok)
	// 尾随 [err, ok]：ok 回合不跨错误回合桥接 → 只计 1。
	require.Equal(t, 1, currentRunLen(rounds))
}

func TestCurrentRunLen_TrailingOnlyAndCollapse(t *testing.T) {
	tests := []struct {
		name   string
		rounds []noProgressRound
		want   int
	}{
		{"empty", nil, 0},
		{"single", []noProgressRound{{fingerprint: "A", ok: true}}, 1},
		{"threeSame", []noProgressRound{
			{fingerprint: "A", ok: true}, {fingerprint: "A", ok: true}, {fingerprint: "A", ok: true},
		}, 3},
		{"changeResets", []noProgressRound{
			{fingerprint: "A", ok: true}, {fingerprint: "A", ok: true}, {fingerprint: "B", ok: true},
		}, 1},
		{"trailingErrorResets", []noProgressRound{
			{fingerprint: "A", ok: true}, {fingerprint: "A", ok: true}, {fingerprint: "A", ok: false},
		}, 0},
		{"errorMidBreaksBridge", []noProgressRound{
			{fingerprint: "A", ok: true}, {fingerprint: "A", ok: false}, {fingerprint: "A", ok: true},
		}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, currentRunLen(tc.rounds))
		})
	}
}

func TestNoProgressDetail_ThresholdLadder(t *testing.T) {
	const (
		nudgeTh = constants.AgentNoProgressNudgeThreshold
		termTh  = constants.AgentNoProgressTerminateThreshold
	)
	build := func(n int) ReActState {
		var rounds [][]port.LLMMessage
		for i := 0; i < n; i++ {
			rounds = append(rounds, npToolRound("calc", map[string]any{"a": 1}, "2"))
		}
		return npState(rounds...)
	}
	tests := []struct {
		name        string
		s           ReActState
		wantVerdict string
		wantRun     int
	}{
		{"zeroRoundsNone", build(0), noProgressNone, 0},
		{"belowNudgeNone", build(nudgeTh - 1), noProgressNone, nudgeTh - 1},
		{"nudgeAtThreshold", build(nudgeTh), noProgressNudge, nudgeTh},
		{"terminateAtCeiling", build(termTh), noProgressTerminate, termTh},
		{"beyondCeilingTerminates", build(termTh + 2), noProgressTerminate, termTh + 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verdict, run := noProgressDetail(tc.s, nudgeTh, termTh)
			require.Equal(t, tc.wantVerdict, verdict)
			require.Equal(t, tc.wantRun, run)
		})
	}
}

func TestNoProgressDetail_ForcedAnswerStepWins(t *testing.T) {
	const termTh = constants.AgentNoProgressTerminateThreshold
	s := npState(
		npToolRound("calc", map[string]any{"a": 1}, "2"),
		npToolRound("calc", map[string]any{"a": 1}, "2"),
		npToolRound("calc", map[string]any{"a": 1}, "2"),
		npToolRound("calc", map[string]any{"a": 1}, "2"),
	)
	s.Steps = 3
	s.MaxLLMSteps = 4 // Steps>=MaxLLMSteps-1 → 强制收尾步，不得误杀
	verdict, _ := noProgressDetail(s, constants.AgentNoProgressNudgeThreshold, termTh)
	require.Equal(t, noProgressNone, verdict, "强制收尾步让位：即将产出真答案")
}

func TestNoProgressDetail_AlreadyTerminatedIsNone(t *testing.T) {
	s := npState(npToolRound("calc", map[string]any{"a": 1}, "2"))
	s.TerminatedBy = CostBudgetTerminated
	verdict, _ := noProgressDetail(s, 3, 4)
	require.Equal(t, noProgressNone, verdict)
}

func TestNoProgressDetail_NewUserTurnResetsScope(t *testing.T) {
	// 前一个任务的 3 个同指纹回合不得污染新任务：只统计最新 user 之后。
	msgs := []port.LLMMessage{{Role: "user", Content: "old task"}}
	old := npToolRound("calc", map[string]any{"a": 1}, "2")
	for i := 0; i < 3; i++ {
		msgs = append(msgs, old...)
	}
	msgs = append(msgs, port.LLMMessage{Role: "user", Content: "new task"})
	msgs = append(msgs, npToolRound("calc", map[string]any{"a": 2}, "3")...)
	s := ReActState{Messages: msgs}
	rounds := completedRoundsSinceTask(s.Messages)
	require.Len(t, rounds, 1, "只含新任务已完成回合")
	verdict, run := noProgressDetail(s, constants.AgentNoProgressNudgeThreshold, constants.AgentNoProgressTerminateThreshold)
	require.Equal(t, noProgressNone, verdict)
	require.Equal(t, 1, run)
}

func TestIsBusinessTermination(t *testing.T) {
	require.True(t, IsBusinessTermination(CostBudgetTerminated))
	require.True(t, IsBusinessTermination(NoProgressTerminated))
	require.False(t, IsBusinessTermination(""))
	require.False(t, IsBusinessTermination("boom"))
}

func TestNoProgressTextDeterministic(t *testing.T) {
	run := constants.AgentNoProgressTerminateThreshold
	output := noProgressTerminationOutput(run)
	require.Contains(t, output, "提前结束")
	require.Contains(t, output, "连续 4 轮相同操作")
	require.Equal(t, output, noProgressTerminationOutput(run), "终止说明必须确定（stable 供 eval/断言）")

	nudge := noProgressNudgeInstruction(constants.AgentNoProgressNudgeThreshold)
	require.Contains(t, nudge, "3 轮工具调用")
	require.Contains(t, nudge, "不要重复上次的操作")
}

// npRounds 把指纹/ok 序列转成 noProgressRound 序列（测振荡窗口统计用）。
func npRounds(fps []string, notOK ...int) []noProgressRound {
	bad := make(map[int]bool)
	for _, i := range notOK {
		bad[i] = true
	}
	out := make([]noProgressRound, 0, len(fps))
	for i, f := range fps {
		out = append(out, noProgressRound{fingerprint: f, ok: !bad[i]})
	}
	return out
}

func TestOscillationStall(t *testing.T) {
	const (
		oscTh  = constants.AgentNoProgressOscillationThreshold
		window = constants.AgentNoProgressWindow
	)
	tests := []struct {
		name          string
		rounds        []noProgressRound
		wantStalled   bool
		wantOK        int
		wantMaxRepeat int
	}{
		{"strictAlternationDetects", npRounds([]string{"A", "B", "A", "B", "A", "B"}), true, 6, 3},
		{"fiveSamplesStillDetects", npRounds([]string{"A", "B", "A", "B", "A"}), true, 5, 3},
		{"tooFewSamplesNoFire", npRounds([]string{"A", "B", "A", "B"}), false, 4, 2},
		{"threeDistinctBalancedNoFire", npRounds([]string{"A", "B", "C", "A", "B", "C"}), false, 6, 2},
		{"manyDistinctNoFire", npRounds([]string{"A", "B", "C", "D", "A", "B"}), false, 6, 2},
		{"singleFingerprintNoFire", npRounds([]string{"A", "A", "A", "A", "A", "A"}), false, 6, 6},
		{"emptyNoFire", nil, false, 0, 0},
		// 错误回合不进窗口统计：B 位全错后窗口只剩 A A A（单一指纹）→ 归连续
		// run 检测，振荡不误报。
		{"errorRoundsExcludedFromWindow", npRounds([]string{"A", "B", "A", "B", "A", "B"}, 1, 3, 5), false, 3, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stalled, okRounds, maxRepeat := oscillationStall(tc.rounds, oscTh, window)
			require.Equal(t, tc.wantStalled, stalled)
			require.Equal(t, tc.wantOK, okRounds)
			require.Equal(t, tc.wantMaxRepeat, maxRepeat)
		})
	}
}

func TestOscillationStall_WindowSizeMatters(t *testing.T) {
	// 同一 A/B 交替样本：窗口太小（样本不足，某指纹重复 <3）不命中；窗口放宽到
	// 覆盖 ≥5 个 ok 回合后命中。验证 window 参数真实参与判定（防把窗口写死）。
	const oscTh = constants.AgentNoProgressOscillationThreshold
	rounds := npRounds([]string{"A", "B", "A", "B", "A", "B"})
	stalled3, ok3, _ := oscillationStall(rounds, oscTh, 3)
	require.False(t, stalled3, "窗口 3：末尾 B A B → B 重复 2 次 < 阈值")
	require.Equal(t, 3, ok3)
	stalled6, ok6, max6 := oscillationStall(rounds, oscTh, 6)
	require.True(t, stalled6, "窗口 6：A×3 B×3 → 命中振荡")
	require.Equal(t, 6, ok6)
	require.Equal(t, 3, max6)
}

func TestOscillationTextDeterministic(t *testing.T) {
	output := oscillationTerminationOutput(6)
	require.Contains(t, output, "反复切换")
	require.Contains(t, output, "提前结束")
	require.Equal(t, output, oscillationTerminationOutput(6), "振荡终止说明必须确定")

	nudge := oscillationNudgeInstruction(6)
	require.Contains(t, nudge, "6 轮工具调用")
	require.Contains(t, nudge, "反复切换")
	require.NotContains(t, nudge, "不要重复上次的操作", "振荡文案独立于连续 run 文案")
}
