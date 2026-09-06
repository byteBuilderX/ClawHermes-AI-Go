package domain

import (
	"fmt"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// Scope 表示分层门禁作用的对象层级（spec §2.2）。evaluation domain 自带类型，
// 禁止 import parameters domain 的 Scope。
type Scope string

const (
	ScopePlatform Scope = "platform" // 平台级参数（judge/observe/ruleguard/gate…）
	ScopeResource Scope = "resource" // 被测资源参数（租户/资源级 agent 等）
)

// GateTarget 标识一次门禁评估的目标参数集（平台键组或被测资源）。
// 平台：GroupKey（evaluation/agent/memory/trace）+ 生效 VersionSeq；
// 资源：Kind + ResourceID + RevisionID（obs.Param.Resource.Ref 映射：Ref 是观测携带的
// 被测资源已执行 revision；Version 只是变体标签，不作为 revision 锚点）。
type GateTarget struct {
	Scope      Scope  `json:"scope"`
	GroupKey   string `json:"group_key,omitempty"` // 平台分组；资源空
	Kind       string `json:"kind,omitempty"`      // 资源 kind（agent/skill/…）；平台空
	ResourceID string `json:"resource_id,omitempty"`
	RevisionID string `json:"revision_id,omitempty"`
	VersionSeq int64  `json:"version_seq,omitempty"` // 平台版本 seq / 资源对照锚点
}

// Key 返回目标的稳定去重键（冷却/去重用）。
func (t GateTarget) Key() string {
	if t.Scope == ScopePlatform {
		return "platform:" + t.GroupKey
	}
	return fmt.Sprintf("resource:%s:%s:%s", t.Kind, t.ResourceID, t.RevisionID)
}

// GateAction 是一次门禁评估的决策动作。常量名沿用 spec GateDecision 的值，
// 类型名改 GateAction 避免与 optimization_strategy.go 的 domain.GateDecision 冲突（裁决 R2）。
// 值即台账 decision 列文本。
type GateAction string

const (
	GateNone           GateAction = "none"
	GateL2Escalate     GateAction = "l2_escalate"
	GateRollbackManual GateAction = "rollback_manual"
	GateRollbackAuto   GateAction = "rollback_auto"
)

// ReviewVerdict 是人工评审/门禁复核结论（spec §2.2）。空值 = 无人工确认。
type ReviewVerdict string

const (
	ReviewVerdictConfirmRegression ReviewVerdict = "confirm_regression"
	ReviewVerdictConfirmRollback   ReviewVerdict = "confirm_rollback"
)

// RunComparison 描述确认 run 相对基线 run 的对照结论（T8+ 装配确认 run 后填充；
// P1 恒 nil）。
type RunComparison struct {
	Regressed       bool // 确认 run 维度劣化超过 RunRegressionDeltaThreshold
	BaselineSeq     int64
	ConfirmedSeq    int64
	DimensionDeltas map[string]float64
}

// GateEvidence 是 Decide 的输入证据（spec §2.2）：观察窗口聚合计数 + 人工/对照判定。
// 窗口计数来自 GateStore.QueryWindow（T13 按 ObservationSource 分类）；
// ReviewVerdict/ConfirmationRun 由后续阶段填充，P1 恒零/nil。
type GateEvidence struct {
	RuleBlockCount  int // 规则阻断（rule_block）观察数
	AnomalyCount    int // 行为异常（behavior_anomaly）观察数
	JudgeFlagCount  int // judge 跌阈 flag 观察数
	ReviewVerdict   ReviewVerdict
	ConfirmationRun *RunComparison
}

// GatePolicy 描述一次评估的生效策略。scope 折叠进值（裁决 R4）：平台恒
// RollbackSupported=true + AutoRollbackAllowed=false；资源按回滚能力与 auto 开关。
// Decide/mapRollback 不再重复判断 scope。
type GatePolicy struct {
	Scope               Scope
	RollbackSupported   bool
	AutoRollbackAllowed bool
}

// Decide 按规格 §2.3 规则阶梯逐条判定（硬编码、确定性，禁止 LLM）。
// 规则序不可调换：rule5（flag/block → l2_escalate）必须晚于回滚候选判定、先于
// rule6 none（早期 rule6 前置会让低计数 flag/block 被错误判 none，裁决 R3）。
func Decide(policy GatePolicy, ev GateEvidence) GateAction {
	// 规则 1：人工评审确认劣化/回滚 → 回滚候选。
	switch ev.ReviewVerdict {
	case ReviewVerdictConfirmRegression, ReviewVerdictConfirmRollback:
		return mapRollback(policy)
	}
	// 规则 2：规则阻断数 ≥ 阈值 → 回滚候选（平台仍 manual，由 mapRollback 折叠）。
	if ev.RuleBlockCount >= constants.GateRuleBlockRollbackMin {
		return mapRollback(policy)
	}
	// 规则 3：行为异常数 ≥ 阈值 且 确认 run 劣化超阈值 → 回滚候选。
	if ev.AnomalyCount >= constants.GateAnomalyRollbackMin && runRegressed(ev) {
		return mapRollback(policy)
	}
	// 规则 5（先于规则 6）：未达回滚候选但有 flag/block → l2_escalate。
	if ev.JudgeFlagCount > 0 || ev.RuleBlockCount > 0 {
		return GateL2Escalate
	}
	// 规则 6：异常低于告警阈值 且 无 run 级劣化 → none。
	if ev.AnomalyCount < constants.GateAnomalyAlertMin && !runRegressed(ev) {
		return GateNone
	}
	// 兜底：run 劣化或异常 ≥ 告警阈值但无 flag/block → l2_escalate（安全偏向人工）。
	return GateL2Escalate
}

// mapRollback 把回滚候选映射为动作：不支持回滚 → l2_escalate；
// 支持且允许自动 → rollback_auto；否则 rollback_manual（含平台 scope）。
func mapRollback(policy GatePolicy) GateAction {
	switch {
	case !policy.RollbackSupported:
		return GateL2Escalate
	case policy.AutoRollbackAllowed:
		return GateRollbackAuto
	default:
		return GateRollbackManual
	}
}

// runRegressed 报告确认 run 是否劣化：仅 ConfirmationRun 存在且 Regressed 为真。
func runRegressed(ev GateEvidence) bool {
	return ev.ConfirmationRun != nil && ev.ConfirmationRun.Regressed
}

// GateConfig 是门禁的实时生效开关（函数型依赖每次评估读取，平台键改动能实时生效，
// 裁决：不缓存静态值）。Enabled 来自 evaluation.gate.enabled；ResourceAutoRollbackEnabled
// 来自 evaluation.gate.auto_rollback_resources（仅资源 scope 决策，平台恒 manual）。
type GateConfig struct {
	Enabled                     bool
	ResourceAutoRollbackEnabled bool
}

// GateActionRecord 是一条门禁台账行，字段映射 eval_gate_actions 列（spec §4.1.2）。
// infra 写入时补 id/created_at/host_tenant_id；approval_id 由人工审批流回填。
type GateActionRecord struct {
	Scope      Scope
	Target     GateTarget
	Layer      string         // 触发层：observation（后续 optimization/casegen）
	Decision   GateAction     // 台账 decision 列文本
	Action     string         // 动作形态：rollback_recommended / escalate / ""
	Evidence   map[string]any // 决策证据（窗口计数等），JSONB 落库
	Actor      string         // 空由 infra 落默认（gate）
	ApprovalID string         // 人工审批 agent_tool_approvals id（后续卡回填）
}

// GateTargetForObservation 从一条观测推导门禁目标。资源观测的锚点是已执行的资源
// revision（Param.Resource.Ref，来自执行证据 Assignments 的 RevisionID）：观测携带
// Ref 即锚定被测资源，RevisionID 取 Ref。Version 只表示变体标签（canary 等，普通
// 资源运行恒空），不能参与锚定判定，否则非变体产品资源观测会被漏判、变体标签会
// 被当 revision 落库。平台锚点（Platform.GroupKey 非空且 VersionSeq>0）且无资源
// Ref 锚点 → 平台组目标；资源 Ref 锚点存在（obs.Resource.ResourceID + Resource.Ref）
// → 资源目标（有平台锚点时资源优先：回滚被测资源才可能恢复行为）；两者皆无 →
// 不可评估（mapping 只认锚点）。
func GateTargetForObservation(obs EvalObservation) (GateTarget, bool) {
	p := obs.Param
	platformAnchored := p.Platform.GroupKey != "" && p.Platform.VersionSeq > 0
	resourceAnchored := obs.Resource.ResourceID != "" && p.Resource.Ref != ""
	switch {
	case platformAnchored && !resourceAnchored:
		return GateTarget{
			Scope:      ScopePlatform,
			GroupKey:   p.Platform.GroupKey,
			VersionSeq: p.Platform.VersionSeq,
		}, true
	case resourceAnchored:
		return GateTarget{
			Scope:      ScopeResource,
			Kind:       string(obs.Resource.Kind),
			ResourceID: obs.Resource.ResourceID,
			RevisionID: p.Resource.Ref,
		}, true
	default:
		return GateTarget{}, false
	}
}
