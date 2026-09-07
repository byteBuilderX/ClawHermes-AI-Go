//go:build integration

package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPgCenterQueryRepositoryRevisionLedger 真实链路验证里程碑 7 版本引用账本三个端点：
// (0) 单被测资源 eval 版本表分页 + 资源不存在 404；(c) 单版本引用方账本（deployment
// 角色投影 + subject/pinned runs + candidate/baseline + experiment 臂）与引用不存在 404；
// (d) 单版本通过率摘要（仅 succeeded 聚合、0 成功即 null、recent_runs 含非成功）。
func TestPgCenterQueryRepositoryRevisionLedger(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; revision ledger integration test requires real PostgreSQL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "center_query_rev"
	if err := postgres.ProvisionTenantSchema(ctx, pool, tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS "tenant_%s" CASCADE`, tenantID))
		pool.Close()
	})
	schema := "tenant_" + tenantID
	seedRevisionLedger(t, ctx, pool, schema)

	repo := NewPgCenterQueryRepository(pool)
	// repo 契约：filter 已由 service 归一（limit 默认 20），与兄弟 list 集成测试一致显式传值。
	skillOne := port.CenterFilter{ResourceKind: "skill", ResourceID: "resource-1", Limit: 20}
	revOne := domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "resource-1", RevisionID: "rev-1"}
	revTwo := domain.ResourceRef{Kind: domain.ResourceKindSkill, ResourceID: "resource-1", RevisionID: "rev-2"}

	// (0) 版本表：默认全量倒序 [rev-2, rev-1]，published 过滤只回 rev-1；跨资源隔离；
	// 分页（limit 1 两页）；无版本资源 → ErrCenterResourceNotFound。
	page, err := repo.ListRevisions(ctx, tenantID, skillOne)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || len(page.Items) != 2 || page.NextCursor != "" {
		t.Fatalf("list revisions all=%+v", page)
	}
	if page.Items[0].ID != "rev-2" || page.Items[0].ParentRevisionID != "rev-1" ||
		page.Items[0].Source != "optimization" || page.Items[0].Status != "draft" ||
		page.Items[0].SafeSummary["version_label"] != "v2" {
		t.Fatalf("first revision row=%+v", page.Items[0])
	}
	if page.Items[1].ID != "rev-1" || page.Items[1].Status != "published" ||
		page.Items[1].CreatedBy != "user-1" || page.Items[1].SafeSummary["version_label"] != "v1" {
		t.Fatalf("second revision row=%+v", page.Items[1])
	}
	published, err := repo.ListRevisions(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "resource-1", Status: "published", Limit: 20,
	})
	if err != nil || len(published.Items) != 1 || published.Items[0].ID != "rev-1" {
		t.Fatalf("published revisions=%+v err=%v", published, err)
	}
	if _, err := repo.ListRevisions(ctx, tenantID, port.CenterFilter{
		ResourceKind: "skill", ResourceID: "resource-unknown", Limit: 20,
	}); !errors.Is(err, port.ErrCenterResourceNotFound) {
		t.Fatalf("unknown resource list revisions error=%v", err)
	}
	var seen []string
	cursor := ""
	for {
		batch, batchErr := repo.ListRevisions(ctx, tenantID, port.CenterFilter{
			ResourceKind: "skill", ResourceID: "resource-1", Limit: 1, Cursor: cursor,
		})
		if batchErr != nil {
			t.Fatal(batchErr)
		}
		if batch.Items == nil {
			t.Fatalf("paginated items nil: %+v", batch)
		}
		for _, item := range batch.Items {
			seen = append(seen, item.ID)
		}
		if batch.NextCursor == "" {
			break
		}
		cursor = batch.NextCursor
	}
	if len(seen) != 2 || seen[0] != "rev-2" || seen[1] != "rev-1" {
		t.Fatalf("paginated order=%v", seen)
	}

	// (c) rev-1 账本：deployment=stable 投影；subject 3 run 倒序；pinned run 命中
	// context_snapshot.PinnedAssignments（agent run）；candidates 既有 baseline(parent)
	// 又有 candidate(revision)；experiment=canary 臂。
	refs, err := repo.RevisionReferences(ctx, tenantID, revOne)
	if err != nil {
		t.Fatal(err)
	}
	if refs.Deployment == nil || refs.Deployment.Role != "stable" || refs.Deployment.StableRevisionID != "rev-1" ||
		refs.Deployment.CanaryRevisionID != "" || refs.Deployment.CanaryPercent != 0 {
		t.Fatalf("deployment projection=%+v", refs.Deployment)
	}
	if len(refs.SubjectRuns) != 3 || refs.SubjectRuns[0].ID != "run-c" || refs.SubjectRuns[2].ID != "run-a" ||
		refs.SubjectRuns[2].Status != "succeeded" {
		t.Fatalf("subject runs=%+v", refs.SubjectRuns)
	}
	if len(refs.PinnedRuns) != 1 || refs.PinnedRuns[0].RunID != "run-pin" ||
		refs.PinnedRuns[0].ResourceKind != domain.ResourceKindAgent || refs.PinnedRuns[0].ResourceID != "agent-9" {
		t.Fatalf("pinned runs=%+v", refs.PinnedRuns)
	}
	if len(refs.Candidates) != 2 {
		t.Fatalf("candidates=%+v, want baseline cand-child + candidate cand-self", refs.Candidates)
	}
	roles := map[string]string{}
	for _, cand := range refs.Candidates {
		roles[cand.ID] = cand.Role
	}
	if roles["cand-child"] != "baseline" || roles["cand-self"] != "candidate" {
		t.Fatalf("candidate roles=%v", roles)
	}
	if len(refs.Experiments) != 1 || refs.Experiments[0].ID != "exp-canary" ||
		refs.Experiments[0].Role != "canary" || refs.Experiments[0].CanaryRevisionID != "rev-1" ||
		refs.Experiments[0].Status != "running" || refs.Experiments[0].StagePercent != 10 {
		t.Fatalf("experiments=%+v", refs.Experiments)
	}

	// (c) rev-2 非线上臂：deployment nil（诚实空态），subject 无 run → 空数组非 nil。
	revTwoRefs, err := repo.RevisionReferences(ctx, tenantID, revTwo)
	if err != nil {
		t.Fatal(err)
	}
	if revTwoRefs.Deployment != nil || revTwoRefs.SubjectRuns == nil || len(revTwoRefs.SubjectRuns) != 0 {
		t.Fatalf("rev-2 references=%+v, want nil deployment + empty subject runs", revTwoRefs)
	}
	if _, err := repo.RevisionReferences(ctx, tenantID, domain.ResourceRef{
		Kind: domain.ResourceKindSkill, ResourceID: "resource-1", RevisionID: "no-such-rev",
	}); !errors.Is(err, port.ErrCenterResourceNotFound) {
		t.Fatalf("unknown revision references error=%v", err)
	}

	// (d) rev-1 通过率：3 run 全量、2 成功；用例聚合仅 succeeded（11+5 / 12+5）；
	// recent_runs 含失败 run-c 且倒序在前。
	rate, err := repo.RevisionPassRate(ctx, tenantID, revOne)
	if err != nil {
		t.Fatal(err)
	}
	if rate.TotalRuns != 3 || rate.SucceededRuns != 2 || rate.PassedCases != 16 || rate.TotalCases != 17 {
		t.Fatalf("pass rate counters=%+v", rate)
	}
	if rate.PassRate == nil || math.Abs(*rate.PassRate-16.0/17.0) > 1e-9 {
		t.Fatalf("pass rate=%v, want 16/17", rate.PassRate)
	}
	if len(rate.RecentRuns) != 3 || rate.RecentRuns[0].ID != "run-c" ||
		rate.RecentRuns[0].Status != "failed" || rate.RecentRuns[0].Passed {
		t.Fatalf("recent runs=%+v", rate.RecentRuns)
	}
	// (d) rev-2 零 run：pass_rate null（诚实空态），recent_runs 空数组。
	zero, err := repo.RevisionPassRate(ctx, tenantID, revTwo)
	if err != nil {
		t.Fatal(err)
	}
	if zero.SucceededRuns != 0 || zero.TotalRuns != 0 || zero.PassRate != nil || zero.RecentRuns == nil || len(zero.RecentRuns) != 0 {
		t.Fatalf("empty pass rate=%+v, want null rate + empty recent runs", zero)
	}
	if _, err := repo.RevisionPassRate(ctx, tenantID, domain.ResourceRef{
		Kind: domain.ResourceKindSkill, ResourceID: "resource-1", RevisionID: "no-such-rev",
	}); !errors.Is(err, port.ErrCenterResourceNotFound) {
		t.Fatalf("unknown revision pass rate error=%v", err)
	}
}

func seedRevisionLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Microsecond)
	add := func(d time.Duration) time.Time { return base.Add(d) }
	// 绑定 run 的快照 JSON：顶层 PascalCase，PinnedAssignments 嵌套小写 map；值含
	// 'rev-1' 使 SQL 文本收窄命中，Go decode 后值命中判定为 pinned。
	pinnedSnapshot := `{"SchemaVersion":1,"PinnedAssignments":{"skill_revisions":{"skill-1":"rev-1"}}}`
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ` + schema + `.eval_suites(id,name,description,created_at) VALUES('suite','suite-rev','safe',$1)`, []any{base}},
		{`INSERT INTO ` + schema + `.eval_suite_revisions(id,suite_id,version_no,status,resource_kind,created_at) VALUES('suite-rev','suite',1,'published','skill',$1)`, []any{base}},
		{`INSERT INTO ` + schema + `.resource_revisions(id,resource_kind,resource_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,created_by,created_at) VALUES('rev-1','skill','resource-1','manual','published','h','p','obj://r1','{"version_label":"v1","resource_name":"res-1"}','user-1',$1)`, []any{add(-2 * time.Hour)}},
		{`INSERT INTO ` + schema + `.resource_revisions(id,resource_kind,resource_id,parent_revision_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,created_at) VALUES('rev-2','skill','resource-1','rev-1','optimization','draft','h','p','obj://r2','{"version_label":"v2"}',$1)`, []any{add(-time.Hour)}},
		{`INSERT INTO ` + schema + `.resource_revisions(id,resource_kind,resource_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,created_at) VALUES('rev-other','skill','resource-other','manual','published','h','p','obj://ro','{}',$1)`, []any{add(-3 * time.Hour)}},
		{`INSERT INTO ` + schema + `.eval_runs(id,resource_kind,resource_id,revision_id,suite_revision_id,status,passed,total_cases,passed_cases,metrics,context_snapshot,created_by,created_at) VALUES('run-a','skill','resource-1','rev-1','suite-rev','succeeded',true,12,11,'{}','{}','user-1',$1)`, []any{add(-50 * time.Minute)}},
		{`INSERT INTO ` + schema + `.eval_runs(id,resource_kind,resource_id,revision_id,suite_revision_id,status,passed,total_cases,passed_cases,metrics,context_snapshot,created_by,created_at) VALUES('run-b','skill','resource-1','rev-1','suite-rev','succeeded',true,5,5,'{}','{}','user-1',$1)`, []any{add(-40 * time.Minute)}},
		{`INSERT INTO ` + schema + `.eval_runs(id,resource_kind,resource_id,revision_id,suite_revision_id,status,passed,total_cases,passed_cases,metrics,context_snapshot,created_by,created_at) VALUES('run-c','skill','resource-1','rev-1','suite-rev','failed',false,10,0,'{}','{}','user-1',$1)`, []any{add(-30 * time.Minute)}},
		{`INSERT INTO ` + schema + `.eval_runs(id,resource_kind,resource_id,revision_id,suite_revision_id,status,passed,total_cases,passed_cases,metrics,context_snapshot,created_by,created_at) VALUES('run-pin','agent','agent-9','agent-rev-x','suite-rev','succeeded',true,3,3,'{}',$1,'user-2',$2)`, []any{pinnedSnapshot, add(-20 * time.Minute)}},
		{`INSERT INTO ` + schema + `.evaluation_deployments(resource_kind,resource_id,stable_revision_id,canary_percent) VALUES('skill','resource-1','rev-1',0)`, nil},
		{`INSERT INTO ` + schema + `.optimization_jobs(id,resource_kind,resource_id,baseline_revision_id,suite_revision_id,status,created_at) VALUES('job','skill','resource-1','rev-1','suite-rev','succeeded',$1)`, []any{add(-time.Hour)}},
		{`INSERT INTO ` + schema + `.optimization_candidates(id,optimization_job_id,revision_id,parent_revision_id,source,status,rank,created_at) VALUES('cand-child','job','rev-2','rev-1','rewrite','proposed',1,$1)`, []any{add(-50 * time.Minute)}},
		{`INSERT INTO ` + schema + `.optimization_candidates(id,optimization_job_id,revision_id,parent_revision_id,source,status,rank,created_at) VALUES('cand-self','job','rev-1','rev-2','manual','proposed',2,$1)`, []any{add(-45 * time.Minute)}},
		{`INSERT INTO ` + schema + `.evaluation_experiments(id,resource_kind,resource_id,stable_revision_id,canary_revision_id,suite_revision_id,status,stage_percent,recommendation,created_at) VALUES('exp-canary','skill','resource-1','rev-2','rev-1','suite-rev','running',10,'hold',$1)`, []any{add(-time.Minute)}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed %s: %v", statement.sql[:60], err)
		}
	}
}
