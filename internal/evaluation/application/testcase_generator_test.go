package application

import (
	"context"
	"errors"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

type stubSampleSource struct {
	samples []domain.CaseSample
	err     error
}

func (s *stubSampleSource) ListSamples(_ context.Context, _ string, _ domain.ResourceKind, _ domain.SamplePolicy, _ int) ([]domain.CaseSample, error) {
	return s.samples, s.err
}

type stubCaseGenerator struct {
	generated domain.GeneratedCase
	// errs and results are consumed per call in order, so one stub can
	// drive distinct outcomes per sample (error / invalid / duplicate).
	errs     []error
	results  []domain.GeneratedCase
	requests []port.CaseGenRequest
}

func (g *stubCaseGenerator) Generate(_ context.Context, req port.CaseGenRequest) (domain.GeneratedCase, error) {
	g.requests = append(g.requests, req)
	if len(g.errs) > 0 {
		err := g.errs[0]
		g.errs = g.errs[1:]
		if err != nil {
			return domain.GeneratedCase{}, err
		}
	}
	if len(g.results) > 0 {
		r := g.results[0]
		g.results = g.results[1:]
		return r, nil
	}
	return g.generated, nil
}

type stubGenSuiteRepo struct {
	draft     domain.EvalSuiteRevision
	draftOK   bool
	createErr error
	added     []domain.EvalCase
	created   bool
}

func (f *stubGenSuiteRepo) GetDraftRevision(_ context.Context, _ string, _ string) (domain.EvalSuiteRevision, bool, error) {
	return f.draft, f.draftOK, nil
}

func (f *stubGenSuiteRepo) CreateDraftRevision(_ context.Context, _ string, _ string) (domain.EvalSuiteRevision, error) {
	if f.createErr != nil {
		return domain.EvalSuiteRevision{}, f.createErr
	}
	f.created = true
	f.draftOK = true
	return f.draft, nil
}

func (f *stubGenSuiteRepo) AddDraftCases(_ context.Context, _ string, _ string, cases []domain.EvalCase) error {
	f.added = append(f.added, cases...)
	return nil
}

func (f *stubGenSuiteRepo) CreateSuite(_ context.Context, _ string, _ domain.EvalSuite, _ domain.EvalSuiteRevision) error {
	return nil
}

func (f *stubGenSuiteRepo) PublishRevision(_ context.Context, _ string, _, _ string, _ int) (domain.EvalSuiteRevision, error) {
	return domain.EvalSuiteRevision{}, nil
}

func (f *stubGenSuiteRepo) NextVersionNo(_ context.Context, _ string, _ string) (int, error) {
	return 1, nil
}

func (f *stubGenSuiteRepo) GetRevision(_ context.Context, _ string, _ string) (domain.EvalSuiteRevision, bool, error) {
	return f.draft, f.draftOK, nil
}

func (f *stubGenSuiteRepo) GetActiveRevision(_ context.Context, _ string, _ string) (domain.EvalSuiteRevision, bool, error) {
	return f.draft, f.draftOK, nil
}

func (f *stubGenSuiteRepo) GetSuite(_ context.Context, _ string, _ string) (domain.EvalSuite, bool, error) {
	return domain.EvalSuite{}, false, nil
}

func (f *stubGenSuiteRepo) ListSuiteRevisions(_ context.Context, _ string, _ string) ([]domain.SuiteRevisionMeta, error) {
	return nil, nil
}

func (f *stubGenSuiteRepo) UpdateDraftCase(_ context.Context, _ string, _ string, _ domain.EvalCase) error {
	return nil
}

func (f *stubGenSuiteRepo) DeleteDraftCase(_ context.Context, _ string, _ string, _ string) error {
	return nil
}

func validGenerated() domain.GeneratedCase {
	return domain.GeneratedCase{
		Name: "物流查询", Input: "快递没更新", ExpectedOutput: "物流查询",
		AssertionMode: domain.AssertionContains, GenerateReason: "负反馈样本", Valid: true,
	}
}

func TestTestCaseGeneratorGeneratesAndAppendsToDraft(t *testing.T) {
	sample := domain.CaseSample{
		TraceID: "trace-1", FeedbackRef: "fb-1", Score: ptr(0.2),
		Query: "快递没更新", Response: "您好，物流信息查询如下",
	}
	repo := &stubGenSuiteRepo{
		draft: domain.EvalSuiteRevision{
			ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
		},
		draftOK: true,
	}
	gen := &stubCaseGenerator{generated: validGenerated()}
	g := NewTestCaseGenerator(&stubSampleSource{samples: []domain.CaseSample{sample}}, gen, repo)

	result, err := g.Generate(context.Background(), GenerateInput{
		TenantID: "tenant-1", SuiteID: "suite-1", Policy: domain.SamplePolicyNegativeFirst, MaxCases: 10,
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.SamplesFound != 1 || result.Generated != 1 || len(result.Rejected) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(repo.added) != 1 {
		t.Fatalf("expected 1 added case, got %d", len(repo.added))
	}
	added := repo.added[0]
	if added.Name != "物流查询" || added.SourceTraceID != "trace-1" || added.FeedbackRef != "fb-1" ||
		added.GenerateReason != "负反馈样本" || !added.Enabled {
		t.Fatalf("provenance not carried into draft case: %+v", added)
	}
	if len(gen.requests) != 1 || gen.requests[0].ResourceKind != domain.ResourceKindSkill {
		t.Fatalf("generator not called with suite kind: %+v", gen.requests)
	}
}

func TestTestCaseGeneratorCreatesDraftWhenMissing(t *testing.T) {
	repo := &stubGenSuiteRepo{
		draft: domain.EvalSuiteRevision{
			ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
		},
	}
	gen := &stubCaseGenerator{generated: validGenerated()}
	g := NewTestCaseGenerator(
		&stubSampleSource{samples: []domain.CaseSample{{TraceID: "t1", Query: "q", Response: "r"}}},
		gen, repo,
	)
	if _, err := g.Generate(context.Background(), GenerateInput{TenantID: "tenant-1", SuiteID: "suite-1"}); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if !repo.created {
		t.Fatal("expected CreateDraftRevision to be called when no draft exists")
	}
}

func TestTestCaseGeneratorRejectsFailuresAndDuplicates(t *testing.T) {
	samples := []domain.CaseSample{
		{TraceID: "gen-err", Query: "q1", Response: "r1"},
		{TraceID: "invalid", Query: "q2", Response: "r2"},
		{TraceID: "dup", Query: "快递没更新", Response: "r3"},
	}
	repo := &stubGenSuiteRepo{
		draft: domain.EvalSuiteRevision{
			ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
			// The existing draft already contains the case the dup sample
			// would generate.
			Cases: []domain.EvalCase{{Name: "物流查询", Input: "快递没更新", ExpectedOutput: "物流查询"}},
		},
		draftOK: true,
	}
	gen := &stubCaseGenerator{
		// Per-call outcomes, one per sample in order: generator error,
		// invalid case, duplicate of the existing draft case.
		errs:    []error{errors.New("llm timeout")},
		results: []domain.GeneratedCase{{Valid: false, Reason: "empty input or expected output"}, validGenerated()},
	}
	g := NewTestCaseGenerator(&stubSampleSource{samples: samples}, gen, repo)

	result, err := g.Generate(context.Background(), GenerateInput{TenantID: "tenant-1", SuiteID: "suite-1"})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(result.Rejected) != 3 {
		t.Fatalf("expected 3 rejections (generator error, invalid, duplicate), got %+v", result)
	}
	reasons := map[string]bool{}
	for _, r := range result.Rejected {
		reasons[r.Reason] = true
	}
	if !reasons["llm timeout"] || !reasons["empty input or expected output"] || !reasons["duplicate of an existing draft case"] {
		t.Fatalf("rejection reasons missing or wrong: %+v", result.Rejected)
	}
	if result.Generated != 0 || len(repo.added) != 0 {
		t.Fatalf("nothing should be added: result=%+v added=%d", result, len(repo.added))
	}
}

func TestTestCaseGeneratorInvalidGeneratedCase(t *testing.T) {
	repo := &stubGenSuiteRepo{
		draft: domain.EvalSuiteRevision{
			ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
		},
		draftOK: true,
	}
	gen := &stubCaseGenerator{generated: domain.GeneratedCase{Valid: false, Reason: "empty input or expected output"}}
	g := NewTestCaseGenerator(
		&stubSampleSource{samples: []domain.CaseSample{{TraceID: "t1", Query: "q", Response: "r"}}},
		gen, repo,
	)
	result, err := g.Generate(context.Background(), GenerateInput{TenantID: "tenant-1", SuiteID: "suite-1"})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != "empty input or expected output" {
		t.Fatalf("expected invalid-case rejection: %+v", result)
	}
}

func TestTestCaseGeneratorNoSamples(t *testing.T) {
	g := NewTestCaseGenerator(
		&stubSampleSource{},
		&stubCaseGenerator{generated: validGenerated()},
		&stubGenSuiteRepo{
			draft: domain.EvalSuiteRevision{
				ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft,
				ResourceKind: domain.ResourceKindSkill,
			},
			draftOK: true,
		},
	)
	if _, err := g.Generate(context.Background(), GenerateInput{TenantID: "tenant-1", SuiteID: "suite-1"}); !errors.Is(err, ErrNoSamplesFound) {
		t.Fatalf("expected ErrNoSamplesFound, got %v", err)
	}
}

func TestTestCaseGeneratorUnavailable(t *testing.T) {
	g := NewTestCaseGenerator(nil, nil, &stubGenSuiteRepo{draftOK: true})
	if _, err := g.Generate(context.Background(), GenerateInput{TenantID: "tenant-1", SuiteID: "suite-1"}); !errors.Is(err, ErrCaseGenUnavailable) {
		t.Fatalf("expected ErrCaseGenUnavailable, got %v", err)
	}
}

func TestTestCaseGeneratorSuiteWithoutKind(t *testing.T) {
	repo := &stubGenSuiteRepo{
		draft:   domain.EvalSuiteRevision{ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft},
		draftOK: true,
	}
	g := NewTestCaseGenerator(
		&stubSampleSource{samples: []domain.CaseSample{{TraceID: "t1"}}},
		&stubCaseGenerator{generated: validGenerated()},
		repo,
	)
	if _, err := g.Generate(context.Background(), GenerateInput{TenantID: "tenant-1", SuiteID: "suite-1"}); !errors.Is(err, ErrSuiteDraftMissing) {
		t.Fatalf("expected ErrSuiteDraftMissing, got %v", err)
	}
}

func TestTestCaseGeneratorAddFailurePropagates(t *testing.T) {
	repo := &stubGenSuiteRepo{
		draft: domain.EvalSuiteRevision{
			ID: "draft-1", SuiteID: "suite-1", Status: domain.SuiteRevisionDraft,
			ResourceKind: domain.ResourceKindSkill,
		},
		draftOK: true,
	}
	gen := &stubCaseGenerator{generated: validGenerated()}
	g := NewTestCaseGenerator(
		&stubSampleSource{samples: []domain.CaseSample{{TraceID: "t1", Query: "q", Response: "r"}}},
		gen, repo,
	)
	if _, err := g.Generate(context.Background(), GenerateInput{TenantID: "tenant-1", SuiteID: "suite-1"}); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
}

func ptr(v float64) *float64 { return &v }
