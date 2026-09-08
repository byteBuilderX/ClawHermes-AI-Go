// Package application implements knowledge bounded context use-cases.
package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/byteBuilderX/stratum/internal/knowledge/domain"
	knowledgeport "github.com/byteBuilderX/stratum/internal/knowledge/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"github.com/byteBuilderX/stratum/pkg/textutil"
	"go.uber.org/zap"
)

var ErrRAGDependency = errors.New("knowledge retrieval dependency unavailable")

// hybridLegRecallFactor widens TopK for the hybrid retrieval legs: each leg
// (vector, keyword) recalls TopK × hybridLegRecallFactor candidates before
// reciprocal rank fusion narrows the pool back to TopK. External reranking
// widens further via RerankWidenFactor.
const hybridLegRecallFactor = 2

// errCollectionNotFound distinguishes a missing Milvus collection from other
// search failures so Query can decide between "legitimately empty workspace"
// and "drift" via ChunkRepo.CountByWorkspace.
var errCollectionNotFound = errors.New("knowledge collection not found")

// errLegacyDimMismatch reports a vector-dimension mismatch on the collection
// being searched. The caller decides the semantics: on the legacy
// (pre-upgrade) collection a mismatch skips retrieval (Warn + empty result,
// spec: legacy dim drift is not fail-closed); on the model-suffixed name it
// still fails closed as ErrRAGDependency.
var errLegacyDimMismatch = errors.New("knowledge legacy collection dimension mismatch")

func isCollectionNotFound(err error) bool {
	return errors.Is(err, errCollectionNotFound) ||
		(err != nil && strings.Contains(err.Error(), "collection not found"))
}

// rerankIdentity splits "provider:model" style rerank identities.
// provider "builtin-score-v1" is local reordering; any other provider
// requires an external reranker backend.
func rerankIdentity(identity string) (provider, model string) {
	if i := strings.Index(identity, ":"); i >= 0 {
		return identity[:i], identity[i+1:]
	}
	return identity, ""
}

// NewRAGSearchFn returns a knowledge search function suitable for the agent's
// WithRAGSearchFn hook. It fans out across workspaces concurrently, bounded
// by MaxConcurrentWorkspaceSearch (a per-query cap protecting the embed
// backend and DB pool), and concatenates results; the first error is returned
// only when no content was produced (at-least-one semantics).
// viewerID is the end user whose document whitelist scopes every search.
func NewRAGSearchFn(rs *RAGService, tenantID, viewerID string) func(
	ctx context.Context, workspaces []string, query string, topK int,
) (string, error) {
	return func(ctx context.Context, workspaces []string, query string, topK int) (string, error) {
		results := make([]wsResult, len(workspaces))
		sem := make(chan struct{}, constants.MaxConcurrentWorkspaceSearch)
		var wg sync.WaitGroup
		for i, ws := range workspaces {
			// Acquire before spawning so at most MaxConcurrentWorkspaceSearch
			// searches are in flight; the launch loop parks here otherwise.
			sem <- struct{}{}
			wg.Add(1)
			go func(i int, ws string) {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] = searchWorkspace(ctx, rs, tenantID, viewerID, ws, query, topK)
			}(i, ws)
		}
		wg.Wait()
		return rs.mergeResults(results)
	}
}

func searchWorkspace(ctx context.Context, rs *RAGService, tenantID, viewerID, ws, query string, topK int) wsResult {
	rw, err := resolveWorkspaceConfig(ctx, rs, tenantID, ws, topK)
	if err != nil {
		return wsResult{err: err}
	}
	out, err := rs.Query(ctx, RAGQueryRequest{
		WorkspaceID:    rw.workspaceID,
		Workspace:      ws,
		Question:       query,
		TenantID:       tenantID,
		Mode:           rw.mode,
		TopK:           rw.effectiveTopK,
		EmbeddingModel: rw.embedModel,
		// workspace config 单一事实源：阈值缺省兜底（0=不过滤），避免
		// 配置存库但不生效的装配断点。
		ScoreThreshold: rw.threshold,
		ViewerID:       viewerID,
		// System-actor contexts (privileged wiring paths such as eval workers)
		// carry the same trust as an admin owner and bypass the D2 gate.
		SkipAccessCheck: reqctx.SystemActorFromContext(ctx) != "",
		// workspace 重排触发与模型/评分指令单一事实源：Reranking 使普通查询
		// 触发配置的重排（builtin 或外部 provider:model）；指令随策略消费。
		Reranking:                 rw.reranking,
		RerankModel:               rw.rerankModel,
		RerankTopK:                rw.rerankTopK,
		JudgeModel:                rw.judgeModel,
		RerankScoringInstructions: rw.rerankScoringInstructions,
		JudgeScoringInstructions:  rw.judgeScoringInstructions,
	})
	if err != nil {
		return wsResult{err: err}
	}
	return wsResult{content: formatSources(out.Sources), noAnswer: out.NoAnswer}
}

// resolvedWorkspace 收敛 resolveWorkspaceConfig 的多返回值，避免 6 值元组
// 解构位错（review I2）。
type resolvedWorkspace struct {
	mode                      string
	effectiveTopK             int
	embedModel                string
	workspaceID               string
	threshold                 float32
	reranking                 string
	rerankModel               string
	rerankTopK                int
	judgeModel                string
	rerankScoringInstructions string
	judgeScoringInstructions  string
}

func resolveWorkspaceConfig(ctx context.Context, rs *RAGService, tenantID, ws string, topK int) (resolvedWorkspace, error) {
	rw := resolvedWorkspace{mode: domain.DefaultQueryMode, effectiveTopK: topK}
	if rs.wsRepo == nil {
		return rw, nil
	}
	w, getErr := rs.wsRepo.GetByName(ctx, tenantID, ws)
	if getErr != nil {
		return rw, ErrRAGDependency
	}
	if w == nil {
		return rw, nil
	}
	rw.workspaceID = w.ID
	if w.Config.TopK > 0 {
		rw.effectiveTopK = w.Config.TopK
	}
	rw.embedModel = w.Config.EmbeddingModel
	rw.threshold = w.Config.ScoreThreshold
	if w.Config.QueryMode != "" {
		rw.mode = w.Config.QueryMode
	}
	// 模型/重排触发/评分指令来自 workspace 显式配置：Reranking 触发重排（普通
	// 查询据此生效，不再仅 snapshot/evaluation 路径）；RerankModel 供 builtin
	// 语义重排消费；JudgeModel 由证据充分性门消费。评分指令随各自策略，空 =
	// 内置评分 prompt。
	rw.reranking = w.Config.Reranking
	rw.rerankModel = w.Config.RerankModel
	rw.rerankTopK = w.Config.RerankTopK
	rw.judgeModel = w.Config.JudgeModel
	rw.rerankScoringInstructions = w.Config.RerankScoringInstructions
	rw.judgeScoringInstructions = w.Config.JudgeScoringInstructions
	return rw, nil
}

// formatSources assembles the retrieval context fed to the answer model and
// the sufficiency judge. Each hit contributes its leaf Content (the focused
// fragment that matched) plus, when the Parent-Child strategy set one, its
// parent chunk Content (the whole enclosing section) so the model sees full
// context, not just the fragment. Parent dedup keeps repeated hits inside the
// same section from bloating the context; dedup keys on the parent text
// because Source carries no parent ID across the REST/agent boundary. A leaf
// without a parent formats exactly as before (Content only).
func formatSources(sources []Source) string {
	var sb strings.Builder
	seenParent := make(map[string]struct{})
	for _, src := range sources {
		sb.WriteString(src.Content)
		if src.ParentContent != "" {
			if _, dup := seenParent[src.ParentContent]; !dup {
				sb.WriteString("\n\n")
				sb.WriteString(src.ParentContent)
				seenParent[src.ParentContent] = struct{}{}
			}
		}
		sb.WriteString("\n---\n")
	}
	return sb.String()
}

type wsResult struct {
	content  string
	noAnswer *NoAnswerInfo
	err      error
}

// mergeResults concatenates per-workspace content with at-least-one
// semantics: failed workspaces are skipped, successful ones contribute, and
// the first error surfaces only when nothing was produced at all. Partial
// failure is deliberately not fatal (one dead workspace must not blank the
// whole answer) but it must not be silent either — a WARN with the failure
// count and first error is emitted so operators can see the degraded fan-out.
func (rs *RAGService) mergeResults(results []wsResult) (string, error) {
	var combined strings.Builder
	var firstErr error
	failures := 0
	for _, r := range results {
		if r.err != nil {
			failures++
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		combined.WriteString(r.content)
	}
	if failures > 0 {
		rs.logger.Warn("knowledge.rag.partial_failure",
			zap.Int("failed_workspaces", failures),
			zap.Int("total_workspaces", len(results)),
			zap.Error(firstErr))
	}
	if combined.Len() == 0 && firstErr != nil {
		return "", firstErr
	}
	return combined.String(), nil
}

type RAGService struct {
	embeddingSvc  knowledgeport.Embedder
	embedResolver EmbedResolver
	wsRepo        knowledgeport.WorkspaceRepo
	chunkRepo     knowledgeport.ChunkRepo
	vectorStore   knowledgeport.VectorStore
	reranker      knowledgeport.Reranker
	// docRepo + roleResolver back the document-level visibility whitelist
	// (D1 matrix); both are required for restricted workspaces and fail
	// closed when unavailable.
	docRepo      knowledgeport.DocRepo
	roleResolver knowledgeport.TenantRoleResolver
	// judgeResolver 按请求中的 judge 模型解析证据充分性 judge（仅 evidence 路径
	// 消费，Plain Query/API 面板零接触）；nil/解析失败 = fail-open 放行（judge
	// 门关或不判定时结果原样通过，与不配置一致，绝不误杀检索）。
	judgeResolver SufficiencyJudgeResolver
	// semanticReranker 是 builtin-score-v1 的 LLM 语义重排器；nil = 未装配
	// （fail-open，builtin 走纯召回分数排序）。semanticTopN 是精排候选上限
	// （≤0 由 wiring 在注入前回落 RerankLLMTopN）。
	semanticReranker knowledgeport.Reranker
	semanticTopN     int
	metrics          observability.MetricsProvider
	logger           *zap.Logger
}

func NewRAGService(
	embeddingSvc knowledgeport.Embedder,
	vectorStore knowledgeport.VectorStore,
	logger *zap.Logger,
) *RAGService {
	return &RAGService{
		embeddingSvc: embeddingSvc,
		vectorStore:  vectorStore,
		logger:       logger,
	}
}

func (rs *RAGService) SetEmbedResolver(r EmbedResolver)                  { rs.embedResolver = r }
func (rs *RAGService) SetWorkspaceRepo(repo knowledgeport.WorkspaceRepo) { rs.wsRepo = repo }
func (rs *RAGService) SetChunkRepo(repo knowledgeport.ChunkRepo)         { rs.chunkRepo = repo }
func (rs *RAGService) SetReranker(r knowledgeport.Reranker)              { rs.reranker = r }
func (rs *RAGService) SetMetrics(m observability.MetricsProvider)        { rs.metrics = m }
func (rs *RAGService) SetDocRepo(repo knowledgeport.DocRepo)             { rs.docRepo = repo }
func (rs *RAGService) SetTenantRoleResolver(r knowledgeport.TenantRoleResolver) {
	rs.roleResolver = r
}
func (rs *RAGService) SetSufficiencyJudgeResolver(r SufficiencyJudgeResolver) {
	rs.judgeResolver = r
}

// SetSemanticReranker 注入 builtin-score-v1 的 LLM 语义重排器；wiring 在注入
// 前把 ≤0 的 topN 解析为 RerankLLMTopN 默认。
func (rs *RAGService) SetSemanticReranker(r knowledgeport.Reranker, topN int) {
	rs.semanticReranker = r
	rs.semanticTopN = topN
}

func (rs *RAGService) resolveEmbedder(ctx context.Context, req RAGQueryRequest) knowledgeport.Embedder {
	if rs.embedResolver != nil && req.TenantID != "" {
		if c := rs.embedResolver(ctx, req.TenantID, req.EmbeddingModel); c != nil {
			return c
		}
	}
	return rs.embeddingSvc
}

// SufficiencyJudgeResolver 按请求中的 judge 模型解析证据充分性 judge；模型未知/
// 目录校验失败返回 error（judge 实现契约 fail-closed：错误向上传播，由调用方
// judgeSufficiencyGate 按 fail-open 原样放行）。wiring 注入闭包，application 不
// import llmgateway（跨 context 接口定义在消费方）。
type SufficiencyJudgeResolver func(ctx context.Context, model string) (knowledgeport.SufficiencyJudge, error)

type RAGQueryRequest struct {
	Question       string
	Workspace      string
	WorkspaceID    string // stable ID for collection naming; resolved from Workspace if empty
	TenantID       string
	Mode           string // "vector", "keyword", "graph", "hybrid"
	TopK           int
	EmbeddingModel string
	// Reranking selects the rerank strategy: "" (none), "builtin-score-v1"
	// (local score desc), or "cohere:<model>" (external reranker).
	Reranking      string
	ScoreThreshold float32 // keep only results with Score >= threshold; 0 disables (keyword mode exempt)
	RerankTopK     int     // final count after external reranking; 0 uses TopK
	// ViewerID is the end user whose document whitelist scopes the search.
	// Empty with SkipAccessCheck=false fails closed (D2 gate): a missing
	// identity must never silently become a filterless full-library search.
	ViewerID string
	// SkipAccessCheck is only settable by privileged wiring paths running in
	// a SystemActor context (eval workers, platform tooling) — the equivalent
	// of the admin-owner exemption in the D1 matrix. Business callers must
	// never set it.
	SkipAccessCheck bool
	// SkipTopKClamp 豁免 TopK/RerankTopK 的 20 硬上限钳制，仅供评估等特权检索路径
	// 置位：RetrievalSnapshot 的既有契约允许 TopK ∈ [1, MaximumEvaluationTopK=100]，
	// 评估 recall@k（k>20）依赖不截断的候选池，Query 入口的防御性 clamp 会静默压回 20
	// 导致指标失真。普通业务调用方必须走 clamp（proto binding 已限 max=20，此为纵深防御）。
	SkipTopKClamp bool
	// RerankModel 是 builtin-score-v1 的 LLM 语义重排模型（workspace 显式配置）；
	// 仅 Reranking=builtin-score-v1 时消费，空模型由重排器 fail-open 拒绝。
	RerankModel string
	// JudgeModel 是证据充分性 judge 模型（workspace 显式配置）；空 = 关闭 judge 门。
	JudgeModel string
	// RerankScoringInstructions / JudgeScoringInstructions 是 workspace 级评分
	// 指令附加段（空 = 使用代码内置评分 prompt；JSON 输出结构固定不可改）。
	RerankScoringInstructions string
	JudgeScoringInstructions  string
}

type RAGQueryResult struct {
	Answer        string
	Sources       []Source
	VectorResults []knowledgeport.VectorSearchResult
	Mode          string
	Latency       time.Duration
	// NoAnswer 是无答案的结构化信号；nil = 有答案（Sources 非空）。信号与
	// BestScore/CandidateCount 解耦：有答案路径也恒填充统计，供校准消费。
	NoAnswer *NoAnswerInfo
	// BestScore 是池内最高分（阈值过滤前采集），恒填充（无候选时 0）。
	// 禁止从过滤后 sources 推导 max(score)——阈值 >0 时分布被截断。
	BestScore float32
	// CandidateCount 是阈值过滤前的候选数（rerank 入口池大小）。
	CandidateCount int
}

type Source struct {
	DocumentID    string
	ChunkID       string
	Content       string
	ParentContent string // non-empty when parent chunk was fetched (Parent-Child strategy)
	ChunkIndex    int64
	Score         float32
	// DocumentTitle is the owning document's source file name, backfilled at
	// the end of Query for the /knowledge/query citation cards. Display-only;
	// empty when the doc index read fails (never fails the query).
	DocumentTitle string
}

// DocumentPreview is the chunk-reassembled content of a document for the
// citation preview UI. Raw document text is not stored (knowledge_docs.content
// was dropped), so previews rebuild the document from its chunks ordered by
// chunk index; Parent-Child chunking attaches the parent context per leaf.
type DocumentPreview struct {
	DocumentID    string
	DocumentTitle string // Source file name of the doc (no title column exists)
	ChunkCount    int
	Segments      []ChunkSegment
}

// ChunkSegment is one ordered content unit of a previewed document.
type ChunkSegment struct {
	ChunkID       string
	Index         int64
	Content       string
	ParentContent string // non-empty when the chunk references a Parent-Child parent
}

func (rs *RAGService) Query(ctx context.Context, req RAGQueryRequest) (*RAGQueryResult, error) {
	startTime := time.Now()
	sc, _ := observability.SpanFromContext(ctx)
	rs.logger.Info("executing RAG query",
		zap.String("trace_id", sc.TraceID),
		zap.Int("question_length", len([]rune(req.Question))),
		zap.String("mode", req.Mode))

	result := &RAGQueryResult{
		Mode:    req.Mode,
		Answer:  "",
		Sources: []Source{},
		Latency: 0,
	}

	if req.TopK <= 0 {
		req.TopK = constants.DefaultRAGTopK
	}
	// 防御性 clamp：查询入口不信任调用方已通过 proto binding 的 max=20。
	// SkipTopKClamp 豁免仅限评估等特权路径（RetrievalSnapshot 契约允许到 100）。
	if !req.SkipTopKClamp && req.TopK > constants.MaxRAGTopK {
		req.TopK = constants.MaxRAGTopK
	}

	if req.WorkspaceID == "" && req.Workspace != "" && rs.wsRepo != nil {
		ws, err := rs.wsRepo.GetByName(ctx, req.TenantID, req.Workspace)
		if err != nil {
			return nil, ErrRAGDependency
		}
		req.WorkspaceID = ws.ID
		if req.EmbeddingModel == "" {
			req.EmbeddingModel = ws.Config.EmbeddingModel
		}
	}

	collectionName := constants.CollectionName(req.TenantID, req.WorkspaceID, req.EmbeddingModel)

	// D2 fail-closed gate + D1 visibility set, resolved in one step.
	visibleDocIDs, unrestricted, err := rs.accessScope(ctx, req)
	if err != nil {
		rs.logger.Error("knowledge.rag.visibility_unavailable",
			zap.String("trace_id", sc.TraceID), zap.Error(err))
		return nil, err
	}
	if !unrestricted && len(visibleDocIDs) == 0 {
		// Nothing visible: an empty result without touching embed/Milvus/keyword.
		result.NoAnswer = buildNoAnswer(NoAnswerAccessRestricted, 0, 0, 0)
		result.Latency = time.Since(startTime)
		return result, nil
	}
	// D7 guard is evaluated per leg: an over-long whitelist degrades the
	// vector leg (empty results, WARN) while the keyword leg keeps filtering.
	filterExpr := buildDocFilterExpr(visibleDocIDs)

	switch req.Mode {
	case "vector":
		legacyName := constants.CollectionLegacyName(req.TenantID, req.WorkspaceID)
		searchName, legacy := rs.resolveSearchCollection(ctx, collectionName, legacyName, req.WorkspaceID)
		vectorResults, vErr := rs.vectorLegSearch(ctx, req, searchName, legacy, filterExpr, visibleDocIDs)
		if vErr != nil {
			if errors.Is(vErr, errCollectionNotFound) {
				if missingErr := rs.handleMissingCollection(ctx, req, searchName); missingErr != nil {
					return nil, missingErr
				}
				result.NoAnswer = buildNoAnswer(NoAnswerNoSources, 0, 0, 0)
				result.Latency = time.Since(startTime)
				return result, nil
			}
			return nil, vErr
		}
		result.VectorResults = vectorResults
		sources, stats, rerankErr := rs.rerankSources(ctx, req, vectorToPool(vectorResults))
		if rerankErr != nil {
			return nil, rerankErr
		}
		result.Sources = sources
		rs.recordStats(result, stats)

	case "keyword":
		sources, legErr := rs.keywordLeg(ctx, req, visibleDocIDs)
		if legErr != nil {
			return nil, legErr
		}
		result.Sources = sources
		// keyword 腿无分数（P4 归一化前）：候选数即池大小，BestScore 恒 0。
		rs.recordStats(result, rerankStats{poolSize: len(sources)})

	case "hybrid":
		vr, sources, stats, legErr := rs.hybridLeg(ctx, req, collectionName, filterExpr, visibleDocIDs)
		if legErr != nil {
			return nil, legErr
		}
		result.VectorResults = vr
		result.Sources = sources
		rs.recordStats(result, stats)

	default:
		// graph 模式（AllowedQueryModes 含 graph 但检索器未实现）与空 mode
		// 落空 switch：显式 unsupported_mode，防止误报 no_sources 污染校准。
		result.NoAnswer = buildNoAnswer(NoAnswerUnsupportedMode, 0, 0, 0)
	}

	if result.NoAnswer != nil && rs.metrics != nil {
		rs.metrics.IncNoAnswer(req.TenantID, string(result.NoAnswer.Reason))
	}

	result.Latency = time.Since(startTime)

	rs.expandParentContext(ctx, req, result)
	// 来源卡片需文档名：decorateSourceTitles 仅按 DocumentID 从文档索引回填 title，
	// 与 expandParentContext（只填 ParentContent、不增删分块）无顺序依赖，置于其后仅为聚合收尾。
	rs.decorateSourceTitles(ctx, req.TenantID, req.WorkspaceID, result.Sources)

	rs.logger.Info("RAG query completed",
		zap.String("trace_id", sc.TraceID),
		zap.Int("vector_results", len(result.VectorResults)),
		zap.Duration("latency", result.Latency))

	return result, nil
}

// accessScope enforces the D2 identity gate and resolves the D1 visibility
// set in one step. A query without an explicit viewer identity must not
// silently degrade into a filterless full-library search; only system-actor
// wiring paths set SkipAccessCheck.
func (rs *RAGService) accessScope(ctx context.Context, req RAGQueryRequest) ([]string, bool, error) {
	if req.ViewerID == "" && !req.SkipAccessCheck {
		sc, _ := observability.SpanFromContext(ctx)
		rs.logger.Warn("knowledge.rag.access_check_skipped",
			zap.String("trace_id", sc.TraceID), zap.String("tenant_id", req.TenantID))
		return nil, false, ErrRAGDependency
	}
	return rs.visibleDocIDs(ctx, req)
}

// vectorLegSearch runs the vector-only retrieval leg with the D7 over-long
// filter guard: an oversized whitelist degrades this leg to empty results with
// a WARN (never a query failure), while the keyword leg keeps filtering.
// errCollectionNotFound propagates for the caller to classify via
// handleMissingCollection; other failures map to ErrRAGDependency.
func (rs *RAGService) vectorLegSearch(ctx context.Context, req RAGQueryRequest, searchName string, legacy bool, filterExpr string, visibleDocIDs []string) ([]knowledgeport.VectorSearchResult, error) {
	if visibleDocIDs != nil && filterExprTooLong(filterExpr) {
		sc, _ := observability.SpanFromContext(ctx)
		rs.logger.Warn("knowledge.rag.filter_degraded",
			zap.String("trace_id", sc.TraceID),
			zap.Int("visible_docs", len(visibleDocIDs)),
			zap.Int("filter_len", len(filterExpr)))
		return nil, nil
	}
	candidateTopK := req.TopK
	if widensRecall(req.Reranking) {
		candidateTopK = req.TopK * constants.RerankWidenFactor
	}
	results, err := rs.queryVector(ctx, req.TenantID, req.Question, searchName, candidateTopK, rs.resolveEmbedder(ctx, req), req.EmbeddingModel, legacy, filterExpr)
	if err != nil {
		if errors.Is(err, errCollectionNotFound) {
			return nil, err
		}
		sc, _ := observability.SpanFromContext(ctx)
		rs.logger.Error("knowledge.retrieval.dependency_failed", zap.String("trace_id", sc.TraceID),
			zap.String("operation", "vector_search"), zap.String("error_category", "dependency_unavailable"))
		return nil, ErrRAGDependency
	}
	return results, nil
}

// keywordLeg runs the keyword-only retrieval leg. The visible doc-ID set is
// passed through to KeywordSearch so both hybrid legs filter identically.
func (rs *RAGService) keywordLeg(ctx context.Context, req RAGQueryRequest, docIDs []string) ([]Source, error) {
	if rs.chunkRepo == nil {
		return nil, fmt.Errorf("keyword search not available: chunk store not configured")
	}
	if req.WorkspaceID == "" {
		return nil, fmt.Errorf("keyword search requires workspace ID")
	}
	chunks, err := rs.chunkRepo.KeywordSearch(ctx, req.TenantID, req.WorkspaceID, req.Question, docIDs, req.TopK)
	if err != nil {
		sc, _ := observability.SpanFromContext(ctx)
		rs.logger.Error("knowledge.retrieval.dependency_failed", zap.String("trace_id", sc.TraceID),
			zap.String("operation", "keyword_search"), zap.String("error_category", "dependency_unavailable"))
		return nil, ErrRAGDependency
	}
	sources := make([]Source, 0, len(chunks))
	for _, c := range chunks {
		sources = append(sources, Source{
			DocumentID: c.DocID,
			ChunkID:    c.ID,
			Content:    c.Text,
			ChunkIndex: c.Index,
		})
	}
	return sources, nil
}

// hybridLeg runs the hybrid retrieval leg: both the vector and keyword legs
// receive the same visible doc-ID filter, so no unfiltered leg exists.
func (rs *RAGService) hybridLeg(ctx context.Context, req RAGQueryRequest, collectionName, filterExpr string, docIDs []string) ([]knowledgeport.VectorSearchResult, []Source, rerankStats, error) {
	embedder := rs.resolveEmbedder(ctx, req)
	if embedder == nil {
		// 嵌入模型不可用：fail-closed 且上报监控（触发 StratumKnowledgeEmbedUnavailable）。
		if rs.metrics != nil {
			rs.metrics.IncKnowledgeEmbedUnavailable(req.TenantID)
		}
		return nil, nil, rerankStats{}, fmt.Errorf("embedding service not configured: enable an embedding model in model management")
	}
	if rs.chunkRepo == nil {
		return nil, nil, rerankStats{}, fmt.Errorf("hybrid search not available: chunk store not configured")
	}
	legTopK := req.TopK * hybridLegRecallFactor
	if widensRecall(req.Reranking) {
		legTopK = req.TopK * constants.RerankWidenFactor
	}
	vectorResults, pool, err := rs.hybridPool(ctx, req, collectionName, embedder, legTopK, filterExpr, docIDs)
	if err != nil {
		return nil, nil, rerankStats{}, err
	}
	sources, stats, rerankErr := rs.rerankSources(ctx, req, pool)
	if rerankErr != nil {
		return nil, nil, rerankStats{}, rerankErr
	}
	return vectorResults, sources, stats, nil
}

// visibleDocIDs resolves the viewer's document whitelist per the D1
// visibility matrix. unrestricted=true means the whole workspace is visible
// (nil ids → no filter expression). Fail closed: identity or dependency
// unavailability never degrades into a filterless search.
func (rs *RAGService) visibleDocIDs(ctx context.Context, req RAGQueryRequest) ([]string, bool, error) {
	if req.SkipAccessCheck {
		return nil, true, nil
	}
	if rs.wsRepo == nil {
		// No workspace metadata: the caller already scoped the query to a
		// collection, so doc-level visibility cannot be evaluated — keep the
		// pre-existing workspace-scoped behavior (no extra filtering).
		return nil, true, nil
	}
	ws, err := rs.wsRepo.GetByID(ctx, req.TenantID, req.WorkspaceID)
	if err != nil {
		return nil, false, fmt.Errorf("knowledge: resolve workspace visibility: %w", err)
	}
	if ws == nil {
		// Unknown workspace keeps collection-level scoping.
		return nil, true, nil
	}
	role, unrestricted, err := rs.viewerScope(ctx, req, ws)
	if err != nil {
		return nil, false, err
	}
	if unrestricted {
		return nil, true, nil
	}
	ids, err := rs.docRepo.VisibleDocIDs(ctx, req.TenantID, req.WorkspaceID, req.ViewerID, role)
	if err != nil {
		return nil, false, err
	}
	return ids, false, nil
}

// viewerScope resolves the viewer's tenant role and the D1 management
// exemption (tenant admin/owner, workspace creator). Fail closed: empty
// identity or unconfigured resolver/repo returns an error, never an
// unrestricted set.
func (rs *RAGService) viewerScope(ctx context.Context, req RAGQueryRequest, ws *domain.Workspace) (role string, unrestricted bool, err error) {
	if req.ViewerID == "" || rs.roleResolver == nil || rs.docRepo == nil {
		return "", false, fmt.Errorf("knowledge: doc-level visibility unavailable: %w", domain.ErrForbidden)
	}
	role, err = rs.roleResolver.ResolveTenantRole(ctx, req.TenantID, req.ViewerID)
	if err != nil {
		return "", false, fmt.Errorf("knowledge: resolve viewer role: %w", domain.ErrForbidden)
	}
	if role == "owner" || role == "admin" || ws.CreatedBy == req.ViewerID {
		return role, true, nil
	}
	return role, false, nil
}

// PreviewDocument returns the chunk-reassembled content of a document for the
// citation preview UI (GET .../documents/:documentID/preview). The viewer must
// be able to see the document per the D1 visibility matrix; invisible and
// nonexistent documents both surface as ErrDocumentNotFound (404) so access
// attempts cannot probe existence. Platform-managed workspaces (built-in
// knowledge) are exempt from the whitelist but still require the document to
// exist. Fail closed: missing identity or unconfigured repositories reject.
func (rs *RAGService) PreviewDocument(ctx context.Context, tenantID, workspaceName, docID, viewerID string) (*DocumentPreview, error) {
	ws, err := rs.previewWorkspace(ctx, tenantID, workspaceName, viewerID)
	if err != nil {
		return nil, err
	}
	visible, unrestricted, err := rs.visibleDocIDs(ctx, RAGQueryRequest{
		TenantID:    tenantID,
		WorkspaceID: ws.ID,
		ViewerID:    viewerID,
	})
	if err != nil {
		return nil, err
	}
	if !unrestricted && !slices.Contains(visible, docID) {
		return nil, domain.ErrDocumentNotFound
	}
	doc, err := rs.docRepo.GetByID(ctx, tenantID, ws.ID, docID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, domain.ErrDocumentNotFound
	}
	chunks, err := rs.chunkRepo.ListByDoc(ctx, tenantID, ws.ID, docID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: list preview chunks: %w", err)
	}
	preview := &DocumentPreview{
		DocumentID:    docID,
		DocumentTitle: doc.Source,
		ChunkCount:    len(chunks),
	}
	// chunks arrive ordered by chunk index (ListByDoc ORDER BY chunk_index).
	for _, ch := range chunks {
		seg := ChunkSegment{ChunkID: ch.ID, Index: ch.Index, Content: ch.Text}
		attachParent(ctx, rs, tenantID, ws.ID, ch, &seg)
		preview.Segments = append(preview.Segments, seg)
	}
	return preview, nil
}

// previewWorkspace resolves and validates the preview target workspace.
// Fail closed on unconfigured dependencies or missing viewer identity.
func (rs *RAGService) previewWorkspace(ctx context.Context, tenantID, workspaceName, viewerID string) (*domain.Workspace, error) {
	if rs.wsRepo == nil || rs.docRepo == nil || rs.chunkRepo == nil {
		return nil, fmt.Errorf("knowledge: preview dependencies unavailable: %w", domain.ErrForbidden)
	}
	if viewerID == "" {
		return nil, fmt.Errorf("knowledge: preview viewer identity missing: %w", domain.ErrForbidden)
	}
	ws, err := rs.wsRepo.GetByName(ctx, tenantID, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("knowledge: resolve preview workspace: %w", err)
	}
	if ws == nil {
		return nil, domain.ErrDocumentNotFound
	}
	return ws, nil
}

// attachParent best-effort appends a leaf segment's parent content. A parent
// fetch failure degrades to leaf-only content: the leaf is the authoritative
// citation text, the parent is enrichment.
func attachParent(ctx context.Context, rs *RAGService, tenantID, workspaceID string, ch domain.Chunk, seg *ChunkSegment) {
	if ch.ParentID == "" {
		return
	}
	if parent, perr := rs.chunkRepo.GetParentByID(ctx, tenantID, workspaceID, ch.ParentID); perr == nil && parent != nil {
		seg.ParentContent = parent.Content
	}
}

// buildDocFilterExpr renders a doc-ID whitelist as a Milvus filter expression
// (`source_document in [...]`). Empty docIDs → "" (no filter). IDs are quoted
// with %q exactly like pkg/storage/milvus DeleteByDocumentIDs: " and \ are
// escaped, so arbitrary IDs cannot break out of the expression.
func buildDocFilterExpr(docIDs []string) string {
	if len(docIDs) == 0 {
		return ""
	}
	quoted := make([]string, len(docIDs))
	for i, id := range docIDs {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	return fmt.Sprintf("source_document in [%s]", strings.Join(quoted, ","))
}

// filterExprTooLong reports whether a whitelist expression exceeds the Milvus
// expression length bound (D7). Over-long filters fail or, worse, get
// truncated into an incorrect filter — so the caller degrades instead.
func filterExprTooLong(expr string) bool {
	return len(expr) > constants.MaxMilvusFilterLen
}

// widensRecall reports whether the selected rerank identity is an external
// provider. External identities widen the internal candidate pool before
// the narrow rerank; builtin and none keep the plain TopK.
func widensRecall(identity string) bool {
	provider, _ := rerankIdentity(identity)
	return provider != "" && provider != "builtin-score-v1"
}

// vectorToPool converts raw vector hits into sources. Scores are already
// 0-1 normalized similarities produced by the storage layer
// (pkg/storage/milvus.SearchResult.Score), so thresholds and the builtin
// rerank sort uniformly across retrieval modes without a per-mode mapping.
func vectorToPool(results []knowledgeport.VectorSearchResult) []Source {
	pool := make([]Source, 0, len(results))
	for _, vr := range results {
		pool = append(pool, Source{
			DocumentID: vr.SourceDocument,
			ChunkID:    vr.ID,
			Content:    vr.Content,
			ChunkIndex: vr.ChunkIndex,
			Score:      vr.Score,
		})
	}
	return pool
}

// hybridPool runs both retrieval legs concurrently and fuses them with
// reciprocal rank fusion. The vector leg mirrors the vector branch's legacy
// fallback: a missing new name falls back to the legacy collection (upgraded
// workspaces that were not re-ingested are an expected state), and a legacy
// dimension mismatch skips that leg with an empty result instead of failing
// closed — the keyword leg still contributes. A missing collection with no
// documents falls through to the keyword leg alone; other vector failures
// fail closed. filterExpr/docIDs carry the viewer whitelist to both legs
// (docIDs nil when unrestricted); an over-long expression degrades the vector
// leg to empty results while the keyword leg keeps filtering (D7).
func (rs *RAGService) hybridPool(ctx context.Context, req RAGQueryRequest, collectionName string, embedder knowledgeport.Embedder, legTopK int, filterExpr string, docIDs []string) ([]knowledgeport.VectorSearchResult, []Source, error) {
	type vRes struct {
		r []knowledgeport.VectorSearchResult
		e error
	}
	type kRes struct {
		r []domain.Chunk
		e error
	}
	legacyName := constants.CollectionLegacyName(req.TenantID, req.WorkspaceID)
	searchName, legacy := rs.resolveSearchCollection(ctx, collectionName, legacyName, req.WorkspaceID)
	vCh := make(chan vRes, 1)
	kCh := make(chan kRes, 1)
	go func() {
		if docIDs != nil && filterExprTooLong(filterExpr) {
			rs.logger.Warn("knowledge.rag.filter_degraded",
				zap.Int("visible_docs", len(docIDs)),
				zap.Int("filter_len", len(filterExpr)))
			vCh <- vRes{}
			return
		}
		r, e := rs.queryVector(ctx, req.TenantID, req.Question, searchName, legTopK, embedder, req.EmbeddingModel, legacy, filterExpr)
		vCh <- vRes{r, e}
	}()
	go func() {
		if req.WorkspaceID == "" {
			kCh <- kRes{e: fmt.Errorf("keyword search requires workspace ID")}
			return
		}
		r, e := rs.chunkRepo.KeywordSearch(ctx, req.TenantID, req.WorkspaceID, req.Question, docIDs, legTopK)
		kCh <- kRes{r, e}
	}()
	vr := <-vCh
	kr := <-kCh
	if vr.e != nil {
		if errors.Is(vr.e, errCollectionNotFound) {
			if missingErr := rs.handleMissingCollection(ctx, req, searchName); missingErr != nil {
				return nil, nil, missingErr
			}
			// Empty workspace: fall through to the keyword leg alone.
			vr.r = nil
		} else {
			rs.logHybridDependencyFailure(ctx, "hybrid_vector_search")
			return nil, nil, ErrRAGDependency
		}
	}
	if kr.e != nil {
		rs.logHybridDependencyFailure(ctx, "hybrid_keyword_search")
		return nil, nil, ErrRAGDependency
	}
	return vr.r, rrfFuse(vr.r, kr.r), nil
}

// rrfFuse merges both hybrid legs with reciprocal rank fusion, producing a
// score-bearing pool ordered by fused relevance.
func rrfFuse(vectorHits []knowledgeport.VectorSearchResult, keywordHits []domain.Chunk) []Source {
	const rrfK = 60.0
	rrfScores := make(map[string]float64)
	for rank, r := range vectorHits {
		rrfScores[r.ID] += 1.0 / (rrfK + float64(rank+1))
	}
	for rank, c := range keywordHits {
		rrfScores[c.ID] += 1.0 / (rrfK + float64(rank+1))
	}
	srcMap := make(map[string]Source)
	for _, r := range vectorHits {
		srcMap[r.ID] = Source{DocumentID: r.SourceDocument, ChunkID: r.ID,
			Content: r.Content, ChunkIndex: r.ChunkIndex}
	}
	for _, c := range keywordHits {
		if _, ok := srcMap[c.ID]; !ok {
			srcMap[c.ID] = Source{DocumentID: c.DocID, ChunkID: c.ID, Content: c.Text, ChunkIndex: c.Index}
		}
	}
	type scoredSrc struct {
		src   Source
		score float64
	}
	all := make([]scoredSrc, 0, len(rrfScores))
	for id, score := range rrfScores {
		if s, ok := srcMap[id]; ok {
			all = append(all, scoredSrc{s, score})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	pool := make([]Source, 0, len(all))
	for i := range all {
		s := all[i].src
		s.Score = float32(all[i].score)
		pool = append(pool, s)
	}
	return pool
}

// logHybridDependencyFailure records a failed hybrid leg with the query
// trace id so operators can attribute the dependency outage.
func (rs *RAGService) logHybridDependencyFailure(ctx context.Context, operation string) {
	sc, _ := observability.SpanFromContext(ctx)
	rs.logger.Error("knowledge.retrieval.dependency_failed", zap.String("trace_id", sc.TraceID),
		zap.String("operation", operation), zap.String("error_category", "dependency_unavailable"))
}

// expandParentContext attaches parent chunk content for leaf chunks that
// have a parent, giving callers richer context.
func (rs *RAGService) expandParentContext(ctx context.Context, req RAGQueryRequest, result *RAGQueryResult) {
	if rs.chunkRepo == nil || req.WorkspaceID == "" || len(result.Sources) == 0 {
		return
	}
	ids := make([]string, len(result.Sources))
	for i, s := range result.Sources {
		ids[i] = s.ChunkID
	}
	leafChunks, err := rs.chunkRepo.GetChunksByIDs(ctx, req.TenantID, req.WorkspaceID, ids)
	if err != nil {
		return
	}
	parentMap := make(map[string]string) // chunkID → parentID
	for _, lc := range leafChunks {
		if lc.ParentID != "" {
			parentMap[lc.ID] = lc.ParentID
		}
	}
	rs.attachParentContent(ctx, req, result, parentMap)
}

// attachParentContent fills ParentContent for sources whose chunk has a
// parent; missing parents are left untouched.
func (rs *RAGService) attachParentContent(ctx context.Context, req RAGQueryRequest, result *RAGQueryResult, parentMap map[string]string) {
	for i := range result.Sources {
		pid, ok := parentMap[result.Sources[i].ChunkID]
		if !ok {
			continue
		}
		parent, perr := rs.chunkRepo.GetParentByID(ctx, req.TenantID, req.WorkspaceID, pid)
		if perr == nil && parent != nil {
			result.Sources[i].ParentContent = parent.Content
		}
	}
}

// resolveSearchCollection decides the collection to search: the model-suffixed
// name normally, or the legacy (no-suffix) name when the new collection is
// missing. Legacy fallback runs before drift classification: a workspace that
// was upgraded but not re-ingested legitimately lacks the new collection, so
// the old data is searched first; only when both names are missing does
// handleMissingCollection classify the state.
func (rs *RAGService) resolveSearchCollection(ctx context.Context, collectionName, legacyName, workspaceID string) (searchName string, legacy bool) {
	searchName = collectionName
	if _, err := rs.vectorStore.DescribeCollection(ctx, collectionName); isCollectionNotFound(err) {
		searchName = legacyName
		legacy = true
		rs.logger.Info("knowledge.retrieval.legacy_collection_fallback",
			zap.String("collection", legacyName), zap.String("workspace_id", workspaceID))
	}
	return searchName, legacy
}

// queryVector embeds the question and searches the workspace collection.
// embedModel ("" when unknown) drives the collection dimension check; a
// missing collection yields errCollectionNotFound for the caller to classify.
// legacy marks the fallback search on the pre-upgrade (no model suffix)
// collection: a dimension mismatch on it skips retrieval with an empty result
// instead of failing closed, per the spec's legacy-drift contract. expression
// ("" when unrestricted) restricts results to the viewer's visible document
// set.
func (rs *RAGService) queryVector(ctx context.Context, tenantID, question string, collection string, topK int, embedder knowledgeport.Embedder, embedModel string, legacy bool, expression string) ([]knowledgeport.VectorSearchResult, error) {
	rs.logger.Debug("querying vector store")

	if embedder == nil {
		// 嵌入模型不可用：fail-closed 且上报监控（触发 StratumKnowledgeEmbedUnavailable）。
		if rs.metrics != nil {
			rs.metrics.IncKnowledgeEmbedUnavailable(tenantID)
		}
		return nil, fmt.Errorf("embedding service not configured: enable an embedding model in model management")
	}

	queryVector, err := embedder.EmbedVector(ctx, question)
	if err != nil {
		return nil, ErrRAGDependency
	}

	if embedModel != "" {
		if err := rs.validateCollectionDim(ctx, collection, embedModel); err != nil {
			if legacy && errors.Is(err, errLegacyDimMismatch) {
				// legacy 集合维数与当前模型不符：Warn 已在 validateCollectionDim
				// 记录，跳过该集合返回空（spec：legacy dim 不一致不 fail-closed）。
				return []knowledgeport.VectorSearchResult{}, nil
			}
			return nil, err
		}
	}

	results, err := rs.vectorStore.SearchWithFilter(ctx, collection, queryVector, topK, expression)
	if err != nil {
		if isCollectionNotFound(err) {
			return nil, errCollectionNotFound
		}
		return nil, ErrRAGDependency
	}

	return results, nil
}

// validateCollectionDim checks the live collection schema against the
// embedding model before searching: a dimension mismatch means the workspace
// was (re)created under a different model and must fail closed instead of
// returning silently wrong results. A collection missing the user_id column
// is tolerated (legacy tenant-scoped collection) and only logged.
func (rs *RAGService) validateCollectionDim(ctx context.Context, collection, embedModel string) error {
	info, err := rs.vectorStore.DescribeCollection(ctx, collection)
	if err != nil {
		if isCollectionNotFound(err) {
			return errCollectionNotFound
		}
		rs.logger.Error("knowledge.retrieval.dependency_failed",
			zap.String("operation", "describe_collection"), zap.Error(err))
		return ErrRAGDependency
	}
	if info.Dim != 0 && info.Dim != constants.DimensionForModel(embedModel) {
		rs.logger.Warn("knowledge.retrieval.legacy_dim_mismatch",
			zap.String("collection", collection), zap.Int("existing_dim", info.Dim),
			zap.Int("required_dim", constants.DimensionForModel(embedModel)))
		return errLegacyDimMismatch // 调用方对 legacy 名跳过、新名转 ErrRAGDependency
	}
	if !info.HasUserID {
		rs.logger.Warn("collection lacks user_id column, skipping user scope check",
			zap.String("collection", collection))
	}
	return nil
}

// handleMissingCollection classifies a missing vector collection: 0 chunks in
// PG means a legitimately empty workspace (empty result), any chunks means
// drift between PG and Milvus and fails closed. collectionName is the name the
// caller actually searched (the legacy name after a fallback), so logs and
// metrics attribute the failure to the real collection.
func (rs *RAGService) handleMissingCollection(ctx context.Context, req RAGQueryRequest, collectionName string) error {
	if rs.chunkRepo == nil {
		return ErrRAGDependency
	}
	count, err := rs.chunkRepo.CountByWorkspace(ctx, req.TenantID, req.WorkspaceID)
	if err != nil {
		rs.logger.Error("knowledge.retrieval.dependency_failed",
			zap.String("operation", "count_chunks"), zap.Error(err))
		return ErrRAGDependency
	}
	if count > 0 {
		rs.logger.Error("knowledge.retrieval.drift",
			zap.Int64("chunk_count", count), zap.String("collection", collectionName))
		return ErrRAGDependency
	}
	rs.logger.Warn("vector collection not found; workspace has no chunks",
		zap.String("collection", collectionName))
	return nil
}

// rerankTopK is the final result count after narrowing: RerankTopK when set,
// otherwise TopK. Clamp 到 MaxRerankTopK 防御 workspace 配置越界（DB 存量脏值），
// SkipTopKClamp（评估路径）豁免以保留 MaximumEvaluationTopK=100 契约。
func rerankTopK(req RAGQueryRequest) int {
	if req.RerankTopK > 0 {
		if !req.SkipTopKClamp && req.RerankTopK > constants.MaxRerankTopK {
			return constants.MaxRerankTopK
		}
		return req.RerankTopK
	}
	return req.TopK
}

// rerankStats 是 rerank 链路的池统计（阈值过滤前采集），供无答案信号与
// 阈值校准消费。
type rerankStats struct {
	poolSize        int     // rerankSources 入口池大小（阈值过滤前候选数）
	thresholdFilter int     // 阈值过滤掉的条数
	bestScore       float32 // 入口池内最高分（rerank 覆盖前采集）
}

// rerankSources applies the request's rerank strategy to the candidate pool
// and narrows it to the final count. External identities widen the recall
// pool upstream; keyword-mode results never reach here (no scores). Threshold
// filtering applies only to score-bearing sources, so it is safe for vector
// (L2-normalized) and hybrid (RRF) pools alike. PoolSize/BestScore are
// captured at entry (before rerankExternal truncation/overwrite) so no-answer
// statistics reflect the true candidate distribution.
func (rs *RAGService) rerankSources(ctx context.Context, req RAGQueryRequest, pool []Source) ([]Source, rerankStats, error) {
	stats := rerankStats{poolSize: len(pool), bestScore: poolBestScore(pool)}
	provider, model := rerankIdentity(req.Reranking)
	switch provider {
	case "builtin-score-v1":
		pool = rs.rerankBuiltinSemantic(ctx, req, pool)
		sort.SliceStable(pool, func(i, j int) bool { return pool[i].Score > pool[j].Score })
	case "":
		// no rerank: keep retrieval order
	default:
		narrowed, err := rs.rerankExternal(ctx, req, pool, model)
		if err != nil {
			return nil, stats, err
		}
		pool = narrowed
	}
	if req.ScoreThreshold > 0 {
		before := len(pool)
		pool = filterByScoreThreshold(pool, req.ScoreThreshold)
		stats.thresholdFilter = before - len(pool)
	}
	if len(pool) > rerankTopK(req) {
		pool = pool[:rerankTopK(req)]
	}
	return pool, stats, nil
}

// poolBestScore returns the highest score in the pool. Pools without scores
// (keyword leg, P4-normalized) yield 0; consumers treat 0 as "no data".
func poolBestScore(pool []Source) float32 {
	var best float32
	for _, s := range pool {
		if s.Score > best {
			best = s.Score
		}
	}
	return best
}

// recordStats fills the retrieval statistics on the result and closes out the
// no-answer signal: non-empty Sources keep NoAnswer nil (has answer), an
// empty result becomes threshold_filtered (filtered > 0) or no_sources.
func (rs *RAGService) recordStats(result *RAGQueryResult, stats rerankStats) {
	result.CandidateCount = stats.poolSize
	result.BestScore = stats.bestScore
	if len(result.Sources) > 0 {
		return
	}
	if stats.thresholdFilter > 0 {
		result.NoAnswer = buildNoAnswer(NoAnswerThresholdFiltered, stats.poolSize, stats.thresholdFilter, stats.bestScore)
		return
	}
	result.NoAnswer = buildNoAnswer(NoAnswerNoSources, stats.poolSize, 0, stats.bestScore)
}

// rerankExternal re-scores the candidate pool with the configured external
// reranker. Pools below MinRerankCandidates skip the call (stable no-op) to
// avoid paying latency for tiny pools.
func (rs *RAGService) rerankExternal(ctx context.Context, req RAGQueryRequest, pool []Source, model string) ([]Source, error) {
	if rs.reranker == nil {
		return nil, fmt.Errorf("rerank requested (%s) but no external reranker configured", req.Reranking)
	}
	if len(pool) < constants.MinRerankCandidates {
		rs.logger.Warn("rerank skipped: candidate pool too small", zap.Int("pool_size", len(pool)))
		if rs.metrics != nil {
			rs.metrics.IncRerankRequest(req.TenantID, model, "skipped")
		}
		return pool, nil
	}
	if len(pool) > constants.RerankMaxCandidates {
		pool = pool[:constants.RerankMaxCandidates]
	}
	docs := make([]string, len(pool))
	for i, s := range pool {
		docs[i] = s.Content
	}
	results, err := rs.reranker.Rerank(ctx, knowledgeport.RerankRequest{
		Query: req.Question, Documents: docs, Model: model, TopN: rerankTopK(req),
	})
	if err != nil {
		rs.logger.Error("knowledge.retrieval.rerank_failed", zap.Error(err))
		return nil, fmt.Errorf("rerank: %w", err)
	}
	reordered := make([]Source, 0, len(results))
	for _, r := range results {
		if r.Index >= 0 && r.Index < len(pool) {
			s := pool[r.Index]
			s.Score = r.Score
			reordered = append(reordered, s)
		}
	}
	return reordered, nil
}

// rerankSemantic 用平台 LLM 语义重排器对召回池精排：先按召回分数取前
// semanticTopN 条（与池取 min）listwise 打分，未被打分的候选按召回分补尾。
// LLM 分数覆盖召回分数；返回后由 rerankSources 统一降序排序（LLM 分与召回
// 分同池混排）。失败向上传播，调用方按 fail-open 降级。
func (rs *RAGService) rerankSemantic(ctx context.Context, req RAGQueryRequest, pool []Source) ([]Source, error) {
	topN := min(rs.semanticTopN, len(pool))
	if topN < 2 {
		return pool, nil // 单候选/空池语义重排无意义，保持池走排序
	}
	docs := make([]string, topN) // 截断由重排器内部负责
	for i := range docs {
		docs[i] = pool[i].Content
	}
	results, err := rs.semanticReranker.Rerank(ctx, knowledgeport.RerankRequest{
		Query: req.Question, Documents: docs, Model: req.RerankModel, TopN: topN,
		ScoringInstructions: req.RerankScoringInstructions,
	})
	if err != nil {
		return nil, err
	}
	narrowed := make([]Source, 0, topN)
	used := make(map[int]struct{}, len(results))
	for _, r := range results {
		if r.Index < 0 || r.Index >= topN {
			continue
		}
		if _, ok := used[r.Index]; ok {
			continue
		}
		used[r.Index] = struct{}{}
		s := pool[r.Index]
		s.Score = r.Score
		narrowed = append(narrowed, s)
	}
	return fillUnscoredTail(narrowed, used, pool, topN), nil
}

// rerankBuiltinSemantic 是 builtin-score-v1 的 LLM 语义精排入口：注入的重排器
// 存在且池 ≥2 时打分，失败 fail-open 降级为召回分数排序（WARN + degraded 指标），
// 检索永不失败。未装配/小池直接返回原池，由 rerankSources 统一排序。
func (rs *RAGService) rerankBuiltinSemantic(ctx context.Context, req RAGQueryRequest, pool []Source) []Source {
	if rs.semanticReranker == nil || len(pool) < 2 {
		return pool
	}
	narrowed, err := rs.rerankSemantic(ctx, req, pool)
	if err != nil {
		rs.logger.Warn("knowledge.retrieval.llm_rerank_degraded",
			zap.Error(err), zap.Int("pool_size", len(pool)), zap.Int("top_n", rs.semanticTopN))
		if rs.metrics != nil {
			rs.metrics.IncRerankRequest(reqctx.TenantIDFromContext(ctx), "builtin-llm", "degraded")
		}
		return pool
	}
	return narrowed
}

// fillUnscoredTail 用召回分补足未被打分的候选（LLM 结果去重后），把 narrowed
// 补齐到 topN 条。
func fillUnscoredTail(narrowed []Source, used map[int]struct{}, pool []Source, topN int) []Source {
	for i := 0; i < topN && len(narrowed) < topN; i++ {
		if _, ok := used[i]; !ok {
			narrowed = append(narrowed, pool[i]) // 未被打分候选按召回分补尾
		}
	}
	return narrowed
}

func filterByScoreThreshold(pool []Source, threshold float32) []Source {
	filtered := make([]Source, 0, len(pool))
	for _, s := range pool {
		if s.Score >= threshold {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func (rs *RAGService) RetrieveRelevantChunks(ctx context.Context, tenantID, question, workspace, embedModel string, topK int, viewerID string) ([]string, error) {
	// D12 gate: this path (tool/test caller) resolves no visible set itself,
	// so the identity check is the whole access control — an empty viewer
	// identity fails closed instead of returning unfiltered chunks.
	if viewerID == "" {
		return nil, fmt.Errorf("knowledge: retrieval viewer identity required")
	}
	if tenantID == "" {
		return nil, fmt.Errorf("knowledge: tenant_id is empty")
	}
	collectionName := constants.CollectionName(tenantID, workspace, embedModel)

	vectorResults, err := rs.queryVector(ctx, tenantID, question, collectionName, topK, rs.embeddingSvc, embedModel, false, "")
	if err != nil {
		if isCollectionNotFound(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var chunks []string
	for _, result := range vectorResults {
		chunks = append(chunks, result.Content)
	}

	return chunks, nil
}

func (rs *RAGService) BuildPrompt(question string, chunks []string) string {
	var prompt strings.Builder

	prompt.WriteString("Answer the following question based on the provided context:\n\n")
	fmt.Fprintf(&prompt, "Question: %s\n\n", question)

	if len(chunks) > 0 {
		prompt.WriteString("Relevant document chunks:\n")
		for i, chunk := range chunks {
			fmt.Fprintf(&prompt, "%d. %s\n", i+1, chunk)
		}
		prompt.WriteString("\n")
	}

	// few-shot 正反例：把拒答行为教给主模型（system prompt 来自 DB，代码
	// 侧只动模板）。反例刻意模拟"模型拿训练记忆硬答"的典型失败模式。
	prompt.WriteString(`Provide a clear, accurate answer based on the context above.

Rules:
- Answer ONLY from the context. If the context doesn't contain enough information to answer, say so explicitly ("the knowledge base does not contain enough information") and do not guess.

Example (good): context mentions no pricing → "The knowledge base does not contain pricing information."
Example (bad): context mentions no pricing → "Pricing starts at $10/month." (fabricated from training data)`)

	return prompt.String()
}

// RAGSearchSource is chunk-level retrieval provenance: which workspace the
// chunk came from, its owning document (for citation display), and (for
// vector retrieval) its similarity score. Keyword mode produces no score
// (HasScore=false). DocumentTitle/Snippet are display metadata only — the
// visible-set filter already ran inside Query before any source is emitted.
// ParentContent is the whole enclosing section (Parent-Child strategy only),
// filled by Query's parent expansion; empty when the leaf has no parent.
type RAGSearchSource struct {
	WorkspaceID   string
	WorkspaceName string
	ChunkID       string
	DocumentID    string
	DocumentTitle string // source file name of the owning document
	Snippet       string // rune-truncated chunk content preview
	ParentContent string // whole enclosing section when the leaf has a parent
	Score         float64
	HasScore      bool
}

// RAGSearchEvidence is the structured result of a knowledge search: the
// concatenated context block plus per-chunk provenance. Wiring adapters map
// it to the agent-side evidence type; the application layer keeps its own
// type so knowledge never imports agent ports. NoAnswer is nil when at least
// one workspace produced sources (at-least-one aggregation).
type RAGSearchEvidence struct {
	Content  string
	Sources  []RAGSearchSource
	NoAnswer *NoAnswerInfo
}

type wsEvidenceResult struct {
	content  string
	sources  []RAGSearchSource
	noAnswer *NoAnswerInfo
	err      error
}

// NewRAGSearchEvidenceFn mirrors NewRAGSearchFn (same fan-out, concurrency
// bound and at-least-one semantics) but retains per-chunk provenance so
// callers can record retrieval evidence without re-querying.
// viewerID is the end user whose document whitelist scopes every search.
func NewRAGSearchEvidenceFn(rs *RAGService, tenantID, viewerID string) func(
	ctx context.Context, workspaces []string, query string, topK int,
) (RAGSearchEvidence, error) {
	return func(ctx context.Context, workspaces []string, query string, topK int) (RAGSearchEvidence, error) {
		results := make([]wsEvidenceResult, len(workspaces))
		sem := make(chan struct{}, constants.MaxConcurrentWorkspaceSearch)
		var wg sync.WaitGroup
		for i, ws := range workspaces {
			sem <- struct{}{}
			wg.Add(1)
			go func(i int, ws string) {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] = searchWorkspaceWithEvidence(ctx, rs, tenantID, viewerID, ws, query, topK)
			}(i, ws)
		}
		wg.Wait()
		return rs.mergeEvidenceResults(results)
	}
}

func searchWorkspaceWithEvidence(ctx context.Context, rs *RAGService, tenantID, viewerID, ws, query string, topK int) wsEvidenceResult {
	rw, err := resolveWorkspaceConfig(ctx, rs, tenantID, ws, topK)
	if err != nil {
		return wsEvidenceResult{err: err}
	}
	out, err := rs.Query(ctx, RAGQueryRequest{
		WorkspaceID:    rw.workspaceID,
		Workspace:      ws,
		Question:       query,
		TenantID:       tenantID,
		Mode:           rw.mode,
		TopK:           rw.effectiveTopK,
		EmbeddingModel: rw.embedModel,
		ScoreThreshold: rw.threshold,
		ViewerID:       viewerID,
		// System-actor contexts (privileged wiring paths) carry admin-owner
		// trust and bypass the D2 gate.
		SkipAccessCheck: reqctx.SystemActorFromContext(ctx) != "",
		// workspace 重排触发与模型/评分指令单一事实源：Reranking 使普通查询
		// 触发配置的重排（builtin 或外部 provider:model）；指令随策略消费。
		Reranking:                 rw.reranking,
		RerankModel:               rw.rerankModel,
		RerankTopK:                rw.rerankTopK,
		JudgeModel:                rw.judgeModel,
		RerankScoringInstructions: rw.rerankScoringInstructions,
		JudgeScoringInstructions:  rw.judgeScoringInstructions,
	})
	if err != nil {
		return wsEvidenceResult{err: err}
	}
	// 充分性门（仅 evidence 路径）：判 INSUFFICIENT 时本 workspace 按无内容
	// 处理（Sources 置空 + NoAnswer=insufficient_evidence），聚合按严重度
	// 上报；gate 无法判定时 fail-open 降级原样放行（不误杀检索）。
	out = rs.judgeSufficiencyGate(ctx, tenantID, ws, query, rw.judgeModel, rw.judgeScoringInstructions, out)
	titles := rs.documentTitles(ctx, tenantID, rw.workspaceID)
	sources := make([]RAGSearchSource, 0, len(out.Sources))
	for _, src := range out.Sources {
		sources = append(sources, RAGSearchSource{
			WorkspaceID:   rw.workspaceID,
			WorkspaceName: ws,
			ChunkID:       src.ChunkID,
			DocumentID:    src.DocumentID,
			DocumentTitle: titles[src.DocumentID],
			Snippet:       textutil.TruncateRunes(src.Content, constants.MaxSourceSnippetRunes),
			ParentContent: src.ParentContent,
			Score:         float64(src.Score),
			HasScore:      src.Score != 0,
		})
	}
	return wsEvidenceResult{content: formatSources(out.Sources), sources: sources, noAnswer: out.NoAnswer}
}

// documentTitles maps doc ID to its source file name for citation display.
// Best-effort: a repo failure yields an empty map (WARN) so retrieval still
// succeeds — titles are display metadata, and the visible-set filter already
// ran inside Query before any source was emitted.
func (rs *RAGService) documentTitles(ctx context.Context, tenantID, workspaceID string) map[string]string {
	titles := make(map[string]string)
	if rs.docRepo == nil {
		return titles
	}
	docs, err := rs.docRepo.List(ctx, tenantID, workspaceID)
	if err != nil {
		rs.logger.Warn("citation titles unavailable",
			zap.String("tenant_id", tenantID), zap.String("workspace_id", workspaceID), zap.Error(err))
		return titles
	}
	for _, doc := range docs {
		if doc != nil {
			titles[doc.ID] = doc.Source
		}
	}
	return titles
}

// decorateSourceTitles backfills each query source's display title from the
// workspace document index. Titles are display metadata only: index read
// failures leave them empty rather than failing the query (documentTitles
// logs the warning).
func (rs *RAGService) decorateSourceTitles(ctx context.Context, tenantID, workspaceID string, sources []Source) {
	if len(sources) == 0 {
		return
	}
	applySourceTitles(sources, rs.documentTitles(ctx, tenantID, workspaceID))
}

func applySourceTitles(sources []Source, titles map[string]string) {
	for i := range sources {
		if title, ok := titles[sources[i].DocumentID]; ok {
			sources[i].DocumentTitle = title
		}
	}
}

// mergeEvidenceResults keeps the same at-least-one semantics as mergeResults:
// failed workspaces are skipped, successful ones contribute, and the first
// error surfaces only when nothing was produced at all; partial failure
// warns but keeps the successful content and their evidence. The no-answer
// signal aggregates the same way: any workspace with sources keeps NoAnswer
// nil; when all succeed empty, the highest-severity reason wins.
func (rs *RAGService) mergeEvidenceResults(results []wsEvidenceResult) (RAGSearchEvidence, error) {
	var combined strings.Builder
	var sources []RAGSearchSource
	var noAnswer *NoAnswerInfo
	var firstErr error
	failures := 0
	for _, r := range results {
		if r.err != nil {
			failures++
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		combined.WriteString(r.content)
		sources = append(sources, r.sources...)
		noAnswer = mergeNoAnswer(noAnswer, r.noAnswer)
	}
	if failures > 0 {
		rs.logger.Warn("knowledge.rag.partial_failure",
			zap.Int("failed_workspaces", failures),
			zap.Int("total_workspaces", len(results)),
			zap.Error(firstErr))
	}
	if combined.Len() == 0 && firstErr != nil {
		return RAGSearchEvidence{}, firstErr
	}
	return RAGSearchEvidence{Content: combined.String(), Sources: sources, NoAnswer: noAnswer}, nil
}
