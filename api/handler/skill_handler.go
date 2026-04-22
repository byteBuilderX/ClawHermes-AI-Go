package handler

import (
	"net/http"
	"time"

	"clawhermes-ai-go/api/model"
	"clawhermes-ai-go/internal/llmgateway"
	"clawhermes-ai-go/internal/orchestrator"
	"clawhermes-ai-go/internal/skill"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type SkillHandler struct {
	registry *orchestrator.Registry
	logger   *zap.Logger
	gateway  *llmgateway.Gateway
}

func NewSkillHandler(registry *orchestrator.Registry, logger *zap.Logger, gateway *llmgateway.Gateway) *SkillHandler {
	return &SkillHandler{
		registry: registry,
		logger:   logger,
		gateway:  gateway,
	}
}

func (h *SkillHandler) CreateSkill(c *gin.Context) {
	var req model.CreateSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Warn("invalid request", zap.Error(err))
		c.JSON(http.StatusBadRequest, model.ErrorResponse{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	id := uuid.New().String()
	var s skill.Skill

	switch req.Type {
	case "code":
		s = skill.NewCodeSkill(id, req.Name, req.Description, req.Code, req.Language)
	case "llm":
		s = skill.NewLLMSkill(id, req.Name, req.Description, h.gateway, h.logger)
	default:
		s = &skill.BaseSkill{
			ID:          id,
			Name:        req.Name,
			Description: req.Description,
			Type:        req.Type,
		}
	}

	h.registry.Register(id, s)
	h.logger.Info("skill created", zap.String("id", id), zap.String("name", req.Name))

	c.JSON(http.StatusCreated, model.SkillResponse{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Type:        req.Type,
		CreatedAt:   time.Now().Format(time.RFC3339),
	})
}

func (h *SkillHandler) GetSkill(c *gin.Context) {
	id := c.Param("id")
	s, ok := h.registry.Get(id)
	if !ok {
		h.logger.Warn("skill not found", zap.String("id", id))
		c.JSON(http.StatusNotFound, model.ErrorResponse{
			Code:    http.StatusNotFound,
			Message: "skill not found",
		})
		return
	}

	c.JSON(http.StatusOK, model.SkillResponse{
		ID:          s.GetID(),
		Name:        s.GetName(),
		Description: s.GetDescription(),
		Type:        s.GetType(),
		CreatedAt:   time.Now().Format(time.RFC3339),
	})
}

func (h *SkillHandler) ExecuteSkill(c *gin.Context) {
	id := c.Param("id")
	var req model.ExecuteSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Warn("invalid request", zap.Error(err))
		c.JSON(http.StatusBadRequest, model.ErrorResponse{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	s, ok := h.registry.Get(id)
	if !ok {
		h.logger.Warn("skill not found", zap.String("id", id))
		c.JSON(http.StatusNotFound, model.ErrorResponse{
			Code:    http.StatusNotFound,
			Message: "skill not found",
		})
		return
	}

	executor, ok := s.(skill.SkillExecutor)
	if !ok {
		h.logger.Error("skill is not executable", zap.String("id", id))
		c.JSON(http.StatusBadRequest, model.ErrorResponse{
			Code:    http.StatusBadRequest,
			Message: "skill is not executable",
		})
		return
	}

	result, err := executor.Execute(req.Input)
	if err != nil {
		h.logger.Error("skill execution failed", zap.String("id", id), zap.Error(err))
		c.JSON(http.StatusInternalServerError, model.ExecuteSkillResponse{
			Error: err.Error(),
		})
		return
	}

	h.logger.Info("skill executed", zap.String("id", id))
	c.JSON(http.StatusOK, model.ExecuteSkillResponse{
		Result: result,
	})
}
