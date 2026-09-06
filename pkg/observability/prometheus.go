// Package observability provides monitoring and tracing.

package observability

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// PrometheusMetrics implements MetricsProvider using Prometheus counters/histograms.
type PrometheusMetrics struct {
	reg *prometheus.Registry

	// HTTP
	httpRequestsTotal    *prometheus.CounterVec
	httpRequestDuration  *prometheus.HistogramVec
	httpRequestsInFlight prometheus.Gauge

	// Skill
	skillExecutionsTotal     *prometheus.CounterVec
	skillExecutionDuration   *prometheus.HistogramVec
	skillCircuitBreakerState *prometheus.GaugeVec

	// Agent
	agentExecutionsTotal              *prometheus.CounterVec
	agentExecutionDuration            *prometheus.HistogramVec
	agentStepCount                    *prometheus.HistogramVec
	systemAssistantRequests           *prometheus.CounterVec
	systemAssistantTTFT               *prometheus.HistogramVec
	systemAssistantSearchResults      *prometheus.HistogramVec
	systemAssistantDiagnosticDuration *prometheus.HistogramVec
	systemAssistantEvidenceGaps       *prometheus.HistogramVec
	resourceProposalsTotal            *prometheus.CounterVec
	resourceProposalReviewDuration    *prometheus.HistogramVec
	resourceProposalDraftEdits        *prometheus.HistogramVec

	// LLM – core
	llmRequestsTotal   *prometheus.CounterVec
	llmRequestDuration *prometheus.HistogramVec
	llmTokenUsage      *prometheus.CounterVec
	// LLM – 模型解析配置失效（无默认模型 / 请求失效模型）
	llmModelResolutionErrors *prometheus.CounterVec
	// LLM – policy 治理（L1-L4）
	llmPolicyBlockedTotal *prometheus.CounterVec
	llmPolicyMissingTotal *prometheus.CounterVec
	// LLM – AI-specific
	llmTokenHistogram    *prometheus.HistogramVec
	llmFirstTokenLatency *prometheus.HistogramVec

	// Knowledge / Memory
	knowledgeQueriesTotal          *prometheus.CounterVec
	knowledgeQueryDuration         *prometheus.HistogramVec
	memoryRetrievalDuration        *prometheus.HistogramVec
	knowledgeIngestTotal           *prometheus.CounterVec
	knowledgeIngestDuration        prometheus.Histogram
	knowledgeIngestInFlight        prometheus.Gauge
	knowledgeEmbedUnavailableTotal *prometheus.CounterVec

	// Hermes
	hermesEventsTotal     *prometheus.CounterVec
	hermesEventsProcessed *prometheus.CounterVec

	// Agent KPI (F3)
	agentTaskCompletedTotal *prometheus.CounterVec
	agentTaskDuration       *prometheus.HistogramVec
	agentCostPerTask        *prometheus.HistogramVec
	agentEvalScore          *prometheus.GaugeVec
	agentConversationTurns  *prometheus.HistogramVec

	// Scheduler (F3)
	scheduledFireTotal *prometheus.CounterVec

	// Reranker (F3)
	rerankRequestTotal    *prometheus.CounterVec
	rerankDurationSeconds *prometheus.HistogramVec

	// Knowledge NoAnswer / judge (F3)
	noAnswerTotal       *prometheus.CounterVec
	knowledgeJudgeTotal *prometheus.CounterVec

	// Model Router (F3)
	routeFallbackTotal *prometheus.CounterVec
	budgetRatio        *prometheus.GaugeVec

	// Model availability & fallback (P6)
	modelHealthGauge        *prometheus.GaugeVec
	memoryMigrationProgress *prometheus.GaugeVec
	memoryMigrationStalled  *prometheus.CounterVec

	// Audit (F3)
	auditEventTotal      *prometheus.CounterVec
	auditWriteQueueDepth prometheus.Gauge

	// Collab (F3)
	collabPlanTotal    *prometheus.CounterVec
	collabTaskDuration *prometheus.HistogramVec

	// Optimizer (F3)
	optimizerCandidateTotal *prometheus.CounterVec
	optimizerCycleDuration  prometheus.Histogram

	// Operation Gate (F3)
	operationProposalTotal *prometheus.CounterVec
	approvalLatency        *prometheus.HistogramVec

	// Schedule skew (F3)
	scheduleSkewSeconds prometheus.Histogram

	// Reaper
	reaperCyclesTotal    *prometheus.CounterVec
	reaperGuestsDeleted  prometheus.Counter
	reaperDeleteErrors   *prometheus.CounterVec
	reaperCycleTimestamp prometheus.Gauge

	// Background components (generic)
	componentCyclesTotal    *prometheus.CounterVec
	componentCycleTimestamp *prometheus.GaugeVec
	componentErrorsTotal    *prometheus.CounterVec

	// Goroutine panics
	goroutinePanicsTotal *prometheus.CounterVec

	// Workflow
	workflowRunsTotal   *prometheus.CounterVec
	workflowRunDuration *prometheus.HistogramVec

	// MCP internal client
	mcpClientRequestsTotal   *prometheus.CounterVec
	mcpClientReconnectsTotal *prometheus.CounterVec

	// Evaluation
	evaluationJobsTotal *prometheus.CounterVec

	// Evaluation observation（§11 运行时评估观测）
	evalObservationTotal     *prometheus.CounterVec
	evalJudgeScore           *prometheus.HistogramVec
	evalJudgeLatency         prometheus.Histogram
	evalJudgeCostTotal       prometheus.Counter
	evalJudgeFailureTotal    *prometheus.CounterVec
	evalQueueBacklog         *prometheus.GaugeVec
	evalRuleHitTotal         *prometheus.CounterVec
	evalBehaviorAnomalyTotal *prometheus.CounterVec
	evalGateActionTotal      *prometheus.CounterVec
	evalSampleCoverage       *prometheus.GaugeVec
	// P1c：评审池积压 Gauge + 升级失败 Counter（§11.3）。
	evalReviewBacklog         prometheus.Gauge
	evalReviewEscalateFailure prometheus.Counter

	// Auth
	authFailuresTotal *prometheus.CounterVec

	logger *zap.Logger
}

var (
	tokenBuckets   = []float64{64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384}
	latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}
)

// NewPrometheusMetrics registers all metrics and returns a ready MetricsProvider.
func NewPrometheusMetrics(logger *zap.Logger) *PrometheusMetrics {
	// Use a private registry so multiple instances (e.g. in tests) don't conflict.
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	m := &PrometheusMetrics{
		reg: reg,
		// HTTP
		httpRequestsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "http_requests_total", Help: "Total HTTP requests"},
			[]string{"method", "path", "status"},
		),
		httpRequestDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request duration", Buckets: prometheus.DefBuckets},
			[]string{"method", "path"},
		),
		httpRequestsInFlight: factory.NewGauge(
			prometheus.GaugeOpts{Name: "http_requests_in_flight", Help: "In-flight HTTP requests"},
		),

		// Skill
		skillExecutionsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "skill_executions_total", Help: "Total skill executions"},
			[]string{"skill_id", "skill_type", "status"},
		),
		skillExecutionDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "skill_execution_duration_seconds", Help: "Skill execution duration", Buckets: prometheus.DefBuckets},
			[]string{"skill_id"},
		),
		skillCircuitBreakerState: factory.NewGaugeVec(
			prometheus.GaugeOpts{Name: "skill_circuit_breaker_state", Help: "Circuit breaker state (0=closed,1=open,2=half_open)"},
			[]string{"skill_id"},
		),

		// Agent
		agentExecutionsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "agent_executions_total", Help: "Total agent executions"},
			[]string{"agent_id", "agent_type", "status"},
		),
		agentExecutionDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "agent_execution_duration_seconds", Help: "Agent execution duration", Buckets: latencyBuckets},
			[]string{"agent_id", "agent_type"},
		),
		agentStepCount: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "agent_step_count",
				Help:    "Number of reasoning steps per agent execution",
				Buckets: []float64{1, 2, 3, 5, 8, 13, 21, 34},
			},
			[]string{"agent_id", "agent_type"},
		),
		systemAssistantRequests: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "system_assistant_requests_total", Help: "System assistant requests by bounded role and outcome"},
			[]string{"role_class", "profile_version", "outcome"},
		),
		systemAssistantTTFT: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "system_assistant_ttft_seconds", Help: "System assistant time to first token", Buckets: latencyBuckets},
			[]string{"role_class", "profile_version"},
		),
		systemAssistantSearchResults: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "system_assistant_official_search_results", Help: "Official document search result count", Buckets: []float64{0, 1, 2, 3, 5}},
			[]string{"profile_version", "outcome"},
		),
		systemAssistantDiagnosticDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "system_assistant_diagnostic_area_duration_seconds", Help: "Diagnostic area duration", Buckets: latencyBuckets},
			[]string{"role_class", "area", "outcome"},
		),
		systemAssistantEvidenceGaps: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "system_assistant_evidence_gaps", Help: "Evidence gap count", Buckets: []float64{0, 1, 2, 3, 5}},
			[]string{"role_class", "profile_version"},
		),
		resourceProposalsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "system_assistant_resource_proposals_total", Help: "Resource proposal outcomes by bounded kind and operation"},
			[]string{"kind", "operation", "outcome"},
		),
		resourceProposalReviewDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "system_assistant_resource_proposal_review_duration_seconds", Help: "Resource proposal review duration", Buckets: latencyBuckets},
			[]string{"kind", "operation"},
		),
		resourceProposalDraftEdits: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "system_assistant_resource_proposal_draft_edits",
				Help:    "Draft edit count observed when a resource proposal is claimed",
				Buckets: []float64{0, 1, 2, 3, 5, 8},
			},
			[]string{"kind", "operation"},
		),
		// LLM – core
		llmRequestsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "llm_requests_total", Help: "Total LLM requests"},
			[]string{"model", "provider", "status"},
		),
		llmRequestDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "llm_request_duration_seconds", Help: "LLM request duration", Buckets: latencyBuckets},
			[]string{"model", "provider"},
		),
		llmTokenUsage: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "llm_token_usage_total", Help: "Cumulative LLM tokens used"},
			[]string{"model", "type"},
		),
		// LLM – AI-specific
		llmTokenHistogram: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "llm_token_count", Help: "Token count distribution per LLM call", Buckets: tokenBuckets},
			[]string{"model", "type"},
		),
		llmFirstTokenLatency: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "llm_first_token_latency_seconds", Help: "Time to first token (TTFT)", Buckets: prometheus.DefBuckets},
			[]string{"model", "provider"},
		),

		// Knowledge / Memory
		knowledgeQueriesTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "knowledge_queries_total", Help: "Total knowledge queries"},
			[]string{"query_type", "status"},
		),
		knowledgeQueryDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "knowledge_query_duration_seconds", Help: "Knowledge query duration", Buckets: prometheus.DefBuckets},
			[]string{"query_type"},
		),
		memoryRetrievalDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{Name: "memory_retrieval_duration_seconds", Help: "Memory retrieval/storage duration", Buckets: prometheus.DefBuckets},
			[]string{"operation"},
		),
		knowledgeIngestTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "knowledge_ingest_total", Help: "Total knowledge ingest jobs by terminal status"},
			[]string{"status"},
		),
		knowledgeIngestDuration: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "knowledge_ingest_duration_seconds",
				Help:    "Wall-clock duration of a knowledge ingest job (chunking + embed + persist)",
				Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600},
			},
		),
		knowledgeIngestInFlight: factory.NewGauge(
			prometheus.GaugeOpts{Name: "knowledge_ingest_in_flight", Help: "In-flight knowledge ingest jobs"},
		),
		// Hermes
		hermesEventsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "hermes_events_total", Help: "Total Hermes events published"},
			[]string{"event_type"},
		),
		hermesEventsProcessed: factory.NewCounterVec(
			prometheus.CounterOpts{Name: "hermes_events_processed_total", Help: "Total Hermes events processed"},
			[]string{"event_type", "status"},
		),

		logger: logger,
	}
	m.llmPolicyBlockedTotal, m.llmPolicyMissingTotal = newModelPolicyMetrics(factory)
	m.registerF3Metrics(factory, latencyBuckets)
	m.registerExtendedMetrics(factory)
	m.registerKnowledgeEmbedUnavailable(factory)
	return m
}

// registerKnowledgeEmbedUnavailable registers the counter backing
// StratumKnowledgeEmbedUnavailable (knowledge ingest/RAG embedding model
// unavailable, fail-closed events). Kept out of the constructor literal to
// stay under the function-length ratchet; the counter fires on the resolver
// nil path so an unregistered (nil) vector would panic at runtime.
func (m *PrometheusMetrics) registerKnowledgeEmbedUnavailable(factory promauto.Factory) {
	m.knowledgeEmbedUnavailableTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "knowledge_embed_unavailable_total",
			Help: "Knowledge ingest/RAG events where the embedding model is unavailable",
		},
		[]string{"tenant"},
	)
}

// registerEvalObservationMetrics registers the eval-observation metrics
// backing the runtime-evaluation signals (§11.1/§11.2).
func (m *PrometheusMetrics) registerEvalObservationMetrics(factory promauto.Factory) {
	m.evalObservationTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "eval_observation_total", Help: "运行态观测落库计数（§11.1）"},
		[]string{"resource", "stratum"},
	)
	m.evalJudgeScore = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "eval_judge_score", Help: "judge 单维度得分（§11.1）", Buckets: prometheus.LinearBuckets(0, 0.1, 11)},
		[]string{"resource", "dimension"},
	)
	// TODO(P1b)：evaluation.observe 采样覆盖率指标待真实计数基础设施接入后恢复。
	m.evalJudgeLatency = factory.NewHistogram(
		prometheus.HistogramOpts{Name: "eval_judge_latency_seconds", Help: "judge 调用耗时（§11.2）", Buckets: prometheus.ExponentialBuckets(0.1, 2, 8)},
	)
	m.evalJudgeCostTotal = factory.NewCounter(
		prometheus.CounterOpts{Name: "eval_judge_cost_total", Help: "judge 累计成本美元（§11.2）"},
	)
	m.evalJudgeFailureTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "eval_judge_failure_total", Help: "judge 调用失败计数（§11.2）"},
		[]string{"reason"},
	)
	m.evalQueueBacklog = factory.NewGaugeVec(
		prometheus.GaugeOpts{Name: "eval_queue_backlog", Help: "消息队列积压（§11.2）"},
		[]string{"queue"},
	)
	m.evalRuleHitTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "eval_rule_hit_total", Help: "规则护栏命中计数（§11.1）"},
		[]string{"rule", "resource", "verdict"},
	)
	m.evalBehaviorAnomalyTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "eval_behavior_anomaly_total", Help: "行为异常判异计数（§11.1）"},
		[]string{"resource", "signal"},
	)
	m.evalGateActionTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "eval_gate_action_total", Help: "分层门禁动作计数（§11.2）"},
		[]string{"layer", "action"},
	)
	m.evalSampleCoverage = factory.NewGaugeVec(
		prometheus.GaugeOpts{Name: "eval_sample_coverage", Help: "主动采样覆盖率（§11.1）"},
		[]string{"resource"},
	)
	// P1c：评审池积压 Gauge + 升级失败 Counter（§11.3）。
	m.evalReviewBacklog = factory.NewGauge(
		prometheus.GaugeOpts{Name: "eval_review_backlog", Help: "评审池待人工评审条目数（P1c 积压告警数据源）"},
	)
	m.evalReviewEscalateFailure = factory.NewCounter(
		prometheus.CounterOpts{Name: "eval_review_escalate_failure_total", Help: "评审池升级失败次数（fail-open，不阻断主流程）"},
	)
}

func newModelPolicyMetrics(factory promauto.Factory) (*prometheus.CounterVec, *prometheus.CounterVec) {
	blocked := factory.NewCounterVec(
		prometheus.CounterOpts{Name: "llm_policy_blocked_total", Help: "Requests blocked by model policy (L1-L4)"},
		[]string{"model"},
	)
	missing := factory.NewCounterVec(
		prometheus.CounterOpts{Name: "llm_policy_missing_total", Help: "Requests with no model policy record (authority missing)"},
		[]string{"model"},
	)
	return blocked, missing
}

// registerReaperMetrics registers the reaper metric family. Must not be
// inlined into registerExtendedMetrics: the reaper is a background component
// with its own alerting rules (see helm stratum-prometheusrule.yaml).
// Registration is idempotent so callers may invoke it safely more than once.
func (m *PrometheusMetrics) registerReaperMetrics(factory promauto.Factory) {
	if m.reaperCyclesTotal != nil {
		return
	}
	m.reaperCyclesTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "reaper_cycles_total", Help: "Guest reaper cycles by outcome"},
		[]string{"outcome"},
	)
	m.reaperGuestsDeleted = factory.NewCounter(
		prometheus.CounterOpts{Name: "reaper_guests_deleted_total", Help: "Expired guests deleted by the guest reaper"},
	)
	m.reaperDeleteErrors = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "reaper_delete_errors_total", Help: "Guest reaper delete errors by phase"},
		[]string{"phase"},
	)
	m.reaperCycleTimestamp = factory.NewGauge(
		prometheus.GaugeOpts{Name: "reaper_last_cycle_timestamp_seconds", Help: "Unix timestamp of the last guest reaper cycle"},
	)
}

// RegisterReaperMetrics registers the guest reaper metric family. Only the
// server process (cmd/server) hosts the guest reaper, so only it should
// export these metrics; platform-mcp and other processes must not. The method
// is safe to call repeatedly.
func (m *PrometheusMetrics) RegisterReaperMetrics() {
	m.registerReaperMetrics(promauto.With(m.reg))
}

// registerF3Metrics initializes the Phase 1 KPI / observability metrics.
func (m *PrometheusMetrics) registerF3Metrics(factory promauto.Factory, latencyBuckets []float64) {
	m.agentTaskCompletedTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "agent_task_completed_total", Help: "Agent tasks completed by type and outcome"},
		[]string{"agent_id", "agent_type", "task_kind", "outcome", "tenant_id"},
	)
	m.agentTaskDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "agent_task_duration_seconds", Help: "Agent task wall-clock duration", Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300}},
		[]string{"agent_id", "task_kind"},
	)
	m.agentCostPerTask = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "agent_cost_per_task_usd", Help: "Agent task cost in USD", Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}},
		[]string{"agent_id", "task_kind"},
	)
	m.agentEvalScore = factory.NewGaugeVec(
		prometheus.GaugeOpts{Name: "agent_eval_score", Help: "Latest agent evaluation score per metric"},
		[]string{"agent_id", "metric"},
	)
	m.agentConversationTurns = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "agent_conversation_turns", Help: "Conversation turn count per execution", Buckets: []float64{1, 2, 3, 5, 8, 13, 21, 34}},
		[]string{"agent_id"},
	)
	m.scheduledFireTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "scheduled_fire_total", Help: "Schedule fires by type and status"},
		[]string{"schedule_type", "status"},
	)
	m.rerankRequestTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "rerank_request_total", Help: "Rerank requests by tenant, model and status"},
		[]string{"tenant", "model", "status"},
	)
	m.rerankDurationSeconds = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "rerank_duration_seconds", Help: "Rerank request duration", Buckets: latencyBuckets},
		[]string{"model"},
	)
	m.noAnswerTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "knowledge_no_answer_total", Help: "RAG queries ending without an answer, by tenant and reason"},
		[]string{"tenant", "reason"},
	)
	m.knowledgeJudgeTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "knowledge_judge_total", Help: "Knowledge judge invocations by model and status"},
		[]string{"model", "status"},
	)
	m.routeFallbackTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "route_fallback_total", Help: "Model route fallback events"},
		[]string{"from_model", "to_model"},
	)
	m.budgetRatio = factory.NewGaugeVec(
		prometheus.GaugeOpts{Name: "budget_ratio", Help: "Current budget consumption ratio"},
		[]string{"scope"},
	)
	m.modelHealthGauge = factory.NewGaugeVec(
		prometheus.GaugeOpts{Name: "model_health", Help: "Current model health state (1 if the model is in that state)"},
		[]string{"model", "status"},
	)
	m.memoryMigrationProgress = factory.NewGaugeVec(
		prometheus.GaugeOpts{Name: "memory_migration_progress", Help: "Memory embedding migration backfill cursor (facts re-embedded so far)"},
		[]string{"tenant_id", "from_model", "to_model", "status"},
	)
	m.memoryMigrationStalled = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "memory_migration_stalled_total", Help: "Memory migration scan ticks with no progress advance"},
		[]string{"tenant_id", "from_model", "to_model"},
	)
	m.auditEventTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "audit_event_total", Help: "Audit events by risk level and outcome"},
		[]string{"risk", "outcome"},
	)
	m.auditWriteQueueDepth = factory.NewGauge(
		prometheus.GaugeOpts{Name: "audit_write_queue_depth", Help: "Current audit write buffer queue depth"},
	)
	m.collabPlanTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "collab_plan_total", Help: "Collaboration plans by strategy and outcome"},
		[]string{"strategy", "outcome"},
	)
	m.collabTaskDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "collab_task_duration_seconds", Help: "Collaboration task execution duration", Buckets: latencyBuckets},
		[]string{"strategy"},
	)
	m.optimizerCandidateTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "optimizer_candidate_total", Help: "Optimization candidates by strategy and outcome"},
		[]string{"strategy", "outcome"},
	)
	m.optimizerCycleDuration = factory.NewHistogram(
		prometheus.HistogramOpts{Name: "optimizer_cycle_duration_seconds", Help: "Optimizer cycle wall-clock duration", Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600}},
	)
	m.operationProposalTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "operation_proposal_total", Help: "Operation proposals by kind and outcome"},
		[]string{"kind", "outcome"},
	)
	m.approvalLatency = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "approval_latency_seconds", Help: "Approval decision latency", Buckets: latencyBuckets},
		[]string{"kind"},
	)
	m.scheduleSkewSeconds = factory.NewHistogram(
		prometheus.HistogramOpts{Name: "schedule_skew_seconds", Help: "Schedule fire time skew", Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300}},
	)
}

// registerExtendedMetrics registers metrics added after the initial
// implementation to keep NewPrometheusMetrics under the file-wide
// 120-line ratchet limit.
func (m *PrometheusMetrics) registerExtendedMetrics(factory promauto.Factory) {
	m.llmModelResolutionErrors = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "llm_model_resolution_errors_total", Help: "LLM model resolution configuration failures (no default model or invalid requested model)"},
		[]string{"model", "reason"},
	)
	m.componentCyclesTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "component_cycles_total", Help: "Background component cycles by outcome"},
		[]string{"component", "outcome"},
	)
	m.componentCycleTimestamp = factory.NewGaugeVec(
		prometheus.GaugeOpts{Name: "component_last_cycle_timestamp_seconds", Help: "Unix timestamp of last component cycle"},
		[]string{"component"},
	)
	m.componentErrorsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "component_errors_total", Help: "Component errors by phase"},
		[]string{"component", "phase"},
	)
	m.goroutinePanicsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "goroutine_panics_total", Help: "Total goroutine panics recovered"},
		[]string{"component"},
	)
	m.workflowRunsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "workflow_runs_total", Help: "Total workflow runs by status"},
		[]string{"tenant_id", "status"},
	)
	m.workflowRunDuration = factory.NewHistogramVec(
		prometheus.HistogramOpts{Name: "workflow_run_duration_seconds", Help: "Workflow run duration", Buckets: latencyBuckets},
		[]string{"tenant_id"},
	)
	m.mcpClientRequestsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "mcp_client_requests_total", Help: "Internal MCP client requests by operation and status"},
		[]string{"server_name", "operation", "status"},
	)
	m.mcpClientReconnectsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "mcp_client_reconnects_total", Help: "Internal MCP client reconnect attempts"},
		[]string{"server_name"},
	)
	m.evaluationJobsTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "evaluation_jobs_total", Help: "Evaluation jobs by outcome"},
		[]string{"status"},
	)
	m.authFailuresTotal = factory.NewCounterVec(
		prometheus.CounterOpts{Name: "auth_failures_total", Help: "Auth failures by reason"},
		[]string{"reason"},
	)
	m.registerEvalObservationMetrics(factory)
}

// Registerer returns the private prometheus.Registerer so callers (e.g. pipeline)
// can register their own metrics against the same registry.
func (m *PrometheusMetrics) Registerer() prometheus.Registerer { return m.reg }

// GetHandler returns a Prometheus scrape handler scoped to this instance's registry.
func (m *PrometheusMetrics) GetHandler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// --- HTTP ---

func (m *PrometheusMetrics) IncHTTPRequest(method, path string, statusCode int) {
	if statusCode <= 0 {
		statusCode = 200
	}
	m.httpRequestsTotal.WithLabelValues(method, path, strconv.Itoa(statusCode/100)+"xx").Inc()
}

func (m *PrometheusMetrics) RecordHTTPRequestDuration(method, path string, duration float64) {
	m.httpRequestDuration.WithLabelValues(method, path).Observe(duration)
}

func (m *PrometheusMetrics) IncHTTPRequestsInFlight() { m.httpRequestsInFlight.Inc() }
func (m *PrometheusMetrics) DecHTTPRequestsInFlight() { m.httpRequestsInFlight.Dec() }

// --- Skill ---

func (m *PrometheusMetrics) IncSkillExecution(skillID, skillType, status string) {
	m.skillExecutionsTotal.WithLabelValues(skillID, skillType, status).Inc()
}

func (m *PrometheusMetrics) RecordSkillExecutionDuration(skillID string, duration float64) {
	m.skillExecutionDuration.WithLabelValues(skillID).Observe(duration)
}

func (m *PrometheusMetrics) SetSkillCircuitBreakerState(skillID string, state float64) {
	m.skillCircuitBreakerState.WithLabelValues(skillID).Set(state)
}

// --- Agent ---

func (m *PrometheusMetrics) IncAgentExecution(agentID, agentType, status string) {
	m.agentExecutionsTotal.WithLabelValues(agentID, agentType, status).Inc()
}

func (m *PrometheusMetrics) RecordAgentExecutionDuration(agentID, agentType string, duration float64) {
	m.agentExecutionDuration.WithLabelValues(agentID, agentType).Observe(duration)
}

func (m *PrometheusMetrics) RecordAgentStepCount(agentID, agentType string, steps int) {
	m.agentStepCount.WithLabelValues(agentID, agentType).Observe(float64(steps))
}

func (m *PrometheusMetrics) IncSystemAssistantRequest(roleClass, profileVersion, outcome string) {
	m.systemAssistantRequests.WithLabelValues(roleClass, profileVersion, outcome).Inc()
}

func (m *PrometheusMetrics) RecordSystemAssistantTTFT(roleClass, profileVersion string, duration float64) {
	m.systemAssistantTTFT.WithLabelValues(roleClass, profileVersion).Observe(duration)
}

func (m *PrometheusMetrics) RecordOfficialDocsSearchResults(profileVersion, outcome string, count int) {
	m.systemAssistantSearchResults.WithLabelValues(profileVersion, outcome).Observe(float64(count))
}

func (m *PrometheusMetrics) RecordSystemAssistantDiagnosticArea(roleClass, area, outcome string, duration float64) {
	m.systemAssistantDiagnosticDuration.WithLabelValues(roleClass, area, outcome).Observe(duration)
}

func (m *PrometheusMetrics) RecordSystemAssistantEvidenceGaps(roleClass, profileVersion string, count int) {
	m.systemAssistantEvidenceGaps.WithLabelValues(roleClass, profileVersion).Observe(float64(count))
}

func (m *PrometheusMetrics) IncResourceProposal(kind, operation, outcome string) {
	m.resourceProposalsTotal.WithLabelValues(kind, operation, outcome).Inc()
}

func (m *PrometheusMetrics) RecordResourceProposalReviewDuration(kind, operation string, duration float64) {
	m.resourceProposalReviewDuration.WithLabelValues(kind, operation).Observe(duration)
}

func (m *PrometheusMetrics) RecordResourceProposalDraftEdits(kind, operation string, count int) {
	m.resourceProposalDraftEdits.WithLabelValues(kind, operation).Observe(float64(count))
}

// --- LLM ---

func (m *PrometheusMetrics) IncLLMRequest(model, provider, status string) {
	m.llmRequestsTotal.WithLabelValues(model, provider, status).Inc()
}

// IncLLMModelResolutionError 记录模型解析配置失效（no_default / invalid_model）。
// 配置层缺陷必须可观测：该指标接入告警规则，禁止代码内写死兜底模型。
func (m *PrometheusMetrics) IncLLMModelResolutionError(model, reason string) {
	m.llmModelResolutionErrors.WithLabelValues(model, reason).Inc()
}

func (m *PrometheusMetrics) RecordLLMRequestDuration(model, provider string, duration float64) {
	m.llmRequestDuration.WithLabelValues(model, provider).Observe(duration)
}

func (m *PrometheusMetrics) IncLLMTokenUsage(model, tokenType string, count int64) {
	m.llmTokenUsage.WithLabelValues(model, tokenType).Add(float64(count))
}

func (m *PrometheusMetrics) RecordLLMTokenHistogram(model, tokenType string, count float64) {
	m.llmTokenHistogram.WithLabelValues(model, tokenType).Observe(count)
}

func (m *PrometheusMetrics) RecordLLMFirstTokenLatency(model, provider string, latency float64) {
	m.llmFirstTokenLatency.WithLabelValues(model, provider).Observe(latency)
}

// --- Knowledge / Memory ---

func (m *PrometheusMetrics) IncKnowledgeQuery(queryType, status string) {
	m.knowledgeQueriesTotal.WithLabelValues(queryType, status).Inc()
}

func (m *PrometheusMetrics) RecordKnowledgeQueryDuration(queryType string, duration float64) {
	m.knowledgeQueryDuration.WithLabelValues(queryType).Observe(duration)
}

func (m *PrometheusMetrics) IncKnowledgeIngest(status string) {
	m.knowledgeIngestTotal.WithLabelValues(status).Inc()
}

func (m *PrometheusMetrics) IncKnowledgeEmbedUnavailable(tenantID string) {
	m.knowledgeEmbedUnavailableTotal.WithLabelValues(tenantID).Inc()
}

func (m *PrometheusMetrics) RecordKnowledgeIngestDuration(duration float64) {
	m.knowledgeIngestDuration.Observe(duration)
}

func (m *PrometheusMetrics) IncKnowledgeIngestInFlight() { m.knowledgeIngestInFlight.Inc() }
func (m *PrometheusMetrics) DecKnowledgeIngestInFlight() { m.knowledgeIngestInFlight.Dec() }

func (m *PrometheusMetrics) RecordMemoryRetrievalDuration(operation string, duration float64) {
	m.memoryRetrievalDuration.WithLabelValues(operation).Observe(duration)
}

// --- Hermes ---

func (m *PrometheusMetrics) IncHermesEvent(eventType string) {
	m.hermesEventsTotal.WithLabelValues(eventType).Inc()
}

func (m *PrometheusMetrics) IncHermesEventProcessed(eventType, status string) {
	m.hermesEventsProcessed.WithLabelValues(eventType, status).Inc()
}

// --- Agent KPI (F3) ---

func (m *PrometheusMetrics) IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string) {
	m.agentTaskCompletedTotal.WithLabelValues(agentID, agentType, taskKind, outcome, tenantID).Inc()
}

func (m *PrometheusMetrics) RecordAgentTaskLatency(agentID, taskKind string, seconds float64) {
	m.agentTaskDuration.WithLabelValues(agentID, taskKind).Observe(seconds)
}

func (m *PrometheusMetrics) RecordAgentCostPerTask(agentID, taskKind string, costUSD float64) {
	m.agentCostPerTask.WithLabelValues(agentID, taskKind).Observe(costUSD)
}

func (m *PrometheusMetrics) RecordAgentEvalScore(agentID, metric string, score float64) {
	m.agentEvalScore.WithLabelValues(agentID, metric).Set(score)
}

func (m *PrometheusMetrics) RecordAgentConversationTurn(agentID string, turnCount int) {
	m.agentConversationTurns.WithLabelValues(agentID).Observe(float64(turnCount))
}

// --- Scheduler (F3) ---

func (m *PrometheusMetrics) IncScheduledFire(scheduleType, status string) {
	m.scheduledFireTotal.WithLabelValues(scheduleType, status).Inc()
}

// --- Reranker (F3) ---

func (m *PrometheusMetrics) IncRerankRequest(tenantID, model, status string) {
	m.rerankRequestTotal.WithLabelValues(tenantID, model, status).Inc()
}

func (m *PrometheusMetrics) RecordRerankDuration(model string, seconds float64) {
	m.rerankDurationSeconds.WithLabelValues(model).Observe(seconds)
}

// --- Knowledge NoAnswer / judge (F3) ---

func (m *PrometheusMetrics) IncNoAnswer(tenantID, reason string) {
	m.noAnswerTotal.WithLabelValues(tenantID, reason).Inc()
}

func (m *PrometheusMetrics) IncKnowledgeJudge(model, status string) {
	m.knowledgeJudgeTotal.WithLabelValues(model, status).Inc()
}

// --- Model Router (F3) ---

func (m *PrometheusMetrics) IncRouteFallback(fromModel, toModel string) {
	m.routeFallbackTotal.WithLabelValues(fromModel, toModel).Inc()
}

func (m *PrometheusMetrics) RecordBudgetRatio(scope string, pct float64) {
	m.budgetRatio.WithLabelValues(scope).Set(pct)
}

// --- Model availability & fallback (P6) ---

// RecordModelHealthTransition 把模型健康状态 gauge 置为 from=0 / to=1（kube-state
// 语义）。from==to 时仍刷新 to=1，让持续健康/持续降级模型保持活络时间序列，
// 供「unhealthy 持续 5min」类告警在该状态上做 for 判定。
func (m *PrometheusMetrics) RecordModelHealthTransition(model, from, to string) {
	if from != "" && from != to {
		m.modelHealthGauge.WithLabelValues(model, from).Set(0)
	}
	m.modelHealthGauge.WithLabelValues(model, to).Set(1)
}

func (m *PrometheusMetrics) SetMemoryMigrationProgress(tenantID, fromModel, toModel, status string, progress int) {
	m.memoryMigrationProgress.WithLabelValues(tenantID, fromModel, toModel, status).Set(float64(progress))
}

func (m *PrometheusMetrics) IncMemoryMigrationStalled(tenantID, fromModel, toModel string) {
	m.memoryMigrationStalled.WithLabelValues(tenantID, fromModel, toModel).Inc()
}

// --- Model policy (L1-L4) ---

func (m *PrometheusMetrics) IncPolicyBlocked(model string) {
	m.llmPolicyBlockedTotal.WithLabelValues(model).Inc()
}

func (m *PrometheusMetrics) IncPolicyMissing(model string) {
	m.llmPolicyMissingTotal.WithLabelValues(model).Inc()
}

// --- Audit (F3) ---

func (m *PrometheusMetrics) IncAuditEvent(risk, outcome string) {
	m.auditEventTotal.WithLabelValues(risk, outcome).Inc()
}

func (m *PrometheusMetrics) RecordAuditWriteQueueDepth(depth int) {
	m.auditWriteQueueDepth.Set(float64(depth))
}

// --- Collab (F3) ---

func (m *PrometheusMetrics) IncCollabPlan(strategy, outcome string) {
	m.collabPlanTotal.WithLabelValues(strategy, outcome).Inc()
}

func (m *PrometheusMetrics) RecordCollabTaskDuration(strategy string, seconds float64) {
	m.collabTaskDuration.WithLabelValues(strategy).Observe(seconds)
}

// --- Optimizer (F3) ---

func (m *PrometheusMetrics) IncOptimizerCandidate(strategy, outcome string) {
	m.optimizerCandidateTotal.WithLabelValues(strategy, outcome).Inc()
}

func (m *PrometheusMetrics) RecordOptimizerCycleDuration(seconds float64) {
	m.optimizerCycleDuration.Observe(seconds)
}

// --- Operation Gate (F3) ---

func (m *PrometheusMetrics) IncOperationProposal(kind, outcome string) {
	m.operationProposalTotal.WithLabelValues(kind, outcome).Inc()
}

func (m *PrometheusMetrics) RecordApprovalLatency(kind string, seconds float64) {
	m.approvalLatency.WithLabelValues(kind).Observe(seconds)
}

// --- Schedule skew (F3) ---

func (m *PrometheusMetrics) RecordScheduleSkew(skewSeconds float64) {
	m.scheduleSkewSeconds.Observe(skewSeconds)
}

// --- Reaper ---

func (m *PrometheusMetrics) IncReaperCycle(outcome string) {
	m.reaperCyclesTotal.WithLabelValues(outcome).Inc()
}

func (m *PrometheusMetrics) SetReaperCycleTimestamp(ts float64) {
	m.reaperCycleTimestamp.Set(ts)
}

func (m *PrometheusMetrics) IncReaperGuestDeleted() {
	m.reaperGuestsDeleted.Inc()
}

func (m *PrometheusMetrics) IncReaperDeleteError(phase string) {
	m.reaperDeleteErrors.WithLabelValues(phase).Inc()
}

// --- Background components (generic) ---

func (m *PrometheusMetrics) RecordComponentCycle(component string) {
	m.componentCyclesTotal.WithLabelValues(component, "ok").Inc()
}

func (m *PrometheusMetrics) SetComponentCycleTimestamp(component string, ts float64) {
	m.componentCycleTimestamp.WithLabelValues(component).Set(ts)
}

func (m *PrometheusMetrics) IncComponentError(component, phase string) {
	m.componentErrorsTotal.WithLabelValues(component, phase).Inc()
	m.componentCyclesTotal.WithLabelValues(component, "error").Inc()
}

// --- Goroutine panics ---

func (m *PrometheusMetrics) IncGoroutinePanic(component string) {
	m.goroutinePanicsTotal.WithLabelValues(component).Inc()
}

// --- Workflow ---

func (m *PrometheusMetrics) IncWorkflowRun(tenantID, status string) {
	m.workflowRunsTotal.WithLabelValues(tenantID, status).Inc()
}

func (m *PrometheusMetrics) RecordWorkflowRunDuration(tenantID string, duration float64) {
	m.workflowRunDuration.WithLabelValues(tenantID).Observe(duration)
}

// --- MCP internal client ---

func (m *PrometheusMetrics) IncMCPClientRequest(serverName, operation, status string) {
	m.mcpClientRequestsTotal.WithLabelValues(serverName, operation, status).Inc()
}

func (m *PrometheusMetrics) IncMCPClientReconnect(serverName string) {
	m.mcpClientReconnectsTotal.WithLabelValues(serverName).Inc()
}

// --- Evaluation ---

func (m *PrometheusMetrics) IncEvaluationJob(status string) {
	m.evaluationJobsTotal.WithLabelValues(status).Inc()
}

// --- Evaluation observation（§11） ---

func (m *PrometheusMetrics) IncEvalObservation(resource, stratum string) {
	m.evalObservationTotal.WithLabelValues(resource, stratum).Inc()
}

func (m *PrometheusMetrics) RecordEvalJudgeScore(resource, dimension string, score float64) {
	m.evalJudgeScore.WithLabelValues(resource, dimension).Observe(score)
}

func (m *PrometheusMetrics) RecordEvalJudgeLatency(seconds float64) {
	m.evalJudgeLatency.Observe(seconds)
}

func (m *PrometheusMetrics) RecordEvalJudgeCost(costUSD float64) {
	m.evalJudgeCostTotal.Add(costUSD)
}

func (m *PrometheusMetrics) IncEvalJudgeFailure(reason string) {
	m.evalJudgeFailureTotal.WithLabelValues(reason).Inc()
}

func (m *PrometheusMetrics) SetEvalQueueBacklog(queue string, count int64) {
	m.evalQueueBacklog.WithLabelValues(queue).Set(float64(count))
}

func (m *PrometheusMetrics) IncEvalRuleHit(rule, resource, verdict string) {
	if m.evalRuleHitTotal != nil {
		m.evalRuleHitTotal.WithLabelValues(rule, resource, verdict).Inc()
	}
}

func (m *PrometheusMetrics) IncEvalBehaviorAnomaly(resource, signal string) {
	if m.evalBehaviorAnomalyTotal != nil {
		m.evalBehaviorAnomalyTotal.WithLabelValues(resource, signal).Inc()
	}
}

func (m *PrometheusMetrics) IncEvalGateAction(layer, action string) {
	if m.evalGateActionTotal != nil {
		m.evalGateActionTotal.WithLabelValues(layer, action).Inc()
	}
}

func (m *PrometheusMetrics) RecordEvalSampleCoverage(resource string, ratio float64) {
	if m.evalSampleCoverage != nil {
		m.evalSampleCoverage.WithLabelValues(resource).Set(ratio)
	}
}

func (m *PrometheusMetrics) SetEvalReviewBacklog(count int64) {
	if m.evalReviewBacklog != nil {
		m.evalReviewBacklog.Set(float64(count))
	}
}

func (m *PrometheusMetrics) IncEvalReviewEscalateFailure() {
	if m.evalReviewEscalateFailure != nil {
		m.evalReviewEscalateFailure.Inc()
	}
}

// --- Auth ---

func (m *PrometheusMetrics) IncAuthFailure(reason string) {
	m.authFailuresTotal.WithLabelValues(reason).Inc()
}
