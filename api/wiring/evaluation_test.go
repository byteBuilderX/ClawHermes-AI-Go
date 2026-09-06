package wiring

import (
	"context"
	"errors"
	"strings"
	"testing"

	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

func TestParsePromptRewritePatchesAcceptsFencedJSON(t *testing.T) {
	patches, err := parsePromptRewritePatches("```json\n" +
		`[{"prompt_patch":{"instructions":"更准确地分类输入"},"rationale":"修复漏分类"}]` + "\n```")
	if err != nil {
		t.Fatalf("parsePromptRewritePatches returned error: %v", err)
	}
	if len(patches) != 1 || patches[0].PromptPatch["instructions"] == "" {
		t.Fatalf("unexpected patches: %#v", patches)
	}
}

func TestParsePromptRewritePatchesRejectsProtectedFields(t *testing.T) {
	_, err := parsePromptRewritePatches(`[{"prompt_patch":{"permissions":{"network":true}}}]`)
	if err == nil {
		t.Fatal("expected protected prompt patch to be rejected")
	}
}

func TestParseJudgeResponseConfidence(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    float64
	}{
		{"explicit confidence", `{"passed":true,"reason":"ok","confidence":0.72}`, 0.72},
		{"null confidence falls back to 1.0", `{"passed":true,"reason":"ok","confidence":null}`, 1.0},
		{"missing confidence falls back to 1.0", `{"passed":false,"reason":"bad"}`, 1.0},
		{"out-of-range confidence falls back to 1.0", `{"passed":true,"reason":"ok","confidence":1.8}`, 1.0},
		{"negative confidence falls back to 1.0", `{"passed":true,"reason":"ok","confidence":-0.3}`, 1.0},
		{"code fence tolerated", "```json\n{\"passed\":true,\"reason\":\"ok\",\"confidence\":0.4}\n```", 0.4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseJudgeResponse(tc.content)
			if err != nil {
				t.Fatalf("parseJudgeResponse: %v", err)
			}
			if got.Confidence != tc.want {
				t.Fatalf("confidence = %v, want %v", got.Confidence, tc.want)
			}
		})
	}
}

func TestParseJudgeResponseDimensions(t *testing.T) {
	content := `{"passed":false,"reason":"事实错误","confidence":0.6,
		"dimensions":[
			{"name":"faithfulness","score":0.4,"passed":false,"reason":"与实际不符","confidence":0.7},
			{"name":"relevance","score":0.9,"passed":true,"reason":"","confidence":0.9},
			{"name":"completeness","score":0.8,"passed":true}
		]}`
	got, err := parseJudgeResponse(content)
	if err != nil {
		t.Fatalf("parseJudgeResponse: %v", err)
	}
	if got.Passed || got.Message != "事实错误" {
		t.Fatalf("verdict mismatch: %+v", got)
	}
	if got.Confidence != 0.6 {
		t.Fatalf("confidence = %v, want 0.6", got.Confidence)
	}
	if len(got.Dimensions) != 3 {
		t.Fatalf("dimensions = %d, want 3", len(got.Dimensions))
	}
	faith := got.Dimensions[0]
	if faith.Name != "faithfulness" || faith.Score != 0.4 || faith.Passed || faith.Confidence != 0.7 {
		t.Fatalf("faithfulness mismatch: %+v", faith)
	}
	if got.Dimensions[2].Confidence != 1.0 { // 缺失回退 1.0
		t.Fatalf("completeness confidence = %v, want 1.0", got.Dimensions[2].Confidence)
	}
}

func TestParseJudgeResponseInvalidDimensionsDropped(t *testing.T) {
	// 说明：name 空、score 越界（2.5 / -0.1）的维度应被丢弃，仅保留合法维度。
	content := `{"passed":true,"reason":"通过","dimensions":[
		{"name":"","score":0.5,"passed":true},
		{"name":"relevance","score":2.5,"passed":true},
		{"name":"completeness","score":-0.1,"passed":true},
		{"name":"faithfulness","score":0.7,"passed":true}
	]}`
	got, err := parseJudgeResponse(content)
	if err != nil {
		t.Fatalf("parseJudgeResponse: %v", err)
	}
	if len(got.Dimensions) != 1 || got.Dimensions[0].Name != "faithfulness" {
		t.Fatalf("dimensions = %+v, want only faithfulness", got.Dimensions)
	}
}

func TestParseJudgeResponseNoDimensionsTolerated(t *testing.T) {
	content := `{"passed":false,"reason":"不及格","confidence":0.3}`
	got, err := parseJudgeResponse(content)
	if err != nil {
		t.Fatalf("parseJudgeResponse: %v", err)
	}
	if got.Passed || len(got.Dimensions) != 0 {
		t.Fatalf("old-style verdict must stay single: %+v", got)
	}
}

// ——— evaluationResourceRouter session dispatch (stage B §5.4) ———

// routerSessionRunnerStub 同时实现 ResourceAdapter + SessionRunner，记录分派透传。
type routerSessionRunnerStub struct {
	evidence   []evaldomain.SessionTurnEvidence
	runErr     error
	runTenant  string
	lastScript evaldomain.EvalSessionScript
}

func (s *routerSessionRunnerStub) ExecuteRevision(context.Context, string, string, evaldomain.ResourceRef, evaldomain.EvalCase) (evalport.ExecutionResult, error) {
	return evalport.ExecutionResult{}, errors.New("router stub: single-turn path not used by session test")
}

func (s *routerSessionRunnerStub) ResolveRevision(context.Context, string, evaldomain.ResourceRef) (evaldomain.ResourceRevision, error) {
	return evaldomain.ResourceRevision{}, nil
}

func (s *routerSessionRunnerStub) SafeSummary(context.Context, string, evaldomain.ResourceRef) (map[string]any, error) {
	return map[string]any{}, nil
}

func (s *routerSessionRunnerStub) RunSession(_ context.Context, tenantID, _ string, _ evaldomain.ResourceRef, script evaldomain.EvalSessionScript) ([]evaldomain.SessionTurnEvidence, error) {
	s.runTenant = tenantID
	s.lastScript = script
	return s.evidence, s.runErr
}

// routerSingleTurnStub 仅实现单轮 ResourceAdapter，用于验证会话分派对非
// SessionRunner adapter 的 fail-close。
type routerSingleTurnStub struct{}

func (routerSingleTurnStub) ExecuteRevision(context.Context, string, string, evaldomain.ResourceRef, evaldomain.EvalCase) (evalport.ExecutionResult, error) {
	return evalport.ExecutionResult{}, nil
}

func (routerSingleTurnStub) ResolveRevision(context.Context, string, evaldomain.ResourceRef) (evaldomain.ResourceRevision, error) {
	return evaldomain.ResourceRevision{}, nil
}

func (routerSingleTurnStub) SafeSummary(context.Context, string, evaldomain.ResourceRef) (map[string]any, error) {
	return map[string]any{}, nil
}

func TestEvaluationRouterRunSessionDispatchesToSessionRunner(t *testing.T) {
	script := evaldomain.EvalSessionScript{Goal: "解答用户", Turns: []evaldomain.SessionTurn{{User: "开场"}}}
	evidence := []evaldomain.SessionTurnEvidence{{Index: 0, User: "开场", Output: "out-0", TraceID: "trace-0"}}
	session := &routerSessionRunnerStub{evidence: evidence}
	router := evaluationResourceRouter{adapters: map[evaldomain.ResourceKind]evalport.ResourceAdapter{
		evaldomain.ResourceKindAgent: session,
	}}

	got, err := router.RunSession(context.Background(), "tenant-1", "user-1",
		evaldomain.ResourceRef{Kind: evaldomain.ResourceKindAgent, ResourceID: "agent-1", RevisionID: "revision-1"}, script)
	if err != nil {
		t.Fatalf("RunSession returned error: %v", err)
	}
	if len(got) != 1 || got[0].Output != "out-0" {
		t.Fatalf("unexpected evidence: %+v", got)
	}
	if session.runTenant != "tenant-1" || len(session.lastScript.Turns) != 1 {
		t.Fatalf("stub not driven with tenant/script: tenant=%q turns=%d",
			session.runTenant, len(session.lastScript.Turns))
	}
}

func TestEvaluationRouterRunSessionFailsClosedOnNonSessionRunner(t *testing.T) {
	router := evaluationResourceRouter{adapters: map[evaldomain.ResourceKind]evalport.ResourceAdapter{
		evaldomain.ResourceKindSkill: routerSingleTurnStub{},
	}}

	_, err := router.RunSession(context.Background(), "tenant-1", "user-1",
		evaldomain.ResourceRef{Kind: evaldomain.ResourceKindSkill, ResourceID: "skill-1", RevisionID: "revision-1"},
		evaldomain.EvalSessionScript{Goal: "g", Turns: []evaldomain.SessionTurn{{User: "开场"}}})
	if err == nil || !strings.Contains(err.Error(), "session evaluation not supported") {
		t.Fatalf("expected fail-close on non-SessionRunner adapter, got err=%v", err)
	}
}
