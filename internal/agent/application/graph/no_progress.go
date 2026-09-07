package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
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
	// noProgressOscNotice 是振荡停滞（在少量相同操作间反复切换）终止时写入
	// s.Output 的确定性中文说明（%[1]d = 窗口内成功回合数）。与连续版 notice
	// 并列，但点破「换路过仍在循环」，防外层把终止误读为单一路径卡死。
	noProgressOscNotice = "执行提示换路后仍在相同操作间反复切换（最近 %[1]d 轮），已提前结束。请调整指令或补充信息后重试。"
	// noProgressOscNudgeFmt 是振荡停滞首次命中时注入本轮回合请求的换路提示
	// （%[1]d = 窗口内成功回合数）。模型看似在做事（切换操作）但未推进，需
	// 明确点破循环本身。
	noProgressOscNudgeFmt = "你最近的 %[1]d 轮工具调用在少量相同操作间反复切换，没有取得进展。请换用不同的工具或参数，或直接给出最终答案。"
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

// isForcedFinalAnswerStep 报告当前步是否为强制收尾步（工具被剥离、要模型直接给最终
// 答案，见 prepareLLMRequest 的 MaxLLMSteps 收尾）。连续与振荡停滞判定都在此让位：
// 即将产出真答案，不得误杀。
func isForcedFinalAnswerStep(s ReActState) bool {
	return s.MaxLLMSteps > 0 && s.Steps >= s.MaxLLMSteps-1
}

// noProgressDetail 由当前停滞状态派生连续 run 三态判定与 run 长度。守卫：已业务
// 终止 → None（LLM 节点入口短路已先行，此处为纯函数兜底）；强制收尾步让位 → None，
// 避免在即将产出真答案时误杀。runLen≥termTh → Terminate；≥nudgeTh → Nudge；
// 否则 None（此时才由组合判定交振荡窗口，见 decideNoProgress）。
func noProgressDetail(s ReActState, nudgeTh, termTh int) (verdict string, runLen int) {
	if s.TerminatedBy != "" {
		return noProgressNone, 0
	}
	if isForcedFinalAnswerStep(s) {
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

// oscillationStall 判定最近的工具回合是否呈振荡停滞：在少量不同指纹间反复切换
// （A→B→A→B→A→B）。与连续 run 停滞互补——振荡时 run 坍缩（指纹在变），S1
// 连续检测不触发。判定基于窗口内统计：取末尾 ≤ window 个成功（ok）回合，去重
// 指纹数 ∈ [2, oscillateTh]（指纹种类更多 = 系统性换路尝试，不算振荡）且最高频
// 指纹重复 ≥ oscillateTh。返回命中的窗口成功回合数与最高频重复数；未命中
// stalled=false（okRounds 仍返回实际成功回合数，供上层判断样本是否足够）。
func oscillationStall(rounds []noProgressRound, oscillateTh, window int) (stalled bool, okRounds, maxRepeat int) {
	okRounds = 0
	counts := make(map[string]int)
	for i := len(rounds) - 1; i >= 0 && okRounds < window; i-- {
		if !rounds[i].ok {
			continue
		}
		counts[rounds[i].fingerprint]++
		okRounds++
	}
	if okRounds < 1 {
		return false, 0, 0
	}
	distinct := len(counts)
	for _, c := range counts {
		if c > maxRepeat {
			maxRepeat = c
		}
	}
	// okRounds/maxRepeat 始终返回窗口内真实统计（上层可据 maxRepeat 判断是否该
	// 归连续 run 而非振荡），stalled 才按阈值判定。
	if distinct < 2 || distinct > oscillateTh || maxRepeat < oscillateTh {
		return false, okRounds, maxRepeat
	}
	return true, okRounds, maxRepeat
}

// oscillationNudgeInstruction 生成本轮回合注入的振荡换路提示正文（轮数 = 窗口内
// 成功回合数，与触发样本一致）。区别于连续版文案：点破「在少量操作间反复切换」
// 这一形态。
func oscillationNudgeInstruction(okRounds int) string {
	return fmt.Sprintf(noProgressOscNudgeFmt, okRounds)
}

// oscillationTerminationOutput 生成振荡停滞终止说明正文：由 decideNoProgress 在已
// 提示换路后仍振荡时写入 s.Output（reason 同 NoProgressTerminated，文案区分形态）。
func oscillationTerminationOutput(okRounds int) string {
	return fmt.Sprintf(noProgressOscNotice, okRounds)
}

// decideNoProgress 在 LLM 节点入口做无进展停滞的完整判定并施加所需状态副作用
// （振荡标记置位 / 锚点复位 / 业务终止写入），返回 (更新后 s, 判定, nudgeContent)：
//   - noProgressNone：照常发起本轮 LLM；
//   - noProgressNudge：本轮请求尾部注入换路提示（nudgeContent 恒非空，按命中形态
//     给连续或振荡对应文案）；
//   - noProgressTerminate：已写 TerminatedBy / Output / 日志，调用方直接 return。
//
// 判定顺序（各形态阈值互斥，不会歧义命中）：已业务终止 / 强制收尾步 → None（让位，
// 不评估任何形态）；连续同指纹 run ≥4 → Terminate、≥3 → Nudge（此时窗口内指纹单调，
// 振荡 distinct<2 必不命中）；其余（run 坍缩 / 真进展）→ 振荡窗口判定。全部派生自
// s.Messages 与 s 内振荡提示状态，无新增回合计数器（checkpoint 只重建 Messages/Steps）。
func decideNoProgress(s ReActState, logger *zap.Logger) (ReActState, string, string) {
	if s.TerminatedBy != "" || isForcedFinalAnswerStep(s) {
		return s, noProgressNone, ""
	}
	verdict, runLen := noProgressDetail(s, constants.AgentNoProgressNudgeThreshold,
		constants.AgentNoProgressTerminateThreshold)
	switch verdict {
	case noProgressTerminate:
		s.TerminatedBy = NoProgressTerminated
		s.Output = noProgressTerminationOutput(runLen)
		logger.Warn("react llm: no-progress termination",
			zap.Int("consecutive_rounds", runLen))
		return s, noProgressTerminate, ""
	case noProgressNudge:
		return s, noProgressNudge, noProgressNudgeInstruction(runLen)
	}
	// 连续 run 未命中（run 坍缩 / 真进展）：交振荡窗口判定（decideOscillationNoProgress
	// 内部处理锚点失效复位 / 首次置位 / 终止）。
	return decideOscillationNoProgress(s, logger)
}

// decideOscillationNoProgress 判定振荡停滞（在少量指纹间反复切换，A→B→A→B→A→B）的
// nudge-then-cut 状态机并施加副作用，返回判定与（nudge 时）注入文案：
//   - 未提示过：窗口命中 → 置位 + 记录锚点（NoProgressOscillationResetAt = 当前已完成
//     回合数）+ 返回 Nudge 与振荡文案，给模型一次换路转机；
//   - 已提示过：只看锚点之后新增回合的窗口统计。nudge 前窗口里的旧振荡轮（某指纹已
//     重复 ≥3）不参与——否则模型收到提示后即使立即换全新指纹，也会因旧惯性在下一入口
//     被误杀，拿不到「换路证明期」。锚点后重新累积满窗口 → Terminate（一次转机，之后
//     仍振荡即停）；锚点失效（nudge 后新 user 使任务范围收缩）→ 复位标记回首次检测。
//
// 仅在连续 run 未命中时由 decideNoProgress 调用（run 坍缩时振荡才是停滞形态）。
func decideOscillationNoProgress(s ReActState, logger *zap.Logger) (ReActState, string, string) {
	rounds := completedRoundsSinceTask(s.Messages)
	if s.NoProgressOscillationNudged {
		if s.NoProgressOscillationResetAt > len(rounds) {
			// nudge 后出现新 user（任务范围收缩）：旧振荡提示历史随旧任务失效，复位
			// 标记——本入口及之后按首次检测全新判定（新任务干净起点）。
			s.NoProgressOscillationNudged = false
			s.NoProgressOscillationResetAt = 0
			return s, noProgressNone, ""
		}
		if stalled, okRounds, maxRepeat := oscillationStall(
			rounds[s.NoProgressOscillationResetAt:],
			constants.AgentNoProgressOscillationThreshold,
			constants.AgentNoProgressWindow,
		); stalled {
			s.TerminatedBy = NoProgressTerminated
			s.Output = oscillationTerminationOutput(okRounds)
			logger.Warn("react llm: oscillation no-progress termination",
				zap.Int("ok_rounds", okRounds),
				zap.Int("max_repeat", maxRepeat))
			return s, noProgressTerminate, ""
		}
		return s, noProgressNone, ""
	}
	if stalled, okRounds, _ := oscillationStall(
		rounds,
		constants.AgentNoProgressOscillationThreshold,
		constants.AgentNoProgressWindow,
	); stalled {
		// 首次命中：置位 + 锚点 = 当前已完成回合数。nudge 后的判定以锚点为窗口起点，
		// 给模型一段不被旧惯性污染的换路证明期。
		s.NoProgressOscillationNudged = true
		s.NoProgressOscillationResetAt = len(rounds)
		return s, noProgressNudge, oscillationNudgeInstruction(okRounds)
	}
	return s, noProgressNone, ""
}
