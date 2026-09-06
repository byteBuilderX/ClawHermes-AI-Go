//go:build contracts

// Package main records HTTP contract golden files by replaying canonical
// requests against the current SetupRouter and writing JSON snapshots.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/byteBuilderX/stratum/api"
	apihttp "github.com/byteBuilderX/stratum/api/http"
	"github.com/byteBuilderX/stratum/api/http/contracttest"
	"github.com/byteBuilderX/stratum/config"
	iamport "github.com/byteBuilderX/stratum/internal/iam/domain/port"
	iamtoken "github.com/byteBuilderX/stratum/internal/iam/infrastructure/token"
	llmgateway "github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

// Case represents a single recorded request/response snapshot.
type Case struct {
	Name       string            `json:"name"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       json.RawMessage   `json:"body,omitempty"`
	WantStatus int               `json:"want_status"`
	WantBody   json.RawMessage   `json:"want_body,omitempty"`
	WantBodyRE string            `json:"want_body_regex,omitempty"`
}

// isDDDAuthOverride decides which auth claims each DDD route is recorded
// under. It MUST mirror the claims switch in api/http/contract_test.go
// TestContracts prefix-for-prefix: the admin five prefixes get
// Role+GlobalRole+TenantID, every other DDD prefix gets Role+TenantID, and
// nothing outside /admin/* is ever signed as global_admin. Keeping these two
// in lockstep is what makes re-recorded goldens byte-identical to committed.
func isDDDAuthOverride(routePath string) (bool, iamport.TokenClaims) {
	adminFull := iamport.TokenClaims{
		Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin", GlobalRole: "global_admin",
	}
	adminClaims := iamport.TokenClaims{Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin"}
	switch {
	case strings.HasPrefix(routePath, "/admin/tenants"),
		strings.HasPrefix(routePath, "/admin/providers"),
		strings.HasPrefix(routePath, "/admin/models"),
		strings.HasPrefix(routePath, "/admin/admins"),
		strings.HasPrefix(routePath, "/admin/users"):
		return true, adminFull
	case strings.HasPrefix(routePath, "/tenant/"), strings.HasPrefix(routePath, "/workflows"),
		strings.HasPrefix(routePath, "/workflow-runs"), strings.HasPrefix(routePath, "/workflow-approvals"),
		strings.HasPrefix(routePath, "/operation-proposals"), strings.HasPrefix(routePath, "/scheduled-tasks"),
		strings.HasPrefix(routePath, "/audit"):
		return true, adminClaims
	default:
		return false, iamport.TokenClaims{}
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: record-contracts <out-dir>")
		os.Exit(2)
	}
	outDir := os.Args[1]
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	cfg.GitHubClientID = "contract-recorder"
	cfg.GitHubClientSecret = "contract-recorder"
	cfg.JWTPrivateKeyPEM = mustGeneratePEM()

	logger, _ := observability.NewLogger("test")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	metrics := observability.NewPrometheusMetrics(logger)
	gateway := llmgateway.NewGateway(nil, nil, nil).WithLogger(logger)
	router := api.SetupRouter(cfg, logger, gateway, nil, nil, nil, nil)

	ddRouter := apihttp.NewRouter(contracttest.BuildContainer(cfg, key, logger, metrics))
	jwtSvc := iamtoken.NewJWTService(key)

	// Phase 1: legacy router records ALL routes (unauth baseline).
	recordLegacyRoutes(router, outDir)

	// Phase 2: DDD router overwrites selected routes with auth responses.
	recordDDDRoutes(ddRouter, jwtSvc, outDir)

	fmt.Printf("done recording\n")
}

func recordLegacyRoutes(router *gin.Engine, outDir string) {
	for _, route := range router.Routes() {
		filename := goldenName(route.Method, route.Path)
		recordRoute(router, route.Method, route.Path, filepath.Join(outDir, filename))
	}
}

func recordDDDRoutes(ddRouter *gin.Engine, jwtSvc iamport.TokenService, outDir string) {
	evalWhitelist := map[string]bool{
		"GET /evaluations/overview": true, "GET /evaluations/resources": true,
		"GET /evaluations/suites": true, "GET /evaluations/runs": true,
		"GET /evaluations/candidates": true, "GET /evaluations/experiments": true,
		"GET /evaluations/resources/:kind/:id/timeline": true,
		"GET /evaluations/monitoring/resources":         true,
		"GET /evaluations/monitoring/resources/trend":   true,
		"POST /evaluations/candidates/:id/reject":       true,
		"POST /evaluations/experiments/:id/pause":       true,
		"POST /evaluations/experiments/:id/promote":     true,
		"POST /evaluations/experiments/:id/rollback":    true,
	}
	for _, route := range ddRouter.Routes() {
		filename := filepath.Join(outDir, goldenName(route.Method, route.Path))
		recordDDDRoute(ddRouter, jwtSvc, route.Method, route.Path, filename, evalWhitelist)
	}
}

func recordDDDRoute(router *gin.Engine, jwtSvc iamport.TokenService, method, routePath, filename string, evalWhitelist map[string]bool) {
	routeKey := method + " " + routePath
	switch {
	case isReviewRoute(routeKey):
		// 评审池 POST 决策请求体与 evalWhitelist 的通用 POST 不同（无 idempotency
		// key），且 reviewed_at 是 live 时间戳需正则断言，故单独录制。
		recordReviewRoute(router, jwtSvc, method, routePath, filename)
	case evalWhitelist[routeKey]:
		recordEvalRoute(router, jwtSvc, method, routePath, filename)
	case routeKey == "POST /agents/:id/self-modify":
		// Proposal ID is a random UUID: record a regex assertion instead
		// of a byte-exact body so replay is deterministic.
		recordSelfModifyRoute(router, jwtSvc, routePath, filename)
	default:
		recordAuthOverride(router, jwtSvc, method, routePath, routeKey, filename)
	}
}

func recordAuthOverride(router *gin.Engine, jwtSvc iamport.TokenService, method, routePath, routeKey, filename string) {
	ok, claims := isDDDAuthOverride(routePath)
	if !ok {
		return
	}
	var body json.RawMessage
	if routeKey == "POST /admin/admins" {
		body = json.RawMessage(`{"user_id":"contract-user"}`)
	}
	recordAuthRoute(router, jwtSvc, claims, method, routePath, body, filename)
}

func goldenName(method, path string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", "*", "_").Replace(path)
	return fmt.Sprintf("%s%s.golden.json", strings.ToLower(method), safe)
}

func recordAuthRoute(router http.Handler, tokens iamport.TokenService, claims iamport.TokenClaims,
	method, routePath string, body json.RawMessage, outPath string,
) {
	path := resolvePath(routePath, method, body)
	token, err := tokens.Sign(claims, time.Hour)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	c := Case{Name: "authenticated-success", Method: method, Path: path, WantStatus: rec.Code}
	if body != nil {
		c.Body = body
	}
	if json.Valid(rec.Body.Bytes()) {
		c.WantBody = json.RawMessage(rec.Body.Bytes())
	}
	out, _ := json.MarshalIndent([]Case{c}, "", "  ")
	writeGolden(outPath, out)
}

func recordEvalRoute(router http.Handler, tokens iamport.TokenService, method, routePath, outPath string) {
	path := strings.ReplaceAll(routePath, ":kind", "skill")
	path = strings.ReplaceAll(path, ":id", "resource-1")
	if method == http.MethodPost {
		path = strings.ReplaceAll(routePath, ":id", "experiment-1")
		if strings.Contains(routePath, "/candidates/") {
			path = strings.ReplaceAll(routePath, ":id", "candidate-1")
		}
	}
	// 评测监控 GET 端点（spec §4.2）附加固定 query：resources 回显 window=from/to
	// （空窗会兜底 live now，golden 不可复现）；trend 的 resource_kind+id 必填。
	if method == http.MethodGet && strings.HasPrefix(routePath, "/evaluations/monitoring/") {
		path = monitoringQueryPath(routePath, path)
	}
	c := Case{Name: "authenticated-success", Method: method, Path: path}
	if method == http.MethodPost {
		c.Name = "authenticated-conflict"
		c.Body = json.RawMessage(`{"reason":"reviewed","idempotency_key":"contract-request","expected_state_version":1}`)
	}
	token, err := tokens.Sign(iamport.TokenClaims{Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin"}, time.Hour)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(c.Body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	c.WantStatus = rec.Code
	if json.Valid(rec.Body.Bytes()) {
		c.WantBody = json.RawMessage(rec.Body.Bytes())
	}
	out, _ := json.MarshalIndent([]Case{c}, "", "  ")
	writeGolden(outPath, out)
}

// monitoringQueryPath 为评测监控 GET 端点附加确定性 query（spec §4.2）。固定
// 窗口与 stub 样例的 time.Date 常量一致，保证 regen 逐字节可复现。
func monitoringQueryPath(routePath, path string) string {
	const fixedWindow = "from=2026-08-27T00:00:00Z&to=2026-09-03T00:00:00Z"
	if strings.HasSuffix(routePath, "/trend") {
		return path + "?resource_kind=skill&resource_id=resource-1&" + fixedWindow
	}
	return path + "?" + fixedWindow
}

// isReviewRoute 判断是否为 P1c 评审池路由（查询 + 决策）。
func isReviewRoute(routeKey string) bool {
	return strings.HasPrefix(routeKey, "GET /evaluations/review") ||
		strings.HasPrefix(routeKey, "POST /evaluations/review")
}

// recordReviewRoute 录制评审池查询/决策 golden。POST 决策的 reviewed_at 是 live
// 时间戳，故决策 case 用 WantBodyRE 断言结构而非逐字节断言；GET 用例返回确定性
// 条目，录精确 WantBody。
func recordReviewRoute(router http.Handler, tokens iamport.TokenService, method, routePath, outPath string) {
	path := strings.ReplaceAll(routePath, ":id", "review-1")
	c := Case{Name: "authenticated-success", Method: method, Path: path}
	if method == http.MethodPost {
		c.Name = "authenticated-reviewed"
		c.Body = json.RawMessage(`{"verdict":"pass","reason":"contract-approved"}`)
	}
	token, err := tokens.Sign(iamport.TokenClaims{Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin"}, time.Hour)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(c.Body))
	req.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	c.WantStatus = rec.Code
	if method == http.MethodPost {
		// pending → reviewed 转换后 reviewed_at 为 live 时间戳：仅断言结构。
		c.WantBodyRE = `\{"id":"review-1","source_type":"observation","source_id":"obs-1",` +
			`"run_id":"run-1","trace_id":"trace-1","resource_kind":"agent","resource_id":"agent-1",` +
			`"trigger_reason":"low_confidence","risk_level":"medium","snapshot":\{"signals":\{"judge":\[\]\}\},` +
			`"status":"reviewed","human_verdict":"pass","reviewer":"contract-admin",` +
			`"review_reason":"contract-approved","created_at":"2026-01-01T00:00:00Z",` +
			`"reviewed_at":"[^"]*"\}`
	} else if json.Valid(rec.Body.Bytes()) {
		c.WantBody = json.RawMessage(rec.Body.Bytes())
	}
	out, _ := json.MarshalIndent([]Case{c}, "", "  ")
	writeGolden(outPath, out)
}

func recordSelfModifyRoute(router http.Handler, tokens iamport.TokenService, routePath, outPath string) {
	path := strings.ReplaceAll(routePath, ":id", "contract-id")
	body := json.RawMessage(`{"name":"contract-renamed","description":"contract","systemPrompt":"prompt",
"llmModel":"qwen-plus","maxIterations":10,"maxContextTokens":8000,"allowedSkills":[],
"mcpToolIds":[],"knowledgeWorkspaceIds":[],"memoryScope":"user"}`)
	c := Case{
		Name: "authenticated-pending", Method: http.MethodPost, Path: path, Body: body,
		WantStatus: http.StatusAccepted,
		// proposalId is a random UUID at record time; assert shape only.
		// Response is a gin.H map: encoding/json sorts map keys alphabetically.
		WantBodyRE: `\{"proposalId":"[0-9a-f-]+","reason":"pending_approval","status":"pending_approval"\}`,
	}
	token, err := tokens.Sign(iamport.TokenClaims{Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin"}, time.Hour)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(c.Body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != c.WantStatus {
		panic(fmt.Sprintf("self-modify: got status %d, want %d: %s", rec.Code, c.WantStatus, rec.Body.String()))
	}
	out, _ := json.MarshalIndent([]Case{c}, "", "  ")
	writeGolden(outPath, out)
}

// resolvePath replaces path params with placeholder IDs.
func resolvePath(routePath, method string, body json.RawMessage) string {
	p := routePath
	p = strings.ReplaceAll(p, ":provider_id", "contract-provider")
	p = strings.ReplaceAll(p, ":model_id", "contract-model")
	p = strings.ReplaceAll(p, ":tenant_id", "contract-tenant-id")
	p = strings.ReplaceAll(p, ":user_id", "contract-user")
	p = strings.ReplaceAll(p, ":member_id", "contract-member")
	p = strings.ReplaceAll(p, ":workflowId", "contract-workflow")
	p = strings.ReplaceAll(p, ":runId", "contract-run")
	p = strings.ReplaceAll(p, ":id", "contract-id")
	return p
}

// ── Legacy router recording (unauth baseline) ──────────────────────────

func recordRoute(router http.Handler, method, path, outPath string) {
	cases := []Case{{
		Name:       "default-unauth",
		Method:     method,
		Path:       path,
		WantStatus: 0,
	}}
	for i := range cases {
		req := httptest.NewRequest(cases[i].Method, cases[i].Path, bytes.NewReader(cases[i].Body))
		for k, v := range cases[i].Headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		cases[i].WantStatus = rec.Code
		body, _ := io.ReadAll(rec.Body)
		if json.Valid(body) {
			cases[i].Body = json.RawMessage(body)
		}
	}
	out, _ := json.MarshalIndent(cases, "", "  ")
	writeGolden(outPath, out)
}

func mustGeneratePEM() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Errorf("generate rsa key: %w", err))
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

// writeGolden 写契约 golden 文件，统一补文件尾换行，使录制产物与提交态
// 逐字节一致（regen 零 diff 守护依赖）。
func writeGolden(outPath string, out []byte) {
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		panic(fmt.Errorf("write golden %s: %w", outPath, err))
	}
}
