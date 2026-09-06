package application

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 运行态观测 judge rubric 维度（规格 §3.1）：三个语义质量维度各一次 Judge 调用。
var observationJudgeDimensions = []string{"faithfulness", "relevance", "completeness"}

// ObservationServiceDeps 是落地服务的依赖（缺失字段由 wiring 保证；任何依赖 nil 时
// Process 按 fail closed 处理——唯 TenantTier 明确 nil-tolerant：未装配或 tier 解析
// 失败时 stratum 留空，不阻断落库）。
type ObservationServiceDeps struct {
	Enabled    func(ctx context.Context) bool    // 平台参数 evaluation.observe.enabled
	SampleRate func(ctx context.Context) float64 // 平台参数 evaluation.observe.sample_rate
	Evidence   port.TraceEvidenceReader
	Judge      port.LLMJudge
	Repo       port.ObservationRepository
	Metrics    observability.MetricsProvider
	Logger     *zap.Logger
	TenantTier port.TenantTierReader // P1b：租户 tier → stratum（nil 时 stratum 留空）
	// PlatformVersion 解析平台配置组当前生效版本序号（Phase 2 §4.3 版本锚点）；
	// nil 或解析失败时 fail-open：版本锚点标记 unknown（seq 0），不阻断落库。
	PlatformVersion func(ctx context.Context) (int64, bool, error)
	// Review 是评审池升级入口（P1c §6.6）；nil 时评审升级静默跳过（fail-open）。
	Review port.ReviewEscalator
	// Gate 是分层门禁入口（spec §2.5）；nil 时门禁评估静默跳过（fail-open）。
	Gate port.GateSink
}

type ObservationService struct {
	deps ObservationServiceDeps

	mu      sync.Mutex
	arrival map[string]int64 // resource -> 采样候选到达数（采样通过且 judge 开启）
	sampled map[string]int64 // resource -> 采样通过且落库数
}

func NewObservationService(deps ObservationServiceDeps) *ObservationService {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	return &ObservationService{
		deps:    deps,
		arrival: make(map[string]int64),
		sampled: make(map[string]int64),
	}
}

// 编译期断言：ObservationService 满足 port.BehaviorSignalWriter。
var _ port.BehaviorSignalWriter = (*ObservationService)(nil)

// Process 处理一条观测引用事件：开启 → 采样 → judge 可用性 → 拉证据 → judge 多维打分 → 落库。
// 返回 error 表示需要 NATS 重投（仅证据查询失败）；judge 关闭 / judge 故障 / 校验非法 /
// 落库失败均在本服务内丢弃并返回 nil（§14 精神：不落零信号 pass 观测、不制造 poison
// message 重投循环），绝不伪成功。
// 采样覆盖率（eval_sample_coverage）= 落库观测 / 采样候选（采样通过且 judge 开启）。
// 分母在 judge 关闭检查之后计数：健康稳态 ratio≈1.0，judge 配置关闭（主动停观测）不计入。
func (s *ObservationService) Process(ctx context.Context, evt domain.ObservationReferenceEvent) error {
	if !s.deps.Enabled(ctx) {
		return nil
	}
	if !sampleDecision(s.deps.SampleRate(ctx), evt.ResourceKind, evt.TraceID) {
		return nil
	}
	// judge 关闭（nil 或 !Enabled）时跳过本次观测：观测的产出是 judge 信号，
	// 无 judge 不落零信号 pass 观测（§14 精神）。配置态跳过，非故障降级、不计数。
	if s.deps.Judge == nil || !s.deps.Judge.Enabled(ctx) {
		return nil
	}
	// 到达计数在 judge 关闭检查之后：分母 = 采样候选。judge 配置关闭不计入分母、
	// 不因此告警（主动停观测）；judge 故障降级 / 校验失败 / 落库失败计入分母但
	// 不计入分子 → 覆盖率掉低 → 触发 StratumEvalSampleCoverageLow。
	s.recordArrival(evt.ResourceKind)
	trace, err := s.deps.Evidence.Resolve(ctx, evt.TenantID, evt.TraceID)
	if err != nil {
		s.deps.Metrics.IncEvalJudgeFailure("evidence_resolve")
		return fmt.Errorf("observation resolve evidence: %w", err)
	}
	obs := s.buildObservation(ctx, evt, trace)
	// 判异识别必须在 applyJudge 之后运行：judge 信号由 applyJudge 填充，
	// 先判异会导致 judge 分支永远空判（judge_below_threshold 指标死代码），
	// 且 rule-block 会被 applyJudge 无条件降级为 flag。此顺序保证 block > flag > pass。
	if err := s.applyJudge(ctx, trace, &obs); err != nil {
		// judge 不可用：§14 采样降级跳过——不落零信号的伪 pass 观察、不重投
		// （避免 judge 持续不可用时的重投空转），仅指标计数 + warn 日志。
		s.deps.Logger.Warn("observation judge degraded, skip", zap.Error(err),
			zap.String("trace_id", evt.TraceID))
		s.deps.Metrics.IncEvalJudgeFailure("judge_unavailable")
		return nil
	}
	s.applyAnomalyVerdict(string(obs.Resource.Kind), &obs)
	if err := obs.Validate(); err != nil {
		// 数据非法：重投必再失败（poison message 循环），丢弃而非重投。
		s.deps.Logger.Warn("observation invalid, drop", zap.Error(err),
			zap.String("trace_id", evt.TraceID))
		s.deps.Metrics.IncEvalJudgeFailure("invalid_observation")
		return nil
	}
	if err := s.deps.Repo.Save(ctx, evt.TenantID, &obs); err != nil {
		// 重投会因每次 buildObservation 新 uuid 重复落库，丢弃而非重投。
		s.deps.Logger.Warn("observation save failed, drop", zap.Error(err),
			zap.String("trace_id", evt.TraceID))
		s.deps.Metrics.IncEvalJudgeFailure("save_failed")
		return nil
	}
	s.recordSampled(evt.ResourceKind)
	s.deps.Metrics.IncEvalObservation(evt.ResourceKind, obs.Stratum)
	// 分层门禁内联评估（spec §2.5）：落库成功后评估窗口证据并路由决策（fail-open）。
	s.handleGateObservation(ctx, evt, &obs)
	// 评审池内联触发（P1c §6.6）：落库成功后按触发规则入池（fail-open，见 escalateToReview）。
	s.escalateToReview(ctx, evt, &obs)
	return nil
}

// escalateToReview 评审池内联触发（P1c §6.6）：落库成功后按触发规则入池。
// fail-open——升级失败仅日志 + 指标，不阻断观测主流程、不改 verdict。
func (s *ObservationService) escalateToReview(
	ctx context.Context, evt domain.ObservationReferenceEvent, obs *domain.EvalObservation,
) {
	if s.deps.Review == nil {
		return
	}
	if err := s.deps.Review.TryEscalateObservation(ctx, evt.TenantID, obs); err != nil {
		s.deps.Logger.Warn("observation review escalation failed", zap.Error(err),
			zap.String("trace_id", evt.TraceID))
		s.deps.Metrics.IncEvalReviewEscalateFailure()
	}
}

// recordArrival 累计某资源的采样候选到达并刷新采样覆盖率（分母）。
// 采样候选 = 采样通过且 judge 开启；judge 配置关闭不计入（主动停观测，非降级）。
// 锁内只做读改写和 flush，锁外不做 map 访问（并发安全）。
func (s *ObservationService) recordArrival(resource string) {
	s.mu.Lock()
	s.arrival[resource]++
	s.flushCoverageLocked(resource)
	s.mu.Unlock()
}

// recordSampled 累计某资源的采样通过数并刷新覆盖率（分子）。
func (s *ObservationService) recordSampled(resource string) {
	s.mu.Lock()
	s.sampled[resource]++
	s.flushCoverageLocked(resource)
	s.mu.Unlock()
}

// flushCoverageLocked 按当前累计值写 eval_sample_coverage Gauge（ratio = 落库/采样候选）。
func (s *ObservationService) flushCoverageLocked(resource string) {
	arrival := s.arrival[resource]
	if arrival == 0 {
		return
	}
	ratio := float64(s.sampled[resource]) / float64(arrival)
	s.deps.Metrics.RecordEvalSampleCoverage(resource, ratio)
}

// buildObservation 组装 EvalObservation（不含 judge 信号，由 applyJudge 填充）。
func (s *ObservationService) buildObservation(ctx context.Context, evt domain.ObservationReferenceEvent,
	trace port.ObservedTrace,
) domain.EvalObservation {
	resourceVersion := domain.ResourceParamVersion{Ref: "", Version: ""}
	source := domain.ParamSourceUnknown
	for _, a := range trace.Assignments {
		if a.RevisionID != "" {
			resourceVersion = domain.ResourceParamVersion{Ref: a.RevisionID, Version: a.Variant}
			source = domain.ParamSourceResource
		}
	}
	// Phase 2 §4.3：平台层版本锚点绑定 evaluation 配置组当前生效版本序号；
	// 解析失败 fail-open 标记 unknown（seq 0），不阻断落库（同 resolveStratum）。
	obs := domain.EvalObservation{
		ID:       uuid.NewString(),
		TraceID:  evt.TraceID,
		Resource: evt.ResourceRef(),
		Param: domain.ParamVersion{
			Platform: s.resolvePlatformVersion(ctx),
			Resource: resourceVersion,
			Source:   source,
		},
		CostPerf: domain.CostPerf{
			LatencyMS: trace.LatencyMs,
			Tokens:    trace.TotalTokens,
			CostUSD:   trace.CostUSD,
		},
		Stratum:   s.resolveStratum(ctx, evt.TenantID),
		Verdict:   domain.VerdictPass,
		CreatedAt: time.Now().UTC(),
	}
	// P1b：事件携带的规则命中 / 行为信号写入观测（§4.2 判异触发信号来源）。
	obs.Signals.Rule = ruleSignalsFromEvent(evt)
	obs.Signals.Behavior = behaviorFromEvent(evt)
	return obs
}

// ruleSignalsFromEvent 事件携带的规则命中信号转观测信号。
func ruleSignalsFromEvent(evt domain.ObservationReferenceEvent) []domain.RuleSignal {
	out := make([]domain.RuleSignal, 0, len(evt.RuleSignals))
	for _, r := range evt.RuleSignals {
		// RuleSignal 与 RuleSignalPayload 字段与 tag 完全同构，直接转换（staticcheck S1016）。
		out = append(out, domain.RuleSignal(r))
	}
	return out
}

// behaviorFromEvent 事件携带的行为信号转观测信号；事件未携带行为段（nil）时
// 返回全 false，绝不 panic（§14 不阻断执行）。
func behaviorFromEvent(evt domain.ObservationReferenceEvent) domain.BehaviorSignals {
	if evt.Behavior == nil {
		return domain.BehaviorSignals{}
	}
	return domain.BehaviorSignals{
		Retry:       evt.Behavior.Retry,
		Escalation:  evt.Behavior.Escalation,
		Abandonment: evt.Behavior.Abandonment,
	}
}

// resolveStratum 解析租户 tier 为 stratum（§4.3）。tier 解析失败不阻断落库：
// stratum 留空 + warn（§14 参数版本锚点缺失标记 unknown 的等价语义）。
func (s *ObservationService) resolveStratum(ctx context.Context, tenantID string) string {
	if s.deps.TenantTier == nil {
		return ""
	}
	tier, err := s.deps.TenantTier.GetTenantTier(ctx, tenantID)
	if err != nil {
		s.deps.Logger.Warn("observation tier resolve failed", zap.Error(err),
			zap.String("tenant_id", tenantID))
		return ""
	}
	return tier
}

// resolvePlatformVersion 解析平台配置组当前生效版本序号（Phase 2 §4.3 版本锚点）。
// fail-open：读取器 nil / 读取失败 / 无已发布版本时标记 unknown（seq 0）+ warn，
// 不阻断落库——与 resolveStratum 的 unknown 语义等价（§14 缺失锚点标记 unknown）。
func (s *ObservationService) resolvePlatformVersion(ctx context.Context) domain.PlatformParamVersion {
	if s.deps.PlatformVersion == nil {
		return domain.PlatformParamVersion{}
	}
	seq, ok, err := s.deps.PlatformVersion(ctx)
	if err != nil {
		s.deps.Logger.Warn("observation platform version resolve failed", zap.Error(err))
		return domain.PlatformParamVersion{}
	}
	if !ok {
		return domain.PlatformParamVersion{}
	}
	return domain.PlatformParamVersion{GroupKey: constants.PlatformGroupEvaluation, VersionSeq: seq}
}

// applyJudge 按三维度 rubric 调用 judge 并填充 signals；任一次失败返回错误
// （上层降级），已完成维度不回滚（保留部分信号）。judge 关闭时跳过。
// JudgeRequest 的预期输出当前留空：运行态观测无 golden（评测集才有 ExpectedOutput）。
func (s *ObservationService) applyJudge(ctx context.Context, trace port.ObservedTrace, obs *domain.EvalObservation) error {
	if s.deps.Judge == nil || !s.deps.Judge.Enabled(ctx) {
		return nil
	}
	start := time.Now()
	for _, dimension := range observationJudgeDimensions {
		res, err := s.deps.Judge.Judge(ctx, port.JudgeRequest{
			Model:          "",
			Rubric:         judgeRubric(dimension),
			Input:          trace.Input,
			ExpectedOutput: "",
			Actual:         trace.Output,
		})
		if err != nil {
			return fmt.Errorf("judge dimension %s: %w", dimension, err)
		}
		// LLMJudge 契约返回 domain.AssertionResult{Passed, Message, Confidence}：
		// 维度通过映射为 1.0/0.0，Confidence/Reason 用 judge 真实输出（P1c §6.2）。
		score := 0.0
		if res.Passed {
			score = 1.0
		}
		obs.Signals.Judge = append(obs.Signals.Judge, domain.JudgeSignal{
			Dimension:  dimension,
			Score:      score,
			Confidence: res.Confidence,
			Reason:     res.Message,
		})
		s.deps.Metrics.RecordEvalJudgeScore(string(obs.Resource.Kind), dimension, score)
	}
	seconds := time.Since(start).Seconds()
	s.deps.Metrics.RecordEvalJudgeLatency(seconds)
	s.deps.Metrics.RecordEvalJudgeCost(trace.CostUSD)
	// 任一维度低于阈值视为 flag（仅信号级，非门禁判定）。
	if anyJudgeBelow(obs.Signals.Judge, constants.JudgeBelowThreshold) {
		obs.Verdict = domain.VerdictFlag
	}
	return nil
}

// applyAnomalyVerdict 判异识别（§4.2 判异触发）：规则命中 → block；行为异常或
// judge 跌阈 → flag；否则 pass。仅信号级结论，非权威判定（§4.3）。
// 不返回 error、不 panic——判异失败不得阻断 Process（§14 精神）。
func (s *ObservationService) applyAnomalyVerdict(resource string, obs *domain.EvalObservation) {
	if len(obs.Signals.Rule) > 0 {
		obs.Verdict = domain.VerdictBlock
		s.deps.Metrics.IncEvalBehaviorAnomaly(resource, "rule_block")
		s.deps.Metrics.IncEvalGateAction("detect", string(domain.VerdictBlock))
	}
	// block 优先级最高（T4 红线）：规则命中置 block 后，`verdict != block` 守卫
	// 抑制行为/judge 判异分支——不降级 verdict、不计入对应信号维度。
	// 非 block 结论（pass/flag）下，各 signal 维度独立计数
	// （§11.1 eval_behavior_anomaly_total{resource, signal}）。
	if obs.Verdict != domain.VerdictBlock && obs.Signals.Behavior.Abandonment {
		obs.Verdict = domain.VerdictFlag
		s.deps.Metrics.IncEvalBehaviorAnomaly(resource, "behavior_abandonment")
		s.deps.Metrics.IncEvalGateAction("detect", string(domain.VerdictFlag))
	}
	if obs.Verdict != domain.VerdictBlock && obs.Signals.Behavior.Escalation {
		obs.Verdict = domain.VerdictFlag
		s.deps.Metrics.IncEvalBehaviorAnomaly(resource, "behavior_escalation")
		s.deps.Metrics.IncEvalGateAction("detect", string(domain.VerdictFlag))
	}
	if obs.Verdict != domain.VerdictBlock && anyJudgeBelow(obs.Signals.Judge, constants.JudgeBelowThreshold) {
		obs.Verdict = domain.VerdictFlag
		s.deps.Metrics.IncEvalBehaviorAnomaly(resource, "judge_below_threshold")
		s.deps.Metrics.IncEvalGateAction("detect", string(domain.VerdictFlag))
	}
}

// judgeRubric 构造单维度 judge 提示词（与 judgeAdapter 的 Complete 输出契约
// {"passed","reason","confidence"} 对齐：指示 LLM 按指定维度判定 pass/不通过、
// 给理由并给出 0-1 置信度，与评测系统主 rubric 逐字对齐）。
func judgeRubric(dimension string) string {
	return fmt.Sprintf("请按维度「%s」对助手回答判定通过/不通过，给出理由与 0-1 置信度。忠实于给定上下文、切题、覆盖全部关键点。"+
		"只输出 JSON：{\"passed\": true 或 false, \"reason\": \"一句话理由\", \"confidence\": 0-1 之间的小数表示判定置信度}", dimension)
}

func anyJudgeBelow(signals []domain.JudgeSignal, threshold float64) bool {
	for _, s := range signals {
		if s.Score < threshold {
			return true
		}
	}
	return false
}

// ListObservations 分页查询观测明细（查询 API 数据源）。
func (s *ObservationService) ListObservations(ctx context.Context, tenantID, resourceKind, resourceID string,
	from, to *time.Time, limit, offset int,
) ([]domain.EvalObservation, error) {
	if s.deps.Repo == nil {
		return nil, fmt.Errorf("observation service: repository unavailable")
	}
	return s.deps.Repo.QueryByResource(ctx, tenantID, resourceKind, resourceID, from, to, limit, offset)
}

// GetObservation 取单条观测明细。
func (s *ObservationService) GetObservation(ctx context.Context, tenantID, id string) (*domain.EvalObservation, error) {
	if s.deps.Repo == nil {
		return nil, fmt.Errorf("observation service: repository unavailable")
	}
	return s.deps.Repo.Get(ctx, tenantID, id)
}

// ApplyBehaviorSignals 把行为信号合并到该 trace 最近一条观测（§4.2）。找不到观测
// 时静默返回 nil（采样未覆盖该 trace，反馈不补造观测）；更新失败返回错误（调用方
// 忽略，best-effort）。
func (s *ObservationService) ApplyBehaviorSignals(
	ctx context.Context, tenantID, traceID string, signals domain.BehaviorSignals,
) error {
	if s.deps.Repo == nil {
		return nil
	}
	obs, err := s.deps.Repo.FindLatestByTrace(ctx, tenantID, traceID)
	if err != nil {
		return err
	}
	if obs == nil {
		return nil
	}
	merged := obs.Signals.Behavior
	merged.Retry = merged.Retry || signals.Retry
	merged.Escalation = merged.Escalation || signals.Escalation
	merged.Abandonment = merged.Abandonment || signals.Abandonment
	if merged == obs.Signals.Behavior {
		return nil
	}
	return s.deps.Repo.UpdateBehaviorSignals(ctx, tenantID, obs.ID, merged)
}

// handleGateObservation 门禁内联评估（spec §2.5）：落库成功后由分层门禁评估观察
// 窗口并路由决策。fail-open——未装配（Gate nil）或门禁内部失败只日志，不阻断
// 观测主流程、不改 verdict（与 escalateToReview 同哲学）。
func (s *ObservationService) handleGateObservation(
	ctx context.Context, evt domain.ObservationReferenceEvent, obs *domain.EvalObservation,
) {
	if s.deps.Gate == nil {
		return
	}
	if err := s.deps.Gate.HandleObservation(ctx, evt.TenantID, *obs); err != nil {
		s.deps.Logger.Warn("observation gate evaluation failed", zap.Error(err),
			zap.String("trace_id", evt.TraceID))
	}
}
