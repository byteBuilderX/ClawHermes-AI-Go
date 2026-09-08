// Package milvus provides vector database integration via the Milvus SDK.
//
// This is a Phase 1 DDD-refactor relocation of pkg/vector. The old import
// path is retained as a re-export alias and will be removed in phase 5.

package milvus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"go.uber.org/zap"
)

// defaultMetricType is the single source of truth for the vector metric used
// when building a collection index and when searching it. Milvus fixes the
// metric on the index and requires search to pass the same one (otherwise
// "metric type not match"), so all three call sites must share one value and
// never drift. COSINE is scale-invariant to vector length (unlike L2) and its
// raw score is a similarity in [-1,1] (larger = closer).
const defaultMetricType entity.MetricType = entity.COSINE

type VectorStore struct {
	mu       sync.RWMutex
	client   client.Client
	host     string
	port     string
	logger   *zap.Logger
	dim      int
	dimCache sync.Map // collectionName -> int
	locks    sync.Map // collectionName -> *sync.Mutex
}

func NewVectorStore(host, port string, logger *zap.Logger) *VectorStore {
	return &VectorStore{
		host:   host,
		port:   port,
		logger: logger,
		dim:    1536,
	}
}

func (vs *VectorStore) doConnect(ctx context.Context) error {
	vs.logger.Info("connecting to Milvus", zap.String("host", vs.host), zap.String("port", vs.port))
	milvusAddr := fmt.Sprintf("%s:%s", vs.host, vs.port)

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", milvusAddr)
	if err != nil {
		vs.logger.Warn("Milvus port not reachable", zap.Error(err))
		return newUnavailableError("connect", fmt.Errorf("milvus port not reachable: %w", err))
	}
	conn.Close() //nolint:errcheck,gosec

	type result struct {
		client client.Client
		err    error
	}
	resultCh := make(chan result, 1)

	go func() {
		c, err := client.NewGrpcClient(ctx, milvusAddr)
		resultCh <- result{client: c, err: err}
	}()

	select {
	case res := <-resultCh:
		if res.err != nil {
			vs.logger.Error("failed to connect to Milvus", zap.Error(res.err))
			return newUnavailableError("connect", fmt.Errorf("failed to connect to Milvus: %w", res.err))
		}
		vs.client = res.client
		vs.logger.Info("connected to Milvus successfully")
		return nil
	case <-ctx.Done():
		// drain buffered channel to close any gRPC client that connects after timeout
		go func() {
			if res := <-resultCh; res.err == nil {
				_ = res.client.Close()
			}
		}()
		vs.logger.Warn("Milvus connection timeout")
		return newUnavailableError("connect", ctx.Err())
	}
}

// getClient ensures a connection exists and returns the client under a read lock,
// preventing a nil-pointer panic if Close() races with an in-flight call.
func (vs *VectorStore) getClient(ctx context.Context) (client.Client, error) {
	if err := vs.ensureConnected(ctx); err != nil {
		return nil, fmt.Errorf("milvus not available: %w", err)
	}
	vs.mu.RLock()
	c := vs.client
	vs.mu.RUnlock()
	if c == nil {
		return nil, newUnavailableError("client", fmt.Errorf("milvus client closed"))
	}
	return c, nil
}

func (vs *VectorStore) Connect(ctx context.Context) error {
	return vs.ensureConnected(ctx)
}

func (vs *VectorStore) ensureConnected(ctx context.Context) error {
	vs.mu.RLock()
	if vs.client != nil {
		vs.mu.RUnlock()
		return nil
	}
	vs.mu.RUnlock()
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.client != nil {
		return nil
	}
	return vs.doConnect(ctx)
}

func (vs *VectorStore) CreateCollection(ctx context.Context, collectionName string) error {
	return vs.CreateCollectionWithDim(ctx, collectionName, vs.dim)
}

// CreateCollectionWithDim creates a collection with a custom vector dimension.
// The schema includes a user_id field for per-user filtering.
// dim is cached in dimCache so Insert can pick it up without a signature change.
func (vs *VectorStore) CreateCollectionWithDim(ctx context.Context, collectionName string, dim int) error {
	var err error
	vs.withCollectionLock(collectionName, func() {
		err = vs.createCollectionWithDimLocked(ctx, collectionName, dim)
	})
	return err
}

func (vs *VectorStore) withCollectionLock(collectionName string, fn func()) {
	mu, _ := vs.locks.LoadOrStore(collectionName, &sync.Mutex{})
	lock := mu.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	fn()
}

func (vs *VectorStore) createCollectionWithDimLocked(ctx context.Context, collectionName string, dim int) error {
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	vs.logger.Info("creating collection", zap.String("collection", collectionName), zap.Int("dim", dim))

	hasCollection, err := c.HasCollection(ctx, collectionName)
	if err != nil {
		vs.logger.Error("failed to check collection", zap.Error(err))
		return classifyAvailabilityError("check collection", fmt.Errorf("failed to check collection %s: %w", collectionName, err))
	}

	if hasCollection {
		existingDim, derr := vs.collectionDim(ctx, c, collectionName)
		if derr != nil {
			vs.logger.Warn("failed to describe existing collection, will reuse",
				zap.String("collection", collectionName), zap.Error(derr))
			vs.dimCache.Store(collectionName, dim)
			return nil
		}
		hasAgentID := vs.collectionHasField(ctx, c, collectionName, "agent_id")
		if err := validateCollectionCompatibility(existingDim, dim, hasAgentID); err != nil {
			return fmt.Errorf("collection %s requires explicit reindex: %w", collectionName, err)
		}
		if existingDim == dim {
			vs.logger.Info("collection already exists",
				zap.String("collection", collectionName), zap.Int("dim", dim))
			vs.dimCache.Store(collectionName, dim)
			idxList, _ := c.DescribeIndex(ctx, collectionName, "vector")
			if len(idxList) == 0 {
				flatIdx, ierr := entity.NewIndexIvfFlat(defaultMetricType, 128)
				if ierr == nil {
					_ = c.CreateIndex(ctx, collectionName, "vector", flatIdx, false)
				}
			}
			return nil
		}
	}

	schema := &entity.Schema{
		CollectionName: collectionName,
		Description:    "memory collection",
		AutoID:         false,
		Fields: []*entity.Field{
			{
				Name:       "id",
				DataType:   entity.FieldTypeVarChar,
				PrimaryKey: true,
				TypeParams: map[string]string{"max_length": "65535"},
			},
			{
				Name:       "user_id",
				DataType:   entity.FieldTypeVarChar,
				TypeParams: map[string]string{"max_length": "128"},
			},
			{
				Name:       "agent_id",
				DataType:   entity.FieldTypeVarChar,
				TypeParams: map[string]string{"max_length": "128"},
			},
			{
				Name:       "scope",
				DataType:   entity.FieldTypeVarChar,
				TypeParams: map[string]string{"max_length": "20"},
			},
			{
				Name:       "content",
				DataType:   entity.FieldTypeVarChar,
				TypeParams: map[string]string{"max_length": "65535"},
			},
			{
				Name:       "source_document",
				DataType:   entity.FieldTypeVarChar,
				TypeParams: map[string]string{"max_length": "255"},
			},
			{
				Name:     "chunk_index",
				DataType: entity.FieldTypeInt64,
			},
			{
				Name:     "vector",
				DataType: entity.FieldTypeFloatVector,
				TypeParams: map[string]string{
					"dim": fmt.Sprintf("%d", dim),
				},
			},
		},
	}

	if err := c.CreateCollection(ctx, schema, 2); err != nil {
		vs.logger.Error("failed to create collection", zap.String("collection", collectionName), zap.Error(err))
		return classifyAvailabilityError("create collection", fmt.Errorf("failed to create collection %s: %w", collectionName, err))
	}
	idx, err := entity.NewIndexIvfFlat(defaultMetricType, 128)
	if err != nil {
		return fmt.Errorf("failed to build index param: %w", err)
	}
	if err := c.CreateIndex(ctx, collectionName, "vector", idx, false); err != nil {
		return classifyAvailabilityError("create vector index", fmt.Errorf("failed to create index on %s: %w", collectionName, err))
	}
	scIdx := entity.NewScalarIndexWithType(entity.Trie)
	for _, f := range []string{"user_id", "agent_id", "scope"} {
		if err := c.CreateIndex(ctx, collectionName, f, scIdx, false); err != nil {
			return classifyAvailabilityError("create scalar index", fmt.Errorf("failed to create scalar index on %s.%s: %w", collectionName, f, err))
		}
	}
	vs.dimCache.Store(collectionName, dim)
	vs.logger.Info("collection created successfully", zap.String("collection", collectionName))
	if err := c.LoadCollection(ctx, collectionName, false); err != nil {
		return classifyAvailabilityError("load collection", fmt.Errorf("failed to load collection %s: %w", collectionName, err))
	}
	return nil
}

// CollectionInfo is the structural snapshot of a Milvus collection returned
// by DescribeCollection. Missing optional fields (agent_id / user_id) are
// reported as false rather than an error so callers can decide their own
// tolerance for legacy schemas.
type CollectionInfo struct {
	Dim        int
	HasAgentID bool
	HasUserID  bool
}

// DescribeCollection inspects an existing collection's schema: vector
// dimension and presence of the agent_id / user_id columns. It does not
// create or mutate anything. Errors surface as availability-classified
// errors via the standard helper.
func (vs *VectorStore) DescribeCollection(ctx context.Context, collectionName string) (CollectionInfo, error) {
	c, err := vs.getClient(ctx)
	if err != nil {
		return CollectionInfo{}, err
	}
	desc, err := c.DescribeCollection(ctx, collectionName)
	if err != nil {
		return CollectionInfo{}, classifyAvailabilityError("describe collection",
			fmt.Errorf("failed to describe collection %s: %w", collectionName, err))
	}
	return collectionInfoFromSchema(desc.Schema), nil
}

// collectionInfoFromSchema extracts the structural snapshot from a Milvus
// collection schema. Missing optional fields (agent_id / user_id) are
// reported as false rather than an error so callers can decide their own
// tolerance for legacy schemas.
func collectionInfoFromSchema(schema *entity.Schema) CollectionInfo {
	info := CollectionInfo{}
	if schema == nil {
		return info
	}
	for _, field := range schema.Fields {
		switch field.Name {
		case "vector":
			if dim, ok := field.TypeParams["dim"]; ok {
				info.Dim, _ = strconv.Atoi(dim)
			}
		case "agent_id":
			info.HasAgentID = true
		case "user_id":
			info.HasUserID = true
		}
	}
	return info
}

func validateCollectionCompatibility(existingDim, requiredDim int, hasAgentID bool) error {
	if existingDim != requiredDim {
		return fmt.Errorf("vector dimension mismatch: existing=%d required=%d", existingDim, requiredDim)
	}
	if !hasAgentID {
		return fmt.Errorf("required field agent_id is missing")
	}
	return nil
}

// EnsurePartition creates the partition if it does not already exist.
func (vs *VectorStore) EnsurePartition(ctx context.Context, collectionName, partitionName string) error {
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	ok, err := c.HasPartition(ctx, collectionName, partitionName)
	if err != nil {
		return fmt.Errorf("failed to check partition: %w", err)
	}
	if ok {
		return nil
	}
	return c.CreatePartition(ctx, collectionName, partitionName)
}

// DropPartition drops a partition and all its vectors. No-op if it does not exist.
func (vs *VectorStore) DropPartition(ctx context.Context, collectionName, partitionName string) error {
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	ok, err := c.HasPartition(ctx, collectionName, partitionName)
	if err != nil {
		return fmt.Errorf("failed to check partition: %w", err)
	}
	if !ok {
		return nil
	}
	return c.DropPartition(ctx, collectionName, partitionName)
}

// CountVectors returns the total number of vectors in the given partition.
func (vs *VectorStore) CountVectors(ctx context.Context, collectionName, partitionName string) (int64, error) {
	c, err := vs.getClient(ctx)
	if err != nil {
		return 0, err
	}
	if err := c.LoadCollection(ctx, collectionName, false); err != nil {
		if isCollectionNotFound(err) {
			return 0, fmt.Errorf("failed to load collection: %w", ErrCollectionNotFound)
		}
		return 0, fmt.Errorf("failed to load collection: %w", err)
	}
	partitions := []string{}
	if partitionName != "" {
		partitions = []string{partitionName}
	}
	rows, err := c.Query(ctx, collectionName, partitions, `id != ""`, []string{"id"})
	if err != nil {
		if isCollectionNotFound(err) {
			return 0, fmt.Errorf("failed to query partition: %w", ErrCollectionNotFound)
		}
		return 0, fmt.Errorf("failed to query partition: %w", err)
	}
	col := rows.GetColumn("id")
	if col == nil {
		return 0, nil
	}
	return int64(col.Len()), nil
}

func (vs *VectorStore) Insert(ctx context.Context, collectionName string, docs []DocumentChunk, partitionName string) error {
	if len(docs) == 0 {
		return nil
	}
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	vs.logger.Debug("inserting vectors", zap.String("collection", collectionName), zap.Int("count", len(docs)))

	dim := vs.dim
	if cached, ok := vs.dimCache.Load(collectionName); ok {
		dim = cached.(int)
	}

	ids := make([]string, len(docs))
	userIDs := make([]string, len(docs))
	agentIDs := make([]string, len(docs))
	scopes := make([]string, len(docs))
	contents := make([]string, len(docs))
	sources := make([]string, len(docs))
	chunkIndices := make([]int64, len(docs))
	vectors := make([][]float32, len(docs))

	for i, doc := range docs {
		ids[i] = doc.ID
		userIDs[i] = doc.UserID
		agentIDs[i] = doc.AgentID
		scopes[i] = doc.Scope
		contents[i] = doc.Content
		sources[i] = doc.SourceDocument
		chunkIndices[i] = doc.ChunkIndex
		vectors[i] = doc.Vector
	}

	idCol := entity.NewColumnVarChar("id", ids)
	userIDCol := entity.NewColumnVarChar("user_id", userIDs)
	agentIDCol := entity.NewColumnVarChar("agent_id", agentIDs)
	scopeCol := entity.NewColumnVarChar("scope", scopes)
	contentCol := entity.NewColumnVarChar("content", contents)
	sourceCol := entity.NewColumnVarChar("source_document", sources)
	chunkIdxCol := entity.NewColumnInt64("chunk_index", chunkIndices)
	vectorCol := entity.NewColumnFloatVector("vector", dim, vectors)

	_, err = c.Insert(ctx, collectionName, partitionName, idCol, userIDCol, agentIDCol, scopeCol, contentCol, sourceCol, chunkIdxCol, vectorCol)
	if err != nil {
		vs.logger.Error("failed to insert vectors", zap.Error(err))
		return classifyAvailabilityError("insert vectors", fmt.Errorf("failed to insert vectors: %w", err))
	}
	vs.logger.Info("vectors inserted successfully", zap.Int("count", len(docs)))
	return nil
}

func (vs *VectorStore) Search(ctx context.Context, collectionName string, queryVector []float32, topK int, partitions ...string) ([]SearchResult, error) {
	return vs.SearchWithFilter(ctx, collectionName, queryVector, topK, "", partitions...)
}

// SearchWithFilter performs vector search with an optional Milvus boolean expression filter.
// Pass expression="" for unfiltered search. partitions scopes the search to specific partitions.
// Stale-schema tolerance: a collection missing the agent_id field yields an
// empty result (legacy memory collections) instead of an error.
func (vs *VectorStore) SearchWithFilter(ctx context.Context, collectionName string, queryVector []float32, topK int, expression string, partitions ...string) ([]SearchResult, error) {
	return vs.searchWithFilter(ctx, collectionName, queryVector, topK, expression, false, partitions...)
}

// SearchWithFilterStrict is the fail-closed variant: a stale collection
// missing agent_id is reported as an error instead of being silently
// tolerated as an empty result. RAG retrieval paths use this because their
// collections are created with the current schema; a mismatch signals drift.
func (vs *VectorStore) SearchWithFilterStrict(ctx context.Context, collectionName string, queryVector []float32, topK int, expression string, partitions ...string) ([]SearchResult, error) {
	return vs.searchWithFilter(ctx, collectionName, queryVector, topK, expression, true, partitions...)
}

func (vs *VectorStore) searchWithFilter(ctx context.Context, collectionName string, queryVector []float32, topK int, expression string, strict bool, partitions ...string) ([]SearchResult, error) {
	c, err := vs.getClient(ctx)
	if err != nil {
		return nil, err
	}
	vs.logger.Debug("searching vectors", zap.String("collection", collectionName), zap.Int("topK", topK))

	if err := c.LoadCollection(ctx, collectionName, false); err != nil {
		if isCollectionNotFound(err) {
			if strict {
				// Fail closed for RAG retrieval: the caller distinguishes a
				// legitimately empty workspace from drift via errors.Is.
				return nil, fmt.Errorf("failed to load collection %s: %w", collectionName, ErrCollectionNotFound)
			}
			// Legal before first use: memory collections are provisioned
			// lazily, so a missing collection means "nothing stored yet".
			vs.logger.Warn("collection not found, returning empty results",
				zap.String("collection", collectionName), zap.Error(err))
			return []SearchResult{}, nil
		}
		vs.logger.Error("failed to load collection", zap.Error(err))
		return nil, classifyAvailabilityError("load collection", fmt.Errorf("failed to load collection %s: %w", collectionName, err))
	}

	// Create search vector
	vectors := make([]entity.Vector, 1)
	vectors[0] = entity.FloatVector(queryVector)

	// IVF_FLAT search parameters (nprobe). The metric type is passed below in
	// searchWithParam and must match the collection index metric.
	sp, err := entity.NewIndexIvfFlatSearchParam(10)
	if err != nil {
		vs.logger.Error("failed to create search params", zap.Error(err))
		return nil, fmt.Errorf("failed to create search params: %w", err)
	}

	results, err := vs.searchWithParam(ctx, c, collectionName, partitions, expression, vectors, topK, sp, strict)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return []SearchResult{}, nil
	}
	out := searchRowsToResults(results[0], defaultMetricType)
	vs.logger.Debug("search completed", zap.Int("results", len(out)))
	return out, nil
}

// searchWithParam executes the Milvus search and classifies errors. A stale
// collection missing agent_id is tolerated as an empty result for the memory
// path (strict=false); RAG retrieval paths pass strict=true to fail closed.
func (vs *VectorStore) searchWithParam(
	ctx context.Context,
	c client.Client,
	collectionName string,
	partitions []string,
	expression string,
	vectors []entity.Vector,
	topK int,
	sp entity.SearchParam,
	strict bool,
) ([]client.SearchResult, error) {
	results, err := c.Search(
		ctx,
		collectionName,
		partitions,
		expression,
		[]string{"id", "content", "source_document", "chunk_index"}, // output fields
		vectors,
		"vector",          // vector field name
		defaultMetricType, // metric type (must match the index metric)
		topK,
		sp,
	)
	if err == nil {
		return results, nil
	}
	if strings.Contains(err.Error(), "field agent_id not exist") {
		if strict {
			vs.logger.Error("collection schema drift: agent_id missing",
				zap.String("collection", collectionName))
			return nil, classifyAvailabilityError("search",
				fmt.Errorf("failed to search vectors: collection %s has stale schema (agent_id missing)", collectionName))
		}
		vs.logger.Warn("memory collection has stale schema, returning empty results",
			zap.String("collection", collectionName))
		return nil, nil
	}
	if isDimensionMismatch(err) {
		// 查询维度与 collection 维度不一致是确定性数据形态错误，不是 outage：
		// 翻译为 ErrDimensionMismatch（errors.Is 可判），调用方降级跳过；
		// 保留原始消息便于排查。
		return nil, fmt.Errorf("failed to search vectors: %w: %v", ErrDimensionMismatch, err)
	}
	vs.logger.Error("failed to search vectors", zap.Error(err))
	return nil, classifyAvailabilityError("search", fmt.Errorf("failed to search vectors: %w", err))
}

// searchRowsToResults maps one Milvus hit batch into SearchResults, skipping
// rows whose id or content cell is unavailable. Raw metric scores are passed
// through normalizeScore so SearchResult.Score always carries the repository's
// external contract: a 0-1 normalized similarity, larger = more relevant.
func searchRowsToResults(result client.SearchResult, metric entity.MetricType) []SearchResult {
	idCol := result.Fields.GetColumn("id")
	contentCol := result.Fields.GetColumn("content")
	sourceCol := result.Fields.GetColumn("source_document")
	chunkIdxCol := result.Fields.GetColumn("chunk_index")
	scores := result.Scores

	out := make([]SearchResult, 0, result.ResultCount)
	for i := 0; i < result.ResultCount; i++ {
		score := float32(0)
		if i < len(scores) {
			score = normalizeScore(metric, float32(scores[i]))
		}
		id := columnString(idCol, i)
		content := columnString(contentCol, i)
		if id != "" && content != "" {
			out = append(out, SearchResult{
				ID:             id,
				Content:        content,
				SourceDocument: columnString(sourceCol, i),
				ChunkIndex:     columnChunkIndex(chunkIdxCol, i),
				Score:          score,
			})
		}
	}
	return out
}

// normalizeScore maps a raw Milvus metric score onto the repository's external
// contract: a 0-1 normalized similarity where larger means more relevant.
//
//   - COSINE: Milvus returns the raw cosine similarity in [-1,1] (verified on a
//     real Milvus v2.4.15: same-direction -> 1, orthogonal -> 0, opposite -> -1,
//     and scale-invariant). (s+1)/2 folds it onto [0,1] with the orthogonal
//     case at 0.5.
//   - L2 and anything unknown fall back to 1/(1+d) (distance d, smaller is
//     closer), preserving the legacy l2ToSim mapping so ordering never inverts.
//
// The result is clamped to [0,1] so callers can rely on the similarity contract
// even against float32 rounding at the extremes.
func normalizeScore(metric entity.MetricType, raw float32) float32 {
	var sim float32
	switch metric {
	case entity.COSINE:
		sim = (raw + 1) / 2
	default:
		sim = 1 / (1 + raw)
	}
	if sim < 0 {
		return 0
	}
	if sim > 1 {
		return 1
	}
	return sim
}

// columnString reads one string cell, tolerating missing columns and type
// mismatches from stale schemas.
func columnString(col entity.Column, i int) string {
	if col == nil || i >= col.Len() {
		return ""
	}
	val, err := col.Get(i)
	if err != nil {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return ""
	}
	return s
}

// columnChunkIndex reads one int64 cell; missing or mismatched cells yield 0.
func columnChunkIndex(col entity.Column, i int) int64 {
	if col == nil || i >= col.Len() {
		return 0
	}
	val, err := col.Get(i)
	if err != nil {
		return 0
	}
	idx, ok := val.(int64)
	if !ok {
		return 0
	}
	return idx
}

func (vs *VectorStore) Flush(ctx context.Context, collectionName string) error {
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	vs.logger.Debug("flushing collection", zap.String("collection", collectionName))
	if err := c.Flush(ctx, collectionName, false); err != nil {
		vs.logger.Error("failed to flush collection", zap.Error(err))
		return classifyAvailabilityError("flush collection", fmt.Errorf("failed to flush collection %s: %w", collectionName, err))
	}
	return nil
}

// DeleteByDocumentIDs removes all vectors whose source_document matches any of
// the given document IDs. Used when deleting a workspace to clean up vectors.
func (vs *VectorStore) DeleteByDocumentIDs(ctx context.Context, collectionName string, docIDs []string) error {
	if len(docIDs) == 0 {
		return nil
	}
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	quoted := make([]string, len(docIDs))
	for i, id := range docIDs {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	expr := fmt.Sprintf("source_document in [%s]", strings.Join(quoted, ","))
	vs.logger.Info("deleting vectors by document IDs",
		zap.String("collection", collectionName),
		zap.Int("doc_count", len(docIDs)))
	if err := c.Delete(ctx, collectionName, "", expr); err != nil {
		return fmt.Errorf("failed to delete vectors: %w", err)
	}
	return nil
}

// DeleteByPrimaryIDs removes vectors by the collection's primary id field.
func (vs *VectorStore) DeleteByPrimaryIDs(ctx context.Context, collectionName string, ids []string) error {
	expr := primaryIDDeleteExpression(ids)
	if expr == "" {
		return nil
	}
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	if err := c.Delete(ctx, collectionName, "", expr); err != nil {
		if strings.Contains(err.Error(), "not exist") || strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("delete by primary IDs: %w", err)
	}
	return nil
}

func primaryIDDeleteExpression(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	return fmt.Sprintf("id in [%s]", strings.Join(quoted, ","))
}

// CountDocuments returns the number of distinct source documents in a collection.
func (vs *VectorStore) CountDocuments(ctx context.Context, collectionName string) (int, error) {
	c, err := vs.getClient(ctx)
	if err != nil {
		return 0, err
	}
	if err := c.LoadCollection(ctx, collectionName, false); err != nil {
		if isCollectionNotFound(err) {
			return 0, fmt.Errorf("failed to load collection: %w", ErrCollectionNotFound)
		}
		return 0, fmt.Errorf("failed to load collection: %w", err)
	}
	rows, err := c.Query(ctx, collectionName, []string{}, `id != ""`, []string{"source_document"})
	if err != nil {
		if isCollectionNotFound(err) {
			return 0, fmt.Errorf("failed to query collection: %w", ErrCollectionNotFound)
		}
		return 0, fmt.Errorf("failed to query collection: %w", err)
	}
	col := rows.GetColumn("source_document")
	if col == nil {
		return 0, nil
	}
	seen := make(map[string]struct{}, col.Len())
	for i := 0; i < col.Len(); i++ {
		if v, err := col.GetAsString(i); err == nil {
			seen[v] = struct{}{}
		}
	}
	return len(seen), nil
}

func (vs *VectorStore) DeleteByFilter(ctx context.Context, collectionName, expr string) error {
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	if err := c.Delete(ctx, collectionName, "", expr); err != nil {
		if strings.Contains(err.Error(), "not exist") || strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("delete by filter: %w", err)
	}
	return nil
}

// ListCollections returns the names of all collections whose name starts with
// prefix. An empty prefix lists every collection. The SDK (v2.4.2) offers no
// server-side prefix filter, so filtering happens here; results are sorted for
// deterministic callers. Delete paths use this with a trailing-underscore
// prefix to enumerate model-suffixed collections (memory_<t>_ / kb_<ws>_)
// without knowing which models a tenant ever used.
func (vs *VectorStore) ListCollections(ctx context.Context, prefix string) ([]string, error) {
	c, err := vs.getClient(ctx)
	if err != nil {
		return nil, err
	}
	collections, err := c.ListCollections(ctx)
	if err != nil {
		return nil, classifyAvailabilityError("list collections",
			fmt.Errorf("failed to list collections: %w", err))
	}
	out := make([]string, 0, len(collections))
	for _, coll := range collections {
		if coll != nil && strings.HasPrefix(coll.Name, prefix) {
			out = append(out, coll.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (vs *VectorStore) DeleteCollection(ctx context.Context, collectionName string) error {
	c, err := vs.getClient(ctx)
	if err != nil {
		return err
	}
	exists, err := c.HasCollection(ctx, collectionName)
	if err != nil {
		return fmt.Errorf("failed to check collection %s: %w", collectionName, err)
	}
	if !exists {
		return nil
	}
	if err := c.DropCollection(ctx, collectionName); err != nil {
		return fmt.Errorf("failed to delete collection %s: %w", collectionName, err)
	}
	vs.logger.Info("collection deleted", zap.String("collection", collectionName))
	return nil
}

// KeywordSearch performs TF-IDF based keyword search on the collection.
// Fetches all documents and ranks them by relevance to query terms.
func (vs *VectorStore) KeywordSearch(ctx context.Context, collectionName string, query string, topK int, partitions ...string) ([]SearchResult, error) {
	c, err := vs.getClient(ctx)
	if err != nil {
		return nil, err
	}
	vs.logger.Debug("keyword searching", zap.String("collection", collectionName), zap.String("query", query))

	if err := c.LoadCollection(ctx, collectionName, false); err != nil {
		if isCollectionNotFound(err) {
			return nil, fmt.Errorf("failed to load collection %s: %w", collectionName, ErrCollectionNotFound)
		}
		return nil, classifyAvailabilityError("load collection", fmt.Errorf("failed to load collection %s: %w", collectionName, err))
	}
	rows, err := c.Query(
		ctx,
		collectionName,
		[]string{},
		"id != \"\"",
		[]string{"id", "content", "source_document", "chunk_index"},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query collection: %w", err)
	}

	idCol := rows.GetColumn("id")
	contentCol := rows.GetColumn("content")
	sourceCol := rows.GetColumn("source_document")
	chunkIdxCol := rows.GetColumn("chunk_index")

	if idCol == nil || contentCol == nil {
		return []SearchResult{}, nil
	}

	terms := tokenize(query)
	if len(terms) == 0 {
		return []SearchResult{}, nil
	}

	type candidate struct {
		SearchResult
		termFreqs map[string]int
		wordCount int
	}

	n := idCol.Len()
	candidates := make([]candidate, 0, n)

	for i := 0; i < n; i++ {
		var id, content, source string
		var chunkIdx int64

		if v, err := idCol.Get(i); err == nil {
			if s, ok := v.(string); ok {
				id = s
			}
		}
		if v, err := contentCol.Get(i); err == nil {
			if s, ok := v.(string); ok {
				content = s
			}
		}
		if sourceCol != nil {
			if v, err := sourceCol.Get(i); err == nil {
				if s, ok := v.(string); ok {
					source = s
				}
			}
		}
		if chunkIdxCol != nil {
			if v, err := chunkIdxCol.Get(i); err == nil {
				if idx, ok := v.(int64); ok {
					chunkIdx = idx
				}
			}
		}

		if id == "" || content == "" {
			continue
		}

		docTerms := tokenize(content)
		tf := make(map[string]int, len(docTerms))
		for _, t := range docTerms {
			tf[t]++
		}

		candidates = append(candidates, candidate{
			SearchResult: SearchResult{
				ID:             id,
				Content:        content,
				SourceDocument: source,
				ChunkIndex:     chunkIdx,
			},
			termFreqs: tf,
			wordCount: len(docTerms),
		})
	}

	// Calculate IDF: log((N+1)/(df+1)) + 1
	N := float64(len(candidates))
	idf := make(map[string]float64, len(terms))
	for _, t := range terms {
		df := 0
		for _, c := range candidates {
			if c.termFreqs[t] > 0 {
				df++
			}
		}
		idf[t] = math.Log((N+1)/(float64(df)+1)) + 1
	}

	// Calculate TF-IDF scores
	type scored struct {
		idx   int
		score float64
	}
	scores := make([]scored, 0, len(candidates))
	for i, c := range candidates {
		var s float64
		wc := float64(c.wordCount)
		if wc == 0 {
			continue
		}
		for _, t := range terms {
			tf := float64(c.termFreqs[t]) / wc
			s += tf * idf[t]
		}
		if s > 0 {
			scores = append(scores, scored{i, s})
		}
	}

	sort.Slice(scores, func(a, b int) bool { return scores[a].score > scores[b].score })

	if topK > len(scores) {
		topK = len(scores)
	}
	results := make([]SearchResult, topK)
	for i := 0; i < topK; i++ {
		r := candidates[scores[i].idx].SearchResult
		r.Score = float32(scores[i].score)
		results[i] = r
	}

	vs.logger.Debug("keyword search completed", zap.Int("results", len(results)))
	return results, nil
}

// HybridSearch performs RRF (Reciprocal Rank Fusion) of vector and keyword search results.
func (vs *VectorStore) HybridSearch(ctx context.Context, collectionName string, queryVector []float32, queryText string, topK int) ([]SearchResult, error) {
	vs.logger.Debug("hybrid searching", zap.String("collection", collectionName))

	// Run both searches in parallel
	type result struct {
		results []SearchResult
		err     error
	}
	vectorCh := make(chan result, 1)
	keywordCh := make(chan result, 1)

	go func() {
		r, err := vs.Search(ctx, collectionName, queryVector, topK*2)
		vectorCh <- result{r, err}
	}()

	go func() {
		r, err := vs.KeywordSearch(ctx, collectionName, queryText, topK*2)
		keywordCh <- result{r, err}
	}()

	vectorRes := <-vectorCh
	keywordRes := <-keywordCh

	if vectorRes.err != nil {
		return nil, fmt.Errorf("vector search failed: %w", vectorRes.err)
	}
	keywordRes.results, keywordRes.err = keywordSearchOutcome(keywordRes.results, keywordRes.err)
	if keywordRes.err != nil {
		return nil, keywordRes.err
	}

	// RRF fusion with k=60 (standard parameter)
	const k = 60.0
	rrfScores := make(map[string]float64)

	for rank, r := range vectorRes.results {
		rrfScores[r.ID] += 1.0 / (k + float64(rank+1))
	}
	for rank, r := range keywordRes.results {
		rrfScores[r.ID] += 1.0 / (k + float64(rank+1))
	}

	// Collect unique results
	resultMap := make(map[string]SearchResult)
	for _, r := range vectorRes.results {
		resultMap[r.ID] = r
	}
	for _, r := range keywordRes.results {
		if _, exists := resultMap[r.ID]; !exists {
			resultMap[r.ID] = r
		}
	}

	// Sort by RRF score
	type scored struct {
		result SearchResult
		score  float64
	}
	scoredResults := make([]scored, 0, len(rrfScores))
	for id, score := range rrfScores {
		if r, ok := resultMap[id]; ok {
			scoredResults = append(scoredResults, scored{r, score})
		}
	}

	sort.Slice(scoredResults, func(i, j int) bool {
		return scoredResults[i].score > scoredResults[j].score
	})

	if topK > len(scoredResults) {
		topK = len(scoredResults)
	}
	results := make([]SearchResult, topK)
	for i := 0; i < topK; i++ {
		results[i] = scoredResults[i].result
		results[i].Score = float32(scoredResults[i].score)
	}

	vs.logger.Debug("hybrid search completed", zap.Int("results", len(results)))
	return results, nil
}

// keywordSearchOutcome tolerates a missing collection (empty keyword leg, same
// semantics as the vector leg) and fails closed on any other failure.
func keywordSearchOutcome(results []SearchResult, err error) ([]SearchResult, error) {
	if err == nil || errors.Is(err, ErrCollectionNotFound) {
		return results, nil
	}
	return nil, fmt.Errorf("keyword search failed: %w", err)
}

// tokenize splits text into lowercase word tokens, filtering punctuation.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	var buf strings.Builder
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127 {
			buf.WriteRune(r)
		} else if buf.Len() > 0 {
			tokens = append(tokens, buf.String())
			buf.Reset()
		}
	}
	if buf.Len() > 0 {
		tokens = append(tokens, buf.String())
	}
	return tokens
}

// collectionDim reads the vector field dim from an existing collection's schema.
// Returns an error if the collection has no float-vector field or the dim is unparseable.
func (vs *VectorStore) collectionDim(ctx context.Context, c client.Client, collectionName string) (int, error) {
	coll, err := c.DescribeCollection(ctx, collectionName)
	if err != nil {
		return 0, fmt.Errorf("describe collection %s: %w", collectionName, err)
	}
	if coll == nil || coll.Schema == nil {
		return 0, fmt.Errorf("collection %s has no schema", collectionName)
	}
	for _, f := range coll.Schema.Fields {
		if f.DataType != entity.FieldTypeFloatVector {
			continue
		}
		raw, ok := f.TypeParams["dim"]
		if !ok {
			return 0, fmt.Errorf("vector field of %s has no dim type-param", collectionName)
		}
		var d int
		if _, err := fmt.Sscanf(raw, "%d", &d); err != nil {
			return 0, fmt.Errorf("parse dim %q of %s: %w", raw, collectionName, err)
		}
		return d, nil
	}
	return 0, fmt.Errorf("collection %s has no float-vector field", collectionName)
}

func (vs *VectorStore) collectionHasField(ctx context.Context, c client.Client, collectionName, fieldName string) bool {
	coll, err := c.DescribeCollection(ctx, collectionName)
	if err != nil || coll == nil || coll.Schema == nil {
		return false
	}
	for _, f := range coll.Schema.Fields {
		if f.Name == fieldName {
			return true
		}
	}
	return false
}

func (vs *VectorStore) Close() error {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.logger.Info("closing Milvus connection")
	if vs.client != nil {
		err := vs.client.Close()
		vs.client = nil
		return err
	}
	return nil
}
