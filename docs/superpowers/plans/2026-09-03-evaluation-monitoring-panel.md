# 评测指标监控面板（四区 × 资源行下钻）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在评测中心 `EvaluationCenterPage` 内新增租户级「监控」视图：以被测资源为行展示窗口内质量×行为×成本×过程四区摘要，行点击下钻该资源四区时间趋势。

**Architecture:** 后端只读聚合落在 `CenterQueryRepository` 纵切（`PgCenterQueryRepository` 内 `execTenantTx` 一条事务多条 SQL → `QueryService` 透传+窗口/limit 兜底 → handler `evaluationQueryService` 新方法 → 两个新 GET 路由 → contract golden）。前端 `EvaluationCenterPage` 在 health 与 review tab 之间加 `monitor` tab，挂 `EvaluationMonitorPanel`（RangePicker 默认近 7 天 + 资源行主表 + 下钻 `MonitorResourceDrawer`），数据走观测线 `eval_observations` 聚合、过程区走评测线 `eval_runs.metrics.process_pass_rate`。

**Tech Stack:** Go 1.25 + pgx v5（`execTenantTx` 多租户事务）、Gin v1.9、zod `.strict()` 前端模型、antd 5、native SVG 折线（仓库无 recharts import）、Vitest + @testing-library/react。

## Global Constraints

来自 spec（2026-09-03-evaluation-monitoring-panel-design.md），逐字绑定，每个 task 默认隐含：

- **端点契约（spec §4.2）**：
  - `GET /evaluations/monitoring/resources?resource_kind=&resource_id=&from=&to=&limit=`（resource_kind/resource_id 可选；单传 resource_id 不传 kind → 400）
  - `GET /evaluations/monitoring/resources/trend?resource_kind=&resource_id=&from=&to=`（kind 与 id 都必填 → 400）
  - 响应 JSON snake_case；`items` 按 `sample_count` 降序；`{items, window}` / `{resource_kind, resource_id, series, runs}`。
- **数值（spec §4.2/§4.3 + 仓库红线「行为数字禁止内联」）**：默认窗口近 **7** 天（后端权威兜底 + 前端 UI 默认各持，spec §4.3）；资源行 `limit` 默认 **20**、上限 **100**。时间窗含端点（`>= from AND <= to`），RFC3339。
- **口径（spec §3/§4.4）**：质量/行为/成本/延迟来自 `eval_observations`；`process_pass_rate` 只来自 `eval_runs.metrics`，且以**同一窗口**内最近一条 `status='succeeded'` run 为准（端点 1 每资源一条 / 端点 2 `runs` 全列）。观测无 process 字段。
- **空态规则（spec §3.1，不假装为零）**：质量区只返回窗口内实际出现过的 judge 维度（`signals->'judge'` 动态展开；dimension 未出现即不返回）；窗口无 succeeded run → `process: null`（前端显示「窗口内无评测」）；trend `runs` 为空数组。行为区未装配时计数为 0 属正常。不返回 `sample_count=0` 的假资源行。
- **信号值**：`verdict ∈ pass|flag|block`；judge score/confidence ∈ [0,1]，score 0/1 归一故 `avg(score)=pass_rate`；judge 维度 `faithfulness|relevance|completeness`。
- **租户隔离（仓库红线）**：所有访问方法必须 `postgres.WithTenant` + `execTenantTx`；port 方法显式带 `tenantID string`；禁止在 `execTenantTx` 外裸查 tenant 表。
- **port/接口改动同步（spec §7.3）**：`port.CenterQueryRepository` 与 handler `evaluationQueryService` 增方法后，必须同步 `internal/evaluation/application/query_service_test.go` 的 `queryRepoStub`、`api/http/handler/evaluation_handler_test.go` 的 `fakeEvaluationQueries`、`api/http/contracttest/review.go` 的 `contractQueryRepo`，缺一编译失败。
- **契约录制**：新路由必须先加进 `scripts/record-contracts.go` 的 `evalWhitelist`，否则 `make record-contracts` 不生成 golden；已有 golden 必须零 diff。
- **DDD 分层**：`domain/` 只依赖 stdlib + `pkg/constants`；`application/` 不 import pgx/Redis/NATS/Gin（可 import `pkg/constants`）；handler 只 bind + 取 tenant + 调 service + `c.Error`；错误逐层 `fmt.Errorf("...: %w", err)`。
- **错误映射**：bind 失败/kind 非法/单传 resource_id/from>to → 400（service 校验返回 `domain.ErrInvalidCenterQuery`，middleware 既定映射 400；handler bind 失败 `middleware.NewHTTPError(400, err)`）。
- **测试**：改动走完整门槛 R3；后端新函数圈复杂度 ≤10、长度 ≤120 行、嵌套 ≤4（不达标须拆分）。契约用 `api/http/contract_test.go` replay。
- **commit**：`[feat](evaluation): ...` 风格，`-m` 多段 + `Co-Authored-By: Claude <noreply@anthropic.com>`，只允许在 worktree `feat/evaluation-monitoring-panel` 提交。
- 后端集成测试需要真实 PG：先 `make infra-up`（或已有容器 PG），`TEST_DATABASE_URL` 指向后运行 `go test -tags=integration ./internal/evaluation/infrastructure/persistence/ -run Monitor`。无该 env 时集成测试 `t.Skip`（允许但不算验证通过）。

---

### Task 1: 后端监控领域类型 + 常量（纯新增）

**Files:**

- Modify: `internal/evaluation/domain/query.go`（文件尾追加类型）
- Modify: `pkg/constants/evaluation.go`（追加常量块）
- Create: `internal/evaluation/domain/query_monitor_test.go`

**Interfaces:**

- Produces（后续 task 使用）：
  - `domain.QualityDim{Dimension string; PassRate, AvgScore, AvgConfidence float64; Samples int}`
  - `domain.VerdictDistribution{Pass, Flag, Block int}`
  - `domain.BehaviorStats{RuleHits, RetryCount, EscalationCount, AbandonmentCount int; Verdict VerdictDistribution}`
  - `domain.CostStats{TotalTokens int64; TotalCostUSD float64; AvgLatencyMS, P95LatencyMS *float64}`
  - `domain.ProcessBaseline{ProcessPassRate float64; RunID string; RunCreatedAt time.Time}`
  - `domain.MonitorResourceSummary{ResourceKind ResourceKind; ResourceID string; SampleCount int; Quality []QualityDim; Behavior BehaviorStats; Cost CostStats; Process *ProcessBaseline}`
  - `domain.MonitorWindow{From, To time.Time}`
  - `domain.MonitorResourcesPage{Items []MonitorResourceSummary; Window MonitorWindow}`
  - `domain.MonitorTrendPoint{BucketAt time.Time; SampleCount int; Quality []QualityDim; Behavior BehaviorStats; Cost CostStats}`
  - `domain.RunProcessPoint{RunID string; ProcessPassRate float64; RunCreatedAt time.Time}`
  - `domain.MonitorTrendSeries{ResourceKind ResourceKind; ResourceID string; Series []MonitorTrendPoint; Runs []RunProcessPoint}`
  - `pkg/constants.EvalMonitorWindowDays = 7`、`EvalMonitorResourceLimitDefault = 20`、`EvalMonitorResourceLimitMax = 100`

本 task 只追加类型与常量，不改任何接口 → 编译绿、无 mock 波及。类型 JSON 字段与 spec §4.2 逐字段一一对应（`json` tag 已内嵌），wire 契约由 Task 3 golden 与 Task 4 前端 zod 双向守护。

- [ ] **Step 1: 写失败测试**

创建 `internal/evaluation/domain/query_monitor_test.go`：

```go
package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMonitorQualityDimJSONPinsWireShape 守护质量维 wire 字段名与 spec §4.2 一致。
func TestMonitorQualityDimJSONPinsWireShape(t *testing.T) {
	conf := 0.87
	dim := QualityDim{Dimension: "faithfulness", PassRate: 0.92, AvgScore: 0.92, AvgConfidence: conf, Samples: 128}
	got, err := json.Marshal(dim)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"dimension":"faithfulness","pass_rate":0.92,"avg_score":0.92,"avg_confidence":0.87,"samples":128}`
	if string(got) != want {
		t.Fatalf("marshal quality dim = %s, want %s", got, want)
	}
}

// TestMonitorCostStatsNullLatencyIsJSONNull 空态诚实：无延迟样本时 avg/p95 序列化为
// null 而非 0（spec §3.1 禁止以 0 伪装无数据）。
func TestMonitorCostStatsNullLatencyIsJSONNull(t *testing.T) {
	got, err := json.Marshal(CostStats{TotalTokens: 154000, TotalCostUSD: 0.42})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"avg_latency_ms":null`) || !strings.Contains(string(got), `"p95_latency_ms":null`) {
		t.Fatalf("marshal cost stats = %s, want null latency fields", got)
	}
}

// TestMonitorProcessNilSerializesNull process 为 nil（窗口无 succeeded run）时 wire 是
// null 而非缺省 0（spec §4.2 process 可为 null）。
func TestMonitorProcessNilSerializesNull(t *testing.T) {
	summary := MonitorResourceSummary{ResourceKind: ResourceKindSkill, ResourceID: "skill-a", Process: nil}
	got, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"process":null`) {
		t.Fatalf("marshal summary = %s, want process null", got)
	}
}

// TestMonitorTrendSeriesRoundTrip 端点 2 响应整体 round-trip（runs 空数组保真）。
func TestMonitorTrendSeriesRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	series := MonitorTrendSeries{
		ResourceKind: ResourceKindSkill,
		ResourceID:   "skill-a",
		Series: []MonitorTrendPoint{{
			BucketAt: at, SampleCount: 20,
			Behavior: BehaviorStats{RuleHits: 2, Verdict: VerdictDistribution{Pass: 19, Flag: 1}},
		}},
		Runs: []RunProcessPoint{},
	}
	data, err := json.Marshal(series)
	if err != nil {
		t.Fatal(err)
	}
	var back MonitorTrendSeries
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"runs":[]`) || len(back.Runs) != 0 {
		t.Fatalf("round trip lost empty runs: %s", data)
	}
	if back.Series[0].BucketAt != at {
		t.Fatalf("bucket_at round trip = %v, want %v", back.Series[0].BucketAt, at)
	}
}
```

- [ ] **Step 2: 运行确认失败（编译失败即红）**

Run: `go test ./internal/evaluation/domain/ -run Monitor`
Expected: 编译失败（`undefined: QualityDim`…）

- [ ] **Step 3: 追加领域类型**

在 `internal/evaluation/domain/query.go` 末尾（`TimelinePage` 之后）追加：

```go
// ---- 评测指标监控面板（spec 2026-09-03 §4.2/§4.3）----

// QualityDim 单 judge 语义维度聚合。score 已 0/1 归一，pass_rate 与 avg_score
// 数值同源（SQL avg(score)）；仅窗口内实际出现过的维度出现，未出现维度不返回。
type QualityDim struct {
	Dimension     string  `json:"dimension"`
	PassRate      float64 `json:"pass_rate"`
	AvgScore      float64 `json:"avg_score"`
	AvgConfidence float64 `json:"avg_confidence"`
	Samples       int     `json:"samples"`
}

// VerdictDistribution verdict 三态分布计数。
type VerdictDistribution struct {
	Pass  int `json:"pass"`
	Flag  int `json:"flag"`
	Block int `json:"block"`
}

// BehaviorStats 行为区聚合。rule/behavior 未装配时计数为 0 属正常（非错误）。
type BehaviorStats struct {
	RuleHits        int                 `json:"rule_hits"`
	RetryCount      int                 `json:"retry_count"`
	EscalationCount int                 `json:"escalation_count"`
	AbandonmentCount int                `json:"abandonment_count"`
	Verdict         VerdictDistribution `json:"verdict"`
}

// CostStats 成本与延迟聚合。latency 无有效样本时为 null（诚实空态，不伪 0）。
type CostStats struct {
	TotalTokens  int64    `json:"total_tokens"`
	TotalCostUSD float64  `json:"total_cost_usd"`
	AvgLatencyMS *float64 `json:"avg_latency_ms"`
	P95LatencyMS *float64 `json:"p95_latency_ms"`
}

// ProcessBaseline 窗口内最近一条 succeeded 评测 run 的过程通过率基线；无 run 时
// process=null（面板显示「窗口内无评测」）。
type ProcessBaseline struct {
	ProcessPassRate float64   `json:"process_pass_rate"`
	RunID           string    `json:"run_id"`
	RunCreatedAt    time.Time `json:"run_created_at"`
}

// MonitorResourceSummary 端点 1 资源行四区摘要。
type MonitorResourceSummary struct {
	ResourceKind ResourceKind     `json:"resource_kind"`
	ResourceID   string           `json:"resource_id"`
	SampleCount  int              `json:"sample_count"`
	Quality      []QualityDim     `json:"quality"`
	Behavior     BehaviorStats    `json:"behavior"`
	Cost         CostStats        `json:"cost"`
	Process      *ProcessBaseline `json:"process"`
}

// MonitorWindow 响应实际生效窗口（含 service 兜底默认近 7 天后的值）。
type MonitorWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// MonitorResourcesPage 端点 1 响应。
type MonitorResourcesPage struct {
	Items  []MonitorResourceSummary `json:"items"`
	Window MonitorWindow            `json:"window"`
}

// MonitorTrendPoint 端点 2 series 的单日桶聚合。
type MonitorTrendPoint struct {
	BucketAt    time.Time     `json:"bucket_at"`
	SampleCount int           `json:"sample_count"`
	Quality     []QualityDim  `json:"quality"`
	Behavior    BehaviorStats `json:"behavior"`
	Cost        CostStats     `json:"cost"`
}

// RunProcessPoint 端点 2 runs：该资源窗口内 succeeded run 过程基线离散点。
type RunProcessPoint struct {
	RunID           string    `json:"run_id"`
	ProcessPassRate float64   `json:"process_pass_rate"`
	RunCreatedAt    time.Time `json:"run_created_at"`
}

// MonitorTrendSeries 端点 2 响应。
type MonitorTrendSeries struct {
	ResourceKind ResourceKind        `json:"resource_kind"`
	ResourceID   string              `json:"resource_id"`
	Series       []MonitorTrendPoint `json:"series"`
	Runs         []RunProcessPoint   `json:"runs"`
}
```

- [ ] **Step 4: 追加行为常量**

在 `pkg/constants/evaluation.go` 末尾（`RunRegressionDeltaThreshold` 之后）追加：

```go
// 评测指标监控面板（EvaluationCenterPage「监控」tab，spec 2026-09-03 §4.3）行为边界：
// 默认监控窗口天数与资源行 limit 默认/上限。前端 web/src/constants 各持默认窗口天数
//（spec §4.3：两端各持有默认值并在 UI 明示，后端为权威兜底）。
const (
	// EvalMonitorWindowDays 面板默认监控窗口（近 N 天，含端点）。
	EvalMonitorWindowDays = 7
	// EvalMonitorResourceLimitDefault 资源行摘要默认返回条数（按观测样本数降序）。
	EvalMonitorResourceLimitDefault = 20
	// EvalMonitorResourceLimitMax 资源行 limit 上限。
	EvalMonitorResourceLimitMax = 100
)
```

- [ ] **Step 5: 运行测试确认绿**

Run: `go vet ./internal/evaluation/domain/ && go test ./internal/evaluation/domain/ -run Monitor`
Expected: PASS（编译通过 + 4 个用例通过）

- [ ] **Step 6: Commit**

```bash
git add internal/evaluation/domain/query.go internal/evaluation/domain/query_monitor_test.go pkg/constants/evaluation.go
git commit -m "feat(evaluation): 监控面板领域类型与窗口/limit 常量（spec 2026-09-03 §4.2/§4.3）" -m "QualityDim/BehaviorStats/CostStats/ProcessBaseline/MonitorResourceSummary/MonitorTrendSeries 与 wire JSON 逐字段对齐；空态诚实（latency null / process null / runs 空数组）。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: 后端聚合查询纵切（port 接口 + PgCenterQueryRepository SQL + QueryService + stub 同步 + 测试）

后端能力核心。本 task 同时扩展 `port.CenterQueryRepository` 与实现 `PgCenterQueryRepository`/`QueryService`（接口一加、wiring 即要求三者同批落地），并同步 `query_service_test.go` 的 `queryRepoStub` 与 `contracttest/review.go` 的 `contractQueryRepo`（两处都静态实现被扩展接口，不同步则 `go test ./internal/evaluation/... ./api/http/...` 编译失败）。`handler` 的 `evaluationQueryService` 接口在 Task 3 才扩展——本 task 不做，故 handler/wiring 不受影响。

**Files:**

- Modify: `internal/evaluation/domain/port/evaluation.go`（`CenterFilter` 旁加 `MonitorFilter`；接口加 2 方法）
- Modify: `internal/evaluation/infrastructure/persistence/query_repository.go`（实现 2 方法 + 行组装 helper）
- Modify: `internal/evaluation/application/query_service.go`（2 passthrough + `normalizeMonitorFilter` + 兜底空态）
- Modify: `internal/evaluation/application/query_service_test.go`（`queryRepoStub` 加 2 方法 + 新测试）
- Modify: `api/http/contracttest/review.go`（`contractQueryRepo` 加 2 方法，确定性样例供 Task 3 录 golden）
- Create: `internal/evaluation/infrastructure/persistence/query_repository_monitor_integration_test.go`（真实 PG + 双租户 fixture 校验 SQL 数值）

**Interfaces:**

- Consumes: Task 1 的 `domain.*Monitor*` 类型、`constants.EvalMonitor*` 常量。
- Produces：
  - `port.MonitorFilter{ResourceKind, ResourceID string; From, To *time.Time; Limit int}`
  - `port.CenterQueryRepository` 增：`MonitorResources(context.Context, string, MonitorFilter) (domain.MonitorResourcesPage, error)`、`MonitorTrend(context.Context, string, MonitorFilter) (domain.MonitorTrendSeries, error)`
  - `QueryService.MonitorResources / MonitorTrend`（Task 3 handler `evaluationQueryService` 依赖）
  - `contractQueryRepo.MonitorResources / MonitorTrend`（Task 3 录制 golden 的确定性数据源）

- [ ] **Step 1: 加 `MonitorFilter` 与接口方法**

`internal/evaluation/domain/port/evaluation.go`，在 `CenterFilter` 之后追加：

```go
// MonitorFilter 评测监控聚合查询过滤（窗口必填由 application 兜底近 7 天）。
type MonitorFilter struct {
	ResourceKind, ResourceID string
	From, To                 *time.Time
	Limit                    int
}
```

`CenterQueryRepository` 接口加两方法（放在 `Timeline` 方法之后）：

```go
	MonitorResources(context.Context, string, MonitorFilter) (domain.MonitorResourcesPage, error)
	MonitorTrend(context.Context, string, MonitorFilter) (domain.MonitorTrendSeries, error)
```

- [ ] **Step 2: 写失败集成测试（SQL 数值 + 双租户隔离的规范源）**

创建 `internal/evaluation/infrastructure/persistence/query_repository_monitor_integration_test.go`：

```go
//go:build integration

package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fixture 时间轴（UTC）。Window 为 [day1, day3]。
// 预期数值（手推）：
//  tenant_a / skill "sk1"：2 观测
//    quality：faithfulness 样本2 avg(1.0,0)=0.5 conf(0.9+0.7)/2=0.8；relevance 样本1 0/conf0.8
//    behavior：rule_hits 1（obs1 rule 长度1），retry 1，escalation 0，abandonment 0，
//    verdict pass 1 / flag 1 / block 0
//    cost：tokens 30 cost 0.03，avg_latency (100+300)/2=200，p95=100+0.95*200=290
//    process：runA（window 内最近 succeeded，process_pass_rate 0.5）
//  tenant_a / skill "sk2"：1 观测
//    quality：completeness 样本1 1.0 conf1.0
//    behavior：abandonment 1，verdict block 1；rule_hits 0
//    cost：tokens 30 cost 0.03 latency 200 p95 200
//    process：runB 是 failed（排除）→ null
//  tenant_b 的数据（skill "bx"）绝不出现在 tenant_a 的查询结果（隔离断言）。

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

	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	repo := NewPgCenterQueryRepository(pool)

	// ── 端点 1：资源行摘要 ──
	page, err := repo.MonitorResources(ctx, "monitor_one", monitorFilter("", "", from, to, 20))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items len = %d, want 2 (%d)", len(page.Items), len(page.Items))
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
	if sk1.Process == nil || sk1.Process.RunID != "runA" || sk1.Process.ProcessPassRate != 0.5 {
		t.Fatalf("sk1 process = %+v", sk1.Process)
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
	if len(trend.Series) != 2 || trend.Series[0].BucketAt != day1 || trend.Series[1].BucketAt != day2 {
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
	if len(trend.Runs) != 1 || trend.Runs[0].RunID != "runA" || trend.Runs[0].ProcessPassRate != 0.5 {
		t.Fatalf("trend runs = %+v", trend.Runs)
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
```

实现者须在该测试文件补 import `"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"`（helper 返回 `port.MonitorFilter`）；`errors` 仅在实际用到时才 import。测试函数 `TestPgCenterQueryRepositoryMonitorResourcesAndTrend` 中调用点保持上文形态：`repo.MonitorResources(ctx, "monitor_one", monitorFilter("skill", "sk2", from, to, 20))`。种子函数：

```go
func seedMonitorA(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	day1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	runA := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	runB := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		id, kind, resourceID string
		signals, costPerf    string
		verdict              string
		at                   time.Time
	}{
		{id: "obs1", kind: "skill", resourceID: "sk1",
			signals: `{"rule":[{"rule":"r1","message":"m"}],"judge":[{"dimension":"faithfulness","score":1,"confidence":0.9,"reason":"ok"},{"dimension":"relevance","score":0,"confidence":0.8,"reason":"off"}],"behavior":{"retry":false,"escalation":false,"abandonment":false}}`,
			costPerf: `{"latency_ms":100,"tokens":10,"cost_usd":0.01}`, verdict: "pass", at: day1},
		{id: "obs2", kind: "skill", resourceID: "sk1",
			signals: `{"rule":[],"judge":[{"dimension":"faithfulness","score":0,"confidence":0.7,"reason":"bad"}],"behavior":{"retry":true,"escalation":false,"abandonment":false}}`,
			costPerf: `{"latency_ms":300,"tokens":20,"cost_usd":0.02}`, verdict: "flag", at: day2},
		{id: "obs3", kind: "skill", resourceID: "sk2",
			signals: `{"rule":[],"judge":[{"dimension":"completeness","score":1,"confidence":1,"reason":"full"}],"behavior":{"retry":false,"escalation":false,"abandonment":true}}`,
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
```

`eval_runs` 种子要求外键 `suite_revision_id` 指向存在的 `eval_suites` revision（DDL `REFERENCES eval_suite_revisions(id) ON DELETE RESTRICT`）。上面直接用字面 `'suite-rev-1'` 会触发外键失败。参照 `query_repository_integration_test.go` 的 `seedCenterQuery`——它先插 `eval_suites` 与 suite revision。monitor 种子需先建立一张 suite + revision。实现者在本文件加：

```go
// seedMonitorRunFK 为 eval_runs 外键准备一张 eval_suites + 一个 revision。
func seedMonitorRunFK(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	now := time.Now().UTC()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ` + schema + `.eval_suites(id,name,description,created_at) VALUES('mon-suite','monitor-suite','safe',$1)`, []any{now}},
		{`INSERT INTO ` + schema + `.eval_suite_revisions(id,suite_id,name,resource_kind,resource_id,version_no,status,created_by,created_at) VALUES('suite-rev-1','mon-suite','v1','skill','sk1',1,'active','owner',$1)`, []any{now}},
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed run FK: %v (%s)", err, s.sql)
		}
	}
}
```

在 `seedMonitorA` 开头先调 `seedMonitorRunFK(t, ctx, pool, schema)`。`eval_suite_revisions` 的确切列名以 `tenant_schema.sql` 为准——实现者先 `grep -n "CREATE TABLE IF NOT EXISTS eval_suite_revisions" pkg/storage/postgres/tenant_schema.sql` 核对（`query_repository_integration_test.go:238-268` 的 `seedCenterQuery` 就是合法 INSERT 样本，直接照抄其 suite/revision 两条 INSERT 并换 id）。若 FK 列约束与样本不一致，以 `seedCenterQuery` 现成可用语句为准。

> 自校正注：上面测试中 `import "errors"` 若未被使用则删除；`time`、`domain`、`port`、`postgres`、`pgxpool` 均用到。测试对 sk1 排 `page.Items[0]`、sk2 排 `[1]`，依赖 `ORDER BY sample_count DESC`——若并列（sk1=2 唯一大），稳定。trend 的 Series 顺序依赖 Go 侧按 `BucketAt` 升序排序（见 Step 3 实现）。

- [ ] **Step 3: 跑集成测试确认失败（方法未实现 / 编译失败）**

Run: `go vet -tags=integration ./internal/evaluation/infrastructure/persistence/ 2>&1 | head -20`
Expected: 编译失败——`PgCenterQueryRepository` 未实现新增的 2 个接口方法（wiring/接口断言处报错），红。

- [ ] **Step 4: 实现 `PgCenterQueryRepository.MonitorResources` 与 helper**

在 `internal/evaluation/infrastructure/persistence/query_repository.go` 追加（文件尾，`wrapCenterQuery` 之后）。两查询在同一 `tenant` 事务内：Q1 资源行聚合（含行为/成本/process LATERAL），Q2 按 top 资源展开 judge 维度，Go 组装。SQL 数值语义：`signals.judge` 以 `jsonb_array_elements` 展开、score 0/1 归一故 `pass_rate=avg(score)`、rule 命中=`jsonb_array_length`、behavior 布尔用 `count(*) FILTER`、P95=`percentile_cont(0.95)`、窗口含端点 `>= from AND <= to`：

```go
// MonitorResources 返回窗口内按观测样本数降序的资源行四区摘要（spec §4.2 端点 1）。
func (r *PgCenterQueryRepository) MonitorResources(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	var page domain.MonitorResourcesPage
	err := r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
WITH top AS (
	SELECT o.resource_kind, o.resource_id,
		count(*)::int AS sample_count,
		coalesce(sum(jsonb_array_length(o.signals->'rule')),0)::int AS rule_hits,
		count(*) FILTER (WHERE o.signals->'behavior'->>'retry' = 'true')::int AS retry_count,
		count(*) FILTER (WHERE o.signals->'behavior'->>'escalation' = 'true')::int AS escalation_count,
		count(*) FILTER (WHERE o.signals->'behavior'->>'abandonment' = 'true')::int AS abandonment_count,
		count(*) FILTER (WHERE o.verdict = 'pass')::int AS verdict_pass,
		count(*) FILTER (WHERE o.verdict = 'flag')::int AS verdict_flag,
		count(*) FILTER (WHERE o.verdict = 'block')::int AS verdict_block,
		coalesce(sum((o.cost_perf->>'tokens')::bigint),0) AS total_tokens,
		coalesce(sum((o.cost_perf->>'cost_usd')::double precision),0) AS total_cost_usd,
		avg((o.cost_perf->>'latency_ms')::double precision) AS avg_latency_ms,
		percentile_cont(0.95) WITHIN GROUP (ORDER BY (o.cost_perf->>'latency_ms')::double precision) AS p95_latency_ms,
		p.run_id, p.process_pass_rate, p.run_created_at
	FROM eval_observations o
	LEFT JOIN LATERAL (
		SELECT r.id AS run_id, (r.metrics->>'process_pass_rate')::double precision AS process_pass_rate,
			r.created_at AS run_created_at
		FROM eval_runs r
		WHERE r.resource_kind = o.resource_kind AND r.resource_id = o.resource_id
			AND r.status = 'succeeded' AND r.created_at >= $3 AND r.created_at <= $4
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT 1
	) p ON true
	WHERE ($1 = '' OR o.resource_kind = $1) AND ($2 = '' OR o.resource_id = $2)
		AND o.created_at >= $3 AND o.created_at <= $4
	GROUP BY o.resource_kind, o.resource_id, p.run_id, p.process_pass_rate, p.run_created_at)
SELECT resource_kind, resource_id, sample_count, rule_hits, retry_count, escalation_count, abandonment_count,
	verdict_pass, verdict_flag, verdict_block, total_tokens, total_cost_usd, avg_latency_ms, p95_latency_ms,
	run_id, process_pass_rate, run_created_at
FROM top
ORDER BY sample_count DESC, resource_id ASC
LIMIT $5`,
			filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To, filter.Limit)
		if err != nil {
			return err
		}
		index := make(map[string]int, 8)
		for rows.Next() {
			var (
				s                domain.MonitorResourceSummary
				kind             string
				ruleHits, retry  int
				escalation       int
				abandonment      int
				vPass, vFlag     int
				vBlock           int
				totalTokens      int64
				totalCostUSD     float64
				avgLatency       *float64
				p95Latency       *float64
				runID            *string
				processRate      *float64
				runCreatedAt     *time.Time
			)
			if err := rows.Scan(&kind, &s.ResourceID, &s.SampleCount, &ruleHits, &retry, &escalation,
				&abandonment, &vPass, &vFlag, &vBlock, &totalTokens, &totalCostUSD, &avgLatency, &p95Latency,
				&runID, &processRate, &runCreatedAt); err != nil {
				return err
			}
			s.ResourceKind = domain.ResourceKind(kind)
			s.Behavior = domain.BehaviorStats{RuleHits: ruleHits, RetryCount: retry,
				EscalationCount: escalation, AbandonmentCount: abandonment,
				Verdict: domain.VerdictDistribution{Pass: vPass, Flag: vFlag, Block: vBlock}}
			s.Cost = domain.CostStats{TotalTokens: totalTokens, TotalCostUSD: totalCostUSD,
				AvgLatencyMS: avgLatency, P95LatencyMS: p95Latency}
			if runID != nil && processRate != nil && runCreatedAt != nil {
				s.Process = &domain.ProcessBaseline{ProcessPassRate: *processRate, RunID: *runID,
					RunCreatedAt: *runCreatedAt}
			}
			index[monitorResourceKey(kind, s.ResourceID)] = len(page.Items)
			page.Items = append(page.Items, s)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) == 0 {
			return nil
		}
		// Q2：按 top 资源展开 judge 维度（与 run 侧 by_dimension「未出现维度不返回」一致）。
		jrows, err := tx.Query(ctx, `
WITH top AS (
	SELECT o.resource_kind, o.resource_id
	FROM eval_observations o
	WHERE ($1 = '' OR o.resource_kind = $1) AND ($2 = '' OR o.resource_id = $2)
		AND o.created_at >= $3 AND o.created_at <= $4
	GROUP BY o.resource_kind, o.resource_id
	ORDER BY count(*) DESC, o.resource_id ASC
	LIMIT $5)
SELECT o.resource_kind, o.resource_id, j.value->>'dimension' AS dimension,
	avg((j.value->>'score')::double precision) AS avg_score,
	avg((j.value->>'confidence')::double precision) AS avg_confidence,
	count(*)::int AS samples
FROM eval_observations o, jsonb_array_elements(o.signals->'judge') j
WHERE (o.resource_kind, o.resource_id) IN (SELECT resource_kind, resource_id FROM top)
GROUP BY o.resource_kind, o.resource_id, j.value->>'dimension'
ORDER BY o.resource_kind, o.resource_id, j.value->>'dimension'`,
			filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To, filter.Limit)
		if err != nil {
			return err
		}
		defer jrows.Close()
		for jrows.Next() {
			var kind, id, dimension string
			var avgScore, avgConf float64
			var samples int
			if err := jrows.Scan(&kind, &id, &dimension, &avgScore, &avgConf, &samples); err != nil {
				return err
			}
			pos, ok := index[monitorResourceKey(kind, id)]
			if !ok {
				continue
			}
			page.Items[pos].Quality = append(page.Items[pos].Quality, domain.QualityDim{
				Dimension: dimension, PassRate: avgScore, AvgScore: avgScore, AvgConfidence: avgConf, Samples: samples})
		}
		return jrows.Err()
	})
	for i := range page.Items {
		if page.Items[i].Quality == nil {
			page.Items[i].Quality = []domain.QualityDim{}
		}
	}
	return page, wrapCenterQuery("monitor resources", err)
}

// monitorResourceKey 组装 (kind, id) 行键。
func monitorResourceKey(kind, id string) string {
	return kind + "\x00" + id
}
```

- [ ] **Step 5: 实现 `PgCenterQueryRepository.MonitorTrend`**

追加（同一文件尾）：

```go
// MonitorTrend 返回单资源窗口内按日桶聚合的四区趋势 + succeeded run 过程基线点
// （spec §4.2 端点 2）。桶为 UTC 日；桶内无观测则不出点（不返回 sample_count=0 假桶）。
func (r *PgCenterQueryRepository) MonitorTrend(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	var result domain.MonitorTrendSeries
	err := r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		bucketExpr := `(date_trunc('day', o.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`
		rows, err := tx.Query(ctx, `
SELECT `+bucketExpr+` AS bucket_at,
	count(*)::int AS sample_count,
	coalesce(sum(jsonb_array_length(o.signals->'rule')),0)::int AS rule_hits,
	count(*) FILTER (WHERE o.signals->'behavior'->>'retry' = 'true')::int AS retry_count,
	count(*) FILTER (WHERE o.signals->'behavior'->>'escalation' = 'true')::int AS escalation_count,
	count(*) FILTER (WHERE o.signals->'behavior'->>'abandonment' = 'true')::int AS abandonment_count,
	count(*) FILTER (WHERE o.verdict = 'pass')::int AS verdict_pass,
	count(*) FILTER (WHERE o.verdict = 'flag')::int AS verdict_flag,
	count(*) FILTER (WHERE o.verdict = 'block')::int AS verdict_block,
	coalesce(sum((o.cost_perf->>'tokens')::bigint),0) AS total_tokens,
	coalesce(sum((o.cost_perf->>'cost_usd')::double precision),0) AS total_cost_usd,
	avg((o.cost_perf->>'latency_ms')::double precision) AS avg_latency_ms,
	percentile_cont(0.95) WITHIN GROUP (ORDER BY (o.cost_perf->>'latency_ms')::double precision) AS p95_latency_ms
FROM eval_observations o
WHERE o.resource_kind = $1 AND o.resource_id = $2
	AND o.created_at >= $3 AND o.created_at <= $4
GROUP BY bucket_at`,
			filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To)
		if err != nil {
			return err
		}
		bucketIndex := make(map[time.Time]int, 16)
		for rows.Next() {
			var (
				bucket         time.Time
				ruleHits, retry, escalation, abandonment int
				vPass, vFlag, vBlock, sampleCount        int
				totalTokens    int64
				totalCostUSD   float64
				avgLatency     *float64
				p95Latency     *float64
			)
			if err := rows.Scan(&bucket, &sampleCount, &ruleHits, &retry, &escalation, &abandonment,
				&vPass, &vFlag, &vBlock, &totalTokens, &totalCostUSD, &avgLatency, &p95Latency); err != nil {
				return err
			}
			point := domain.MonitorTrendPoint{BucketAt: bucket, SampleCount: sampleCount,
				Quality: []domain.QualityDim{},
				Behavior: domain.BehaviorStats{RuleHits: ruleHits, RetryCount: retry,
					EscalationCount: escalation, AbandonmentCount: abandonment,
					Verdict: domain.VerdictDistribution{Pass: vPass, Flag: vFlag, Block: vBlock}},
				Cost: domain.CostStats{TotalTokens: totalTokens, TotalCostUSD: totalCostUSD,
					AvgLatencyMS: avgLatency, P95LatencyMS: p95Latency}}
			bucketIndex[bucket] = len(result.Series)
			result.Series = append(result.Series, point)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Q2：judge 维度按桶展开。
		jrows, err := tx.Query(ctx, `
WITH obs AS (
	SELECT o.id, o.created_at, o.signals FROM eval_observations o
	WHERE o.resource_kind = $1 AND o.resource_id = $2 AND o.created_at >= $3 AND o.created_at <= $4)
SELECT (date_trunc('day', o.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS bucket_at,
	j.value->>'dimension' AS dimension,
	avg((j.value->>'score')::double precision) AS avg_score,
	avg((j.value->>'confidence')::double precision) AS avg_confidence,
	count(*)::int AS samples
FROM obs o, jsonb_array_elements(o.signals->'judge') j
GROUP BY bucket_at, j.value->>'dimension'`,
			filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To)
		if err != nil {
			return err
		}
		defer jrows.Close()
		for jrows.Next() {
			var bucket time.Time
			var dimension string
			var avgScore, avgConf float64
			var samples int
			if err := jrows.Scan(&bucket, &dimension, &avgScore, &avgConf, &samples); err != nil {
				return err
			}
			pos, ok := bucketIndex[bucket]
			if !ok {
				continue
			}
			result.Series[pos].Quality = append(result.Series[pos].Quality, domain.QualityDim{
				Dimension: dimension, PassRate: avgScore, AvgScore: avgScore, AvgConfidence: avgConf, Samples: samples})
		}
		if err := jrows.Err(); err != nil {
			return err
		}
		// Q3：succeeded run 过程基线点（升序）。
		rrows, err := tx.Query(ctx, `
SELECT r.id, (r.metrics->>'process_pass_rate')::double precision, r.created_at
FROM eval_runs r
WHERE r.resource_kind = $1 AND r.resource_id = $2 AND r.status = 'succeeded'
	AND r.created_at >= $3 AND r.created_at <= $4
ORDER BY r.created_at ASC, r.id ASC`,
			filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var (
				runID    string
				rate     *float64
				runAt    time.Time
			)
			if err := rrows.Scan(&runID, &rate, &runAt); err != nil {
				return err
			}
			if rate == nil {
				continue // metrics 无 process_pass_rate（理论可达），如实跳过该 run
			}
			result.Runs = append(result.Runs, domain.RunProcessPoint{RunID: runID, ProcessPassRate: *rate, RunCreatedAt: runAt})
		}
		if err := rrows.Err(); err != nil {
			return err
		}
		if result.Runs == nil {
			result.Runs = []domain.RunProcessPoint{}
		}
		// 按桶升序稳定排序（SQL group by 无顺序保证）。
		sort.Slice(result.Series, func(i, j int) bool { return result.Series[i].BucketAt.Before(result.Series[j].BucketAt) })
		return nil
	})
	result.ResourceKind = domain.ResourceKind(filter.ResourceKind)
	result.ResourceID = filter.ResourceID
	return result, wrapCenterQuery("monitor trend", err)
}
```

`sort` 需加入 import：`query_repository.go` import 块追加 `"sort"`。

- [ ] **Step 5b: 编译清扫**

Run: `gofmt -w internal/evaluation/infrastructure/persistence/query_repository.go && go vet -tags=integration ./internal/evaluation/infrastructure/persistence/`
Expected: 编译仍失败点在 service/stub（port 接口已扩、实现齐）——下一步同步 QueryService 与 stub 后全绿。

- [ ] **Step 6: QueryService passthrough + normalize + 窗口兜底**

在 `internal/evaluation/application/query_service.go` 追加（文件尾）。import 补 `"time"` 与 `"github.com/byteBuilderX/stratum/pkg/constants"`：

```go
func normalizeMonitorFilter(filter port.MonitorFilter) (port.MonitorFilter, error) {
	if filter.ResourceKind != "" && domain.ResourceKind(filter.ResourceKind).Validate() != nil {
		return filter, domain.ErrInvalidCenterQuery
	}
	if filter.ResourceKind == "" && strings.TrimSpace(filter.ResourceID) != "" {
		return filter, fmt.Errorf("%w: resource_id requires resource_kind", domain.ErrInvalidCenterQuery)
	}
	if filter.From != nil && filter.To != nil && filter.To.Before(*filter.From) {
		return filter, fmt.Errorf("%w: from after to", domain.ErrInvalidCenterQuery)
	}
	if filter.From == nil {
		from := time.Now().UTC().Add(-time.Duration(constants.EvalMonitorWindowDays) * 24 * time.Hour)
		filter.From = &from
	}
	if filter.To == nil {
		to := time.Now().UTC()
		filter.To = &to
	}
	if filter.Limit == 0 {
		filter.Limit = constants.EvalMonitorResourceLimitDefault
	}
	if filter.Limit > constants.EvalMonitorResourceLimitMax {
		filter.Limit = constants.EvalMonitorResourceLimitMax
	}
	return filter, nil
}

func (s *QueryService) MonitorResources(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	f, e := normalizeMonitorFilter(filter)
	if e != nil {
		return domain.MonitorResourcesPage{}, e
	}
	p, e := s.repo.MonitorResources(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.MonitorResourceSummary{}
	}
	p.Window = domain.MonitorWindow{From: *f.From, To: *f.To}
	return p, mapCenterError(e)
}

func (s *QueryService) MonitorTrend(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	if strings.TrimSpace(filter.ResourceKind) == "" || strings.TrimSpace(filter.ResourceID) == "" {
		return domain.MonitorTrendSeries{}, fmt.Errorf("%w: resource required", domain.ErrInvalidCenterQuery)
	}
	f, e := normalizeMonitorFilter(filter)
	if e != nil {
		return domain.MonitorTrendSeries{}, e
	}
	trend, e := s.repo.MonitorTrend(ctx, tenantID, f)
	if trend.Series == nil {
		trend.Series = []domain.MonitorTrendPoint{}
	}
	if trend.Runs == nil {
		trend.Runs = []domain.RunProcessPoint{}
	}
	return trend, mapCenterError(e)
}
```

- [ ] **Step 7: 同步 `queryRepoStub` 并补 service 测试**

`internal/evaluation/application/query_service_test.go`：`queryRepoStub` 增两方法并加捕获字段：

```go
type queryRepoStub struct {
	filter     port.CenterFilter
	err        error
	candidates domain.CandidatePage
	monFilter  port.MonitorFilter
	monPage    domain.MonitorResourcesPage
}
```

追加 stub 方法（在文件内 stub 其余方法之后）：

```go
func (r *queryRepoStub) MonitorResources(_ context.Context, _ string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	r.monFilter = filter
	return r.monPage, r.err
}
func (r *queryRepoStub) MonitorTrend(_ context.Context, _ string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	r.monFilter = filter
	return domain.MonitorTrendSeries{}, r.err
}
```

新增测试（追加到 `query_service_test.go`）：

```go
// TestQueryServiceMonitorNormalizesWindowAndLimit 兜底：未给窗口 → 填近 EvalMonitorWindowDays 天；
// limit 默认/封顶；window 与空切片由 service 保证。
func TestQueryServiceMonitorNormalizesWindowAndLimit(t *testing.T) {
	repo := &queryRepoStub{}
	svc := NewQueryService(repo)
	page, err := svc.MonitorResources(context.Background(), "tenant-1", port.MonitorFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if repo.monFilter.From == nil || repo.monFilter.To == nil {
		t.Fatal("window not defaulted")
	}
	if !repo.monFilter.To.After(*repo.monFilter.From) {
		t.Fatalf("window invalid: %v -> %v", repo.monFilter.From, repo.monFilter.To)
	}
	if repo.monFilter.Limit != constants.EvalMonitorResourceLimitDefault {
		t.Fatalf("default limit = %d", repo.monFilter.Limit)
	}
	if page.Items == nil || page.Window.From.IsZero() {
		t.Fatalf("page items/window not normalized: %+v", page)
	}
}

// TestQueryServiceMonitorRejectsBadFilters kind 非法 / 单传 resource_id / from>to → invalid。
func TestQueryServiceMonitorRejectsBadFilters(t *testing.T) {
	svc := NewQueryService(&queryRepoStub{})
	now := time.Now().UTC()
	before, after := now.Add(-time.Hour), now
	cases := []port.MonitorFilter{
		{ResourceKind: "invalid"},
		{ResourceID: "only-id"},
		{From: &after, To: &before},
	}
	for _, filter := range cases {
		if _, err := svc.MonitorResources(context.Background(), "tenant-1", filter); !errors.Is(err, domain.ErrInvalidCenterQuery) {
			t.Errorf("filter %+v error = %v, want invalid", filter, err)
		}
	}
}

// TestQueryServiceMonitorTrendRequiresResource trend 缺 kind/id → invalid；repo not-found 透传。
func TestQueryServiceMonitorTrendRequiresResource(t *testing.T) {
	repo := &queryRepoStub{}
	svc := NewQueryService(repo)
	if _, err := svc.MonitorTrend(context.Background(), "tenant-1", port.MonitorFilter{}); !errors.Is(err, domain.ErrInvalidCenterQuery) {
		t.Fatalf("missing resource error = %v", err)
	}
	repo.err = port.ErrCenterResourceNotFound
	if _, err := svc.MonitorTrend(context.Background(), "tenant-1", port.MonitorFilter{
		ResourceKind: "skill", ResourceID: "sk1",
	}); !errors.Is(err, domain.ErrCenterResourceNotFound) {
		t.Fatalf("not found error = %v", err)
	}
}
```

`query_service_test.go` import 需补 `"time"` 与 `"github.com/byteBuilderX/stratum/pkg/constants"`。

- [ ] **Step 8: 同步 `contractQueryRepo`**

`api/http/contracttest/review.go` 的 `contractQueryRepo`（实现 `port.CenterQueryRepository`）加两方法，返回**确定性**样例（Task 3 录制 golden 即取自此）：

```go
func (r *contractQueryRepo) MonitorResources(context.Context, string, port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	return domain.MonitorResourcesPage{
		Items: []domain.MonitorResourceSummary{{
			ResourceKind: domain.ResourceKindSkill, ResourceID: "resource-1", SampleCount: 128,
			Quality: []domain.QualityDim{{Dimension: "faithfulness", PassRate: 0.92, AvgScore: 0.92, AvgConfidence: 0.87, Samples: 128}},
			Behavior: domain.BehaviorStats{RuleHits: 15, RetryCount: 3, EscalationCount: 1, Verdict: domain.VerdictDistribution{Pass: 120, Flag: 6, Block: 2}},
			Cost:     costPtr(154000, 0.42, 1800, 5200),
		}},
		Window: domain.MonitorWindow{From: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)},
	}, nil
}

func (r *contractQueryRepo) MonitorTrend(context.Context, string, port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	return domain.MonitorTrendSeries{
		ResourceKind: domain.ResourceKindSkill, ResourceID: "resource-1",
		Series: []domain.MonitorTrendPoint{{
			BucketAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), SampleCount: 20,
			Behavior: domain.BehaviorStats{Verdict: domain.VerdictDistribution{Pass: 19, Flag: 1}},
			Cost:     costPtr(24000, 0.06, 1600, 4100),
		}},
		Runs: []domain.RunProcessPoint{{RunID: "run-9", ProcessPassRate: 0.67, RunCreatedAt: time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)}},
	}, nil
}

// costPtr 便捷构造（nil-safe 延迟指针）。放 contracttest 包级。
func costPtr(tokens int64, costUSD, avg, p95 float64) domain.CostStats {
	return domain.CostStats{TotalTokens: tokens, TotalCostUSD: costUSD, AvgLatencyMS: &avg, P95LatencyMS: &p95}
}
```

按 `contracttest/review.go` 既有 helper 命名风格微调（若已有类似 `ptr` helper 则复用），import 补 `domain`（若未引入）与 `time`。

- [ ] **Step 9: 全量编译 + 单测**

Run: `go vet ./internal/evaluation/... ./api/http/... && go test -short ./internal/evaluation/... ./api/http/...`
Expected: 全 PASS（含新增 service 测试；集成测试因无 `TEST_DATABASE_URL` 自动 skip）

- [ ] **Step 10: 跑集成测试验证 SQL 数值（真实 PG）**

Run: `make infra-up`（如 PG 未起）；`TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/stratum_test?sslmode=disable go test -tags=integration ./internal/evaluation/infrastructure/persistence/ -run MonitorResourcesAndTrend -v`
Expected: PASS——sk1/sk2 数值、quality 维度展开、process LATERAL 与 null、跨租户隔离、trend 日桶全部命中手推值。若断言失败，对照 seed 与 SQL 修正（先确认 fixture 外键通过：`seedMonitorRunFK` 前先 `grep` 套件 revision 列名）。
（若本地无 PG 且无法起，如实记录「集成测试未执行」为阻塞项，不静默通过。）

- [ ] **Step 11: Commit**

```bash
git add internal/evaluation/domain/port/evaluation.go internal/evaluation/infrastructure/persistence/query_repository.go internal/evaluation/infrastructure/persistence/query_repository_monitor_integration_test.go internal/evaluation/application/query_service.go internal/evaluation/application/query_service_test.go api/http/contracttest/review.go
git commit -m "feat(evaluation): 监控四区聚合查询纵切（MonitorResources/MonitorTrend）" -m "port.CenterQueryRepository 增两方法；PgCenterQueryRepository 于 execTenantTx 内多条 SQL 聚合观测（jsonb_array_elements judge 展开/rule 长度/behavior FILTER/percentile_cont P95）并 LATERAL 关联窗口内最近 succeeded run 过程基线；QueryService 透传+窗口近7天/limit 兜底+校验；同步 queryRepoStub 与 contractQueryRepo。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: handler 新端点 + 路由 + handler 单测

把新能力接上 HTTP：扩展 handler `evaluationQueryService` 接口、两个 GET handler、路由注册、`fakeEvaluationQueries` 同步 + 200/400 handler 测试。

**Files:**

- Modify: `api/http/handler/evaluation_handler.go`（接口 + 2 handler + 2 query DTO）
- Modify: `api/http/router.go`（`registerEvaluations` 加 2 路由）
- Modify: `api/http/handler/evaluation_handler_test.go`（`fakeEvaluationQueries` 加 2 方法 + 新测试）

**Interfaces:**

- Consumes: Task 2 的 `port.MonitorFilter`、`QueryService.MonitorResources/MonitorTrend`。
- Produces: 路由 `GET /evaluations/monitoring/resources`、`GET /evaluations/monitoring/resources/trend`（Task 4 契约录制、前端调用依赖）。

- [ ] **Step 1: 写失败 handler 测试（stub 未实现 → 编译红）**

在 `api/http/handler/evaluation_handler_test.go` 追加（import 若无则补 `"encoding/json"`、`domain`、`time` 已在）：

```go
// TestEvaluationHandlerMonitorResourcesPropagatesFilterAndPage 端点 1：200 + 参数透传。
func TestEvaluationHandlerMonitorResourcesPropagatesFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/monitoring/resources", withTenant("tenant-1"), h.ListMonitorResources)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/evaluations/monitoring/resources?resource_kind=skill&resource_id=sk1&from=2026-09-01T00:00:00Z&to=2026-09-03T00:00:00Z&limit=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if queries.monitorKind != "skill" || queries.monitorID != "sk1" || queries.monitorLimit != 7 {
		t.Fatalf("filter not propagated: kind=%q id=%q limit=%d", queries.monitorKind, queries.monitorID, queries.monitorLimit)
	}
	if queries.monitorFrom == nil || queries.monitorTo == nil {
		t.Fatal("from/to not propagated")
	}
	var page domain.MonitorResourcesPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("typed page response=%s err=%v", rec.Body.String(), err)
	}
}

// TestEvaluationHandlerMonitorResourcesRejectsBadQuery 端点 1：400 表。
func TestEvaluationHandlerMonitorResourcesRejectsBadQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/monitoring/resources", withTenant("tenant-1"), h.ListMonitorResources)
	urls := []string{
		"/evaluations/monitoring/resources?resource_id=only",           // 单传 id 无 kind
		"/evaluations/monitoring/resources?resource_kind=bad",           // kind 非法
		"/evaluations/monitoring/resources?resource_kind=skill&from=2026-09-03T00:00:00Z&to=2026-09-01T00:00:00Z", // from>to
	}
	for _, raw := range urls {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, raw, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status=%d body=%s, want 400", raw, rec.Code, rec.Body.String())
		}
	}
}

// TestEvaluationHandlerMonitorTrendPropagatesAndValidates 端点 2：200 + 缺 kind/id → 400。
func TestEvaluationHandlerMonitorTrendPropagates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &fakeEvaluationQueries{}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/monitoring/resources/trend", withTenant("tenant-1"), h.GetMonitorTrend)
	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodGet,
		"/evaluations/monitoring/resources/trend?resource_kind=skill&resource_id=sk1", nil))
	if ok.Code != http.StatusOK {
		t.Fatalf("ok status=%d body=%s", ok.Code, ok.Body.String())
	}
	var series domain.MonitorTrendSeries
	if err := json.Unmarshal(ok.Body.Bytes(), &series); err != nil || series.ResourceID != "sk1" {
		t.Fatalf("typed series response=%s err=%v", ok.Body.String(), err)
	}
	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodGet,
		"/evaluations/monitoring/resources/trend?resource_kind=skill", nil)) // 缺 id
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad status=%d body=%s, want 400", bad.Code, bad.Body.String())
	}
}
```

- [ ] **Step 2: 扩展接口 + handler + DTO + stub 数据**

`evaluation_handler.go` 的 `evaluationQueryService` 接口加：

```go
	MonitorResources(context.Context, string, port.MonitorFilter) (domain.MonitorResourcesPage, error)
	MonitorTrend(context.Context, string, port.MonitorFilter) (domain.MonitorTrendSeries, error)
```

`ListObservationsQuery` 附近追加 DTO + 两个 handler 方法（放在 `Overview` 附近、`queryPage` helpers 之后即可）。DTO 字段名与 spec §4.2 查询参数一一对应：

```go
// MonitorQuery 监控聚合查询参数。from/to 可选（RFC3339，缺省由 service 兜底近 7 天）；
// resource_kind/resource_id 可选（端点 1）；limit 可选（默认/上限走 pkg/constants）。
// 端点 2 trend 复用同 DTO：kind/id 必填由 service 校验。
type MonitorQuery struct {
	ResourceKind string     `form:"resource_kind"`
	ResourceID   string     `form:"resource_id"`
	From         *time.Time `form:"from" time_format:"2006-01-02T15:04:05Z07:00"`
	To           *time.Time `form:"to" time_format:"2006-01-02T15:04:05Z07:00"`
	Limit        int        `form:"limit"`
}

// ListMonitorResources 返回窗口内资源行四区摘要（spec §4.2 端点 1，member 可读）。
func (h *EvaluationHandler) ListMonitorResources(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req MonitorQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	page, err := h.queries.MonitorResources(c.Request.Context(), tenantID, port.MonitorFilter{
		ResourceKind: req.ResourceKind, ResourceID: req.ResourceID, From: req.From, To: req.To, Limit: req.Limit,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// GetMonitorTrend 返回单资源四区时间趋势（spec §4.2 端点 2，member 可读）。
func (h *EvaluationHandler) GetMonitorTrend(c *gin.Context) {
	tenantID, ok := tenantIDFromCtx(c)
	if !ok {
		respondMissingTenant(c)
		return
	}
	var req MonitorQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		_ = c.Error(middleware.NewHTTPError(http.StatusBadRequest, err))
		return
	}
	series, err := h.queries.MonitorTrend(c.Request.Context(), tenantID, port.MonitorFilter{
		ResourceKind: req.ResourceKind, ResourceID: req.ResourceID, From: req.From, To: req.To,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, series)
}
```

- [ ] **Step 3: 同步 `fakeEvaluationQueries`**

`evaluation_handler_test.go` 的 `fakeEvaluationQueries` 加捕获字段与两方法：

```go
type fakeEvaluationQueries struct {
	tenantID        string
	filter          port.CenterFilter
	monitorKind     string
	monitorID       string
	monitorFrom     *time.Time
	monitorTo       *time.Time
	monitorLimit    int
}
```

```go
func (f *fakeEvaluationQueries) MonitorResources(_ context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	f.tenantID = tenantID
	f.monitorKind = filter.ResourceKind
	f.monitorID = filter.ResourceID
	f.monitorFrom = filter.From
	f.monitorTo = filter.To
	f.monitorLimit = filter.Limit
	pass := 0.92
	return domain.MonitorResourcesPage{Items: []domain.MonitorResourceSummary{{
		ResourceKind: domain.ResourceKindSkill, ResourceID: "sk1", SampleCount: 2,
		Quality: []domain.QualityDim{{Dimension: "faithfulness", PassRate: pass, AvgScore: pass, AvgConfidence: 0.8, Samples: 2}},
		Process: &domain.ProcessBaseline{ProcessPassRate: 0.5, RunID: "runA", RunCreatedAt: time.Now().UTC()},
	}}}, nil
}
func (f *fakeEvaluationQueries) MonitorTrend(_ context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	f.tenantID = tenantID
	f.monitorKind = filter.ResourceKind
	f.monitorID = filter.ResourceID
	f.monitorFrom = filter.From
	f.monitorTo = filter.To
	return domain.MonitorTrendSeries{ResourceKind: domain.ResourceKind(filter.ResourceKind), ResourceID: filter.ResourceID,
		Series: []domain.MonitorTrendPoint{{BucketAt: time.Now().UTC()}}, Runs: []domain.RunProcessPoint{}}, nil
}
```

- [ ] **Step 4: 注册路由**

`api/http/router.go` `registerEvaluations` 的 GET 组内、`/observations/:id` 之后加：

```go
		// 评测指标监控（spec 2026-09-03）：租户自有观测/评测聚合，member 可读。
		evaluations.GET("/monitoring/resources", h.ListMonitorResources)
		evaluations.GET("/monitoring/resources/trend", h.GetMonitorTrend)
```

- [ ] **Step 5: 编译 + 测试**

Run: `go vet ./api/http/... && go test ./api/http/handler/ -run 'Monitor' -v`
Expected: PASS（3 个新 handler 测试）；整包 `go test -short ./api/http/...` 全绿。

- [ ] **Step 6: Commit**

```bash
git add api/http/handler/evaluation_handler.go api/http/handler/evaluation_handler_test.go api/http/router.go
git commit -m "feat(evaluation): 监控聚合端点 handler + 路由（member 可读）" -m "evaluationQueryService 接口增 MonitorResources/MonitorTrend；MonitorQuery DTO（form + RFC3339 time_format）bind 失败 400；kind 非法/单传 resource_id/from>to 由 service 校验映射 400；fakeEvaluationQueries 同步。路由 /evaluations/monitoring/resources[/trend]。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: 契约 golden 扩展（record whitelist + 录制 + 反例）

新路由必须进 `scripts/record-contracts.go` 的 `evalWhitelist`，跑 recorder 生成正例 golden；再手写 400 反例 case 追加到 golden 文件；`TestContracts` replay 全绿。录制基于 `contracttest.BuildContainer`（全 stub，无 DB）。

**Files:**

- Modify: `scripts/record-contracts.go`（`evalWhitelist` 加 2 路由）
- Create: `api/http/testdata/contracts/get_evaluations_monitoring_resources.golden.json`
- Create: `api/http/testdata/contracts/get_evaluations_monitoring_resources_trend.golden.json`
- Test: `api/http/contract_test.go`（replay，不直接改文件）

- [ ] **Step 1: 加 recorder whitelist**

`scripts/record-contracts.go` `evalWhitelist` map（约 L122-131）加两键：

```go
	"GET /evaluations/monitoring/resources":          true,
	"GET /evaluations/monitoring/resources/trend":    true,
```

（现有键为 `... : true`，补两行即可。）

- [ ] **Step 2: 录制正例 golden**

Run: `make record-contracts`
Expected: 生成两个新 golden（`git status` 见新增）；**已有 golden 零 diff**（若旧 golden 变化，检查是录制时序/环境漂移而非本次改动，勿混入）。

- [ ] **Step 3: 核对正例 golden 形状**

Read: `api/http/testdata/contracts/get_evaluations_monitoring_resources.golden.json`
Expected: 单个 case `authenticated-success`，`want_status:200`，`want_body` 含 Task 2 `contractQueryRepo` 返回的样例（resource-1、quality、behavior、cost、process、window）。若 `want_body` 缺 `window`/`process` 等字段 → 回 Task 2 修 stub 与录制（golden 必须与 spec §4.2 形状一致，前端 Task 5 zod 将依赖）。

- [ ] **Step 4: 追加 400 反例 case**

在两份 golden JSON 文件的数组内各追加校验失败 case（保持数组元素逗号正确）：

`get_evaluations_monitoring_resources.golden.json` 末尾追加：

```json
  ,{
    "name": "rejects-resource-id-without-kind",
    "method": "GET",
    "path": "/evaluations/monitoring/resources?resource_id=only",
    "want_status": 400,
    "want_body_regex": "\\{\"error\":\"[^\"]*\"\\}"
  },
  {
    "name": "rejects-invalid-resource-kind",
    "method": "GET",
    "path": "/evaluations/monitoring/resources?resource_kind=bad",
    "want_status": 400,
    "want_body_regex": "\\{\"error\":\"[^\"]*\"\\}"
  },
  {
    "name": "rejects-from-after-to",
    "method": "GET",
    "path": "/evaluations/monitoring/resources?resource_kind=skill&from=2026-09-03T00:00:00Z&to=2026-09-01T00:00:00Z",
    "want_status": 400,
    "want_body_regex": "\\{\"error\":\"[^\"]*\"\\}"
  }
```

`get_evaluations_monitoring_resources_trend.golden.json` 末尾追加：

```json
  ,{
    "name": "rejects-missing-resource-id",
    "method": "GET",
    "path": "/evaluations/monitoring/resources/trend?resource_kind=skill",
    "want_status": 400,
    "want_body_regex": "\\{\"error\":\"[^\"]*\"\\}"
  }
```

- [ ] **Step 5: replay 全绿**

Run: `go test -run TestContracts ./api/http/ -count=1`
Expected: 全部 PASS（含新 golden 的正例 + 反例）。若 400 的 body regex 不匹配，读实际 body 微调 regex。

- [ ] **Step 6: 前端契约一致性快速核对（可选）**

后端 golden `want_body` 与 spec §4.2 JSON 逐字段一致（quality 数组、behavior 内层 verdict、cost、process null 语义、window）。与 Task 5 zod 对齐即可。

- [ ] **Step 7: Commit**

```bash
git add scripts/record-contracts.go api/http/testdata/contracts/get_evaluations_monitoring_resources.golden.json api/http/testdata/contracts/get_evaluations_monitoring_resources_trend.golden.json
git commit -m "test(evaluation): 监控端点 contract golden（正例 + 校验失败反例）" -m "record-contracts evalWhitelist 加 /evaluations/monitoring/resources[/trend]；反例守护 400（resource_id 单传/kind 非法/from>to/缺 id）。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: 前端监控 model zod schema + api + 常量（含单测）

前端数据契约层。`web/src/modules/evaluation/model/evaluation.ts` 的 zod schema 严格对齐 spec §4.2 端点 JSON（`resourceKindSchema` 已存在 L3-4、`resourceRefSchema` 已存在）；`web/src/modules/evaluation/api/evaluation.api.ts` 沿用既有 GET 范式（`api.get(path, filters ? { params: filters } : undefined)` + zod parse，见 `listRuns` L54-57）；窗口天数等行为数字入 `web/src/constants/index.ts` EVALUATION 块。

> **前置**：本 worktree `web/node_modules` 未安装（隔离 worktree）。首次跑前端单测前先 `cd web && npm ci`（依赖与根 `package-lock.json` 一致；dayjs 由 antd 传递依赖 npm-hoist 至顶层，运行时 `import dayjs from 'dayjs'` 可解析，与现有 `import type { Dayjs } from 'dayjs'` 的两文件兼容，无需改 package.json）。

**Files:**

- Modify: `web/src/modules/evaluation/model/evaluation.ts`（尾追加监控 schema + 类型）
- Test: `web/src/modules/evaluation/model/evaluation.test.ts`
- Modify: `web/src/modules/evaluation/api/evaluation.api.ts`（新增 2 方法 + import）
- Test: `web/src/modules/evaluation/api/evaluation.api.test.ts`
- Modify: `web/src/constants/index.ts`（EVALUATION 块尾追加 3 常量）

**Interfaces:**

- Consumes：spec §4.2 端点 JSON（Task 2 `contractQueryRepo` 与 Task 4 golden 已按此形状录制）。
- Produces（供 Task 7/8/9 使用，名字必须一致）：
  - model 导出 `qualityDimSchema / verdictDistributionSchema / behaviorStatsSchema / costStatsSchema / processBaselineSchema / monitorResourceSummarySchema / monitorWindowSchema / monitorResourcesPageSchema / monitorTrendPointSchema / runProcessPointSchema / monitorTrendSchema` 及其 z.infer 类型 `MonitorResourceSummary / MonitorTrend`，`MonitorFilters` interface。
  - api 导出 `evaluationApi.listMonitorResources(filters?) / getMonitorTrend(filters)`。
  - constants 导出 `EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS = 7`、`EVALUATION_MONITOR_RESOURCE_LIMIT = 20`、`EVALUATION_MONITOR_WINDOW_PRESETS_DAYS = [7, 14, 30] as const`。

- [ ] **Step 1: model 尾追加监控 schema**

在 `web/src/modules/evaluation/model/evaluation.ts` 末尾（`EvaluationCenterFilters` interface 之后）追加。逐字段对齐 spec §4.2：`quality` 为动态数组（judge 未装配/无样本维度不出现）；`process` 顶层可为 null（窗口内无 succeeded run）；`cost.avg_latency_ms / p95_latency_ms` 可为 null（无延迟样本）；`series`/`runs` 为空数组即空态（不伪造点）；所有对象 `.strict()`：

```ts
// —— 评测指标监控面板（spec 2026-09-03 §4.2）——

// qualityDimSchema 单 judge 语义维度的窗口聚合；仅在实际观测到样本时出现。
export const qualityDimSchema = z.object({
  dimension: z.string(),
  pass_rate: z.number(),
  avg_score: z.number(),
  avg_confidence: z.number(),
  samples: z.number(),
}).strict();
export type QualityDim = z.infer<typeof qualityDimSchema>;

export const verdictDistributionSchema = z.object({
  pass: z.number(),
  flag: z.number(),
  block: z.number(),
}).strict();
export type VerdictDistribution = z.infer<typeof verdictDistributionSchema>;

export const behaviorStatsSchema = z.object({
  rule_hits: z.number(),
  retry_count: z.number(),
  escalation_count: z.number(),
  abandonment_count: z.number(),
  verdict: verdictDistributionSchema,
}).strict();
export type BehaviorStats = z.infer<typeof behaviorStatsSchema>;

// costStatsSchema 观测线成本；avg/p95 延迟无样本时为 null（不做假装为零）。
export const costStatsSchema = z.object({
  total_tokens: z.number(),
  total_cost_usd: z.number(),
  avg_latency_ms: z.number().nullable(),
  p95_latency_ms: z.number().nullable(),
}).strict();
export type CostStats = z.infer<typeof costStatsSchema>;

// processBaselineSchema 窗口内最近一条 succeeded 评测 run 的过程通过率基线；
// process 为 null 表示该窗口无离线评测（run 未做过程断言时该值恒 1.0，前端须带 run 元信息语境呈现）。
export const processBaselineSchema = z.object({
  process_pass_rate: z.number(),
  run_id: z.string(),
  run_created_at: z.string(),
}).strict();
export type ProcessBaseline = z.infer<typeof processBaselineSchema>;

export const monitorResourceSummarySchema = z.object({
  resource_kind: resourceKindSchema,
  resource_id: z.string(),
  sample_count: z.number(),
  quality: z.array(qualityDimSchema),
  behavior: behaviorStatsSchema,
  cost: costStatsSchema,
  process: processBaselineSchema.nullable(),
}).strict();
export type MonitorResourceSummary = z.infer<typeof monitorResourceSummarySchema>;

export const monitorWindowSchema = z.object({ from: z.string(), to: z.string() }).strict();
export type MonitorWindow = z.infer<typeof monitorWindowSchema>;

// monitorResourcesPageSchema 端点 1；MVP 不分页，故无 next_cursor/truncated 字段，
// 截断由前端以「items.length 达到 limit」推断（Task 7），schema 不为此添加字段。
export const monitorResourcesPageSchema = z.object({
  items: z.array(monitorResourceSummarySchema),
  window: monitorWindowSchema,
}).strict();
export type MonitorResourcesPage = z.infer<typeof monitorResourcesPageSchema>;

export const monitorTrendPointSchema = z.object({
  bucket_at: z.string(),
  sample_count: z.number(),
  quality: z.array(qualityDimSchema),
  behavior: behaviorStatsSchema,
  cost: costStatsSchema,
}).strict();
export type MonitorTrendPoint = z.infer<typeof monitorTrendPointSchema>;

export const runProcessPointSchema = z.object({
  run_id: z.string(),
  process_pass_rate: z.number(),
  run_created_at: z.string(),
}).strict();
export type RunProcessPoint = z.infer<typeof runProcessPointSchema>;

// monitorTrendSchema 端点 2：series 按日桶；runs 为该资源窗口内 succeeded run 过程基线点。
export const monitorTrendSchema = z.object({
  resource_kind: resourceKindSchema,
  resource_id: z.string(),
  series: z.array(monitorTrendPointSchema),
  runs: z.array(runProcessPointSchema),
}).strict();
export type MonitorTrend = z.infer<typeof monitorTrendSchema>;

export interface MonitorFilters {
  resource_kind?: ResourceKind;
  resource_id?: string;
  /** RFC3339；省略由后端兜底近 7 天。 */
  from?: string;
  /** RFC3339。 */
  to?: string;
  limit?: number;
}
```

- [ ] **Step 2: api 新增 2 方法**

`web/src/modules/evaluation/api/evaluation.api.ts`：把 `evaluationRunSchema` 行上方 import 组里补入新 schema（按字母序就近插入），对象加 2 方法。import 需加入：

```ts
  monitorResourcesPageSchema,
  monitorTrendSchema,
  type MonitorFilters,
  type MonitorResourcesPage,
  type MonitorTrend,
```

对象内（`getTimeline` 之后、`createSuite` 之前）加：

```ts
  listMonitorResources: async (filters?: MonitorFilters): Promise<MonitorResourcesPage> => {
    const response = await api.get('/evaluations/monitoring/resources', filters ? { params: filters } : undefined);
    return monitorResourcesPageSchema.parse(response.data);
  },
  getMonitorTrend: async (filters: MonitorFilters): Promise<MonitorTrend> => {
    const response = await api.get('/evaluations/monitoring/resources/trend', { params: filters });
    return monitorTrendSchema.parse(response.data);
  },
```

- [ ] **Step 3: constants 追加 3 常量**

`web/src/constants/index.ts`，在 `EVALUATION_TREND_RUN_LIMIT = 100` 常量行后追加：

```ts
// 评测监控面板（EvaluationCenterPage「监控」tab）行为边界（spec 2026-09-03 §4.3）：
// 默认窗口天数前端自持（后端 pkg/constants EvalMonitorWindowDays=7 为权威兜底，
// 两端各持默认值并在 UI 明示）；资源行 limit 与后端默认 20 保持一致，用于显式传参
// 并推断「仅显示观测最多的前 N 资源」截断。
export const EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS = 7;
export const EVALUATION_MONITOR_RESOURCE_LIMIT = 20;
// RangePicker 快捷预设（近 N 天含端点）；N 本身即行为数字，禁止散落组件。
export const EVALUATION_MONITOR_WINDOW_PRESETS_DAYS = [7, 14, 30] as const;
```

- [ ] **Step 4: model schema 单测**

在 `web/src/modules/evaluation/model/evaluation.test.ts` 顶部 import 加入：

```ts
  behaviorStatsSchema,
  costStatsSchema,
  monitorResourceSummarySchema,
  monitorResourcesPageSchema,
  monitorTrendSchema,
  processBaselineSchema,
  qualityDimSchema,
  runProcessPointSchema,
```

文件尾部追加 describe（解析 §4.2 样例、null/空态容错、strict 拒未知字段、截断语义=无 next_cursor 仍解析满 limit 行、维度动态数组）：

```ts
describe('evaluation monitor schemas', () => {
  // §4.2 端点 1 样例：quality 仅列实际出现维度；cost 延迟可 null；process 可为对象或 null。
  const summaryRow = {
    resource_kind: 'skill', resource_id: 'skill-a', sample_count: 128,
    quality: [{ dimension: 'faithfulness', pass_rate: 0.92, avg_score: 0.92, avg_confidence: 0.87, samples: 128 }],
    behavior: { rule_hits: 15, retry_count: 3, escalation_count: 1, abandonment_count: 0,
      verdict: { pass: 120, flag: 6, block: 2 } },
    cost: { total_tokens: 154000, total_cost_usd: 0.42, avg_latency_ms: 1800, p95_latency_ms: 5200 },
    process: { process_pass_rate: 0.67, run_id: 'run-9', run_created_at: '2026-09-02T08:00:00Z' },
  };

  it('parses the endpoint 1 resource-row summary with inner behavior and nullable process', () => {
    const page = monitorResourcesPageSchema.parse({ items: [summaryRow], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    expect(page.items[0].quality[0].pass_rate).toBe(0.92);
    expect(page.items[0].behavior.verdict.block).toBe(2);
    expect(page.items[0].cost.p95_latency_ms).toBe(5200);
    expect(page.items[0].process?.run_id).toBe('run-9');
    expect(page.window.from).toBe('2026-08-27T00:00:00Z');
  });

  it('keeps process null when the window has no succeeded run', () => {
    const row = monitorResourceSummarySchema.parse({ ...summaryRow, process: null });
    expect(row.process).toBeNull();
  });

  it('keeps latency null when no latency sample exists', () => {
    const cost = costStatsSchema.parse({ total_tokens: 0, total_cost_usd: 0, avg_latency_ms: null, p95_latency_ms: null });
    expect(cost.avg_latency_ms).toBeNull();
  });

  it('accepts empty quality and empty series/runs as honest empty states', () => {
    expect(monitorResourceSummarySchema.parse({ ...summaryRow, quality: [], process: null }).quality).toEqual([]);
    const trend = monitorTrendSchema.parse({ resource_kind: 'skill', resource_id: 'skill-a', series: [], runs: [] });
    expect(trend.series).toEqual([]);
    expect(trend.runs).toEqual([]);
  });

  it('rejects unknown top-level and nested keys (strict wire contract)', () => {
    expect(() => monitorResourcesPageSchema.parse({ items: [summaryRow], window: { from: 'a', to: 'b' }, next_cursor: 'x' })).toThrow();
    expect(() => behaviorStatsSchema.parse({ rule_hits: 1, retry_count: 0, escalation_count: 0,
      abandonment_count: 0, verdict: { pass: 1, flag: 0, block: 0 }, extra: true })).toThrow();
    expect(() => monitorResourceSummarySchema.parse({ ...summaryRow, resource_kind: 'plugin' })).toThrow();
  });

  it('parses a full-limit row set without a pagination field (truncation is client-inferred)', () => {
    const items = Array.from({ length: 20 }, (_, index) => ({ ...summaryRow, resource_id: `skill-${index}` }));
    const page = monitorResourcesPageSchema.parse({ items, window: { from: 'a', to: 'b' } });
    expect(page.items).toHaveLength(20);
    expect(page.items[0].resource_id).toBe('skill-0');
  });
});
```

- [ ] **Step 5: api 单测**

在 `web/src/modules/evaluation/api/evaluation.api.test.ts` 的 `describe('evaluation center api')` 内新增 2 用例（沿用 `client.get.mockResolvedValue({ data })` 范式；窗口 RFC3339 直传，不做本地换算）：

```ts
  it('lists monitor resources with the window and limit forwarded as query params', async () => {
    const data = { items: [], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } };
    client.get.mockResolvedValue({ data });
    const page = await evaluationApi.listMonitorResources({ resource_kind: 'skill', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z', limit: 20 });
    expect(client.get).toHaveBeenCalledWith('/evaluations/monitoring/resources', {
      params: { resource_kind: 'skill', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z', limit: 20 },
    });
    expect(page.window.from).toBe('2026-08-27T00:00:00Z');
  });

  it('fetches the per-resource trend through the trend endpoint', async () => {
    const data = { resource_kind: 'skill', resource_id: 'skill-a', series: [], runs: [] };
    client.get.mockResolvedValue({ data });
    const trend = await evaluationApi.getMonitorTrend({ resource_kind: 'skill', resource_id: 'skill-a', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' });
    expect(client.get).toHaveBeenCalledWith('/evaluations/monitoring/resources/trend', {
      params: { resource_kind: 'skill', resource_id: 'skill-a', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' },
    });
    expect(trend.runs).toEqual([]);
  });
```

- [ ] **Step 6: 跑单测确认绿**

Run: `cd web && npx vitest run src/modules/evaluation/model/evaluation.test.ts src/modules/evaluation/api/evaluation.api.test.ts`
Expected: PASS（全部用例通过；typecheck 不因 import 顺序报错——eslint `import/order` 的告警按 `make fe-lint` 阶段处理）。

- [ ] **Step 7: Commit**

```bash
git add web/src/modules/evaluation/model/evaluation.ts web/src/modules/evaluation/model/evaluation.test.ts web/src/modules/evaluation/api/evaluation.api.ts web/src/modules/evaluation/api/evaluation.api.test.ts web/src/constants/index.ts
git commit -m "feat(evaluation): 监控面板前端 model/api/常量（spec 2026-09-03 §4.2/§4.3）" -m "zod schema 逐字段对齐端点 JSON：quality 动态数组、process null、cost 延迟 null、runs 空数组均如实表达；listMonitorResources/getMonitorTrend 走共享 client；窗口与 limit 入常量。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: native SVG 多序列折线组件 MultiLineTrendChart（含单测）

下钻抽屉所有趋势图共用的多序列折线组件（spec §5.1「native SVG 多线」、§5.2「多线需区分色 + legend；不做 hover tooltip 的重型图表」）。仓库无 recharts import，沿用 native SVG 约定。**新增通用组件**而非复制 `HealthTrendChart` 多次：`HealthTrendChart` 是单序列 + 通过/未通过形态编码（状态语义），不适合多维度/数值轴；本组件专注「按日桶的多序列折线」，断线/空值语义与 `HealthTrendChart` 的 `lineSegments` 一致（某桶缺某序列则断开，不跨空值连线）。

**Files:**

- Create: `web/src/modules/evaluation/components/MultiLineTrendChart.tsx`
- Test: `web/src/modules/evaluation/components/MultiLineTrendChart.test.tsx`

**Interfaces:**

- Consumes：无（纯展示，数据来自调用方 Task 8 的 `MonitorTrend`）。
- Produces（Task 8 使用，名字必须一致）：
  - `export interface TrendBucket { bucketLabel: string; fullLabel: string }`
  - `export interface MultiLineSeries { name: string; color: string; values: (number | null)[] }`
  - `export const MultiLineTrendChart = ({ buckets, series, unit, ariaLabel, yTickLabel, noDataText, dataTestId }: { buckets: TrendBucket[]; series: MultiLineSeries[]; unit: 'percent' | 'number'; ariaLabel: string; yTickLabel: (value: number) => string; noDataText: string; dataTestId: string })`

- [ ] **Step 1: 写失败测试**

`web/src/modules/evaluation/components/MultiLineTrendChart.test.tsx`（渲染后断言 path/circle 数量与断线行为，复用 `HealthTrendChart.test.tsx` 的 `container.querySelector('svg path[stroke=...]')` 范式）：

```ts
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { MultiLineTrendChart } from './MultiLineTrendChart';
import type { MultiLineSeries, TrendBucket } from './MultiLineTrendChart';

const buckets: TrendBucket[] = ['09-01', '09-02', '09-03'].map((label, i) => ({
  bucketLabel: label,
  fullLabel: `2026-09-0${i + 1}T00:00:00Z`,
}));

const percent = (v: number) => `${Math.round(v * 100)}%`;

describe('MultiLineTrendChart', () => {
  it('renders one colored line per series and a legend', () => {
    const series: MultiLineSeries[] = [
      { name: 'faithfulness', color: '#1677ff', values: [0.9, 0.8, 0.7] },
      { name: 'relevance', color: '#52c41a', values: [0.5, 0.6, null] },
    ];
    const { container } = render(<MultiLineTrendChart buckets={buckets} series={series} unit="percent"
      ariaLabel="质量通过率趋势" yTickLabel={percent} noDataText="无数据" dataTestId="quality-chart" />);

    expect(screen.getByRole('img', { name: '质量通过率趋势' })).toBeInTheDocument();
    expect(container.querySelector('svg path[stroke="#1677ff"]')).not.toBeNull();
    // relevance 在第三桶为 null：前两桶一段连线（M 一次），而非贯穿三桶的整线。
    const green = container.querySelector('svg path[stroke="#52c41a"]');
    expect((green!.getAttribute('d') || '').match(/M/g)).toHaveLength(1);
    expect(screen.getByText('faithfulness')).toBeInTheDocument();
    expect(screen.getByText('relevance')).toBeInTheDocument();
  });

  it('draws one continuous line when no series value is null', () => {
    const series: MultiLineSeries[] = [
      { name: 'faithfulness', color: '#1677ff', values: [0.9, 0.8, 0.7] },
    ];
    const { container } = render(<MultiLineTrendChart buckets={buckets} series={series} unit="percent"
      ariaLabel="质量通过率趋势" yTickLabel={percent} noDataText="无数据" dataTestId="quality-chart" />);
    const line = container.querySelector('svg path[stroke="#1677ff"]');
    expect((line!.getAttribute('d') || '').match(/M/g)).toHaveLength(1);
  });

  it('renders the number unit axis with value-scaled ticks and honors noDataText', () => {
    const series: MultiLineSeries[] = [{ name: 'tokens', color: '#13c2c2', values: [null, null, null] }];
    render(<MultiLineTrendChart buckets={buckets} series={series} unit="number"
      ariaLabel="成本趋势" yTickLabel={(v) => String(Math.round(v))} noDataText="窗口内无该项数值" dataTestId="cost-chart" />);
    expect(screen.getByText('窗口内无该项数值')).toBeInTheDocument();
    // 有值时渲染轴刻度文本（number 轴 0/0.5/1 的若干 tick 之一出现）。
    const values: MultiLineSeries[] = [{ name: 'tokens', color: '#13c2c2', values: [100, 200, 150] }];
    const { container } = render(<MultiLineTrendChart buckets={buckets} series={values} unit="number"
      ariaLabel="成本趋势" yTickLabel={(v) => String(Math.round(v))} noDataText="窗口内无该项数值" dataTestId="cost-chart-2" />);
    expect(container.querySelectorAll('svg path').length).toBeGreaterThan(0);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npx vitest run src/modules/evaluation/components/MultiLineTrendChart.test.tsx`
Expected: FAIL——`Cannot find module './MultiLineTrendChart'`。

- [ ] **Step 3: 实现组件**

`web/src/modules/evaluation/components/MultiLineTrendChart.tsx`（参照 `HealthTrendChart.tsx` 的坐标/网格/图例实现；唯一差异是「多序列 × 每序列断线」与「percent/number 双 y 量纲」。percent 数据值为 [0,1] 通过率，轴固定 0~1；number 数据值为原始量（次数/USD/ms），轴 = 全序列有限最大值，避免误导）：

```ts
// MultiLineTrendChart 渲染按日桶的多序列折线（native SVG，spec 2026-09-03 §5.2）。
// 单图只承载同一 y 量纲的序列：percent（[0,1] 通过率，轴固定）或 number（同单位计数，
// 轴按数据自动取整）。某桶某序列缺值（null）时该序列断开，不跨空值连线——与
// HealthTrendChart 的 lineSegments 语义一致。多序列以颜色区分并配图例，无 hover 重型图表。

export interface TrendBucket {
  /** x 轴刻度短标签（MM-DD）。 */
  bucketLabel: string;
  /** RFC3339，用于 <title> 完整时间。 */
  fullLabel: string;
}

export interface MultiLineSeries {
  name: string;
  color: string;
  /** 与 buckets 等长；null 表示该桶缺该序列（断线）。 */
  values: (number | null)[];
}

export interface MultiLineTrendChartProps {
  buckets: TrendBucket[];
  series: MultiLineSeries[];
  /** percent：数据为 [0,1] 通过率，y 固定 0..1；number：数据为同单位原始值，y 自动取整。 */
  unit: 'percent' | 'number';
  ariaLabel: string;
  /** 轴刻度文本格式化（接收「刻度对应的数据值」：percent 为 0..1 比例，number 为绝对数）。 */
  yTickLabel: (value: number) => string;
  /** 全序列无任何有效值时展示的空态文案（诚实表达，不画 0 轴假图）。 */
  noDataText: string;
  dataTestId: string;
}

const WIDTH = 640;
const HEIGHT = 220;
const MARGIN = { top: 14, right: 16, bottom: 30, left: 46 };
const PLOT_WIDTH = WIDTH - MARGIN.left - MARGIN.right;
const PLOT_HEIGHT = HEIGHT - MARGIN.top - MARGIN.bottom;
const MAX_TICK_LABELS = 6;
const GRID_RATIOS = [0, 0.25, 0.5, 0.75, 1];
const gridColor = '#e5e5e5';
const textColor = '#6b6b6b';

const xOf = (i: number, n: number) => (n === 1 ? MARGIN.left + PLOT_WIDTH / 2 : MARGIN.left + (i / (n - 1)) * PLOT_WIDTH);
const yOf = (ratio: number) => MARGIN.top + (1 - ratio) * PLOT_HEIGHT;

function tickSubset(n: number): number[] {
  if (n <= MAX_TICK_LABELS) {
    return Array.from({ length: n }, (_, i) => i);
  }
  const step = (n - 1) / (MAX_TICK_LABELS - 1);
  return Array.from({ length: MAX_TICK_LABELS }, (_, i) => Math.round(i * step));
}

// segmentPath 把一条序列的值映射为折线 path：null 处断开。
function segmentPath(values: (number | null)[], scale: number): string | null {
  const parts: string[] = [];
  let pen: string | null = null;
  for (let i = 0; i < values.length; i += 1) {
    const value = values[i];
    if (value === null) { pen = null; continue; }
    const ratio = Math.max(0, Math.min(1, value / scale));
    const command = `${xOf(i, values.length).toFixed(1)} ${yOf(ratio).toFixed(1)}`;
    parts.push(pen === null ? `M${command}` : `${pen}L${command}`);
    pen = '';
  }
  return parts.length ? parts.join(' ') : null;
}

export const MultiLineTrendChart = ({ buckets, series, unit, ariaLabel, yTickLabel, noDataText, dataTestId }: MultiLineTrendChartProps) => {
  const n = buckets.length;
  const finiteValues = series.flatMap((s) => s.values).filter((v): v is number => v !== null);
  // percent 轴固定 [0,1]；number 轴取全序列有限最大值（0 兜底避免除零）。
  const axisMax = unit === 'percent' ? 1 : Math.max(1, ...finiteValues);
  const hasData = finiteValues.length > 0;
  const ticks = tickSubset(n);

  if (!hasData) {
    return <div data-testid={dataTestId}>{noDataText}</div>;
  }

  return (
    <div data-testid={dataTestId}>
      <svg width={WIDTH} height={HEIGHT} viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label={ariaLabel}
        style={{ width: '100%', height: 'auto', display: 'block' }}>
        {GRID_RATIOS.map((ratio) => (
          <g key={ratio}>
            <line x1={MARGIN.left} x2={WIDTH - MARGIN.right} y1={yOf(ratio)} y2={yOf(ratio)} stroke={gridColor} strokeWidth={1} />
            <text x={MARGIN.left - 6} y={yOf(ratio) + 4} textAnchor="end" fontSize={11} fill={textColor}>
              {yTickLabel(axisMax * ratio)}
            </text>
          </g>
        ))}
        {series.map((s) => {
          const path = segmentPath(s.values, axisMax);
          return path
            ? <path key={s.name} d={path} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" />
            : null;
        })}
        {buckets.map((bucket, i) => (
          <g key={bucket.fullLabel}>
            {series.map((s) => {
              const value = s.values[i];
              if (value === null) return null;
              const ratio = Math.max(0, Math.min(1, value / axisMax));
              return <circle key={s.name} cx={xOf(i, n)} cy={yOf(ratio)} r={3} fill={s.color} stroke="#fff" strokeWidth={1} />;
            })}
          </g>
        ))}
        {ticks.map((i) => (
          <text key={`${buckets[i].fullLabel}-tick`} x={xOf(i, n)} y={HEIGHT - MARGIN.bottom + 16} textAnchor="middle"
            fontSize={10} fill={textColor}>{buckets[i].bucketLabel}</text>
        ))}
      </svg>
      <div style={{ marginTop: 6, fontSize: 12, color: textColor }}>
        {series.map((s) => (
          <span key={s.name} style={{ marginRight: 14 }}>
            <svg width={10} height={10} style={{ verticalAlign: -1, marginRight: 4 }}>
              <circle cx={5} cy={5} r={4} fill={s.color} />
            </svg>
            {s.name}
          </span>
        ))}
      </div>
    </div>
  );
};
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd web && npx vitest run src/modules/evaluation/components/MultiLineTrendChart.test.tsx`
Expected: PASS（3 用例）。

- [ ] **Step 5: Commit**

```bash
git add web/src/modules/evaluation/components/MultiLineTrendChart.tsx web/src/modules/evaluation/components/MultiLineTrendChart.test.tsx
git commit -m "feat(evaluation): 监控多序列 native SVG 折线组件（spec 2026-09-03 §5.2）" -m "MultiLineTrendChart 通用多线组件：percent 轴固定 0-1 / number 轴按数据取整，null 断线、图例、noDataText 空态；下钻抽屉所有趋势图复用。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

### Task 7: EvaluationMonitorPanel（监控 tab 主体组件 + 组件测试）

评测中心「监控」tab 的视图主体（spec §5.1/§5.2）：租户级资源行主表 × 点击行下钻单资源趋势。形态与 `RuntimeHealthTrendPanel` 对齐（cancelled-flag effect、error+retry tick、截断提示），差异在数据线：观测线聚合（`eval_observations`）为质量×行为×成本，评测线（`eval_runs` 最近 succeeded run）补过程基线；时间窗由 antd `RangePicker` 承载（默认近 7 天，preset 快捷改窗）。表无分页，`rows.length === EVALUATION_MONITOR_RESOURCE_LIMIT` 时展示截断 banner（诚实，从不隐藏，见 spec §7.4）。

**Files:**

- Create: `web/src/modules/evaluation/components/EvaluationMonitorPanel.tsx`
- Test: `web/src/modules/evaluation/components/EvaluationMonitorPanel.test.tsx`

**Interfaces:**

- Consumes：
  - Task 5：`evaluationApi.listMonitorResources(filters?: MonitorFilters)`；`MonitorResourceSummary`；`MonitorFilters`（`resource_kind?/resource_id?/from?/to?/limit?`，from/to RFC3339）；常量 `EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS`/`EVALUATION_MONITOR_RESOURCE_LIMIT`/`EVALUATION_MONITOR_WINDOW_PRESETS_DAYS`。
  - `displayLabel`（`evaluationView.tsx`）、`extractErrorMessage`（`@/shared/lib`）。
  - dayjs（antd 传递依赖 npm-hoist 顶层，与 `AuditEventsPage` 的 dayjs 用法一致）。
- Produces（Task 9 使用，名字必须一致）：
  - `export const EvaluationMonitorPanel = ({ defaultKind, defaultResourceId, isMobile }: { defaultKind?: ResourceKind; defaultResourceId?: string; isMobile?: boolean })`。
  - 内部点击行 `setOpenRow(row)` 挂载 Task 8 `MonitorResourceDrawer`，传入 `resource/from/to/isMobile/onClose`。
  - 根节点 `data-testid="evaluation-monitor-panel"`。

- [ ] **Step 0: 确认 node_modules**

Task 5 Step 0 若未在本 worktree 执行过，先 `cd web && npm ci`。

- [ ] **Step 1: 写失败测试**

`web/src/modules/evaluation/components/EvaluationMonitorPanel.test.tsx`。RangePicker 改窗用 **antd preset 点击**驱动（确定性优于向日期输入框逐字键入；preset label 渲染在 popup portal，`screen.findByText('近 14 天')` 可命中——本仓库测试仅覆盖 Select popup 先例，DatePicker popup 同 rc 机制）。时间断言用相对值（`from` 变早、`to` 不变），避免依赖测试宿主 TZ/当前时刻：

```ts
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { EvaluationMonitorPanel } from './EvaluationMonitorPanel';
import type { MonitorResourceSummary } from '../model/evaluation';

import { EVALUATION_MONITOR_RESOURCE_LIMIT } from '@/constants';

const mocks = vi.hoisted(() => ({ listMonitorResources: vi.fn(), getMonitorTrend: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({
  evaluationApi: { listMonitorResources: mocks.listMonitorResources, getMonitorTrend: mocks.getMonitorTrend },
}));

const rowA: MonitorResourceSummary = {
  resource_kind: 'skill', resource_id: 'skill-a', sample_count: 128,
  quality: [{ dimension: 'faithfulness', pass_rate: 0.92, avg_score: 0.92, avg_confidence: 0.87, samples: 128 }],
  behavior: { rule_hits: 15, retry_count: 3, escalation_count: 1, abandonment_count: 0,
    verdict: { pass: 120, flag: 6, block: 2 } },
  cost: { total_tokens: 154000, total_cost_usd: 0.42, avg_latency_ms: 1800, p95_latency_ms: 5200 },
  process: { process_pass_rate: 0.67, run_id: 'run-9', run_created_at: '2026-09-02T08:00:00Z' },
};
const rowB: MonitorResourceSummary = {
  resource_kind: 'agent', resource_id: 'agent-x', sample_count: 3, quality: [],
  behavior: { rule_hits: 0, retry_count: 0, escalation_count: 0, abandonment_count: 0,
    verdict: { pass: 3, flag: 0, block: 0 } },
  cost: { total_tokens: 900, total_cost_usd: 0.003, avg_latency_ms: null, p95_latency_ms: null },
  process: null,
};
const emptyPage = { items: [], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } };

describe('EvaluationMonitorPanel', () => {
  beforeEach(() => { mocks.listMonitorResources.mockReset(); mocks.getMonitorTrend.mockReset(); });

  it('loads the top resource rows scoped by the default kind and resource', async () => {
    mocks.listMonitorResources.mockResolvedValue({ items: [rowA, rowB], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" defaultResourceId="skill-a" />);
    expect(await screen.findByText('技能 skill-a')).toBeInTheDocument();
    expect(mocks.listMonitorResources).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'skill', resource_id: 'skill-a', limit: EVALUATION_MONITOR_RESOURCE_LIMIT,
      from: expect.any(String), to: expect.any(String),
    }));
    // 过程/成本/判定列如实呈现：process null → 「—」，不假装 0%。「—」在空 quality/process
    // 等单元格可多次出现，用 getAllByText 断言存在（不得 getByText 期望唯一）。
    expect(screen.getByText('通过 120 / 待复核 6 / 阻断 2')).toBeInTheDocument();
    expect(screen.getByText('faithfulness 92%')).toBeInTheDocument();
    expect(screen.getByText(/1800ms/)).toBeInTheDocument();
    expect(screen.getByText('67%')).toBeInTheDocument();
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });

  it('opens the per-resource drawer and refetches its trend on row click', async () => {
    mocks.listMonitorResources.mockResolvedValue({ items: [rowA], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    mocks.getMonitorTrend.mockResolvedValue({ resource_kind: 'skill', resource_id: 'skill-a', series: [], runs: [] });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    fireEvent.click(await screen.findByText('技能 skill-a'));
    await waitFor(() => expect(mocks.getMonitorTrend).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'skill', resource_id: 'skill-a',
    })));
    expect(await screen.findByText('质量趋势')).toBeInTheDocument();
  });

  it('shows an honest empty state when the window has no observation', async () => {
    mocks.listMonitorResources.mockResolvedValue(emptyPage);
    render(<EvaluationMonitorPanel />);
    expect(await screen.findByText('该时间窗口内暂无评测观测样本')).toBeInTheDocument();
  });

  it('renders the error alert and refetches on retry', async () => {
    mocks.listMonitorResources.mockRejectedValueOnce(new Error('加载监控数据失败'));
    mocks.listMonitorResources.mockResolvedValueOnce({ items: [rowA], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    fireEvent.click(await screen.findByRole('button', { name: '重试' }));
    expect(await screen.findByText('技能 skill-a')).toBeInTheDocument();
    expect(mocks.listMonitorResources).toHaveBeenCalledTimes(2);
  });

  it('marks the row list truncated when the limit is reached', async () => {
    const items = Array.from({ length: EVALUATION_MONITOR_RESOURCE_LIMIT }, (_, index) => ({
      ...rowA, resource_id: `skill-${index}` }));
    mocks.listMonitorResources.mockResolvedValue({ items, window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    expect(await screen.findByText(/仅显示观测最多的前 20 个资源/)).toBeInTheDocument();
  });

  it('refetches with an earlier window when a RangePicker preset is chosen', async () => {
    mocks.listMonitorResources.mockResolvedValue({ items: [rowA], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    await screen.findByText('技能 skill-a');
    const firstCall = mocks.listMonitorResources.mock.calls[0][0] as { from: string; to: string };

    const panel = screen.getByTestId('evaluation-monitor-panel');
    const input = panel.querySelector('.ant-picker input');
    expect(input).not.toBeNull();
    fireEvent.mouseDown(input as Element);
    fireEvent.click(await screen.findByText('近 14 天'));

    await waitFor(() => expect(mocks.listMonitorResources).toHaveBeenCalledTimes(2));
    const secondCall = mocks.listMonitorResources.mock.calls[1][0] as { from: string; to: string };
    // 「近 14 天」窗口起点更早、终点与近 7 天同（同为今天），避免依赖宿主 TZ 断言具体值。
    expect(new Date(secondCall.from).getTime()).toBeLessThan(new Date(firstCall.from).getTime());
    expect(secondCall.to).toBe(firstCall.to);
    expect(secondCall.limit).toBe(EVALUATION_MONITOR_RESOURCE_LIMIT);
  });
});
```

> **RangePicker 弹层脆弱点备选**：若 jsdom 下 `mouseDown` 未展开面板（rc-picker 弹层可见性依赖 focus），先对同 input `fireEvent.focus` 再 `mouseDown`；若 preset 文案匹配抖动，改按 `.ant-picker-preset` 类选择器内文本匹配。本实现以 preset 点击为准，因它走与 Select option 相同的 portal + 文本命中路径。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npx vitest run src/modules/evaluation/components/EvaluationMonitorPanel.test.tsx`
Expected: FAIL——`Cannot find module './EvaluationMonitorPanel'`。

- [ ] **Step 3: 实现面板组件**

`web/src/modules/evaluation/components/EvaluationMonitorPanel.tsx`。表格列为资源/样本/质量/行为/判定/成本/过程；质量列按观测实际出现维度渲染 Tag（dimension 用 `displayLabel`，未定义维度回落原文）；过程列 `process === null` → `—`；表上方截断提示只在该窗口资源行数达 limit 时出现。**行为数字均来自常量，禁止内联 7/20**：

```ts
// EvaluationMonitorPanel 「评测指标监控」视图（spec 2026-09-03 §5.2）。
// 数据线：观测线 eval_observations（质量/行为/成本聚合）+ 评测线 eval_runs 最近
// succeeded run（过程基线，process 为 null 表示窗口内无离线评测）。表按样本数
// 降序只展示观测最多的资源（limit 截断用 banner 显式标注，从不暗示全量）。
// 时间窗含端点（后端 >= from AND <= to），前端 to 取 endOf('day') 保证含整天。
import { Alert, Button, DatePicker, Empty, Flex, Select, Skeleton, Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import type { Dayjs } from 'dayjs';
import { useEffect, useMemo, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { MonitorFilters, MonitorResourceSummary, ResourceKind } from '../model/evaluation';
import { displayLabel } from './evaluationView';
import { MonitorResourceDrawer } from './MonitorResourceDrawer';

import {
  EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS, EVALUATION_MONITOR_RESOURCE_LIMIT,
  EVALUATION_MONITOR_WINDOW_PRESETS_DAYS,
} from '@/constants';
import { extractErrorMessage } from '@/shared/lib';

const { RangePicker } = DatePicker;

const kindOptions = ['skill', 'agent', 'mcp', 'knowledge'].map((value) => ({ value, label: displayLabel(value) }));
type WindowRange = [Dayjs, Dayjs];

const windowPresets = EVALUATION_MONITOR_WINDOW_PRESETS_DAYS.map((days) => ({
  label: `近 ${days} 天`,
  value: [dayjs().subtract(days - 1, 'day').startOf('day'), dayjs().endOf('day')] as WindowRange,
}));

const defaultWindowRange = (): WindowRange => [
  dayjs().subtract(EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS - 1, 'day').startOf('day'),
  dayjs().endOf('day'),
];

// isoWindow 归一化到整日起点/终点：与后端含端点窗口一致（RFC3339）。
function isoWindow(range: WindowRange): { from: string; to: string } {
  return { from: range[0].startOf('day').toISOString(), to: range[1].endOf('day').toISOString() };
}

const joinNonEmpty = (parts: string[]) => parts.filter(Boolean).join(' · ');

export const EvaluationMonitorPanel = ({ defaultKind, defaultResourceId, isMobile }: {
  defaultKind?: ResourceKind; defaultResourceId?: string; isMobile?: boolean;
}) => {
  const [kind, setKind] = useState<ResourceKind | undefined>(defaultKind);
  const [resourceId, setResourceId] = useState(defaultResourceId ?? '');
  const [range, setRange] = useState<WindowRange>(defaultWindowRange);
  const [rows, setRows] = useState<MonitorResourceSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // 重试计数：失败 Alert 的「重试」递增以重新触发拉取 effect。
  const [tick, setTick] = useState(0);
  const [openRow, setOpenRow] = useState<MonitorResourceSummary | null>(null);
  const window = useMemo(() => isoWindow(range), [range]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    const filters: MonitorFilters = {
      ...(kind ? { resource_kind: kind } : {}),
      ...(resourceId ? { resource_id: resourceId } : {}),
      from: window.from,
      to: window.to,
      limit: EVALUATION_MONITOR_RESOURCE_LIMIT,
    };
    evaluationApi.listMonitorResources(filters)
      .then((page) => { if (!cancelled) setRows(page.items); })
      .catch((err) => { if (!cancelled) setError(extractErrorMessage(err) || '加载监控数据失败'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [kind, resourceId, window.from, window.to, tick]);

  // 端点 1 无 total：limit 条即认为可能截断（spec §7.4），banner 显式说明按样本数排序。
  const truncated = rows.length >= EVALUATION_MONITOR_RESOURCE_LIMIT;
  const scoped = Boolean(kind || resourceId);

  const columns: ColumnsType<MonitorResourceSummary> = [
    { title: '资源', key: 'resource', render: (_, row) => `${displayLabel(row.resource_kind)} ${row.resource_id}` },
    { title: '样本', dataIndex: 'sample_count', width: 80, align: 'right' },
    { title: '质量通过率', dataIndex: 'quality', render: (value: MonitorResourceSummary['quality']) => value.length
      ? value.map((dim) => (
        <Tag key={dim.dimension} style={{ marginInlineEnd: 4 }}>{`${displayLabel(dim.dimension)} ${Math.round(dim.pass_rate * 100)}%`}</Tag>))
      : '—' },
    { title: '行为', key: 'behavior', render: (_, row) => joinNonEmpty([
      `规则命中 ${row.behavior.rule_hits}`,
      row.behavior.retry_count ? `重试 ${row.behavior.retry_count}` : '',
      row.behavior.escalation_count ? `升级 ${row.behavior.escalation_count}` : '',
      row.behavior.abandonment_count ? `放弃 ${row.behavior.abandonment_count}` : '',
    ]) },
    { title: '判定', key: 'verdict', render: (_, row) => {
      const verdict = row.behavior.verdict;
      return `通过 ${verdict.pass} / 待复核 ${verdict.flag} / 阻断 ${verdict.block}`;
    } },
    { title: '成本', key: 'cost', render: (_, row) => {
      const latency = row.cost.avg_latency_ms === null ? '—' : `${Math.round(row.cost.avg_latency_ms)}ms`;
      return joinNonEmpty([latency, `$${row.cost.total_cost_usd.toFixed(2)}`]);
    } },
    { title: '过程通过率', key: 'process', render: (_, row) => row.process
      ? `${Math.round(row.process.process_pass_rate * 100)}%` : '—' },
  ];

  return (
    <div data-testid="evaluation-monitor-panel">
      <Space wrap style={{ marginBottom: 12 }}>
        <RangePicker
          value={range}
          presets={windowPresets}
          allowClear={false}
          format="YYYY-MM-DD"
          onChange={(next) => { if (next && next[0] && next[1]) setRange([next[0].startOf('day'), next[1].endOf('day')]); }}
        />
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindOptions}
          value={kind} onChange={(value: ResourceKind | undefined) => { setKind(value); setResourceId(''); }} />
      </Space>
      {error
        ? <Alert type="error" showIcon message={error} action={<Button size="small" onClick={() => setTick((value) => value + 1)}>重试</Button>} />
        : rows.length === 0
          ? (loading ? <Skeleton active /> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={scoped ? '所选范围在本窗口内暂无评测观测样本' : '该时间窗口内暂无评测观测样本'} />)
          : <>
            {truncated && <Typography.Text type="warning" style={{ display: 'block', marginBottom: 12 }}>
              仅显示观测最多的前 {EVALUATION_MONITOR_RESOURCE_LIMIT} 个资源（按样本数排序）
            </Typography.Text>}
            <Table<MonitorResourceSummary> size="small" rowKey={(row) => `${row.resource_kind}:${row.resource_id}`}
              dataSource={rows} columns={columns} pagination={false} loading={loading}
              onRow={(row) => ({ onClick: () => setOpenRow(row), style: { cursor: 'pointer' } })} />
            {openRow && <MonitorResourceDrawer resource={openRow} open from={window.from} to={window.to}
              isMobile={isMobile} onClose={() => setOpenRow(null)} />}
          </>}
    </div>
  );
};
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd web && npx vitest run src/modules/evaluation/components/EvaluationMonitorPanel.test.tsx`
Expected: PASS。若第 6 用例 RangePicker 面板未展开，按 Step 1 备选加 `fireEvent.focus` 后再 `mouseDown`，重跑至 PASS。

- [ ] **Step 5: Commit**

```bash
git add web/src/modules/evaluation/components/EvaluationMonitorPanel.tsx web/src/modules/evaluation/components/EvaluationMonitorPanel.test.tsx
git commit -m "feat(evaluation): 监控面板主表组件（spec 2026-09-03 §5.2）" -m "EvaluationMonitorPanel 资源行主表：RangePicker 默认近 7 天 preset 改窗、kind 过滤、limit 截断 banner、行点击下钻 Drawer；空态诚实（无观测/process null）均如实呈现。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

### Task 8: MonitorResourceDrawer（单资源趋势下钻抽屉 + 组件测试）

点选资源行后的下钻（spec §5.1 MonitorResourceDrawer、§5.2「单资源时间趋势」）。复用 Task 6 `MultiLineTrendChart` 呈现四类趋势：质量 % 多线、行为计数多线、延迟/成本数值线；过程基线以 runs 列表呈现（含空态与恒 1.0 诚实注明）。所有日桶标签按 **UTC** 生成——后端 `bucket_at` 是 UTC 日零点，用本地时区格式化会在负偏移时区把桶标到前一天，破坏对齐（`HealthTrendChart` 展示的是事件时刻、非按日聚合，不受此约束）。延迟缺样本（null）跨日断线，成本/行为用 MultiLineTrendChart 自身 noData 空态兜底。

**Files:**

- Create: `web/src/modules/evaluation/components/MonitorResourceDrawer.tsx`
- Test: `web/src/modules/evaluation/components/MonitorResourceDrawer.test.tsx`

**Interfaces:**

- Consumes：
  - Task 5：`evaluationApi.getMonitorTrend(filters: MonitorFilters)`；`MonitorResourceSummary`、`MonitorTrend`（含 `RunProcessPoint`）；`MonitorFilters`。
  - Task 6：`MultiLineTrendChart`、`TrendBucket`、`MultiLineSeries`。
  - `displayLabel`、`drawerWidth`（`evaluationView.tsx`）、`extractErrorMessage`。
- Produces（Task 7 使用，名字必须一致）：
  - `export const MonitorResourceDrawer = ({ resource, open, from, to, isMobile, onClose }: { resource: MonitorResourceSummary; open: boolean; from: string; to: string; isMobile?: boolean; onClose: () => void })`
  - 根 Drawer `title={`${displayLabel(resource.resource_kind)} ${resource.resource_id} · 评测观测趋势`}`、`width={drawerWidth(isMobile)}`、`destroyOnHidden`。
  - 图表容器 `data-testid`：`monitor-quality-chart` / `monitor-behavior-chart` / `monitor-latency-chart` / `monitor-cost-chart`；过程区 `data-testid="monitor-process-runs"`。

- [ ] **Step 1: 写失败测试**

`web/src/modules/evaluation/components/MonitorResourceDrawer.test.tsx`：

```ts
import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { MonitorResourceDrawer } from './MonitorResourceDrawer';
import type { MonitorResourceSummary, MonitorTrend } from '../model/evaluation';

const mocks = vi.hoisted(() => ({ getMonitorTrend: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({
  evaluationApi: { getMonitorTrend: mocks.getMonitorTrend },
}));

const resource: MonitorResourceSummary = {
  resource_kind: 'skill', resource_id: 'skill-a', sample_count: 4, quality: [],
  behavior: { rule_hits: 3, retry_count: 1, escalation_count: 0, abandonment_count: 0,
    verdict: { pass: 2, flag: 2, block: 0 } },
  cost: { total_tokens: 12000, total_cost_usd: 0.03, avg_latency_ms: 900, p95_latency_ms: 1200 },
  process: { process_pass_rate: 0.67, run_id: 'run-9', run_created_at: '2026-09-01T08:00:00Z' },
};

// trendTwo 两条日桶：quality 仅出现在观测样本的维度；day2 无延迟样本（avg/p95 null，
// 折线断开而非跨空连点）；token/成本为每日合计，跨桶求和得窗口总量。
const trendTwo: MonitorTrend = {
  resource_kind: 'skill', resource_id: 'skill-a',
  series: [
    { bucket_at: '2026-09-01T00:00:00Z', sample_count: 3,
      quality: [{ dimension: 'faithfulness', pass_rate: 0.92, avg_score: 0.92, avg_confidence: 0.87, samples: 3 }],
      behavior: { rule_hits: 2, retry_count: 1, escalation_count: 0, abandonment_count: 0,
        verdict: { pass: 2, flag: 1, block: 0 } },
      cost: { total_tokens: 10000, total_cost_usd: 0.02, avg_latency_ms: 900, p95_latency_ms: 1200 } },
    { bucket_at: '2026-09-02T00:00:00Z', sample_count: 1,
      quality: [{ dimension: 'faithfulness', pass_rate: 0.67, avg_score: 0.67, avg_confidence: 0.6, samples: 1 },
        { dimension: 'relevance', pass_rate: 1, avg_score: 1, avg_confidence: 0.9, samples: 1 }],
      behavior: { rule_hits: 1, retry_count: 0, escalation_count: 0, abandonment_count: 0,
        verdict: { pass: 0, flag: 1, block: 0 } },
      cost: { total_tokens: 2000, total_cost_usd: 0.01, avg_latency_ms: null, p95_latency_ms: null } },
  ],
  runs: [{ run_id: 'run-9', process_pass_rate: 0.67, run_created_at: '2026-09-01T08:00:00Z' }],
};

const window = { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' };

describe('MonitorResourceDrawer', () => {
  beforeEach(() => { mocks.getMonitorTrend.mockReset(); });

  it('fetches the trend for the resource window and renders summary + section charts', async () => {
    mocks.getMonitorTrend.mockResolvedValue(trendTwo);
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);

    expect(await screen.findByText('质量趋势')).toBeInTheDocument();
    // 单资源下钻携带 kind/id 与面板同窗 from/to。
    expect(mocks.getMonitorTrend).toHaveBeenCalledWith({
      resource_kind: 'skill', resource_id: 'skill-a', from: window.from, to: window.to,
    });
    // 汇总：跨桶求和（sample 3+1、token 10000+2000、usd 0.02+0.01）。
    expect(screen.getByText('总样本 4')).toBeInTheDocument();
    expect(screen.getByText('总成本 $0.03')).toBeInTheDocument();
    // 质量多线按实际维度成线（faithfulness/relevance 图例），null 延迟当日不断 quality 线。
    expect(screen.getByText('faithfulness')).toBeInTheDocument();
    expect(screen.getByText('relevance')).toBeInTheDocument();
    expect(screen.getByText('行为趋势')).toBeInTheDocument();
    expect(screen.getByText('成本趋势')).toBeInTheDocument();
    // 过程基线列出窗口内 succeeded run，process 0.67 不触发恒 1.0 注记。
    expect(screen.getByText('run-9')).toBeInTheDocument();
    expect(screen.getByText('67%')).toBeInTheDocument();
    expect(screen.queryByText(/恒为 1.0/)).not.toBeInTheDocument();
  });

  it('breaks latency lines on null days and renders an honest empty state when all latency is null', async () => {
    mocks.getMonitorTrend.mockResolvedValue({
      ...trendTwo,
      series: trendTwo.series.map((point) => ({ ...point,
        cost: { ...point.cost, avg_latency_ms: null, p95_latency_ms: null } })),
    });
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText('窗口内无延迟样本')).toBeInTheDocument();
  });

  it('shows the process empty state and the always-1.0 caveat for a run without process assertions', async () => {
    mocks.getMonitorTrend.mockResolvedValue({
      ...trendTwo,
      runs: [{ run_id: 'run-fn', process_pass_rate: 1, run_created_at: '2026-09-01T08:00:00Z' }],
    });
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText(/恒为 1.0/)).toBeInTheDocument();
  });

  it('shows the process empty state when the window has no succeeded run', async () => {
    mocks.getMonitorTrend.mockResolvedValue({ ...trendTwo, runs: [] });
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText('该时间窗口内无离线评测过程基线')).toBeInTheDocument();
  });

  it('keeps previous content hidden while closed', async () => {
    mocks.getMonitorTrend.mockResolvedValue(trendTwo);
    const { rerender } = render(<MonitorResourceDrawer resource={resource} open={false}
      from={window.from} to={window.to} onClose={() => {}} />);
    expect(screen.queryByText('质量趋势')).not.toBeInTheDocument();
    expect(mocks.getMonitorTrend).not.toHaveBeenCalled();
    rerender(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText('质量趋势')).toBeInTheDocument();
    await waitFor(() => expect(mocks.getMonitorTrend).toHaveBeenCalledTimes(1));
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npx vitest run src/modules/evaluation/components/MonitorResourceDrawer.test.tsx`
Expected: FAIL——`Cannot find module './MonitorResourceDrawer'`。

- [ ] **Step 3: 实现抽屉**

`web/src/modules/evaluation/components/MonitorResourceDrawer.tsx`。单一职责拆分：主组件只负责「开窗→fetch→渲染标题/汇总」，图表通过一个纯展示 `SeriesChartSection` 收敛（有任一非 null 值走图表、否则 Empty），过程区独立 `ProcessRunsSection`，保持各函数在门禁长度内：

```ts
// MonitorResourceDrawer 单资源时间趋势下钻（spec 2026-09-03 §5.2）。
// 数据来自 getMonitorTrend：series 按 UTC 日桶，runs 为该资源窗口内 succeeded 评测 run。
// 质量/行为/成本/延迟折线图若某日无该序列样本则以 null 断线（不跨空连线）；
// 全窗口无该项数值时用 noData 文案诚实空态。run 无过程断言用例时通过率恒为 1.0，
// 列出 run 时显式注记，避免被误读为「过程完美」。
import { Alert, Button, Drawer, Empty, Flex, Spin, Table, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { MonitorResourceSummary, MonitorTrend, RunProcessPoint } from '../model/evaluation';
import { displayLabel, drawerWidth } from './evaluationView';
import { MultiLineTrendChart } from './MultiLineTrendChart';
import type { MultiLineSeries, TrendBucket } from './MultiLineTrendChart';

import { extractErrorMessage } from '@/shared/lib';

// bucketLabel 与后端 date_trunc('day' AT TIME ZONE 'UTC') 的日界一致：bucket_at 为 UTC 零点。
function bucketLabel(iso: string): string {
  const date = new Date(iso);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
}

const canonicalDims = ['faithfulness', 'relevance', 'completeness'];
// qualityDimOrder 固定 judge 三维优先、其余按首现顺序，保证多日维度顺序稳定。
function qualityDimOrder(names: string[]): string[] {
  const known = canonicalDims.filter((name) => names.includes(name));
  return [...known, ...names.filter((name) => !canonicalDims.includes(name))];
}

const colorOf = (index: number) => ['#1677ff', '#52c41a', '#722ed1', '#fa8c16', '#13c2c2', '#eb2f96'][index % 6];
const percentTick = (value: number) => `${Math.round(value * 100)}%`;
const countTick = (value: number) => String(Math.round(value));
const msTick = (value: number) => `${Math.round(value)}ms`;
const usdTick = (value: number) => `$${value.toFixed(2)}`;

function SeriesChartSection({ title, buckets, series, unit, tick, noDataText, dataTestId }: {
  title: string; buckets: TrendBucket[]; series: MultiLineSeries[]; unit: 'percent' | 'number';
  tick: (value: number) => string; noDataText: string; dataTestId: string;
}) {
  const hasValues = series.some((item) => item.values.some((value) => value !== null));
  return (
    <div>
      <Typography.Title level={5} style={{ marginTop: 0 }}>{title}</Typography.Title>
      {hasValues
        ? <MultiLineTrendChart buckets={buckets} series={series} unit={unit} ariaLabel={title}
          yTickLabel={tick} noDataText={noDataText} dataTestId={dataTestId} />
        : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={noDataText} />}
    </div>
  );
}

const runColumns: ColumnsType<RunProcessPoint> = [
  { title: '运行 ID', dataIndex: 'run_id', ellipsis: true },
  { title: '运行时间', dataIndex: 'run_created_at', width: 170,
    render: (value: string) => new Date(value).toLocaleString('zh-CN') },
  { title: '过程通过率', dataIndex: 'process_pass_rate', width: 110,
    render: (value: number) => `${Math.round(value * 100)}%` },
];

function ProcessRunsSection({ runs }: { runs: MonitorTrend['runs'] }) {
  const hasConstant = runs.some((run) => run.process_pass_rate === 1);
  return (
    <div data-testid="monitor-process-runs">
      <Typography.Title level={5}>过程基线</Typography.Title>
      {hasConstant && <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        注：无过程断言用例的 run，过程通过率恒为 1.0
      </Typography.Paragraph>}
      {runs.length === 0
        ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该时间窗口内无离线评测过程基线" />
        : <Table<RunProcessPoint> size="small" rowKey="run_id" dataSource={runs} columns={runColumns} pagination={false} />}
    </div>
  );
}

export const MonitorResourceDrawer = ({ resource, open, from, to, isMobile, onClose }: {
  resource: MonitorResourceSummary; open: boolean; from: string; to: string; isMobile?: boolean; onClose: () => void;
}) => {
  const [trend, setTrend] = useState<MonitorTrend | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setTrend(null);
    setLoading(true);
    setError('');
    evaluationApi.getMonitorTrend({ resource_kind: resource.resource_kind, resource_id: resource.resource_id, from, to })
      .then((data) => { if (!cancelled) setTrend(data); })
      .catch((err) => { if (!cancelled) setError(extractErrorMessage(err) || '加载趋势失败'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [open, resource, from, to, tick]);

  const series = trend?.series ?? [];
  const runs = trend?.runs ?? [];
  const buckets: TrendBucket[] = series.map((point) => ({
    bucketLabel: bucketLabel(point.bucket_at), fullLabel: point.bucket_at,
  }));
  const totals = series.reduce((acc, point) => ({
    samples: acc.samples + point.sample_count,
    tokens: acc.tokens + point.cost.total_tokens,
    cost: acc.cost + point.cost.total_cost_usd,
  }), { samples: 0, tokens: 0, cost: 0 });

  // 质量：仅对实际出现的维度成线；每桶缺该维度样本 → null（断线），不补齐。
  const dims = qualityDimOrder([...new Set(series.flatMap((point) => point.quality.map((q) => q.dimension)))]);
  const qualitySeries: MultiLineSeries[] = dims.map((dim, index) => ({
    name: displayLabel(dim), color: colorOf(index),
    values: series.map((point) => {
      const hit = point.quality.find((q) => q.dimension === dim);
      return hit ? hit.pass_rate : null;
    }),
  }));
  const behaviorSeries: MultiLineSeries[] = [
    { name: '规则命中', color: colorOf(0), values: series.map((point) => point.behavior.rule_hits) },
    { name: '重试', color: colorOf(4), values: series.map((point) => point.behavior.retry_count) },
    { name: '待复核', color: '#faad14', values: series.map((point) => point.behavior.verdict.flag) },
    { name: '阻断', color: '#ff4d4f', values: series.map((point) => point.behavior.verdict.block) },
  ];
  const latencySeries: MultiLineSeries[] = [
    { name: '平均延迟', color: colorOf(0), values: series.map((point) => point.cost.avg_latency_ms) },
    { name: 'P95 延迟', color: colorOf(2), values: series.map((point) => point.cost.p95_latency_ms) },
  ];
  const costSeries: MultiLineSeries[] = [
    { name: '成本 USD', color: colorOf(3), values: series.map((point) => point.cost.total_cost_usd) },
  ];

  return (
    <Drawer open={open} onClose={onClose} width={drawerWidth(isMobile)} destroyOnHidden
      title={`${displayLabel(resource.resource_kind)} ${resource.resource_id} · 评测观测趋势`}>
      {error
        ? <Alert type="error" showIcon message={error}
          action={<Button size="small" onClick={() => setTick((value) => value + 1)}>重试</Button>} />
        : !trend
          ? <Flex justify="center" style={{ paddingTop: 48 }}><Spin spinning={loading} /></Flex>
          : <Flex vertical gap={24}>
            <Flex gap={24} wrap>
              <Typography.Text strong>{`总样本 ${totals.samples}`}</Typography.Text>
              <Typography.Text strong>{`总 Token ${totals.tokens.toLocaleString('zh-CN')}`}</Typography.Text>
              <Typography.Text strong>{`总成本 $${totals.cost.toFixed(2)}`}</Typography.Text>
            </Flex>
            <SeriesChartSection title="质量趋势" buckets={buckets} series={qualitySeries} unit="percent"
              tick={percentTick} noDataText="窗口内无质量判定维度样本" dataTestId="monitor-quality-chart" />
            <SeriesChartSection title="行为趋势" buckets={buckets} series={behaviorSeries} unit="number"
              tick={countTick} noDataText="窗口内无行为异常样本" dataTestId="monitor-behavior-chart" />
            <SeriesChartSection title="延迟趋势" buckets={buckets} series={latencySeries} unit="number"
              tick={msTick} noDataText="窗口内无延迟样本" dataTestId="monitor-latency-chart" />
            <SeriesChartSection title="成本趋势" buckets={buckets} series={costSeries} unit="number"
              tick={usdTick} noDataText="窗口内无成本数据" dataTestId="monitor-cost-chart" />
            <ProcessRunsSection runs={runs} />
          </Flex>}
    </Drawer>
  );
};
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd web && npx vitest run src/modules/evaluation/components/MonitorResourceDrawer.test.tsx`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add web/src/modules/evaluation/components/MonitorResourceDrawer.tsx web/src/modules/evaluation/components/MonitorResourceDrawer.test.tsx
git commit -m "feat(evaluation): 监控单资源趋势下钻抽屉（spec 2026-09-03 §5.2）" -m "MonitorResourceDrawer 按窗口取 getMonitorTrend：质量/行为/延迟/成本四趋势复用 MultiLineTrendChart，日桶按 UTC 分界、null 断线；过程基线 runs 列表含空态与恒 1.0 诚实注记。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

### Task 9: EvaluationCenterPage 监控 Tab 接线 + 页面测试 + 前端门禁

把 Task 7 的 `EvaluationMonitorPanel` 挂进评测中心 Tabs（spec §5.1：新增「监控」tab，位置在 health「运行通过率趋势」之后、review「人工评审池」之前）。antd Tabs 默认只渲染激活 pane 的 children、且每个 pane 惰性挂载——现有页面测试从不激活 monitor tab，因此不会触发 monitor 的请求；本任务追加的页面级测试只在显式点击「监控」tab 时挂载面板，需要为 `listMonitorResources` 提供 partial mock。面板自身（含趋势下钻）已有 Task 7/8 的组件测试覆盖，这里只验证「tab 位置 + 点击后挂载 + 透传 kind/resource_id 预过滤」。

**Files:**

- Modify: `web/src/modules/evaluation/pages/EvaluationCenterPage.tsx`
  - 组件 import 块（`EvaluationMonitorPanel` 按字母序插入 `CreateSuiteModal`(L10) 与 `EvaluationOverview`(L11) 之间——`M` < `O`）
  - Tabs items 数组：health tab（现 L139-140）与 review tab（现 L141）之间插 monitor tab
- Test: `web/src/modules/evaluation/pages/EvaluationCenterPage.test.tsx`
  - antd message mock（现 L44-47）之后加 evaluationApi partial mock
  - describe 末尾（现 L182 `});` 之前）追加 2 个 `it`

**Interfaces:**

- Consumes：
  - Task 7：`EvaluationMonitorPanel = ({ defaultKind, defaultResourceId, isMobile })`——根容器 `data-testid="evaluation-monitor-panel"`。
  - Task 5：`evaluationApi.listMonitorResources(filters)`；页面只 mock 这一方法，其余 spread actual（页面其它组件/路径可能引用 `evaluationApi` 的真实方法）。
- Produces：无新增导出（页面内部接线）。tab 结构必须满足 spec：`label: '监控'`、位于 health 与 review 之间、children 复用 `kind`/`filterResourceId` 作为 remount key。

- [ ] **Step 1: 写失败测试**

在 `web/src/modules/evaluation/pages/EvaluationCenterPage.test.tsx` 做两处修改。

(1) 在 antd `message` 的 `vi.mock` 块之后（现 L44-47 之后、`LocationProbe` 之前）插入 partial api mock——spread actual 保证页面其它真实依赖不破坏，仅覆盖 monitor 端点：

```tsx
// 监控 Tab 挂载后按默认窗拉资源观测汇总；spread actual 保留 evaluationApi
// 其余真实方法（页面其它路径引用），只替换本页测试关心的 listMonitorResources。
const monitorApi = vi.hoisted(() => ({ listMonitorResources: vi.fn() }));
vi.mock('../api/evaluation.api', async () => {
  const actual = await vi.importActual<typeof import('../api/evaluation.api')>('../api/evaluation.api');
  return { ...actual, evaluationApi: { ...actual.evaluationApi, listMonitorResources: monitorApi.listMonitorResources } };
});

const emptyMonitorWindow = { items: [], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } };
```

(2) 在 describe 块末尾（最后一个 `it` 的 `});` 之后、describe 收尾 `});` 之前）追加两个用例：

```tsx
it('places the monitoring tab after health and before the review pool', () => {
  renderPage();
  const names = screen.getAllByRole('tab').map((node) => node.textContent ?? '');
  expect(names.indexOf('监控')).toBeGreaterThan(names.indexOf('运行通过率趋势'));
  expect(names.indexOf('监控')).toBeLessThan(names.indexOf('人工评审池'));
});

it('mounts the monitoring panel with kind and resource filters from the deep link', async () => {
  monitorApi.listMonitorResources.mockResolvedValue(emptyMonitorWindow);
  renderPage('/evaluations?kind=skill&resource_id=skill-1');
  fireEvent.click(screen.getByRole('tab', { name: '监控' }));
  expect(await screen.findByTestId('evaluation-monitor-panel')).toBeInTheDocument();
  await waitFor(() => expect(monitorApi.listMonitorResources).toHaveBeenCalledWith(expect.objectContaining({
    resource_kind: 'skill', resource_id: 'skill-1',
  })));
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npx vitest run src/modules/evaluation/pages/EvaluationCenterPage.test.tsx`
Expected: FAIL——两条新用例分别因「找不到 `监控` tab」与「`findByTestId('evaluation-monitor-panel')` 超时」失败；存量用例仍 PASS（antd 惰性挂载，不点 monitor 不触发请求）。

- [ ] **Step 3: 接线 monitor tab**

`web/src/modules/evaluation/pages/EvaluationCenterPage.tsx` 两处修改：

(1) import（按字母序插在 `EvaluationOverview` 之前）：

```tsx
import { EvaluationMonitorPanel } from '../components/EvaluationMonitorPanel';
```

(2) Tabs items 数组在 health 与 review 之间插入（与 health 的 remount key 惯例一致，随 URL kind/resource_id 变化重建面板）：

```tsx
      { key: 'monitor', label: '监控', children: <EvaluationMonitorPanel key={`monitor-${kind ?? 'all'}-${filterResourceId ?? 'none'}`}
        defaultKind={kind} defaultResourceId={filterResourceId} isMobile={isMobile} /> },
```

- [ ] **Step 4: 跑测试确认绿**

Run: `cd web && npx vitest run src/modules/evaluation/pages/EvaluationCenterPage.test.tsx`
Expected: PASS（含新增 2 条，存量用例不受影响）。

- [ ] **Step 5: 前端门禁**

Run: `cd .. && make fe-lint && make fe-build`（worktree 无 node_modules 时先在 `cd web && npm ci`）
Expected: lint 无 error、build 成功。

- [ ] **Step 6: Commit**

```bash
git add web/src/modules/evaluation/pages/EvaluationCenterPage.tsx web/src/modules/evaluation/pages/EvaluationCenterPage.test.tsx
git commit -m "feat(evaluation): 评测中心挂载监控 Tab（spec 2026-09-03 §5.1）" -m "健康趋势之后、人工评审池之前插入「监控」Tab，挂载 EvaluationMonitorPanel 并按 URL kind/resource_id 预过滤；页面测试以 partial api mock 覆盖 tab 位置与深链透传。" -m "Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Acceptance / Verification（全部 Task 完成后）

系统验收红线（CLAUDE.md）：PR 前必须在 clean commit 上由 `stratum-e2e-tester` agent 走 `stratum-e2e-development` skill 完成系统验收。R3 风险级（新增能力 + 前端联调 + 数据库链路）按 `.test/verification.yaml` 自动升级 e2e-short；登录与验证流程只用无头浏览器。

合并前完整验证命令（在 worktree 根执行，前端依赖先 `cd web && npm ci`）：

```bash
# 1) 后端（Task 1-4 产物）：无回归
cd web && npx vitest run src/modules/evaluation        # 全量前端评测模块（model/api/组件/页面）
cd .. && make fe-lint && make fe-build                  # 前端门禁
go vet && go test -short ./...                          # 后端 vet + short 单测
```

再交由 `stratum-e2e-tester` agent 执行系统验收（headless Chromium E2E），覆盖：评测中心三级筛选下的「监控」tab 可见与切换、资源行主表数据、点行下钻趋势抽屉与过程基线、空态/错误态/重试，以及 process_pass_rate 恒 1.0 的诚实呈现。local report 仅为 developer audit assertion，非 GitHub trusted status；merge_authority: ci。

通过后由用户（而非子代理）在 CI 全绿时合并 PR，并在 origin 删除 worktree 分支。
