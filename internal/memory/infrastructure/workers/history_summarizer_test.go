package workers_test

import (
	"context"
	"errors"
	"testing"

	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/workers"
	"github.com/stretchr/testify/require"
)

func TestResolvingHistoryProcessorResolvesForSummarizeAndCompress(t *testing.T) {
	resolved := 0
	resolver := func(context.Context, string) (workers.TenantLLMClient, error) {
		resolved++
		label := "summary-a"
		if resolved == 2 {
			label = "summary-b"
		}
		return completionClientFunc(func(context.Context, *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
			return &llmdomain.CompletionResponse{Content: label}, nil
		}), nil
	}
	processor := newResolvingTestHistorySummarizer("tenant-1", resolver)

	summary, err := processor.SummarizeHistory(context.Background(), []string{"one"})
	require.NoError(t, err)
	require.Equal(t, "summary-a", summary)
	compressed, err := processor.CompressHistory(context.Background(), []string{"two"})
	require.NoError(t, err)
	require.Equal(t, "summary-b", compressed)
	require.Equal(t, 2, resolved)
}

func TestResolvingHistoryProcessorRecoversWithoutReusingOldClient(t *testing.T) {
	available := false
	calls := 0
	resolver := func(context.Context, string) (workers.TenantLLMClient, error) {
		if !available {
			return nil, errors.New("temporarily unavailable")
		}
		return completionClientFunc(func(context.Context, *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
			calls++
			return &llmdomain.CompletionResponse{Content: "recovered"}, nil
		}), nil
	}
	processor := newResolvingTestHistorySummarizer("tenant-1", resolver)

	_, err := processor.SummarizeHistory(context.Background(), []string{"one"})
	require.ErrorContains(t, err, "resolve tenant llm")
	require.Zero(t, calls)
	available = true
	summary, err := processor.SummarizeHistory(context.Background(), []string{"one"})
	require.NoError(t, err)
	require.Equal(t, "recovered", summary)
	require.Equal(t, 1, calls)
}

// TestHistoryProcessorFailsClosedWithoutModel 验证未装配参数服务（nil resolver，
// history_summary_model 空）即失败：*modelconfig.Err 且 State==missing，fail-closed，
// 绝不回落 llmgateway 默认。
func TestHistoryProcessorFailsClosedWithoutModel(t *testing.T) {
	client := completionClientFunc(func(_ context.Context, _ *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		return &llmdomain.CompletionResponse{Content: "s"}, nil
	})
	processor := workers.NewLLMHistorySummarizer(client)

	_, err := processor.SummarizeHistory(context.Background(), []string{"item"})
	ce, ok := modelconfig.AsConfigError(err)
	require.True(t, ok, "want *modelconfig.Err, got %v", err)
	require.Equal(t, modelconfig.KeyHistorySummaryModel, ce.Key)
	require.Equal(t, modelconfig.StateMissing, ce.State)
}

// TestHistoryProcessorFailsClosedWithoutPrompt 验证显式配置了 history_summary_model
// 但未配置 history_summary_prompt 即失败（fail-closed，无内置前缀兜底）。
func TestHistoryProcessorFailsClosedWithoutPrompt(t *testing.T) {
	client := completionClientFunc(func(_ context.Context, _ *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		return &llmdomain.CompletionResponse{Content: "s"}, nil
	})
	processor := workers.NewLLMHistorySummarizer(client).WithParamResolver(fakePlatformParamResolver{vals: map[string]any{
		"memory.history_summary_model": testModel,
	}})

	_, err := processor.SummarizeHistory(context.Background(), []string{"item"})
	require.ErrorContains(t, err, "memory.history_summary_prompt not configured")
}

// TestHistoryProcessorUsesConfiguredModel 验证总结请求 Model = 平台参数显式配置的
// history_summary_model（空值回落已废除：模型缺失时 fail-closed，不会以空串放行）。
func TestHistoryProcessorUsesConfiguredModel(t *testing.T) {
	var gotModel string
	client := completionClientFunc(func(_ context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		gotModel = req.Model
		return &llmdomain.CompletionResponse{Content: "s"}, nil
	})
	processor := newTestHistorySummarizer(client)

	if _, err := processor.SummarizeHistory(context.Background(), []string{"item"}); err != nil {
		t.Fatal(err)
	}
	if gotModel != testModel {
		t.Fatalf("expected model %q propagated to request, got %q", testModel, gotModel)
	}
}

// TestHistoryProcessorRoundsPlatformTemperature 回归 PR #441 漏网覆盖点：
// memory.history_summary_temperature 平台配置必须经 PlatformTemperaturePtr
// 舍入 2 位小数，float64(float32(0.2)) 直转会变成 0.20000000298023224 触发
// 智谱 400。
func TestHistoryProcessorRoundsPlatformTemperature(t *testing.T) {
	var got *float64
	client := completionClientFunc(func(_ context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		got = req.Temperature
		return &llmdomain.CompletionResponse{Content: "s"}, nil
	})
	processor := workers.NewLLMHistorySummarizer(client).WithParamResolver(fakePlatformParamResolver{vals: map[string]any{
		"memory.history_summary_prompt":      testHistorySummaryPrompt,
		"memory.history_summary_model":       testModel,
		"memory.history_summary_temperature": float64(0.2),
	}})

	if _, err := processor.SummarizeHistory(context.Background(), []string{"item"}); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != 0.2 {
		t.Fatalf("platform summary temperature = %v, want 0.2 (2 位小数)", got)
	}
}

// TestHistoryProcessorZeroTemperatureStaysUnset 平台温度 0 = 保留默认
// （nil → 网关采样注入层生效），不能覆盖成显式 0。
func TestHistoryProcessorZeroTemperatureStaysUnset(t *testing.T) {
	var got *float64
	client := completionClientFunc(func(_ context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
		got = req.Temperature
		return &llmdomain.CompletionResponse{Content: "s"}, nil
	})
	processor := workers.NewLLMHistorySummarizer(client).WithParamResolver(fakePlatformParamResolver{vals: map[string]any{
		"memory.history_summary_prompt":      testHistorySummaryPrompt,
		"memory.history_summary_model":       testModel,
		"memory.history_summary_temperature": float64(0),
	}})

	if _, err := processor.SummarizeHistory(context.Background(), []string{"item"}); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("zero platform temperature must keep unset (nil), got %v", got)
	}
}
