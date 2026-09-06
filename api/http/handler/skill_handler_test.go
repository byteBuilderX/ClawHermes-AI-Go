package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gen "github.com/byteBuilderX/stratum/api/http/dto/gen"
	"github.com/byteBuilderX/stratum/api/middleware"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	"github.com/byteBuilderX/stratum/internal/skill/domain"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// fakeSkillRevisionService 记录 create/save/rollback 入参；create 与 save 都返回
// 已发布的 active 版本（保存即生效，无 draft）。
type fakeSkillRevisionService struct {
	created    skillapp.CreateSkillInput
	saved      skillapp.SaveRevisionInput
	savedDraft skillapp.SaveDraftInput
	gotHash    string
	rolledBack []string
}

func (f *fakeSkillRevisionService) CreateSkill(_ context.Context, input skillapp.CreateSkillInput) (skillapp.SkillWorkspaceView, error) {
	f.created = input
	return skillapp.SkillWorkspaceView{
		Skill: skillapp.SkillProduct{ID: "skill-1", Name: input.Name, Description: input.Description,
			Status: "published", ActiveRevisionID: "revision-1"},
		Active: domain.SkillRevision{
			ID: "revision-1", SkillID: "skill-1", RevisionNo: 1, Status: domain.VersionStatusPublished,
			Name: input.Name, Description: input.Description, Instructions: input.Instructions,
		},
	}, nil
}
func (f *fakeSkillRevisionService) GetWorkspace(ctx context.Context, _, _ string) (skillapp.SkillWorkspaceView, error) {
	return f.CreateSkill(ctx, skillapp.CreateSkillInput{Name: "complaint", Description: "分类", Instructions: "分类投诉"})
}
func (f *fakeSkillRevisionService) ListSkills(context.Context) ([]skillapp.SkillProduct, error) {
	return []skillapp.SkillProduct{{ID: "skill-1", Name: "complaint", Status: "published"}}, nil
}
func (f *fakeSkillRevisionService) DeleteSkill(context.Context, string, string) error { return nil }
func (f *fakeSkillRevisionService) SaveRevision(_ context.Context, _, hash string, input skillapp.SaveRevisionInput) (skillapp.SkillWorkspaceView, error) {
	f.saved = input
	f.gotHash = hash
	return skillapp.SkillWorkspaceView{
		Skill: skillapp.SkillProduct{ID: "skill-1", Name: input.Name, Description: input.Description,
			Status: "published", ActiveRevisionID: "revision-2"},
		Active: domain.SkillRevision{
			ID: "revision-2", SkillID: "skill-1", RevisionNo: 2, Status: domain.VersionStatusPublished,
			Name: input.Name, Description: input.Description, Instructions: input.Instructions,
		},
	}, nil
}
func (f *fakeSkillRevisionService) ListRevisions(context.Context, string) ([]skillapp.SkillRevision, error) {
	return []skillapp.SkillRevision{
		{ID: "revision-2", SkillID: "skill-1", RevisionNo: 2, ParentRevisionID: "revision-1",
			Status: domain.VersionStatusPublished, IsCurrent: true, CreatedBy: "u-2", CreatedByName: "Bob",
			Name: "新名字", Description: "新描述"},
		{ID: "revision-1", SkillID: "skill-1", RevisionNo: 1, ParentRevisionID: "",
			Status: domain.VersionStatusDeprecated, CreatedBy: "u-1", CreatedByName: "Alice",
			Name: "旧名字", Description: "旧描述"},
	}, nil
}
func (f *fakeSkillRevisionService) RollbackRevision(_ context.Context, _, revisionID, _ string) error {
	f.rolledBack = append(f.rolledBack, revisionID)
	return nil
}
func (f *fakeSkillRevisionService) SetEditors(context.Context, string, string, []string) error {
	return nil
}
func (f *fakeSkillRevisionService) SaveDraft(_ context.Context, _, hash string, input skillapp.SaveDraftInput) (skillapp.SkillWorkspaceView, error) {
	f.savedDraft = input
	f.gotHash = hash
	return skillapp.SkillWorkspaceView{
		Skill: skillapp.SkillProduct{ID: "skill-1", Name: input.Name, Description: input.Description,
			Status: "published", ActiveRevisionID: "revision-1", DraftRevisionID: "draft-1"},
		Active: domain.SkillRevision{
			ID: "revision-1", SkillID: "skill-1", RevisionNo: 1, Status: domain.VersionStatusPublished,
			Name: "complaint", Description: "分类", Instructions: "分类投诉",
		},
		HasDraft: true,
	}, nil
}
func (f *fakeSkillRevisionService) PublishDraft(_ context.Context, _, hash, _ string) (skillapp.SkillWorkspaceView, error) {
	f.gotHash = hash
	return skillapp.SkillWorkspaceView{
		Skill: skillapp.SkillProduct{ID: "skill-1", Name: "draft-name", Description: "draft-desc",
			Status: "published", ActiveRevisionID: "draft-1"},
		Active: domain.SkillRevision{
			ID: "draft-1", SkillID: "skill-1", RevisionNo: 2, Status: domain.VersionStatusPublished,
			Name: "draft-name", Description: "draft-desc", Instructions: "draft-ins",
		},
	}, nil
}
func (f *fakeSkillRevisionService) DiscardDraft(_ context.Context, _, _ string) (skillapp.SkillWorkspaceView, error) {
	return skillapp.SkillWorkspaceView{
		Skill: skillapp.SkillProduct{ID: "skill-1", Name: "complaint", Description: "分类",
			Status: "published", ActiveRevisionID: "revision-1"},
		Active: domain.SkillRevision{
			ID: "revision-1", SkillID: "skill-1", RevisionNo: 1, Status: domain.VersionStatusPublished,
			Name: "complaint", Description: "分类", Instructions: "分类投诉",
		},
	}, nil
}

func newSkillTestRouter(method, path string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler(zap.NewNop()))
	// Write handlers resolve the actor via ContextKeySub; without it they
	// would all 401 before reaching the service fake.
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeySub, "user-1")
		c.Next()
	})
	switch method {
	case http.MethodGet:
		router.GET(path, handler)
	case http.MethodPost:
		router.POST(path, handler)
	case http.MethodPut:
		router.PUT(path, handler)
	case http.MethodPatch:
		router.PATCH(path, handler)
	case http.MethodDelete:
		router.DELETE(path, handler)
	}
	return router
}

func TestSkillHandlerCreateSkill(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodPost, "/skills", handler.CreateSkill)
	body, _ := json.Marshal(gen.CreateSkillRequest{
		Name: "投诉分类", Description: "分类用户投诉", Instructions: "根据规则分类",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/skills", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if service.created.Instructions != "根据规则分类" || service.created.Description != "分类用户投诉" {
		t.Fatalf("create input not forwarded: %#v", service.created)
	}
}

func TestSkillHandlerRejectsIncompleteCreate(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodPost, "/skills", handler.CreateSkill)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/skills", bytes.NewBufferString(`{"name":"legacy"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected missing required fields to return 400, got %d", w.Code)
	}
}

func TestSkillHandlerSetEditors(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodPut, "/skills/:id/editors", handler.SetSkillEditors)
	body := bytes.NewBufferString(`{"editorIds":["user-2","user-3"]}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/skills/skill-1/editors", body)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestSkillHandlerUpdateSkill(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodPatch, "/skills/:id", handler.UpdateSkill)
	body, _ := json.Marshal(gen.UpdateSkillRequest{
		Name: "投诉分类", Description: "分类用户投诉", Instructions: "新方法",
		ExpectedContentHash: "h-1",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/skills/skill-1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if service.saved.Instructions != "新方法" || service.saved.Description != "分类用户投诉" {
		t.Fatalf("save input not forwarded: %#v", service.saved)
	}
	if service.gotHash != "h-1" {
		t.Fatalf("expectedContentHash not forwarded: %q", service.gotHash)
	}
}

func TestSkillHandlerListRevisions(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodGet, "/skills/:id/revisions", handler.ListSkillRevisions)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/skills/skill-1/revisions", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp gen.SkillRevisionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode revisions: %v", err)
	}
	if len(resp.Revisions) != 2 || resp.Revisions[0].ID != "revision-2" || !resp.Revisions[0].IsCurrent {
		t.Fatalf("unexpected revisions: %+v", resp.Revisions)
	}
	// parentRevisionId：revision-2 自链到 revision-1；首版 revision-1 为空串。
	if resp.Revisions[0].ParentRevisionID != "revision-1" || resp.Revisions[1].ParentRevisionID != "" {
		t.Fatalf("parentRevisionId not forwarded: %+v", resp.Revisions)
	}
	// 操作者昵称与完整编辑面透传（列表行已含 content，Drawer 直接基于行数据 diff）。
	if resp.Revisions[0].CreatedByName != "Bob" || resp.Revisions[1].CreatedByName != "Alice" {
		t.Fatalf("createdByName not forwarded: %+v", resp.Revisions)
	}
	if resp.Revisions[0].Description != "新描述" || resp.Revisions[1].Description != "旧描述" {
		t.Fatalf("edit surface not forwarded: %+v", resp.Revisions)
	}
}

func TestSkillHandlerRollbackSkill(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodPost, "/skills/:id/rollback", handler.RollbackSkill)
	body := bytes.NewBufferString(`{"revisionId":"revision-1"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/skills/skill-1/rollback", body)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(service.rolledBack) != 1 || service.rolledBack[0] != "revision-1" {
		t.Fatalf("rollback target not forwarded: %v", service.rolledBack)
	}
}

func TestSkillHandlerSaveSkillDraft(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodPost, "/skills/:id/draft", handler.SaveSkillDraft)
	body, _ := json.Marshal(gen.SaveSkillDraftRequest{
		Name: "draft-name", Description: "draft-desc", Instructions: "draft-ins",
		ExpectedContentHash: "h-1",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/skills/skill-1/draft", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if service.savedDraft.Instructions != "draft-ins" || service.savedDraft.Name != "draft-name" {
		t.Fatalf("draft input not forwarded: %#v", service.savedDraft)
	}
	if service.gotHash != "h-1" {
		t.Fatalf("expectedContentHash not forwarded: %q", service.gotHash)
	}
	var resp gen.SkillWorkspaceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	if !resp.HasDraft {
		t.Fatalf("expected HasDraft=true, got false")
	}
}

func TestSkillHandlerPublishSkillDraft(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodPost, "/skills/:id/publish", handler.PublishSkillDraft)
	body, _ := json.Marshal(gen.PublishSkillDraftRequest{ExpectedContentHash: "h-1"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/skills/skill-1/publish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if service.gotHash != "h-1" {
		t.Fatalf("expectedContentHash not forwarded: %q", service.gotHash)
	}
}

func TestSkillHandlerDiscardSkillDraft(t *testing.T) {
	service := &fakeSkillRevisionService{}
	handler := NewSkillHandler(service, zap.NewNop())
	router := newSkillTestRouter(http.MethodDelete, "/skills/:id/draft", handler.DiscardSkillDraft)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/skills/skill-1/draft", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}
