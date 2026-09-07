package wiring

import (
	"context"
	"errors"
	"fmt"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
)

// centerNamerTenantCtx 包装评测中心真名解析的租户载体：evaluation-worker 身份 + 租户
// admin 角色（只读 SELECT，不参与业务鉴权）。与 evaluationSkillContext/evaluationMCPContext
// 走同一载体（tenantdb.FromContext），保证 agent/skill/mcp 产品侧读按租户隔离。
func centerNamerTenantCtx(ctx context.Context, tenantID string) context.Context {
	return postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: "evaluation-worker", Role: postgres.RoleTenantAdmin,
	})
}

// centerResourceAgentLister / centerResourceSkillLister / centerResourceMCPReader 是
// 复合真名解析器依赖的产品侧只读窄接口（容器组件天然满足，单测可用 stub 注入）。
type centerResourceAgentLister interface {
	List(ctx context.Context) ([]agentapp.AgentDTO, error)
}
type centerResourceSkillLister interface {
	ListSkills(ctx context.Context) ([]skillapp.SkillProduct, error)
}
type centerResourceMCPReader interface {
	GetServerConfig(ctx context.Context, serverID string) (*mcpdomain.ServerConfig, error)
}

// centerResourceNamer 实现 evalport.CenterResourceNamer：评测中心资源行被测资源的
// 跨模块真名。agent=Agent 产品名、skill=Skill 产品名、mcp=MCP server 名（各经产品侧
// 读在中心租户载体下解析）；knowledge 的 resource_id 本身即 workspace 名 → 恒等返回。
// 解析 best-effort：单个产品侧读取失败只并入返回 error（调用方记 Warn），已解析类
// 仍随 map 返回；查不到的行在 map 中缺席（前端显式占位 —）。
type centerResourceNamer struct {
	agents centerResourceAgentLister
	skills centerResourceSkillLister
	mcp    centerResourceMCPReader
}

func (n *centerResourceNamer) ResolveCenterNames(ctx context.Context, tenantID string,
	keys []evaldomain.CenterResourceKey) (map[evaldomain.CenterResourceKey]string, error) {
	names := make(map[evaldomain.CenterResourceKey]string, len(keys))
	ctx = centerNamerTenantCtx(ctx, tenantID)
	var errs []error

	agentIDs, skillIDs, mcpIDs := partitionCenterKeys(keys, names)
	if nameByID, err := n.agentNames(ctx, agentIDs); err != nil {
		errs = append(errs, err)
	} else {
		fillCenterNames(names, evaldomain.ResourceKindAgent, nameByID)
	}
	if nameByID, err := n.skillNames(ctx, skillIDs); err != nil {
		errs = append(errs, err)
	} else {
		fillCenterNames(names, evaldomain.ResourceKindSkill, nameByID)
	}
	if n.mcp != nil {
		for _, id := range mcpIDs {
			cfg, err := n.mcp.GetServerConfig(ctx, id)
			if err != nil {
				errs = append(errs, fmt.Errorf("resolve mcp center resource name %q: %w", id, err))
				continue
			}
			if cfg != nil && cfg.Name != "" {
				names[evaldomain.CenterResourceKey{Kind: evaldomain.ResourceKindMCP, ResourceID: id}] = cfg.Name
			}
		}
	}
	return names, errors.Join(errs...)
}

// partitionCenterKeys 按 kind 分离需产品侧解析的 id；恒等类（knowledge：resource_id
// 即 workspace 名）直接写入 names，不触产品侧读。
func partitionCenterKeys(keys []evaldomain.CenterResourceKey,
	names map[evaldomain.CenterResourceKey]string) (agentIDs, skillIDs, mcpIDs []string) {
	for _, key := range keys {
		switch key.Kind {
		case evaldomain.ResourceKindAgent:
			agentIDs = append(agentIDs, key.ResourceID)
		case evaldomain.ResourceKindSkill:
			skillIDs = append(skillIDs, key.ResourceID)
		case evaldomain.ResourceKindMCP:
			mcpIDs = append(mcpIDs, key.ResourceID)
		case evaldomain.ResourceKindKnowledge:
			names[key] = key.ResourceID
		}
	}
	return agentIDs, skillIDs, mcpIDs
}

// fillCenterNames 把某 kind 的 id→真名批量写回 names（仅写非空真名）。
func fillCenterNames(names map[evaldomain.CenterResourceKey]string, kind evaldomain.ResourceKind,
	nameByID map[string]string) {
	for id, name := range nameByID {
		if name != "" {
			names[evaldomain.CenterResourceKey{Kind: kind, ResourceID: id}] = name
		}
	}
}

// catalogNames 从全量目录行中抽取请求 id 的真名 map。单次 List 覆盖多行解析；
// 只保留请求 id（目录中未请求的 id 丢弃，返回 map 不出现多余键），查不到的行
// 在 map 中缺席。want 是目录行切片，idOf/nameOf 提取行主键与真名。
func catalogNames[T any](rows []T, ids []string, idOf func(T) string, nameOf func(T) string) map[string]string {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	byID := make(map[string]string, len(ids))
	for _, row := range rows {
		id, name := idOf(row), nameOf(row)
		if _, ok := wanted[id]; !ok {
			continue
		}
		if _, seen := byID[id]; seen {
			continue
		}
		byID[id] = name
	}
	return byID
}

// agentNames 拉全租户 agent 目录并只保留请求 id 的真名（单次 List 覆盖多行）。
func (n *centerResourceNamer) agentNames(ctx context.Context, ids []string) (map[string]string, error) {
	if n.agents == nil || len(ids) == 0 {
		return nil, nil
	}
	all, err := n.agents.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve agent center resource names: %w", err)
	}
	return catalogNames(all, ids,
		func(a agentapp.AgentDTO) string { return a.ID },
		func(a agentapp.AgentDTO) string { return a.Name }), nil
}

// skillNames 拉全租户 skill 产品目录并只保留请求 id 的真名（单次 ListSkills 覆盖多行）。
func (n *centerResourceNamer) skillNames(ctx context.Context, ids []string) (map[string]string, error) {
	if n.skills == nil || len(ids) == 0 {
		return nil, nil
	}
	all, err := n.skills.ListSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve skill center resource names: %w", err)
	}
	return catalogNames(all, ids,
		func(s skillapp.SkillProduct) string { return s.ID },
		func(s skillapp.SkillProduct) string { return s.Name }), nil
}
