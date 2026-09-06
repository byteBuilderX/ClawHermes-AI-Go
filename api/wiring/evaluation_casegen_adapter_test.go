package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	llmgatewaydomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/application"
	pdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/stretchr/testify/require"
)

type fakeCaseGenCompleter struct {
	response *llmgatewaydomain.CompletionResponse
	err      error
	got      *llmgatewaydomain.CompletionRequest
}

func (f *fakeCaseGenCompleter) Complete(_ context.Context, req *llmgatewaydomain.CompletionRequest) (*llmgatewaydomain.CompletionResponse, error) {
	f.got = req
	return f.response, f.err
}

func (f *fakeCaseGenCompleter) CompleteStream(context.Context, *llmgatewaydomain.CompletionRequest, func(string)) (*llmgatewaydomain.CompletionResponse, error) {
	return nil, errors.New("unused")
}

// staticStore is a read-only PlatformStore stub returning one platform value.
type staticStore struct {
	values map[string]json.RawMessage
}

func (s *staticStore) GetValue(_ context.Context, key string) (json.RawMessage, bool, error) {
	raw, ok := s.values[key]
	return raw, ok, nil
}

func (s *staticStore) SetValue(_ context.Context, _ string, _ json.RawMessage, _ string) error {
	return errors.New("unused")
}

func (s *staticStore) GetAll(_ context.Context) ([]port.PlatformValue, error) {
	var out []port.PlatformValue
	for k, v := range s.values {
		out = append(out, port.PlatformValue{Key: k, Value: v})
	}
	return out, nil
}

// GetSnapshot 按 GroupForKey 过滤出该组快照（staticStore 保持只读）。
func (s *staticStore) GetSnapshot(_ context.Context, groupKey string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage)
	for key, raw := range s.values {
		if pdomain.GroupForKey(key) == groupKey {
			out[key] = raw
		}
	}
	return out, nil
}

func (s *staticStore) CreateDraft(_ context.Context, _ string, _ map[string]json.RawMessage, _, _ string) (port.PlatformVersion, error) {
	return port.PlatformVersion{}, nil
}

func (s *staticStore) Publish(_ context.Context, _ string, _ int64, _ string) error { return nil }

func (s *staticStore) Rollback(_ context.Context, _ string, _ int64, _ string) error { return nil }

func (s *staticStore) ListVersions(_ context.Context, _ string) ([]port.PlatformVersion, error) {
	return []port.PlatformVersion{}, nil
}

// GetVersion/UpdateEvalState 补齐接口（分层门禁 P1）：staticStore 只读且无版本
// 历史，版本寻址恒 ErrVersionNotFound。
func (s *staticStore) GetVersion(context.Context, string, int64) (port.PlatformVersion, error) {
	return port.PlatformVersion{}, pdomain.ErrVersionNotFound
}

func (s *staticStore) UpdateEvalState(context.Context, string, int64, string, string) error {
	return pdomain.ErrVersionNotFound
}

func caseGenSample() evalport.CaseGenRequest {
	return evalport.CaseGenRequest{
		ResourceKind: domain.ResourceKindSkill,
		Sample: domain.CaseSample{
			Query: "快递没更新", Response: "为您查询物流进度", Score: float64Ptr(0.2),
			Outcome: map[string]any{"label": "bad"},
		},
	}
}

func float64Ptr(v float64) *float64 { return &v }

func TestParseCaseGenResponsePlainJSON(t *testing.T) {
	got, err := parseCaseGenResponse(
		`{"name":"物流查询","input":"快递没更新","expected_output":"为您查询物流进度","assertion_mode":"contains","reason":"负反馈样本"}`)
	require.NoError(t, err)
	require.True(t, got.Valid)
	require.Equal(t, "物流查询", got.Name)
	require.Equal(t, "快递没更新", got.Input)
	require.Equal(t, domain.AssertionContains, got.AssertionMode)
	require.Equal(t, "负反馈样本", got.GenerateReason)
}

func TestParseCaseGenResponseToleratesCodeFence(t *testing.T) {
	got, err := parseCaseGenResponse("```json\n{\"name\":\"x\",\"assertion_mode\":\"judge\",\"reason\":\"r\"}\n```")
	require.NoError(t, err)
	require.True(t, got.Valid)
	require.Equal(t, domain.AssertionJudge, got.AssertionMode)
}

func TestParseCaseGenResponseRejectsGarbage(t *testing.T) {
	_, err := parseCaseGenResponse("不是 JSON")
	require.ErrorContains(t, err, "parse generated case")
}

func TestCaseGenUserContentRendersSampleWithFeedback(t *testing.T) {
	content := caseGenUserContent(caseGenSample())
	require.Contains(t, content, "资源类型：skill")
	require.Contains(t, content, "用户查询：快递没更新")
	require.Contains(t, content, "实际回答：为您查询物流进度")
	require.Contains(t, content, "反馈分数：0.20")
	require.Contains(t, content, "反馈标签：{\"label\":\"bad\"}")
}

func TestCaseGenUserContentHandlesMissingScore(t *testing.T) {
	req := caseGenSample()
	req.Sample.Score = nil
	req.Sample.Outcome = nil
	content := caseGenUserContent(req)
	require.Contains(t, content, "反馈分数：无")
	require.NotContains(t, content, "反馈标签")
}

func TestCaseGenAdapterFailsClosedWithoutCompleter(t *testing.T) {
	adapter := casegenAdapter{}
	got, err := adapter.Generate(context.Background(), caseGenSample())
	require.NoError(t, err)
	require.False(t, got.Valid)
	require.Contains(t, got.Reason, "no LLM completer")
}

func TestCaseGenAdapterPropagatesCompleterError(t *testing.T) {
	completer := &fakeCaseGenCompleter{err: errors.New("provider timeout")}
	adapter := casegenAdapter{completer: completer}
	_, err := adapter.Generate(context.Background(), caseGenSample())
	require.ErrorContains(t, err, "case generator: provider timeout")
}

func TestCaseGenAdapterUsesDefaultsAndParsesResponse(t *testing.T) {
	completer := &fakeCaseGenCompleter{
		response: &llmgatewaydomain.CompletionResponse{
			Content: `{"name":"物流","input":"快递","expected_output":"结果","assertion_mode":"contains","reason":"来源"}`,
		},
	}
	adapter := casegenAdapter{completer: completer} // no params -> 空模型交由 llmgateway 解析

	got, err := adapter.Generate(context.Background(), caseGenSample())
	require.NoError(t, err)
	require.True(t, got.Valid)
	require.Equal(t, "物流", got.Name)

	require.Empty(t, completer.got.Model, "模型默认必须为空：交由 llmgateway 从模型目录解析，代码内不写死兜底模型")
	// float32(0.2) → *float64 有精度放大，按 epsilon 近似断言。
	require.NotNil(t, completer.got.Temperature)
	require.InDelta(t, 0.2, *completer.got.Temperature, 1e-6)
	require.Equal(t, constants.CaseGenMaxTokens, completer.got.MaxTokens)
	require.Contains(t, completer.got.Messages[0].Content, "评测用例生成器")
	require.Contains(t, completer.got.Messages[1].Content, "用户查询：快递没更新")
}

func TestCaseGenAdapterHonorsPlatformOptimizerModel(t *testing.T) {
	completer := &fakeCaseGenCompleter{
		response: &llmgatewaydomain.CompletionResponse{Content: `{"name":"x"}`},
	}
	params := application.NewService(pdomain.NewParametersRegistry(), &staticStore{
		values: map[string]json.RawMessage{"evaluation.optimizer.model": json.RawMessage(`"qwen-max"`)},
	})
	adapter := casegenAdapter{completer: completer, params: params}

	_, err := adapter.Generate(context.Background(), caseGenSample())
	require.NoError(t, err)
	require.Equal(t, "qwen-max", completer.got.Model)
}
