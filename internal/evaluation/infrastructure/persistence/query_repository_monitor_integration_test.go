//go:build integration

package persistence

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fixture 时间轴（UTC）。Window 端点含 to（SQL 为 `>= from AND <= to`）：
// from=day1 00:00、to=09-04 00:00，fixture 观测/run 均落在 day1..day3、严格小于 to，
// 故 day3 全天（obs3/runA2/runB）全部纳入窗口。
// 预期数值（手推）：
//
//	tenant_a / skill "sk1"：2 观测
//	  quality：faithfulness 样本2 avg(1.0,0)=0.5 conf(0.9+0.7)/2=0.8；relevance 样本1 0/conf0.8
//	  behavior：rule_hits 1（obs1 rule 长度1），retry 1，escalation 0，abandonment 0，
//	  verdict pass 1 / flag 1 / block 0
//	  cost：tokens 30 cost 0.03，avg_latency (100+300)/2=200，p95=100+0.95*200=290
//	  process：runA2（window 内最近 succeeded，process_pass_rate 0.8；runA 0.5 仅作趋势点）
//	tenant_a / skill "sk2"：1 观测
//	  quality：completeness 样本1 1.0 conf1.0
//	  behavior：abandonment 1，verdict block 1；rule_hits 0
//	  cost：tokens 30 cost 0.03 latency 200 p95 200
//	  process：runB 是 failed（排除）→ null
//	tenant_b 的数据（skill "bx"）绝不出现在 tenant_a 的查询结果（隔离断言）。
func TestPgCenterQueryRepositoryMonitorResourcesAndTrend(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; monitor aggregation integration test requires real PostgreSQL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	tenants := []string{"monitor_one", "monitor_two"}
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

	seedMonitorA(t, ctx, pool, "tenant_monitor_one")
	seedMonitorB(t, ctx, pool, "tenant_monitor_two")

	// to 取 09-04 00:00；SQL 端点含 to（`<= $to`），fixture 观测/run 均严格小于 to，day3 全天纳入窗口。
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	repo := NewPgCenterQueryRepository(pool)

	// ── 端点 1：资源行摘要 ──
	page, err := repo.MonitorResources(ctx, "monitor_one", monitorFilter("", "", from, to, 20))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(page.Items))
	}
	sk1 := page.Items[0] // 按 sample_count 降序：sk1(2) 在前
	if sk1.ResourceKind != domain.ResourceKindSkill || sk1.ResourceID != "sk1" || sk1.SampleCount != 2 {
		t.Fatalf("sk1 header = %+v", sk1)
	}
	if sk1.Behavior.RuleHits != 1 || sk1.Behavior.RetryCount != 1 ||
		sk1.Behavior.EscalationCount != 0 || sk1.Behavior.AbandonmentCount != 0 {
		t.Fatalf("sk1 behavior = %+v", sk1.Behavior)
	}
	if sk1.Behavior.Verdict != (domain.VerdictDistribution{Pass: 1, Flag: 1, Block: 0}) {
		t.Fatalf("sk1 verdict = %+v", sk1.Behavior.Verdict)
	}
	if sk1.Cost.TotalTokens != 30 || sk1.Cost.TotalCostUSD != 0.03 ||
		sk1.Cost.AvgLatencyMS == nil || *sk1.Cost.AvgLatencyMS != 200 ||
		sk1.Cost.P95LatencyMS == nil || *sk1.Cost.P95LatencyMS != 290 {
		t.Fatalf("sk1 cost = %+v", sk1.Cost)
	}
	// sk1 窗口内有两条 succeeded run（runA day2、runA2 day3）：LATERAL `ORDER BY created_at DESC LIMIT 1`
	// 必须取最近一条 runA2；若 LIMIT 1 被删，观测会 join 两条 run 并拆成多行/放大 sample_count，
	// 上面的 SampleCount==2 与下列 RunID 断言即会失败（防横向 fanout 回归）。
	if sk1.Process == nil || sk1.Process.RunID != "runA2" || sk1.Process.ProcessPassRate != 0.8 {
		t.Fatalf("sk1 process = %+v, want runA2 0.8", sk1.Process)
	}
	faith := findQuality(sk1.Quality, "faithfulness")
	if faith == nil || faith.Samples != 2 || faith.PassRate != 0.5 || faith.AvgScore != 0.5 || faith.AvgConfidence != 0.8 {
		t.Fatalf("sk1 faithfulness = %+v", faith)
	}
	if rel := findQuality(sk1.Quality, "relevance"); rel == nil || rel.Samples != 1 || rel.PassRate != 0 {
		t.Fatalf("sk1 relevance = %+v", rel)
	}
	if findQuality(sk1.Quality, "completeness") != nil {
		t.Fatal("sk1 must not contain completeness dim (未出现维度不返回)")
	}

	sk2 := page.Items[1]
	if sk2.ResourceID != "sk2" || sk2.SampleCount != 1 {
		t.Fatalf("sk2 header = %+v", sk2)
	}
	if sk2.Process != nil {
		t.Fatalf("sk2 process = %+v, want nil (failed run 排除)", sk2.Process)
	}
	if comp := findQuality(sk2.Quality, "completeness"); comp == nil || comp.PassRate != 1.0 || comp.AvgConfidence != 1.0 {
		t.Fatalf("sk2 completeness = %+v", comp)
	}
	if sk2.Behavior.AbandonmentCount != 1 || sk2.Behavior.Verdict.Block != 1 {
		t.Fatalf("sk2 behavior = %+v", sk2.Behavior)
	}

	// kind/resource_id 过滤
	single, err := repo.MonitorResources(ctx, "monitor_one", monitorFilter("skill", "sk2", from, to, 20))
	if err != nil {
		t.Fatal(err)
	}
	if len(single.Items) != 1 || single.Items[0].ResourceID != "sk2" {
		t.Fatalf("filtered items = %+v", single.Items)
	}

	// 跨租户隔离：monitor_one 视角看不到 monitor_two 的任何资源
	for _, item := range page.Items {
		if item.ResourceID == "bx" {
			t.Fatalf("tenant isolation breached: %+v", item)
		}
	}

	// ── 端点 2：趋势 ──
	trend, err := repo.MonitorTrend(ctx, "monitor_one", monitorFilter("skill", "sk1", from, to, 0))
	if err != nil {
		t.Fatal(err)
	}
	if trend.ResourceKind != domain.ResourceKindSkill || trend.ResourceID != "sk1" {
		t.Fatalf("trend header = %+v", trend)
	}
	day1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	if len(trend.Series) != 2 || !trend.Series[0].BucketAt.Equal(day1) || !trend.Series[1].BucketAt.Equal(day2) {
		t.Fatalf("trend series buckets = %+v", trend.Series)
	}
	// day1 仅 obs1：faithfulness 1.0 conf 0.9，behavior retry 0；day2 仅 obs2。
	bucket := trend.Series[0]
	if bucket.SampleCount != 1 || bucket.Behavior.RetryCount != 0 {
		t.Fatalf("day1 bucket = %+v", bucket)
	}
	if f := findQuality(bucket.Quality, "faithfulness"); f == nil || f.PassRate != 1.0 || f.AvgConfidence != 0.9 {
		t.Fatalf("day1 faithfulness = %+v", f)
	}
	day2Bucket := trend.Series[1]
	if day2Bucket.SampleCount != 1 || day2Bucket.Behavior.RetryCount != 1 || day2Bucket.Behavior.Verdict.Flag != 1 {
		t.Fatalf("day2 bucket = %+v", day2Bucket)
	}
	// trend runs 应含窗口内全部 succeeded run（runA day2、runA2 day3）且按 created_at 升序。
	if len(trend.Runs) != 2 ||
		trend.Runs[0].RunID != "runA" || trend.Runs[0].ProcessPassRate != 0.5 ||
		trend.Runs[1].RunID != "runA2" || trend.Runs[1].ProcessPassRate != 0.8 ||
		!trend.Runs[0].RunCreatedAt.Before(trend.Runs[1].RunCreatedAt) {
		t.Fatalf("trend runs = %+v, want [runA(day2), runA2(day3)] 升序", trend.Runs)
	}

	// trend 无 run 的资源：runs 空数组
	emptyRuns, err := repo.MonitorTrend(ctx, "monitor_one", monitorFilter("skill", "sk2", from, to, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyRuns.Runs) != 0 || emptyRuns.Runs == nil {
		t.Fatalf("sk2 trend runs = %+v, want empty slice", emptyRuns.Runs)
	}
}

func monitorFilter(kind, id string, from, to time.Time, limit int) port.MonitorFilter {
	return port.MonitorFilter{ResourceKind: kind, ResourceID: id, From: &from, To: &to, Limit: limit}
}

// findQuality 返回指定维度，未出现返回 nil。
func findQuality(dims []domain.QualityDim, dimension string) *domain.QualityDim {
	for i := range dims {
		if dims[i].Dimension == dimension {
			return &dims[i]
		}
	}
	return nil
}

func seedMonitorA(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	seedMonitorRunFK(t, ctx, pool, schema)
	day1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	runA := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	runA2 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) // 窗口内第二条 succeeded（晚于 runA）
	runB := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		id, kind, resourceID string
		signals, costPerf    string
		verdict              string
		at                   time.Time
	}{
		{id: "obs1", kind: "skill", resourceID: "sk1",
			signals:  `{"rule":[{"rule":"r1","message":"m"}],"judge":[{"dimension":"faithfulness","score":1,"confidence":0.9,"reason":"ok"},{"dimension":"relevance","score":0,"confidence":0.8,"reason":"off"}],"behavior":{"retry":false,"escalation":false,"abandonment":false}}`,
			costPerf: `{"latency_ms":100,"tokens":10,"cost_usd":0.01}`, verdict: "pass", at: day1},
		{id: "obs2", kind: "skill", resourceID: "sk1",
			signals:  `{"rule":[],"judge":[{"dimension":"faithfulness","score":0,"confidence":0.7,"reason":"bad"}],"behavior":{"retry":true,"escalation":false,"abandonment":false}}`,
			costPerf: `{"latency_ms":300,"tokens":20,"cost_usd":0.02}`, verdict: "flag", at: day2},
		{id: "obs3", kind: "skill", resourceID: "sk2",
			signals:  `{"rule":[],"judge":[{"dimension":"completeness","score":1,"confidence":1,"reason":"full"}],"behavior":{"retry":false,"escalation":false,"abandonment":true}}`,
			costPerf: `{"latency_ms":200,"tokens":30,"cost_usd":0.03}`, verdict: "block", at: day3},
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx, `INSERT INTO `+schema+`.eval_observations
			(id, trace_id, resource_kind, resource_id, param_version, signals, cost_perf, stratum, verdict, created_at)
			VALUES ($1,$2,$3,$4,'{}'::jsonb,$5::jsonb,$6::jsonb,'', $7,$8)`,
			row.id, "trace-"+row.id, row.kind, row.resourceID, row.signals, row.costPerf, row.verdict, row.at); err != nil {
			t.Fatalf("seed obs %s: %v", row.id, err)
		}
	}
	runs := []struct {
		id, resourceID, status string
		metrics                string
		at                     time.Time
	}{
		{id: "runA", resourceID: "sk1", status: "succeeded", metrics: `{"process_pass_rate":0.5}`, at: runA},
		{id: "runA2", resourceID: "sk1", status: "succeeded", metrics: `{"process_pass_rate":0.8}`, at: runA2},
		{id: "runB", resourceID: "sk2", status: "failed", metrics: `{"process_pass_rate":0.2}`, at: runB},
	}
	for _, run := range runs {
		if _, err := pool.Exec(ctx, `INSERT INTO `+schema+`.eval_runs
			(id, resource_kind, resource_id, revision_id, suite_revision_id, status, passed, total_cases, passed_cases, metrics, context_snapshot, created_at)
			VALUES ($1,'skill',$2,'rev-1','suite-rev-1',$3,true,1,1,$4::jsonb,'{}'::jsonb,$5)`,
			run.id, run.resourceID, run.status, run.metrics, run.at); err != nil {
			t.Fatalf("seed run %s: %v", run.id, err)
		}
	}
}

func seedMonitorB(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	day1 := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO `+schema+`.eval_observations
		(id, trace_id, resource_kind, resource_id, param_version, signals, cost_perf, stratum, verdict, created_at)
		VALUES ('obs-bx','trace-bx','skill','bx','{}'::jsonb,'{"judge":[]}'::jsonb,'{"latency_ms":900,"tokens":99,"cost_usd":9}'::jsonb,'','block',$1)`,
		day1); err != nil {
		t.Fatalf("seed tenant b: %v", err)
	}
}

// seedMonitorRunFK 为 eval_runs 外键准备一张 eval_suites + 一个 revision。
// eval_suite_revisions 无 name/resource_id 列，INSERT 照抄 query_repository_integration_test.go
// seedCenterQuery 的合法列（id,suite_id,version_no,status,resource_kind,created_at）。
func seedMonitorRunFK(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	now := time.Now().UTC()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ` + schema + `.eval_suites(id,name,description,created_at) VALUES('mon-suite','monitor-suite','safe',$1)`, []any{now}},
		{`INSERT INTO ` + schema + `.eval_suite_revisions(id,suite_id,version_no,status,resource_kind,created_at) VALUES('suite-rev-1','mon-suite',1,'active','skill',$1)`, []any{now}},
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed run FK: %v (%s)", err, s.sql)
		}
	}
}
