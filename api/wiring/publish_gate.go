package wiring

import (
	"context"
	"fmt"

	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	"github.com/byteBuilderX/stratum/pkg/reqctx"
)

// runCompareAdapter 把 Task 1 纯函数（*RunComparison，永不为 nil）适配为 deps 的
// (RunComparison, error) 签名。B4（哨兵）与 C5（验证 worker）复用，见「任务依赖与次序」跨任务绑定 A。
func runCompareAdapter(baseline, current *evaldomain.EvalRun) (evaldomain.RunComparison, error) {
	return *evalapp.CompareRunRegression(baseline, current), nil
}

// PublishGateFunc 与 handler.PublishGateFunc 形状一致（wiring 不 import handler——
// router 层是唯一允许适配点，见 dlqReplayAdapter 注释）。decision ∈
// {"passthrough","approval_pending","blocked","refused_not_wired"}。
type PublishGateFunc func(ctx context.Context, groupKey string, versionID int64, actor string) (decision, message, runID string, err error)

// buildPublishGate 在 evaluation 装配后构建发布闸协调器并挂到 Container（独立 build
// step：buildEvaluation 长度已在质量门禁基线内，不再向函数体增行）。
func (c *Container) buildPublishGate(ctx context.Context) error {
	c.PublishGate = c.newPublishGateCoordinator()
	return nil
}

// newPublishGateCoordinator 组装发布闸协调器真实 deps（E5 桥接）；parameters 服务缺失
// → nil（handler 保持未装配裸发布语义）。SentinelSpec/EnqueueSentinel/BaselineRun 在 P2
// 未接线（nil）——gate.enabled=true 时编排器经 SentinelSpec nil → DecisionRefusedNotWired
// 兜底拒发（A7.6：P2 不得开启该开关，T13 全链路 wiring 前拒绝发布）。
func (c *Container) newPublishGateCoordinator() PublishGateFunc {
	params := c.Parameters
	if params == nil || params.Service == nil {
		return nil
	}
	coordinator := evalapp.NewPublishGateCoordinator(evalapp.PublishGateDeps{
		Logger:          c.Logger,
		Metrics:         c.platformMetrics(),
		GateEnabled:     gateEnabledPlatform(params.Service),
		UpdateEvalState: params.Service.UpdateEvalState,
		ResolveVersion:  resolvePlatformVersion(params.Service),
		Compare:         runCompareAdapter,
	})
	return gatePublishCoordinatorAdapter(coordinator)
}

// gateEnabledPlatform 读平台参数 evaluation.gate.enabled（请求 ctx 无评测快照，直读
// 当前 production 值）；默认 false（参数服务不可用时禁用门禁 → 直通，行为与现状一致）。
func gateEnabledPlatform(params *parametersapp.Service) func(context.Context) bool {
	return func(ctx context.Context) bool {
		if params == nil {
			return false
		}
		values, err := params.PlatformValues(ctx)
		if err != nil {
			return false
		}
		enabled, _ := values["evaluation.gate.enabled"].(bool)
		return enabled
	}
}

// resolvePlatformVersion 把 id（HTTP path 语义）解析为 seq/status/isCurrent：遍历
// group 版本匹配 PlatformVersion.ID。未命中 ok=false。
func resolvePlatformVersion(params *parametersapp.Service) func(context.Context, string, int64) (int, string, bool, bool, error) {
	return func(ctx context.Context, groupKey string, versionID int64) (int, string, bool, bool, error) {
		versions, err := params.Versions(ctx, groupKey)
		if err != nil {
			return 0, "", false, false, err
		}
		for _, v := range versions {
			if v.ID == versionID {
				return v.VersionSeq, v.Status, v.IsCurrent, true, nil
			}
		}
		return 0, "", false, false, nil
	}
}

// gatePublishCoordinatorAdapter 把协调器整数决策翻译为 handler seam 字符串集合并封进
// PublishGateFunc：DecisionPassThrough→"passthrough"、DecisionApprovalPending→
// "approval_pending"（RunID 一并带出）、DecisionBlocked→"blocked"、DecisionRefusedNotWired→
// "refused_not_wired"；err!=nil 返回原始 error（handler 走 500），否则按 decision 渲染
// 409/202。宿主租户由 reqctx 取（publish/rollback 路由经 RequireDefaultTenant 门）。
func gatePublishCoordinatorAdapter(coordinator *evalapp.PublishGateCoordinator) PublishGateFunc {
	return func(ctx context.Context, groupKey string, versionID int64, actor string) (decision, message, runID string, err error) {
		hostTenantID := reqctx.TenantIDFromContext(ctx)
		result, err := coordinator.GatePublish(ctx, hostTenantID, evalapp.PublishGateRequest{
			GroupKey:  groupKey,
			VersionID: versionID,
			Actor:     actor,
		})
		if err != nil {
			return "", "", "", err
		}
		switch result.Decision {
		case evalapp.DecisionPassThrough:
			return "passthrough", "", "", nil
		case evalapp.DecisionApprovalPending:
			return "approval_pending", result.Message, result.RunID, nil
		case evalapp.DecisionBlocked:
			return "blocked", result.Message, result.RunID, nil
		case evalapp.DecisionRefusedNotWired:
			return "refused_not_wired", result.Message, "", nil
		default:
			return "", "", "", fmt.Errorf("unknown publish gate decision %v", result.Decision)
		}
	}
}
