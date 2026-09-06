package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type ExecutionResult = port.ExecutionResult

var ErrRunNotFound = errors.New("evaluation run not found")

type RunInput struct {
	TenantID    string
	RequestedBy string
	Resource    domain.ResourceRef
	Suite       domain.EvalSuiteRevision
}

type Service struct {
	adapter     port.ResourceAdapter
	repo        port.RunRepository
	suites      port.SuiteRepository
	traceReader port.TraceEvidenceReader
	// judge evaluates assertion_mode=judge cases; nil keeps rule assertions
	// working and makes judge cases fail closed.
	judge port.LLMJudge
	// review 是人工评审池升级入口（P1c §6.6 内联触发）；nil 时评审升级静默跳过
	// （fail-open，评审池未装配不阻断评测执行）。
	review    port.ReviewEscalator
	reviewCfg domain.ReviewConfig
	logger    *zap.Logger
	// metrics 记录平台指标（case_result 升级失败计数，spec §6.6）；nil 时升级
	// 失败仅日志（fail-open，不 panic）。
	metrics observability.MetricsProvider
	// platformVersion 解析平台配置组当前生效版本序号（Phase 2 §4.3 版本锚点）。
	// nil 时 run.metrics.version.platform_seq 记 0（unknown，fail-open）。
	platformVersion func(ctx context.Context) (int64, bool, error)
}

func NewService(
	adapter port.ResourceAdapter,
	repo port.RunRepository,
	traceReader port.TraceEvidenceReader,
	judge port.LLMJudge,
	suites ...port.SuiteRepository,
) *Service {
	var suiteRepo port.SuiteRepository
	if len(suites) > 0 {
		suiteRepo = suites[0]
	}
	return &Service{adapter: adapter, repo: repo, suites: suiteRepo, traceReader: traceReader, judge: judge, logger: zap.NewNop()}
}

// SetReviewEscalator 注入评审池升级器（wiring 在 NewService 之后调用）。
func (s *Service) SetReviewEscalator(e port.ReviewEscalator, cfg domain.ReviewConfig) {
	s.review = e
	s.reviewCfg = cfg
}

// SetObservability 注入真 logger 与平台指标（case_result 升级失败计数，spec §6.6）。
// wiring 在 SetReviewEscalator 后调用；logger 为 nil 保留默认 Nop，metrics 为 nil
// 时升级失败仅日志（fail-open，不 panic）。
func (s *Service) SetObservability(logger *zap.Logger, metrics observability.MetricsProvider) {
	if logger != nil {
		s.logger = logger
	}
	s.metrics = metrics
}

// SetPlatformVersion 注入平台版本读取器（wiring 在 NewService 后调用）；nil
// 表示未装配，run.metrics.version.platform_seq 记 0（unknown，fail-open）。
func (s *Service) SetPlatformVersion(fn func(ctx context.Context) (int64, bool, error)) {
	s.platformVersion = fn
}

// escalateCaseResult 通过评审池升级器判定评测集结果是否入池并幂等落条目
// （fail-open：失败仅日志，不阻断评测流程）。ref 供条目 resource_kind/resource_id
// 归因；outputPass/processPass 是输出断言与过程断言（§6.5）的通过结果，
// 供 process_output_conflict 触发。
func (s *Service) escalateCaseResult(
	ctx context.Context, tenantID, runID string, ref domain.ResourceRef, result domain.EvalCaseResult,
	c domain.EvalCase, assertion domain.AssertionResult, outputPass, processPass bool,
) {
	if s.review == nil {
		return
	}
	err := s.review.TryEscalateCaseResult(ctx, tenantID, runID, ref, result, c, assertion, outputPass, processPass)
	if err != nil {
		s.logReviewEscalateError(ctx, err)
		if s.metrics != nil {
			s.metrics.IncEvalReviewEscalateFailure()
		}
	}
}

func (s *Service) logReviewEscalateError(_ context.Context, err error) {
	s.logger.Warn("evaluation review escalation failed", zap.Error(err))
}

func (s *Service) RunStored(
	ctx context.Context,
	tenantID, requestedBy string,
	resource domain.ResourceRef,
	suiteRevisionID string,
	snapshot *domain.EvaluationContextSnapshot,
) (domain.EvalRun, error) {
	if snapshot == nil {
		return domain.EvalRun{}, errors.New("evaluation run: context snapshot missing; recreate the run")
	}
	ctx = domain.WithEvalSnapshot(ctx, snapshot)
	if s.suites == nil {
		return domain.EvalRun{}, errors.New("evaluation suite repository not configured")
	}
	suite, ok, err := s.suites.GetRevision(ctx, tenantID, suiteRevisionID)
	if err != nil {
		return domain.EvalRun{}, err
	}
	if !ok || suite.Status != domain.SuiteRevisionPublished {
		return domain.EvalRun{}, ErrSuiteNotFound
	}
	if suite.ResourceKind != resource.Kind {
		return domain.EvalRun{}, fmt.Errorf("evaluation suite resource kind %q does not match %q", suite.ResourceKind, resource.Kind)
	}
	return s.Run(ctx, RunInput{TenantID: tenantID, RequestedBy: requestedBy, Resource: resource, Suite: suite})
}

func (s *Service) GetRun(ctx context.Context, tenantID, runID string) (domain.EvalRun, error) {
	run, ok, err := s.repo.GetRun(ctx, tenantID, runID)
	if err != nil {
		return domain.EvalRun{}, err
	}
	if !ok {
		return domain.EvalRun{}, ErrRunNotFound
	}
	return run, nil
}

func (s *Service) Run(ctx context.Context, input RunInput) (domain.EvalRun, error) {
	if domain.EvalSnapshotFromCtx(ctx) == nil {
		return domain.EvalRun{}, errors.New("evaluation run: context snapshot missing; recreate the run")
	}
	if err := input.Resource.Validate(); err != nil {
		return domain.EvalRun{}, err
	}
	// ContextSnapshot 取注入 ctx 的创建时快照（上方已 fail-closed 确保非 nil），
	// 随 SaveRun 落库 eval_runs.context_snapshot（spec §7 版本快照持久化）。
	run := domain.EvalRun{
		ID:              uuid.Must(uuid.NewV7()).String(),
		Resource:        input.Resource,
		SuiteRevisionID: input.Suite.ID,
		Passed:          true,
		Results:         make([]domain.EvalCaseResult, 0, len(input.Suite.Cases)),
		ContextSnapshot: domain.EvalSnapshotFromCtx(ctx),
		CreatedAt:       time.Now().UTC(),
	}
	for _, testCase := range input.Suite.Cases {
		if !testCase.Enabled {
			continue
		}
		run.TotalCases++
		result := s.runCase(ctx, input.TenantID, input.RequestedBy, input.Resource, run.ID, testCase)
		if result.Passed {
			run.PassedCases++
		} else {
			run.Passed = false
		}
		run.Results = append(run.Results, result)
	}
	run.Metrics = aggregateRunMetrics(run, runVersionAnchor{
		SuiteRevisionID: run.SuiteRevisionID,
		PlatformSeq:     s.resolvePlatformSeq(ctx, input.TenantID),
		ResourceVersion: run.Resource.RevisionID,
	})
	if err := s.repo.SaveRun(ctx, input.TenantID, run); err != nil {
		return domain.EvalRun{}, err
	}
	return run, nil
}

// resolvePlatformSeq 解析平台版本锚点；失败 fail-open（记 0）不阻断落库，但留痕便于诊断。
func (s *Service) resolvePlatformSeq(ctx context.Context, tenantID string) int64 {
	if s.platformVersion == nil {
		return 0
	}
	seqVal, ok, err := s.platformVersion(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("evaluation run: platform version", zap.Error(err))
		}
		return 0
	}
	if ok {
		return seqVal
	}
	return 0
}

func (s *Service) runCase(
	ctx context.Context, tenantID, requestedBy string, ref domain.ResourceRef, runID string, testCase domain.EvalCase,
) domain.EvalCaseResult {
	// 会话剧本 case（阶段 B §5.4）：adapter 实现 SessionRunner 才可执行，否则
	// runCaseSession 内部 fail-close。nil Session 的单轮 case 走下方既有路径零改动。
	if testCase.IsSession() {
		return s.runCaseSession(ctx, tenantID, requestedBy, ref, runID, testCase)
	}
	execution, err := s.adapter.ExecuteRevision(ctx, tenantID, requestedBy, ref, testCase)
	result := domain.EvalCaseResult{ID: uuid.Must(uuid.NewV7()).String(), CaseID: testCase.ID}
	if err != nil {
		result.Error = err.Error()
		result.FailureReason = "execution"
		return result
	}
	result.Actual = execution.Output
	result.TraceID = execution.TraceID
	result.Tokens = execution.Tokens
	result.CostUSD = execution.CostUSD
	result.DurationMs = execution.DurationMs
	result.RAGEvidence = execution.RAGEvidence
	result.Tools = execution.Tools

	// Resolve trace evidence from Opik (best-effort: Opik unavailability must
	// not block Agent execution or evaluation).
	if execution.TraceID != "" && s.traceReader != nil {
		trace, resolveErr := s.traceReader.Resolve(ctx, tenantID, execution.TraceID)
		if resolveErr != nil {
			// warn-only: trace evidence is supplementary, not critical
			result.Message = "trace evidence unavailable"
		} else {
			result.TraceEvidence = observedTraceToEvidence(trace)
		}
	}

	// 过程断言（§6.5）：tool_spec 确定性规则 + step_judge 可选 LLM 评分。判定
	// 失败（或 judge 基础设施故障）时 fail-closed：置 Error + execution 归因返回，
	// 绝不静默 pass。过程归因单独落 ProcessPass/ProcessFailure，不改输出归因。
	verdict, err := s.evaluateProcess(ctx, testCase, result)
	if err != nil {
		result.Error = err.Error()
		result.FailureReason = "execution"
		return result
	}
	result.ProcessPass = verdict.Passed
	result.ProcessFailure = verdict.Failure
	// 步骤级 judge 维度先于输出断言维度并入结果（judgeCase 在其基础上追加）。
	result.Dimensions = verdict.Dimensions

	// 输出断言按 assertion_mode 分派：judge 分支走 LLM judge 端口，规则分支走
	// domain 纯函数。两种分支都把过程断言与输出断言 AND——任一路失败即 case
	// 失败；FailureReason 保持输出归因，过程归因单独在 ProcessFailure。两个分支
	// 都在判定后按新签名触发评审池升级（§6.5 process_output_conflict）。
	if testCase.AssertionMode == domain.AssertionJudge {
		return s.judgeCaseResult(ctx, tenantID, runID, ref, testCase, result)
	}
	return s.ruleCaseResult(ctx, tenantID, runID, ref, testCase, execution.Output, result)
}

// runCaseSession 执行会话剧本 case（阶段 B §5.4）：adapter 必须是 port.SessionRunner，
// 否则 fail-close 报错（绝不静默退化为单轮）；剧本结构非法（Validate 不过）同样
// fail-close，在驱动适配器开受控会话前即拒绝。RunSession 逐轮驱动执行并返回每轮
// 证据；任一轮失败保留已收集 partial evidence 并返回 error。终态断言复用单轮既有
// 规则/judge 分支（actual=末轮输出，零断言复制）；case 级过程断言作用于末轮工具
// 序列。TraceID 取末轮（逐轮 trace 已留在 Turns 证据里）；Tokens/CostUSD/DurationMs
// 聚合为逐轮之和（成败均计：partial evidence 的消耗如实入 case 级成本），run 级
// Metrics 自然汇总整段会话消耗。
func (s *Service) runCaseSession(
	ctx context.Context, tenantID, requestedBy string, ref domain.ResourceRef, runID string,
	testCase domain.EvalCase,
) domain.EvalCaseResult {
	result := domain.EvalCaseResult{ID: uuid.Must(uuid.NewV7()).String(), CaseID: testCase.ID}
	runner, ok := s.adapter.(port.SessionRunner)
	if !ok {
		result.Error = "session evaluation not supported by this resource adapter"
		result.FailureReason = "execution"
		return result
	}
	// 剧本结构 preflight：非法剧本（零轮/空 user）在 RunSession 开 source='evaluation'
	// 会话前即拒绝，不浪费一次受控会话。归因 execution 与其它「会话无法执行」路径
	// 一致（FailureReason 语义集无独立 config 枚举）。
	if reason, valid := testCase.Session.Validate(); !valid {
		result.Error = reason
		result.FailureReason = "execution"
		return result
	}
	evidences, err := runner.RunSession(ctx, tenantID, requestedBy, ref, *testCase.Session)
	result.Turns = evidences
	// 逐轮消耗无论成败都聚合：中途失败带回 partial evidence 时 case/run 级
	// tokens/cost/duration 如实反映真实消耗，避免 Turns 内为真实值而 case 级记 0。
	for _, e := range evidences {
		result.Tokens += e.Tokens
		result.CostUSD += e.CostUSD
		result.DurationMs += e.DurationMs
	}
	if err != nil {
		result.Error = err.Error()
		result.FailureReason = "execution"
		return result
	}
	if len(evidences) == 0 {
		result.Error = "session evaluation returned no turn evidence"
		result.FailureReason = "execution"
		return result
	}
	last := evidences[len(evidences)-1]
	result.Actual = last.Output
	result.TraceID = last.TraceID
	result.Tools = last.Tools
	verdict, err := s.evaluateProcess(ctx, testCase, result)
	if err != nil {
		result.Error = err.Error()
		result.FailureReason = "execution"
		return result
	}
	result.ProcessPass = verdict.Passed
	result.ProcessFailure = verdict.Failure
	result.Dimensions = verdict.Dimensions
	// 演化轨迹判据（阶段 B §4.2）：判定会话是否收敛/停滞/漂移，归因整段演化形态。
	// 纯函数确定性计算；judge 会话终态收敛由 judgeCaseResult 在 LLM 通过后翻转
	// （judge 模式纯函数只给 NA/Stalled，收敛命中以 LLM 权威终态为准，不臆断）。
	// 单轮剧本 → NotApplicable（无轮间演化可判），旧单轮 case（无 Session）不进本路径。
	trajectory := domain.EvaluateTrajectory(evidences, testCase.ExpectedOutput, testCase.AssertionMode, testCase.Session.Goal)
	result.Trajectory = &trajectory
	if testCase.AssertionMode == domain.AssertionJudge {
		return s.judgeCaseResult(ctx, tenantID, runID, ref, testCase, result)
	}
	return s.ruleCaseResult(ctx, tenantID, runID, ref, testCase, last.Output, result)
}

// judgeCaseResult 走 LLM judge 输出断言并把过程断言并入最终 Passed；随后内联
// 触发评审池升级（P1c §6.6，仅 judge 实际产出判定时）。ref 供条目资源归因。
func (s *Service) judgeCaseResult(
	ctx context.Context, tenantID, runID string, ref domain.ResourceRef, testCase domain.EvalCase,
	result domain.EvalCaseResult,
) domain.EvalCaseResult {
	assertion, result := s.judgeCase(ctx, testCase, result)
	result.Passed = assertion.Passed && result.ProcessPass
	// judge 会话终态收敛翻转（阶段 B §4.2）：LLM 权威判定末轮到达目标 → 轨迹翻转
	// 为 Converged。judge 模式纯函数只产出 NA/Stalled——先重复后成功的会话，终态
	// 通过说明最终收敛，不得判负。翻转后 Passed 与 Trajectory 自洽：通过即收敛。
	if result.Error == "" && assertion.Passed && result.Trajectory != nil {
		result.Trajectory = &domain.TrajectoryVerdict{
			Kind:   domain.TrajectoryConverged,
			Reason: "LLM judge 判定末轮到达目标（收敛）",
		}
	}
	// LLM 判负 + 轨迹停滞/漂移：容器级归因优先——整段会话绕圈/漂移没走对比单轮维度
	// 更能解释判负（§4.2），评审池据此强制入池复核。基础设施失败（Error 非空）不改
	// 归因，保持 execution fail-closed 语义。
	if result.Error == "" && !assertion.Passed && result.Trajectory != nil && result.Trajectory.Kind.Failed() {
		result.FailureReason = "trajectory:" + string(result.Trajectory.Kind)
	}
	if result.Error == "" {
		s.escalateCaseResult(ctx, tenantID, runID, ref, result, testCase, assertion, assertion.Passed, result.ProcessPass)
	}
	return result
}

// ruleCaseResult 走 domain 纯函数规则断言并把过程断言并入最终 Passed；随后内联
// 触发评审池升级（§6.5）：规则 case 无 judge 信号，仅 process_output_conflict
// 可能入池，low_confidence 不生效。ref 供条目资源归因。
func (s *Service) ruleCaseResult(
	ctx context.Context, tenantID, runID string, ref domain.ResourceRef, testCase domain.EvalCase, actual any,
	result domain.EvalCaseResult,
) domain.EvalCaseResult {
	assertion, err := domain.EvaluateAssertion(testCase.AssertionMode, actual, testCase.ExpectedOutput)
	if err != nil {
		result.Error = err.Error()
		result.FailureReason = "execution"
		return result
	}
	result.Passed = assertion.Passed && result.ProcessPass
	result.Message = assertion.Message
	if !assertion.Passed {
		// 会话轨迹判负优先容器级归因（§4.2）：stalled/drifted 说明整段会话没走对，
		// 比单轮 assert:<mode> 更能解释判负（末轮 miss 是表象，演化是根因）；无轨迹
		// 维度（旧单轮 case Trajectory=nil）回退断言归因，golden 判定不变。
		result.FailureReason = trajectoryFailureReason(result.Trajectory, "assert:"+string(testCase.AssertionMode))
	}
	s.escalateCaseResult(ctx, tenantID, runID, ref, result, testCase, assertion, assertion.Passed, result.ProcessPass)
	return result
}

// processVerdict 是过程断言（§6.5）的合并判定：tool_spec 确定性规则与 step_judge
// LLM 评分两路独立、逐路收集失败。Failure 是过程归因失败描述（多失败以 "; " 连接）；
// Dimensions 来自步骤级 judge，由调用方并入 result.Dimensions。
type processVerdict struct {
	Passed     bool
	Failure    string
	Dimensions []domain.DimensionScore
}

// evaluateProcess 判定 case 的过程断言：ToolSpec!=nil → EvaluateToolSequence
// 确定性规则；StepJudge!=nil → judgeProcess LLM 步骤级评分。任一失败即过程判定
// 失败；step_judge 基础设施故障（judge nil/disabled/marshal/解析失败）向上返回
// error，由 runCase fail-closed 处理，绝不静默 pass。
func (s *Service) evaluateProcess(
	ctx context.Context, testCase domain.EvalCase, result domain.EvalCaseResult,
) (processVerdict, error) {
	verdict := processVerdict{Passed: true}
	var failures []string

	if testCase.ToolSpec != nil {
		assertion := domain.EvaluateToolSequence(toolNames(result.Tools), *testCase.ToolSpec)
		failures = append(failures, assertion.Failures...)
	}
	if testCase.StepJudge != nil {
		ja, err := s.judgeProcess(ctx, testCase, *testCase.StepJudge, result)
		if err != nil {
			return processVerdict{}, err
		}
		verdict.Dimensions = append(verdict.Dimensions, ja.Dimensions...)
		if !ja.Passed {
			failures = append(failures, judgeFailureReason(ja))
		}
	}

	if len(failures) > 0 {
		verdict.Passed = false
		verdict.Failure = strings.Join(failures, "; ")
	}
	return verdict, nil
}

// judgeProcess 用 LLM 对工具序列做步骤级评分（§6.5 step_judge）。fail-closed：
// judge nil/disabled 或输入 marshal 失败 → 返回 error。Model 为空表示走平台默认
// 模型；Rubric 为空时由 judge 适配层回退平台默认步骤 rubric。
func (s *Service) judgeProcess(
	ctx context.Context, testCase domain.EvalCase, stepJudge domain.StepJudge, result domain.EvalCaseResult,
) (domain.AssertionResult, error) {
	if s.judge == nil || !s.judge.Enabled(ctx) {
		return domain.AssertionResult{}, errors.New("LLM judge disabled")
	}
	inputJSON, err := json.Marshal(testCase.Input)
	if err != nil {
		return domain.AssertionResult{}, fmt.Errorf("step judge: marshal input: %w", err)
	}
	expectedJSON, err := json.Marshal(testCase.ExpectedOutput)
	if err != nil {
		return domain.AssertionResult{}, fmt.Errorf("step judge: marshal expected output: %w", err)
	}
	actualJSON, err := json.Marshal(result.Actual)
	if err != nil {
		return domain.AssertionResult{}, fmt.Errorf("step judge: marshal actual output: %w", err)
	}
	return s.judge.Judge(ctx, port.JudgeRequest{
		Model:          "",
		Rubric:         stepJudge.Criteria,
		Input:          string(inputJSON),
		ExpectedOutput: string(expectedJSON),
		Actual:         string(actualJSON),
		ToolSequence:   domain.FormatToolSequence(result.Tools),
	})
}

// toolNames 从工具观察序列提取工具名列表（EvaluateToolSequence 的输入）。
func toolNames(tools []domain.ToolObservation) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.ToolName)
	}
	return names
}

// judgeCase runs the LLM judge assertion for a judge case. Fail-closed: a
// nil or disabled judge makes the case fail with an explicit error instead
// of a silent pass. It returns the raw assertion (for review-pool escalation)
// alongside the result.
func (s *Service) judgeCase(ctx context.Context, testCase domain.EvalCase, result domain.EvalCaseResult) (domain.AssertionResult, domain.EvalCaseResult) {
	var zero domain.AssertionResult
	if s.judge == nil || !s.judge.Enabled(ctx) {
		result.Error = "LLM judge disabled"
		result.FailureReason = "execution"
		return zero, result
	}
	inputJSON, err := json.Marshal(testCase.Input)
	if err != nil {
		result.Error = fmt.Errorf("judge: marshal input: %w", err).Error()
		result.FailureReason = "execution"
		return zero, result
	}
	expectedJSON, err := json.Marshal(testCase.ExpectedOutput)
	if err != nil {
		result.Error = fmt.Errorf("judge: marshal expected output: %w", err).Error()
		result.FailureReason = "execution"
		return zero, result
	}
	actualJSON, err := json.Marshal(result.Actual)
	if err != nil {
		result.Error = fmt.Errorf("judge: marshal actual output: %w", err).Error()
		result.FailureReason = "execution"
		return zero, result
	}
	var spec domain.JudgeSpec
	if testCase.JudgeSpec != nil {
		spec = *testCase.JudgeSpec
	}
	judgeReq := port.JudgeRequest{
		Model:          spec.Model,
		Rubric:         spec.Rubric,
		Input:          string(inputJSON),
		ExpectedOutput: string(expectedJSON),
		Actual:         string(actualJSON),
	}
	// 会话剧本 case 携带逐轮 transcript（阶段 B §4.3）：judge 据此评「末轮是否到达
	// 目标/守住探针」。单轮 case 无 transcript（result.Turns 为空），请求契约与既有
	// 逐字节一致（wiring adapter 回归测试守护）。
	if testCase.IsSession() {
		judgeReq.Transcript = domain.FormatTranscript(result.Turns, testCase.Session.Goal)
	}
	assertion, err := s.judge.Judge(ctx, judgeReq)
	if err != nil {
		result.Error = err.Error()
		result.FailureReason = "execution"
		return zero, result
	}
	result.Passed = assertion.Passed
	result.Message = assertion.Message
	// 步骤级 judge 维度已由 runCase 预置到 result.Dimensions；输出维度追加合并，
	// 保持单一路径的既有语义（nil 起点 → 仅输出维度）。
	result.Dimensions = append(result.Dimensions, assertion.Dimensions...)
	result.FailureReason = judgeFailureReason(assertion)
	return assertion, result
}

// judgeFailureReason 从 judge 判定推导主要失败维度（spec §6.2）：优先取显式
// 判负的维度，否则取 score 最低维度；无维度信息时回退 "judge"（保持归因可见）。
func judgeFailureReason(assertion domain.AssertionResult) string {
	if assertion.Passed {
		return ""
	}
	for _, d := range assertion.Dimensions {
		if !d.Passed {
			return "dimension:" + d.Name
		}
	}
	if len(assertion.Dimensions) > 0 {
		worst := assertion.Dimensions[0]
		for _, d := range assertion.Dimensions[1:] {
			if d.Score < worst.Score {
				worst = d
			}
		}
		return "dimension:" + worst.Name
	}
	return "judge"
}

// trajectoryFailureReason 组装会话 case 失败归因：轨迹判负（stalled/drifted）时用
// 容器级 "trajectory:<kind>"（整段会话没走对，§4.2——比单轮断言归因更能解释判负），
// 否则回退断言/维度归因。Trajectory=nil（旧单轮 case）恒回退，golden 判定不变。
func trajectoryFailureReason(t *domain.TrajectoryVerdict, fallback string) string {
	if t != nil && t.Kind.Failed() {
		return "trajectory:" + string(t.Kind)
	}
	return fallback
}

func observedTraceToEvidence(t port.ObservedTrace) *domain.ObservedTraceEvidence {
	return &domain.ObservedTraceEvidence{
		CostUSD:           t.CostUSD,
		LatencyMs:         t.LatencyMs,
		Success:           t.Success,
		SecurityViolation: t.SecurityViolation,
	}
}
