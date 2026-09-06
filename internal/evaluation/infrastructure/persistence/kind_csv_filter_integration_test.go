//go:build integration

package persistence

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 被测收敛后评测中心默认并列 agent/knowledge 两轨：center 读路径 kind 谓词支持
// 逗号分隔 CSV（agent,knowledge 聚合），单值 skill 仍可只读打开历史。
func TestPgCenterQueryRepositoryKindCSVFilter(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; center query integration test requires real PostgreSQL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	tenant := "csvfilter"
	if err := postgres.ProvisionTenantSchema(ctx, pool, tenant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS "tenant_csvfilter" CASCADE`)
		pool.Close()
	})
	seedKindCSVFilter(t, ctx, pool)

	repo := NewPgCenterQueryRepository(pool)

	// ListResources：agent,knowledge 聚合命中两轨，skill/mcp 历史不混入。
	both, err := repo.ListResources(ctx, tenant, port.CenterFilter{ResourceKind: "agent,knowledge", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(both.Items) != 2 {
		t.Fatalf("csv agent,knowledge resources=%d items", len(both.Items))
	}
	for _, item := range both.Items {
		if item.ResourceKind != "agent" && item.ResourceKind != "knowledge" {
			t.Fatalf("csv filter leaked kind=%s item=%+v", item.ResourceKind, item)
		}
	}
	// 单值 skill 只读语义保留。
	onlySkill, err := repo.ListResources(ctx, tenant, port.CenterFilter{ResourceKind: "skill", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlySkill.Items) != 1 || onlySkill.Items[0].ResourceID != "skill-legacy" {
		t.Fatalf("single-value skill resources=%+v", onlySkill.Items)
	}
	// 空 = 全部（含历史 skill/mcp）。
	all, err := repo.ListResources(ctx, tenant, port.CenterFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 4 {
		t.Fatalf("empty filter should return all kinds: %d", len(all.Items))
	}

	// ListRuns：两轨聚合 / 单值只读。
	runsBoth, err := repo.ListRuns(ctx, tenant, port.CenterFilter{ResourceKind: "agent,knowledge", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(runsBoth.Items) != 2 {
		t.Fatalf("csv runs=%+v", runsBoth.Items)
	}
	runsSkill, err := repo.ListRuns(ctx, tenant, port.CenterFilter{ResourceKind: "skill", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(runsSkill.Items) != 1 || runsSkill.Items[0].ResourceID != "skill-legacy" {
		t.Fatalf("single-value skill runs=%+v", runsSkill.Items)
	}

	// ListSuites：kind 取自 active/draft revision。
	suitesBoth, err := repo.ListSuites(ctx, tenant, port.CenterFilter{ResourceKind: "agent,knowledge", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(suitesBoth.Items) != 2 {
		t.Fatalf("csv suites=%+v", suitesBoth.Items)
	}
	suitesSkill, err := repo.ListSuites(ctx, tenant, port.CenterFilter{ResourceKind: "skill", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(suitesSkill.Items) != 1 || suitesSkill.Items[0].Name != "suite-skill" {
		t.Fatalf("single-value skill suites=%+v", suitesSkill.Items)
	}

	// ListCandidates：kind 取自 optimization_jobs。
	candsBoth, err := repo.ListCandidates(ctx, tenant, port.CenterFilter{ResourceKind: "agent,knowledge", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(candsBoth.Items) != 1 || candsBoth.Items[0].Source != "rewrite-agent" {
		t.Fatalf("csv candidates=%+v", candsBoth.Items)
	}
	candsSkill, err := repo.ListCandidates(ctx, tenant, port.CenterFilter{ResourceKind: "skill", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(candsSkill.Items) != 1 || candsSkill.Items[0].Source != "rewrite-skill" {
		t.Fatalf("single-value skill candidates=%+v", candsSkill.Items)
	}

	// ListExperiments：kind 自身列。
	expsBoth, err := repo.ListExperiments(ctx, tenant, port.CenterFilter{ResourceKind: "agent,knowledge", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(expsBoth.Items) != 2 {
		t.Fatalf("csv experiments=%+v", expsBoth.Items)
	}
	expsSkill, err := repo.ListExperiments(ctx, tenant, port.CenterFilter{ResourceKind: "skill", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(expsSkill.Items) != 1 || expsSkill.Items[0].ResourceID != "skill-legacy" {
		t.Fatalf("single-value skill experiments=%+v", expsSkill.Items)
	}
}

func seedKindCSVFilter(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	schema := "tenant_csvfilter"
	now := time.Now().UTC().Truncate(time.Microsecond)
	statements := []struct {
		sql  string
		args []any
	}{
		// resource_revisions：每类被测/历史 kind 一个资源（agent 两版供 canary）。
		{`INSERT INTO ` + schema + `.resource_revisions(id,resource_kind,resource_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,created_at)
			VALUES('rev-agent1','agent','agent-a','manual','published','h','p','r','{"label":"agent-a"}',$1),
			      ('rev-agent2','agent','agent-a','manual','published','h','p','r','{"label":"agent-a"}',$2),
			      ('rev-kb1','knowledge','kb-1','manual','published','h','p','r','{"label":"kb-1"}',$1),
			      ('rev-skill1','skill','skill-legacy','manual','published','h','p','r','{"label":"skill-legacy"}',$1),
			      ('rev-mcp1','mcp','mcp-legacy','manual','published','h','p','r','{"label":"mcp-legacy"}',$1)`,
			[]any{now.Add(-time.Minute), now}},
		// eval_suites + revisions：suite 的 kind 由 active revision 承载。
		{`INSERT INTO ` + schema + `.eval_suites(id,name,description,active_revision_id,created_at)
			VALUES('suite-agent','suite-agent','safe','sr-agent',$1),('suite-kb','suite-kb','safe','sr-kb',$1),('suite-skill','suite-skill','safe','sr-skill',$1)`,
			[]any{now.Add(-time.Minute)}},
		{`INSERT INTO ` + schema + `.eval_suite_revisions(id,suite_id,version_no,status,resource_kind,created_at)
			VALUES('sr-agent','suite-agent',1,'published','agent',$1),('sr-kb','suite-kb',1,'published','knowledge',$1),('sr-skill','suite-skill',1,'published','skill',$1)`,
			[]any{now.Add(-time.Minute)}},
		// eval_runs：两轨 + skill/mcp 历史各一条。
		{`INSERT INTO ` + schema + `.eval_runs(id,resource_kind,resource_id,revision_id,suite_revision_id,status,passed,total_cases,passed_cases,metrics,created_at)
			VALUES('run-agent','agent','agent-a','rev-agent1','sr-agent','succeeded',true,1,1,'{}',$1),
			      ('run-kb','knowledge','kb-1','rev-kb1','sr-kb','succeeded',true,1,1,'{}',$2),
			      ('run-skill','skill','skill-legacy','rev-skill1','sr-skill','succeeded',true,1,1,'{}',$2),
			      ('run-mcp','mcp','mcp-legacy','rev-mcp1','sr-skill','succeeded',false,1,0,'{}',$2)`,
			[]any{now.Add(-time.Second), now}},
		// optimization_jobs+candidates：agent 与 skill 各一条。
		{`INSERT INTO ` + schema + `.optimization_jobs(id,resource_kind,resource_id,baseline_revision_id,suite_revision_id,status,created_at)
			VALUES('job-agent','agent','agent-a','rev-agent1','sr-agent','succeeded',$1),
			      ('job-skill','skill','skill-legacy','rev-skill1','sr-skill','succeeded',$1)`,
			[]any{now.Add(-time.Minute)}},
		{`INSERT INTO ` + schema + `.optimization_candidates(id,optimization_job_id,revision_id,parent_revision_id,source,rationale,created_at)
			VALUES('cand-agent','job-agent','rev-agent2','rev-agent1','rewrite-agent','safe',$1),
			      ('cand-skill','job-skill','rev-skill1','rev-skill1','rewrite-skill','safe',$1)`,
			[]any{now.Add(-time.Second)}},
		// evaluation_experiments：agent/knowledge/skill 各一。
		{`INSERT INTO ` + schema + `.evaluation_experiments(id,resource_kind,resource_id,stable_revision_id,canary_revision_id,suite_revision_id,status,decision_snapshot,created_at)
			VALUES('exp-agent','agent','agent-a','rev-agent1','rev-agent2','sr-agent','completed','{}',$1),
			      ('exp-kb','knowledge','kb-1','rev-kb1','rev-kb1','sr-kb','completed','{}',$2),
			      ('exp-skill','skill','skill-legacy','rev-skill1','rev-skill1','sr-skill','completed','{}',$2)`,
			[]any{now.Add(-time.Second), now}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}
