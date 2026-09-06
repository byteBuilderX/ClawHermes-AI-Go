package application

import (
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/stretchr/testify/require"
)

// agentKPISpy 捕获 recordAgentKPI 的 4 类 KPI 打点；embed NoopMetrics 满足接口。
type agentKPISpy struct {
	observability.NoopMetrics
	completedAgentID, completedAgentType, completedTaskKind, completedOutcome, completedTenant string
	latencySeconds                                                                             float64
	costUSD                                                                                    float64
	turns                                                                                      int
}

func (s *agentKPISpy) IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string) {
	s.completedAgentID, s.completedAgentType, s.completedTaskKind, s.completedOutcome, s.completedTenant =
		agentID, agentType, taskKind, outcome, tenantID
}

func (s *agentKPISpy) RecordAgentTaskLatency(_ string, _ string, seconds float64) {
	s.latencySeconds = seconds
}

func (s *agentKPISpy) RecordAgentCostPerTask(_ string, _ string, costUSD float64) {
	s.costUSD = costUSD
}

func (s *agentKPISpy) RecordAgentConversationTurn(_ string, turnCount int) {
	s.turns = turnCount
}

// TestRecordAgentKPI 验证 KPI 打点语义与 C2 tenant 透传：tenant_id 槽落真实租户，
// task_kind 槽当前镜像 agent_type（平台暂无独立 task-kind 维度）。
func TestRecordAgentKPI(t *testing.T) {
	spy := &agentKPISpy{}
	result := &AgentResult{Duration: 3 * time.Second, CostUSD: 0.42, Steps: 5}

	recordAgentKPI(spy, "agent-1", "react", "ok", "tenant-9", result)

	require.Equal(t, "agent-1", spy.completedAgentID)
	require.Equal(t, "react", spy.completedAgentType)
	require.Equal(t, "react", spy.completedTaskKind)
	require.Equal(t, "ok", spy.completedOutcome)
	require.Equal(t, "tenant-9", spy.completedTenant)
	require.Equal(t, 3.0, spy.latencySeconds)
	require.Equal(t, 0.42, spy.costUSD)
	require.Equal(t, 5, spy.turns)
}
