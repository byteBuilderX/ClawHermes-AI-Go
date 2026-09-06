package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHealthRequiresCurrentRunnerIdentity(t *testing.T) {
	h := newHandler("run-1")
	for _, tc := range []struct {
		header string
		status int
	}{{"run-1", http.StatusNoContent}, {"wrong", http.StatusConflict}, {"", http.StatusConflict}} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set("X-Stratum-E2E-Instance", tc.header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("header=%q status=%d", tc.header, rec.Code)
		}
		if tc.status == http.StatusNoContent && rec.Header().Get("X-Stratum-E2E-Instance") != "run-1" {
			t.Fatal("missing identity response header")
		}
	}
}

func TestServerConfigRequiresExplicitLoopbackAddress(t *testing.T) {
	if err := validateServerConfig("127.0.0.1:12345", "run-1"); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"", ":19091", "0.0.0.0:19091", "example.com:19091"} {
		if err := validateServerConfig(address, "run-1"); err == nil {
			t.Fatalf("address %q accepted", address)
		}
	}
	if err := validateServerConfig("127.0.0.1:12345", ""); err == nil {
		t.Fatal("missing identity accepted")
	}
}

// TestMCPHandlerServesDeterministicProtocol drives the fixture with the real
// official SDK client (the same library internal/mcp/infrastructure uses):
// initialize → tools/list → tools/call → resources/read must all succeed.
// A hand-rolled POST-only client would pass an incompatible server, so the
// SDK client is the contract here.
func TestMCPHandlerServesDeterministicProtocol(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(mcpHandler))
	t.Cleanup(func() { ts.CloseClientConnections(); ts.Close() })

	sdkClient := mcp.NewClient(&mcp.Implementation{Name: "fixture-test", Version: "1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: ts.URL}
	session, err := sdkClient.Connect(context.Background(), transport, &mcp.ClientSessionOptions{
		ProtocolVersion: "2025-06-18",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	initResult := session.InitializeResult()
	if initResult.ServerInfo.Name != "stratum-stateful-mcp" || initResult.ServerInfo.Version != "1.0.0" {
		t.Fatalf("serverInfo=%+v", initResult.ServerInfo)
	}

	tools, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "stateful_echo" {
		t.Fatalf("tools=%+v", tools.Tools)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "stateful_echo", Arguments: json.RawMessage(`{"text":"probe"}`),
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("call content=%+v", result.Content)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "stateful MCP call completed" {
		t.Fatalf("call text=%+v", result.Content[0])
	}

	resources, err := session.ListResources(context.Background(), &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	if len(resources.Resources) != 1 || resources.Resources[0].URI != "stratum://stateful/evidence" {
		t.Fatalf("resources=%+v", resources.Resources)
	}
}

func TestHandlerServesOpenAICompatibleCompletion(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"qwen-max","messages":[{"role":"user","content":"stateful"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content == "" {
		t.Fatalf("response=%s", rec.Body.String())
	}
}

func TestHandlerServesOpenAICompatibleModels(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	// modelsHandler returns 4 models: qwen-turbo, qwen-plus, qwen-max, text-embedding-v3
	if len(response.Data) != 4 || response.Data[0].ID != "qwen-turbo" || response.Data[3].ID != "text-embedding-v3" {
		t.Fatalf("response=%s", rec.Body.String())
	}
}

// TestCompletionReturnsOptimizationCandidatesByPromptMarker 验证优化器请求按
// 系统提示词标记识别（不再绑定具体模型名）。
func TestCompletionReturnsOptimizationCandidatesByPromptMarker(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(
		`{"model":"qwen-plus","messages":[{"role":"system","content":"你是提示词优化器。只生成候选内容，不决定发布。仅输出 JSON 数组。"}]}`))
	rec := httptest.NewRecorder()
	completionHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 {
		t.Fatalf("response=%s", rec.Body.String())
	}
	var candidates []struct {
		PromptPatch map[string]any `json:"prompt_patch"`
		Rationale   string         `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(response.Choices[0].Message.Content), &candidates); err != nil {
		t.Fatalf("optimization content is not JSON: %v content=%q", err, response.Choices[0].Message.Content)
	}
	if len(candidates) != 2 || candidates[0].PromptPatch["instructions"] == nil || candidates[1].Rationale == "" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestCompletionAdvancesFromSkillActivationToMCPTool(t *testing.T) {
	t.Parallel()
	tools := `"tools":[` +
		`{"type":"function","function":{"name":"stratum_create_plan","parameters":{"type":"object"}}},` +
		`{"type":"function","function":{"name":"skill-1","parameters":{"type":"object","properties":{"input":{"type":"string"}}}}},` +
		`{"type":"function","function":{"name":"mcp:server-1:stateful_echo","parameters":{"type":"object"}}}]`

	activation := completionToolName(t, `{"messages":[{"role":"user"}],`+tools+`}`)
	if activation != "skill-1" {
		t.Fatalf("first tool=%q", activation)
	}
	mcp := completionToolName(t, `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"skill-1"}}]},{"role":"tool"}],`+tools+`}`)
	if mcp != "mcp:server-1:stateful_echo" {
		t.Fatalf("second tool=%q", mcp)
	}
}

func TestHandlerServesOpenAICompatibleEmbeddings(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewBufferString(
		`{"model":"text-embedding-v3","input":["first","second"]}`))
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("unexpected embedding item count: %d", len(response.Data))
	}
	if len(response.Data[0].Embedding) != 1024 || response.Data[1].Index != 1 {
		t.Fatalf("unexpected embedding response: dimensions=%d second_index=%d", len(response.Data[0].Embedding), response.Data[1].Index)
	}
}

func TestHandlerServesRegisteredOpikEvidence(t *testing.T) {
	t.Parallel()
	traceID := "trace-opik-contract"
	register := httptest.NewRequest(http.MethodPost, "/e2e/opik/register", bytes.NewBufferString(
		`{"trace_id":"`+traceID+`","tenant_id":"tenant-1","user_id":"user-1",`+
			`"resource_id":"skill-1","revision_id":"revision-1"}`))
	registerRec := httptest.NewRecorder()
	handler(registerRec, register)
	if registerRec.Code != http.StatusNoContent {
		t.Fatalf("register status=%d body=%s", registerRec.Code, registerRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/opik/v1/private/traces?filters=ignored", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trace status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`skill:skill-1`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`revision-1`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`opik.metadata.stratum.user_id`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`user-1`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(traceID)) {
		t.Fatalf("trace evidence=%s", rec.Body.String())
	}

	spans := httptest.NewRequest(http.MethodGet, "/opik/v1/private/spans?trace_id=opik-"+traceID, nil)
	spansRec := httptest.NewRecorder()
	handler(spansRec, spans)
	if spansRec.Code != http.StatusOK || !bytes.Contains(spansRec.Body.Bytes(), []byte(`"content":[]`)) {
		t.Fatalf("span evidence status=%d body=%s", spansRec.Code, spansRec.Body.String())
	}
}

func TestHandlerServesRegisteredOpikEvidenceWithRegistrableKind(t *testing.T) {
	t.Parallel()
	traceID := "trace-opik-agent-kind"
	register := httptest.NewRequest(http.MethodPost, "/e2e/opik/register", bytes.NewBufferString(
		`{"trace_id":"`+traceID+`","tenant_id":"tenant-1","user_id":"user-1",`+
			`"resource_kind":"agent","resource_id":"agent-1","revision_id":"revision-1"}`))
	registerRec := httptest.NewRecorder()
	handler(registerRec, register)
	if registerRec.Code != http.StatusNoContent {
		t.Fatalf("register status=%d body=%s", registerRec.Code, registerRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/opik/v1/private/traces", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trace status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`agent:agent-1`)) {
		t.Fatalf("registered registrable kind must surface agent:agent-1 manifest key; trace evidence=%s", rec.Body.String())
	}
}

func TestCompletionRecordsContextMarkersWithoutReturningPrompt(t *testing.T) {
	register := httptest.NewRequest(http.MethodPost, "/e2e/context/register", bytes.NewBufferString(
		`{"knowledge_marker":"knowledge-42","memory_marker":"memory-73"}`))
	registerRec := httptest.NewRecorder()
	handler(registerRec, register)
	if registerRec.Code != http.StatusNoContent {
		t.Fatalf("register status=%d", registerRec.Code)
	}
	completion := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(
		`{"messages":[{"role":"system","content":"knowledge-42 and memory-73"}]}`))
	completionRec := httptest.NewRecorder()
	handler(completionRec, completion)

	evidence := httptest.NewRequest(http.MethodGet, "/e2e/context/evidence", nil)
	evidenceRec := httptest.NewRecorder()
	handler(evidenceRec, evidence)
	if evidenceRec.Code != http.StatusOK || evidenceRec.Body.String() != "{\"knowledge_seen\":true,\"memory_seen\":true}\n" {
		t.Fatalf("context evidence=%s", evidenceRec.Body.String())
	}
	if bytes.Contains(completionRec.Body.Bytes(), []byte("knowledge-42")) ||
		bytes.Contains(completionRec.Body.Bytes(), []byte("memory-73")) {
		t.Fatalf("completion exposed context markers: %s", completionRec.Body.String())
	}
}

func completionToolName(t *testing.T, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	completionHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("response=%s", rec.Body.String())
	}
	return response.Choices[0].Message.ToolCalls[0].Function.Name
}
