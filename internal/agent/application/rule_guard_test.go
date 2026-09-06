package application

import (
	"context"
	"testing"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

// ruleGuardMetricsSpy 记录 IncEvalRuleHit/IncEvalGateAction 调用，锁 R20 label 契约。
type ruleGuardMetricsSpy struct {
	observability.NoopMetrics
	ruleHits    []ruleHitCall
	gateActions []gateActionCall
}

type ruleHitCall struct{ rule, resource, verdict string }
type gateActionCall struct{ layer, action string }

func (s *ruleGuardMetricsSpy) IncEvalRuleHit(rule, resource, verdict string) {
	s.ruleHits = append(s.ruleHits, ruleHitCall{rule, resource, verdict})
}

func (s *ruleGuardMetricsSpy) IncEvalGateAction(layer, action string) {
	s.gateActions = append(s.gateActions, gateActionCall{layer, action})
}

func TestRuleGuardCheck(t *testing.T) {
	always := func(context.Context) bool { return true }
	never := func(context.Context) bool { return false }
	noDenylist := func(context.Context) []string { return nil }
	cases := []struct {
		name           string
		enabled        func(context.Context) bool
		denylist       func(context.Context) []string
		toolID         string
		skipGuard      bool // nil guard 用例
		wantBlocked    bool
		wantVerdict    string // "" = 不应产生 hit
		wantGateAction bool
		wantCollector  int
	}{
		{name: "enabled hit blocks and observes", enabled: always,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "danger_tool",
			wantBlocked: true, wantVerdict: "block", wantGateAction: true, wantCollector: 1},
		{name: "not listed allows with zero observation", enabled: always,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "safe_tool",
			wantCollector: 0},
		{name: "disabled hit detects but does not block", enabled: never,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "danger_tool",
			wantVerdict: "detected", wantCollector: 1},
		{name: "nil enabled treated as disabled", enabled: nil,
			denylist: func(context.Context) []string { return []string{"danger_tool"} }, toolID: "danger_tool",
			wantVerdict: "detected", wantCollector: 1},
		{name: "case-insensitive match blocks", enabled: always,
			denylist: func(context.Context) []string { return []string{"DANGER_Tool"} }, toolID: "danger_tool",
			wantBlocked: true, wantVerdict: "block", wantGateAction: true, wantCollector: 1},
		{name: "empty denylist entries skipped", enabled: always,
			denylist: func(context.Context) []string { return []string{"", "   "} }, toolID: "danger_tool",
			wantCollector: 0},
		{name: "nil denylist zero hit zero observation", enabled: always, denylist: noDenylist,
			toolID: "danger_tool", wantCollector: 0},
		{name: "nil guard allows", toolID: "danger_tool", skipGuard: true, wantCollector: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &ruleGuardMetricsSpy{}
			var g *RuleGuard
			if !tc.skipGuard {
				g = NewRuleGuard(RuleGuardDeps{Enabled: tc.enabled, Denylist: tc.denylist, Metrics: spy})
			}
			blocks := &[]domain.RuleBlock{}
			ctx := context.WithValue(context.Background(), ruleBlockCollectorKey{}, blocks)
			block, blocked := g.Check(ctx, tc.toolID)
			if blocked != tc.wantBlocked || (block != nil) != tc.wantBlocked {
				t.Fatalf("blocked=%v block=%v, want blocked=%v", blocked, block, tc.wantBlocked)
			}
			if len(*blocks) != tc.wantCollector {
				t.Fatalf("collector len = %d, want %d", len(*blocks), tc.wantCollector)
			}
			wantHits := 0
			if tc.wantVerdict != "" {
				wantHits = 1
			}
			if len(spy.ruleHits) != wantHits {
				t.Fatalf("rule hits = %v, want %d", spy.ruleHits, wantHits)
			}
			if tc.wantVerdict != "" {
				if hit := spy.ruleHits[0]; hit.rule != "tool_denylist" || hit.resource != "agent" || hit.verdict != tc.wantVerdict {
					t.Fatalf("rule hit mismatch: %+v", hit)
				}
			}
			if tc.wantGateAction {
				if len(spy.gateActions) != 1 {
					t.Fatalf("gate actions = %v, want 1", spy.gateActions)
				}
				if act := spy.gateActions[0]; act.layer != "l1_rule" || act.action != "block" {
					t.Fatalf("gate action mismatch: %+v", act)
				}
			} else if len(spy.gateActions) != 0 {
				t.Fatalf("unexpected gate action: %v", spy.gateActions)
			}
		})
	}
}
