package workers

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/byteBuilderX/stratum/internal/memory/domain"
	"github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"go.uber.org/zap"
)

type HistorySummarizer interface {
	SummarizeHistory(context.Context, []string) (string, error)
}
type HistoryCompressor interface {
	CompressHistory(context.Context, []string) (string, error)
}

const historyAggregationMaxBatchesPerRun = 4

type HistoryWorker struct {
	tenantID    string
	repo        port.HistoryRepo
	summarizer  HistorySummarizer
	compressor  HistoryCompressor
	vectorStore port.VectorStore
	logger      *zap.Logger
	stopCh      chan struct{}
	stopOnce    sync.Once
}

func NewHistoryWorker(tenantID string, repo port.HistoryRepo, summarizer HistorySummarizer, compressor HistoryCompressor, logger *zap.Logger) *HistoryWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &HistoryWorker{tenantID: tenantID, repo: repo, summarizer: summarizer, compressor: compressor, logger: logger, stopCh: make(chan struct{})}
}

// WithVectorStore wires the vector store used to delete archived facts'
// vectors so the vector store converges to PG status.
func (w *HistoryWorker) WithVectorStore(vs port.VectorStore) *HistoryWorker {
	w.vectorStore = vs
	return w
}
func (w *HistoryWorker) Start(ctx context.Context) {
	runWithRestart(ctx, w.stopCh, w.logger, "memory.history_worker", w.run)
}
func (w *HistoryWorker) run(ctx context.Context) {
	w.passGuarded(ctx)
	ticker := time.NewTicker(constants.HistoryWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.passGuarded(ctx)
		}
	}
}

// passGuarded 执行一轮带模型配置预检的 history pass：仅当 summarizer 在场且其
// history_summary 模型未配置/解析失败时计 error 并跳过 RunOnce——绝不让下游把
// 模型缺失当成功上报（原 ~97 行无条件 success）。summarizer 为 nil（仅维护/GC）
// 时不构成失败，照常执行。从 run() 调用，定时与首拍两条路径都 fail-closed。
func (w *HistoryWorker) passGuarded(ctx context.Context) {
	start := time.Now()
	if w.modelConfigUnavailable(ctx, start) {
		return
	}
	w.RunOnce(ctx)
}
func (w *HistoryWorker) Stop() { w.stopOnce.Do(func() { close(w.stopCh) }) }

func (w *HistoryWorker) RunOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("memory.history_worker.panic", zap.Any("panic", r), zap.Stack("stack"))
			incWorkerPanics("history_worker")
		}
	}()

	start := time.Now()

	for range historyAggregationMaxBatchesPerRun {
		if !w.aggregateNext(ctx) {
			break
		}
	}
	if err := w.repo.Maintain(ctx, w.tenantID); err != nil {
		w.logger.Warn("memory.history.maintain_failed", zap.Error(err))
	}
	w.compressNext(ctx)
	archivedIDs, err := w.repo.ArchiveColdFacts(ctx, w.tenantID)
	if err != nil {
		w.logger.Warn("memory.history.archive_facts_failed", zap.Error(err))
	} else if len(archivedIDs) > 0 && w.vectorStore != nil {
		if err := w.vectorStore.DeleteFactVectors(ctx, w.tenantID, archivedIDs); err != nil {
			w.logger.Error("memory.history.archive_fact_vectors_failed",
				zap.String("tenant_id", w.tenantID),
				zap.Int("count", len(archivedIDs)),
				zap.Error(err))
		}
	}
	incWorkerMessages("history", w.tenantID, "success")
	observeWorkerDuration("history", w.tenantID, time.Since(start).Seconds())
}

// modelConfigUnavailable 预检 history_summary 模型配置：summarizer 在场且支持
// CheckModelConfig 但模型未配置/解析失败时返回 true（已计 error + duration）。
// summarizer 为 nil（仅维护/GC 模式）或非 LLM 实现时不构成失败。
func (w *HistoryWorker) modelConfigUnavailable(ctx context.Context, start time.Time) bool {
	if w.summarizer == nil {
		return false
	}
	c, ok := w.summarizer.(interface{ CheckModelConfig(context.Context) error })
	if !ok {
		return false
	}
	if err := c.CheckModelConfig(ctx); err != nil {
		logModelConfigError(w.logger, "history_summary", err)
		incWorkerMessages("history", w.tenantID, "error")
		observeWorkerDuration("history", w.tenantID, time.Since(start).Seconds())
		return true
	}
	return false
}

func (w *HistoryWorker) aggregateNext(ctx context.Context) bool {
	batch, err := w.repo.NextBatch(ctx, w.tenantID, constants.HistoryAggregationMinEntries, constants.HistoryAggregationBatchSize)
	if err != nil {
		w.logger.Warn("memory.history.batch_failed", zap.Error(err))
		return false
	}
	if batch == nil || len(batch.Entries) == 0 || w.summarizer == nil {
		return false
	}
	items := make([]string, 0, len(batch.Entries))
	for _, e := range batch.Entries {
		items = append(items, e.Content)
	}
	callCtx, cancel := context.WithTimeout(ctx, constants.HistoryOperationTimeout)
	summary, sumErr := w.summarizer.SummarizeHistory(callCtx, items)
	cancel()
	if sumErr != nil || strings.TrimSpace(summary) == "" {
		w.logger.Warn("memory.history.summarize_failed", zap.Error(sumErr))
		return false
	}
	first, last := batch.Entries[0], batch.Entries[len(batch.Entries)-1]
	sourceIDs := make([]string, 0, len(batch.Entries))
	for _, entry := range batch.Entries {
		sourceIDs = append(sourceIDs, entry.ID)
	}
	h := &domain.HistorySegment{TenantID: w.tenantID, ConversationID: batch.ConversationID, UserID: batch.UserID, AgentID: batch.AgentID, Scope: batch.Scope, Tier: domain.HistoryTierRecent, Summary: strings.TrimSpace(summary), SourceStart: first.ID, SourceEnd: last.ID, SourceIDs: sourceIDs, PeriodStart: first.CreatedAt, PeriodEnd: last.CreatedAt, Importance: .5, Confidence: .5, Status: domain.HistoryStatusActive}
	h.AggregationKey = domain.HistoryAggregationKey(h.UserID, h.AgentID, string(h.Scope), h.Tier, h.PeriodStart, h.PeriodEnd, h.SourceStart, h.SourceEnd)
	if err := w.repo.Upsert(ctx, w.tenantID, h); err != nil {
		w.logger.Warn("memory.history.upsert_failed", zap.Error(err))
		return false
	}
	return true
}

func (w *HistoryWorker) compressNext(ctx context.Context) {
	group, err := w.repo.NextOverflow(ctx, w.tenantID, constants.HistoryRecentMaxSegments, constants.HistoryEarlierMaxSegments, constants.HistoryAggregationBatchSize)
	if err != nil {
		w.logger.Warn("memory.history.overflow_failed", zap.Error(err))
		return
	}
	if group == nil || len(group.Sources) == 0 || w.compressor == nil {
		return
	}
	items := make([]string, 0, len(group.Sources))
	sourceIDs := make([]string, 0, len(group.Sources))
	underlyingIDs := make([]string, 0)
	seenUnderlyingIDs := make(map[string]struct{})
	importance, confidence := 0.0, 0.0
	periodStart, periodEnd := group.Sources[0].PeriodStart, group.Sources[0].PeriodEnd
	sourceStart, sourceEnd := group.Sources[0].SourceStart, group.Sources[0].SourceEnd
	for _, source := range group.Sources {
		if len(source.SourceIDs) == 0 {
			w.logger.Warn("memory.history.missing_source_ids",
				zap.String("history_id", source.ID),
			)
			return
		}
		items = append(items, source.Summary)
		sourceIDs = append(sourceIDs, source.ID)
		importance += source.Importance
		confidence += source.Confidence
		for _, id := range source.SourceIDs {
			if id == "" {
				continue
			}
			if _, exists := seenUnderlyingIDs[id]; exists {
				continue
			}
			seenUnderlyingIDs[id] = struct{}{}
			underlyingIDs = append(underlyingIDs, id)
		}
		if source.PeriodStart.Before(periodStart) || (source.PeriodStart.Equal(periodStart) && source.SourceStart < sourceStart) {
			periodStart, sourceStart = source.PeriodStart, source.SourceStart
		}
		if source.PeriodEnd.After(periodEnd) || (source.PeriodEnd.Equal(periodEnd) && source.SourceEnd > sourceEnd) {
			periodEnd, sourceEnd = source.PeriodEnd, source.SourceEnd
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, constants.HistoryOperationTimeout)
	summary, compressErr := w.compressor.CompressHistory(callCtx, items)
	cancel()
	if compressErr != nil || strings.TrimSpace(summary) == "" {
		w.logger.Warn("memory.history.compress_failed", zap.Error(compressErr))
		return
	}
	replacement := &domain.HistorySegment{
		TenantID: w.tenantID, ConversationID: group.ConversationID, UserID: group.UserID, AgentID: group.AgentID,
		Scope: group.Scope, Tier: domain.NextHistoryTier(group.Tier), Summary: strings.TrimSpace(summary),
		SourceStart: sourceStart, SourceEnd: sourceEnd, PeriodStart: periodStart, PeriodEnd: periodEnd,
		SourceIDs:  underlyingIDs,
		Importance: importance / float64(len(group.Sources)), Confidence: confidence / float64(len(group.Sources)), Status: domain.HistoryStatusActive,
	}
	replacement.AggregationKey = domain.HistoryCompressionKey(replacement.UserID, replacement.AgentID, string(replacement.Scope), replacement.Tier, sourceIDs)
	if err := w.repo.ReplaceOverflow(ctx, w.tenantID, replacement, sourceIDs); err != nil {
		w.logger.Warn("memory.history.replace_failed", zap.Error(err))
	}
}
