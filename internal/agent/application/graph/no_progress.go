package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
)

// NoProgressTerminated 是无进展停滞的业务终止标记（值 = reason）。与
// CostBudgetTerminated 同类：属业务终止而非错误——保留已产出部分结果，
// trace/memory run skeleton 透传该自由串，零契约改动。
const NoProgressTerminated = "no_progress"

// IsBusinessTermination 报告 reason 是否属引擎级业务终止（可解释的收尾，非错误）。
// collectGraphResult 等上浮 reason 的唯一判据：业务终止把 reason 拷入结果，其他
// 一律不写。新增业务终止值必须在此登记。
func IsBusinessTermination(reason string) bool {
	return reason == CostBudgetTerminated || reason == NoProgressTerminated
}

// no-progress 判定三态：由当前连续同指纹 run 长度派生（见 noProgressDetail）。
// 无状态派生——每次 LLM 入口从完整 s.Messages 重算，不新增 ReActState 标量
// 计数器（子循环用值拷贝、MergeReActWave 不合并标量、checkpoint 不持久化）。
const (
	noProgressNone      = ""
	noProgressNudge     = "nudge"
	noProgressTerminate = "terminate"
)

const (
	// noProgressNotice 是终止时写入 s.Output 的确定性中文说明（%[1]s = 重复描述）。
	// 防「静默空正文」：被强制结束后外层/评测至少读到发生了什么。固定模板，透给
	// eval/对话；禁止带内部标识或工具错误正文。
	noProgressNotice = "执行提示换路后仍无进展，已提前结束：连续 %[1]s。请调整指令或补充信息后重试。"
	// noProgressNudgeFmt 是注入本轮回合请求的换路提示（%[1]d = 已连续同指纹轮数）。
	// 只进本轮请求、不落持久会话；提示模型换用不同工具/参数或直接作答。
	noProgressNudgeFmt = "你最近的 %[1]d 轮工具调用相同且结果未变，没有取得进展。请换用不同的工具或参数，或直接给出最终答案；不要重复上次的操作。"
)

// noProgressRound 是当前任务里一个已完成的工具回合（assistant 的 tool_calls 组 +
// 其全部 tool 结果）。ok=false 表示该回合含错误结果：错误循环已由 per-tool
// stop-loss 管理，不计入停滞 run，避免抢先降级收尾路径。
type noProgressRound struct {
	fingerprint string
	ok          bool
}

// completedRoundsSinceTask 返回最新 user 消息之后的已完成工具回合序列。复用
// groupMessages 的配对规则切分回合；范围限定在末条 user 之后 = 当前任务（对齐
// compaction 的 protected-user 语义），避免跨轮历史污染判定。出现非工具回合即
// 截断（LLM 入口处末 user 之后只可能是连续已完成工具回合）。
func completedRoundsSinceTask(msgs []port.LLMMessage) []noProgressRound {
	groups := groupMessages(msgs)
	start := 0
	for i, g := range groups {
		if g.role0 == "user" {
			start = i + 1
		}
	}
	var out []noProgressRound
	for i := start; i < len(groups); i++ {
		g := groups[i]
		if !g.hasTool {
			break
		}
		out = append(out, noProgressRound{
			fingerprint: roundFingerprint(g),
			ok:          !roundHasError(g),
		})
	}
	return out
}

// roundFingerprint 把一整轮的模型动作与结果压成归一化指纹：有序
// toolName(canonicalJSON(arguments)) + 各工具结果归一化摘要的顺序拼接。同工具
// 同参但结果在变（分页/轮询/累积型返回）→ 摘要不同 → 判为有进展；同参同结果
// → 同指纹（真停滞）。
func roundFingerprint(g msgGroup) string {
	assistant := g.msgs[0]
	actions := make([]string, 0, len(assistant.ToolCalls))
	for _, tc := range assistant.ToolCalls {
		actions = append(actions, tc.Name+"("+canonicalArgs(tc.Arguments)+")")
	}
	sort.Strings(actions)
	action := strings.Join(actions, " ")
	// 每则工具结果单独归一化取摘要后顺序拼接：64 位 hex 定长、边界无歧义，
	// 大小写/空白差异在摘要内折叠。不直接拼原始正文 + 分隔符再整体哈希——
	// 正文尾随空白会让分隔符变成独立 token，破坏空白归一化。
	var results strings.Builder
	for _, m := range g.msgs[1:] {
		results.WriteString(normalizeResultDigest(m.Content))
	}
	return action + "|" + results.String()
}

// canonicalArgs 序列化 tool arguments：encoding/json 对 map 键排序 ⇒ 键序/空白
// 无关。marshal 失败（非常见）退空对象串，仍可比较工具名（保守：宁可多跑一轮）。
func canonicalArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// normalizeResultDigest 对单则工具结果文本做归一化摘要：大小写 + 任意空白折叠后
// sha256。对全量内容哈希（不做头截断），保证任何正文变化——含尾部累积增长——
// 都算结果变化，从根上防「长而有效的任务被误杀」。内容受上游 guard 限界
// （32KB/64KB 级），sha256 成本可忽略。
func normalizeResultDigest(s string) string {
	norm := strings.Join(strings.Fields(strings.ToLower(s)), " ")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// roundHasError 报告回合内是否有失败的工具结果：引擎把工具错误统一渲染成 content
// 前缀 "error: "（react_tool.go 各 exec 路径）。含该前缀即不进停滞 run——错误循环
// 由 per-tool stop-loss 管；同前缀的软降级「not configured」成功结果一并排除，
// 属保守（本就跑满 MaxSteps，无回归）。
func roundHasError(g msgGroup) bool {
	for _, m := range g.msgs[1:] {
		if strings.HasPrefix(m.Content, "error: ") {
			return true
		}
	}
	return false
}

// currentRunLen 统计末尾连续同指纹的成功回合数：自后向前回溯，遇错误回合或指纹
// 变化即坍缩（换路/失败即中断）。无状态记账：每次入口 run 至多 +1，进程内 run
// 达终止阈值必然已在 nudge 阈值提示过，无需 marker。
func currentRunLen(rounds []noProgressRound) int {
	run := 0
	anchor := ""
	for i := len(rounds) - 1; i >= 0; i-- {
		r := rounds[i]
		if !r.ok || (run > 0 && r.fingerprint != anchor) {
			break
		}
		anchor = r.fingerprint
		run++
	}
	return run
}

// noProgressDetail 由当前停滞状态派生三态判定与 run 长度。守卫：已业务终止 →
// None（makeLLMNode 入口短路已先行，此处为纯函数兜底）；强制收尾步（工具被剥离、
// 要最终答案）让位 → None，避免在即将产出真答案时误杀。runLen≥termTh → Terminate；
// ≥nudgeTh → Nudge；否则 None。
func noProgressDetail(s ReActState, nudgeTh, termTh int) (verdict string, runLen int) {
	if s.TerminatedBy != "" {
		return noProgressNone, 0
	}
	if s.MaxLLMSteps > 0 && s.Steps >= s.MaxLLMSteps-1 {
		return noProgressNone, 0
	}
	runLen = currentRunLen(completedRoundsSinceTask(s.Messages))
	switch {
	case runLen >= termTh:
		return noProgressTerminate, runLen
	case runLen >= nudgeTh:
		return noProgressNudge, runLen
	default:
		return noProgressNone, runLen
	}
}

// noProgressRepeatPhrase 生成 notice 中重复动作的中文描述（确定性强、无内部标识）。
func noProgressRepeatPhrase(runLen int) string {
	return fmt.Sprintf("%d 轮相同操作", runLen)
}

// noProgressTerminationOutput 生成终止说明正文：由 makeLLMNode Terminate 分支写入
// s.Output，作为用户/外层可见的确定性结果。
func noProgressTerminationOutput(runLen int) string {
	return fmt.Sprintf(noProgressNotice, noProgressRepeatPhrase(runLen))
}

// noProgressNudgeInstruction 生成本轮回合注入的换路提示正文（轮数 = 已连续同指纹数，
// 与 nudge 阈值一致，保证文案数字与触发条件同步）。
func noProgressNudgeInstruction(runLen int) string {
	return fmt.Sprintf(noProgressNudgeFmt, runLen)
}
