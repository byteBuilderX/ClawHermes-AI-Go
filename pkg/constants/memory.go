package constants

import "time"

// Outbox pre-filter — lightweight rules applied before INSERT INTO memory_outbox.
// Only messages passing all rules are enqueued for embedding.
const (
	// MemoryOutboxMinRunes is the minimum rune count for a message to be recorded.
	// Short acks ("OK", "好", "继续") carry no semantic value.
	MemoryOutboxMinRunes = 10
	// MemoryOutboxMaxRunes is the maximum rune count stored in the outbox payload.
	// Content beyond this is truncated to limit noise in the embedding vector.
	MemoryOutboxMaxRunes = 2000
)

// Active short-term snapshot - Phase 1 bounded overwrite memory.
const (
	ActiveSnapshotTTL               = 24 * time.Hour
	ActiveSnapshotSectionMaxItems   = 8
	ActiveSnapshotItemMaxRunes      = 240
	ActiveSnapshotTotalMaxRunes     = 1200
	ActiveSnapshotSourceRefMaxRunes = 128
	ActiveSnapshotInjectionBudget   = 600
	MemoryInjectionCharBudget       = 1800
	ActiveSnapshotReadTimeout       = 500 * time.Millisecond
)

const (
	MemoryOutboxPollInterval = 1 * time.Second
	MemoryOutboxBatchSize    = 50
)

// JetStream
const (
	MemoryStreamMaxAge    = 72 * time.Hour
	MemoryDLQMaxAge       = 168 * time.Hour
	MemoryRawStream       = "MEMORY_RAW"
	MemoryEnrichedStream  = "MEMORY_ENRICHED"
	MemoryDLQStream       = "MEMORY_DLQ"
	MemoryRawSubject      = "memory.raw"
	MemoryEnrichedSubject = "memory.enriched"
	MemoryDLQSubject      = "memory.dlq"
	// MemoryExtractionStream 承载对话事实提取任务（Redis buffer flush 后发布）。
	MemoryExtractionStream  = "MEMORY_EXTRACTION"
	MemoryExtractionSubject = "memory.extraction"
	// MemoryReflectionStream 承载任务结束后的工具轨迹反思任务。
	MemoryReflectionStream  = "MEMORY_REFLECTION"
	MemoryReflectionSubject = "memory.reflection"
)

// Embedder
const (
	EmbedderConsumerName = "embed-worker"
	EmbedderAckWait      = 30 * time.Second
	EmbedderMaxDeliver   = 5
	EmbedderWorkerCount  = 2
)

// Enricher
const (
	EnricherConsumerName          = "enrich-worker"
	EnricherAckWait               = 60 * time.Second
	EnricherMaxDeliver            = 5
	EnricherWorkerCount           = 1
	EnricherSummaryTokenThreshold = 1000
	EnricherMaxInjectionTokens    = 500
	EnricherTopEntities           = 10
	EnricherSummaryMaxMessages    = 100 // max messages fetched per summary to avoid unbounded query
	MemoryLongTermTopK            = 5
)

// Extraction / Reflection NATS consumers
const (
	ExtractionConsumerName = "extraction-worker"
	ExtractionAckWait      = 60 * time.Second
	ExtractionMaxDeliver   = 3 // 与 PG 队列 retry_count<2（共 3 次尝试）一致
	ExtractionWorkerCount  = 1
	ReflectionConsumerName = "reflection-worker"
	ReflectionAckWait      = 60 * time.Second
	ReflectionMaxDeliver   = 5
	ReflectionWorkerCount  = 1
)

// Trajectory reflection policy
const (
	// MemoryReflectionMinToolCalls 是触发反思的最小工具调用数（初值，可调）。
	MemoryReflectionMinToolCalls = 3
	// MemoryReflectionSkeletonMaxBytes 是轨迹骨架序列化后的上限；超出按工具截断。
	MemoryReflectionSkeletonMaxBytes = 8 * 1024
	// MemoryReflectionMaxEntries 是单任务反思候选的最大持久化条数。
	MemoryReflectionMaxEntries = 5
	// MemoryReflectionStepMax 是骨架保留的最大步骤数，防止超长任务撑爆提示词。
	MemoryReflectionStepMax = 50
	// MemoryReflectionLLMMaxTokens 是反思 LLM 输出的 max_tokens 上限。
	MemoryReflectionLLMMaxTokens = 4096
	// MemoryReflectionArgsSummaryMaxRunes 是单步参数摘要的最大 rune 数。
	MemoryReflectionArgsSummaryMaxRunes = 200
	// MemoryReflectionErrorFingerprintMaxRunes 是错误指纹的最大 rune 数。
	MemoryReflectionErrorFingerprintMaxRunes = 200
	// MemoryReflectionResultSummaryMaxRunes 是任务结果摘要的最大 rune 数。
	MemoryReflectionResultSummaryMaxRunes = 500
)

// MemoryExplicitRememberKeywords 是用户显式"记住"指令的关键词（agent 侧
// 检测后置入反思触发 gate）。中文按字面匹配，英文区分大小写不敏感。
var MemoryExplicitRememberKeywords = []string{"记住", "remember", "REMEMBER"}

// Pipeline runtime safeguards.
const (
	// MemoryFetchBackoffBase 是 JetStream Fetch 失败后的初始退避，避免 NATS 抖动时 worker 100% CPU 自旋。
	MemoryFetchBackoffBase = 200 * time.Millisecond
	// MemoryFetchBackoffMax 退避上限。
	MemoryFetchBackoffMax = 10 * time.Second
	// MemoryQueueEmptyBackoff 是抽取队列为空时的最小轮询间隔：
	// 队列空时不得紧接重查，避免多租户空转打满 DB（2026-08-21 CPU 打满事故）。
	MemoryQueueEmptyBackoff = 1 * time.Second
	// MemoryOutboxPublishTimeout 限制单次 NATS Publish 的最长阻塞时间。
	// Publish 在 outbox 取出行事务提交后执行（事务内禁止网络 IO），
	// 该超时防止 NATS 慢/断连时 poll 循环卡死。
	MemoryOutboxPublishTimeout = 3 * time.Second
	// MemoryEnrichLLMTimeout 富化阶段 LLM 调用上限。
	MemoryEnrichLLMTimeout = 30 * time.Second
	// MemorySummaryLLMTimeout 摘要 LLM 调用上限（事务外执行）。
	MemorySummaryLLMTimeout = 60 * time.Second
	// PlatformModelValidationTimeout 平台模型 key 写时校验模型目录的单次 DB 预算
	// （PUT /admin/parameters 的 ValidateFn 内使用,避免目录查询挂死写路径）。
	PlatformModelValidationTimeout = 3 * time.Second
)

// Memory Buffer - controls fact extraction pipeline batching
const (
	MemoryBufferFlushSize     = 5 // flush after K messages
	MemoryBufferFlushInterval = 2 * time.Minute
	// MemoryBufferFlushLockTTL bounds how long a flusher may hold the per-key
	// single-flight lock. A crashed flusher (process kill, network partition)
	// releases via TTL so the buffer is never starved; a live flush completes
	// in milliseconds, so 30s is far beyond the critical section.
	MemoryBufferFlushLockTTL = 30 * time.Second
	// MemoryBufferKeyTTL is a sliding safety TTL on the Redis list key.
	// Prevents leaked keys when a conversation ends before K or T flush triggers
	// (e.g. tab closed, server restart). Reset on every push so slow but active
	// conversations are never evicted prematurely. 24 h matches industry-standard
	// session-buffer lifetimes (LangChain ConversationBufferMemory, Mem0).
	MemoryBufferKeyTTL = 24 * time.Hour

	MemoryBufferSizeLimit    = 8 * 1024         // flush if accumulated bytes >= 8KB
	MemoryBufferIdleTimeout  = 60 * time.Second // scanner: flush if no new message for 60s
	MemoryBufferAgeTimeout   = 5 * time.Minute  // scanner: flush if oldest message > 5min
	MemoryBufferScanInterval = 30 * time.Second // how often BufferScanner polls Redis
	// MemoryBufferScanTimeout is the per-scan operation budget. store.Scan can
	// hang on DNS/network (e.g. WSL2 lookup timeout reaches 30s); without a
	// budget the ticker cadence stalls behind the hung call. Must be <
	// MemoryBufferScanInterval so the scan can never outlive its ticker slot.
	MemoryBufferScanTimeout   = 20 * time.Second
	MemoryTenantWatchInterval = 60 * time.Second // how often TenantWatcher polls tenant list

	// MemoryBufferMinContentRunes is the minimum rune count of non-tool messages required to
	// trigger fact extraction. Flushes with less substantive content are discarded.
	// 50: filters pure ack sessions ("OK"×5≈10 runes) while allowing short factual statements
	// (e.g. "我喜欢Python"=8 chars passes when combined with other messages).
	MemoryBufferMinContentRunes = 50
)

// Memory Recall - controls retrieval behavior
const (
	// MemoryRecallTopK 是召回工具非法/缺失 limit 时的兜底条数。与 registry
	// memory.recall_top_k 的 Default=5 对齐（接入消费点后未配置 agent 走同一值，
	// 无 5→10 静默漂移）。
	MemoryRecallTopK     = 5    // max facts per recall
	MemoryRecallMinTopK  = 1    // clamp 下限
	MemoryRecallMaxTopK  = 20   // clamp 上限（工具 docstring 的合法 limit 上界）
	MemoryFrecencyLambda = 0.05 // decay rate for frecency scoring
	MemoryRRFConstant    = 60   // RRF k parameter for hybrid retrieval fusion
)

// Memory GC - controls soft-delete and episodic TTL cleanup
const (
	MemorySoftDeleteRetention = 30 * 24 * time.Hour // 30 days
	// MemoryEpisodicTTL is the retention window for the raw-turn episodic
	// layer (memory_entries + memory_<tenant>_<model> vectors). Raw dialogue
	// value decays fast; industry practice bounds episodic stores at 30-90
	// days. 90d matches MemorySupersededRetention so both layers share the
	// same retention cadence. Entries with an explicit expires_at are removed
	// at their own expiry, independently of this cutoff.
	MemoryEpisodicTTL = 90 * 24 * time.Hour
)

// Dynamic long-term History policy. Values are centralized so workers,
// persistence, and injection cannot silently diverge.
const (
	HistoryAggregationMinEntries = 5
	HistoryAggregationBatchSize  = 50
	HistoryRecentMaxSegments     = 12
	HistoryEarlierMaxSegments    = 12
	HistoryRecentPromotionAge    = 90 * 24 * time.Hour
	HistoryEarlierPromotionAge   = 365 * 24 * time.Hour
	HistoryWorkerInterval        = 6 * time.Hour
	HistoryOperationTimeout      = 30 * time.Second
	HistoryReadTimeout           = 500 * time.Millisecond
	HistoryInjectionTopN         = 3
	HistoryInjectionCharBudget   = 500
	HistoryArchiveInactiveAge    = 180 * 24 * time.Hour
	HistoryProtectedImportance   = 0.8
	HistoryProtectedConfidence   = 0.8
)

// Memory Quota - per-user limits
const (
	MemoryFactQuotaPerUser = 5000 // max facts per user
)

// Memory Extraction - LLM extraction limits
const (
	// MemoryMaxFactsPerExtraction 是单轮抽取最大事实数（prompt 软约束上限）。
	// 与写入硬上限 FactPerRoundPersistLimit=10 对齐：registry Default/VisualHint.Max
	// 与前端 Slider Max 同值，配置 N = 每轮入库 ≤N，避免"配了不生效"困惑。
	MemoryMaxFactsPerExtraction = 10
	MemoryMinFactLength         = 10   // min chars for a valid fact
	MemoryMaxFactLength         = 500  // max chars for a valid fact
	MemoryExtractLLMMaxTokens   = 4096 // JSON array of facts; 1024 truncates large conversations
	MemoryEnrichLLMTemperature  = 0.1  // 富化抽取任务温度（低温度换取字段语义稳定）
	// MemoryMaxStructuredRetries 结构化 JSON 输出解析/校验失败后的带错重试次数
	// （共 MemoryMaxStructuredRetries+1 次尝试）。每次重试把具体错误位置/值/原因
	// 作为 system-role correction 丢回模型。provider 硬错误不消耗重试（fail-fast）。
	MemoryMaxStructuredRetries = 2
)

// Memory Supersede - supersede detection thresholds
const (
	MemorySupersedeCandidateMin     = 0.6  // min similarity to consider supersede
	MemorySupersedeCandidateMax     = 3    // max candidates to check per fact
	MemorySupersedeLLMCallsPerRun   = 20   // max LLM judgments per RunOnce pass
	MemoryInlineSupersedeFastThresh = 0.85 // similarity above which supersede is decided inline without LLM
	MemoryInlineSupersedeLLMPerFact = 3    // max inline LLM calls per extracted fact during extraction
	// MemorySupersedeJudgeMaxTokens 取代判定请求的 max_tokens 上限
	MemorySupersedeJudgeMaxTokens = 256
)

// Facts quality filter — Phase 0 hardening
const (
	// FactConfidenceMin 写入前低置信过滤阈值；低于此值的事实在持久化前被丢弃
	FactConfidenceMin = 0.3
	// FactInjectionConfidenceMin 注入器读取阈值；只注入 confidence >= 此值的 active 事实
	FactInjectionConfidenceMin = 0.4
	// FactPerRoundPersistLimit 单轮抽取最多持久化的事实数；超出部分按质量排序后截断
	FactPerRoundPersistLimit = 10
	// FactInjectionTopN 注入器每次取的最大事实数
	FactInjectionTopN = 8
	// FactInjectionCharBudget 注入器事实段的最大字符数；超出时截断
	FactInjectionCharBudget = 1200
	// FactInjectionTimeout 注入器读取超时；超时降级为空而不是错误
	FactInjectionTimeout = 3 * time.Second
)

// Memory Workers - background processing intervals and batch sizes
const (
	MemoryExtractionBatchSize  = 10                  // facts per extraction queue poll
	MemoryExtractionLease      = 5 * time.Minute     // reclaim processing tasks after worker loss
	MemorySupersedeBatchSize   = 20                  // facts per supersede judgment batch
	MemoryEmbedInterval        = 10 * time.Second    // embed worker poll interval
	MemoryEmbedBatchSize       = 50                  // facts per embed batch
	MemoryProfileInterval      = 5 * time.Minute     // supersede worker poll interval
	MemoryGCInterval           = 24 * time.Hour      // garbage collection interval
	MemoryGCBatchSize          = 100                 // facts per GC batch
	MemoryGCQueueRetentionDays = 7                   // days to keep completed queue tasks
	MemoryDeletedRetention     = 30 * 24 * time.Hour // purge deleted after 30 days
	MemorySupersededRetention  = 90 * 24 * time.Hour // purge superseded after 90 days
)

// MemoryModelConfig — 记忆模型参数完备性探针周期。探针与流量无关，周期比对
// 平台参数值与 enabled 模型目录，使「模型未配置/被禁用」在零流量下也能告警。
const (
	MemoryModelConfigProbeInterval = 5 * time.Minute
)

// MemoryMigration — 记忆嵌入模型平滑迁移（P5）回填 worker 与成本预览参数。
const (
	// MemoryMigrationScanInterval 回填 worker 轮询所有租户待处理迁移的间隔。
	// 值 ≤1min：管理员确认切换后，迁移应在秒级内被 worker 拾起开始渐进回填。
	MemoryMigrationScanInterval = 30 * time.Second
	// MemoryMigrationPageSize 回填单次从 memory_facts 读取并批量 re-embed 的行数。
	// 与 MemoryEmbedBatchSize 同级，控制单次 Milvus Upsert 的批次大小。
	MemoryMigrationPageSize = 50
	// MemoryMigrationPerFactEstimateMS 成本预览对单条事实 embed+upsert 的耗时
	// 估算（毫秒），仅用于 UI 展示预计时长，非精确计量。
	MemoryMigrationPerFactEstimateMS = 200
)
