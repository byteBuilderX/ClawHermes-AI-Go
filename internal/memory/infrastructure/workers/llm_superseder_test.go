package workers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/workers"
	"github.com/stretchr/testify/require"
)

type completionClientFunc func(context.Context, *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error)

func (f completionClientFunc) Complete(ctx context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
	return f(ctx, req)
}

func TestResolvingLLMSupersederUsesCurrentTenantClientOnEveryCall(t *testing.T) {
	var resolved, calledA, calledB int
	clientA := completionClientFunc(func(context.Context, *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		calledA++
		return &llmdomain.CompletionResponse{Content: `{"supersedes":false,"reason":"a"}`}, nil
	})
	clientB := completionClientFunc(func(context.Context, *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		calledB++
		return &llmdomain.CompletionResponse{Content: `{"supersedes":true,"reason":"b"}`}, nil
	})
	resolver := func(context.Context, string) (workers.TenantLLMClient, error) {
		resolved++
		if resolved == 1 {
			return clientA, nil
		}
		return clientB, nil
	}

	judge := newResolvingTestSuperseder("tenant-1", resolver)
	first, err := judge.JudgeSupersede(context.Background(), "old", "new")
	require.NoError(t, err)
	require.False(t, first.Supersedes)
	second, err := judge.JudgeSupersede(context.Background(), "old", "newer")
	require.NoError(t, err)
	require.True(t, second.Supersedes)
	require.Equal(t, 2, resolved)
	require.Equal(t, 1, calledA)
	require.Equal(t, 1, calledB)
}

func TestResolvingLLMSupersederRoutesThroughNewProviderGateway(t *testing.T) {
	qwenCalls, zhipuCalls := 0, 0
	completionServer := func(calls *int, supersedes bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			*calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"supersedes\":` + map[bool]string{true: "true", false: "false"}[supersedes] + `,\"reason\":\"provider\"}"}}],"model":"fake-model"}`))
		}))
	}
	qwenServer := completionServer(&qwenCalls, false)
	defer qwenServer.Close()
	zhipuServer := completionServer(&zhipuCalls, true)
	defer zhipuServer.Close()

	clientA := completionClientFunc(func(ctx context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		return callCompletionServer(ctx, qwenServer.URL, req)
	})
	clientB := completionClientFunc(func(ctx context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		return callCompletionServer(ctx, zhipuServer.URL, req)
	})
	resolved := 0
	judge := newResolvingTestSuperseder("tenant-1", func(context.Context, string) (workers.TenantLLMClient, error) {
		resolved++
		if resolved == 1 {
			return clientA, nil
		}
		return clientB, nil
	})

	first, err := judge.JudgeSupersede(context.Background(), "old", "new")
	require.NoError(t, err)
	require.False(t, first.Supersedes)
	second, err := judge.JudgeSupersede(context.Background(), "old", "newer")
	require.NoError(t, err)
	require.True(t, second.Supersedes)
	require.Equal(t, 1, qwenCalls)
	require.Equal(t, 1, zhipuCalls)
}

func callCompletionServer(ctx context.Context, baseURL string, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
	body := map[string]interface{}{
		"model":    req.Model,
		"messages": []map[string]string{},
	}
	for _, m := range req.Messages {
		body["messages"] = append(body["messages"].([]map[string]string), map[string]string{"role": m.Role, "content": m.Content})
	}
	b, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", bytes.NewReader(b))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer test-key")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &llmdomain.CompletionResponse{Content: result.Choices[0].Message.Content}, nil
}

func TestResolvingLLMSupersederDoesNotReuseClientAfterResolverFailure(t *testing.T) {
	available := true
	calls := 0
	client := completionClientFunc(func(context.Context, *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		calls++
		return &llmdomain.CompletionResponse{Content: `{"supersedes":false,"reason":"ok"}`}, nil
	})
	resolver := func(context.Context, string) (workers.TenantLLMClient, error) {
		if !available {
			return nil, errors.New("resolver unavailable")
		}
		return client, nil
	}
	judge := newResolvingTestSuperseder("tenant-1", resolver)

	_, err := judge.JudgeSupersede(context.Background(), "old", "new")
	require.NoError(t, err)
	available = false
	_, err = judge.JudgeSupersede(context.Background(), "old", "new")
	require.ErrorContains(t, err, "resolve tenant llm")
	require.Equal(t, 1, calls)
	available = true
	_, err = judge.JudgeSupersede(context.Background(), "old", "new")
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestResolvingLLMSupersederPropagatesContextCancellationBeforeClientCall(t *testing.T) {
	clientCalls := 0
	resolver := func(ctx context.Context, _ string) (workers.TenantLLMClient, error) {
		return nil, ctx.Err()
	}
	judge := newResolvingTestSuperseder("tenant-1", resolver)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := judge.JudgeSupersede(ctx, "old", "new")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, clientCalls)
}

// TestLLMSupersederFailsClosedWithoutModel 验证未装配参数服务（nil resolver，
// supersede_model 空）即失败：*modelconfig.Err 且 State==missing，fail-closed，
// 绝不回落 llmgateway 默认。
func TestLLMSupersederFailsClosedWithoutModel(t *testing.T) {
	client := completionClientFunc(func(_ context.Context, _ *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		return &llmdomain.CompletionResponse{Content: `{"supersedes":false,"reason":"ok"}`}, nil
	})
	judge := workers.NewLLMSuperseder(client)

	_, err := judge.JudgeSupersede(context.Background(), "old", "new")
	ce, ok := modelconfig.AsConfigError(err)
	require.True(t, ok, "want *modelconfig.Err, got %v", err)
	require.Equal(t, modelconfig.KeySupersedeModel, ce.Key)
	require.Equal(t, modelconfig.StateMissing, ce.State)
}

// TestLLMSupersederFailsClosedWithoutPrompt 验证显式配置了 supersede_model 但未配置
// supersede_prompt 即失败（fail-closed，无内置模板兜底）。
func TestLLMSupersederFailsClosedWithoutPrompt(t *testing.T) {
	client := completionClientFunc(func(_ context.Context, _ *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		return &llmdomain.CompletionResponse{Content: `{"supersedes":false,"reason":"ok"}`}, nil
	})
	judge := workers.NewLLMSuperseder(client).WithParamResolver(fakePlatformParamResolver{vals: map[string]any{
		"memory.supersede_model": testModel,
	}})

	_, err := judge.JudgeSupersede(context.Background(), "old", "new")
	require.ErrorContains(t, err, "memory.supersede_prompt not configured")
}

// TestLLMSupersederUsesConfiguredModel 验证判定请求 Model = 平台参数显式配置的
// supersede_model（空值回落已废除：模型缺失时 fail-closed，不会以空串放行）。
func TestLLMSupersederUsesConfiguredModel(t *testing.T) {
	var gotModel string
	client := completionClientFunc(func(_ context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		gotModel = req.Model
		return &llmdomain.CompletionResponse{Content: `{"supersedes":false,"reason":"ok"}`}, nil
	})
	judge := newTestSuperseder(client)

	if _, err := judge.JudgeSupersede(context.Background(), "old", "new"); err != nil {
		t.Fatal(err)
	}
	if gotModel != testModel {
		t.Fatalf("expected model %q propagated to request, got %q", testModel, gotModel)
	}
}

// TestLLMSupersederRetriesWithCorrectionOnInvalidJSON 验证判定解析失败时走
// 带错重试：第 2 次请求附加 system-role correction（错误位置丢回模型）。
func TestLLMSupersederRetriesWithCorrectionOnInvalidJSON(t *testing.T) {
	var requests [][]llmdomain.Message
	client := completionClientFunc(func(_ context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		requests = append(requests, append([]llmdomain.Message(nil), req.Messages...))
		if len(requests) == 1 {
			return &llmdomain.CompletionResponse{Content: "not json at all"}, nil
		}
		return &llmdomain.CompletionResponse{Content: `{"supersedes":true,"reason":"ok"}`}, nil
	})
	judge := newTestSuperseder(client)

	got, err := judge.JudgeSupersede(context.Background(), "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Supersedes {
		t.Fatalf("judgment = %+v, want supersedes true after correction retry", got)
	}
	if len(requests) != 2 {
		t.Fatalf("calls = %d, want 2 (initial + correction retry)", len(requests))
	}
	if len(requests[1]) != 2 || requests[1][1].Role != "system" {
		t.Fatalf("retry must append system-role correction, got %#v", requests[1])
	}
	if !strings.Contains(requests[1][1].Content, "{correction: ") {
		t.Fatalf("correction must carry error context, got %q", requests[1][1].Content)
	}
}
