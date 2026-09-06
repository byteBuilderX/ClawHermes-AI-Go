package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"go.uber.org/zap"
)

// errNoRollbackCandidate 产品路径找不到上一好版本（如唯一 published 版本即坏版）。
var errNoRollbackCandidate = errors.New("resource rollback: no previous good version")

// ---------------------------------------------------------------------------
// Planner 纯函数（零 IO；输入由 executor 经窄 port 拉取）
// ---------------------------------------------------------------------------

// previousGoodVersion 返回候选里可回滚的上一好版本：非 IsCurrent、Rollbackable、
// RevisionNo 最高者（对输入顺序不敏感，表驱动覆盖乱序）。
func previousGoodVersion(candidates []port.RollbackCandidate) (port.RollbackCandidate, bool) {
	best, ok := port.RollbackCandidate{}, false
	for _, c := range candidates {
		if c.IsCurrent || !c.Rollbackable {
			continue
		}
		if !ok || c.RevisionNo > best.RevisionNo {
			best, ok = c, true
		}
	}
	return best, ok
}

// isCanaryBadState 判定观测锚定的坏版本是否为金丝雀版本：有 deployment、canary 非空、
// 且 observedRevisionID 命中 canary。命中 → 走 spec §3.3 path 1（清 canary）；否则 path 2。
func isCanaryBadState(dep domain.Deployment, found bool, observedRevisionID string) bool {
	if !found || dep.CanaryRevisionID == "" || observedRevisionID == "" {
		return false
	}
	return observedRevisionID == dep.CanaryRevisionID
}

// ---------------------------------------------------------------------------
// Executor：实现 port.ResourceRollbackExecutor（E2 6 参签名）
// ---------------------------------------------------------------------------

// ResourceRollbackExecutorDeps 是 executor 的窄依赖。products 至少含一个 kind 时
// executor 才有实际可回滚能力；canary 可为 nil（跳过金丝雀判定，只走产品路径）。
type ResourceRollbackExecutorDeps struct {
	Logger   *zap.Logger
	Products map[domain.ResourceKind]port.ProductRollbackBackend // agent/knowledge/skill
	Canary   port.CanaryRollbackBackend                          // 可选
}

// ResourceRollbackExecutor 是无状态执行器（goroutine-safe），按 target 分派到
// canary 后端或对应 kind 产品后端。mcp / 未知 kind / 非资源 scope → ErrRollbackUnsupported。
type ResourceRollbackExecutor struct {
	deps ResourceRollbackExecutorDeps
}

var _ port.ResourceRollbackExecutor = (*ResourceRollbackExecutor)(nil)

// NewResourceRollbackExecutor 构造执行器；Logger 缺省 zap.NewNop()。
func NewResourceRollbackExecutor(deps ResourceRollbackExecutorDeps) *ResourceRollbackExecutor {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	return &ResourceRollbackExecutor{deps: deps}
}

// Rollback 实现 port.ResourceRollbackExecutor。auto 路径 actor="gate"/decidedBy=""/approvalID=""
// （gate_service.execAutoRollback）；manual 路径 decidedBy=审批人/approvalID=审批 id（Task 4
// executeResourceRollback）。有效执行者 = decidedBy（有则取）否则 actor，作为审计 actor 透传。
func (e *ResourceRollbackExecutor) Rollback(ctx context.Context, tenantID string, target domain.GateTarget,
	actor, decidedBy, approvalID string) error {
	if target.Scope != domain.ScopeResource {
		return fmt.Errorf("resource rollback: unsupported scope %q: %w", target.Scope, port.ErrRollbackUnsupported)
	}
	kind := domain.ResourceKind(target.Kind)
	backend, ok := e.deps.Products[kind]
	if !ok {
		// mcp 无产品链；未知 kind 亦不支持（fail-closed，不静默降级）。
		return fmt.Errorf("resource rollback: kind %q has no product rollback backend: %w", kind, port.ErrRollbackUnsupported)
	}
	actingUser := decidedBy
	if actingUser == "" {
		actingUser = actor
	}

	// path 1：金丝雀坏（observedRevision 命中 canary）→ 清 canary 回 stable，全部 kind 通用。
	// handled=true 表示该分支已消费本次回滚（成功或已带错返回），不再走产品路径。
	handled, err := e.rollbackCanary(ctx, tenantID, kind, target, actingUser, approvalID)
	if handled || err != nil {
		return err
	}

	// path 2：产品生效版本坏 → 回滚到上一好版本（deprecated 历史版本）。
	candidates, err := backend.ListCandidates(ctx, tenantID, target.ResourceID)
	if err != nil {
		return fmt.Errorf("resource rollback: list candidates %s/%s: %w", kind, target.ResourceID, err)
	}
	good, ok := previousGoodVersion(candidates)
	if !ok {
		return fmt.Errorf("resource rollback: %s/%s: %w", kind, target.ResourceID, errNoRollbackCandidate)
	}
	if err := backend.RollbackProduct(ctx, tenantID, target.ResourceID, good.ID, actingUser); err != nil {
		return fmt.Errorf("resource rollback: %s/%s to %s: %w", kind, target.ResourceID, good.ID, err)
	}
	e.deps.Logger.Info("resource rollback: product rolled back to previous good",
		zap.String("tenant", tenantID), zap.String("kind", string(kind)),
		zap.String("resource", target.ResourceID), zap.String("version", good.ID),
		zap.String("actor", actingUser))
	return nil
}

// rollbackCanary 处理金丝雀坏状态（spec §3.3 path 1）。Canary 未装配或观测未命中
// canary → (false, nil) 由调用方继续产品路径；清除成功 → (true, nil)；判定/清除失败 →
// (false|true, err)。独立成函数以控制 Rollback 圈复杂度（质量门禁 CC≤10）。
func (e *ResourceRollbackExecutor) rollbackCanary(ctx context.Context, tenantID string,
	kind domain.ResourceKind, target domain.GateTarget, actingUser, approvalID string,
) (bool, error) {
	if e.deps.Canary == nil {
		return false, nil
	}
	dep, found, err := e.deps.Canary.ResolveDeployment(ctx, tenantID, kind, target.ResourceID)
	if err != nil {
		return false, fmt.Errorf("resource rollback: resolve deployment %s/%s: %w", kind, target.ResourceID, err)
	}
	if !isCanaryBadState(dep, found, target.RevisionID) {
		return false, nil
	}
	reason := fmt.Sprintf("gate rollback: revision %s judged bad (approval %s)", target.RevisionID, approvalID)
	if err := e.deps.Canary.ClearCanary(ctx, tenantID, dep.ExperimentID, actingUser, reason); err != nil {
		return true, fmt.Errorf("resource rollback: clear canary %s/%s: %w", kind, target.ResourceID, err)
	}
	e.deps.Logger.Info("resource rollback: canary cleared",
		zap.String("tenant", tenantID), zap.String("kind", string(kind)),
		zap.String("resource", target.ResourceID), zap.String("experiment", dep.ExperimentID))
	return true, nil
}
