package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	knowledge "github.com/byteBuilderX/stratum/internal/knowledge/application"
	"github.com/byteBuilderX/stratum/internal/knowledge/domain"
	versioningdomain "github.com/byteBuilderX/stratum/internal/versioning/domain"
)

// versionHandlerWorkspaceRepo 满足 WorkspaceRepo port：GetByName/GetByID 返回
// 固定 workspace，其余 no-op。restored 非 nil 时 GetByID 返回回滚后的 workspace
// （模拟 repo 事务把版本快照写回行后重读的最新状态）。
type versionHandlerWorkspaceRepo struct {
	ws       *domain.Workspace
	restored *domain.Workspace
	// getErr 非 nil 时 GetByName 返回该错误（模拟 workspace 缺失等读失败路径）。
	getErr error
}

func (r *versionHandlerWorkspaceRepo) Create(
	context.Context, string, *domain.Workspace, []string, *auditdomain.ResourceChangeAuditEvent,
) error {
	return nil
}
func (r *versionHandlerWorkspaceRepo) GetByName(context.Context, string, string) (*domain.Workspace, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.ws, nil
}
func (r *versionHandlerWorkspaceRepo) GetByID(context.Context, string, string) (*domain.Workspace, error) {
	if r.restored != nil {
		return r.restored, nil
	}
	return r.ws, nil
}
func (r *versionHandlerWorkspaceRepo) List(context.Context, string) ([]*domain.Workspace, error) {
	return nil, nil
}
func (r *versionHandlerWorkspaceRepo) UpdateWorkspaceAll(
	context.Context, string, string, *string, *string, domain.KnowledgeWorkspaceSnapshot,
	string, string, *auditdomain.ResourceChangeAuditEvent,
) error {
	return nil
}
func (r *versionHandlerWorkspaceRepo) RollbackWorkspace(
	context.Context, string, string, domain.KnowledgeWorkspaceSnapshot, string, string,
	*auditdomain.ResourceChangeAuditEvent,
) error {
	return nil
}
func (r *versionHandlerWorkspaceRepo) Delete(
	context.Context, string, string, *auditdomain.ResourceChangeAuditEvent,
) error {
	return nil
}
func (r *versionHandlerWorkspaceRepo) GetConfigForUpload(context.Context, string, string) (domain.WorkspaceConfig, error) {
	return r.ws.Config, nil
}
func (r *versionHandlerWorkspaceRepo) GetConfigByID(context.Context, string, string) (domain.WorkspaceConfig, error) {
	return r.ws.Config, nil
}

// versionStubRepo 满足 versioningport.VersionRepo，行为与 application 包的
// stubVersionRepo 对齐（不同 package，各自独立定义）。
type versionStubRepo struct {
	versions []versioningdomain.Version
	get      func(ctx context.Context, tenantID string, kind versioningdomain.ResourceKind, resourceID, versionID string) (versioningdomain.Version, bool, error)
}

func (s *versionStubRepo) ListVersions(
	ctx context.Context, tenantID string, kind versioningdomain.ResourceKind, resourceID string,
) ([]versioningdomain.Version, error) {
	return s.versions, nil
}

func (s *versionStubRepo) GetVersion(
	ctx context.Context, tenantID string, kind versioningdomain.ResourceKind, resourceID, versionID string,
) (versioningdomain.Version, bool, error) {
	if s.get != nil {
		return s.get(ctx, tenantID, kind, resourceID, versionID)
	}
	return versioningdomain.Version{}, false, nil
}

func TestRAGHandlerListWorkspaceVersions(t *testing.T) {
	svc := knowledge.NewWorkspaceService(
		&versionHandlerWorkspaceRepo{ws: &domain.Workspace{ID: "ws-1", Name: "kb"}}, nil, zap.NewNop())
	// v2 自链到 v1（parentVersionId），首版 v1 为空串；昵称解析覆盖操作者展示名。
	svc.SetActorNameResolver(&fakeHandlerActorNames{names: map[string]string{"u1": "Alice", "u2": "Bob"}})
	svc.SetVersionRepo(&versionStubRepo{versions: []versioningdomain.Version{
		{ID: "v2", RevisionNo: 2, ParentVersionID: "v1", ResourceKind: versioningdomain.ResourceKindKnowledge,
			Status: versioningdomain.VersionStatusPublished, Source: versioningdomain.VersionSourceManual,
			CreatedBy: "u1", SafeSummary: map[string]any{"name": "kb"}},
		{ID: "v1", RevisionNo: 1, ResourceKind: versioningdomain.ResourceKindKnowledge,
			Status: versioningdomain.VersionStatusDeprecated, Source: versioningdomain.VersionSourceManual,
			CreatedBy: "u2", SafeSummary: map[string]any{"name": "kb"}},
	}})

	h := NewRAGHandler(nil, svc, zap.NewNop())
	r := newRouterWithErrorHandler()
	r.GET("/knowledge/workspaces/:name/versions", injectRAGTenant("tenant-1"), h.ListWorkspaceVersions)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/knowledge/workspaces/kb/versions", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body WorkspaceVersionsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Versions, 2)
	require.Equal(t, "v2", body.Versions[0].ID)
	require.Equal(t, 2, body.Versions[0].VersionNo)
	// parentVersionId：v2 自链到 v1；首版 v1 为空串。
	require.Equal(t, "v1", body.Versions[0].ParentVersionID)
	require.Equal(t, "", body.Versions[1].ParentVersionID)
	// 操作者展示名：id → display_name（u1→Alice, u2→Bob）。
	require.Equal(t, "Alice", body.Versions[0].CreatedByName)
	require.Equal(t, "Bob", body.Versions[1].CreatedByName)
}

func TestRAGHandlerGetWorkspaceVersion(t *testing.T) {
	// 整份编辑面 payload（domain.SnapshotFromWorkspace().Map()：name/description/
	// config 嵌套键），由「详情」Drawer 与直父版本 payload 递归 diff 叶子字段。
	ws := &domain.Workspace{ID: "ws-1", Name: "kb"}
	v2 := versioningdomain.Version{
		ID: "v2", RevisionNo: 2, ParentVersionID: "v1", ResourceKind: versioningdomain.ResourceKindKnowledge,
		Status: versioningdomain.VersionStatusPublished, Source: versioningdomain.VersionSourceManual,
		ContentHash: "h2", CreatedBy: "u1", SafeSummary: map[string]any{"name": "kb"},
		Payload: domain.SnapshotFromWorkspace(&domain.Workspace{
			Name: "kb", Description: "desc", Config: domain.WorkspaceConfig{TopK: 8},
		}).Map(),
	}
	svc := knowledge.NewWorkspaceService(&versionHandlerWorkspaceRepo{ws: ws}, nil, zap.NewNop())
	svc.SetActorNameResolver(&fakeHandlerActorNames{names: map[string]string{"u1": "Alice"}})
	svc.SetVersionRepo(&versionStubRepo{get: func(ctx context.Context, tenantID string, kind versioningdomain.ResourceKind, resourceID, versionID string) (versioningdomain.Version, bool, error) {
		if versionID == "v2" {
			return v2, true, nil
		}
		return versioningdomain.Version{}, false, nil
	}})

	h := NewRAGHandler(nil, svc, zap.NewNop())
	r := newRouterWithErrorHandler()
	r.GET("/knowledge/workspaces/:name/versions/:versionID", injectRAGTenant("tenant-1"), h.GetWorkspaceVersion)

	// 成功 → 200：列表元数据 + parentVersionId + 昵称 + 整份 payload。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/knowledge/workspaces/kb/versions/v2", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.Contains(t, body, `"id":"v2"`)
	require.Contains(t, body, `"versionNo":2`)
	require.Contains(t, body, `"parentVersionId":"v1"`)
	require.Contains(t, body, `"createdByName":"Alice"`)
	require.Contains(t, body, `"payload":{`)
	require.Contains(t, body, `"name":"kb"`)
	require.Contains(t, body, `"description":"desc"`)

	// 极端情况：缺 tenant → 401。
	r2 := newRouterWithErrorHandler()
	r2.GET("/knowledge/workspaces/:name/versions/:versionID", h.GetWorkspaceVersion)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/knowledge/workspaces/kb/versions/v2", nil)
	r2.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 极端情况：版本不存在 → 404（GetVersion found=false → ErrVersionNotFound）。
	svc.SetVersionRepo(&versionStubRepo{})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/knowledge/workspaces/kb/versions/nope", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：workspace 不存在 → 404（ErrWorkspaceNotFound → middleware）。
	svc2 := knowledge.NewWorkspaceService(
		&versionHandlerWorkspaceRepo{getErr: domain.ErrWorkspaceNotFound}, nil, zap.NewNop())
	svc2.SetVersionRepo(&versionStubRepo{get: func(ctx context.Context, tenantID string, kind versioningdomain.ResourceKind, resourceID, versionID string) (versioningdomain.Version, bool, error) {
		return v2, true, nil
	}})
	h2 := NewRAGHandler(nil, svc2, zap.NewNop())
	r3 := newRouterWithErrorHandler()
	r3.GET("/knowledge/workspaces/:name/versions/:versionID", injectRAGTenant("tenant-1"), h2.GetWorkspaceVersion)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/knowledge/workspaces/missing/versions/v2", nil)
	r3.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 极端情况：版本基座查询失败 → 500。
	svc.SetVersionRepo(&versionStubRepo{get: func(ctx context.Context, tenantID string, kind versioningdomain.ResourceKind, resourceID, versionID string) (versioningdomain.Version, bool, error) {
		return versioningdomain.Version{}, false, errors.New("db down")
	}})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/knowledge/workspaces/kb/versions/v2", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// 极端情况：版本基座未装配（nil）→ fail-closed 500。
	svc3 := knowledge.NewWorkspaceService(&versionHandlerWorkspaceRepo{ws: ws}, nil, zap.NewNop())
	h3 := NewRAGHandler(nil, svc3, zap.NewNop())
	r4 := newRouterWithErrorHandler()
	r4.GET("/knowledge/workspaces/:name/versions/:versionID", injectRAGTenant("tenant-1"), h3.GetWorkspaceVersion)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/knowledge/workspaces/kb/versions/v2", nil)
	r4.ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestRAGHandlerRollbackWorkspace(t *testing.T) {
	ws := &domain.Workspace{ID: "ws-1", Name: "kb", Description: "d", Config: domain.WorkspaceConfig{TopK: 8}}
	repo := &versionHandlerWorkspaceRepo{
		ws: ws,
		// 回滚后 repo 事务把版本快照写回行，GetByID 重读返回恢复后的 workspace。
		restored: &domain.Workspace{ID: "ws-1", Name: "old", Description: "od", Config: domain.WorkspaceConfig{TopK: 4}},
	}
	svc := knowledge.NewWorkspaceService(repo, nil, zap.NewNop())
	// 回滚沿用更新矩阵（fail-closed）：注入 owner 角色使写入路径可达。
	svc.SetTenantRoleResolver(fixedTenantRole{role: "owner"})
	svc.SetVersionRepo(&versionStubRepo{get: func(ctx context.Context, tenantID string, kind versioningdomain.ResourceKind, resourceID, versionID string) (versioningdomain.Version, bool, error) {
		return versioningdomain.Version{ID: "v1", Status: versioningdomain.VersionStatusDeprecated,
			Payload: domain.SnapshotFromWorkspace(&domain.Workspace{Name: "old", Description: "od", Config: domain.WorkspaceConfig{TopK: 4}}).Map()}, true, nil
	}})

	h := NewRAGHandler(nil, svc, zap.NewNop())
	r := newRouterWithErrorHandler()
	r.POST("/knowledge/workspaces/:name/rollback", injectRAGTenant("tenant-1"), h.RollbackWorkspace)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/knowledge/workspaces/kb/rollback", strings.NewReader(`{"versionId":"v1"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "old", body["name"])
}
