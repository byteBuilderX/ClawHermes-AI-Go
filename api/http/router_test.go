package http

import (
	"strings"
	"testing"
)

// TestParameterPublishRollbackRoutesRequireHostTenant guards R29/O2: Publish and
// Rollback move the production label of a public platform parameter group, which
// affects every tenant. They must run under InjectTenantContext +
// RequireDefaultTenant (host = default tenant, fail-closed 403 otherwise), while
// PUT /parameters and CreateDraft stay on the base adminGroup (no host tenant).
// Route composition is guarded at the source level (same as the other router_*_rbac
// tests): constructing a full wiring.Container is not feasible here, and the
// middleware behaviour itself is covered by api/middleware tests.
func TestParameterPublishRollbackRoutesRequireHostTenant(t *testing.T) {
	source := readRouterSource(t)
	for _, line := range []string{
		`hostWrite := adminGroup.Group("", middleware.InjectTenantContext(), middleware.RequireDefaultTenant())`,
		`hostWrite.POST("/parameters/versions/:groupKey/:versionID/publish", paramHandler.Publish)`,
		`hostWrite.POST("/parameters/versions/:groupKey/:versionID/rollback", paramHandler.Rollback)`,
	} {
		if !strings.Contains(source, line) {
			t.Fatalf("publish/rollback route must require host tenant middleware: %s", line)
		}
	}
	// 无宿主租户需求的写接口必须保留在 base adminGroup（不随 Publish/Rollback 挪动）。
	for _, line := range []string{
		`adminGroup.PUT("/parameters", paramHandler.Update)`,
		`adminGroup.POST("/parameters/versions/:groupKey", paramHandler.CreateDraft)`,
	} {
		if !strings.Contains(source, line) {
			t.Fatalf("plain registry write must stay on admin group: %s", line)
		}
	}
	// 防止历史形态把 Publish/Rollback 直接注册在 adminGroup 上（无宿主租户门）。
	if strings.Contains(source, `adminGroup.POST("/parameters/versions/:groupKey/:versionID/publish", paramHandler.Publish)`) {
		t.Fatal("publish route must not be registered directly on the base admin group")
	}
}
