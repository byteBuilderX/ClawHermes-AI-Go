package application

import (
	"context"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

// attributionTestRun builds an EvalRun whose failed case attribution keys
// carry classifier keywords (spec §9 / §6.2 failure_reason semantics).
func attributionTestRun(results []domain.EvalCaseResult) domain.EvalRun {
	return domain.EvalRun{
		ID:              "run-attrib-1",
		Resource:        domain.ResourceRef{Kind: domain.ResourceKindAgent, ResourceID: "agent-x"},
		SuiteRevisionID: "rev-1",
		TotalCases:      len(results),
		Results:         results,
	}
}

func failedCase(id, failureReason, processFailure, message string) domain.EvalCaseResult {
	return domain.EvalCaseResult{CaseID: id, Passed: false,
		FailureReason: failureReason, ProcessFailure: processFailure, Message: message}
}

func TestAnalyzeRunEmptyWhenNoFailures(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		{CaseID: "a", Passed: true},
		{CaseID: "b", Passed: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedCases != 0 {
		t.Fatalf("FailedCases = %d, want 0", report.FailedCases)
	}
	if report.Diagnosis.TotalFailures != 0 {
		t.Fatalf("Diagnosis.TotalFailures = %d, want 0", report.Diagnosis.TotalFailures)
	}
	if len(report.Clusters) != 0 || len(report.TunableDirections) != 0 {
		t.Fatalf("expected no clusters/directions for a passing run, got %d/%d",
			len(report.Clusters), len(report.TunableDirections))
	}
	if report.Advanceable {
		t.Fatal("a passing run must not be advanceable")
	}
}

func TestAnalyzeRunClassifiesTimeoutToModelConfig(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		failedCase("case-timeout-retry", "execution", "", "context deadline exceeded; retry exhausted"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedCases != 1 || report.Diagnosis.TotalFailures != 1 {
		t.Fatalf("want 1 attributed failure, got run=%d diagnosis=%d",
			report.FailedCases, report.Diagnosis.TotalFailures)
	}
	if report.Diagnosis.CategoryBreakdown[domain.CatModelConfig] < 1 {
		t.Errorf("expected model_config category from timeout failure, got %+v",
			report.Diagnosis.CategoryBreakdown)
	}
	// Model-config tunables surface as concrete adjustment directions.
	assertHasDirection(t, report, "temperature")
	assertDirectionSpace(t, report, "temperature", TunableSpaceGridSearch)
	if !report.Advanceable {
		t.Fatal("a failed run with directions must be advanceable")
	}
}

func TestAnalyzeRunClassifiesToolExecToRetriesTimeout(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		failedCase("tool-schema-case", "execution", "", "invalid json: structured output parse error"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if report.Diagnosis.CategoryBreakdown[domain.CatToolExec] < 1 {
		t.Errorf("expected tool_exec category, got %+v", report.Diagnosis.CategoryBreakdown)
	}
	assertHasDirection(t, report, "max_retries")
	assertHasDirection(t, report, "timeout_ms")
}

func TestAnalyzeRunClustersByFailureReason(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		failedCase("timeout-1", "execution", "", "timed out"),
		failedCase("timeout-2", "execution", "", "context deadline exceeded"),
		failedCase("dim-1", "dimension:relevance_score", "", "low relevance"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Clusters) != 2 {
		t.Fatalf("expected 2 clusters (execution / dimension), got %d: %+v",
			len(report.Clusters), report.Clusters)
	}
	for _, c := range report.Clusters {
		switch c.Reason {
		case "execution":
			if c.Count != 2 || len(c.Cases) != 2 {
				t.Errorf("execution cluster = %+v, want 2 cases", c)
			}
		case "dimension:relevance_score":
			if c.Count != 1 {
				t.Errorf("dimension cluster = %+v, want 1", c)
			}
		default:
			t.Errorf("unexpected cluster reason %q", c.Reason)
		}
	}
}

func TestAnalyzeRunJudgeDimensionYieldsEvalChainOnly(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		failedCase("dim-1", "dimension:relevance_score", "", "output not grounded in context"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	// I1: a judge-dimension failure is attributable to the evaluation harness,
	// never to the agent under evaluation. It must not leak an agent-level
	// tunable direction even though the judge message carries keywords the
	// Diagnoser would otherwise classify ("context"/"memory" → memory prompts).
	if len(report.TunableDirections) != 0 {
		t.Fatalf("judge-dimension failure must not yield agent tunable directions, got %+v",
			report.TunableDirections)
	}
	if report.Diagnosis.TotalFailures != 0 {
		t.Fatalf("Diagnosis.TotalFailures = %d, want 0 for a harness-only run",
			report.Diagnosis.TotalFailures)
	}
	found := false
	for _, d := range report.EvalChainDirections {
		if d.PlatformKey == "evaluation.judge.temperature" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected evaluation.judge.temperature direction, got %+v",
			report.EvalChainDirections)
	}
	if !report.Advanceable {
		t.Fatal("a harness-attributable direction must keep the run advanceable")
	}
	clustersMatch := false
	for _, c := range report.Clusters {
		if c.Reason == "dimension:relevance_score" && c.Count == 1 {
			clustersMatch = true
		}
	}
	if !clustersMatch {
		t.Errorf("expected dimension:relevance_score cluster, got %+v", report.Clusters)
	}
}

func TestAnalyzeRunProcessAssertionFlagsRuleGuardDirection(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		failedCase("delete-hit", "", "process:must_not_call:delete", ""),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Clusters) != 1 || report.Clusters[0].Reason != "process:must_not_call:delete" {
		t.Fatalf("expected process cluster, got %+v", report.Clusters)
	}
	// Process-assertion failures belong to the harness bucket: no agent tunable
	// direction may leak from the raw assertion text.
	if len(report.TunableDirections) != 0 {
		t.Fatalf("process-assertion failure must not yield agent tunable directions, got %+v",
			report.TunableDirections)
	}
	found := false
	for _, d := range report.EvalChainDirections {
		if d.PlatformKey == "evaluation.ruleguard.enabled" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected evaluation.ruleguard.enabled direction, got %+v",
			report.EvalChainDirections)
	}
}

func TestAnalyzeRunOutputAndMustNotCallConcurrentKeepsRuleGuard(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		failedCase("del-1", "execution", "process:must_not_call:delete", "context deadline exceeded"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	// The case carries both an output failure and a process-assertion safety
	// signal. primaryFailureReason keeps "execution" as the output reason…
	if len(report.Clusters) != 1 || report.Clusters[0].Reason != "execution" {
		t.Fatalf("expected a single execution cluster, got %+v", report.Clusters)
	}
	// …but the EvalChain scan must still see process:must_not_call so the
	// ruleguard safety direction is not masked by the output reason (I2).
	found := false
	for _, d := range report.EvalChainDirections {
		if d.PlatformKey == "evaluation.ruleguard.enabled" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected evaluation.ruleguard.enabled despite output reason, got %+v",
			report.EvalChainDirections)
	}
	// A harness safety signal takes the whole case out of the agent bucket.
	if len(report.TunableDirections) != 0 {
		t.Fatalf("expected no agent tunable directions, got %+v", report.TunableDirections)
	}
}

func TestAnalyzeRunTagsGridSearchAndPromptSpaces(t *testing.T) {
	svc := NewAttributionService()
	report, err := svc.AnalyzeRun(context.Background(), attributionTestRun([]domain.EvalCaseResult{
		failedCase("to-1", "execution", "", "context deadline exceeded; retry exhausted"),
		failedCase("mem-1", "execution", "", "forgot the earlier context"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	// I3: every direction must carry a Space consistent with the domain
	// allowlists it belongs to — grid-search keys stay out of the prompt
	// bucket and prompt keys stay out of the grid-search bucket.
	var sawGrid, sawPrompt bool
	for _, d := range report.TunableDirections {
		switch d.Space {
		case TunableSpaceGridSearch:
			if !domain.IsGridSearchableParameter(d.Key) {
				t.Errorf("grid_search direction %q is not grid-searchable", d.Key)
			}
			if d.Key == "temperature" {
				sawGrid = true
			}
		case TunableSpacePrompt:
			if !domain.IsPromptTunableField(d.Key) {
				t.Errorf("prompt direction %q is not a prompt field", d.Key)
			}
			sawPrompt = true
		default:
			t.Errorf("direction %q has unrecognized space %q", d.Key, d.Space)
		}
	}
	if !sawGrid {
		t.Errorf("expected a grid_search direction for temperature, got %+v",
			report.TunableDirections)
	}
	if !sawPrompt {
		t.Errorf("expected a prompt direction for memory_*_prompt, got %+v",
			report.TunableDirections)
	}
}

func TestBuildTunableDirectionsDropsKeysOutsideAllowlists(t *testing.T) {
	svc := NewAttributionService()
	got := svc.buildTunableDirections(domain.DiagnosisReport{
		AffectedTunables: []string{"temperature", "not_a_real_key"},
	})
	if len(got) != 1 {
		t.Fatalf("buildTunableDirections returned %d directions, want 1 (unknown key dropped): %+v",
			len(got), got)
	}
	if got[0].Key != "temperature" || got[0].Space != TunableSpaceGridSearch {
		t.Errorf("got %+v, want a single temperature/grid_search direction", got)
	}
}

func assertHasDirection(t *testing.T, report AttributionReport, key string) {
	t.Helper()
	for _, d := range report.TunableDirections {
		if d.Key == key {
			return
		}
	}
	t.Errorf("expected tunable direction %q, got %+v", key, report.TunableDirections)
}

func assertDirectionSpace(t *testing.T, report AttributionReport, key string, space TunableSpace) {
	t.Helper()
	for _, d := range report.TunableDirections {
		if d.Key == key {
			if d.Space != space {
				t.Errorf("direction %q space = %q, want %q", key, d.Space, space)
			}
			return
		}
	}
	t.Errorf("expected tunable direction %q, got %+v", key, report.TunableDirections)
}
