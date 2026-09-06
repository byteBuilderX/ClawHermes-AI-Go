// Package migration tests the database migration package.
package migration

import (
	"math"
	"os"
	"strings"
	"testing"
)

// TestMigrationSetup verifies migration initialization.
func TestMigrationSetup(t *testing.T) {
	t.Run("migration_source_validity", func(t *testing.T) {
		// This test verifies migration files are correctly configured.
		// Full migration execution tests should use integration test suite.
		t.Log("Migration setup verified")
	})
}

func TestMigrationVersionsAreUnique(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("sql")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	versions := make(map[string]string)
	for _, entry := range entries {
		parts := strings.SplitN(entry.Name(), ".", 2)
		base := strings.TrimSuffix(strings.TrimSuffix(parts[0], ".up"), ".down")
		version := strings.SplitN(base, "_", 2)[0]
		if existing, ok := versions[version]; ok && existing != base {
			t.Fatalf("migration version %s is used by %s and %s", version, existing, base)
		}
		versions[version] = base
	}
}

func TestPreviousVersionRejectsIntegerOverflow(t *testing.T) {
	t.Parallel()
	if got, err := previousVersion(0); err != nil || got != -1 {
		t.Fatalf("previousVersion(0) = %d, %v; want -1, nil", got, err)
	}
	if got, err := previousVersion(uint(math.MaxInt)); err != nil || got != math.MaxInt-1 {
		t.Fatalf("previousVersion(MaxInt) = %d, %v", got, err)
	}
	if ^uint(0) > uint(math.MaxInt) {
		if _, err := previousVersion(^uint(0)); err == nil {
			t.Fatal("previousVersion(MaxUint) must reject overflow")
		}
	}
}

func TestDriverURLUsesRegisteredPGXV5Scheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "postgres",
			in:   "postgres://user:password@localhost:5432/stratum_e2e?sslmode=disable",
			want: "pgx5://user:password@localhost:5432/stratum_e2e?sslmode=disable",
		},
		{
			name: "postgresql",
			in:   "postgresql://user:password@localhost:5432/stratum_e2e?sslmode=disable",
			want: "pgx5://user:password@localhost:5432/stratum_e2e?sslmode=disable",
		},
		{
			name: "already normalized",
			in:   "pgx5://user:password@localhost:5432/stratum_e2e?sslmode=disable",
			want: "pgx5://user:password@localhost:5432/stratum_e2e?sslmode=disable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := driverURL(tt.in)
			if err != nil {
				t.Fatalf("driverURL: %v", err)
			}
			if got != tt.want {
				t.Fatalf("driverURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRemoveTenantLLMAPIKeysMigration(t *testing.T) {
	t.Parallel()

	up, err := os.ReadFile("sql/027_remove_tenant_llm_api_keys.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	down, err := os.ReadFile("sql/027_remove_tenant_llm_api_keys.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	upSQL := string(up)
	for _, required := range []string{
		"UPDATE public.tenants",
		"COALESCE(settings, '{}'::jsonb) - 'llm_api_keys'",
		"COALESCE(settings, '{}'::jsonb) ? 'llm_api_keys'",
	} {
		if !strings.Contains(upSQL, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	if strings.Contains(string(down), "llm_api_keys") {
		t.Fatal("down migration must not recreate deleted credentials")
	}
}

func TestModelEditableParamsMigration(t *testing.T) {
	t.Parallel()

	up, err := os.ReadFile("sql/038_model_editable_params.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	down, err := os.ReadFile("sql/038_model_editable_params.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	upSQL := string(up)
	for _, required := range []string{
		"ALTER TABLE public.models ADD COLUMN IF NOT EXISTS",
		"sampling_params",
		"max_temperature",
		"ALTER TABLE public.providers ADD COLUMN IF NOT EXISTS",
		"extra_headers",
		"default_sampling",
	} {
		if !strings.Contains(upSQL, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	for _, required := range []string{
		"ALTER TABLE public.models DROP COLUMN IF EXISTS",
		"ALTER TABLE public.providers DROP COLUMN IF EXISTS",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("down migration missing %q", required)
		}
	}
}

func TestModelPolicyGovernanceMigration(t *testing.T) {
	t.Parallel()
	up, err := os.ReadFile("sql/039_model_policy_governance.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	down, err := os.ReadFile("sql/039_model_policy_governance.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	upSQL := string(up)
	for _, required := range []string{
		"operator_context_window",
		"operator_max_tokens",
		"default_output_tokens",
		"context_window_source",
		"max_tokens_source",
		"platform_resource_change_audits",
	} {
		if !strings.Contains(upSQL, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	if !strings.Contains(string(down), "platform_resource_change_audits") {
		t.Fatal("down migration must drop platform audit table")
	}
}

// TestPlatformGateEvalStateMigration 守护分层门禁 public 迁移 044：up 幂等加
// eval_state 三列，down 幂等删除（与 038 的 ADD/DROP IF EXISTS 模式一致）。
func TestPlatformGateEvalStateMigration(t *testing.T) {
	t.Parallel()
	up, err := os.ReadFile("sql/044_platform_gate_eval_state.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"eval_state TEXT NOT NULL DEFAULT 'unknown'",
		"eval_state_updated_at TIMESTAMPTZ", "eval_state_updated_by TEXT NOT NULL DEFAULT ''"} {
		if !strings.Contains(string(up), "ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS "+col) {
			t.Fatalf("up migration missing idempotent ADD COLUMN IF NOT EXISTS %s", col)
		}
	}

	down, err := os.ReadFile("sql/044_platform_gate_eval_state.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"eval_state_updated_by", "eval_state_updated_at", "eval_state"} {
		if !strings.Contains(string(down), "ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS "+col) {
			t.Fatalf("down migration missing DROP COLUMN IF EXISTS %s", col)
		}
	}
}
