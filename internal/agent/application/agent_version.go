// Agent product version history (generic resource_versions base) and rollback.

package application

import (
	"context"
	"fmt"
	"time"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	versioningdomain "github.com/byteBuilderX/stratum/internal/versioning/domain"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"go.uber.org/zap"
)

// VersionDTO is the wire shape for the generic product version history
// (resource_versions). Strings only — handler reuses field-for-field.
type VersionDTO struct {
	ID            string
	VersionNo     int
	Status        string
	Source        string
	ContentHash   string
	CreatedBy     string
	CreatedByName string
	CreatedAt     string // RFC3339
	PublishedAt   string // RFC3339；未发布为空串
	IsCurrent     bool
	SafeSummary   map[string]any
	// ParentVersionID 指向直父版本（版本创建时自链的前一最高 revision 行）；空串表示
	// 首版。供前端「详情」Drawer 以父版本整份 payload 为 before 基线现算字段 diff。
	ParentVersionID string
}

// VersionContentDTO 是单版本内容读接口的 wire shape：列表元数据 + 整份编辑面快照
// payload（snake_case 键）。前端取点击版与其直父版两次内容，交给共享 Drawer 递归
// diff 叶子字段。
type VersionContentDTO struct {
	VersionDTO
	Payload map[string]any
}

// RollbackAgentInput carries the actor performing the rollback and the target
// version (by ID) to restore.
type RollbackAgentInput struct {
	ActorID   string
	VersionID string
}

// ListVersions returns the agent's product version history (newest first) with
// created_by display names resolved. A missing VersionRepo fails closed — 装配
// 缺失不得静默返回空历史。
func (s *AgentService) ListVersions(ctx context.Context, id string) ([]VersionDTO, error) {
	if s.deps.VersionRepo == nil {
		return nil, fmt.Errorf("agent service list versions: version repo not wired")
	}
	tenantID := reqctx.TenantIDFromContext(ctx)
	versions, err := s.deps.VersionRepo.ListVersions(ctx, tenantID, versioningdomain.ResourceKindAgent, id)
	if err != nil {
		return nil, fmt.Errorf("agent service list versions: %w", err)
	}
	if err := s.resolveVersionNames(ctx, versions); err != nil {
		return nil, err
	}
	out := make([]VersionDTO, 0, len(versions))
	for _, v := range versions {
		out = append(out, versionToDTO(v))
	}
	return out, nil
}

// resolveVersionNames 批量填充版本 created_by 的展示名（display-only）。未注入
// 解析器时跳过（raw id 展示）；注入后查询失败传播错误（fail-closed）；查不到的
// actor 不在映射中，回退原文。与 skill 版本服务的解析语义一致。
func (s *AgentService) resolveVersionNames(ctx context.Context, versions []versioningdomain.Version) error {
	if s.deps.ActorNameResolver == nil {
		return nil
	}
	ids := make([]string, 0, len(versions))
	for _, v := range versions {
		if v.CreatedBy != "" {
			ids = append(ids, v.CreatedBy)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names, err := s.deps.ActorNameResolver.ResolveActorNames(ctx, ids)
	if err != nil {
		return fmt.Errorf("agent service resolve version names: %w", err)
	}
	for i := range versions {
		versions[i].CreatedByName = names[versions[i].CreatedBy]
	}
	return nil
}

// versionToDTO renders a versioningdomain.Version for transport. Times are
// RFC3339 strings; an unpublished version's PublishedAt is an empty string.
func versionToDTO(v versioningdomain.Version) VersionDTO {
	var publishedAt string
	if v.PublishedAt != nil {
		publishedAt = v.PublishedAt.UTC().Format(time.RFC3339)
	}
	return VersionDTO{
		ID:              v.ID,
		VersionNo:       v.RevisionNo,
		Status:          string(v.Status),
		Source:          string(v.Source),
		ContentHash:     v.ContentHash,
		CreatedBy:       v.CreatedBy,
		CreatedByName:   v.CreatedByName,
		CreatedAt:       v.CreatedAt.UTC().Format(time.RFC3339),
		PublishedAt:     publishedAt,
		IsCurrent:       v.IsCurrent,
		SafeSummary:     v.SafeSummary,
		ParentVersionID: v.ParentVersionID,
	}
}

// versionContentToDTO extends versionToDTO with the full edit-surface payload
// snapshot (the agent config map persisted at version creation time).
func versionContentToDTO(v versioningdomain.Version) VersionContentDTO {
	return VersionContentDTO{
		VersionDTO: versionToDTO(v),
		Payload:    v.Payload,
	}
}

// GetVersion returns one historical version's full content snapshot (metadata
// + payload), which the frontend "detail" view diffs against the direct parent.
// A missing/unknown version fails with versioningdomain.ErrVersionNotFound
// (mapped 404); a missing assembly fails closed with a 500-class error.
func (s *AgentService) GetVersion(ctx context.Context, id, versionID string) (VersionContentDTO, error) {
	if s.deps.VersionRepo == nil {
		return VersionContentDTO{}, fmt.Errorf("agent service get version: version repo not wired")
	}
	tenantID := reqctx.TenantIDFromContext(ctx)
	v, found, err := s.deps.VersionRepo.GetVersion(ctx, tenantID, versioningdomain.ResourceKindAgent, id, versionID)
	if err != nil {
		return VersionContentDTO{}, fmt.Errorf("agent service get version: %w", err)
	}
	if !found {
		return VersionContentDTO{}, versioningdomain.ErrVersionNotFound
	}
	// 单版本经 slice 解析展示名（resolveVersionNames 就地改元素），再映射 DTO。
	one := []versioningdomain.Version{v}
	if err := s.resolveVersionNames(ctx, one); err != nil {
		return VersionContentDTO{}, err
	}
	return versionContentToDTO(one[0]), nil
}

// Rollback restores a deprecated product version of an agent. It rebuilds the
// agent config from the target version's snapshot payload, applies it through
// Registry.Rollback (one transaction: re-validate editor, UPDATE agents,
// replace bindings, promote target, repoint active_version_id, audit), then
// re-reads and returns the fresh config. Rolling back does not create a new
// version (matches skill semantics).
//
// A missing agent fails with ErrNotFound; an unknown or non-deprecated target
// fails with versioningdomain.ErrVersionNotFound (only deprecated historical
// versions may be rolled back).
func (s *AgentService) Rollback(ctx context.Context, id string, in RollbackAgentInput) (AgentDTO, error) {
	existing, ok, err := s.deps.Registry.Get(ctx, id)
	if err != nil {
		return AgentDTO{}, fmt.Errorf("agent service rollback: %w", err)
	}
	if !ok {
		return AgentDTO{}, ErrNotFound
	}
	editorActor, err := s.resolveUpdateEditorActor(ctx, in.ActorID, id, existing.GetConfig().CreatedBy)
	if err != nil {
		return AgentDTO{}, err
	}
	target, err := s.loadRollbackTarget(ctx, id, in.VersionID)
	if err != nil {
		return AgentDTO{}, err
	}
	snapshot, err := domain.SnapshotFromMap(target.Payload)
	if err != nil {
		return AgentDTO{}, fmt.Errorf("agent service rollback: parse version payload: %w", err)
	}
	cfg := snapshot.ToConfig(id, existing.GetConfig().CreatedBy)
	audit, err := newChangeAudit(ctx, auditdomain.ResourceKindAgent, id, auditdomain.ChangeOpRollback, in.ActorID,
		AgentSafeProjection(existing.GetConfig()), AgentSafeProjection(cfg))
	if err != nil {
		return AgentDTO{}, err
	}
	if err := s.deps.Registry.Rollback(ctx, cfg, audit, editorActor, in.VersionID); err != nil {
		s.recordFailure(ctx, id, "rollback", err)
		return AgentDTO{}, err
	}
	s.deps.Logger.Info("agent rolled back", zap.String("id", id), zap.String("version_id", in.VersionID))
	// 回读而非返回内存 DTO：API 断言必须以 DB 为准（防假绿）。
	fresh, ok, err := s.deps.Registry.Get(ctx, id)
	if err != nil {
		return AgentDTO{}, fmt.Errorf("agent service rollback: re-read: %w", err)
	}
	if !ok {
		return AgentDTO{}, ErrNotFound
	}
	return cfgToDTO(fresh.GetConfig()), nil
}

// loadRollbackTarget 校验版本基座装配并取回目标版本：仅 deprecated 历史版本
// 可回滚（published 当前版本与不存在的版本统一返回 ErrVersionNotFound，映射
// 404）。校验集中在 helper 以控制 Rollback 主函数复杂度。
func (s *AgentService) loadRollbackTarget(ctx context.Context, id, versionID string) (versioningdomain.Version, error) {
	if s.deps.VersionRepo == nil {
		return versioningdomain.Version{}, fmt.Errorf("agent service rollback: version repo not wired")
	}
	tenantID := reqctx.TenantIDFromContext(ctx)
	target, found, err := s.deps.VersionRepo.GetVersion(ctx, tenantID, versioningdomain.ResourceKindAgent, id, versionID)
	if err != nil {
		return versioningdomain.Version{}, fmt.Errorf("agent service rollback: %w", err)
	}
	if !found || target.Status != versioningdomain.VersionStatusDeprecated {
		return versioningdomain.Version{}, versioningdomain.ErrVersionNotFound
	}
	return target, nil
}
