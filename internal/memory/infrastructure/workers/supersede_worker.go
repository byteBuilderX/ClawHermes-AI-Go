package workers

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/internal/memory/domain"
	"github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// SupersedeWorker periodically checks for superseded facts.
type SupersedeWorker struct {
	tenantID    string
	factRepo    port.FactRepo
	judge       port.LLMSuperseder
	vectorStore port.VectorStore
	logger      *zap.Logger
	stopCh      chan struct{}
	stopOnce    sync.Once
}

// NewSupersedeWorker creates a supersede worker for a specific tenant.
func NewSupersedeWorker(tenantID string, repo port.FactRepo, judge port.LLMSuperseder, logger *zap.Logger) *SupersedeWorker {
	return &SupersedeWorker{
		tenantID: tenantID,
		factRepo: repo,
		judge:    judge,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
}

// WithVectorStore wires the vector store used to delete superseded facts'
// vectors so stale content stops being recalled.
func (w *SupersedeWorker) WithVectorStore(vs port.VectorStore) *SupersedeWorker {
	w.vectorStore = vs
	return w
}

func (w *SupersedeWorker) Start(ctx context.Context) {
	runWithRestart(ctx, w.stopCh, w.logger, "memory.supersede_worker", w.run)
}

func (w *SupersedeWorker) run(ctx context.Context) {
	w.logger.Info("memory.supersede_worker.start")
	ticker := time.NewTicker(constants.MemoryProfileInterval)
	defer ticker.Stop()
	w.passGuarded(ctx)
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("memory.supersede_worker.context_cancelled")
			return
		case <-w.stopCh:
			w.logger.Info("memory.supersede_worker.stopped")
			return
		case <-ticker.C:
			w.passGuarded(ctx)
		}
	}
}

// passGuarded 执行一轮带模型配置预检的 supersede pass：模型未配置/解析失败时计
// error 并跳过 RunOnce，禁止「空跑一轮假 success」静默。disabled 判定由探针
// （enabled 目录比对）负责。从 run() 调用，保证定时与首拍两条路径都 fail-closed。
func (w *SupersedeWorker) passGuarded(ctx context.Context) {
	start := time.Now()
	if w.modelConfigUnavailable(ctx, start) {
		return
	}
	w.RunOnce(ctx)
}

// RunOnce performs a single supersede check pass with panic recovery.
func (w *SupersedeWorker) RunOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("memory.supersede_worker.panic",
				zap.Any("panic", r),
				zap.Stack("stack"))
			incWorkerPanics("supersede_worker")
		}
	}()

	start := time.Now()

	// Find recent active facts that might have supersede candidates
	// Using empty filter to check across all active facts (simplified for v1)
	recentFacts, err := w.factRepo.ListActive(ctx, w.tenantID, domain.ScopeFilter{IncludeUserScope: true, IncludeAgentScope: true}, constants.MemorySupersedeBatchSize)
	if err != nil {
		w.logger.Error("memory.supersede_worker.list_active_failed", zap.Error(err))
		incWorkerMessages("supersede", w.tenantID, "error")
		observeWorkerDuration("supersede", w.tenantID, time.Since(start).Seconds())
		return
	}

	if len(recentFacts) == 0 {
		incWorkerMessages("supersede", w.tenantID, "success")
		observeWorkerDuration("supersede", w.tenantID, time.Since(start).Seconds())
		return
	}

	supersededCount := 0
	llmCalls := 0
outer:
	for _, fact := range recentFacts {
		// Find candidates that this fact might supersede
		candidates, err := w.factRepo.FindSupersedeCandidates(
			ctx,
			fact.TenantID,
			domain.ScopeFilter{UserID: fact.UserID, AgentID: fact.AgentID, IncludeUserScope: fact.Scope == domain.ScopeUser, IncludeAgentScope: fact.Scope == domain.ScopeAgent},
			fact.Content,
			constants.MemorySupersedeCandidateMin,
			float64(constants.MemorySupersedeCandidateMax),
		)
		if err != nil {
			w.logger.Warn("memory.supersede_worker.find_candidates_failed",
				zap.String("fact_id", fact.ID),
				zap.Error(err))
			continue
		}

		for _, candidate := range candidates {
			// Skip self
			if candidate.Fact.ID == fact.ID {
				continue
			}

			// Skip already superseded
			if candidate.Fact.Status == "superseded" {
				continue
			}

			if llmCalls >= constants.MemorySupersedeLLMCallsPerRun {
				break outer
			}

			judgment, err := w.judge.JudgeSupersede(ctx, candidate.Fact.Content, fact.Content)
			llmCalls++
			if err != nil {
				w.logger.Warn("memory.supersede_worker.judge_failed",
					zap.String("old_fact_id", candidate.Fact.ID),
					zap.String("new_fact_id", fact.ID),
					zap.Error(err))
				continue
			}

			if judgment.Supersedes {
				if err := candidate.Fact.MarkSuperseded(fact.ID); err != nil {
					w.logger.Error("memory.supersede_worker.mark_failed",
						zap.String("fact_id", candidate.Fact.ID),
						zap.Error(err))
					continue
				}

				if err := w.factRepo.Update(ctx, fact.TenantID, candidate.Fact); err != nil {
					w.logger.Error("memory.supersede_worker.update_failed",
						zap.String("fact_id", candidate.Fact.ID),
						zap.Error(err))
					continue
				}
				w.deleteSupersededVector(ctx, fact.TenantID, candidate.Fact.ID)

				supersededCount++
				w.logger.Info("memory.supersede_worker.superseded",
					zap.String("old_fact_id", candidate.Fact.ID),
					zap.String("new_fact_id", fact.ID),
					zap.String("reason", judgment.Reason))
			}
		}
	}

	if supersededCount > 0 {
		w.logger.Info("memory.supersede_worker.batch_complete",
			zap.Int("superseded_count", supersededCount),
			zap.Int64("latency_ms", time.Since(start).Milliseconds()))
		incWorkerMessages("supersede", w.tenantID, "success")
		observeWorkerDuration("supersede", w.tenantID, time.Since(start).Seconds())
	}
}

// modelConfigUnavailable 预检 supersede 模型配置：judge 支持 CheckModelConfig 且
// 模型未配置/解析失败时返回 true（已计 error + duration），RunOnce 据此提前收尾，
// 避免把模型缺失当一轮 success 上报。
func (w *SupersedeWorker) modelConfigUnavailable(ctx context.Context, start time.Time) bool {
	c, ok := w.judge.(interface{ CheckModelConfig(context.Context) error })
	if !ok {
		return false
	}
	if err := c.CheckModelConfig(ctx); err != nil {
		logModelConfigError(w.logger, "supersede", err)
		incWorkerMessages("supersede", w.tenantID, "error")
		observeWorkerDuration("supersede", w.tenantID, time.Since(start).Seconds())
		return true
	}
	return false
}

// deleteSupersededVector removes the superseded fact's vector. Best-effort
// with ERROR surfacing: the daily GC purge backstops missed deletions once the
// row passes retention, and recall filters non-active facts regardless, so a
// failed delete never leaks stale facts into recall.
func (w *SupersedeWorker) deleteSupersededVector(ctx context.Context, tenantID, factID string) {
	if w.vectorStore == nil {
		return
	}
	if err := w.vectorStore.DeleteFactVectors(ctx, tenantID, []string{factID}); err != nil {
		w.logger.Error("memory.supersede_worker.vector_delete_failed",
			zap.String("tenant_id", tenantID),
			zap.String("fact_id", factID),
			zap.Error(err))
	}
}

// Stop gracefully stops the worker (idempotent).
func (w *SupersedeWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
	})
}
