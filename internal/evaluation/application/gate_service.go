package application

import (
	"context"
	"sync"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

// gateLayer 是台账 layer 列与指标维度的统一层名。
const gateLayer = "observation"

// GateServiceDeps 是分层门禁应用服务依赖。全部依赖可选（nil → 对应动作跳过，
// fail-open），保证观测主流程不被门禁链路阻断（与 escalateToReview 同哲学）。
type GateServiceDeps struct {
	Logger *zap.Logger
	// Metrics nil-safe（IncEvalGateAction 不发）；生产由 wiring 注入真实 provider。
	Metrics observability.MetricsProvider
	// Cfg 实时读平台参数（evaluation.gate.enabled / auto_rollback_resources）。
	// nil 或返回 !Enabled → 门禁整体跳过（fail open 于门禁层）。
	Cfg func(ctx context.Context) domain.GateConfig
	// Repo nil（未装配证据源）→ 无法查窗口，跳过评估（fail-open）。
	Repo port.GateStore
	// Policy nil → 跳过决策（策略未装配不评估）。
	Policy port.GatePolicySource
	// Platform 平台 eval_state 写回（decision ∈ {rollback_manual, rollback_auto}）；
	// nil 跳过写回（裁决 R11）。
	Platform port.PlatformGateOps
	// Approvals rollback_manual 人工审批请求；nil 跳过（台账已记录决策）。
	Approvals port.GateApprovalRequester
	// ResourceRollback 资源自动回滚执行器；P1 恒 nil（决策动作不内联执行回滚，
	// 裁决 R9），T8+ 装配。
	ResourceRollback port.ResourceRollbackExecutor
}

// GateService 实现 port.GateSink：观测落库后评估窗口证据并路由决策。
type GateService struct {
	deps GateServiceDeps

	now  func() time.Time
	mu   sync.Mutex
	last map[string]time.Time // target.Key() → 最近一次非 none 决策时间（GateCooldown）
}

// NewGateService 构造门禁服务。Logger 缺省 zap.NewNop。
func NewGateService(deps GateServiceDeps) *GateService {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	return &GateService{
		deps: deps,
		now:  time.Now,
		last: make(map[string]time.Time),
	}
}

var _ port.GateSink = (*GateService)(nil)

// HandleObservation 在观测落库后评估一次门禁并路由（裁决 R9：不内联执行回滚）。
// fail-open：任何依赖缺失/失败只日志，返回 nil 不阻断调用方；决策只对非 none
// 生效冷却，none 不记台账。
func (s *GateService) HandleObservation(ctx context.Context, tenantID string, obs domain.EvalObservation) error {
	target, ok := domain.GateTargetForObservation(obs)
	if !ok {
		return nil
	}
	if !s.cfgEnabled(ctx) || s.cooldownActive(target) {
		return nil
	}
	if s.deps.Repo == nil {
		// Repo nil（未装配证据源）→ 无法查窗口，跳过评估（fail-open）。
		return nil
	}
	if s.deps.Policy == nil {
		// Policy nil → 跳过决策（策略未装配不评估），免去无谓的窗口查询。
		return nil
	}
	since := s.now().UTC().Add(-constants.GateObservationWindow)
	ev, err := s.deps.Repo.QueryWindow(ctx, tenantID, target, since)
	if err != nil {
		s.warn("gate window query failed", zap.Error(err), zap.String("target", target.Key()))
		return nil
	}
	policy, err := s.deps.Policy.Resolve(ctx, target)
	if err != nil {
		s.warn("gate policy resolve failed", zap.Error(err), zap.String("target", target.Key()))
		return nil
	}
	action := domain.Decide(policy, ev)
	if action == domain.GateNone {
		return nil
	}
	s.markTriggered(target)
	s.route(ctx, tenantID, target, action, ev)
	return nil
}

// cfgEnabled 返回门禁开关：Cfg nil 视为关闭（安全默认，fail open）。
func (s *GateService) cfgEnabled(ctx context.Context) bool {
	if s.deps.Cfg == nil {
		return false
	}
	return s.deps.Cfg(ctx).Enabled
}

// route 路由非 none 决策：台账 + 指标 + 平台写回 / 审批 / 自动回滚（全 fail-open）。
func (s *GateService) route(ctx context.Context, tenantID string, target domain.GateTarget,
	action domain.GateAction, ev domain.GateEvidence,
) {
	rec := domain.GateActionRecord{
		Scope:    target.Scope,
		Target:   target,
		Layer:    gateLayer,
		Decision: action,
		Action:   actionLabel(action),
		Evidence: evidencePayload(ev),
		Actor:    "gate",
	}
	s.inc(action)
	switch action {
	case domain.GateL2Escalate:
		// 裁决 R10：l2 只记台账 + 告警日志，不重复评审池入池（上游 escalateToReview 已处理）。
		s.warn("gate l2 escalate", zap.String("target", target.Key()),
			zap.Int("rule_blocks", ev.RuleBlockCount), zap.Int("anomalies", ev.AnomalyCount),
			zap.Int("judge_flags", ev.JudgeFlagCount))
	case domain.GateRollbackManual, domain.GateRollbackAuto:
		s.applyRollbackRecommendation(ctx, tenantID, target, action, &rec)
	}
	s.appendRecord(ctx, tenantID, rec)
}

// applyRollbackRecommendation 裁决 R11：平台版本写 eval_state=rollback_recommended；
// 资源 auto 且装配执行器才真正回滚（P1 不装配）；manual 走审批（Approvals，nil 跳过）。
func (s *GateService) applyRollbackRecommendation(ctx context.Context, tenantID string,
	target domain.GateTarget, action domain.GateAction, rec *domain.GateActionRecord,
) {
	if target.Scope == domain.ScopePlatform {
		s.writePlatformEvalState(ctx, target)
		return
	}
	s.applyResourceRollback(ctx, tenantID, target, action, rec)
}

// writePlatformEvalState 平台版本写回 eval_state=rollback_recommended（裁决 R11）。
func (s *GateService) writePlatformEvalState(ctx context.Context, target domain.GateTarget) {
	if s.deps.Platform == nil {
		return
	}
	if err := s.deps.Platform.UpdateEvalState(ctx, target.GroupKey, target.VersionSeq,
		"rollback_recommended", "gate"); err != nil {
		s.warn("gate platform eval_state writeback failed", zap.Error(err), zap.String("target", target.Key()))
	}
}

// applyResourceRollback 资源目标的回滚动作：auto 且装配执行器才真正回滚
// （P1 不装配）；manual 走人工审批（Approvals nil 跳过，台账已记录决策）。
func (s *GateService) applyResourceRollback(ctx context.Context, tenantID string,
	target domain.GateTarget, action domain.GateAction, rec *domain.GateActionRecord,
) {
	if action == domain.GateRollbackAuto {
		s.execAutoRollback(ctx, tenantID, target)
		return
	}
	if action == domain.GateRollbackManual {
		s.requestApproval(ctx, tenantID, target, rec)
	}
}

// execAutoRollback 执行资源自动回滚（执行器未装配时跳过，裁决 R9）。
func (s *GateService) execAutoRollback(ctx context.Context, tenantID string, target domain.GateTarget) {
	if s.deps.ResourceRollback == nil {
		return
	}
	// auto 路径无审批人：actor 记为 gate（与台账 rec.Actor 同值，见 route()），
	// decidedBy/approvalID 空串由 executor 语义消费。
	if err := s.deps.ResourceRollback.Rollback(ctx, tenantID, target, "gate", "", ""); err != nil {
		s.warn("gate resource auto rollback failed", zap.Error(err), zap.String("target", target.Key()))
	}
}

// requestApproval 为 rollback_manual 请求人工审批；失败仅日志（台账已记录决策）。
func (s *GateService) requestApproval(ctx context.Context, tenantID string, target domain.GateTarget,
	rec *domain.GateActionRecord,
) {
	if s.deps.Approvals == nil {
		return
	}
	approvalID, err := s.deps.Approvals.RequestRollbackApproval(ctx, tenantID, *rec)
	if err != nil {
		s.warn("gate rollback approval request failed", zap.Error(err), zap.String("target", target.Key()))
		return
	}
	rec.ApprovalID = approvalID
}

// appendRecord 追加台账（Repo nil / 失败 → 仅日志，fail-open）。
func (s *GateService) appendRecord(ctx context.Context, tenantID string, rec domain.GateActionRecord) {
	if s.deps.Repo == nil {
		return
	}
	if err := s.deps.Repo.AppendAction(ctx, tenantID, rec); err != nil {
		s.warn("gate append action failed", zap.Error(err), zap.String("target", rec.Target.Key()))
	}
}

// inc 发门禁决策指标（Metrics nil-safe；layer=observation，action=决策文本）。
func (s *GateService) inc(action domain.GateAction) {
	if s.deps.Metrics != nil {
		s.deps.Metrics.IncEvalGateAction(gateLayer, string(action))
	}
}

// warn 记录非阻断问题（logger nil 时静默，保持 fail-open 不 panic）。
func (s *GateService) warn(msg string, fields ...zap.Field) {
	if s.deps.Logger != nil {
		s.deps.Logger.Warn(msg, fields...)
	}
}

// cooldownActive 判定目标是否处于决策冷却期（自最近非 none 决策起 GateCooldown 内）。
func (s *GateService) cooldownActive(target domain.GateTarget) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lastAt, ok := s.last[target.Key()]
	if !ok {
		return false
	}
	return s.now().Before(lastAt.Add(constants.GateCooldown))
}

// markTriggered 记录一次非 none 决策时间（冷却起点）。
func (s *GateService) markTriggered(target domain.GateTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[target.Key()] = s.now().UTC()
}

// actionLabel 返回台账 action 列文本（决策的动作形态，裁决 R11）。
func actionLabel(a domain.GateAction) string {
	switch a {
	case domain.GateRollbackAuto, domain.GateRollbackManual:
		return "rollback_recommended"
	case domain.GateL2Escalate:
		return "escalate"
	}
	return ""
}

// evidencePayload 组装证据 JSONB（窗口计数 + 人工/对照判定摘要）。
func evidencePayload(ev domain.GateEvidence) map[string]any {
	payload := map[string]any{
		"rule_blocks":    ev.RuleBlockCount,
		"anomalies":      ev.AnomalyCount,
		"judge_flags":    ev.JudgeFlagCount,
		"review_verdict": string(ev.ReviewVerdict),
	}
	if ev.ConfirmationRun != nil {
		payload["confirmation_regressed"] = ev.ConfirmationRun.Regressed
	}
	return payload
}
