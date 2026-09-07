package wiring

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
	skillapp "github.com/byteBuilderX/stratum/internal/skill/application"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
)

type centerNamerAgentStub struct {
	agents []agentapp.AgentDTO
	err    error
	tenant string
}

func (s *centerNamerAgentStub) List(ctx context.Context) ([]agentapp.AgentDTO, error) {
	if t, ok := postgres.FromContext(ctx); ok {
		s.tenant = t.TenantID
	}
	return s.agents, s.err
}

type centerNamerSkillStub struct {
	skills []skillapp.SkillProduct
	err    error
	tenant string
}

func (s *centerNamerSkillStub) ListSkills(ctx context.Context) ([]skillapp.SkillProduct, error) {
	if t, ok := postgres.FromContext(ctx); ok {
		s.tenant = t.TenantID
	}
	return s.skills, s.err
}

type centerNamerMCPStub struct {
	cfg  *mcpdomain.ServerConfig
	err  error
	got  string
	call int
}

func (s *centerNamerMCPStub) GetServerConfig(_ context.Context, serverID string) (*mcpdomain.ServerConfig, error) {
	s.got = serverID
	s.call++
	if s.err != nil {
		return nil, s.err
	}
	return s.cfg, nil
}

func TestCenterNamerResolveCompositeFillsProductNames(t *testing.T) {
	agentStub := &centerNamerAgentStub{agents: []agentapp.AgentDTO{
		{ID: "agent-1", Name: "客服 Agent"}, {ID: "agent-3", Name: "未请求 Agent"},
	}}
	skillStub := &centerNamerSkillStub{skills: []skillapp.SkillProduct{{ID: "skill-2", Name: "意图分类技能"}}}
	mcpStub := &centerNamerMCPStub{cfg: &mcpdomain.ServerConfig{ID: "mcp-1", Name: "订单查询工具"}}
	n := &centerResourceNamer{agents: agentStub, skills: skillStub, mcp: mcpStub}

	names, err := n.ResolveCenterNames(context.Background(), "tenant-1", []evaldomain.CenterResourceKey{
		{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1"},
		{Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-2"},
		{Kind: evaldomain.ResourceKindMCP, ResourceID: "mcp-1"},
		{Kind: evaldomain.ResourceKindKnowledge, ResourceID: "workspace-x"},
	})
	if err != nil {
		t.Fatalf("ResolveCenterNames() error = %v", err)
	}
	want := map[evaldomain.CenterResourceKey]string{
		{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1"}:         "客服 Agent",
		{Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-2"}:         "意图分类技能",
		{Kind: evaldomain.ResourceKindMCP, ResourceID: "mcp-1"}:             "订单查询工具",
		{Kind: evaldomain.ResourceKindKnowledge, ResourceID: "workspace-x"}: "workspace-x",
	}
	for k, name := range want {
		if names[k] != name {
			t.Fatalf("names[%+v] = %q, want %q; all=%+v", k, names[k], name, names)
		}
	}
	// 未请求的 agent-3 不得混入；agent/skill 单次 List 必须按租户载体调用。
	if _, ok := names[evaldomain.CenterResourceKey{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-3"}]; ok {
		t.Fatalf("unrequested agent leaked into names: %+v", names)
	}
	if agentStub.tenant != "tenant-1" || skillStub.tenant != "tenant-1" {
		t.Fatalf("product reads not tenant-scoped: agent=%q skill=%q", agentStub.tenant, skillStub.tenant)
	}
	if mcpStub.call != 1 || mcpStub.got != "mcp-1" {
		t.Fatalf("mcp resolved once for id %q, want mcp-1", mcpStub.got)
	}
}

func TestCenterNamerKnowledgeIdentityWithoutProductDeps(t *testing.T) {
	// 未挂任何产品侧依赖时（nil agents/skills/mcp）knowledge 恒等仍生效，其它类缺席、无错。
	n := &centerResourceNamer{}
	names, err := n.ResolveCenterNames(context.Background(), "tenant-1", []evaldomain.CenterResourceKey{
		{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1"},
		{Kind: evaldomain.ResourceKindKnowledge, ResourceID: "workspace-x"},
	})
	if err != nil {
		t.Fatalf("ResolveCenterNames() error = %v", err)
	}
	if names[evaldomain.CenterResourceKey{Kind: evaldomain.ResourceKindKnowledge, ResourceID: "workspace-x"}] != "workspace-x" {
		t.Fatalf("knowledge identity missing: %+v", names)
	}
	if _, ok := names[evaldomain.CenterResourceKey{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1"}]; ok {
		t.Fatalf("agent must be absent without product deps: %+v", names)
	}
}

func TestCenterNamerAgentErrorKeepsResolvedSubset(t *testing.T) {
	skillStub := &centerNamerSkillStub{skills: []skillapp.SkillProduct{{ID: "skill-2", Name: "意图分类技能"}}}
	n := &centerResourceNamer{
		agents: &centerNamerAgentStub{err: errors.New("agent service down")},
		skills: skillStub,
	}
	names, err := n.ResolveCenterNames(context.Background(), "tenant-1", []evaldomain.CenterResourceKey{
		{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1"},
		{Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-2"},
		{Kind: evaldomain.ResourceKindKnowledge, ResourceID: "workspace-x"},
	})
	if err == nil {
		t.Fatal("ResolveCenterNames() error = nil, want agent failure surfaced")
	}
	// 单个产品侧读取失败仅并入 error；已解析类仍随 map 返回。
	if names[evaldomain.CenterResourceKey{Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-2"}] != "意图分类技能" {
		t.Fatalf("resolved skill dropped after agent error: %+v", names)
	}
	if _, ok := names[evaldomain.CenterResourceKey{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1"}]; ok {
		t.Fatalf("agent must stay absent on its own provider error: %+v", names)
	}
}

func TestCenterNamerMCPNilDepsSkip(t *testing.T) {
	// n.mcp == nil（未装配 MCP 集群）时 mcp 键静默跳过、不报错。
	n := &centerResourceNamer{agents: nil, skills: nil, mcp: nil}
	names, err := n.ResolveCenterNames(context.Background(), "tenant-1", []evaldomain.CenterResourceKey{
		{Kind: evaldomain.ResourceKindMCP, ResourceID: "mcp-1"},
	})
	if err != nil {
		t.Fatalf("ResolveCenterNames() error = %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("mcp absent mcp deps must resolve nothing: %+v", names)
	}
}
