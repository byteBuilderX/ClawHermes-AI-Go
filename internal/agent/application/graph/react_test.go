package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/agent/application/graph"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestBuildReActGraph_DestructiveToolPausesBeforeExecution(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{ToolCalls: []port.ToolCall{{
		ID: "delete-1", Name: "delete_order", Arguments: map[string]any{"id": "o1"},
	}}}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)
	guardCalled := false
	state := graph.ReActState{
		TenantID: "tenant-1", TraceID: "trace-1", Model: "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "delete order"}},
		AvailableTools: []port.ToolDefinition{{
			Name: "delete_order", ProviderType: "mcp", ServerID: "orders", CapabilityID: "delete_order",
			Metadata: map[string]any{"risk_level": "destructive"},
		}},
		ToolExecutionFn: func(context.Context, port.ToolExecutionRequest) (any, error) {
			guardCalled = true
			return nil, &port.ToolApprovalRequiredError{ToolName: "delete_order"}
		},
	}
	_, err = cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 5})
	var approvalErr *port.ToolApprovalRequiredError
	require.True(t, errors.As(err, &approvalErr))
	require.Equal(t, "delete_order", approvalErr.ToolName)
	require.True(t, guardCalled)
}

func TestBuildReActGraph_ForgedToolCallUsesExecutionGuard(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{ToolCalls: []port.ToolCall{{
		ID: "forged-1", Name: "mcp:orders:delete", Arguments: map[string]any{"id": "order-1"},
	}}}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)
	guardCalls := 0

	_, err = cg.Invoke(context.Background(), graph.ReActState{
		TenantID: "tenant-1", Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "run"}},
		AvailableTools: []port.ToolDefinition{{
			Name: "mcp:orders:delete", ProviderType: "mcp", ServerID: "orders", CapabilityID: "delete",
		}},
		ToolExecutionFn: func(_ context.Context, request port.ToolExecutionRequest) (any, error) {
			guardCalls++
			require.Equal(t, "forged-1", request.ToolCallID)
			return nil, &port.ToolApprovalRequiredError{ApprovalID: "approval-1"}
		},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 5})

	var approvalErr *port.ToolApprovalRequiredError
	require.ErrorAs(t, err, &approvalErr)
	require.Equal(t, 1, guardCalls)
}

func TestBuildReActGraph_FinalInstructionFitsContextBudget(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "done"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	messages := []port.LLMMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "task"},
	}
	for i := 0; i < 8; i++ {
		messages = append(messages, port.LLMMessage{Role: "assistant", Content: strings.Repeat("x", 700)})
	}

	const budget = 800
	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model:            "qwen",
		Messages:         messages,
		MaxLLMSteps:      1,
		MaxContextTokens: budget,
		// 账本快照：压缩阈值基于 HistoryCap（= usable − fixedHead − tools）。
		Budget: graph.ComputeBudget(budget, 0, 0),
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)
	require.Len(t, stub.llmReqs, 1)

	reqMessages := stub.llmReqs[0].Messages
	joined, err := json.Marshal(reqMessages)
	require.NoError(t, err)
	require.Contains(t, string(joined), "maximum reasoning steps")

	estimate := make([]tokenutil.Message, len(reqMessages))
	for i, message := range reqMessages {
		estimate[i] = tokenutil.Message{Role: message.Role, Content: message.Content}
	}
	wantMax := int(float64(graph.ComputeBudget(budget, 0, 0).HistoryCap) * constants.LoopCompactionSafetyRatio)
	require.LessOrEqual(t, tokenutil.EstimateMessages(estimate), wantMax)
}

func TestBuildReActGraph_FinalInstructionDoesNotReplaceCurrentTask(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "done"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	messages := []port.LLMMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "old request"}}
	for i := 0; i < 8; i++ {
		messages = append(messages, port.LLMMessage{Role: "assistant", Content: strings.Repeat("history", 120)})
	}
	messages = append(messages, port.LLMMessage{Role: "user", Content: "CURRENT TASK"})

	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: messages, MaxLLMSteps: 1, MaxContextTokens: 800,
		Budget: graph.ComputeBudget(800, 0, 0),
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)
	reqMessages := stub.llmReqs[0].Messages
	encoded, err := json.Marshal(reqMessages)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "CURRENT TASK")
	require.Contains(t, string(encoded), "maximum reasoning steps")
	// 收尾指令以 system role 注入首个 system 消息之后（头部 anchor 区），
	// 末尾仍是最新任务，指令不与用户任务混淆。
	require.Equal(t, "CURRENT TASK", reqMessages[len(reqMessages)-1].Content)
	require.Equal(t, "system", reqMessages[1].Role)
	require.Contains(t, reqMessages[1].Content, "maximum reasoning steps")
}

func TestBuildReActGraph_ReservesContextBudgetForToolSchemas(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "done"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	messages := []port.LLMMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "task"}}
	for i := 0; i < 8; i++ {
		messages = append(messages, port.LLMMessage{Role: "assistant", Content: strings.Repeat("x", 700)})
	}
	tools := []port.ToolDefinition{{
		Name: "search", Description: strings.Repeat("tool description ", 30),
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string", "description": strings.Repeat("query details ", 30)},
		}},
	}}

	const budget = 10000
	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: messages, AvailableTools: tools, MaxContextTokens: budget,
		// 账本快照：ToolsCap 足够容纳 ~300 token 工具定义，history 压缩走 HistoryCap。
		Budget: graph.ComputeBudget(budget, 0, 0),
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)
	require.Len(t, stub.llmReqs, 1)

	req := stub.llmReqs[0]
	estimate := make([]tokenutil.Message, len(req.Messages))
	for i, message := range req.Messages {
		estimate[i] = tokenutil.Message{Role: message.Role, Content: message.Content}
	}
	toolJSON, err := json.Marshal(req.Tools)
	require.NoError(t, err)
	total := tokenutil.EstimateMessages(estimate) + tokenutil.EstimateText(string(toolJSON))
	// 派发总量 = 压缩后的 history（≤ HistoryCap·safety）+ 工具（≤ ToolsCap 份额）。
	ledger := graph.ComputeBudget(budget, 0, 0)
	require.LessOrEqual(t, total, int(float64(ledger.HistoryCap)*constants.LoopCompactionSafetyRatio)+ledger.ToolsCap)
	// 工具定义必须保留（ToolsCap 独立配额，不挤占 history）。
	require.NotEmpty(t, req.Tools)
}

func TestBuildReActGraph_DropsToolsThatConsumeMessageAllowance(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "done"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	const budget = 800
	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", MaxContextTokens: budget,
		Budget:   graph.ComputeBudget(budget, 0, 0),
		Messages: []port.LLMMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "CURRENT TASK"}},
		AvailableTools: []port.ToolDefinition{{
			Name: "oversized", Description: strings.Repeat("schema", 1000),
			InputSchema: map[string]any{"type": "object"},
		}},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)
	require.NotContains(t, toolNames(stub.llmReqs[0].Tools), "oversized")
	require.Equal(t, "CURRENT TASK", stub.llmReqs[0].Messages[len(stub.llmReqs[0].Messages)-1].Content)
}

func TestBuildReActGraph_ReservedPlanToolsConsumeContextBeforeOptionalTools(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "done"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", MaxContextTokens: 1000,
		Budget:   graph.ComputeBudget(1000, 0, 0),
		Messages: []port.LLMMessage{{Role: "user", Content: "short task"}},
		AvailableTools: []port.ToolDefinition{{
			Name: "large_but_usable", Description: strings.Repeat("schema", 310),
			InputSchema: map[string]any{"type": "object"},
		}},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)
	require.NotContains(t, toolNames(stub.llmReqs[0].Tools), "large_but_usable")
}

func TestBuildReActGraph_UnclassifiedToolAlsoRequiresApproval(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{ToolCalls: []port.ToolCall{{ID: "call-1", Name: "unknown_risk"}}}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)
	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen-turbo", Messages: []port.LLMMessage{{Role: "user", Content: "run"}},
		AvailableTools: []port.ToolDefinition{{Name: "unknown_risk", ProviderType: "mcp", ServerID: "server"}},
		ToolExecutionFn: func(context.Context, port.ToolExecutionRequest) (any, error) {
			return nil, &port.ToolApprovalRequiredError{}
		},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 5})
	var approvalErr *port.ToolApprovalRequiredError
	require.True(t, errors.As(err, &approvalErr))
}

func TestBuildReActGraph_ApprovedDestructiveToolUsesExecutionGuardOnce(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "new-call", Name: "delete_order", Arguments: map[string]any{"id": "o1"}}}},
		{Content: "deleted"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)
	guardCalls := 0
	out, err := cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "delete"}},
		AvailableTools: []port.ToolDefinition{{Name: "delete_order", ProviderType: "mcp", ServerID: "orders", CapabilityID: "delete_order", Metadata: map[string]any{"risk_level": "destructive"}}},
		ToolExecutionFn: func(context.Context, port.ToolExecutionRequest) (any, error) {
			guardCalls++
			return guardedToolOutput("ok"), nil
		},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 5})
	require.NoError(t, err)
	require.Equal(t, "deleted", out.Output)
	require.Equal(t, 1, guardCalls)
}

func TestBuildReActGraph_PropagatesTemperatureAndMaxTokensToLLMRequest(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "done"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Temperature: 0.9, MaxTokens: 2048,
		Messages: []port.LLMMessage{{Role: "user", Content: "hi"}},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)
	require.Len(t, stub.llmReqs, 1)
	require.Equal(t, float32(0.9), stub.llmReqs[0].Temperature)
	require.Equal(t, 2048, stub.llmReqs[0].MaxTokens)
}

func TestBuildReActGraph_ZeroTemperatureAndMaxTokensStayUnset(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "done"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "hi"}},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)
	require.Len(t, stub.llmReqs, 1)
	require.Equal(t, float32(0), stub.llmReqs[0].Temperature)
	require.Equal(t, 0, stub.llmReqs[0].MaxTokens)
}

// capGWSequence drives LLM responses in sequence; tool always returns fixed resp.
type capGWSequence struct {
	responses []port.CapabilityResponse
	idx       int
	// non-zero infinite means return this after the sequence is exhausted
	infinite port.CapabilityResponse
	llmReqs  []port.LLMCapRequest
}

func (s *capGWSequence) Route(_ context.Context, req port.CapabilityRequest) (port.CapabilityResponse, error) {
	if req.LLM != nil {
		// 记录 dispatch 时刻的快照：克隆外层 Messages，避免与调用方 s.Messages 共享
		// 底层数组——后续 appendLLMResponse 会把新消息写进同一索引，覆盖已记录的尾件
		// （stub 浅拷贝会导致 llmReqs 与真实网关看到的内容不一致）。
		snap := *req.LLM
		snap.Messages = append([]port.LLMMessage{}, req.LLM.Messages...)
		s.llmReqs = append(s.llmReqs, snap)
	}
	if s.idx < len(s.responses) {
		r := s.responses[s.idx]
		s.idx++
		return r, nil
	}
	return s.infinite, nil
}

func TestBuildReActGraph_StacksInstructionsAndKeepsAgentToolSurface(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "activate-1", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-a"}}}},
		{ToolCalls: []port.ToolCall{{ID: "activate-2", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-b"}}}},
		{Content: "done"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "complete task"}},
		AvailableTools: []port.ToolDefinition{
			{Name: "mcp:orders:get", ProviderType: "mcp"},
			{Name: "mcp:orders:delete", ProviderType: "mcp"},
			{Name: "stratum_recall_memory", ProviderType: "builtin"},
		},
		AgentMemoryScope: "user",
		SkillCatalog: map[string]port.SkillActivation{
			"skill-a": {SkillID: "skill-a", Name: "skill-a", RevisionID: "revision-a", Instructions: "USE INSTRUCTION A"},
			"skill-b": {SkillID: "skill-b", Name: "skill-b", RevisionID: "revision-b", Instructions: "USE INSTRUCTION B"},
		},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 10})
	require.NoError(t, err)
	require.Len(t, out.Actives, 2)
	require.Equal(t, []string{"skill-a", "skill-b"}, []string{out.Actives[0].SkillID, out.Actives[1].SkillID})
	require.Len(t, stub.llmReqs, 3)

	secondMessages, _ := json.Marshal(stub.llmReqs[1].Messages)
	require.Contains(t, string(secondMessages), "USE INSTRUCTION A")
	require.NotContains(t, string(secondMessages), "USE INSTRUCTION B")
	// 工具面 = stratum_skill 统一工具 + plan 工具 + agent 绑定全集（Spec D5），
	// 激活 skill 不再隐藏/叠加 MCP 或 memory 工具，两轮工具面恒定。
	agentSurface := []string{"stratum_skill", "stratum_create_plan", "stratum_revise_plan", "stratum_continue_plan", "stratum_cancel_plan", "stratum_complete_task", "mcp:orders:get", "mcp:orders:delete", "stratum_recall_memory"}
	require.Equal(t, agentSurface, toolNames(stub.llmReqs[1].Tools))

	thirdMessages, _ := json.Marshal(stub.llmReqs[2].Messages)
	require.Contains(t, string(thirdMessages), "USE INSTRUCTION A")
	require.Contains(t, string(thirdMessages), "USE INSTRUCTION B")
	require.Equal(t, agentSurface, toolNames(stub.llmReqs[2].Tools))
}

func TestBuildReActGraph_ActiveSkillInheritsAgentKnowledgeWorkspaces(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "a1", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-a"}}}},
		{ToolCalls: []port.ToolCall{{ID: "k1", Name: "stratum_search_knowledge", Arguments: map[string]any{"workspaces": []any{"kb-allowed", "kb-agent-only", "kb-skill-only"}, "query": "q"}}}},
		{Content: "done"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)
	var searched []string
	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "search"}},
		AvailableTools:             []port.ToolDefinition{{Name: "stratum_search_knowledge", ProviderType: "builtin"}},
		AgentKnowledgeWorkspaceIDs: []string{"kb-allowed", "kb-agent-only"},
		SkillCatalog:               map[string]port.SkillActivation{"skill-a": {SkillID: "skill-a", Name: "skill-a"}},
		RAGSearchFn: func(_ context.Context, workspaces []string, _ string, _ int, _ string) (string, error) {
			searched = workspaces
			return "result", nil
		},
		InternalToolResultGuardFn: untrustedTestGuard,
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 8})
	require.NoError(t, err)
	// 知识边界恒为 agent 绑定（Spec D5）：skill 激活不携带 knowledge 声明，不叠加/收窄。
	require.Equal(t, []string{"kb-allowed", "kb-agent-only"}, searched)
}

func TestBuildReActGraph_KnowledgeRevisionFailureStopsBeforeSecondLLMCall(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{
			ID: "knowledge-1", Name: "stratum_search_knowledge",
			Arguments: map[string]any{"workspaces": []any{"Knowledge One"}, "query": "q"},
		}}},
		{Content: "must not turn a failed revision search into success"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state, err := cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "search"}},
		AvailableTools:             []port.ToolDefinition{{Name: "stratum_search_knowledge", ProviderType: "builtin"}},
		AgentKnowledgeWorkspaceIDs: []string{"Knowledge One"},
		RAGSearchFn: func(context.Context, []string, string, int, string) (string, error) {
			return "", fmt.Errorf("%w: vector backend unavailable", domain.ErrKnowledgeRevisionUnavailable)
		},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 5})

	require.ErrorIs(t, err, domain.ErrKnowledgeRevisionUnavailable)
	require.Len(t, stub.llmReqs, 1)
	require.Len(t, state.ToolObservations, 1)
	require.Equal(t, domain.ToolTraceStatusError, state.ToolObservations[0].Status)
	require.Len(t, state.TraceEvents, 4)
	require.Equal(t, domain.TraceEventToolFailed, state.TraceEvents[3].EventType)
}

// contextLengthMarkerErr 是 llmgateway ErrContextLengthExceeded 的测试副本：
// Permanent + ContextLengthExceeded 双标记与真实错误一致，供 graph 包
// duck-typing 探测验证。
type contextLengthMarkerErr struct{ msg string }

func (e *contextLengthMarkerErr) Error() string               { return e.msg }
func (e *contextLengthMarkerErr) Permanent() bool             { return true }
func (e *contextLengthMarkerErr) ContextLengthExceeded() bool { return true }

// permanentOnlyErr 是参数校验类 400 的测试副本：permanent 但不带
// context_length 标记——重试无意义，也不触发降级。
type permanentOnlyErr struct{ msg string }

func (e *permanentOnlyErr) Error() string   { return e.msg }
func (e *permanentOnlyErr) Permanent() bool { return true }

func TestIsContextLengthExceeded(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare marker", err: &contextLengthMarkerErr{msg: "context length exceeded"}, want: true},
		{name: "wrapped once", err: fmt.Errorf("react llm node: %w", &contextLengthMarkerErr{msg: "context length exceeded"}), want: true},
		{name: "wrapped twice", err: fmt.Errorf("react: %w", fmt.Errorf("react llm node: %w", &contextLengthMarkerErr{})), want: true},
		{name: "plain error", err: errors.New("boom"), want: false},
		{name: "permanent but not context length", err: &permanentOnlyErr{msg: "invalid parameter schema"}, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, graph.IsContextLengthExceeded(tc.err))
		})
	}
}

// TestBuildMinimalRetryMessages 覆盖 Spec D4 降级最小请求构造：
// 成对剔除工具交换（assistant tool_calls 与其 tool 结果）、预算上限、
// 最近优先保留、首条 system / 末条当前任务。
func TestBuildMinimalRetryMessages(t *testing.T) {
	task := "CURRENT TASK"
	cases := []struct {
		name     string
		system   string
		task     string
		messages []port.LLMMessage
		window   int
		want     []port.LLMMessage
	}{
		{
			name:   "keeps plain history and appends task",
			system: "sys",
			task:   task,
			messages: []port.LLMMessage{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: "prior task"},
				{Role: "assistant", Content: "prior answer"},
			},
			window: 1000,
			want: []port.LLMMessage{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: "prior task"},
				{Role: "assistant", Content: "prior answer"},
				{Role: "user", Content: task},
			},
		},
		{
			name:   "strips tool result and its assistant tool_calls pair",
			system: "sys",
			task:   task,
			messages: []port.LLMMessage{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: "prior task"},
				{Role: "assistant", ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc"}}},
				{Role: "tool", ToolCallID: "c1", Content: "42"},
				{Role: "assistant", Content: "interim"},
			},
			window: 1000,
			want: []port.LLMMessage{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: "prior task"},
				{Role: "assistant", Content: "interim"},
				{Role: "user", Content: task},
			},
		},
		{
			name:   "budget exhaustion keeps most recent messages and restores system",
			system: "sys",
			task:   task,
			messages: []port.LLMMessage{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: strings.Repeat("x", 50)},
				{Role: "assistant", Content: strings.Repeat("y", 50)},
				{Role: "user", Content: "recent"},
			},
			// 预算只容得下最近一条 user 消息：历史 system 被挤出，但占位
			// 必须补回——预算在扫描前已为 system 预留字节。
			window: len("sys") + len("recent") + len(task) + 64 + 6,
			want: []port.LLMMessage{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: "recent"},
				{Role: "user", Content: task},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := graph.BuildMinimalRetryMessages(tc.system, tc.task, tc.messages, tc.window)
			require.Equal(t, tc.want, got)
			// 不变式：剔除全部工具消息与 assistant tool_calls；末条为当前任务。
			contentSum := 0
			for _, m := range got {
				require.NotEqual(t, "tool", m.Role)
				require.Empty(t, m.ToolCalls)
				contentSum += len(m.Content)
			}
			require.Equal(t, "user", got[len(got)-1].Role)
			require.Equal(t, tc.task, got[len(got)-1].Content)
			// D4 语义不变式：最小请求首条恒为 system（预算已为 system
			// 预留字节，历史 system 被预算挤出时由占位补回）。
			require.Equal(t, "system", got[0].Role)
			require.Equal(t, tc.system, got[0].Content)
			// 预算充足时总量必须 ≤ window（最小请求必然小于原请求）。
			if tc.window > 100 {
				require.LessOrEqual(t, contentSum, tc.window)
			}
		})
	}
}

func toolNames(tools []port.ToolDefinition) []string {
	names := make([]string, len(tools))
	for i := range tools {
		names[i] = tools[i].Name
	}
	return names
}

type slowCapGW struct{ delay time.Duration }

func (s *slowCapGW) Route(ctx context.Context, _ port.CapabilityRequest) (port.CapabilityResponse, error) {
	select {
	case <-ctx.Done():
		return port.CapabilityResponse{}, ctx.Err()
	case <-time.After(s.delay):
		return port.CapabilityResponse{Content: "slow"}, nil
	}
}

type errCapGW struct{ err error }

func (e *errCapGW) Route(_ context.Context, _ port.CapabilityRequest) (port.CapabilityResponse, error) {
	return port.CapabilityResponse{}, e.err
}

func TestBuildReActGraph_DirectAnswer(t *testing.T) {
	stub := &capGWSequence{
		responses: []port.CapabilityResponse{{Content: "42"}},
	}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		TenantID: "t1",
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "what is 6x7?"}},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 5})
	require.NoError(t, err)
	require.Equal(t, "42", out.Output)
	require.Equal(t, 1, out.Steps)
}

func TestBuildReActGraph_DoesNotLogLLMResponseContent(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	stub := &capGWSequence{responses: []port.CapabilityResponse{{Content: "synthetic-private-answer"}}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.New(core))
	require.NoError(t, err)

	_, err = cg.Invoke(context.Background(), graph.ReActState{
		TraceID:  "trace-1",
		TenantID: "tenant-1",
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "private prompt"}},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 2})
	require.NoError(t, err)

	for _, entry := range observed.All() {
		logged := entry.Message + fmt.Sprint(entry.ContextMap())
		require.NotContains(t, logged, "synthetic-private-answer")
		require.NotContains(t, logged, "content_preview")
	}
}

func TestBuildReActGraph_DoesNotLogToolResponseContent(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "call-1", Name: "lookup", Arguments: map[string]any{}}}},
		{Content: "done"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.New(core))
	require.NoError(t, err)

	_, err = cg.Invoke(context.Background(), graph.ReActState{
		TraceID:  "trace-1",
		TenantID: "tenant-1",
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "private prompt"}},
		AvailableTools: []port.ToolDefinition{{
			Name: "lookup", ProviderType: "mcp", ServerID: "server-1", Metadata: map[string]any{"risk_level": "read"},
		}},
		ToolExecutionFn: func(context.Context, port.ToolExecutionRequest) (any, error) {
			return guardedToolOutput("synthetic-private-tool-result"), nil
		},
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 4})
	require.NoError(t, err)

	for _, entry := range observed.All() {
		logged := entry.Message + fmt.Sprint(entry.ContextMap())
		require.NotContains(t, logged, "synthetic-private-tool-result")
		require.NotContains(t, logged, "content_preview")
	}
}

func TestBuildReActGraph_ToolCall(t *testing.T) {
	stub := &capGWSequence{
		responses: []port.CapabilityResponse{
			{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{"expr": "6*7"}}}},
			{Content: "The answer is 42"},
		},
	}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:          "qwen-turbo",
		Messages:       []port.LLMMessage{{Role: "user", Content: "calc 6*7"}},
		AvailableTools: []port.ToolDefinition{{Name: "calc", ProviderType: "mcp", ProviderID: "math", ServerID: "math", Metadata: map[string]any{"risk_level": "read"}}},
		ToolExecutionFn: func(_ context.Context, request port.ToolExecutionRequest) (any, error) {
			require.Equal(t, "math", request.Tool.ServerID)
			require.Equal(t, "calc", request.Tool.CapabilityID)
			return guardedToolOutput("42"), nil
		},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 10})
	require.NoError(t, err)
	require.Equal(t, "The answer is 42", out.Output)
	require.Equal(t, 2, out.Steps)
	require.Len(t, out.AllToolCalls, 1)
	require.Equal(t, "calc", out.AllToolCalls[0].Name)
	require.Len(t, out.ToolObservations, 1)
	require.Equal(t, "c1", out.ToolObservations[0].ToolCallID)
	require.Equal(t, "calc", out.ToolObservations[0].ToolName)
	require.Equal(t, "success", out.ToolObservations[0].Status)
	require.Equal(t, "42", out.ToolObservations[0].RawText)
	require.Equal(t, "mcp", out.ToolObservations[0].ProviderType)
	require.Equal(t, "math", out.ToolObservations[0].ProviderID)
	require.Equal(t, "agent", out.TraceEvents[0].RunType)
	require.NotEmpty(t, out.ToolObservations[0].Summary)
	require.NotEmpty(t, out.TraceEvents)
}

func TestBuildReActGraph_MCPToolCallRecordsProviderMetadata(t *testing.T) {
	stub := &capGWSequence{
		responses: []port.CapabilityResponse{
			{ToolCalls: []port.ToolCall{{ID: "mcp-call-1", Name: "mcp_search", Arguments: map[string]any{"query": "status"}}}},
			{Content: "Done"},
		},
	}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "use mcp"}},
		AvailableTools: []port.ToolDefinition{{
			Name:         "mcp_search",
			Description:  "search through mcp",
			InputSchema:  map[string]any{"type": "object"},
			ProviderType: "mcp",
			ProviderID:   "server-1",
			ServerID:     "server-1",
			Metadata:     map[string]any{"risk_level": "read"},
		}},
		ToolExecutionFn: func(_ context.Context, request port.ToolExecutionRequest) (any, error) {
			require.Equal(t, "server-1", request.Tool.ServerID)
			require.Equal(t, "mcp_search", request.Tool.CapabilityID)
			require.Equal(t, "status", request.Arguments["query"])
			return guardedToolOutput("mcp result"), nil
		},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 10})
	require.NoError(t, err)
	require.Len(t, out.ToolObservations, 1)
	require.Equal(t, "mcp", out.ToolObservations[0].ProviderType)
	require.Equal(t, "server-1", out.ToolObservations[0].ProviderID)
	require.Equal(t, "server-1", out.ToolObservations[0].ServerID)
	require.Equal(t, "mcp", out.ToolObservations[0].ToolType)
	require.NotEmpty(t, out.TraceEvents)
	require.Equal(t, "mcp_search", out.TraceEvents[3].NodeID)
	require.Equal(t, "mcp", out.TraceEvents[3].NodeType)
}

func TestBuildReActGraph_NoProgressTerminatesBeforeMaxSteps(t *testing.T) {
	// 无限重复同 noop{}→"ok"：runLen 3 注入换路提示（模型仍重复）→ runLen 4 以业务
	// 终止 no_progress 收尾，提前于 MaxSteps 撞顶——不误报 error、不烧满步数预算。
	stub := &capGWSequence{
		infinite: port.CapabilityResponse{
			ToolCalls: []port.ToolCall{{ID: "c1", Name: "noop", Arguments: map[string]any{}}},
		},
	}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:          "qwen-turbo",
		Messages:       []port.LLMMessage{{Role: "user", Content: "loop"}},
		AvailableTools: []port.ToolDefinition{{Name: "noop", ProviderType: "mcp", ServerID: "test", Metadata: map[string]any{"risk_level": "read"}}},
		ToolExecutionFn: func(context.Context, port.ToolExecutionRequest) (any, error) {
			return guardedToolOutput("ok"), nil
		},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 10})
	require.NoError(t, err)
	require.Equal(t, graph.NoProgressTerminated, out.TerminatedBy)
	require.Contains(t, out.Output, "提前结束")
	require.Equal(t, 4, len(stub.llmReqs), "3 次同指纹 + 1 次带提示轮后即终止，不再烧预算")
	require.Len(t, out.AllToolCalls, 4)
}

func TestBuildReActGraph_LLMError(t *testing.T) {
	stub := &errCapGW{err: context.DeadlineExceeded}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "hi"}},
	}
	_, err = cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 5})
	require.Error(t, err)
}

func TestBuildReActGraph_TokensAccumulated(t *testing.T) {
	stub := &capGWSequence{
		responses: []port.CapabilityResponse{
			{Content: "result", Usage: port.TokenUsage{Prompt: 10, Completion: 5, Total: 15}},
		},
	}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "hi"}},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 5})
	require.NoError(t, err)
	require.Equal(t, 15, out.TotalTokens)
}

func TestBuildReActGraph_TokensAccumulatedOverMultipleSteps(t *testing.T) {
	stub := &capGWSequence{
		responses: []port.CapabilityResponse{
			{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{}}}, Usage: port.TokenUsage{Total: 20}},
			{Content: "done", Usage: port.TokenUsage{Total: 10}},
		},
	}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:          "qwen-turbo",
		Messages:       []port.LLMMessage{{Role: "user", Content: "go"}},
		AvailableTools: []port.ToolDefinition{{Name: "calc", ProviderType: "mcp", ServerID: "test", Metadata: map[string]any{"risk_level": "read"}}},
		ToolExecutionFn: func(context.Context, port.ToolExecutionRequest) (any, error) {
			return guardedToolOutput("ok"), nil
		},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 10})
	require.NoError(t, err)
	require.Equal(t, 30, out.TotalTokens)
}

func TestBuildReActGraph_ContextTimeout(t *testing.T) {
	stub := &slowCapGW{delay: 200 * time.Millisecond}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	state := graph.ReActState{
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "hi"}},
	}
	_, err = cg.Invoke(ctx, state, graph.RunConfig[graph.ReActState]{MaxSteps: 5})
	require.Error(t, err)
}

// streamFailGateway fails the first failCount calls; emitBeforeFail streams one
// token through the request's TokenStream before returning the error, so the
// graph can distinguish「已输出 token 后失败」与「首 token 前失败」。
type streamFailGateway struct {
	failCount      int
	emitBeforeFail bool
	failErr        error
	calls          int
}

func (s *streamFailGateway) Route(_ context.Context, req port.CapabilityRequest) (port.CapabilityResponse, error) {
	s.calls++
	if s.calls <= s.failCount {
		if s.emitBeforeFail && req.TokenStream != nil {
			req.TokenStream("partial")
		}
		return port.CapabilityResponse{}, s.failErr
	}
	return port.CapabilityResponse{Content: "ok"}, nil
}

// TestRouteLLM_ErrorAfterTokenEmit_DoesNotRetry pins that a failure after the
// first token reached the client is treated as permanent: retrying would
// replay the whole stream and corrupt the frontend content.
func TestRouteLLM_ErrorAfterTokenEmit_DoesNotRetry(t *testing.T) {
	origErr := errors.New("stream truncated before completion marker")
	gw := &streamFailGateway{failCount: 1, emitBeforeFail: true, failErr: origErr}
	var streamed []string
	state := graph.ReActState{
		TenantID: "t1", TraceID: "trace-1", Model: "qwen",
		Messages: []port.LLMMessage{{Role: "user", Content: "hi"}},
		OnToken:  func(tok string) { streamed = append(streamed, tok) },
	}

	_, err := graph.RouteLLMForTest(context.Background(), state, state.Messages, nil, gw)
	require.Error(t, err)
	// 原错误透传（errors.Is 可见），不吞错、不换包装语义。
	require.ErrorIs(t, err, origErr)
	require.Equal(t, 1, gw.calls, "已输出 token 的失败必须跳过图级重试")
	require.Equal(t, []string{"partial"}, streamed, "token 只推流一次，不得重放")
}

// TestRouteLLM_FailureBeforeTokenEmit_Retries pins that a failure before any
// token was emitted keeps the retry path: nothing reached the client, so a
// retry cannot corrupt the stream.
func TestRouteLLM_FailureBeforeTokenEmit_Retries(t *testing.T) {
	gw := &streamFailGateway{failCount: 2, failErr: errors.New("transient upstream failure")}
	state := graph.ReActState{
		TenantID: "t1", TraceID: "trace-1", Model: "qwen",
		Messages: []port.LLMMessage{{Role: "user", Content: "hi"}},
		OnToken:  func(string) {},
	}

	resp, err := graph.RouteLLMForTest(context.Background(), state, state.Messages, nil, gw)
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Content)
	require.Equal(t, 3, gw.calls, "首 token 前失败应走完 DefaultRetry 的三次尝试")
}

func guardedToolOutput(content string) port.GuardedToolResult {
	return port.GuardedToolResult{ModelContent: content, Summary: content, Untrusted: true}
}

func TestBuildReActGraph_ReactivatingActiveSkillIsIdempotent(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "activate-1", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-a"}}}},
		{ToolCalls: []port.ToolCall{{ID: "activate-2", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-a"}}}},
		{ToolCalls: []port.ToolCall{{ID: "activate-3", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-b"}}}},
		{Content: "done"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	state := graph.ReActState{
		Model:    "qwen-turbo",
		Messages: []port.LLMMessage{{Role: "user", Content: "complete task"}},
		AvailableTools: []port.ToolDefinition{
			{Name: "mcp:orders:get", ProviderType: "mcp"},
			{Name: "mcp:orders:delete", ProviderType: "mcp"},
			{Name: "stratum_recall_memory", ProviderType: "builtin"},
		},
		AgentMemoryScope: "user",
		SkillCatalog: map[string]port.SkillActivation{
			"skill-a": {SkillID: "skill-a", Name: "skill-a", RevisionID: "revision-a", Instructions: "USE INSTRUCTION A"},
			"skill-b": {SkillID: "skill-b", Name: "skill-b", RevisionID: "revision-b", Instructions: "USE INSTRUCTION B"},
		},
	}
	out, err := cg.Invoke(context.Background(), state, graph.RunConfig[graph.ReActState]{MaxSteps: 10})
	require.NoError(t, err)
	// D6 幂等拦截：第二次激活 skill-a 被拦，Actives 仍按首次激活顺序累积为两个。
	require.Len(t, out.Actives, 2)
	require.Equal(t, []string{"skill-a", "skill-b"}, []string{out.Actives[0].SkillID, out.Actives[1].SkillID})

	require.Len(t, stub.llmReqs, 4)
	thirdMessages, _ := json.Marshal(stub.llmReqs[2].Messages)
	// 重复激活不重复注入指令：A 只出现一次。
	require.Equal(t, 1, strings.Count(string(thirdMessages), "USE INSTRUCTION A"))
	require.NotContains(t, string(thirdMessages), "USE INSTRUCTION B")
	fourthMessages, _ := json.Marshal(stub.llmReqs[3].Messages)
	require.Equal(t, 1, strings.Count(string(fourthMessages), "USE INSTRUCTION A"))
	require.Contains(t, string(fourthMessages), "USE INSTRUCTION B")
	agentSurface := []string{"stratum_skill", "stratum_create_plan", "stratum_revise_plan", "stratum_continue_plan", "stratum_cancel_plan", "stratum_complete_task", "mcp:orders:get", "mcp:orders:delete", "stratum_recall_memory"}
	require.Equal(t, agentSurface, toolNames(stub.llmReqs[3].Tools))
}

func TestBuildReActGraph_ActivesInheritAgentKnowledgeWorkspaces(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "a1", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-a"}}}},
		{ToolCalls: []port.ToolCall{{ID: "b1", Name: "stratum_skill", Arguments: map[string]any{"skill": "skill-b"}}}},
		{ToolCalls: []port.ToolCall{{ID: "k1", Name: "stratum_search_knowledge", Arguments: map[string]any{"workspaces": []any{"kb-allowed", "kb-agent-only", "kb-skill-only"}, "query": "q"}}}},
		{Content: "done"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)
	var searched []string
	_, err = cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "search"}},
		AvailableTools:             []port.ToolDefinition{{Name: "stratum_search_knowledge", ProviderType: "builtin"}},
		AgentKnowledgeWorkspaceIDs: []string{"kb-allowed", "kb-agent-only"},
		SkillCatalog: map[string]port.SkillActivation{
			"skill-a": {SkillID: "skill-a", Name: "skill-a"},
			"skill-b": {SkillID: "skill-b", Name: "skill-b"},
		},
		RAGSearchFn: func(_ context.Context, workspaces []string, _ string, _ int, _ string) (string, error) {
			searched = workspaces
			return "result", nil
		},
		InternalToolResultGuardFn: untrustedTestGuard,
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 8})
	require.NoError(t, err)
	// 多 skill 并列激活后知识边界仍恒为 agent 绑定（Spec D5），skill 不携带 knowledge 声明。
	require.Equal(t, []string{"kb-allowed", "kb-agent-only"}, searched)
}

func TestMessagesWithActiveSkills(t *testing.T) {
	cases := []struct {
		name     string
		messages []port.LLMMessage
		actives  []port.SkillActivation
		want     []port.LLMMessage
	}{
		{
			name:     "no actives returns messages unchanged",
			messages: []port.LLMMessage{{Role: "user", Content: "task"}},
			actives:  nil,
			want:     []port.LLMMessage{{Role: "user", Content: "task"}},
		},
		{
			name:     "all empty instructions returns messages unchanged",
			messages: []port.LLMMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "task"}},
			actives:  []port.SkillActivation{{Name: "skill-a", Instructions: ""}},
			want:     []port.LLMMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "task"}},
		},
		{
			name:     "multiple actives inserted as contiguous block after first system message",
			messages: []port.LLMMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "task"}},
			actives: []port.SkillActivation{
				{Name: "skill-a", RevisionID: "rev-a", Instructions: "INST A"},
				{Name: "skill-b", RevisionID: "rev-b", Instructions: "INST B"},
			},
			want: []port.LLMMessage{
				{Role: "system", Content: "system"},
				{Role: "system", Content: "多个 skill 并列生效，指令冲突时由模型按任务意图自行取舍。"},
				{Role: "system", Content: "Active Skill skill-a (revision rev-a):\nINST A"},
				{Role: "system", Content: "Active Skill skill-b (revision rev-b):\nINST B"},
				{Role: "user", Content: "task"},
			},
		},
		{
			name:     "without leading system message instructions precede all messages",
			messages: []port.LLMMessage{{Role: "user", Content: "task"}},
			actives: []port.SkillActivation{
				{Name: "skill-b", RevisionID: "rev-b", Instructions: "INST B"},
				{Name: "skill-a", RevisionID: "rev-a", Instructions: "INST A"},
			},
			want: []port.LLMMessage{
				{Role: "system", Content: "多个 skill 并列生效，指令冲突时由模型按任务意图自行取舍。"},
				{Role: "system", Content: "Active Skill skill-b (revision rev-b):\nINST B"},
				{Role: "system", Content: "Active Skill skill-a (revision rev-a):\nINST A"},
				{Role: "user", Content: "task"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := graph.MessagesWithActiveSkillsForTest(tc.messages, tc.actives)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAllowedKnowledgeWorkspacesInheritsAgentBinding(t *testing.T) {
	cases := []struct {
		name         string
		requested    []string
		agentAllowed []string
		want         []string
	}{
		{
			name:         "requested within agent binding kept",
			requested:    []string{"kb-allowed"},
			agentAllowed: []string{"kb-allowed"},
			want:         []string{"kb-allowed"},
		},
		{
			name:         "skill-declared-only workspace dropped",
			requested:    []string{"kb-allowed", "kb-skill-only"},
			agentAllowed: []string{"kb-allowed", "kb-agent-only"},
			want:         []string{"kb-allowed"},
		},
		{
			name:         "requested fully outside agent binding dropped",
			requested:    []string{"kb-one", "kb-two", "kb-other"},
			agentAllowed: []string{"kb-one", "kb-two", "kb-agent"},
			want:         []string{"kb-one", "kb-two"},
		},
		{
			name:         "empty requested falls back to agent allowed",
			requested:    nil,
			agentAllowed: []string{"kb-agent"},
			want:         []string{"kb-agent"},
		},
		{
			name:         "empty intersection yields nothing",
			requested:    []string{"kb-requested"},
			agentAllowed: []string{"kb-agent"},
			want:         []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := graph.AllowedKnowledgeWorkspacesForTest(tc.requested, tc.agentAllowed)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEffectiveTools_ToolSurfaceUnchangedByActives(t *testing.T) {
	baseAvailable := []port.ToolDefinition{
		{Name: "mcp:orders:get", ProviderType: "mcp"},
		{Name: "mcp:orders:delete", ProviderType: "mcp"},
		{Name: "stratum_recall_memory", ProviderType: "builtin"},
		{Name: "stratum_search_knowledge", ProviderType: "builtin"},
	}
	fullSurface := []string{"stratum_create_plan", "stratum_revise_plan", "stratum_continue_plan", "stratum_cancel_plan", "stratum_complete_task", "mcp:orders:get", "mcp:orders:delete", "stratum_recall_memory", "stratum_search_knowledge"}
	planTools := []string{"stratum_create_plan", "stratum_revise_plan", "stratum_continue_plan", "stratum_cancel_plan", "stratum_complete_task"}

	// 工具面 = plan 工具 + agent 绑定全集：激活与否不改变可见工具（Spec D5）。
	// stratum_skill 由 prepareLLMRequest 按预算动态前置，不在本函数静态生成。
	require.Equal(t, fullSurface, toolNames(graph.EffectiveToolsForTest(baseAvailable)))

	// plan 工具若混入 AvailableTools 会被防御性去重，不重复暴露。
	withPlanInAvailable := append([]port.ToolDefinition{
		{Name: "stratum_create_plan", ProviderType: "builtin"},
		{Name: "stratum_revise_plan", ProviderType: "builtin"},
	}, baseAvailable...)
	require.Equal(t, fullSurface, toolNames(graph.EffectiveToolsForTest(withPlanInAvailable)))

	// 空 available 时工具面 = plan 工具（agent_tasks 触发源，唯一真实用户路径）。
	require.Equal(t, planTools, toolNames(graph.EffectiveToolsForTest(nil)))
}

func TestUpsertActivationReplacesInPlaceOrAppends(t *testing.T) {
	a := port.SkillActivation{SkillID: "skill-a", RevisionID: "rev-1"}
	b := port.SkillActivation{SkillID: "skill-b", RevisionID: "rev-1"}
	a2 := port.SkillActivation{SkillID: "skill-a", RevisionID: "rev-2"}
	got := graph.UpsertActivationForTest(nil, a)
	require.Equal(t, []port.SkillActivation{a}, got)
	got = graph.UpsertActivationForTest(got, b)
	require.Equal(t, []port.SkillActivation{a, b}, got)
	got = graph.UpsertActivationForTest(got, a2)
	require.Equal(t, []port.SkillActivation{a2, b}, got)
}

func TestBuildReActGraph_SearchKnowledgePrefersEvidenceFn(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "k1", Name: "stratum_search_knowledge",
			Arguments: map[string]any{"workspaces": []any{"kb"}, "query": "q"}}}},
		{Content: "done"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	var plainCalled, evidenceCalled bool
	out, err := cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "search"}},
		AvailableTools:             []port.ToolDefinition{{Name: "stratum_search_knowledge", ProviderType: "builtin"}},
		AgentKnowledgeWorkspaceIDs: []string{"kb"},
		RAGSearchFn: func(context.Context, []string, string, int, string) (string, error) {
			plainCalled = true
			return "plain", nil
		},
		RAGSearchFnWithEvidence: func(context.Context, []string, string, int, string) (port.RAGSearchEvidence, error) {
			evidenceCalled = true
			return port.RAGSearchEvidence{Content: "ev", Sources: []port.RAGSearchSource{
				{WorkspaceID: "w1", WorkspaceName: "KB One", ChunkID: "c1", Score: 0.85, HasScore: true},
				{WorkspaceID: "w2", WorkspaceName: "KB Two", ChunkID: "c2"},
			}}, nil
		},
		InternalToolResultGuardFn: untrustedTestGuard,
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 8})

	require.NoError(t, err)
	require.True(t, evidenceCalled)
	require.False(t, plainCalled)
	require.Equal(t, "done", out.Output)

	require.Len(t, out.ToolObservations, 1)
	meta, ok := out.ToolObservations[0].Metadata["evidence"].(map[string]any)
	require.True(t, ok, "evidence metadata expected on tool observation")
	require.Equal(t, 2, meta["source_count"])
	sources, ok := meta["sources"].([]any)
	require.True(t, ok)
	require.Len(t, sources, 2)
	first := sources[0].(map[string]any)
	require.Equal(t, "w1", first["workspace_id"])
	require.Equal(t, "KB One", first["workspace_name"])
	require.Equal(t, "c1", first["chunk_id"])
	require.InDelta(t, 0.85, first["score"], 1e-6)
	second := sources[1].(map[string]any)
	require.NotContains(t, second, "score")
}

func TestBuildReActGraph_SearchKnowledgeFallsBackToPlainFn(t *testing.T) {
	stub := &capGWSequence{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "k1", Name: "stratum_search_knowledge",
			Arguments: map[string]any{"workspaces": []any{"kb"}, "query": "q"}}}},
		{Content: "done"},
	}}
	cg, err := graph.BuildReActGraph(stub, graph.NoopTokenRecorder{}, zap.NewNop())
	require.NoError(t, err)

	out, err := cg.Invoke(context.Background(), graph.ReActState{
		Model: "qwen", Messages: []port.LLMMessage{{Role: "user", Content: "search"}},
		AvailableTools:             []port.ToolDefinition{{Name: "stratum_search_knowledge", ProviderType: "builtin"}},
		AgentKnowledgeWorkspaceIDs: []string{"kb"},
		RAGSearchFn: func(context.Context, []string, string, int, string) (string, error) {
			return "plain result", nil
		},
		InternalToolResultGuardFn: untrustedTestGuard,
	}, graph.RunConfig[graph.ReActState]{MaxSteps: 8})

	require.NoError(t, err)
	require.Equal(t, "done", out.Output)
	require.Len(t, out.ToolObservations, 1)
	_, hasEvidence := out.ToolObservations[0].Metadata["evidence"]
	require.False(t, hasEvidence, "plain search must not fabricate evidence metadata")
}

// untrustedTestGuard 是 RAG/recall 工具成功路径测试共用的最小 guard fn：
// 直接包 <untrusted_tool_result>，模拟 application 层装配 guard 的效果。
// 缺失时 guardUntrustedToolText 会 fail-closed，工具改走 error 分支，
// 无法覆盖真实成功路径。
func untrustedTestGuard(value any) (port.GuardedToolResult, error) {
	return port.GuardedToolResult{
		ModelContent: fmt.Sprintf("<untrusted_tool_result>\n%v\n</untrusted_tool_result>", value),
		Untrusted:    true,
	}, nil
}
