package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// trajTurn 构造一条会话轮次证据（Index 从 0 起）。Tokens/Cost/Duration 与轨迹判据
// 无关，给定确定值即可；Tools 可选（工具序列停滞信号需真实工具调用）。
func trajTurn(idx int, user, out string, tools ...ToolObservation) SessionTurnEvidence {
	return SessionTurnEvidence{
		Index:      idx,
		User:       user,
		Output:     out,
		TraceID:    "trace-traj",
		Tokens:     10,
		CostUSD:    0.01,
		DurationMs: 20,
		Tools:      tools,
	}
}

func trajTool(name string) ToolObservation {
	return ToolObservation{ToolName: name, StepIndex: 0}
}

// TestEvaluateTrajectory 表驱动覆盖演化轨迹判据五个分支（spec 阶段 B §4.2）：
// 单轮 NA / 末轮收敛 / 前轮命中末轮推翻（漂移）/ 轮间重复（停滞）/ 规则模式兜底停滞；
// judge 模式仅确定性重复判停滞、无重复判 NA（收敛/漂移交 LLM）。
func TestEvaluateTrajectory(t *testing.T) {
	tests := []struct {
		name     string
		turns    []SessionTurnEvidence
		expected any
		mode     AssertionMode
		goal     string
		wantKind TrajectoryKind
		wantSub  string // Reason 子串（空 = 不检查）
	}{
		{
			name:     "single turn is not applicable",
			turns:    []SessionTurnEvidence{trajTurn(0, "start", "first draft")},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryNotApplicable,
		},
		{
			name: "converged when last turn hits terminal",
			turns: []SessionTurnEvidence{
				trajTurn(0, "draft", "draft text"),
				trajTurn(1, "revise", "final answer here"),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryConverged,
			wantSub:  "末轮",
		},
		{
			name: "converged after earlier misses then hit",
			turns: []SessionTurnEvidence{
				trajTurn(0, "draft", "nothing useful"),
				trajTurn(1, "revise", "still nothing"),
				trajTurn(2, "final", "ok final deliverable"),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryConverged,
		},
		{
			name: "drifted when earlier turn hit then last gave up",
			turns: []SessionTurnEvidence{
				trajTurn(0, "first", "this is the final answer"),
				trajTurn(1, "again", "sorry I am not sure"),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryDrifted,
			wantSub:  "第 0 轮",
		},
		{
			name: "drifted reports the earliest hit turn index",
			turns: []SessionTurnEvidence{
				trajTurn(0, "draft", "sketch"),
				trajTurn(1, "revise", "meets final target"),
				trajTurn(2, "revert", "let me start over"),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryDrifted,
			wantSub:  "第 1 轮",
		},
		{
			name: "stalled when adjacent outputs identical and terminal never reached",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "same incomplete output"),
				trajTurn(1, "q2", "same incomplete output"),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryStalled,
			wantSub:  "重复",
		},
		{
			name: "stalled when adjacent tool sequences identical despite changing text",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "probe result A", trajTool("search"), trajTool("read")),
				trajTurn(1, "q2", "probe result B", trajTool("search"), trajTool("read")),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryStalled,
			wantSub:  "重复",
		},
		{
			name: "not stalled when tool sequences differ",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "probe A", trajTool("search")),
				trajTurn(1, "q2", "probe B", trajTool("search"), trajTool("read")),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryStalled, // 未达终态 + 兜底停滞归因
			wantSub:  "未到达终态",
		},
		{
			name: "not stalled when both turns have no tools and outputs differ",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "working through it"),
				trajTurn(1, "q2", "making progress now"),
			},
			expected: "final",
			mode:     AssertionContains,
			wantKind: TrajectoryStalled, // 兜底：2 轮仍未达终态目标
			wantSub:  "2 轮",
		},
		{
			name: "rule fallback stalled attributes goal",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "a"),
				trajTurn(1, "q2", "b"),
				trajTurn(2, "q3", "c"),
			},
			expected: "final",
			mode:     AssertionContains,
			goal:     "生成 JSON 报告",
			wantKind: TrajectoryStalled,
			wantSub:  `"生成 JSON 报告"`,
		},
		{
			name: "judge mode repeats output is stalled",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "answer v1"),
				trajTurn(1, "q2", "answer v1"),
			},
			expected: "final",
			mode:     AssertionJudge,
			wantKind: TrajectoryStalled,
		},
		{
			name: "judge mode without repetition is not applicable",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "first attempt"),
				trajTurn(1, "q2", "second attempt, better"),
			},
			expected: "final",
			mode:     AssertionJudge,
			wantKind: TrajectoryNotApplicable,
			wantSub:  "LLM 判定",
		},
		{
			name: "judge converged handled at final branch not by pure function",
			turns: []SessionTurnEvidence{
				trajTurn(0, "q", "draft"),
				trajTurn(1, "q2", "anything"),
			},
			expected: "final",
			mode:     AssertionJudge,
			wantKind: TrajectoryNotApplicable, // 终态翻转由 judgeCaseResult 收敛后落
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateTrajectory(tc.turns, tc.expected, tc.mode, tc.goal)
			if got.Kind != tc.wantKind {
				t.Fatalf("Kind = %q, want %q (Reason: %s)", got.Kind, tc.wantKind, got.Reason)
			}
			if tc.wantSub != "" && !strings.Contains(got.Reason, tc.wantSub) {
				t.Fatalf("Reason = %q, want substring %q", got.Reason, tc.wantSub)
			}
		})
	}
}

// TestTrajectoryKindFailed 谓词归集：停滞/漂移判负（review 池强制入池入口），
// 收敛/NA 不判负。
func TestTrajectoryKindFailed(t *testing.T) {
	negative := []TrajectoryKind{TrajectoryStalled, TrajectoryDrifted}
	for _, kind := range negative {
		if !kind.Failed() {
			t.Fatalf("%q Failed() = false, want true", kind)
		}
	}
	for _, kind := range []TrajectoryKind{TrajectoryConverged, TrajectoryNotApplicable} {
		if kind.Failed() {
			t.Fatalf("%q Failed() = true, want false", kind)
		}
	}
}

// TestTrajectoryVerdictJSON 断言 verdict 序列化契约：kind 必须出现（评审快照按 kind
// 归因），reason 空时省略（语义自洽）。EvalCaseResult 侧指针 omitempty 省略由
// application/service 测试覆盖。
func TestTrajectoryVerdictJSON(t *testing.T) {
	raw := []byte(`{"kind":"drifted"}`)
	var got TrajectoryVerdict
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != TrajectoryDrifted {
		t.Fatalf("Kind = %q, want drifted", got.Kind)
	}
	out, err := json.Marshal(TrajectoryVerdict{Kind: TrajectoryStalled})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"kind":"stalled"`) || strings.Contains(string(out), "reason") {
		t.Fatalf("marshal = %s, want kind present and reason omitted", out)
	}
}

func TestFormatTranscript(t *testing.T) {
	turns := []SessionTurnEvidence{
		trajTurn(0, "请写一个函数", "第一版草稿"),
		trajTurn(1, "请修正", "def f(): return 1"),
	}

	t.Run("renders goal header and chronological turns", func(t *testing.T) {
		got := FormatTranscript(turns, "生成可运行代码")
		want := "Goal: 生成可运行代码\n\n[Turn 0]\nUser: 请写一个函数\nAssistant: 第一版草稿\n\n[Turn 1]\nUser: 请修正\nAssistant: def f(): return 1"
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("omits empty user and output lines", func(t *testing.T) {
		sparse := []SessionTurnEvidence{trajTurn(3, "", "只有输出")}
		got := FormatTranscript(sparse, "")
		want := "Goal: \n\n[Turn 3]\nAssistant: 只有输出"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("truncates a single over-long assistant text by runes", func(t *testing.T) {
		long := strings.Repeat("x", constants.SessionTurnTextMaxRunes+100)
		got := FormatTranscript([]SessionTurnEvidence{trajTurn(0, "", long)}, "g")
		prefix := "Goal: g\n\n[Turn 0]\nAssistant: "
		if !strings.HasPrefix(got, prefix) {
			t.Fatalf("missing prefix, got head %q", got[:min(40, len(got))])
		}
		body := strings.TrimPrefix(got, prefix)
		if utf8.RuneCountInString(body) != constants.SessionTurnTextMaxRunes+1 { // +省略号
			t.Fatalf("body runes = %d, want %d", utf8.RuneCountInString(body), constants.SessionTurnTextMaxRunes+1)
		}
		if !strings.HasSuffix(body, "…") {
			t.Fatalf("body should end with ellipsis, got tail %q", body[len(body)-3:])
		}
	})

	t.Run("drops oldest turns first when over total budget, never the latest", func(t *testing.T) {
		// 每轮输出 500 rune，总预算 8000 只容得下约 15 轮；20 轮时最新保留、最旧丢弃。
		var many []SessionTurnEvidence
		for i := 0; i < 20; i++ {
			out := strings.Repeat("y", 500)
			many = append(many, trajTurn(i, "", out))
		}
		got := FormatTranscript(many, "g")
		if !strings.Contains(got, "[Turn 19]") {
			t.Fatalf("latest turn dropped: head %q", got[:min(60, len(got))])
		}
		if strings.Contains(got, "[Turn 0]") {
			t.Fatalf("expected oldest turn dropped, got len=%d", utf8.RuneCountInString(got))
		}
		if utf8.RuneCountInString(got) > constants.SessionTranscriptMaxRunes {
			t.Fatalf("total runes = %d exceed budget %d", utf8.RuneCountInString(got), constants.SessionTranscriptMaxRunes)
		}
	})

	t.Run("caps turn count at most recent SessionTranscriptMaxTurns", func(t *testing.T) {
		// 单轮文本极小 → 受轮次上限约束而非预算约束。
		var many []SessionTurnEvidence
		for i := 0; i < constants.SessionTranscriptMaxTurns+10; i++ {
			many = append(many, trajTurn(i, "", "z"))
		}
		got := FormatTranscript(many, "g")
		if strings.Contains(got, "[Turn 0]") {
			t.Fatalf("expected oldest dropped by turn cap, got len=%d", utf8.RuneCountInString(got))
		}
		last := constants.SessionTranscriptMaxTurns + 9
		if !strings.Contains(got, fmt.Sprintf("[Turn %d]", last)) {
			t.Fatalf("latest turn %d not retained", last)
		}
	})
}
