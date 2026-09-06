package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	"github.com/byteBuilderX/stratum/internal/memory/domain"
	"github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/persistence"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
)

// EnrichmentResult holds the structured metadata extracted by the LLM.
type EnrichmentResult struct {
	Entities        []EntityExtraction `json:"entities"`
	Importance      float64            `json:"importance"`
	TokenEstimate   int                `json:"token_estimate"`
	Keywords        []string           `json:"keywords"`
	WorkContext     []string           `json:"work_context"`
	PersonalContext []string           `json:"personal_context"`
	TopOfMind       []string           `json:"top_of_mind"`
}

// EntityExtraction represents a single entity found in a message.
type EntityExtraction struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

// EnricherWorker consumes embedded events, calls the LLM for metadata
// extraction, persists enrichment results, and triggers conversation
// summaries when the token budget is exceeded.
type EnricherWorker struct {
	consumer      jetstream.Consumer
	js            dlqPublisher
	pool          *pgxpool.Pool
	llmResolver   LLMResolver
	paramResolver port.PlatformParamResolver
	vectorCleaner entryVectorDeleter
	logger        *zap.Logger
	stopCh        chan struct{}
	stopOnce      sync.Once
	ackWait       time.Duration
	maxDeliver    int
	snapshotRepo  port.ActiveSnapshotRepo
}

// NewEnricherWorker creates an enricher configured from the pipeline Config.
// LLM content settings (model / temperature / prompt / threshold) are no
// longer captured here: they resolve from the platform parameter store per
// call, so admin edits apply without a worker restart.
func NewEnricherWorker(
	consumer jetstream.Consumer,
	js dlqPublisher,
	pool *pgxpool.Pool,
	logger *zap.Logger,
	cfg Config,
) *EnricherWorker {
	return &EnricherWorker{
		consumer:     consumer,
		js:           js,
		pool:         pool,
		logger:       logger,
		stopCh:       make(chan struct{}),
		ackWait:      cfg.EnrichAckWait,
		maxDeliver:   cfg.MaxDeliver,
		snapshotRepo: persistence.NewActiveSnapshotRepo(pool),
	}
}

// WithLLMResolver sets a per-tenant LLM resolver used as the primary client
// for enrich/summary calls. The base llm is kept only as a fallback for
// resolver-less single-tenant test setups.
func (w *EnricherWorker) WithLLMResolver(r LLMResolver) *EnricherWorker {
	w.llmResolver = r
	return w
}

// WithParamResolver sets the platform parameter resolver used to resolve
// per-call LLM content settings. A nil resolver keeps the const defaults.
func (w *EnricherWorker) WithParamResolver(r port.PlatformParamResolver) *EnricherWorker {
	w.paramResolver = r
	return w
}

// WithEntryVectorDeleter wires the cleaner used to remove orphan vectors when
// enrichment dead-letters after the embedder already wrote the vector.
func (w *EnricherWorker) WithEntryVectorDeleter(d entryVectorDeleter) *EnricherWorker {
	w.vectorCleaner = d
	return w
}

// resolvePlatformString resolves a platform string param, falling back to def
// when the resolver is absent, the value is unset, or resolution fails.
func (w *EnricherWorker) resolvePlatformString(ctx context.Context, key, def string) string {
	if w.paramResolver == nil {
		return def
	}
	v, ok, err := w.paramResolver.ResolvePlatform(ctx, key)
	if err != nil || !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

// resolvePlatformFloat resolves a platform float param, falling back to def
// when the resolver is absent, the value is unset (0), or resolution fails.
func (w *EnricherWorker) resolvePlatformFloat(ctx context.Context, key string, def float32) float32 {
	if w.paramResolver == nil {
		return def
	}
	v, ok, err := w.paramResolver.ResolvePlatform(ctx, key)
	if err != nil || !ok {
		return def
	}
	if f, ok := v.(float64); ok {
		return float32(f)
	}
	return def
}

// resolvePlatformInt resolves a platform int param, falling back to def when
// the resolver is absent, the value is unset (0), or resolution fails.
func (w *EnricherWorker) resolvePlatformInt(ctx context.Context, key string, def int) int {
	if w.paramResolver == nil {
		return def
	}
	v, ok, err := w.paramResolver.ResolvePlatform(ctx, key)
	if err != nil || !ok {
		return def
	}
	if i, ok := v.(int64); ok {
		return int(i)
	}
	return def
}

// logConfigError 记录一次记忆模型参数配置失败并计数（stage 标识消费组件）。
// 各消费点在解析模型失败处复用；err 非 *modelconfig.Err 时为空操作（不吞普通
// 错误、不伪造计数）。logger 可为 nil（测试未注入时安全）。
func logConfigError(logger *zap.Logger, key, stage string, err error) {
	ce, ok := modelconfig.AsConfigError(err)
	if !ok {
		return
	}
	modelconfig.IncError(ce.Key, stage, ce.State)
	if logger != nil {
		logger.Error("memory.modelconfig.config_error",
			zap.String("param", ce.Key),
			zap.String("config_state", string(ce.State)),
			zap.Error(err))
	}
}

// enrichSettings holds the per-call platform-resolved enrich LLM settings.
type enrichSettings struct {
	model       string
	temperature float32
}

// resolveEnrichSettings resolves the enrich model/temperature for one event.
// 模型为必需平台参数（fail-closed）：未显式配置或解析失败返回 *modelconfig.Err，
// 禁止空模型回落 llmgateway 默认；温度仍回落常量默认。
func (w *EnricherWorker) resolveEnrichSettings(ctx context.Context) (enrichSettings, error) {
	model, err := modelconfig.ResolveChatModel(ctx, w.paramResolver, modelconfig.KeyEnrichModel)
	if err != nil {
		logConfigError(w.logger, modelconfig.KeyEnrichModel, "enrich", err)
		return enrichSettings{}, err
	}
	return enrichSettings{
		model:       model,
		temperature: w.resolvePlatformFloat(ctx, "memory.enrich_temperature", constants.MemoryEnrichLLMTemperature),
	}, nil
}

// summarySettings holds the per-call platform-resolved session-summary LLM
// settings plus the trigger threshold.
type summarySettings struct {
	model       string
	temperature float32
	threshold   int
}

// resolveSummarySettings resolves the summary model/temperature/threshold for
// one event. 模型为必需平台参数（fail-closed）：未显式配置或解析失败返回
// *modelconfig.Err，禁止空模型回落 llmgateway 默认；温度/阈值回落常量默认。
func (w *EnricherWorker) resolveSummarySettings(ctx context.Context) (summarySettings, error) {
	model, err := modelconfig.ResolveChatModel(ctx, w.paramResolver, modelconfig.KeySummaryModel)
	if err != nil {
		logConfigError(w.logger, modelconfig.KeySummaryModel, "enrich_summary", err)
		return summarySettings{}, err
	}
	return summarySettings{
		model:       model,
		temperature: w.resolvePlatformFloat(ctx, "memory.summary_temperature", constants.TaskSummarizeTemperature),
		threshold:   w.resolvePlatformInt(ctx, "memory.summary_token_threshold", constants.EnricherSummaryTokenThreshold),
	}, nil
}

// llmFor returns the LLMClient for tenantID. Prefers the resolver-supplied
// per-tenant client; falls back to the base llm if the resolver is unset or
// returns nil. Returns nil only when no LLM is wired at all — callers must
// llmFor returns the per-tenant LLMClient. Returns nil when no resolver is set
// or the resolver returns nil — callers must nil-check before calling Complete.
func (w *EnricherWorker) llmFor(ctx context.Context, tenantID string) LLMClient {
	if w.llmResolver != nil {
		return w.llmResolver(ctx, tenantID)
	}
	return nil
}

// Start begins the consume loop. It blocks until ctx is cancelled or Stop is called.
func (w *EnricherWorker) Start(ctx context.Context) {
	backoff := constants.MemoryFetchBackoffBase
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		default:
		}

		msgs, err := w.consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Warn("memory.enrich.fetch_failed",
				zap.Error(err),
				zap.Duration("backoff", backoff))
			if !sleepCtx(ctx, w.stopCh, backoff) {
				return
			}
			if backoff < constants.MemoryFetchBackoffMax {
				backoff *= 2
				if backoff > constants.MemoryFetchBackoffMax {
					backoff = constants.MemoryFetchBackoffMax
				}
			}
			continue
		}
		backoff = constants.MemoryFetchBackoffBase

		for msg := range msgs.Messages() {
			w.safeProcessMessage(ctx, msg)
		}
	}
}

// Stop signals the worker to exit its consume loop.
func (w *EnricherWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// safeProcessMessage isolates per-message panics so a single bad payload can't
// take down the whole worker goroutine.
func (w *EnricherWorker) safeProcessMessage(ctx context.Context, msg jetstream.Msg) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("memory.enrich.panic",
				zap.Any("panic", r),
				zap.String("subject", msg.Subject()),
				zap.Stack("stack"))
			enrichTotal.With(prometheus.Labels{"tenant_id": "unknown", "status": "panic"}).Inc()
			_ = msg.Nak()
		}
	}()
	w.processMessage(ctx, msg)
}

func (w *EnricherWorker) processMessage(ctx context.Context, msg jetstream.Msg) {
	start := time.Now()
	stopHeartbeat := startProgressHeartbeat(msg, w.ackWait/2)
	defer stopHeartbeat()

	ev, err := UnmarshalEnrichedEvent(msg.Data())
	if err != nil {
		w.logger.Error("memory.enrich.unmarshal", zap.Error(err))
		enrichTotal.With(prometheus.Labels{"tenant_id": "unknown", "status": "error"}).Inc()
		if dlqErr := deadLetterWithHeartbeat(
			ctx, w.js, msg, stopHeartbeat, deadLetterDetails{Stage: "enrich", ErrorCode: "invalid_event"},
		); dlqErr != nil {
			w.logger.Error("memory.enrich.dlq", zap.Error(dlqErr))
		}
		return
	}

	traceID := ev.TraceID
	w.logger.Debug("memory.enrich.start",
		zap.String("trace_id", traceID),
		zap.String("message_id", ev.MessageID),
		zap.String("tenant_id", ev.TenantID))

	llm := w.llmFor(ctx, ev.TenantID)
	if llm == nil {
		deadLetterWithOrphan(ctx, w.js, msg, stopHeartbeat, deadLetterDetails{
			Stage: "enrich", TenantID: ev.TenantID, MessageID: ev.MessageID, ErrorCode: "llm_service_unavailable",
			TraceID: traceID,
		}, w.vectorCleaner, w.logger, ev.TenantID, ev.MessageID, "memory.enrich.dlq")
		return
	}
	enrichment, err := w.callEnrichLLM(ctx, llm, ev.Role, ev.Content)
	if err != nil {
		w.disposeEnrichError(ctx, msg, stopHeartbeat, ev, traceID, err)
		return
	}

	if err := w.persistEnrichment(ctx, ev, enrichment); err != nil {
		w.logger.Error("memory.enrich.persist",
			zap.String("trace_id", traceID),
			zap.String("message_id", ev.MessageID),
			zap.Error(err))
		enrichTotal.With(prometheus.Labels{"tenant_id": ev.TenantID, "status": "error"}).Inc()
		retryOrDeadLetterWithOrphan(ctx, w.js, msg, w.maxDeliver, stopHeartbeat, deadLetterDetails{
			Stage: "enrich", TenantID: ev.TenantID, MessageID: ev.MessageID, ErrorCode: "persist_failed",
			TraceID: traceID,
		}, w.vectorCleaner, w.logger, ev.TenantID, ev.MessageID, "memory.enrich.retry_or_dlq")
		return
	}
	_ = w.refreshActiveSnapshot(ctx, ev, enrichment)

	enrichDuration.Observe(time.Since(start).Seconds())
	enrichTotal.With(prometheus.Labels{"tenant_id": ev.TenantID, "status": "success"}).Inc()
	entitiesExtracted.Add(float64(len(enrichment.Entities)))

	stopHeartbeat()
	if err := msg.Ack(); err != nil {
		w.logger.Warn("memory.enrich.ack_failed",
			zap.String("trace_id", traceID),
			zap.String("message_id", ev.MessageID),
			zap.Error(err))
	}
	w.logger.Info("memory.enrich.success",
		zap.String("trace_id", traceID),
		zap.String("message_id", ev.MessageID),
		zap.String("tenant_id", ev.TenantID),
		zap.Float64("importance", enrichment.Importance),
		zap.Int("entities", len(enrichment.Entities)),
		zap.Int64("latency_ms", time.Since(start).Milliseconds()))

	// 关键：摘要触发必须在 Ack 之后、事务之外执行。
	// 原实现把 LLM 调用塞在 persistEnrichment 的事务里，单次摘要 30s+，
	// 一个慢富化能把整个 pgxpool 连接池耗尽，连带主流程 DB 调用全部超时。
	// 这里独立 ctx + 独立 tx + 独立 panic recover，失败只 warn 不影响主流程。
	w.runSummaryAsyncSafe(ctx, ev)
}

func (w *EnricherWorker) refreshActiveSnapshot(ctx context.Context, ev *MemoryEnrichedEvent, enrichment *EnrichmentResult) error {
	if w.snapshotRepo == nil || (len(enrichment.WorkContext) == 0 && len(enrichment.PersonalContext) == 0 && len(enrichment.TopOfMind) == 0) {
		return nil
	}
	eventTime := ev.CreatedAt.UTC()
	if eventTime.IsZero() {
		w.logger.Warn("memory.enrich.active_snapshot_zero_event_time",
			zap.String("tenant_id", ev.TenantID), zap.String("message_id", ev.MessageID))
		return nil
	}
	snapshot := &domain.ActiveSnapshot{
		TenantID: ev.TenantID, UserID: ev.UserID, AgentID: ev.AgentID,
		WorkContext: enrichment.WorkContext, PersonalContext: enrichment.PersonalContext, TopOfMind: enrichment.TopOfMind,
		Source:    domain.SnapshotSource{Type: "message", Reference: ev.MessageID},
		ExpiresAt: eventTime.Add(constants.ActiveSnapshotTTL), UpdatedAt: eventTime, Status: domain.SnapshotStatusActive,
	}
	if err := w.snapshotRepo.Upsert(ctx, snapshot); err != nil {
		w.logger.Warn("memory.enrich.active_snapshot_failed", zap.String("tenant_id", ev.TenantID), zap.Error(err))
		enrichTotal.With(prometheus.Labels{"tenant_id": ev.TenantID, "status": "snapshot_error"}).Inc()
	}
	return nil
}

func (w *EnricherWorker) runSummaryAsyncSafe(ctx context.Context, ev *MemoryEnrichedEvent) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("memory.enrich.summary_panic",
				zap.String("trace_id", ev.TraceID),
				zap.String("conversation_id", ev.ConversationID),
				zap.Any("panic", r),
				zap.Stack("stack"))
		}
	}()
	if err := w.maybeTriggerSummary(ctx, ev); err != nil {
		w.logger.Warn("memory.enrich.summary_check",
			zap.String("trace_id", ev.TraceID),
			zap.String("conversation_id", ev.ConversationID),
			zap.Error(err))
	}
}

func (w *EnricherWorker) callEnrichLLM(ctx context.Context, llm LLMClient, role, content string) (*EnrichmentResult, error) {
	settings, err := w.resolveEnrichSettings(ctx)
	if err != nil {
		// 模型为必需平台参数：缺失/解析失败已 logConfigError 计数，禁止空回落。
		return nil, err
	}
	promptTmpl := w.resolvePlatformString(ctx, "memory.enrich_prompt", "")
	if strings.TrimSpace(promptTmpl) == "" {
		// fail-closed：无显式配置不允许空 system prompt 静默调用 LLM。
		return nil, fmt.Errorf("memory enrich: memory.enrich_prompt not configured (fail-closed)")
	}
	prompt := formatEnrichmentPrompt(promptTmpl, role, content)
	w.logger.Debug("memory.enrich_resolved",
		zap.String("model", settings.model),
		zap.Float32("temperature", settings.temperature))
	req := llmdomain.NewExtractRequest(settings.model, "", prompt, settings.temperature, 0)

	llmCtx, cancel := context.WithTimeout(ctx, constants.MemoryEnrichLLMTimeout)
	defer cancel()
	result, err := CompleteStructured(llmCtx, llm, req, parseEnrichmentResult,
		func(r EnrichmentResult) error { return r.Validate() }, w.logger, "enrich")
	if err != nil {
		return nil, err
	}
	// token_estimate 由代码计算，不依赖 LLM 自填（不可靠）
	result.TokenEstimate = tokenutil.EstimateText(content)
	return &result, nil
}

// disposeEnrichError 分派富化失败路径：模型配置错（永久性，重试不会自愈）即时
// DLQ 不消耗重试预算；其余 LLM/IO 错仍走 maxDeliver 重试→DLQ。独立方法使
// processMessage 保持在线数与行数基线内。
func (w *EnricherWorker) disposeEnrichError(
	ctx context.Context,
	msg jetstream.Msg,
	stopHeartbeat func(),
	ev *MemoryEnrichedEvent,
	traceID string,
	err error,
) {
	enrichTotal.With(prometheus.Labels{"tenant_id": ev.TenantID, "status": "error"}).Inc()
	details := deadLetterDetails{
		Stage: "enrich", TenantID: ev.TenantID, MessageID: ev.MessageID, TraceID: traceID,
	}
	if _, isCfg := modelconfig.AsConfigError(err); isCfg {
		// 永久配置错 → 即时 DLQ（仿 embedder 无模型先例），配置修复后经重放恢复。
		details.ErrorCode = "model_not_configured"
		deadLetterWithOrphan(ctx, w.js, msg, stopHeartbeat, details, w.vectorCleaner,
			w.logger, ev.TenantID, ev.MessageID, "memory.enrich.dlq")
		return
	}
	w.logger.Error("memory.enrich.llm",
		zap.String("trace_id", traceID),
		zap.String("message_id", ev.MessageID),
		zap.Error(err))
	details.ErrorCode = "llm_failed"
	retryOrDeadLetterWithOrphan(ctx, w.js, msg, w.maxDeliver, stopHeartbeat, details, w.vectorCleaner,
		w.logger, ev.TenantID, ev.MessageID, "memory.enrich.retry_or_dlq")
}

// parseEnrichmentResult 解析富化 JSON 输出。解析失败由 CompleteStructured
// 带错重试处理（错误位置经 correction 丢回模型自修复）。
func parseEnrichmentResult(raw string) (EnrichmentResult, error) {
	var result EnrichmentResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return result, fmt.Errorf("parse enrichment response: %w", err)
	}
	return result, nil
}

// unitInterval 判断 v 是否在 [0,1] 闭区间（pipeline 包共享 helper；
// port 包的 inUnitInterval 未导出，避免跨包引依赖）。
func unitInterval(v float64) bool {
	return v >= 0 && v <= 1
}

// Validate 校验富化结果语义：importance ∈ [0,1]；每条实体 name 非空、
// confidence ∈ [0,1]。返回 *port.ValidationError 或 nil。
func (r EnrichmentResult) Validate() error {
	if !unitInterval(r.Importance) {
		return &port.ValidationError{Location: "enrichment", FieldName: "importance",
			Value: strconv.FormatFloat(r.Importance, 'g', -1, 64), Reason: "importance must be in [0,1]"}
	}
	for i, e := range r.Entities {
		if strings.TrimSpace(e.Name) == "" {
			return &port.ValidationError{Location: "enrichment", FieldName: "entities",
				Value: strconv.Itoa(i), Reason: "entity name must not be empty"}
		}
		if !unitInterval(e.Confidence) {
			return &port.ValidationError{Location: "enrichment", FieldName: "entities",
				Value:  fmt.Sprintf("index %d confidence=%s", i, strconv.FormatFloat(e.Confidence, 'g', -1, 64)),
				Reason: "entity confidence must be in [0,1]"}
		}
	}
	return nil
}

func (w *EnricherWorker) persistEnrichment(ctx context.Context, ev *MemoryEnrichedEvent, enrichment *EnrichmentResult) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	schema := "tenant_" + ev.TenantID
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public", pgx.Identifier{schema}.Sanitize())); err != nil {
		return fmt.Errorf("set schema: %w", err)
	}

	keywords := enrichment.Keywords
	if keywords == nil {
		keywords = []string{}
	}

	// scope 归一化：在途遗留消息（旧版本 agents.memory_scope 无 CHECK 时可存 ''）
	// 可能携带空 scope。memory_entries/memory_entities 均有 scope 白名单 CHECK，
	// 空值会把整条 enrichment 打成 SQLSTATE 23514；按 agent 归属回落默认，与
	// tenant_schema 的 backfill 语义一致（详见 normalizeScope）。
	scope := normalizeScope(ev)

	_, err = tx.Exec(ctx, `
		INSERT INTO memory_entries (id, user_id, agent_id, scope, role, content, type, importance, keywords, token_estimate, enriched_at, conversation_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'long_term', $7, $8, $9, NOW(), $10, $11)
		ON CONFLICT (id) DO UPDATE SET
			importance = EXCLUDED.importance,
			keywords = EXCLUDED.keywords,
			token_estimate = EXCLUDED.token_estimate,
			enriched_at = NOW()`,
		ev.MessageID, ev.UserID, ev.AgentID, scope, ev.Role, ev.Content,
		enrichment.Importance, keywords, enrichment.TokenEstimate,
		ev.ConversationID, ev.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert memory_entries: %w", err)
	}

	for _, entity := range enrichment.Entities {
		_, err = tx.Exec(ctx, `
			INSERT INTO memory_entities (name, entity_type, user_id, agent_id, scope, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (name, entity_type, user_id, COALESCE(agent_id, '')) DO UPDATE SET
				last_seen_at = NOW()`,
			entity.Name, entity.Type, ev.UserID, ev.AgentID, scope)
		if err != nil {
			return fmt.Errorf("upsert entity %s: %w", entity.Name, err)
		}
	}

	return tx.Commit(ctx)
}

// maybeTriggerSummary 走完全独立的事务生命周期：
//  1. 短事务读阈值 + 历史消息（持锁 ≤几十 ms）
//  2. Rollback 后调 LLM（30s+，与 DB 解耦）
//  3. 新事务写 memory_summaries
//
// 老实现把 LLM Complete 塞在 persistEnrichment 的事务里，单条记录持锁 30s+，
// 高 QPS 下 pgxpool 连接耗尽，主流程全部 DB 调用排队超时甚至拖崩 worker。
func (w *EnricherWorker) maybeTriggerSummary(ctx context.Context, ev *MemoryEnrichedEvent) error {
	if ev.ConversationID == "" {
		return nil
	}

	settings, err := w.resolveSummarySettings(ctx)
	if err != nil {
		// 摘要模型为必需平台参数；缺失/解析失败已 logConfigError 计数 + ERROR。
		// 副链不可重投：本轮跳过 summary，不触发任何 DB 读或 LLM 调用。
		return nil
	}
	w.logger.Debug("memory.summary_resolved",
		zap.String("model", settings.model),
		zap.Float32("temperature", settings.temperature),
		zap.Int("token_threshold", settings.threshold))
	schema := "tenant_" + ev.TenantID
	accumulated, prevSummary, sb, err := w.fetchSummaryInputs(ctx, schema, ev.ConversationID, settings.threshold)
	if err != nil {
		return err
	}
	if sb == nil {
		return nil
	}

	llm := w.llmFor(ctx, ev.TenantID)
	if llm == nil {
		return fmt.Errorf("no llm client configured for tenant %s", ev.TenantID)
	}
	input := sb.String()
	if prevSummary != "" {
		input = "[Previous Summary]: " + prevSummary + "\n\n[New Messages]:\n" + input
	}
	promptTmpl := w.resolvePlatformString(ctx, "memory.summary_prompt", "")
	if strings.TrimSpace(promptTmpl) == "" {
		// fail-closed：摘要提示词未配置 → 记 ERROR 并跳过（不阻塞 enrich 主链路）。
		w.logger.Error("memory.summary.skip_prompt_not_configured",
			zap.String("trace_id", ev.TraceID),
			zap.String("conversation_id", ev.ConversationID))
		summaryTriggered.Inc()
		return nil
	}
	prompt := formatSummaryPrompt(promptTmpl, input)
	req := newSummaryLLMRequest(settings, prompt)
	llmCtx, cancel := context.WithTimeout(ctx, constants.MemorySummaryLLMTimeout)
	defer cancel()
	resp, err := llm.Complete(llmCtx, req)
	if err != nil {
		return fmt.Errorf("summary llm: %w", err)
	}
	summary := strings.TrimSpace(resp.Content)

	if err := w.writeSummary(ctx, schema, ev, summary, resp.Usage.CompletionTokens); err != nil {
		return err
	}

	summaryTriggered.Inc()
	w.logger.Info("memory.enrich.summary",
		zap.String("trace_id", ev.TraceID),
		zap.String("conversation_id", ev.ConversationID),
		zap.Int("token_budget", accumulated),
		zap.Int("summary_length", len(summary)))
	return nil
}

// newSummaryLLMRequest 构造会话摘要 LLM 请求：平台配置的温度（memory.summary_temperature，
// 0 = 保留默认）经 llmgateway.PlatformTemperaturePtr 统一舍入到 2 位小数。
// 禁止 float64(float32) 直转覆盖 Temperature——会把 0.1 放大成 0.10000000149011612，
// 触发智谱等端点的小数位校验 400（PR #441 修复后仍有两处覆盖点漏网）。
func newSummaryLLMRequest(settings summarySettings, prompt string) *llmdomain.CompletionRequest {
	req := llmdomain.NewSummarizeRequest(settings.model, prompt, nil, 0)
	req.Temperature = llmdomain.PlatformTemperaturePtr(settings.temperature)
	return req
}

// fetchSummaryInputs 在短事务里检查阈值并捞历史消息，立即释放事务后再去调 LLM。
// threshold 由调用方（maybeTriggerSummary）解析平台 memory.summary_token_threshold，
// 传参避免读取可变 worker 状态。返回 (累计 token, 历史消息文本 builder, err)。
// 当 builder 为 nil 时表示无需触发摘要。
func (w *EnricherWorker) fetchSummaryInputs(ctx context.Context, schema, convID string, threshold int) (int, string, *strings.Builder, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return 0, "", nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public", pgx.Identifier{schema}.Sanitize())); err != nil {
		return 0, "", nil, fmt.Errorf("set schema: %w", err)
	}

	var accumulated int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(token_estimate), 0) FROM memory_entries
		WHERE conversation_id = $1 AND enriched_at IS NOT NULL
		AND created_at > COALESCE(
			(SELECT covered_until FROM memory_summaries WHERE conversation_id = $1 ORDER BY created_at DESC LIMIT 1),
			'1970-01-01'
		)`, convID).Scan(&accumulated); err != nil {
		return 0, "", nil, fmt.Errorf("check token budget: %w", err)
	}
	if accumulated < threshold {
		return accumulated, "", nil, nil
	}

	var prevSummary string
	_ = tx.QueryRow(ctx,
		"SELECT summary FROM memory_summaries WHERE conversation_id = $1 ORDER BY created_at DESC LIMIT 1",
		convID).Scan(&prevSummary)

	rows, err := tx.Query(ctx, `
		SELECT role, content FROM memory_entries
		WHERE conversation_id = $1
		AND created_at > COALESCE(
			(SELECT covered_until FROM memory_summaries WHERE conversation_id = $1 ORDER BY created_at DESC LIMIT 1),
			'1970-01-01'
		)
		ORDER BY created_at ASC LIMIT $2`,
		convID, constants.EnricherSummaryMaxMessages)
	if err != nil {
		return 0, "", nil, fmt.Errorf("fetch messages: %w", err)
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err != nil {
			return 0, "", nil, fmt.Errorf("scan message: %w", err)
		}
		fmt.Fprintf(&sb, "[%s]: %s\n", role, content)
	}
	if err := rows.Err(); err != nil {
		return 0, "", nil, fmt.Errorf("rows err: %w", err)
	}
	return accumulated, prevSummary, &sb, nil
}

// normalizeScope 对在途遗留消息（旧版本 agents.memory_scope 无 CHECK 时可存 ”）
// 的 scope 做归一化：空值按 agent 归属回落默认，保证同一事件写
// memory_entries/memory_entities/memory_summaries 三表 scope 同源。空 scope 直接
// 透传给 memory_summaries（无 CHECK 约束）会被 history worker 的
// HistorySegment.Validate() 拒绝（仅接受 user|agent），导致 summary 写入失败。
func normalizeScope(ev *MemoryEnrichedEvent) string {
	if ev.Scope != "" {
		return ev.Scope
	}
	if ev.AgentID != "" {
		return "agent"
	}
	return "user"
}

func (w *EnricherWorker) writeSummary(ctx context.Context, schema string, ev *MemoryEnrichedEvent, summary string, tokens int) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public", pgx.Identifier{schema}.Sanitize())); err != nil {
		return fmt.Errorf("set schema: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO memory_summaries (conversation_id, user_id, agent_id, scope, summary, covered_until, token_count)
		VALUES ($1, $2, $3, $4, $5, NOW(), $6)`,
		ev.ConversationID, ev.UserID, ev.AgentID, normalizeScope(ev), summary, tokens); err != nil {
		return fmt.Errorf("insert summary: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit summary: %w", err)
	}
	return nil
}
