package domain

import (
	"reflect"
	"testing"
)

func TestResolveRunAnchors(t *testing.T) {
	subject := ResourceRef{Kind: ResourceKindAgent, ResourceID: "agent-1", RevisionID: "agent-rev-3"}
	cases := []struct {
		name string
		run  EvalRun
		want []RunResourceAnchor // 被测恒在首位，绑定资源按 kind+id 稳定有序
	}{
		{
			name: "被测 agent 携带绑定的 skill/mcp/knowledge pin",
			run: EvalRun{
				Resource: subject,
				ContextSnapshot: &EvaluationContextSnapshot{PinnedAssignments: PinnedAssignments{
					SkillRevisions:     map[string]string{"skill-b": "skill-rev-2", "skill-a": "skill-rev-1"},
					MCPRevisions:       map[string]string{"mcp-1": "mcp-rev-9"},
					KnowledgeRevisions: map[string]string{"kb-1": "kb-rev-4"},
					SkillAgentRevision: map[string]string{},
				}},
			},
			want: []RunResourceAnchor{
				{Kind: ResourceKindAgent, ResourceID: "agent-1", RevisionID: "agent-rev-3"},
				{Kind: ResourceKindSkill, ResourceID: "skill-a", RevisionID: "skill-rev-1"},
				{Kind: ResourceKindSkill, ResourceID: "skill-b", RevisionID: "skill-rev-2"},
				{Kind: ResourceKindMCP, ResourceID: "mcp-1", RevisionID: "mcp-rev-9"},
				{Kind: ResourceKindKnowledge, ResourceID: "kb-1", RevisionID: "kb-rev-4"},
			},
		},
		{
			name: "改动前创建的历史 run 无快照，仅含被测主体",
			run:  EvalRun{Resource: subject},
			want: []RunResourceAnchor{
				{Kind: ResourceKindAgent, ResourceID: "agent-1", RevisionID: "agent-rev-3"},
			},
		},
		{
			name: "快照存在但无绑定 pin，仅含被测主体",
			run: EvalRun{
				Resource:        subject,
				ContextSnapshot: &EvaluationContextSnapshot{},
			},
			want: []RunResourceAnchor{
				{Kind: ResourceKindAgent, ResourceID: "agent-1", RevisionID: "agent-rev-3"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRunAnchors(tc.run)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ResolveRunAnchors() =\n  %#v\nwant\n  %#v", got, tc.want)
			}
		})
	}
}
