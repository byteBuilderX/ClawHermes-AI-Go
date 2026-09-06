package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

// SentinelTarget 是哨兵 suite 的目标（resource + suite revision），由
// SentinelSpec 在宿主租户维度解析；P2 接线前恒 nil → 拒发（T13 接真实 suite 源）。
type SentinelTarget struct {
	Resource        domain.ResourceRef
	SuiteRevisionID string
}

// SentinelVerdict 哨兵判定结论（run 级回归 pass/block，§3.2-②同判据）。
type SentinelVerdict string

const (
	SentinelVerdictPass  SentinelVerdict = "pass"
	SentinelVerdictBlock SentinelVerdict = "block"
)

type SentinelDecision struct {
	Verdict      SentinelVerdict    // pass | block
	BaselineSeq  int64              // 生产基线 run 锚 seq（无基线 0）
	ConfirmedSeq int64              // 哨兵 run 锚 seq（= 草案 seq）
	Deltas       map[string]float64 // 维度 delta（哨兵 vs 基线；基线 nil 空）
}

type PublishGateRequest struct {
	GroupKey  string
	VersionID int64 // 行 PK id（HTTP path 语义，E5）
	Actor     string
}

type PublishGateDecision int

const (
	DecisionPassThrough     PublishGateDecision = iota // gate 关闭 → 调用方直通裸发布（默认）
	DecisionApprovalPending                            // 哨兵通过 → 待人工审批（T13 完成）
	DecisionBlocked                                    // 哨兵 block/失败 → 拒发（eval_state=sentinel_failed）
	DecisionRefusedNotWired                            // 哨兵 suite 解析源未接线（T13）→ fail-closed 拒发
)

type PublishGateResult struct {
	Decision PublishGateDecision
	Message  string
	RunID    string // 哨兵 run（DecisionBlocked/ApprovalPending 时）
}

type PublishGateDeps struct {
	Logger  *zap.Logger
	Metrics observability.MetricsProvider // nil-safe
	// GateEnabled 实时读 evaluation.gate.enabled（nil → false）。默认 false → 直通。
	GateEnabled func(ctx context.Context) bool
	// UpdateEvalState 平台版本写回（wiring → parameters Service.UpdateEvalState，E5）。
	UpdateEvalState func(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error
	// ResolveVersion id→seq 桥（wiring → parameters Service.Versions 匹配 ID，E5）。
	ResolveVersion func(ctx context.Context, groupKey string, versionID int64) (seq int, status string, isCurrent bool, ok bool, err error)
	// SentinelSpec 解析哨兵目标（resource+suite，宿主租户维度；P2 wiring 恒 nil → 拒发，T13 接真实 suite 源）。
	SentinelSpec func(ctx context.Context, hostTenantID, groupKey string, draftSeq int) (SentinelTarget, error)
	// EnqueueSentinel 入队哨兵 run（wiring 绑 application.JobService.EnqueueRun，入参携带 PlatformSeqOverrides{groupKey:draftSeq}）。
	EnqueueSentinel func(ctx context.Context, tenantID string, in EnqueueRunInput) (string, error)
	// BaselineRun 返回该哨兵目标的最近 completed 基线 run（wiring 包 Task 1 FindLatestCompletedRunForResource，
	// 排除哨兵自身 runID；nil,nil = 无基线 → 无回归信号，判定 pass）。
	BaselineRun func(ctx context.Context, tenantID string, target SentinelTarget, excludeRunID string) (*domain.EvalRun, error)
	// Compare run 级回归（wiring 经 runCompareAdapter 绑 Task 1 CompareRunRegression；
	// nil → DecideSentinel 返回错误 fail-closed）。
	Compare func(baseline, current *domain.EvalRun) (domain.RunComparison, error)
}

type PublishGateCoordinator struct {
	deps PublishGateDeps
}

func NewPublishGateCoordinator(deps PublishGateDeps) *PublishGateCoordinator {
	return &PublishGateCoordinator{deps: deps}
}

// gateEnabled 判总开关：关闭（默认）→ 直通，不改动任何现状。
func (c *PublishGateCoordinator) gateEnabled(ctx context.Context) bool {
	return c.deps.GateEnabled != nil && c.deps.GateEnabled(ctx)
}

// GatePublish 编排入口。任何无法证明「走哨兵且通过」的路径一律拒发（fail-closed），
// 绝不静默直发（O5/§3.4）。
func (c *PublishGateCoordinator) GatePublish(ctx context.Context, hostTenantID string, req PublishGateRequest) (PublishGateResult, error) {
	if !c.gateEnabled(ctx) {
		return PublishGateResult{Decision: DecisionPassThrough}, nil
	}
	seq, status, _, ok, err := c.deps.ResolveVersion(ctx, req.GroupKey, req.VersionID)
	if err != nil {
		// infra/查询错误 → 编排层透传 error → handler 统一 500；不直发（fail-closed）。
		return PublishGateResult{Decision: DecisionBlocked, Message: "解析版本失败：" + err.Error()}, err
	}
	if !ok {
		return PublishGateResult{Decision: DecisionBlocked, Message: fmt.Sprintf("版本不存在：version_id=%d", req.VersionID)}, nil
	}
	if status != "draft" {
		return PublishGateResult{Decision: DecisionBlocked, Message: fmt.Sprintf("仅 draft 可发布哨兵，当前 status=%s", status)}, nil
	}
	if c.deps.SentinelSpec == nil {
		// T13 接线哨兵 suite 源前，enabled=true 一律 fail-closed 拒发（无静默直发、无假通过）。
		return PublishGateResult{Decision: DecisionRefusedNotWired,
			Message: "发布哨兵 suite 解析源未接线（SentinelSpec nil）：gate.enabled=true 下 P2 拒绝发布，待 T13 接入哨兵 suite + 人工审批环"}, nil
	}
	spec, err := c.deps.SentinelSpec(ctx, hostTenantID, req.GroupKey, seq)
	if err != nil {
		return PublishGateResult{Decision: DecisionBlocked, Message: "哨兵目标解析失败：" + err.Error()}, nil
	}
	runID, err := c.RunSentinelForDraft(ctx, hostTenantID, req, seq, spec)
	if err != nil {
		return PublishGateResult{Decision: DecisionBlocked, Message: "哨兵 run 入队失败：" + err.Error()}, nil
	}
	// O5 阻断式：哨兵 run 为异步执行；run 完成 → T13 完成「DecideSentinel → 人工审批 →
	// store.Publish」。P2 返回待审批/待接线态，不直发。
	return PublishGateResult{Decision: DecisionApprovalPending, RunID: runID,
		Message: "哨兵 run 已入队，待完成判定与人工审批（T13 完成发布环）"}, nil
}

// RunSentinelForDraft 对草案 seq 入队哨兵 run（CaptureInput.PlatformSeqOverrides，E6）。
func (c *PublishGateCoordinator) RunSentinelForDraft(ctx context.Context, hostTenantID string, req PublishGateRequest, draftSeq int, spec SentinelTarget) (string, error) {
	if c.deps.EnqueueSentinel == nil {
		return "", errors.New("EnqueueSentinel 未注入")
	}
	return c.deps.EnqueueSentinel(ctx, hostTenantID, EnqueueRunInput{
		Resource:             spec.Resource,
		SuiteRevisionID:      spec.SuiteRevisionID,
		IdempotencyKey:       fmt.Sprintf("sentinel:%s:%d", req.GroupKey, req.VersionID),
		RequestedBy:          req.Actor,
		PlatformSeqOverrides: map[string]int64{req.GroupKey: int64(draftSeq)},
	})
}

// DecideSentinel 哨兵 run 完成后的判定：仅消费两次 run 与 Compare，无 IO（可单测）。
// sentinel nil = 哨兵 run 未完成/未找到 → block（fail-closed，无法证明安全）。
func (c *PublishGateCoordinator) DecideSentinel(ctx context.Context, hostTenantID, groupKey string, baseline, sentinel *domain.EvalRun) (SentinelDecision, error) {
	decision := SentinelDecision{Verdict: SentinelVerdictPass, Deltas: map[string]float64{}}
	if sentinel == nil {
		decision.Verdict = SentinelVerdictBlock
		c.emitGate(ctx, domain.LayerL3Sentinel, domain.ActionBlock)
		c.emitGate(ctx, domain.LayerL2, domain.ActionRegression)
		return decision, nil
	}
	decision.ConfirmedSeq = sentinelSeq(groupKey, sentinel)
	if baseline != nil {
		decision.BaselineSeq = sentinelSeq(groupKey, baseline)
		if c.deps.Compare == nil {
			return decision, errors.New("Compare 未注入：无法完成哨兵回归判定（fail-closed，不直发）")
		}
		comparison, err := c.deps.Compare(baseline, sentinel)
		if err != nil {
			return decision, fmt.Errorf("哨兵回归对照失败: %w", err) // 编排层透传 → handler 500，不直发
		}
		decision.Deltas = comparison.DimensionDeltas
		if comparison.Regressed {
			decision.Verdict = SentinelVerdictBlock
			c.emitGate(ctx, domain.LayerL3Sentinel, domain.ActionBlock)
			c.emitGate(ctx, domain.LayerL2, domain.ActionRegression)
			_ = c.updateEvalState(ctx, groupKey, decision.ConfirmedSeq, domain.EvalStateSentinelFailed, hostTenantID)
			return decision, nil
		}
	}
	// pass：记录通过 + 门计数 + eval_state=sentinel_passed（Task 4 executePlatformPublishGated 依赖此前置）。
	decision.Verdict = SentinelVerdictPass
	c.emitGate(ctx, domain.LayerL3Sentinel, domain.ActionPass)
	c.emitGate(ctx, constants.GateLayerL3Platform, domain.ActionPublishGated)
	_ = c.updateEvalState(ctx, groupKey, decision.ConfirmedSeq, constants.PlatformEvalStateSentinelPassed, hostTenantID)
	return decision, nil
}

// emitGate 记门禁动作计数；metrics 未装配（nil）时静默跳过。
func (c *PublishGateCoordinator) emitGate(ctx context.Context, layer, action string) {
	if c.deps.Metrics != nil {
		c.deps.Metrics.IncEvalGateAction(layer, action)
	}
}

// updateEvalState 平台版本 eval_state 写回；deps 未装配（nil）时静默跳过。
func (c *PublishGateCoordinator) updateEvalState(ctx context.Context, groupKey string, seq int64, state, actor string) error {
	if c.deps.UpdateEvalState == nil {
		return nil
	}
	return c.deps.UpdateEvalState(ctx, groupKey, seq, state, actor)
}

// sentinelSeq 从 run.ContextSnapshot 取 groupKey 组的 version_seq：优先 Evaluation 组，
// 再遍历 Execution 组；nil 快照/未命中回退 0（历史 run 无锚点信号）。
func sentinelSeq(groupKey string, run *domain.EvalRun) int64 {
	if run == nil || run.ContextSnapshot == nil {
		return 0
	}
	if run.ContextSnapshot.Evaluation.GroupKey == groupKey {
		return run.ContextSnapshot.Evaluation.VersionSeq
	}
	for _, g := range run.ContextSnapshot.Execution {
		if g.GroupKey == groupKey {
			return g.VersionSeq
		}
	}
	return 0
}
