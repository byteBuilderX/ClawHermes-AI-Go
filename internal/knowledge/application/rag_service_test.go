package application

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/knowledge/domain"
	"github.com/byteBuilderX/stratum/internal/knowledge/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewKnowledgeIngest(t *testing.T) {
	logger := zap.NewNop()
	ingest := NewKnowledgeIngest(nil, nil, nil, nil, logger)

	if ingest == nil {
		t.Error("expected KnowledgeIngest to be non-nil")
	}
}

func TestNewRAGService(t *testing.T) {
	logger := zap.NewNop()
	service := NewRAGService(nil, nil, logger)

	if service == nil {
		t.Error("expected RAGService to be non-nil")
	}
}

func TestRAGQueryKeywordUsesWorkspaceID(t *testing.T) {
	logger := zap.NewNop()
	service := NewRAGService(nil, nil, logger)
	chunks := &recordingChunkRepo{
		chunks: []domain.Chunk{{ID: "chunk-1", DocID: "doc-1", Text: "content", Index: 0}},
	}
	service.SetChunkRepo(chunks)

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID:    "tenant-1",
		Workspace:   "项目资料",
		WorkspaceID: "019047ac-0000-7000-9000-000000000001",
		Question:    "如何申请",
		Mode:        "keyword",
		TopK:        3,
		ViewerID:    "test-user",
	})
	if err != nil {
		t.Fatalf("expected keyword query to succeed, got %v", err)
	}

	if chunks.workspaceID != "019047ac-0000-7000-9000-000000000001" {
		t.Fatalf("expected keyword search to use workspace ID, got %q", chunks.workspaceID)
	}
}

// TestRAGQueryEnrichesSourceWithParentContent 覆盖 expandParentContext 回填路径:
// 命中 leaf 带 ParentID 时,Query 经 GetChunksByIDs→GetParentByID 把整节原文写进
// Source.ParentContent(Parent-Child 设计意图"命中 child 回取 parent"的检索侧闭环)。
func TestRAGQueryEnrichesSourceWithParentContent(t *testing.T) {
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{
		chunks: []domain.Chunk{{
			ID: "leaf-1", DocID: "doc-1", Text: "leaf-text", ParentID: "p1",
		}},
		chunksByIDs: []domain.Chunk{{ID: "leaf-1", ParentID: "p1"}},
		lastParent:  &port.ParentChunk{ID: "p1", Content: "whole-section-text"},
	})

	result, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "q",
		Mode: "keyword", TopK: 5, ViewerID: "test-user",
	})
	if err != nil {
		t.Fatalf("keyword query with parent enrichment must succeed, got %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(result.Sources))
	}
	src := result.Sources[0]
	if src.ChunkID != "leaf-1" {
		t.Fatalf("ChunkID = %q, want leaf-1", src.ChunkID)
	}
	if src.Content != "leaf-text" {
		t.Fatalf("Content = %q, want leaf-text", src.Content)
	}
	if src.ParentContent != "whole-section-text" {
		t.Fatalf("ParentContent = %q, want whole-section-text (leaf parent must be attached)", src.ParentContent)
	}
}

// TestRAGQuerySourceWithoutParentKeepsEmptyParentContent: 非 Parent-Child 命中
// (无 ParentID)不得凭空产生 parent,保持递归摄取 workspace 的行为回归不变。
func TestRAGQuerySourceWithoutParentKeepsEmptyParentContent(t *testing.T) {
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{
		chunks:      []domain.Chunk{{ID: "leaf-1", DocID: "doc-1", Text: "leaf-text"}},
		chunksByIDs: []domain.Chunk{{ID: "leaf-1"}},
	})
	result, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "q",
		Mode: "keyword", TopK: 5, ViewerID: "test-user",
	})
	if err != nil {
		t.Fatalf("keyword query must succeed, got %v", err)
	}
	if len(result.Sources) != 1 || result.Sources[0].ParentContent != "" {
		t.Fatalf("parentless leaf must stay ParentContent empty, got %+v", result.Sources)
	}
}

func TestRAGQueryPreservesDocumentIdentityAcrossRetrievalModes(t *testing.T) {
	for _, mode := range []string{"vector", "keyword", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			vectors := NewMockVectorStore()
			vectors.SetSearchResults([]port.VectorSearchResult{{
				ID: "chunk-vector", SourceDocument: "doc-vector", Content: "vector content", Score: 0.95,
			}})
			service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
			service.SetChunkRepo(&recordingChunkRepo{chunks: []domain.Chunk{{
				ID: "chunk-keyword", DocID: "doc-keyword", Text: "keyword content",
			}}})
			expectedIDs := []string{"doc-vector"}
			if mode == "keyword" {
				expectedIDs = []string{"doc-keyword"}
			} else if mode == "hybrid" {
				expectedIDs = []string{"doc-vector", "doc-keyword"}
			}
			result, err := NewRetrievalEvaluator(service).EvaluateRetrieval(
				reqctx.WithTenantID(context.Background(), "tenant-1"), RetrievalSnapshot{
					WorkspaceID: "workspace-1", WorkspaceName: "support", EmbeddingModel: "embedding-3",
					QueryMode: mode, TopK: 5, Reranking: RerankingNone, QueryRewrite: QueryRewriteNone,
				}, RetrievalCase{Query: "query", RelevantDocumentIDs: expectedIDs,
					CitationDocumentIDs: expectedIDs})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Relevant || !result.CitationCorrect {
				t.Fatalf("document-level evaluation failed: %+v", result)
			}
			got := make(map[string]bool, len(result.RetrievedDocumentIDs))
			for _, id := range result.RetrievedDocumentIDs {
				got[id] = true
			}
			for _, expectedID := range expectedIDs {
				if !got[expectedID] {
					t.Fatalf("document identity %q lost: %+v", expectedID, result)
				}
			}
		})
	}
}

func TestRAGQuerySanitizesDependencyErrorsAndLogs(t *testing.T) {
	sensitive := errors.New("POST https://user:password@example.test/search?api_key=secret-token " +
		"response body private document")
	for _, mode := range []string{"vector", "keyword", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			vectors := NewMockVectorStore()
			vectors.SetSearchError(sensitive)
			service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.New(core))
			chunks := &recordingChunkRepo{searchErr: sensitive}
			service.SetChunkRepo(chunks)
			if mode == "vector" {
				chunks.searchErr = nil
			}
			_, err := service.Query(context.Background(), RAGQueryRequest{
				TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: mode, TopK: 5,
				ViewerID: "test-user",
			})
			if !errors.Is(err, ErrRAGDependency) || errors.Is(err, sensitive) {
				t.Fatalf("dependency classification/cause exposure mismatch: %v", err)
			}
			assertSensitiveTextAbsent(t, err.Error(), logs.All())
		})
	}
}

func assertSensitiveTextAbsent(t *testing.T, errorMessage string, entries []observer.LoggedEntry) {
	t.Helper()
	for _, leaked := range []string{"example.test", "password", "api_key", "secret-token", "private document", "response body"} {
		if strings.Contains(errorMessage, leaked) {
			t.Fatalf("error leaked %q: %s", leaked, errorMessage)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Message, leaked) {
				t.Fatalf("log message leaked %q: %s", leaked, entry.Message)
			}
			for _, value := range entry.ContextMap() {
				if text, ok := value.(string); ok && strings.Contains(text, leaked) {
					t.Fatalf("structured log leaked %q: %#v", leaked, entry.ContextMap())
				}
			}
		}
	}
}

func TestRAGQueryDoesNotLogQuestionContent(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	service := NewRAGService(nil, nil, zap.New(core))
	service.SetChunkRepo(&recordingChunkRepo{})

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1",
		Question: "rag-sensitive-sentinel", Mode: "keyword", TopK: 3,
		ViewerID: "test-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, "rag-sensitive-sentinel") {
			t.Fatalf("question reached log message: %q", entry.Message)
		}
		for _, value := range entry.ContextMap() {
			if text, ok := value.(string); ok && strings.Contains(text, "rag-sensitive-sentinel") {
				t.Fatalf("question reached structured log: %#v", entry.ContextMap())
			}
		}
	}
}

func TestRAGQueryKeywordResolvesWorkspaceIDByName(t *testing.T) {
	logger := zap.NewNop()
	service := NewRAGService(nil, nil, logger)
	chunks := &recordingChunkRepo{}
	service.SetChunkRepo(chunks)
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{
		workspace: &domain.Workspace{
			ID:   "019047ac-0000-7000-9000-000000000002",
			Name: "产品文档",
		},
	})

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID:  "tenant-1",
		Workspace: "产品文档",
		Question:  "如何申请",
		Mode:      "keyword",
		TopK:      3,
		// This test exercises name→ID resolution, not access semantics: the
		// service has no role/doc dependency wired, so the D2 gate is
		// explicitly bypassed the way a system-actor wiring path would.
		SkipAccessCheck: true,
	})
	if err != nil {
		t.Fatalf("expected keyword query to succeed, got %v", err)
	}

	if chunks.workspaceID != "019047ac-0000-7000-9000-000000000002" {
		t.Fatalf("expected keyword search to use resolved workspace ID, got %q", chunks.workspaceID)
	}
}

func TestRAGQueryKeywordDefaultsTopK(t *testing.T) {
	logger := zap.NewNop()
	service := NewRAGService(nil, nil, logger)
	chunks := &recordingChunkRepo{}
	service.SetChunkRepo(chunks)

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID:    "tenant-1",
		WorkspaceID: "workspace-1",
		Question:    "如何申请",
		Mode:        "keyword",
		TopK:        0,
		ViewerID:    "test-user",
	})
	if err != nil {
		t.Fatalf("expected keyword query to succeed, got %v", err)
	}

	if chunks.topK != constants.DefaultRAGTopK {
		t.Fatalf("expected non-positive TopK to default to %d, got %d", constants.DefaultRAGTopK, chunks.topK)
	}
}

func TestRAGQueryKeywordRequiresWorkspaceIDWhenNameCannotBeResolved(t *testing.T) {
	logger := zap.NewNop()
	service := NewRAGService(nil, nil, logger)
	service.SetChunkRepo(&recordingChunkRepo{})

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1",
		Question: "如何申请",
		Mode:     "keyword",
		TopK:     3,
		ViewerID: "test-user",
	})
	if err == nil {
		t.Fatal("expected keyword query without workspace ID to fail, got nil")
	}
	if !strings.Contains(err.Error(), "requires workspace ID") {
		t.Fatalf("expected workspace ID error, got %v", err)
	}
}

func TestNewRAGSearchFnResolvesWorkspaceNameToID(t *testing.T) {
	logger := zap.NewNop()
	service := NewRAGService(nil, nil, logger)
	chunks := &recordingChunkRepo{
		chunks: []domain.Chunk{{ID: "c1", DocID: "d1", Text: "关于学习的段落", Index: 0}},
	}
	service.SetChunkRepo(chunks)
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{
		workspace: &domain.Workspace{
			ID:   "019047ac-0000-7000-9000-000000000099",
			Name: "个人知识库",
			Config: domain.WorkspaceConfig{
				EmbeddingModel: "text-embedding-v3",
				QueryMode:      "keyword",
				TopK:           7,
			},
		},
	})
	// The D1 matrix requires a role/doc provider even for owner viewers, so
	// both fakes are wired; role "owner" makes the search unrestricted.
	service.SetTenantRoleResolver(stubRoleResolver{role: "owner"})
	service.SetDocRepo(stubDocRepo{})

	fn := NewRAGSearchFn(service, "tenant-1", "viewer-1")
	content, err := fn(context.Background(), []string{"个人知识库"}, "学习", 3)
	if err != nil {
		t.Fatalf("expected search to succeed, got %v", err)
	}
	if !strings.Contains(content, "关于学习的段落") {
		t.Fatalf("expected content to include chunk text, got %q", content)
	}
	if chunks.workspaceID != "019047ac-0000-7000-9000-000000000099" {
		t.Fatalf("expected keyword search to receive resolved UUID, got %q", chunks.workspaceID)
	}
	if chunks.topK != 7 {
		t.Fatalf("expected topK from workspace config (7), got %d", chunks.topK)
	}
}

type recordingChunkRepo struct {
	workspaceID string
	topK        int
	docIDs      []string
	chunks      []domain.Chunk
	insertErr   error
	parentErr   error
	searchErr   error
	// listByDoc drives ListByDoc (preview); the last call's docID is recorded
	// in lastDocID so tests can assert the double constraint.
	listByDoc  []domain.Chunk
	listByErr  error
	lastDocID  string
	lastParent *port.ParentChunk
	// chunksByIDs drives GetChunksByIDs (Query 层 expandParentContext 用它查
	// 命中 leaf 的 ParentID); 默认 nil 让既有用例保持 no-op。
	chunksByIDs []domain.Chunk
}

func (r *recordingChunkRepo) InsertBatch(ctx context.Context, tenantID, workspaceID string, chunks []domain.Chunk) error {
	r.workspaceID = workspaceID
	return r.insertErr
}

func (r *recordingChunkRepo) KeywordSearch(ctx context.Context, tenantID, workspaceID, query string, docIDs []string, topK int) ([]domain.Chunk, error) {
	r.workspaceID = workspaceID
	r.docIDs = docIDs
	r.topK = topK
	return r.chunks, r.searchErr
}

func (r *recordingChunkRepo) DeleteByWorkspace(ctx context.Context, tenantID, workspaceID string) error {
	r.workspaceID = workspaceID
	return nil
}

func (r *recordingChunkRepo) InsertParentBatch(_ context.Context, _, _ string, _ []port.ParentChunk) error {
	return r.parentErr
}

func (r *recordingChunkRepo) GetParentByID(_ context.Context, _, _, _ string) (*port.ParentChunk, error) {
	return r.lastParent, r.parentErr
}

func (r *recordingChunkRepo) ListByDoc(ctx context.Context, tenantID, workspaceID, docID string) ([]domain.Chunk, error) {
	r.workspaceID = workspaceID
	r.lastDocID = docID
	return r.listByDoc, r.listByErr
}

func (r *recordingChunkRepo) GetChunksByIDs(_ context.Context, _, _ string, _ []string) ([]domain.Chunk, error) {
	return r.chunksByIDs, nil
}

func (r *recordingChunkRepo) CountByWorkspace(context.Context, string, string) (int64, error) {
	return 0, nil
}

type recordingWorkspaceRepo struct {
	workspace *domain.Workspace
}

func (r *recordingWorkspaceRepo) Create(ctx context.Context, tenantID string, ws *domain.Workspace, _ []string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}

func (r *recordingWorkspaceRepo) GetByName(ctx context.Context, tenantID, name string) (*domain.Workspace, error) {
	return r.workspace, nil
}

func (r *recordingWorkspaceRepo) GetByID(ctx context.Context, tenantID, id string) (*domain.Workspace, error) {
	return r.workspace, nil
}

func (r *recordingWorkspaceRepo) List(ctx context.Context, tenantID string) ([]*domain.Workspace, error) {
	return nil, nil
}

func (r *recordingWorkspaceRepo) UpdateWorkspaceAll(ctx context.Context, tenantID, name string, renameTo, description *string, snap domain.KnowledgeWorkspaceSnapshot, _ string, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}

func (r *recordingWorkspaceRepo) RollbackWorkspace(ctx context.Context, tenantID, name string, snap domain.KnowledgeWorkspaceSnapshot, _ string, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}

func (r *recordingWorkspaceRepo) Delete(ctx context.Context, tenantID, name string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}

func (r *recordingWorkspaceRepo) GetConfigForUpload(ctx context.Context, tenantID, name string) (domain.WorkspaceConfig, error) {
	return domain.WorkspaceConfig{}, nil
}

func (r *recordingWorkspaceRepo) GetConfigByID(ctx context.Context, tenantID, id string) (domain.WorkspaceConfig, error) {
	if r.workspace != nil && r.workspace.ID == id {
		return r.workspace.Config, nil
	}
	return domain.WorkspaceConfig{}, nil
}

func TestRAGService_MergeResults_PartialFailureKeepsContentAndWarns(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	svc := NewRAGService(nil, nil, zap.New(core))

	content, err := svc.mergeResults([]wsResult{
		{content: "chunk-a"},
		{err: errors.New("workspace down")},
		{content: "chunk-b"},
	})
	if err != nil {
		t.Fatalf("partial failure must not fail the query, got %v", err)
	}
	if !strings.Contains(content, "chunk-a") || !strings.Contains(content, "chunk-b") {
		t.Fatalf("expected successful workspace content, got %q", content)
	}

	warns := logs.FilterMessage("knowledge.rag.partial_failure").All()
	if len(warns) != 1 {
		t.Fatalf("expected 1 partial-failure WARN, got %d", len(warns))
	}
	failed := warns[0].ContextMap()["failed_workspaces"]
	if failed != int64(1) {
		t.Errorf("failed_workspaces=%v, want 1", failed)
	}
}

func TestRAGService_MergeResults_AllFailedReturnsFirstError(t *testing.T) {
	svc := NewRAGService(nil, nil, zap.NewNop())
	errA := errors.New("workspace A down")

	content, err := svc.mergeResults([]wsResult{{err: errA}, {err: errors.New("workspace B down")}})
	if !errors.Is(err, errA) {
		t.Fatalf("expected first error to surface, got %v", err)
	}
	if content != "" {
		t.Fatalf("expected empty content when all workspaces failed, got %q", content)
	}
}

func TestRAGService_MergeResults_AllSucceedConcatenatesContent(t *testing.T) {
	svc := NewRAGService(nil, nil, zap.NewNop())

	content, err := svc.mergeResults([]wsResult{{content: "a"}, {content: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if content != "ab" {
		t.Fatalf("expected concatenated content, got %q", content)
	}
}

// blockingChunkRepo blocks each KeywordSearch until release is closed while
// tracking peak concurrency, making the fan-out bound deterministically
// observable.
type blockingChunkRepo struct {
	recordingChunkRepo
	release chan struct{}
	current int32
	max     int32
}

func (r *blockingChunkRepo) KeywordSearch(_ context.Context, _, _, _ string, _ []string, _ int) ([]domain.Chunk, error) {
	inFlight := atomic.AddInt32(&r.current, 1)
	for {
		peak := atomic.LoadInt32(&r.max)
		if inFlight <= peak || atomic.CompareAndSwapInt32(&r.max, peak, inFlight) {
			break
		}
	}
	<-r.release
	atomic.AddInt32(&r.current, -1)
	return []domain.Chunk{{ID: "c1", DocID: "d1", Text: "关于学习的段落", Index: 0}}, nil
}

func TestNewRAGSearchFn_FanoutBoundedByMaxConcurrentWorkspaceSearch(t *testing.T) {
	repo := &blockingChunkRepo{release: make(chan struct{})}
	svc := NewRAGService(nil, nil, zap.NewNop())
	svc.SetChunkRepo(repo)
	svc.SetWorkspaceRepo(&recordingWorkspaceRepo{
		workspace: &domain.Workspace{
			ID:   "019047ac-0000-7000-9000-000000000099",
			Name: "个人知识库",
			Config: domain.WorkspaceConfig{
				EmbeddingModel: "text-embedding-v3",
				QueryMode:      "keyword",
				TopK:           3,
			},
		},
	})
	svc.SetTenantRoleResolver(stubRoleResolver{role: "owner"})
	svc.SetDocRepo(stubDocRepo{})

	const wsCount = 6
	names := make([]string, wsCount)
	for i := range names {
		names[i] = "ws-" + itoa(i+1)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = NewRAGSearchFn(svc, "tenant-1", "viewer-1")(context.Background(), names, "学习", 3)
	}()

	limit := int32(constants.MaxConcurrentWorkspaceSearch)
	// Wait until the bound is saturated: searches may enter KeywordSearch
	// only one at a time, so reaching `limit` proves bounded launch.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&repo.current) < limit {
		select {
		case <-done:
			t.Fatal("fan-out finished before the concurrency bound was saturated")
		case <-deadline:
			t.Fatalf("timeout waiting for %d concurrent searches", limit)
		case <-time.After(5 * time.Millisecond):
		}
	}

	// If the fan-out were unbounded, extra searches would have entered by now.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&repo.current); got > limit {
		t.Fatalf("fan-out exceeded bound: %d searches in flight, limit %d", got, limit)
	}

	close(repo.release)
	<-done
	if peak := atomic.LoadInt32(&repo.max); peak > limit {
		t.Fatalf("peak concurrency %d exceeds bound %d", peak, limit)
	}
}

func itoa(n int) string {
	return string(rune('0' + n))
}

// stubRoleResolver returns a fixed tenant role; RAGService tests use it to
// exercise the D1 matrix without an IAM dependency.
type stubRoleResolver struct{ role string }

func (s stubRoleResolver) ResolveTenantRole(context.Context, string, string) (string, error) {
	return s.role, nil
}

// stubDocRepo is an inert DocRepo fake whose VisibleDocIDs returns a fixed
// whitelist and List returns the configured docs; the remaining methods are
// stubs so RAGService tests can wire doc-level visibility without a database.
type stubDocRepo struct {
	visible []string
	docs    []*domain.Document
	listErr error
	// doc is returned by GetByID; nil means "not found".
	doc *domain.Document
}

func (s stubDocRepo) Save(context.Context, string, string, *domain.Document) (bool, error) {
	return true, nil
}
func (s stubDocRepo) List(context.Context, string, string) ([]*domain.Document, error) {
	return s.docs, s.listErr
}
func (s stubDocRepo) Delete(context.Context, string, string, string) error { return nil }
func (s stubDocRepo) ExistsByHash(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (s stubDocRepo) CountByWorkspace(context.Context, string, string) (int, error) {
	return 0, nil
}
func (s stubDocRepo) MarkIngestStarted(context.Context, string, string, int) error   { return nil }
func (s stubDocRepo) MarkIngestCompleted(context.Context, string, string, int) error { return nil }
func (s stubDocRepo) MarkIngestFailed(context.Context, string, string, string) error { return nil }
func (s stubDocRepo) RecoverStuckIngests(context.Context, string, time.Duration) (int, error) {
	return 0, nil
}
func (s stubDocRepo) VisibleDocIDs(context.Context, string, string, string, string) ([]string, error) {
	return s.visible, nil
}
func (s stubDocRepo) GetByID(context.Context, string, string, string) (*domain.Document, error) {
	return s.doc, nil
}
func (s stubDocRepo) SetDocAccess(context.Context, string, string, []string, []string) error {
	return nil
}
func (s stubDocRepo) CASReplace(context.Context, string, string, string, string, string, string, map[string]any, int) (bool, error) {
	return true, nil
}
func (s stubDocRepo) CASBeginDelete(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (s stubDocRepo) MarkBuiltinLegacy(context.Context, string, string, []string) error {
	return nil
}

// countingEmbedder wraps mockEmbedder and counts EmbedVector calls so the
// empty-whitelist test can assert the embed path is never entered.
type countingEmbedder struct {
	mockEmbedder
	calls atomic.Int32
}

func (c *countingEmbedder) EmbedVector(ctx context.Context, text string) ([]float32, error) {
	c.calls.Add(1)
	return c.mockEmbedder.EmbedVector(ctx, text)
}

// memberWorkspace returns a workspace created by someone else, so a viewer is
// a plain member whose visibility comes from docRepo.VisibleDocIDs.
func memberWorkspace() *domain.Workspace {
	return &domain.Workspace{ID: "workspace-1", Name: "support", CreatedBy: "other-user"}
}

func TestRAGQueryFailsClosedWithoutViewerIdentity(t *testing.T) {
	service := NewRAGService(&mockEmbedder{dim: 3}, NewMockVectorStore(), zap.NewNop())
	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector", TopK: 5,
	})
	if !errors.Is(err, ErrRAGDependency) {
		t.Fatalf("D2 gate must fail closed without viewer identity, got %v", err)
	}
}

func TestRAGQuerySkipAccessCheckBypassesD2Gate(t *testing.T) {
	service := NewRAGService(&mockEmbedder{dim: 3}, NewMockVectorStore(), zap.NewNop())
	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector", TopK: 5,
		SkipAccessCheck: true,
	})
	if err != nil {
		t.Fatalf("explicit SkipAccessCheck must pass the D2 gate, got %v", err)
	}
}

func TestRAGQueryVectorFiltersByVisibleDocIDs(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]port.VectorSearchResult{{
		ID: "c1", SourceDocument: "doc-visible", Content: "hit", Score: 0.9,
	}})
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-visible"}})

	result, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector", TopK: 5,
		ViewerID: "viewer-1",
	})
	if err != nil {
		t.Fatalf("expected vector query to succeed, got %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(result.Sources))
	}
	if !strings.Contains(vectors.lastExpression, `"doc-visible"`) {
		t.Fatalf("vector leg expression %q missing the visible doc", vectors.lastExpression)
	}
}

func TestRAGQueryKeywordFiltersByVisibleDocIDs(t *testing.T) {
	chunks := &recordingChunkRepo{chunks: []domain.Chunk{{ID: "c1", DocID: "doc-visible", Text: "hit"}}}
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetChunkRepo(chunks)
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-visible"}})

	_, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "keyword", TopK: 5,
		ViewerID: "viewer-1",
	})
	if err != nil {
		t.Fatalf("expected keyword query to succeed, got %v", err)
	}
	if len(chunks.docIDs) != 1 || chunks.docIDs[0] != "doc-visible" {
		t.Fatalf("keyword leg received docIDs %v, want [doc-visible]", chunks.docIDs)
	}
}

func TestRAGQueryHybridFiltersBothLegs(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]port.VectorSearchResult{{
		ID: "c1", SourceDocument: "doc-visible", Content: "vector hit", Score: 0.9,
	}})
	chunks := &recordingChunkRepo{chunks: []domain.Chunk{{ID: "c2", DocID: "doc-visible", Text: "keyword hit"}}}
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
	service.SetChunkRepo(chunks)
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-visible"}})

	result, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "hybrid", TopK: 5,
		ViewerID: "viewer-1",
	})
	if err != nil {
		t.Fatalf("expected hybrid query to succeed, got %v", err)
	}
	if len(result.Sources) == 0 {
		t.Fatal("expected hybrid query to return sources")
	}
	if !strings.Contains(vectors.lastExpression, `"doc-visible"`) {
		t.Fatalf("hybrid vector leg expression %q missing the visible doc", vectors.lastExpression)
	}
	if len(chunks.docIDs) != 1 || chunks.docIDs[0] != "doc-visible" {
		t.Fatalf("hybrid keyword leg received docIDs %v, want [doc-visible]", chunks.docIDs)
	}
}

func TestRAGQueryEmptyVisibleSetReturnsEmptyWithoutEmbedding(t *testing.T) {
	embedder := &countingEmbedder{}
	vectors := NewMockVectorStore()
	service := NewRAGService(embedder, vectors, zap.NewNop())
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: nil})

	result, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector", TopK: 5,
		ViewerID: "viewer-1",
	})
	if err != nil {
		t.Fatalf("expected empty result, not error, got %v", err)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources for an empty visible set, got %d", len(result.Sources))
	}
	if embedder.calls.Load() != 0 {
		t.Fatalf("embedder must not be called when the visible set is empty, got %d calls", embedder.calls.Load())
	}
	if vectors.lastExpression != "" {
		t.Fatalf("vector store must not be searched, got expression %q", vectors.lastExpression)
	}
}

func TestRAGQueryOwnerRoleIsUnrestricted(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]port.VectorSearchResult{{
		ID: "c1", SourceDocument: "doc", Content: "hit", Score: 0.9,
	}})
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{})
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "owner"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-private"}})

	result, err := service.Query(context.Background(), RAGQueryRequest{
		TenantID: "tenant-1", WorkspaceID: "workspace-1", Question: "query", Mode: "vector", TopK: 5,
		ViewerID: "viewer-1",
	})
	if err != nil {
		t.Fatalf("expected owner query to succeed, got %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(result.Sources))
	}
	if vectors.lastExpression != "" {
		t.Fatalf("owner query must not filter, got expression %q", vectors.lastExpression)
	}
}

func TestBuildDocFilterExprQuotesAndEscapes(t *testing.T) {
	if expr := buildDocFilterExpr(nil); expr != "" {
		t.Fatalf("empty whitelist must yield no filter, got %q", expr)
	}
	if expr := buildDocFilterExpr([]string{}); expr != "" {
		t.Fatalf("empty whitelist must yield no filter, got %q", expr)
	}
	expr := buildDocFilterExpr([]string{`doc"quoted`, `back\slash`})
	if !strings.HasPrefix(expr, "source_document in [") {
		t.Fatalf("unexpected expression prefix: %q", expr)
	}
	if !strings.Contains(expr, `"doc\"quoted"`) || !strings.Contains(expr, `"back\\slash"`) {
		t.Fatalf(`ids must be %q-quoted with " and \ escaped: %q`, expr, expr)
	}
}

func TestFilterExprTooLong(t *testing.T) {
	if filterExprTooLong("") {
		t.Fatal("empty expression must not be too long")
	}
	atLimit := strings.Repeat("x", constants.MaxMilvusFilterLen)
	if filterExprTooLong(atLimit) {
		t.Fatal("expression at the limit must be accepted")
	}
	if !filterExprTooLong(strings.Repeat("x", constants.MaxMilvusFilterLen+1)) {
		t.Fatal("expression beyond the limit must be rejected")
	}
}

func TestRAGSearchEvidenceCarriesCitationMetadata(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]port.VectorSearchResult{{
		ID: "c1", SourceDocument: "doc-visible", Content: "hit", Score: 0.9,
	}})
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{})
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{
		visible: []string{"doc-visible"},
		docs:    []*domain.Document{{ID: "doc-visible", Source: "annual-report.pdf"}},
	})

	fn := NewRAGSearchEvidenceFn(service, "tenant-1", "viewer-1")
	ev, err := fn(context.Background(), []string{"support"}, "query", 5)
	if err != nil {
		t.Fatalf("expected evidence search to succeed, got %v", err)
	}
	if len(ev.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(ev.Sources))
	}
	src := ev.Sources[0]
	if src.DocumentID != "doc-visible" {
		t.Fatalf("DocumentID = %q, want doc-visible", src.DocumentID)
	}
	if src.DocumentTitle != "annual-report.pdf" {
		t.Fatalf("DocumentTitle = %q, want annual-report.pdf", src.DocumentTitle)
	}
	if src.Snippet != "hit" {
		t.Fatalf("Snippet = %q, want hit", src.Snippet)
	}
}

// TestRAGSearchEvidenceCarriesParentContent 覆盖 B2 引用链闭环:Query 已把命中
// chunk 的整节原文写进 Source.ParentContent,evidence 组装(RAGSearchSource 字面量
// 补拷)必须把它带进下游 —— agent/SSE/持久化引用卡片据此就地展开上下文。
func TestRAGSearchEvidenceCarriesParentContent(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]port.VectorSearchResult{{
		ID: "c1", SourceDocument: "doc-visible", Content: "hit", Score: 0.9,
	}})
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{
		chunksByIDs: []domain.Chunk{{ID: "c1", ParentID: "p1"}},
		lastParent:  &port.ParentChunk{ID: "p1", Content: "whole-section-text"},
	})
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{
		visible: []string{"doc-visible"},
		docs:    []*domain.Document{{ID: "doc-visible", Source: "annual-report.pdf"}},
	})

	fn := NewRAGSearchEvidenceFn(service, "tenant-1", "viewer-1")
	ev, err := fn(context.Background(), []string{"support"}, "query", 5)
	if err != nil {
		t.Fatalf("expected evidence search to succeed, got %v", err)
	}
	if len(ev.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(ev.Sources))
	}
	if got := ev.Sources[0].ParentContent; got != "whole-section-text" {
		t.Fatalf("evidence ParentContent = %q, want whole-section-text (must flow from Query through citation)", got)
	}
}

func TestRAGSearchEvidenceSnippetTruncatedToConstant(t *testing.T) {
	long := strings.Repeat("很", constants.MaxSourceSnippetRunes+50)
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]port.VectorSearchResult{{
		ID: "c1", SourceDocument: "doc-visible", Content: long, Score: 0.9,
	}})
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{})
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-visible"}})

	fn := NewRAGSearchEvidenceFn(service, "tenant-1", "viewer-1")
	ev, err := fn(context.Background(), []string{"support"}, "query", 5)
	if err != nil {
		t.Fatalf("expected evidence search to succeed, got %v", err)
	}
	if len(ev.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(ev.Sources))
	}
	if got := len([]rune(ev.Sources[0].Snippet)); got != constants.MaxSourceSnippetRunes {
		t.Fatalf("Snippet length = %d runes, want %d", got, constants.MaxSourceSnippetRunes)
	}
}

func TestRAGSearchEvidenceTitlesDegradeOnRepoFailure(t *testing.T) {
	vectors := NewMockVectorStore()
	vectors.SetSearchResults([]port.VectorSearchResult{{
		ID: "c1", SourceDocument: "doc-visible", Content: "hit", Score: 0.9,
	}})
	service := NewRAGService(&mockEmbedder{dim: 3}, vectors, zap.NewNop())
	service.SetChunkRepo(&recordingChunkRepo{})
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-visible"}, listErr: errors.New("db down")})

	// A title lookup failure must not fail retrieval: citation titles are
	// display metadata; the visible-set filter already ran inside Query.
	fn := NewRAGSearchEvidenceFn(service, "tenant-1", "viewer-1")
	ev, err := fn(context.Background(), []string{"support"}, "query", 5)
	if err != nil {
		t.Fatalf("evidence search must survive title repo failure, got %v", err)
	}
	if len(ev.Sources) != 1 || ev.Sources[0].DocumentID != "doc-visible" {
		t.Fatalf("sources lost despite title failure: %+v", ev.Sources)
	}
	if ev.Sources[0].DocumentTitle != "" {
		t.Fatalf("DocumentTitle = %q, want empty on repo failure", ev.Sources[0].DocumentTitle)
	}
}

func TestPreviewDocumentInvisibleAndNonexistentUniformlyNotFound(t *testing.T) {
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetChunkRepo(&recordingChunkRepo{})

	// Invisible: whitelist does not contain the requested doc.
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-a"}, doc: &domain.Document{ID: "doc-b"}})
	_, err := service.PreviewDocument(context.Background(), "tenant-1", "support", "doc-b", "viewer-1")
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("invisible doc must surface as ErrDocumentNotFound (no existence leak), got %v", err)
	}

	// Nonexistent: whitelist contains it but the doc row is missing.
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-b"}, doc: nil})
	_, err = service.PreviewDocument(context.Background(), "tenant-1", "support", "doc-b", "viewer-1")
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("missing doc must surface as ErrDocumentNotFound, got %v", err)
	}
}

func TestPreviewDocumentFailsClosedWithoutIdentityOrRepos(t *testing.T) {
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-a"}, doc: &domain.Document{ID: "doc-a"}})

	// chunkRepo missing → dependency fail closed.
	if _, err := service.PreviewDocument(context.Background(), "tenant-1", "support", "doc-a", "viewer-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("missing chunkRepo must fail closed, got %v", err)
	}

	service.SetChunkRepo(&recordingChunkRepo{})
	// Empty viewer identity → fail closed (preview is a user read path).
	if _, err := service.PreviewDocument(context.Background(), "tenant-1", "support", "doc-a", ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("empty viewerID must fail closed, got %v", err)
	}
}

func TestPreviewDocumentUnknownWorkspaceNotFound(t *testing.T) {
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: nil})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{visible: []string{"doc-a"}})
	service.SetChunkRepo(&recordingChunkRepo{})

	if _, err := service.PreviewDocument(context.Background(), "tenant-1", "ghost", "doc-a", "viewer-1"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("unknown workspace must surface as ErrDocumentNotFound, got %v", err)
	}
}

func TestPreviewDocumentReassemblesChunksWithParents(t *testing.T) {
	chunks := &recordingChunkRepo{
		listByDoc: []domain.Chunk{
			{ID: "c1", DocID: "doc-a", Index: 0, Text: "leaf-0"},
			{ID: "c2", DocID: "doc-a", Index: 1, Text: "leaf-1", ParentID: "p1"},
			{ID: "c3", DocID: "doc-a", Index: 2, Text: "leaf-2"},
		},
		lastParent: &port.ParentChunk{ID: "p1", Content: "parent-context"},
	}
	service := NewRAGService(nil, nil, zap.NewNop())
	service.SetWorkspaceRepo(&recordingWorkspaceRepo{workspace: memberWorkspace()})
	service.SetTenantRoleResolver(stubRoleResolver{role: "member"})
	service.SetDocRepo(stubDocRepo{
		visible: []string{"doc-a"},
		doc:     &domain.Document{ID: "doc-a", Source: "annual-report.pdf"},
	})
	service.SetChunkRepo(chunks)

	preview, err := service.PreviewDocument(context.Background(), "tenant-1", "support", "doc-a", "viewer-1")
	if err != nil {
		t.Fatal(err)
	}
	if preview.DocumentID != "doc-a" || preview.DocumentTitle != "annual-report.pdf" {
		t.Fatalf("preview identity wrong: %+v", preview)
	}
	if preview.ChunkCount != 3 || len(preview.Segments) != 3 {
		t.Fatalf("expected 3 segments, got count=%d len=%d", preview.ChunkCount, len(preview.Segments))
	}
	if chunks.lastDocID != "doc-a" || chunks.workspaceID != "workspace-1" {
		t.Fatalf("ListByDoc must carry workspace+doc double constraint, got ws=%q doc=%q", chunks.workspaceID, chunks.lastDocID)
	}
	seg1 := preview.Segments[1]
	if seg1.ParentContent != "parent-context" {
		t.Fatalf("expected parent content attached to leaf-1, got %q", seg1.ParentContent)
	}
	for i, seg := range preview.Segments {
		if seg.Index != int64(i) || seg.Content != "leaf-"+string(rune('0'+i)) {
			t.Fatalf("segment %d out of order: %+v", i, seg)
		}
	}
}
