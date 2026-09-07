package graph_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/byteBuilderX/stratum/internal/agent/application/graph"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// noopState 组一个「工具面只有 noop、工具恒返回 ok」的图测试 state（复用现有
// MaxIterations 测试同款 shape：ProviderType mcp + ToolExecutionFn 走工具执行）。
func noopState(maxTokensPerExec, maxLLMSteps int) graph.ReActState {
	return graph.ReActState{
		Model:                 "qwen-turbo",
		Messages:              []port.LLMMessage{{Role: "user", Content: "loop"}},
		MaxTokensPerExecution: maxTokensPerExec,
		MaxLLMSteps:           maxLLMSteps,
		AvailableTools: []port.ToolDefinition{{Name: "noop", ProviderType: "mcp",
			ServerID: "test", Metadata: map[string]any{"risk_level": "read"}}},
		ToolExecutionFn: func(context.Context, port.ToolExecutionRequest) (any, error) {
			return guardedToolOutput("ok"), nil
		},
	}
}

func buildNoopGraph(t *testing.T, stub *capGWSequence) *graph.CompiledGraph[graph.ReActState] {
	t.Helper()
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)
	return cg
}

// lastLLMRequest 返回最近一次分发的 LLM 请求（routeLLM 实际收到的 messages）。
func lastLLMRequest(stub *capGWSequence) port.LLMCapRequest {
	return stub.llmReqs[len(stub.llmReqs)-1]
}

func TestBuildReActGraph_NudgeThenComplyEndsNormally(t *testing.T) {
	// 同指纹 3 轮后：第 4 轮请求注入换路提示，模型收到提示后给最终答案 → 正常成功，
	// 无 TerminatedBy、无多余工具轮；提示文本确实出现在本轮分发请求里。
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "c1", Name: "noop", Arguments: map[string]any{}}}},
		{ToolCalls: []port.ToolCall{{ID: "c2", Name: "noop", Arguments: map[string]any{}}}},
		{ToolCalls: []port.ToolCall{{ID: "c3", Name: "noop", Arguments: map[string]any{}}}},
		{Content: "done"},
	}}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(0, 0),
		graph.RunConfig[graph.ReActState]{MaxSteps: 20})
	require.NoError(t, err)
	require.Equal(t, "", out.TerminatedBy)
	require.Equal(t, "done", out.Output)
	require.Len(t, stub.llmReqs, 4)
	last := lastLLMRequest(stub)
	tail := last.Messages[len(last.Messages)-1]
	require.Equal(t, "user", tail.Role)
	require.Contains(t, tail.Content, "不要重复上次的操作")
}

func TestBuildReActGraph_NudgeIgnoredTerminatesBeforeMaxSteps(t *testing.T) {
	// 提示轮后模型仍重复同一 noop：run 达 4 即业务终止（无 4 轮后的多余 LLM/工具轮）。
	stub := &capGWSequence{infinite: port.CapabilityResponse{
		ToolCalls: []port.ToolCall{{ID: "c1", Name: "noop", Arguments: map[string]any{}}},
	}}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(0, 0),
		graph.RunConfig[graph.ReActState]{MaxSteps: 20})
	require.NoError(t, err)
	require.Equal(t, graph.NoProgressTerminated, out.TerminatedBy)
	require.Len(t, stub.llmReqs, 4)
	require.Len(t, out.AllToolCalls, 4)
}

func TestBuildReActGraph_DistinctArgsLoopHitsMaxSteps(t *testing.T) {
	// 每轮换参 → 真进展（指纹不同）→ 不触发 no-progress，max-steps 撞顶兜底保留。
	stub := &capGWSequence{responses: func() []port.CapabilityResponse {
		responses := make([]port.CapabilityResponse, 0, 4)
		for i := 0; i < 4; i++ {
			responses = append(responses, port.CapabilityResponse{
				ToolCalls: []port.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "noop", Arguments: map[string]any{"n": i}}},
			})
		}
		return responses
	}()}
	cg := buildNoopGraph(t, stub)
	_, err := cg.Invoke(context.Background(), noopState(0, 0),
		graph.RunConfig[graph.ReActState]{MaxSteps: 4})
	require.ErrorContains(t, err, "max steps")
}

func TestBuildReActGraph_ForcedAnswerWinsOverNoProgress(t *testing.T) {
	// MaxLLMSteps=4：runLen3 恰好落在强制收尾步（Steps>=MaxLLMSteps-1）→ 不让位给
	// no-progress nudge，直接给最终答案；断言本轮请求无换路提示注入。
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "c1", Name: "noop", Arguments: map[string]any{}}}},
		{ToolCalls: []port.ToolCall{{ID: "c2", Name: "noop", Arguments: map[string]any{}}}},
		{ToolCalls: []port.ToolCall{{ID: "c3", Name: "noop", Arguments: map[string]any{}}}},
		{Content: "done"},
	}}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(0, 4),
		graph.RunConfig[graph.ReActState]{MaxSteps: 20})
	require.NoError(t, err)
	require.Equal(t, "", out.TerminatedBy)
	require.Equal(t, "done", out.Output)
	require.Len(t, stub.llmReqs, 4)
	last := lastLLMRequest(stub)
	for _, m := range last.Messages {
		require.NotContains(t, m.Content, "不要重复上次的操作", "强制收尾步不得注入换路提示")
	}
}

func TestBuildReActGraph_OscillationNudgeThenTerminates(t *testing.T) {
	// 在 a=1 / a=2 两个操作间反复切换（A→B→A→B→A→B）。A 首次重复到 3 次（5 个成功
	// 回合）→ 第 6 个 LLM 入口注入振荡换路提示并置位 NoProgressOscillationNudged，
	// 锚点 = 5；模型收到提示后仍回振荡（未换路）→ 锚点之后重新累积满窗口（再 5 个
	// 新回合）→ 第 11 个入口业务终止（一次转机后仍振荡即停）。terminate 在入口短路，
	// 无第 11 次分发；nudge 只注入一次，其后的正常分发轮不带提示。
	resp := func(id, arg int) port.CapabilityResponse {
		return port.CapabilityResponse{ToolCalls: []port.ToolCall{{
			ID: fmt.Sprintf("c%d", id), Name: "noop", Arguments: map[string]any{"n": arg},
		}}}
	}
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		resp(1, 1), resp(2, 2), resp(3, 1), resp(4, 2), resp(5, 1), // A×3 → nudge 注入
		resp(6, 2), resp(7, 1), resp(8, 2), resp(9, 1), resp(10, 2), // nudge 后仍振荡 → 终止
	}}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(0, 0),
		graph.RunConfig[graph.ReActState]{MaxSteps: 30})
	require.NoError(t, err)
	require.Equal(t, graph.NoProgressTerminated, out.TerminatedBy)
	require.Contains(t, out.Output, "反复切换", "振荡终止说明点破切换形态")
	require.Len(t, stub.llmReqs, 10, "10 轮振荡全分发；终止在入口短路不再发起 LLM")
	require.Len(t, out.AllToolCalls, 10)
	// nudge 注入轮 = 第 6 个请求（振荡 5 轮后首个入口）：尾部是振荡文案，非连续文案。
	req6 := stub.llmReqs[5]
	tail := req6.Messages[len(req6.Messages)-1]
	require.Equal(t, "user", tail.Role)
	require.Contains(t, tail.Content, "反复切换")
	require.NotContains(t, tail.Content, "不要重复上次的操作")
	// nudge 后模型继续振荡的判定轮（锚点后样本累积中）不带提示——终止在入口短路，
	// 不追加分发。
	for _, req := range stub.llmReqs[6:] {
		require.NotContains(t, req.Messages[len(req.Messages)-1].Content, "反复切换",
			"nudge 已给过，锚点后累积期不再重复提示")
	}
}

func TestBuildReActGraph_OscillationNudgeThenComplyEndsNormally(t *testing.T) {
	// 振荡 nudge 后模型系统性换路（每轮全新参数）→ 锚点之后的回合指纹分散、不再命中
	// 振荡 → 正常给最终答案收尾，无 TerminatedBy。验证 nudge 给的是「换路证明期」而
	// 非立即判决：模型拿到提示后第一轮换新指纹即活，不因 nudge 前窗口里的旧 A×3 惯性
	// 被误杀。
	resp := func(id, arg int) port.CapabilityResponse {
		return port.CapabilityResponse{ToolCalls: []port.ToolCall{{
			ID: fmt.Sprintf("c%d", id), Name: "noop", Arguments: map[string]any{"n": arg},
		}}}
	}
	responses := []port.CapabilityResponse{
		resp(1, 1), resp(2, 2), resp(3, 1), resp(4, 2), resp(5, 1), // A×3 → nudge 注入
		resp(6, 3), resp(7, 4), resp(8, 5), resp(9, 6), // nudge 后每轮新指纹（真换路）
		{Content: "done"},
	}
	stub := &capGWSequence{responses: responses}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(0, 0),
		graph.RunConfig[graph.ReActState]{MaxSteps: 30})
	require.NoError(t, err)
	require.Equal(t, "", out.TerminatedBy)
	require.Equal(t, "done", out.Output)
	require.Len(t, stub.llmReqs, len(responses), "10 轮全分发（无提前终止）")
	// 振荡 nudge 只出现在第 6 个请求（振荡 5 轮后首个入口）；其后换路推进轮不注入。
	req6 := stub.llmReqs[5]
	tail6 := req6.Messages[len(req6.Messages)-1]
	require.Equal(t, "user", tail6.Role)
	require.Contains(t, tail6.Content, "反复切换")
	for _, req := range stub.llmReqs[6:] {
		require.NotContains(t, req.Messages[len(req.Messages)-1].Content, "反复切换",
			"换路推进后不再注入振荡提示")
	}
}

func TestBuildReActGraph_OscillationThreeDistinctNoFire(t *testing.T) {
	// 模型在 3 种参数间轮转（a1→a2→a3→a1→a2→a3）：窗口内无指纹重复 ≥3（各自 ≤2）
	// → 振荡不命中；连续 run 也坍缩。这是系统性换路尝试，直至模型给最终答案，不误杀。
	resp := func(id, arg int) port.CapabilityResponse {
		return port.CapabilityResponse{ToolCalls: []port.ToolCall{{
			ID: fmt.Sprintf("c%d", id), Name: "noop", Arguments: map[string]any{"n": arg},
		}}}
	}
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		resp(1, 1), resp(2, 2), resp(3, 3), resp(4, 1), resp(5, 2), resp(6, 3),
		{Content: "done"},
	}}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(0, 0),
		graph.RunConfig[graph.ReActState]{MaxSteps: 20})
	require.NoError(t, err)
	require.Equal(t, "", out.TerminatedBy)
	require.Equal(t, "done", out.Output)
	require.Len(t, stub.llmReqs, 7)
	for _, req := range stub.llmReqs {
		for _, m := range req.Messages {
			require.NotContains(t, m.Content, "反复切换", "三指纹轮转不触发振荡提示")
		}
	}
}

func TestBuildReActGraph_OscillationHistoryYieldsToForcedFinalAnswer(t *testing.T) {
	// 历史 5 轮振荡（A×3 本会 nudge/terminate），但当前步是 MaxLLMSteps 强制收尾步
	// （Steps ≥ MaxLLMSteps-1，工具已剥离、要最终答案）→ 让位：不注入换路提示、
	// 不终止，模型直接给答案正常收尾。防「即将产出真答案时被历史振荡误杀」。
	resp := func(id, arg int) port.CapabilityResponse {
		return port.CapabilityResponse{ToolCalls: []port.ToolCall{{
			ID: fmt.Sprintf("c%d", id), Name: "noop", Arguments: map[string]any{"n": arg},
		}}}
	}
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		resp(1, 1), resp(2, 2), resp(3, 1), resp(4, 2), resp(5, 1),
		{Content: "done"}, // forced 步模型直接作答
	}}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(0, 6),
		graph.RunConfig[graph.ReActState]{MaxSteps: 30})
	require.NoError(t, err)
	require.Equal(t, "", out.TerminatedBy)
	require.Equal(t, "done", out.Output)
	require.Len(t, stub.llmReqs, 6, "forced 收尾步照常分发，不被振荡终止短路")
	last := lastLLMRequest(stub)
	for _, m := range last.Messages {
		require.NotContains(t, m.Content, "反复切换", "强制收尾步不得注入振荡换路提示")
	}
}

func TestBuildReActGraph_CostBudgetWinsOverNoProgress(t *testing.T) {
	// 预算在第 3 轮 LLM 后超限 → TerminatedBy=cost_budget 置位；第 4 入口短路先于
	// no-progress（runLen3 本会 nudge）→ 不注入提示、不发起第 4 次 LLM、reason 保持 cost_budget。
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "c1", Name: "noop", Arguments: map[string]any{}}}, Usage: port.TokenUsage{Total: 10}},
		{ToolCalls: []port.ToolCall{{ID: "c2", Name: "noop", Arguments: map[string]any{}}}, Usage: port.TokenUsage{Total: 10}},
		{ToolCalls: []port.ToolCall{{ID: "c3", Name: "noop", Arguments: map[string]any{}}}, Usage: port.TokenUsage{Total: 10}},
	}}
	cg := buildNoopGraph(t, stub)
	out, err := cg.Invoke(context.Background(), noopState(25, 0),
		graph.RunConfig[graph.ReActState]{MaxSteps: 20})
	require.NoError(t, err)
	require.Equal(t, "cost_budget", out.TerminatedBy)
	require.NotEqual(t, graph.NoProgressTerminated, out.TerminatedBy)
	require.Len(t, stub.llmReqs, 3, "预算终止后短路，第 4 入口不得因 nudge 再发起 LLM")
}
