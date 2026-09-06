package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

type LLMExtractor struct {
	client   LLMClient
	resolver memport.PlatformParamResolver
	// tenantID is captured at construction (the extractor is built per tenant
	// by the wiring seam); agentID arrives per ExtractFacts call.
	tenantID string
	logger   *zap.Logger
}

func NewLLMExtractor(client LLMClient) *LLMExtractor {
	return &LLMExtractor{client: client}
}

// SetPlatformParamResolver wires the platform parameter resolver
// (registry-backed); nil keeps the pkg/constants defaults. 记忆相关配置统一
// 平台级，不与 agent 绑定。
func (e *LLMExtractor) SetPlatformParamResolver(r memport.PlatformParamResolver) { e.resolver = r }

// SetTenantID sets the tenant identity for parameter resolution. The extractor
// is constructed per tenant by the wiring seam, so the tenant is stable for
// the extractor's lifetime.
func (e *LLMExtractor) SetTenantID(t string) { e.tenantID = t }

// WithLogger 注入降级日志记录器（结构化失败白名单摘要）。nil 安全。
func (e *LLMExtractor) WithLogger(l *zap.Logger) *LLMExtractor {
	e.logger = l
	return e
}

// maxFacts resolves memory.max_facts_per_extraction（平台级），
// falling back to the constant default when unset, unresolved or unavailable.
func (e *LLMExtractor) maxFacts(ctx context.Context) int {
	if e.resolver == nil {
		return constants.MemoryMaxFactsPerExtraction
	}
	v, ok, err := e.resolver.ResolvePlatform(ctx, "memory.max_facts_per_extraction")
	if err != nil || !ok {
		return constants.MemoryMaxFactsPerExtraction
	}
	return coerceResourceInt(v, constants.MemoryMaxFactsPerExtraction)
}

// extractionPrompt 渲染抽取 system prompt：memory.extraction_prompt（resource
// scope）承载**完整**系统提示词，代码渲染 {user_id}/{agent_id}/{max_facts}
// 占位符；未配置/空 → 错误（fail-closed，无任何内置模板兜底）。JSON 输出契约
// 由 parseExtractedFacts/Validate 强制，不依赖提示词文本。
func (e *LLMExtractor) extractionPrompt(ctx context.Context, agentID, userID string, maxFacts int) (string, error) {
	if e.resolver == nil {
		return "", fmt.Errorf("memory extraction: memory.extraction_prompt not configured (fail-closed)")
	}
	v, _, err := e.resolver.ResolvePlatform(ctx, "memory.extraction_prompt")
	if err != nil {
		return "", fmt.Errorf("memory extraction: resolve prompt: %w", err)
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("memory extraction: memory.extraction_prompt not configured (fail-closed)")
	}
	s = strings.ReplaceAll(s, "{user_id}", userID)
	s = strings.ReplaceAll(s, "{agent_id}", agentID)
	s = strings.ReplaceAll(s, "{max_facts}", strconv.Itoa(maxFacts))
	return s, nil
}

func (e *LLMExtractor) ExtractFacts(ctx context.Context, userID, agentID string, message string) ([]*memport.ExtractedFact, error) {
	maxFacts := e.maxFacts(ctx)
	system, err := e.extractionPrompt(ctx, agentID, userID, maxFacts)
	if err != nil {
		return nil, err
	}
	// 抽取模型为必需平台参数（fail-closed）：未显式配置或解析失败即返回
	// *modelconfig.Err，禁止空模型静默回落 llmgateway 默认。
	model, err := modelconfig.ResolveChatModel(ctx, e.resolver, modelconfig.KeyExtractionModel)
	if err != nil {
		logConfigError(e.logger, modelconfig.KeyExtractionModel, "extraction", err)
		return nil, err
	}
	req := llmdomain.NewExtractRequest(model, system, message, 0, constants.MemoryExtractLLMMaxTokens)
	return extractFactsStructured(ctx, e.client, req, e.logger)
}

// extractFactsStructured 走 CompleteStructured 的带错重试管线，并实现部分成功
// 语义：逐条 Validate，≥1 条通过立即返回通过子集（不为小问题浪费重试）；
// 0 条通过才触发带错重试，耗尽返回 typed error（保留 MarkFailed/DLQ）。
func extractFactsStructured(
	ctx context.Context,
	client llmdomain.Completer,
	req *llmdomain.CompletionRequest,
	logger *zap.Logger,
) ([]*memport.ExtractedFact, error) {
	var valid []*memport.ExtractedFact
	_, err := CompleteStructured(ctx, client, req, parseExtractedFacts,
		func(facts []*memport.ExtractedFact) error {
			if len(facts) == 0 {
				// 模型明确表示无事实（[]）：合法结果，非校验失败，
				// 调用方据此跳过抽取，不触发带错重试。
				valid = nil
				return nil
			}
			valid = facts[:0]
			allInvalid := true
			for _, f := range facts {
				if f.Validate() == nil {
					valid = append(valid, f)
					allInvalid = false
				}
			}
			if allInvalid {
				return &memport.ValidationError{
					Location: "facts", FieldName: "facts",
					Reason: "no fact passed validation",
				}
			}
			return nil
		}, logger, "extract_facts")
	if err != nil {
		return nil, err
	}
	return valid, nil
}

// parseExtractedFacts 从 LLM 原始输出中剥离非 JSON 前缀并解析事实数组。
// Token 截断时按最后完整对象恢复（recoverTruncatedArray）。
func parseExtractedFacts(raw string) ([]*memport.ExtractedFact, error) {
	start := strings.Index(raw, "[")
	if start == -1 {
		return nil, fmt.Errorf("parse extracted facts: no JSON array in response")
	}
	body := raw[start:]
	var facts []*memport.ExtractedFact
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&facts); err != nil {
		// Token limit may have truncated the JSON mid-object; recover by closing at the last complete item.
		if recovered := recoverTruncatedArray(body); recovered != "" {
			var recoveredFacts []*memport.ExtractedFact
			if err2 := json.Unmarshal([]byte(recovered), &recoveredFacts); err2 == nil {
				return recoveredFacts, nil
			}
		}
		return nil, fmt.Errorf("parse extracted facts: %w", err)
	}
	return facts, nil
}

// recoverTruncatedArray finds the last complete JSON object in a truncated array and closes it.
func recoverTruncatedArray(s string) string {
	last := strings.LastIndex(s, "},")
	if last == -1 {
		last = strings.LastIndex(s, "}")
	} else {
		last++ // include the }
	}
	if last == -1 {
		return ""
	}
	return s[:last+1] + "]"
}
