package domain

import (
	"testing"
)

func TestFailedCaseSummariesFiltersAndAggregates(t *testing.T) {
	run := EvalRun{
		ID:          "run-1",
		TotalCases:  4,
		PassedCases: 1,
		Results: []EvalCaseResult{
			// Passed case — must be filtered out.
			{CaseID: "pass-ok", Passed: true, Message: "ok"},
			// Output-assertion failure with a primary reason.
			{CaseID: "timeout-case", Passed: false, FailureReason: "execution",
				Error: "context deadline exceeded", Message: "case timed out"},
			// Process-assertion failure (independent of output pass).
			{CaseID: "delete-case", Passed: false, ProcessFailure: "process:must_not_call:delete",
				Message: "called a forbidden tool"},
			// Dimension-judge failure.
			{CaseID: "relevance-case", Passed: false, FailureReason: "dimension:relevance_score",
				Message: "low relevance"},
		},
	}

	summaries := FailedCaseSummaries(run)
	if len(summaries) != 3 {
		t.Fatalf("expected 3 failure summaries, got %d", len(summaries))
	}
	if summaries[0].CaseName != "timeout-case" {
		t.Errorf("summary[0].CaseName = %q, want timeout-case", summaries[0].CaseName)
	}
	// Description must aggregate the per-case attribution keys so the
	// rule-based Diagnoser can classify the failure.
	if got := summaries[0].Description; got != "execution; context deadline exceeded; case timed out" {
		t.Errorf("summary[0].Description = %q", got)
	}
	if got := summaries[1].Description; got != "process:must_not_call:delete; called a forbidden tool" {
		t.Errorf("summary[1].Description = %q", got)
	}
	if got := summaries[2].Description; got != "dimension:relevance_score; low relevance" {
		t.Errorf("summary[2].Description = %q", got)
	}
	if summaries[1].Actual != "" {
		t.Errorf("summary[1].Actual = %q, want empty when case has no actual", summaries[1].Actual)
	}
}

func TestFailedCaseSummariesEmptyWhenAllPass(t *testing.T) {
	run := EvalRun{TotalCases: 2, PassedCases: 2, Results: []EvalCaseResult{
		{CaseID: "a", Passed: true},
		{CaseID: "b", Passed: true, ProcessPass: true},
	}}
	if got := FailedCaseSummaries(run); len(got) != 0 {
		t.Fatalf("expected no summaries for an all-pass run, got %d", len(got))
	}
}

func TestFailedCaseSummariesNilResults(t *testing.T) {
	if got := FailedCaseSummaries(EvalRun{TotalCases: 0}); len(got) != 0 {
		t.Fatalf("expected no summaries for nil results, got %d", len(got))
	}
}

func TestFailedCaseSummariesDefaultDescription(t *testing.T) {
	summaries := FailedCaseSummaries(EvalRun{Results: []EvalCaseResult{
		{CaseID: "bare", Passed: false},
	}})
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Description != "evaluation failure" {
		t.Errorf("default Description = %q, want 'evaluation failure'", summaries[0].Description)
	}
}
