package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	agent "github.com/byteBuilderX/stratum/internal/agent/application"
	agentgraph "github.com/byteBuilderX/stratum/internal/agent/application/graph"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

// Mock for ChatStore interface
type mockChatStore struct {
	agent.ChatStore
	listMsgs func(ctx context.Context, tenantID, convID, userID string) ([]*agent.ChatMessage, error)
	addMsg   func(ctx context.Context, tenantID string, msg *agent.ChatMessage) error
}

func (m *mockChatStore) ListMessages(ctx context.Context, tenantID, convID, userID string) ([]*agent.ChatMessage, error) {
	if m.listMsgs != nil {
		return m.listMsgs(ctx, tenantID, convID, userID)
	}
	return nil, nil
}

func (m *mockChatStore) AddMessage(ctx context.Context, tenantID string, msg *agent.ChatMessage) error {
	if m.addMsg != nil {
		return m.addMsg(ctx, tenantID, msg)
	}
	return nil
}

type failingPayloadStore struct{}

func (failingPayloadStore) Put(
	context.Context, port.TracePayload,
) (port.TracePayloadRef, error) {
	return port.TracePayloadRef{}, errors.New("minio unavailable")
}

// mockCapGW drives LLM responses in sequence; tools always succeed.
type mockCapGW struct {
	mu        sync.Mutex
	responses []port.CapabilityResponse
	requests  []port.CapabilityRequest
	idx       int
	err       error
}

func (m *mockCapGW) Route(_ context.Context, req port.CapabilityRequest) (port.CapabilityResponse, error) {
	if m.err != nil {
		return port.CapabilityResponse{}, m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// 记录 dispatch 时刻快照：克隆 Messages，避免 req.LLM 与调用方 s.Messages 共享
	// 底层数组——后续 appendLLMResponse 会覆盖已记录尾件（stub 浅拷贝与真实网关
	// 看到的内容不一致，导致 post-run 断言失真）。
	if req.LLM != nil {
		snap := *req.LLM
		snap.Messages = append([]port.LLMMessage{}, req.LLM.Messages...)
		req.LLM = &snap
	}
	m.requests = append(m.requests, req)
	if m.idx < len(m.responses) {
		r := m.responses[m.idx]
		m.idx++
		return r, nil
	}
	return port.CapabilityResponse{Content: "done"}, nil
}

// contextLengthErr 是 llmgateway ErrContextLengthExceeded 的测试副本：
// duck-type 标记与真实错误一致（Permanent + ContextLengthExceeded），
// 经 Execute 全链路验证最终请求降级。
type contextLengthErr struct{ msg string }

func (e *contextLengthErr) Error() string               { return e.msg }
func (e *contextLengthErr) Permanent() bool             { return true }
func (e *contextLengthErr) ContextLengthExceeded() bool { return true }

// paramValidationErr 是参数校验类 400 的测试副本：permanent 但不带
// context_length 标记——直接终止，重试无意义也不触发降级。
type paramValidationErr struct{ msg string }

func (e *paramValidationErr) Error() string   { return e.msg }
func (e *paramValidationErr) Permanent() bool { return true }

// degradeCapGW 按脚本驱动 Route：每条目要么返回响应要么返回错误，
// 用于验证最终请求 context_length_exceeded 的降级重试次数与请求内容。
type degradeCapGW struct {
	mu       sync.Mutex
	script   []capGWResult
	requests []port.CapabilityRequest
}

type capGWResult struct {
	resp port.CapabilityResponse
	err  error
}

func (g *degradeCapGW) Route(_ context.Context, req port.CapabilityRequest) (port.CapabilityResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, req)
	if len(g.script) == 0 {
		return port.CapabilityResponse{Content: "done"}, nil
	}
	r := g.script[0]
	g.script = g.script[1:]
	return r.resp, r.err
}

// TestExecute_FinalRequestContextLengthExceeded_DegradesToMinimalRetry 覆盖
// Spec D4：循环结束（最终请求）第一次 400 context_length_exceeded → 降级
// 最小请求重试一次；重试成功返回答案。降级请求剔除全部工具结果与
// assistant tool_calls，不带工具定义，末条为当前任务。
func TestExecute_FinalRequestContextLengthExceeded_DegradesToMinimalRetry(t *testing.T) {
	a := newReActAgent()
	gw := &degradeCapGW{script: []capGWResult{
		{resp: port.CapabilityResponse{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{"expr": "6*7"}}}}},
		{err: &contextLengthErr{msg: "context length exceeded"}},
		{resp: port.CapabilityResponse{Content: "final answer"}},
	}}
	a.SetCapGateway(gw)

	result, err := a.Execute(context.Background(), "calc 6*7",
		agent.WithTenantID("t1"),
		agent.WithMaxSteps(10),
		agent.WithExtraTools([]port.ToolDefinition{{Name: "calc", ProviderType: "mcp", ServerID: "math", Metadata: map[string]any{"risk_level": "read"}}}),
		agent.WithToolExecutionFn(func(context.Context, port.ToolExecutionRequest) (any, error) {
			return port.GuardedToolResult{ModelContent: "42"}, nil
		}),
	)
	require.NoError(t, err)
	require.Equal(t, "final answer", result.Output)
	require.Len(t, gw.requests, 3, "图内 2 次 + 降级重试 1 次")

	retryReq := gw.requests[2]
	require.Nil(t, retryReq.LLM.Tools, "降级请求不带工具定义")
	require.NotEmpty(t, retryReq.LLM.Messages)
	for _, m := range retryReq.LLM.Messages {
		require.NotEqual(t, "tool", m.Role, "降级请求剔除全部工具结果")
		require.Empty(t, m.ToolCalls, "降级请求剔除 assistant tool_calls")
	}
	last := retryReq.LLM.Messages[len(retryReq.LLM.Messages)-1]
	require.Equal(t, "user", last.Role)
	require.Equal(t, "calc 6*7", last.Content)
}

// TestExecute_FinalRequestContextLengthExceeded_RetryFailsTerminates 覆盖
// Spec D4：降级重试仍失败 → 终止，不换模型不退避循环。
func TestExecute_FinalRequestContextLengthExceeded_RetryFailsTerminates(t *testing.T) {
	a := newReActAgent()
	gw := &degradeCapGW{script: []capGWResult{
		{resp: port.CapabilityResponse{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{}}}}},
		{err: &contextLengthErr{msg: "context length exceeded"}},
		{err: &contextLengthErr{msg: "context length exceeded"}},
	}}
	a.SetCapGateway(gw)

	_, err := a.Execute(context.Background(), "calc",
		agent.WithTenantID("t1"),
		agent.WithMaxSteps(10),
		agent.WithExtraTools([]port.ToolDefinition{{Name: "calc", ProviderType: "mcp", ServerID: "math", Metadata: map[string]any{"risk_level": "read"}}}),
		agent.WithToolExecutionFn(func(context.Context, port.ToolExecutionRequest) (any, error) {
			return port.GuardedToolResult{ModelContent: "42"}, nil
		}),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "context length exceeded")
	require.Len(t, gw.requests, 3, "降级只重试一次，仍失败即终止")
}

// TestExecute_NonContextLengthErrorNoDegrade 覆盖 Spec D4：参数校验类 400
// （非 context_length）直接终止，不降级重试（重试无意义，是 bug）。
func TestExecute_NonContextLengthErrorNoDegrade(t *testing.T) {
	a := newReActAgent()
	gw := &degradeCapGW{script: []capGWResult{
		{err: &paramValidationErr{msg: "invalid parameter schema"}},
	}}
	a.SetCapGateway(gw)

	_, err := a.Execute(context.Background(), "calc",
		agent.WithTenantID("t1"),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid parameter schema")
	require.Len(t, gw.requests, 1, "参数校验类 400 直接终止，无降级重试")
}

// TestExecute_MaxTokensResolvedWithFallback 验证执行层兜底（生产故障：
// max_tokens=0 被 omitempty 丢弃 → 智谱 400）：无 WithMaxTokens 时单次
// LLM 请求携带 DefaultOutputReserveTokens；WithMaxTokens(512) 时携带 512。
func TestExecute_MaxTokensResolvedWithFallback(t *testing.T) {
	cases := []struct {
		name   string
		option agent.ExecutionOption
		want   int
	}{
		{"falls back to default reserve when unset", nil, constants.DefaultOutputReserveTokens},
		{"uses explicit max tokens", agent.WithMaxTokens(512), 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newReActAgent()
			gw := &mockCapGW{responses: []port.CapabilityResponse{{Content: "42"}}}
			a.SetCapGateway(gw)

			opts := []agent.ExecutionOption{agent.WithTenantID("t1")}
			if tc.option != nil {
				opts = append(opts, tc.option)
			}
			_, err := a.Execute(context.Background(), "hi", opts...)
			require.NoError(t, err)
			require.Len(t, gw.requests, 1, "直接回答只发一次 LLM 请求")
			require.Equal(t, tc.want, gw.requests[0].LLM.MaxTokens)
		})
	}
}

// TestIsFinalRequest 覆盖 isFinalRequest 语义：图终止时最后一条消息是
// 等待工具结果的 assistant 消息（非最终回答位置）→ 不降级；其余位置
// （tool 结果、user 任务、空）→ 最终请求位置。
func TestIsFinalRequest(t *testing.T) {
	cases := []struct {
		name     string
		messages []port.LLMMessage
		want     bool
	}{
		{name: "empty", messages: nil, want: true},
		{name: "waiting for tool result", messages: []port.LLMMessage{{Role: "assistant", ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc"}}}}, want: false},
		{name: "tool result last", messages: []port.LLMMessage{{Role: "tool", ToolCallID: "c1", Content: "42"}}, want: true},
		{name: "user task last", messages: []port.LLMMessage{{Role: "user", Content: "task"}}, want: true},
		{name: "plain assistant last", messages: []port.LLMMessage{{Role: "assistant", Content: "answer"}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, agent.IsFinalRequestForTest(agentgraph.ReActState{Messages: tc.messages}))
		})
	}
}

func newReActAgent() *agent.BaseAgent {
	cfg := &agent.AgentConfig{
		ID:            "agent-001",
		Name:          "test-agent",
		Type:          agent.ReActAgent,
		LLMModel:      "qwen-turbo",
		SystemPrompt:  "You are helpful.",
		MaxIterations: 5,
	}
	return agent.NewBaseAgent(cfg, zap.NewNop())
}

func TestBaseAgent_ReActExecute_DirectAnswer(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{responses: []port.CapabilityResponse{
		{Content: "42", Usage: port.TokenUsage{Total: 20}},
	}}
	a.SetCapGateway(gw)

	result, err := a.Execute(context.Background(), "what is 6x7?",
		agent.WithTenantID("t1"),
	)
	require.NoError(t, err)
	require.Equal(t, "42", result.Output)
	require.Equal(t, "agent-001", result.AgentID)
	require.Equal(t, 1, result.Steps)
	require.Equal(t, 20, result.TokensUsed)
}

func TestBaseAgent_HistoricalTypeValuesUseUnifiedReActPath(t *testing.T) {
	for _, historicalType := range []agent.AgentType{"react", "planning", "cot", "tool_calling", "rag", "swarm"} {
		t.Run(string(historicalType), func(t *testing.T) {
			a := newReActAgent()
			a.GetConfig().Type = historicalType
			a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{{Content: "unified"}}})
			result, err := a.Execute(context.Background(), "answer", agent.WithTenantID("t1"))
			require.NoError(t, err)
			require.Equal(t, "unified", result.Output)
		})
	}
}

func TestBaseAgent_ReActExecute_WithToolCall(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{
		responses: []port.CapabilityResponse{
			{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{"expr": "6*7"}}}},
			{Content: "The answer is 42"},
		},
	}
	a.SetCapGateway(gw)

	result, err := a.Execute(context.Background(), "calc 6*7",
		agent.WithTenantID("t1"),
		agent.WithMaxSteps(10),
		agent.WithExtraTools([]port.ToolDefinition{{Name: "calc", ProviderType: "mcp", ServerID: "math", Metadata: map[string]any{"risk_level": "read"}}}),
		agent.WithToolExecutionFn(func(context.Context, port.ToolExecutionRequest) (any, error) {
			return port.GuardedToolResult{ModelContent: "42"}, nil
		}),
	)
	require.NoError(t, err)
	require.Equal(t, "The answer is 42", result.Output)
	require.Equal(t, 2, result.Steps)
	require.Len(t, result.ToolCalls, 1)
	require.Equal(t, "calc", result.ToolCalls[0].ToolName)
}

// TestBaseAgent_ReActExecute_ToolArtifactsUseProfileVersionConstant 回归：
// 非 system-assistant agent（EvolutionTrace.ResourceManifest 无
// system-assistant-profile key）执行时产生 tool artifacts，result.Artifacts 的
// ProfileVersion 必须非空且等于常量。此前 Execute 从 map 读版本，普通 agent
// 读到空串 → chat_store.decodeExecutionArtifacts 报 invalid profile version，
// 导致 chat message 保存失败。
func TestBaseAgent_ReActExecute_ToolArtifactsUseProfileVersionConstant(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{
		responses: []port.CapabilityResponse{
			{ToolCalls: []port.ToolCall{{ID: "c1", Name: domain.SystemAssistantToolDiagnoseTenant, Arguments: map[string]any{"areas": []string{"agent"}}}}},
			{Content: "diagnostic complete"},
		},
	}
	a.SetCapGateway(gw)

	result, err := a.Execute(context.Background(), "diagnose tenant",
		agent.WithTenantID("t1"),
		agent.WithMaxSteps(10),
		agent.WithDiagnosticFn(func(context.Context, []domain.DiagnosticArea) (domain.DiagnosticEvidence, error) {
			return domain.DiagnosticEvidence{
				Scope: domain.DiagnosticScopeTenant,
				Facts: []domain.DiagnosticFact{{Area: domain.DiagnosticAreaAgent, Statement: "ok", Source: "unit-test"}},
			}, nil
		}),
	)
	require.NoError(t, err)
	require.Equal(t, "diagnostic complete", result.Output)
	require.NotEmpty(t, result.Artifacts, "工具产生 evidence 应产出 diagnostic_report artifact")
	for _, artifact := range result.Artifacts {
		require.Equal(t, domain.CurrentExecutionArtifactProfileVersion, artifact.ProfileVersion,
			"artifact %q 的 ProfileVersion 必须为常量", artifact.Type)
	}
}

func TestBaseAgentPayloadStoreFailureDoesNotFailExecution(t *testing.T) {
	t.Setenv("OTEL_CAPTURE_CONTENT", "true")
	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{{Content: "answer"}}})
	result, err := a.Execute(context.Background(), "question",
		agent.WithTenantID("tenant-1"),
		agent.WithTraceID("trace-1"),
		agent.WithTracePayloadStore(failingPayloadStore{}),
	)
	require.NoError(t, err)
	require.Equal(t, "answer", result.Output)
}

func TestBaseAgentOTelHierarchyFollowsReActGraphContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{"expr": "6*7"}}}, Usage: port.TokenUsage{Prompt: 11, Completion: 3, Total: 14}},
		{Content: "The answer is 42", Usage: port.TokenUsage{Prompt: 17, Completion: 5, Total: 22}},
	}})
	_, err := a.Execute(context.Background(), "calc 6*7",
		agent.WithTenantID("tenant-1"),
		agent.WithExtraTools([]port.ToolDefinition{{
			Name: "calc", ProviderType: "mcp", ProviderID: "math", ServerID: "math", CapabilityID: "calculate",
			Metadata: map[string]any{"risk_level": "read", "version_id": "tool-revision-1"},
		}}),
		agent.WithToolExecutionFn(func(context.Context, port.ToolExecutionRequest) (any, error) {
			return port.GuardedToolResult{ModelContent: "42"}, nil
		}),
	)
	require.NoError(t, err)

	spans := recorder.Ended()
	byName := make(map[string][]sdktrace.ReadOnlySpan)
	for _, span := range spans {
		byName[span.Name()] = append(byName[span.Name()], span)
	}
	require.Len(t, byName["agent.execute"], 1)
	require.Len(t, byName["react.graph.invoke"], 1)
	require.Len(t, byName["react.llm"], 2)
	require.Len(t, byName["react.tool"], 1)

	rootID := byName["agent.execute"][0].SpanContext().SpanID()
	graph := byName["react.graph.invoke"][0]
	require.Equal(t, rootID, graph.Parent().SpanID())
	for _, name := range []string{"react.llm", "react.tool"} {
		for _, span := range byName[name] {
			require.Equal(t, graph.SpanContext().SpanID(), span.Parent().SpanID(), name)
		}
	}
	firstLLM := spanAttributes(byName["react.llm"][0])
	require.Equal(t, "qwen-turbo", firstLLM["gen_ai.request.model"])
	require.Equal(t, int64(11), firstLLM["gen_ai.usage.input_tokens"])
	require.Equal(t, int64(3), firstLLM["gen_ai.usage.output_tokens"])
	require.NotEmpty(t, firstLLM["stratum.input.sha256"])
	require.NotEmpty(t, firstLLM["stratum.output.sha256"])
	toolAttrs := spanAttributes(byName["react.tool"][0])
	require.Equal(t, "c1", toolAttrs["gen_ai.tool.call.id"])
	require.Equal(t, "calc", toolAttrs["gen_ai.tool.name"])
	require.Equal(t, "mcp", toolAttrs["stratum.provider.type"])
	require.Equal(t, "math", toolAttrs["stratum.server.id"])
	require.Equal(t, "calculate", toolAttrs["stratum.capability.id"])
	require.Equal(t, "tool-revision-1", toolAttrs["stratum.resource.revision_id"])
	require.Equal(t, "tenant-1", toolAttrs["opik.metadata.stratum.tenant_id"])
	require.Equal(t, "mcp", toolAttrs["opik.metadata.stratum.provider_type"])
	require.Equal(t, "tool-revision-1", toolAttrs["opik.metadata.stratum.resource_revision_id"])
	require.NotEmpty(t, toolAttrs["stratum.arguments.sha256"])
	require.NotEmpty(t, toolAttrs["stratum.result.sha256"])
}

// TestBaseAgentHTTPDroppedParentStillSamplesAgentExecute is the HTTP-entry
// regression guard for the P0 sampler: with OTEL_SAMPLING_RATIO=0.1-style head
// sampling the otelgin root span is dropped, and if agent.execute were its
// child span ParentBased would short-circuit on the parent's sampling bit and
// never call the agent sampler — the whole agent trace would vanish before
// reaching the collector. agent.execute must be its own root (WithNewRoot) and
// carry the agent attribute so NewAgentSampler always RecordAndSample's it.
func TestBaseAgentHTTPDroppedParentStillSamplesAgentExecute(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		// ratio=0：非 agent 根全丢，agent.execute 恒采（模拟生产 0.1 的极端形态）。
		sdktrace.WithSampler(sdktrace.ParentBased(observability.NewAgentSampler(0))),
	)
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	// 模拟 HTTP 入口：先在请求 ctx 创建被丢弃的 otelgin 根 span。
	httpCtx, httpSpan := provider.Tracer("stratum/test").Start(context.Background(), "POST /agents/:id/execute")
	defer httpSpan.End()
	require.False(t, httpSpan.SpanContext().IsSampled(), "HTTP root must be dropped at ratio 0")

	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{{Content: "answer"}}})
	_, err := a.Execute(httpCtx, "question", agent.WithTenantID("tenant-1"))
	require.NoError(t, err)

	var execSpan sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() == "agent.execute" {
			execSpan = s
			break
		}
	}
	require.NotNil(t, execSpan, "agent.execute span must exist")
	require.True(t, execSpan.SpanContext().IsSampled(), "agent.execute must stay sampled when its HTTP parent is dropped")
	require.False(t, execSpan.Parent().IsValid(), "agent.execute must be a new root (WithNewRoot), not a child of the dropped HTTP span")
}

// TestBaseAgentExecutionSpanCarriesParamSnapshotAndPromptVersion verifies the
// Phase 2 trace enhancement: the execution span always carries the parameter
// fingerprint (stratum.params.sha256), value attributes appear only under
// trace.capture_parameters, and LLM spans carry the effective temperature /
// max_tokens plus the system prompt version fingerprint.
func TestBaseAgentExecutionSpanCarriesParamSnapshotAndPromptVersion(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{
		{Content: "answer", Usage: port.TokenUsage{Prompt: 10, Completion: 4, Total: 14}},
	}})
	_, err := a.Execute(context.Background(), "hello",
		agent.WithTenantID("tenant-1"),
		agent.WithTemperature(0.7),
		agent.WithMaxTokens(512),
		agent.WithReasoningEffort("high"),
		agent.WithCaptureParameters(true),
	)
	require.NoError(t, err)

	spans := recorder.Ended()
	byName := make(map[string][]sdktrace.ReadOnlySpan)
	for _, span := range spans {
		byName[span.Name()] = append(byName[span.Name()], span)
	}

	execAttrs := spanAttributes(byName["agent.execute"][0])
	require.NotEmpty(t, execAttrs["stratum.params.sha256"])
	require.InDelta(t, 0.7, execAttrs["stratum.params.temperature"], 1e-6)
	require.Equal(t, int64(512), execAttrs["stratum.params.max_tokens"])
	require.Equal(t, "high", execAttrs["stratum.params.reasoning_effort"])

	llmAttrs := spanAttributes(byName["react.llm"][0])
	require.InDelta(t, 0.7, llmAttrs["gen_ai.request.temperature"], 1e-6)
	require.Equal(t, int64(512), llmAttrs["gen_ai.request.max_tokens"])
	require.Equal(t, "system_prompt", llmAttrs["stratum.prompt.key"])
	require.NotEmpty(t, llmAttrs["stratum.prompt.version"])
}

// TestBaseAgentExecutionSpanOmitsParamValuesWhenCaptureDisabled verifies the
// privacy gate: without trace.capture_parameters the raw values are absent
// while the fingerprint remains.
func TestBaseAgentExecutionSpanOmitsParamValuesWhenCaptureDisabled(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{
		{Content: "answer", Usage: port.TokenUsage{Prompt: 10, Completion: 4, Total: 14}},
	}})
	_, err := a.Execute(context.Background(), "hello",
		agent.WithTenantID("tenant-1"),
	)
	require.NoError(t, err)

	byName := make(map[string][]sdktrace.ReadOnlySpan)
	for _, span := range recorder.Ended() {
		byName[span.Name()] = append(byName[span.Name()], span)
	}
	execAttrs := spanAttributes(byName["agent.execute"][0])
	require.NotEmpty(t, execAttrs["stratum.params.sha256"])
	_, hasTemperature := execAttrs["stratum.params.temperature"]
	require.False(t, hasTemperature, "parameter values must be gated by trace.capture_parameters")

	// 0=unset semantics: temperature attribute absent from LLM spans too.
	llmAttrs := spanAttributes(byName["react.llm"][0])
	_, hasGenTemperature := llmAttrs["gen_ai.request.temperature"]
	require.False(t, hasGenTemperature, "unset temperature must not be recorded as 0, got %#v", llmAttrs)
}

func TestBaseAgentRootSpanCarriesEvaluationEvidence(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{{Content: "42"}}})
	_, err := a.Execute(context.Background(), "what is 6x7?",
		agent.WithTenantID("tenant-1"),
		agent.WithUserID("user-1"),
		agent.WithTraceID("business-trace-1"),
		agent.WithExecutionID("execution-1"),
		agent.WithConversationID("conversation-1"),
		agent.WithEvolutionTraceMetadata(agent.EvolutionTraceMetadata{
			Evaluation:   true,
			ExperimentID: "experiment-1",
			Variant:      "canary",
			ExperimentAssignments: map[string]agent.ExperimentAssignment{
				"skill:skill-1": {ExperimentID: "experiment-1", Variant: "canary"},
			},
			ResourceManifest: map[string]string{
				"agent:agent-001": "agent-revision-1",
				"skill:skill-1":   "skill-revision-2",
			},
		}),
	)
	require.NoError(t, err)

	var root sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "agent.execute" {
			root = span
			break
		}
	}
	require.NotNil(t, root)
	attrs := spanAttributes(root)
	require.Equal(t, "tenant-1", attrs["stratum.tenant.id"])
	require.Equal(t, "user-1", attrs["stratum.user.id"])
	require.Equal(t, "business-trace-1", attrs["stratum.trace.id"])
	require.Equal(t, "execution-1", attrs["stratum.execution.id"])
	require.Equal(t, "conversation-1", attrs["stratum.conversation.id"])
	require.Equal(t, "tenant-1", attrs["opik.metadata.stratum.tenant_id"])
	require.Equal(t, "business-trace-1", attrs["opik.metadata.stratum.trace_id"])
	require.Equal(t, "execution-1", attrs["opik.metadata.stratum.execution_id"])
	require.Equal(t, "true", attrs["stratum.evaluation"])
	require.Equal(t, "experiment-1", attrs["stratum.experiment.id"])
	require.Equal(t, "canary", attrs["stratum.experiment.variant"])
	var assignments map[string]agent.ExperimentAssignment
	require.NoError(t, json.Unmarshal([]byte(attrs["stratum.experiment.assignments"].(string)), &assignments))
	require.Equal(t, "experiment-1", assignments["skill:skill-1"].ExperimentID)
	require.Equal(t, "success", attrs["opik.metadata.stratum.status"])
	require.Equal(t, int64(0), attrs["opik.metadata.stratum.total_tokens"])
	require.Contains(t, attrs, "opik.metadata.stratum.duration_ms")
	require.Contains(t, attrs, "opik.metadata.stratum.cost_usd")
	var manifest map[string]string
	require.NoError(t, json.Unmarshal([]byte(attrs["stratum.resource.manifest"].(string)), &manifest))
	require.Equal(t, "skill-revision-2", manifest["skill:skill-1"])
}

func TestBaseAgentCopiesExecutionEvidenceToRequestSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	ctx, requestSpan := otel.Tracer("test/http").Start(context.Background(), "/agents/:id/execute")
	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{responses: []port.CapabilityResponse{{Content: "OK"}}})
	_, err := a.Execute(ctx, "reply OK",
		agent.WithTenantID("tenant-1"),
		agent.WithUserID("user-1"),
		agent.WithTraceID("business-trace-1"),
		agent.WithExecutionID("execution-1"),
	)
	require.NoError(t, err)
	requestSpan.End()

	var request sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "/agents/:id/execute" {
			request = span
			break
		}
	}
	require.NotNil(t, request)
	attrs := spanAttributes(request)
	require.Equal(t, "tenant-1", attrs["opik.metadata.stratum.tenant_id"])
	require.Equal(t, "user-1", attrs["opik.metadata.stratum.user_id"])
	require.Equal(t, "execution-1", attrs["opik.metadata.stratum.execution_id"])
	require.Equal(t, "agent-001", attrs["opik.metadata.stratum.agent_id"])
	require.Equal(t, "success", attrs["opik.metadata.stratum.status"])
	require.Contains(t, attrs, "opik.metadata.stratum.duration_ms")
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	out := make(map[string]any)
	for _, attr := range span.Attributes() {
		out[string(attr.Key)] = attr.Value.AsInterface()
	}
	return out
}

func TestBaseAgent_ReActExecute_CapGWNil(t *testing.T) {
	a := newReActAgent()
	// no SetCapGateway call → CapGateway is nil

	_, err := a.Execute(context.Background(), "hello")
	require.Error(t, err)
	require.Contains(t, err.Error(), "CapGateway not set")
}

func TestBaseAgent_ReActExecute_LLMError(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{err: errors.New("llm unavailable")}
	a.SetCapGateway(gw)

	_, err := a.Execute(context.Background(), "hello")
	require.Error(t, err)
}

func TestBaseAgentOTelMarksLLMFailureAsError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	a := newReActAgent()
	a.SetCapGateway(&mockCapGW{err: errors.New("llm unavailable")})
	_, err := a.Execute(context.Background(), "hello", agent.WithTenantID("tenant-1"))
	require.Error(t, err)

	for _, span := range recorder.Ended() {
		if span.Name() == "react.llm" {
			require.Equal(t, codes.Error, span.Status().Code)
			return
		}
	}
	t.Fatal("react.llm span not found")
}

func TestWithConversationID_SetsField(t *testing.T) {
	cfg := &agent.ExecutionConfig{}
	agent.WithConversationID("conv-123")(cfg)
	require.Equal(t, "conv-123", cfg.ConversationID)
}

func TestWithUserID_SetsField(t *testing.T) {
	cfg := &agent.ExecutionConfig{}
	agent.WithUserID("user-456")(cfg)
	require.Equal(t, "user-456", cfg.UserID)
}

func TestWithExecutionID_SetsField(t *testing.T) {
	cfg := &agent.ExecutionConfig{}
	agent.WithExecutionID("exec-123")(cfg)
	require.Equal(t, "exec-123", cfg.ExecutionID)
}

func TestWithHistoryWindow_SetsField(t *testing.T) {
	cfg := &agent.ExecutionConfig{}
	agent.WithHistoryWindow(10)(cfg)
	require.Equal(t, 10, cfg.HistoryWindow)
}

func TestBaseAgent_SetCapGateway_DataRace(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{responses: []port.CapabilityResponse{{Content: "ok"}}}
	var wg sync.WaitGroup
	// concurrent SetCapGateway + Execute
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.SetCapGateway(gw)
		}()
		go func() {
			defer wg.Done()
			_, _ = a.Execute(context.Background(), "ping")
		}()
	}
	wg.Wait()
}

func TestBaseAgent_WithChatStore_SetsField(t *testing.T) {
	a := newReActAgent()
	cs := &mockChatStore{}
	result := a.WithChatStore(cs)
	require.NotNil(t, result)
}

func TestExecute_PersistsMessagesToChatStore(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{
		responses: []port.CapabilityResponse{
			{Content: "six", Usage: port.TokenUsage{Total: 5}},
		},
	}
	a.SetCapGateway(gw)

	var savedMsgs []*agent.ChatMessage
	cs := &mockChatStore{
		addMsg: func(ctx context.Context, tenantID string, msg *agent.ChatMessage) error {
			saved := *msg
			savedMsgs = append(savedMsgs, &saved)
			return nil
		},
	}
	a.WithChatStore(cs)

	_, err := a.Execute(context.Background(), "what is 3+3?",
		agent.WithTenantID("t1"),
		agent.WithConversationID("conv-xyz"),
		agent.WithUserID("user-2"),
	)
	require.NoError(t, err)
	require.Len(t, savedMsgs, 2)
	require.Equal(t, "user", savedMsgs[0].Role)
	require.Equal(t, "what is 3+3?", savedMsgs[0].Content)
	require.Equal(t, "assistant", savedMsgs[1].Role)
	require.Equal(t, "six", savedMsgs[1].Content)
	require.Equal(t, "conv-xyz", savedMsgs[0].ConversationID)
	require.Equal(t, "conv-xyz", savedMsgs[1].ConversationID)
}

func TestExecute_ReturnsToolTraceAndPersistsSummaryMessage(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{
		responses: []port.CapabilityResponse{
			{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{"expr": "6*7"}}}},
			{Content: "The answer is 42", Usage: port.TokenUsage{Total: 10}},
		},
	}
	a.SetCapGateway(gw)

	var savedMsgs []*agent.ChatMessage
	cs := &mockChatStore{
		addMsg: func(ctx context.Context, tenantID string, msg *agent.ChatMessage) error {
			saved := *msg
			savedMsgs = append(savedMsgs, &saved)
			return nil
		},
	}
	a.WithChatStore(cs)

	result, err := a.Execute(context.Background(), "calc 6*7",
		agent.WithTenantID("t1"),
		agent.WithTraceID("trace-1"),
		agent.WithExecutionID("exec-1"),
		agent.WithConversationID("conv-xyz"),
		agent.WithUserID("user-2"),
		agent.WithMaxSteps(10),
		agent.WithExtraTools([]port.ToolDefinition{{Name: "calc", ProviderType: "mcp", ServerID: "math", Metadata: map[string]any{"risk_level": "read"}}}),
		agent.WithToolExecutionFn(func(context.Context, port.ToolExecutionRequest) (any, error) {
			return port.GuardedToolResult{ModelContent: "42"}, nil
		}),
	)
	require.NoError(t, err)
	require.Len(t, result.ToolObservations, 1)
	require.Equal(t, "c1", result.ToolObservations[0].ToolCallID)
	require.Equal(t, "exec-1", result.ToolObservations[0].ExecutionID)
	require.Equal(t, "calc", result.ToolObservations[0].ToolName)
	require.Equal(t, "42", result.ToolObservations[0].RawText)
	require.NotEmpty(t, result.TraceEvents)
	for _, ev := range result.TraceEvents {
		require.Equal(t, "exec-1", ev.ExecutionID)
	}

	require.Len(t, savedMsgs, 3)
	require.Equal(t, "assistant", savedMsgs[2].Role)
	require.Contains(t, savedMsgs[2].Content, "本轮工具观察摘要")
	require.Contains(t, savedMsgs[2].Content, "calc")
	require.True(t, savedMsgs[2].SkipOutbox)
	require.Equal(t, agent.ChatMessageVisibilityInternal, savedMsgs[2].Visibility)
}

func TestExecute_LoadsHistoryFromChatStore(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{
		responses: []port.CapabilityResponse{
			{Content: "I remember you asked before", Usage: port.TokenUsage{Total: 5}},
		},
	}
	a.SetCapGateway(gw)

	history := []*agent.ChatMessage{
		{Role: "user", Content: "what is 2+2?"},
		{Role: "assistant", Content: "2+2=4"},
	}
	cs := &mockChatStore{
		listMsgs: func(ctx context.Context, tenantID, convID, userID string) ([]*agent.ChatMessage, error) {
			return history, nil
		},
	}
	a.WithChatStore(cs)

	result, err := a.Execute(context.Background(), "and 3+3?",
		agent.WithTenantID("t1"),
		agent.WithConversationID("conv-abc"),
		agent.WithUserID("user-1"),
	)
	require.NoError(t, err)
	require.Equal(t, "I remember you asked before", result.Output)
}

func TestExecute_CompactsOverflowingInitialHistory(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{responses: []port.CapabilityResponse{{Content: "done"}}}
	compactor := &fakeCompactor{summary: "compacted earlier discussion"}
	a.SetCapGateway(gw)
	a.SetHistoryCompactor(compactor)

	history := makeHistory(12)
	a.WithChatStore(&mockChatStore{
		listMsgs: func(context.Context, string, string, string) ([]*agent.ChatMessage, error) {
			return history, nil
		},
	})

	// 账本语义：默认 fallback 窗口 8000 − safety 6400 − 自动输出预留 4096
	// → usable 0，初始组装退化为最小 head（system + 输入），无历史可压缩；
	// 显式窗口 30000 下 HistoryCap = 1904−380−380−任务(2) = 1142，
	// 才让溢出压缩有意义（Spec 第 2 节）。
	_, err := a.Execute(
		context.Background(),
		"continue",
		agent.WithTenantID("t1"),
		agent.WithConversationID("conv-abc"),
		agent.WithUserID("user-1"),
		agent.WithHistoryWindow(4),
		agent.WithMaxContextTokens(30000),
	)
	require.NoError(t, err)
	require.Equal(t, 1, compactor.callCount)
	// D3 轮数窗口：12 条 = 6 轮，window 4 轮 → 仅溢出 2 轮 = 4 条进压缩
	// （旧条数窗口下 12−4 = 8 条）。整轮截断不拆工具对。
	require.Equal(t, 4, compactor.gotMsgs)
	require.Len(t, gw.requests, 1)
	require.NotNil(t, gw.requests[0].LLM)
	require.True(t, strings.Contains(gw.requests[0].LLM.Messages[0].Content, compactor.summary))
}

func TestBuildInitMessages_EmptyHistory(t *testing.T) {
	msgs := agent.BuildInitMessages("You are helpful.", nil, 0)
	require.Len(t, msgs, 1)
	require.Equal(t, "system", msgs[0].Role)
	require.Equal(t, "You are helpful.", msgs[0].Content)
}

func TestBuildInitMessages_PreservesAssistantRole(t *testing.T) {
	history := []*agent.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	msgs := agent.BuildInitMessages("sys", history, 10)
	require.Len(t, msgs, 3)
	require.Equal(t, "system", msgs[0].Role)
	require.Equal(t, "user", msgs[1].Role)
	require.Equal(t, "assistant", msgs[2].Role)
}

func TestBuildInitMessages_WindowTruncation(t *testing.T) {
	history := make([]*agent.ChatMessage, 25)
	for i := range history {
		history[i] = &agent.ChatMessage{Role: "user", Content: "msg"}
	}
	msgs := agent.BuildInitMessages("", history, 20)
	// 20 history + 0 system (empty string)
	require.Len(t, msgs, 20)
}

func TestBuildInitMessages_DefaultWindow(t *testing.T) {
	history := make([]*agent.ChatMessage, 25)
	for i := range history {
		history[i] = &agent.ChatMessage{Role: "user", Content: "msg"}
	}
	msgs := agent.BuildInitMessages("sys", history, 0) // 0 → default 20
	require.Len(t, msgs, 21)                           // 20 history + 1 system
}

func TestBaseAgent_AddToMemory_StillAddsToSlice(t *testing.T) {
	a := newReActAgent()
	a.AddToMemory(agent.Message{Role: "user", Content: "hello"})
	mem := a.GetMemory()
	require.Len(t, mem, 1)
	require.Equal(t, "user", mem[0].Role)
	require.Equal(t, "hello", mem[0].Content)
}

func TestWithCompactionCooldownSec_SetsField(t *testing.T) {
	cfg := &agent.ExecutionConfig{}
	agent.WithCompactionCooldownSec(15)(cfg)
	if cfg.CompactionCooldownSec != 15 {
		t.Errorf("CompactionCooldownSec = %d, want 15", cfg.CompactionCooldownSec)
	}
}

// TestExecute_CompactionCooldownSuppressesPerStepSummary 覆盖 Spec 第 4 节默认
// 冷却（0 = constants.DefaultCompactionCooldown）：一次执行内首次循环压缩触发
// 同步摘要并回写时间戳，冷却窗口内后续超限步骤退化为截断兜底，不再每步触发
// 同步 LLM 摘要（spec 症状 #2）。
func TestExecute_CompactionCooldownSuppressesPerStepSummary(t *testing.T) {
	a := newReActAgent()
	gw := &mockCapGW{responses: []port.CapabilityResponse{
		{ToolCalls: []port.ToolCall{{ID: "c1", Name: "calc", Arguments: map[string]any{"expr": "6*7"}}}},
		{ToolCalls: []port.ToolCall{{ID: "c2", Name: "calc", Arguments: map[string]any{"expr": "7*8"}}}},
		{Content: "done"},
	}}
	compactor := &fakeCompactor{summary: "compacted earlier discussion"}
	a.SetCapGateway(gw)
	a.SetHistoryCompactor(compactor)
	a.WithChatStore(&mockChatStore{
		listMsgs: func(context.Context, string, string, string) ([]*agent.ChatMessage, error) {
			return makeHistory(12), nil
		},
	})

	_, err := a.Execute(
		context.Background(),
		"continue",
		agent.WithTenantID("t1"),
		agent.WithConversationID("conv-abc"),
		agent.WithUserID("user-1"),
		agent.WithHistoryWindow(4),
		// recentGroups=1 使中间段非空、压缩走同步摘要路径。压缩在默认消息形状下
		// 确实触发（本测试 callCount==3 即证据）；早前草稿场景不触发的真实原因是
		// 估算未过阈值，而非默认形状下压缩永不触发。
		agent.WithCompactionRecentGroups(1),
		// 默认 safety 0.2 下 HistoryCap 随 usable 放大（30000 窗口 HistoryCap
		// ≈11940t，循环内不再持续超限，冷却分支走不到）。10000 窗口
		// HistoryCap ≈2340t：每步工具结果（≈1000t）都超限，冷却抑制可观测。
		agent.WithMaxContextTokens(10000),
		agent.WithMaxSteps(10),
		agent.WithExtraTools([]port.ToolDefinition{{Name: "calc", ProviderType: "mcp", ServerID: "math", Metadata: map[string]any{"risk_level": "read"}}}),
		agent.WithToolExecutionFn(func(context.Context, port.ToolExecutionRequest) (any, error) {
			return port.GuardedToolResult{ModelContent: strings.Repeat("x", 3000)}, nil
		}),
	)
	require.NoError(t, err)
	require.Len(t, gw.requests, 3)
	// D3 轮数窗口下每步工具段非空、逐步骤超限都触发同步摘要（无冷却按步
	// 累计）→ callCount == 3；冷却窗口语义抑制的是同一执行内后续多次触发。
	require.Equal(t, 3, compactor.callCount)
}
