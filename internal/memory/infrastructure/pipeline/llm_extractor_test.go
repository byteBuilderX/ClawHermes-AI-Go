package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/pkg/constants"

	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
)

type extractorLLMStub struct {
	content string
	prompt  string
	model   string
	user    string
}

func (s *extractorLLMStub) Complete(_ context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
	s.prompt = req.Messages[0].Content
	s.model = req.Model
	if len(req.Messages) > 1 {
		s.user = req.Messages[len(req.Messages)-1].Content
	}
	return &llmdomain.CompletionResponse{Content: s.content}, nil
}

// keyedResolverStub 按 key 返回平台级配置值（记忆配置统一平台级）。
type keyedResolverStub struct {
	vals map[string]any // key → value
	err  error
}

func (s keyedResolverStub) ResolvePlatform(_ context.Context, key string) (any, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	v, ok := s.vals[key]
	return v, ok, nil
}

const testExtractionPrompt = "你是长期记忆提取助手，服务用户 {user_id}，供 AI 助手 {agent_id} 使用。最多提取 {max_facts} 条事实。只输出 JSON 数组。"

// newConfiguredExtractor 构造已配置完整抽取提示词 + 抽取模型（均属必需平台参数：
// memory.extraction_prompt / memory.extraction_model 必填，代码渲染占位符）。
func newConfiguredExtractor(llm llmdomain.Completer, extra map[string]any) *LLMExtractor {
	vals := map[string]any{
		"memory.extraction_prompt": testExtractionPrompt,
		"memory.extraction_model":  "test-model",
	}
	for k, v := range extra {
		vals[k] = v
	}
	e := NewLLMExtractor(llm)
	e.SetTenantID("t1")
	e.SetPlatformParamResolver(keyedResolverStub{vals: vals})
	return e
}

func TestLLMExtractorDecodesFactTypeAndExplicitZeroConfidence(t *testing.T) {
	llm := &extractorLLMStub{content: `[{"content":"uses Go","importance":0.8,"fact_type":"skill","confidence":0.0,"entities":["Go"]}]`}
	facts, err := newConfiguredExtractor(llm, nil).ExtractFacts(context.Background(), "user-1", "agent-1", "I use Go")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].FactType != "skill" || facts[0].Confidence == nil || *facts[0].Confidence != 0 {
		t.Fatalf("unexpected decoded fact: %#v", facts)
	}
	// 完整提示词按占位符渲染（user/agent/max_facts）。
	for _, want := range []string{"user-1", "agent-1", fmt.Sprintf("%d", constants.MemoryMaxFactsPerExtraction)} {
		if !strings.Contains(llm.prompt, want) {
			t.Fatalf("prompt placeholder %q not rendered: %q", want, llm.prompt)
		}
	}
}

// TestLLMExtractorFailsClosedWithoutPrompt 验证未配置 memory.extraction_prompt
// 即失败（fail-closed，无内置模板兜底）。
func TestLLMExtractorFailsClosedWithoutPrompt(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	extractor := NewLLMExtractor(llm)
	if _, err := extractor.ExtractFacts(context.Background(), "user-1", "agent-1", "msg"); err == nil {
		t.Fatal("missing memory.extraction_prompt must fail closed")
	}
}

// TestLLMExtractorFailsClosedWithoutModel 验证 memory.extraction_model 缺失即
// fail-closed：ExtractFacts 返回 *modelconfig.Err(state=missing)，请求绝不带空
// 模型下发（禁止回落 llmgateway 目录默认——原缺陷根因）。
func TestLLMExtractorFailsClosedWithoutModel(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	extractor := NewLLMExtractor(llm)
	extractor.SetTenantID("t1")
	extractor.SetPlatformParamResolver(keyedResolverStub{vals: map[string]any{
		"memory.extraction_prompt": testExtractionPrompt,
	}})
	_, err := extractor.ExtractFacts(context.Background(), "user-1", "agent-1", "msg")
	if err == nil {
		t.Fatal("missing memory.extraction_model must fail closed")
	}
	ce, ok := modelconfig.AsConfigError(err)
	if !ok {
		t.Fatalf("expected *modelconfig.Err, got %v", err)
	}
	if ce.State != modelconfig.StateMissing || ce.Key != modelconfig.KeyExtractionModel {
		t.Fatalf("state/key = %s/%s, want missing/%s", ce.State, ce.Key, modelconfig.KeyExtractionModel)
	}
	if llm.model != "" {
		t.Fatalf("no LLM call must be issued on config failure, got model %q", llm.model)
	}
}

// TestLLMExtractorMaxFactsPlatform 验证 memory.max_facts_per_extraction 平台级
// 解析并渲染进完整提示词；未命中回落常量默认。
func TestLLMExtractorMaxFactsPlatform(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	extractor := newConfiguredExtractor(llm, map[string]any{
		"memory.max_facts_per_extraction": float64(35),
	})
	if _, err := extractor.ExtractFacts(context.Background(), "user-1", "agent-1", "msg"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.prompt, "最多提取 35 条事实") {
		t.Fatalf("platform max_facts not applied: %q", llm.prompt)
	}
}

// TestLLMExtractorMaxFactsDegrade 验证 resolver 缺省时 maxFacts 回落常量默认。
func TestLLMExtractorMaxFactsDegrade(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	extractor := NewLLMExtractor(llm)
	extractor.SetTenantID("t1")
	extractor.SetPlatformParamResolver(keyedResolverStub{vals: map[string]any{
		"memory.extraction_prompt": testExtractionPrompt,
		"memory.extraction_model":  "test-model",
	}})
	if _, err := extractor.ExtractFacts(context.Background(), "user-1", "agent-1", "msg"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.prompt, fmt.Sprintf("最多提取 %d 条事实", constants.MemoryMaxFactsPerExtraction)) {
		t.Fatalf("default max_facts not applied: %q", llm.prompt)
	}
}

// TestLLMExtractorCustomExtractionPrompt 验证 memory.extraction_prompt 为完整
// 系统提示词并按占位符渲染；未配置 → fail-closed（无内置兜底）。
func TestLLMExtractorCustomExtractionPrompt(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	extractor := NewLLMExtractor(llm)
	extractor.SetTenantID("t1")
	custom := "记忆助手：用户 {user_id}，助手 {agent_id}，上限 {max_facts} 条。只输出 JSON。"
	extractor.SetPlatformParamResolver(keyedResolverStub{vals: map[string]any{
		"memory.extraction_prompt": custom,
		"memory.extraction_model":  "test-model",
	}})

	if _, err := extractor.ExtractFacts(context.Background(), "user-1", "agent-1", "msg"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"user-1", "agent-1", fmt.Sprintf("%d", constants.MemoryMaxFactsPerExtraction)} {
		if !strings.Contains(llm.prompt, want) {
			t.Fatalf("placeholder %q not rendered: %q", want, llm.prompt)
		}
	}
	if strings.Contains(llm.prompt, "长期记忆提取助手") && !strings.Contains(custom, "长期记忆提取助手") {
		t.Fatalf("custom prompt must be used verbatim (no builtin injected): %q", llm.prompt)
	}
}

// TestLLMExtractorCustomExtractionModel 验证显式配置的 memory.extraction_model
// 传入抽取请求（必需平台参数，非空才放行）。
func TestLLMExtractorCustomExtractionModel(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	extractor := NewLLMExtractor(llm)
	extractor.SetTenantID("t1")
	extractor.SetPlatformParamResolver(keyedResolverStub{vals: map[string]any{
		"memory.extraction_prompt": testExtractionPrompt,
		"memory.extraction_model":  "qwen-turbo",
	}})

	if _, err := extractor.ExtractFacts(context.Background(), "user-1", "agent-1", "msg"); err != nil {
		t.Fatal(err)
	}
	if llm.model != "qwen-turbo" {
		t.Fatalf("custom model not applied: got %q", llm.model)
	}
}

// TestLLMExtractorResolverErrorPropagates 验证解析失败（非未配置）传播错误，
// 不静默降级。
func TestLLMExtractorResolverErrorPropagates(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	extractor := NewLLMExtractor(llm)
	extractor.SetTenantID("t1")
	extractor.SetPlatformParamResolver(keyedResolverStub{err: errors.New("db down")})
	if _, err := extractor.ExtractFacts(context.Background(), "user-1", "agent-1", "msg"); err == nil || !strings.Contains(err.Error(), "resolve prompt") {
		t.Fatalf("resolver error must propagate, got %v", err)
	}
}
