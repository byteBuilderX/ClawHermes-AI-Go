package port

import (
	"context"
	"errors"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

// GateStore 持久化分层门禁台账与窗口证据（tenant scope，eval_gate_actions）。
// DB 投影在 T13 实现（QueryWindow 按 ObservationSource 分类计数窗口内的
// rule_block/behavior_anomaly/judge_flag 观察）；P1 只定义契约，单测用 stub。
type GateStore interface {
	// AppendAction 追加一条门禁决策台账行。
	AppendAction(ctx context.Context, tenantID string, rec domain.GateActionRecord) error
	// QueryWindow 返回 since 至今、目标 target 的证据聚合（RuleBlockCount/AnomalyCount/
	// JudgeFlagCount）。since 由调用方按 GateObservationWindow 推进。
	QueryWindow(ctx context.Context, tenantID string, target domain.GateTarget,
		since time.Time) (domain.GateEvidence, error)
}

// GatePolicySource 解析目标当前的生效门禁策略（scope 折叠：平台恒
// RollbackSupported=true + AutoRollbackAllowed=false）。
type GatePolicySource interface {
	Resolve(ctx context.Context, target domain.GateTarget) (domain.GatePolicy, error)
}

// GateApprovalRequester 为 rollback_manual 决策请求人工审批（agent_tool_approvals）。
// 返回审批 id；失败由调用方 fail-open 处理（记录台账后跳过）。
type GateApprovalRequester interface {
	RequestRollbackApproval(ctx context.Context, tenantID string, rec domain.GateActionRecord) (string, error)
}

// PlatformGateOps 是 public platform_config_versions 的门禁写回面（actor 空 → "api"）。
type PlatformGateOps interface {
	UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error
}

// ResourceRollbackExecutor 执行资源自动回滚。auto 动作由 GateService 装配（T13 生产
// wiring）；manual 动作由审批执行器 executeResourceRollback 调用（Task 4）。分派见
// application/resource_rollback.go：ScopeResource + Kind → 产品后端 / canary 后端；
// mcp / 未知 kind / 非资源 scope → ErrRollbackUnsupported。执行日限 GateAutoRollbackMaxPerDay
// 由调用方（gate service / T13 wiring 按台账聚合）保障。
type ResourceRollbackExecutor interface {
	// actor = 动作执行者（auto 传 "gate"）；decidedBy = 审批人（manual 传审批 row 的
	// DecidedBy，auto 空）；approvalID = 审批 id（manual 传，auto 空）。
	Rollback(ctx context.Context, tenantID string, target domain.GateTarget,
		actor, decidedBy, approvalID string) error
}

// GateSink 是观测落库后的门禁入口（fail-open：nil / 关闭 / 失败均不阻断主流程）。
type GateSink interface {
	HandleObservation(ctx context.Context, tenantID string, obs domain.EvalObservation) error
}

// ErrRollbackUnsupported 表示该资源无回滚链路（mcp / 未知 kind / 非资源 scope）。
var ErrRollbackUnsupported = errors.New("evaluation: rollback unsupported for target")

// ErrAutoRollbackForbidden 表示该目标禁止自动回滚（auto 意图被策略拒绝；spec §3.4 平台恒
// 人工、资源默认 AutoRollbackAllowed=false，Task 4 executePlatformRollback 首行 guard 消费）。
var ErrAutoRollbackForbidden = errors.New("evaluation: auto rollback forbidden for target")
