package application

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

// AttributionReport is the structured output of a §9 parameter-attribution
// pass over a real (failed) evaluation run.
//
// It splits attribution into three layers:
//   - FailureSummaries / Clusters: which cases failed and under which primary
//     attribution key (output reason, process assertion, execution error).
//   - Diagnosis / TunableDirections: the rule-based Diagnoser's classification
//     of agent-attributable failures into tunable categories, mapped to
//     concrete agent parameter directions. Each TunableDirection carries a
//     Space tag: grid_search keys (∈ allowedParameterFields) may enter the
//     automatic GenerateParameterPatches search space, while prompt keys
//     (∈ allowedPromptFields) only prompt-rewrite — never grid search.
//   - EvalChainDirections: failures attributable to the evaluation harness
//     itself (judge dimension scoring, process assertions) map to the
//     platform-scoped evaluation.* parameters — declared tunable but NOT in
//     the automatic search space, so a harness-quality problem is never
//     "fixed" by grid-searching the agent under evaluation.
type AttributionReport struct {
	Resource    domain.ResourceRef
	RunID       string
	TotalCases  int
	FailedCases int
	// Advanceable is true when attribution produced at least one direction
	// (tunable or eval-chain) worth acting on. It is the clean signal a
	// caller uses to decide whether to enter the improvement loop.
	Advanceable bool
	// FailureSummaries describe every failed case (passed cases are excluded).
	// They deliberately include harness-attributable failures too — the
	// harness/agent split is applied only inside Diagnosis/TunableDirections.
	// Downstream optimizer stages that re-run DIAGNOSE over these summaries
	// must re-apply the same split, or a judge/process failure could be
	// misattributed as an agent defect (§9 backlog).
	FailureSummaries    []domain.FailureSummary
	Diagnosis           domain.DiagnosisReport
	Clusters            []FailureCluster
	TunableDirections   []TunableDirection
	EvalChainDirections []EvalChainDirection
}

// TunableSpace distinguishes which optimizable patch space a
// TunableDirection.Key belongs to: grid_search (∈ allowedParameterFields,
// may enter GenerateParameterPatches) versus prompt (∈ allowedPromptFields,
// LLM prompt-rewrite only, never grid search). Repair I3: the previous doc
// claimed every direction sat inside the search-space allowlist, which was
// false for the prompt keys the Diagnoser can surface.
type TunableSpace string

const (
	TunableSpaceGridSearch TunableSpace = "grid_search"
	TunableSpacePrompt     TunableSpace = "prompt"
)

// FailureCluster groups failed cases by their primary attribution key.
type FailureCluster struct {
	Reason string   `json:"reason"`
	Count  int      `json:"count"`
	Cases  []string `json:"cases"`
}

// TunableDirection names an agent parameter worth adjusting, the patch space
// it belongs to, and why. Space is derived from the domain allowlists so a
// caller can route the key to the correct optimizer without re-deriving it.
type TunableDirection struct {
	Key       string                 `json:"key"`
	Category  domain.TunableCategory `json:"category,omitempty"`
	Space     TunableSpace           `json:"space"`
	Direction string                 `json:"direction"`
}

// EvalChainDirection names a platform-scoped evaluation parameter to review
// when the harness — not the agent — is the suspected failure source.
type EvalChainDirection struct {
	PlatformKey string `json:"platform_key"`
	Reason      string `json:"reason"`
	Direction   string `json:"direction"`
}

// AttributionService converts a completed evaluation run into the structured
// §9 attribution report that drives the improvement loop. Rule-based
// classification always runs (no LLM in the hot path); LLM hypothesis
// enhancement is deliberately out of scope for the minimal landing.
type AttributionService struct {
	diagnoser *Diagnoser
	// tunables is the domain tunable registry, built once and reused for
	// category lookups (Minor 1: previously reconstructed per key).
	tunables *domain.TunableRegistry
}

func NewAttributionService() *AttributionService {
	return &AttributionService{
		diagnoser: NewDiagnoser(nil),
		tunables:  domain.NewTunableRegistry(),
	}
}

// AnalyzeRun attributes the failed cases of run. A run with no failures
// yields an empty report (no clusters, no diagnosis) rather than an error.
func (s *AttributionService) AnalyzeRun(
	ctx context.Context, run domain.EvalRun,
) (AttributionReport, error) {
	summaries := domain.FailedCaseSummaries(run)
	report := AttributionReport{
		Resource:         run.Resource,
		RunID:            run.ID,
		TotalCases:       run.TotalCases,
		FailedCases:      len(summaries),
		FailureSummaries: summaries,
	}
	if len(summaries) == 0 {
		return report, nil
	}

	failed := failedResults(run.Results)
	report.Clusters = buildFailureClusters(failed)
	// I1: failures whose attribution key carries a dimension:/process: prefix
	// belong to the evaluation harness, not the agent under evaluation. Strip
	// them before the rule-based Diagnoser so judge/process text embedded in
	// the description cannot be keyword-classified into agent-level tunables.
	agentResults := agentAttributableResults(failed)
	agentSummaries := summariesForCases(summaries, agentResults)
	if len(agentSummaries) > 0 {
		diagnosis, err := s.diagnoser.Diagnose(ctx, agentSummaries, agentResults)
		if err != nil {
			return AttributionReport{}, fmt.Errorf("attribute run %s: %w", run.ID, err)
		}
		report.Diagnosis = diagnosis
	}
	report.TunableDirections = s.buildTunableDirections(report.Diagnosis)
	// I2: EvalChain detection scans every failed case's attribution keys rather
	// than cluster primary keys, so an output failure concurrent with a
	// process:must_not_call safety signal does not mask the ruleguard direction.
	report.EvalChainDirections = buildEvalChainDirections(failed)
	report.Advanceable = len(report.TunableDirections) > 0 ||
		len(report.EvalChainDirections) > 0
	return report, nil
}

// harnessKeyPrefix reports whether an attribution key attributes the failure
// to the evaluation harness (judge dimension / process assertion) rather than
// to the agent under evaluation. Such failures only enter the EvalChain bucket.
func harnessKeyPrefix(key string) bool {
	return strings.HasPrefix(key, "dimension:") || strings.HasPrefix(key, "process:")
}

// isHarnessAttributable reports whether a failed case carries any
// harness-attribution key. When true, the case's describeFailure text embeds
// judge/process language, which the agent keyword classifier would otherwise
// fabricate into agent-level directions (I1 reproduced: dimension:relevance_score
// + "output not grounded in context" → memory_*_prompt).
func isHarnessAttributable(cr domain.EvalCaseResult) bool {
	return harnessKeyPrefix(cr.FailureReason) || harnessKeyPrefix(cr.ProcessFailure)
}

// agentAttributableResults narrows the failed cases to those the agent could
// plausibly be responsible for (order-preserving), excluding harness-attributable
// ones that only belong in the EvalChain bucket.
func agentAttributableResults(results []domain.EvalCaseResult) []domain.EvalCaseResult {
	out := make([]domain.EvalCaseResult, 0, len(results))
	for _, cr := range results {
		if !isHarnessAttributable(cr) {
			out = append(out, cr)
		}
	}
	return out
}

// summariesForCases returns the FailureSummary subset whose CaseName matches one
// of the given case results. FailedCaseSummaries and failedResults derive from
// the same run.Results by the same filter, so CaseID matching keeps the two
// independently-built slices aligned.
func summariesForCases(summaries []domain.FailureSummary, results []domain.EvalCaseResult) []domain.FailureSummary {
	if len(summaries) == 0 || len(results) == 0 {
		return nil
	}
	include := make(map[string]struct{}, len(results))
	for _, cr := range results {
		include[cr.CaseID] = struct{}{}
	}
	out := make([]domain.FailureSummary, 0, len(results))
	for _, fs := range summaries {
		if _, ok := include[fs.CaseName]; ok {
			out = append(out, fs)
		}
	}
	return out
}

// failedResults narrows the case results to the failed set so DIAGNOSE
// attribution signals (e.g. security-violation trace counts) match the
// failure summaries being classified.
func failedResults(results []domain.EvalCaseResult) []domain.EvalCaseResult {
	out := make([]domain.EvalCaseResult, 0, len(results))
	for _, cr := range results {
		if !cr.Passed {
			out = append(out, cr)
		}
	}
	return out
}

// primaryFailureReason picks the most actionable attribution key of a case:
// the output failure_reason wins over the process assertion, which wins over
// the bare execution error.
func primaryFailureReason(cr domain.EvalCaseResult) string {
	switch {
	case cr.FailureReason != "":
		return cr.FailureReason
	case cr.ProcessFailure != "":
		return cr.ProcessFailure
	case cr.Error != "":
		return "execution"
	default:
		return "unknown"
	}
}

// buildFailureClusters groups the failed cases by their primary attribution
// key (output reason → process assertion → execution error), with stable
// ordering and case-name evidence.
func buildFailureClusters(results []domain.EvalCaseResult) []FailureCluster {
	byReason := map[string]*FailureCluster{}
	order := make([]string, 0, len(results))
	for _, cr := range results {
		reason := primaryFailureReason(cr)
		cluster, ok := byReason[reason]
		if !ok {
			cluster = &FailureCluster{Reason: reason}
			byReason[reason] = cluster
			order = append(order, reason)
		}
		cluster.Count++
		cluster.Cases = append(cluster.Cases, cr.CaseID)
	}
	clusters := make([]FailureCluster, 0, len(order))
	for _, reason := range order {
		clusters = append(clusters, *byReason[reason])
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Reason < clusters[j].Reason })
	return clusters
}

// buildTunableDirections maps the diagnosis' affected tunables to actionable
// adjustment directions. Only keys inside one of the two domain allowlists are
// emitted and each is tagged with its patch space: grid_search keys may enter
// GenerateParameterPatches, prompt keys only prompt-rewrite. Keys outside both
// spaces are dropped (fail closed), so the report never mixes a prompt key
// into the automatic search space (I3 self-certifying invariant).
func (s *AttributionService) buildTunableDirections(diagnosis domain.DiagnosisReport) []TunableDirection {
	seen := map[string]bool{}
	out := make([]TunableDirection, 0, len(diagnosis.AffectedTunables))
	for _, key := range diagnosis.AffectedTunables {
		if seen[key] {
			continue
		}
		seen[key] = true
		space := tunableSpace(key)
		if space == "" {
			continue // Not actionable in either allowlist: drop rather than emit.
		}
		out = append(out, TunableDirection{
			Key:       key,
			Category:  s.tunableCategory(key),
			Space:     space,
			Direction: tunableDirectionText(key),
		})
	}
	return out
}

// tunableSpace classifies a key into its patch space by the domain allowlists.
// It must not use Category: the registry reports promptTunable.Category() as
// CatPrompt for every prompt key, while grid keys such as max_retries/top_k are
// not even registered — Category cannot tell the two spaces apart.
func tunableSpace(key string) TunableSpace {
	switch {
	case domain.IsGridSearchableParameter(key):
		return TunableSpaceGridSearch
	case domain.IsPromptTunableField(key):
		return TunableSpacePrompt
	default:
		return ""
	}
}

// tunableCategory looks up a tunable's category from the service's injected
// registry (empty when the key is not registered).
func (s *AttributionService) tunableCategory(key string) domain.TunableCategory {
	t := s.tunables.Get(key)
	if t == nil {
		return ""
	}
	return t.Category()
}

// judgeDimensionDirectionText is the suggested direction for dimension-class
// failures. It deliberately avoids a hard calibration threshold figure (the old
// "≥90%" had no spec backing, §9.2) and only describes the calibrate-then-tune
// heuristic.
const judgeDimensionDirectionText = "维度失败可能是 LLM judge 自身波动：" +
	"先对 golden 集完成一致性校准、确认 judge 可信后，再调低 judge 采样温度以降低判定方差；" +
	"在 judge 可信前勿把维度失败当 agent 缺陷"

// buildEvalChainDirections scans every failed case's attribution keys
// (FailureReason + ProcessFailure) for harness-attributable prefixes and maps
// them onto the platform-scoped evaluation.* parameters. Scanning per case —
// not per cluster primary key — keeps a safety signal such as
// process:must_not_call from being masked by the case's output failure reason
// (I2). One direction is kept per (platformKey, reason).
func buildEvalChainDirections(results []domain.EvalCaseResult) []EvalChainDirection {
	var out []EvalChainDirection
	seen := make(map[string]bool)
	for _, cr := range results {
		for _, key := range []string{cr.FailureReason, cr.ProcessFailure} {
			var d EvalChainDirection
			switch {
			case strings.HasPrefix(key, "dimension:"):
				d = EvalChainDirection{
					PlatformKey: "evaluation.judge.temperature",
					Reason:      key,
					Direction:   judgeDimensionDirectionText,
				}
			case strings.HasPrefix(key, "process:must_not_call:"):
				d = EvalChainDirection{
					PlatformKey: "evaluation.ruleguard.enabled",
					Reason:      key,
					Direction: "禁用工具被实际调用：仅过程断言事后告警不够，建议开启规则护栏 denylist，" +
						"在执行期即时硬拦截而非仅标记失败",
				}
			default:
				continue
			}
			if seen[d.PlatformKey+"|"+d.Reason] {
				continue
			}
			seen[d.PlatformKey+"|"+d.Reason] = true
			out = append(out, d)
		}
	}
	return out
}

// tunableDirectionText returns the human-facing adjustment direction for a
// tunable key. The fallback keeps any future allowlisted key readable instead
// of silently dropping it.
func tunableDirectionText(key string) string {
	if text, ok := directionText[key]; ok {
		return text
	}
	return fmt.Sprintf("调整 %s 以针对诊断出的失败模式", key)
}

// directionText holds direction copy for the keys categoryToTunables can
// produce (optimizer_diagnose.go). Keys the Diagnoser never emits were pruned
// (Minor 2) so the map cannot drift from the registry; keep this set a
// superset of categoryToTunables' output keys only.
var directionText = map[string]string{
	"temperature":              "降低温度以获得更确定性的输出",
	"max_tokens":               "增大 max_tokens 预算以容纳被截断的长输出",
	"max_context_tokens":       "增大上下文窗口预算以缓解长上下文截断",
	"max_iterations":           "增加迭代上限以容纳多步规划的收敛",
	"max_retries":              "增加重试次数以容忍瞬时工具/网关失败",
	"timeout_ms":               "增大超时预算以覆盖慢模型/慢工具响应",
	"top_k":                    "调整 top_k 检索窗口以平衡召回与精确",
	"score_threshold":          "调整 score_threshold 过滤阈值以改善检索相关性",
	"query_rewrite":            "启用查询改写以提升检索命中质量",
	"system_prompt":            "优化系统提示词以约束输出格式与安全边界",
	"memory_extraction_prompt": "优化记忆抽取提示词以减少误抽取",
	"memory_summary_prompt":    "优化记忆摘要提示词以保留关键事实",
	"memory_enrichment_prompt": "优化记忆富化提示词以提升上下文质量",
}
