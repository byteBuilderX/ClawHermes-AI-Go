// Package observability provides monitoring and tracing.

package observability

// MetricsProvider is the pluggable interface for all observability metrics.
// PrometheusMetrics implements this; NoopMetrics is used in tests.
type MetricsProvider interface {
	// HTTP
	IncHTTPRequest(method, path string, statusCode int)
	RecordHTTPRequestDuration(method, path string, duration float64)
	IncHTTPRequestsInFlight()
	DecHTTPRequestsInFlight()

	// Skill
	IncSkillExecution(skillID, skillType, status string)
	RecordSkillExecutionDuration(skillID string, duration float64)
	SetSkillCircuitBreakerState(skillID string, state float64)

	// Agent
	IncAgentExecution(agentID, agentType, status string)
	RecordAgentExecutionDuration(agentID, agentType string, duration float64)
	RecordAgentStepCount(agentID, agentType string, steps int)
	IncSystemAssistantRequest(roleClass, profileVersion, outcome string)
	RecordSystemAssistantTTFT(roleClass, profileVersion string, duration float64)
	RecordOfficialDocsSearchResults(profileVersion, outcome string, count int)
	RecordSystemAssistantDiagnosticArea(roleClass, area, outcome string, duration float64)
	RecordSystemAssistantEvidenceGaps(roleClass, profileVersion string, count int)
	IncResourceProposal(kind, operation, outcome string)
	RecordResourceProposalReviewDuration(kind, operation string, duration float64)
	RecordResourceProposalDraftEdits(kind, operation string, count int)

	// Platform MCP uses only bounded operational labels.

	// LLM
	IncLLMRequest(model, provider, status string)
	IncLLMModelResolutionError(model, reason string)
	RecordLLMRequestDuration(model, provider string, duration float64)
	IncLLMTokenUsage(model, tokenType string, count int64)
	RecordLLMTokenHistogram(model, tokenType string, count float64)
	RecordLLMFirstTokenLatency(model, provider string, latency float64)

	// Knowledge / Memory
	IncKnowledgeQuery(queryType, status string)
	RecordKnowledgeQueryDuration(queryType string, duration float64)
	RecordMemoryRetrievalDuration(operation string, duration float64)
	IncKnowledgeIngest(status string)
	RecordKnowledgeIngestDuration(duration float64)
	IncKnowledgeIngestInFlight()
	DecKnowledgeIngestInFlight()
	IncKnowledgeEmbedUnavailable(tenantID string)

	// Hermes
	IncHermesEvent(eventType string)
	IncHermesEventProcessed(eventType, status string)

	// Agent KPI (F3)
	IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string)
	RecordAgentTaskLatency(agentID, taskKind string, seconds float64)
	RecordAgentCostPerTask(agentID, taskKind string, costUSD float64)
	RecordAgentEvalScore(agentID, metric string, score float64)
	RecordAgentConversationTurn(agentID string, turnCount int)

	// Scheduler (F3)
	IncScheduledFire(scheduleType, status string)

	// Reranker (F3)
	IncRerankRequest(tenantID, model, status string)
	RecordRerankDuration(model string, seconds float64)

	// Knowledge NoAnswer / judge (F3)
	IncNoAnswer(tenantID, reason string)
	IncKnowledgeJudge(model, status string)

	// Model Router (F3)
	IncRouteFallback(fromModel, toModel string)
	RecordBudgetRatio(scope string, pct float64)

	// Model policy (L1-L4 治理)
	IncPolicyBlocked(model string)
	IncPolicyMissing(model string)

	// Audit (F3)
	IncAuditEvent(risk, outcome string)
	RecordAuditWriteQueueDepth(depth int)

	// Collab (F3)
	IncCollabPlan(strategy, outcome string)
	RecordCollabTaskDuration(strategy string, seconds float64)

	// Optimizer (F3)
	IncOptimizerCandidate(strategy, outcome string)
	RecordOptimizerCycleDuration(seconds float64)

	// Operation Gate (F3)
	IncOperationProposal(kind, outcome string)
	RecordApprovalLatency(kind string, seconds float64)

	// Schedule skew (F3)
	RecordScheduleSkew(skewSeconds float64)

	// Reaper
	IncReaperCycle(outcome string)
	SetReaperCycleTimestamp(ts float64)
	IncReaperGuestDeleted()
	IncReaperDeleteError(phase string)

	// Background components (generic ticker-based components)
	RecordComponentCycle(component string)
	SetComponentCycleTimestamp(component string, ts float64)
	IncComponentError(component, phase string)

	// Goroutine panic recovery
	IncGoroutinePanic(component string)

	// Workflow
	IncWorkflowRun(tenantID, status string)
	RecordWorkflowRunDuration(tenantID string, duration float64)

	// MCP internal client (backend→MCP server calls)
	IncMCPClientRequest(serverName, operation, status string)
	IncMCPClientReconnect(serverName string)

	// Evaluation
	IncEvaluationJob(status string)

	// Evaluation observation（§11 运行时评估观测；stratum 为租户 tier 映射层）
	IncEvalObservation(resource, stratum string)
	RecordEvalJudgeScore(resource, dimension string, score float64)
	// TODO(P1b)：evaluation.observe 采样覆盖率指标待真实计数基础设施接入后恢复。
	RecordEvalJudgeLatency(seconds float64)
	RecordEvalJudgeCost(costUSD float64)
	IncEvalJudgeFailure(reason string)
	SetEvalQueueBacklog(queue string, count int64)
	// P1b：规则护栏命中计数（§11.1 eval_rule_hit_total）。
	IncEvalRuleHit(rule, resource, verdict string)
	// P1b：行为异常判异计数（§11.1 eval_behavior_anomaly_total）。
	IncEvalBehaviorAnomaly(resource, signal string)
	// P1b：分层门禁动作计数（§11.2 eval_gate_action_total）。
	IncEvalGateAction(layer, action string)
	// P1b：主动采样覆盖率（§11.1 eval_sample_coverage，Gauge [0,1]）。
	RecordEvalSampleCoverage(resource string, ratio float64)
	// P1c：评审池积压（eval_review_backlog，Gauge）与升级失败计数。
	SetEvalReviewBacklog(count int64)
	IncEvalReviewEscalateFailure()

	// Auth
	IncAuthFailure(reason string)

	// Model availability & fallback (P6)
	// RecordModelHealthTransition 在模型健康状态 from→to 转移后上报；Prometheus
	// 实现把 gauge 置为 from=0 / to=1（kube-state 语义），Noop 安全跳过。
	RecordModelHealthTransition(model, from, to string)
	// SetMemoryMigrationProgress 设置某租户一次记忆迁移的当前进度（断点游标值），
	// 供「迁移停滞」告警对同一 status 序列做 offset 差分。
	SetMemoryMigrationProgress(tenantID, fromModel, toModel, status string, progress int)
	// IncMemoryMigrationStalled 在扫描间隔间迁移进度未推进时累加（迁移停滞信号）。
	IncMemoryMigrationStalled(tenantID, fromModel, toModel string)
}

// NoopMetrics satisfies MetricsProvider with no-ops. Safe for tests and disabled mode.
type NoopMetrics struct{}

func (NoopMetrics) IncHTTPRequest(_, _ string, _ int)                             {}
func (NoopMetrics) RecordHTTPRequestDuration(_, _ string, _ float64)              {}
func (NoopMetrics) IncHTTPRequestsInFlight()                                      {}
func (NoopMetrics) DecHTTPRequestsInFlight()                                      {}
func (NoopMetrics) IncSkillExecution(_, _, _ string)                              {}
func (NoopMetrics) RecordSkillExecutionDuration(_ string, _ float64)              {}
func (NoopMetrics) SetSkillCircuitBreakerState(_ string, _ float64)               {}
func (NoopMetrics) IncAgentExecution(_, _, _ string)                              {}
func (NoopMetrics) RecordAgentExecutionDuration(_, _ string, _ float64)           {}
func (NoopMetrics) RecordAgentStepCount(_, _ string, _ int)                       {}
func (NoopMetrics) IncSystemAssistantRequest(_, _, _ string)                      {}
func (NoopMetrics) RecordSystemAssistantTTFT(_, _ string, _ float64)              {}
func (NoopMetrics) RecordOfficialDocsSearchResults(_, _ string, _ int)            {}
func (NoopMetrics) RecordSystemAssistantDiagnosticArea(_, _, _ string, _ float64) {}
func (NoopMetrics) RecordSystemAssistantEvidenceGaps(_, _ string, _ int)          {}
func (NoopMetrics) IncResourceProposal(_, _, _ string)                            {}
func (NoopMetrics) RecordResourceProposalReviewDuration(_, _ string, _ float64)   {}
func (NoopMetrics) RecordResourceProposalDraftEdits(_, _ string, _ int)           {}
func (NoopMetrics) IncLLMRequest(_, _, _ string)                                  {}
func (NoopMetrics) IncLLMModelResolutionError(_, _ string)                        {}
func (NoopMetrics) RecordLLMRequestDuration(_, _ string, _ float64)               {}
func (NoopMetrics) IncLLMTokenUsage(_, _ string, _ int64)                         {}
func (NoopMetrics) RecordLLMTokenHistogram(_, _ string, _ float64)                {}
func (NoopMetrics) RecordLLMFirstTokenLatency(_, _ string, _ float64)             {}
func (NoopMetrics) IncKnowledgeQuery(_, _ string)                                 {}
func (NoopMetrics) RecordKnowledgeQueryDuration(_ string, _ float64)              {}
func (NoopMetrics) RecordMemoryRetrievalDuration(_ string, _ float64)             {}
func (NoopMetrics) IncKnowledgeIngest(_ string)                                   {}
func (NoopMetrics) RecordKnowledgeIngestDuration(_ float64)                       {}
func (NoopMetrics) IncKnowledgeIngestInFlight()                                   {}
func (NoopMetrics) DecKnowledgeIngestInFlight()                                   {}
func (NoopMetrics) IncKnowledgeEmbedUnavailable(_ string)                         {}
func (NoopMetrics) IncHermesEvent(_ string)                                       {}
func (NoopMetrics) IncHermesEventProcessed(_, _ string)                           {}
func (NoopMetrics) IncAgentTaskCompleted(_, _, _, _, _ string)                    {}
func (NoopMetrics) RecordAgentTaskLatency(_, _ string, _ float64)                 {}
func (NoopMetrics) RecordAgentCostPerTask(_, _ string, _ float64)                 {}
func (NoopMetrics) RecordAgentEvalScore(_, _ string, _ float64)                   {}
func (NoopMetrics) RecordAgentConversationTurn(_ string, _ int)                   {}
func (NoopMetrics) IncScheduledFire(_, _ string)                                  {}
func (NoopMetrics) IncRerankRequest(_, _, _ string)                               {}
func (NoopMetrics) RecordRerankDuration(_ string, _ float64)                      {}
func (NoopMetrics) IncNoAnswer(_, _ string)                                       {}
func (NoopMetrics) IncKnowledgeJudge(_, _ string)                                 {}
func (NoopMetrics) IncRouteFallback(_, _ string)                                  {}
func (NoopMetrics) RecordBudgetRatio(_ string, _ float64)                         {}
func (NoopMetrics) IncPolicyBlocked(_ string)                                     {}
func (NoopMetrics) IncPolicyMissing(_ string)                                     {}
func (NoopMetrics) IncAuditEvent(_, _ string)                                     {}
func (NoopMetrics) RecordAuditWriteQueueDepth(_ int)                              {}
func (NoopMetrics) IncCollabPlan(_, _ string)                                     {}
func (NoopMetrics) RecordCollabTaskDuration(_ string, _ float64)                  {}
func (NoopMetrics) IncOptimizerCandidate(_, _ string)                             {}
func (NoopMetrics) RecordOptimizerCycleDuration(_ float64)                        {}
func (NoopMetrics) IncOperationProposal(_, _ string)                              {}
func (NoopMetrics) RecordApprovalLatency(_ string, _ float64)                     {}
func (NoopMetrics) RecordScheduleSkew(_ float64)                                  {}
func (NoopMetrics) IncReaperCycle(_ string)                                       {}
func (NoopMetrics) SetReaperCycleTimestamp(_ float64)                             {}
func (NoopMetrics) IncReaperGuestDeleted()                                        {}
func (NoopMetrics) IncReaperDeleteError(_ string)                                 {}
func (NoopMetrics) RecordComponentCycle(_ string)                                 {}
func (NoopMetrics) SetComponentCycleTimestamp(_ string, _ float64)                {}
func (NoopMetrics) IncComponentError(_, _ string)                                 {}
func (NoopMetrics) IncGoroutinePanic(_ string)                                    {}
func (NoopMetrics) IncWorkflowRun(_, _ string)                                    {}
func (NoopMetrics) RecordWorkflowRunDuration(_ string, _ float64)                 {}
func (NoopMetrics) IncMCPClientRequest(_, _, _ string)                            {}
func (NoopMetrics) IncMCPClientReconnect(_ string)                                {}
func (NoopMetrics) IncEvaluationJob(_ string)                                     {}
func (NoopMetrics) IncEvalObservation(_, _ string)                                {}
func (NoopMetrics) RecordEvalJudgeScore(_, _ string, _ float64)                   {}
func (NoopMetrics) RecordEvalJudgeLatency(_ float64)                              {}
func (NoopMetrics) RecordEvalJudgeCost(_ float64)                                 {}
func (NoopMetrics) IncEvalJudgeFailure(_ string)                                  {}
func (NoopMetrics) SetEvalQueueBacklog(_ string, _ int64)                         {}
func (NoopMetrics) IncEvalRuleHit(_, _, _ string)                                 {}
func (NoopMetrics) IncEvalBehaviorAnomaly(_, _ string)                            {}
func (NoopMetrics) IncEvalGateAction(_, _ string)                                 {}
func (NoopMetrics) RecordEvalSampleCoverage(_ string, _ float64)                  {}
func (NoopMetrics) SetEvalReviewBacklog(_ int64)                                  {}
func (NoopMetrics) IncEvalReviewEscalateFailure()                                 {}
func (NoopMetrics) IncAuthFailure(_ string)                                       {}
func (NoopMetrics) RecordModelHealthTransition(_, _, _ string)                    {}
func (NoopMetrics) SetMemoryMigrationProgress(_ string, _, _, _ string, _ int)    {}
func (NoopMetrics) IncMemoryMigrationStalled(_, _, _ string)                      {}
