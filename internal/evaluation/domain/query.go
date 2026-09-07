package domain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidCenterQuery     = errors.New("invalid evaluation center query")
	ErrCenterResourceNotFound = errors.New("evaluation center resource not found")
)

type CenterCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func EncodeCenterCursor(createdAt time.Time, id string) string {
	b, _ := json.Marshal(CenterCursor{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeCenterCursor(value string) (CenterCursor, error) {
	var cursor CenterCursor
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.CreatedAt.IsZero() || strings.TrimSpace(cursor.ID) == "" {
		return cursor, ErrInvalidCenterQuery
	}
	return cursor, nil
}

type CenterOverview struct {
	Resources   int `json:"resources"`
	Suites      int `json:"suites"`
	Runs        int `json:"runs"`
	Candidates  int `json:"candidates"`
	Experiments int `json:"experiments"`
}

// CenterResourceKey 唯一标识评测中心一个被测资源行（kind+id）。跨模块真名解析
// （port.CenterResourceNamer）以它为键批量解析，映射回各资源行 DTO 的
// ResourceName 显示字段；未解析到的键在返回 map 中缺席（前端显式占位 —）。
type CenterResourceKey struct {
	Kind       ResourceKind
	ResourceID string
}

type ResourceSummary struct {
	ID               string         `json:"id"`
	ResourceID       string         `json:"resource_id"`
	ResourceName     string         `json:"resource_name,omitempty"`
	Status           string         `json:"status"`
	StableRevisionID string         `json:"stable_revision_id,omitempty"`
	LatestRunStatus  string         `json:"latest_run_status,omitempty"`
	ResourceKind     ResourceKind   `json:"resource_kind"`
	SafeSummary      map[string]any `json:"safe_summary"`
	CreatedAt        time.Time      `json:"created_at"`
}

type SuiteSummary struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Description      string       `json:"description"`
	ResourceKind     ResourceKind `json:"resource_kind,omitempty"`
	Status           string       `json:"status"`
	ActiveRevisionID string       `json:"active_revision_id,omitempty"`
	DraftRevisionID  string       `json:"draft_revision_id,omitempty"`
	ActiveVersionNo  int          `json:"active_version_no,omitempty"`
	DraftVersionNo   int          `json:"draft_version_no,omitempty"`
	// ActiveCaseCount / DraftCaseCount 是 active/draft revision 的启用 case 数
	// （与 SuiteRevisionMeta.EnabledCaseCount 同口径）；无该 revision 时为 0。
	ActiveCaseCount int       `json:"active_case_count,omitempty"`
	DraftCaseCount  int       `json:"draft_case_count,omitempty"`
	CreatedBy       string    `json:"created_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// SuiteDetail 是评测集详情页顶部元信息：套件自身字段叠加当前 active/draft
// revision 的 kind/版本号/启用 case 数。case 正文不进本结构（草稿/版本正文走
// 各自只读/编辑端点装载），避免详情元信息与正文两份读取互相拖累。
type SuiteDetail struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Description      string       `json:"description"`
	ResourceKind     ResourceKind `json:"resource_kind"`
	Status           string       `json:"status"`
	ActiveRevisionID string       `json:"active_revision_id,omitempty"`
	DraftRevisionID  string       `json:"draft_revision_id,omitempty"`
	ActiveVersionNo  int          `json:"active_version_no,omitempty"`
	DraftVersionNo   int          `json:"draft_version_no,omitempty"`
	ActiveCaseCount  int          `json:"active_case_count,omitempty"`
	DraftCaseCount   int          `json:"draft_case_count,omitempty"`
	CreatedBy        string       `json:"created_by,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
}
type RunSummary struct {
	ID           string       `json:"id"`
	ResourceID   string       `json:"resource_id"`
	ResourceName string       `json:"resource_name,omitempty"`
	RevisionID   string       `json:"revision_id"`
	Status       string       `json:"status"`
	ResourceKind ResourceKind `json:"resource_kind"`
	Passed       bool         `json:"passed"`
	TotalCases   int          `json:"total_cases"`
	PassedCases  int          `json:"passed_cases"`
	CreatedBy    string       `json:"created_by,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
}
type CandidateSummary struct {
	ID               string            `json:"id"`
	ResourceID       string            `json:"resource_id"`
	ResourceName     string            `json:"resource_name,omitempty"`
	RevisionID       string            `json:"revision_id"`
	ParentRevisionID string            `json:"parent_revision_id"`
	Source           string            `json:"source"`
	Status           string            `json:"status"`
	ResourceKind     ResourceKind      `json:"resource_kind"`
	Rank             *int              `json:"rank,omitempty"`
	StateVersion     int64             `json:"state_version"`
	SafeDiff         CandidateSafeDiff `json:"safe_diff"`
	CreatedBy        string            `json:"created_by,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type SafeFieldChange struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

type CandidateSafeDiff struct {
	ChangedFields []string                   `json:"changed_fields"`
	Changes       map[string]SafeFieldChange `json:"changes"`
	ParentMissing bool                       `json:"parent_missing"`
}
type ExperimentSummary struct {
	ID                string            `json:"id"`
	ResourceID        string            `json:"resource_id"`
	ResourceName      string            `json:"resource_name,omitempty"`
	StableRevisionID  string            `json:"stable_revision_id"`
	CanaryRevisionID  string            `json:"canary_revision_id"`
	Status            string            `json:"status"`
	Recommendation    string            `json:"recommendation"`
	ResourceKind      ResourceKind      `json:"resource_kind"`
	StagePercent      int               `json:"stage_percent"`
	SafetyStopped     bool              `json:"safety_stopped"`
	StateVersion      int64             `json:"state_version"`
	PromotionEvidence PromotionEvidence `json:"promotion_evidence"`
	CreatedBy         string            `json:"created_by,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
}

type TimelineEvent struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Status       string       `json:"status"`
	Summary      string       `json:"summary"`
	ResourceID   string       `json:"resource_id"`
	ResourceKind ResourceKind `json:"resource_kind"`
	CreatedAt    time.Time    `json:"created_at"`
}

type ResourcePage struct {
	Items      []ResourceSummary `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}
type SuitePage struct {
	Items      []SuiteSummary `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}
type RunPage struct {
	Items      []RunSummary `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}
type CandidatePage struct {
	Items      []CandidateSummary `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}
type ExperimentPage struct {
	Items      []ExperimentSummary `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}
type TimelinePage struct {
	Items      []TimelineEvent `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// ---- 评测指标监控面板（spec 2026-09-03 §4.2/§4.3）----

// QualityDim 单 judge 语义维度聚合。score 已 0/1 归一，pass_rate 与 avg_score
// 数值同源（SQL avg(score)）；仅窗口内实际出现过的维度出现，未出现维度不返回。
type QualityDim struct {
	Dimension     string  `json:"dimension"`
	PassRate      float64 `json:"pass_rate"`
	AvgScore      float64 `json:"avg_score"`
	AvgConfidence float64 `json:"avg_confidence"`
	Samples       int     `json:"samples"`
}

// VerdictDistribution verdict 三态分布计数。
type VerdictDistribution struct {
	Pass  int `json:"pass"`
	Flag  int `json:"flag"`
	Block int `json:"block"`
}

// BehaviorStats 行为区聚合。rule/behavior 未装配时计数为 0 属正常（非错误）。
type BehaviorStats struct {
	RuleHits         int                 `json:"rule_hits"`
	RetryCount       int                 `json:"retry_count"`
	EscalationCount  int                 `json:"escalation_count"`
	AbandonmentCount int                 `json:"abandonment_count"`
	Verdict          VerdictDistribution `json:"verdict"`
}

// CostStats 成本与延迟聚合。latency 无有效样本时为 null（诚实空态，不伪 0）。
type CostStats struct {
	TotalTokens  int64    `json:"total_tokens"`
	TotalCostUSD float64  `json:"total_cost_usd"`
	AvgLatencyMS *float64 `json:"avg_latency_ms"`
	P95LatencyMS *float64 `json:"p95_latency_ms"`
}

// ProcessBaseline 窗口内最近一条 succeeded 评测 run 的过程通过率基线；无 run 时
// process=null（面板显示「窗口内无评测」）。
type ProcessBaseline struct {
	ProcessPassRate float64   `json:"process_pass_rate"`
	RunID           string    `json:"run_id"`
	RunCreatedAt    time.Time `json:"run_created_at"`
}

// MonitorResourceSummary 端点 1 资源行四区摘要。
type MonitorResourceSummary struct {
	ResourceKind ResourceKind     `json:"resource_kind"`
	ResourceID   string           `json:"resource_id"`
	ResourceName string           `json:"resource_name,omitempty"`
	SampleCount  int              `json:"sample_count"`
	Quality      []QualityDim     `json:"quality"`
	Behavior     BehaviorStats    `json:"behavior"`
	Cost         CostStats        `json:"cost"`
	Process      *ProcessBaseline `json:"process"`
}

// MonitorWindow 响应实际生效窗口（含 service 兜底默认近 7 天后的值）。
type MonitorWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// MonitorResourcesPage 端点 1 响应。
type MonitorResourcesPage struct {
	Items  []MonitorResourceSummary `json:"items"`
	Window MonitorWindow            `json:"window"`
}

// MonitorTrendPoint 端点 2 series 的单日桶聚合。
type MonitorTrendPoint struct {
	BucketAt    time.Time     `json:"bucket_at"`
	SampleCount int           `json:"sample_count"`
	Quality     []QualityDim  `json:"quality"`
	Behavior    BehaviorStats `json:"behavior"`
	Cost        CostStats     `json:"cost"`
}

// RunProcessPoint 端点 2 runs：该资源窗口内 succeeded run 过程基线离散点。
type RunProcessPoint struct {
	RunID           string    `json:"run_id"`
	ProcessPassRate float64   `json:"process_pass_rate"`
	RunCreatedAt    time.Time `json:"run_created_at"`
}

// MonitorTrendSeries 端点 2 响应。
type MonitorTrendSeries struct {
	ResourceKind ResourceKind        `json:"resource_kind"`
	ResourceID   string              `json:"resource_id"`
	Series       []MonitorTrendPoint `json:"series"`
	Runs         []RunProcessPoint   `json:"runs"`
}

// ---- 版本引用账本（里程碑 7：单 eval 版本引用 usage 与通过率摘要）----

// RevisionSummary 是被测资源单个 eval 版本行（资源详情「版本引用账本」表用，
// 含零引用版本）。safe_summary 携带 version_label/resource_name 等脱敏元信息，
// 前端按 status 对比 deployment 标记「当前稳定」。
type RevisionSummary struct {
	ID               string         `json:"id"`
	ResourceKind     ResourceKind   `json:"resource_kind"`
	ResourceID       string         `json:"resource_id"`
	ResourceName     string         `json:"resource_name,omitempty"`
	ParentRevisionID string         `json:"parent_revision_id,omitempty"`
	Source           string         `json:"source"`
	Status           string         `json:"status"`
	SafeSummary      map[string]any `json:"safe_summary"`
	CreatedBy        string         `json:"created_by,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

// RevisionPage (0) 端点资源 eval 版本表分页响应。
type RevisionPage struct {
	Items      []RevisionSummary `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// RevisionDeployment 是所查版本在 evaluation_deployments 中的角色投影；无部署行时
// references.deployment 为 null。Role 取值 stable|canary|both（同一版本同时作稳定与
// 金丝雀臂的退化态）。
type RevisionDeployment struct {
	Role             string `json:"role"`
	StableRevisionID string `json:"stable_revision_id,omitempty"`
	CanaryRevisionID string `json:"canary_revision_id,omitempty"`
	CanaryPercent    int    `json:"canary_percent"`
}

// RevisionPinnedRun 是「把本版本作为绑定资源 pin 进其它 run」的一条引用行：本版本
// 不是该 run 的被测主体，而是被测主体执行时固化的绑定 skill/mcp/knowledge 版本。
// 判定走 Go decode context_snapshot.PinnedAssignments 值命中，不依赖落库 JSON 键大小写。
type RevisionPinnedRun struct {
	RunID        string       `json:"run_id"`
	ResourceKind ResourceKind `json:"resource_kind"` // 执行评测的主体资源
	ResourceID   string       `json:"resource_id"`
	ResourceName string       `json:"resource_name,omitempty"`
	Status       string       `json:"status"`
	Passed       bool         `json:"passed"`
	TotalCases   int          `json:"total_cases"`
	PassedCases  int          `json:"passed_cases"`
	CreatedAt    time.Time    `json:"created_at"`
}

// RevisionCandidateRef 是引用本版本的优化候选行：Role=candidate 表示本版本是候选版本
// （revision_id 命中），Role=baseline 表示本版本是候选的父基线（parent_revision_id 命中）。
type RevisionCandidateRef struct {
	ID               string    `json:"id"`
	RevisionID       string    `json:"revision_id"`
	ParentRevisionID string    `json:"parent_revision_id"`
	Role             string    `json:"role"`
	Source           string    `json:"source"`
	Status           string    `json:"status"`
	Rank             *int      `json:"rank,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// RevisionExperimentRef 是引用本版本的实验行：Role=stable|canary|both。
type RevisionExperimentRef struct {
	ID               string    `json:"id"`
	Role             string    `json:"role"`
	StableRevisionID string    `json:"stable_revision_id"`
	CanaryRevisionID string    `json:"canary_revision_id"`
	Status           string    `json:"status"`
	StagePercent     int       `json:"stage_percent"`
	Recommendation   string    `json:"recommendation"`
	CreatedAt        time.Time `json:"created_at"`
}

// RevisionReferences (c) 端点：单 eval 版本的全量引用方账本。subject_runs 是被评主体
// 版本即本版本的 run（eval_runs.revision_id 命中）；pinned_runs 是把它当绑定资源 pin
// 的其它 run；candidates/experiments 是本版本作为候选/基线或实验臂的演进引用。各明细
// 数组始终为非 nil（无引用 → 空数组）；deployment 无部署行时为 null（诚实空态）。
type RevisionReferences struct {
	Deployment  *RevisionDeployment     `json:"deployment"`
	SubjectRuns []RunSummary            `json:"subject_runs"`
	PinnedRuns  []RevisionPinnedRun     `json:"pinned_runs"`
	Candidates  []RevisionCandidateRef  `json:"candidates"`
	Experiments []RevisionExperimentRef `json:"experiments"`
}

// RevisionPassRate (d) 端点：单 eval 版本通过率摘要。总/成功 run 数按该版本全部
// status 统计；用例 a/b 仅聚合 succeeded run；pass_rate=成功 run 用例通过合计/用例
// 合计，0 次成功或用例合计为 0 → null（诚实空态，不伪 0）。recent_runs 最近
// EvalReferenceRecentRunsLimit 条（含非成功，created_at 降序），前端筛 succeeded 绘点。
type RevisionPassRate struct {
	SucceededRuns int          `json:"succeeded_runs"`
	TotalRuns     int          `json:"total_runs"`
	PassedCases   int          `json:"passed_cases"`
	TotalCases    int          `json:"total_cases"`
	PassRate      *float64     `json:"pass_rate"`
	RecentRuns    []RunSummary `json:"recent_runs"`
}
