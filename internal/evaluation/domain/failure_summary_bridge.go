package domain

import (
	"fmt"
	"strings"
)

// FailedCaseSummaries converts the failed results of an evaluation run into
// the concise FailureSummary shape consumed by attribution and the optimizer
// pipeline (spec §9). It is the missing link between a real persisted run and
// the improvement loop: no production path derived FailureSummary from an
// EvalRun before this bridge existed.
//
// Only cases that failed the run gate (Passed=false, which already folds in
// the §6.5 process assertion) produce a summary; passed cases are noise for
// attribution. The Description aggregates every per-case attribution key —
// FailureReason, ProcessFailure, Error, Message — so the rule-based Diagnoser
// can classify the failure into a tunable category without extra plumbing.
//
// Expected is always empty: EvalCaseResult's data model does not persist an
// expected-output field (expected values belong to the suite side; the case
// result stores only actual/message). Consistency/difference attribution based
// on expected output needs suite-expected backfill — tracked in the §9 backlog,
// not synthesized here.
func FailedCaseSummaries(run EvalRun) []FailureSummary {
	out := make([]FailureSummary, 0, len(run.Results))
	for _, cr := range run.Results {
		if cr.Passed {
			continue
		}
		out = append(out, FailureSummary{
			CaseName:    cr.CaseID,
			Actual:      stringifyAny(cr.Actual),
			Description: describeFailure(cr),
		})
	}
	return out
}

// describeFailure joins the case's attribution keys into one classification
// text. Order is stable: primary output attribution, then process attribution,
// then execution error, then the judge/assertion message. A case with no
// signal at all yields a neutral fallback instead of an empty string.
func describeFailure(cr EvalCaseResult) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{cr.FailureReason, cr.ProcessFailure, cr.Error, cr.Message} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "evaluation failure"
	}
	return strings.Join(parts, "; ")
}

// stringifyAny renders a result's Actual payload into the FailureSummary's
// string field. nil and absent values yield "" (no signal to classify on).
func stringifyAny(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
