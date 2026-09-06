// System-assistant options and tool/skill catalog building.

package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentgraph "github.com/byteBuilderX/stratum/internal/agent/application/graph"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"go.uber.org/zap"
)

func (s *AgentService) assistantExecutionOptions(
	ctx context.Context,
	meta ExecMeta,
	req ExecRequest,
	roleClass string,
	authorization domain.DiagnosticAuthorization,
	profileVersion string,
	agentID string,
) []ExecutionOption {
	var options []ExecutionOption
	if s.deps.OfficialDocsSearch != nil {
		search := s.deps.OfficialDocsSearch
		options = append(options, WithOfficialDocsSearchFn(func(callCtx context.Context, query string) ([]domain.Citation, error) {
			citations, searchErr := search(callCtx, query)
			if s.deps.Metrics != nil {
				outcome := "matched"
				if searchErr != nil {
					outcome = "error"
				}
				s.deps.Metrics.RecordOfficialDocsSearchResults(profileVersion, outcome, len(citations))
			}
			return citations, searchErr
		}))
	}
	if s.deps.DiagnosticProvider != nil {
		provider, authorized := s.deps.DiagnosticProvider, authorization.Request
		options = append(options, WithDiagnosticFn(func(callCtx context.Context, areas []domain.DiagnosticArea) (domain.DiagnosticEvidence, error) {
			request := authorized
			request.Areas = append([]domain.DiagnosticArea(nil), areas...)
			evidence, diagnosticErr := provider.CollectAuthorized(callCtx, request)
			if s.deps.Metrics != nil {
				for _, result := range evidence.AreaResults {
					s.deps.Metrics.RecordSystemAssistantDiagnosticArea(roleClass, string(result.Area), boundedAssistantOutcome(result.Outcome), float64(result.DurationMs)/1000)
				}
				s.deps.Metrics.RecordSystemAssistantEvidenceGaps(roleClass, profileVersion, len(evidence.Gaps))
			}
			return evidence, diagnosticErr
		}))
	}
	if s.deps.ProposalService != nil {
		proposalService := s.deps.ProposalService
		tenantID, actorID, conversationID := meta.TenantID, req.UserID, req.ConversationID
		// D6：admin/owner 提案创建后立即自动确认并应用，一气呵成；member 保持待审提案流。
		autoApply := roleClass == "admin" || roleClass == "owner"
		options = append(options, withProposalCreateFn(func(callCtx context.Context, args map[string]any) (domain.ResourceChangeProposalArtifact, error) {
			kind, operation, resourceID, payload, parseErr := ParseResourceChangeToolArguments(args)
			if parseErr != nil {
				return domain.ResourceChangeProposalArtifact{}, parseErr
			}
			proposal, createErr := proposalService.CreateProposal(callCtx, CreateProposalInput{
				TenantID: tenantID, ConversationID: conversationID, ActorID: actorID,
				Kind: kind, Operation: operation, ResourceID: resourceID, Payload: payload,
			})
			if createErr != nil {
				return domain.ResourceChangeProposalArtifact{}, createErr
			}
			if autoApply {
				applied, applyErr := proposalService.ConfirmAndApply(callCtx, tenantID, proposal.ID, actorID)
				if applyErr != nil {
					// 保留已创建的 proposal artifact：graph 错误分支据此记录
					// proposal ID，避免模型重复创建提案。ConfirmAndApply 失败前
					// 可能已推进状态机（stale/failed/unknown_outcome），回读当前
					// DB 状态避免用创建时的 ready_for_review 快照误导模型；回读
					// 失败（如上下文超时）则退回创建快照。
					current, getErr := proposalService.Get(callCtx, tenantID, actorID, proposal.ID)
					if getErr == nil {
						proposal = current
					}
					created := domain.ResourceChangeProposalArtifact{
						ID: proposal.ID, ResourceKind: proposal.ResourceKind, Operation: proposal.Operation,
						Status: proposal.Status, Summary: proposal.Summary, ExpiresAt: proposal.ExpiresAt,
					}
					return created, fmt.Errorf("auto apply proposal %s: %w", proposal.ID, applyErr)
				}
				proposal = applied
			}
			artifact := domain.ResourceChangeProposalArtifact{
				ID: proposal.ID, ResourceKind: proposal.ResourceKind, Operation: proposal.Operation,
				Status: proposal.Status, Summary: proposal.Summary, ExpiresAt: proposal.ExpiresAt,
			}
			return artifact, nil
		}))
	}
	if s.deps.ResourceChangeApplier != nil {
		applier := s.deps.ResourceChangeApplier
		actorID := req.UserID
		// apply 工具全角色可见（D6）；member 闭包内 fail closed 明确拒绝，
		// 不触达 applier，与 update_system_model 的写路径模式一致。
		options = append(options, withResourceChangeApplyFn(func(callCtx context.Context, args map[string]any) (domain.ApplyResult, error) {
			if roleClass != "admin" && roleClass != "owner" {
				return domain.ApplyResult{}, fmt.Errorf("%w: 需要管理员权限，member 请改用提案工具", domain.ErrProposalForbidden)
			}
			return applier(callCtx, actorID, args)
		}))
	}
	options = s.appendSystemModelToolOptions(options, meta, req, roleClass, agentID)
	return options
}

// updateAgentModelForTool updates an agent's chat model through the ordinary
// Update path (validation + audit + atomic), returning the settings projection
// the model tool surface renders. 平台助手等同化后不再有专属 registry 方法，
// 写路径走普通流程并受普通 ownership 矩阵约束（owner 放行、admin/member
// 按 created_by/editors 判定，seed created_by=” 故 admin 默认被拒）。

func (s *AgentService) updateAgentModelForTool(callCtx context.Context, agentID, actorID, model string) (SystemAssistantSettings, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return SystemAssistantSettings{}, domain.ErrInvalidAgentModel
	}
	tenantID := reqctx.TenantIDFromContext(callCtx)
	if tenantID == "" {
		return SystemAssistantSettings{}, fmt.Errorf("agent service update agent model: tenant id required")
	}
	if s.deps.TenantModelValidator != nil {
		if err := s.deps.TenantModelValidator.ValidateTenantChatModel(callCtx, tenantID, model); err != nil {
			return SystemAssistantSettings{}, err
		}
	}
	existing, found, err := s.deps.Registry.Get(callCtx, agentID)
	if err != nil {
		return SystemAssistantSettings{}, fmt.Errorf("agent service update agent model: %w", err)
	}
	if !found {
		return SystemAssistantSettings{}, ErrNotFound
	}
	cfg := existing.GetConfig()
	// buildUpdateConfig 全量构造：未填字段会以零值清空存量，故从 existing
	// 继承全部可变字段，仅替换 LLMModel。
	_, err = s.Update(callCtx, agentID, UpdateAgentInput{
		ActorID:               actorID,
		Name:                  cfg.Name,
		Type:                  string(cfg.Type),
		Description:           cfg.Description,
		SystemPrompt:          cfg.SystemPrompt,
		LLMModel:              model,
		MaxIterations:         cfg.MaxIterations,
		MaxContextTokens:      cfg.MaxContextTokens,
		Temperature:           cfg.Temperature,
		ReasoningEffort:       cfg.ReasoningEffort,
		MaxTokens:             cfg.MaxTokens,
		Parameters:            cfg.MemoryParameters,
		AllowedSkills:         cfg.AllowedSkills,
		MCPToolIDs:            cfg.MCPToolIDs,
		KnowledgeWorkspaceIDs: cfg.KnowledgeWorkspaceIDs,
		MemoryScope:           cfg.MemoryScope,
	})
	if err != nil {
		return SystemAssistantSettings{}, err
	}
	models, err := s.listTenantChatModels(callCtx, tenantID)
	if err != nil {
		return SystemAssistantSettings{}, err
	}
	return SystemAssistantSettings{
		AgentID: agentID, Model: model, Ready: true, AvailableModels: models,
	}, nil
}

// appendSystemModelToolOptions 装配模型工具闭包：list_models 全角色可见；
// update_system_model 写路径在闭包内按 roleClass fail closed，member 明确
// 拒绝且不触达 Registry。提取为独立方法以控制主函数圈复杂度。

func (s *AgentService) appendSystemModelToolOptions(
	options []ExecutionOption, meta ExecMeta, req ExecRequest, roleClass string, defaultAgentID string,
) []ExecutionOption {
	if s.deps.ModelDetailsProvider != nil {
		details := s.deps.ModelDetailsProvider
		options = append(options, WithListModelsFn(func(callCtx context.Context) (map[string]any, error) {
			models, listErr := details.ListTenantModelDetails(callCtx, meta.TenantID)
			if listErr != nil {
				return nil, fmt.Errorf("list tenant models: %w", listErr)
			}
			return map[string]any{"models": models}, nil
		}))
	}
	if s.deps.Registry != nil {
		actorID := req.UserID
		updateModel := func(callCtx context.Context, model, agentID string) (map[string]any, error) {
			if roleClass != "admin" && roleClass != "owner" {
				// 写路径 fail closed：member 明确拒绝，不触达 Registry。
				return nil, errors.New("更新 Agent 模型需要管理员权限")
			}
			targetID := strings.TrimSpace(agentID)
			if targetID == "" {
				targetID = defaultAgentID
			}
			if strings.TrimSpace(targetID) == "" {
				return nil, fmt.Errorf("update agent model: agent id required")
			}
			settings, updateErr := s.updateAgentModelForTool(callCtx, targetID, actorID, model)
			if updateErr != nil {
				return nil, updateErr
			}
			return map[string]any{
				"model":           settings.Model,
				"ready":           settings.Ready,
				"availableModels": settings.AvailableModels,
			}, nil
		}
		options = append(options, WithUpdateSystemModelFn(updateModel))
	}
	if s.deps.Registry != nil {
		options = append(options, WithListAgentsFn(func(callCtx context.Context) (map[string]any, error) {
			agents, listErr := s.List(callCtx)
			if listErr != nil {
				return nil, fmt.Errorf("list agents: %w", listErr)
			}
			items := make([]map[string]any, 0, len(agents))
			for _, dto := range agents {
				// 复用安全投影：不携带 systemPrompt/systemKey 等敏感字段。
				items = append(items, AgentDTOSafeProjection(dto))
			}
			return map[string]any{"agents": items}, nil
		}))
	}
	if s.deps.MCPServerLister != nil {
		lister := s.deps.MCPServerLister
		options = append(options, WithListMCPServersFn(func(callCtx context.Context) (map[string]any, error) {
			servers, listErr := lister.ListMCPServers(callCtx)
			if listErr != nil {
				return nil, fmt.Errorf("list mcp servers: %w", listErr)
			}
			return map[string]any{"servers": servers}, nil
		}))
	}
	return options
}

// buildExtraTools converts MCPToolIDs and AllowedSkills into ToolDefinitions
// for the ReAct loop. Published skills use their tool contract names; legacy
// skills fall back to tenant-scoped names. The returned index maps tool names
// back to skill/version refs for execution routing.

func (s *AgentService) buildExtraTools(
	ctx context.Context,
	tenantID, subjectID string,
	mcpToolIDs, allowedSkills []string,
) ([]port.ToolDefinition, map[string]port.SkillActivation) {
	tools, catalog, _ := s.buildExtraToolsChecked(ctx, tenantID, subjectID, mcpToolIDs, allowedSkills)
	return tools, catalog
}

func (s *AgentService) buildExtraToolsChecked(
	ctx context.Context,
	tenantID, subjectID string,
	mcpToolIDs, allowedSkills []string,
) ([]port.ToolDefinition, map[string]port.SkillActivation, error) {
	tools := s.buildMCPTools(ctx, tenantID, mcpToolIDs)
	catalog, err := s.buildSkillCatalog(ctx, tenantID, subjectID, allowedSkills)
	if err != nil {
		return nil, nil, err
	}
	return tools, catalog, nil
}

func (s *AgentService) buildMCPTools(
	ctx context.Context, tenantID string, mcpToolIDs []string,
) []port.ToolDefinition {
	var tools []port.ToolDefinition
	allowedTools := make(map[string]struct{}, len(mcpToolIDs))
	servers := map[string]struct{}{}
	for _, toolID := range mcpToolIDs {
		parts := strings.Split(toolID, ":")
		if len(parts) == 3 && parts[0] == "mcp" {
			allowedTools[toolID] = struct{}{}
			servers[parts[1]] = struct{}{}
		}
	}
	for serverID := range servers {
		if s.deps.MCPTools == nil {
			continue
		}
		for _, tool := range s.deps.MCPTools.ToolsForServer(ctx, tenantID, serverID) {
			if _, ok := allowedTools[tool.Name]; !ok {
				continue
			}
			tool = normalizeMCPTool(tool, serverID)
			risk, policyResolved := s.resolveMCPToolRisk(ctx, tenantID, serverID, tool.CapabilityID)
			tool.Metadata["risk_level"] = string(risk)
			tool.Metadata["policy_resolved"] = policyResolved
			tools = append(tools, tool)
		}
	}
	if len(mcpToolIDs) > 0 && len(tools) == 0 {
		// 显式暴露工具缺失：agent 绑定了 MCP 工具但最终一个都没暴露。
		// 此前静默 drop，远端故障表现为"模型无 MCP 工具可调用"却无任何日志，
		// 排查困难。这里显式告警（含绑定 ID，便于定位是 catalog 空还是
		// 服务端未返回对应工具）。
		s.deps.Logger.Warn("agent bound MCP tools but none exposed",
			zap.String("tenant_id", tenantID),
			zap.Strings("bound_tool_ids", mcpToolIDs))
	}
	return tools
}

func normalizeMCPTool(tool port.ToolDefinition, serverID string) port.ToolDefinition {
	if tool.ProviderType == "" {
		tool.ProviderType = domain.ProviderTypeMCP
	}
	if tool.ProviderID == "" {
		tool.ProviderID = serverID
	}
	if tool.ServerID == "" {
		tool.ServerID = serverID
	}
	if tool.CapabilityID == "" {
		tool.CapabilityID = tool.Name
	}
	if tool.NodeType == "" {
		tool.NodeType = domain.ObservationTypeMCP
	}
	if tool.Metadata == nil {
		tool.Metadata = make(map[string]any)
	}
	return tool
}

func (s *AgentService) resolveMCPToolRisk(
	ctx context.Context, tenantID, serverID, capabilityID string,
) (port.ToolRiskLevel, bool) {
	risk := port.ToolRiskUnclassified
	resolved := false
	if s.deps.MCPToolPolicy == nil {
		return risk, resolved
	}
	policyRisk, err := s.deps.MCPToolPolicy.ResolveMCPToolRisk(ctx, tenantID, serverID, capabilityID)
	if err != nil || policyRisk == "" {
		return risk, resolved
	}
	return stricterToolRisk(risk, policyRisk), true
}

func (s *AgentService) buildSkillCatalog(
	ctx context.Context, tenantID, subjectID string, allowedSkills []string,
) (map[string]port.SkillActivation, error) {
	refs, assignments, err := s.resolveSkillRevisionRefs(ctx, tenantID, subjectID, allowedSkills)
	if err != nil {
		return nil, err
	}
	catalog := make(map[string]port.SkillActivation)
	if s.deps.SkillActivationResolver != nil && len(refs) > 0 {
		resolved, err := s.deps.SkillActivationResolver.ResolveSkills(ctx, tenantID, refs)
		if err != nil {
			return nil, fmt.Errorf("resolve Skill experiment revisions: %w", err)
		}
		catalog = resolved
	}
	applySkillAssignments(catalog, assignments)
	if err := validateSkillCatalogNames(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

// validateSkillCatalogNames 校验绑定集合内 skill 解析名（contract Name 回退
// SkillID）唯一且不命中平台内置工具保留名（Spec D1）。stratum_skill 统一工具
// 按参数名分发，解析名歧义或与内置工具名冲突时 fail-closed，禁止静默截胡或
// 双义定位。

func validateSkillCatalogNames(catalog map[string]port.SkillActivation) error {
	seen := make(map[string]string, len(catalog))
	for skillID, a := range catalog {
		name := a.Name
		if name == "" {
			name = skillID
		}
		if agentgraph.IsReservedToolName(name) {
			return fmt.Errorf("skill %q: activation name %q collides with reserved platform tool name", skillID, name)
		}
		if other, exists := seen[name]; exists {
			return fmt.Errorf("skill activation name %q collides between %q and %q", name, other, skillID)
		}
		seen[name] = skillID
	}
	return nil
}

func (s *AgentService) resolveSkillRevisionRefs(
	ctx context.Context, tenantID, subjectID string, allowedSkills []string,
) ([]port.SkillRevisionRef, map[string]port.SkillRevisionAssignment, error) {
	refs := make([]port.SkillRevisionRef, 0, len(allowedSkills))
	assignments := make(map[string]port.SkillRevisionAssignment)
	for _, skillID := range allowedSkills {
		ref := port.SkillRevisionRef{SkillID: skillID}
		var assignment port.SkillRevisionAssignment
		// 评测执行（执行快照在 ctx）：绑定 skill 固定到 run 创建时点 pin
		// （PinnedSkills），优先于 skill 自身 canary 实验分流——评测锚定当时生效
		// 发布版，之后 skill 发版/实验都不影响已创建 run。pin 缺失（创建时该
		// skill 未发布、未解析）回退既有非评测逻辑。
		if rev, pinned := s.pinnedSkillRevision(ctx, skillID); pinned {
			ref.RevisionID = rev
		} else if s.deps.SkillRevisionResolver != nil {
			resolved, found, err := s.deps.SkillRevisionResolver.ResolveSkillRevision(ctx, tenantID, skillID, subjectID)
			if err != nil {
				return nil, nil, fmt.Errorf("resolve Skill %s experiment assignment: %w", skillID, err)
			}
			if found {
				assignment = resolved
				ref.RevisionID = resolved.RevisionID
			}
		}
		refs = append(refs, ref)
		if assignment.RevisionID != "" {
			assignments[skillID] = assignment
		}
	}
	return refs, assignments, nil
}

// pinnedSkillRevision 返回评测执行快照中被测 agent 绑定的 skill pin revisionID。
// 执行快照缺失（非评测链路）或该 skill 未 pin 时返回 found=false。
func (s *AgentService) pinnedSkillRevision(ctx context.Context, skillID string) (string, bool) {
	es := port.ExecutionSnapshotFromCtx(ctx)
	if es == nil {
		return "", false
	}
	rev, ok := es.PinnedSkills[skillID]
	return rev, ok
}

func applySkillAssignments(
	catalog map[string]port.SkillActivation, assignments map[string]port.SkillRevisionAssignment,
) {
	for skillID, assignment := range assignments {
		activation := catalog[skillID]
		activation.SkillID = skillID
		activation.RevisionID = assignment.RevisionID
		activation.ExperimentID = assignment.ExperimentID
		activation.Variant = assignment.Variant
		catalog[skillID] = activation
	}
}

func stricterToolRisk(left, right port.ToolRiskLevel) port.ToolRiskLevel {
	if toolRiskRank(right) > toolRiskRank(left) {
		return right
	}
	return left
}

func toolRiskRank(risk port.ToolRiskLevel) int {
	switch risk {
	case port.ToolRiskDestructive:
		return 3
	case port.ToolRiskWriteReversible:
		return 2
	case port.ToolRiskRead:
		return 1
	default:
		return 0
	}
}
