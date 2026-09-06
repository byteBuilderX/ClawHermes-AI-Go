package domain

import "strings"

// RiskTier 是参数变更的 O3 风险分级（spec §4.2.1）：门禁/审批按它决定回滚与升级路径。
type RiskTier string

const (
	RiskTierHigh   RiskTier = "high"
	RiskTierMedium RiskTier = "medium"
	RiskTierLow    RiskTier = "low"
)

// riskTierHighKeys 是 O3 判定的 high 风险键全集：平台 8 + 资源 2（规格 §7.2 已确认）。
// 平台键影响全租户评测/judge/记忆质量；资源键 agent.model 更换推理模型、mcp.enabled_tools
// 变更能力面。
var riskTierHighKeys = map[string]struct{}{
	"agent.system_prompt": {}, "agent.compaction_model": {},
	"evaluation.judge.model": {}, "evaluation.optimizer.model": {}, "agent.factcheck.judge.model": {},
	"memory.embedding_model": {}, "memory.extraction_model": {}, "memory.reflection_model": {},
	"agent.model": {}, "mcp.enabled_tools": {},
}

// riskTierResourceMediumKeys 是资源 scope medium 键集（平台 medium 后缀规则不覆盖的资源键）。
var riskTierResourceMediumKeys = map[string]struct{}{
	"rag.top_k": {}, "rag.score_threshold": {}, "rag.reranking": {}, "rag.query_rewrite": {},
	"agent.reasoning_effort": {}, "agent.max_iterations": {}, "memory.long_term_top_k": {},
}

// riskTierTypeLeaves 是平台 medium 的类型叶集合（spec §7.2 O3 medium 行：
// 其余 *_model、*_prompt、*_temperature）。判定取键的「类型叶」而非裸下划线后缀，
// 以同时覆盖 registry 两种命名：下划线压平（agent.compaction_prompt）与点号分层
// （evaluation.judge.temperature / agent.factcheck.judge.prompt，spec 示例）。
var riskTierTypeLeaves = map[string]struct{}{
	"model": {}, "prompt": {}, "temperature": {},
}

// riskTierTypeLeaf 提取参数键的「类型叶」：最后一个点号段；该段若含下划线再取
// 下划线后的词根（agent.compaction_prompt → prompt；agent.factcheck.judge.prompt
// → prompt）。开关/数值键（enabled、sample_rate、cooldown_sec、…）返回自身词根，
// 不落入 medium 叶集。
func riskTierTypeLeaf(key string) string {
	leaf := key
	if i := strings.LastIndex(key, "."); i >= 0 {
		leaf = key[i+1:]
	}
	if i := strings.LastIndex(leaf, "_"); i >= 0 {
		leaf = leaf[i+1:]
	}
	return leaf
}

// DefaultRiskTierForKey 返回键的默认风险分级。判定顺序：high 全集 → 平台 medium
// （键末段类型叶 ∈ {model,prompt,temperature}，已 high 的键先行短路）→ 资源
// medium 集 → low。scope 参与判定保证互斥：agent.temperature（资源采样键）不
// 落入平台 medium 类型叶规则。
func DefaultRiskTierForKey(scope Scope, key string) RiskTier {
	if _, ok := riskTierHighKeys[key]; ok {
		return RiskTierHigh
	}
	if scope == ScopePlatform {
		if _, ok := riskTierTypeLeaves[riskTierTypeLeaf(key)]; ok {
			return RiskTierMedium
		}
		return RiskTierLow
	}
	if _, ok := riskTierResourceMediumKeys[key]; ok {
		return RiskTierMedium
	}
	return RiskTierLow
}
