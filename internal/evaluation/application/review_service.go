package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var (
	ErrReviewItemNotFound = errors.New("evaluation review item not found")
	// ErrReviewItemNotPending 是 MarkReviewed 的契约 sentinel：条目已非 pending。
	// 注意：PgReviewRepository 当前返回普通错误而非本 sentinel，Decide 采用统一
	// 重查降级（见 Decide 内注释），本 sentinel 保留为 port 契约文档。
	ErrReviewItemNotPending = errors.New("evaluation review item not pending")
)

// ReviewServiceDeps 是评审池服务的依赖。Suites/Evidence 为 nil 时 promote 跳过
// （fail-open：无评测集/证据能力则不沉淀，仅落轻量记录）。
type ReviewServiceDeps struct {
	Repo     port.ReviewRepository
	Suites   port.SuiteRepository
	Evidence port.TraceEvidenceReader
	Metrics  observability.MetricsProvider
	Logger   *zap.Logger
	Cfg      domain.ReviewConfig
	// TenantIDs 枚举活动租户，供跨租户求和刷新 eval_review_backlog（spec §8.3）。
	// nil 时退化为当前租户单租户语义（fail-open，未接线不 panic）。
	TenantIDs func(ctx context.Context) ([]string, error)
}

type ReviewService struct {
	deps ReviewServiceDeps
}

func NewReviewService(deps ReviewServiceDeps) *ReviewService {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	if deps.Metrics == nil {
		deps.Metrics = observability.NoopMetrics{}
	}
	return &ReviewService{deps: deps}
}

// 编译期断言：ReviewService 满足 port.ReviewEscalator。
var _ port.ReviewEscalator = (*ReviewService)(nil)

// TryEscalateObservation 判定观测入池原因并幂等落条目（spec §6.6）。返回错误表示
// 升级失败，调用方（ObservationService）记日志 + 指标，不阻断主流程。
func (s *ReviewService) TryEscalateObservation(
	ctx context.Context, tenantID string, obs *domain.EvalObservation,
) error {
	triggers := domain.TriggersForObservation(obs, s.deps.Cfg)
	if len(triggers) == 0 {
		return nil
	}
	snapshot := observationSnapshot(obs)
	for _, reason := range triggers {
		if _, err := s.deps.Repo.UpsertItem(ctx, tenantID, &domain.ReviewItem{
			ID:            uuid.Must(uuid.NewV7()).String(),
			SourceType:    domain.ReviewSourceObservation,
			SourceID:      obs.ID,
			TraceID:       obs.TraceID,
			ResourceKind:  obs.Resource.Kind,
			ResourceID:    obs.Resource.ResourceID,
			TriggerReason: reason,
			Snapshot:      snapshot,
			Status:        domain.ReviewStatusPending,
			CreatedAt:     time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("escalate observation %s: %w", obs.ID, err)
		}
	}
	s.refreshBacklogFailOpen(ctx, tenantID)
	return nil
}

// TryEscalateCaseResult 判定评测集结果入池原因并幂等落条目。断言来源
// result.Error==""（judge 实际产出）由调用方 runCase 保证。按 AssertionMode 分支：
// judge case 走完整 TriggersForCaseResult（含 needs_review / low_confidence）；
// 规则 case 仅走 TriggersForProcessConflict——规则 case 无 judge 信号，low_confidence
// 不得误触发，needs_review 也仅对 judge 生效（spec §6.6）。会话轨迹判负
// （result.Trajectory.Kind 为 stalled/drifted）在任何分支追加 trajectory_failed 强制
// 入池（§4.5 盲点），不依赖逐轮信号是否命中。resource_kind/resource_id 归因取自 ref
// （与观测路径 obs.Resource 对齐），保证资源维度评审池过滤可用。
func (s *ReviewService) TryEscalateCaseResult(
	ctx context.Context, tenantID, runID string, ref domain.ResourceRef,
	result domain.EvalCaseResult, c domain.EvalCase, assertion domain.AssertionResult,
	outputPass, processPass bool,
) error {
	var triggers []domain.ReviewTriggerReason
	if c.AssertionMode == domain.AssertionJudge {
		triggers = domain.TriggersForCaseResult(c.NeedsReview, outputPass, processPass, assertion, s.deps.Cfg)
	} else {
		triggers = domain.TriggersForProcessConflict(outputPass, processPass)
	}
	// 会话容器级轨迹判负强制入池（spec 阶段 B §4.5 盲点）：stalled/drifted 是「整段
	// 演化没走对」的负例——逐轮单看可能挑不出独立错误、也无其它触发——必须由人工
	// 复核归因，与逐轮信号正交追加，不掩盖 needs_review/low_confidence 等原有原因。
	if result.Trajectory != nil && result.Trajectory.Kind.Failed() {
		triggers = append(triggers, domain.TriggerTrajectoryFailed)
	}
	if len(triggers) == 0 {
		return nil
	}
	snapshot := caseSnapshot(result, c, assertion)
	for _, reason := range triggers {
		if _, err := s.deps.Repo.UpsertItem(ctx, tenantID, &domain.ReviewItem{
			ID:            uuid.Must(uuid.NewV7()).String(),
			SourceType:    domain.ReviewSourceCaseResult,
			SourceID:      result.ID,
			RunID:         runID,
			TraceID:       result.TraceID,
			ResourceKind:  ref.Kind,
			ResourceID:    ref.ResourceID,
			TriggerReason: reason,
			Snapshot:      snapshot,
			Status:        domain.ReviewStatusPending,
			CreatedAt:     time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("escalate case result %s: %w", result.ID, err)
		}
	}
	s.refreshBacklogFailOpen(ctx, tenantID)
	return nil
}

// List 分页列出评审条目。
func (s *ReviewService) List(
	ctx context.Context, tenantID string, f port.ReviewFilter,
) ([]domain.ReviewItem, int64, error) {
	return s.deps.Repo.ListItems(ctx, tenantID, f)
}

// Get 取单条评审条目。
func (s *ReviewService) Get(ctx context.Context, tenantID, id string) (*domain.ReviewItem, error) {
	item, err := s.deps.Repo.GetItem(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, ErrReviewItemNotFound
	}
	return item, nil
}

// Decide 人工评审结论状态机 + 回写副作用（spec §9）。幂等：重复决策同一已 reviewed
// 条目返回当前条目且不重复写副作用。
func (s *ReviewService) Decide(
	ctx context.Context, tenantID, id, actor string, verdict domain.HumanVerdict, reason string,
) (*domain.ReviewItem, error) {
	if !verdict.Valid() {
		return nil, fmt.Errorf("review decide: invalid verdict %q", verdict)
	}
	item, err := s.deps.Repo.GetItem(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, ErrReviewItemNotFound
	}
	if item.Status == domain.ReviewStatusReviewed {
		return item, nil // 幂等：已评审直接返回
	}
	if err := s.deps.Repo.MarkReviewed(ctx, tenantID, id, verdict, actor, reason); err != nil {
		// 并发评审竞态 / 条目已非 pending：PgReviewRepository 对非 pending 返回
		// 普通错误（非 ErrReviewItemNotPending sentinel），统一降级为重查——若已被
		// 他人评审为 reviewed 则返回现状（幂等），否则原样返回原始错误。
		latest, getErr := s.deps.Repo.GetItem(ctx, tenantID, id)
		if getErr != nil {
			return nil, getErr
		}
		if latest != nil && latest.Status == domain.ReviewStatusReviewed {
			return latest, nil
		}
		return nil, err
	}
	item.Status = domain.ReviewStatusReviewed
	item.HumanVerdict = verdict
	item.Reviewer = actor
	item.ReviewReason = reason
	now := time.Now().UTC()
	item.ReviewedAt = &now

	s.applySideEffects(ctx, tenantID, item)
	s.refreshBacklogFailOpen(ctx, tenantID)
	return item, nil
}

// applySideEffects 按人工结论落轻量回写（fail-open：副作用失败仅日志，不改变
// 评审结论）。judge_misjudgment → 校准样本；fail / case_revision → 归因条目。
func (s *ReviewService) applySideEffects(ctx context.Context, tenantID string, item *domain.ReviewItem) {
	switch item.HumanVerdict {
	case domain.HumanVerdictJudgeMisjudgment:
		if err := s.createCalibrationSample(ctx, tenantID, item); err != nil {
			s.deps.Logger.Warn("review calibration sample failed", zap.Error(err),
				zap.String("review_item_id", item.ID))
		}
	case domain.HumanVerdictFail, domain.HumanVerdictCaseRevision:
		if err := s.createAttributionEntry(ctx, tenantID, item); err != nil {
			s.deps.Logger.Warn("review attribution entry failed", zap.Error(err),
				zap.String("review_item_id", item.ID))
		}
	}
}

func (s *ReviewService) createCalibrationSample(ctx context.Context, tenantID string, item *domain.ReviewItem) error {
	signals := snapshotSignals(item)
	sample := &domain.CalibrationSample{
		ID:           uuid.Must(uuid.NewV7()).String(),
		ReviewItemID: item.ID,
		SourceType:   item.SourceType,
		SourceID:     item.SourceID,
		Signals:      signals,
		HumanVerdict: item.HumanVerdict,
		Reviewer:     item.Reviewer,
		Reason:       item.ReviewReason,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.deps.Repo.CreateCalibrationSample(ctx, tenantID, sample); err != nil {
		return err
	}
	// promote：observation 来源解析 trace 构造 judge case 进评测集草稿。
	if s.deps.Suites == nil {
		return nil
	}
	if err := s.promote(ctx, tenantID, item); err != nil {
		return fmt.Errorf("promote review item %s: %w", item.ID, err)
	}
	return nil
}

func (s *ReviewService) createAttributionEntry(ctx context.Context, tenantID string, item *domain.ReviewItem) error {
	entry := &domain.AttributionEntry{
		ID:           uuid.Must(uuid.NewV7()).String(),
		ReviewItemID: item.ID,
		SourceType:   item.SourceType,
		SourceID:     item.SourceID,
		ResourceKind: item.ResourceKind,
		ResourceID:   item.ResourceID,
		Dimension:    s.firstLowConfidenceDimension(item),
		Snapshot:     item.Snapshot,
		Status:       string(item.HumanVerdict),
		Reviewer:     item.Reviewer,
		Reason:       item.ReviewReason,
		CreatedAt:    time.Now().UTC(),
	}
	return s.deps.Repo.CreateAttributionEntry(ctx, tenantID, entry)
}

// promote 把评审条目沉淀为评测集草稿 case（spec §9）：observation 来源经
// TraceEvidenceReader 解析 trace 得 input/output，构造 judge-mode EvalCase 后
// 经 SuiteRepository.AddDraftCases 落草稿；case_result 来源从快照重建 case。
func (s *ReviewService) promote(ctx context.Context, tenantID string, item *domain.ReviewItem) error {
	suiteID := "review-promote-" + tenantID
	switch item.SourceType {
	case domain.ReviewSourceObservation:
		if s.deps.Evidence == nil || item.TraceID == "" {
			return nil
		}
		trace, err := s.deps.Evidence.Resolve(ctx, tenantID, item.TraceID)
		if err != nil {
			return fmt.Errorf("resolve trace %s: %w", item.TraceID, err)
		}
		name := fmt.Sprintf("review-observation-%s", item.SourceID)
		revisionID, err := s.draftRevisionForSuite(ctx, tenantID, suiteID, item.ResourceKind)
		if err != nil {
			return err
		}
		return s.deps.Suites.AddDraftCases(ctx, tenantID, revisionID, []domain.EvalCase{{
			ID: uuid.Must(uuid.NewV7()).String(), Name: name,
			Input: trace.Input, ExpectedOutput: nil, AssertionMode: domain.AssertionJudge,
			Enabled: false, // 草稿禁用，人工确认后启用
		}})
	case domain.ReviewSourceCaseResult:
		revisionID, err := s.draftRevisionForSuite(ctx, tenantID, suiteID, item.ResourceKind)
		if err != nil {
			return err
		}
		return s.deps.Suites.AddDraftCases(ctx, tenantID, revisionID, []domain.EvalCase{rebuiltCase(item)})
	default:
		return fmt.Errorf("promote: unsupported source type %q", item.SourceType)
	}
}

// draftRevisionForSuite 解析沉淀目标套件（review-promote-<tenant>）的草稿 revision；
// 套件不存在时惰性创建（P1c 轻量约定：沉淀到独立专用套件，P2 再纳入人工选择目标
// 评测集）。resourceKind 取自评审条目的资源 kind。promote 失败仍经 applySideEffects
// 的 Logger.Warn 记录，不阻断评审结论。
func (s *ReviewService) draftRevisionForSuite(
	ctx context.Context, tenantID, suiteID string, kind domain.ResourceKind,
) (string, error) {
	revision, found, err := s.deps.Suites.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return "", fmt.Errorf("get draft revision for %s: %w", suiteID, err)
	}
	if found {
		return revision.ID, nil
	}
	if err := s.deps.Suites.CreateSuite(ctx, tenantID, domain.EvalSuite{
		ID: suiteID, Name: "Review Promote Pool",
		Description: "人工评审池 promote 沉淀的专用评测集（P1c §9）",
	}, domain.EvalSuiteRevision{
		ID: uuid.Must(uuid.NewV7()).String(), SuiteID: suiteID,
		Status: domain.SuiteRevisionDraft, ResourceKind: kind,
	}); err != nil {
		return "", fmt.Errorf("create review promote suite %s: %w", suiteID, err)
	}
	created, found, err := s.deps.Suites.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return "", fmt.Errorf("get draft revision after create %s: %w", suiteID, err)
	}
	if !found {
		return "", fmt.Errorf("review promote suite %s has no draft revision after create", suiteID)
	}
	return created.ID, nil
}

// rebuiltCase 从评审条目快照平铺字段重建评测 case；快照缺失/字段类型不符时退化为
// 禁用 judge case（promote 失败由 applySideEffects 记录，不阻断评审结论）。
func rebuiltCase(item *domain.ReviewItem) domain.EvalCase {
	c := domain.EvalCase{
		ID:            uuid.Must(uuid.NewV7()).String(),
		AssertionMode: domain.AssertionJudge,
		Enabled:       false, // 草稿禁用，人工确认后启用
	}
	m, ok := item.Snapshot.(map[string]any)
	if !ok {
		return c
	}
	if name, ok := m["case_name"].(string); ok {
		c.Name = name
	}
	if input, ok := m["input"]; ok {
		c.Input = input
	}
	if expected, ok := m["expected"]; ok {
		c.ExpectedOutput = expected
	}
	return c
}

func observationSnapshot(obs *domain.EvalObservation) map[string]any {
	// signals 直接存对象（JSONB 自然序列化），避免二次编码。
	return map[string]any{
		"signals":    obs.Signals,
		"verdict":    string(obs.Verdict),
		"stratum":    obs.Stratum,
		"cost_usd":   obs.CostPerf.CostUSD,
		"created_at": obs.CreatedAt,
	}
}

func caseSnapshot(result domain.EvalCaseResult, c domain.EvalCase, assertion domain.AssertionResult) map[string]any {
	// actual 与 tool_sequence 入评审池快照前经 domain.SanitizeValue / SanitizeTools
	// 脱敏，与 eval_case_results 读回一致（spec §6.5）：敏感 key 与键值对不落库不外泄。
	// trajectory 是会话轨迹判负归因（§4.5）：评审员据此复核整段停滞/漂移，非敏感派生值。
	snapshot := map[string]any{
		"case_id":          c.ID,
		"case_name":        c.Name,
		"assertion_mode":   string(c.AssertionMode),
		"input":            c.Input,
		"expected":         c.ExpectedOutput,
		"actual":           domain.SanitizeValue(result.Actual),
		"passed":           result.Passed,
		"message":          result.Message,
		"judge_confidence": assertion.Confidence,
		"process_pass":     result.ProcessPass,
		"process_failure":  result.ProcessFailure,
		"tool_sequence":    domain.SanitizeTools(result.Tools),
	}
	if result.Trajectory != nil {
		snapshot["trajectory"] = result.Trajectory
	}
	return snapshot
}

// snapshotSignals 提取评审条目快照里的 signals 对象（judge 误判校准样本数据源）。
func snapshotSignals(item *domain.ReviewItem) any {
	m, ok := item.Snapshot.(map[string]any)
	if !ok {
		return nil
	}
	return m["signals"]
}

// firstLowConfidenceDimension 从快照 signals 找第一个低置信维度（归因条目
// dimension 数据源）；阈值来自配置，禁止内联数字。
func (s *ReviewService) firstLowConfidenceDimension(item *domain.ReviewItem) string {
	m, ok := item.Snapshot.(map[string]any)
	if !ok {
		return ""
	}
	signals, ok := m["signals"].(map[string]any)
	if !ok {
		return ""
	}
	judge, ok := signals["judge"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range judge {
		j, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		confidence, _ := j["confidence"].(float64)
		dimension, _ := j["dimension"].(string)
		if confidence < s.deps.Cfg.LowConfidenceThreshold {
			return dimension
		}
	}
	return ""
}

// RefreshBacklog 刷新 eval_review_backlog Gauge（积压告警数据源，spec §8.3）。
// 语义：跨租户求和。先 count 当前租户（保底——当前租户查询失败即返回，DB
// 故障由此传播到 refreshBacklogFailOpen 的 Warn），再枚举活动租户累加其余
// 租户 pending。非当前租户 count 失败（schema 未 provision 等）跳过计 0，
// 跨租户求和 fail-open。
func (s *ReviewService) RefreshBacklog(ctx context.Context, tenantID string) error {
	if s.deps.TenantIDs == nil {
		n, err := s.deps.Repo.CountPending(ctx, tenantID)
		if err != nil {
			return err
		}
		s.deps.Metrics.SetEvalReviewBacklog(n)
		return nil
	}
	cur, err := s.deps.Repo.CountPending(ctx, tenantID)
	if err != nil {
		return err
	}
	ids, err := s.deps.TenantIDs(ctx)
	if err != nil {
		return err
	}
	total := cur
	seen := map[string]bool{tenantID: true}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		n, err := s.deps.Repo.CountPending(ctx, id)
		if err != nil {
			continue // 非当前租户失败跳过（缺表等），跨租户求和 fail-open
		}
		total += n
	}
	s.deps.Metrics.SetEvalReviewBacklog(total)
	return nil
}

// refreshBacklogFailOpen 刷新积压指标并 fail-open：失败仅记录日志，不阻断主流程。
func (s *ReviewService) refreshBacklogFailOpen(ctx context.Context, tenantID string) {
	if err := s.RefreshBacklog(ctx, tenantID); err != nil {
		s.deps.Logger.Warn("review backlog refresh failed", zap.Error(err))
	}
}
