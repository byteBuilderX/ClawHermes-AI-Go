// Package wiring is the composition root: it constructs concrete
// dependencies once at startup and exposes them as a Container.
// Handlers depend on application services through the Container; they
// never reach into infrastructure directly.
package wiring

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/config"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	llmgateway "github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	mempipeline "github.com/byteBuilderX/stratum/internal/memory/infrastructure/pipeline"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/byteBuilderX/stratum/pkg/storage/milvus"
	pkgobjectstore "github.com/byteBuilderX/stratum/pkg/storage/objectstore"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	pkgredis "github.com/byteBuilderX/stratum/pkg/storage/redis"
	"github.com/minio/minio-go/v7"
)

// Container is the root holder for all wired dependencies. It is
// constructed once at startup by BuildContainer and torn down by
// Shutdown in reverse construction order.
type Container struct {
	Config *config.Config
	Logger *zap.Logger

	Storage              *Storage
	LLMGateway           *LLMGateway
	Platform             *Platform
	Parameters           *Parameters
	MCP                  *MCP
	Skill                *Skill
	Evaluation           *Evaluation
	Knowledge            *Knowledge
	Memory               *Memory
	IAM                  *IAM
	Agent                *Agent
	Workflow             *Workflow
	Scheduler            *Scheduler
	Collab               *Collab
	Audit                *Audit
	PublishGate          PublishGateFunc
	ReadinessCheck       func(context.Context) map[string]error
	RevisionObjectStore  pkgobjectstore.Store
	revisionObjectClient *minio.Client

	shutdown []func(context.Context) error
}

// buildStep names a wiring stage and its builder. The name is used in
// the wrapped error returned to BuildContainer's caller.
type buildStep struct {
	name string
	fn   func(context.Context) error
}

// BuildContainer wires all dependencies in dependency order. On any
// error after partial construction, it invokes Shutdown to release
// already-built resources before returning.
//
// Order: storage → llmgateway → platform → mcp → skill → knowledge →
// memory → iam → agent → workflow → scheduler. Shutdown reverses
// construction.
func BuildContainer(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*Container, error) {
	c := &Container{Config: cfg, Logger: logger}

	steps := []buildStep{
		{"storage", c.buildStorage},
		{"audit", c.buildAudit},
		{"llmgateway", c.buildLLMGateway},
		{"platform", c.buildPlatform},
		{"parameters", c.buildParameters},
		{"revision-object-store", c.buildRevisionObjectStore},
		{"mcp", c.buildMCP},
		{"skill", c.buildSkill},
		{"knowledge", c.buildKnowledge},
		{"memory", c.buildMemory},
		{"iam", c.buildIAM},
		{"agent", c.buildAgent},
		{"workflow", c.buildWorkflow},
		{"scheduler", c.buildScheduler},
		{"collab", c.buildCollab},
		{"evaluation", c.buildEvaluation},
		{"evaluation-center-namer", c.attachEvaluationCenterNamer},
		{"publish-gate", c.buildPublishGate},
		{"platform-verify-worker", c.buildPlatformVerifyWorker},
	}

	for _, step := range steps {
		if err := step.fn(ctx); err != nil {
			_ = c.Shutdown(ctx)
			return nil, fmt.Errorf("wiring.%s: %w", step.name, err)
		}
	}
	return c, nil
}

// platformMetrics returns the configured MetricsProvider or a safe
// no-op default when Platform has not been wired yet (e.g. in tests).
func (c *Container) platformMetrics() observability.MetricsProvider {
	if c.Platform != nil && c.Platform.Metrics != nil {
		return c.Platform.Metrics
	}
	return observability.NoopMetrics{}
}

// Shutdown invokes registered teardown hooks in reverse order. The
// first error encountered is returned; remaining hooks still run.
func (c *Container) Shutdown(ctx context.Context) error {
	var firstErr error
	for i := len(c.shutdown) - 1; i >= 0; i-- {
		if err := c.shutdown[i](ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// dbOrNil returns the underlying pgxpool.Pool if Storage and its PG
// pool are present, otherwise nil. Builders use this to degrade
// gracefully when running without a database (matches main.go/router.go
// nil-checks).
func (c *Container) dbOrNil() *pgxpool.Pool {
	if c.Storage == nil || c.Storage.PG == nil {
		return nil
	}
	return c.Storage.PG.DB()
}

// DB returns the underlying *pgxpool.Pool, or nil when no PostgreSQL
// pool is available. Exported counterpart to dbOrNil for use by api/http.
func (c *Container) DB() *pgxpool.Pool {
	return c.dbOrNil()
}

// NewFromExisting wires a Container around dependencies already created
// by cmd/server/main.go. It bypasses buildStorage / buildLLMGateway /
// buildSkill (those resources come from the caller) and runs only the
// derived sub-builders.
//
// This exists for transitional compatibility while Task 10c migrates
// main.go to BuildContainer. After that migration this function is
// deleted.
func NewFromExisting(
	ctx context.Context,
	cfg *config.Config,
	logger *zap.Logger,
	gateway *llmgateway.Gateway,
	db *pgxpool.Pool,
	rdb *goredis.Client,
	_ port.Adapter,
	memPipeline *mempipeline.Pipeline,
) (*Container, error) {
	c := &Container{Config: cfg, Logger: logger}
	if err := c.adoptExistingStorage(ctx, db, rdb); err != nil {
		return nil, err
	}
	if err := c.adoptExistingGateway(ctx, gateway, db); err != nil {
		_ = c.Shutdown(ctx)
		return nil, err
	}
	if err := c.buildExistingRuntime(ctx, memPipeline); err != nil {
		_ = c.Shutdown(ctx)
		return nil, err
	}
	return c, nil
}

func (c *Container) adoptExistingStorage(
	ctx context.Context, db *pgxpool.Pool, rdb *goredis.Client,
) error {
	storage := &Storage{}
	if db != nil {
		storage.PG = postgres.Wrap(db)
	}
	if rdb != nil {
		storage.Redis = pkgredis.Wrap(rdb)
	}
	mil := milvus.NewVectorStore(c.Config.MilvusHost, c.Config.MilvusPort, c.Logger)
	if err := mil.Connect(ctx); err != nil {
		c.Logger.Warn("failed to connect to Milvus", zap.Error(err))
	}
	storage.Milvus = mil
	c.Storage = storage
	c.shutdown = append(c.shutdown, func(_ context.Context) error { return mil.Close() })
	return nil
}

func (c *Container) adoptExistingGateway(
	ctx context.Context, gateway *llmgateway.Gateway, db *pgxpool.Pool,
) error {
	if db != nil {
		if err := c.buildLLMGateway(ctx); err != nil {
			return fmt.Errorf("wiring.llmgateway: %w", err)
		}
		return nil
	}
	if gateway != nil {
		metrics := observability.NewPrometheusMetrics(c.Logger)
		// cmd/server 装配路径：guest reaper 指标只在这里注册。
		metrics.RegisterReaperMetrics()
		c.LLMGateway = &LLMGateway{
			Gateway: gateway,
			Metrics: metrics,
		}
	}
	return nil
}

func (c *Container) buildExistingRuntime(ctx context.Context, memPipeline *mempipeline.Pipeline) error {
	if err := runBuildSteps(ctx, c.newFromExistingInitialSteps()); err != nil {
		return err
	}
	if err := c.buildSkill(ctx); err != nil {
		return fmt.Errorf("wiring.skill: %w", err)
	}
	if err := c.buildMemory(ctx); err != nil {
		return fmt.Errorf("wiring.memory: %w", err)
	}
	if memPipeline != nil {
		// Replace freshly-built pipeline with the caller's instance.
		c.Memory.Pipeline = memPipeline
		if c.Knowledge != nil && c.Knowledge.EmbedResolver != nil {
			memPipeline.SetEmbedResolver(c.Knowledge.EmbedResolver)
		}
	}
	return runBuildSteps(ctx, []buildStep{
		{"iam", c.buildIAM},
		{"agent", c.buildAgent},
		{"evaluation", c.buildEvaluation},
		{"evaluation-center-namer", c.attachEvaluationCenterNamer},
	})
}

func runBuildSteps(ctx context.Context, steps []buildStep) error {
	for _, step := range steps {
		if err := step.fn(ctx); err != nil {
			return fmt.Errorf("wiring.%s: %w", step.name, err)
		}
	}
	return nil
}

func (c *Container) buildAudit(ctx context.Context) error {
	db := c.dbOrNil()
	if db == nil {
		return nil
	}
	c.Audit = buildAudit(db)
	return nil
}

func (c *Container) newFromExistingInitialSteps() []buildStep {
	return []buildStep{
		{"audit", c.buildAudit},
		{"platform", c.buildPlatform},
		{"parameters", c.buildParameters},
		{"revision-object-store", c.buildRevisionObjectStore},
		{"mcp", c.buildMCP},
		{"knowledge", c.buildKnowledge},
	}
}
