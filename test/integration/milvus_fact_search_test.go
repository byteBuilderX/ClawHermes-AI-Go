//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/persistence"
	storagemilvus "github.com/byteBuilderX/stratum/pkg/storage/milvus"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestMilvusFactSearchScopeIsolationAndMissingCollection(t *testing.T) {
	host := envOrDefault("TEST_MILVUS_HOST", "localhost")
	port := envOrDefault("TEST_MILVUS_PORT", "19530")
	store := storagemilvus.NewVectorStore(host, port, zap.NewNop())
	required := os.Getenv("REQUIRE_MILVUS_E2E") == "1"
	readinessTimeout := 5 * time.Second
	if required {
		readinessTimeout = 120 * time.Second
	}
	readinessCtx, readinessCancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer readinessCancel()
	if err := waitForMilvusReady(readinessCtx, required, time.Second, store.Connect, func(ctx context.Context) error {
		return probeMilvusCollectionLifecycle(ctx, store)
	}); err != nil {
		if required {
			t.Fatalf("Milvus unavailable while REQUIRE_MILVUS_E2E=1: %v", err)
		}
		t.Skipf("Milvus unavailable: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	adapter := persistence.NewMilvusPortAdapter(store)
	collection := "memory_facts_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_ = store.DeleteCollection(cleanupCtx, collection)
	})

	// Nonzero, angle-distinct fixtures: the store uses the COSINE metric, which
	// is scale-invariant (zero/near-zero vectors would be degenerate). Query is
	// [1,0]: user-scope is collinear (cos 1 -> similarity 1), current-agent is
	// orthogonal (cos 0 -> similarity 0.5).
	docs := []*memport.VectorDoc{
		factVectorDoc("other-user", "other-user", "", "user", []float32{0.5, 0.5}),
		factVectorDoc("user-scope", "user-1", "", "user", []float32{1, 0}),
		factVectorDoc("current-agent", "user-1", "agent-1", "agent", []float32{0, 1}),
		factVectorDoc("other-agent", "user-1", "agent-2", "agent", []float32{-1, 0}),
	}
	if err := adapter.Upsert(ctx, collection, docs); err != nil {
		t.Fatalf("upsert fixtures: %v", err)
	}
	if err := store.Flush(ctx, collection); err != nil {
		t.Fatalf("flush fixtures: %v", err)
	}

	filter := memport.VectorSearchFilter{UserID: "user-1", AgentID: "agent-1", IncludeUserScope: true, IncludeAgentScope: true}
	hits, err := adapter.Search(ctx, collection, []float32{1, 0}, 10, filter)
	if err != nil {
		t.Fatalf("search fixtures: %v", err)
	}
	if got := hitIDs(hits); fmt.Sprint(got) != fmt.Sprint([]string{"user-scope", "current-agent"}) {
		t.Fatalf("hit IDs = %v, want scope-safe ordered results", got)
	}
	if !(hits[0].Similarity > hits[1].Similarity) {
		t.Fatalf("similarities not ordered descending: %v, %v", hits[0].Similarity, hits[1].Similarity)
	}

	missing, err := adapter.Search(ctx, collection+"_missing", []float32{1, 0}, 10, filter)
	if err != nil {
		t.Fatalf("missing collection search: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing collection returned %d hits", len(missing))
	}
}

func probeMilvusCollectionLifecycle(ctx context.Context, store *storagemilvus.VectorStore) error {
	collection := "memory_facts_readiness_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = store.DeleteCollection(cleanupCtx, collection)
	}()

	if err := store.CreateCollectionWithDim(ctx, collection, 2); err != nil {
		return err
	}
	if err := store.Insert(ctx, collection, []storagemilvus.DocumentChunk{{
		ID: "readiness", UserID: "readiness-user", Scope: "user", Content: "readiness", Vector: []float32{1, 1},
	}}, ""); err != nil {
		return err
	}
	if err := store.Flush(ctx, collection); err != nil {
		return err
	}
	_, err := store.SearchWithFilter(ctx, collection, []float32{1, 1}, 1, `user_id == "readiness-user" && scope == "user"`)
	return err
}

func factVectorDoc(id, userID, agentID, scope string, vector []float32) *memport.VectorDoc {
	return &memport.VectorDoc{
		ID: id, Embedding: vector,
		Metadata: map[string]interface{}{
			"user_id": userID, "agent_id": agentID, "scope": scope, "content": id,
			"conversation_id": "conversation-1", "importance": 0.8, "category": "other",
			"confidence": 0.9, "source": "llm_extraction",
		},
	}
}

func hitIDs(docs []*memport.VectorDoc) []string {
	ids := make([]string, len(docs))
	for i, doc := range docs {
		ids[i] = doc.ID
	}
	return ids
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
