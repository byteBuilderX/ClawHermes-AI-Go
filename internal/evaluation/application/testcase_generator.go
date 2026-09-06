package application

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

var (
	// ErrSuiteDraftMissing means no draft revision exists where one is needed:
	// generation cannot scope its sampling without a kind-carrying draft,
	// draft-editing commands (add/delete case, start next draft) require the
	// suite's current draft, and a suite never published has no active revision
	// to seed a fresh draft from. 映射 409（先开启/继承草稿再操作）。
	ErrSuiteDraftMissing = errors.New("evaluation suite has no draft revision")
	// ErrNoSamplesFound means no (query, response, feedback) triplets exist
	// for the suite's resource kind.
	ErrNoSamplesFound = errors.New("no production samples found for suite resource kind")
	// ErrCaseGenUnavailable means the LLM gateway is not configured, so no
	// cases can be generated; the request fails loudly instead of silently
	// producing an empty draft.
	ErrCaseGenUnavailable = errors.New("case generator unavailable: LLM gateway not configured")
)

// RejectedCase records one sample the generator could not turn into a draft
// case, so failures stay visible instead of silently shrinking the set.
type RejectedCase struct {
	TraceID string `json:"trace_id"`
	Reason  string `json:"reason"`
}

// GenerateResult is the outcome of one generation pass.
type GenerateResult struct {
	SamplesFound int            `json:"samples_found"`
	Generated    int            `json:"generated"`
	Rejected     []RejectedCase `json:"rejected"`
}

// GenerateInput drives TestCaseGenerator.Generate.
type GenerateInput struct {
	TenantID    string
	SuiteID     string
	Policy      domain.SamplePolicy
	MaxCases    int
	RequestedBy string
}

// TestCaseGenerator turns production (query, response, feedback) samples
// into eval cases. Cases are written into the suite's draft revision and
// only become evidence after a human approves them through the publish
// flow — the generator never publishes automatically.
type TestCaseGenerator struct {
	samples port.CaseSampleSource
	gen     port.CaseGenerator
	suites  port.SuiteRepository
}

func NewTestCaseGenerator(
	samples port.CaseSampleSource,
	gen port.CaseGenerator,
	suites port.SuiteRepository,
) *TestCaseGenerator {
	return &TestCaseGenerator{samples: samples, gen: gen, suites: suites}
}

// Generate samples production interactions for the suite's resource kind,
// turns each into an eval case via the LLM generator, quality-filters the
// results and appends the survivors to the suite's draft revision. Samples
// that fail generation or filtering are reported in Rejected with their
// reason; they never enter the draft.
func (g *TestCaseGenerator) Generate(ctx context.Context, input GenerateInput) (GenerateResult, error) {
	if g.gen == nil || g.samples == nil {
		return GenerateResult{}, ErrCaseGenUnavailable
	}
	draft, err := g.resolveDraft(ctx, input.TenantID, input.SuiteID)
	if err != nil {
		return GenerateResult{}, err
	}
	samples, err := g.samples.ListSamples(ctx, input.TenantID, draft.ResourceKind, input.Policy, input.MaxCases)
	if err != nil {
		return GenerateResult{}, err
	}
	if len(samples) == 0 {
		return GenerateResult{}, ErrNoSamplesFound
	}
	accepted, rejected := g.generateCases(ctx, draft, samples)
	result := GenerateResult{SamplesFound: len(samples), Generated: len(accepted), Rejected: rejected}
	if len(accepted) > 0 {
		if err := g.suites.AddDraftCases(ctx, input.TenantID, draft.ID, accepted); err != nil {
			return GenerateResult{}, err
		}
	}
	return result, nil
}

// resolveDraft returns the suite's active draft revision, creating one for a
// suite that has none yet. A draft without a resource kind cannot scope the
// sampling, which fails loudly instead of generating into the void.
func (g *TestCaseGenerator) resolveDraft(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error) {
	draft, ok, err := g.suites.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !ok {
		draft, err = g.suites.CreateDraftRevision(ctx, tenantID, suiteID)
		if err != nil {
			return domain.EvalSuiteRevision{}, err
		}
	}
	if draft.ResourceKind == "" {
		return domain.EvalSuiteRevision{}, ErrSuiteDraftMissing
	}
	return draft, nil
}

// generateCases runs one LLM generation per sample, filters the results
// (assertion validity, dedupe against existing draft cases) and returns the
// survivors plus per-sample rejection reasons.
func (g *TestCaseGenerator) generateCases(ctx context.Context, draft domain.EvalSuiteRevision, samples []domain.CaseSample) ([]domain.EvalCase, []RejectedCase) {
	seen := newCaseDeduper(draft.Cases)
	accepted := make([]domain.EvalCase, 0, len(samples))
	var rejected []RejectedCase
	for _, sample := range samples {
		generated, err := g.gen.Generate(ctx, port.CaseGenRequest{
			ResourceKind: draft.ResourceKind,
			Sample:       sample,
		})
		if err != nil {
			rejected = append(rejected, RejectedCase{TraceID: sample.TraceID, Reason: err.Error()})
			continue
		}
		if reason, ok := generated.Validate(); !ok {
			rejected = append(rejected, RejectedCase{TraceID: sample.TraceID, Reason: reason})
			continue
		}
		if seen.duplicate(generated) {
			rejected = append(rejected, RejectedCase{TraceID: sample.TraceID, Reason: "duplicate of an existing draft case"})
			continue
		}
		accepted = append(accepted, newDraftCase(generated, sample))
	}
	return accepted, rejected
}

func newDraftCase(generated domain.GeneratedCase, sample domain.CaseSample) domain.EvalCase {
	return domain.EvalCase{
		ID:             uuid.Must(uuid.NewV7()).String(),
		Name:           generated.Name,
		Input:          generated.Input,
		ExpectedOutput: generated.ExpectedOutput,
		AssertionMode:  generated.AssertionMode,
		Enabled:        true,
		SourceTraceID:  sample.TraceID,
		FeedbackRef:    sample.FeedbackRef,
		GenerateReason: generated.GenerateReason,
	}
}

// caseDeduper keeps generated cases from duplicating hand-authored or
// previously generated draft cases by comparing serialized input and
// expected output.
type caseDeduper struct {
	keys map[string]struct{}
}

func newCaseDeduper(existing []domain.EvalCase) *caseDeduper {
	keys := make(map[string]struct{}, len(existing))
	for i := range existing {
		keys[caseKey(existing[i].Input, existing[i].ExpectedOutput)] = struct{}{}
	}
	return &caseDeduper{keys: keys}
}

func (d *caseDeduper) duplicate(c domain.GeneratedCase) bool {
	_, ok := d.keys[caseKey(c.Input, c.ExpectedOutput)]
	return ok
}

func caseKey(input, expected any) string {
	in, _ := json.Marshal(input)
	exp, _ := json.Marshal(expected)
	return string(in) + "\x00" + string(exp)
}
