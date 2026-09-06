// Package handler — agent_crud_handler.go.
//
// CRUD HTTP transport for /agents. Each handler binds → calls
// AgentService → renders. No registry, repo, or SQL knowledge here.
package handler

import (
	"net/http"
	"strconv"

	"github.com/byteBuilderX/stratum/api/middleware"
	agent "github.com/byteBuilderX/stratum/internal/agent/application"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *AgentHandler) GetAllAgents(c *gin.Context) {
	if _, ok := tenantIDFromCtx(c); !ok {
		respondMissingTenant(c)
		return
	}
	dtos, err := h.svc.List(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	responses := make([]AgentResponse, 0, len(dtos))
	for _, d := range dtos {
		responses = append(responses, dtoToResponse(d))
	}
	c.JSON(http.StatusOK, gin.H{"agents": responses})
}

func (h *AgentHandler) GetAgent(c *gin.Context) {
	if _, ok := tenantIDFromCtx(c); !ok {
		respondMissingTenant(c)
		return
	}
	dto, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, dtoToResponse(dto))
}

func (h *AgentHandler) CreateAgent(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req CreateAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	dto, err := h.svc.Create(c.Request.Context(), agent.CreateAgentInput{
		TenantID:                tenantID,
		ActorID:                 actorID,
		Name:                    req.Name,
		Type:                    req.Type,
		Description:             req.Description,
		SystemPrompt:            req.SystemPrompt,
		LLMModel:                req.LLMModel,
		MaxIterations:           req.MaxIterations,
		MaxContextTokens:        req.MaxContextTokens,
		Temperature:             req.Temperature,
		ReasoningEffort:         req.ReasoningEffort,
		MaxTokens:               req.MaxTokens,
		AllowedSkills:           req.AllowedSkills,
		MCPToolIDs:              req.MCPToolIDs,
		KnowledgeWorkspaceIDs:   req.KnowledgeWorkspaceIDs,
		MemoryScope:             req.MemoryScope,
		DelegateEnabled:         req.DelegateEnabled,
		DelegateMaxDepth:        req.DelegateMaxDepth,
		DelegateDefaultMaxSteps: req.DelegateDefaultMaxSteps,
		Parameters:              req.Parameters,
		Editors:                 req.Editors,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, dtoToResponse(dto))
}

// SetAgentEditors replaces the granted editor set of an agent (PUT
// /agents/:id/editors). Only creator/owner may call; eligibility of each
// editor is enforced inside the repository transaction.
func (h *AgentHandler) SetAgentEditors(c *gin.Context) {
	if _, ok := tenantIDFromCtx(c); !ok {
		respondMissingTenant(c)
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	var req struct {
		EditorIDs []string `json:"editorIds" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	if err := h.svc.SetEditors(c.Request.Context(), c.Param("id"), actorID, req.EditorIDs); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "editors updated"})
}

func (h *AgentHandler) UpdateAgent(c *gin.Context) {
	if _, ok := tenantIDFromCtx(c); !ok {
		respondMissingTenant(c)
		return
	}
	var req UpdateAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	dto, err := h.svc.Update(c.Request.Context(), c.Param("id"), agent.UpdateAgentInput{
		ActorID:                 actorID,
		Name:                    req.Name,
		Type:                    req.Type,
		Description:             req.Description,
		SystemPrompt:            req.SystemPrompt,
		LLMModel:                req.LLMModel,
		MaxIterations:           req.MaxIterations,
		MaxContextTokens:        req.MaxContextTokens,
		Temperature:             req.Temperature,
		ReasoningEffort:         req.ReasoningEffort,
		MaxTokens:               req.MaxTokens,
		Parameters:              req.Parameters,
		AllowedSkills:           req.AllowedSkills,
		MCPToolIDs:              req.MCPToolIDs,
		KnowledgeWorkspaceIDs:   req.KnowledgeWorkspaceIDs,
		MemoryScope:             req.MemoryScope,
		DelegateEnabled:         req.DelegateEnabled,
		DelegateMaxDepth:        req.DelegateMaxDepth,
		DelegateDefaultMaxSteps: req.DelegateDefaultMaxSteps,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, dtoToResponse(dto))
}

func (h *AgentHandler) DeleteAgent(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), tenantID, c.Param("id"), actorID); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "agent deleted successfully"})
}

// ListAgentVersions returns the agent's product version history (newest
// first) with created_by display names resolved.
func (h *AgentHandler) ListAgentVersions(c *gin.Context) {
	if _, ok := tenantIDFromCtx(c); !ok {
		respondMissingTenant(c)
		return
	}
	versions, err := h.svc.ListVersions(c.Request.Context(), c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	out := make([]AgentVersionResponse, 0, len(versions))
	for _, v := range versions {
		out = append(out, agentVersionToResponse(v))
	}
	c.JSON(http.StatusOK, AgentVersionsResponse{Versions: out})
}

// GetAgentVersion returns one historical version's full content snapshot
// (metadata + payload) for the "detail" drawer to diff field before/after
// values against the direct parent version. Not-found maps 404 via the
// unified error middleware.
func (h *AgentHandler) GetAgentVersion(c *gin.Context) {
	if _, ok := tenantIDFromCtx(c); !ok {
		respondMissingTenant(c)
		return
	}
	content, err := h.svc.GetVersion(c.Request.Context(), c.Param("id"), c.Param("versionID"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, agentVersionContentToResponse(content))
}

// RollbackAgent restores a deprecated historical version, repointing the
// agent to it immediately without creating a new version. Returns the fresh
// agent config so the client can re-render in place.
func (h *AgentHandler) RollbackAgent(c *gin.Context) {
	if _, ok := tenantIDFromCtx(c); !ok {
		respondMissingTenant(c)
		return
	}
	var req RollbackAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	dto, err := h.svc.Rollback(c.Request.Context(), c.Param("id"), agent.RollbackAgentInput{
		ActorID:   actorID,
		VersionID: req.VersionID,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, dtoToResponse(dto))
}

// PauseExecution marks a running execution as paused so it can be resumed later.
func (h *AgentHandler) PauseExecution(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	executionID := c.Param("executionID")
	if err := h.svc.PauseExecution(c.Request.Context(), tenantID, executionID); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "paused"})
}

// ResumeExecution restarts a paused execution from its last checkpoint.
func (h *AgentHandler) ResumeExecution(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	id := c.Param("id")
	executionID := c.Param("executionID")
	var req ExecuteAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	userID, _ := userIDFromCtx(c)

	result, _, err := h.svc.ResumeExecution(c.Request.Context(), id, agent.ExecRequest{
		Query:          req.Query,
		ConversationID: req.ConversationID,
		UserID:         userID,
		MaxSteps:       intOption(req.Options, "maxSteps"),
		Timeout:        timeoutOption(req.Options, "timeout"),
	}, agent.ExecMeta{
		TenantID: tenantID,
		TraceID:  middleware.GetTraceID(c),
	}, executionID)

	if err != nil {
		h.logger.Error("resume execution failed",
			zap.String("agentId", id),
			zap.String("executionId", executionID),
			zap.Error(err),
		)
		respondAgentExecutionError(c, err)
		return
	}
	c.JSON(http.StatusOK, agentExecutionResultDTO(result))
}

// executionRow is the wire shape rendered by ListExecutions. JSON tags
// are frozen by the contract test; do not rename.
type executionRow struct {
	ID            string `json:"id"`
	TraceID       string `json:"trace_id"`
	AgentID       string `json:"agent_id"`
	AgentName     string `json:"agent_name"`
	UserID        string `json:"user_id"`
	Status        string `json:"status"`
	InputPreview  string `json:"input_preview"`
	OutputPreview string `json:"output_preview"`
	ErrorMessage  string `json:"error_message"`
	TotalTokens   int    `json:"total_tokens"`
	DurationMs    int    `json:"duration_ms"`
	CreatedAt     string `json:"created_at"`
}

func (h *AgentHandler) ListExecutions(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	userID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	rows, total, err := h.svc.ListExecutions(c.Request.Context(), tenantID, userID, page, pageSize)
	if err != nil {
		_ = c.Error(err)
		return
	}
	out := make([]executionRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, executionRow{
			ID:            r.ID,
			TraceID:       r.TraceID,
			AgentID:       r.AgentID,
			AgentName:     r.AgentName,
			UserID:        r.UserID,
			Status:        r.Status,
			InputPreview:  r.InputPreview,
			OutputPreview: r.OutputPreview,
			ErrorMessage:  r.ErrorMessage,
			TotalTokens:   r.TotalTokens,
			DurationMs:    r.DurationMs,
			CreatedAt:     r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"executions": out, "total": total})
}

func (h *AgentHandler) ListExecutionToolTraces(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	userID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	traceID := c.Param("traceID")
	rows, err := h.svc.ListToolTraces(c.Request.Context(), tenantID, userID, traceID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tool_traces": rows})
}

func (h *AgentHandler) ListExecutionTraceEvents(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	userID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	traceID := c.Param("traceID")
	rows, err := h.svc.ListTraceEvents(c.Request.Context(), tenantID, userID, traceID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"trace_events": rows})
}
