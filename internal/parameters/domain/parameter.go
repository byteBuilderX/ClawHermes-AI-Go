// Package domain holds the canonical parameter definitions for the unified
// parameter registry. Definitions live in code (schema + bounds + effect
// point); only values live in the database.
package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Scope declares the single layer where a parameter can be written
// (single-attribution: each parameter belongs to exactly one layer, no
// priority-override matrix).
type Scope string

const (
	// ScopePlatform parameters are written at the platform layer
	// (platform_settings table) and apply globally.
	ScopePlatform Scope = "platform"
	// ScopeResource parameters are written at the resource layer
	// (agents.parameters JSONB / rag_workspaces.config) and fall back to
	// the platform default then the definition default.
	ScopeResource Scope = "resource"
)

// ValueType is the JSON value kind of a parameter.
type ValueType string

const (
	TypeInt    ValueType = "int"
	TypeFloat  ValueType = "float"
	TypeBool   ValueType = "bool"
	TypeString ValueType = "string"
)

// Control describes the frontend control to render for a parameter.
type Control string

const (
	ControlSlider   Control = "slider"
	ControlSelect   Control = "select"
	ControlToggle   Control = "toggle"
	ControlTextarea Control = "textarea"
	ControlNumber   Control = "number"
	// ControlModel renders a provider-grouped model picker (llmgateway model
	// directory: pick provider, then a model under it). The stored value is
	// still the model name string, validated against the directory at write.
	ControlModel Control = "model"
	// ControlEmbeddingModel renders an embedding-model picker (capability=embedding).
	ControlEmbeddingModel Control = "embedding_model"
)

// VisualHint drives schema-driven frontend rendering.
type VisualHint struct {
	Control Control  `json:"control"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Step    *float64 `json:"step,omitempty"`
	Options []any    `json:"options,omitempty"`
	Unit    string   `json:"unit,omitempty"`
}

// ParameterDefinition is the code-level single source of truth for one
// optimizable/tunable parameter: key, scope, bounds, default, UI hint and
// whether it participates in the evaluation search space.
type ParameterDefinition struct {
	Key         string     `json:"key"`
	Scope       Scope      `json:"scope"`
	GroupKey    string     `json:"group_key,omitempty"` // 平台分组归属（agent/memory/evaluation/trace），仅 ScopePlatform 必有
	Category    string     `json:"category"`
	DisplayName string     `json:"display_name"` // 用户可见中文名
	Description string     `json:"description"`
	ValueType   ValueType  `json:"value_type"`
	Default     any        `json:"default"`
	VisualHint  VisualHint `json:"visual_hint"`
	Optimizable bool       `json:"optimizable"`
	Sensitive   bool       `json:"sensitive"`
	RiskTier    RiskTier   `json:"risk_tier,omitempty"` // O3 风险分级 high/medium/low（空 = 注册时按 DefaultRiskTierForKey 自动填充）
	// ValidateFn overrides the built-in bounds check for complex-structure
	// parameters (bindings / enabled_tools) whose validation lives in the
	// evaluation adapters. Nil means built-in validation.
	ValidateFn func(any) error `json:"-"`
	// EvaluationKeys are the legacy bare-name aliases used in the
	// evaluation search space / candidate patches (e.g. "temperature",
	// "max_tokens"). A parameter may carry several aliases (camelCase and
	// snake_case legacy spellings both appear in real candidates). Empty
	// means the parameter does not participate in the evaluation loop.
	EvaluationKeys []string `json:"-"`
}

// ErrInvalidParameter marks a client-error (validation) failure so handlers
// map it to 400 instead of the generic error middleware's 500.
type ErrInvalidParameter struct {
	Key string
	Err error
}

func (e *ErrInvalidParameter) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "invalid parameter " + e.Key
}

func (e *ErrInvalidParameter) Unwrap() error { return e.Err }

// AsInvalidParameter extracts an *ErrInvalidParameter from err.
func AsInvalidParameter(err error, target **ErrInvalidParameter) bool {
	e, ok := err.(*ErrInvalidParameter)
	if ok {
		*target = e
	}
	return ok
}

// Validate checks value against the definition's bounds/options.
func (d *ParameterDefinition) Validate(value any) error {
	if value == nil {
		return fmt.Errorf("%s: value is required", d.Key)
	}
	switch d.ValueType {
	case TypeInt:
		return d.validateNumber(value, true)
	case TypeFloat:
		return d.validateNumber(value, false)
	case TypeBool:
		return d.validateBool(value)
	case TypeString:
		return d.validateString(value)
	default:
		return fmt.Errorf("%s: unknown value type %q", d.Key, d.ValueType)
	}
}

// validateBool checks a boolean value's type. Split out of Validate to keep the
// switch dispatch shallow (cyclomatic ratchet).
func (d *ParameterDefinition) validateBool(value any) error {
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("%s: expected bool, got %T", d.Key, value)
	}
	return nil
}

// validateString checks a string value's type and, when the definition constrains
// select options, membership in them. ValidateFn (registry-level semantic check)
// runs last when present.
func (d *ParameterDefinition) validateString(value any) error {
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s: expected string, got %T", d.Key, value)
	}
	if len(d.VisualHint.Options) > 0 && !d.matchesStringOption(s) {
		return fmt.Errorf("%s: value %q not in allowed options %v", d.Key, value, d.VisualHint.Options)
	}
	if d.ValidateFn != nil {
		return d.ValidateFn(value)
	}
	return nil
}

// matchesStringOption reports whether the string value is among the declared
// select options. Options constrain string-select parameters (e.g.
// reasoning_effort tiers); a nil/empty option list accepts any string.
// 空串恒放行:Select 参数的空串 = unset 哨兵(与 omitempty、isUnset 一致),
// Default:"" + Options 组合必须合法,否则 resolver 校验默认值就失败。
func (d *ParameterDefinition) matchesStringOption(s string) bool {
	if s == "" {
		return true
	}
	for _, opt := range d.VisualHint.Options {
		if o, ok := opt.(string); ok && o == s {
			return true
		}
	}
	return false
}

// validateNumber checks numeric type/bounds/options shared by int and float
// parameters. integer additionally rejects fractional values.
func (d *ParameterDefinition) validateNumber(value any, integer bool) error {
	v, ok := toFloat(value)
	if !ok {
		return fmt.Errorf("%s: expected number, got %T", d.Key, value)
	}
	if integer && math.Trunc(v) != v {
		return fmt.Errorf("%s: expected integer, got %v", d.Key, value)
	}
	if d.VisualHint.Min != nil && v < *d.VisualHint.Min {
		return fmt.Errorf("%s: must be >= %v, got %v", d.Key, *d.VisualHint.Min, value)
	}
	if d.VisualHint.Max != nil && v > *d.VisualHint.Max {
		return fmt.Errorf("%s: must be <= %v, got %v", d.Key, *d.VisualHint.Max, value)
	}
	if !d.matchesOption(v) {
		return fmt.Errorf("%s: value %v not in allowed options %v", d.Key, value, d.VisualHint.Options)
	}
	return nil
}

// Normalize converts a JSON-decoded value into the definition's canonical
// Go type (json.Number → int64/float64) so stored values are stable.
func (d *ParameterDefinition) Normalize(value any) (any, error) {
	if err := d.Validate(value); err != nil {
		return nil, err
	}
	switch d.ValueType {
	case TypeInt:
		f, _ := toFloat(value)
		return int64(f), nil
	case TypeFloat:
		f, _ := toFloat(value)
		return f, nil
	default:
		return value, nil
	}
}

// MarshalValue emits the value as the definition's canonical JSON type.
func (d *ParameterDefinition) MarshalValue(value any) (json.RawMessage, error) {
	v, err := d.Normalize(value)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// matchesOption reports whether the float value is among the declared select
// options. Options constrain numeric select parameters (compaction group
// counts); a nil/empty option list accepts any value within bounds.
func (d *ParameterDefinition) matchesOption(v float64) bool {
	if len(d.VisualHint.Options) == 0 {
		return true
	}
	for _, opt := range d.VisualHint.Options {
		if f, ok := toFloat(opt); ok && f == v {
			return true
		}
	}
	return false
}

// toFloat converts a JSON-decoded scalar to float64.
func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// sortedDefs returns definitions sorted by scope then key for stable output.
func sortedDefs(defs []ParameterDefinition) []ParameterDefinition {
	out := make([]ParameterDefinition, len(defs))
	copy(out, defs)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Key < out[j].Key
	})
	return out
}
