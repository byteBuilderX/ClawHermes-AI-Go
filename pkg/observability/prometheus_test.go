package observability

import (
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func newTestMetrics(t *testing.T) *PrometheusMetrics {
	t.Helper()
	m := NewPrometheusMetrics(zap.NewNop())
	// cmd/server 装配路径会显式注册 reaper 指标；冒烟测试覆盖该路径。
	m.RegisterReaperMetrics()
	t.Cleanup(func() { _ = m.reg.Unregister(m.reg) })
	return m
}

func TestPrometheusAllMethodsSmoke(t *testing.T) {
	// 所有指标方法冒烟：调用不 panic、注册无冲突、可 Gather。
	m := newTestMetrics(t)

	m.IncHTTPRequest("GET", "/health", 200)
	m.IncHTTPRequest("GET", "/err", 0) // 极端情况：statusCode<=0 归一为 2xx
	m.RecordHTTPRequestDuration("GET", "/health", 0.1)
	m.IncHTTPRequestsInFlight()
	m.DecHTTPRequestsInFlight()

	m.IncSkillExecution("s1", "chat", "ok")
	m.RecordSkillExecutionDuration("s1", 1.5)
	m.SetSkillCircuitBreakerState("s1", 1)

	m.IncAgentExecution("a1", "chat", "ok")
	m.RecordAgentExecutionDuration("a1", "chat", 2.0)
	m.RecordAgentStepCount("a1", "chat", 3)
	m.IncSystemAssistantRequest("admin", "v1", "ok")
	m.RecordSystemAssistantTTFT("admin", "v1", 0.3)
	m.RecordOfficialDocsSearchResults("v1", "ok", 2)
	m.RecordSystemAssistantDiagnosticArea("admin", "knowledge", "ok", 0.5)
	m.RecordSystemAssistantEvidenceGaps("admin", "v1", 1)
	m.IncResourceProposal("skill", "create", "accepted")
	m.RecordResourceProposalReviewDuration("skill", "create", 1.0)
	m.RecordResourceProposalDraftEdits("skill", "create", 4)

	m.IncLLMRequest("deepseek-v4-flash", "deepseek", "ok")
	m.RecordLLMRequestDuration("deepseek-v4-flash", "deepseek", 1.0)
	m.IncLLMTokenUsage("deepseek-v4-flash", "output", 123)
	m.RecordLLMTokenHistogram("deepseek-v4-flash", "output", 123)
	m.RecordLLMFirstTokenLatency("deepseek-v4-flash", "deepseek", 0.4)

	m.IncKnowledgeQuery("graphrag", "ok")
	m.RecordKnowledgeQueryDuration("graphrag", 0.2)
	m.RecordMemoryRetrievalDuration("get", 0.1)
	m.IncKnowledgeIngest("succeeded")
	m.RecordKnowledgeIngestDuration(10.0)
	m.IncKnowledgeIngestInFlight()
	m.DecKnowledgeIngestInFlight()

	m.IncHermesEvent("created")
	m.IncHermesEventProcessed("created", "ok")

	m.IncAgentTaskCompleted("a1", "chat", "research", "ok", "tenant-1")
	m.RecordAgentTaskLatency("a1", "research", 30.0)
	m.RecordAgentCostPerTask("a1", "research", 0.05)
	m.RecordAgentEvalScore("a1", "accuracy", 0.9)
	m.RecordAgentConversationTurn("a1", 5)

	m.IncScheduledFire("cron", "ok")
	m.IncRerankRequest("tenant-1", "bge-reranker", "ok")
	m.RecordRerankDuration("bge-reranker", 0.2)
	m.IncNoAnswer("tenant-1", "no_sources")
	m.IncKnowledgeJudge("qwen-turbo", "ok")
	m.IncRouteFallback("deepseek-v4-flash", "qwen-max")
	m.RecordBudgetRatio("monthly", 0.4)
	m.IncAuditEvent("high", "accepted")
	m.RecordAuditWriteQueueDepth(3)
	m.IncCollabPlan("split", "ok")
	m.RecordCollabTaskDuration("split", 5.0)
	m.IncOptimizerCandidate("bayesian", "kept")
	m.RecordOptimizerCycleDuration(20.0)
	m.IncOperationProposal("delete", "pending")
	m.RecordApprovalLatency("delete", 60.0)
	m.RecordScheduleSkew(2.5)

	m.IncReaperCycle("ok")
	m.SetReaperCycleTimestamp(1234567)
	m.IncReaperGuestDeleted()
	m.IncReaperDeleteError("delete")

	m.RecordComponentCycle("ticker")
	m.SetComponentCycleTimestamp("ticker", 1234567)
	m.IncComponentError("ticker", "run")

	m.IncGoroutinePanic("ticker")
	m.IncWorkflowRun("t1", "completed")
	m.RecordWorkflowRunDuration("t1", 5.0)
	m.IncMCPClientRequest("mcp-1", "list_tools", "ok")
	m.IncMCPClientReconnect("mcp-1")
	m.IncEvaluationJob("passed")
	m.IncAuthFailure("expired_token")

	// 所有注册的指标可被正常 Gather（重复注册会报错）。
	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(families) < 40 {
		t.Errorf("expected >=40 metric families, got %d", len(families))
	}
}

func TestPrometheusIncHTTPRequestNormalizesStatus(t *testing.T) {
	// 极端情况：statusCode<=0 必须归一为 2xx，不能 panic 也不能用空标签。
	m := newTestMetrics(t)
	m.IncHTTPRequest("POST", "/api", 0)
	m.IncHTTPRequest("POST", "/api", -1)

	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "http_requests_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "status" && label.GetValue() == "" {
					t.Error("status label must never be empty")
				}
			}
		}
	}
}

func TestPrometheusRegistererAndHandler(t *testing.T) {
	m := newTestMetrics(t)
	if m.Registerer() == nil {
		t.Error("Registerer must not be nil")
	}
	h := m.GetHandler()
	if h == nil {
		t.Fatal("GetHandler must return a handler")
	}
	// handler 必须可响应 scrape。
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("scrape status = %d, want 200", rec.Code)
	}
	if len(rec.Body.String()) == 0 {
		t.Error("scrape body must not be empty")
	}
}

func TestNoopMetricsSafeForAllMethods(t *testing.T) {
	// NoopMetrics 必须实现 MetricsProvider 且全方法安全。
	var _ MetricsProvider = NoopMetrics{}
	nm := NoopMetrics{}
	nm.IncHTTPRequest("GET", "/", 500)
	nm.RecordHTTPRequestDuration("GET", "/", 1)
	nm.IncHTTPRequestsInFlight()
	nm.DecHTTPRequestsInFlight()
	nm.IncSkillExecution("s", "t", "ok")
	nm.RecordSkillExecutionDuration("s", 1)
	nm.SetSkillCircuitBreakerState("s", 1)
	nm.IncAgentExecution("a", "t", "ok")
	nm.RecordAgentExecutionDuration("a", "t", 1)
	nm.RecordAgentStepCount("a", "t", 1)
	nm.IncSystemAssistantRequest("r", "v", "ok")
	nm.RecordSystemAssistantTTFT("r", "v", 1)
	nm.RecordOfficialDocsSearchResults("v", "ok", 1)
	nm.RecordSystemAssistantDiagnosticArea("r", "a", "ok", 1)
	nm.RecordSystemAssistantEvidenceGaps("r", "v", 1)
	nm.IncResourceProposal("k", "op", "ok")
	nm.RecordResourceProposalReviewDuration("k", "op", 1)
	nm.RecordResourceProposalDraftEdits("k", "op", 1)
	nm.IncLLMRequest("m", "p", "ok")
	nm.RecordLLMRequestDuration("m", "p", 1)
	nm.IncLLMTokenUsage("m", "t", 1)
	nm.RecordLLMTokenHistogram("m", "t", 1)
	nm.RecordLLMFirstTokenLatency("m", "p", 1)
	nm.IncKnowledgeQuery("q", "ok")
	nm.RecordKnowledgeQueryDuration("q", 1)
	nm.RecordMemoryRetrievalDuration("op", 1)
	nm.IncKnowledgeIngest("ok")
	nm.RecordKnowledgeIngestDuration(1)
	nm.IncKnowledgeIngestInFlight()
	nm.DecKnowledgeIngestInFlight()
	nm.IncHermesEvent("e")
	nm.IncHermesEventProcessed("e", "ok")
	nm.IncAgentTaskCompleted("a", "t", "k", "ok", "tenant-1")
	nm.RecordAgentTaskLatency("a", "k", 1)
	nm.RecordAgentCostPerTask("a", "k", 1)
	nm.RecordAgentEvalScore("a", "m", 1)
	nm.RecordAgentConversationTurn("a", 1)
	nm.IncScheduledFire("s", "ok")
	nm.IncRerankRequest("tenant-1", "m", "ok")
	nm.RecordRerankDuration("m", 1)
	nm.IncNoAnswer("tenant-1", "no_sources")
	nm.IncKnowledgeJudge("m", "ok")
	nm.IncRouteFallback("a", "b")
	nm.RecordBudgetRatio("s", 1)
	nm.IncAuditEvent("h", "ok")
	nm.RecordAuditWriteQueueDepth(1)
	nm.IncCollabPlan("s", "ok")
	nm.RecordCollabTaskDuration("s", 1)
	nm.IncOptimizerCandidate("s", "ok")
	nm.RecordOptimizerCycleDuration(1)
	nm.IncOperationProposal("k", "ok")
	nm.RecordApprovalLatency("k", 1)
	nm.RecordScheduleSkew(1)
	nm.IncReaperCycle("ok")
	nm.SetReaperCycleTimestamp(1)
	nm.IncReaperGuestDeleted()
	nm.IncReaperDeleteError("p")
	nm.RecordComponentCycle("c")
	nm.SetComponentCycleTimestamp("c", 1)
	nm.IncComponentError("c", "p")
	nm.IncGoroutinePanic("c")
	nm.IncWorkflowRun("t", "ok")
	nm.RecordWorkflowRunDuration("t", 1)
	nm.IncMCPClientRequest("s", "op", "ok")
	nm.IncMCPClientReconnect("s")
	nm.IncEvaluationJob("ok")
	nm.IncAuthFailure("r")
}

func TestGetMeterNoop(t *testing.T) {
	var _ = GetMeter()
	GetMeter().RecordInt64("counter", 1)
	GetMeter().RecordFloat64("gauge", 1.5)
}

func TestPrometheusInstanceIsolation(t *testing.T) {
	// 两个实例使用私有 registry，不会互相注册冲突。
	m1 := NewPrometheusMetrics(zap.NewNop())
	m2 := NewPrometheusMetrics(zap.NewNop())
	t.Cleanup(func() { _ = m1.reg.Unregister(m1.reg); _ = m2.reg.Unregister(m2.reg) })
	m1.IncHTTPRequest("GET", "/a", 200)
	m2.IncHTTPRequest("GET", "/b", 200)
	if _, err := m1.reg.Gather(); err != nil {
		t.Fatalf("m1 Gather: %v", err)
	}
	if _, err := m2.reg.Gather(); err != nil {
		t.Fatalf("m2 Gather: %v", err)
	}
}

func TestPrometheusLabelsRegistered(t *testing.T) {
	// 检查 metrics 的 label 集合是否与注册时一致（防 WithLabelValues 参数错位）。
	m := newTestMetrics(t)
	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() == "" {
					t.Errorf("metric %s has empty label %s (label/value mismatch)", f.GetName(), label.GetName())
				}
			}
		}
	}
}

func TestPrometheusMetricsEvalObservationP1b(t *testing.T) {
	// P1b 评测指标冒烟：注册后不 panic + 值递增/写入符合 §11。
	m := newTestMetrics(t)
	m.IncEvalRuleHit("tool_denylist", "agent", "block")
	m.IncEvalRuleHit("tool_denylist", "agent", "block")
	m.IncEvalBehaviorAnomaly("agent", "judge_below_threshold")
	m.IncEvalGateAction("rule_guard", "block")
	m.RecordEvalSampleCoverage("agent", 0.5)

	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	assertCounterVecSum(t, families, "eval_rule_hit_total", "rule", "tool_denylist", 2)
	assertCounterVecSum(t, families, "eval_behavior_anomaly_total", "signal", "judge_below_threshold", 1)
	assertCounterVecSum(t, families, "eval_gate_action_total", "action", "block", 1)
	assertGaugeVecSum(t, families, "eval_sample_coverage", "resource", "agent", 0.5)
}

// TestAgentTaskCompletedTenantLabel 防 C2 回归：tenant_id label 必须落真实值，
// 且不会产生空串 label 的孤儿 series。
func TestAgentTaskCompletedTenantLabel(t *testing.T) {
	m := newTestMetrics(t)
	m.IncAgentTaskCompleted("a1", "react", "react", "ok", "tenant-9")

	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "agent_task_completed_total" {
			continue
		}
		if len(f.Metric) != 1 {
			t.Fatalf("want exactly 1 series, got %d", len(f.Metric))
		}
		labels := map[string]string{}
		for _, lp := range f.Metric[0].Label {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels["tenant_id"] != "tenant-9" {
			t.Fatalf("tenant_id = %q, want tenant-9", labels["tenant_id"])
		}
		if got := f.Metric[0].GetCounter().GetValue(); got != 1 {
			t.Fatalf("count = %v, want 1", got)
		}
		return
	}
	t.Fatal("agent_task_completed_total not gathered")
}
