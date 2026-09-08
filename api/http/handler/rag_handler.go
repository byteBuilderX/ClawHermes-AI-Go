package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/byteBuilderX/stratum/api/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	gen "github.com/byteBuilderX/stratum/api/http/dto/gen"
	knowledge "github.com/byteBuilderX/stratum/internal/knowledge/application"
	"github.com/byteBuilderX/stratum/internal/knowledge/domain"
	skillpkg "github.com/byteBuilderX/stratum/internal/skill/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// RAGHandler exposes /knowledge/* endpoints. All persistence and validation is
// delegated to WorkspaceService; this layer only binds requests, calls the
// service, and renders responses.
type RAGHandler struct {
	ragService *knowledge.RAGService
	wsService  *knowledge.WorkspaceService
	logger     *zap.Logger
}

// NewRAGHandler constructs a RAGHandler. wsService may be nil for unit tests
// that only exercise the missing-tenant guard rails — every other path
// dereferences it.
func NewRAGHandler(
	ragService *knowledge.RAGService,
	wsService *knowledge.WorkspaceService,
	logger *zap.Logger,
) *RAGHandler {
	return &RAGHandler{
		ragService: ragService,
		wsService:  wsService,
		logger:     logger,
	}
}

func toDTOConfig(c domain.WorkspaceConfig) gen.WorkspaceConfig {
	return gen.WorkspaceConfig{
		EmbeddingModel:   c.EmbeddingModel,
		ChunkingStrategy: c.ChunkingStrategy,
		//nolint:gosec // chunk_size 等配置值由用户设置,不可能溢出 int32(proto 契约)
		ChunkSize: int32(c.ChunkSize),
		//nolint:gosec // 同 ChunkSize
		ChunkOverlap: int32(c.ChunkOverlap),
		QueryMode:    c.QueryMode,
		//nolint:gosec // 同 ChunkSize
		TopK:           int32(c.TopK),
		Reranking:      c.Reranking,
		ScoreThreshold: c.ScoreThreshold,
		//nolint:gosec // 同 ChunkSize
		RerankTopK:                int32(c.RerankTopK),
		RerankModel:               c.RerankModel,
		JudgeModel:                c.JudgeModel,
		RerankScoringInstructions: c.RerankScoringInstructions,
		JudgeScoringInstructions:  c.JudgeScoringInstructions,
	}
}

func fromDTOConfig(c gen.WorkspaceConfig) domain.WorkspaceConfig {
	return domain.WorkspaceConfig{
		EmbeddingModel:            c.EmbeddingModel,
		ChunkingStrategy:          c.ChunkingStrategy,
		ChunkSize:                 int(c.ChunkSize),
		ChunkOverlap:              int(c.ChunkOverlap),
		QueryMode:                 c.QueryMode,
		TopK:                      int(c.TopK),
		Reranking:                 c.Reranking,
		ScoreThreshold:            c.ScoreThreshold,
		RerankTopK:                int(c.RerankTopK),
		RerankModel:               c.RerankModel,
		JudgeModel:                c.JudgeModel,
		RerankScoringInstructions: c.RerankScoringInstructions,
		JudgeScoringInstructions:  c.JudgeScoringInstructions,
	}
}

func (h *RAGHandler) UploadDocument(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.UploadDocumentRequest
	if err := c.ShouldBind(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}

	if req.File.Size > constants.MaxUploadFileSize {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("file size exceeds 100MB limit")))
		return
	}

	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}

	h.logger.Info("uploading document",
		zap.String("workspace", req.Workspace),
		zap.String("filename", req.File.Filename))

	result, err := h.wsService.IngestUpload(
		c.Request.Context(), tenantID, req.Workspace, req.File, actorID,
		req.AllowedUserIDs, req.AllowedRoleIDs,
	)
	if err != nil {
		_ = c.Error(err)
		return
	}

	// 202 Accepted: doc row is persisted with status='processing'; the embed
	// + vector persist runs in a detached goroutine (see knowledge.IngestDocument).
	// Client polls the docs list to observe terminal status transitions.
	c.JSON(http.StatusAccepted, gin.H{
		"success":      true,
		"document_id":  result.DocumentID,
		"workspace":    result.Workspace,
		"status":       result.Status,
		"total_chunks": result.TotalChunks,
		"errors":       result.Errors,
	})
}

func (h *RAGHandler) Query(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.QueryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}

	h.logger.Info("executing RAG query",
		zap.String("question", req.Question),
		zap.String("workspace", req.Workspace),
		zap.String("mode", req.Mode))

	if req.TopK <= 0 {
		req.TopK = skillpkg.DefaultTopK
	}

	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}

	var embedModel, workspaceID string
	var threshold float32
	var reranking, rerankModel string
	var rerankTopK int
	var rerankScoringInstructions string
	if h.wsService != nil {
		if ws, err := h.wsService.GetWorkspace(c.Request.Context(), tenantID, req.Workspace); err == nil {
			embedModel = ws.Config.EmbeddingModel
			workspaceID = ws.ID
			// workspace config 单一事实源：API 查询面板不传阈值，config 缺省
			// 兜底（0=不过滤），保证配置保存后查询即生效。
			threshold = ws.Config.ScoreThreshold
			// 重排策略/模型/TopK/评分指令同样来自 config：面板查询据此触发
			// workspace 配置的重排（与 agent/evidence 检索路径 searchWorkspace 一致）。
			reranking = ws.Config.Reranking
			rerankModel = ws.Config.RerankModel
			rerankTopK = ws.Config.RerankTopK
			rerankScoringInstructions = ws.Config.RerankScoringInstructions
		}
	}

	result, err := h.ragService.Query(c.Request.Context(), knowledge.RAGQueryRequest{
		Question:    req.Question,
		Workspace:   req.Workspace,
		WorkspaceID: workspaceID,
		TenantID:    tenantID,
		Mode:        req.Mode,
		//nolint:gosec // TopK 已受 binding max=20 约束,不可能溢出 int(proto 契约)
		TopK:                      int(req.TopK),
		EmbeddingModel:            embedModel,
		ScoreThreshold:            threshold,
		Reranking:                 reranking,
		RerankModel:               rerankModel,
		RerankTopK:                rerankTopK,
		RerankScoringInstructions: rerankScoringInstructions,
		ViewerID:                  actorID,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}

	sources := make([]gin.H, len(result.Sources))
	for i, src := range result.Sources {
		sources[i] = gin.H{
			"document_id":    src.DocumentID,
			"document_title": src.DocumentTitle,
			"content":        src.Content,
			// parent_content: 命中 chunk 所在整节原文(Parent-Child 策略);非该
			// 策略为空串。前端据此就地展开"查看上下文"。
			"parent_content": src.ParentContent,
			"chunk_index":    src.ChunkIndex,
			"score":          src.Score,
			// workspace 传请求名：前端 SourceItem 用它判定可预览并传给预览抽屉。
			"workspace": req.Workspace,
		}
	}
	response := gin.H{
		"answer":          result.Answer,
		"sources":         sources,
		"mode":            result.Mode,
		"latency_ms":      result.Latency.Milliseconds(),
		"best_score":      result.BestScore,
		"candidate_count": result.CandidateCount,
	}
	// 无答案信号：nil（有答案）时 omitempty 语义手控——键不存在，避免
	// "no_answer": null 破坏前端 zod .nullable().optional() 之外的老 schema。
	if result.NoAnswer != nil {
		response["no_answer"] = gin.H{
			"reason":          string(result.NoAnswer.Reason),
			"retrieved_count": result.NoAnswer.RetrievedCount,
			"filtered_count":  result.NoAnswer.FilteredCount,
			"best_score":      result.NoAnswer.BestScore,
			"retried":         result.NoAnswer.Retried,
			"rewritten_query": result.NoAnswer.RewrittenQuery,
			"detail":          result.NoAnswer.Detail,
		}
	}
	c.JSON(http.StatusOK, response)
}

func (h *RAGHandler) CreateWorkspace(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req gen.CreateWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}

	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	ws, err := h.wsService.CreateWorkspace(c.Request.Context(), tenantID, knowledge.CreateWorkspaceInput{
		Name:        req.Name,
		Description: req.Description,
		Config:      fromDTOConfig(req.Config),
		Editors:     req.Editors,
	}, actorID)
	if err != nil {
		_ = c.Error(err)
		return
	}

	h.logger.Info("workspace created", zap.String("name", ws.Name), zap.String("tenant_id", tenantID))
	c.JSON(http.StatusCreated, gin.H{
		"id":          ws.ID,
		"name":        ws.Name,
		"description": ws.Description,
		"config":      toDTOConfig(ws.Config),
	})
}

func (h *RAGHandler) ListWorkspaces(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}

	list, err := h.wsService.ListWorkspaces(c.Request.Context(), tenantID)
	if err != nil {
		_ = c.Error(err)
		return
	}

	out := make([]gen.WorkspaceListItem, 0, len(list))
	for _, ws := range list {
		item := gen.WorkspaceListItem{
			ID:          ws.ID,
			Name:        ws.Name,
			Description: ws.Description,
			Config:      toDTOConfig(ws.Config),
			CreatedAt:   ws.CreatedAt,
			UpdatedAt:   ws.UpdatedAt,
		}
		editors, listErr := h.wsService.ListEditors(c.Request.Context(), tenantID, ws.ID)
		if listErr != nil {
			_ = c.Error(listErr)
			return
		}
		item.Editors = editors
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"workspaces": out})
}

func (h *RAGHandler) UpdateWorkspace(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	name := c.Param("name")
	if name == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace name required")))
		return
	}

	var req gen.UpdateWorkspaceRequest
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	// 显式空字符串字段（"reranking":"" / "rerank_model":"" / "judge_model":"" /
	// "rerank_scoring_instructions":"" / "judge_scoring_instructions":""）
	// 是合法"关闭/清空"值，但 MergeUpdate 的 partial 合并以零值=未传，编码为
	// NUL 前缀 sentinel 区分显式清空（与 ScoreThresholdResetSentinel 同构）。
	c.Request.Body = io.NopCloser(bytes.NewReader(encodeResetSentinels(body)))
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}

	in := knowledge.UpdateWorkspaceInput{Name: req.Name, Description: req.Description}
	if req.Config != nil {
		cfg := fromDTOConfig(*req.Config)
		// PATCH 契约 Config 整体替换：score_threshold=0 是显式"关闭过滤"，
		// 但 MergeUpdate 的 partial 合并以零值=未传。用哨兵编码显式 0，
		// domain 侧转回 0（0.99→0 的恢复默认必须真的写回）。
		if cfg.ScoreThreshold == 0 {
			cfg.ScoreThreshold = domain.ScoreThresholdResetSentinel
		}
		in.Config = &cfg
	}

	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	ws, err := h.wsService.UpdateWorkspace(c.Request.Context(), tenantID, name, in, actorID)
	if err != nil {
		_ = c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"name": ws.Name, "config": toDTOConfig(ws.Config)})
}

// ListWorkspaceVersions returns the workspace's product version history
// (newest first) with created_by display names resolved.
func (h *RAGHandler) ListWorkspaceVersions(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	versions, err := h.wsService.ListWorkspaceVersions(c.Request.Context(), tenantID, c.Param("name"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	out := make([]WorkspaceVersionResponse, 0, len(versions))
	for _, v := range versions {
		out = append(out, workspaceVersionToResponse(v))
	}
	c.JSON(http.StatusOK, WorkspaceVersionsResponse{Versions: out})
}

// GetWorkspaceVersion returns one historical version's full content snapshot
// (list metadata + edit-surface payload). The frontend "detail" drawer fetches
// the clicked version and its direct parent (parentVersionId), then diffs the
// two payloads field-by-field.
func (h *RAGHandler) GetWorkspaceVersion(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	content, err := h.wsService.GetWorkspaceVersion(c.Request.Context(), tenantID, c.Param("name"), c.Param("versionID"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, workspaceVersionContentToResponse(content))
}

// RollbackWorkspace restores a deprecated historical version, repointing the
// workspace to it immediately without creating a new version. Returns the
// fresh workspace so the client can re-render in place.
func (h *RAGHandler) RollbackWorkspace(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req RollbackWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	ws, err := h.wsService.RollbackWorkspace(c.Request.Context(), tenantID, c.Param("name"), knowledge.RollbackWorkspaceInput{
		ActorID:   actorID,
		VersionID: req.VersionID,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": ws.Name, "description": ws.Description, "config": toDTOConfig(ws.Config)})
}

func (h *RAGHandler) GetWorkspaceStats(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	name := c.Param("name")
	if name == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace name required")))
		return
	}

	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}

	res, err := h.wsService.GetWorkspaceStats(c.Request.Context(), tenantID, name, actorID)
	if err != nil {
		_ = c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":        res.Name,
		"description": res.Description,
		"config":      toDTOConfig(res.Config),
		"stats":       res.Stats,
		"editors":     strSliceOrEmpty(res.Editors),
	})
}

// SetWorkspaceEditors replaces the editor set of a workspace (PUT
// /knowledge/workspaces/:name/editors). Only creator/owner may grant editors;
// each editor id must hold role admin or owner.
func (h *RAGHandler) SetWorkspaceEditors(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	name := c.Param("name")
	if name == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace name required")))
		return
	}
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
	if err := h.wsService.SetEditors(c.Request.Context(), tenantID, name, req.EditorIDs, actorID); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "editors updated"})
}

func (h *RAGHandler) DeleteWorkspace(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	name := c.Param("name")
	if name == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace parameter required")))
		return
	}

	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if err := h.wsService.DeleteWorkspace(c.Request.Context(), tenantID, name, actorID); err != nil {
		_ = c.Error(err)
		return
	}

	h.logger.Info("workspace deleted", zap.String("name", name), zap.String("tenant_id", tenantID))
	c.JSON(http.StatusOK, gin.H{"success": true, "workspace": name})
}

// ListDocuments returns the documents in a workspace with their ingest
// lifecycle fields. Front-end polls this endpoint every ~5s while a doc
// is in 'processing' to render a status badge and show terminal state.
// The access whitelist fields are echoed only for admins/owners (see
// WorkspaceService.ListDocuments); members always receive empty values.
func (h *RAGHandler) ListDocuments(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	name := c.Param("name")
	if name == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace name required")))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	docs, err := h.wsService.ListDocuments(c.Request.Context(), tenantID, name, actorID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	items := make([]gin.H, len(docs))
	for i, d := range docs {
		items[i] = gin.H{
			"id":                 d.ID,
			"source":             d.Source,
			"content_hash":       d.ContentHash,
			"ingest_status":      d.IngestStatus,
			"ingest_error":       d.IngestError,
			"processed_chunks":   d.ProcessedChunks,
			"total_chunks":       d.TotalChunks,
			"created_at":         d.CreatedAt,
			"ingest_started_at":  d.IngestStartedAt,
			"ingest_finished_at": d.IngestFinishedAt,
			"allowed_user_ids":   strSliceOrEmpty(d.AllowedUserIDs),
			"allowed_role_ids":   strSliceOrEmpty(d.AllowedRoleIDs),
			"created_by":         d.CreatedBy,
			"restricted":         d.Restricted,
		}
	}
	c.JSON(http.StatusOK, gin.H{"workspace": name, "documents": items})
}

// strSliceOrEmpty renders nil slices as [] so JSON clients never see null for
// whitelist fields echoed to members.
func strSliceOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (h *RAGHandler) DeleteDocument(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	workspace, documentID := c.Param("name"), c.Param("documentID")
	if workspace == "" || documentID == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace and document required")))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if err := h.wsService.DeleteDocument(c.Request.Context(), tenantID, workspace, documentID, actorID); err != nil {
		_ = c.Error(err)
		return
	}
	h.logger.Info("knowledge document deleted",
		zap.String("workspace", workspace),
		zap.String("document_id", documentID),
		zap.String("tenant_id", tenantID))
	c.JSON(http.StatusOK, gin.H{"success": true, "document_id": documentID})
}

// SetDocumentAccess replaces the document-level access whitelist of a
// document (PUT /knowledge/workspaces/:name/documents/:documentID/access).
// Only tenant owner, or admin acting on their own document (checkOwnership
// matrix), may grant access; the response echoes the applied whitelist.
func (h *RAGHandler) SetDocumentAccess(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	workspace, documentID := c.Param("name"), c.Param("documentID")
	if workspace == "" || documentID == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace and document required")))
		return
	}
	var req gen.DocumentAccessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	actorID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	if err := h.wsService.SetDocAccess(
		c.Request.Context(), tenantID, workspace, documentID, actorID,
		req.AllowedUserIDs, req.AllowedRoleIDs,
	); err != nil {
		_ = c.Error(err)
		return
	}
	h.logger.Info("knowledge document access updated",
		zap.String("workspace", workspace),
		zap.String("document_id", documentID),
		zap.String("tenant_id", tenantID))
	c.JSON(http.StatusOK, gen.DocumentAccessResponse{
		AllowedUserIDs: strSliceOrEmpty(req.AllowedUserIDs),
		AllowedRoleIDs: strSliceOrEmpty(req.AllowedRoleIDs),
	})
}

// PreviewDocument returns the chunk-reassembled content of a document for
// citation preview (GET /knowledge/workspaces/:name/documents/:documentID/preview).
// Any tenant member may preview a document they can see; invisible and
// nonexistent documents both render 404 (fail closed, no existence leak).
func (h *RAGHandler) PreviewDocument(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	workspace, documentID := c.Param("name"), c.Param("documentID")
	if workspace == "" || documentID == "" {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, errors.New("workspace and document required")))
		return
	}
	viewerID, ok := userIDFromCtx(c)
	if !ok {
		respondMissingUser(c)
		return
	}
	preview, err := h.ragService.PreviewDocument(c.Request.Context(), tenantID, workspace, documentID, viewerID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	segments := make([]gen.ChunkSegment, 0, len(preview.Segments))
	for _, seg := range preview.Segments {
		segments = append(segments, gen.ChunkSegment{
			ChunkID:       seg.ChunkID,
			Index:         seg.Index,
			Content:       seg.Content,
			ParentContent: seg.ParentContent,
		})
	}
	h.logger.Info("knowledge document previewed",
		zap.String("workspace", workspace),
		zap.String("document_id", documentID),
		zap.String("tenant_id", tenantID))
	c.JSON(http.StatusOK, gen.PreviewDocumentResponse{
		Workspace:     workspace,
		DocumentID:    preview.DocumentID,
		DocumentTitle: preview.DocumentTitle,
		ChunkCount:    int32(preview.ChunkCount), //nolint:gosec // 分块数来自重组结果,不可能溢出 int32(proto 契约)
		Segments:      segments,
	})
}

// encodeResetSentinels 把 PATCH 请求体中显式空字符串字段编码为重置哨兵。匹配
// 原始 JSON 字节的 `"key":""`（紧凑 JSON，axios 序列化格式），替换为
// sentinelJSON 生成的转义字面量 —— NUL 字节必须转义为 \u0000 才是合法 JSON，
// 不能直接塞裸 NUL 字节（否则 ShouldBindJSON 解析失败）。
func encodeResetSentinels(raw []byte) []byte {
	raw = resetReplace(raw, `"reranking":""`, `"reranking":`, domain.RerankingResetSentinel)
	raw = resetReplace(raw, `"rerank_model":""`, `"rerank_model":`, domain.RerankModelResetSentinel)
	raw = resetReplace(raw, `"judge_model":""`, `"judge_model":`, domain.JudgeModelResetSentinel)
	raw = resetReplace(raw, `"rerank_scoring_instructions":""`, `"rerank_scoring_instructions":`, domain.RerankScoringInstructionsResetSentinel)
	raw = resetReplace(raw, `"judge_scoring_instructions":""`, `"judge_scoring_instructions":`, domain.JudgeScoringInstructionsResetSentinel)
	return raw
}

// resetReplace 把 raw 中 emptyLit（如 `"reranking":""`）替换为
// keyLit+sentinelJSON(sentinel)（如 `"reranking":"\u0000rerank_reset"`）。
func resetReplace(raw []byte, emptyLit, keyLit, sentinel string) []byte {
	return bytes.ReplaceAll(raw, []byte(emptyLit), []byte(keyLit+sentinelJSON(sentinel)))
}

// sentinelJSON 返回 sentinel 字符串的合法 JSON 字面量（控制字符转义为 \u0000）。
func sentinelJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
