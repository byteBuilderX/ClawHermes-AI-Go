package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v2"
)

func TestPlatformRepositoryGetValue(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	repo := &PlatformRepository{pool: mock}

	t.Run("found", func(t *testing.T) {
		mock.ExpectQuery(`SELECT value FROM public\.platform_settings WHERE key = \$1`).
			WithArgs("evaluation.optimizer.temperature").
			WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow([]byte(`0.5`)))

		raw, present, err := repo.GetValue(context.Background(), "evaluation.optimizer.temperature")
		if err != nil {
			t.Fatal(err)
		}
		if !present || string(raw) != `0.5` {
			t.Fatalf("got (%q, %v), want (0.5, true)", raw, present)
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		mock.ExpectQuery(`SELECT value FROM public\.platform_settings WHERE key = \$1`).
			WithArgs("missing.key").
			WillReturnError(pgx.ErrNoRows)

		raw, present, err := repo.GetValue(context.Background(), "missing.key")
		if err != nil || present || raw != nil {
			t.Fatalf("got (%q, %v, %v), want (nil, false, nil)", raw, present, err)
		}
	})
}

func TestPlatformRepositorySetValueUpsert(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	repo := &PlatformRepository{pool: mock}

	mock.ExpectQuery(`INSERT INTO public\.platform_settings`).
		WithArgs("trace.capture_parameters", `true`, "admin-1").
		WillReturnRows(pgxmock.NewRows([]string{"key"}).AddRow("trace.capture_parameters"))

	if err := repo.SetValue(context.Background(), "trace.capture_parameters", json.RawMessage(`true`), "admin-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformRepositoryGetAll(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	repo := &PlatformRepository{pool: mock}

	mock.ExpectQuery(`SELECT key, value, updated_by, updated_at FROM public\.platform_settings`).
		WillReturnRows(pgxmock.NewRows([]string{"key", "value", "updated_by", "updated_at"}).
			AddRow("a.key", []byte(`1`), "admin-1", time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)))

	values, err := repo.GetAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Key != "a.key" || string(values[0].Value) != `1` {
		t.Fatalf("unexpected rows: %+v", values)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestPlatformRepositoryListVersionsCreatedByName 锁定 ListVersions 对
// public.users 的 LEFT JOIN + COALESCE 现算：真实用户出可读名，system/未知
// uuid 无命中则回退 created_by 原文（display_name > github_login > 原文）。
func TestPlatformRepositoryListVersionsCreatedByName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	repo := &PlatformRepository{pool: mock}

	mock.ExpectQuery(
		`(?s)COALESCE\(u\.display_name, u\.github_login, v\.created_by\) AS created_by_name\s+FROM public\.platform_config_versions v\s+LEFT JOIN public\.users u ON u\.id::text = v\.created_by`).
		WithArgs("evaluation").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "group_key", "version_seq", "status", "eval_state", "snapshot",
			"base_version_id", "message", "created_by", "created_at", "is_current", "created_by_name",
		}).
			AddRow(int64(3), "evaluation", 3, "published", "unknown", []byte(`{"model":"qwen"}`), nil,
				"publish #3", "user-abc", time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), true, "Alice").
			AddRow(int64(1), "evaluation", 1, "archived", "unknown", []byte(`{"model":"qwen"}`), nil,
				"seed", "system", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), false, "system"))

	versions, err := repo.ListVersions(context.Background(), "evaluation")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2", len(versions))
	}
	if versions[0].CreatedBy != "user-abc" || versions[0].CreatedByName != "Alice" {
		t.Fatalf("real user row: got CreatedBy=%q CreatedByName=%q, want user-abc/Alice", versions[0].CreatedBy, versions[0].CreatedByName)
	}
	if versions[1].CreatedBy != "system" || versions[1].CreatedByName != "system" {
		t.Fatalf("system row: got CreatedBy=%q CreatedByName=%q, want system/system", versions[1].CreatedBy, versions[1].CreatedByName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
