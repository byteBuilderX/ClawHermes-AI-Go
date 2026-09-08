package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	storagemilvus "github.com/byteBuilderX/stratum/pkg/storage/milvus"
)

type fakeMilvusStore struct {
	primaryCollection string
	primaryIDs        []string
	filterCalls       []string
	filterErrors      []error
	searchExpression  string
	searchResults     []storagemilvus.SearchResult
	searchErr         error
	searchCalls       int
	lists             map[string][]string
	listCalls         []string
	listErr           error
	primaryErrors     []error
	primaryCalls      []string
}

func (f *fakeMilvusStore) SearchWithFilter(_ context.Context, _ string, _ []float32, _ int, expression string, _ ...string) ([]storagemilvus.SearchResult, error) {
	f.searchCalls++
	f.searchExpression = expression
	return f.searchResults, f.searchErr
}

func (f *fakeMilvusStore) CreateCollectionWithDim(context.Context, string, int) error { return nil }
func (f *fakeMilvusStore) Insert(context.Context, string, []storagemilvus.DocumentChunk, string) error {
	return nil
}

func (f *fakeMilvusStore) DeleteByPrimaryIDs(_ context.Context, collection string, ids []string) error {
	f.primaryCollection, f.primaryIDs = collection, ids
	f.primaryCalls = append(f.primaryCalls, collection+":"+strings.Join(ids, ","))
	if len(f.primaryErrors) == 0 {
		return nil
	}
	err := f.primaryErrors[0]
	f.primaryErrors = f.primaryErrors[1:]
	return err
}

func (f *fakeMilvusStore) DeleteByFilter(_ context.Context, collection, expr string) error {
	f.filterCalls = append(f.filterCalls, collection+":"+expr)
	if len(f.filterErrors) == 0 {
		return nil
	}
	err := f.filterErrors[0]
	f.filterErrors = f.filterErrors[1:]
	return err
}

func (f *fakeMilvusStore) ListCollections(_ context.Context, prefix string) ([]string, error) {
	f.listCalls = append(f.listCalls, prefix)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.lists[prefix], nil
}

func TestMilvusPortAdapterDeleteUsesPrimaryIDs(t *testing.T) {
	store := &fakeMilvusStore{}
	adapter := NewMilvusPortAdapter(store)
	ids := []string{"fact-1", "fact-2"}

	if err := adapter.Delete(context.Background(), "memory_facts_tenant_1", ids); err != nil {
		t.Fatal(err)
	}
	if store.primaryCollection != "memory_facts_tenant_1" || strings.Join(store.primaryIDs, ",") != "fact-1,fact-2" {
		t.Fatalf("primary delete = %q %v", store.primaryCollection, store.primaryIDs)
	}
}

func TestMilvusPortAdapterDeleteEntryVectorsCoversLegacyAndModelSuffixed(t *testing.T) {
	store := &fakeMilvusStore{lists: map[string][]string{
		"memory_tenant_1_": {"memory_tenant_1_text_embedding_v2"},
	}}
	adapter := NewMilvusPortAdapter(store)

	require.NoError(t, adapter.DeleteEntryVectors(context.Background(), "tenant-1", []string{"e1", "e2"}))
	require.Equal(t, []string{
		"memory_tenant_1:e1,e2",
		"memory_tenant_1_text_embedding_v2:e1,e2",
	}, store.primaryCalls)
}

func TestMilvusPortAdapterDeleteFactVectorsAggregatesErrors(t *testing.T) {
	store := &fakeMilvusStore{
		lists: map[string][]string{"memory_facts_tenant_1_": {"memory_facts_tenant_1_text_embedding_v3"}},
		primaryErrors: []error{
			errors.New("legacy failed"),
			errors.New("suffixed failed"),
		},
	}
	adapter := NewMilvusPortAdapter(store)

	err := adapter.DeleteFactVectors(context.Background(), "tenant-1", []string{"f1"})
	require.ErrorContains(t, err, "legacy failed")
	require.ErrorContains(t, err, "suffixed failed")
}

func TestMilvusPortAdapterDeleteVectorsByIDs_SurfacesListingFailure(t *testing.T) {
	store := &fakeMilvusStore{listErr: errors.New("list collections failed")}
	adapter := NewMilvusPortAdapter(store)

	err := adapter.DeleteFactVectors(context.Background(), "tenant-1", []string{"f1"})
	require.ErrorContains(t, err, "list collections")
}

func TestMilvusPortAdapterDeleteVectorsByIDs_EmptyIDsNoOp(t *testing.T) {
	store := &fakeMilvusStore{}
	adapter := NewMilvusPortAdapter(store)

	require.NoError(t, adapter.DeleteEntryVectors(context.Background(), "tenant-1", nil))
	require.NoError(t, adapter.DeleteFactVectors(context.Background(), "tenant-1", nil))
	require.Empty(t, store.primaryCalls)
	require.Empty(t, store.listCalls)
}

func TestMilvusPortAdapterDeleteAllByUserCleansBothCollectionsAndAggregatesErrors(t *testing.T) {
	errFacts, errLegacy := errors.New("facts failed"), errors.New("legacy failed")
	store := &fakeMilvusStore{filterErrors: []error{errFacts, errLegacy}}
	adapter := NewMilvusPortAdapter(store)

	err := adapter.DeleteAllByUser(context.Background(), "tenant-1", `user-"1`)
	if !errors.Is(err, errFacts) || !errors.Is(err, errLegacy) {
		t.Fatalf("error = %v, want both collection errors", err)
	}
	want := []string{
		`memory_facts_tenant_1:user_id == "user-\"1"`,
		`memory_tenant_1:user_id == "user-\"1"`,
	}
	if strings.Join(store.filterCalls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("filter calls = %v, want %v", store.filterCalls, want)
	}
}

func TestMilvusPortAdapterDeleteAllByAgentCleansBothCollections(t *testing.T) {
	store := &fakeMilvusStore{}
	adapter := NewMilvusPortAdapter(store)
	if err := adapter.DeleteAllByAgent(context.Background(), "tenant-1", "agent-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`memory_facts_tenant_1:agent_id == "agent-1" and scope == "agent"`,
		`memory_tenant_1:agent_id == "agent-1" and scope == "agent"`,
	}
	if strings.Join(store.filterCalls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("filter calls = %v, want %v", store.filterCalls, want)
	}
}

func TestMilvusPortAdapterDeleteAllByAgentCleansModelSuffixedCollections(t *testing.T) {
	// 模型后缀集合按前缀枚举（尾下划线，避免 t1 误匹配 t10）：legacy 名先删，
	// 再删 memory_<t>_ / memory_facts_<t>_ 下列出的全部模型后缀集合。
	store := &fakeMilvusStore{lists: map[string][]string{
		"memory_tenant_1_":       {"memory_tenant_1_text_embedding_v2", "memory_tenant_1_text_embedding_v3"},
		"memory_facts_tenant_1_": {"memory_facts_tenant_1_text_embedding_v3"},
	}}
	adapter := NewMilvusPortAdapter(store)
	if err := adapter.DeleteAllByAgent(context.Background(), "tenant-1", "agent-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`memory_facts_tenant_1:agent_id == "agent-1" and scope == "agent"`,
		`memory_tenant_1:agent_id == "agent-1" and scope == "agent"`,
		`memory_tenant_1_text_embedding_v2:agent_id == "agent-1" and scope == "agent"`,
		`memory_tenant_1_text_embedding_v3:agent_id == "agent-1" and scope == "agent"`,
		`memory_facts_tenant_1_text_embedding_v3:agent_id == "agent-1" and scope == "agent"`,
	}
	if strings.Join(store.filterCalls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("filter calls = %v, want %v", store.filterCalls, want)
	}
	if strings.Join(store.listCalls, ",") != "memory_tenant_1_,memory_facts_tenant_1_" {
		t.Fatalf("list calls = %v", store.listCalls)
	}
}

func TestMilvusPortAdapterDeleteAllListFailureSurfaced(t *testing.T) {
	// 列不出模型后缀集合不能静默当没这回事：legacy 仍删，list 失败必须暴露。
	store := &fakeMilvusStore{listErr: errors.New("list boom")}
	adapter := NewMilvusPortAdapter(store)
	err := adapter.DeleteAllByAgent(context.Background(), "tenant-1", "agent-1")
	if err == nil || !strings.Contains(err.Error(), "list boom") {
		t.Fatalf("error = %v, want list failure surfaced", err)
	}
	if len(store.filterCalls) != 2 {
		t.Fatalf("filter calls = %v, want legacy deletes only", store.filterCalls)
	}
}

func TestMemoryFactDocumentChunkPreservesWhitelistedMetadata(t *testing.T) {
	doc := &memport.VectorDoc{
		ID:        "fact-1",
		Embedding: []float32{0.1, 0.2},
		Metadata: map[string]interface{}{
			"user_id":         "user-1",
			"agent_id":        "agent-1",
			"scope":           "agent",
			"content":         "likes Go",
			"conversation_id": "conversation-1",
			"importance":      0.75,
			"category":        "skill",
			"confidence":      0.875,
			"source":          "llm_extraction",
			"api_key":         "must-not-leak",
			"arbitrary":       map[string]string{"secret": "must-not-leak"},
		},
	}

	chunk, err := memoryFactDocumentChunk(doc)
	if err != nil {
		t.Fatalf("memoryFactDocumentChunk: %v", err)
	}
	if chunk.ID != doc.ID || chunk.UserID != "user-1" || chunk.AgentID != "agent-1" ||
		chunk.Scope != "agent" || chunk.Content != "likes Go" || len(chunk.Vector) != 2 {
		t.Fatalf("chunk fields not preserved: %#v", chunk)
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(chunk.SourceDocument), &metadata); err != nil {
		t.Fatalf("source document is not JSON: %v", err)
	}
	wantKeys := []string{"conversation_id", "importance", "category", "confidence", "source"}
	if len(metadata) != len(wantKeys) {
		t.Fatalf("metadata keys = %v, want only %v", metadata, wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := metadata[key]; !ok {
			t.Errorf("missing metadata key %q", key)
		}
	}
	if metadata["importance"] != 0.75 || metadata["confidence"] != 0.875 {
		t.Fatalf("numeric metadata changed type/value: %#v", metadata)
	}
	if strings.Contains(chunk.SourceDocument, "api_key") || strings.Contains(chunk.SourceDocument, "must-not-leak") || strings.Contains(chunk.SourceDocument, "arbitrary") {
		t.Fatalf("source document copied non-whitelisted metadata: %s", chunk.SourceDocument)
	}

	second, err := memoryFactDocumentChunk(doc)
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceDocument != chunk.SourceDocument {
		t.Fatalf("JSON is not deterministic: %q != %q", second.SourceDocument, chunk.SourceDocument)
	}
}

func TestMemoryFactDocumentChunkRejectsInvalidMetadata(t *testing.T) {
	doc := validMemoryFactVectorDoc()
	doc.Metadata["confidence"] = "high"

	_, err := memoryFactDocumentChunk(doc)
	if err == nil || !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("error = %v, want clear confidence metadata error", err)
	}
}

func TestMemoryFactDocumentChunkRejectsOversizedSourceDocument(t *testing.T) {
	doc := validMemoryFactVectorDoc()
	doc.Metadata["conversation_id"] = strings.Repeat("x", 256)

	_, err := memoryFactDocumentChunk(doc)
	if err == nil || !strings.Contains(err.Error(), "source_document") || !strings.Contains(err.Error(), "255") {
		t.Fatalf("error = %v, want clear source_document length error", err)
	}
}

func TestMilvusPortAdapterSearchBuildsScopeSafeExpressions(t *testing.T) {
	tests := []struct {
		name   string
		filter memport.VectorSearchFilter
		want   string
	}{
		{
			name: "user and current agent",
			filter: memport.VectorSearchFilter{UserID: "user-1", AgentID: "agent-1",
				IncludeUserScope: true, IncludeAgentScope: true},
			want: `user_id == "user-1" && (scope == "user" || (scope == "agent" && agent_id == "agent-1"))`,
		},
		{
			name:   "user only",
			filter: memport.VectorSearchFilter{UserID: "user-1", IncludeUserScope: true},
			want:   `user_id == "user-1" && scope == "user"`,
		},
		{
			name: "escaped values",
			filter: memport.VectorSearchFilter{UserID: `user-"\\`, AgentID: `agent-"\\`,
				IncludeUserScope: true, IncludeAgentScope: true},
			want: `user_id == "user-\"\\\\" && (scope == "user" || (scope == "agent" && agent_id == "agent-\"\\\\"))`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeMilvusStore{}
			_, err := NewMilvusPortAdapter(store).Search(context.Background(), "memory_facts_tenant", []float32{1}, 5, tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			if store.searchExpression != tt.want {
				t.Fatalf("expression = %q, want %q", store.searchExpression, tt.want)
			}
		})
	}
}

func TestMilvusPortAdapterSearchRejectsInvalidFiltersWithoutCallingStore(t *testing.T) {
	tests := []memport.VectorSearchFilter{
		{IncludeUserScope: true},
		{UserID: "user-1", IncludeAgentScope: true},
		{UserID: "user-1"},
	}
	for _, filter := range tests {
		store := &fakeMilvusStore{}
		_, err := NewMilvusPortAdapter(store).Search(context.Background(), "memory_facts_tenant", []float32{1}, 5, filter)
		if !errors.Is(err, memport.ErrInvalidVectorSearchFilter) {
			t.Fatalf("error = %v, want invalid filter", err)
		}
		if store.searchCalls != 0 {
			t.Fatalf("search calls = %d, want 0", store.searchCalls)
		}
	}
}

func TestMilvusPortAdapterSearchMapsResultsAndPropagatesErrors(t *testing.T) {
	filter := memport.VectorSearchFilter{UserID: "user-1", IncludeUserScope: true}
	store := &fakeMilvusStore{searchResults: []storagemilvus.SearchResult{{
		ID: "fact-1", Content: "content", SourceDocument: "source", ChunkIndex: 3, Score: 0.25,
	}}}
	docs, err := NewMilvusPortAdapter(store).Search(context.Background(), "memory_facts_tenant", []float32{1}, 5, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].ID != "fact-1" || docs[0].Metadata["content"] != "content" ||
		docs[0].Metadata["source_document"] != "source" || docs[0].Metadata["chunk_index"] != int64(3) ||
		docs[0].Similarity != 0.25 {
		t.Fatalf("mapped docs = %#v", docs)
	}

	wantErr := errors.New("schema mismatch")
	store = &fakeMilvusStore{searchErr: wantErr}
	_, err = NewMilvusPortAdapter(store).Search(context.Background(), "memory_facts_tenant", []float32{1}, 5, filter)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestMilvusPortAdapterSearchMapsTypedUnavailableError(t *testing.T) {
	source := errors.New("grpc unavailable")
	store := &fakeMilvusStore{searchErr: &storagemilvus.UnavailableError{Op: "search", Err: source}}
	filter := memport.VectorSearchFilter{UserID: "user-1", IncludeUserScope: true}

	_, err := NewMilvusPortAdapter(store).Search(context.Background(), "memory_facts_tenant", []float32{1}, 5, filter)
	var unavailable *memport.VectorStoreUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, source) {
		t.Fatalf("error = %v, want port unavailable wrapping source", err)
	}
}

func validMemoryFactVectorDoc() *memport.VectorDoc {
	return &memport.VectorDoc{
		ID:        "fact-1",
		Embedding: []float32{1},
		Metadata: map[string]interface{}{
			"user_id":         "user-1",
			"agent_id":        "agent-1",
			"scope":           "user",
			"content":         "content",
			"conversation_id": "conversation-1",
			"importance":      0.5,
			"category":        "other",
			"confidence":      0.8,
			"source":          "llm_extraction",
		},
	}
}

// TestLegacyCollectionNamesPin 把 persistence 侧 legacy collection 名 pin 到
// 与 pipeline/vector_adapter_test.go（TestMemoryCollectionNames /
// TestMemoryFactsCollectionNames）完全相同的字面量：两处 helper 各自维护同名
// 实现（跨包禁止共享），本测试保证任一包漂移时立即断裂，且删除路径
// （legacyMemoryCollections）与查询回退路径（pipeline memory*LegacyName）
// 对同一租户产出字节级一致的 collection 名。
func TestLegacyCollectionNamesPin(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"facts dashed tenant", memoryFactsCollectionLegacyName("my-tenant-42"), "memory_facts_my_tenant_42"},
		{"raw dashed tenant", memoryCollectionLegacyName("my-tenant-42"), "memory_my_tenant_42"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q (与 pipeline 侧 pin 字面量必须一致)", tc.name, tc.got, tc.want)
		}
	}
}
