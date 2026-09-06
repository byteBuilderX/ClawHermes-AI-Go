package gen

type EvaluationCenterQuery struct {
	// ResourceKind 为空=全部；单值=skill/agent/mcp/knowledge（历史单值深链只读）；
	// 逗号分隔多值=双轨聚合（默认 'agent,knowledge'）。合法取值由
	// handler.centerFilter 统一校验（逐 token 走 domain.ResourceKind.Validate）。
	ResourceKind string `form:"resource_kind" binding:"omitempty"`
	ResourceID   string `form:"resource_id"`
	Status       string `form:"status"`
	Cursor       string `form:"cursor"`
	Limit        int    `form:"limit" binding:"omitempty,min=1,max=100"`
}
