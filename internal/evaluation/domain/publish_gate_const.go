package domain

// 平台门禁分层/动作字面量（spec §3.4 计数、R31 精确值；跨包共享单一归属）。
// 跨任务常量单一归属（compile order 强制）：`l3_platform` 与 `sentinel_passed` 由
// Task 4 定义于 pkg/constants/evaluation.go（GateLayerL3Platform / PlatformEvalStateSentinelPassed），
// 本文件不重复定义；编排代码引用 constants.GateLayerL3Platform / constants.PlatformEvalStateSentinelPassed。
// 本文件只保留 Task 5 独有常量（l2/l3_sentinel/l3_multitenant_verify/全部 action/sentinel_failed）。
// 组常量唯一 home = internal/evaluation/domain/snapshot.go GroupMemory（Sub-commit A0 定义，
// 与 parameters 域同值；本文件不再定义组字面量）。
const (
	LayerL2                  = "l2"
	LayerL3Sentinel          = "l3_sentinel"
	LayerL3MultiTenantVerify = "l3_multitenant_verify"

	ActionRegression     = "regression"
	ActionBlock          = "block"
	ActionPass           = "pass"
	ActionPublishGated   = "publish_gated"
	ActionPublishBlocked = "publish_blocked"
	ActionQueued         = "queued"
	ActionRecovered      = "recovered"
	ActionNotRecovered   = "not_recovered"
)

// 平台版本 eval_state（spec §4.1.1 值域子集；parameters 侧 UPDATE 消费）。Task 5 只写
// sentinel_failed（本文件）/ sentinel_passed（constants.PlatformEvalStateSentinelPassed）。
const (
	EvalStateSentinelFailed = "sentinel_failed"
)
