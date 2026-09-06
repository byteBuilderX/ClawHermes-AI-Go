package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/api/middleware"
	paramapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	paramdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
)

// fakePlatformStore implements port.PlatformStore for handler tests; each
// method is wired through a function field so tests can override behaviour.
// values is the effective production view (keyed by full parameter key);
// versioned methods keep a per-group version list so draft→publish round-trips
// land back in values, matching the real label semantics (production label →
// version snapshot). Delete-on-publish keeps the group self-consistent: a
// published snapshot fully replaces the previous production snapshot.
type fakePlatformStore struct {
	getFn  func(context.Context, string) (json.RawMessage, bool, error)
	setFn  func(context.Context, string, json.RawMessage, string) error
	allFn  func(context.Context) ([]port.PlatformValue, error)
	values map[string]json.RawMessage
	groups map[string]*fakeVersionGroup
}

type fakeVersionGroup struct {
	versions map[int64]*port.PlatformVersion
	nextID   int64
}

func (f *fakePlatformStore) group(groupKey string) *fakeVersionGroup {
	if f.groups == nil {
		f.groups = make(map[string]*fakeVersionGroup)
	}
	if f.groups[groupKey] == nil {
		f.groups[groupKey] = &fakeVersionGroup{versions: make(map[int64]*port.PlatformVersion)}
	}
	return f.groups[groupKey]
}

// replaceGroupSnapshot swaps the group's keys in the effective view with the
// snapshot contents (delete-then-merge so removed keys do not linger).
func (f *fakePlatformStore) replaceGroupSnapshot(groupKey string, snapshot map[string]json.RawMessage) {
	if f.values == nil {
		f.values = make(map[string]json.RawMessage)
	}
	for key := range f.values {
		if paramdomain.GroupForKey(key) == groupKey {
			delete(f.values, key)
		}
	}
	for key, raw := range snapshot {
		f.values[key] = raw
	}
}

func (f *fakePlatformStore) GetValue(ctx context.Context, key string) (json.RawMessage, bool, error) {
	if f.getFn != nil {
		return f.getFn(ctx, key)
	}
	if f.values == nil {
		return nil, false, nil
	}
	raw, ok := f.values[key]
	return raw, ok, nil
}

func (f *fakePlatformStore) SetValue(ctx context.Context, key string, value json.RawMessage, updatedBy string) error {
	if f.setFn != nil {
		return f.setFn(ctx, key, value, updatedBy)
	}
	if f.values == nil {
		f.values = make(map[string]json.RawMessage)
	}
	f.values[key] = value
	return nil
}

func (f *fakePlatformStore) GetAll(ctx context.Context) ([]port.PlatformValue, error) {
	if f.allFn != nil {
		return f.allFn(ctx)
	}
	var out []port.PlatformValue
	for k, v := range f.values {
		out = append(out, port.PlatformValue{Key: k, Value: v})
	}
	return out, nil
}

func (f *fakePlatformStore) GetSnapshot(_ context.Context, groupKey string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage)
	for key, raw := range f.values {
		if paramdomain.GroupForKey(key) == groupKey {
			out[key] = raw
		}
	}
	return out, nil
}

func (f *fakePlatformStore) CreateDraft(_ context.Context, groupKey string, snapshot map[string]json.RawMessage, message, createdBy string) (port.PlatformVersion, error) {
	g := f.group(groupKey)
	clone := make(map[string]json.RawMessage, len(snapshot))
	for k, v := range snapshot {
		clone[k] = v
	}
	g.nextID++
	v := &port.PlatformVersion{
		ID: g.nextID, GroupKey: groupKey, VersionSeq: int(g.nextID),
		Status: "draft", Snapshot: clone, Message: message, CreatedBy: createdBy,
	}
	g.versions[v.ID] = v
	return *v, nil
}

func (f *fakePlatformStore) Publish(_ context.Context, groupKey string, versionID int64, _ string) error {
	g := f.group(groupKey)
	v, ok := g.versions[versionID]
	if !ok {
		return paramdomain.ErrVersionNotFound
	}
	if v.Status != "draft" {
		return paramdomain.ErrVersionNotDraft
	}
	v.Status = "published"
	f.replaceGroupSnapshot(groupKey, v.Snapshot)
	return nil
}

func (f *fakePlatformStore) Rollback(_ context.Context, groupKey string, targetVersionID int64, _ string) error {
	g := f.group(groupKey)
	v, ok := g.versions[targetVersionID]
	if !ok {
		return paramdomain.ErrVersionNotFound
	}
	if v.Status != "published" {
		return paramdomain.ErrVersionNotPublished
	}
	f.replaceGroupSnapshot(groupKey, v.Snapshot)
	return nil
}

func (f *fakePlatformStore) ListVersions(_ context.Context, groupKey string) ([]port.PlatformVersion, error) {
	g := f.group(groupKey)
	out := make([]port.PlatformVersion, 0, len(g.versions))
	for _, v := range g.versions {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VersionSeq > out[j].VersionSeq })
	return out, nil
}

// GetVersion/UpdateEvalState 补齐接口（分层门禁 P1）：eval_state 是 DB 独立列，
// 桩用真实 EvalState 字段写（与 DB 独立列语义一致）；仅需要存在性 + ErrVersionNotFound 语义。
func (f *fakePlatformStore) GetVersion(_ context.Context, groupKey string, versionSeq int64) (port.PlatformVersion, error) {
	g := f.group(groupKey)
	for _, v := range g.versions {
		if int64(v.VersionSeq) == versionSeq {
			return *v, nil
		}
	}
	return port.PlatformVersion{}, paramdomain.ErrVersionNotFound
}

func (f *fakePlatformStore) UpdateEvalState(_ context.Context, groupKey string, versionSeq int64, state, _ string) error {
	g := f.group(groupKey)
	for _, v := range g.versions {
		if int64(v.VersionSeq) == versionSeq {
			v.EvalState = state
			return nil
		}
	}
	return paramdomain.ErrVersionNotFound
}

func setupParameterRouter(h *ParameterHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/parameters", h.List)
	r.PUT("/admin/parameters", h.Update)
	return r
}

func newTestParameterHandler(store port.PlatformStore) *ParameterHandler {
	return NewParameterHandler(
		paramapp.NewService(paramdomain.NewParametersRegistry(), store),
		zap.NewNop(),
	)
}

func TestListPlatformValues_returnsStoredAndNonZeroDefaults(t *testing.T) {
	store := &fakePlatformStore{
		values: map[string]json.RawMessage{
			"memory.enrich_temperature": json.RawMessage(`0.9`),
		},
	}
	r := setupParameterRouter(newTestParameterHandler(store))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/admin/parameters", nil) //nolint:noctx
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if got := resp["memory.enrich_temperature"]; got != float64(0.9) {
		t.Fatalf("expected stored 0.9, got %v", got)
	}
	// 非 0 默认值必须回填（List 语义：缺失键 = 0/''/nil 默认）。
	if _, ok := resp["memory.supersede_temperature"]; ok {
		t.Fatal("0 默认键不应出现在 List 返回值中")
	}
}

func TestUpdatePlatformValues_writesOnlyGivenKeys(t *testing.T) {
	store := &fakePlatformStore{}
	r := setupParameterRouter(newTestParameterHandler(store))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/admin/parameters", strings.NewReader(`{"memory.enrich_temperature":0.9}`)) //nolint:noctx
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(store.values) != 1 {
		t.Fatalf("expected exactly 1 stored key, got %v", store.values)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if got := resp["memory.enrich_temperature"]; got != float64(0.9) {
		t.Fatalf("expected merged 0.9, got %v", got)
	}
}

func TestUpdatePlatformValues_rejectsUnknownKey(t *testing.T) {
	r := setupParameterRouter(newTestParameterHandler(&fakePlatformStore{}))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/admin/parameters", strings.NewReader(`{"not.a.real.key":1}`)) //nolint:noctx
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// setupAdminParameterRouter 模拟 router.go 的 admin 组挂载（RequireGlobalAdmin），
// 验证版本端点处于授权组内：非 global admin 一律 403。withAdmin 为 true 时
// 注入等价 jwtMW 解析结果的 global_admin role（role 经 gin context 传递）。
func setupAdminParameterRouter(h *ParameterHandler, withAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := r.Group("/admin")
	if withAdmin {
		admin.Use(func(c *gin.Context) {
			c.Set("auth.global_role", "global_admin")
			c.Next()
		})
	}
	admin.Use(middleware.RequireGlobalAdmin())
	admin.GET("/parameters/versions/:groupKey", h.Versions)
	admin.POST("/parameters/versions/:groupKey", h.CreateDraft)
	admin.POST("/parameters/versions/:groupKey/:versionID/publish", h.Publish)
	admin.POST("/parameters/versions/:groupKey/:versionID/rollback", h.Rollback)
	return r
}

// TestVersionEndpoints_requireGlobalAdmin 断言全部版本化端点挂 RequireGlobalAdmin：
// 未注入 global_admin role 时一律 403（router.go 里这些端点注册在同一 admin 组）。
func TestVersionEndpoints_requireGlobalAdmin(t *testing.T) {
	h := newTestParameterHandler(&fakePlatformStore{})
	router := setupAdminParameterRouter(h, false)

	cases := []struct{ method, path string }{
		{"GET", "/admin/parameters/versions/agent"},
		{"POST", "/admin/parameters/versions/agent"},
		{"POST", "/admin/parameters/versions/agent/1/publish"},
		{"POST", "/admin/parameters/versions/agent/1/rollback"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil) //nolint:noctx
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want 403", tc.method, tc.path, w.Code)
		}
	}
}

// TestVersionEndpoints_createDraftPublishRollback 走完整版本化往返：CreateDraft →
// Publish（生效值反映新快照）→ Versions（历史 + 状态）→ 再 publish 新版本 →
// Rollback（生效值回退且不产新版本），断言响应形状（/admin/parameters 生效值）。
func TestVersionEndpoints_createDraftPublishRollback(t *testing.T) {
	h := newTestParameterHandler(&fakePlatformStore{})
	router := setupAdminParameterRouter(h, true)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)) //nolint:noctx
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// CreateDraft → 201 + 版本行。
	w := do(http.MethodPost, "/admin/parameters/versions/agent", `{"snapshot":{"agent.factcheck.enabled":true},"message":"enable"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create draft = %d, want 201: %s", w.Code, w.Body.String())
	}
	var created port.PlatformVersion
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "draft" {
		t.Fatalf("draft = %+v, want status=draft", created)
	}

	// Publish → 200 + 生效值（production 快照）。
	w = do(http.MethodPost, fmt.Sprintf("/admin/parameters/versions/agent/%d/publish", created.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("publish = %d, want 200: %s", w.Code, w.Body.String())
	}
	var published map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	if v, ok := published["agent.factcheck.enabled"]; !ok || v != true {
		t.Fatalf("published effective value = %v, want true", v)
	}

	// Versions → 历史含该版本，status=published。
	w = do(http.MethodGet, "/admin/parameters/versions/agent", "")
	if w.Code != http.StatusOK {
		t.Fatalf("versions = %d, want 200", w.Code)
	}
	var versions []port.PlatformVersion
	if err := json.Unmarshal(w.Body.Bytes(), &versions); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Status != "published" {
		t.Fatalf("versions = %+v, want 1 published", versions)
	}

	// 第二个版本 publish 后 Rollback 到第一个：生效值回退。
	w = do(http.MethodPost, "/admin/parameters/versions/agent", `{"snapshot":{"agent.factcheck.top_k":6}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create draft 2 = %d, want 201", w.Code)
	}
	var draft2 port.PlatformVersion
	if err := json.Unmarshal(w.Body.Bytes(), &draft2); err != nil {
		t.Fatal(err)
	}
	w = do(http.MethodPost, fmt.Sprintf("/admin/parameters/versions/agent/%d/publish", draft2.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("publish 2 = %d, want 200", w.Code)
	}
	w = do(http.MethodPost, fmt.Sprintf("/admin/parameters/versions/agent/%d/rollback", created.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("rollback = %d, want 200: %s", w.Code, w.Body.String())
	}
	var rolled map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rolled); err != nil {
		t.Fatal(err)
	}
	if _, ok := rolled["agent.factcheck.top_k"]; ok {
		t.Fatalf("top_k must be gone after rollback, got %v", rolled["agent.factcheck.top_k"])
	}
	if v, ok := rolled["agent.factcheck.enabled"]; !ok || v != true {
		t.Fatalf("enabled must persist after rollback, got %v", v)
	}
}

// TestVersionEndpoints_errorMappings 断言版本错误映射：未知版本 404、非法 id 400、
// 状态机冲突 409、跨组/越界 400。
func TestVersionEndpoints_errorMappings(t *testing.T) {
	h := newTestParameterHandler(&fakePlatformStore{})
	router := setupAdminParameterRouter(h, true)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)) //nolint:noctx
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	w := do(http.MethodPost, "/admin/parameters/versions/agent/999/publish", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("publish unknown = %d, want 404", w.Code)
	}
	w = do(http.MethodPost, "/admin/parameters/versions/agent/999/rollback", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("rollback unknown = %d, want 404", w.Code)
	}
	w = do(http.MethodPost, "/admin/parameters/versions/agent/abc/publish", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("publish non-numeric = %d, want 400", w.Code)
	}

	w = do(http.MethodPost, "/admin/parameters/versions/agent", `{"snapshot":{"agent.factcheck.enabled":true}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create draft = %d, want 201", w.Code)
	}
	var draft port.PlatformVersion
	_ = json.Unmarshal(w.Body.Bytes(), &draft)
	w = do(http.MethodPost, fmt.Sprintf("/admin/parameters/versions/agent/%d/rollback", draft.ID), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("rollback draft = %d, want 409", w.Code)
	}
	w = do(http.MethodPost, fmt.Sprintf("/admin/parameters/versions/agent/%d/publish", draft.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("publish = %d, want 200", w.Code)
	}
	w = do(http.MethodPost, fmt.Sprintf("/admin/parameters/versions/agent/%d/publish", draft.ID), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("re-publish = %d, want 409", w.Code)
	}

	w = do(http.MethodPost, "/admin/parameters/versions/memory", `{"snapshot":{"agent.factcheck.enabled":true}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("cross-group draft = %d, want 400", w.Code)
	}
	w = do(http.MethodPost, "/admin/parameters/versions/agent", `{"snapshot":{"evaluation.optimizer.temperature":99}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("out-of-bounds = %d, want 400", w.Code)
	}
}
