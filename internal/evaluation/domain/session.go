package domain

import (
	"fmt"
	"strings"
)

// EvalSessionScript 是会话剧本（spec 2026-09-04 §5.4 阶段 B）：把 EvalCase 升级为
// 多轮会话形态——Goal 描述被测任务终点，Turns 逐轮注入 user 消息。会话是唯一原子
// 被测单元；一轮 = 一次 agent 执行/assistant 交付。nil EvalCase.Session = 旧单轮
// case（Session==nil 走既有单轮执行路径，本结构零改动语义）。
type EvalSessionScript struct {
	// Goal 是会话目标：judge / 演化轨迹判据判定「末轮是否到达目标」的语义锚点。
	Goal string `json:"goal"`
	// Turns 是剧本轮次（顺序注入）；非空，首轮 user 消息即会话开场。
	Turns []SessionTurn `json:"turns"`
}

// SessionTurn 是会话剧本的一轮。User 是注入 agent 的 user 消息；Probe 是该轮期望 /
// 探针锚点（逐轮 checkpoint：给 transcript judge 的参考描述，阶段 B 承载、S3
// 轨迹判据消费）；ToolSpec 是该轮工具序列过程断言（nil = 该轮不做确定性过程校验）。
type SessionTurn struct {
	User     string    `json:"user"`
	Probe    string    `json:"probe,omitempty"`
	ToolSpec *ToolSpec `json:"tool_spec,omitempty"`
}

// SessionTurnEvidence 是会话剧本一轮的执行证据投影（阶段 B；落 eval_case_results.turns
// JSONB）。Index 从 0 起；Output 是该轮 assistant 最终交付文本。
type SessionTurnEvidence struct {
	Index      int               `json:"index"`
	User       string            `json:"user,omitempty"`
	Output     string            `json:"output,omitempty"`
	TraceID    string            `json:"trace_id,omitempty"`
	Tokens     int               `json:"tokens"`
	CostUSD    float64           `json:"cost_usd"`
	DurationMs int               `json:"duration_ms"`
	Tools      []ToolObservation `json:"tools,omitempty"`
}

// IsSession 报告 case 是否为会话剧本形态（Session 非 nil → 多轮执行语义）。
func (c EvalCase) IsSession() bool { return c.Session != nil }

// Validate 校验会话剧本结构：轮次非空、每轮 user 消息非空。返回 (reason, valid)。
func (s *EvalSessionScript) Validate() (string, bool) {
	if s == nil {
		return "", true
	}
	if len(s.Turns) == 0 {
		return "session script: at least one turn required", false
	}
	for i, turn := range s.Turns {
		if strings.TrimSpace(turn.User) == "" {
			return fmt.Sprintf("session script: turn %d user message is empty", i), false
		}
	}
	return "", true
}
