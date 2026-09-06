package persistence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/pashagolub/pgxmock/v2"
	"github.com/stretchr/testify/require"
)

func TestSanitizeValueRedactsSensitiveEvaluationOutput(t *testing.T) {
	value := map[string]any{
		"result": "ok",
		"token":  "secret-token",
		"nested": map[string]any{"api_key": "secret-key", "count": 2},
		"text":   "authorization=Bearer secret-value",
	}

	encoded, err := json.Marshal(domain.SanitizeValue(value))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"secret-token", "secret-key", "secret-value"} {
		if strings.Contains(text, secret) {
			t.Fatalf("sanitized output leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, `"result":"ok"`) {
		t.Fatalf("unexpected sanitized output: %s", text)
	}
}

// TestSanitizeTools 验证工具序列落库前的脱敏（spec §6.5）：Arguments 中
// password/api_key 等敏感 key 替换为 [REDACTED]，RawText 中的
// `Authorization=Bearer <token>` 等键值被正则脱敏，普通字段原样保留。
func TestSanitizeTools(t *testing.T) {
	tools := []domain.ToolObservation{
		{
			ToolName:     "web_search",
			ToolType:     "search",
			StepIndex:    0,
			ProviderType: "openai",
			CapabilityID: "cap-1",
			Arguments: map[string]any{
				"query":    "stratum",
				"api_key":  "secret-key",
				"password": "hunter2",
			},
			RawText: "web_search(query='stratum', api_key='secret-key', Authorization=Bearer tok123)",
		},
		{
			ToolName:  "read_file",
			StepIndex: 1,
			RawText:   "read_file(path='x')",
		},
	}

	got := domain.SanitizeTools(tools)
	require.Len(t, got, 2)

	// Arguments 中敏感 key 替换为 [REDACTED]，普通 key 原样。
	require.Equal(t, "[REDACTED]", got[0].Arguments["api_key"])
	require.Equal(t, "[REDACTED]", got[0].Arguments["password"])
	require.Equal(t, "stratum", got[0].Arguments["query"])

	// RawText 中 Authorization=Bearer <token> 被正则脱敏且前缀保留。
	require.NotContains(t, got[0].RawText, "tok123")
	require.Contains(t, got[0].RawText, "Authorization=[REDACTED]")

	// 普通字段原样透传。
	require.Equal(t, "web_search", got[0].ToolName)
	require.Equal(t, "search", got[0].ToolType)
	require.Equal(t, 0, got[0].StepIndex)
	require.Equal(t, "openai", got[0].ProviderType)
	require.Equal(t, "cap-1", got[0].CapabilityID)

	// 无敏感数据的工具原样保留。
	require.Equal(t, tools[1], got[1])

	// 不修改入参切片。
	require.Equal(t, "secret-key", tools[0].Arguments["api_key"])
	require.Nil(t, domain.SanitizeTools(nil))
}

// TestPgRunRepository_dimensionsFailureReasonRoundTrip 验证 SaveRun 写入
// dimensions/failure_reason 后 GetRun 能读回相等（spec §6.2 多维分数与失败归因）。
func TestPgRunRepository_dimensionsFailureReasonRoundTrip(t *testing.T) {
	writeMock := newMockRepo(t)
	readMock := newMockRepo(t)
	now := time.Now()
	run := domain.EvalRun{
		ID:              "run-rt",
		Resource:        domain.ResourceRef{Kind: "prompt", ResourceID: "r-1", RevisionID: "rev-1"},
		SuiteRevisionID: "s-1",
		Passed:          true,
		TotalCases:      1,
		PassedCases:     1,
		Metrics:         map[string]any{"pass_rate": 1.0},
		CreatedAt:       now,
		Results: []domain.EvalCaseResult{
			{
				ID: "case-rt", CaseID: "case-1", Passed: true,
				Actual:        map[string]any{"ok": true},
				Message:       "m",
				TraceID:       "tr-1",
				Tokens:        5,
				CostUSD:       0.1,
				DurationMs:    2,
				Dimensions:    []domain.DimensionScore{{Name: "correctness", Score: 1, Passed: true, Reason: "ok"}},
				FailureReason: "assert failed",
				TraceEvidence: &domain.ObservedTraceEvidence{
					CostUSD: 0.05, LatencyMs: 250, Success: false, SecurityViolation: true,
					ToolCallCount: 4, ToolErrorCount: 2,
				},
			},
		},
	}

	// SaveRun 侧：dimensions 序列化为 JSON 数组，failure_reason 原样写入。
	expectTenantTx(writeMock)
	writeMock.ExpectExec("INSERT INTO eval_runs").
		WithArgs("run-rt", "prompt", "r-1", "rev-1", "s-1", true, 1, 1, `{"pass_rate":1}`, "{}", "", now).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	writeMock.ExpectExec("INSERT INTO eval_case_results").
		WithArgs("case-rt", "run-rt", "case-1", true, `{"ok":true}`, "m", "", "tr-1", 5, 0.1, 2,
			`[{"name":"correctness","score":1,"passed":true,"reason":"ok"}]`, "assert failed",
			`{"cost_usd":0.05,"latency_ms":250,"success":false,`+
				`"security_violation":true,"tool_call_count":4,"tool_error_count":2}`,
			false, "", "[]").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	writeMock.ExpectCommit()
	writeRepo := &PgRunRepository{pool: writeMock}
	require.NoError(t, writeRepo.SaveRun(context.Background(), "t1", run))
	require.NoError(t, writeMock.ExpectationsWereMet())

	// GetRun 侧：读回相等。
	expectTenantTx(readMock)
	readMock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("run-rt").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "suite_revision_id",
			"passed", "total_cases", "passed_cases", "metrics", "context_snapshot", "created_by", "created_at",
		}).AddRow("run-rt", "prompt", "r-1", "rev-1", "s-1", true, 1, 1, []byte(`{"pass_rate":1}`), []byte("{}"), "creator-1", now))
	readMock.ExpectQuery("SELECT case_id, passed, actual_output").
		WithArgs("run-rt").
		WillReturnRows(pgxmock.NewRows([]string{
			"case_id", "passed", "actual_output", "message", "error_message", "trace_id",
			"tokens", "cost_usd", "duration_ms", "dimensions", "failure_reason", "trace_evidence",
			"process_pass", "process_failure", "tool_sequence",
		}).AddRow("case-1", true, []byte(`{"ok":true}`), "m", "", "tr-1", 5, 0.1, 2,
			[]byte(`[{"name":"correctness","score":1,"passed":true,"reason":"ok"}]`), "assert failed",
			[]byte(`{"cost_usd":0.05,"latency_ms":250,"success":false,`+
				`"security_violation":true,"tool_call_count":4,"tool_error_count":2}`),
			false, "", []byte("[]")))
	readMock.ExpectCommit()
	readRepo := &PgRunRepository{pool: readMock}
	got, found, err := readRepo.GetRun(context.Background(), "t1", "run-rt")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.Results, 1)
	require.Equal(t, run.Results[0].Dimensions, got.Results[0].Dimensions)
	require.Equal(t, run.Results[0].FailureReason, got.Results[0].FailureReason)
	require.Equal(t, run.Results[0].TraceEvidence, got.Results[0].TraceEvidence)
	require.NoError(t, readMock.ExpectationsWereMet())
}

// TestPgRunRepository_processAndToolRoundTrip 验证 SaveRun 写入
// process_pass/process_failure/tool_sequence 后 GetRun 能读回相等，且工具序列
// 落库前经过 domain.SanitizeTools 脱敏（spec §6.5 多步推理与工具调用评测）。
func TestPgRunRepository_processAndToolRoundTrip(t *testing.T) {
	writeMock := newMockRepo(t)
	readMock := newMockRepo(t)
	now := time.Now()
	tools := []domain.ToolObservation{
		{
			ToolName:     "web_search",
			ToolType:     "search",
			StepIndex:    0,
			ProviderType: "openai",
			CapabilityID: "cap-1",
			Arguments:    map[string]any{"query": "stratum", "api_key": "secret-key"},
			RawText:      "web_search(query='stratum', api_key='secret-key')",
		},
	}
	sanitized := domain.SanitizeTools(tools)
	toolSeqJSON, err := json.Marshal(sanitized)
	require.NoError(t, err)

	run := domain.EvalRun{
		ID:              "run-pt",
		Resource:        domain.ResourceRef{Kind: "prompt", ResourceID: "r-1", RevisionID: "rev-1"},
		SuiteRevisionID: "s-1",
		Passed:          true,
		TotalCases:      1,
		PassedCases:     1,
		Metrics:         map[string]any{"pass_rate": 1.0},
		CreatedAt:       now,
		Results: []domain.EvalCaseResult{
			{
				ID: "case-pt", CaseID: "case-1", Passed: true,
				Actual:         map[string]any{"ok": true},
				Message:        "m",
				TraceID:        "tr-1",
				Tokens:         5,
				CostUSD:        0.1,
				DurationMs:     2,
				ProcessPass:    true,
				ProcessFailure: "process:must_not_call:delete",
				Tools:          tools,
			},
		},
	}

	// SaveRun 侧：process_pass/failure 原样，tool_sequence 为脱敏后的工具序列 JSON。
	expectTenantTx(writeMock)
	writeMock.ExpectExec("INSERT INTO eval_runs").
		WithArgs("run-pt", "prompt", "r-1", "rev-1", "s-1", true, 1, 1, `{"pass_rate":1}`, "{}", "", now).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	writeMock.ExpectExec("INSERT INTO eval_case_results").
		WithArgs("case-pt", "run-pt", "case-1", true, `{"ok":true}`, "m", "", "tr-1", 5, 0.1, 2,
			"[]", "", "null",
			true, "process:must_not_call:delete", string(toolSeqJSON)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	writeMock.ExpectCommit()
	writeRepo := &PgRunRepository{pool: writeMock}
	require.NoError(t, writeRepo.SaveRun(context.Background(), "t1", run))
	require.NoError(t, writeMock.ExpectationsWereMet())

	// GetRun 侧：三字段读回相等，工具序列为脱敏后的结果。
	expectTenantTx(readMock)
	readMock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("run-pt").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "suite_revision_id",
			"passed", "total_cases", "passed_cases", "metrics", "context_snapshot", "created_by", "created_at",
		}).AddRow("run-pt", "prompt", "r-1", "rev-1", "s-1", true, 1, 1, []byte(`{"pass_rate":1}`), []byte("{}"), "creator-1", now))
	readMock.ExpectQuery("SELECT case_id, passed, actual_output").
		WithArgs("run-pt").
		WillReturnRows(pgxmock.NewRows([]string{
			"case_id", "passed", "actual_output", "message", "error_message", "trace_id",
			"tokens", "cost_usd", "duration_ms", "dimensions", "failure_reason", "trace_evidence",
			"process_pass", "process_failure", "tool_sequence",
		}).AddRow("case-1", true, []byte(`{"ok":true}`), "m", "", "tr-1", 5, 0.1, 2,
			[]byte("[]"), "", []byte("null"),
			true, "process:must_not_call:delete", toolSeqJSON))
	readMock.ExpectCommit()
	readRepo := &PgRunRepository{pool: readMock}
	got, found, err := readRepo.GetRun(context.Background(), "t1", "run-pt")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.Results, 1)
	require.True(t, got.Results[0].ProcessPass)
	require.Equal(t, run.Results[0].ProcessFailure, got.Results[0].ProcessFailure)
	require.Equal(t, sanitized, got.Results[0].Tools)
	require.NoError(t, readMock.ExpectationsWereMet())
}

// TestRunRoundTripSnapshot 验证 SaveRun（含 ContextSnapshot）→ GetRun 的
// context_snapshot 列 round-trip：存入的非 nil 快照读回相等；"{}"（旧 run /
// 未捕获）读回 nil，与 omitempty 序列化自洽（§7 版本快照落库）。
func TestRunRoundTripSnapshot(t *testing.T) {
	writeMock := newMockRepo(t)
	readMock := newMockRepo(t)
	now := time.Now().Truncate(time.Microsecond).UTC()

	snap := &domain.EvaluationContextSnapshot{
		SchemaVersion: domain.SnapshotSchemaVersion,
		Evaluation: domain.GroupSnapshot{
			GroupKey:   domain.GroupEvaluation,
			VersionSeq: 7,
			Values:     map[string]any{"judge_model": "qwen-max", "observe": "enabled", "pass_threshold": 0.8},
		},
		Execution: []domain.GroupSnapshot{
			{GroupKey: domain.GroupAgent, VersionSeq: 3, Values: map[string]any{"model": "qwen-plus", "temperature": 0.3}},
			{GroupKey: domain.GroupTrace, VersionSeq: 1, Values: map[string]any{"level": "step", "verbose": true}},
		},
		ResolvedExecution: domain.ResolvedExecution{ContextWindow: 2000, OutputReserve: 500},
		PinnedAssignments: domain.PinnedAssignments{
			SkillAgentRevision: map[string]string{"skill-1": "rev-abc"},
			SkillRevisions:     map[string]string{"skill-1": "rev-skill-7"},
			MCPRevisions:       map[string]string{"mcp-1": "rev-mcp"},
			KnowledgeRevisions: map[string]string{"kb-1": "rev-kb"},
		},
		CapturedAt: time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC),
		CapturedBy: "user-1",
	}
	snapshotJSON, err := json.Marshal(snap)
	require.NoError(t, err)

	run := domain.EvalRun{
		ID:              "run-snap",
		Resource:        domain.ResourceRef{Kind: "prompt", ResourceID: "r-1", RevisionID: "rev-1"},
		SuiteRevisionID: "s-1",
		Passed:          true,
		TotalCases:      1,
		PassedCases:     1,
		Metrics:         map[string]any{"pass_rate": 1.0},
		CreatedAt:       now,
		ContextSnapshot: snap,
	}

	// SaveRun 侧：context_snapshot 序列化进 INSERT（pgx v5 JSONB 收 string）。
	expectTenantTx(writeMock)
	writeMock.ExpectExec("INSERT INTO eval_runs").
		WithArgs("run-snap", "prompt", "r-1", "rev-1", "s-1", true, 1, 1,
			`{"pass_rate":1}`, string(snapshotJSON), "", now).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	writeMock.ExpectCommit()
	writeRepo := &PgRunRepository{pool: writeMock}
	require.NoError(t, writeRepo.SaveRun(context.Background(), "t1", run))
	require.NoError(t, writeMock.ExpectationsWereMet())

	// GetRun 侧：context_snapshot 列读回并反序列化，快照相等。
	expectTenantTx(readMock)
	readMock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("run-snap").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "suite_revision_id",
			"passed", "total_cases", "passed_cases", "metrics", "context_snapshot", "created_by", "created_at",
		}).AddRow("run-snap", "prompt", "r-1", "rev-1", "s-1", true, 1, 1,
			[]byte(`{"pass_rate":1}`), snapshotJSON, "creator-1", now))
	readMock.ExpectQuery("SELECT case_id, passed, actual_output").
		WithArgs("run-snap").
		WillReturnRows(pgxmock.NewRows([]string{
			"case_id", "passed", "actual_output", "message", "error_message", "trace_id",
			"tokens", "cost_usd", "duration_ms", "dimensions", "failure_reason", "trace_evidence",
			"process_pass", "process_failure", "tool_sequence",
		}))
	readMock.ExpectCommit()
	readRepo := &PgRunRepository{pool: readMock}
	got, found, err := readRepo.GetRun(context.Background(), "t1", "run-snap")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, snap, got.ContextSnapshot)
	require.NoError(t, readMock.ExpectationsWereMet())
}

// TestRunRoundTripSnapshotEmptyJSON 验证旧 run（context_snapshot='{}'）读回
// nil ContextSnapshot，与 omitempty 序列化行为自洽。
func TestRunRoundTripSnapshotEmptyJSON(t *testing.T) {
	mock := newMockRepo(t)
	repo := &PgRunRepository{pool: mock}
	now := time.Now().Truncate(time.Microsecond).UTC()

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, resource_kind, resource_id, revision_id, suite_revision_id").
		WithArgs("run-legacy").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "resource_kind", "resource_id", "revision_id", "suite_revision_id",
			"passed", "total_cases", "passed_cases", "metrics", "context_snapshot", "created_by", "created_at",
		}).AddRow("run-legacy", "prompt", "r-1", "rev-1", "s-1", true, 1, 1,
			[]byte(`{"pass_rate":1}`), []byte("{}"), "creator-1", now))
	mock.ExpectQuery("SELECT case_id, passed, actual_output").
		WithArgs("run-legacy").
		WillReturnRows(pgxmock.NewRows([]string{
			"case_id", "passed", "actual_output", "message", "error_message", "trace_id",
			"tokens", "cost_usd", "duration_ms", "dimensions", "failure_reason", "trace_evidence",
			"process_pass", "process_failure", "tool_sequence",
		}))
	mock.ExpectCommit()

	got, found, err := repo.GetRun(context.Background(), "t1", "run-legacy")
	require.NoError(t, err)
	require.True(t, found)
	require.Nil(t, got.ContextSnapshot)
	require.NoError(t, mock.ExpectationsWereMet())
}
