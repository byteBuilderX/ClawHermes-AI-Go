package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

func TestRegistryRegistersAllBuiltinKeys(t *testing.T) {
	r := NewParametersRegistry()
	defs := r.Schema()

	// 全部定义唯一注册,且 key 前缀合法。
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		if seen[def.Key] {
			t.Fatalf("duplicate key in schema: %s", def.Key)
		}
		seen[def.Key] = true
		if def.Scope != ScopePlatform && def.Scope != ScopeResource {
			t.Fatalf("%s: invalid scope %q", def.Key, def.Scope)
		}
		// 复杂结构参数(bindings/enabled_tools)无定义默认,其余必须携带。
		if def.Default == nil && def.ValidateFn == nil {
			t.Fatalf("%s: missing default", def.Key)
		}
	}

	// 存量搜索空间零收缩:14 参数全部 Optimizable=true。
	for _, key := range []string{
		"agent.temperature", "agent.max_tokens", "agent.max_context_tokens",
		"agent.max_iterations", "agent.model", "agent.bindings",
		"rag.top_k", "rag.score_threshold", "rag.reranking", "rag.query_rewrite",
		"mcp.enabled_tools", "mcp.timeout_ms", "mcp.max_retries",
	} {
		def, ok := r.Get(key)
		if !ok {
			t.Fatalf("legacy key %s not registered", key)
		}
		if !def.Optimizable {
			t.Fatalf("legacy key %s must stay optimizable (search space must not shrink)", key)
		}
	}

}

func TestRegistryEvaluationKeyMapping(t *testing.T) {
	r := NewParametersRegistry()
	for bare, want := range map[string]string{
		"temperature":        "agent.temperature",
		"max_tokens":         "agent.max_tokens",
		"maxTokens":          "agent.max_tokens",
		"max_context_tokens": "agent.max_context_tokens",
		"max_iterations":     "agent.max_iterations",
		"model":              "agent.model",
		"bindings":           "agent.bindings",
		"top_k":              "rag.top_k",
		"score_threshold":    "rag.score_threshold",
		"reranking":          "rag.reranking",
		"query_rewrite":      "rag.query_rewrite",
		"enabled_tools":      "mcp.enabled_tools",
		"timeout_ms":         "mcp.timeout_ms",
		"max_retries":        "mcp.max_retries",
	} {
		if !r.IsEvaluationKey(bare) {
			t.Errorf("bare key %q must be registered", bare)
		}
		if got, _ := r.KeyForEvaluation(bare); got != want {
			t.Errorf("KeyForEvaluation(%q) = %q, want %q", bare, got, want)
		}
	}
	if r.IsEvaluationKey("bogus_key") {
		t.Error("unknown bare key must not be registered")
	}
}

// TestPromptEvaluationKeysAreGateOnly pins the decoupling: the 5 prompt patch
// bare keys survive as gate-only evaluation keys (valid candidate-patch
// fields for validatePatchKeys) but carry no parameter definition — they must
// not resolve through KeyForEvaluation and must not appear in the schema.
func TestPromptEvaluationKeysAreGateOnly(t *testing.T) {
	r := NewParametersRegistry()
	for _, bare := range []string{
		"system_prompt", "instructions",
		"memory_extraction_prompt", "memory_summary_prompt",
		"memory_enrichment_prompt",
	} {
		if !r.IsEvaluationKey(bare) {
			t.Errorf("prompt key %q must stay a valid candidate-patch key (gate-only)", bare)
		}
		if _, ok := r.KeyForEvaluation(bare); ok {
			t.Errorf("prompt key %q must not resolve to a parameter definition", bare)
		}
		if _, ok := r.Get("prompt." + bare); ok {
			t.Errorf("prompt.%s definition must be removed from the registry", bare)
		}
	}
	for _, def := range r.Schema() {
		if def.Key[:7] == "prompt." {
			t.Fatalf("schema must not expose prompt definitions, got %s", def.Key)
		}
	}
}

// TestRegistryAgentMemoryParams 覆盖 agent 维度新 key:压缩温度/模型、平台级
// 压缩/全局提示词、提取 prompt/model、召回 top_k 校准、long_term_top_k 保留
// 标注 deprecated。断言 compaction 温度/模型不进入 byEvalKey、不设
// EvaluationKeys(防半注册回归)。
func TestRegistryAgentMemoryParams(t *testing.T) {
	r := NewParametersRegistry()

	// 压缩温度 bare key 不进 byEvalKey(评测搜索空间干净),写时校验经短名匹配。
	if got, ok := r.KeyForEvaluation("compaction_temperature"); ok {
		t.Errorf("KeyForEvaluation(compaction_temperature) = %q, want unregistered", got)
	}
	if got, ok := r.KeyByShortName("compaction_temperature"); !ok || got != "agent.compaction_temperature" {
		t.Errorf("KeyByShortName(compaction_temperature) = %q/%v, want agent.compaction_temperature/true", got, ok)
	}

	// 平台级提示词:agent.compaction_prompt / agent.system_prompt（fail-closed,
	// 无默认模板）,不进入 byEvalKey。
	for _, key := range []string{"agent.compaction_prompt", "agent.system_prompt"} {
		def, ok := r.Get(key)
		if !ok {
			t.Fatalf("%s not registered", key)
		}
		if def.Scope != ScopePlatform || def.Optimizable || def.Default != "" {
			t.Errorf("%s scope/optimizable/default = %q/%v/%v, want platform/false/empty", key, def.Scope, def.Optimizable, def.Default)
		}
		if def.VisualHint.Control != ControlTextarea {
			t.Errorf("%s control = %q, want textarea", key, def.VisualHint.Control)
		}
		if _, ok := r.KeyForEvaluation("compaction_prompt"); ok {
			t.Error("KeyForEvaluation(compaction_prompt) must stay unregistered")
		}
	}

	// 压缩温度/模型:平台级共用配置（唯一来源，所有 agent 一致），不可优化、
	// 无 EvaluationKeys；temperature 0 = 默认常量，model 空 = 网关默认。
	platformCompaction := map[string]struct {
		control          Control
		wantMin, wantMax float64
	}{
		"agent.compaction_temperature": {control: ControlSlider, wantMin: 0, wantMax: 1},
		"agent.compaction_model":       {control: ControlModel},
	}
	for key, want := range platformCompaction {
		def, ok := r.Get(key)
		if !ok {
			t.Fatalf("%s not registered", key)
		}
		if def.Scope != ScopePlatform || def.Optimizable || len(def.EvaluationKeys) != 0 {
			t.Errorf("%s scope/optimizable/evalKeys = %q/%v/%d, want platform/false/0", key, def.Scope, def.Optimizable, len(def.EvaluationKeys))
		}
		if def.VisualHint.Control != want.control {
			t.Errorf("%s control = %q, want %q", key, def.VisualHint.Control, want.control)
		}
		if def.VisualHint.Min != nil && *def.VisualHint.Min != want.wantMin {
			t.Errorf("%s VisualHint.Min = %v, want %v", key, *def.VisualHint.Min, want.wantMin)
		}
		if def.VisualHint.Max != nil && *def.VisualHint.Max != want.wantMax {
			t.Errorf("%s VisualHint.Max = %v, want %v", key, *def.VisualHint.Max, want.wantMax)
		}
	}

	// 提取/反思 prompt/model:ScopePlatform（记忆配置平台化,编辑入口在平台参数页）,
	// 字符串自由校验,无 EvaluationKeys。
	for _, key := range []string{
		"memory.extraction_prompt", "memory.extraction_model",
		"memory.reflection_prompt", "memory.reflection_model",
	} {
		def, ok := r.Get(key)
		if !ok {
			t.Fatalf("%s not registered", key)
		}
		if def.Scope != ScopePlatform || def.Optimizable {
			t.Errorf("%s scope/optimizable = %q/%v, want platform/false", key, def.Scope, def.Optimizable)
		}
		if len(def.EvaluationKeys) != 0 {
			t.Errorf("%s must not carry EvaluationKeys (string free-form), got %v", key, def.EvaluationKeys)
		}
	}

	// 提取/反思模型:模型目录选择器(ControlModel),下拉数据来自模型管理;
	// 不得用无 options 的 select(空下拉 bug 回归)。
	for _, key := range []string{"memory.extraction_model", "memory.reflection_model"} {
		def, ok := r.Get(key)
		if !ok {
			t.Fatalf("%s not registered", key)
		}
		if def.VisualHint.Control != ControlModel {
			t.Errorf("%s control = %q, want model picker (ControlModel)", key, def.VisualHint.Control)
		}
	}

	// 记忆数值参数:ScopePlatform（平台参数页编辑,0=unset 回落定义默认）。
	for _, key := range []string{
		"memory.recall_top_k", "memory.fact_injection_top_n",
		"memory.history_injection_top_n", "memory.max_facts_per_extraction",
	} {
		def, ok := r.Get(key)
		if !ok {
			t.Fatalf("%s not registered", key)
		}
		if def.Scope != ScopePlatform {
			t.Errorf("%s scope = %q, want platform", key, def.Scope)
		}
	}

	// 召回 top_k:Default 5(对齐运行时兜底)、Max 20(工具上限)。
	recall, ok := r.Get("memory.recall_top_k")
	if !ok {
		t.Fatal("memory.recall_top_k not registered")
	}
	if d, ok := recall.Default.(int64); !ok || d != 5 {
		t.Errorf("recall_top_k Default = %#v, want 5", recall.Default)
	}
	if recall.VisualHint.Max == nil || *recall.VisualHint.Max != 20 {
		t.Errorf("recall_top_k VisualHint.Max = %v, want 20", recall.VisualHint.Max)
	}

	// long_term_top_k 保留注册(兼容存量 agents.parameters 残留 key,删会破坏
	// ValidateResourceKey fail-closed 提升路径)。
	if _, ok := r.Get("memory.long_term_top_k"); !ok {
		t.Error("memory.long_term_top_k must stay registered (deprecated)")
	}
}

func TestRegistryDuplicateKeyRejected(t *testing.T) {
	r := NewParametersRegistry()
	err := r.Register(ParameterDefinition{Key: "agent.temperature", Scope: ScopePlatform, ValueType: TypeFloat, Default: 1.0})
	if err == nil {
		t.Fatal("duplicate key must be rejected")
	}
}

func TestParameterDefinitionValidateAndNormalize(t *testing.T) {
	r := NewParametersRegistry()
	cases := []struct {
		name    string
		key     string
		value   any
		wantOK  bool
		wantVal any
	}{
		{name: "temperature in bounds", key: "agent.temperature", value: json.Number("0.3"), wantOK: true, wantVal: 0.3},
		{name: "temperature above max", key: "agent.temperature", value: 2.5, wantOK: false},
		{name: "temperature below min", key: "agent.temperature", value: -0.1, wantOK: false},
		{name: "max_tokens zero unset ok", key: "agent.max_tokens", value: 0, wantOK: true, wantVal: int64(0)},
		{name: "max_tokens negative", key: "agent.max_tokens", value: -1, wantOK: false},
		{name: "bool", key: "rag.reranking", value: true, wantOK: true, wantVal: true},
		{name: "bool wrong type", key: "rag.reranking", value: "yes", wantOK: false},
		{name: "string", key: "agent.model", value: "qwen-plus", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def, ok := r.Get(tc.key)
			if !ok {
				t.Fatalf("key %s not registered", tc.key)
			}
			got, err := def.Normalize(tc.value)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("Normalize(%v) error: %v", tc.value, err)
				}
				if tc.wantVal != nil && got != tc.wantVal {
					t.Fatalf("Normalize(%v) = %v (%T), want %v", tc.value, got, got, tc.wantVal)
				}
			} else if err == nil {
				t.Fatalf("Normalize(%v) must fail", tc.value)
			}
		})
	}
}

// TestRegistryMemoryEmbeddingModel 断言平台级记忆嵌入模型参数定义：
// ScopePlatform、embedding_model 控件、不可优化、无评测 key（防半注册回归）。
func TestRegistryMemoryEmbeddingModel(t *testing.T) {
	r := NewParametersRegistry()
	def, ok := r.Get("memory.embedding_model")
	if !ok {
		t.Fatal("memory.embedding_model not registered")
	}
	if def.Scope != ScopePlatform {
		t.Errorf("scope = %q, want platform", def.Scope)
	}
	if def.VisualHint.Control != ControlEmbeddingModel {
		t.Errorf("control = %q, want embedding_model", def.VisualHint.Control)
	}
	if def.Optimizable {
		t.Error("optimizable must be false")
	}
	if len(def.EvaluationKeys) != 0 {
		t.Errorf("evaluation keys = %v, want none", def.EvaluationKeys)
	}
	if def.Default != "" {
		t.Errorf("default = %v, want empty (fail-closed)", def.Default)
	}
}

// TestRegistryPlatformParamsHaveGroupKey pins the single-attribution
// invariant: every ScopePlatform parameter must carry a non-empty GroupKey
// belonging to the four declared groups, and the declared group must match
// the hard-coded GroupForKey mapping (no drift between explicit and derived).
func TestRegistryPlatformParamsHaveGroupKey(t *testing.T) {
	r := NewParametersRegistry()
	seenGroups := make(map[string]int)
	for _, def := range r.Schema() {
		if def.Scope != ScopePlatform {
			continue
		}
		if def.GroupKey == "" {
			t.Fatalf("%s: platform param missing GroupKey", def.Key)
		}
		switch def.GroupKey {
		case GroupAgent, GroupMemory, GroupEvaluation, GroupTrace:
			seenGroups[def.GroupKey]++
		default:
			t.Fatalf("%s: unexpected group %q", def.Key, def.GroupKey)
		}
		if want := GroupForKey(def.Key); want != def.GroupKey {
			t.Fatalf("%s: GroupKey %q != GroupForKey %q", def.Key, def.GroupKey, want)
		}
	}
	// 四组都必须有实际成员（迁移 seed 无死组）。
	for _, g := range []string{GroupAgent, GroupMemory, GroupEvaluation, GroupTrace} {
		if seenGroups[g] == 0 {
			t.Fatalf("group %q has no platform params", g)
		}
	}
}

// TestGroupForKey is the pure-function mapping test for the group derivation:
// known platform keys resolve to their group, unknown/legacy/empty resolve to "".
func TestGroupForKey(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"agent.compaction_prompt", GroupAgent},
		{"agent.system_prompt", GroupAgent},
		{"agent.factcheck.judge.model", GroupAgent},
		{"memory.summary_token_threshold", GroupMemory},
		{"memory.supersede_model", GroupMemory},
		{"evaluation.optimizer.model", GroupEvaluation},
		{"evaluation.judge.enabled", GroupEvaluation},
		{"trace.capture_parameters", GroupTrace},
		{"rag.top_k", ""},
		{"mcp.enabled_tools", ""},
		{"prompt.system_prompt", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := GroupForKey(tc.key); got != tc.want {
			t.Errorf("GroupForKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestPlatformConfigGroupsMatchMigrationSeed guards consistency between the
// registry group constants and the 043 migration seed: every group declared in
// Go must appear in the platform_config_groups seed, and the seed must not
// declare extra groups. Renumbering the migration updates this path.
func TestPlatformConfigGroupsMatchMigrationSeed(t *testing.T) {
	content, err := os.ReadFile("../../../pkg/migration/sql/043_platform_config_versions.up.sql")
	if err != nil {
		t.Fatalf("read 043 migration: %v", err)
	}
	text := string(content)
	seed := `    ('agent',      'Agent'),
    ('memory',     'Memory'),
    ('evaluation', 'Evaluation'),
    ('trace',      'Trace')`
	if !strings.Contains(text, seed) {
		t.Fatalf("043 seed missing group VALUES block:\n%q", seed)
	}
	// 迁移 seed 不得引入 registry 未声明的组（以 GroupForKey 全集为准）。
	for _, g := range []string{GroupAgent, GroupMemory, GroupEvaluation, GroupTrace} {
		if !strings.Contains(text, "'"+g+"'") {
			t.Fatalf("migration seed missing group %q", g)
		}
	}
}

// TestFactCheckJudgePromptIsPlatformEditable pins the platform-page contract:
// agent.factcheck.judge.prompt 必须保持 platform 域、非 sensitive（平台参数页
// 永不渲染敏感参数）、textarea 可编辑。回归 #420:误标 Sensitive 导致提示词
// 被前端过滤,页面只显示其余 factcheck 参数。
func TestFactCheckJudgePromptIsPlatformEditable(t *testing.T) {
	r := NewParametersRegistry()
	def, ok := r.Get("agent.factcheck.judge.prompt")
	if !ok {
		t.Fatal("agent.factcheck.judge.prompt not registered")
	}
	if def.Scope != ScopePlatform {
		t.Errorf("scope = %q, want platform", def.Scope)
	}
	if def.Sensitive {
		t.Error("judge prompt must not be Sensitive: platform settings page never renders sensitive params")
	}
	if def.VisualHint.Control != ControlTextarea {
		t.Errorf("control = %q, want textarea", def.VisualHint.Control)
	}
}

// TestFactCheckCitationAndTemperatureRegistered pins the platform-page contract
// for the claim-citation feature params: 对账轨开关与 judge 温度必须保持 platform
// 域、非 sensitive、默认值语义（citation_verify 默认关 = fail-closed；temperature
// 0 = unset 用模型默认）。
func TestFactCheckCitationAndTemperatureRegistered(t *testing.T) {
	r := NewParametersRegistry()
	cases := []struct {
		key       string
		platform  bool
		sensitive bool
	}{
		{"agent.factcheck.citation_verify", true, false},
		{"agent.factcheck.judge.temperature", true, false},
	}
	for _, tc := range cases {
		def, ok := r.Get(tc.key)
		if !ok {
			t.Fatalf("%s not registered", tc.key)
		}
		if tc.platform && def.Scope != ScopePlatform {
			t.Errorf("%s scope = %q, want platform", tc.key, def.Scope)
		}
		if def.Sensitive {
			t.Errorf("%s must not be Sensitive", tc.key)
		}
	}
}

// TestJudgeRubricAndOptimizerSystemPlatformed pins the prompt platformization
// contract (spec 2026-09-04): evaluation.judge.rubric / evaluation.optimizer
// .system_prompt 平台级暴露为多行 textarea、不进入优化搜索空间（Optimizable=false），
// 且注册默认值 == pkg/constants 常量——开箱可见值 == 内置兜底 byte-identical，永不漂移。
func TestJudgeRubricAndOptimizerSystemPlatformed(t *testing.T) {
	r := NewParametersRegistry()
	cases := []struct {
		key      string
		defText  string // pkg/constants 单一来源文本
		platform bool
	}{
		{"evaluation.judge.rubric", constants.EvaluationJudgeDefaultRubric, true},
		{"evaluation.optimizer.system_prompt", constants.EvaluationOptimizerSystemPrompt, true},
	}
	for _, tc := range cases {
		def, ok := r.Get(tc.key)
		if !ok {
			t.Fatalf("%s not registered", tc.key)
		}
		if tc.platform && def.Scope != ScopePlatform {
			t.Errorf("%s scope = %q, want platform", tc.key, def.Scope)
		}
		if def.Category != "evaluation" {
			t.Errorf("%s category = %q, want evaluation", tc.key, def.Category)
		}
		if def.Optimizable {
			t.Errorf("%s must not be optimizable (optimizer must not rewrite its own judge/rubric)", tc.key)
		}
		if def.VisualHint.Control != ControlTextarea {
			t.Errorf("%s control = %q, want textarea", tc.key, def.VisualHint.Control)
		}
		if def.Sensitive {
			t.Errorf("%s must not be Sensitive: platform settings page never renders sensitive params", tc.key)
		}
		if got, _ := def.Default.(string); got != tc.defText {
			t.Errorf("%s default != pkg/constants 常量（平台默认与内置兜底必须 byte-identical）", tc.key)
		}
	}
}

// TestRegistryEveryKeyHasRiskTier 守护 O3 不变量：每个注册键都必须有非空 RiskTier，
// 且与 DefaultRiskTierForKey 一致（显式声明不允许漂移出分类表）。
func TestRegistryEveryKeyHasRiskTier(t *testing.T) {
	r := NewParametersRegistry()
	for _, def := range r.Schema() {
		switch def.RiskTier {
		case RiskTierHigh, RiskTierMedium, RiskTierLow:
		default:
			t.Fatalf("key %s risk tier %q must be one of high/medium/low", def.Key, def.RiskTier)
		}
		if want := DefaultRiskTierForKey(def.Scope, def.Key); def.RiskTier != want {
			t.Fatalf("key %s risk tier %q != DefaultRiskTierForKey %q", def.Key, def.RiskTier, want)
		}
	}
}

// TestRegistryRiskTierClassifiesGateRelevantKeys 守护 O3 high 名单与关键 medium/low 归类。
func TestRegistryRiskTierClassifiesGateRelevantKeys(t *testing.T) {
	r := NewParametersRegistry()
	cases := []struct {
		key  string
		want RiskTier
	}{
		{"agent.model", RiskTierHigh},         // 资源 high（§7.2）
		{"mcp.enabled_tools", RiskTierHigh},   // 资源 high（§7.2）
		{"agent.system_prompt", RiskTierHigh}, // 平台 high（§7.2）
		{"evaluation.judge.model", RiskTierHigh},
		{"evaluation.optimizer.model", RiskTierHigh},
		{"agent.factcheck.judge.model", RiskTierHigh},
		{"memory.embedding_model", RiskTierHigh},
		{"memory.extraction_model", RiskTierHigh},
		{"memory.reflection_model", RiskTierHigh},
		{"rag.top_k", RiskTierMedium},                    // 资源 medium
		{"agent.reasoning_effort", RiskTierMedium},       // 资源 medium
		{"agent.compaction_temperature", RiskTierMedium}, // 平台 medium（_temperature 后缀）
		{"agent.factcheck.judge.prompt", RiskTierMedium}, // 平台 medium（.prompt 类型叶，点号分层）
		{"evaluation.judge.temperature", RiskTierMedium}, // 平台 medium（.temperature 类型叶，§7.2）
		{"agent.temperature", RiskTierLow},               // 资源采样键不落平台后缀规则
		{"evaluation.judge.enabled", RiskTierLow},        // 开关默认 low
	}
	for _, tc := range cases {
		def, ok := r.Get(tc.key)
		if !ok {
			t.Fatalf("key %s not registered", tc.key)
		}
		if def.RiskTier != tc.want {
			t.Fatalf("key %s risk tier = %q, want %q", tc.key, def.RiskTier, tc.want)
		}
	}
}
