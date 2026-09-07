//go:build integration

package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPgCenterQueryRepositoryEvidenceTimelinePaginationAndIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; center query integration test requires real PostgreSQL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	tenants := []string{"center_query_one", "center_query_two"}
	for _, tenant := range tenants {
		if err := postgres.ProvisionTenantSchema(ctx, pool, tenant); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, tenant := range tenants {
			_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS "tenant_%s" CASCADE`, tenant))
		}
		pool.Close()
	})

	seedCenterQuery(t, ctx, pool, "tenant_center_query_one", "one")
	seedCenterQuery(t, ctx, pool, "tenant_center_query_two", "two")
	repo := NewPgCenterQueryRepository(pool)
	overview, err := repo.Overview(ctx, tenants[0])
	if err != nil {
		t.Fatal(err)
	}
	if overview.Resources != 3 || overview.Suites != 2 || overview.Runs != 2 || overview.Candidates != 2 || overview.Experiments != 2 {
		t.Fatalf("overview=%+v", overview)
	}

	first, err := repo.ListResources(ctx, tenants[0], port.CenterFilter{ResourceKind: "skill", Status: "published", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].SafeSummary["label"] != "one-new" ||
		first.Items[0].StableRevisionID != "rev-new" || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	second, err := repo.ListResources(ctx, tenants[0], port.CenterFilter{ResourceKind: "skill", Status: "published", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID ||
		!second.Items[0].CreatedAt.Before(first.Items[0].CreatedAt) {
		t.Fatalf("second page=%+v", second)
	}
	filtered, err := repo.ListResources(ctx, tenants[0], port.CenterFilter{
		ResourceKind: "skill", ResourceID: "other", Status: "published", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 0 {
		t.Fatalf("newer draft resource matched published filter: %+v", filtered.Items)
	}
	other, err := repo.ListResources(ctx, tenants[1], port.CenterFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	foundOtherTenant := false
	for _, item := range other.Items {
		foundOtherTenant = foundOtherTenant || item.SafeSummary["label"] == "two-new"
	}
	if !foundOtherTenant {
		t.Fatalf("tenant isolation=%+v", other)
	}
	assertCenterLists(t, ctx, repo, tenants[0], "one")
	assertCenterLists(t, ctx, repo, tenants[1], "two")

	// (b) revision_id 过滤：同资源 runs 精确取锚定版本行；空值放行全量（新增 $7 谓词
	// 不改变既有 ListRuns 行为）。Batch 6 资源详情回归视图据此服务端过滤。
	revNew, err := repo.ListRuns(ctx, tenants[0], port.CenterFilter{ResourceKind: "skill", ResourceID: "shared", RevisionID: "rev-new", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(revNew.Items) != 1 || revNew.Items[0].ID != "collision" {
		t.Fatalf("revision=rev-new runs=%+v, want exactly run collision", revNew.Items)
	}
	revOld, err := repo.ListRuns(ctx, tenants[0], port.CenterFilter{ResourceKind: "skill", ResourceID: "shared", RevisionID: "rev-old", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(revOld.Items) != 1 || revOld.Items[0].ID != "run-old" {
		t.Fatalf("revision=rev-old runs=%+v, want exactly run run-old", revOld.Items)
	}
	missing, err := repo.ListRuns(ctx, tenants[0], port.CenterFilter{ResourceKind: "skill", ResourceID: "shared", RevisionID: "no-such-revision", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Items) != 0 {
		t.Fatalf("unknown revision matched runs: %+v", missing.Items)
	}
	all, err := repo.ListRuns(ctx, tenants[0], port.CenterFilter{ResourceKind: "skill", ResourceID: "shared", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 2 {
		t.Fatalf("unfiltered runs=%+v, want both seeded runs", all.Items)
	}
	for _, index := range []string{"idx_optimization_jobs_center_query", "idx_optimization_candidates_job_created"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=$1 AND indexname=$2)`,
			"tenant_center_query_one", index).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s exists=%v err=%v", index, exists, err)
		}
	}

	timeline, err := repo.Timeline(ctx, tenants[0], port.CenterFilter{ResourceKind: "skill", ResourceID: "shared", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, event := range timeline.Items {
		kinds[event.Kind] = true
	}
	for _, kind := range []string{"revision", "run", "candidate", "experiment", "decision"} {
		if !kinds[kind] {
			t.Errorf("timeline missing %s: %+v", kind, timeline.Items)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, pageErr := repo.Timeline(ctx, tenants[0], port.CenterFilter{
			ResourceKind: "skill", ResourceID: "shared", Cursor: cursor, Limit: 1,
		})
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		for _, event := range page.Items {
			key := event.Kind + ":" + event.ID
			if seen[key] {
				t.Fatalf("timeline duplicate across pages: %s", key)
			}
			seen[key] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !seen["run:collision"] || !seen["decision:collision"] {
		t.Fatalf("same timestamp/id events skipped across pages: %+v", seen)
	}
	assertSafeCenterJSON(t, struct {
		Resources any `json:"resources"`
		Timeline  any `json:"timeline"`
	}{first.Items, timeline.Items})
}

func assertSafeCenterJSON(t *testing.T, value any) {
	t.Helper()
	b, _ := json.Marshal(value)
	serialized := strings.ToLower(string(b))
	for _, forbidden := range []string{"object://secret", "payload_ref", "payload_hash", "content_hash", "actual_output",
		"feedback", "decision_snapshot", "metrics", "credentials", "system_prompt", "retrieved_chunks"} {
		if strings.Contains(serialized, forbidden) {
			t.Errorf("safe response contains %q: %s", forbidden, serialized)
		}
	}
}

func assertCenterLists(t *testing.T, ctx context.Context, repo *PgCenterQueryRepository, tenantID, label string) {
	t.Helper()
	suites, err := repo.ListSuites(ctx, tenantID, port.CenterFilter{ResourceKind: "skill", Status: "published", Limit: 1})
	if err != nil || len(suites.Items) != 1 || suites.NextCursor == "" || suites.Items[0].Name != "suite-"+label+"-new" {
		t.Fatalf("suite first page=%+v err=%v", suites, err)
	}
	filteredSuites, err := repo.ListSuites(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "shared", Status: "published", Limit: 10,
	})
	if err != nil || len(filteredSuites.Items) != 1 || filteredSuites.Items[0].ID != "suite" {
		t.Fatalf("suite resource filter=%+v err=%v", filteredSuites, err)
	}
	suitesNext, err := repo.ListSuites(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", Status: "published", Limit: 1, Cursor: suites.NextCursor,
	})
	if err != nil || len(suitesNext.Items) != 1 || suitesNext.Items[0].ID == suites.Items[0].ID ||
		!suitesNext.Items[0].CreatedAt.Before(suites.Items[0].CreatedAt) {
		t.Fatalf("suite second page=%+v err=%v", suitesNext, err)
	}

	runs, err := repo.ListRuns(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "shared", Status: "succeeded", Limit: 1,
	})
	if err != nil || len(runs.Items) != 1 || runs.NextCursor == "" || runs.Items[0].Passed != (label == "one") {
		t.Fatalf("run first page=%+v err=%v", runs, err)
	}
	runsNext, err := repo.ListRuns(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "shared", Status: "succeeded", Limit: 1, Cursor: runs.NextCursor,
	})
	if err != nil || len(runsNext.Items) != 1 || runsNext.Items[0].ID == runs.Items[0].ID ||
		!runsNext.Items[0].CreatedAt.Before(runs.Items[0].CreatedAt) {
		t.Fatalf("run second page=%+v err=%v", runsNext, err)
	}
	evidenceExperiments, err := repo.ListExperiments(ctx, tenantID, port.CenterFilter{ResourceKind: "skill", Limit: 20})
	if err != nil || len(evidenceExperiments.Items) == 0 {
		t.Fatalf("experiments=%+v err=%v", evidenceExperiments, err)
	}
	for _, experiment := range evidenceExperiments.Items {
		if experiment.ResourceID == "shared" && experiment.PromotionEvidence.Gates.Quality == "" {
			t.Fatalf("experiment promotion evidence missing: %+v", experiment)
		}
	}

	candidates, err := repo.ListCandidates(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "shared", Status: "succeeded", Limit: 1,
	})
	if err != nil || len(candidates.Items) != 1 || candidates.NextCursor == "" ||
		candidates.Items[0].Source != "rewrite-"+label ||
		!reflect.DeepEqual(candidates.Items[0].SafeDiff.ChangedFields, []string{"label"}) ||
		candidates.Items[0].SafeDiff.Changes["label"].Before != label+"-old" ||
		candidates.Items[0].SafeDiff.Changes["label"].After != label+"-new" {
		t.Fatalf("candidate first page=%+v err=%v", candidates, err)
	}
	candidatesNext, err := repo.ListCandidates(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "shared", Status: "succeeded", Limit: 1, Cursor: candidates.NextCursor,
	})
	if err != nil || len(candidatesNext.Items) != 1 || candidatesNext.Items[0].ID == candidates.Items[0].ID ||
		!candidatesNext.Items[0].CreatedAt.Before(candidates.Items[0].CreatedAt) {
		t.Fatalf("candidate second page=%+v err=%v", candidatesNext, err)
	}

	experiments, err := repo.ListExperiments(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "shared", Status: "completed", Limit: 1,
	})
	if err != nil || len(experiments.Items) != 1 || experiments.NextCursor == "" ||
		experiments.Items[0].Recommendation != "tenant-"+label {
		t.Fatalf("experiment first page=%+v err=%v", experiments, err)
	}
	experimentsNext, err := repo.ListExperiments(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "shared", Status: "completed", Limit: 1, Cursor: experiments.NextCursor,
	})
	if err != nil || len(experimentsNext.Items) != 1 || experimentsNext.Items[0].ID == experiments.Items[0].ID ||
		!experimentsNext.Items[0].CreatedAt.Before(experiments.Items[0].CreatedAt) {
		t.Fatalf("experiment second page=%+v err=%v", experimentsNext, err)
	}
	assertSafeCenterJSON(t, struct {
		Suites      any `json:"suites"`
		Runs        any `json:"runs"`
		Candidates  any `json:"candidates"`
		Experiments any `json:"experiments"`
	}{suites.Items, runs.Items, candidates.Items, experiments.Items})
}

func seedCenterQuery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema, label string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ` + schema + `.eval_suites(id,name,description,created_at) VALUES('suite','suite-` + label + `','safe',$1)`, []any{now}},
		{`INSERT INTO ` + schema + `.eval_suite_revisions(id,suite_id,version_no,status,resource_kind,created_at) VALUES('suite-rev','suite',1,'published','skill',$1)`, []any{now}},
		{`UPDATE ` + schema + `.eval_suites SET active_revision_id='suite-rev' WHERE id='suite'`, nil},
		{`INSERT INTO ` + schema + `.eval_suites(id,name,description,active_revision_id,created_at) VALUES('suite-new','suite-` + label + `-new','safe','suite-rev-new',$1)`, []any{now.Add(time.Second)}},
		{`INSERT INTO ` + schema + `.eval_suite_revisions(id,suite_id,version_no,status,resource_kind,created_at) VALUES('suite-rev-new','suite-new',1,'published','skill',$1)`, []any{now.Add(time.Second)}},
		{`INSERT INTO ` + schema + `.resource_revisions(id,resource_kind,resource_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,created_at) VALUES('rev-old','skill','shared','manual','published','secret-content','secret-hash','object://secret','{"label":"` + label + `-old"}',$1),('rev-new','skill','shared','manual','published','secret-content','secret-hash','object://secret','{"label":"` + label + `-new","auth":{"credentials":"secret"},"system_prompt":"raw","retrieved_chunks":["raw"]}',$2)`, []any{now.Add(-time.Minute), now}},
		{`INSERT INTO ` + schema + `.resource_revisions(id,resource_kind,resource_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,created_at) VALUES('rev-other-old','skill','other','manual','published','secret-content','secret-hash','object://secret','{"label":"` + label + `-other-old"}',$1),('rev-other-draft','skill','other','manual','draft','secret-content','secret-hash','object://secret','{"label":"` + label + `-other-draft"}',$2)`, []any{now.Add(-2 * time.Minute), now.Add(time.Second)}},
		{`INSERT INTO ` + schema + `.resource_revisions(id,resource_kind,resource_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,created_at) VALUES('rev-third','skill','third','manual','published','secret-content','secret-hash','object://secret','{"label":"` + label + `-third"}',$1)`, []any{now.Add(-3 * time.Minute)}},
		{`INSERT INTO ` + schema + `.eval_runs(id,resource_kind,resource_id,revision_id,suite_revision_id,status,passed,total_cases,passed_cases,metrics,created_at) VALUES('collision','skill','shared','rev-new','suite-rev','succeeded',$1,1,1,'{"secret":"metric"}',$2)`, []any{label == "one", now.Add(5 * time.Second)}},
		{`INSERT INTO ` + schema + `.eval_runs(id,resource_kind,resource_id,revision_id,suite_revision_id,status,passed,total_cases,passed_cases,metrics,created_at) VALUES('run-old','skill','shared','rev-old','suite-rev','succeeded',true,1,1,'{}',$1)`, []any{now.Add(time.Second)}},
		{`INSERT INTO ` + schema + `.optimization_jobs(id,resource_kind,resource_id,baseline_revision_id,suite_revision_id,status,created_at) VALUES('job','skill','shared','rev-old','suite-rev','succeeded',$1)`, []any{now.Add(2 * time.Second)}},
		{`INSERT INTO ` + schema + `.optimization_candidates(id,optimization_job_id,revision_id,parent_revision_id,source,rationale,created_at) VALUES('candidate','job','rev-new','rev-old',$1,'safe rationale',$2)`, []any{"rewrite-" + label, now.Add(3 * time.Second)}},
		{`INSERT INTO ` + schema + `.optimization_jobs(id,resource_kind,resource_id,baseline_revision_id,suite_revision_id,status,created_at) VALUES('job-old','skill','shared','rev-old','suite-rev','succeeded',$1)`, []any{now}},
		{`INSERT INTO ` + schema + `.optimization_candidates(id,optimization_job_id,revision_id,parent_revision_id,source,rationale,created_at) VALUES('candidate-old','job-old','rev-old','rev-old','rewrite','safe rationale',$1)`, []any{now}},
		{`INSERT INTO ` + schema + `.evaluation_experiments(id,resource_kind,resource_id,stable_revision_id,canary_revision_id,suite_revision_id,status,recommendation,decision_snapshot,created_at) VALUES('experiment','skill','shared','rev-old','rev-new','suite-rev','completed',$1,'{"secret":"body"}',$2)`, []any{"tenant-" + label, now.Add(4 * time.Second)}},
		{`INSERT INTO ` + schema + `.evaluation_experiments(id,resource_kind,resource_id,stable_revision_id,canary_revision_id,suite_revision_id,status,decision_snapshot,created_at) VALUES('experiment-old','skill','shared','rev-old','rev-new','suite-rev','completed','{}',$1)`, []any{now}},
		{`INSERT INTO ` + schema + `.experiment_decisions(id,experiment_id,action,actor_type,prior_status,new_status,metrics,reason,idempotency_key,created_at) VALUES('collision','experiment','promote','system','active','completed','{"secret":"body"}','safe reason','key',$1)`, []any{now.Add(5 * time.Second)}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}
