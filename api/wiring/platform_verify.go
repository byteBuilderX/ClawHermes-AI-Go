package wiring

import (
	"context"

	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evalpersist "github.com/byteBuilderX/stratum/internal/evaluation/infrastructure/persistence"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// buildPlatformVerifyWorker 装配多租户验证 worker（spec §3.4-3）并注册 Start/Stop。
// 独立 build step（{"platform-verify-worker", ...}），不触碰 buildEvaluation body：
// 质量门禁基线限定 buildEvaluation 行数。与评测 worker 同一 evaluationTenantLister
// （同一 db 上的租户轮询），各自 Claim 互不重叠（本 worker 走 job_type='platform_verify'
// 的 ClaimPlatformVerify）。
func (c *Container) buildPlatformVerifyWorker(ctx context.Context) error {
	db := c.dbOrNil()
	if db == nil || c.Evaluation == nil || c.Evaluation.Worker == nil {
		return nil // 评测组件未装配（degraded）→ 验证 worker 无宿主租户/DB，跳过
	}
	runner := evalapp.NewMultiTenantVerifyRunner(evalapp.MultiTenantVerifyDeps{
		Logger:  c.Logger,
		Metrics: c.platformMetrics(),
		Repo:    evalpersist.NewPgJobRepository(db),
		Runs:    evalpersist.NewPgRunRepository(db),
		Compare: runCompareAdapter, // Task 1 CompareRunRegression 适配；语义：Compare(坏, 好) → Regressed=true=未恢复
	})
	worker := evalapp.NewWorker(
		evaluationTenantLister{pool: db},
		runner,
		constants.EvaluationIdleInterval,
		c.platformMetrics(),
	)
	worker.Start(ctx)
	c.shutdown = append(c.shutdown, func(context.Context) error { worker.Stop(); return nil })
	return nil
}
