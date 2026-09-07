package application_test

import (
	"context"
	"testing"

	agent "github.com/byteBuilderX/stratum/internal/agent/application"
	agentgraph "github.com/byteBuilderX/stratum/internal/agent/application/graph"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/stretchr/testify/require"
)

// npCalcTool 是折叠测试复用的确定性工具：恒返回同一条结果 → 连续同指纹回合可
// 稳定复现（nudge 在第 3 轮、terminate 在第 4 轮）。
var npCalcTool = port.ToolDefinition{Name: "calc", ProviderType: "mcp", ServerID: "math",
	Metadata: map[string]any{"risk_level": "read"}}

// npRepeatedCalcGW 构造连续同指纹的 LLM 响应序列（n 轮都调 calc、参数固定）。
func npRepeatedCalcGW(n int) *mockCapGW {
	resp := make([]port.CapabilityResponse, 0, n)
	for i := 0; i < n; i++ {
		resp = append(resp, port.CapabilityResponse{
			ToolCalls: []port.ToolCall{{ID: "c", Name: "calc", Arguments: map[string]any{"expr": "1+1"}}},
		})
	}
	return &mockCapGW{responses: resp}
}

// npSameResultFn 恒返回相同结果的工具执行函数，保证结果摘要归一化后一致。
func npSameResultFn(context.Context, port.ToolExecutionRequest) (any, error) {
	return port.GuardedToolResult{ModelContent: "2"}, nil
}

// npNoProgressCompliance 是应用层无进展用例的公共入口：跑完整 Execute、返回
// result/gw，调用方做具体断言。
func npNoProgressCompliance(t *testing.T, gw *mockCapGW) (*agent.AgentResult, *mockCapGW) {
	t.Helper()
	a := newReActAgent()
	a.SetCapGateway(gw)
	result, err := a.Execute(context.Background(), "calc 1+1",
		agent.WithTenantID("t1"),
		agent.WithMaxSteps(10),
		agent.WithExtraTools([]port.ToolDefinition{npCalcTool}),
		agent.WithToolExecutionFn(npSameResultFn),
	)
	require.NoError(t, err)
	return result, gw
}

func TestExecute_NoProgressTerminationSurfacesReason(t *testing.T) {
	// 4 轮同指纹后业务终止（非错误）：TerminatedBy 上浮 reason=no_progress，
	// Output 是确定性中文说明，请求数远小于 MaxSteps（提前停而非等撞顶）。
	gw := npRepeatedCalcGW(4)
	result, gw := npNoProgressCompliance(t, gw)

	require.Equal(t, agentgraph.NoProgressTerminated, result.TerminatedBy,
		"应用层须透出 no_progress 业务终止 reason")
	require.Contains(t, result.Output, "提前结束")
	require.Contains(t, result.Output, "连续 4 轮相同操作")
	require.Len(t, gw.requests, 4, "第 4 轮完成即终止，不发第 5 次 LLM")
	require.Equal(t, 4, result.Steps)
	require.Less(t, len(gw.requests), 10, "远小于 MaxSteps：无进展提前停")
}

func TestExecute_NudgeRoundShownThenEarlyStop(t *testing.T) {
	// nudge 提示只注入第 4 次请求（runLen3 的换路回合）且仅本轮；模型仍重复 →
	// 下一入口 runLen4 终止。验证提示确实到达网关请求且不落入其它轮次。
	gw := npRepeatedCalcGW(4)
	result, gw := npNoProgressCompliance(t, gw)

	require.Equal(t, agentgraph.NoProgressTerminated, result.TerminatedBy)
	require.Len(t, gw.requests, 4)

	nudgeReq := gw.requests[3].LLM
	require.NotNil(t, nudgeReq)
	tail := nudgeReq.Messages[len(nudgeReq.Messages)-1]
	require.Equal(t, "user", tail.Role, "换路提示以尾部 user 指令进入本轮请求")
	require.Contains(t, tail.Content, "不要重复上次的操作")
	require.Contains(t, tail.Content, "3 轮工具调用", "文案轮数须与 nudge 阈值(3)同步")

	for i := 0; i < 3; i++ {
		req := gw.requests[i].LLM
		require.NotNil(t, req)
		for _, m := range req.Messages {
			require.NotContains(t, m.Content, "不要重复上次的操作",
				"nudge 提示只进换路回合，不得污染前 3 轮请求")
		}
	}
}

func TestExecute_NormalRunNoTerminationReason(t *testing.T) {
	// 正常完成（1 轮工具后模型直接作答）不得带 no_progress reason。
	gw := &mockCapGW{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{"expr": "1+1"}}}},
		{Content: "1+1=2"},
	}}
	result, _ := npNoProgressCompliance(t, gw)

	require.Equal(t, "1+1=2", result.Output)
	require.Equal(t, "", result.TerminatedBy, "正常完成不带业务终止 reason")
}
