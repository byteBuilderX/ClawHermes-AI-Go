package domain

import (
	"errors"
	"fmt"
	"sort"
)

const MaxGeneratedCandidates = 64

// allowedParameterFields mirrors the registry evaluation keys (14 legacy +
// 1 newly opened compaction key + reasoning_effort). Keep in sync:
// registry_consistency_test fails when either side drifts, and the registry
// is the source of truth.
var allowedParameterFields = map[string]struct{}{
	"model": {}, "temperature": {}, "maxTokens": {}, "max_tokens": {},
	"max_context_tokens": {}, "max_iterations": {}, "bindings": {},
	"enabled_tools": {}, "timeout_ms": {}, "max_retries": {},
	"top_k": {}, "score_threshold": {}, "reranking": {}, "query_rewrite": {},
	"reasoning_effort": {},
}

var allowedPromptFields = map[string]struct{}{
	"instructions":             {},
	"system_prompt":            {},
	"memory_extraction_prompt": {},
	"memory_summary_prompt":    {},
	"memory_enrichment_prompt": {},
}

// IsGridSearchableParameter reports whether key belongs to the automatic
// grid-search parameter space (allowedParameterFields). Prompt fields are
// deliberately absent — ValidatePromptPatch is their patch gate, and feeding
// a prompt key into GenerateParameterPatches must fail closed.
func IsGridSearchableParameter(key string) bool {
	_, ok := allowedParameterFields[key]
	return ok
}

// IsPromptTunableField reports whether key belongs to the prompt-rewrite
// allowlist (allowedPromptFields). Prompt rewrites are LLM-driven and never
// enter the grid-search patch space.
func IsPromptTunableField(key string) bool {
	_, ok := allowedPromptFields[key]
	return ok
}

func GenerateParameterPatches(searchSpace map[string][]any) ([]map[string]any, error) {
	if len(searchSpace) == 0 {
		return nil, errors.New("parameter search space required")
	}
	keys := make([]string, 0, len(searchSpace))
	for key, values := range searchSpace {
		if _, ok := allowedParameterFields[key]; !ok {
			return nil, fmt.Errorf("parameter field is not optimizable: %s", key)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("parameter field has no candidates: %s", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	patches := []map[string]any{{}}
	for _, key := range keys {
		values := searchSpace[key]
		if len(patches)*len(values) > MaxGeneratedCandidates {
			return nil, fmt.Errorf("parameter search exceeds %d candidates", MaxGeneratedCandidates)
		}
		next := make([]map[string]any, 0, len(patches)*len(values))
		for _, patch := range patches {
			for _, value := range values {
				candidate := make(map[string]any, len(patch)+1)
				for existingKey, existingValue := range patch {
					candidate[existingKey] = existingValue
				}
				candidate[key] = value
				next = append(next, candidate)
			}
		}
		patches = next
	}
	return patches, nil
}

func ValidatePromptPatch(patch map[string]any) error {
	if len(patch) == 0 {
		return errors.New("prompt patch required")
	}
	for key, value := range patch {
		if _, ok := allowedPromptFields[key]; !ok {
			return fmt.Errorf("prompt field is not optimizable: %s", key)
		}
		text, ok := value.(string)
		if !ok || text == "" {
			return fmt.Errorf("prompt field %s must be a non-empty string", key)
		}
	}
	return nil
}
