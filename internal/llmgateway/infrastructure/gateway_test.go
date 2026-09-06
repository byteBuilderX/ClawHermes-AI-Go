package infrastructure_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	"github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
)

// errChatProto returns errors on all chat methods.
type errChatProto struct{}

func (errChatProto) Complete(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest) (*infrastructure.CompletionResponse, error) {
	return nil, errors.New("provider error")
}
func (errChatProto) CompleteStream(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest, onToken func(string)) (*infrastructure.CompletionResponse, error) {
	return nil, errors.New("provider error")
}
func (errChatProto) Health(ctx context.Context, cfg infrastructure.ProviderConfig) error {
	return errors.New("provider error")
}
func (errChatProto) ListModels(ctx context.Context, cfg infrastructure.ProviderConfig) ([]infrastructure.DiscoveredModel, error) {
	return nil, nil
}

// successChatProto returns successful responses.
type successChatProto struct{}

func (successChatProto) Complete(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest) (*infrastructure.CompletionResponse, error) {
	return &infrastructure.CompletionResponse{Content: "ok", Usage: infrastructure.TokenUsage{PromptTokens: 1, CompletionTokens: 1}}, nil
}
func (successChatProto) CompleteStream(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest, onToken func(string)) (*infrastructure.CompletionResponse, error) {
	onToken("test")
	return &infrastructure.CompletionResponse{Content: "test", Usage: infrastructure.TokenUsage{PromptTokens: 1, CompletionTokens: 1}}, nil
}
func (successChatProto) Health(ctx context.Context, cfg infrastructure.ProviderConfig) error {
	return nil
}
func (successChatProto) ListModels(ctx context.Context, cfg infrastructure.ProviderConfig) ([]infrastructure.DiscoveredModel, error) {
	return nil, nil
}

func TestNewGateway(t *testing.T) {
	gateway := infrastructure.NewGateway(nil, nil, nil)
	if gateway == nil {
		t.Error("expected Gateway to be non-nil")
	}
}

func TestCompletionRequestHasToolsField(t *testing.T) {
	req := infrastructure.CompletionRequest{
		Model:    "qwen-turbo",
		Messages: []infrastructure.Message{{Role: "user", Content: "hi"}},
		Tools: []infrastructure.Tool{{
			Type: "function",
			Function: infrastructure.ToolFunction{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		ToolChoice: "auto",
	}
	b, err := json.Marshal(req)
	require.NoError(t, err)
	require.Contains(t, string(b), `"tools"`)
	require.Contains(t, string(b), `"tool_choice"`)
}

func TestMessageHasToolCallFields(t *testing.T) {
	msg := infrastructure.Message{
		Role: "assistant",
		ToolCalls: []infrastructure.ToolCall{{
			ID:   "call_abc",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "get_weather", Arguments: `{"city":"Beijing"}`},
		}},
	}
	b, err := json.Marshal(msg)
	require.NoError(t, err)
	require.Contains(t, string(b), `"tool_calls"`)
}

func TestCompletionResponseHasToolCallsField(t *testing.T) {
	resp := infrastructure.CompletionResponse{
		ToolCalls: []infrastructure.ToolCall{{ID: "call_1", Type: "function"}},
	}
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	require.Contains(t, string(b), `"tool_calls"`)
}

func TestGatewayOTelMarksFailureAsError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	modelRepo := &mockModelRepo{
		models: []domain.Model{
			{ID: "m1", ProviderID: "p1", Name: "qwen-turbo", Enabled: true,
				Capabilities: []domain.ModelCapability{domain.CapChat, domain.CapToolUse}},
		},
	}
	providerRepo := &mockProviderRepo{
		providers: map[string]*domain.Provider{
			"p1": {
				ID: "p1", Name: "Test Qwen", Kind: domain.ProviderOpenAICompat,
				BaseURL: "https://api.test", APIKey: "sk-test", DefaultModel: "qwen-turbo", Enabled: true,
			},
		},
	}
	chatProtos := map[domain.ProviderKind]infrastructure.ChatProtocol{
		domain.ProviderOpenAICompat: errChatProto{},
	}
	embedProtos := map[domain.ProviderKind]infrastructure.EmbedProtocol{}
	reg := infrastructure.NewModelRegistry(modelRepo, providerRepo, chatProtos, embedProtos, 5*time.Minute)

	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")
	gateway := infrastructure.NewGateway(reg, chatProtos, embedProtos).WithLogger(zap.NewNop())
	_, err := gateway.CompleteStream(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"}, func(string) {})
	require.Error(t, err)

	for _, span := range recorder.Ended() {
		if span.Name() == "llm.complete" {
			require.Equal(t, codes.Error, span.Status().Code)
			return
		}
	}
	t.Fatal("llm.complete span not found")
}

func TestGatewayLLMLogsExcludePromptToolAndResponsePayloads(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)

	modelRepo := &mockModelRepo{
		models: []domain.Model{
			{ID: "m1", ProviderID: "p1", Name: "qwen-turbo", Enabled: true,
				Capabilities: []domain.ModelCapability{domain.CapChat, domain.CapToolUse}},
		},
	}
	providerRepo := &mockProviderRepo{
		providers: map[string]*domain.Provider{
			"p1": {
				ID: "p1", Name: "Test Qwen", Kind: domain.ProviderOpenAICompat,
				BaseURL: "https://api.test", APIKey: "sk-test", DefaultModel: "qwen-turbo", Enabled: true,
			},
		},
	}
	chatProtos := map[domain.ProviderKind]infrastructure.ChatProtocol{
		domain.ProviderOpenAICompat: successChatProto{},
	}
	embedProtos := map[domain.ProviderKind]infrastructure.EmbedProtocol{}
	reg := infrastructure.NewModelRegistry(modelRepo, providerRepo, chatProtos, embedProtos, 5*time.Minute)

	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")
	gateway := infrastructure.NewGateway(reg, chatProtos, embedProtos).WithLogger(zap.New(core))

	_, err := gateway.CompleteStream(ctx, &infrastructure.CompletionRequest{
		Model:    "qwen-turbo",
		Messages: []infrastructure.Message{{Role: "user", Content: "private prompt"}},
		Tools:    []infrastructure.Tool{{Type: "function", Function: infrastructure.ToolFunction{Name: "private_tool"}}},
	}, func(string) {})
	require.NoError(t, err)

	for _, entry := range logs.All() {
		if entry.Message != "llm.request" && entry.Message != "llm.complete" && entry.Message != "llm.response" {
			continue
		}
		for _, field := range entry.Context {
			if field.Key == "messages" || field.Key == "tools" || field.Key == "output" {
				t.Fatalf("%s log contained sensitive payload field %q", entry.Message, field.Key)
			}
		}
	}
}

func TestQwenComplete_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"model": "qwen-turbo",
			"choices": [{
				"finish_reason": "tool_calls",
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [{
						"id": "call_001",
						"type": "function",
						"function": {"name": "get_weather", "arguments": "{\"city\":\"Beijing\"}"}
					}]
				}
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`)
	}))
	defer srv.Close()

	client := infrastructure.NewQwenClientWithBase("test-key", srv.URL, zap.NewNop())
	resp, err := client.Complete(context.Background(), &infrastructure.CompletionRequest{
		Model:    "qwen-turbo",
		Messages: []infrastructure.Message{{Role: "user", Content: "weather?"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	require.Equal(t, "call_001", resp.ToolCalls[0].ID)
	require.Equal(t, "get_weather", resp.ToolCalls[0].Function.Name)
	require.Equal(t, `{"city":"Beijing"}`, resp.ToolCalls[0].Function.Arguments)
	require.Empty(t, resp.Content)
}

func TestOpenAICompatProtocolUsesResolvedProviderConfig(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer tenant-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"qwen-max","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	template := infrastructure.NewOpenAICompatClient(infrastructure.ProviderConfig{Name: "template"}, zap.NewNop())
	protocol := infrastructure.NewOpenAICompatProtocol(template)
	resp, err := protocol.Complete(context.Background(), infrastructure.ProviderConfig{
		Name: "tenant-provider", BaseURL: srv.URL, APIKey: "tenant-key",
	}, &infrastructure.CompletionRequest{
		Model: "qwen-max", Messages: []infrastructure.Message{{Role: "user", Content: "hello"}},
	})

	require.NoError(t, err)
	require.Equal(t, "ok", resp.Content)
}

func TestOpenAICompatProtocolListsProviderModels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want []infrastructure.DiscoveredModel
	}{
		{name: "returns discovered model IDs", body: `{"data":[{"id":"mock-model-1"},{"id":"mock-model-2"}]}`,
			want: []infrastructure.DiscoveredModel{{Name: "mock-model-1"}, {Name: "mock-model-2"}}},
		{name: "returns an empty slice when provider has no models", body: `{"data":[]}`, want: []infrastructure.DiscoveredModel{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/models", r.URL.Path)
				require.Equal(t, "Bearer tenant-key", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			template := infrastructure.NewOpenAICompatClient(infrastructure.ProviderConfig{Name: "template"}, zap.NewNop())
			protocol := infrastructure.NewOpenAICompatProtocol(template)
			models, err := protocol.ListModels(context.Background(), infrastructure.ProviderConfig{
				Name: "tenant-provider", BaseURL: srv.URL, APIKey: "tenant-key",
			})

			require.NoError(t, err)
			require.Equal(t, tc.want, models)
		})
	}
}

// zeroUsageChatProto succeeds but reports zero token usage.
type zeroUsageChatProto struct{}

func (zeroUsageChatProto) Complete(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest) (*infrastructure.CompletionResponse, error) {
	return &infrastructure.CompletionResponse{Content: "ok", Usage: infrastructure.TokenUsage{}}, nil
}
func (zeroUsageChatProto) CompleteStream(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest, onToken func(string)) (*infrastructure.CompletionResponse, error) {
	onToken("test")
	return &infrastructure.CompletionResponse{Content: "test", Usage: infrastructure.TokenUsage{}}, nil
}
func (zeroUsageChatProto) Health(ctx context.Context, cfg infrastructure.ProviderConfig) error {
	return nil
}
func (zeroUsageChatProto) ListModels(ctx context.Context, cfg infrastructure.ProviderConfig) ([]infrastructure.DiscoveredModel, error) {
	return nil, nil
}

// multiTokenChatProto emits two tokens so TTFT should be recorded exactly once.
type multiTokenChatProto struct{}

func (multiTokenChatProto) Complete(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest) (*infrastructure.CompletionResponse, error) {
	return &infrastructure.CompletionResponse{Content: "ok"}, nil
}
func (multiTokenChatProto) CompleteStream(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.CompletionRequest, onToken func(string)) (*infrastructure.CompletionResponse, error) {
	onToken("a")
	onToken("b")
	return &infrastructure.CompletionResponse{Content: "ab"}, nil
}
func (multiTokenChatProto) Health(ctx context.Context, cfg infrastructure.ProviderConfig) error {
	return nil
}
func (multiTokenChatProto) ListModels(ctx context.Context, cfg infrastructure.ProviderConfig) ([]infrastructure.DiscoveredModel, error) {
	return nil, nil
}

// successEmbedProto returns a fixed embedding vector.
type successEmbedProto struct{}

func (successEmbedProto) CreateEmbeddings(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.EmbeddingRequest) (*infrastructure.EmbeddingResponse, error) {
	return &infrastructure.EmbeddingResponse{Embeddings: [][]float32{{0.1, 0.2}}}, nil
}
func (successEmbedProto) BatchSize() int { return 8 }

// errEmbedProto fails all embedding calls.
type errEmbedProto struct{}

func (errEmbedProto) CreateEmbeddings(ctx context.Context, cfg infrastructure.ProviderConfig, req *infrastructure.EmbeddingRequest) (*infrastructure.EmbeddingResponse, error) {
	return nil, errors.New("embedding provider error")
}
func (errEmbedProto) BatchSize() int { return 8 }

// llmMetricsSpy embeds NoopMetrics and records LLM metric calls.
type llmMetricsSpy struct {
	observability.NoopMetrics
	requests         []string // model|provider|status
	resolutionErrors []string // model|reason
	durations        int
	tokenUsage       []string // model|tokenType|count
	tokenHistogram   []string // model|tokenType|count (histogram)
	ttft             int
}

func (s *llmMetricsSpy) IncLLMRequest(model, provider, status string) {
	s.requests = append(s.requests, model+"|"+provider+"|"+status)
}
func (s *llmMetricsSpy) IncLLMModelResolutionError(model, reason string) {
	s.resolutionErrors = append(s.resolutionErrors, model+"|"+reason)
}
func (s *llmMetricsSpy) RecordLLMRequestDuration(model, provider string, duration float64) {
	s.durations++
}
func (s *llmMetricsSpy) IncLLMTokenUsage(model, tokenType string, count int64) {
	s.tokenUsage = append(s.tokenUsage, fmt.Sprintf("%s|%s|%d", model, tokenType, count))
}
func (s *llmMetricsSpy) RecordLLMTokenHistogram(model, tokenType string, count float64) {
	s.tokenHistogram = append(s.tokenHistogram, fmt.Sprintf("%s|%s|%.1f", model, tokenType, count))
}
func (s *llmMetricsSpy) RecordLLMFirstTokenLatency(model, provider string, latency float64) {
	s.ttft++
}

// nonGatewayCompleter satisfies domain.LLMCompleter but is not a *Gateway.
type nonGatewayCompleter struct{}

func (nonGatewayCompleter) Complete(ctx context.Context, req *infrastructure.CompletionRequest) (*infrastructure.CompletionResponse, error) {
	return nil, nil
}
func (nonGatewayCompleter) CompleteStream(ctx context.Context, req *infrastructure.CompletionRequest, onToken func(string)) (*infrastructure.CompletionResponse, error) {
	return nil, nil
}

// gatewayFixture wires a registry with one enabled chat model ("qwen-turbo") and
// one embedding model ("text-embed") so Gateway methods resolve successfully.
func gatewayFixture(chat infrastructure.ChatProtocol, embed infrastructure.EmbedProtocol) (*infrastructure.Gateway, *llmMetricsSpy, *mockModelRepo) {
	modelRepo := &mockModelRepo{
		models: []domain.Model{
			{ID: "m1", ProviderID: "p1", Name: "qwen-turbo", Enabled: true,
				ContextWindow: 32768, MaxTokens: 8192,
				Capabilities: []domain.ModelCapability{domain.CapChat, domain.CapToolUse}},
			{ID: "m2", ProviderID: "p2", Name: "text-embed", Enabled: true,
				Capabilities: []domain.ModelCapability{domain.CapEmbedding}},
		},
	}
	providerRepo := &mockProviderRepo{
		providers: map[string]*domain.Provider{
			"p1": {ID: "p1", Name: "Test Qwen", Kind: domain.ProviderOpenAICompat,
				BaseURL: "https://api.test", APIKey: "sk-test", DefaultModel: "qwen-turbo", Enabled: true},
			"p2": {ID: "p2", Name: "Test Embed", Kind: domain.ProviderOpenAICompat,
				BaseURL: "https://api.test", APIKey: "sk-test", DefaultModel: "text-embed", Enabled: true},
		},
	}
	chatProtos := map[domain.ProviderKind]infrastructure.ChatProtocol{domain.ProviderOpenAICompat: chat}
	embedProtos := map[domain.ProviderKind]infrastructure.EmbedProtocol{domain.ProviderOpenAICompat: embed}
	reg := infrastructure.NewModelRegistry(modelRepo, providerRepo, chatProtos, embedProtos, 5*time.Minute)
	spy := &llmMetricsSpy{}
	return infrastructure.NewGateway(reg, chatProtos, embedProtos).WithMetrics(spy), spy, modelRepo
}

// gatewayFixtureEmpty 构造空模型/空 provider 目录，用于解析链 ⑤ fail-closed
// 路径（显式 model 未命中且 ②③④ 均无兜底）。
func gatewayFixtureEmpty() (*infrastructure.Gateway, *llmMetricsSpy, *mockModelRepo) {
	chatProtos := map[domain.ProviderKind]infrastructure.ChatProtocol{domain.ProviderOpenAICompat: &successChatProto{}}
	embedProtos := map[domain.ProviderKind]infrastructure.EmbedProtocol{domain.ProviderOpenAICompat: &successEmbedProto{}}
	modelRepo := &mockModelRepo{}
	reg := infrastructure.NewModelRegistry(modelRepo, &mockProviderRepo{}, chatProtos, embedProtos, 5*time.Minute)
	spy := &llmMetricsSpy{}
	return infrastructure.NewGateway(reg, chatProtos, embedProtos).WithMetrics(spy), spy, modelRepo
}

func TestGatewayComplete_success(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	resp, err := gateway.Complete(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"})
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Content)
	require.Equal(t, 1, resp.Usage.PromptTokens)
}

func TestGatewayComplete_resolveFails(t *testing.T) {
	// 空目录：5 级解析链 ②③ 无兜底，⑤ fail-closed。
	gateway, _, _ := gatewayFixtureEmpty()
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	_, err := gateway.Complete(ctx, &infrastructure.CompletionRequest{Model: "nope"})
	require.ErrorContains(t, err, `resolve model "nope"`)
}

func TestGatewayComplete_providerErrorRecordsErrorStatus(t *testing.T) {
	gateway, spy, _ := gatewayFixture(errChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	_, err := gateway.Complete(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"})
	require.ErrorContains(t, err, "provider error")
	require.Equal(t, []string{"qwen-turbo|Test Qwen|error"}, spy.requests)
}

func TestGatewayComplete_recordsSuccessAndUsageMetrics(t *testing.T) {
	gateway, spy, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	resp, err := gateway.Complete(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, []string{"qwen-turbo|Test Qwen|success"}, spy.requests)
	require.Equal(t, 1, spy.durations)
	require.Equal(t, []string{"qwen-turbo|prompt|1", "qwen-turbo|completion|1"}, spy.tokenUsage)
	require.Equal(t, []string{"qwen-turbo|prompt|1.0", "qwen-turbo|completion|1.0"}, spy.tokenHistogram)
}

func TestGatewayComplete_skipsZeroTokenUsageMetrics(t *testing.T) {
	gateway, spy, _ := gatewayFixture(zeroUsageChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	resp, err := gateway.Complete(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Empty(t, spy.tokenUsage)
	require.Empty(t, spy.tokenHistogram)
	require.Equal(t, []string{"qwen-turbo|Test Qwen|success"}, spy.requests)
}

func TestGatewayCompleteStream_recordsSuccessUsageAndHistogram(t *testing.T) {
	gateway, spy, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	resp, err := gateway.CompleteStream(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"}, func(string) {})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, []string{"qwen-turbo|Test Qwen|success"}, spy.requests)
	require.Equal(t, 1, spy.durations)
	require.Equal(t, []string{"qwen-turbo|prompt|1", "qwen-turbo|completion|1"}, spy.tokenUsage)
	require.Equal(t, []string{"qwen-turbo|prompt|1.0", "qwen-turbo|completion|1.0"}, spy.tokenHistogram)
}

func TestGatewayCompleteStream_skipsZeroTokenUsageMetrics(t *testing.T) {
	gateway, spy, _ := gatewayFixture(zeroUsageChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	resp, err := gateway.CompleteStream(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"}, func(string) {})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Empty(t, spy.tokenUsage)
	require.Empty(t, spy.tokenHistogram)
	require.Equal(t, []string{"qwen-turbo|Test Qwen|success"}, spy.requests)
}

func TestGatewayCompleteStream_recordsTTFTOnce(t *testing.T) {
	gateway, spy, _ := gatewayFixture(multiTokenChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	resp, err := gateway.CompleteStream(ctx, &infrastructure.CompletionRequest{Model: "qwen-turbo"}, func(string) {})
	require.NoError(t, err)
	require.Equal(t, "ab", resp.Content)
	require.Equal(t, 1, spy.ttft)
}

func TestGatewayCompleteStream_resolveFails(t *testing.T) {
	// 空目录：5 级解析链 ②③ 无兜底，⑤ fail-closed。
	gateway, spy, _ := gatewayFixtureEmpty()
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	_, err := gateway.CompleteStream(ctx, &infrastructure.CompletionRequest{Model: "nope"}, func(string) {})
	require.ErrorContains(t, err, `resolve model "nope"`)
	require.Equal(t, []string{"nope|unknown|error"}, spy.requests)
}

func TestGatewayCreateEmbeddings_success(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	resp, err := gateway.CreateEmbeddings(ctx, &infrastructure.EmbeddingRequest{Model: "text-embed", Input: []string{"hello"}})
	require.NoError(t, err)
	require.Equal(t, [][]float32{{0.1, 0.2}}, resp.Embeddings)
}

func TestGatewayCreateEmbeddings_resolveFails(t *testing.T) {
	// 空目录：embedding 链 ④ 无可用模型，⑤ fail-closed。
	gateway, _, _ := gatewayFixtureEmpty()
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	_, err := gateway.CreateEmbeddings(ctx, &infrastructure.EmbeddingRequest{Model: "nope"})
	require.ErrorContains(t, err, `resolve embedding model "nope"`)
}

func TestGatewayCreateEmbeddings_providerError(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, errEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	_, err := gateway.CreateEmbeddings(ctx, &infrastructure.EmbeddingRequest{Model: "text-embed", Input: []string{"x"}})
	require.ErrorContains(t, err, "embedding provider error")
}

func TestGatewayHealth(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	require.NoError(t, gateway.Health(context.Background()))
}

func TestGatewayListModelsReturnsEmptySlices(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	require.Empty(t, gateway.ListChatModels())
	require.Empty(t, gateway.ListEmbeddingModels())
}

func TestGatewayListChatModelsByTenant(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	names, err := gateway.ListChatModelsByTenant(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"qwen-turbo"}, names)
}

func TestGatewayListEmbeddingModelsByTenant(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	names, err := gateway.ListEmbeddingModelsByTenant(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"text-embed"}, names)
}

func TestGatewayListModelsByTenant_repoFails(t *testing.T) {
	gateway, _, modelRepo := gatewayFixture(successChatProto{}, successEmbedProto{})
	modelRepo.err = errors.New("db down")
	ctx := reqctx.WithTenantID(context.Background(), "test-tenant")

	_, err := gateway.ListChatModelsByTenant(ctx)
	require.ErrorContains(t, err, "list models")
	_, err = gateway.ListEmbeddingModelsByTenant(ctx)
	require.ErrorContains(t, err, "list models")
}

func TestGatewayWithMetricsReturnsGateway(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	require.Same(t, gateway, gateway.WithMetrics(&llmMetricsSpy{}))
}

func TestGatewayWithLoggerReturnsGateway(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})
	require.Same(t, gateway, gateway.WithLogger(zap.NewNop()))
}

func TestWithGatewayAndGatewayFromContext_roundTrip(t *testing.T) {
	gateway, _, _ := gatewayFixture(successChatProto{}, successEmbedProto{})

	ctx := infrastructure.WithGateway(context.Background(), gateway)
	got, ok := infrastructure.GatewayFromContext(ctx)
	require.True(t, ok)
	require.Same(t, gateway, got)
}

func TestGatewayFromContext_wrongType(t *testing.T) {
	ctx := domain.WithCompleter(context.Background(), nonGatewayCompleter{})
	got, ok := infrastructure.GatewayFromContext(ctx)
	require.False(t, ok)
	require.Nil(t, got)
}

func TestZhipuComplete_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"model": "glm-4-flash",
			"choices": [{
				"finish_reason": "tool_calls",
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [{
						"id": "call_002",
						"type": "function",
						"function": {"name": "search", "arguments": "{\"query\":\"Go Temporal\"}"}
					}]
				}
			}],
			"usage": {"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12}
		}`)
	}))
	defer srv.Close()

	client := infrastructure.NewZhipuClientWithBase("test-key", srv.URL, zap.NewNop())
	resp, err := client.Complete(context.Background(), &infrastructure.CompletionRequest{
		Model:    "glm-4-flash",
		Messages: []infrastructure.Message{{Role: "user", Content: "search?"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	require.Equal(t, "call_002", resp.ToolCalls[0].ID)
	require.Equal(t, "search", resp.ToolCalls[0].Function.Name)
	require.Equal(t, `{"query":"Go Temporal"}`, resp.ToolCalls[0].Function.Arguments)
	require.Empty(t, resp.Content)
}
