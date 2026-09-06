package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
)

// memStore is an in-memory PlatformStore modelling versioned groups: each group
// owns an immutable version list plus production/latest labels. Seeding uses
// seedPublished so resolver/service tests start from a concrete published
// snapshot; versioned writes go through CreateDraft/Publish exactly like the
// real repository (so SetPlatformValues/Publish/Rollback semantics are
// exercised here, not just on the DB-backed repo).
type memStore struct {
	groups map[string]*memGroup
	// lastEvalActor 记录最近一次 UpdateEvalState 收到的 actor，供 service 默认路径断言。
	lastEvalActor string
}

type memGroup struct {
	versions map[int64]*port.PlatformVersion
	nextID   int64
	prod     int64 // production label -> version id (0 = never published)
}

func (m *memStore) group(groupKey string) *memGroup {
	if m.groups == nil {
		m.groups = make(map[string]*memGroup)
	}
	if m.groups[groupKey] == nil {
		m.groups[groupKey] = &memGroup{versions: make(map[int64]*port.PlatformVersion)}
	}
	return m.groups[groupKey]
}

// snapshot returns the production version's snapshot (unset group -> empty
// map, matching the store's "missing label is not an error" contract).
func (g *memGroup) snapshot() map[string]json.RawMessage {
	if g.prod == 0 {
		return map[string]json.RawMessage{}
	}
	return g.versions[g.prod].Snapshot
}

func (g *memGroup) nextSeq() int {
	max := 0
	for _, v := range g.versions {
		if v.VersionSeq > max {
			max = v.VersionSeq
		}
	}
	return max + 1
}

// seedPublished publishes a synthetic version for a group so tests can set up
// "production already has these values" without going through a draft.
func (m *memStore) seedPublished(groupKey string, values map[string]json.RawMessage) {
	g := m.group(groupKey)
	snapshot := make(map[string]json.RawMessage, len(values))
	for k, v := range values {
		snapshot[k] = v
	}
	g.nextID++
	v := &port.PlatformVersion{
		ID: g.nextID, GroupKey: groupKey, VersionSeq: g.nextSeq(),
		Status: "published", Snapshot: snapshot, CreatedBy: "test",
	}
	g.versions[v.ID] = v
	g.prod = v.ID
}

func (m *memStore) GetValue(_ context.Context, key string) (json.RawMessage, bool, error) {
	raw, ok := m.group(domain.GroupForKey(key)).snapshot()[key]
	return raw, ok, nil
}

func (m *memStore) SetValue(_ context.Context, _ string, _ json.RawMessage, _ string) error {
	// Legacy port method; versioned writes go through CreateDraft+Publish.
	return nil
}

func (m *memStore) GetAll(_ context.Context) ([]port.PlatformValue, error) {
	var out []port.PlatformValue
	for groupKey := range m.groups {
		for key, raw := range m.group(groupKey).snapshot() {
			out = append(out, port.PlatformValue{Key: key, Value: raw})
		}
	}
	return out, nil
}

func (m *memStore) GetSnapshot(_ context.Context, groupKey string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage)
	for k, v := range m.group(groupKey).snapshot() {
		out[k] = v
	}
	return out, nil
}

func (m *memStore) CreateDraft(_ context.Context, groupKey string, snapshot map[string]json.RawMessage, message, createdBy string) (port.PlatformVersion, error) {
	g := m.group(groupKey)
	clone := make(map[string]json.RawMessage, len(snapshot))
	for k, v := range snapshot {
		clone[k] = v
	}
	g.nextID++
	v := &port.PlatformVersion{
		ID: g.nextID, GroupKey: groupKey, VersionSeq: g.nextSeq(),
		Status: "draft", Snapshot: clone, Message: message, CreatedBy: createdBy,
	}
	g.versions[v.ID] = v
	return *v, nil
}

func (m *memStore) Publish(_ context.Context, groupKey string, versionID int64, _ string) error {
	g := m.group(groupKey)
	v, ok := g.versions[versionID]
	if !ok {
		return domain.ErrVersionNotFound
	}
	if v.Status != "draft" {
		return domain.ErrVersionNotDraft
	}
	if g.prod != 0 {
		base := g.prod
		v.BaseVersion = &base
	}
	v.Status = "published"
	g.prod = v.ID
	return nil
}

func (m *memStore) Rollback(_ context.Context, groupKey string, targetVersionID int64, _ string) error {
	g := m.group(groupKey)
	v, ok := g.versions[targetVersionID]
	if !ok {
		return domain.ErrVersionNotFound
	}
	if v.Status != "published" {
		return domain.ErrVersionNotPublished
	}
	g.prod = v.ID
	return nil
}

func (m *memStore) ListVersions(_ context.Context, groupKey string) ([]port.PlatformVersion, error) {
	g := m.group(groupKey)
	out := make([]port.PlatformVersion, 0, len(g.versions))
	for _, v := range g.versions {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VersionSeq > out[j].VersionSeq })
	return out, nil
}

func (m *memStore) GetVersion(_ context.Context, groupKey string, versionSeq int64) (port.PlatformVersion, error) {
	g := m.group(groupKey)
	for _, v := range g.versions {
		if int64(v.VersionSeq) == versionSeq {
			return *v, nil
		}
	}
	return port.PlatformVersion{}, domain.ErrVersionNotFound
}

// UpdateEvalState 写平台版本真实 EvalState 字段（与 DB 独立列语义一致）；actor 单独
// 记录到 lastEvalActor，供 service 空 actor 默认 "api" 路径断言。
func (m *memStore) UpdateEvalState(_ context.Context, groupKey string, versionSeq int64, state, actor string) error {
	m.lastEvalActor = actor
	g := m.group(groupKey)
	for _, v := range g.versions {
		if int64(v.VersionSeq) == versionSeq {
			v.EvalState = state
			return nil
		}
	}
	return domain.ErrVersionNotFound
}

func newTestStore() *memStore {
	return &memStore{groups: make(map[string]*memGroup)}
}

func TestResolverPlatformTwoLevelFallback(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	// 平台级 key:存储值 → 定义默认两级兜底(全局参数契约)。
	store.seedPublished(domain.GroupMemory, map[string]json.RawMessage{"memory.recall_top_k": json.RawMessage(`9`)})
	store.seedPublished(domain.GroupAgent, map[string]json.RawMessage{"agent.factcheck.top_k": json.RawMessage(`6`)})
	resolver := NewResolver(registry, store)

	t.Run("declared wins over stored platform value", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "memory.recall_top_k", map[string]any{"memory.recall_top_k": 3})
		if err != nil {
			t.Fatal(err)
		}
		if !present || value != int64(3) {
			t.Fatalf("got (%v, %v), want (3, true)", value, present)
		}
	})

	t.Run("stored platform value applies when declared absent", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "memory.recall_top_k", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !present || value != int64(9) {
			t.Fatalf("got (%v, %v), want (9, true)", value, present)
		}
	})

	t.Run("definition default applies when both tiers unset", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "memory.fact_injection_top_n", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !present || value != int64(8) {
			t.Fatalf("got (%v, %v), want (8, true)", value, present)
		}
	})

	t.Run("declared zero is unset, falls to stored platform value", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "agent.factcheck.top_k", map[string]any{"agent.factcheck.top_k": 0})
		if err != nil {
			t.Fatal(err)
		}
		if !present || value != int64(6) {
			t.Fatalf("got (%v, %v), want (6, true): explicit 0 == unset", value, present)
		}
	})

	t.Run("zero default resolves to unset", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "evaluation.judge.temperature", nil)
		if err != nil {
			t.Fatal(err)
		}
		if present || value != nil {
			t.Fatalf("got (%v, %v), want (nil, false): 0 default == unset", value, present)
		}
	})

	t.Run("unknown key errors", func(t *testing.T) {
		if _, _, err := resolver.Resolve(context.Background(), "nope.nope", nil); err == nil {
			t.Fatal("unknown key must error")
		}
	})
}

func TestResolverResourceDeclaredOnly(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	// 平台存储里残留的资源级值必须被忽略:资源默认值已下线,资源配置只在资源层。
	store.seedPublished(domain.GroupAgent, map[string]json.RawMessage{"agent.temperature": json.RawMessage(`0.9`)})
	resolver := NewResolver(registry, store)

	t.Run("declared value resolves", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "agent.temperature", map[string]any{"agent.temperature": 0.3})
		if err != nil {
			t.Fatal(err)
		}
		if !present || value != 0.3 {
			t.Fatalf("got (%v, %v), want (0.3, true)", value, present)
		}
	})

	t.Run("absent declared resolves to unset despite stored platform value", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "agent.temperature", nil)
		if err != nil {
			t.Fatal(err)
		}
		if present || value != nil {
			t.Fatalf("got (%v, %v), want (nil, false): no platform default for resource keys", value, present)
		}
	})

	t.Run("definition default never applies", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "agent.max_iterations", nil)
		if err != nil {
			t.Fatal(err)
		}
		if present || value != nil {
			t.Fatalf("got (%v, %v), want (nil, false): definition default must not apply", value, present)
		}
	})

	t.Run("declared zero is unset", func(t *testing.T) {
		value, present, err := resolver.Resolve(context.Background(), "agent.temperature", map[string]any{"agent.temperature": 0})
		if err != nil {
			t.Fatal(err)
		}
		if present || value != nil {
			t.Fatalf("got (%v, %v), want (nil, false): explicit 0 == unset", value, present)
		}
	})
}

func TestResolverResolveForResourceDeclaredOnly(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	store.seedPublished(domain.GroupAgent, map[string]json.RawMessage{"agent.temperature": json.RawMessage(`0.9`)})
	resolver := NewResolver(registry, store)

	effective, err := resolver.ResolveForResource(context.Background(), map[string]any{
		"agent.max_tokens": 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 只返回声明值:平台存储的 temperature 0.9 不再进入资源生效值。
	if _, ok := effective["agent.temperature"]; ok {
		t.Fatal("platform-stored resource default must not be applied")
	}
	if got := effective["agent.max_tokens"]; got != int64(2048) {
		t.Fatalf("max_tokens = %v, want 2048", got)
	}
	if _, ok := effective["agent.max_context_tokens"]; ok {
		t.Fatal("0-default key must not appear in effective map")
	}
	if _, ok := effective["agent.max_iterations"]; ok {
		t.Fatal("definition default must not apply for resource keys")
	}
	if _, ok := effective["agent.bindings"]; ok {
		t.Fatal("bindings has no default and must stay absent")
	}
}

func TestServiceSetPlatformValues(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	svc := NewService(registry, store)

	t.Run("sets valid platform values", func(t *testing.T) {
		err := svc.SetPlatformValues(context.Background(), map[string]any{
			"evaluation.optimizer.temperature": 0.5,
			"trace.capture_parameters":         true,
		}, "admin-1")
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.PlatformValues(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := got["evaluation.optimizer.temperature"]; !ok || v != 0.5 {
			t.Fatalf("temperature = %v, want 0.5 (published)", v)
		}
		if v, ok := got["trace.capture_parameters"]; !ok || v != true {
			t.Fatalf("capture_parameters = %v, want true (published)", v)
		}
	})

	t.Run("rejects resource-scope value (no platform resource defaults)", func(t *testing.T) {
		err := svc.SetPlatformValues(
			context.Background(),
			map[string]any{"agent.temperature": 0.3},
			"admin-1",
		)
		var invalid *domain.ErrInvalidParameter
		if !domain.AsInvalidParameter(err, &invalid) {
			t.Fatalf("want ErrInvalidParameter, got %v", err)
		}
		if len(store.group(domain.GroupAgent).versions) != 0 {
			t.Fatal("resource-scope key must not be stored as a platform default")
		}
	})

	t.Run("rejects unknown key", func(t *testing.T) {
		err := svc.SetPlatformValues(context.Background(), map[string]any{"bogus.key": 1}, "admin-1")
		var invalid *domain.ErrInvalidParameter
		if !domain.AsInvalidParameter(err, &invalid) {
			t.Fatalf("want ErrInvalidParameter, got %v", err)
		}
	})

	t.Run("rejects out-of-bounds value", func(t *testing.T) {
		err := svc.SetPlatformValues(context.Background(), map[string]any{"evaluation.optimizer.temperature": 5}, "admin-1")
		var invalid *domain.ErrInvalidParameter
		if !domain.AsInvalidParameter(err, &invalid) {
			t.Fatalf("want ErrInvalidParameter, got %v", err)
		}
	})

	t.Run("merge semantics: partial write keeps existing", func(t *testing.T) {
		_ = svc.SetPlatformValues(context.Background(), map[string]any{"evaluation.optimizer.temperature": 0.5}, "admin-1")
		_ = svc.SetPlatformValues(context.Background(), map[string]any{"evaluation.optimizer.max_tokens": 2048}, "admin-1")
		// 第二次只写一个 key,不能清掉第一个。
		got, err := svc.PlatformValues(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := got["evaluation.optimizer.temperature"]; !ok || v != 0.5 {
			t.Fatalf("temperature = %v, want 0.5: merge write must not wipe previously stored keys", v)
		}
		// PlatformValues 解码 JSONB 数值为 float64（与 handler List 语义一致）。
		if v, ok := got["evaluation.optimizer.max_tokens"]; !ok || v != float64(2048) {
			t.Fatalf("max_tokens = %v, want 2048", v)
		}
	})

	t.Run("empty input is a no-op", func(t *testing.T) {
		if err := svc.SetPlatformValues(context.Background(), map[string]any{}, "admin-1"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestServicePlatformValuesMergesDefaults(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	store.seedPublished(domain.GroupEvaluation, map[string]json.RawMessage{"evaluation.optimizer.temperature": json.RawMessage(`0.5`)})
	svc := NewService(registry, store)

	values, err := svc.PlatformValues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := values["evaluation.optimizer.temperature"]; got != 0.5 {
		t.Fatalf("stored value = %v, want 0.5", got)
	}
	if got := values["evaluation.optimizer.model"]; got != "" {
		t.Fatalf("default value = %v, want empty (model must resolve from catalog, no hardcoded fallback)", got)
	}
	if _, ok := values["agent.temperature"]; ok {
		t.Fatal("resource-scope key must not be returned by PlatformValues")
	}
}

// TestServiceVersionedLifecycle 覆盖版本化生命周期不变量：draft 是唯一可编辑
// 状态（发布前不影响 production）；Publish 传播 created_by/message 并记录
// base_version_id=当前 production；Rollback 不产新版本、production 整体回退；
// Versions 按 version_seq 降序返回全部历史。
func TestServiceVersionedLifecycle(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	svc := NewService(registry, store)

	// 1) CreateDraft：created_by/message 传播；draft 未发布不影响 production。
	draft1, err := svc.CreateDraft(context.Background(), domain.GroupAgent,
		map[string]any{"agent.factcheck.enabled": true},
		"enable factcheck", "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if draft1.Status != "draft" || draft1.CreatedBy != "admin-1" || draft1.Message != "enable factcheck" {
		t.Fatalf("draft = %+v, want status=draft created_by=admin-1 message=enable factcheck", draft1)
	}
	// draft 未发布：production 快照必须为空（PlatformValues 无法区分默认值，
	// 直接断言 store 快照）。
	if snap := store.group(domain.GroupAgent).snapshot(); len(snap) != 0 {
		t.Fatalf("draft must not affect production snapshot, got %v", snap)
	}

	// 2) Publish 首个版本：production 生效，版本行无 base（首个无父版本）。
	if err := svc.Publish(context.Background(), domain.GroupAgent, draft1.ID, "admin-1"); err != nil {
		t.Fatal(err)
	}
	if base := publishedBaseVersion(t, svc, domain.GroupAgent, draft1.ID); base != nil {
		t.Fatalf("first publish base_version_id = %v, want nil", *base)
	}
	vals, err := svc.PlatformValues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := vals["agent.factcheck.enabled"]; !ok || v != true {
		t.Fatalf("published value = %v, want true", v)
	}

	// 3) 第二次发布：base_version_id = 当前 production（draft1）；merge 保留旧值。
	draft2, err := svc.CreateDraft(context.Background(), domain.GroupAgent,
		map[string]any{"agent.factcheck.top_k": 6},
		"bump top_k", "admin-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(context.Background(), domain.GroupAgent, draft2.ID, "admin-2"); err != nil {
		t.Fatal(err)
	}
	if base := publishedBaseVersion(t, svc, domain.GroupAgent, draft2.ID); base == nil || *base != draft1.ID {
		t.Fatalf("second publish base_version_id = %v, want %d", base, draft1.ID)
	}
	vals, err = svc.PlatformValues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := vals["agent.factcheck.enabled"]; !ok || v != true {
		t.Fatalf("merge must preserve enabled, got %v", v)
	}
	if v, ok := vals["agent.factcheck.top_k"]; !ok || v != float64(6) {
		t.Fatalf("top_k = %v, want 6 (handler List 语义 float64)", v)
	}

	// 4) Rollback 到 draft1：不产新版本；production 回退 draft1 快照。
	before := len(store.group(domain.GroupAgent).versions)
	if err := svc.Rollback(context.Background(), domain.GroupAgent, draft1.ID, "admin-3"); err != nil {
		t.Fatal(err)
	}
	if after := len(store.group(domain.GroupAgent).versions); after != before {
		t.Fatalf("rollback must not create a version: %d → %d", before, after)
	}
	vals, err = svc.PlatformValues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := vals["agent.factcheck.top_k"]; ok {
		t.Fatalf("top_k must be gone after rollback to draft1 snapshot, got %v", v)
	}
	if v, ok := vals["agent.factcheck.enabled"]; !ok || v != true {
		t.Fatalf("enabled must persist after rollback, got %v", v)
	}

	// 5) Versions 全量、按 seq 降序。
	versions, err := svc.Versions(context.Background(), domain.GroupAgent)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != before {
		t.Fatalf("versions = %d, want %d", len(versions), before)
	}
	for i := 1; i < len(versions); i++ {
		if versions[i-1].VersionSeq <= versions[i].VersionSeq {
			t.Fatalf("versions not descending: %+v", versions)
		}
	}
}

// TestServicePublishRollbackErrors 断言状态机拒绝：未知版本、draft 不可回滚、
// published 不可重复发布、跨组 draft 拒绝。
func TestServicePublishRollbackErrors(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	svc := NewService(registry, store)

	if err := svc.Publish(context.Background(), domain.GroupAgent, 999, "admin-1"); !errors.Is(err, domain.ErrVersionNotFound) {
		t.Fatalf("publish unknown = %v, want ErrVersionNotFound", err)
	}
	if err := svc.Rollback(context.Background(), domain.GroupAgent, 999, "admin-1"); !errors.Is(err, domain.ErrVersionNotFound) {
		t.Fatalf("rollback unknown = %v, want ErrVersionNotFound", err)
	}

	draft, err := svc.CreateDraft(context.Background(), domain.GroupAgent,
		map[string]any{"agent.factcheck.enabled": true}, "m", "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Rollback(context.Background(), domain.GroupAgent, draft.ID, "admin-1"); !errors.Is(err, domain.ErrVersionNotPublished) {
		t.Fatalf("rollback draft = %v, want ErrVersionNotPublished", err)
	}

	if err := svc.Publish(context.Background(), domain.GroupAgent, draft.ID, "admin-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(context.Background(), domain.GroupAgent, draft.ID, "admin-1"); !errors.Is(err, domain.ErrVersionNotDraft) {
		t.Fatalf("publish published = %v, want ErrVersionNotDraft", err)
	}

	// 跨组：key 归属 agent，createDraft 到 memory 组必须拒绝。
	var invalid *domain.ErrInvalidParameter
	if _, err := svc.CreateDraft(context.Background(), domain.GroupMemory,
		map[string]any{"agent.factcheck.enabled": true}, "m", "admin-1"); !domain.AsInvalidParameter(err, &invalid) {
		t.Fatalf("cross-group draft = %v, want ErrInvalidParameter", err)
	}
}

// errStore 是只返回错误的 PlatformStore，用于 fail-closed 断言：DB 错误必须
// 向上传播，禁止静默回退定义默认。
type errStore struct {
	err error
}

func (e *errStore) GetValue(context.Context, string) (json.RawMessage, bool, error) {
	return nil, false, e.err
}
func (e *errStore) SetValue(context.Context, string, json.RawMessage, string) error { return e.err }
func (e *errStore) GetAll(context.Context) ([]port.PlatformValue, error)            { return nil, e.err }
func (e *errStore) GetSnapshot(context.Context, string) (map[string]json.RawMessage, error) {
	return nil, e.err
}
func (e *errStore) CreateDraft(context.Context, string, map[string]json.RawMessage, string, string) (port.PlatformVersion, error) {
	return port.PlatformVersion{}, e.err
}
func (e *errStore) Publish(context.Context, string, int64, string) error  { return e.err }
func (e *errStore) Rollback(context.Context, string, int64, string) error { return e.err }
func (e *errStore) ListVersions(context.Context, string) ([]port.PlatformVersion, error) {
	return nil, e.err
}
func (e *errStore) GetVersion(context.Context, string, int64) (port.PlatformVersion, error) {
	return port.PlatformVersion{}, e.err
}
func (e *errStore) UpdateEvalState(context.Context, string, int64, string, string) error {
	return e.err
}

// TestResolverSnapshotFailClosed 断言平台快照读取失败 fail-closed：DB 错误上抛，
// 不回退默认；resource-scope 解析不触平台存储。
func TestResolverSnapshotFailClosed(t *testing.T) {
	registry := domain.NewParametersRegistry()
	resolver := NewResolver(registry, &errStore{err: errors.New("db down")})

	if _, _, err := resolver.Resolve(context.Background(), "agent.factcheck.enabled", nil); err == nil {
		t.Fatal("DB error must propagate, not fall back to definition default")
	}
	if _, err := resolver.ResolveForResource(context.Background(), map[string]any{"agent.max_tokens": 2048}); err != nil {
		t.Fatalf("resource-scope resolution must not touch platform store: %v", err)
	}
}

// TestResolverGroupSnapshotAtomicity 断言整组快照切换原子：同一 production 内
// 同组 key 解析一致，Publish 后整组无残留旧值。
func TestResolverGroupSnapshotAtomicity(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	store.seedPublished(domain.GroupAgent, map[string]json.RawMessage{
		"agent.factcheck.enabled": json.RawMessage(`true`),
		"agent.factcheck.top_k":   json.RawMessage(`6`),
	})
	resolver := NewResolver(registry, store)

	enabled, _, err := resolver.Resolve(context.Background(), "agent.factcheck.enabled", nil)
	if err != nil {
		t.Fatal(err)
	}
	topK, _, err := resolver.Resolve(context.Background(), "agent.factcheck.top_k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if enabled != true || topK != int64(6) {
		t.Fatalf("snapshot atomicity violated: enabled=%v topK=%v, want true/6", enabled, topK)
	}

	// Publish 切换 production 后整组一致更新（经 service merge：enabled 保留）。
	svc := NewService(registry, store)
	draft, err := svc.CreateDraft(context.Background(), domain.GroupAgent,
		map[string]any{"agent.factcheck.top_k": 9}, "bump", "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Publish(context.Background(), domain.GroupAgent, draft.ID, "admin-1"); err != nil {
		t.Fatal(err)
	}
	enabled, _, err = resolver.Resolve(context.Background(), "agent.factcheck.enabled", nil)
	if err != nil {
		t.Fatal(err)
	}
	topK, _, err = resolver.Resolve(context.Background(), "agent.factcheck.top_k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if enabled != true || topK != int64(9) {
		t.Fatalf("post-publish snapshot: enabled=%v topK=%v, want true/9", enabled, topK)
	}
}

// publishedBaseVersion 从版本历史读取指定版本的 BaseVersion（Publish 写入版本行，
// CreateDraft 返回的值拷贝不会后续更新，必须读回断言）。
func publishedBaseVersion(t *testing.T, svc *Service, groupKey string, versionID int64) *int64 {
	t.Helper()
	versions, err := svc.Versions(context.Background(), groupKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		if v.ID == versionID {
			return v.BaseVersion
		}
	}
	t.Fatalf("version %d not found in history", versionID)
	return nil
}

// TestServiceCreateDraftSanitizesSensitiveValues 守护 sanitize 幂等不变量：
// Sensitive 平台参数写入版本快照时必须变成 sha256 指纹（platform_config_versions
// 不是凭据存储），已指纹化的值原样透传（前端回读指纹再保存不得二次哈希）。
// review 发现的 off-by-one：`"sha256:` 前缀是 8 字节，raw[:7] 恒 false 会让
// 幂等透传变成死代码，每次保存都重哈希出不同指纹，diff 恒显示变更且断链。
func TestServiceCreateDraftSanitizesSensitiveValues(t *testing.T) {
	registry := domain.NewParametersRegistry()
	if err := registry.Register(domain.ParameterDefinition{
		Key:       "agent.sensitive_secret",
		Scope:     domain.ScopePlatform,
		ValueType: domain.TypeString,
		Sensitive: true,
	}); err != nil {
		t.Fatal(err)
	}
	store := newTestStore()
	svc := NewService(registry, store)

	secret := "super-secret-value"

	t.Run("raw secret never reaches the snapshot", func(t *testing.T) {
		draft, err := svc.CreateDraft(context.Background(), domain.GroupAgent,
			map[string]any{"agent.sensitive_secret": secret}, "mask on write", "admin-1")
		if err != nil {
			t.Fatal(err)
		}
		raw := draft.Snapshot["agent.sensitive_secret"]
		if !bytes.HasPrefix(raw, []byte(`"sha256:`)) {
			t.Fatalf("snapshot value = %s, want sha256 fingerprint", raw)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("raw secret must not appear in the version snapshot")
		}
	})

	t.Run("already-fingerprinted value passes through unchanged", func(t *testing.T) {
		// 模拟前端回读快照指纹后原样保存：前端拿到的是不含引号的指纹字符串，
		// 经 json.Marshal 补回外层引号后仍须透传，否则每次保存都重哈希成新
		// 指纹。回归 off-by-one：`"sha256:` 是 8 字节，旧代码 len(raw) > 8 且
		// raw[:7] 恒 false，这里恒走重哈希路径导致指纹漂移。
		fingerprint := "sha256:" + strings.Repeat("ab", 32)
		draft, err := svc.CreateDraft(context.Background(), domain.GroupAgent,
			map[string]any{"agent.sensitive_secret": fingerprint}, "re-save fingerprint", "admin-1")
		if err != nil {
			t.Fatal(err)
		}
		want := `"sha256:` + strings.Repeat("ab", 32) + `"`
		if got := string(draft.Snapshot["agent.sensitive_secret"]); got != want {
			t.Fatalf("re-saved value = %s, want passthrough %s", got, want)
		}
	})
}

// TestServiceGateVersionOps 覆盖平台版本门禁读写的 service 薄转发（spec §4.2.3）：
// GetVersion 按 group+version_seq 命中返回版本元数据；UpdateEvalState 命中写
// eval_state、未知 seq → ErrVersionNotFound（errors.Is）；store 错误上抛（fail-closed）。
func TestServiceGateVersionOps(t *testing.T) {
	registry := domain.NewParametersRegistry()
	store := newTestStore()
	store.seedPublished(domain.GroupEvaluation, map[string]json.RawMessage{"evaluation.optimizer.temperature": json.RawMessage(`0.5`)})
	svc := NewService(registry, store)

	versions, err := svc.Versions(context.Background(), domain.GroupEvaluation)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("seed versions = %d, want 1", len(versions))
	}
	seq := int64(versions[0].VersionSeq)

	t.Run("GetVersion hit returns version seq", func(t *testing.T) {
		got, err := svc.GetVersion(context.Background(), domain.GroupEvaluation, seq)
		if err != nil {
			t.Fatal(err)
		}
		if int64(got.VersionSeq) != seq || got.GroupKey != domain.GroupEvaluation {
			t.Fatalf("GetVersion = %+v, want group=%s seq=%d", got, domain.GroupEvaluation, seq)
		}
	})

	t.Run("UpdateEvalState hit records state", func(t *testing.T) {
		if err := svc.UpdateEvalState(context.Background(), domain.GroupEvaluation, seq, "rollback_recommended", "gate"); err != nil {
			t.Fatal(err)
		}
		got, err := svc.GetVersion(context.Background(), domain.GroupEvaluation, seq)
		if err != nil {
			t.Fatal(err)
		}
		if got.EvalState != "rollback_recommended" {
			t.Fatalf("eval_state = %q, want %q", got.EvalState, "rollback_recommended")
		}
	})

	t.Run("actor defaults to api when empty", func(t *testing.T) {
		if err := svc.UpdateEvalState(context.Background(), domain.GroupEvaluation, seq, "rollback_recommended", ""); err != nil {
			t.Fatalf("empty actor must default to api, got error: %v", err)
		}
		if store.lastEvalActor != "api" {
			t.Fatalf("empty actor default = %q, want \"api\"", store.lastEvalActor)
		}
	})

	t.Run("unknown seq maps to ErrVersionNotFound", func(t *testing.T) {
		if _, err := svc.GetVersion(context.Background(), domain.GroupEvaluation, 999); !errors.Is(err, domain.ErrVersionNotFound) {
			t.Fatalf("GetVersion unknown = %v, want ErrVersionNotFound", err)
		}
		if err := svc.UpdateEvalState(context.Background(), domain.GroupEvaluation, 999, "rollback_recommended", "gate"); !errors.Is(err, domain.ErrVersionNotFound) {
			t.Fatalf("UpdateEvalState unknown = %v, want ErrVersionNotFound", err)
		}
	})

	t.Run("store failure propagates (fail-closed)", func(t *testing.T) {
		esvc := NewService(registry, &errStore{err: errors.New("db down")})
		if _, err := esvc.GetVersion(context.Background(), domain.GroupEvaluation, 1); err == nil {
			t.Fatal("GetVersion store error must propagate")
		}
		if err := esvc.UpdateEvalState(context.Background(), domain.GroupEvaluation, 1, "rollback_recommended", "gate"); err == nil {
			t.Fatal("UpdateEvalState store error must propagate")
		}
	})
}
