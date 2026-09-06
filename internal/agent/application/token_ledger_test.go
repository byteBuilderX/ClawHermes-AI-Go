package application

import (
	"context"
	"testing"

	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}

// TestTokenLedger_Record 固定 C1 去重后的核心契约：返回真实成本、span 写出
// token/cost 属性（agent 侧执行证据，Opik re-pull 依赖）。Prometheus usage 打点
// 已移出 ledger —— 由网关出站唯一记账（spec §11 D2①）。
func TestTokenLedger_Record(t *testing.T) {
	const (
		model      = "qwen-turbo"
		prompt     = 100
		completion = 50
		total      = 150
	)
	recorder := newSpanRecorder(t)
	l := NewTokenLedger(nil) // nil logger：debug 日志路径不炸

	spanCtx, span := otel.Tracer("test").Start(context.Background(), "llm-call")
	gotTotal, gotCost := l.Record(spanCtx, model, port.TokenUsage{Prompt: prompt, Completion: completion, Total: total})
	span.End()

	require.Equal(t, total, gotTotal)
	wantCost := tokenutil.CostUSD(prompt, completion, model)
	require.InDelta(t, wantCost, gotCost, 1e-9)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()
	require.Contains(t, attrs, attribute.Int("llm.prompt_tokens", prompt))
	require.Contains(t, attrs, attribute.Int("llm.completion_tokens", completion))
	require.Contains(t, attrs, attribute.Float64("llm.cost_usd", wantCost))
}

// TestTokenLedger_Record_nilDeps 覆盖 logger/span 全 nil 的构建路径，不得 panic。
func TestTokenLedger_Record_nilDeps(t *testing.T) {
	l := NewTokenLedger(nil)
	total, cost := l.Record(context.Background(), "unknown-model", port.TokenUsage{Prompt: 10, Completion: 5, Total: 15})
	require.Equal(t, 15, total)
	require.Equal(t, 0.0, cost) // 未知名模型无定价 → 0
}

// TestTokenLedger_Estimate 固定估算算法与 tokenutil 一致。
func TestTokenLedger_Estimate(t *testing.T) {
	l := NewTokenLedger(nil)
	msgs := []port.LLMMessage{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi"},
	}
	want := 0
	for _, m := range msgs {
		want += tokenutil.EstimateText(m.Role) + tokenutil.EstimateText(m.Content) + 4
	}
	require.Equal(t, want, l.Estimate(msgs))
}
