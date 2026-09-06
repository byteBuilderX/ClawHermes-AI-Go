package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	gen "github.com/byteBuilderX/stratum/api/http/dto/gen"
	"github.com/byteBuilderX/stratum/api/middleware"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type SkillHandler struct {
	service skillRevisionService
	logger  *zap.Logger
}

type skillRevisionService interface {
	CreateSkill(context.Context, skillapp.CreateSkillInput) (skillapp.SkillWorkspaceView, error)
	GetWorkspace(context.Context, string, string) (skillapp.SkillWorkspaceView, error)
	ListSkills(context.Context) ([]skillapp.SkillProduct, error)
	DeleteSkill(context.Context, string, string) error
	SaveRevision(context.Context, string, string, skillapp.SaveRevisionInput) (skillapp.SkillWorkspaceView, error)
	ListRevisions(context.Context, string) ([]skillapp.SkillRevision, error)
	RollbackRevision(context.Context, string, string, string) error
	SetEditors(context.Context, string, string, []string) error
	SaveDraft(context.Context, string, string, skillapp.SaveDraftInput) (skillapp.SkillWorkspaceView, error)
	PublishDraft(context.Context, string, string, string) (skillapp.SkillWorkspaceView, error)
	DiscardDraft(context.Context, string, string) (skillapp.SkillWorkspaceView, error)
}

func NewSkillHandler(service skillRevisionService, logger *zap.Logger) *SkillHandler {
	return &SkillHandler{service: service, logger: logger}
}

func (h *SkillHandler) CreateSkill(c *gin.Context) {
	var req gen.CreateSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Warn("invalid instruction Skill request", zap.Error(err))
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	view, err := h.service.CreateSkill(c.Request.Context(), skillapp.CreateSkillInput{
		Name: req.Name, Description: req.Description, Instructions: req.Instructions,
		ActorID: actorID, Editors: req.Editors,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, workspaceToResponse(view))
}

func (h *SkillHandler) GetAllSkills(c *gin.Context) {
	items, err := h.service.ListSkills(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	out := make([]gen.SkillProductResponse, 0, len(items))
	for _, item := range items {
		out = append(out, productToResponse(item))
	}
	c.JSON(http.StatusOK, gin.H{"skills": out})
}

func (h *SkillHandler) GetSkill(c *gin.Context) { h.GetSkillWorkspace(c) }

func (h *SkillHandler) GetSkillWorkspace(c *gin.Context) {
	actorID, _ := userIDFromCtx(c)
	view, err := h.service.GetWorkspace(c.Request.Context(), c.Param("id"), actorID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, workspaceToResponse(view))
}

// UpdateSkill persists a new immediately-effective revision derived from the
// current active version (保存即生效, no publish step). expectedContentHash is
// the frontend's optimistic-concurrency baseline; the editor actor's
// qualification is re-validated inside the write transaction.
func (h *SkillHandler) UpdateSkill(c *gin.Context) {
	var req gen.UpdateSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	view, err := h.service.SaveRevision(c.Request.Context(), c.Param("id"), req.ExpectedContentHash,
		skillapp.SaveRevisionInput{
			Name: req.Name, Description: req.Description, Instructions: req.Instructions,
			ActorID: actorID,
		})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, workspaceToResponse(view))
}

// SaveSkillDraft persists the skill's draft without making it effective.
// expectedContentHash is the optimistic-concurrency baseline (current active
// content hash); the saved draft becomes effective only after PublishSkillDraft.
func (h *SkillHandler) SaveSkillDraft(c *gin.Context) {
	var req gen.SaveSkillDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	view, err := h.service.SaveDraft(c.Request.Context(), c.Param("id"), req.ExpectedContentHash,
		skillapp.SaveDraftInput{
			Name: req.Name, Description: req.Description, Instructions: req.Instructions,
			ActorID: actorID,
		})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, workspaceToResponse(view))
}

// PublishSkillDraft promotes the skill's draft to the new active published
// version (immediately effective).
func (h *SkillHandler) PublishSkillDraft(c *gin.Context) {
	var req gen.PublishSkillDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	view, err := h.service.PublishDraft(c.Request.Context(), c.Param("id"), req.ExpectedContentHash, actorID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, workspaceToResponse(view))
}

// DiscardSkillDraft deletes the skill's draft and returns the workspace so the
// client refills the form from the active version. No request body.
func (h *SkillHandler) DiscardSkillDraft(c *gin.Context) {
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	view, err := h.service.DiscardDraft(c.Request.Context(), c.Param("id"), actorID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, workspaceToResponse(view))
}

// ListSkillRevisions returns the skill's version history, newest first, with
// the currently effective version marked.
func (h *SkillHandler) ListSkillRevisions(c *gin.Context) {
	revisions, err := h.service.ListRevisions(c.Request.Context(), c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	out := make([]gen.SkillRevisionResponse, 0, len(revisions))
	for _, revision := range revisions {
		out = append(out, revisionToResponse(revision))
	}
	c.JSON(http.StatusOK, gen.SkillRevisionsResponse{Revisions: out})
}

// RollbackSkill repoints the skill's active_revision_id to a historical
// published revision — immediately effective without creating a new version.
func (h *SkillHandler) RollbackSkill(c *gin.Context) {
	var req gen.RollbackSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if err := h.service.RollbackRevision(c.Request.Context(), c.Param("id"), req.RevisionID, actorID); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "skill rolled back"})
}

func (h *SkillHandler) DeleteSkill(c *gin.Context) {
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if err := h.service.DeleteSkill(c.Request.Context(), c.Param("id"), actorID); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "skill deleted successfully"})
}

// SetSkillEditors replaces the granted editor set of a skill resource
// (creator/owner only; any tenant member may be granted editor, whitelist).
func (h *SkillHandler) SetSkillEditors(c *gin.Context) {
	var req struct {
		EditorIDs []string `json:"editorIds" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if err := h.service.SetEditors(c.Request.Context(), c.Param("id"), actorID, req.EditorIDs); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "editors updated"})
}

func productToResponse(value skillapp.SkillProduct) gen.SkillProductResponse {
	return gen.SkillProductResponse{
		ID: value.ID, Name: value.Name, Description: value.Description, Status: value.Status,
		ActiveRevisionID: value.ActiveRevisionID,
		// builtin: 前缀即系统内置 skill;前端据此对普通 agent 的选择列过滤。
		IsSystem: strings.HasPrefix(value.ID, "builtin:"),
	}
}

func workspaceToResponse(value skillapp.SkillWorkspaceView) gen.SkillWorkspaceResponse {
	return gen.SkillWorkspaceResponse{
		Skill: productToResponse(value.Skill), Active: revisionToResponse(value.Active),
		Editors: value.Editors, HasDraft: value.HasDraft,
	}
}

func revisionToResponse(value skillapp.SkillRevision) gen.SkillRevisionResponse {
	var createdAt string
	if !value.CreatedAt.IsZero() {
		createdAt = value.CreatedAt.UTC().Format(time.RFC3339)
	}
	//nolint:gosec // 版本号不可能溢出 int32(proto 契约)
	return gen.SkillRevisionResponse{
		ID: value.ID, SkillID: value.SkillID, RevisionNo: int32(value.RevisionNo), Status: string(value.Status),
		Name: value.Name, Description: value.Description,
		Instructions: value.Instructions, PublishChecks: value.PublishChecks,
		IsCurrent: value.IsCurrent, CreatedBy: value.CreatedBy, CreatedByName: value.CreatedByName,
		CreatedAt: createdAt, ContentHash: value.ContentHash, ParentRevisionID: value.ParentRevisionID,
	}
}
