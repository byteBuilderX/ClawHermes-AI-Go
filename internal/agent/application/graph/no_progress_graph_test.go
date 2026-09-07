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
