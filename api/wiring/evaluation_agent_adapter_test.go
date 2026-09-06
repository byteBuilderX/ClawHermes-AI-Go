package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	parametersdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
)

func TestAgentEvaluationAdapterRequiresPublishedTenantRevision(t *testing.T) {
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "rev-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusDraft,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true}
	adapter := agentEvaluationAdapter{revisions: revisions, parameters: parametersdomain.NewParametersRegistry()}
	_, err := adapter.LoadOptimizableSnapshot(context.Background(), "tenant-1", agentRef("rev-1"))
	if err == nil {
		t.Fatal("expected draft baseline rejection")
	}
	if revisions.tenantID != "tenant-1" {
		t.Fatalf("tenant not propagated: %q", revisions.tenantID)
	}
}

func TestAgentEvaluationAdapterCandidateIsIdempotentAndBounded(t *testing.T) {
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5,"bindings":[{"kind":"skill","id":"skill-1","enabled":true}]}`), found: true}
	adapter := agentEvaluationAdapter{revisions: revisions, actorID: "evaluation-worker", parameters: parametersdomain.NewParametersRegistry()}
	patch := evaldomain.CandidatePatch{Source: "llm_rewrite", PromptPatch: map[string]any{"instructions": "candidate"}, ParameterPatch: map[string]any{
		"bindings": map[string]any{"skill:skill-1": false},
	}}
	first, err := adapter.CreateCandidate(context.Background(), "tenant-1", agentRef("published-1"), patch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.CreateCandidate(context.Background(), "tenant-1", agentRef("published-1"), patch)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || revisions.createCalls != 2 || !strings.HasPrefix(revisions.input.IdempotencyKey, "agent-candidate-") {
		t.Fatalf("candidate replay mismatch: first=%#v second=%#v calls=%d", first, second, revisions.createCalls)
	}

	patch.ParameterPatch["bindings"] = map[string]any{"skill:skill-2": true}
	if _, err := adapter.CreateCandidate(context.Background(), "tenant-1", agentRef("published-1"), patch); err == nil {
		t.Fatal("expected unauthorized binding rejection")
	}
}

func TestAgentEvaluationAdapterPropagatesRevisionPersistenceFailure(t *testing.T) {
	wantErr := errors.New("object persistence failed")
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true, createErr: wantErr}
	adapter := agentEvaluationAdapter{revisions: revisions, actorID: "evaluation-worker", parameters: parametersdomain.NewParametersRegistry()}
	_, err := adapter.CreateCandidate(context.Background(), "tenant-1", agentRef("published-1"), evaldomain.CandidatePatch{
		Source: "llm_rewrite", PromptPatch: map[string]any{"instructions": "candidate"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected persistence failure, got %v", err)
	}
}

func TestAgentEvaluationAdapterTreatsProviderFailureAsExecutionFailure(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true}
	adapter := agentEvaluationAdapter{revisions: revisions, agents: fakeAgentRevisionExecutor{err: wantErr}, parameters: parametersdomain.NewParametersRegistry()}
	result, err := adapter.ExecuteRevision(
		context.Background(), "tenant-1", "user-1", agentRef("published-1"), evaldomain.EvalCase{Input: "hello"},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected provider failure, got result=%#v err=%v", result, err)
	}
}

func TestAgentEvaluationAdapterCrossTenantRevisionIsNotFound(t *testing.T) {
	adapter := agentEvaluationAdapter{revisions: &fakeAgentRevisionService{found: false}, parameters: parametersdomain.NewParametersRegistry()}
	_, err := adapter.ResolveRevision(context.Background(), "other-tenant", agentRef("published-1"))
	if !errors.Is(err, evalport.ErrCenterResourceNotFound) {
		t.Fatalf("expected tenant-safe not found, got %v", err)
	}
}

func TestAgentEvaluationAdapterRejectsDraftExecution(t *testing.T) {
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "draft-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusDraft,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true}
	adapter := agentEvaluationAdapter{revisions: revisions, agents: fakeAgentRevisionExecutor{}, parameters: parametersdomain.NewParametersRegistry()}
	_, err := adapter.ExecuteRevision(
		context.Background(), "tenant-1", "user-1", agentRef("draft-1"), evaldomain.EvalCase{Input: "hello"},
	)
	if !errors.Is(err, evaldomain.ErrRevisionNotPublished) {
		t.Fatalf("expected not-published error, got %v", err)
	}
}

func TestAgentEvaluationAdapterResolvesOptimizationCandidateForOfflineEvaluation(t *testing.T) {
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "candidate-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusDraft, Source: evaldomain.RevisionSourceOptimization,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"candidate","model":"qwen-plus","max_iterations":5}`), found: true}

	resolved, err := (agentEvaluationAdapter{revisions: revisions}).ResolveRevision(
		context.Background(), "tenant-1", agentRef("candidate-1"),
	)
	if err != nil || !resolved.CanEvaluateOffline() {
		t.Fatalf("candidate resolution=%+v err=%v", resolved, err)
	}
}

func TestAgentEvaluationAdapterCreatesPublishedBaselineFromLiveAgent(t *testing.T) {
	revisions := &fakeAgentRevisionService{}
	agents := fakeAgentRevisionExecutor{snapshot: agentdomain.AgentRevision{
		AgentID: "agent-1", Type: agentdomain.ReActAgent, SystemPrompt: "baseline", Model: "qwen-plus",
		MaxIterations: 5,
	}}
	adapter := agentEvaluationAdapter{revisions: revisions, agents: agents, actorID: "evaluation-worker", parameters: parametersdomain.NewParametersRegistry()}
	ref, err := adapter.CreatePublishedBaseline(context.Background(), "tenant-1", "agent-1")
	if err != nil || ref.RevisionID != "candidate-1" || revisions.publishCalls != 1 {
		t.Fatalf("unexpected baseline: ref=%+v publishCalls=%d err=%v", ref, revisions.publishCalls, err)
	}
}

func TestAgentEvaluationAdapterDoesNotPublishFailedBaselinePersistence(t *testing.T) {
	wantErr := errors.New("object persistence failed")
	revisions := &fakeAgentRevisionService{createErr: wantErr}
	agents := fakeAgentRevisionExecutor{snapshot: agentdomain.AgentRevision{
		AgentID: "agent-1", Type: agentdomain.ReActAgent, SystemPrompt: "baseline", Model: "qwen-plus",
		MaxIterations: 5,
	}}
	adapter := agentEvaluationAdapter{revisions: revisions, agents: agents, parameters: parametersdomain.NewParametersRegistry()}
	_, err := adapter.CreatePublishedBaseline(context.Background(), "tenant-1", "agent-1")
	if !errors.Is(err, wantErr) || revisions.publishCalls != 0 {
		t.Fatalf("failed persistence must abort publish: calls=%d err=%v", revisions.publishCalls, err)
	}
}

func TestAgentEvaluationAdapterParsesModelParameters(t *testing.T) {
	baseline := agentdomain.AgentRevision{AgentID: "agent-1", Type: agentdomain.ReActAgent,
		SystemPrompt: "baseline", Model: "qwen-plus", MaxIterations: 5}
	t.Run("accepts temperature and max_tokens", func(t *testing.T) {
		parsed, err := parseAgentCandidatePatch(baseline, evaldomain.CandidatePatch{
			ParameterPatch: map[string]any{"temperature": 0.9, "max_tokens": 2048},
		})
		if err != nil {
			t.Fatalf("expected supported parameters to be accepted: %v", err)
		}
		if parsed.ModelParameters == nil ||
			parsed.ModelParameters.Temperature != 0.9 || parsed.ModelParameters.MaxTokens != 2048 {
			t.Fatalf("parameters not written back: %#v", parsed.ModelParameters)
		}
	})
	t.Run("rejects unknown parameter fields", func(t *testing.T) {
		for _, field := range []string{"maxTokens", "top_p"} {
			_, err := parseAgentCandidatePatch(baseline, evaldomain.CandidatePatch{
				ParameterPatch: map[string]any{field: 1},
			})
			if err == nil {
				t.Fatalf("expected unsupported %s to be rejected", field)
			}
		}
	})
	t.Run("rejects non-numeric temperature and max_tokens", func(t *testing.T) {
		for _, field := range []string{"temperature", "max_tokens"} {
			_, err := parseAgentCandidatePatch(baseline, evaldomain.CandidatePatch{
				ParameterPatch: map[string]any{field: "hot"},
			})
			if err == nil {
				t.Fatalf("expected non-numeric %s to be rejected", field)
			}
		}
	})
	t.Run("accepts max_context_tokens", func(t *testing.T) {
		parsed, err := parseAgentCandidatePatch(baseline, evaldomain.CandidatePatch{
			ParameterPatch: map[string]any{"max_context_tokens": 16384},
		})
		if err != nil {
			t.Fatalf("expected max_context_tokens to be accepted: %v", err)
		}
		if parsed.ModelParameters == nil || parsed.ModelParameters.MaxContextTokens != 16384 {
			t.Fatalf("max_context_tokens not written back: %#v", parsed.ModelParameters)
		}
	})
	t.Run("rejects invalid model values", func(t *testing.T) {
		for _, patch := range []map[string]any{
			{"max_context_tokens": -1},    // negative window
			{"max_context_tokens": 40000}, // above 32768
		} {
			_, err := parseAgentCandidatePatch(baseline, evaldomain.CandidatePatch{
				ParameterPatch: patch,
			})
			if err == nil {
				t.Fatalf("expected patch %v to be rejected", patch)
			}
		}
	})
	t.Run("accepts valid reasoning_effort and writes it back", func(t *testing.T) {
		parsed, err := parseAgentCandidatePatch(baseline, evaldomain.CandidatePatch{
			ParameterPatch: map[string]any{"reasoning_effort": "high"},
		})
		if err != nil {
			t.Fatalf("expected valid reasoning_effort to be accepted: %v", err)
		}
		if parsed.ModelParameters == nil || parsed.ModelParameters.ReasoningEffort != "high" {
			t.Fatalf("reasoning_effort not written back: %#v", parsed.ModelParameters)
		}
	})
	t.Run("rejects invalid reasoning_effort and non-string values", func(t *testing.T) {
		for _, patch := range []map[string]any{
			{"reasoning_effort": "deep"}, // outside low/medium/high
			{"reasoning_effort": 1},      // non-string must fail closed
		} {
			_, err := parseAgentCandidatePatch(baseline, evaldomain.CandidatePatch{
				ParameterPatch: patch,
			})
			if err == nil {
				t.Fatalf("expected patch %v to be rejected", patch)
			}
		}
	})
}

func TestAgentEvaluationAdapterSummariesPassRealRevisionValidation(t *testing.T) {
	store := &validatingRevisionStore{}
	repo := &validatingRevisionRepo{}
	revisions := evalapp.NewRevisionService(store, repo)
	agents := fakeAgentRevisionExecutor{snapshot: agentdomain.AgentRevision{
		AgentID: "agent-1", Type: agentdomain.ReActAgent, SystemPrompt: "baseline", Model: "qwen-plus",
		MaxIterations: 5,
	}}
	adapter := agentEvaluationAdapter{revisions: revisions, agents: agents, parameters: parametersdomain.NewParametersRegistry()}
	baseline, err := adapter.CreatePublishedBaseline(context.Background(), "tenant-1", "agent-1")
	if err != nil {
		t.Fatalf("baseline rejected by real RevisionService: %v", err)
	}
	if _, err := adapter.CreateCandidate(context.Background(), "tenant-1", baseline, evaldomain.CandidatePatch{
		PromptPatch: map[string]any{"instructions": "candidate"},
	}); err != nil {
		t.Fatalf("candidate rejected by real RevisionService: %v", err)
	}
}

func TestAgentEvaluationAdapterApplyPublishedRevisionFailsClosedOnModelValidation(t *testing.T) {
	payload := []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5,"model_parameters":{"temperature":0.9,"max_tokens":2048}}`)
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: payload, found: true}

	t.Run("missing validator blocks apply", func(t *testing.T) {
		updater := &recordingAgentUpdater{}
		adapter := agentEvaluationAdapter{revisions: revisions, agentUpdater: updater, parameters: parametersdomain.NewParametersRegistry()}
		if err := adapter.ApplyPublishedRevision(context.Background(), "tenant-1", "agent-1", "published-1"); err == nil {
			t.Fatal("expected missing validator to fail closed")
		}
		if updater.updateCalls != 0 {
			t.Fatalf("apply proceeded without validator: updateCalls=%d", updater.updateCalls)
		}
	})

	t.Run("validator dependency failure fails closed", func(t *testing.T) {
		wantErr := errors.New("model registry unavailable")
		updater := &recordingAgentUpdater{}
		adapter := agentEvaluationAdapter{
			revisions: revisions, agentUpdater: updater,
			modelValidator: fakeModelValidator{err: wantErr},
			parameters:     parametersdomain.NewParametersRegistry(),
		}
		err := adapter.ApplyPublishedRevision(context.Background(), "tenant-1", "agent-1", "published-1")
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected validator failure, got %v", err)
		}
		if updater.updateCalls != 0 {
			t.Fatalf("apply proceeded despite validator failure: updateCalls=%d", updater.updateCalls)
		}
	})

	t.Run("valid model applies with routed parameters", func(t *testing.T) {
		updater := &recordingAgentUpdater{}
		adapter := agentEvaluationAdapter{
			revisions: revisions, agentUpdater: updater,
			modelValidator: fakeModelValidator{},
			parameters:     parametersdomain.NewParametersRegistry(),
		}
		if err := adapter.ApplyPublishedRevision(context.Background(), "tenant-1", "agent-1", "published-1"); err != nil {
			t.Fatal(err)
		}
		if updater.updateCalls != 1 {
			t.Fatalf("expected exactly one update, got %d", updater.updateCalls)
		}
		if updater.input.Temperature != 0.9 || updater.input.MaxTokens != 2048 {
			t.Fatalf("routed parameters not applied: %#v", updater.input)
		}
	})
}

func TestAgentEvaluationAdapterCreateCandidateValidatesPatchedModel(t *testing.T) {
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true}
	adapter := agentEvaluationAdapter{
		revisions: revisions, actorID: "evaluation-worker",
		modelValidator: fakeModelValidator{err: errors.New("model not in tenant catalog")},
		parameters:     parametersdomain.NewParametersRegistry(),
	}
	_, err := adapter.CreateCandidate(context.Background(), "tenant-1", agentRef("published-1"), evaldomain.CandidatePatch{
		Source: "llm_rewrite", ParameterPatch: map[string]any{"model": "other-model"},
	})
	if err == nil {
		t.Fatal("expected patched model to be validated and rejected")
	}
}

type recordingAgentUpdater struct {
	updateCalls int
	input       agentapp.UpdateAgentInput
}

func (u *recordingAgentUpdater) Get(context.Context, string) (agentapp.AgentDTO, error) {
	return agentapp.AgentDTO{}, nil
}

func (u *recordingAgentUpdater) Update(_ context.Context, _ string, input agentapp.UpdateAgentInput) (agentapp.AgentDTO, error) {
	u.updateCalls++
	u.input = input
	return agentapp.AgentDTO{}, nil
}

type fakeModelValidator struct{ err error }

func (f fakeModelValidator) ValidateTenantChatModel(context.Context, string, string) error {
	return f.err
}

func TestAgentEvaluationAdapterExecutionReceivesTenantContext(t *testing.T) {
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true}
	executor := &tenantCaptureAgentExecutor{}
	adapter := agentEvaluationAdapter{revisions: revisions, agents: executor, parameters: parametersdomain.NewParametersRegistry()}
	_, _ = adapter.ExecuteRevision(
		context.Background(), "tenant-1", "user-1", agentRef("published-1"), evaldomain.EvalCase{Input: "hello"},
	)
	if executor.tenantID != "tenant-1" || executor.userID != "user-1" {
		t.Fatalf("execution tenant context = tenant %q user %q", executor.tenantID, executor.userID)
	}
}

type tenantCaptureAgentExecutor struct{ tenantID, userID string }

func (e *tenantCaptureAgentExecutor) SnapshotRevision(context.Context, string, string) (agentdomain.AgentRevision, error) {
	return agentdomain.AgentRevision{}, nil
}

func (e *tenantCaptureAgentExecutor) ExecuteRevision(
	ctx context.Context, _ agentdomain.AgentRevision, _ agentapp.ExecRequest, _ agentapp.ExecMeta,
) (*agentapp.AgentResult, int, error) {
	tenant, _ := postgres.FromContext(ctx)
	if tenant != nil {
		e.tenantID = tenant.TenantID
		e.userID = tenant.UserID
	}
	return &agentapp.AgentResult{Output: "ok"}, 1, nil
}

func (e *tenantCaptureAgentExecutor) ExecuteSkillScenarioRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation,
) (*agentapp.AgentResult, int, error) {
	return &agentapp.AgentResult{Output: "ok"}, 1, nil
}

type validatingRevisionStore struct{ payloads map[string][]byte }

func (s *validatingRevisionStore) Put(_ context.Context, payload evalport.RevisionPayload) (evalport.RevisionPayloadRef, error) {
	encoded, _ := json.Marshal(payload.Value)
	if s.payloads == nil {
		s.payloads = map[string][]byte{}
	}
	s.payloads[payload.ID] = encoded
	return evalport.RevisionPayloadRef{URI: "object://" + payload.ID, SHA256: "hash"}, nil
}
func (s *validatingRevisionStore) Get(_ context.Context, ref evalport.RevisionPayloadRef) ([]byte, error) {
	return s.payloads[strings.TrimPrefix(ref.URI, "object://")], nil
}
func (*validatingRevisionStore) Delete(context.Context, evalport.RevisionPayloadRef) error {
	return nil
}

type validatingRevisionRepo struct {
	revisions map[string]evaldomain.ResourceRevision
}

func (r *validatingRevisionRepo) Create(_ context.Context, _ string, revision evaldomain.ResourceRevision, _ string) (evaldomain.ResourceRevision, bool, error) {
	if r.revisions == nil {
		r.revisions = map[string]evaldomain.ResourceRevision{}
	}
	r.revisions[revision.ID] = revision
	return revision, true, nil
}
func (r *validatingRevisionRepo) Get(_ context.Context, _ string, ref evaldomain.ResourceRef) (evaldomain.ResourceRevision, bool, error) {
	revision, ok := r.revisions[ref.RevisionID]
	return revision, ok, nil
}
func (r *validatingRevisionRepo) Publish(_ context.Context, _ string, ref evaldomain.ResourceRef) (evaldomain.ResourceRevision, error) {
	revision, ok := r.revisions[ref.RevisionID]
	if !ok {
		return evaldomain.ResourceRevision{}, evalport.ErrCenterResourceNotFound
	}
	revision.Status = evaldomain.RevisionStatusPublished
	r.revisions[ref.RevisionID] = revision
	return revision, nil
}

type fakeAgentRevisionService struct {
	revision     evaldomain.ResourceRevision
	payload      []byte
	found        bool
	tenantID     string
	input        evalport.CreateRevisionInput
	createCalls  int
	createErr    error
	publishCalls int
}

func (f *fakeAgentRevisionService) Publish(
	_ context.Context, _ string, ref evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, error) {
	f.publishCalls++
	return evaldomain.ResourceRevision{ID: ref.RevisionID, ResourceKind: ref.Kind,
		ResourceID: ref.ResourceID, Status: evaldomain.RevisionStatusPublished}, nil
}

type fakeAgentRevisionExecutor struct {
	err      error
	snapshot agentdomain.AgentRevision
}

func (f fakeAgentRevisionExecutor) SnapshotRevision(
	context.Context, string, string,
) (agentdomain.AgentRevision, error) {
	return f.snapshot, f.err
}

func (f fakeAgentRevisionExecutor) ExecuteRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta,
) (*agentapp.AgentResult, int, error) {
	return nil, 0, f.err
}

func (f fakeAgentRevisionExecutor) ExecuteSkillScenarioRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation,
) (*agentapp.AgentResult, int, error) {
	return nil, 0, f.err
}

func (f *fakeAgentRevisionService) Get(_ context.Context, tenantID string, _ evaldomain.ResourceRef) (evaldomain.ResourceRevision, []byte, bool, error) {
	f.tenantID = tenantID
	return f.revision, f.payload, f.found, nil
}

func (f *fakeAgentRevisionService) Create(_ context.Context, tenantID string, input evalport.CreateRevisionInput) (evaldomain.ResourceRevision, bool, error) {
	f.tenantID, f.input = tenantID, input
	f.createCalls++
	if f.createErr != nil {
		return evaldomain.ResourceRevision{}, false, f.createErr
	}
	return evaldomain.ResourceRevision{ID: "candidate-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1"}, f.createCalls == 1, nil
}

func agentRef(revisionID string) evaldomain.ResourceRef {
	return evaldomain.ResourceRef{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: revisionID}
}

func TestAgentEvaluationAdapterPropagatesToolObservations(t *testing.T) {
	const snapshotPayload = `{"agent_id":"agent-1","type":"react","system_prompt":"baseline",
		"model":"qwen-plus","max_iterations":5}`
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(snapshotPayload), found: true}
	want := []agentdomain.ToolObservation{
		{
			ToolName: "search_web", ToolType: "builtin", StepIndex: 1,
			ProviderType: "provider-a", CapabilityID: "cap-search",
			Arguments: map[string]any{"query": "multi-step reasoning"}, RawText: "web results for multi-step reasoning",
		},
		{
			ToolName: "read_memory", ToolType: "memory", StepIndex: 2,
			ProviderType: "provider-b", CapabilityID: "cap-memory",
			Arguments: map[string]any{"scope": "recent", "limit": 5}, RawText: "memory excerpt",
		},
	}
	adapter := agentEvaluationAdapter{
		revisions: revisions,
		agents: &toolObservationsAgentExecutor{result: &agentapp.AgentResult{
			Output: "done", ToolObservations: want,
		}},
		parameters: parametersdomain.NewParametersRegistry(),
	}
	result, err := adapter.ExecuteRevision(
		context.Background(), "tenant-1", "user-1", agentRef("published-1"), evaldomain.EvalCase{Input: "hello"},
	)
	if err != nil {
		t.Fatalf("execute revision: %v", err)
	}
	if len(result.Tools) != len(want) {
		t.Fatalf("tool sequence length = %d, want %d", len(result.Tools), len(want))
	}
	for i, got := range result.Tools {
		expected := want[i]
		if got.ToolName != expected.ToolName || got.ToolType != expected.ToolType ||
			got.StepIndex != expected.StepIndex || got.ProviderType != expected.ProviderType ||
			got.CapabilityID != expected.CapabilityID || !reflect.DeepEqual(got.Arguments, expected.Arguments) ||
			got.RawText != expected.RawText {
			t.Fatalf("tool[%d] mismatch: got %#v, want %#v", i, got, expected)
		}
	}
}

type toolObservationsAgentExecutor struct{ result *agentapp.AgentResult }

func (e *toolObservationsAgentExecutor) SnapshotRevision(
	context.Context, string, string,
) (agentdomain.AgentRevision, error) {
	return agentdomain.AgentRevision{}, nil
}

func (e *toolObservationsAgentExecutor) ExecuteRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta,
) (*agentapp.AgentResult, int, error) {
	return e.result, 1, nil
}

func (e *toolObservationsAgentExecutor) ExecuteSkillScenarioRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation,
) (*agentapp.AgentResult, int, error) {
	return e.result, 1, nil
}

var _ = agentdomain.AgentRevision{}

func TestProjectExecutionSnapshot(t *testing.T) {
	adapter := agentEvaluationAdapter{}
	snap := &evaldomain.EvaluationContextSnapshot{
		ResolvedExecution: evaldomain.ResolvedExecution{ContextWindow: 32768, OutputReserve: 8192},
		PinnedAssignments: evaldomain.PinnedAssignments{
			MCPRevisions:       map[string]string{"mcp-server-1": "rev-mcp-1"},
			KnowledgeRevisions: map[string]string{"workspace-a": "rev-know-1"},
			SkillRevisions:     map[string]string{"skill-1": "rev-skill-1"},
		},
		Execution: []evaldomain.GroupSnapshot{
			{GroupKey: evaldomain.GroupTrace, Values: map[string]any{"trace.capture_parameters": true}},
			{GroupKey: evaldomain.GroupAgent, Values: map[string]any{"agent.temperature": 0.7}},
		},
	}
	es := adapter.projectExecutionSnapshot(snap)
	require.Equal(t, 32768, es.ContextWindowTokens)
	require.Equal(t, 8192, es.OutputReserveTokens)
	require.Equal(t, "rev-mcp-1", es.PinnedMCP["mcp-server-1"].RevisionID)
	require.Equal(t, "rev-know-1", es.PinnedKnowledge["workspace-a"].RevisionID)
	// skill pin 原样投影到 agent 消费侧。
	require.Equal(t, "rev-skill-1", es.PinnedSkills["skill-1"])
	// 仅 trace 组投影为 TraceParameters，其他组不进入。
	require.Equal(t, true, es.TraceParameters["trace.capture_parameters"])
	require.NotContains(t, es.TraceParameters, "agent.temperature")
}

func TestExecuteRevisionInjectsExecutionSnapshot(t *testing.T) {
	revisions := &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true}
	executor := &snapshotCaptureAgentExecutor{}
	adapter := agentEvaluationAdapter{revisions: revisions, agents: executor, parameters: parametersdomain.NewParametersRegistry()}
	snap := &evaldomain.EvaluationContextSnapshot{
		ResolvedExecution: evaldomain.ResolvedExecution{ContextWindow: 32768, OutputReserve: 8192},
		PinnedAssignments: evaldomain.PinnedAssignments{
			MCPRevisions:       map[string]string{"mcp-server-1": "rev-mcp-1"},
			KnowledgeRevisions: map[string]string{"workspace-a": "rev-know-1"},
			SkillRevisions:     map[string]string{"skill-1": "rev-skill-1"},
		},
		Execution: []evaldomain.GroupSnapshot{
			{GroupKey: evaldomain.GroupTrace, Values: map[string]any{"trace.capture_parameters": true}},
		},
	}
	ctx := evaldomain.WithEvalSnapshot(context.Background(), snap)
	_, err := adapter.ExecuteRevision(ctx, "tenant-1", "user-1", agentRef("published-1"), evaldomain.EvalCase{Input: "hello"})
	require.NoError(t, err)
	require.True(t, executor.hasSnap, "execution snapshot must be injected into ctx")
	require.Equal(t, 32768, executor.window)
	require.Equal(t, 8192, executor.reserve)
	require.Equal(t, "rev-mcp-1", executor.mcpPins["mcp-server-1"])
	require.Equal(t, "rev-skill-1", executor.skillPins["skill-1"])
	require.True(t, executor.meta.KnowledgeAssignmentsPinned)
	require.Equal(t, "rev-know-1", executor.meta.PinnedKnowledgeRevisions["workspace-a"].RevisionID)
	require.Equal(t, "rev-mcp-1", executor.meta.PinnedMCPRevisions["mcp-server-1"].RevisionID)
}

type snapshotCaptureAgentExecutor struct {
	meta      agentapp.ExecMeta
	hasSnap   bool
	window    int
	reserve   int
	mcpPins   map[string]string
	skillPins map[string]string
}

func (e *snapshotCaptureAgentExecutor) SnapshotRevision(
	context.Context, string, string,
) (agentdomain.AgentRevision, error) {
	return agentdomain.AgentRevision{}, nil
}

func (e *snapshotCaptureAgentExecutor) ExecuteRevision(
	ctx context.Context, _ agentdomain.AgentRevision, _ agentapp.ExecRequest, meta agentapp.ExecMeta,
) (*agentapp.AgentResult, int, error) {
	e.meta = meta
	if es := agentport.ExecutionSnapshotFromCtx(ctx); es != nil {
		e.hasSnap = true
		e.window = es.ContextWindowTokens
		e.reserve = es.OutputReserveTokens
		e.mcpPins = make(map[string]string, len(es.PinnedMCP))
		for serverID, pin := range es.PinnedMCP {
			e.mcpPins[serverID] = pin.RevisionID
		}
		e.skillPins = make(map[string]string, len(es.PinnedSkills))
		maps.Copy(e.skillPins, es.PinnedSkills)
	}
	return &agentapp.AgentResult{Output: "ok"}, 1, nil
}

func (e *snapshotCaptureAgentExecutor) ExecuteSkillScenarioRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation,
) (*agentapp.AgentResult, int, error) {
	return &agentapp.AgentResult{Output: "ok"}, 1, nil
}

// ——— agentEvaluationAdapter.RunSession（阶段 B §5.4 会话逐轮执行） ———

// agentSessionRevision 构造一条已发布、可离线评测的 agent revision（会话测试共用）。
func agentSessionRevision() *fakeAgentRevisionService {
	return &fakeAgentRevisionService{revision: evaldomain.ResourceRevision{
		ID: "published-1", ResourceKind: evaldomain.ResourceKindAgent, ResourceID: "agent-1",
		Status: evaldomain.RevisionStatusPublished,
	}, payload: []byte(`{"agent_id":"agent-1","type":"react","system_prompt":"baseline","model":"qwen-plus","max_iterations":5}`), found: true}
}

// sessionTurnRecordingExecutor 记录每次 ExecuteRevision 的 ExecRequest（断言会话续跑透传），
// 并按调用序返回可区分的 AgentResult；failAtN>0 时第 N 次调用返回 sentinel 错误。
type sessionTurnRecordingExecutor struct {
	requests  []agentapp.ExecRequest
	outputs   []string
	failAtN   int
	failErr   error
	execCalls int
}

func (e *sessionTurnRecordingExecutor) SnapshotRevision(context.Context, string, string) (agentdomain.AgentRevision, error) {
	return agentdomain.AgentRevision{}, nil
}

func (e *sessionTurnRecordingExecutor) ExecuteRevision(
	ctx context.Context, _ agentdomain.AgentRevision, req agentapp.ExecRequest, _ agentapp.ExecMeta,
) (*agentapp.AgentResult, int, error) {
	e.execCalls++
	e.requests = append(e.requests, req)
	if e.failAtN == e.execCalls {
		return nil, 0, e.failErr
	}
	n := e.execCalls
	output := "out-" + string(rune('0'+n))
	if n-1 < len(e.outputs) {
		output = e.outputs[n-1]
	}
	return &agentapp.AgentResult{Output: output, TokensUsed: n * 10, CostUSD: 0.01 * float64(n)}, n * 20, nil
}

func (e *sessionTurnRecordingExecutor) ExecuteSkillScenarioRevision(
	context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation,
) (*agentapp.AgentResult, int, error) {
	return nil, 0, nil
}

// recordingConversationOpener 记录 OpenEvalConversation 调用并返回固定 convID；err 非空时
// 首调即失败。
type recordingConversationOpener struct {
	convID  string
	err     error
	calls   int
	tenants []string
	agents  []string
	users   []string
}

func (o *recordingConversationOpener) OpenEvalConversation(_ context.Context, tenantID, agentID, userID string) (string, error) {
	o.calls++
	o.tenants = append(o.tenants, tenantID)
	o.agents = append(o.agents, agentID)
	o.users = append(o.users, userID)
	if o.err != nil {
		return "", o.err
	}
	return o.convID, nil
}

func TestAgentEvaluationAdapterRunSessionDrivesTurnsOnOneConversation(t *testing.T) {
	script := evaldomain.EvalSessionScript{Goal: "解答用户", Turns: []evaldomain.SessionTurn{
		{User: "开场问题"}, {User: "追问细节"},
	}}
	executor := &sessionTurnRecordingExecutor{outputs: []string{"开场答复", "追问答复"}}
	opener := &recordingConversationOpener{convID: "conv-eval-1"}
	adapter := agentEvaluationAdapter{
		revisions: agentSessionRevision(), agents: executor, conversations: opener,
	}

	evidences, err := adapter.RunSession(context.Background(), "tenant-1", "user-1", agentRef("published-1"), script)
	require.NoError(t, err)
	require.Len(t, evidences, 2)

	if evidences[0].Index != 0 || evidences[0].User != "开场问题" || evidences[0].Output != "开场答复" {
		t.Fatalf("turn-0 evidence mismatch: %+v", evidences[0])
	}
	if evidences[1].Index != 1 || evidences[1].User != "追问细节" || evidences[1].Output != "追问答复" {
		t.Fatalf("turn-1 evidence mismatch: %+v", evidences[1])
	}
	// 每轮独立 trace（逐轮证据各自可追溯）。
	if evidences[0].TraceID == "" || evidences[0].TraceID == evidences[1].TraceID {
		t.Fatalf("per-turn trace must be non-empty and unique: %+v / %+v",
			evidences[0].TraceID, evidences[1].TraceID)
	}
	// 聚合语义：tokens=n*10、duration=n*20。
	if evidences[0].Tokens != 10 || evidences[1].Tokens != 20 ||
		evidences[0].DurationMs != 20 || evidences[1].DurationMs != 40 {
		t.Fatalf("per-turn metrics mismatch: %+v / %+v", evidences[0], evidences[1])
	}

	// 恰开一条受控会话，且 requestedBy 作为 agent 执行人透传。
	if opener.calls != 1 || opener.tenants[0] != "tenant-1" ||
		opener.agents[0] != "agent-1" || opener.users[0] != "user-1" {
		t.Fatalf("conversation opener driven wrong: %+v", opener)
	}
	// 逐轮以同一会话续跑：ExecRequest.ConversationID 恒为受控会话、Query 依剧本顺序。
	require.Len(t, executor.requests, 2)
	for i, req := range executor.requests {
		if req.ConversationID != "conv-eval-1" || req.UserID != "user-1" {
			t.Fatalf("turn %d did not continue evaluation conversation: %+v", i, req)
		}
		if req.Query != script.Turns[i].User {
			t.Fatalf("turn %d query = %q, want %q", i, req.Query, script.Turns[i].User)
		}
	}
}

func TestAgentEvaluationAdapterRunSessionFailsClosedWithoutConversationOpener(t *testing.T) {
	executor := &sessionTurnRecordingExecutor{}
	adapter := agentEvaluationAdapter{revisions: agentSessionRevision(), agents: executor}

	_, err := adapter.RunSession(context.Background(), "tenant-1", "user-1",
		agentRef("published-1"), evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "x"}}})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "conversation opener unavailable") {
		t.Fatalf("expected conversation-opener fail-close, got %v", err)
	}
	if executor.execCalls != 0 {
		t.Fatalf("executor must not run without conversation opener: calls=%d", executor.execCalls)
	}
}

func TestAgentEvaluationAdapterRunSessionFailsClosedWithoutExecutor(t *testing.T) {
	opener := &recordingConversationOpener{convID: "conv-eval-1"}
	adapter := agentEvaluationAdapter{revisions: agentSessionRevision(), conversations: opener}

	_, err := adapter.RunSession(context.Background(), "tenant-1", "user-1",
		agentRef("published-1"), evaldomain.EvalSessionScript{Turns: []evaldomain.SessionTurn{{User: "x"}}})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "executor unavailable") {
		t.Fatalf("expected executor fail-close, got %v", err)
	}
	if opener.calls != 0 {
		t.Fatalf("conversation must not open without executor: calls=%d", opener.calls)
	}
}

func TestAgentEvaluationAdapterRunSessionKeepsPartialEvidenceOnMidTurnFailure(t *testing.T) {
	script := evaldomain.EvalSessionScript{Goal: "三轮问答", Turns: []evaldomain.SessionTurn{
		{User: "开场问题"}, {User: "追问细节"}, {User: "收尾确认"},
	}}
	wantFlake := errors.New("provider flaked at turn 2")
	executor := &sessionTurnRecordingExecutor{
		outputs: []string{"第一轮答复", "第二轮答复", "第三轮答复"},
		failAtN: 2, failErr: wantFlake,
	}
	opener := &recordingConversationOpener{convID: "conv-eval-1"}
	adapter := agentEvaluationAdapter{
		revisions: agentSessionRevision(), agents: executor, conversations: opener,
	}

	evidences, err := adapter.RunSession(context.Background(), "tenant-1", "user-1",
		agentRef("published-1"), script)
	// 第二轮失败：保留第一轮 partial evidence，且错误绝不吞没。
	require.ErrorIs(t, err, wantFlake)
	if !strings.Contains(err.Error(), "execute revision") {
		t.Fatalf("expected wrapped adapter error, got %v", err)
	}
	require.Len(t, evidences, 1)
	if evidences[0].Index != 0 || evidences[0].User != "开场问题" || evidences[0].Output != "第一轮答复" {
		t.Fatalf("partial evidence mismatch: %+v", evidences[0])
	}
	if opener.calls != 1 || executor.execCalls != 2 {
		t.Fatalf("expected one conversation and two executed turns, opener=%d exec=%d",
			opener.calls, executor.execCalls)
	}
}
