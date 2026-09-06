package domain

import (
	"context"
	"time"
)

const (
	SnapshotSchemaVersion = 1
	GroupEvaluation       = "evaluation" // 与 parameters 域 PlatformGroupEvaluation 值一致
	GroupAgent            = "agent"
	GroupTrace            = "trace"
)

// GroupSnapshot 是评测 run 创建时点某个配置组的固化解：参数 key → 创建时点值。
type GroupSnapshot struct {
	GroupKey   string         `json:"group_key"`
	VersionSeq int64          `json:"version_seq"`
	Values     map[string]any `json:"values"` // 创建时点值（已复制），nil = 空组（消费层默认适用）
}

// ResolvedExecution 是固化的执行窗口（已 clamp，create 时点解析）。
type ResolvedExecution struct {
	ContextWindow int `json:"context_window"`
	OutputReserve int `json:"output_reserve"`
}

// PinnedAssignments 是评测执行固定的版本 pin（canary 隔离）。
type PinnedAssignments struct {
	SkillAgentRevision map[string]string `json:"skill_agent_revision,omitempty"` // skillID → 承载 agent 锁定 revisionID
	MCPRevisions       map[string]string `json:"mcp_revisions,omitempty"`        // serverID → 固定 revisionID
	KnowledgeRevisions map[string]string `json:"knowledge_revisions,omitempty"`  // workspaceName → 固定 revisionID
	// SkillRevisions 是 agent 评测锚定被测 agent 绑定 skill 的版本 pin：skillID →
	// run 创建时点该 skill 当时生效的发布 revisionID（D7 扩展）。之后 skill 再
	// 发版不影响已创建 run；旧 run（改动前创建）该 map 为 nil。
	SkillRevisions map[string]string `json:"skill_revisions,omitempty"`
}

// EvaluationContextSnapshot 是评测 run 创建时点固化的全链路执行上下文快照。
type EvaluationContextSnapshot struct {
	SchemaVersion     int
	Evaluation        GroupSnapshot
	Execution         []GroupSnapshot // agent + trace 组（被测启用 memory 时追加 memory 组）
	ResolvedExecution ResolvedExecution
	PinnedAssignments PinnedAssignments
	CapturedAt        time.Time
	CapturedBy        string
}

type snapshotCtxKey struct{}

// WithEvalSnapshot 把评测上下文快照注入 ctx。
func WithEvalSnapshot(ctx context.Context, snap *EvaluationContextSnapshot) context.Context {
	return context.WithValue(ctx, snapshotCtxKey{}, snap)
}

// EvalSnapshotFromCtx 从 ctx 取评测上下文快照；缺失返回 nil（非评测执行）。
func EvalSnapshotFromCtx(ctx context.Context) *EvaluationContextSnapshot {
	snap, _ := ctx.Value(snapshotCtxKey{}).(*EvaluationContextSnapshot)
	return snap
}
