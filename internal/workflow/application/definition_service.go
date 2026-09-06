package application

import (
	"context"
	"fmt"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	auditport "github.com/byteBuilderX/stratum/internal/audit/domain/port"
	"github.com/byteBuilderX/stratum/internal/workflow/domain"
	"github.com/byteBuilderX/stratum/internal/workflow/domain/port"
	"go.uber.org/zap"
)

type CreateDefinitionCommand struct {
	Name        string
	Description string
	Spec        domain.Spec
	InputSchema domain.InputSchema
}

type UpdateDefinitionCommand struct {
	Name             string
	Description      string
	Spec             domain.Spec
	InputSchema      domain.InputSchema
	ExpectedRevision int64
}

type DefinitionService struct {
	definitions  port.DefinitionRepository
	versions     port.VersionRepository
	newID        func() string
	failureAudit auditport.FailureAuditRecorder
	bindings     port.SkillBindingResolver
	roles        port.TenantRoleResolver
	editors      port.ResourceEditorRepo
	nameResolver port.ActorNameResolver
	logger       *zap.Logger
}

func NewDefinitionService(definitions port.DefinitionRepository, versions port.VersionRepository, newID func() string) *DefinitionService {
	return &DefinitionService{definitions: definitions, versions: versions, newID: newID, logger: zap.NewNop()}
}

// SetFailureAuditRecorder 注入失败资源操作审计。未注入时跳过记录。
func (s *DefinitionService) SetFailureAuditRecorder(r auditport.FailureAuditRecorder) {
	s.failureAudit = r
}

// SetSkillBindingResolver 注入 agent 技能绑定解析器，用于校验 skill 节点的
// agent-skill 引用关系。未注入时跳过绑定校验（测试/降级）。
func (s *DefinitionService) SetSkillBindingResolver(r port.SkillBindingResolver) {
	s.bindings = r
}

// validateSkillBindings 校验 spec 中所有 skill 节点引用的 agent 确实启用了该技能。
// resolver 存在但查询失败则传播错误（fail-closed），不允许静默放行。
func (s *DefinitionService) validateSkillBindings(ctx context.Context, tenantID string, spec domain.Spec) error {
	if s.bindings == nil {
		return nil
	}
	cache := make(map[string][]string, len(spec.Nodes))
	for _, node := range spec.Nodes {
		if node.Type != domain.NodeTypeSkill || node.AgentID == "" || node.SkillID == "" {
			continue
		}
		allowed, ok := cache[node.AgentID]
		if !ok {
			var err error
			allowed, err = s.bindings.AgentAllowedSkills(ctx, tenantID, node.AgentID)
			if err != nil {
				return err
			}
			cache[node.AgentID] = allowed
		}
		if err := domain.ValidateSkillBinding(allowed, node.SkillID); err != nil {
			return err
		}
	}
	return nil
}

// SetLogger 注入日志器（默认 Nop，测试与生产均可覆盖）。
func (s *DefinitionService) SetLogger(l *zap.Logger) {
	if l != nil {
		s.logger = l
	}
}

// SetTenantRoleResolver 注入租户角色解析器（单事实源），所有权矩阵据此判定。
// 未注入时 fail closed（禁止一切 Update/Delete/Publish）。
func (s *DefinitionService) SetTenantRoleResolver(r port.TenantRoleResolver) {
	s.roles = r
}

// SetEditorRepo 注入可编辑人白名单仓库。未注入时白名单成员路径 fail closed。
func (s *DefinitionService) SetEditorRepo(r port.ResourceEditorRepo) {
	s.editors = r
}

// SetActorNameResolver 注入发布者昵称解析器（iam 基础设施实现），由 wiring 装配，
// 供版本历史「操作者」列展示。未注入时保留 raw id（降级）；注入后查询失败必须
// 传播错误（fail-closed：禁止默认名掩盖查询故障）。
func (s *DefinitionService) SetActorNameResolver(r port.ActorNameResolver) {
	s.nameResolver = r
}
func (s *DefinitionService) Create(ctx context.Context, tenantID string, cmd CreateDefinitionCommand, actorID string) (*domain.Definition, error) {
	definition, err := domain.NewDefinition(s.newID(), cmd.Name, cmd.Description, cmd.Spec, normalizeInputSchema(cmd.InputSchema))
	if err != nil {
		return nil, err
	}
	// 落库即写回 creator：历史租户的空 created_by 行由 owner/admin 空 actor 路径
	// 维护；新行一律带创建者，供所有权矩阵 admin 判定。
	definition.CreatedBy = actorID
	// 草稿保存也强制图完整性（含环检测）：用户拖拽新边时前端已阻止成环，
	// 这里作为 fail-closed 兜底，避免非法拓扑流入存储。允许空图（画一半先保存）。
	if err := domain.ValidateSpecGraph(definition.Spec); err != nil {
		return nil, err
	}
	if err := s.validateSkillBindings(ctx, tenantID, definition.Spec); err != nil {
		return nil, err
	}
	ev, err := newWorkflowChangeAudit(definition.ID, auditdomain.ChangeOpCreate, actorID, nil, workflowSafeProjection(definition))
	if err != nil {
		return nil, err
	}
	if err := s.definitions.CreateDefinition(ctx, tenantID, definition, ev); err != nil {
		s.recordFailure(ctx, definition.ID, "create", err)
		return nil, err
	}
	return definition, nil
}

func (s *DefinitionService) Update(ctx context.Context, tenantID, id string, cmd UpdateDefinitionCommand, actorID string) (*domain.Definition, error) {
	definition, err := s.definitions.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	// 所有权判定：owner/admin 放行（空 editorActor），白名单 member 放行并带
	// 非空 editorActor，store 在写事务内复查关闭 TOCTOU。
	editorActor, err := s.resolveUpdateActor(ctx, tenantID, actorID, definition)
	if err != nil {
		return nil, err
	}
	before := workflowSafeProjection(definition)
	if err := definition.UpdateDraft(cmd.Name, cmd.Description, cmd.Spec, cmd.ExpectedRevision, normalizeInputSchema(cmd.InputSchema)); err != nil {
		return nil, err
	}
	// 与 Create 一致：草稿更新强制图完整性（含环检测），fail-closed。
	if err := domain.ValidateSpecGraph(definition.Spec); err != nil {
		return nil, err
	}
	if err := s.validateSkillBindings(ctx, tenantID, definition.Spec); err != nil {
		return nil, err
	}
	ev, err := newWorkflowChangeAudit(id, auditdomain.ChangeOpUpdate, actorID, before, workflowSafeProjection(definition))
	if err != nil {
		return nil, err
	}
	if err := s.definitions.UpdateDefinition(ctx, tenantID, definition, cmd.ExpectedRevision, editorActor, ev); err != nil {
		s.recordFailure(ctx, id, "update", err)
		return nil, err
	}
	return definition, nil
}

func (s *DefinitionService) Delete(ctx context.Context, tenantID, id string, actorID string) error {
	definition, err := s.definitions.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return err
	}
	// 破坏性删除仅 owner 与 creator-admin 可执行（member 一律拒绝）。
	if err := s.checkOwnership(ctx, tenantID, actorID, definition.CreatedBy, nil, OpDelete); err != nil {
		return err
	}
	ev, err := newWorkflowChangeAudit(id, auditdomain.ChangeOpDelete, actorID, workflowSafeProjection(definition), nil)
	if err != nil {
		return err
	}
	return s.definitions.DeleteDefinition(ctx, tenantID, id, ev)
}

func normalizeInputSchema(schema domain.InputSchema) domain.InputSchema {
	if schema.TaskLabel == "" && schema.TaskDescription == "" && len(schema.Fields) == 0 {
		return domain.InputSchema{TaskLabel: "任务", Fields: []domain.InputField{}}
	}
	return schema
}

func (s *DefinitionService) Validate(ctx context.Context, tenantID, id string) error {
	definition, err := s.definitions.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return err
	}
	return domain.ValidateSpec(definition.Spec)
}

func (s *DefinitionService) Get(ctx context.Context, tenantID, id string) (*domain.Definition, error) {
	definition, err := s.definitions.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	// 详情附带白名单 editors；editorRepo 未装配时保持现状（只读路径不 fail-closed）。
	if s.editors != nil {
		editors, listErr := s.editors.ListEditors(ctx, tenantID, definition.ID)
		if listErr != nil {
			return nil, listErr
		}
		definition.Editors = editors
	}
	return definition, nil
}

// ListEditors returns the granted editor ids for a workflow. editorRepo 未装配
// 时返回空列表（只读路径不 fail-closed）。
func (s *DefinitionService) ListEditors(ctx context.Context, tenantID, workflowID string) ([]string, error) {
	if s.editors == nil {
		return []string{}, nil
	}
	return s.editors.ListEditors(ctx, tenantID, workflowID)
}

// SetEditors replaces the whitelist. 仅 owner/admin（OpAccess）可管理；
// member 一律拒绝。变更写入同事务并记录审计。
func (s *DefinitionService) SetEditors(ctx context.Context, tenantID, workflowID string, editorIDs []string, actorID string) error {
	if s.editors == nil {
		return fmt.Errorf("workflow set editors: editor repo not wired")
	}
	current, err := s.definitions.GetDefinition(ctx, tenantID, workflowID)
	if err != nil {
		return err
	}
	if err := s.checkOwnership(ctx, tenantID, actorID, current.CreatedBy, nil, OpAccess); err != nil {
		return err
	}
	before, err := s.editors.ListEditors(ctx, tenantID, current.ID)
	if err != nil {
		return fmt.Errorf("workflow set editors: list: %w", err)
	}
	audit, err := newWorkflowChangeAudit(current.ID, auditdomain.ChangeOpUpdate, actorID,
		workflowSafeProjectionWithEditors(current, before), workflowSafeProjectionWithEditors(current, editorIDs))
	if err != nil {
		return err
	}
	if err := s.editors.ReplaceEditors(ctx, tenantID, current.ID, editorIDs, actorID, audit); err != nil {
		return err
	}
	s.logger.Info("workflow editors updated",
		zap.String("workflow", workflowID), zap.Int("editors", len(editorIDs)))
	return nil
}

func (s *DefinitionService) GetVersion(ctx context.Context, tenantID, id string) (*domain.Version, error) {
	return s.versions.GetVersion(ctx, tenantID, id)
}

func (s *DefinitionService) Publish(ctx context.Context, tenantID, id string, actorID string) (*domain.Version, error) {
	definition, err := s.definitions.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	// 所有权判定与 Update 一致：白名单 member 经 editorActor 传参由原子发布器
	// 在写事务内复查白名单，owner/admin 走空 actor 路径。
	editorActor, err := s.resolveUpdateActor(ctx, tenantID, actorID, definition)
	if err != nil {
		return nil, err
	}
	// 发布前同样校验 skill 节点绑定关系：发布即对外生效，非法绑定必须 fail-closed。
	if err := s.validateSkillBindings(ctx, tenantID, definition.Spec); err != nil {
		return nil, err
	}
	projection := workflowSafeProjection(definition)
	ev, err := newWorkflowChangeAudit(id, auditdomain.ChangeOpPublish, actorID, projection, projection)
	if err != nil {
		return nil, err
	}
	if publisher, ok := s.versions.(port.AtomicVersionPublisher); ok {
		version, err := publisher.CreateNextVersion(ctx, tenantID, definition, s.newID(), editorActor, ev)
		if err != nil {
			s.recordFailure(ctx, id, "publish", err)
			return nil, err
		}
		return version, nil
	}
	number, err := s.versions.NextVersionNumber(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	version, err := definition.Publish(s.newID(), number)
	if err != nil {
		return nil, err
	}
	// 把发布者记录为版本 created_by（落库 + 返回体），使版本历史「操作者」可溯源；
	// atomic 路径由 CreateNextVersion 从 ev.ActorID 注入，两条发布路径一致。
	version.CreatedBy = actorID
	if err := s.versions.CreateVersion(ctx, tenantID, version, ev); err != nil {
		s.recordFailure(ctx, id, "publish", err)
		return nil, err
	}
	return version, nil
}

// Rollback 把生效指针指回历史已发布版本，不产生新版本。
// 目标版本必须存在且归属于同一工作流，否则 fail-closed 返回 ErrNotFound。
func (s *DefinitionService) Rollback(ctx context.Context, tenantID, id, versionID string, actorID string) (*domain.Definition, error) {
	definition, err := s.definitions.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	// 回退生效指针与删除同属高破坏性写操作：仅 owner/admin 可执行（admin 无需本人
	// 为 creator），白名单 member 一律拒绝。检查须在任何版本写入之前 fail-closed。
	if err := s.checkOwnership(ctx, tenantID, actorID, definition.CreatedBy, nil, OpRollback); err != nil {
		return nil, err
	}
	version, err := s.versions.GetVersion(ctx, tenantID, versionID)
	if err != nil {
		return nil, err
	}
	if version.DefinitionID != definition.ID {
		return nil, domain.ErrNotFound
	}
	before := workflowSafeProjection(definition)
	after := workflowSafeProjection(definition)
	after["active_version_id"] = versionID
	ev, err := newWorkflowChangeAudit(id, auditdomain.ChangeOpRollback, actorID, before, after)
	if err != nil {
		return nil, err
	}
	if err := s.versions.SetActiveVersion(ctx, tenantID, id, versionID, ev); err != nil {
		s.recordFailure(ctx, id, "rollback", err)
		return nil, err
	}
	updated, err := s.definitions.GetDefinition(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// recordFailure 旁路记录一次失败的工作流创建/更新/发布（best-effort）。
// 记录失败仅 WARN，不改变主流程错误。
func (s *DefinitionService) recordFailure(ctx context.Context, id, op string, err error) {
	if s.failureAudit == nil {
		return
	}
	if recordErr := s.failureAudit.Record(ctx, auditport.ResourceFailure{
		ResourceKind: auditdomain.ResourceKindWorkflow,
		ResourceID:   id,
		Operation:    op,
		ErrorCode:    auditport.ClassifyFailure(err),
	}); recordErr != nil {
		s.logger.Warn("failed to record workflow failure audit",
			zap.String("definition_id", id),
			zap.String("op", op),
			zap.Error(recordErr))
	}
}
