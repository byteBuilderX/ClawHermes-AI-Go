// Package http_test replays recorded HTTP contract goldens to detect
// backward-incompatible changes during the DDD refactor.
package http_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/api"
	apihttp "github.com/byteBuilderX/stratum/api/http"
	"github.com/byteBuilderX/stratum/api/http/contracttest"
	"github.com/byteBuilderX/stratum/config"
	iamport "github.com/byteBuilderX/stratum/internal/iam/domain/port"
	iamtoken "github.com/byteBuilderX/stratum/internal/iam/infrastructure/token"
	llmgateway "github.com/byteBuilderX/stratum/internal/llmgateway/infrastructure"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

type contractCase struct {
	Name       string            `json:"name"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       json.RawMessage   `json:"body,omitempty"`
	WantBody   json.RawMessage   `json:"want_body,omitempty"`
	WantBodyRE string            `json:"want_body_regex,omitempty"`
	WantStatus int               `json:"want_status"`
}

func TestContracts(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("config load failed: %v", err)
	}
	cfg.GitHubClientID = "contract-recorder"
	cfg.GitHubClientSecret = "contract-recorder"
	cfg.JWTPrivateKeyPEM = mustGeneratePEM(t)

	logger, _ := observability.NewLogger("test")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewPrometheusMetrics(logger)
	gateway := llmgateway.NewGateway(nil, nil, nil).WithLogger(logger)

	// ── Legacy router (auth, health, models catalogue, etc.) ──────────────
	router := api.SetupRouter(cfg, logger, gateway, nil, nil, nil, nil)

	// ── DDD router with full stub population (shared contracttest container) ─
	dddRouter := apihttp.NewRouter(contracttest.BuildContainer(cfg, key, logger, metrics))

	jwtSvc := iamtoken.NewJWTService(key)

	// ── Route dispatch: paths handled by the DDD router ───────────────────
	dddPrefixes := []string{
		"/evaluations/", "/dashboard/", "/resource-change-proposals/",
		"/admin/providers", "/admin/models", "/admin/tenants",
		"/admin/admins", "/admin/users",
		"/tenant/", "/workflows", "/workflow-runs", "/workflow-approvals",
		"/operation-proposals", "/scheduled-tasks", "/audit",
	}

	files, err := filepath.Glob("testdata/contracts/*.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no golden files: run `make record-contracts` first")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var cases []contractCase
			if err := json.Unmarshal(data, &cases); err != nil {
				t.Fatal(err)
			}
			for _, c := range cases {
				req := httptest.NewRequest(c.Method, c.Path, bytes.NewReader(c.Body))

				useDDD := strings.Contains(c.Path, "/self-modify")
				for _, prefix := range dddPrefixes {
					if strings.HasPrefix(c.Path, prefix) {
						useDDD = true
						break
					}
				}

				if useDDD {
					var claims iamport.TokenClaims
					switch {
					case strings.HasPrefix(c.Path, "/admin/tenants"),
						strings.HasPrefix(c.Path, "/admin/providers"),
						strings.HasPrefix(c.Path, "/admin/models"),
						strings.HasPrefix(c.Path, "/admin/admins"),
						strings.HasPrefix(c.Path, "/admin/users"):
						claims = iamport.TokenClaims{
							Sub: "contract-admin", TenantID: "contract-tenant",
							Role: "admin", GlobalRole: "global_admin",
						}
					default:
						claims = iamport.TokenClaims{Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin"}
					}
					token, signErr := jwtSvc.Sign(claims, time.Hour)
					if signErr != nil {
						t.Fatal(signErr)
					}
					req.Header.Set("Authorization", "Bearer "+token)
				}

				for k, v := range c.Headers {
					req.Header.Set(k, v)
				}
				rec := httptest.NewRecorder()
				if useDDD {
					dddRouter.ServeHTTP(rec, req)
				} else {
					router.ServeHTTP(rec, req)
				}

				if rec.Code != c.WantStatus {
					t.Errorf("%s %s: got status %d, want %d", c.Method, c.Path, rec.Code, c.WantStatus)
				}
				if len(c.WantBodyRE) > 0 {
					if !regexp.MustCompile(c.WantBodyRE).Match(rec.Body.Bytes()) {
						t.Errorf("%s %s: body=%s does not match %s", c.Method, c.Path, rec.Body.String(), c.WantBodyRE)
					}
				} else if len(c.WantBody) > 0 && !jsonEquivalent(rec.Body.Bytes(), c.WantBody) {
					t.Errorf("%s %s: body=%s want=%s", c.Method, c.Path, rec.Body.String(), c.WantBody)
				}
			}
		})
	}
}

func jsonEquivalent(got, want []byte) bool {
	var g, w any
	return json.Unmarshal(got, &g) == nil && json.Unmarshal(want, &w) == nil && reflect.DeepEqual(g, w)
}

func mustGeneratePEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}
