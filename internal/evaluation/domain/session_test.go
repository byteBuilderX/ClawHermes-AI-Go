package domain

import (
	"encoding/json"
	"testing"
)

func TestEvalCaseIsSession(t *testing.T) {
	cases := []struct {
		name string
		tc   EvalCase
		want bool
	}{
		{"legacy single-turn case is not a session", EvalCase{Input: "问", ExpectedOutput: "答"}, false},
		{"session script present is a session", EvalCase{Session: &EvalSessionScript{
			Goal: "g", Turns: []SessionTurn{{User: "u"}},
		}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tc.IsSession(); got != tc.want {
				t.Fatalf("IsSession() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvalSessionScriptValidate(t *testing.T) {
	cases := []struct {
		name   string
		script *EvalSessionScript
		valid  bool
	}{
		{"nil script is valid", nil, true},
		{"empty turns is invalid", &EvalSessionScript{Goal: "g"}, false},
		{"blank user after trim is invalid", &EvalSessionScript{
			Goal: "g", Turns: []SessionTurn{{User: "  "}},
		}, false},
		{"whitespace-only user is invalid", &EvalSessionScript{
			Goal: "g", Turns: []SessionTurn{{User: "\n"}},
		}, false},
		{"all turns non-empty is valid", &EvalSessionScript{
			Goal: "g", Turns: []SessionTurn{{User: "开场"}, {User: "追问", Probe: "期望守住", ToolSpec: &ToolSpec{}}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, valid := tc.script.Validate()
			if valid != tc.valid {
				t.Fatalf("Validate() valid=%v, want %v", valid, tc.valid)
			}
		})
	}
}

func TestEvalSessionScriptValidateReportsFirstEmptyTurn(t *testing.T) {
	script := &EvalSessionScript{Goal: "g", Turns: []SessionTurn{{User: "ok"}, {User: ""}, {User: "ok"}}}
	reason, valid := script.Validate()
	if valid {
		t.Fatalf("expected invalid session script")
	}
	if reason == "" {
		t.Fatalf("expected a diagnostic reason for the empty turn")
	}
}

// TestEvalSessionScriptJSONRoundTrip 校验 session JSONB 编解码逐字段一致（repository
// round-trip 的 domain 侧契约）：nil/非 nil 指针与 omitempty 的持久化形态。
func TestEvalSessionScriptJSONRoundTrip(t *testing.T) {
	tc := EvalCase{
		Session: &EvalSessionScript{
			Goal: "处理退换货",
			Turns: []SessionTurn{
				{User: "我的订单 3 天没发货", ToolSpec: &ToolSpec{MustNotCall: []string{"delete"}}},
				{User: "好的，帮我申请退款", Probe: "应调用退款工具而非删除", ToolSpec: &ToolSpec{MustCall: []string{"refund"}}},
			},
		},
	}
	raw, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back EvalCase
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Session == nil {
		t.Fatalf("session lost on JSON round-trip")
	}
	if back.Session.Goal != "处理退换货" || len(back.Session.Turns) != 2 {
		t.Fatalf("session shape mismatch: %+v", back.Session)
	}
	turn := back.Session.Turns[1]
	if turn.User != "好的，帮我申请退款" || turn.Probe != "应调用退款工具而非删除" {
		t.Fatalf("turn 1 mismatch: %+v", turn)
	}
	if turn.ToolSpec == nil || len(turn.ToolSpec.MustCall) != 1 || turn.ToolSpec.MustCall[0] != "refund" {
		t.Fatalf("per-turn tool spec mismatch: %+v", turn.ToolSpec)
	}
}

func TestEvalCaseResultTurnsJSONRoundTrip(t *testing.T) {
	result := EvalCaseResult{
		Turns: []SessionTurnEvidence{
			{Index: 0, User: "开场", Output: "已查询", Tokens: 12, CostUSD: 0.01, DurationMs: 300},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back EvalCaseResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Turns) != 1 || back.Turns[0].Output != "已查询" || back.Turns[0].Index != 0 {
		t.Fatalf("turns round-trip mismatch: %+v", back.Turns)
	}
}
