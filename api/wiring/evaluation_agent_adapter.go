package wiring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	parametersdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/google/uuid"
)

type agentRevisionService interface {
	Get(context.Context, string, evaldomain.ResourceRef) (evaldomain.ResourceRevision, []byte, bool, error)
	Create(context.Context, string, evalport.CreateRevisionInput) (evaldomain.ResourceRevision, bool, error)
	Publish(context.Context, string, evaldomain.ResourceRef) (evaldomain.ResourceRevision, error)
}

type agentRevisionExecutor interface {
	SnapshotRevision(context.Context, string, string) (agentdomain.AgentRevision, error)
	ExecuteRevision(context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta) (*agentapp.AgentResult, int, error)
	ExecuteSkillScenarioRevision(context.Context, agentdomain.AgentRevision, agentapp.ExecRequest, agentapp.ExecMeta, []agentport.SkillActivation) (*agentapp.AgentResult, int, error)
}

// agentReadWriter provides read-write access to the Agent table for
// applying published optimization revisions back to production agents.
type agentReadWriter interface {
	Get(context.Context, string) (agentapp.AgentDTO, error)
	Update(context.Context, string, agentapp.UpdateAgentInput) (agentapp.AgentDTO, error)
}

type agentEvaluationAdapter struct {
	revisions      agentRevisionService
	agents         agentRevisionExecutor
	agentUpdater   agentReadWriter
	modelValidator agentport.TenantChatModelValidator
	// parameters is the registry source of truth: candidate patches are
	// rejected unless their keys are registered (legacy hard-coded whitelists
	// removed; key registrability lives in the registry alone).
	parameters *parametersdomain.ParametersRegistry
	actorID    string
}

// validateCandidateModel fails closed: a candidate model that cannot be
// resolved for the tenant must never be created or applied.
func (a agentEvaluationAdapter) validateCandidateModel(ctx context.Context, tenantID, model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("evaluation Agent adapter: model required")
	}
	if a.modelValidator == nil {
		return errors.New("evaluation Agent adapter: model validator unavailable")
	}
	if err := a.modelValidator.ValidateTenantChatModel(ctx, tenantID, model); err != nil {
		return fmt.Errorf("evaluation Agent adapter: validate candidate model %q: %w", model, err)
	}
	return nil
}

func (a agentEvaluationAdapter) CreatePublishedBaseline(
	ctx context.Context, tenantID, agentID string,
) (evaldomain.ResourceRef, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(agentID) == "" {
		return evaldomain.ResourceRef{}, errors.New("evaluation Agent adapter: tenant and agent IDs required")
	}
	if a.agents == nil {
		return evaldomain.ResourceRef{}, errors.New("evaluation Agent adapter: Agent service unavailable")
	}
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: "evaluation-worker", Role: postgres.RoleTenantAdmin,
	})
	snapshot, err := a.agents.SnapshotRevision(ctx, tenantID, agentID)
	if err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Agent adapter: snapshot baseline: %w", err)
	}
	contentHash, err := snapshot.ContentHash()
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	actorID := strings.TrimSpace(a.actorID)
	if actorID == "" {
		actorID = "evaluation-worker"
	}
	created, _, err := a.revisions.Create(ctx, tenantID, evalport.CreateRevisionInput{
		ResourceKind: evaldomain.ResourceKindAgent, ResourceID: agentID,
		CreatedBy: actorID, IdempotencyKey: "agent-baseline-" + contentHash,
		Source: evaldomain.RevisionSourceManual, Payload: snapshot, SafeSummary: snapshot.SafeSummary(),
	})
	if err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Agent adapter: create baseline: %w", err)
	}
	ref := evaldomain.ResourceRef{Kind: evaldomain.ResourceKindAgent, ResourceID: agentID, RevisionID: created.ID}
	if _, err := a.revisions.Publish(ctx, tenantID, ref); err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Agent adapter: publish baseline: %w", err)
	}
	return ref, nil
}

// ApplyPublishedRevision loads an already-published agent revision and writes
// the optimized SystemPrompt, Model, MaxIterations, and MaxContextTokens back to
// the agents table — closing the evaluation → production loop.
//
// Callers must ensure the revision is already published (e.g., by
// promoteCandidateTx).  This method is idempotent: repeated calls with the same
// revision produce the same Agent state.
func (a agentEvaluationAdapter) ApplyPublishedRevision(
	ctx context.Context, tenantID, agentID, revisionID string,
) error {
	// The optimization pipeline is a system actor: ownership checks are
	// bypassed, but the change is still audited with actor_type=system,
	// source=optimization.
	const evaluationWorker = "evaluation-worker"
	ctx = reqctx.WithSystemActor(ctx, evaluationWorker)
	if a.agentUpdater == nil {
		return errors.New("evaluation Agent adapter: agent updater unavailable")
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(revisionID) == "" {
		return errors.New("evaluation Agent adapter: tenant, agent and revision IDs required")
	}
	ref := evaldomain.ResourceRef{
		Kind: evaldomain.ResourceKindAgent, ResourceID: agentID, RevisionID: revisionID,
	}
	_, snapshot, found, err := a.get(ctx, tenantID, ref)
	if err != nil {
		return fmt.Errorf("evaluation Agent adapter: load revision: %w", err)
	}
	if !found {
		return fmt.Errorf("evaluation Agent adapter: revision %s not found", revisionID)
	}
	if err := a.validateCandidateModel(ctx, tenantID, snapshot.Model); err != nil {
		return err
	}
	existing, err := a.agentUpdater.Get(ctx, agentID)
	if err != nil {
		return fmt.Errorf("evaluation Agent adapter: get agent: %w", err)
	}
	// Preserve every field except those the optimization pipeline is authorized to change.
	_, err = a.agentUpdater.Update(ctx, agentID, agentapp.UpdateAgentInput{
		ActorID:          evaluationWorker,
		Name:             existing.Name,
		Type:             existing.Type,
		Description:      existing.Description,
		SystemPrompt:     snapshot.SystemPrompt,
		LLMModel:         snapshot.Model,
		MaxIterations:    snapshot.MaxIterations,
		MaxContextTokens: snapshot.ModelParameters.MaxContextTokens,
		Temperature:      snapshot.ModelParameters.Temperature,
		ReasoningEffort:  snapshot.ModelParameters.ReasoningEffort,
		MaxTokens:        snapshot.ModelParameters.MaxTokens,
		// promote 写回 = 整体替换:零值采样参数必须以 JSONB null 清除旧值,
		// 与表单路径的 merge 语义(零值不落库)区分。
		ReplaceParameters:     true,
		AllowedSkills:         existing.AllowedSkills,
		MCPToolIDs:            existing.MCPToolIDs,
		KnowledgeWorkspaceIDs: existing.KnowledgeWorkspaceIDs,
		MemoryScope:           existing.MemoryScope,
	})
	if err != nil {
		return fmt.Errorf("evaluation Agent adapter: apply to agent: %w", err)
	}
	return nil
}

func (a agentEvaluationAdapter) LoadOptimizableSnapshot(
	ctx context.Context, tenantID string, baseline evaldomain.ResourceRef,
) (map[string]any, error) {
	_, snapshot, err := a.loadPublished(ctx, tenantID, baseline)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("evaluation Agent adapter: encode snapshot: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("evaluation Agent adapter: map snapshot: %w", err)
	}
	return result, nil
}

func (a agentEvaluationAdapter) CreateCandidate(
	ctx context.Context, tenantID string, baseline evaldomain.ResourceRef, patch evaldomain.CandidatePatch,
) (evaldomain.ResourceRef, error) {
	if err := validatePatchKeys(a.parameters, patch); err != nil {
		return evaldomain.ResourceRef{}, err
	}
	parent, snapshot, err := a.loadPublished(ctx, tenantID, baseline)
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	candidatePatch, err := parseAgentCandidatePatch(snapshot, patch)
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	if candidatePatch.Model != "" {
		if err := a.validateCandidateModel(ctx, tenantID, candidatePatch.Model); err != nil {
			return evaldomain.ResourceRef{}, err
		}
	}
	candidate, err := snapshot.ApplyCandidate(candidatePatch)
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	contentHash, err := candidate.ContentHash()
	if err != nil {
		return evaldomain.ResourceRef{}, err
	}
	actorID := strings.TrimSpace(a.actorID)
	if actorID == "" {
		actorID = "evaluation-worker"
	}
	stored, _, err := a.revisions.Create(ctx, tenantID, evalport.CreateRevisionInput{
		ResourceKind: evaldomain.ResourceKindAgent, ResourceID: baseline.ResourceID,
		ParentRevisionID: parent.ID, CreatedBy: actorID,
		IdempotencyKey: agentCandidateIdempotencyKey(tenantID, baseline, contentHash),
		Source:         evaldomain.RevisionSourceOptimization, Payload: candidate,
		SafeSummary: agentCandidateSafeSummary(snapshot, candidate),
	})
	if err != nil {
		return evaldomain.ResourceRef{}, fmt.Errorf("evaluation Agent adapter: create candidate: %w", err)
	}
	return evaldomain.ResourceRef{Kind: evaldomain.ResourceKindAgent, ResourceID: baseline.ResourceID, RevisionID: stored.ID}, nil
}

func (a agentEvaluationAdapter) ResolveRevision(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, error) {
	revision, _, found, err := a.get(ctx, tenantID, ref)
	if err != nil {
		return evaldomain.ResourceRevision{}, err
	}
	if !found {
		return evaldomain.ResourceRevision{}, evalport.ErrCenterResourceNotFound
	}
	if !revision.CanEvaluateOffline() {
		return evaldomain.ResourceRevision{}, evaldomain.ErrRevisionNotPublished
	}
	return revision, nil
}

func (a agentEvaluationAdapter) SafeSummary(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (map[string]any, error) {
	revision, err := a.ResolveRevision(ctx, tenantID, ref)
	if err != nil {
		return nil, err
	}
	return revision.SafeSummary, nil
}

func (a agentEvaluationAdapter) ExecuteRevision(
	ctx context.Context, tenantID, requestedBy string, ref evaldomain.ResourceRef, testCase evaldomain.EvalCase,
) (evalport.ExecutionResult, error) {
	if a.agents == nil {
		return evalport.ExecutionResult{}, errors.New("evaluation Agent adapter: executor unavailable")
	}
	ctx, err := evaluationAgentContext(ctx, tenantID, requestedBy)
	if err != nil {
		return evalport.ExecutionResult{}, err
	}
	revision, snapshot, found, err := a.get(ctx, tenantID, ref)
	if err != nil {
		return evalport.ExecutionResult{}, err
	}
	if !found {
		return evalport.ExecutionResult{}, evalport.ErrCenterResourceNotFound
	}
	if !revision.CanEvaluateOffline() {
		return evalport.ExecutionResult{}, evaldomain.ErrRevisionNotPublished
	}
	query, err := evaluationCaseQuery(testCase.Input)
	if err != nil {
		return evalport.ExecutionResult{}, err
	}
	traceID := uuid.Must(uuid.NewV7()).String()
	meta := agentapp.ExecMeta{
		TenantID: tenantID, TraceID: traceID,
		EvolutionTrace: agentapp.EvolutionTraceMetadata{Evaluation: true,
			ResourceManifest: map[string]string{"agent:" + ref.ResourceID: ref.RevisionID}},
	}
	// 评测执行注入执行快照（D6）：ctx 中评测上下文快照存在时投影为 agent 消费侧
	// ExecutionSnapshot 并固定 canary pin，供 assembleOptions / 窗口解析读取固化值；
	// 快照缺失（非评测链路）保持现状。
	ctx, meta = a.injectExecutionSnapshot(ctx, meta)
	result, duration, err := a.agents.ExecuteRevision(ctx, snapshot,
		agentapp.ExecRequest{Query: query, UserID: requestedBy}, meta)
	if err != nil {
		return evalport.ExecutionResult{}, fmt.Errorf("evaluation Agent adapter: execute revision: %w", err)
	}
	if result == nil {
		return evalport.ExecutionResult{}, errors.New("evaluation Agent adapter: provider returned no result")
	}
	return evalport.ExecutionResult{Output: result.Output, TraceID: traceID, Tokens: result.TokensUsed,
		CostUSD: result.CostUSD, DurationMs: duration, Tools: mapToolObservations(result.ToolObservations)}, nil
}

// injectExecutionSnapshot 把 ctx 中的评测上下文快照投影为 agent 消费侧
// ExecutionSnapshot 注入 ctx，并设置 canary pin meta（D4）。快照缺失（非评测
// 链路）时保持 ctx/meta 不变。
func (a agentEvaluationAdapter) injectExecutionSnapshot(
	ctx context.Context, meta agentapp.ExecMeta,
) (context.Context, agentapp.ExecMeta) {
	snap := evaldomain.EvalSnapshotFromCtx(ctx)
	if snap == nil {
		return ctx, meta
	}
	es := a.projectExecutionSnapshot(snap)
	ctx = agentport.WithExecutionSnapshot(ctx, es)
	// canary 隔离：评测执行固定 MCP/Knowledge 版本 pin，不实时分流（D4）。
	meta.KnowledgeAssignmentsPinned = true
	meta.PinnedKnowledgeRevisions = es.PinnedKnowledge
	meta.PinnedMCPRevisions = es.PinnedMCP
	return ctx, meta
}

// projectExecutionSnapshot 把 evaldomain 快照投影为 agent 消费侧 ExecutionSnapshot
// （薄 ACL，D6）：trace 组 → TraceParameters；ResolvedExecution → 固化窗口/保留；
// PinnedAssignments → MCP/Knowledge/Skill pin。
func (a agentEvaluationAdapter) projectExecutionSnapshot(snap *evaldomain.EvaluationContextSnapshot) *agentport.ExecutionSnapshot {
	es := &agentport.ExecutionSnapshot{
		ContextWindowTokens: snap.ResolvedExecution.ContextWindow,
		OutputReserveTokens: snap.ResolvedExecution.OutputReserve,
		PinnedMCP:           make(map[string]agentport.MCPRevisionPin, len(snap.PinnedAssignments.MCPRevisions)),
		PinnedKnowledge:     make(map[string]agentport.KnowledgeRevisionPin, len(snap.PinnedAssignments.KnowledgeRevisions)),
	}
	for serverID, revID := range snap.PinnedAssignments.MCPRevisions {
		es.PinnedMCP[serverID] = agentport.MCPRevisionPin{RevisionID: revID}
	}
	for name, revID := range snap.PinnedAssignments.KnowledgeRevisions {
		es.PinnedKnowledge[name] = agentport.KnowledgeRevisionPin{RevisionID: revID}
	}
	if len(snap.PinnedAssignments.SkillRevisions) > 0 {
		es.PinnedSkills = make(map[string]string, len(snap.PinnedAssignments.SkillRevisions))
		maps.Copy(es.PinnedSkills, snap.PinnedAssignments.SkillRevisions)
	}
	for _, g := range snap.Execution {
		if g.GroupKey == evaldomain.GroupTrace {
			es.TraceParameters = g.Values
		}
	}
	return es
}

func (a agentEvaluationAdapter) loadPublished(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, agentdomain.AgentRevision, error) {
	revision, snapshot, found, err := a.get(ctx, tenantID, ref)
	if err != nil {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, err
	}
	if !found {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, evalport.ErrCenterResourceNotFound
	}
	if revision.Status != evaldomain.RevisionStatusPublished {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, evaldomain.ErrRevisionNotPublished
	}
	return revision, snapshot, nil
}

func (a agentEvaluationAdapter) get(
	ctx context.Context, tenantID string, ref evaldomain.ResourceRef,
) (evaldomain.ResourceRevision, agentdomain.AgentRevision, bool, error) {
	if strings.TrimSpace(tenantID) == "" {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, false,
			errors.New("evaluation Agent adapter: tenant ID required")
	}
	if ref.Kind != evaldomain.ResourceKindAgent {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, false,
			fmt.Errorf("evaluation Agent adapter: unsupported resource kind %q", ref.Kind)
	}
	if err := ref.Validate(); err != nil {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, false, err
	}
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: "evaluation-worker", Role: postgres.RoleTenantAdmin,
	})
	revision, payload, found, err := a.revisions.Get(ctx, tenantID, ref)
	if err != nil || !found {
		return revision, agentdomain.AgentRevision{}, found, err
	}
	var snapshot agentdomain.AgentRevision
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, false,
			fmt.Errorf("evaluation Agent adapter: decode revision: %w", err)
	}
	if snapshot.AgentID != ref.ResourceID {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, false,
			evalport.ErrCenterResourceNotFound
	}
	if err := snapshot.Validate(); err != nil {
		return evaldomain.ResourceRevision{}, agentdomain.AgentRevision{}, false, err
	}
	return revision, snapshot, true, nil
}

func parseAgentCandidatePatch(
	baseline agentdomain.AgentRevision, patch evaldomain.CandidatePatch,
) (agentdomain.AgentCandidatePatch, error) {
	result := agentdomain.AgentCandidatePatch{}
	for key, value := range patch.PromptPatch {
		prompt, ok := value.(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return result, fmt.Errorf("evaluation Agent adapter: prompt field %s must be non-empty", key)
		}
		switch key {
		case "instructions", "system_prompt":
			result.SystemPrompt = prompt
		case "memory_extraction_prompt",
			"memory_summary_prompt",
			"memory_enrichment_prompt":
			if result.PromptOverrides == nil {
				result.PromptOverrides = make(map[string]string)
			}
			result.PromptOverrides[key] = prompt
		default:
			return result, fmt.Errorf("evaluation Agent adapter: prompt field is not optimizable: %s", key)
		}
	}
	params := baseline.ModelParameters
	parametersChanged := false
	for key, value := range patch.ParameterPatch {
		switch key {
		case "model", "max_context_tokens", "temperature", "max_tokens",
			"reasoning_effort":
			model, changed, err := parseModelParameterPatch(key, value, &params)
			if err != nil {
				return result, err
			}
			if model != "" {
				result.Model = model
			}
			parametersChanged = parametersChanged || changed
		case "max_iterations":
			parsed, ok := integer(value)
			if !ok {
				return result, errors.New("evaluation Agent adapter: max_iterations must be an integer")
			}
			result.MaxIterations = parsed
		case "bindings":
			bindings, err := bindingPatch(value)
			if err != nil {
				return result, err
			}
			result.Bindings = bindings
		default:
			return result, fmt.Errorf("evaluation Agent adapter: parameter field is not optimizable: %s", key)
		}
	}
	if parametersChanged {
		result.ModelParameters = &params
	}
	return result, nil
}

// parseModelParameterPatch applies one model-config parameter patch key
// (model, max_context_tokens, temperature, max_tokens, reasoning_effort) into
// params. It returns the patched model name (empty when unchanged) and whether
// any parameter value was modified. Kept separate so the candidate patch
// parser stays within the code-quality complexity budget.
func parseModelParameterPatch(key string, value any, params *agentdomain.ModelParameters) (string, bool, error) {
	switch key {
	case "reasoning_effort":
		return applyReasoningEffortPatch(value, params)
	case "model":
		return applyModelNamePatch(value)
	default:
		return applyModelNumericPatch(key, value, params)
	}
}

// applyReasoningEffortPatch validates a reasoning_effort patch value. Invalid
// tiers fail closed: written revisions propagate through promote to strict
// endpoints where an invalid value 400s (a permanent error aborting the
// fallback chain), so the tier must be rejected here.
func applyReasoningEffortPatch(value any, params *agentdomain.ModelParameters) (string, bool, error) {
	effort, ok := value.(string)
	if !ok {
		return "", false, errors.New("evaluation Agent adapter: reasoning_effort must be a string")
	}
	if !constants.IsValidReasoningEffort(effort) {
		return "", false, errors.New("evaluation Agent adapter: reasoning_effort must be one of low/medium/high")
	}
	params.ReasoningEffort = effort
	return "", true, nil
}

// applyModelNamePatch validates a model patch value and returns the new name.
func applyModelNamePatch(value any) (string, bool, error) {
	model, _ := value.(string)
	if strings.TrimSpace(model) == "" {
		return "", false, errors.New("evaluation Agent adapter: model must be non-empty")
	}
	return model, false, nil
}

// applyModelNumericPatch runs the shared parse → range-validate → assign
// sequence for the numeric model-config patch keys.
func applyModelNumericPatch(key string, value any, params *agentdomain.ModelParameters) (string, bool, error) {
	switch key {
	case "max_context_tokens":
		changed, err := applyNumericPatch(key, value, parseInteger, func(v any) error {
			params.MaxContextTokens = v.(int)
			return nil
		}, "max_context_tokens")
		return "", changed, err
	case "temperature":
		changed, err := applyNumericPatch(key, value, parseFloat, func(v any) error {
			params.Temperature = float32(v.(float64))
			return nil
		}, "temperature")
		return "", changed, err
	case "max_tokens":
		changed, err := applyNumericPatch(key, value, parseInteger, func(v any) error {
			params.MaxTokens = v.(int)
			return nil
		}, "max_tokens")
		return "", changed, err
	}
	return "", false, nil
}

// applyNumericPatch runs the shared parse → range-validate → assign sequence
// for every numeric model-config patch key, keeping the per-key cases one line
// each and the failure mode identical across keys (fail closed before apply).
func applyNumericPatch(key string, value any, parse func(any) (any, bool), apply func(any) error, what string) (bool, error) {
	parsed, ok := parse(value)
	if !ok {
		return false, fmt.Errorf("evaluation Agent adapter: %s must be a number", what)
	}
	if err := validateParameterRange(key, parsed); err != nil {
		return false, err
	}
	return true, apply(parsed)
}

func parseInteger(value any) (any, bool) {
	i, ok := integer(value)
	return i, ok
}

func parseFloat(value any) (any, bool) {
	f, ok := floatValue(value)
	return f, ok
}

// validateParameterRange fails closed at parse time for every model-config
// patch key: out-of-range candidates are rejected before they reach the
// revision pipeline, matching the domain-side validateModelParameters bounds.
// 0 keeps its "unset / auto" semantics everywhere it is meaningful.
func validateParameterRange(key string, parsed any) error {
	switch key {
	case "max_context_tokens":
		return validateIntRange(key, parsed.(int),
			constants.TunableMaxContextTokensMin, constants.TunableMaxContextTokensMax)
	case "temperature":
		return validateFloatRange(key, parsed.(float64),
			constants.TunableTemperatureMin, constants.TunableTemperatureMax)
	case "max_tokens":
		return validateIntRange(key, parsed.(int),
			constants.TunableMaxTokensMin, constants.TunableMaxTokensMax)
	}
	return nil
}

func validateIntRange(key string, v, min, max int) error {
	if v < min || v > max {
		return fmt.Errorf("evaluation Agent adapter: %s must be in [%d, %d]", key, min, max)
	}
	return nil
}

func validateFloatRange(key string, v, min, max float64) error {
	if v < min || v > max {
		return fmt.Errorf("evaluation Agent adapter: %s must be in [%v, %v]", key, min, max)
	}
	return nil
}

func bindingPatch(value any) ([]agentdomain.AgentBinding, error) {
	states, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("evaluation Agent adapter: bindings must be an object")
	}
	bindings := make([]agentdomain.AgentBinding, 0, len(states))
	for key, rawEnabled := range states {
		kindText, id, ok := strings.Cut(key, ":")
		enabled, enabledOK := rawEnabled.(bool)
		if !ok || strings.TrimSpace(id) == "" || !enabledOK {
			return nil, fmt.Errorf("evaluation Agent adapter: invalid binding patch %q", key)
		}
		bindings = append(bindings, agentdomain.AgentBinding{Kind: agentdomain.AgentBindingKind(kindText), ID: id, Enabled: enabled})
	}
	return bindings, nil
}

func integer(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		converted := int(typed)
		return converted, float64(converted) == typed
	default:
		return 0, false
	}
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}

func agentCandidateIdempotencyKey(tenantID string, baseline evaldomain.ResourceRef, contentHash string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{tenantID, string(baseline.Kind), baseline.ResourceID,
		baseline.RevisionID, contentHash}, "\x00")))
	return "agent-candidate-" + hex.EncodeToString(sum[:])
}

func agentCandidateSafeSummary(baseline, candidate agentdomain.AgentRevision) map[string]any {
	changed := make([]string, 0, 4)
	types := make([]string, 0, 4)
	appendChange := func(field, changeType string, condition bool) {
		if condition {
			changed = append(changed, field)
			types = append(types, changeType)
		}
	}
	appendChange("system_prompt", "modified", baseline.SystemPrompt != candidate.SystemPrompt)
	appendChange("model", "modified", baseline.Model != candidate.Model)
	appendChange("model_parameters", "modified", baseline.ModelParameters != candidate.ModelParameters)
	appendChange("max_iterations", "modified", baseline.MaxIterations != candidate.MaxIterations)
	appendChange("bindings", "modified", !bindingsEqual(baseline.Bindings, candidate.Bindings))
	return map[string]any{
		"resource_name":  candidate.AgentID,
		"version_label":  "candidate",
		"changed_fields": changed,
		"change_types":   types,
	}
}

func evaluationAgentContext(ctx context.Context, tenantID, requestedBy string) (context.Context, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("evaluation Agent adapter: tenant ID required")
	}
	if strings.TrimSpace(requestedBy) == "" {
		return nil, errors.New("evaluation Agent adapter: requesting user ID required")
	}
	return postgres.WithTenant(ctx, &postgres.TenantContext{
		TenantID: tenantID, UserID: requestedBy, Role: postgres.RoleTenantAdmin,
	}), nil
}

func bindingsEqual(left, right []agentdomain.AgentBinding) bool {
	leftRevision := agentdomain.AgentRevision{AgentID: "compare", Type: agentdomain.ReActAgent,
		SystemPrompt: "compare", Model: "compare", MaxIterations: 1, Bindings: left}
	rightRevision := leftRevision
	rightRevision.Bindings = right
	leftHash, leftErr := leftRevision.ContentHash()
	rightHash, rightErr := rightRevision.ContentHash()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

func evaluationCaseQuery(input any) (string, error) {
	if text, ok := input.(string); ok {
		return text, nil
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("evaluation Agent adapter: encode input: %w", err)
	}
	return string(payload), nil
}

var _ evalport.ResourceAdapter = agentEvaluationAdapter{}
var _ evalport.CandidateCreator = agentEvaluationAdapter{}
var _ evalport.AgentRevisionProvider = agentEvaluationAdapter{}
