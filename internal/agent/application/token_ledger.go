package application

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
)

// TokenLedger 聚合 agent 侧的 token 估算与成本计算。
// Record 只做 cost 计算 + span 属性 + 日志，不再打 Prometheus usage：C1 计数去重
// （spec §1.3 C1 / §11 D2①），llm_token_usage_total/llm_token_count 由网关出站
// 唯一记账（gateway invokeComplete/invokeStream）。span 属性是 agent 侧执行证据，
// Opik re-pull 依赖，保留。
type TokenLedger struct {
	logger *zap.Logger
}

func NewTokenLedger(logger *zap.Logger) *TokenLedger {
	return &TokenLedger{logger: logger}
}

// UsageSummary 封装单次 LLM 调用的 token + 成本。
type UsageSummary struct {
	Prompt     int
	Completion int
	Total      int
	CostUSD    float64
}

// Record 在每次 LLM 调用返回后调用，完成成本计算、OTEL span 标注、zap 日志。
// 返回 (total tokens, cost USD)。
func (l *TokenLedger) Record(ctx context.Context, model string, usage port.TokenUsage) (int, float64) {
	cost := tokenutil.CostUSD(usage.Prompt, usage.Completion, model)

	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(
			attribute.Int("llm.prompt_tokens", usage.Prompt),
			attribute.Int("llm.completion_tokens", usage.Completion),
			attribute.Float64("llm.cost_usd", cost),
		)
	}

	if l.logger != nil {
		l.logger.Debug("token.record",
			zap.String("model", model),
			zap.Int("prompt_tokens", usage.Prompt),
			zap.Int("completion_tokens", usage.Completion),
			zap.Int("total_tokens", usage.Total),
			zap.Float64("cost_usd", cost),
		)
	}

	return usage.Total, cost
}

// Estimate 估算消息列表 token 数，统一使用 tokenutil 算法。
func (l *TokenLedger) Estimate(msgs []port.LLMMessage) int {
	total := 0
	for _, m := range msgs {
		total += tokenutil.EstimateText(m.Role) + tokenutil.EstimateText(m.Content) + 4
	}
	return total
}
