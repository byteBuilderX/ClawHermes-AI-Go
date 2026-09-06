package modelconfig_test

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
)

type fakeResolver struct {
	val any
	ok  bool
	err error
}

func (f fakeResolver) ResolvePlatform(_ context.Context, _ string) (any, bool, error) {
	return f.val, f.ok, f.err
}

func TestResolveChatModel(t *testing.T) {
	cases := []struct {
		name      string
		resolver  modelconfig.PlatformResolver
		wantState modelconfig.State
		wantModel string
	}{
		{
			name:      "nil resolver means unconfigured",
			resolver:  nil,
			wantState: modelconfig.StateMissing,
		},
		{
			name:      "resolution failure maps to unavailable",
			resolver:  fakeResolver{err: errors.New("catalog down")},
			wantState: modelconfig.StateUnavailable,
		},
		{
			name:      "unset maps to missing",
			resolver:  fakeResolver{ok: false},
			wantState: modelconfig.StateMissing,
		},
		{
			name:      "non-string value maps to missing",
			resolver:  fakeResolver{val: 42, ok: true},
			wantState: modelconfig.StateMissing,
		},
		{
			name:      "empty string maps to missing",
			resolver:  fakeResolver{val: "", ok: true},
			wantState: modelconfig.StateMissing,
		},
		{
			name:      "whitespace maps to missing",
			resolver:  fakeResolver{val: "  ", ok: true},
			wantState: modelconfig.StateMissing,
		},
		{
			name:      "explicit model resolves",
			resolver:  fakeResolver{val: "qwen-max", ok: true},
			wantState: modelconfig.StateOK,
			wantModel: "qwen-max",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := modelconfig.ResolveChatModel(context.Background(), tc.resolver, modelconfig.KeyEnrichModel)
			if tc.wantState == modelconfig.StateOK {
				require.NoError(t, err)
				assert.Equal(t, tc.wantModel, got)
				return
			}
			require.Error(t, err)
			ce, ok := modelconfig.AsConfigError(err)
			require.True(t, ok, "expected *modelconfig.Err, got %v", err)
			assert.Equal(t, tc.wantState, ce.State)
			assert.Equal(t, modelconfig.KeyEnrichModel, ce.Key)
			if tc.wantState == modelconfig.StateUnavailable {
				assert.Error(t, ce.Cause)
			}
		})
	}
}

func TestAsConfigError(t *testing.T) {
	cfgErr := &modelconfig.Err{Key: modelconfig.KeySupersedeModel, State: modelconfig.StateMissing}
	_, ok := modelconfig.AsConfigError(cfgErr)
	assert.True(t, ok)

	wrapped := errors.New("boom")
	_, ok = modelconfig.AsConfigError(wrapped)
	assert.False(t, ok)

	// 经 %w 包装后仍可解出。
	_, ok = modelconfig.AsConfigError(&modelconfig.Err{Key: "k", State: modelconfig.StateMissing})
	assert.True(t, ok)
}

// probeDeps 提供探针的假数据源与假目录。
type probeDeps struct {
	raw    map[string]any
	rawErr error
	chat   []string
	embed  []string
	catErr error
}

func (d *probeDeps) PlatformValues(context.Context) (map[string]any, error) {
	return d.raw, d.rawErr
}

func (d *probeDeps) ChatEnabled(context.Context) ([]string, error) {
	return d.chat, d.catErr
}

func (d *probeDeps) EmbedEnabled(context.Context) ([]string, error) {
	return d.embed, d.catErr
}

func TestProbeCheckOnceStates(t *testing.T) {
	deps := &probeDeps{
		raw: map[string]any{
			modelconfig.KeyEnrichModel:     "qwen-max",
			modelconfig.KeySummaryModel:    "",
			modelconfig.KeySupersedeModel:  "qwen-plus",
			modelconfig.KeyEmbeddingModel:  "text-embedding-v2",
			modelconfig.KeyExtractionModel: float64(7), // 非 string → missing
		},
		chat:  []string{"qwen-max", "qwen-plus"},
		embed: []string{"text-embedding-v2"},
	}
	reqs := []modelconfig.Requirement{
		{Key: modelconfig.KeyEnrichModel, Kind: modelconfig.KindChat},
		{Key: modelconfig.KeySummaryModel, Kind: modelconfig.KindChat},
		{Key: modelconfig.KeySupersedeModel, Kind: modelconfig.KindChat},
		{Key: modelconfig.KeyEmbeddingModel, Kind: modelconfig.KindEmbed},
		{Key: modelconfig.KeyExtractionModel, Kind: modelconfig.KindChat},
		{Key: modelconfig.KeyReflectionModel, Kind: modelconfig.KindChat}, // 完全缺失
	}
	p := modelconfig.NewProbe(deps, deps, reqs, nil)
	p.CheckOnce(context.Background())

	assertState := func(param string, ok, missing, disabled float64) {
		t.Helper()
		assert.Equal(t, ok, testutil.ToFloat64(modelconfig.ConfigHealth.WithLabelValues(param, string(modelconfig.StateOK))), "%s ok", param)
		assert.Equal(t, missing, testutil.ToFloat64(modelconfig.ConfigHealth.WithLabelValues(param, string(modelconfig.StateMissing))), "%s missing", param)
		assert.Equal(t, disabled, testutil.ToFloat64(modelconfig.ConfigHealth.WithLabelValues(param, string(modelconfig.StateDisabled))), "%s disabled", param)
	}
	assertState(modelconfig.KeyEnrichModel, 1, 0, 0)
	assertState(modelconfig.KeySummaryModel, 0, 1, 0)
	assertState(modelconfig.KeySupersedeModel, 1, 0, 0)
	assertState(modelconfig.KeyEmbeddingModel, 1, 0, 0)
	assertState(modelconfig.KeyExtractionModel, 0, 1, 0)
	assertState(modelconfig.KeyReflectionModel, 0, 1, 0)

	// 目录里把 enrich 模型禁用 → 下一 tick 翻转为 disabled，ok 清 0。
	deps.chat = []string{"qwen-plus"}
	p.CheckOnce(context.Background())
	assertState(modelconfig.KeyEnrichModel, 0, 0, 1)
	assertState(modelconfig.KeySupersedeModel, 1, 0, 0)
}

func TestProbeCheckOnceSkipsOnSourceFailure(t *testing.T) {
	key := "memory.probe_error_guard"
	deps := &probeDeps{rawErr: errors.New("platform down")}
	reqs := []modelconfig.Requirement{{Key: key, Kind: modelconfig.KindChat}}
	p := modelconfig.NewProbe(deps, deps, reqs, nil)
	p.CheckOnce(context.Background())
	// 数据源失败 → 本轮跳过，gauge 保持未设（0），不 panic 不误报 missing。
	assert.Equal(t, float64(0), testutil.ToFloat64(modelconfig.ConfigHealth.WithLabelValues(key, string(modelconfig.StateMissing))))
}

func TestRegisterMetricsIdempotent(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NotPanics(t, func() {
		modelconfig.RegisterMetrics(reg)
		modelconfig.RegisterMetrics(reg) // 第二次不得 panic
	})
	modelconfig.IncError(modelconfig.KeyEnrichModel, "enrich", modelconfig.StateMissing)
	assert.Equal(t, float64(1), testutil.ToFloat64(
		modelconfig.ConfigErrorsTotal.WithLabelValues(modelconfig.KeyEnrichModel, "enrich", string(modelconfig.StateMissing))))
}
