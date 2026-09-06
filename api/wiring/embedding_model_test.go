package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	llmgateway "github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	parametersdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
)

// fakePlatformStore 是 PlatformStore 的 map 版桩：值按 registry key 存原始
// JSON 字符串，模拟 public.platform_settings。
type fakePlatformStore struct {
	values map[string]string
	// versions 按 group key 注入版本历史；nil 组返回空历史（未发布）。
	versions map[string][]port.PlatformVersion
	err      error
}

func (s *fakePlatformStore) GetValue(_ context.Context, key string) (json.RawMessage, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	raw, ok := s.values[key]
	if !ok {
		return nil, false, nil
	}
	return json.RawMessage(raw), true, nil
}

func (s *fakePlatformStore) SetValue(context.Context, string, json.RawMessage, string) error {
	return nil
}

func (s *fakePlatformStore) GetAll(context.Context) ([]port.PlatformValue, error) {
	return nil, nil
}

// GetSnapshot 按 GroupForKey 过滤出该组快照；err 非空时传播（fail-closed 用例
// 依赖读取路径的 DB 错误上抛）。
func (s *fakePlatformStore) GetSnapshot(_ context.Context, groupKey string) (map[string]json.RawMessage, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]json.RawMessage)
	for key, raw := range s.values {
		if parametersdomain.GroupForKey(key) == groupKey {
			out[key] = json.RawMessage(raw)
		}
	}
	return out, nil
}

func (s *fakePlatformStore) CreateDraft(_ context.Context, _ string, _ map[string]json.RawMessage, _, _ string) (port.PlatformVersion, error) {
	return port.PlatformVersion{}, nil
}

func (s *fakePlatformStore) Publish(_ context.Context, _ string, _ int64, _ string) error { return nil }

func (s *fakePlatformStore) Rollback(_ context.Context, _ string, _ int64, _ string) error {
	return nil
}

func (s *fakePlatformStore) ListVersions(_ context.Context, groupKey string) ([]port.PlatformVersion, error) {
	if s.err != nil {
		return nil, s.err
	}
	if versions, ok := s.versions[groupKey]; ok {
		return versions, nil
	}
	return []port.PlatformVersion{}, nil
}

// GetVersion/UpdateEvalState 补齐接口（分层门禁 P1）：eval_state 是 DB 独立列，
// 桩用真实 EvalState 字段写（与 DB 独立列语义一致）；仅需要存在性 + ErrVersionNotFound 语义。
func (s *fakePlatformStore) GetVersion(_ context.Context, groupKey string, versionSeq int64) (port.PlatformVersion, error) {
	if s.err != nil {
		return port.PlatformVersion{}, s.err
	}
	for _, v := range s.versions[groupKey] {
		if int64(v.VersionSeq) == versionSeq {
			return v, nil
		}
	}
	return port.PlatformVersion{}, parametersdomain.ErrVersionNotFound
}

func (s *fakePlatformStore) UpdateEvalState(_ context.Context, groupKey string, versionSeq int64, state, _ string) error {
	if s.err != nil {
		return s.err
	}
	for i := range s.versions[groupKey] {
		if int64(s.versions[groupKey][i].VersionSeq) == versionSeq {
			s.versions[groupKey][i].EvalState = state
			return nil
		}
	}
	return parametersdomain.ErrVersionNotFound
}

// newTestTenantEmbeddingResolver 构造一个平台参数里含给定配置的
// tenantEmbeddingModelResolver（值 key 为 "memory.embedding_model"），
// registry 复用 knowledge 目录。
func newTestTenantEmbeddingResolver(
	platformValues map[string]any,
	registry *llmgateway.ModelRegistry,
) *tenantEmbeddingModelResolver {
	values := make(map[string]string, len(platformValues))
	for k, v := range platformValues {
		raw, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		values[k] = string(raw)
	}
	svc := parametersapp.NewService(
		parametersdomain.NewParametersRegistry(),
		&fakePlatformStore{values: values},
	)
	return newTenantEmbeddingModelResolver(
		func() *parametersapp.Service { return svc },
		registry,
		zap.NewNop(),
	)
}

func TestResolveMemoryEmbeddingModelFailClosed(t *testing.T) {
	registry := newKnowledgeRegistry([]domain.Model{{
		ID: "embedding-1", ProviderID: "provider-1", Name: "managed-embedding",
		Enabled: true, Capabilities: []domain.ModelCapability{domain.CapEmbedding},
	}})
	ctx := context.Background()

	t.Run("parameters service unavailable", func(t *testing.T) {
		r := &tenantEmbeddingModelResolver{registry: registry, logger: zap.NewNop()}
		_, err := r.ResolveMemoryEmbeddingModel(ctx, "t1")
		require.ErrorIs(t, err, errMemoryEmbeddingNotConfigured)
	})
	t.Run("key missing", func(t *testing.T) {
		r := newTestTenantEmbeddingResolver(map[string]any{"other": "x"}, registry)
		_, err := r.ResolveMemoryEmbeddingModel(ctx, "t1")
		require.ErrorIs(t, err, errMemoryEmbeddingNotConfigured)
	})
	t.Run("key present but empty", func(t *testing.T) {
		r := newTestTenantEmbeddingResolver(map[string]any{"memory.embedding_model": "  "}, registry)
		_, err := r.ResolveMemoryEmbeddingModel(ctx, "t1")
		require.ErrorIs(t, err, errMemoryEmbeddingNotConfigured)
	})
	t.Run("model absent from catalogue", func(t *testing.T) {
		r := newTestTenantEmbeddingResolver(map[string]any{"memory.embedding_model": "ghost"}, registry)
		_, err := r.ResolveMemoryEmbeddingModel(ctx, "t1")
		require.Error(t, err)
		require.Contains(t, err.Error(), "resolve model")
	})
	t.Run("platform store read failure propagates fail-closed", func(t *testing.T) {
		svc := parametersapp.NewService(
			parametersdomain.NewParametersRegistry(),
			&fakePlatformStore{err: errors.New("db down")},
		)
		r := newTenantEmbeddingModelResolver(func() *parametersapp.Service { return svc }, registry, zap.NewNop())
		_, err := r.ResolveMemoryEmbeddingModel(ctx, "t1")
		require.Error(t, err)
		require.Contains(t, err.Error(), "resolve platform parameter")
	})
}

func TestResolveMemoryEmbeddingModelSuccess(t *testing.T) {
	registry := newKnowledgeRegistry([]domain.Model{{
		ID: "embedding-1", ProviderID: "provider-1", Name: "managed-embedding",
		Enabled: true, Capabilities: []domain.ModelCapability{domain.CapEmbedding},
	}})
	r := newTestTenantEmbeddingResolver(
		map[string]any{"memory.embedding_model": "managed-embedding"}, registry)

	model, err := r.ResolveMemoryEmbeddingModel(context.Background(), "t1")
	require.NoError(t, err)
	require.Equal(t, "managed-embedding", model)
}

func TestBuildEmbedResolverUsesPlatformConfig(t *testing.T) {
	registry := newKnowledgeRegistry([]domain.Model{{
		ID: "embedding-1", ProviderID: "provider-1", Name: "managed-embedding",
		Enabled: true, Capabilities: []domain.ModelCapability{domain.CapEmbedding},
	}})
	ctx := context.Background()

	t.Run("configured model resolves to a client", func(t *testing.T) {
		r := newTestTenantEmbeddingResolver(
			map[string]any{"memory.embedding_model": "managed-embedding"}, registry)
		require.NotNil(t, buildEmbedResolver(r, zap.NewNop())(ctx, "t1"))
	})
	t.Run("unconfigured fails closed to nil", func(t *testing.T) {
		r := newTestTenantEmbeddingResolver(nil, registry)
		require.Nil(t, buildEmbedResolver(r, zap.NewNop())(ctx, "t1"))
	})
	t.Run("misconfigured model fails closed to nil", func(t *testing.T) {
		r := newTestTenantEmbeddingResolver(map[string]any{"memory.embedding_model": "ghost"}, registry)
		require.Nil(t, buildEmbedResolver(r, zap.NewNop())(ctx, "t1"))
	})
}
