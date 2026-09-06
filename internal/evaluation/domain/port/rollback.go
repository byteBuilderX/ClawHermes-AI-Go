package port

import (
	"context"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

// RollbackCandidate 是产品回滚入口的「可回滚历史版本」归一化候选。ACL 适配器按
// kind 语义填充（agent/knowledge 取 versioningdomain.VersionStatusDeprecated；
// skill 取 skilldomain.VersionStatusDeprecated），executor 纯函数在其上选上一好。
type RollbackCandidate struct {
	ID           string // 服务层版本/修订行主键（agent/knowledge version id；skill revision id）
	RevisionNo   int
	IsCurrent    bool
	Rollbackable bool // deprecated 历史版本（可被 Rollback/RollbackRevision 接受）
}

// ProductRollbackBackend 是单个资源 kind 的产品回滚适配面（spec §3.3 path 2）。
// provider：api/wiring 中基于真实 service 的 ACL 适配器（agent/knowledge/skill 各一），
// 由 executor 按 target.Kind 选取。禁止在 evaluation 内部 import 兄弟 context infra。
type ProductRollbackBackend interface {
	// ListCandidates 返回该资源全部可回滚候选（deprecated 历史版本，newest-first）。
	// knowledge 的 resourceID = workspace name（与 evaluation 资源锚点一致）。
	ListCandidates(ctx context.Context, tenantID, resourceID string) ([]RollbackCandidate, error)
	// RollbackProduct 把 resourceID 回滚到 candidateID（服务层内部单事务 + 自带
	// ChangeOpRollback 审计；仅 deprecated 目标可回滚）。
	RollbackProduct(ctx context.Context, tenantID, resourceID, candidateID, actorID string) error
}

// CanaryRollbackBackend 是金丝雀坏状态的判定与清除适配面（spec §3.3 path 1）。
// provider：api/wiring 适配器（experimentRepo.ResolveDeployment + ExperimentService.Rollback）。
type CanaryRollbackBackend interface {
	// ResolveDeployment 返回资源当前 deployment（无实验 → ok=false）。
	ResolveDeployment(ctx context.Context, tenantID string, kind domain.ResourceKind,
		resourceID string) (domain.Deployment, bool, error)
	// ClearCanary 通过 ExperimentService.Rollback(CommandRollback) 清 canary，流量回 stable。
	ClearCanary(ctx context.Context, tenantID, experimentID, actorID, reason string) error
}
