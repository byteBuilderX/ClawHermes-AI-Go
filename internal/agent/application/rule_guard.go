package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

// RuleBlockedError 是规则护栏即时拦截信号：命中即失败返回，禁止默认放行（§4.1 fail closed）。
type RuleBlockedError struct {
	Rule    string
	Tool    string
	Message string
}

func (e *RuleBlockedError) Error() string {
	return fmt.Sprintf("rule_blocked:%s:tool=%s:%s", e.Rule, e.Tool, e.Message)
}

// ruleBlockCollectorKey 是 context 累积器 key：AgentService 在执行上下文中注入
// *[]domain.RuleBlock，RuleGuard 命中时追加，emitObservation 读取进观测事件。
type ruleBlockCollectorKey struct{}

// RuleGuardDeps 是规则护栏依赖。Enabled/Denylist 由 wiring 包装平台参数读取
// （evaluation.ruleguard.*，仅注册不播种；enabled 默认 false 时静默放行）。
type RuleGuardDeps struct {
	Enabled  func(ctx context.Context) bool
	Denylist func(ctx context.Context) []string
	Metrics  observability.MetricsProvider
	Logger   *zap.Logger
}

// RuleGuard 是内联 L1 规则护栏（spec §3.1 快路径，O4：检测恒开 + 执行受控）：
// denylist 命中恒检测/恒观测，仅 enabled 时真拦截。零 LLM、零额外延迟。
type RuleGuard struct {
	deps RuleGuardDeps
}

func NewRuleGuard(deps RuleGuardDeps) *RuleGuard {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	if deps.Metrics == nil {
		deps.Metrics = observability.NoopMetrics{}
	}
	return &RuleGuard{deps: deps}
}

// 规则护栏指标标签常量（spec §3.1 L198 / dict R20）：检测与执行分离后 hit 判别由
// verdict 承担（block=真拦截 / detected=检测未拦截）；guard 层计数落 l1_rule
// （原 rule_guard label 全仓零消费，R20 裁决更名）。enabled==nil 视为 false。
const (
	ruleGuardKindDenylist    = "tool_denylist"
	ruleGuardResourceAgent   = "agent"
	ruleGuardLayerL1         = "l1_rule"
	ruleGuardVerdictBlock    = "block"
	ruleGuardVerdictDetected = "detected"
)

// Check 对单个工具名执行 L1 规则护栏（spec §3.1 / O4）。检测与执行分离：
//   - denylist 为空/nil → (nil,false)，零命中零观测；
//   - 命中判定保留 strings.EqualFold(strings.TrimSpace(denied), toolID)（大小写不敏感）；
//   - 任一命中（不论 enabled）→ (a) 恒写 ctx 累积器（ruleBlockCollectorKey，供
//     emitObservation 产出 rule 信号）+ (b) IncEvalRuleHit("tool_denylist","agent",verdict)，
//     verdict = enabled ? "block" : "detected"（判别由 verdict 承担，R20）；
//   - 仅 enabled && hit → (c) IncEvalGateAction("l1_rule","block") + 返回
//     RuleBlockedError（真拦截，fail closed）；否则 (nil,false) 放行（检测/观测照常）。
//
// enabled==nil 视为 false；RuleGuardDeps 结构与调用点（tool_execution_guard.go）语义不变。
func (g *RuleGuard) Check(ctx context.Context, toolID string) (*RuleBlockedError, bool) {
	if g == nil {
		return nil, false
	}
	enabled := g.deps.Enabled != nil && g.deps.Enabled(ctx)
	if g.deps.Denylist == nil {
		return nil, false
	}
	for _, denied := range g.deps.Denylist(ctx) {
		denied = strings.TrimSpace(denied)
		if denied == "" || !strings.EqualFold(denied, toolID) {
			continue
		}
		message := fmt.Sprintf("tool %q blocked by platform rule", toolID)
		verdict := ruleGuardVerdictDetected
		if enabled {
			verdict = ruleGuardVerdictBlock
		}
		// (a)+(b) 检测恒开：命中恒记 hit、恒填累积器（O4：disabled 命中同样产观测）。
		g.deps.Metrics.IncEvalRuleHit(ruleGuardKindDenylist, ruleGuardResourceAgent, verdict)
		if collector, ok := ctx.Value(ruleBlockCollectorKey{}).(*[]domain.RuleBlock); ok {
			*collector = append(*collector, domain.RuleBlock{Rule: ruleGuardKindDenylist, Tool: toolID, Message: message})
		}
		// (c) 执行受控：仅 enabled && hit 真拦截（fail closed）。
		if enabled {
			g.deps.Metrics.IncEvalGateAction(ruleGuardLayerL1, ruleGuardVerdictBlock)
			return &RuleBlockedError{Rule: ruleGuardKindDenylist, Tool: toolID, Message: message}, true
		}
		return nil, false
	}
	return nil, false
}
