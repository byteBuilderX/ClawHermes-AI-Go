package application

import (
	"context"
	"fmt"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

// VerifyIdempotencyKey 生成确定性幂等键（tenant 表 UNIQUE(idempotency_key) 去重；
// 同组同 from/to seq 只跑一次，回滚重复触发不叠加验证任务）。
func VerifyIdempotencyKey(groupKey string, fromSeq, toSeq int64) string {
	return fmt.Sprintf("platform_verify:%s:%d:%d", groupKey, fromSeq, toSeq)
}

// EnqueueMultiTenantVerify 平台回滚成功后调用（调用点延迟 T13，见「开放问题 #2」；
// Task 4 executePlatformRollback 成功路径由 T13 接线）。幂等键冲突（已存在）→
// inserted=false → 去重不重复 +queued。
func EnqueueMultiTenantVerify(
	ctx context.Context,
	repo port.JobPlatformVerifyRepo,
	tenantID string,
	p domain.PlatformVerifyPayload,
	createdBy string,
	metrics observability.MetricsProvider,
) error {
	inserted, err := repo.EnqueuePlatformVerify(ctx, tenantID, p, VerifyIdempotencyKey(p.GroupKey, p.FromSeq, p.ToSeq), createdBy)
	if err != nil {
		return fmt.Errorf("enqueue platform verify: %w", err)
	}
	if inserted && metrics != nil {
		metrics.IncEvalGateAction(domain.LayerL3MultiTenantVerify, domain.ActionQueued)
	}
	return nil
}

// MultiTenantVerifyDeps 是 MultiTenantVerifyRunner 的依赖（wiring 组装；Compare 绑
// runCompareAdapter，Repo/Runs 用 tenant-scoped repo 窄接口）。
type MultiTenantVerifyDeps struct {
	Logger  *zap.Logger
	Metrics observability.MetricsProvider
	Repo    port.JobPlatformVerifyRepo
	Runs    port.RunRepository
	// Compare 用 Task 1 run 级回归（runCompareAdapter）；语义注释见 RunOnce：本 runner
	// 传参 (坏版本 run, 好版本 run)，与哨兵判定方向相反。
	Compare func(baseline, current *domain.EvalRun) (domain.RunComparison, error)
}

type MultiTenantVerifyRunner struct {
	deps MultiTenantVerifyDeps
}

func NewMultiTenantVerifyRunner(deps MultiTenantVerifyDeps) *MultiTenantVerifyRunner {
	return &MultiTenantVerifyRunner{deps: deps}
}

// RunOnce 实现 TenantJobRunner：每租户 Claim 一条 platform_verify；拿到 job 后转
// verify 处理锚定 run 对照。claim 失败返回 err（worker 上报单租户失败，job 可重拾）；
// 无 job = 空转。拆 verify helper 保持 RunOnce 圈复杂度在门禁内。
func (r *MultiTenantVerifyRunner) RunOnce(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	job, err := r.deps.Repo.ClaimPlatformVerify(ctx, tenantID, workerID, lease)
	if err != nil {
		if r.deps.Metrics != nil {
			r.deps.Metrics.IncEvaluationJob("platform_verify_error") // 既有 eval 工作计数维度，不加新 family
		}
		return false, err
	}
	if job == nil {
		return false, nil
	}
	return r.verify(ctx, tenantID, job)
}

// verify 用 FindLatestCompletedRunForPlatformSeq 取 from/to 锚定 run → Compare(坏, 好)
// → 好不劣于坏 = recovered，否则 not_recovered（R31 计数）。run 缺失 = 无信号 → 跳过
// 不发计数。per-tenant fail-open：单租户查询/对照失败只上报该租户，不让整体 worker 停摆
// （job 留 running 可重试）。
func (r *MultiTenantVerifyRunner) verify(
	ctx context.Context,
	tenantID string,
	job *domain.PlatformVerifyJob,
) (bool, error) {
	fromRun, err := r.deps.Runs.FindLatestCompletedRunForPlatformSeq(ctx, tenantID, job.Payload.GroupKey, job.Payload.FromSeq)
	if err != nil {
		return true, fmt.Errorf("verify from-seq lookup: %w", err)
	}
	toRun, err := r.deps.Runs.FindLatestCompletedRunForPlatformSeq(ctx, tenantID, job.Payload.GroupKey, job.Payload.ToSeq)
	if err != nil {
		return true, fmt.Errorf("verify to-seq lookup: %w", err)
	}
	if fromRun == nil || toRun == nil {
		return true, nil // 无信号租户：跳过（不产生 recovered/not_recovered 计数）
	}
	cmp, err := r.deps.Compare(fromRun, toRun)
	if err != nil {
		return true, fmt.Errorf("verify compare: %w", err)
	}
	if r.deps.Metrics != nil {
		if cmp.Regressed {
			r.deps.Metrics.IncEvalGateAction(domain.LayerL3MultiTenantVerify, domain.ActionNotRecovered)
		} else {
			r.deps.Metrics.IncEvalGateAction(domain.LayerL3MultiTenantVerify, domain.ActionRecovered)
		}
	}
	return true, nil
}
