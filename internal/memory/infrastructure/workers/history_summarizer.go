package workers

import (
	"context"
	"fmt"
	"strings"

	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

type historyLLM = TenantLLMClient

type LLMHistorySummarizer struct {
	llm           historyLLM
	tenantID      string
	resolver      TenantLLMResolver
	paramResolver memport.PlatformParamResolver
}

var _ HistorySummarizer = (*LLMHistorySummarizer)(nil)
var _ HistoryCompressor = (*LLMHistorySummarizer)(nil)

func NewLLMHistorySummarizer(llm historyLLM) *LLMHistorySummarizer {
	return &LLMHistorySummarizer{llm: llm}
}

// NewResolvingLLMHistorySummarizer resolves the tenant client for every operation.
func NewResolvingLLMHistorySummarizer(tenantID string, resolver TenantLLMResolver) *LLMHistorySummarizer {
	return &LLMHistorySummarizer{tenantID: tenantID, resolver: resolver}
}

// WithParamResolver sets the platform parameter resolver used to resolve
// per-call summary model/temperature/prompt. A nil resolver keeps the const
// defaults.
func (s *LLMHistorySummarizer) WithParamResolver(r memport.PlatformParamResolver) *LLMHistorySummarizer {
	s.paramResolver = r
	return s
}

// CheckModelConfig 预检 memory.history_summary_model 是否可解析（周期 worker
// RunOnce 顶部调用，防「模型缺失 → 无条件假 success」）。不查目录；disabled
// 判定由探针负责。错误为 *modelconfig.Err。
func (s *LLMHistorySummarizer) CheckModelConfig(ctx context.Context) error {
	_, err := modelconfig.ResolveChatModel(ctx, s.paramResolver, modelconfig.KeyHistorySummaryModel)
	return err
}

func (s *LLMHistorySummarizer) SummarizeHistory(ctx context.Context, items []string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("history llm unavailable")
	}
	client := s.llm
	if s.resolver != nil {
		resolved, err := resolveTenantLLM(ctx, s.tenantID, s.resolver)
		if err != nil {
			return "", err
		}
		client = resolved
	}
	if client == nil {
		return "", fmt.Errorf("history llm unavailable")
	}
	// 总结模型为必需平台参数（fail-closed）：未显式配置或解析失败即返回
	// *modelconfig.Err，禁止空模型回落 llmgateway 默认。
	model, err := modelconfig.ResolveChatModel(ctx, s.paramResolver, modelconfig.KeyHistorySummaryModel)
	if err != nil {
		logModelConfigError(nil, "history_summary", err)
		return "", err
	}
	temperature := resolvePlatformFloat(ctx, s.paramResolver, "memory.history_summary_temperature", constants.TaskSummarizeTemperature)
	prompt := resolvePlatformString(ctx, s.paramResolver, "memory.history_summary_prompt", "")
	if strings.TrimSpace(prompt) == "" {
		// fail-closed：无显式配置不允许空提示词调用总结模型。
		return "", fmt.Errorf("memory history summary: memory.history_summary_prompt not configured (fail-closed)")
	}
	req := llmdomain.NewSummarizeRequest(model, prompt, items, 0)
	// NewSummarizeRequest 内部固定 TaskSummarizeTemperature；平台配置的温度
	// 在构造后覆盖（0 = 保留默认）。必须经 PlatformTemperaturePtr 舍入 2 位，
	// float64(float32) 直转会把 0.1 放大成 0.10000000149011612，触发智谱 400。
	req.Temperature = llmdomain.PlatformTemperaturePtr(temperature)
	resp, err := client.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (s *LLMHistorySummarizer) CompressHistory(ctx context.Context, items []string) (string, error) {
	return s.SummarizeHistory(ctx, items)
}
