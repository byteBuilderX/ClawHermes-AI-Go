package domain

import (
	"encoding/json"
	"testing"
)

// TestAssertionResultConfidenceRoundTrip 断言 Confidence 进入序列化契约；
// 缺失 confidence 反序列化为 0（解析层负责回退 1.0，domain 不静默改值）。
func TestAssertionResultConfidenceRoundTrip(t *testing.T) {
	raw := []byte(`{"passed":true,"message":"ok","confidence":0.8}`)
	var got AssertionResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Confidence != 0.8 {
		t.Fatalf("confidence = %v, want 0.8", got.Confidence)
	}
	// 缺失 confidence 原样保留 0，由 parseJudgeResponse 层回退。注意必须用
	// 全新零值结构：json.Unmarshal 对已存在结构只合并不清理，复用 got 会残留
	// 上一次的 0.8，测的是合并语义而非反序列化契约。
	var missing AssertionResult
	if err := json.Unmarshal([]byte(`{"passed":false,"message":"x"}`), &missing); err != nil {
		t.Fatalf("unmarshal missing confidence: %v", err)
	}
	if missing.Confidence != 0 {
		t.Fatalf("confidence = %v, want 0 (unset)", missing.Confidence)
	}
}

// TestEvalCaseResultIDJSON 断言 ID 字段存在且缺省为零值（runCase 生成前）。
func TestEvalCaseResultIDJSON(t *testing.T) {
	raw := []byte(`{"case_id":"c1","passed":true}`)
	var got EvalCaseResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "" {
		t.Fatalf("ID = %q, want empty", got.ID)
	}
	got.ID = "r-1"
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid json: %s", out)
	}
}

func TestTriggersForObservation(t *testing.T) {
	cfg := ReviewConfig{LowConfidenceThreshold: 0.6, JudgePassThreshold: 0.5}
	obs := func() *EvalObservation {
		return &EvalObservation{Resource: ObservationResourceRef{Kind: ResourceKindSkill, ResourceID: "s1"}}
	}

	t.Run("no judge signals yields no triggers", func(t *testing.T) {
		if got := TriggersForObservation(obs(), cfg); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})

	t.Run("behavior abandonment with flag verdict triggers behavior_anomaly", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Abandonment = true
		if got := TriggersForObservation(o, cfg); len(got) != 1 || got[0] != TriggerBehaviorAnomaly {
			t.Fatalf("got %v, want [behavior_anomaly]", got)
		}
	})

	t.Run("behavior escalation with flag verdict triggers behavior_anomaly", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Escalation = true
		if got := TriggersForObservation(o, cfg); len(got) != 1 || got[0] != TriggerBehaviorAnomaly {
			t.Fatalf("got %v, want [behavior_anomaly]", got)
		}
	})

	t.Run("behavior signals without flag verdict do not trigger", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictBlock
		o.Signals.Behavior.Abandonment = true
		if got := TriggersForObservation(o, cfg); containsReason(got, TriggerBehaviorAnomaly) {
			t.Fatalf("got %v, want no behavior_anomaly (block 无 judge 仍不进池)", got)
		}
	})

	t.Run("retry signal alone does not trigger behavior_anomaly", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Retry = true
		if got := TriggersForObservation(o, cfg); containsReason(got, TriggerBehaviorAnomaly) {
			t.Fatalf("got %v, want no behavior_anomaly", got)
		}
	})

	t.Run("behavior_anomaly accumulates with judge triggers when both present", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictFlag
		o.Signals.Behavior.Abandonment = true
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 1.0, Confidence: 0.4}}
		got := TriggersForObservation(o, cfg)
		if !containsReason(got, TriggerBehaviorAnomaly) || !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want [behavior_anomaly low_confidence]", got)
		}
	})

	t.Run("low confidence triggers", func(t *testing.T) {
		o := obs()
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 1.0, Confidence: 0.4}}
		if got := TriggersForObservation(o, cfg); len(got) != 1 || got[0] != TriggerLowConfidence {
			t.Fatalf("got %v, want [low_confidence]", got)
		}
	})

	t.Run("dimension split triggers", func(t *testing.T) {
		o := obs()
		o.Signals.Judge = []JudgeSignal{
			{Dimension: "faithfulness", Score: 1.0, Confidence: 0.9},
			{Dimension: "relevance", Score: 0.2, Confidence: 0.9},
		}
		if got := TriggersForObservation(o, cfg); !containsReason(got, TriggerDimensionSplit) {
			t.Fatalf("got %v, want dimension_split present", got)
		}
	})

	t.Run("rule conflict triggers only when all judge pass and verdict block", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictBlock
		o.Signals.Rule = []RuleSignal{{Rule: "r1"}}
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 1.0, Confidence: 0.9}}
		got := TriggersForObservation(o, cfg)
		if !containsReason(got, TriggerJudgeRuleConflict) {
			t.Fatalf("got %v, want judge_rule_conflict present", got)
		}
	})

	t.Run("rule conflict suppressed when judge below threshold", func(t *testing.T) {
		o := obs()
		o.Verdict = VerdictBlock
		o.Signals.Rule = []RuleSignal{{Rule: "r1"}}
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 0.2, Confidence: 0.9}}
		if got := TriggersForObservation(o, cfg); containsReason(got, TriggerJudgeRuleConflict) {
			t.Fatalf("got %v, want no judge_rule_conflict", got)
		}
	})

	t.Run("boundary confidence triggers low confidence", func(t *testing.T) {
		o := obs()
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 1.0, Confidence: 0.5, Reason: "理由充分"}}
		if got := TriggersForObservation(o, cfg); !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want low_confidence present", got)
		}
	})

	t.Run("vague reason triggers low confidence", func(t *testing.T) {
		o := obs()
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 1.0, Confidence: 0.9, Reason: "不确定"}}
		if got := TriggersForObservation(o, cfg); !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want low_confidence present", got)
		}
	})

	t.Run("substantive reason and high confidence do not trigger", func(t *testing.T) {
		o := obs()
		o.Signals.Judge = []JudgeSignal{{Dimension: "faithfulness", Score: 1.0, Confidence: 0.9, Reason: "输出完全符合预期"}}
		if got := TriggersForObservation(o, cfg); containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want no low_confidence", got)
		}
	})

	t.Run("nil observation yields no triggers", func(t *testing.T) {
		if got := TriggersForObservation(nil, cfg); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})
}

// TestTriggersForProcessConflict 覆盖过程/输出断言不一致的入池触发（§6.5 §6.6）：
// 仅 output 通过 + 过程失败（true,false）触发 process_output_conflict，其余组合不触发。
func TestTriggersForProcessConflict(t *testing.T) {
	t.Run("output pass and process fail triggers conflict", func(t *testing.T) {
		got := TriggersForProcessConflict(true, false)
		if len(got) != 1 || got[0] != TriggerProcessOutputConflict {
			t.Fatalf("got %v, want [process_output_conflict]", got)
		}
	})

	t.Run("both pass yields no triggers", func(t *testing.T) {
		if got := TriggersForProcessConflict(true, true); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})

	t.Run("both fail yields no triggers", func(t *testing.T) {
		if got := TriggersForProcessConflict(false, false); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})

	t.Run("output fail and process pass yields no triggers", func(t *testing.T) {
		if got := TriggersForProcessConflict(false, true); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})
}

func TestTriggersForCaseResult(t *testing.T) {
	cfg := ReviewConfig{LowConfidenceThreshold: 0.6}
	// 空 Message 现在按 spec §6.6 视为含糊（hasVagueReason("")=true）→ 触发 low_confidence；
	// 非触发场景必须携带实质理由。
	passing := AssertionResult{Passed: true, Confidence: 0.9, Message: "输出完全符合预期"}

	t.Run("passing assertion yields no triggers", func(t *testing.T) {
		if got := TriggersForCaseResult(false, true, true, passing, cfg); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})

	t.Run("needs review triggers", func(t *testing.T) {
		got := TriggersForCaseResult(true, true, true, passing, cfg)
		if !containsReason(got, TriggerNeedsReview) {
			t.Fatalf("got %v, want needs_review present", got)
		}
	})

	t.Run("low confidence triggers", func(t *testing.T) {
		got := TriggersForCaseResult(false, true, true, AssertionResult{Passed: true, Confidence: 0.3}, cfg)
		if !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want low_confidence present", got)
		}
	})

	t.Run("both triggers coexist", func(t *testing.T) {
		got := TriggersForCaseResult(true, true, true, AssertionResult{Passed: false, Confidence: 0.2}, cfg)
		if !containsReason(got, TriggerNeedsReview) || !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want needs_review + low_confidence", got)
		}
	})

	t.Run("boundary confidence triggers low confidence", func(t *testing.T) {
		got := TriggersForCaseResult(false, true, true, AssertionResult{Passed: true, Confidence: 0.5}, cfg)
		if !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want low_confidence present", got)
		}
	})

	t.Run("vague overall reason triggers low confidence", func(t *testing.T) {
		got := TriggersForCaseResult(false, true, true, AssertionResult{Passed: true, Confidence: 0.9, Message: "无法判断"}, cfg)
		if !containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want low_confidence present", got)
		}
	})

	t.Run("substantive message and high confidence do not trigger", func(t *testing.T) {
		got := TriggersForCaseResult(false, true, true, AssertionResult{Passed: true, Confidence: 0.9, Message: "输出完全符合预期"}, cfg)
		if containsReason(got, TriggerLowConfidence) {
			t.Fatalf("got %v, want no low_confidence", got)
		}
	})

	t.Run("low confidence plus output pass and process fail", func(t *testing.T) {
		got := TriggersForCaseResult(false, true, false, AssertionResult{Passed: true, Confidence: 0.3}, cfg)
		want := []ReviewTriggerReason{TriggerLowConfidence, TriggerProcessOutputConflict}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

// TestReviewTriggerReasonRiskLevel 断言入池原因到评审优先级（high/medium/low）的映射
// （spec §6.6 规模控制：评审池按风险排序，安全/写操作/高危资源优先）。
func TestReviewTriggerReasonRiskLevel(t *testing.T) {
	cases := []struct {
		reason ReviewTriggerReason
		want   ReviewRiskLevel
	}{
		{TriggerJudgeRuleConflict, ReviewRiskHigh},
		{TriggerProcessOutputConflict, ReviewRiskHigh},
		{TriggerLowConfidence, ReviewRiskMedium},
		{TriggerDimensionSplit, ReviewRiskMedium},
		{TriggerNeedsReview, ReviewRiskMedium},
		{TriggerBehaviorAnomaly, ReviewRiskMedium},
		{TriggerTrajectoryFailed, ReviewRiskMedium},
		{ReviewTriggerReason("unknown_future"), ReviewRiskLow},
	}
	for _, tc := range cases {
		t.Run(string(tc.reason), func(t *testing.T) {
			if got := tc.reason.RiskLevel(); got != tc.want {
				t.Fatalf("RiskLevel(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

func containsReason(got []ReviewTriggerReason, want ReviewTriggerReason) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

func TestIsBoundaryConfidence(t *testing.T) {
	cases := []struct {
		name string
		conf float64
		want bool
	}{
		{"boundary low edge", 0.45, true},
		{"boundary midpoint", 0.50, true},
		{"boundary high edge", 0.55, true},
		{"below boundary", 0.44, false},
		{"above boundary", 0.56, false},
		{"normal high", 0.6, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBoundaryConfidence(tc.conf); got != tc.want {
				t.Fatalf("isBoundaryConfidence(%v) = %v, want %v", tc.conf, got, tc.want)
			}
		})
	}
}

func TestHasVagueReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   bool
	}{
		{"empty reason is vague", "", true},
		{"whitespace only is vague", "   ", true},
		{"too short reason is vague", "pass", true},
		{"hedging word is vague", "无法确定答案是否正确", true},
		{"maybe is vague", "可能正确", true},
		{"substantive reason is not vague", "输出完全符合预期，无任何偏差或遗漏", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasVagueReason(tc.reason); got != tc.want {
				t.Fatalf("hasVagueReason(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}
