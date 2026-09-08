package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/byteBuilderX/stratum/internal/memory/application"
	"github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/persistence"
	pgstorage "github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// resolvePostgresDSN picks the Postgres connection string in priority order:
//  1. TEST_POSTGRES_URL — explicit full override.
//  2. Standard libpq PG* vars (PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE) —
//     what GitHub Actions service containers export. Built into a keyword DSN so
//     the suite never silently depends on those vars matching a hardcoded default.
//  3. Local docker-compose default (stratum/stratum).
func resolvePostgresDSN() string {
	if dsn := os.Getenv("TEST_POSTGRES_URL"); dsn != "" {
		return dsn
	}
	if host := os.Getenv("PGHOST"); host != "" {
		envOr := func(key, def string) string {
			if v := os.Getenv(key); v != "" {
				return v
			}
			return def
		}
		return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
			host,
			envOr("PGPORT", "5432"),
			envOr("PGUSER", "postgres"),
			os.Getenv("PGPASSWORD"),
			envOr("PGDATABASE", "stratum_test"),
			envOr("PGSSLMODE", "disable"),
		)
	}
	return "postgres://stratum:stratum@localhost:5432/stratum_test?sslmode=disable"
}

// resolveRedisAddr honors TEST_REDIS_ADDR, then REDIS_ADDR (CI), then local default.
func resolveRedisAddr() string {
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		return addr
	}
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		return addr
	}
	return "localhost:6379"
}

// MemoryTestEnv holds resources for E2E memory tests.
type MemoryTestEnv struct {
	PGPool         *pgxpool.Pool
	Redis          *redis.Client
	MemoryService  *application.MemoryService
	FactRepo       port.FactRepo
	EntityRepo     port.EntityRepo
	Queue          port.ExtractionQueue
	SnapshotRepo   *persistence.ActiveSnapshotRepo
	HistoryRepo    *persistence.HistoryRepo
	TenantID       string
	SecondTenantID string
	UserID         string
	AgentID        string
}

func handleMemoryDependencyFailure(t *testing.T, dependency string, err error) {
	t.Helper()
	if os.Getenv("REQUIRE_MEMORY_E2E") == "1" {
		t.Fatalf("%s unavailable while REQUIRE_MEMORY_E2E=1: %v", dependency, err)
	}
	t.Skipf("%s unavailable: %v", dependency, err)
}

// SetupMemoryTestEnv creates isolated tenant schema + mocked dependencies.
func SetupMemoryTestEnv(t *testing.T) *MemoryTestEnv {
	t.Helper()

	ctx := context.Background()

	// Step 1: Connect to PostgreSQL.
	// Direct-connect to Postgres 5432 (NOT pgbouncer 6432): schema/extension
	// provisioning needs session-level privileges that transaction pooling breaks.
	// DSN resolution honors TEST_POSTGRES_URL, then CI's PG* vars, then the local
	// docker-compose default — see resolvePostgresDSN. When REQUIRE_MEMORY_E2E is
	// set, an unreachable DB fails hard instead of silently skipping.
	dsn := resolvePostgresDSN()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		handleMemoryDependencyFailure(t, "PostgreSQL", err)
	}
	if pingErr := pool.Ping(ctx); pingErr != nil {
		pool.Close()
		handleMemoryDependencyFailure(t, "PostgreSQL", pingErr)
	}

	// Step 2: Generate unique tenant ID for isolation
	tenantID := uuid.NewString()
	secondTenantID := uuid.NewString()
	schemaName := fmt.Sprintf("tenant_%s", tenantID)
	secondSchemaName := fmt.Sprintf("tenant_%s", secondTenantID)

	// Step 3: Provision public schema (gen_uuid_v7, pg_trgm, public.tenants, ...).
	// Idempotent — safe to call per-test; no-op if already applied to this DB.
	if err := pgstorage.ProvisionPublicSchema(ctx, pool, zap.NewNop()); err != nil {
		pool.Close()
		t.Fatalf("provision public schema: %v", err)
	}

	// Step 4: Provision the full tenant schema via the canonical DDL
	// (pkg/storage/postgres/tenant_schema.sql is the single source of truth;
	// production startup and tests apply the same file via this call).
	if err := pgstorage.ProvisionTenantSchema(ctx, pool, tenantID); err != nil {
		pool.Close()
		t.Fatalf("provision tenant schema: %v", err)
	}
	if err := pgstorage.ProvisionTenantSchema(ctx, pool, secondTenantID); err != nil {
		pool.Close()
		t.Fatalf("provision second tenant schema: %v", err)
	}

	// Step 5: Connect to Redis. Honors TEST_REDIS_ADDR, then REDIS_ADDR (CI).
	redisAddr := resolveRedisAddr()
	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
		DB:   0,
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		pool.Close()
		handleMemoryDependencyFailure(t, "Redis", err)
	}

	// Step 6: Build MemoryService with mocked LLM/Vector/Embed
	memoryService, factRepo, entityRepo, queue := newMemoryService(pool, redisClient)

	env := &MemoryTestEnv{
		PGPool:         pool,
		Redis:          redisClient,
		MemoryService:  memoryService,
		FactRepo:       factRepo,
		EntityRepo:     entityRepo,
		Queue:          queue,
		SnapshotRepo:   persistence.NewActiveSnapshotRepo(pool),
		HistoryRepo:    persistence.NewHistoryRepo(pool),
		TenantID:       tenantID,
		SecondTenantID: secondTenantID,
		UserID:         "test-user-001",
		AgentID:        "test-agent-001",
	}

	// Step 7: Register cleanup
	t.Cleanup(func() {
		ctx := context.Background()
		// Drop schema CASCADE removes all tables
		_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schemaName))
		_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, secondSchemaName))
		// Clear Redis buffer keys
		_ = redisClient.FlushDB(ctx).Err()
		pool.Close()
		redisClient.Close()
	})

	return env
}

// stubExtractionQueue 是 E2E 用的 Enqueue-only 队列替身：NATS 传输链路由
// pipeline 集成测试覆盖，E2E 聚焦事实提取语义本身。
type stubExtractionQueue struct{}

func (stubExtractionQueue) Enqueue(context.Context, string, *port.ExtractionTask) error { return nil }

// newMemoryService constructs MemoryService with mocked LLM extractor.
func newMemoryService(pool *pgxpool.Pool, redis *redis.Client) (*application.MemoryService, port.FactRepo, port.EntityRepo, port.ExtractionQueue) {
	factRepo := persistence.NewFactRepo(pool)
	entityRepo := persistence.NewEntityRepo(pool)
	queue := stubExtractionQueue{}
	messageBufferStore := persistence.NewRedisMessageBufferStore(redis)

	service := application.NewMemoryService(
		factRepo,
		entityRepo,
		queue,
		&mockVectorStore{},
		&mockLLMExtractor{},
		&mockEmbedClient{},
		messageBufferStore,
		nil,
	)

	return service, factRepo, entityRepo, queue
}

// mockLLMExtractor returns deterministic facts for testing.
type mockLLMExtractor struct{}

func (m *mockLLMExtractor) ExtractFacts(ctx context.Context, userID, agentID, message string) ([]*port.ExtractedFact, error) {
	// Deterministic extraction: if message contains "dark mode", extract preference fact
	if len(message) > 0 {
		return []*port.ExtractedFact{
			{
				Content:    "User prefers dark mode",
				Importance: 0.8,
				Entities:   []string{"dark mode"},
			},
		}, nil
	}
	return nil, nil
}

// mockEmbedClient returns deterministic embeddings.
type mockEmbedClient struct{}

func (m *mockEmbedClient) Embed(ctx context.Context, text string) ([]float32, error) {
	// Return fixed 1024-dim vector
	vec := make([]float32, 1024)
	for i := range vec {
		vec[i] = 0.1
	}
	return vec, nil
}

func (m *mockEmbedClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i], _ = m.Embed(ctx, texts[i])
	}
	return result, nil
}

func (m *mockEmbedClient) Model() string { return "text-embedding-v3" }

// mockVectorStore stores vectors in memory for testing.
type mockVectorStore struct {
	docs map[string]*port.VectorDoc // collectionName_docID -> doc
}

func (m *mockVectorStore) Upsert(ctx context.Context, collectionName string, docs []*port.VectorDoc) error {
	if m.docs == nil {
		m.docs = make(map[string]*port.VectorDoc)
	}
	for _, doc := range docs {
		key := fmt.Sprintf("%s_%s", collectionName, doc.ID)
		m.docs[key] = doc
	}
	return nil
}

func (m *mockVectorStore) Search(ctx context.Context, collectionName string, queryVector []float32, topK int, filter port.VectorSearchFilter) ([]*port.VectorDoc, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	if m.docs == nil {
		return nil, nil
	}

	var hits []*port.VectorDoc
	for key, doc := range m.docs {
		if len(key) > len(collectionName) && key[:len(collectionName)] == collectionName {
			// Simple filter match (user_id)
			if docUserID, exists := doc.Metadata["user_id"].(string); exists && docUserID != filter.UserID {
				continue
			}
			scope, _ := doc.Metadata["scope"].(string)
			switch scope {
			case "user":
				if !filter.IncludeUserScope {
					continue
				}
			case "agent":
				docAgentID, _ := doc.Metadata["agent_id"].(string)
				if !filter.IncludeAgentScope || docAgentID != filter.AgentID {
					continue
				}
			default:
				continue
			}
			// Return with fixed similarity score
			docCopy := *doc
			docCopy.Similarity = 0.05
			hits = append(hits, &docCopy)
		}
	}

	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

func (m *mockVectorStore) Delete(ctx context.Context, collectionName string, ids []string) error {
	if m.docs == nil {
		return nil
	}
	for _, id := range ids {
		key := fmt.Sprintf("%s_%s", collectionName, id)
		delete(m.docs, key)
	}
	return nil
}

func (m *mockVectorStore) DeleteAllByUser(_ context.Context, _, _ string) error { return nil }

func (m *mockVectorStore) DeleteAllByAgent(_ context.Context, _, _ string) error { return nil }

func (m *mockVectorStore) DeleteEntryVectors(_ context.Context, _ string, _ []string) error {
	return nil
}

func (m *mockVectorStore) DeleteFactVectors(_ context.Context, _ string, _ []string) error {
	return nil
}

func (m *mockVectorStore) CreateCollection(ctx context.Context, collectionName string, dimension int) error {
	return nil // no-op for in-memory mock
}
