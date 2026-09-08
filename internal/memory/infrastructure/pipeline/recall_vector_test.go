package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
	storagemilvus "github.com/byteBuilderX/stratum/pkg/storage/milvus"
	vector "github.com/byteBuilderX/stratum/pkg/vector"
	"github.com/pashagolub/pgxmock/v2"
	"go.uber.org/zap"
)

// fakeEmbedClient returns a fixed vector so tryVectorSearch reaches the store.
type fakeEmbedClient struct {
	vec   []float32
	err   error
	model string
}

func (f *fakeEmbedClient) EmbedVector(_ context.Context, _ string) ([]float32, error) {
	return f.vec, f.err
}
func (f *fakeEmbedClient) GetVectorDimension() int { return len(f.vec) }
func (f *fakeEmbedClient) Model() string           { return f.model }

// fakeVectorSearcher records the collections queried and returns per-collection
// canned results/errors so we can assert the dual-collection fusion behaviour.
type fakeVectorSearcher struct {
	byCollection map[string][]vector.SearchResult
	errByColl    map[string]error
	queried      []string
}

func (f *fakeVectorSearcher) SearchWithFilter(_ context.Context, collectionName string, _ []float32, _ int, _ string, _ ...string) ([]vector.SearchResult, error) {
	f.queried = append(f.queried, collectionName)
	if err := f.errByColl[collectionName]; err != nil {
		return nil, err
	}
	return f.byCollection[collectionName], nil
}

func newTestRecallHandler(embed EmbedClient, vs vectorSearcher) *RecallHandler {
	return &RecallHandler{logger: zap.NewNop(), embedSvc: embed, vectorDB: vs, metrics: observability.NoopMetrics{}}
}

// recallResolverStub 固定返回 memory.recall_top_k 的解析值(其余 key 缺席),
// 用于验证召回条数接入（平台级语义：ResolvePlatform 只按 key 解析）。
type recallResolverStub struct{ topK int }

func (s recallResolverStub) ResolvePlatform(_ context.Context, key string) (any, bool, error) {
	if key != "memory.recall_top_k" {
		return nil, false, nil
	}
	return float64(s.topK), true, nil
}

// recallMetricSpy captures IncKnowledgeQuery calls so tests can assert the
// degraded/error signalling without a real Prometheus registry.
type recallMetricSpy struct {
	observability.NoopMetrics
	knowledgeQuery map[string]int
}

func (m *recallMetricSpy) IncKnowledgeQuery(queryType, status string) {
	if m.knowledgeQuery == nil {
		m.knowledgeQuery = map[string]int{}
	}
	m.knowledgeQuery[queryType+"."+status]++
}

func TestTryVectorSearch_QueriesFourCandidatesInOrderAndSortsBySimilarity(t *testing.T) {
	tenant := "acme"
	model := "embedding-3"
	names := []string{
		memoryCollectionName(tenant, model),
		memoryCollectionLegacyName(tenant),
		memoryFactsCollectionName(tenant, model),
		memoryFactsCollectionLegacyName(tenant),
	}

	vs := &fakeVectorSearcher{byCollection: map[string][]vector.SearchResult{
		names[0]: {{ID: "raw-far", Content: "raw far", Score: 0.1}},
		names[2]: {{ID: "fact-near", Content: "fact near", Score: 0.9}},
	}}
	h := newTestRecallHandler(&fakeEmbedClient{vec: []float32{1, 2, 3}, model: model}, vs)

	got, _ := h.tryVectorSearch(context.Background(), tenant, "u1", "", "user", RecallRequest{Query: "q", Limit: 5})

	// 候选 = 当前模型新名（raw+facts）∪ legacy 名（升级前数据），顺序固定。
	if len(vs.queried) != 4 || vs.queried[0] != names[0] || vs.queried[1] != names[1] || vs.queried[2] != names[2] || vs.queried[3] != names[3] {
		t.Fatalf("expected query sequence %v, got %v", names, vs.queried)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 merged candidates, got %d", len(got))
	}
	// Higher similarity (0-1 cosine, larger = closer) ranks first across collections.
	if got[0].ID != "fact-near" || got[1].ID != "raw-far" {
		t.Fatalf("expected descending-similarity order [fact-near raw-far], got [%s %s]", got[0].ID, got[1].ID)
	}
}

func TestTryVectorSearch_LegacyOnlyWhenNoModel(t *testing.T) {
	tenant := "acme"

	vs := &fakeVectorSearcher{byCollection: map[string][]vector.SearchResult{
		memoryCollectionLegacyName(tenant):      {{ID: "legacy-raw", Content: "legacy raw", Score: 0.3}},
		memoryFactsCollectionLegacyName(tenant): {{ID: "legacy-fact", Content: "legacy fact", Score: 0.1}},
	}}
	h := newTestRecallHandler(&fakeEmbedClient{vec: []float32{1, 2, 3}, model: ""}, vs)

	got, _ := h.tryVectorSearch(context.Background(), tenant, "u1", "", "user", RecallRequest{Query: "q", Limit: 5})

	// 无模型 → 只查 legacy 两个 collection（新名带尾下划线无意义，且升级前数据在旧名）。
	want := []string{memoryCollectionLegacyName(tenant), memoryFactsCollectionLegacyName(tenant)}
	if len(vs.queried) != 2 || vs.queried[0] != want[0] || vs.queried[1] != want[1] {
		t.Fatalf("expected legacy-only query sequence %v, got %v", want, vs.queried)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 merged legacy candidates, got %d", len(got))
	}
}

func TestTryVectorSearch_SkipsEmptyContentAndToleratesOneCollectionFailure(t *testing.T) {
	tenant := "acme"
	raw := memoryCollectionLegacyName(tenant)
	facts := memoryFactsCollectionLegacyName(tenant)

	vs := &fakeVectorSearcher{
		byCollection: map[string][]vector.SearchResult{
			raw: {
				{ID: "keep", Content: "has content", Score: 0.2},
				{ID: "drop", Content: "", Score: 0.1}, // empty content must be filtered out
			},
		},
		errByColl: map[string]error{facts: errors.New("facts collection down")},
	}
	h := newTestRecallHandler(&fakeEmbedClient{vec: []float32{1}}, vs)

	got, _ := h.tryVectorSearch(context.Background(), tenant, "u1", "", "user", RecallRequest{Query: "q", Limit: 5})

	// A single failing collection must not abort the whole vector search.
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate (empty-content dropped, facts failure tolerated), got %d", len(got))
	}
	if got[0].ID != "keep" {
		t.Fatalf("expected surviving candidate 'keep', got %q", got[0].ID)
	}
}

func TestTryVectorSearch_NilVectorDBReturnsNil(t *testing.T) {
	h := newTestRecallHandler(&fakeEmbedClient{vec: []float32{1}}, nil)
	got, err := h.tryVectorSearch(context.Background(), "t", "u", "", "user", RecallRequest{Query: "q", Limit: 5})
	if got != nil || err != nil {
		t.Fatalf("expected nil candidates and nil error when vectorDB absent, got (%v, %v)", got, err)
	}
}

func TestTryVectorSearch_EmbedFailureReturnsNil(t *testing.T) {
	vs := &fakeVectorSearcher{}
	h := newTestRecallHandler(&fakeEmbedClient{err: errors.New("embed down")}, vs)
	got, err := h.tryVectorSearch(context.Background(), "t", "u", "", "user", RecallRequest{Query: "q", Limit: 5})
	if got != nil || err != nil {
		t.Fatalf("expected nil candidates and nil error on embed failure, got (%v, %v)", got, err)
	}
	if len(vs.queried) != 0 {
		t.Fatal("vector store must not be queried when embedding fails")
	}
}

func TestTryVectorSearch_OutageClassifiedAndDegradationKept(t *testing.T) {
	tenant := "acme"
	model := "embedding-3"
	names := recallCandidateCollections(tenant, model)
	// 一个 collection 遭遇非 not-found 的 outage（wrap 验证 errors.Is 解包），
	// 一个为 legacy not-found，其余正常返回——降级契约：幸存 candidates 仍返回，
	// outage 以 error + degraded 指标可观测，not-found 不计入。
	outage := fmt.Errorf("milvus unreachable: %w", errors.New("connection refused"))
	vs := &fakeVectorSearcher{
		byCollection: map[string][]vector.SearchResult{
			names[1]: {{ID: "legacy-hit", Content: "legacy hit", Score: 0.2}},
			names[2]: {{ID: "fact-hit", Content: "fact hit", Score: 0.1}},
		},
		errByColl: map[string]error{
			names[0]: outage,
			names[3]: storagemilvus.ErrCollectionNotFound,
		},
	}
	spy := &recallMetricSpy{}
	h := &RecallHandler{logger: zap.NewNop(), embedSvc: &fakeEmbedClient{vec: []float32{1}, model: model}, vectorDB: vs, metrics: spy}

	got, err := h.tryVectorSearch(context.Background(), tenant, "u1", "", "user", RecallRequest{Query: "q", Limit: 5})

	if len(got) != 2 {
		t.Fatalf("degradation: expected 2 survivors from non-failing collections, got %d", len(got))
	}
	if !errors.Is(err, outage) {
		t.Fatalf("outage must surface as error, got %v", err)
	}
	if spy.knowledgeQuery["recall.degraded"] != 1 {
		t.Fatalf("expected exactly 1 degraded signal for one outage, got %d", spy.knowledgeQuery["recall.degraded"])
	}
	if spy.knowledgeQuery["recall.error"] != 0 {
		t.Fatalf("not-found must not count as recall error, got %d", spy.knowledgeQuery["recall.error"])
	}
}

func TestTryVectorSearch_DimensionMismatchIsSilentLegacyFallback(t *testing.T) {
	tenant := "acme"
	model := "embedding-3"
	names := recallCandidateCollections(tenant, model)
	// 模型切换后的存量集合维度与当前 embedding 不一致是必然的 dim mismatch：
	// 必须与 collection-not-found 同级——Debug 跳过、不 ERROR、不 degraded、
	// 不作为 outage 上抛。幸存 collection 的结果照常返回。
	vs := &fakeVectorSearcher{
		byCollection: map[string][]vector.SearchResult{
			names[1]: {{ID: "legacy-hit", Content: "legacy hit", Score: 0.2}},
		},
		errByColl: map[string]error{
			names[0]: storagemilvus.ErrDimensionMismatch,
			names[3]: fmt.Errorf("failed to search vectors: %w: %v", storagemilvus.ErrDimensionMismatch, "dimension mismatch: query vector dimension (1536) does not match collection dimension (1024)"),
		},
	}
	spy := &recallMetricSpy{}
	h := &RecallHandler{logger: zap.NewNop(), embedSvc: &fakeEmbedClient{vec: []float32{1}, model: model}, vectorDB: vs, metrics: spy}

	got, err := h.tryVectorSearch(context.Background(), tenant, "u1", "", "user", RecallRequest{Query: "q", Limit: 5})

	if err != nil {
		t.Fatalf("dim mismatch must not surface as outage, got %v", err)
	}
	if len(got) != 1 || got[0].ID != "legacy-hit" {
		t.Fatalf("expected survivor 'legacy-hit' from non-mismatch collection, got %v", got)
	}
	if spy.knowledgeQuery["recall.degraded"] != 0 {
		t.Fatalf("dim mismatch must not emit degraded signal, got %d", spy.knowledgeQuery["recall.degraded"])
	}
}

func TestTryVectorSearch_CollectionNotFoundIsSilentLegacyFallback(t *testing.T) {
	tenant := "acme"
	vs := &fakeVectorSearcher{errByColl: map[string]error{
		memoryCollectionLegacyName(tenant):      storagemilvus.ErrCollectionNotFound,
		memoryFactsCollectionLegacyName(tenant): storagemilvus.ErrCollectionNotFound,
	}}
	spy := &recallMetricSpy{}
	h := &RecallHandler{logger: zap.NewNop(), embedSvc: &fakeEmbedClient{vec: []float32{1}}, vectorDB: vs, metrics: spy}

	got, err := h.tryVectorSearch(context.Background(), tenant, "u1", "", "user", RecallRequest{Query: "q", Limit: 5})

	if got != nil || err != nil {
		t.Fatalf("all-not-found must degrade silently (nil candidates, nil error), got (%v, %v)", got, err)
	}
	if spy.knowledgeQuery["recall.degraded"] != 0 {
		t.Fatalf("not-found must not emit degraded signal, got %d", spy.knowledgeQuery["recall.degraded"])
	}
}

func TestHandle_VectorOutageStillReturnsTextResults(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.ExpectBegin()
	pool.ExpectExec("SET LOCAL search_path").WillReturnResult(pgxmock.NewResult("SET", 0))
	pool.ExpectQuery("SELECT id, content, role, importance, created_at FROM memory_entries").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "content", "role", "importance", "created_at"}).
			AddRow("t1", "text hit", "user", 0.9, "2026-08-11 10:00:00"))
	pool.ExpectRollback()

	// 全部 4 个候选 collection 均为非 not-found 的 Milvus outage——最坏情形：
	// 用户 recall 退化为纯 text，但必须带 degraded 信号而非无声成功。
	tenant := "acme"
	model := "embedding-3"
	vs := &fakeVectorSearcher{errByColl: map[string]error{}}
	for _, c := range recallCandidateCollections(tenant, model) {
		vs.errByColl[c] = errors.New("milvus connection refused")
	}
	spy := &recallMetricSpy{}
	h := &RecallHandler{pool: pool, logger: zap.NewNop(), embedSvc: &fakeEmbedClient{vec: []float32{1}, model: model}, vectorDB: vs, metrics: spy}

	got, err := h.Handle(context.Background(), tenant, "u1", "", "user", map[string]any{"query": "hit", "limit": 5})

	if err != nil {
		t.Fatalf("degradation contract: Handle must succeed on vector outage, got %v", err)
	}
	if !strings.Contains(got, "text hit") {
		t.Fatalf("expected text results in output, got %q", got)
	}
	if spy.knowledgeQuery["recall.degraded"] != 1 {
		t.Fatalf("expected exactly 1 degraded signal, got %d", spy.knowledgeQuery["recall.degraded"])
	}
	if spy.knowledgeQuery["recall.success"] != 1 {
		t.Fatalf("expected success metric for degraded-but-successful recall, got %d", spy.knowledgeQuery["recall.success"])
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestHandle_RecallTopKFallback 验证 memory.recall_top_k 接入:工具 limit 缺失/
// 非法(≤0 或 >20)时解析该值,显式合法 limit 优先,resolver 缺失回退 5。经 text
// 查询 SQL 的 LIMIT 参数(= req.Limit*2)断言。
func TestHandle_RecallTopKFallback(t *testing.T) {
	cases := []struct {
		name     string
		limit    int
		resolver memport.PlatformParamResolver // nil → 回退 5
		wantSQL  int                           // req.Limit*2 落到 SQL LIMIT
	}{
		{name: "invalid limit resolves recall_top_k", limit: 0, resolver: recallResolverStub{topK: 7}, wantSQL: 14},
		{name: "over-limit resolved via recall_top_k", limit: 50, resolver: recallResolverStub{topK: 7}, wantSQL: 14},
		{name: "nil resolver falls back to 5", limit: 0, resolver: nil, wantSQL: 10},
		{name: "explicit legal limit wins", limit: 3, resolver: recallResolverStub{topK: 7}, wantSQL: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			pool.ExpectBegin()
			pool.ExpectExec("SET LOCAL search_path").WillReturnResult(pgxmock.NewResult("SET", 0))
			pool.ExpectQuery("SELECT id, content, role, importance, created_at FROM memory_entries").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), tc.wantSQL).
				WillReturnRows(pgxmock.NewRows([]string{"id", "content", "role", "importance", "created_at"}))
			pool.ExpectRollback()

			h := &RecallHandler{pool: pool, logger: zap.NewNop(), metrics: observability.NoopMetrics{}}
			h.SetPlatformParamResolver(tc.resolver)
			input := map[string]any{"query": "x"}
			if tc.limit != 0 {
				input["limit"] = tc.limit
			}
			if _, err := h.Handle(context.Background(), "acme", "u1", "", "user", input); err != nil {
				t.Fatal(err)
			}
			if err := pool.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
