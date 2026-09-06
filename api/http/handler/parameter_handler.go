package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/byteBuilderX/stratum/api/middleware"
	paramapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	"github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// PublishGateFunc 是 Publish 前置发布闸（nil = 未装配，保持裸发布；Task 5 装配编排器，
// 行为默认不变——gate.enabled=false 返回 passthrough）。
// decision ∈ {"passthrough","approval_pending","blocked","refused_not_wired"}
type PublishGateFunc func(ctx context.Context, groupKey string, versionID int64, actor string) (decision, message, runID string, err error)

// ParameterHandler exposes the unified parameter registry under /admin/parameters.
type ParameterHandler struct {
	svc         *paramapp.Service
	logger      *zap.Logger
	publishGate PublishGateFunc
}

func (h *ParameterHandler) SetPublishGate(g PublishGateFunc) { h.publishGate = g }

func NewParameterHandler(svc *paramapp.Service, logger *zap.Logger) *ParameterHandler {
	return &ParameterHandler{svc: svc, logger: logger}
}

// Schema GET /admin/parameters/schema — all definitions for schema-driven
// frontend rendering (value layer stays separate).
func (h *ParameterHandler) Schema(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.Schema())
}

// List GET /admin/parameters — current effective platform-layer values.
func (h *ParameterHandler) List(c *gin.Context) {
	values, err := h.svc.PlatformValues(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, values)
}

// Update PUT /admin/parameters — merge-write platform values. Only keys
// present in the body are touched; every key must be platform-scope and pass
// its definition validation.
func (h *ParameterHandler) Update(c *gin.Context) {
	var body map[string]json.RawMessage
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}

	values := make(map[string]any, len(body))
	for key, raw := range body {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid value for " + key})
			return
		}
		values[key] = value
	}

	updatedBy := c.GetString(middleware.ContextKeySub)
	if err := h.svc.SetPlatformValues(c.Request.Context(), values, updatedBy); err != nil {
		var bad *domain.ErrInvalidParameter
		if ok := domain.AsInvalidParameter(err, &bad); ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": bad.Error()})
			return
		}
		_ = c.Error(err)
		return
	}
	updated, err := h.svc.PlatformValues(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// createDraftRequest is the POST /admin/parameters/versions/:groupKey body.
// snapshot carries the platform-scope keys to change (merged over the current
// production snapshot); message is the operator's human-readable note.
type createDraftRequest struct {
	Snapshot map[string]any `json:"snapshot"`
	Message  string         `json:"message"`
}

// Versions GET /admin/parameters/versions/:groupKey — the full version history
// (newest first) with each immutable snapshot; the configuration audit view
// diffs against base_version_id.
func (h *ParameterHandler) Versions(c *gin.Context) {
	versions, err := h.svc.Versions(c.Request.Context(), c.Param("groupKey"))
	if err != nil {
		h.renderVersionError(c, err)
		return
	}
	if versions == nil {
		versions = []port.PlatformVersion{}
	}
	c.JSON(http.StatusOK, versions)
}

// CreateDraft POST /admin/parameters/versions/:groupKey — validates and stores
// a draft snapshot for the group (the only editable state). Actor comes from
// the JWT; the body never carries by/tenant.
func (h *ParameterHandler) CreateDraft(c *gin.Context) {
	var body createDraftRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	actor := c.GetString(middleware.ContextKeySub)
	version, err := h.svc.CreateDraft(c.Request.Context(), c.Param("groupKey"), body.Snapshot, body.Message, actor)
	if err != nil {
		var bad *domain.ErrInvalidParameter
		if ok := domain.AsInvalidParameter(err, &bad); ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": bad.Error()})
			return
		}
		h.renderVersionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, version)
}

// Publish POST /admin/parameters/versions/:groupKey/:versionID/publish —
// promotes a draft to published and moves the production/latest labels.
// 装配发布闸后先询问编排器：非 passthrough 一律不触 h.svc.Publish（fail-closed，
// 不静默直发）；nil 闸（默认）= 未装配 → 维持现状裸发布。
func (h *ParameterHandler) Publish(c *gin.Context) {
	versionID, ok := parseVersionID(c)
	if !ok {
		return
	}
	groupKey := c.Param("groupKey")
	actor := c.GetString(middleware.ContextKeySub)
	if h.publishGate != nil {
		decision, message, runID, err := h.publishGate(c.Request.Context(), groupKey, versionID, actor)
		if err != nil {
			_ = c.Error(err) // 编排器内部错误 → 统一 500；不直发
			return
		}
		switch decision {
		case "passthrough": // gate 关闭：落回裸发布（默认语义，行为与现状一致）
		case "approval_pending":
			c.JSON(http.StatusAccepted, gin.H{"status": "sentinel_pending", "run_id": runID, "message": message})
			return
		case "blocked", "refused_not_wired":
			c.JSON(http.StatusConflict, gin.H{"error": message})
			return
		default:
			_ = c.Error(fmt.Errorf("unknown publish gate decision %q", decision))
			return
		}
	}
	if err := h.svc.Publish(c.Request.Context(), groupKey, versionID, actor); err != nil {
		h.renderVersionError(c, err)
		return
	}
	// 返回生效值（production 快照），与 GET /admin/parameters 一致。
	values, err := h.svc.PlatformValues(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, values)
}

// Rollback POST /admin/parameters/versions/:groupKey/:versionID/rollback —
// moves the production/latest labels onto a historical published version.
func (h *ParameterHandler) Rollback(c *gin.Context) {
	versionID, ok := parseVersionID(c)
	if !ok {
		return
	}
	if err := h.svc.Rollback(c.Request.Context(), c.Param("groupKey"), versionID, c.GetString(middleware.ContextKeySub)); err != nil {
		h.renderVersionError(c, err)
		return
	}
	values, err := h.svc.PlatformValues(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, values)
}

// parseVersionID extracts the :versionID path parameter as int64, emitting a
// 400 on malformed input.
func parseVersionID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("versionID"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid version id"})
		return 0, false
	}
	return id, true
}

// renderVersionError maps version-domain errors to HTTP statuses; everything
// else falls through to the unified error middleware (500).
func (h *ParameterHandler) renderVersionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrGroupNotFound), errors.Is(err, domain.ErrVersionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, domain.ErrVersionNotDraft), errors.Is(err, domain.ErrVersionNotPublished):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, domain.ErrConcurrentPublish):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		_ = c.Error(err)
	}
}
