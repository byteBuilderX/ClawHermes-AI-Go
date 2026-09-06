package domain

import (
	"testing"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// gateTestCase 折叠一个 Decide 输入与期望动作（表驱动）。
type gateTestCase struct {
	name   string
	policy GatePolicy
	ev     GateEvidence
	want   GateAction
}

// confirmReviewVerdict 生成一个已确认回滚的人工结论。
func confirmReviewVerdict() ReviewVerdict { return ReviewVerdictConfirmRollback }

func TestDecideRollbackCandidatesMapToActions(t *testing.T) {
	// 平台 scope 恒 manual：AutoRollbackAllowed=false 已由 policy 折叠（裁决 R4）。
	platform := GatePolicy{Scope: ScopePlatform, RollbackSupported: true, AutoRollbackAllowed: false}
	resourceAuto := GatePolicy{Scope: ScopeResource, RollbackSupported: true, AutoRollbackAllowed: true}
	resourceManual := GatePolicy{Scope: ScopeResource, RollbackSupported: true, AutoRollbackAllowed: false}
	noRollback := GatePolicy{Scope: ScopeResource, RollbackSupported: false, AutoRollbackAllowed: false}

	cases := []gateTestCase{
		{
			name:   "rule1 human confirm rollback -> platform manual",
			policy: platform,
			ev:     GateEvidence{ReviewVerdict: confirmReviewVerdict()},
			want:   GateRollbackManual,
		},
		{
			name:   "rule2 rule blocks >= min -> resource manual",
			policy: resourceManual,
			ev:     GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin},
			want:   GateRollbackManual,
		},
		{
			name:   "rule2 rule blocks >= min -> resource auto",
			policy: resourceAuto,
			ev:     GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin + 1},
			want:   GateRollbackAuto,
		},
		{
			name:   "rollback unsupported -> l2 escalate even when auto allowed absent",
			policy: noRollback,
			ev:     GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin},
			want:   GateL2Escalate,
		},
		{
			name:   "rule2 rule blocks >= min -> platform manual",
			policy: platform,
			ev:     GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin},
			want:   GateRollbackManual,
		},
		{
			name:   "rule3 anomalies >= rollback min and confirmation regressed -> resource auto",
			policy: resourceAuto,
			ev: GateEvidence{
				AnomalyCount:    constants.GateAnomalyRollbackMin,
				ConfirmationRun: &RunComparison{Regressed: true},
			},
			want: GateRollbackAuto,
		},
		{
			name:   "rule3 anomalies high but no confirmation run -> escalate not rollback",
			policy: resourceAuto,
			ev:     GateEvidence{AnomalyCount: constants.GateAnomalyRollbackMin + 2},
			want:   GateL2Escalate,
		},
		{
			// 异常数 ≥ 告警 3 但 < 回滚门槛 10：即使确认 run 劣化也不回滚，
			// rule6 none 又因 ≥ 告警阈值不触发 → 兜底 escalate。
			name:   "rule3 anomaly within [alert, rollback) with regression -> escalate",
			policy: resourceAuto,
			ev: GateEvidence{
				AnomalyCount:    constants.GateAnomalyAlertMin + 2,
				ConfirmationRun: &RunComparison{Regressed: true},
			},
			want: GateL2Escalate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.policy, tc.ev); got != tc.want {
				t.Fatalf("Decide() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideRuleOrderingFlagBeforeNone(t *testing.T) {
	// 裁决 R3：rule5（flag/block → l2_escalate）必须先于 rule6（none）。
	// 单条规则阻断（低于回滚门槛 3）仍须 escalate，不能因"低计数"被判 none。
	platform := GatePolicy{Scope: ScopePlatform, RollbackSupported: true, AutoRollbackAllowed: false}
	cases := []gateTestCase{
		{
			name:   "single rule block below rollback min -> escalate",
			policy: platform,
			ev:     GateEvidence{RuleBlockCount: 1},
			want:   GateL2Escalate,
		},
		{
			name:   "single judge flag -> escalate",
			policy: platform,
			ev:     GateEvidence{JudgeFlagCount: 1},
			want:   GateL2Escalate,
		},
		{
			name:   "clean window -> none",
			policy: platform,
			ev:     GateEvidence{},
			want:   GateNone,
		},
		{
			name:   "run regressed without flag/block -> escalate (rule6 none guard)",
			policy: platform,
			ev: GateEvidence{
				AnomalyCount:    1,
				ConfirmationRun: &RunComparison{Regressed: true},
			},
			want: GateL2Escalate,
		},
		{
			name:   "anomalies below alert with regression -> escalate not none",
			policy: platform,
			ev: GateEvidence{
				ConfirmationRun: &RunComparison{Regressed: true},
			},
			want: GateL2Escalate,
		},
		{
			name:   "anomalies at alert floor without flags -> escalate",
			policy: platform,
			ev:     GateEvidence{AnomalyCount: constants.GateAnomalyAlertMin},
			want:   GateL2Escalate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.policy, tc.ev); got != tc.want {
				t.Fatalf("Decide() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMapRollback(t *testing.T) {
	cases := []struct {
		name   string
		policy GatePolicy
		want   GateAction
	}{
		{"unsupported -> escalate", GatePolicy{Scope: ScopeResource, RollbackSupported: false}, GateL2Escalate},
		{
			name:   "supported + auto -> auto",
			policy: GatePolicy{Scope: ScopeResource, RollbackSupported: true, AutoRollbackAllowed: true},
			want:   GateRollbackAuto,
		},
		{"supported + manual -> manual", GatePolicy{Scope: ScopePlatform, RollbackSupported: true}, GateRollbackManual},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapRollback(tc.policy); got != tc.want {
				t.Fatalf("mapRollback() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunRegressed(t *testing.T) {
	if runRegressed(GateEvidence{}) {
		t.Fatal("runRegressed on empty evidence must be false")
	}
	if runRegressed(GateEvidence{ConfirmationRun: &RunComparison{Regressed: false}}) {
		t.Fatal("runRegressed with Regressed=false must be false")
	}
	if !runRegressed(GateEvidence{ConfirmationRun: &RunComparison{Regressed: true}}) {
		t.Fatal("runRegressed with Regressed=true must be true")
	}
}

func TestGateActionValuesMatchLedgerDecisionText(t *testing.T) {
	// 台账 decision 列直接存 GateAction 文本（eval_gate_actions.decision），
	// 常量值即落库值：锁定精确拼写，防改名/错字悄悄改台账。
	cases := []struct {
		action GateAction
		want   string
	}{
		{GateNone, "none"},
		{GateL2Escalate, "l2_escalate"},
		{GateRollbackManual, "rollback_manual"},
		{GateRollbackAuto, "rollback_auto"},
	}
	for _, tc := range cases {
		if got := string(tc.action); got != tc.want {
			t.Fatalf("GateAction text = %q, want %q", got, tc.want)
		}
	}
}

func TestReviewVerdictValuesPinned(t *testing.T) {
	// ReviewVerdict 文本随证据 JSONB review_verdict 落台账（evidencePayload），
	// 锁定精确拼写，防改名/错字悄悄改人工确认结论口径。
	cases := []struct {
		verdict ReviewVerdict
		want    string
	}{
		{ReviewVerdictConfirmRegression, "confirm_regression"},
		{ReviewVerdictConfirmRollback, "confirm_rollback"},
	}
	for _, tc := range cases {
		if got := string(tc.verdict); got != tc.want {
			t.Fatalf("ReviewVerdict text = %q, want %q", got, tc.want)
		}
	}
}

func TestGateTargetKey(t *testing.T) {
	// Key 是冷却/去重与窗口聚合的稳定键：平台键只含组，资源键含 kind/resource/revision，
	// 两者不串扰、不带 version 等易变标签。
	cases := []struct {
		name string
		t    GateTarget
		want string
	}{
		{
			name: "platform scope key pins group only",
			t:    GateTarget{Scope: ScopePlatform, GroupKey: "agent", VersionSeq: 2},
			want: "platform:agent",
		},
		{
			name: "resource scope key joins kind resource and revision",
			t:    GateTarget{Scope: ScopeResource, Kind: "skill", ResourceID: "s1", RevisionID: "rev-9", VersionSeq: 2},
			want: "resource:skill:s1:rev-9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.Key(); got != tc.want {
				t.Fatalf("GateTarget.Key() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunRegressionDeltaThresholdIsNegative(t *testing.T) {
	if constants.RunRegressionDeltaThreshold >= 0 {
		t.Fatal("RunRegressionDeltaThreshold must be negative (dimension delta below baseline)")
	}
}
