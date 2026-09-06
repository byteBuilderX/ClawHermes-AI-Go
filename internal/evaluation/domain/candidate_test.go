package domain

import "testing"

func TestGenerateParameterPatchesUsesDeterministicCartesianProduct(t *testing.T) {
	patches, err := GenerateParameterPatches(map[string][]any{
		"temperature": {0.1, 0.3},
		"maxTokens":   {512, 1024},
	})
	if err != nil {
		t.Fatalf("GenerateParameterPatches returned error: %v", err)
	}
	if len(patches) != 4 {
		t.Fatalf("expected 4 patches, got %d", len(patches))
	}
	if patches[0]["maxTokens"] != 512 || patches[0]["temperature"] != 0.1 {
		t.Fatalf("unexpected deterministic first patch: %#v", patches[0])
	}
}

func TestGenerateParameterPatchesSupportsEachResourceBoundary(t *testing.T) {
	for _, field := range []string{
		"model", "max_context_tokens", "max_iterations", "bindings",
		"enabled_tools", "timeout_ms", "max_retries",
		"top_k", "score_threshold", "reranking", "query_rewrite",
	} {
		if _, err := GenerateParameterPatches(map[string][]any{field: {1}}); err != nil {
			t.Fatalf("supported field %q rejected: %v", field, err)
		}
	}
}

func TestGenerateParameterPatchesRejectsProtectedFields(t *testing.T) {
	for _, field := range []string{"secretRefs", "permissions", "api_key"} {
		if _, err := GenerateParameterPatches(map[string][]any{field: {"unsafe"}}); err == nil {
			t.Fatalf("expected protected field %q to be rejected", field)
		}
	}
}

func TestValidatePromptPatchAllowsOnlyPromptContent(t *testing.T) {
	if err := ValidatePromptPatch(map[string]any{"instructions": "先分析输入，再按规则输出。"}); err != nil {
		t.Fatalf("valid prompt patch rejected: %v", err)
	}
	if err := ValidatePromptPatch(map[string]any{"permissions": map[string]any{"network": true}}); err == nil {
		t.Fatal("expected permissions patch to be rejected")
	}
}

func TestAllowlistPredicatesMatchPatchGates(t *testing.T) {
	tests := []struct {
		key    string
		grid   bool
		prompt bool
	}{
		{key: "temperature", grid: true},
		{key: "max_tokens", grid: true},
		{key: "max_retries", grid: true},
		{key: "top_k", grid: true},
		{key: "system_prompt", prompt: true},
		{key: "instructions", prompt: true},
		{key: "memory_extraction_prompt", prompt: true},
		{key: "memory_summary_prompt", prompt: true},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if got := IsGridSearchableParameter(tc.key); got != tc.grid {
				t.Errorf("IsGridSearchableParameter(%q) = %v, want %v", tc.key, got, tc.grid)
			}
			if got := IsPromptTunableField(tc.key); got != tc.prompt {
				t.Errorf("IsPromptTunableField(%q) = %v, want %v", tc.key, got, tc.prompt)
			}
		})
	}
	// The split must match the patch gates: a grid key can never pass through
	// the prompt-rewrite gate and vice versa, so wiring a direction into the
	// wrong optimizer can never silently succeed (I3).
	for _, gridKey := range []string{"temperature", "max_tokens", "max_retries", "top_k"} {
		if _, err := GenerateParameterPatches(map[string][]any{gridKey: {1}}); err != nil {
			t.Errorf("grid key %q rejected by GenerateParameterPatches: %v", gridKey, err)
		}
		if err := ValidatePromptPatch(map[string]any{gridKey: "x"}); err == nil {
			t.Errorf("grid key %q passed ValidatePromptPatch, want rejection", gridKey)
		}
	}
	for _, promptKey := range []string{"system_prompt", "instructions",
		"memory_extraction_prompt", "memory_summary_prompt"} {
		if _, err := GenerateParameterPatches(map[string][]any{promptKey: {"x"}}); err == nil {
			t.Errorf("prompt key %q passed GenerateParameterPatches, want rejection", promptKey)
		}
		if err := ValidatePromptPatch(map[string]any{promptKey: "refine"}); err != nil {
			t.Errorf("prompt key %q rejected by ValidatePromptPatch: %v", promptKey, err)
		}
	}
}
