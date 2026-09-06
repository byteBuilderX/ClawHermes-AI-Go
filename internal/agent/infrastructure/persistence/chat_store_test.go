package persistence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/pashagolub/pgxmock/v2"
	"go.uber.org/zap"
)

// newChatStoreWithMock returns a PgChatStore backed by pgxmock.
func newChatStoreWithMock(t *testing.T) (*PgChatStore, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	return &PgChatStore{pool: pool, logger: zap.NewNop()}, pool
}

// expectTenantTx expects BEGIN + SET LOCAL search_path for tenant t1.
func expectTenantTx(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL search_path").WillReturnResult(pgxmock.NewResult("SET", 0))
}

func TestChatStore_CreateConversation(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	now := time.Now()
	expectTenantTx(mock)
	mock.ExpectQuery("INSERT INTO chat_conversations").
		WithArgs("agent-1", "user-1", "新会话", "manual").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "agent_id", "user_id", "name", "created_at", "updated_at", "expires_at",
		}).AddRow("conv-uuid", "agent-1", "user-1", "新会话", now, now, now.AddDate(0, 0, 30)))
	mock.ExpectCommit()

	conv, err := store.CreateConversation(context.Background(), "t1", "agent-1", "user-1", "新会话", "manual")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conv.ID != "conv-uuid" {
		t.Errorf("want conv-uuid, got %s", conv.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_ListConversations(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	now := time.Now()
	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, agent_id, user_id, name").
		WithArgs("agent-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "agent_id", "user_id", "name", "created_at", "updated_at", "expires_at",
		}).
			AddRow("c1", "agent-1", "user-1", "Chat A", now, now, now.AddDate(0, 0, 30)).
			AddRow("c2", "agent-1", "user-1", "Chat B", now, now, now.AddDate(0, 0, 30)))
	mock.ExpectCommit()

	convs, err := store.ListConversations(context.Background(), "t1", "agent-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(convs) != 2 {
		t.Errorf("want 2 conversations, got %d", len(convs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

// TestChatStore_ListConversations_FiltersOutWorkflowAndEvaluation 显式断言默认会话列表
// 同时过滤 workflow 与 evaluation 两类非手动来源：评测受控会话（source=evaluation）
// 与工作流自动会话都不应出现在生产默认会话列表，SQL 必须带
// source NOT IN ('workflow', 'evaluation')。
func TestChatStore_ListConversations_FiltersOutWorkflowAndEvaluation(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	now := time.Now()
	expectTenantTx(mock)
	mock.ExpectQuery(`AND source NOT IN \('workflow', 'evaluation'\)`).
		WithArgs("agent-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "agent_id", "user_id", "name", "created_at", "updated_at", "expires_at",
		}).AddRow("c1", "agent-1", "user-1", "Chat A", now, now, now.AddDate(0, 0, 30)))
	mock.ExpectCommit()

	convs, err := store.ListConversations(context.Background(), "t1", "agent-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(convs) != 1 {
		t.Errorf("want 1 conversation, got %d", len(convs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_RenameConversation_success(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	expectTenantTx(mock)
	mock.ExpectExec("UPDATE chat_conversations").
		WithArgs("新名字", "conv-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	if err := store.RenameConversation(context.Background(), "t1", "conv-1", "user-1", "新名字"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_RenameConversation_notFound(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	expectTenantTx(mock)
	mock.ExpectExec("UPDATE chat_conversations").
		WithArgs("新名字", "no-such", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	err := store.RenameConversation(context.Background(), "t1", "no-such", "user-1", "新名字")
	if err == nil {
		t.Fatal("expected error for missing conversation")
	}
}

func TestChatStore_DeleteConversation_success(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	expectTenantTx(mock)
	mock.ExpectExec("DELETE FROM chat_messages").
		WithArgs("conv-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 3))
	mock.ExpectExec("DELETE FROM chat_conversations").
		WithArgs("conv-1", "user-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	if err := store.DeleteConversation(context.Background(), "t1", "conv-1", "user-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_DeleteConversation_notOwned(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	expectTenantTx(mock)
	mock.ExpectExec("DELETE FROM chat_messages").
		WithArgs("conv-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectExec("DELETE FROM chat_conversations").
		WithArgs("conv-1", "other-user").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectRollback()

	err := store.DeleteConversation(context.Background(), "t1", "conv-1", "other-user")
	if err == nil {
		t.Fatal("expected ErrNotFound for unowned conversation")
	}
}

func TestChatStore_AddMessage(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	now := time.Now()
	steps := json.RawMessage(`[{"type":"think","content":"hmm"}]`)
	msg := &domain.ChatMessage{
		ConversationID: "conv-1",
		Role:           "user",
		Content:        "hello",
		StepsJSON:      steps,
		IsError:        false,
		TraceID:        "trace-1",
	}

	expectTenantTx(mock)
	mock.ExpectExec("UPDATE chat_conversations").
		WithArgs("conv-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO chat_messages").
		WithArgs("conv-1", "user", "hello", string(steps), false, "[]", "[]", "user", "trace-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow("msg-uuid", now))
	mock.ExpectCommit()

	if err := store.AddMessage(context.Background(), "t1", msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.ID != "msg-uuid" {
		t.Errorf("want msg-uuid, got %s", msg.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_AddMessage_nilStepsDefaultsToEmpty(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	now := time.Now()
	msg := &domain.ChatMessage{
		ConversationID: "conv-1",
		Role:           "user",
		Content:        "hi",
		StepsJSON:      nil,
		TraceID:        "trace-2",
	}

	expectTenantTx(mock)
	mock.ExpectExec("UPDATE chat_conversations").
		WithArgs("conv-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO chat_messages").
		WithArgs("conv-1", "user", "hi", "[]", false, "[]", "[]", "user", "trace-2").
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow("msg-2", now))
	mock.ExpectCommit()

	if err := store.AddMessage(context.Background(), "t1", msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_ListMessages(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	now := time.Now()
	expectTenantTx(mock)
	mock.ExpectQuery("SELECT m.id, m.conversation_id").
		WithArgs("conv-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "conversation_id", "role", "content", "steps_json", "is_error", "created_at", "artifacts_json", "sources_json", "visibility",
		}).
			AddRow("m1", "conv-1", "user", "hi", json.RawMessage("[]"), false, now, json.RawMessage("[]"), json.RawMessage("[]"), "user").
			AddRow("m2", "conv-1", "assistant", "hello back", json.RawMessage("[]"), false, now, json.RawMessage("[]"), json.RawMessage("[]"), "user"))
	mock.ExpectCommit()

	msgs, err := store.ListMessages(context.Background(), "t1", "conv-1", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("want 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("unexpected roles: %s, %s", msgs[0].Role, msgs[1].Role)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_ArtifactRoundTrip(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()
	now := time.Now()
	artifacts := []domain.ExecutionArtifact{{Type: "diagnostic_report", ProfileVersion: "v1", DiagnosticReport: &domain.DiagnosticReport{Inferences: []string{}}}}
	raw, err := encodeExecutionArtifacts(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	msg := &domain.ChatMessage{ConversationID: "conv-1", Role: "assistant", Content: "ok", Artifacts: artifacts}
	expectTenantTx(mock)
	mock.ExpectExec("UPDATE chat_conversations").WithArgs("conv-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO chat_messages").WithArgs("conv-1", "assistant", "ok", "[]", false, string(raw), "[]", "user", "").
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow("m1", now))
	mock.ExpectCommit()
	if err := store.AddMessage(context.Background(), "t1", msg); err != nil {
		t.Fatal(err)
	}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT m.id, m.conversation_id").WithArgs("conv-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "conversation_id", "role", "content", "steps_json", "is_error", "created_at", "artifacts_json", "sources_json", "visibility"}).
			AddRow("m1", "conv-1", "assistant", "ok", json.RawMessage("[]"), false, now, raw, json.RawMessage("[]"), "user"))
	mock.ExpectCommit()
	got, err := store.ListMessages(context.Background(), "t1", "conv-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Artifacts) != 1 || got[0].Artifacts[0].ProfileVersion != "v1" {
		t.Fatalf("unexpected artifacts: %#v", got)
	}
}

// TestExecutionArtifactsAllTypesRoundTrip pins the full artifact type set the
// execution layer can produce — proposal and direct-apply evidence included —
// so encodeExecutionArtifacts (the AddMessage gate) never rejects a reply the
// agent just generated (regression: assistant replies were dropped on save).
func TestExecutionArtifactsAllTypesRoundTrip(t *testing.T) {
	now := time.Now()
	artifacts := []domain.ExecutionArtifact{
		{Type: "citations", ProfileVersion: "2026-08-08.v3", Citations: []domain.Citation{{DocumentID: "doc-1", Section: "s", URL: "u", Title: "t", ProductVersion: "v", Excerpt: "e"}}},
		{Type: "diagnostic_report", ProfileVersion: "2026-08-08.v3", DiagnosticReport: &domain.DiagnosticReport{
			Facts: []domain.DiagnosticFact{}, Inferences: []string{}, EvidenceGaps: []domain.EvidenceGap{},
			RecommendedActions: []string{}, Citations: []domain.Citation{}, Steps: []domain.DiagnosticStep{},
		}},
		{Type: "resource_change_proposal", ProfileVersion: "2026-08-08.v3", ResourceChangeProposal: &domain.ResourceChangeProposalArtifact{
			ID: "proposal-1", ResourceKind: domain.ResourceAgent, Operation: domain.OperationUpdate,
			Status: domain.StatusReadyForReview, Summary: "update agent model", ExpiresAt: now,
		}},
		{Type: "resource_change_direct_apply", ProfileVersion: "2026-08-08.v3", DirectApply: &domain.SystemAssistantDirectApplyArtifact{
			Tool: domain.SystemAssistantToolApplyResourceChange, ResourceKind: domain.ResourceAgent,
			Operation: domain.OperationUpdate, ResourceID: "agent-1", Outcome: "success",
		}},
	}
	raw, err := encodeExecutionArtifacts(artifacts)
	if err != nil {
		t.Fatalf("encode all artifact types: %v", err)
	}
	got, err := decodeExecutionArtifacts(raw)
	if err != nil {
		t.Fatalf("decode all artifact types: %v", err)
	}
	if len(got) != len(artifacts) {
		t.Fatalf("want %d artifacts, got %d", len(artifacts), len(got))
	}
	for i, a := range got {
		if a.Type != artifacts[i].Type {
			t.Fatalf("artifact %d type mismatch: %s != %s", i, a.Type, artifacts[i].Type)
		}
	}
}

func TestChatStore_HistoricalMessageHydratesEmptyArtifacts(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()
	now := time.Now()
	expectTenantTx(mock)
	mock.ExpectQuery("SELECT m.id, m.conversation_id").WithArgs("conv-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "conversation_id", "role", "content", "steps_json", "is_error", "created_at", "artifacts_json", "sources_json", "visibility"}).
			AddRow("m1", "conv-1", "assistant", "old", json.RawMessage("[]"), false, now, json.RawMessage("[]"), json.RawMessage("[]"), "user"))
	mock.ExpectCommit()
	got, err := store.ListMessages(context.Background(), "t1", "conv-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Artifacts == nil || len(got[0].Artifacts) != 0 {
		t.Fatalf("want non-nil empty artifacts: %#v", got[0].Artifacts)
	}
}

func TestChatStore_MalformedArtifactsReturnError(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()
	now := time.Now()
	expectTenantTx(mock)
	mock.ExpectQuery("SELECT m.id, m.conversation_id").WithArgs("conv-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "conversation_id", "role", "content", "steps_json", "is_error", "created_at", "artifacts_json", "sources_json", "visibility"}).
			AddRow("m1", "conv-1", "assistant", "bad", json.RawMessage("[]"), false, now, []byte("not-json"), json.RawMessage("[]"), "user"))
	mock.ExpectRollback()
	_, err := store.ListMessages(context.Background(), "t1", "conv-1", "user-1")
	if err == nil {
		t.Fatal("expected malformed artifact error")
	}
}

func TestDecodeExecutionArtifactsRejectsInvalidPersistedShapes(t *testing.T) {
	tests := map[string]string{
		"null":                   `null`,
		"unknown top field":      `[{"type":"citations","profileVersion":"v1","citations":[],"extra":1}]`,
		"unknown nested field":   `[{"type":"diagnostic_report","profileVersion":"v1","diagnosticReport":{"facts":[],"inferences":[],"evidenceGaps":[],"recommendedActions":[],"citations":[],"steps":[],"extra":1}}]`,
		"empty artifact":         `[{}]`,
		"wrong discriminator":    `[{"type":"other","profileVersion":"v1"}]`,
		"exclusive fields":       `[{"type":"citations","profileVersion":"v1","citations":[],"diagnosticReport":{"facts":[],"inferences":[],"evidenceGaps":[],"recommendedActions":[],"citations":[],"steps":[]}}]`,
		"proposal exclusive":     `[{"type":"resource_change_proposal","profileVersion":"v1","resourceChangeProposal":{"id":"p1","resourceKind":"agent","operation":"update","status":"draft","summary":"s","expiresAt":"2026-08-12T00:00:00Z"},"citations":[]}]`,
		"direct apply exclusive": `[{"type":"resource_change_direct_apply","profileVersion":"v1","directApply":{"tool":"stratum_apply_resource_change","resourceKind":"agent","operation":"update","resourceId":"a1","outcome":"success"},"diagnosticReport":{"facts":[],"inferences":[],"evidenceGaps":[],"recommendedActions":[],"citations":[],"steps":[]}}]`,
		"invalid proposal enum":  `[{"type":"resource_change_proposal","profileVersion":"v1","resourceChangeProposal":{"id":"p1","resourceKind":"bogus","operation":"update","status":"draft","summary":"s"}}]`,
		"invalid apply outcome":  `[{"type":"resource_change_direct_apply","profileVersion":"v1","directApply":{"tool":"stratum_apply_resource_change","resourceKind":"agent","operation":"update","resourceId":"a1","outcome":"maybe"}}]`,
		"trailing json":          `[] {}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeExecutionArtifacts([]byte(raw)); err == nil {
				t.Fatal("expected strict decode error")
			}
		})
	}
	got, err := decodeExecutionArtifacts([]byte(`[]`))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("historical [] decode = %#v, %v", got, err)
	}
}

func TestChatStoreRejectsInvalidArtifactsBeforeTransaction(t *testing.T) {
	tests := map[string][]domain.ExecutionArtifact{
		"wrong type":       {{Type: "other", ProfileVersion: "v1"}},
		"exclusive":        {{Type: "citations", ProfileVersion: "v1", Citations: []domain.Citation{}, DiagnosticReport: &domain.DiagnosticReport{}}},
		"unsafe inference": {{Type: "diagnostic_report", ProfileVersion: "v1", DiagnosticReport: &domain.DiagnosticReport{Inferences: []string{"Authorization: Bearer secret"}}}},
		"unsafe action":    {{Type: "diagnostic_report", ProfileVersion: "v1", DiagnosticReport: &domain.DiagnosticReport{RecommendedActions: []string{"password=secret"}}}},
		"unsafe object":    {{Type: "diagnostic_report", ProfileVersion: "v1", DiagnosticReport: &domain.DiagnosticReport{Facts: []domain.DiagnosticFact{{Area: domain.DiagnosticAreaAgent, ObjectID: "password=secret", Statement: "ok", Source: "source"}}}}},
		"invalid area":     {{Type: "diagnostic_report", ProfileVersion: "v1", DiagnosticReport: &domain.DiagnosticReport{Facts: []domain.DiagnosticFact{{Area: "global", Statement: "ok", Source: "source"}}}}},
		"prose code":       {{Type: "diagnostic_report", ProfileVersion: "v1", DiagnosticReport: &domain.DiagnosticReport{Steps: []domain.DiagnosticStep{{Tool: "tool", Outcome: "maybe later", ErrorCode: "provider said no because prose"}}}}},
		"invalid proposal kind": {
			{Type: "resource_change_proposal", ProfileVersion: "v1", ResourceChangeProposal: &domain.ResourceChangeProposalArtifact{
				ID: "p1", ResourceKind: "bogus", Operation: domain.OperationCreate, Status: domain.StatusDraft, Summary: "s",
			}},
		},
		"invalid apply outcome": {
			{Type: "resource_change_direct_apply", ProfileVersion: "v1", DirectApply: &domain.SystemAssistantDirectApplyArtifact{
				Tool: domain.SystemAssistantToolApplyResourceChange, ResourceKind: domain.ResourceAgent,
				Operation: domain.OperationUpdate, ResourceID: "a1", Outcome: "maybe",
			}},
		},
	}
	for name, artifacts := range tests {
		t.Run(name, func(t *testing.T) {
			store, mock := newChatStoreWithMock(t)
			defer mock.Close()
			err := store.AddMessage(context.Background(), "t1", &domain.ChatMessage{ConversationID: "c1", Role: "assistant", Content: "x", Artifacts: artifacts})
			if err == nil {
				t.Fatal("expected write invariant error")
			}
			if strings.Contains(err.Error(), "begin") {
				t.Fatalf("validation happened after transaction start: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("validation started transaction: %v", err)
			}
		})
	}
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()
	huge := []domain.ExecutionArtifact{{Type: "citations", ProfileVersion: "v1", Citations: []domain.Citation{{DocumentID: "doc", Excerpt: strings.Repeat("x", 40*1024)}}}}
	if err := store.AddMessage(context.Background(), "t1", &domain.ChatMessage{ConversationID: "c1", Role: "assistant", Content: "x", Artifacts: huge}); err == nil || strings.Contains(err.Error(), "begin") {
		t.Fatalf("expected pre-transaction oversize rejection, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("oversize validation started transaction: %v", err)
	}
}

func TestChatStore_CleanupExpired(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	expectTenantTx(mock)
	mock.ExpectExec("DELETE FROM chat_conversations").
		WillReturnResult(pgxmock.NewResult("DELETE", 3))
	mock.ExpectCommit()

	if err := store.CleanupExpired(context.Background(), "t1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestChatStore_InvalidTenantID(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	_, err := store.CreateConversation(context.Background(), "t1; DROP TABLE", "a", "u", "n", "manual")
	if err == nil {
		t.Fatal("expected error for invalid tenant_id")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

// TestChatStore_RejectsUnsafeTenantIDsBeforeTransaction pins the shared
// pkg/storage/postgres validation ([a-z0-9_-] only). The old per-repo
// execTenantID used unicode.IsLetter, which admitted uppercase and Unicode
// tenant IDs into a quoted schema identifier — this must stay rejected before
// any transaction begins.
func TestChatStore_RejectsUnsafeTenantIDsBeforeTransaction(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
	}{
		{name: "empty", tenantID: ""},
		{name: "uppercase", tenantID: "TenantA"},
		{name: "unicode", tenantID: "租户1"},
		{name: "sql injection", tenantID: "t1; DROP TABLE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newChatStoreWithMock(t)
			defer mock.Close()

			_, err := store.GetConversation(context.Background(), tc.tenantID, "conv-1")
			if err == nil {
				t.Fatalf("expected error for invalid tenant_id %q", tc.tenantID)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("invalid tenant_id started a transaction: %v", err)
			}
		})
	}
}

// TestChatStore_ValidTenantIDPassesUnifiedValidation proves the unified
// entry accepts a legal [a-z0-9_-] tenant ID end to end.
func TestChatStore_ValidTenantIDPassesUnifiedValidation(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()

	now := time.Now()
	expectTenantTx(mock)
	mock.ExpectQuery("SELECT id, agent_id, user_id, name").
		WithArgs("conv-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "agent_id", "user_id", "name", "created_at", "updated_at", "expires_at",
		}).AddRow("conv-1", "agent-1", "user-1", "Chat", now, now, now.AddDate(0, 0, 30)))
	mock.ExpectCommit()

	conv, err := store.GetConversation(context.Background(), "t1-tenant_2", "conv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conv.ID != "conv-1" {
		t.Errorf("want conv-1, got %s", conv.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestDecodeSources(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantLen   int
		wantErr   bool
		wantTitle string // 非空时额外断言首条来源的 DocumentTitle 已从 camelCase 字段解码
	}{
		{name: "empty", raw: "", wantLen: 0},
		{name: "null is empty slice", raw: "null", wantLen: 0},
		{name: "empty array", raw: "[]", wantLen: 0},
		{name: "camelCase round trip", raw: `[{"workspaceId":"ws-1","workspaceName":"产品库","chunkId":"c-1","documentId":"doc-1","documentTitle":"用户手册.pdf","snippet":"s","score":0.91,"hasScore":true}]`, wantLen: 1, wantTitle: "用户手册.pdf"},
		{name: "malformed", raw: `{`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeSources([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("want %d sources, got %d (%#v)", tc.wantLen, len(got), got)
			}
			if got == nil {
				t.Fatal("decodeSources must return non-nil slice")
			}
			if tc.wantTitle != "" && got[0].DocumentTitle != tc.wantTitle {
				t.Fatalf("want camelCase DocumentTitle %q decoded, got %#v", tc.wantTitle, got[0])
			}
		})
	}
}

func TestChatStore_SourcesRoundTrip(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()
	now := time.Now()
	sources := []domain.RAGSearchSource{{
		WorkspaceID: "ws-1", WorkspaceName: "产品知识库", ChunkID: "chunk-1",
		DocumentID: "doc-1", DocumentTitle: "用户手册.pdf", Snippet: "s",
		Score: 0.91, HasScore: true,
	}}
	raw, err := encodeSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	msg := &domain.ChatMessage{
		ConversationID: "conv-1", Role: "assistant", Content: "ok",
		Sources: sources, TraceID: "trace-rt",
	}

	expectTenantTx(mock)
	mock.ExpectExec("UPDATE chat_conversations").WithArgs("conv-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO chat_messages").WithArgs("conv-1", "assistant", "ok", "[]", false, "[]", string(raw), "user", "trace-rt").
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow("m1", now))
	mock.ExpectCommit()
	if err := store.AddMessage(context.Background(), "t1", msg); err != nil {
		t.Fatal(err)
	}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT m.id, m.conversation_id").WithArgs("conv-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "conversation_id", "role", "content", "steps_json", "is_error", "created_at", "artifacts_json", "sources_json", "visibility"}).
			AddRow("m1", "conv-1", "assistant", "ok", json.RawMessage("[]"), false, now, json.RawMessage("[]"), raw, "user"))
	mock.ExpectCommit()
	got, err := store.ListMessages(context.Background(), "t1", "conv-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Sources) != 1 || got[0].Sources[0].DocumentTitle != "用户手册.pdf" || got[0].Sources[0].HasScore != true {
		t.Fatalf("unexpected sources: %#v", got[0].Sources)
	}
}
