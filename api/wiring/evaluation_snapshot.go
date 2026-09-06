package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	agentgraph "github.com/byteBuilderX/stratum/internal/agent/application/graph"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// snapshotCapturer 实现 evalport.SnapshotCapturer：run 创建时捕获评测上下文
// 版本快照（D1 值复制）。读 parameters 组 production label（IsCurrent）复制值、
// 现场固化被测执行窗口、pin 被测 MCP/Knowledge 当前分流 revision。任何失败
// 返回 error → EnqueueRun 拒绝创建（D5 fail-closed 锚定创建时）。
type snapshotCapturer struct {
	params      *parametersapp.Service
	revisions   agentRevisionService // 被测 agent revision 读取
	modelCtx    agentport.ModelContextProvider
	details     agentport.TenantModelDetailsProvider
	vendor      func(string) (int, int)
	mcpResolver agentport.MCPRevisionResolver
	knowRes     agentport.KnowledgeRevisionResolver
	// skills 解析被测 agent 绑定的 skill 的当时生效发布版（run 创建时点锚定）。
	skills agentport.SkillActivationResolver
	// bindings 解析 skill→承载 agent（agent_skill_links 只读 port）。
	bindings agentport.AgentSkillBinding
	// baselines 提供承载 agent 的 CreatePublishedBaseline（skill 场景锁 pin，D7）。
	baselines *agentEvaluationAdapter
	logger    *zap.Logger
}

// warn 记录快照捕获过程中的非阻断问题（resolver/DB 读取失败仍回退，不阻断创建）。
// logger 未装配（nil）时静默跳过，保持既有 fail-open 语义且不 panic。
func (c snapshotCapturer) warn(msg string, fields ...zap.Field) {
	if c.logger != nil {
		c.logger.Warn(msg, fields...)
	}
}

func (c snapshotCapturer) Capture(ctx context.Context, tenantID string, input evalport.CaptureInput) (*evaldomain.EvaluationContextSnapshot, error) {
	if c.params == nil {
		return nil, errors.New("capture evaluation context: parameters service unavailable")
	}
	snap := &evaldomain.EvaluationContextSnapshot{
		SchemaVersion: evaldomain.SnapshotSchemaVersion,
		CapturedAt:    time.Now().UTC(),
		CapturedBy:    input.RequestedBy,
		PinnedAssignments: evaldomain.PinnedAssignments{
			SkillAgentRevision: map[string]string{},
			MCPRevisions:       map[string]string{},
			KnowledgeRevisions: map[string]string{},
			SkillRevisions:     map[string]string{},
		},
	}
	evalGroup, err := c.captureGroup(ctx, evaldomain.GroupEvaluation)
	if err != nil {
		return nil, err
	}
	snap.Evaluation = evalGroup
	agentGroup, err := c.captureGroup(ctx, evaldomain.GroupAgent)
	if err != nil {
		return nil, err
	}
	traceGroup, err := c.captureGroup(ctx, evaldomain.GroupTrace)
	if err != nil {
		return nil, err
	}
	snap.Execution = []evaldomain.GroupSnapshot{agentGroup, traceGroup}
	subject, pinnedID, err := c.loadSubject(ctx, tenantID, input.Resource)
	if err != nil {
		return nil, err
	}
	win, reserve, err := c.resolveSnapshotWindow(ctx, tenantID, subject)
	if err != nil {
		return nil, err
	}
	snap.ResolvedExecution = evaldomain.ResolvedExecution{ContextWindow: win, OutputReserve: reserve}
	c.capturePinnedAssignments(ctx, tenantID, subject, snap)
	// skill 资源额外记录承载 agent 锁定 revision pin（D7）：执行时用 pin 而非
	// Registry 当前生产配置，可重放。
	if pinnedID != "" {
		snap.PinnedAssignments.SkillAgentRevision[input.Resource.ResourceID] = pinnedID
	}
	return snap, nil
}

// captureGroup 读一组 production label：IsCurrent 版本 → 复制 snapshot 值。
// 未发布（无 IsCurrent）→ 空组（nil Values），执行时消费层默认适用。
func (c snapshotCapturer) captureGroup(ctx context.Context, groupKey string) (evaldomain.GroupSnapshot, error) {
	if c.params == nil {
		return evaldomain.GroupSnapshot{}, fmt.Errorf("capture %s group versions: parameters service unavailable", groupKey)
	}
	versions, err := c.params.Versions(ctx, groupKey)
	if err != nil {
		return evaldomain.GroupSnapshot{}, fmt.Errorf("capture %s group versions: %w", groupKey, err)
	}
	for _, v := range versions {
		if !v.IsCurrent {
			continue
		}
		values := make(map[string]any, len(v.Snapshot))
		for k, raw := range v.Snapshot {
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return evaldomain.GroupSnapshot{}, fmt.Errorf("capture %s snapshot decode %s: %w", groupKey, k, err)
			}
			values[k] = decoded
		}
		return evaldomain.GroupSnapshot{GroupKey: groupKey, VersionSeq: int64(v.VersionSeq), Values: values}, nil
	}
	return evaldomain.GroupSnapshot{GroupKey: groupKey}, nil
}

// loadSubject 加载被测承载 agent revision：agent 资源读锁定 revision 的 payload；
// skill 资源走 lockSkillSubject（D7：FindAgentBySkill → CreatePublishedBaseline
// 幂等 pin），返回承载 agent revision 与 pin 的 revisionID（skill 分支非空）。
func (c snapshotCapturer) loadSubject(ctx context.Context, tenantID string, resource evaldomain.ResourceRef) (*agentdomain.AgentRevision, string, error) {
	switch resource.Kind {
	case evaldomain.ResourceKindAgent:
		if c.revisions == nil {
			return nil, "", fmt.Errorf("capture subject agent %s: revision service unavailable", resource.RevisionID)
		}
		_, payload, found, err := c.revisions.Get(ctx, tenantID, resource)
		if err != nil {
			return nil, "", fmt.Errorf("capture subject agent %s: %w", resource.RevisionID, err)
		}
		if !found {
			return nil, "", fmt.Errorf("capture subject agent %s: revision not found", resource.RevisionID)
		}
		var rev agentdomain.AgentRevision
		if err := json.Unmarshal(payload, &rev); err != nil {
			return nil, "", fmt.Errorf("capture subject agent %s: decode revision: %w", resource.RevisionID, err)
		}
		return &rev, "", nil
	case evaldomain.ResourceKindSkill:
		return c.lockSkillSubject(ctx, tenantID, resource.ResourceID)
	default:
		return nil, "", fmt.Errorf("capture subject: unsupported resource kind %q", resource.Kind)
	}
}

// lockSkillSubject 锁承载 agent 的 published revision（D7）：FindAgentBySkill →
// CreatePublishedBaseline（SnapshotRevision→Create→Publish，幂等 contentHash）→
// 返回承载 agent revision + 记录 pin。创建时点固化，评测执行与生产 drift 无关。
// CreatePublishedBaseline 已自行处理 tenant ctx，此处无需再 WithTenant。
func (c snapshotCapturer) lockSkillSubject(ctx context.Context, tenantID, skillID string) (*agentdomain.AgentRevision, string, error) {
	if c.bindings == nil || c.baselines == nil {
		return nil, "", errors.New("capture skill subject: bindings/baselines not configured")
	}
	agentID, found, err := c.bindings.FindAgentBySkill(ctx, skillID)
	if err != nil {
		return nil, "", fmt.Errorf("capture skill %s: resolve agent: %w", skillID, err)
	}
	if !found {
		return nil, "", fmt.Errorf("capture skill %s: no Agent bound", skillID)
	}
	ref, err := c.baselines.CreatePublishedBaseline(ctx, tenantID, agentID)
	if err != nil {
		return nil, "", fmt.Errorf("capture skill %s: pin agent baseline: %w", skillID, err)
	}
	snapshot, err := c.baselines.agents.SnapshotRevision(ctx, tenantID, agentID)
	if err != nil {
		return nil, "", fmt.Errorf("capture skill %s: snapshot agent %s: %w", skillID, agentID, err)
	}
	return &snapshot, ref.RevisionID, nil
}

// resolveSnapshotWindow 现场固化执行窗口与输出保留（D4：创建时点解析，执行不再读
// DB 模型权威）。复用 agentgraph 导出函数保持与 agent_window.go 同源链，并同步
// agent_window.go:21 的 MaxContextWindowTokens clamp（执行路径一致性）。
// output reserve 的 DB 权威读取复刻 agent_window.go:58 modelOutputReserve。
func (c snapshotCapturer) resolveSnapshotWindow(ctx context.Context, tenantID string, rev *agentdomain.AgentRevision) (int, int, error) {
	if rev == nil {
		return 0, 0, errors.New("capture resolved execution: subject revision missing")
	}
	modelWin, _ := agentgraph.ResolveModelWindow(ctx, rev.Model, c.modelCtx, c.vendor)
	if modelWin > constants.MaxContextWindowTokens {
		modelWin = constants.MaxContextWindowTokens
	}
	window, _ := agentgraph.ResolveAgentWindow(modelWin, rev.ModelParameters.MaxContextTokens)
	if window <= 0 {
		return 0, 0, fmt.Errorf("capture resolved execution: invalid context window %d for model %q", window, rev.Model)
	}
	reserve := rev.ModelParameters.MaxTokens
	if reserve <= 0 {
		reserve = c.outputReserveSnapshot(ctx, tenantID, rev.Model)
	}
	if reserve <= 0 {
		return 0, 0, fmt.Errorf("capture resolved execution: invalid output reserve %d for model %q", reserve, rev.Model)
	}
	return window, reserve, nil
}

// outputReserveSnapshot 取被测模型输出保留：DB 权威 > vendor 缺省 > 常量缺省。
func (c snapshotCapturer) outputReserveSnapshot(ctx context.Context, tenantID, model string) int {
	if reserve := c.modelReserve(ctx, tenantID, model); reserve > 0 {
		return reserve
	}
	if c.vendor != nil {
		if _, maxOut := c.vendor(model); maxOut > 0 {
			return maxOut
		}
	}
	return constants.DefaultOutputReserveTokens
}

// modelReserve 读 tenant model details 的 DB 权威输出保留，复刻
// agent_window.go:58 modelOutputReserve 的取值链。
func (c snapshotCapturer) modelReserve(ctx context.Context, tenantID, model string) int {
	if c.details == nil {
		return 0
	}
	details, err := c.details.ListTenantModelDetails(ctx, tenantID)
	if err != nil {
		c.warn("evaluation.capture.pin model reserve failed",
			zap.Error(err), zap.String("tenant_id", tenantID), zap.String("model", model))
		return 0
	}
	for _, d := range details {
		if d.Model != model {
			continue
		}
		if reserve := reserveFromDetail(d); reserve > 0 {
			return reserve
		}
	}
	return 0
}

func reserveFromDetail(d agentdomain.TenantModelDetail) int {
	switch {
	case d.EffectiveMaxTokens > 0:
		return d.EffectiveMaxTokens
	case d.MaxTokens > 0:
		return d.MaxTokens
	case d.DefaultOutputTokens > 0:
		return d.DefaultOutputTokens
	}
	return 0
}

// capturePinnedAssignments 固化被测 agent 绑定的 MCP/Knowledge/Skill 当前分流
// revision（D4：评测执行用 pin 替代实时 canary 分流/发版漂移）。resolver 未命中
// （未分流/未绑定）→ 不入 pin，执行时该资源走非分流路径。subjectID 用
// "evaluation:"+tenantID 固定键（评测无会话 subject；resolver 仅用它做确定性分流）。
func (c snapshotCapturer) capturePinnedAssignments(ctx context.Context, tenantID string, rev *agentdomain.AgentRevision, snap *evaldomain.EvaluationContextSnapshot) {
	subjectID := "evaluation:" + tenantID
	for _, b := range rev.Bindings {
		if !b.Enabled {
			continue
		}
		c.pinBinding(ctx, tenantID, subjectID, b, snap)
	}
}

func (c snapshotCapturer) pinBinding(ctx context.Context, tenantID, subjectID string, b agentdomain.AgentBinding, snap *evaldomain.EvaluationContextSnapshot) {
	switch b.Kind {
	case agentdomain.AgentBindingMCP:
		c.pinMCP(ctx, tenantID, subjectID, b, snap)
	case agentdomain.AgentBindingKnowledge:
		c.pinKnowledge(ctx, tenantID, subjectID, b, snap)
	case agentdomain.AgentBindingSkill:
		c.pinSkill(ctx, tenantID, b, snap)
	}
}

func (c snapshotCapturer) pinMCP(ctx context.Context, tenantID, subjectID string, b agentdomain.AgentBinding, snap *evaldomain.EvaluationContextSnapshot) {
	if c.mcpResolver == nil {
		return
	}
	a, found, err := c.mcpResolver.ResolveMCPRevision(ctx, tenantID, b.ID, subjectID)
	if err != nil {
		c.warn("evaluation.capture.pin mcp resolve failed",
			zap.Error(err), zap.String("tenant_id", tenantID), zap.String("server_id", b.ID))
		return
	}
	if !found || a.RevisionID == "" {
		return
	}
	snap.PinnedAssignments.MCPRevisions[b.ID] = a.RevisionID
}

func (c snapshotCapturer) pinKnowledge(ctx context.Context, tenantID, subjectID string, b agentdomain.AgentBinding, snap *evaldomain.EvaluationContextSnapshot) {
	if c.knowRes == nil {
		return
	}
	a, found, err := c.knowRes.ResolveKnowledgeRevision(ctx, tenantID, b.Name, subjectID)
	if err != nil {
		c.warn("evaluation.capture.pin knowledge resolve failed",
			zap.Error(err), zap.String("tenant_id", tenantID), zap.String("workspace_name", b.Name))
		return
	}
	if !found || a.Revision.RevisionID == "" {
		return
	}
	snap.PinnedAssignments.KnowledgeRevisions[b.Name] = a.Revision.RevisionID
}

// pinSkill 锚定被测 agent 绑定的 skill：空 revision → ResolveSkills 取当时
// 生效的 active published revision，命中写 PinnedAssignments.SkillRevisions。
// 与 MCP/Knowledge D4 一致的 fail-open：解析失败或 skill 未发布（无激活版）
// → warn 不 pin，执行期该 skill 走非 pin 路径（本就解析不到时等同缺失）。
func (c snapshotCapturer) pinSkill(ctx context.Context, tenantID string, b agentdomain.AgentBinding, snap *evaldomain.EvaluationContextSnapshot) {
	if c.skills == nil {
		return
	}
	catalog, err := c.skills.ResolveSkills(ctx, tenantID, []agentport.SkillRevisionRef{{SkillID: b.ID}})
	if err != nil {
		c.warn("evaluation.capture.pin skill resolve failed",
			zap.Error(err), zap.String("tenant_id", tenantID), zap.String("skill_id", b.ID))
		return
	}
	activation, ok := catalog[b.ID]
	if !ok || activation.RevisionID == "" {
		return
	}
	if snap.PinnedAssignments.SkillRevisions == nil {
		snap.PinnedAssignments.SkillRevisions = map[string]string{}
	}
	snap.PinnedAssignments.SkillRevisions[b.ID] = activation.RevisionID
}
