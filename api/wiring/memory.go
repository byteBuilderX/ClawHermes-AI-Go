package wiring

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/config"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	llmgateway "github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	memory "github.com/byteBuilderX/stratum/internal/memory/application"
	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/persistence"
	pipeline "github.com/byteBuilderX/stratum/internal/memory/infrastructure/pipeline"
	memworkers "github.com/byteBuilderX/stratum/internal/memory/infrastructure/workers"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"

	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
)

// Memory groups memory-system services: the user-facing manager, the
// per-tenant memory injector consumed by agents, and the async write
// pipeline (JetStream-backed) that embeds and persists memories.
//
// Pipeline is nil when MEMORY_PIPELINE_ENABLED is false or NATS is not
// reachable; downstream consumers must nil-check before use. DLQReplay
// follows the same availability (depends on the shared NATS connection),
// and needs no shutdown code (no goroutines).
type Memory struct {
	Manager   *memory.MemoryManager
	Service   *memory.MemoryService
	Injector  port.MemoryInjector
	Pipeline  *pipeline.Pipeline
	DLQReplay *pipeline.ReplayService
	RecallFn  port.RecallMemoryFn
	// ExtractionQueue 是 Redis buffer flush 后的 NATS 提取任务发布器；
	// pipeline 未启用时为 nil（buffer flush 显式降级）。
	ExtractionQueue memport.ExtractionQueue
	// ReflectionPublisher 是任务结束轨迹反思的 NATS 发布器；pipeline 未启用
	// 时为 nil（agent 主路径 fail-open 降级）。
	ReflectionPublisher *pipeline.NATSReflectionPublisher
}

type memoryGatewayCompleter interface {
	Complete(context.Context, *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error)
}

// memoryLLMAdapter 把 memory pipeline 的 LLM 请求适配到 llmgateway。
// tenantID 由构造方闭包捕获（pipeline worker 的 ctx 不含请求租户），
// Complete 时注入 reqctx——Gateway 内部从 ctx 取租户做模型解析（gateway.go
// 的 TenantIDFromContext），不注入则 resolve 报 tenant_id is empty。
type memoryLLMAdapter struct {
	client   memoryGatewayCompleter
	tenantID string
}

func (a memoryLLMAdapter) Complete(ctx context.Context, req *llmdomain.CompletionRequest) (*llmdomain.CompletionResponse, error) {
	if a.client == nil {
		return nil, fmt.Errorf("memory llm adapter: client is nil")
	}
	if req == nil {
		return nil, fmt.Errorf("memory llm adapter: request is nil")
	}
	if a.tenantID != "" {
		ctx = reqctx.WithTenantID(ctx, a.tenantID)
	}
	resp, err := a.client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("memory llm adapter: provider returned nil response")
	}
	return resp, nil
}

// platformParamResolver adapts the parameters resolver to the memory domain's
// platform-scope port for cross-agent background workers (thin ACL; wiring is
// the only allowed adapter seam). declared=nil yields the pure platform value
// or the definition default — exactly the ScopePlatform consumption contract.
type platformParamResolver struct {
	svc *parametersapp.Service
}

func (r platformParamResolver) ResolvePlatform(ctx context.Context, key string) (any, bool, error) {
	if r.svc == nil {
		return nil, false, nil
	}
	return r.svc.Resolver().Resolve(ctx, key, nil)
}

// memoryPlatformParamResolver builds the cross-agent platform resolver, or nil
// when the parameters registry is unavailable (degrade). A nil resolver keeps
// workers on their const defaults, matching pre-config behaviour.
func (c *Container) memoryPlatformParamResolver() memport.PlatformParamResolver {
	if c.Parameters == nil {
		return nil
	}
	return platformParamResolver{svc: c.Parameters.Service}
}

func (c *Container) buildMemory(ctx context.Context) error {
	memRepo := persistence.NewMemoryRepo(c.dbOrNil())
	mem := &Memory{
		Manager: memory.NewMemoryManager(c.Logger, memRepo),
	}

	db := c.dbOrNil()
	c.buildMemoryService(mem, db, memRepo)
	c.buildMemoryInjector(mem, db)
	c.buildMemoryRecall(mem, db)

	c.Memory = mem
	return c.buildMemoryPipeline(mem, db)
}

func (c *Container) buildMemoryService(mem *Memory, db *pgxpool.Pool, memRepo memport.MemoryRepo) {
	if db == nil {
		return
	}
	factRepo := persistence.NewFactRepo(db)
	entityRepo := persistence.NewEntityRepo(db)

	var messageBufferStore memport.MessageBufferStore
	if c.Storage != nil && c.Storage.Redis != nil {
		messageBufferStore = persistence.NewRedisMessageBufferStore(c.Storage.Redis.Client())
	}

	// 提取任务队列由 buildMemoryPipeline 在 NATS 就绪后通过
	// SetExtractionQueue 注入；此处传 nil，flush 路径显式降级。
	mem.Service = memory.NewMemoryService(factRepo, entityRepo, nil, nil, nil, nil, messageBufferStore, c.Logger)
	mem.Service.SetMemoryRepo(memRepo)
	mem.Service.SetHistoryRepo(persistence.NewHistoryRepo(db))
	mem.Service.SetActiveSnapshotRepo(persistence.NewActiveSnapshotRepo(db))

	if c.LLMGateway != nil && c.LLMGateway.Registry != nil {
		llmRes := newTenantCapabilityResolver(
			c.LLMGateway.Registry, c.LLMGateway.Gateway, c.Logger,
		).(*tenantCapabilityResolver)
		mem.Service.SetLLMExtractResolver(makeLLMExtractResolver(llmRes, c.memoryPlatformParamResolver(), c.Logger))
		mem.Service.SetLLMSupersederResolver(makeLLMSupersederResolver(llmRes, c.memoryPlatformParamResolver(), c.Logger))
		mem.Service.SetTrajectoryReflectorResolver(makeTrajectoryReflectorResolver(llmRes, c.memoryPlatformParamResolver(), c.Logger))
	}
	if c.Knowledge != nil && c.Knowledge.EmbedResolver != nil {
		mem.Service.SetEmbedClientResolver(makeEmbedClientResolver(c.Knowledge.EmbedResolver))
	}
}

func (c *Container) buildMemoryInjector(mem *Memory, db *pgxpool.Pool) {
	if db == nil {
		return
	}
	vectorStore := c.Storage.Milvus
	inj := pipeline.NewMemoryInjector(db, c.Logger, nil, vectorStore)
	if c.Knowledge != nil && c.Knowledge.EmbedResolver != nil {
		inj.SetEmbedResolver(c.Knowledge.EmbedResolver)
	}
	inj.SetPlatformParamResolver(c.memoryPlatformParamResolver())
	mem.Injector = injectorAdapter{inj: inj}
}

func (c *Container) buildMemoryRecall(mem *Memory, db *pgxpool.Pool) {
	if db == nil || c.Storage == nil || c.Storage.Milvus == nil {
		return
	}
	var embedResolver pipeline.EmbedServiceResolver
	if c.Knowledge != nil && c.Knowledge.EmbedResolver != nil {
		embedResolver = c.Knowledge.EmbedResolver
	}
	recallHandler := pipeline.NewRecallHandler(db, c.Logger, nil, embedResolver, c.Storage.Milvus)
	if c.LLMGateway != nil && c.LLMGateway.Metrics != nil {
		recallHandler.WithMetrics(c.LLMGateway.Metrics)
	}
	recallHandler.SetPlatformParamResolver(c.memoryPlatformParamResolver())
	mem.RecallFn = func(ctx context.Context, tenantID, userID, agentID, scope string, input map[string]any) (string, error) {
		return recallHandler.Handle(ctx, tenantID, userID, agentID, scope, input)
	}

	if mem.Service != nil {
		mem.Service.SetVectorStore(persistence.NewMilvusPortAdapter(c.Storage.Milvus))
	}
}

func (c *Container) buildMemoryPipeline(mem *Memory, db *pgxpool.Pool) error {
	mp := c.Config.MemoryPipeline
	pipelineCfg := pipeline.Config{
		Enabled:       mp.Enabled,
		PollInterval:  mp.PollInterval,
		BatchSize:     mp.BatchSize,
		EmbedWorkers:  mp.EmbedWorkers,
		EnrichWorkers: mp.EnrichWorkers,
		EmbedAckWait:  mp.EmbedAckWait,
		EnrichAckWait: mp.EnrichAckWait,
		MaxDeliver:    mp.MaxDeliver,
	}

	if !pipelineCfg.Enabled || db == nil || c.Storage == nil || c.Storage.Milvus == nil {
		return nil
	}

	// 复用平台共享 NATS 连接（pkg/messaging/nats.Connect 创建，
	// MaxReconnects(-1)）；连接生命周期归 wiring，pipeline 只使用不关闭。
	if c.Storage.NATS == nil {
		c.Logger.Warn("memory-pipeline: NATS unavailable, pipeline disabled")
		return nil
	}

	replaySvc, err := pipeline.NewReplayService(c.Storage.NATS, c.Logger)
	if err != nil {
		return fmt.Errorf("memory dlq replay service: %w", err)
	}
	mem.DLQReplay = replaySvc

	dimResolver := pipeline.DimResolver(func(ctx context.Context, tenantID string) int {
		return c.resolveEmbeddingDim(ctx, tenantID)
	})

	vectorAdapter := pipeline.NewMilvusVectorAdapter(c.Storage.Milvus).WithDimResolver(dimResolver)
	p := pipeline.New(pipelineCfg, db, c.Storage.NATS, vectorAdapter, c.Logger)
	// 孤儿向量清理：embedder 写向量后 enrich 阶段永久失败时按 message_id 删除，
	// 避免无 memory_entries 行的向量被召回。
	p.SetEntryVectorDeleter(persistence.NewMilvusPortAdapter(c.Storage.Milvus))
	c.attachPipelineDynamic(p)
	if c.LLMGateway != nil && c.LLMGateway.Metrics != nil {
		pipeline.RegisterMetrics(c.LLMGateway.Metrics.Registerer())
		// modelconfig 指标进程级同一 registerer 幂等注册；pipeline 与 workers 两
		// 装配点各调一次也不重复收集（见 RegisterMetrics 吞 AlreadyRegistered）。
		modelconfig.RegisterMetrics(c.LLMGateway.Metrics.Registerer())
	}
	if c.Knowledge != nil && c.Knowledge.EmbedResolver != nil {
		p.SetEmbedResolver(c.Knowledge.EmbedResolver)
	}
	if c.LLMGateway != nil && c.LLMGateway.Registry != nil {
		llmRes := newTenantCapabilityResolver(
			c.LLMGateway.Registry, c.LLMGateway.Gateway, c.Logger,
		).(*tenantCapabilityResolver)
		p.SetLLMResolver(func(ctx context.Context, tenantID string) pipeline.LLMClient {
			gw := llmRes.ResolveLLM(ctx, tenantID)
			if gw == nil {
				return nil
			}
			return memoryLLMAdapter{client: gw, tenantID: tenantID}
		})
	}
	p.SetPlatformParamResolver(c.memoryPlatformParamResolver())

	return c.finalizeMemoryPipeline(mem, p, c.Storage.NATS)
}

// attachMemoryTaskPublishers 装配提取/反思 NATS 任务发布器与 pipeline 消费
// service：Redis 只负责攒批，NATS JetStream 为任务传输层，PG 不再作为
// 消息队列。
func (c *Container) attachMemoryTaskPublishers(mem *Memory, p *pipeline.Pipeline, nc *nats.Conn) error {
	jsm, err := pipeline.NewJetStreamManager(nc, c.Logger)
	if err != nil {
		return fmt.Errorf("memory jetstream manager: %w", err)
	}
	js := jsm.JS()
	mem.ExtractionQueue = pipeline.NewNATSExtractionPublisher(js, c.Logger)
	mem.ReflectionPublisher = pipeline.NewNATSReflectionPublisher(js, c.Logger)
	if mem.Service != nil {
		mem.Service.SetExtractionQueue(mem.ExtractionQueue)
	}
	p.SetExtractionService(mem.Service)
	p.SetReflectionService(mem.Service)
	return nil
}

// finalizeMemoryPipeline 装配任务发布器后挂载 pipeline 到 container。
// 独立成函数保持 buildMemoryPipeline 复杂度在基线内。
func (c *Container) finalizeMemoryPipeline(mem *Memory, p *pipeline.Pipeline, nc *nats.Conn) error {
	if err := c.attachMemoryTaskPublishers(mem, p, nc); err != nil {
		return err
	}
	mem.Pipeline = p
	return nil
}

// resolveEmbeddingDim 按租户显式配置的记忆嵌入模型查维度表；未配置或解析失败
// 返回 0 → MilvusVectorAdapter 跳过建 collection（fail-closed），不再回退
// 1536 全局兜底。独立成方法以保持 buildMemoryPipeline 复杂度在基线内。
func (c *Container) resolveEmbeddingDim(ctx context.Context, tenantID string) int {
	if c.LLMGateway == nil || c.LLMGateway.TenantEmbeddingResolver == nil {
		return 0
	}
	model, err := c.LLMGateway.TenantEmbeddingResolver.ResolveMemoryEmbeddingModel(ctx, tenantID)
	if err != nil || model == "" {
		return 0
	}
	return constants.DimensionForModel(model)
}

// attachPipelineDynamic 桥接热更新管道：config 层动态配置 → atomic 指针 →
// poller 每轮 re-read。dynamic 逃逸到堆，生命周期与 Container 一致；
// 若从未 Store 过，poller 回退静态值——与现状一致。
// 独立成函数以保持 buildMemoryPipeline 复杂度在基线内。
func (c *Container) attachPipelineDynamic(p *pipeline.Pipeline) {
	var dynamic atomic.Pointer[pipeline.DynamicConfig]
	if d := c.Config.LoadMemoryPipelineDynamic(); d.PollInterval > 0 || d.BatchSize > 0 {
		dynamic.Store(&pipeline.DynamicConfig{PollInterval: d.PollInterval, BatchSize: d.BatchSize})
	}
	c.Config.OnMemoryPipelineDynamic(func(d config.MemoryPipelineDynamic) {
		dynamic.Store(&pipeline.DynamicConfig{PollInterval: d.PollInterval, BatchSize: d.BatchSize})
	})
	p.WithDynamic(&dynamic)
}

func makeLLMExtractResolver(llmRes *tenantCapabilityResolver, resolver memport.PlatformParamResolver, logger *zap.Logger) func(context.Context, string) memport.LLMExtractor {
	return func(ctx context.Context, tenantID string) memport.LLMExtractor {
		llm := llmRes.ResolveLLM(ctx, tenantID)
		if llm == nil {
			return nil
		}
		extractor := pipeline.NewLLMExtractor(memoryLLMAdapter{client: llm, tenantID: tenantID}).WithLogger(logger)
		extractor.SetTenantID(tenantID)
		extractor.SetPlatformParamResolver(resolver)
		return extractor
	}
}

// trajectoryReflectionClosure 把 agent 任务结束钩子适配到 memory 反思链路：
// VO 转骨架输入 → 确定性压缩 → NATS 入队。原始 tool steps 不进入链路，
// 失败由调用方按 fail-open 显式降级。
func trajectoryReflectionClosure(c *Container) port.EnqueueTrajectoryReflectionFn {
	return func(
		ctx context.Context,
		tenantID, userID, agentID, conversationID, scope, executionID, taskGoal, resultSummary, terminatedBy string,
		calls []port.TrajectoryToolCallVO,
		explicitMemory bool,
	) error {
		if c.Memory == nil || c.Memory.ReflectionPublisher == nil {
			return fmt.Errorf("trajectory reflection: NATS publisher not configured")
		}
		inputs := make([]memory.ToolCallInput, 0, len(calls))
		for _, tc := range calls {
			inputs = append(inputs, memory.ToolCallInput{
				ToolName:    tc.ToolName,
				ArgsSummary: tc.ArgsSummary,
				Status:      tc.Status,
				ErrorMsg:    tc.ErrorMsg,
				DurationMS:  tc.DurationMS,
			})
		}
		skeleton, err := memory.BuildTrajectorySkeleton(executionID, taskGoal, resultSummary, terminatedBy, inputs)
		if err != nil {
			return fmt.Errorf("trajectory reflection: build skeleton: %w", err)
		}
		raw, err := json.Marshal(skeleton)
		if err != nil {
			return fmt.Errorf("trajectory reflection: marshal skeleton: %w", err)
		}
		return c.Memory.ReflectionPublisher.Enqueue(ctx, &memport.ReflectionTask{
			TenantID:       tenantID,
			UserID:         userID,
			AgentID:        agentID,
			ConversationID: conversationID,
			Scope:          scope,
			ExecutionID:    executionID,
			Skeleton:       raw,
			ExplicitMemory: explicitMemory,
		})
	}
}

// makeTrajectoryReflectorResolver 构建按租户解析的轨迹反思器：模型走租户
// LLM gateway，提示词/模型参数走平台级 resolver（不与 agent 绑定）。
func makeTrajectoryReflectorResolver(
	llmRes *tenantCapabilityResolver,
	paramResolver memport.PlatformParamResolver,
	logger *zap.Logger,
) memory.TrajectoryReflectorResolver {
	return func(ctx context.Context, tenantID string) memport.TrajectoryReflector {
		llm := llmRes.ResolveLLM(ctx, tenantID)
		if llm == nil {
			return nil
		}
		reflector := pipeline.NewTrajectoryReflector(memoryLLMAdapter{client: llm, tenantID: tenantID}).WithLogger(logger)
		reflector.SetTenantID(tenantID)
		reflector.SetParamResolver(paramResolver)
		return reflector
	}
}

func makeLLMSupersederResolver(llmRes *tenantCapabilityResolver, paramResolver memport.PlatformParamResolver, logger *zap.Logger) func(context.Context, string) memport.LLMSuperseder {
	return func(ctx context.Context, tenantID string) memport.LLMSuperseder {
		llm := llmRes.ResolveLLM(ctx, tenantID)
		if llm == nil {
			return nil
		}
		s := memworkers.NewLLMSuperseder(memoryLLMAdapter{client: llm, tenantID: tenantID}).WithLogger(logger)
		if paramResolver != nil {
			s = s.WithParamResolver(paramResolver)
		}
		return s
	}
}

func makeEmbedClientResolver(embedRes pipeline.EmbedServiceResolver) func(context.Context, string) memport.EmbedClient {
	return func(ctx context.Context, tenantID string) memport.EmbedClient {
		ec := embedRes(ctx, tenantID)
		if ec == nil {
			return nil
		}
		return pipeline.NewEmbedClientAdapter(ec)
	}
}

// injectorAdapter adapts *pipeline.MemoryInjector to port.MemoryInjector.
// Pipeline keeps its own InjectionContext VO; this thin shim copies fields
// so the application layer (port) stays free of pipeline imports.
type injectorAdapter struct{ inj *pipeline.MemoryInjector }

func (a injectorAdapter) BuildContext(ctx context.Context, ic port.InjectionContext) (string, error) {
	return a.inj.BuildContext(ctx, pipeline.InjectionContext{
		TenantID:       ic.TenantID,
		UserID:         ic.UserID,
		AgentID:        ic.AgentID,
		ConversationID: ic.ConversationID,
		Query:          ic.Query,
		Scope:          ic.Scope,
	})
}

// modelConfigEnabledCatalog 把 *llmgateway.ModelRegistry 的 enabled 名单方法适配
// 到 modelconfig.EnabledCatalog。方法名不同、语义一致（全局 enabled chat/embed
// 模型名）；探针用它做目录比对，不构造 client。
type modelConfigEnabledCatalog struct {
	registry *llmgateway.ModelRegistry
}

func (a modelConfigEnabledCatalog) ChatEnabled(ctx context.Context) ([]string, error) {
	return a.registry.ListChatModelsByTenant(ctx)
}

func (a modelConfigEnabledCatalog) EmbedEnabled(ctx context.Context) ([]string, error) {
	return a.registry.ListEmbeddingModelsByTenant(ctx)
}

// memoryModelConfigRequirements 返回当前装配下「必需」的记忆模型平台参数清单
// （探针完备性检查口径）。组件未装配的 key 不列入，避免误报：
//
//   - LLMGateway 目录或平台参数服务缺失 → nil（无从判定，探针空转）；
//   - pipeline 在场（c.Memory.Pipeline 非空）→ extraction/reflection/enrich/
//     summary/embedding 必需——这些消费点只随 pipeline 装配；
//   - Registry 在场 → supersede/history_summary 必需——llmRes 装配即会为租户
//     起对应周期 worker（memoryModelConfigProbe 只在 BuildMemoryWorkers 挂载，
//     该入口已保证 db/Service 在场）。
func (c *Container) memoryModelConfigRequirements() []modelconfig.Requirement {
	if c.LLMGateway == nil || c.LLMGateway.Registry == nil {
		return nil
	}
	if c.Parameters == nil || c.Parameters.Service == nil {
		return nil
	}
	var reqs []modelconfig.Requirement
	if c.Memory != nil && c.Memory.Pipeline != nil {
		reqs = append(reqs,
			modelconfig.Requirement{Key: modelconfig.KeyExtractionModel, Kind: modelconfig.KindChat},
			modelconfig.Requirement{Key: modelconfig.KeyReflectionModel, Kind: modelconfig.KindChat},
			modelconfig.Requirement{Key: modelconfig.KeyEnrichModel, Kind: modelconfig.KindChat},
			modelconfig.Requirement{Key: modelconfig.KeySummaryModel, Kind: modelconfig.KindChat},
			modelconfig.Requirement{Key: modelconfig.KeyEmbeddingModel, Kind: modelconfig.KindEmbed},
		)
	}
	reqs = append(reqs,
		modelconfig.Requirement{Key: modelconfig.KeySupersedeModel, Kind: modelconfig.KindChat},
		modelconfig.Requirement{Key: modelconfig.KeyHistorySummaryModel, Kind: modelconfig.KindChat},
	)
	return reqs
}

// memoryModelConfigProbe 构造记忆模型参数完备性探针；目录/参数服务/必需集任一
// 不满足返回 nil，由 BuildMemoryWorkers 决定是否挂载（与周期 worker 同生命周期）。
func (c *Container) memoryModelConfigProbe() *modelconfig.Probe {
	reqs := c.memoryModelConfigRequirements()
	if len(reqs) == 0 {
		return nil
	}
	return modelconfig.NewProbe(
		c.Parameters.Service,
		modelConfigEnabledCatalog{registry: c.LLMGateway.Registry},
		reqs,
		c.Logger,
	)
}

// memoryWorker 是记忆后台 worker 的生命周期接口。
type memoryWorker interface {
	Start(context.Context)
	Stop()
}

// BuildMemoryWorkers constructs memory background workers.
// TenantWatcher replaces the static per-tenant startup loop — new tenants are
// automatically picked up on the next 60s reconcile tick.
// BufferScanner is global (Redis key names encode tenantID).
func BuildMemoryWorkers(c *Container) []memoryWorker {
	if c.Memory == nil || c.Memory.Service == nil {
		return nil
	}
	db := c.dbOrNil()
	if db == nil {
		return nil
	}

	factRepo := persistence.NewFactRepo(db)
	historyRepo := persistence.NewHistoryRepo(db)
	memoryRepo := persistence.NewMemoryRepo(db)
	vectorStore := buildMemoryWorkerVectorStore(c)

	if c.LLMGateway != nil && c.LLMGateway.Metrics != nil {
		memworkers.RegisterMetrics(c.LLMGateway.Metrics.Registerer())
		// 与 pipeline 装配点同 registerer 幂等注册；pipeline 禁用而 workers 在场时
		// 也保证 modelconfig 指标可见。
		modelconfig.RegisterMetrics(c.LLMGateway.Metrics.Registerer())
	}

	var llmRes *tenantCapabilityResolver
	if c.LLMGateway != nil && c.LLMGateway.Registry != nil {
		llmRes = newTenantCapabilityResolver(
			c.LLMGateway.Registry, c.LLMGateway.Gateway, c.Logger,
		).(*tenantCapabilityResolver)
	}

	watcher := memworkers.NewTenantWatcher(db, func(tid string) memworkers.WorkerSet {
		ws := memworkers.WorkerSet{
			memworkers.NewGCWorker(tid, factRepo, c.Logger).
				WithMemoryRepo(memoryRepo).WithVectorStore(vectorStore),
		}
		return appendTenantLLMWorkers(ws, tid, factRepo, historyRepo, vectorStore,
			buildWorkerLLMResolver(llmRes), c.memoryPlatformParamResolver(), c.Logger)
	}, c.Logger)

	result := []memoryWorker{watcher}

	if w := c.bufferScannerWorker(); w != nil {
		result = append(result, w)
	}

	// 记忆模型参数完备性探针：与流量无关，周期比对平台参数 × enabled 目录，
	// 随 memory workers 同起同停。装配不满足时返回 nil（不误报）。
	if probe := c.memoryModelConfigProbe(); probe != nil {
		result = append(result, probe)
	}

	return result
}

// bufferScannerWorker 构建 Redis buffer 扫描 worker；pipeline 未启用（NATS
// 发布器缺失）时返回 nil，buffer flush 在调用方显式降级。
func (c *Container) bufferScannerWorker() memoryWorker {
	if c.Storage == nil || c.Storage.Redis == nil || c.Memory == nil || c.Memory.ExtractionQueue == nil {
		return nil
	}
	store := persistence.NewRedisMessageBufferStore(c.Storage.Redis.Client())
	scanner := memory.NewBufferScanner(store, c.Memory.ExtractionQueue, c.Logger)
	scanner.SetMetrics(c.Platform.Metrics)
	return scanner
}

// buildMemoryWorkerVectorStore 返回 memory worker 共用的 Milvus adapter；
// Milvus 未装配时返回 nil，对应清理路径安全跳过。
func buildMemoryWorkerVectorStore(c *Container) memport.VectorStore {
	if c.Storage != nil && c.Storage.Milvus != nil {
		return persistence.NewMilvusPortAdapter(c.Storage.Milvus)
	}
	return nil
}

func buildWorkerLLMResolver(llmRes *tenantCapabilityResolver) memworkers.TenantLLMResolver {
	if llmRes == nil {
		return nil
	}
	return func(ctx context.Context, tenantID string) (memworkers.TenantLLMClient, error) {
		llm, err := llmRes.ResolveWorkerLLM(ctx, tenantID)
		if err != nil || llm == nil {
			return nil, err
		}
		return memoryLLMAdapter{client: llm, tenantID: tenantID}, nil
	}
}

func appendTenantLLMWorkers(
	workerSet memworkers.WorkerSet,
	tenantID string,
	factRepo memport.FactRepo,
	historyRepo memport.HistoryRepo,
	vectorStore memport.VectorStore,
	resolver memworkers.TenantLLMResolver,
	paramResolver memport.PlatformParamResolver,
	logger *zap.Logger,
) memworkers.WorkerSet {
	var summarizer memworkers.HistorySummarizer
	var compressor memworkers.HistoryCompressor
	if resolver != nil {
		superseder := memworkers.NewResolvingLLMSuperseder(tenantID, resolver).WithLogger(logger)
		if paramResolver != nil {
			superseder = superseder.WithParamResolver(paramResolver)
		}
		workerSet = append(workerSet, memworkers.NewSupersedeWorker(
			tenantID,
			factRepo,
			superseder,
			logger,
		).WithVectorStore(vectorStore))
		historyProcessor := memworkers.NewResolvingLLMHistorySummarizer(tenantID, resolver)
		if paramResolver != nil {
			historyProcessor = historyProcessor.WithParamResolver(paramResolver)
		}
		summarizer = historyProcessor
		compressor = historyProcessor
	}
	return append(workerSet, memworkers.NewHistoryWorker(tenantID, historyRepo, summarizer, compressor, logger).
		WithVectorStore(vectorStore))
}
