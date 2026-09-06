package observability

import (
	"math"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
)

// exerciseAllMetrics calls every MetricsProvider method with representative
// dummy arguments. Any metric that is declared but never registered makes the
// underlying Prometheus vector nil and panics, which fails this test.
func exerciseAllMetrics(m MetricsProvider) {
	// HTTP
	m.IncHTTPRequest("GET", "/health", 200)
	m.RecordHTTPRequestDuration("GET", "/health", 0.1)
	m.IncHTTPRequestsInFlight()
	m.DecHTTPRequestsInFlight()
	// Skill
	m.IncSkillExecution("skill-1", "rag", "ok")
	m.RecordSkillExecutionDuration("skill-1", 0.1)
	m.SetSkillCircuitBreakerState("skill-1", 0)
	// Agent
	m.IncAgentExecution("agent-1", "react", "ok")
	m.RecordAgentExecutionDuration("agent-1", "react", 0.1)
	m.RecordAgentStepCount("agent-1", "react", 3)
	m.IncSystemAssistantRequest("admin", "v1", "ok")
	m.RecordSystemAssistantTTFT("admin", "v1", 0.2)
	m.RecordOfficialDocsSearchResults("v1", "ok", 2)
	m.RecordSystemAssistantDiagnosticArea("admin", "security", "ok", 0.3)
	m.RecordSystemAssistantEvidenceGaps("v1", "ok", 1)
	m.IncResourceProposal("memory", "create", "approved")
	m.RecordResourceProposalReviewDuration("memory", "create", 0.4)
	m.RecordResourceProposalDraftEdits("memory", "create", 2)
	// LLM
	m.IncLLMRequest("qwen-plus", "qwen", "ok")
	m.IncLLMModelResolutionError("", "no_default")
	m.IncLLMModelResolutionError("ghost", "invalid_model")
	m.RecordLLMRequestDuration("qwen-plus", "qwen", 0.5)
	m.IncLLMTokenUsage("qwen-plus", "prompt", 100)
	m.RecordLLMTokenHistogram("qwen-plus", "completion", 50)
	m.RecordLLMFirstTokenLatency("qwen-plus", "qwen", 0.3)
	// Knowledge / Memory
	m.IncKnowledgeQuery("rag", "ok")
	m.RecordKnowledgeQueryDuration("rag", 0.2)
	m.RecordMemoryRetrievalDuration("search", 0.1)
	m.IncKnowledgeIngest("ok")
	m.RecordKnowledgeIngestDuration(1.5)
	m.IncKnowledgeIngestInFlight()
	m.DecKnowledgeIngestInFlight()
	m.IncKnowledgeEmbedUnavailable("tenant-1")
	// Hermes
	m.IncHermesEvent("memory.raw")
	m.IncHermesEventProcessed("memory.raw", "ok")
	// Agent KPI (F3)
	m.IncAgentTaskCompleted("agent-1", "react", "proposal", "ok", "tenant-1")
	m.RecordAgentTaskLatency("agent-1", "proposal", 1.0)
	m.RecordAgentCostPerTask("agent-1", "proposal", 0.01)
	m.RecordAgentEvalScore("agent-1", "accuracy", 0.9)
	m.RecordAgentConversationTurn("agent-1", 5)
	// Scheduler / Reranker / Router
	m.IncScheduledFire("cron", "ok")
	m.IncRerankRequest("tenant-1", "bge-m3", "ok")
	m.RecordRerankDuration("bge-m3", 0.2)
	m.IncNoAnswer("tenant-1", "threshold_filtered")
	m.IncKnowledgeJudge("qwen-turbo", "ok")
	m.IncRouteFallback("qwen-plus", "qwen-turbo")
	m.RecordBudgetRatio("tenant-1", 42)
	// Model availability & fallback (P6)
	m.RecordModelHealthTransition("qwen-plus", "healthy", "unhealthy")
	m.SetMemoryMigrationProgress("tenant-1", "text-embedding-v1", "text-embedding-v3", "migrating", 30)
	m.IncMemoryMigrationStalled("tenant-1", "text-embedding-v1", "text-embedding-v3")
	// Audit / Collab / Optimizer / Operation Gate / Schedule skew
	m.IncAuditEvent("high", "allowed")
	m.RecordAuditWriteQueueDepth(3)
	m.IncCollabPlan("parallel", "created")
	m.RecordCollabTaskDuration("parallel", 2.0)
	m.IncOptimizerCandidate("proposal", "accepted")
	m.RecordOptimizerCycleDuration(3.0)
	m.IncOperationProposal("memory", "approved")
	m.RecordApprovalLatency("memory", 0.5)
	m.RecordScheduleSkew(1.5)
	// Reaper（注册缺陷的触发点）
	m.IncReaperCycle("ok")
	m.SetReaperCycleTimestamp(1785762800)
	m.IncReaperGuestDeleted()
	m.IncReaperDeleteError("delete_user")
	// Background components / Panics / Workflow / MCP client / Evaluation / Auth
	m.RecordComponentCycle("chat-cleanup")
	m.SetComponentCycleTimestamp("chat-cleanup", 1785762800)
	m.IncComponentError("chat-cleanup", "run")
	m.IncGoroutinePanic("memory-worker")
	m.IncWorkflowRun("tenant-1", "ok")
	m.RecordWorkflowRunDuration("tenant-1", 4.0)
	m.IncMCPClientRequest("mcp-server", "call", "ok")
	m.IncMCPClientReconnect("mcp-server")
	m.IncEvaluationJob("ok")
	// Evaluation observation（§11；第二参为 stratum，非 verdict）
	m.IncEvalObservation("agent", "pro")
	m.RecordEvalJudgeScore("agent", "faithfulness", 0.9)
	m.RecordEvalJudgeLatency(0.3)
	m.RecordEvalJudgeCost(0.001)
	m.IncEvalJudgeFailure("evidence_missing")
	m.SetEvalQueueBacklog("observation", 0)
	// Evaluation observation（P1b §11）
	m.IncEvalRuleHit("tool_denylist", "agent", "block")
	m.IncEvalBehaviorAnomaly("agent", "judge_below_threshold")
	m.IncEvalGateAction("rule_guard", "block")
	m.RecordEvalSampleCoverage("agent", 0.5)
	// Evaluation observation（P1c §11.3）：评审池积压 Gauge + 升级失败 Counter。
	m.SetEvalReviewBacklog(0)
	m.IncEvalReviewEscalateFailure()
	m.IncAuthFailure("invalid_token")
}

func TestPrometheusMetricsAllMethodsRegistered(t *testing.T) {
	m := NewPrometheusMetrics(zap.NewNop())
	m.RegisterReaperMetrics() // cmd/server 装配路径会显式注册 reaper 指标
	exerciseAllMetrics(m)
}

func TestNoopMetricsAllMethods(t *testing.T) {
	exerciseAllMetrics(NoopMetrics{})
}

func TestReaperMetricsServerOnly(t *testing.T) {
	m := NewPrometheusMetrics(zap.NewNop())
	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() == "reaper_last_cycle_timestamp_seconds" {
			t.Fatal("reaper metrics must not be exported before RegisterReaperMetrics")
		}
	}

	m.RegisterReaperMetrics()
	families, err = m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "reaper_last_cycle_timestamp_seconds" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("reaper metric missing after RegisterReaperMetrics")
	}
}

func TestEvalObservationMetrics(t *testing.T) {
	m := NewPrometheusMetrics(zap.NewNop())
	m.IncEvalObservation("agent", "pro")
	m.IncEvalObservation("agent", "pro")
	m.RecordEvalJudgeScore("agent", "faithfulness", 0.9)
	m.RecordEvalJudgeLatency(1.5)
	m.RecordEvalJudgeCost(0.012)
	m.IncEvalJudgeFailure("evidence_missing")
	m.SetEvalQueueBacklog("observation", 7)

	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	// 断言各指标名、label 维度与值对齐规格 §11（helper 见 Step 3 末尾）。
	// eval_observation_total 标签为 {resource, stratum}（§11.1）：按 stratum 过滤必须命中
	// 2 条 —— 旧实现把 verdict 丢弃写空 stratum，会在此处失败。
	assertCounterVecSum(t, families, "eval_observation_total", "resource", "agent", 2)
	assertCounterVecSum(t, families, "eval_observation_total", "stratum", "pro", 2)
	assertCounterVecSum(t, families, "eval_judge_failure_total", "reason", "evidence_missing", 1)
	assertHistogramVecSum(t, families, "eval_judge_score", "dimension", "faithfulness", 0.9)
	assertHistogramSum(t, families, "eval_judge_latency_seconds", 1.5)
	assertCounterSum(t, families, "eval_judge_cost_total", 0.012)
	assertGaugeVecSum(t, families, "eval_queue_backlog", "queue", "observation", 7)
}

func TestPrometheusMetricsReviewPool(t *testing.T) {
	m := NewPrometheusMetrics(zap.NewNop())
	m.SetEvalReviewBacklog(7)
	m.IncEvalReviewEscalateFailure()

	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	// eval_review_backlog 是无标签 Gauge（P1c §11.3）：注册成功即可抓取，值为 Set 的 7。
	fam := findFamily(families, "eval_review_backlog")
	if fam == nil {
		t.Fatalf("metric family %q not found", "eval_review_backlog")
	}
	assertFloatClose(t, "eval_review_backlog", fam.GetMetric()[0].GetGauge().GetValue(), 7)
	// eval_review_escalate_failure_total 是无标签 Counter，Inc 一次值为 1。
	assertCounterSum(t, families, "eval_review_escalate_failure_total", 1)
}

func findFamily(families []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func labelValueFor(m *dto.Metric, key string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == key {
			return lp.GetValue()
		}
	}
	return ""
}

// assertFloatClose 比较浮点指标值，1e-9 容差（0.012 等浮点累加不精确）。
func assertFloatClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertCounterVecSum(t *testing.T, families []*dto.MetricFamily, name, labelKey, labelValue string, want float64) {
	t.Helper()
	fam := findFamily(families, name)
	if fam == nil {
		t.Fatalf("metric family %q not found", name)
	}
	var got float64
	for _, m := range fam.GetMetric() {
		if labelValueFor(m, labelKey) == labelValue {
			got += m.GetCounter().GetValue()
		}
	}
	assertFloatClose(t, name, got, want)
}

func assertCounterSum(t *testing.T, families []*dto.MetricFamily, name string, want float64) {
	t.Helper()
	fam := findFamily(families, name)
	if fam == nil {
		t.Fatalf("metric family %q not found", name)
	}
	assertFloatClose(t, name, fam.GetMetric()[0].GetCounter().GetValue(), want)
}

func assertHistogramVecSum(t *testing.T, families []*dto.MetricFamily, name, labelKey, labelValue string, want float64) {
	t.Helper()
	fam := findFamily(families, name)
	if fam == nil {
		t.Fatalf("metric family %q not found", name)
	}
	for _, m := range fam.GetMetric() {
		if labelValueFor(m, labelKey) == labelValue {
			assertFloatClose(t, name, m.GetHistogram().GetSampleSum(), want)
			return
		}
	}
	t.Fatalf("%s label %s=%s not found", name, labelKey, labelValue)
}

func assertHistogramSum(t *testing.T, families []*dto.MetricFamily, name string, want float64) {
	t.Helper()
	fam := findFamily(families, name)
	if fam == nil {
		t.Fatalf("metric family %q not found", name)
	}
	assertFloatClose(t, name, fam.GetMetric()[0].GetHistogram().GetSampleSum(), want)
}

func assertGaugeVecSum(t *testing.T, families []*dto.MetricFamily, name, labelKey, labelValue string, want float64) {
	t.Helper()
	fam := findFamily(families, name)
	if fam == nil {
		t.Fatalf("metric family %q not found", name)
	}
	for _, m := range fam.GetMetric() {
		if labelValueFor(m, labelKey) == labelValue {
			assertFloatClose(t, name, m.GetGauge().GetValue(), want)
			return
		}
	}
	t.Fatalf("%s label %s=%s not found", name, labelKey, labelValue)
}
