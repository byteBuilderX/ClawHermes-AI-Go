package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/knowledge/domain"
	knowledgeport "github.com/byteBuilderX/stratum/internal/knowledge/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeReranker records invocations and returns canned results.
type fakeReranker struct {
	calls   int
	lastReq knowledgeport.RerankRequest
	results []knowledgeport.RerankResult
	err     error
}

func (f *fakeReranker) Rerank(_ context.Context, req knowledgeport.RerankRequest) ([]knowledgeport.RerankResult, error) {
	f.calls++
	f.lastReq = req
	return f.results, f.err
}

// rerankMetrics records rerank metric calls alongside NoopMetrics.
type rerankMetrics struct {
	observability.NoopMetrics
	requests []string // tenant:model:status
}

func (m *rerankMetrics) IncRerankRequest(tenantID, model, status string) {
	m.requests = append(m.requests, tenantID+":"+model+":"+status)
}

// countingChunkRepo lets tests configure the chunk count used by
// handleMissingCollection's drift classification.
type countingChunkRepo struct {
	recordingChunkRepo
	count    int64
	countErr error
}

func (c *countingChunkRepo) CountByWorkspace(context.Context, string, string) (int64, error) {
	return c.count, c.countErr
}

func vectorRAGService(vectors *MockVectorStore) *RAGService {
	return NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
}

func TestRAGQueryExternalRerankWidensRecallAndNarrows(t *testing.T) {
	vectors := NewMockVectorStore()
	results := make([]knowledgeport.VectorSearchResult, 0, 8)
	for i := 0; i < 8; i++ {
		results = append(results, knowledgeport.VectorSearchResult{
			ID: "chunk-" + string(rune('a'+i)), SourceDocument: "doc-" + string(rune('a'+i)),
			Content: "content " + string(rune('a'+i)), Score: float32(i + 1),
		})
	}
	vectors.SetSearchResults(results)
	reranker := &fakeReranker{results: []knowledgeport.RerankResult{
		{Index: 7, Score: 0.99}, {Index: 0, Score: 0.5},
	}}
	service := vectorRAGService(vectors)
	service.SetReranker(reranker)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     2, EmbeddingModel: "embedding-3", Reranking: "cohere:rerank-v3.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Recall was widened to TopK x RerankWidenFactor before the rerank call.
	if reranker.calls != 1 || len(reranker.lastReq.Documents) != 2*constants.RerankWidenFactor ||
		reranker.lastReq.TopN != 2 {
		t.Fatalf("reranker invocation=%+v want widened pool and TopN=2", reranker.lastReq)
	}
	// The final list is narrowed back to TopK in reranker order.
	if len(got.Sources) != 2 || got.Sources[0].ChunkID != "chunk-h" || got.Sources[1].ChunkID != "chunk-a" ||
		got.Sources[0].Score != 0.99 {
		t.Fatalf("sources=%+v", got.Sources)
	}
}

func TestRAGQueryExternalRerankAppliesThresholdAfterRescore(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.1},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.2},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.3},
	})
	service := vectorRAGService(vectors)
	service.SetReranker(&fakeReranker{results: []knowledgeport.RerankResult{
		{Index: 2, Score: 0.9}, {Index: 0, Score: 0.1},
	}})

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     2, EmbeddingModel: "embedding-3", Reranking: "cohere:rerank-v3.0",
		ScoreThreshold: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || got.Sources[0].ChunkID != "chunk-c" {
		t.Fatalf("threshold must keep only the reranker-confirmed result: %+v", got.Sources)
	}
}

func TestRAGQueryBuiltinRerankStableScoreDesc(t *testing.T) {
	vectors := NewMockVectorStore()
	// Mock scores are the storage layer's 0-1 similarities: chunk-b is most
	// relevant (0.9), chunk-a least (0.2).
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.2},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.9},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.5},
	})
	service := vectorRAGService(vectors)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     3, EmbeddingModel: "embedding-3", Reranking: "builtin-score-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 3 || got.Sources[0].ChunkID != "chunk-b" ||
		got.Sources[1].ChunkID != "chunk-c" || got.Sources[2].ChunkID != "chunk-a" {
		t.Fatalf("builtin rerank must order by similarity desc: %+v", got.Sources)
	}
	if got.Sources[0].Score != 0.9 || got.Sources[2].Score != 0.2 {
		t.Fatalf("scores must pass through storage similarities: %+v", got.Sources)
	}
}

func TestRAGQueryExternalRerankSkipsTinyPoolWithMetric(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.5},
	})
	reranker := &fakeReranker{}
	metrics := &rerankMetrics{}
	service := vectorRAGService(vectors)
	service.SetReranker(reranker)
	service.SetMetrics(metrics)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     1, EmbeddingModel: "embedding-3", Reranking: "cohere:rerank-v3.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reranker.calls != 0 {
		t.Fatal("tiny pool must skip the external rerank call")
	}
	if len(got.Sources) != 1 || got.Sources[0].ChunkID != "chunk-a" {
		t.Fatalf("skipped rerank must keep retrieval order: %+v", got.Sources)
	}
	if len(metrics.requests) != 1 || metrics.requests[0] != "tenant-1:rerank-v3.0:skipped" {
		t.Fatalf("metrics=%v", metrics.requests)
	}
}

func TestRAGQueryExternalRerankFailsClosedWithoutBackend(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.5},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.5},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.5},
	})
	service := vectorRAGService(vectors)

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     2, EmbeddingModel: "embedding-3", Reranking: "cohere:rerank-v3.0",
	})
	if err == nil || !strings.Contains(err.Error(), "no external reranker configured") {
		t.Fatalf("external identity without backend must fail closed, got %v", err)
	}
}

func TestRAGQueryKeywordExemptFromScoreThreshold(t *testing.T) {
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{chunks: []domain.Chunk{
		{ID: "chunk-a", DocID: "doc-a", Text: "a"},
		{ID: "chunk-b", DocID: "doc-b", Text: "b"},
	}})

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "keyword",
		ViewerID: "test-user",
		TopK:     5, ScoreThreshold: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 2 {
		t.Fatalf("keyword results carry no scores and must never be dropped by the threshold: %+v", got.Sources)
	}
}

func TestRAGQueryMissingCollectionClassifiesDrift(t *testing.T) {
	notFound := errors.New("collection not found: knowledge_tenant-1_workspace-1")
	for _, tc := range []struct {
		name      string
		count     int64
		wantErr   bool
		wantEmpty bool
	}{
		{name: "empty workspace returns empty result", count: 0, wantEmpty: true},
		{name: "chunk drift fails closed", count: 3, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vectors := NewMockVectorStore()
			vectors.SetSearchError(notFound)
			service := vectorRAGService(vectors)
			service.SetChunkRepo(&countingChunkRepo{count: tc.count})

			got, err := service.Query(context.Background(), RAGQueryRequest{
				TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
				ViewerID: "test-user",
				TopK:     3, EmbeddingModel: "embedding-3",
			})
			if tc.wantErr {
				if !errors.Is(err, ErrRAGDependency) {
					t.Fatalf("drift must fail closed, got %v", err)
				}
				return
			}
			if err != nil || !tc.wantEmpty || len(got.Sources) != 0 {
				t.Fatalf("empty workspace must yield empty result, got %+v err=%v", got, err)
			}
		})
	}
}

func TestRAGQueryDimensionMismatchFailsClosed(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetCollectionInfo(knowledgeport.CollectionInfo{Dim: 3, HasUserID: true})
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.5},
	})
	service := vectorRAGService(vectors)

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     3, EmbeddingModel: "embedding-3", // DimensionForModel("embedding-3") = 2048 != 3
	})
	if !errors.Is(err, ErrRAGDependency) {
		t.Fatalf("dimension mismatch must fail closed, got %v", err)
	}
}

func TestRAGQueryMissingUserIDColumnTolerated(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	vectors := NewMockVectorStore()
	vectors.SetCollectionInfo(knowledgeport.CollectionInfo{Dim: 2048, HasUserID: false})
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.5},
	})
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.New(core))

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     3, EmbeddingModel: "embedding-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("legacy collection without user_id column must still return results: %+v", got.Sources)
	}
	warned := false
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, "lacks user_id column") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("missing user_id column must be logged as a warning")
	}
}

func TestRAGQueryHybridExternalRerankWidensBothLegs(t *testing.T) {
	vectors := NewMockVectorStore()
	vectorResults := make([]knowledgeport.VectorSearchResult, 0, 8)
	for i := 0; i < 8; i++ {
		vectorResults = append(vectorResults, knowledgeport.VectorSearchResult{
			ID: "chunk-" + string(rune('a'+i)), SourceDocument: "doc-" + string(rune('a'+i)),
			Content: "content", Score: float32(i + 1),
		})
	}
	vectors.SetSearchResults(vectorResults)
	reranker := &fakeReranker{results: []knowledgeport.RerankResult{{Index: 0, Score: 1.0}}}
	chunks := &recordingChunkRepo{}
	service := vectorRAGService(vectors)
	service.SetChunkRepo(chunks)
	service.SetReranker(reranker)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "hybrid",
		ViewerID: "test-user",
		TopK:     2, EmbeddingModel: "embedding-3", Reranking: "cohere:rerank-v3.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if chunks.topK != 2*constants.RerankWidenFactor {
		t.Fatalf("keyword leg must widen to %d, got %d", 2*constants.RerankWidenFactor, chunks.topK)
	}
	if len(reranker.lastReq.Documents) != 8 {
		t.Fatalf("RRF pool must reflect the widened vector leg, got %d", len(reranker.lastReq.Documents))
	}
	if len(got.Sources) != 1 {
		t.Fatalf("hybrid rerank must narrow to TopN: %+v", got.Sources)
	}
}

func TestRAGQueryBuiltinSemanticRerankRescores(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.9},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.1},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.5},
	})
	reranker := &fakeReranker{results: []knowledgeport.RerankResult{
		{Index: 2, Score: 0.9}, {Index: 0, Score: 0.5}, {Index: 1, Score: 0.2},
	}}
	service := vectorRAGService(vectors)
	service.SetSemanticReranker(reranker, 10)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     3, EmbeddingModel: "embedding-3", Reranking: "builtin-score-v1",
		RerankModel: "qwen-turbo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reranker.calls != 1 {
		t.Fatalf("semantic rerank calls=%d, want 1", reranker.calls)
	}
	if reranker.lastReq.Model != "qwen-turbo" {
		t.Fatalf("semantic rerank must pass workspace rerank model, got %q", reranker.lastReq.Model)
	}
	if len(reranker.lastReq.Documents) != 3 || reranker.lastReq.TopN != 3 {
		t.Fatalf("semantic rerank must score the whole pool: %+v", reranker.lastReq)
	}
	// LLM 分数覆盖召回分数，结果按 LLM 分数降序。
	if len(got.Sources) != 3 || got.Sources[0].ChunkID != "chunk-c" || got.Sources[0].Score != 0.9 ||
		got.Sources[1].ChunkID != "chunk-a" || got.Sources[1].Score != 0.5 ||
		got.Sources[2].ChunkID != "chunk-b" || got.Sources[2].Score != 0.2 {
		t.Fatalf("sources=%+v", got.Sources)
	}
}

func TestRAGQueryBuiltinSemanticRerankFailsOpenOnError(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.3},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.9},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.6},
	})
	reranker := &fakeReranker{err: errors.New("llm down")}
	metrics := &rerankMetrics{}
	service := vectorRAGService(vectors)
	service.SetSemanticReranker(reranker, 10)
	service.SetMetrics(metrics)

	ctx := reqctx.WithTenantID(context.Background(), "tenant-1")
	got, err := service.Query(ctx, RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     3, EmbeddingModel: "embedding-3", Reranking: "builtin-score-v1",
		RerankModel: "qwen-turbo",
	})
	if err != nil {
		t.Fatalf("rerank failure must not fail the query: %v", err)
	}
	// fail-open：保持召回分数排序（chunk-b 相似度 0.9 最高）。
	if len(got.Sources) != 3 || got.Sources[0].ChunkID != "chunk-b" || got.Sources[0].Score != 0.9 {
		t.Fatalf("fallback must keep recall-score ordering: %+v", got.Sources)
	}
	if len(metrics.requests) != 1 || metrics.requests[0] != "tenant-1:builtin-llm:degraded" {
		t.Fatalf("metrics=%v", metrics.requests)
	}
}

func TestRAGQueryBuiltinSemanticRerankSkipsTinyPool(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.5},
	})
	reranker := &fakeReranker{}
	service := vectorRAGService(vectors)
	service.SetSemanticReranker(reranker, 10)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     1, EmbeddingModel: "embedding-3", Reranking: "builtin-score-v1",
		RerankModel: "qwen-turbo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reranker.calls != 0 {
		t.Fatal("single-candidate pool must skip the LLM call")
	}
	if len(got.Sources) != 1 || got.Sources[0].ChunkID != "chunk-a" {
		t.Fatalf("skipped rerank keeps retrieval order: %+v", got.Sources)
	}
}

func TestRerankSemanticNarrowsToTopN(t *testing.T) {
	pool := make([]Source, 0, 20)
	for i := 0; i < 20; i++ {
		pool = append(pool, Source{
			DocumentID: "doc", ChunkID: fmt.Sprintf("chunk-%02d", i),
			Content: fmt.Sprintf("content %d", i), Score: float32(20 - i),
		})
	}
	reranker := &fakeReranker{results: []knowledgeport.RerankResult{
		{Index: 0, Score: 1.0}, {Index: 1, Score: 0.9}, {Index: 2, Score: 0.8},
	}}
	service := vectorRAGService(nil)
	service.SetSemanticReranker(reranker, 5)

	narrowed, err := service.rerankSemantic(context.Background(), RAGQueryRequest{Question: "q", RerankModel: "qwen-turbo"}, pool)
	if err != nil {
		t.Fatal(err)
	}
	// 池 20 条 > semanticTopN 5 → 只精排召回分前 5；返回 5 条。
	if reranker.calls != 1 || len(reranker.lastReq.Documents) != 5 || reranker.lastReq.TopN != 5 {
		t.Fatalf("semantic rerank must score the top-5 recall candidates: %+v", reranker.lastReq)
	}
	if len(narrowed) != 5 {
		t.Fatalf("narrowed pool = %d, want 5", len(narrowed))
	}
	if narrowed[0].ChunkID != "chunk-00" || narrowed[0].Score != 1.0 {
		t.Fatalf("LLM rescored candidate first: %+v", narrowed[0])
	}
}

func TestRAGQueryBuiltinSemanticRerankPartialTailFill(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.4},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.5},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.6},
	})
	reranker := &fakeReranker{results: []knowledgeport.RerankResult{
		{Index: 2, Score: 0.9}, // LLM 只返回第 3 条（chunk-c）
	}}
	service := vectorRAGService(vectors)
	service.SetSemanticReranker(reranker, 10)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     3, EmbeddingModel: "embedding-3", Reranking: "builtin-score-v1",
		RerankModel: "qwen-turbo",
	})
	if err != nil {
		t.Fatal(err)
	}
	// LLM 只给 chunk-c 0.9；chunk-a/b 未被打分，按召回相似度降序补尾。
	if len(got.Sources) != 3 {
		t.Fatalf("sources=%+v", got.Sources)
	}
	if got.Sources[0].ChunkID != "chunk-c" || got.Sources[0].Score != 0.9 {
		t.Fatalf("LLM-scored candidate must sort first, got %+v", got.Sources[0])
	}
	if got.Sources[1].ChunkID != "chunk-b" || got.Sources[1].Score != 0.5 ||
		got.Sources[2].ChunkID != "chunk-a" || got.Sources[2].Score != 0.4 {
		t.Fatalf("tail-filled candidates keep recall scores, got %+v", got.Sources[1:])
	}
}

func TestRAGQueryBuiltinSemanticRerankEmptyLLMResultsFillsTail(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.4},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.6},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.8},
	})
	reranker := &fakeReranker{results: []knowledgeport.RerankResult{}} // LLM 返回空
	service := vectorRAGService(vectors)
	service.SetSemanticReranker(reranker, 10)

	got, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
		ViewerID: "test-user",
		TopK:     3, EmbeddingModel: "embedding-3", Reranking: "builtin-score-v1",
		RerankModel: "qwen-turbo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reranker.calls != 1 {
		t.Fatalf("semantic rerank must be invoked for a 3-candidate pool, calls=%d", reranker.calls)
	}
	// LLM 空结果 → 全池按召回相似度降序补尾，无重复无越界。
	if len(got.Sources) != 3 {
		t.Fatalf("sources=%+v", got.Sources)
	}
	if got.Sources[0].ChunkID != "chunk-c" || got.Sources[0].Score != 0.8 ||
		got.Sources[1].ChunkID != "chunk-b" || got.Sources[1].Score != 0.6 ||
		got.Sources[2].ChunkID != "chunk-a" || got.Sources[2].Score != 0.4 {
		t.Fatalf("tail-filled candidates keep recall scores, got %+v", got.Sources)
	}
}

// TestRerankTopKClampsToMax 守护 rerankTopK 的最终条数硬上限：请求 RerankTopK
// 超过 MaxRerankTopK 时 clamp 到上限，0 = 跟随 TopK。
func TestRerankTopKClampsToMax(t *testing.T) {
	cases := []struct {
		name      string
		rerankTop int
		topK      int
		skipClamp bool
		want      int
	}{
		{"follows topk when unset", 0, 3, false, 3},
		{"uses explicit rerank topk", 3, 5, false, 3},
		{"clamps above max", 25, 5, false, constants.MaxRerankTopK},
		{"clamps exactly at max is allowed", constants.MaxRerankTopK, 5, false, constants.MaxRerankTopK},
		// 评估路径豁免：SkipTopKClamp 保留 MaximumEvaluationTopK=100 契约，不截断候选池。
		{"skip clamp preserves evaluation topk", 25, 5, true, 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rerankTopK(RAGQueryRequest{TopK: tc.topK, RerankTopK: tc.rerankTop, SkipTopKClamp: tc.skipClamp}); got != tc.want {
				t.Errorf("rerankTopK(RerankTopK=%d, TopK=%d, SkipTopKClamp=%v) = %d, want %d",
					tc.rerankTop, tc.topK, tc.skipClamp, got, tc.want)
			}
		})
	}
}

// TestRAGQueryClampsTopKToMax 守护 Query 入口的防御性 topK 上限 clamp：
// 即使调用方绕过 proto binding 提交越界 topK，检索也 clamp 到 MaxRAGTopK；
// 评估路径 SkipTopKClamp 豁免，保留 MaximumEvaluationTopK=100 契约不被截断。
func TestRAGQueryClampsTopKToMax(t *testing.T) {
	vectors := NewMockVectorStore()
	results := make([]knowledgeport.VectorSearchResult, 0, 30)
	for i := 0; i < 30; i++ {
		results = append(results, knowledgeport.VectorSearchResult{
			ID: fmt.Sprintf("chunk-%d", i), SourceDocument: "doc", Content: "content", Score: float32(i + 1),
		})
	}
	vectors.SetSearchResults(results)
	service := vectorRAGService(vectors)

	cases := []struct {
		name      string
		topK      int
		skipClamp bool
		want      int
	}{
		{"clamps above max", constants.MaxRAGTopK + 5, false, constants.MaxRAGTopK},
		// 评估快照 TopK 允许到 100：豁免 clamp 后返回候选池全量，不做 20 截断。
		{"skip clamp preserves evaluation candidate pool", 100, true, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := service.Query(context.Background(), RAGQueryRequest{
				TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector",
				ViewerID: "test-user", TopK: tc.topK, EmbeddingModel: "embedding-3", SkipTopKClamp: tc.skipClamp,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Sources) != tc.want {
				t.Fatalf("sources = %d, want %d (skipClamp=%v)", len(got.Sources), tc.want, tc.skipClamp)
			}
		})
	}
}

// TestNewRAGSearchFnBuiltinRerankFromWorkspaceConfig 守护核心修复：普通查询
// （agent 检索 fan-out searchWorkspace）把 workspace config 的重排策略/模型/
// 评分指令/TopK 透传给语义重排器——而非仅评测链路生效。config 是触发与指令的
// 单一事实源：Reranking=builtin-score-v1 使普通查询真正调用语义重排器，
// RerankScoringInstructions 随之进入 RerankRequest。
func TestNewRAGSearchFnBuiltinRerankFromWorkspaceConfig(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]knowledgeport.VectorSearchResult{
		{ID: "chunk-a", SourceDocument: "doc-a", Content: "a", Score: 0.9},
		{ID: "chunk-b", SourceDocument: "doc-b", Content: "b", Score: 0.1},
		{ID: "chunk-c", SourceDocument: "doc-c", Content: "c", Score: 0.5},
	})
	reranker := &fakeReranker{results: []knowledgeport.RerankResult{
		{Index: 2, Score: 0.9}, {Index: 0, Score: 0.5}, {Index: 1, Score: 0.2},
	}}
	service := vectorRAGService(vectors)
	service.SetSemanticReranker(reranker, 10)
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: &domain.Workspace{
		ID:   "019047ac-0000-7000-9000-000000000099",
		Name: "个人知识库",
		Config: domain.WorkspaceConfig{
			EmbeddingModel:            "text-embedding-v3",
			QueryMode:                 "vector",
			TopK:                      3,
			Reranking:                 "builtin-score-v1",
			RerankModel:               "qwen-turbo",
			RerankTopK:                2,
			RerankScoringInstructions: "分数须有区分度，避免全部同分",
		},
	}})
	service.SetTenantRoleResolver(stubRoleResolver{role: "owner"})
	service.SetDocRepo(stubDocRepo{})

	fn := NewRAGSearchFn(service, "tenant-1", "viewer-1")
	content, err := fn(context.Background(), []string{"个人知识库"}, "query", 3)
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("search must return content")
	}
	if reranker.calls != 1 {
		t.Fatalf("semantic rerank calls=%d, want 1 (普通查询必须触发 workspace 配置的重排)", reranker.calls)
	}
	if reranker.lastReq.Model != "qwen-turbo" {
		t.Fatalf("rerank model = %q, want qwen-turbo (from workspace config)", reranker.lastReq.Model)
	}
	if reranker.lastReq.ScoringInstructions != "分数须有区分度，避免全部同分" {
		t.Fatalf("scoring instructions lost: got %q", reranker.lastReq.ScoringInstructions)
	}
}
