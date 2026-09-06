package port

import "context"

// ExecutionSnapshot 是被测执行在评测 run 创建时点固化的最小投影（agent 消费侧）。
// 由 evaluation wiring 从 evaldomain.EvaluationContextSnapshot 投影注入；
// ctx 中缺失 = 非评测执行，走现有运行时路径（D6）。
type ExecutionSnapshot struct {
	TraceParameters     map[string]any
	ContextWindowTokens int
	OutputReserveTokens int
	PinnedMCP           map[string]MCPRevisionPin
	PinnedKnowledge     map[string]KnowledgeRevisionPin
	// PinnedSkills 是评测锚定的被测 agent 绑定 skill → run 创建时点锁定
	// revisionID（agent 消费侧；来源 eval snapshot PinnedAssignments.SkillRevisions）。
	PinnedSkills map[string]string
}

type executionSnapshotCtxKey struct{}

// WithExecutionSnapshot 把执行快照投影注入 ctx。
func WithExecutionSnapshot(ctx context.Context, snap *ExecutionSnapshot) context.Context {
	return context.WithValue(ctx, executionSnapshotCtxKey{}, snap)
}

// ExecutionSnapshotFromCtx 从 ctx 取执行快照投影；缺失返回 nil（非评测执行）。
func ExecutionSnapshotFromCtx(ctx context.Context) *ExecutionSnapshot {
	snap, _ := ctx.Value(executionSnapshotCtxKey{}).(*ExecutionSnapshot)
	return snap
}
