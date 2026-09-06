package domain

import "sort"

// RunResourceAnchor 是评测 run 创建时锚定的一个资源版本（只读 read-model，供
// run 详情显式展示与复现对账）。被测主体来自 run.Resource；绑定 skill/MCP/
// Knowledge 来自创建时固化的 PinnedAssignments（改动前创建的历史 run 无快照
// pin，仅含被测主体）。kind 复用资源 kind，前端可据此跳对应资源版本历史。
type RunResourceAnchor struct {
	Kind       ResourceKind `json:"kind"`
	ResourceID string       `json:"resource_id"`
	RevisionID string       `json:"revision_id"`
}

// ResolveRunAnchors 把 run 锚定的资源版本平铺为稳定有序清单：被测主体恒在首位，
// 随后是绑定的 skill/mcp/knowledge 资源（各自按 id 升序，展示稳定）。被测 agent
// 评测锚定其绑定 skill（SkillRevisions）；skill 评测锚定被测 skill 自身，catalog
// 整体覆盖故不再带绑定资源。
func ResolveRunAnchors(run EvalRun) []RunResourceAnchor {
	anchors := []RunResourceAnchor{{
		Kind: run.Resource.Kind, ResourceID: run.Resource.ResourceID, RevisionID: run.Resource.RevisionID,
	}}
	if run.ContextSnapshot == nil {
		return anchors
	}
	pinned := run.ContextSnapshot.PinnedAssignments
	groups := []struct {
		kind    ResourceKind
		entries map[string]string
	}{
		{kind: ResourceKindSkill, entries: pinned.SkillRevisions},
		{kind: ResourceKindMCP, entries: pinned.MCPRevisions},
		{kind: ResourceKindKnowledge, entries: pinned.KnowledgeRevisions},
	}
	for _, group := range groups {
		if len(group.entries) == 0 {
			continue
		}
		ids := make([]string, 0, len(group.entries))
		for id := range group.entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			anchors = append(anchors, RunResourceAnchor{
				Kind: group.kind, ResourceID: id, RevisionID: group.entries[id],
			})
		}
	}
	return anchors
}
