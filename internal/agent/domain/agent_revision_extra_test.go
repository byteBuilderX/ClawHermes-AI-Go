package domain

import (
	"errors"
	"testing"
)

func validRevision() AgentRevision {
	return AgentRevision{
		AgentID:       "agent-1",
		Type:          ReActAgent,
		SystemPrompt:  "You are helpful",
		Model:         "deepseek-v4-flash",
		MaxIterations: 10,
		Bindings: []AgentBinding{
			{Kind: AgentBindingSkill, ID: "skill-1", Enabled: true},
			{Kind: AgentBindingMCP, ID: "mcp-1", Enabled: false},
		},
	}
}

func TestAgentRevisionValidateWhitespaceFields(t *testing.T) {
	// 极端情况：TrimSpace 后为空的字段必须拒绝。
	base := validRevision()
	cases := []struct {
		name string
		mut  func(*AgentRevision)
	}{
		{"blank agent id", func(r *AgentRevision) { r.AgentID = "  " }},
		{"blank prompt", func(r *AgentRevision) { r.SystemPrompt = "\t" }},
		{"blank model", func(r *AgentRevision) { r.Model = "\n" }},
		{"max iterations zero", func(r *AgentRevision) { r.MaxIterations = 0 }},
		{"max iterations negative", func(r *AgentRevision) { r.MaxIterations = -1 }},
		{"max iterations too high", func(r *AgentRevision) { r.MaxIterations = 91 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mut(&r)
			if err := r.Validate(); err == nil {
				t.Errorf("%s must fail validation", tc.name)
			}
		})
	}
}

func TestAgentRevisionBlankPromptReturnsMatchableSentinel(t *testing.T) {
	// 建档（评测登记）路径对被测 agent 快照做领域校验，缺 system_prompt 的错误经
	// middleware 映射为 4xx 可读中文；故快照校验返回的错误必须是可 errors.Is 判定的
	// sentinel，且保留原文本以兼容既有日志检索与错误文案断言。
	r := validRevision()
	r.SystemPrompt = "   "
	err := r.Validate()
	if err == nil {
		t.Fatal("blank system prompt must fail validation")
	}
	if !errors.Is(err, ErrAgentSystemPromptRequired) {
		t.Fatalf("Validate() = %v, want errors.Is(err, ErrAgentSystemPromptRequired)", err)
	}
	if err.Error() != ErrAgentSystemPromptRequired.Error() {
		t.Fatalf("sentinel error text changed: %q", err.Error())
	}
}

func TestAgentRevisionValidateIterationBoundaries(t *testing.T) {
	// 极端情况：1 和 90 是合法边界。
	for _, n := range []int{1, 90} {
		r := validRevision()
		r.MaxIterations = n
		if err := r.Validate(); err != nil {
			t.Errorf("MaxIterations=%d must pass: %v", n, err)
		}
	}
}

func TestAgentRevisionValidateBindingEdgeCases(t *testing.T) {
	base := validRevision()
	cases := []struct {
		name string
		mut  func(*AgentRevision)
	}{
		{"unsupported binding kind", func(r *AgentRevision) { r.Bindings[0].Kind = "unknown" }},
		{"blank binding id", func(r *AgentRevision) { r.Bindings[0].ID = " " }},
		{"duplicate binding", func(r *AgentRevision) {
			r.Bindings = append(r.Bindings, AgentBinding{Kind: AgentBindingSkill, ID: "skill-1"})
		}},
		{"duplicate binding id with whitespace", func(r *AgentRevision) {
			r.Bindings = append(r.Bindings, AgentBinding{Kind: AgentBindingSkill, ID: " skill-1 "})
		}},
		{"empty bindings ok", func(r *AgentRevision) { r.Bindings = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mut(&r)
			err := r.Validate()
			if tc.name == "empty bindings ok" && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tc.name != "empty bindings ok" && err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestAgentRevisionValidateModelParametersBoundaries(t *testing.T) {
	base := validRevision()
	base.ModelParameters = ModelParameters{MaxContextTokens: maxAgentContextTokens}
	if err := base.Validate(); err != nil {
		t.Errorf("ceiling boundary must pass: %v", err)
	}
	base.ModelParameters = ModelParameters{MaxContextTokens: maxAgentContextTokens + 1}
	if err := base.Validate(); err == nil {
		t.Error("above ceiling must fail")
	}
}

func TestAgentRevisionApplyCandidateSuccess(t *testing.T) {
	// 成功路径：修改 prompt/model/迭代数 + 切换现有 binding 的 enabled。
	r := validRevision()
	candidate, err := r.ApplyCandidate(AgentCandidatePatch{
		SystemPrompt:  "New prompt",
		Model:         "qwen-max",
		MaxIterations: 20,
		Bindings: []AgentBinding{
			{Kind: AgentBindingSkill, ID: "skill-1", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("ApplyCandidate: %v", err)
	}
	if candidate.SystemPrompt != "New prompt" || candidate.Model != "qwen-max" || candidate.MaxIterations != 20 {
		t.Errorf("patch not applied: %+v", candidate)
	}
	if candidate.Bindings[0].Enabled {
		t.Error("binding enablement must flip to false")
	}
}

func TestAgentRevisionApplyCandidateUnauthorizedBinding(t *testing.T) {
	r := validRevision()
	_, err := r.ApplyCandidate(AgentCandidatePatch{Bindings: []AgentBinding{{Kind: AgentBindingKnowledge, ID: "new-kb", Enabled: true}}})
	if err == nil {
		t.Error("new binding must be rejected")
	}
}

func TestAgentRevisionApplyCandidateInvalidModelParameters(t *testing.T) {
	r := validRevision()
	_, err := r.ApplyCandidate(AgentCandidatePatch{ModelParameters: &ModelParameters{MaxContextTokens: -5}})
	if err == nil {
		t.Error("invalid model parameters must be rejected")
	}
}

func TestAgentRevisionContentHashInvalidRevision(t *testing.T) {
	// 极端情况：非法 revision 的 ContentHash 返回错误而非 hash。
	r := AgentRevision{}
	if _, err := r.ContentHash(); err == nil {
		t.Error("invalid revision must error on ContentHash")
	}
}

func TestAgentRevisionSafeSummary(t *testing.T) {
	r := validRevision()
	s := r.SafeSummary()
	if s["resource_name"] != "agent-1" {
		t.Errorf("resource_name = %v", s["resource_name"])
	}
	if s["version_label"] != "baseline" {
		t.Errorf("version_label = %v", s["version_label"])
	}
	if changed, ok := s["changed_fields"].([]string); !ok || len(changed) != 0 {
		t.Errorf("changed_fields must be empty slice: %v", s["changed_fields"])
	}
}

func TestAgentRevisionCanonicalSortsBindings(t *testing.T) {
	// 极端情况：binding 顺序不影响 canonical 序列化结果。
	r := validRevision()
	reversed := r
	reversed.Bindings = []AgentBinding{
		{Kind: AgentBindingMCP, ID: "mcp-1", Enabled: false},
		{Kind: AgentBindingSkill, ID: "skill-1", Enabled: true},
	}
	h1, _ := r.ContentHash()
	h2, _ := reversed.ContentHash()
	if h1 != h2 {
		t.Error("binding order must not affect content hash")
	}
}
