package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	jschema "github.com/byteBuilderX/stratum/pkg/jsonschema"
	"github.com/byteBuilderX/stratum/pkg/observability"
	storagemilvus "github.com/byteBuilderX/stratum/pkg/storage/milvus"
	vector "github.com/byteBuilderX/stratum/pkg/vector"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// RecallRequest holds the parsed input for the recall_memory tool.
type RecallRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// RecallEntry represents a single memory result returned to the agent.
type RecallEntry struct {
	Content    string  `json:"content"`
	Role       string  `json:"role"`
	Importance float64 `json:"importance"`
	CreatedAt  string  `json:"created_at"`
}

// RecallResult is a slice of recalled memory entries.
type RecallResult []RecallEntry

type recallCandidate struct {
	ID    string
	Entry RecallEntry
}

type scoredRecallCandidate struct {
	candidate recallCandidate
	score     float64
	textHit   bool
}

// RecallToolDefinition returns the tool schema for recall_memory.
func RecallToolDefinition() map[string]any {
	return map[string]any{
		"name":        "stratum_recall_memory",
		"description": "Search long-term memory for relevant past interactions, entities, and context. Use when you need to recall information from previous conversations.",
		"input_schema": jschema.Must(jschema.Object(
			jschema.RequiredProp("query", jschema.String("Search query to find relevant memories")),
			jschema.OptionalProp("limit", jschema.Integer(nil, nil, "Max results (1-20, default 5)")),
		)).Map(),
	}
}

// vectorSearcher is the minimal slice of *vector.VectorStore that recall needs.
// Narrowing to this interface (rather than the concrete store) lets the
// dual-collection fusion in tryVectorSearch be unit-tested with a fake, without
// standing up Milvus. *vector.VectorStore satisfies it via SearchWithFilter.
type vectorSearcher interface {
	SearchWithFilter(ctx context.Context, collectionName string, queryVector []float32, topK int, expression string, partitions ...string) ([]vector.SearchResult, error)
}

// recallDB is the minimal slice of *pgxpool.Pool that text recall needs.
// Narrowing to this interface (rather than the concrete pool) lets Handle's
// text fallback be unit-tested with pgxmock, mirroring the vectorSearcher
// narrowing above and the tenantPool pattern in persistence.
type recallDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

// RecallHandler executes recall_memory queries against the memory_entries table.
// It retrieves semantic and text candidates, then fuses them with RRF.
type RecallHandler struct {
	pool          recallDB
	logger        *zap.Logger
	embedSvc      EmbedClient
	embedResolver EmbedServiceResolver
	vectorDB      vectorSearcher
	metrics       observability.MetricsProvider
	resolver      memport.PlatformParamResolver
}

// NewRecallHandler creates a RecallHandler backed by the given pool.
func NewRecallHandler(pool *pgxpool.Pool, logger *zap.Logger, embedSvc EmbedClient, embedResolver EmbedServiceResolver, vectorDB *vector.VectorStore) *RecallHandler {
	h := &RecallHandler{pool: pool, logger: logger, embedSvc: embedSvc, embedResolver: embedResolver, metrics: observability.NoopMetrics{}}
	// Guard against the typed-nil trap: a nil *vector.VectorStore stored in an
	// interface field is NOT == nil, so tryVectorSearch's nil check would pass
	// and then panic. Only assign when the concrete pointer is non-nil.
	if vectorDB != nil {
		h.vectorDB = vectorDB
	}
	return h
}

// WithMetrics injects a MetricsProvider; returns the handler for chaining.
func (h *RecallHandler) WithMetrics(m observability.MetricsProvider) *RecallHandler {
	h.metrics = m
	return h
}

// SetPlatformParamResolver wires the platform parameter resolver
// (registry-backed); nil keeps the constant default for recall_top_k.
func (h *RecallHandler) SetPlatformParamResolver(r memport.PlatformParamResolver) { h.resolver = r }

// recallTopK 在工具 limit 缺失/非法时解析 memory.recall_top_k（平台级，
// clamp [1,20]）。解析失败或 resolver 缺失回退 constants.MemoryRecallTopK(5)
// ——与 registry Default=5 对齐，避免未配置 5→10 静默漂移。
func (h *RecallHandler) recallTopK(ctx context.Context) int {
	if h.resolver == nil {
		return constants.MemoryRecallTopK
	}
	v, ok, err := h.resolver.ResolvePlatform(ctx, "memory.recall_top_k")
	if err != nil || !ok {
		return constants.MemoryRecallTopK
	}
	n := coerceResourceInt(v, constants.MemoryRecallTopK)
	if n < constants.MemoryRecallMinTopK {
		return constants.MemoryRecallMinTopK
	}
	if n > constants.MemoryRecallMaxTopK {
		return constants.MemoryRecallMaxTopK
	}
	return n
}

// Handle executes the recall_memory tool invocation.
func (h *RecallHandler) Handle(ctx context.Context, tenantID, userID, agentID, scope string, input map[string]any) (string, error) {
	start := time.Now()
	raw, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal input: %w", err)
	}
	var req RecallRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", fmt.Errorf("unmarshal request: %w", err)
	}

	if req.Query == "" {
		return "error: query is required", nil
	}
	// 工具显式传合法 limit(1-20) 优先;缺失/非法时回退 memory.recall_top_k
	// (agent 维度,失败回退 5)。
	if req.Limit <= 0 || req.Limit > constants.MemoryRecallMaxTopK {
		req.Limit = h.recallTopK(ctx)
	}

	vectorCandidates, vecErr := h.tryVectorSearch(ctx, tenantID, userID, agentID, scope, req)
	textCandidates, err := h.textSearchCandidates(ctx, tenantID, userID, agentID, scope, req)
	h.metrics.RecordMemoryRetrievalDuration("recall_hybrid", time.Since(start).Seconds())
	if err != nil && len(vectorCandidates) == 0 {
		h.metrics.IncKnowledgeQuery("recall", "error")
		return "", err
	}
	if err != nil {
		h.logger.Debug("memory.recall: text search failed, using vector candidates", zap.Error(err))
	}

	results := fuseRecallCandidates(vectorCandidates, textCandidates, req.Limit)
	if len(results) == 0 {
		h.metrics.IncKnowledgeQuery("recall", "success")
		return "No relevant memories found.", nil
	}

	out, _ := json.Marshal(results)
	sc, _ := observability.SpanFromContext(ctx)
	h.logger.Debug("memory.recall.hybrid",
		zap.String("trace_id", sc.TraceID),
		zap.String("tenant_id", tenantID),
		zap.String("query", req.Query),
		zap.Int("vector_results", len(vectorCandidates)),
		zap.Int("text_results", len(textCandidates)),
		zap.Int("results", len(results)),
		// vecErr 非 nil 表示向量库 outage，已在 searchAllCollections 内 ERROR log
		// + degraded 指标；此处仅随 Debug 链路追溯，zap.Error(nil) 自动省略。
		zap.Error(vecErr))
	h.metrics.IncKnowledgeQuery("recall", "success")
	return string(out), nil
}

// tryVectorSearch 返回向量候选与是否发生向量库 outage。非 nil 的 error 表示
// 至少一个候选 collection 遭遇非 not-found 的查询失败（见 searchAllCollections
// 的分类）——调用方（Handle）保持 text 降级契约不变，outage 信号由
// searchAllCollections 内以 ERROR log + degraded 指标发出。
func (h *RecallHandler) tryVectorSearch(ctx context.Context, tenantID, userID, agentID, scope string, req RecallRequest) ([]recallCandidate, error) {
	embedSvc := h.embedSvc
	if embedSvc == nil && h.embedResolver != nil {
		embedSvc = h.embedResolver(ctx, tenantID)
	}
	if embedSvc == nil || h.vectorDB == nil {
		return nil, nil
	}

	vec, err := embedSvc.EmbedVector(ctx, req.Query)
	if err != nil {
		h.logger.Debug("memory.recall: embed failed, falling back to text search", zap.Error(err))
		return nil, nil
	}

	if strings.ContainsAny(userID, `"'\`) {
		return nil, nil
	}
	var expr string
	if scope == "agent" && agentID != "" && !strings.ContainsAny(agentID, `"'\`) {
		expr = fmt.Sprintf(`user_id == "%s" && agent_id == "%s" && scope == "agent"`, userID, agentID)
	} else if userID != "" {
		expr = fmt.Sprintf(`user_id == "%s" && scope == "user"`, userID)
	}

	// 候选集合 = raw 与 facts 分组（各自 新名 ∪ legacy 名）。向量 metadata 没有
	// status/expiry 字段，必须回 PG 校验：facts 只保留 status='active'，raw 只
	// 保留未过期的 memory_entries——与文本侧语义对齐，即使生命周期清理尚未
	// 运行也保证召回正确。SearchWithFilter 对不存在的 collection 报错被跳过，
	// dim mismatch（模型切换后旧集合维度不符）同样跳过——天然 legacy 回退
	// （不 fail-closed），详见 searchAllCollections 的错误分类。
	// embedSvc 已由上方 guard 保证非 nil；Model() 可能为空串 → legacy-only。
	rawCols, factsCols := recallCandidateCollectionGroups(tenantID, embedSvc.Model())
	rawResults, rawOutage := h.searchAllCollections(ctx, rawCols, vec, req.Limit*2, expr)
	factsResults, factsOutage := h.searchAllCollections(ctx, factsCols, vec, req.Limit*2, expr)

	rawResults, factsResults, err = h.filterRecallResults(ctx, tenantID, rawResults, factsResults)
	if err != nil {
		return nil, err
	}

	merged := make([]vector.SearchResult, 0, len(rawResults)+len(factsResults))
	merged = append(merged, rawResults...)
	merged = append(merged, factsResults...)
	// Score is a 0-1 normalized similarity (larger = more relevant) produced by
	// the storage layer. Sort descending so downstream RRF ranks the closest
	// match across both collections first; the re-sort is required because the
	// raw/facts legs each returned their own best-first order.
	sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	outage := errors.Join(rawOutage, factsOutage)
	recordVectorDegradation(h.metrics, outage)

	var entries []recallCandidate
	for _, r := range merged {
		if r.Content != "" {
			entries = append(entries, recallCandidate{
				ID: r.ID,
				Entry: RecallEntry{
					Content: r.Content,
				},
			})
		}
	}
	return entries, outage
}

// filterRecallResults 分别过滤 raw 与 facts 候选：raw 只保留未过期的
// memory_entries，facts 只保留 active 状态。任一过滤失败即 fail-closed——
// 未验证的候选不得进入召回。
func (h *RecallHandler) filterRecallResults(ctx context.Context, tenantID string, rawResults, factsResults []vector.SearchResult) ([]vector.SearchResult, []vector.SearchResult, error) {
	rawResults, err := h.keepLiveEntryResults(ctx, tenantID, rawResults)
	if err != nil {
		return nil, nil, fmt.Errorf("filter raw recall candidates: %w", err)
	}
	factsResults, err = h.keepActiveFactResults(ctx, tenantID, factsResults)
	if err != nil {
		return nil, nil, fmt.Errorf("filter fact recall candidates: %w", err)
	}
	return rawResults, factsResults, nil
}

// recordVectorDegradation 在向量路径发生 outage 时发一次 degraded 信号。
// 分组搜索后整个向量路径只记一次：任一分组 outage 都代表向量库降级，避免
// 同一查询重复计数。
func recordVectorDegradation(metrics observability.MetricsProvider, outage error) {
	if outage != nil {
		metrics.IncKnowledgeQuery("recall", "degraded")
	}
}

// recallCandidateCollections 返回查询候选：模型非空 → [新 raw, legacy raw,
// 新 facts, legacy facts]（升级后数据在新名，升级前在 legacy 名）；模型未知
// → 仅 legacy 对（空模型后缀的新名无意义且升级前数据都在旧名）。
func recallCandidateCollections(tenantID, embedModel string) []string {
	raw, facts := recallCandidateCollectionGroups(tenantID, embedModel)
	return append(raw, facts...)
}

// recallCandidateCollectionGroups 按 raw/facts 分组返回候选集合列表，使向量
// 结果能在回 PG 校验时区分来源（raw 校验过期、facts 校验 active 状态）。
func recallCandidateCollectionGroups(tenantID, embedModel string) (raw, facts []string) {
	if embedModel == "" {
		return []string{memoryCollectionLegacyName(tenantID)}, []string{memoryFactsCollectionLegacyName(tenantID)}
	}
	return []string{
			memoryCollectionName(tenantID, embedModel), memoryCollectionLegacyName(tenantID),
		}, []string{
			memoryFactsCollectionName(tenantID, embedModel), memoryFactsCollectionLegacyName(tenantID),
		}
}

// keepActiveFactResults keeps only fact-sourced results whose PG row is still
// active. Superseded/archived facts must not be recalled even if their vectors
// linger (e.g. deletion failed or predates this change); the trigram side
// already filters status='active', so this aligns both legs.
func (h *RecallHandler) keepActiveFactResults(ctx context.Context, tenantID string, results []vector.SearchResult) ([]vector.SearchResult, error) {
	return h.intersectResults(ctx, tenantID, results,
		`SELECT id::text FROM memory_facts WHERE id::text = ANY($1) AND status = 'active'`)
}

// keepLiveEntryResults keeps only raw-turn results whose memory_entries row is
// not expired (per-entry expires_at or the global episodic TTL). Vectors are
// physically cleaned by the daily GC; the PG-side filter closes the window in
// between so expired entries never surface.
func (h *RecallHandler) keepLiveEntryResults(ctx context.Context, tenantID string, results []vector.SearchResult) ([]vector.SearchResult, error) {
	return h.intersectResults(ctx, tenantID, results,
		`SELECT id::text FROM memory_entries WHERE id::text = ANY($1) AND (expires_at IS NULL OR expires_at > NOW())`)
}

// intersectResults filters results down to ids present in the PG table under
// query. Candidate count is bounded (≤ 2*topK); the transaction is read-only
// and rolled back. Fail-closed: a filter query error drops the whole group so
// unverifiable candidates never surface (the caller degrades to text recall).
func (h *RecallHandler) intersectResults(ctx context.Context, tenantID string, results []vector.SearchResult, query string) ([]vector.SearchResult, error) {
	if len(results) == 0 || h.pool == nil {
		return results, nil
	}
	ids := collectResultIDs(results)
	if len(ids) == 0 {
		return results, nil
	}
	keep, err := h.queryKeepIDs(ctx, tenantID, query, ids)
	if err != nil {
		return nil, err
	}
	return filterResultsByIDs(results, keep), nil
}

// collectResultIDs 提取结果的非空 id，保持原有顺序。
func collectResultIDs(results []vector.SearchResult) []string {
	ids := make([]string, 0, len(results))
	for _, r := range results {
		if r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// filterResultsByIDs 只保留 keep 集合中的结果（复用原切片底层数组）。
func filterResultsByIDs(results []vector.SearchResult, keep map[string]struct{}) []vector.SearchResult {
	out := results[:0]
	for _, r := range results {
		if _, ok := keep[r.ID]; ok {
			out = append(out, r)
		}
	}
	return out
}

// queryKeepIDs 以只读事务查询 ids 在 PG 表中的有效集合。Fail-closed：查询
// 失败返回错误，调用方丢弃整组候选（未验证的候选不得进入召回）。
func (h *RecallHandler) queryKeepIDs(ctx context.Context, tenantID, query string, ids []string) (map[string]struct{}, error) {
	schema := "tenant_" + tenantID
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public", pgx.Identifier{schema}.Sanitize())); err != nil {
		return nil, fmt.Errorf("set schema: %w", err)
	}
	rows, err := tx.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("query recall filter: %w", err)
	}
	defer rows.Close()
	keep := make(map[string]struct{}, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan recall filter id: %w", err)
		}
		keep[id] = struct{}{}
	}
	return keep, rows.Err()
}

// searchAllCollections 对每个候选 collection 查询并合并结果；单个 collection
// 查询失败不 fail-closed——legacy 回退由"先新名后旧名"的顺序天然实现。失败按
// 性质分类：collection-not-found 与 dim mismatch（模型切换后旧集合维度不符）
// 都是升级前存量数据的预期状态，Debug 后静默跳过；其余错误（Milvus 连接/
// 超时等）才是向量库 outage——降级保留，但必须 ERROR 可见并计入 degraded
// 指标，禁止无声降级。返回的 error 为首个 outage（nil 表示无 outage），供调用
// 方追溯。
func (h *RecallHandler) searchAllCollections(ctx context.Context, collections []string, vec []float32, limit int, expr string) ([]vector.SearchResult, error) {
	var merged []vector.SearchResult
	var outageErr error
	for _, collection := range collections {
		results, err := h.vectorDB.SearchWithFilter(ctx, collection, vec, limit, expr)
		if err == nil {
			merged = append(merged, results...)
			continue
		}
		if errors.Is(err, storagemilvus.ErrCollectionNotFound) {
			h.logger.Debug("memory.recall: collection not found, legacy fallback",
				zap.String("collection", collection))
			continue
		}
		if errors.Is(err, storagemilvus.ErrDimensionMismatch) {
			// 模型切换后旧集合维度与当前 embedding 不一致：确定性数据形态错误，
			// 与 collection-not-found 同级——Debug 跳过，不 ERROR、不计 degraded、
			// 不构成 outage。
			h.logger.Debug("memory.recall: collection dimension mismatch, legacy fallback",
				zap.String("collection", collection))
			continue
		}
		if outageErr == nil {
			outageErr = err
		}
		h.logger.Error("memory.recall.vector_search_failed",
			zap.String("collection", collection), zap.Error(err))
	}
	return merged, outageErr
}

func (h *RecallHandler) textSearchCandidates(ctx context.Context, tenantID, userID, agentID, scope string, req RecallRequest) ([]recallCandidate, error) {
	if h.pool == nil {
		return nil, nil
	}
	schema := "tenant_" + tenantID
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public", pgx.Identifier{schema}.Sanitize())); err != nil {
		return nil, fmt.Errorf("set schema: %w", err)
	}

	baseQuery := `SELECT id, content, role, importance, created_at FROM memory_entries WHERE enriched_at IS NOT NULL`
	args := []any{}
	argIdx := 1

	baseQuery += fmt.Sprintf(" AND content ILIKE '%%' || $%d || '%%'", argIdx)
	args = append(args, req.Query)
	argIdx++

	baseQuery += fmt.Sprintf(" AND user_id = $%d", argIdx)
	args = append(args, userID)
	argIdx++

	if scope == "agent" && agentID != "" {
		baseQuery += fmt.Sprintf(" AND agent_id = $%d AND scope = 'agent'", argIdx)
		args = append(args, agentID)
		argIdx++
	} else {
		baseQuery += " AND scope = 'user'"
	}

	baseQuery += " ORDER BY importance DESC, created_at DESC"
	baseQuery += fmt.Sprintf(" LIMIT $%d", argIdx)
	args = append(args, req.Limit*2)

	rows, err := tx.Query(ctx, baseQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	var results []recallCandidate
	for rows.Next() {
		var id string
		var e RecallEntry
		var createdAt any
		if err := rows.Scan(&id, &e.Content, &e.Role, &e.Importance, &createdAt); err != nil {
			continue
		}
		e.CreatedAt = fmt.Sprintf("%v", createdAt)
		results = append(results, recallCandidate{ID: id, Entry: e})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan memories: %w", err)
	}
	return results, nil
}

func fuseRecallCandidates(vectorCandidates, textCandidates []recallCandidate, topK int) RecallResult {
	if topK <= 0 {
		topK = 5
	}
	byID := make(map[string]scoredRecallCandidate, len(vectorCandidates)+len(textCandidates))
	k := float64(constants.MemoryRRFConstant)

	for rank, candidate := range vectorCandidates {
		if candidate.ID == "" {
			candidate.ID = candidate.Entry.Content
		}
		current := byID[candidate.ID]
		if current.candidate.ID == "" {
			current.candidate = candidate
		}
		current.score += 1.0 / (k + float64(rank+1))
		byID[candidate.ID] = current
	}

	for rank, candidate := range textCandidates {
		if candidate.ID == "" {
			candidate.ID = candidate.Entry.Content
		}
		current := byID[candidate.ID]
		if current.candidate.ID == "" || current.candidate.Entry.Role == "" {
			current.candidate = candidate
		}
		current.score += 1.0 / (k + float64(rank+1))
		current.textHit = true
		byID[candidate.ID] = current
	}

	scored := make([]scoredRecallCandidate, 0, len(byID))
	for _, candidate := range byID {
		scored = append(scored, candidate)
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].textHit != scored[j].textHit {
			return scored[i].textHit
		}
		return scored[i].candidate.Entry.Importance > scored[j].candidate.Entry.Importance
	})

	if topK > len(scored) {
		topK = len(scored)
	}
	out := make(RecallResult, 0, topK)
	for i := 0; i < topK; i++ {
		out = append(out, scored[i].candidate.Entry)
	}
	return out
}
