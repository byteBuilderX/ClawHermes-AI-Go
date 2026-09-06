# Agent 评测驱动可观测 P0：usage 去重 + KPI label 修复 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 spec《2026-09-03-agent-evaluation-driven-observability-design》的 P0 最小批次：C1 token usage 计数去重、C2 `agent_task_completed_total.tenant_id` 标签接线、C3 `recordFingerprintAndKPI` 形参命名/语义收敛，并回写 spec 对齐代码事实。

**Architecture:** 三处独立收敛 —— ① `TokenLedger` 撤回 Prometheus usage 打点，让网关成为 token 唯一出站记账方（spec §11 D2①）；② `MetricsProvider.IncAgentTaskCompleted` 增补 `tenantID` 参数贯穿 interface → `PrometheusMetrics`/`NoopMetrics` → 唯一调用链（Execute 尾收 `recordFingerprintAndKPI`，tenant 取 `cfg.TenantID`），并把 KPI 四打点拆成单一职责的 `recordAgentKPI` 以便单测；③ spec 文档措辞对齐（C3 实为命名错位、`agent_eval_score` label 保持已注册的 `{agent_id, metric}`、D1/D2 定稿标注）。

**Tech Stack:** Go 1.25.12、Prometheus client_golang、OTel、pgx/依赖不改动。

## Global Constraints

从 spec §0.2/§10 与仓库 CLAUDE.md 摘录（每条任务都隐含遵守）：

- **仓库 git 红线**：禁止在 `main` 直接提交/推送；实现须在从最新 `origin/main` 创建的隔离 worktree 进行：
  `bash scripts/new-worktree.sh ../stratum-eval-obs-p0 feat/eval-obs-p0`，完成后 `git push -u origin feat/eval-obs-p0`。
- **不要动 Prometheus 已注册指标的 label schema**：`agent_executions_total`（`:202-203` 无 tenant）、`agent_task_duration_seconds`、`agent_cost_per_task_usd`、`agent_eval_score`（`{agent_id, metric}`）均保持既有 label 集合。同一 metric name 换 label 集合会触发 Prometheus「inconsistent label names」拒收（滚动期新旧进程并存即炸）。P0 唯一改值不改 schema 的是 `agent_task_completed_total`：它已注册 `{agent_id, agent_type, task_kind, outcome, tenant_id}`（`prometheus.go:435-437`），只是实现写死了 `""`。
- **`agent_task_completed_total` 语义**：`agent_type` 与 `task_kind` 槽当前收到同一个值（agent 类型）——平台尚无独立 task-kind 维度；`IncAgentTaskCompleted` 的唯一生产调用方是 `recordFingerprintAndKPI`。P0 不做 task_kind 语义发明，只去掉误导形参名并加注释声明。
- **Opik 是 agent 执行唯一权威证据源**：删除 ledger 的 Prometheus 打点时，**保留** `trace.SpanFromContext` 的 span 属性写盘（`llm.prompt_tokens/completion_tokens/cost_usd`）——那是 agent 侧执行证据，评测 re-pull 依赖它。
- 单测不依赖外部组件；`AgentResult`、`ExecutionConfig` 零值可构造。
- Commit 标题格式 `type(scope): description`，type 用 `fix|docs`；每任务独立 commit。

---

### 文件结构

| 文件 | 责任 | 动作 |
|---|---|---|
| `internal/agent/application/token_ledger.go` | 单次 LLM 调用的 cost 计算 + span 属性 + 日志（不再打 Prometheus usage） | Modify |
| `internal/agent/application/token_ledger_test.go` | ledger 契约测试：span 属性、cost 返回、estimate 一致（删 metrics spy 断言） | Rewrite |
| `api/wiring/agent.go` | `wireTokenLedger` 不再注入 metrics | Modify |
| `pkg/observability/provider.go` | `MetricsProvider.IncAgentTaskCompleted` + `NoopMetrics` 增补 `tenantID` | Modify |
| `pkg/observability/prometheus.go` | `PrometheusMetrics.IncAgentTaskCompleted` 用 tenantID 填第 5 label | Modify |
| `pkg/observability/prometheus_test.go` | smoke/NoopMetrics 调用点同步 5 参；新增 tenant label 断言测试 | Modify |
| `internal/agent/application/agent.go` | Execute 尾传 `cfg.TenantID`；拆 `recordAgentKPI`；`recordFingerprintAndKPI` 形参改名+增参 | Modify |
| `internal/agent/application/agent_kpi_test.go` | `recordAgentKPI` 单测：tenant 透传 + 4 打点语义 | Create |
| `docs/superpowers/specs/2026-09-03-agent-evaluation-driven-observability-design.md` | P0 落地后回写（C1/C2/C3、§6.2、§7、§11、附录） | Modify（spec worktree） |

接口契约（跨任务）：

- `func NewTokenLedger(logger *zap.Logger) *TokenLedger`
- `func recordAgentKPI(metrics observability.MetricsProvider, agentID, agentType, status, tenantID string, result *AgentResult)`
- `IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string)`（interface + 双实现）

---

### Task 1：C1 token usage 计数去重（ledger 撤回 Prometheus 打点）

**Files:**

- Modify: `internal/agent/application/token_ledger.go`（struct、constructor、Record）
- Rewrite: `internal/agent/application/token_ledger_test.go`
- Modify: `api/wiring/agent.go:389`（调用）、`:494-501`（`wireTokenLedger` 签名）

**Interfaces:**

- Consumes: 现状 `NewTokenLedger(metrics, logger)`（唯一生产调用点 `api/wiring/agent.go:498`）
- Produces: `NewTokenLedger(logger *zap.Logger) *TokenLedger`（后续 Task 依赖此签名）

背景：`llm_token_usage_total / llm_token_count` 对 agent 发起调用偏高约 2× —— 网关 `invokeComplete`（非流式，`gateway.go:407-412`）打 `IncLLMTokenUsage + RecordLLMTokenHistogram` 双写，`invokeStream`（流式，`gateway.go:468-471`）只打 `IncLLMTokenUsage`（histogram 缺口此前由 ledger 覆盖，去 ledger 后由网关 `invokeStream` 对称补齐——Fix B）；agent 侧 `ledger.Record`（`react_llm.go:386` → `token_ledger.go:40-49`）对同一 `resp.Usage` 再打一次。裁决（spec §11 D2①）：网关出站为 token 唯一事实源；ledger 只保留 cost 计算 + span 属性（Opik 证据）+ 日志。

- [ ] **Step 1：改写测试文件以编码新契约（无 metrics 打点、单参 constructor）**

整文件替换 `internal/agent/application/token_ledger_test.go`：

```go
package application

import (
	"context"
	"testing"

	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}

// TestTokenLedger_Record 固定 C1 去重后的核心契约：返回真实成本、span 写出
// token/cost 属性（agent 侧执行证据，Opik re-pull 依赖）。Prometheus usage 打点
// 已移出 ledger —— 由网关出站唯一记账（spec §11 D2①）。
func TestTokenLedger_Record(t *testing.T) {
	const (
		model      = "qwen-turbo"
		prompt     = 100
		completion = 50
		total      = 150
	)
	recorder := newSpanRecorder(t)
	l := NewTokenLedger(nil) // nil logger：debug 日志路径不炸

	spanCtx, span := otel.Tracer("test").Start(context.Background(), "llm-call")
	gotTotal, gotCost := l.Record(spanCtx, model, port.TokenUsage{Prompt: prompt, Completion: completion, Total: total})
	span.End()

	require.Equal(t, total, gotTotal)
	wantCost := tokenutil.CostUSD(prompt, completion, model)
	require.InDelta(t, wantCost, gotCost, 1e-9)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()
	require.Contains(t, attrs, attribute.Int("llm.prompt_tokens", prompt))
	require.Contains(t, attrs, attribute.Int("llm.completion_tokens", completion))
	require.Contains(t, attrs, attribute.Float64("llm.cost_usd", wantCost))
}

// TestTokenLedger_Record_nilDeps 覆盖 logger/span 全 nil 的构建路径，不得 panic。
func TestTokenLedger_Record_nilDeps(t *testing.T) {
	l := NewTokenLedger(nil)
	total, cost := l.Record(context.Background(), "unknown-model", port.TokenUsage{Prompt: 10, Completion: 5, Total: 15})
	require.Equal(t, 15, total)
	require.Equal(t, 0.0, cost) // 未知名模型无定价 → 0
}

// TestTokenLedger_Estimate 固定估算算法与 tokenutil 一致。
func TestTokenLedger_Estimate(t *testing.T) {
	l := NewTokenLedger(nil)
	msgs := []port.LLMMessage{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi"},
	}
	want := 0
	for _, m := range msgs {
		want += tokenutil.EstimateText(m.Role) + tokenutil.EstimateText(m.Content) + 4
	}
	require.Equal(t, want, l.Estimate(msgs))
}
```

- [ ] **Step 2：运行测试确认失败（编译期红）**

Run: `cd <worktree> && go test ./internal/agent/application/ -run 'TestTokenLedger' 2>&1 | head -20`
Expected: 编译失败 —— `NewTokenLedger` 仍要求 2 参（`too many arguments` / `not enough arguments`），因实现尚未改。

- [ ] **Step 3：实现去重（token_ledger.go + wiring）**

3a. `internal/agent/application/token_ledger.go` 整文件替换：

```go
package application

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
)

// TokenLedger 聚合 agent 侧的 token 估算与成本计算。
// Record 只做 cost 计算 + span 属性 + 日志，不再打 Prometheus usage：C1 计数去重
// （spec §1.3 C1 / §11 D2①），llm_token_usage_total/llm_token_count 由网关出站
// 唯一记账（gateway invokeComplete/invokeStream）。span 属性是 agent 侧执行证据，
// Opik re-pull 依赖，保留。
type TokenLedger struct {
	logger *zap.Logger
}

func NewTokenLedger(logger *zap.Logger) *TokenLedger {
	return &TokenLedger{logger: logger}
}

// UsageSummary 封装单次 LLM 调用的 token + 成本。
type UsageSummary struct {
	Prompt     int
	Completion int
	Total      int
	CostUSD    float64
}

// Record 在每次 LLM 调用返回后调用，完成成本计算、OTEL span 标注、zap 日志。
// 返回 (total tokens, cost USD)。
func (l *TokenLedger) Record(ctx context.Context, model string, usage port.TokenUsage) (int, float64) {
	cost := tokenutil.CostUSD(usage.Prompt, usage.Completion, model)

	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(
			attribute.Int("llm.prompt_tokens", usage.Prompt),
			attribute.Int("llm.completion_tokens", usage.Completion),
			attribute.Float64("llm.cost_usd", cost),
		)
	}

	if l.logger != nil {
		l.logger.Debug("token.record",
			zap.String("model", model),
			zap.Int("prompt_tokens", usage.Prompt),
			zap.Int("completion_tokens", usage.Completion),
			zap.Int("total_tokens", usage.Total),
			zap.Float64("cost_usd", cost),
		)
	}

	return usage.Total, cost
}

// Estimate 估算消息列表 token 数，统一使用 tokenutil 算法。
func (l *TokenLedger) Estimate(msgs []port.LLMMessage) int {
	total := 0
	for _, m := range msgs {
		total += tokenutil.EstimateText(m.Role) + tokenutil.EstimateText(m.Content) + 4
	}
	return total
}
```

3b. `api/wiring/agent.go` `wireTokenLedger`（约 `:494-501`）整函数替换：

```go
// wireTokenLedger 注入 TokenLedger 到 Registry 与 AgentService deps：span cost
// 此前恒 0（Noop 返回 0），接线后为真实 USD。ledger 不打 Prometheus usage ——
// token 计数由网关出站唯一记账（spec §11 D2①，C1 去重）。Registry.Get
// hydrate 的 agent 同样走执行链路，两个构建点必须同源。
func wireTokenLedger(registry *agent.Registry, deps *agent.AgentServiceDeps, logger *zap.Logger) {
	ledger := agent.NewTokenLedger(logger)
	registry.SetLedger(ledger)
	deps.Ledger = ledger
}
```

3c. 调用点（`:389`）去掉第三个实参：

```go
	wireTokenLedger(registry, &deps, c.Logger)
```

- [ ] **Step 4：运行测试确认通过**

Run: `cd <worktree> && go build ./... && go vet ./internal/agent/application/ ./api/wiring/ && go test ./internal/agent/application/ -run 'TestTokenLedger' -v`
Expected: PASS（`TestTokenLedger_Record` 校验 span 属性 + 真实 cost；estimate 与 tokenutil 一致）。`go build ./...` 无错（确认 wiring 与全仓库无其它 `NewTokenLedger` 调用残留）。

- [ ] **Step 5：Commit**

```bash
git add internal/agent/application/token_ledger.go internal/agent/application/token_ledger_test.go api/wiring/agent.go
git commit -m "fix(agent): dedup llm token usage metrics to gateway (C1)

TokenLedger.Record 撤回 IncLLMTokenUsage/RecordLLMTokenHistogram 打点，
llm_token_usage_total/llm_token_count 归网关出站唯一记账（spec §11 D2①），
消除 agent 调用 2x 双计。span token/cost 属性保留，作为 agent 侧执行证据。"
```

---

### Task 2：C2 tenant_id 标签接线 + C3 形参语义收敛

**Files:**

- Modify: `pkg/observability/provider.go:57`、`:197`
- Modify: `pkg/observability/prometheus.go:734-736`
- Modify: `pkg/observability/prometheus_test.go:62`、`:193`
- Modify: `internal/agent/application/agent.go:744`、`:773-797`（拆分出 `recordAgentKPI`）
- Create: `internal/agent/application/agent_kpi_test.go`

**Interfaces:**

- Consumes: Task 1 后的 `NewTokenLedger(logger)`；现网 `IncAgentTaskCompleted(agentID, agentType, taskKind, outcome string)`
- Produces: `recordAgentKPI(metrics, agentID, agentType, status, tenantID string, result *AgentResult)`；`IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string)`

背景：`agent_task_completed_total` 注册含 `tenant_id`（`prometheus.go:435-437`）但实现 `WithLabelValues(...,"")` 写死空串（C2）；`recordFingerprintAndKPI` 形参名 `taskKind` 实收 `string(snap.agentType)`，两槽同值但命名误导（C3 实为命名错位，非值污染）。tenant 权威来源是 `ExecutionConfig.TenantID` —— 与 agent 内层 `newReActExecContext` 用 `ec.cfg.TenantID` 重注入同源（`agent.go:1037`）。

- [ ] **Step 1：先改 observability 接口层（红到绿的编译契约）**

`pkg/observability/provider.go:57`：

```go
	IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string)
```

`pkg/observability/provider.go:197`（NoopMetrics，对齐列宽）：

```go
func (NoopMetrics) IncAgentTaskCompleted(_, _, _, _, _ string)                   {}
```

`pkg/observability/prometheus.go:734-736`（实现，填真实 tenant）：

```go
func (m *PrometheusMetrics) IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string) {
	m.agentTaskCompletedTotal.WithLabelValues(agentID, agentType, taskKind, outcome, tenantID).Inc()
}
```

`pkg/observability/prometheus_test.go:62`（smoke）与 `:193`（NoopMetrics）：

```go
	m.IncAgentTaskCompleted("a1", "chat", "research", "ok", "tenant-1")
```

```go
	nm.IncAgentTaskCompleted("a", "t", "k", "ok", "tenant-1")
```

- [ ] **Step 2：新增 tenant label 断言测试（防回归 C2）**

在 `pkg/observability/prometheus_test.go` 末尾追加：

```go
// TestAgentTaskCompletedTenantLabel 防 C2 回归：tenant_id label 必须落真实值，
// 且不会产生空串 label 的孤儿 series。
func TestAgentTaskCompletedTenantLabel(t *testing.T) {
	m := newTestMetrics(t)
	m.IncAgentTaskCompleted("a1", "react", "react", "ok", "tenant-9")

	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "agent_task_completed_total" {
			continue
		}
		if len(f.Metric) != 1 {
			t.Fatalf("want exactly 1 series, got %d", len(f.Metric))
		}
		labels := map[string]string{}
		for _, lp := range f.Metric[0].Label {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels["tenant_id"] != "tenant-9" {
			t.Fatalf("tenant_id = %q, want tenant-9", labels["tenant_id"])
		}
		if got := f.Metric[0].GetCounter().GetValue(); got != 1 {
			t.Fatalf("count = %v, want 1", got)
		}
		return
	}
	t.Fatal("agent_task_completed_total not gathered")
}
```

- [ ] **Step 3：新增 recordAgentKPI 单测（红：函数未定义）**

Create `internal/agent/application/agent_kpi_test.go`：

```go
package application

import (
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/stretchr/testify/require"
)

// agentKPISpy 捕获 recordAgentKPI 的 4 类 KPI 打点；embed NoopMetrics 满足接口。
type agentKPISpy struct {
	observability.NoopMetrics
	completedAgentID, completedAgentType, completedTaskKind, completedOutcome, completedTenant string
	latencySeconds float64
	costUSD        float64
	turns          int
}

func (s *agentKPISpy) IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID string) {
	s.completedAgentID, s.completedAgentType, s.completedTaskKind, s.completedOutcome, s.completedTenant =
		agentID, agentType, taskKind, outcome, tenantID
}

func (s *agentKPISpy) RecordAgentTaskLatency(_ string, _ string, seconds float64) {
	s.latencySeconds = seconds
}

func (s *agentKPISpy) RecordAgentCostPerTask(_ string, _ string, costUSD float64) {
	s.costUSD = costUSD
}

func (s *agentKPISpy) RecordAgentConversationTurn(_ string, turnCount int) {
	s.turns = turnCount
}

// TestRecordAgentKPI 验证 KPI 打点语义与 C2 tenant 透传：tenant_id 槽落真实租户，
// task_kind 槽当前镜像 agent_type（平台暂无独立 task-kind 维度）。
func TestRecordAgentKPI(t *testing.T) {
	spy := &agentKPISpy{}
	result := &AgentResult{Duration: 3 * time.Second, CostUSD: 0.42, Steps: 5}

	recordAgentKPI(spy, "agent-1", "react", "ok", "tenant-9", result)

	require.Equal(t, "agent-1", spy.completedAgentID)
	require.Equal(t, "react", spy.completedAgentType)
	require.Equal(t, "react", spy.completedTaskKind)
	require.Equal(t, "ok", spy.completedOutcome)
	require.Equal(t, "tenant-9", spy.completedTenant)
	require.Equal(t, 3.0, spy.latencySeconds)
	require.Equal(t, 0.42, spy.costUSD)
	require.Equal(t, 5, spy.turns)
}
```

Run: `cd <worktree> && go test ./internal/agent/application/ -run TestRecordAgentKPI 2>&1 | head`
Expected: 编译失败 —— `undefined: recordAgentKPI`。

- [ ] **Step 4：实现 agent 侧改动**

4a. `internal/agent/application/agent.go`：在 `recordFingerprintAndKPI` 定义前插入 `recordAgentKPI`，并替换 `recordFingerprintAndKPI` 签名与 metrics 段。

替换点 A —— 现 `:773-797` 整函数替换为：

```go
// recordAgentKPI 打点 agent 任务级 KPI（agent_task_completed / task_duration /
// cost_per_task / conversation_turns）。task_kind 槽当前镜像 agent_type：平台尚无
// 独立 task-kind 维度（IncAgentTaskCompleted 唯一生产调用方就是这里），预留真实
// task-kind（如审批类任务）接入时再分离。
func recordAgentKPI(metrics observability.MetricsProvider, agentID, agentType, status, tenantID string, result *AgentResult) {
	metrics.IncAgentTaskCompleted(agentID, agentType, agentType, status, tenantID)
	metrics.RecordAgentTaskLatency(agentID, agentType, result.Duration.Seconds())
	metrics.RecordAgentCostPerTask(agentID, agentType, result.CostUSD)
	metrics.RecordAgentConversationTurn(agentID, result.Steps)
}

func recordFingerprintAndKPI(
	metrics observability.MetricsProvider,
	execSpan, requestSpan oteltrace.Span,
	agentID, agentType, llmModel, systemPrompt string,
	cfg *ExecutionConfig,
	maxContextTokens int,
	result *AgentResult,
	status, tenantID string,
) {
	recordAgentKPI(metrics, agentID, agentType, status, tenantID, result)
	// 指纹记录实际解析模型与路由链：fallback 降级后 ModelResolved 为实际
	// 成功模型，ModelRoutedVia 为尝试过的模型链；未降级时保持配置模型。
	resolved := llmModel
	if result.ModelResolved != "" {
		resolved = result.ModelResolved
	}
	fp := CaptureFingerprint(resolved, result.ModelRoutedVia, systemPrompt, skillRevisionHashes(cfg.SkillCatalog),
		tunableConfigVersion(cfg, maxContextTokens), tunableSnapshot(cfg, maxContextTokens), 0)
	fpAttrs := fingerprintAttributes(fp)
	execSpan.SetAttributes(fpAttrs...)
	requestSpan.SetAttributes(fpAttrs...)
}
```

4b. 调用点 `:744` 追加 `cfg.TenantID` 实参：

```go
	recordFingerprintAndKPI(snap.metrics, execSpan, requestSpan, snap.agentID, string(snap.agentType), snap.llmModel, snap.systemPrompt+globalSystemSuffix(snap.globalSystemSuffix), cfg, snap.maxContextTokens, result, status, cfg.TenantID)
```

（`cfg` 为 Execute 顶部 `cfg := &ExecutionConfig{}`，作用域覆盖收尾；`cfg.TenantID` 与 `agent.go:1037` 内层重注入同源。）

- [ ] **Step 5：运行测试确认通过**

Run: `cd <worktree> && go build ./... && go test ./pkg/observability/ ./internal/agent/application/ -run 'TestRecordAgentKPI|TestAgentTaskCompletedTenantLabel|TestTokenLedger|TestPrometheus' -v`
Expected: 全 PASS。接口签名变更后，全仓其余 `MetricsProvider` 使用方（全部 embed `NoopMetrics` 的测试 spy）自动跟随，无手工改动——由 `go build ./...` 保证无遗漏调用点。

- [ ] **Step 6：Commit**

```bash
git add pkg/observability/provider.go pkg/observability/prometheus.go pkg/observability/prometheus_test.go internal/agent/application/agent.go internal/agent/application/agent_kpi_test.go
git commit -m "fix(observability): thread tenant_id into agent task KPI (C2/C3)

agent_task_completed_total 5-label 中 tenant_id 此前写死空串；现经
IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID) 贯通
interface/Prometheus/Noop，tenant 取自 ExecutionConfig.TenantID（与 agent 内层
重注入同源）。recordFingerprintAndKPI 拆出 recordAgentKPI 单一职责并正名形参
（taskKind 实收 agentType，两槽同值；平台暂无独立 task-kind，语义注释声明）。"
```

---

### Task 3：spec 回写对齐（C3 措辞 / agent_eval_score label / 落地状态）

**Files:**

- Modify（spec worktree）: `docs/superpowers/specs/2026-09-03-agent-evaluation-driven-observability-design.md` —— §1.3 C2/C3 行、§5.2 label、§5.3 C-a、§5.4、§6.2、§7 P0 行、§11 D1/D2、附录 token/agent 指标两行

**Interfaces:**

- Consumes: Task 1/2 落地后的代码事实（`recordAgentKPI` 新函数、`IncAgentTaskCompleted` 5 参、`wireTokenLedger` 无 metrics、`NewTokenLedger(logger)`）
- Produces: 与代码一致的 spec（供 P1/P2/P3 及门禁 spec 引用）

背景：spec 是唯一权威设计文档，P0 落地后必须回写以对齐真实代码，避免后续分期照错误措辞实现。三处事实性勘误：① C3 实为「形参名误导」非「agent_type 值污染」；② `agent_eval_score` 已注册 `{agent_id, metric}`（`prometheus.go:446-449`），非 spec 写的 `{agent_id, dimension}`，且已有方法 `RecordAgentEvalScore`（0 调用），无需新增 `SetAgentEvalScore`；③ `cfg.TenantID`（非 `execTenant 上下文取值`）是 Execute 收尾的租户来源。

执行前先在工作目录确认是 spec 分支：`cd /home/yang/go-projects/stratum-agent-eval-obs-spec && git rev-parse --abbrev-ref HEAD`。

- [ ] **Step 1：修 §1.3 C2/C3 行（表 72-74）**

用下述三行整体替换 `sed -n '72,74p'` 出的当前三行（C1 行追加「P0 已修」注记）：

```
| C1 | **token 双重计数（坐实）**：同一次 agent LLM 调用，gateway `invokeComplete/invokeStream` 打 `IncLLMTokenUsage + RecordLLMTokenHistogram`，agent 层 `react_llm.go:386` 经 `ledger.Record` **再打一次** | `gateway.go:407-412, 468-471`；`react_llm.go:386`；`token_ledger.go:37-70` | `llm_token_usage_total / llm_token_count` 对 agent 发起调用偏高约 2×；用量告警与成本对账失真 |
| C2 | **`tenant_id` 标签空串**：`agent_task_completed_total` 有 `tenant_id` label 但实现写死空串（`prometheus.go:734-736`），非注册缺位 | `prometheus.go:435-437`（注册含 tenant_id）、`:734-736`（写死 `""`） | 平台租户级用量/成本不可分——多租户评测对比（平台参数多租户效应）无数据支撑 |
| C3 | **KPI 形参名误导**：`recordFingerprintAndKPI` 形参名 `taskKind` 实收 `string(snap.agentType)`，`IncAgentTaskCompleted(agentID, taskKind, taskKind, status)` 使 agent_type 与 task_kind 两槽同值为 agentType（命名错位，非值污染；平台暂无独立 task-kind 维度） | `agent.go:782`（旧）→ 拆分 `recordAgentKPI`；注册 `:435-437` 五标签 | task_kind 语义悬空，读者误以为存在独立 task-kind 分类 |
```

并在此表下、`### 1.4` 标题前插入一行注记：

```
> **P0 修复状态（2026-09-03）**：C1 已去重（ledger 撤回 usage 打点，见 §11 D2①）；C2 已接线（tenant 取自 `ExecutionConfig.TenantID`）；C3 已通过拆分 `recordAgentKPI` 正名。实现细节见计划 `2026-09-03-agent-eval-obs-p0-metrics-fix.md`。
```

- [ ] **Step 2：修 §5.2 label 措辞（`{agent_id, dimension}` → 已注册 `{agent_id, metric}`）**

替换 §5.2「- **label**：」整行为：

```
- **label**：沿用已注册 `{agent_id, metric}`（`prometheus.go:446-449`，P0 不改 schema——同名字段 label 集合变更会触发 Prometheus 拒收）。metric ∈ `overall|faithfulness|relevance|completeness|safety|format|cost_perf|process|behavior`。agent 量级（平台托管 + 租户自建）有限，基数可控。
```

- [ ] **Step 3：修 §5.3 C-a 行与 §5.4 成功标准**

替换 `sed -n '184p'` 的 C-a 行：

```
| C-a | 复用已注册 `RecordAgentEvalScore(agentID, metric string, score float64)`（`provider.go`，0 调用）与 `{agent_id, metric}` label（`prometheus.go:446-449`），只补写者接线；**不新增 `SetAgentEvalScore`、不改 label** |
```

替换 `sed -n '190p'`（§5.4 首行）`agent_eval_score{agent_id, dimension}` → `agent_eval_score{agent_id, metric}`：

```
- 评测集跑完 → `agent_eval_score{agent_id, metric}` 出现，维度完整，Dashboard 可查；
```

- [ ] **Step 4：重写 §6.2 段（落地后描述）**

将 §6.2 heading 下三条 bullet（`sed -n '208,210p'`）整体替换为：

```
### 6.2 agent KPI 修复（P0 已落地）

- C2 `tenant_id` 空串 → 已接线：`IncAgentTaskCompleted` 增 `tenantID` 参贯通 interface/双实现，Execute 收尾取 `cfg.TenantID`（`ExecutionConfig.TenantID`，与内层 `newReActExecContext` 重注入同源），解锁租户级成本对比；
- C3 形参误导 → 已收敛：`recordFingerprintAndKPI` 拆分出 `recordAgentKPI`（单一职责），形参正名 `agentType`；task_kind 槽镜像 agent_type，平台暂无独立 task-kind 维度（注释声明，预留接入）；
- `agent_executions_total`（无 tenant，schema 锁）与 `agent_task_completed_total`（含 tenant）**口径分工**：前者是执行事件计数（不含租户，避免改已注册 schema），后者是租户级任务 KPI；跨租户评测对比统一查 `agent_task_completed_total`。
```

- [ ] **Step 5：修 §7 P0 行出口判据**

替换 P0 表行（`sed -n '233p'`）为落地态：

```
| **P0（计数与语义收敛，已落地 2026-09-03）** | C1 计数去重（D2①）；C2/C3 标签修复；D1 `agent_eval_score` label 对齐已注册 `{agent_id, metric}`（语义写作留 P2） | 无 | usage 双计消除；tenant_id 落真实租户；task_kind 命名收敛；score label 与代码一致 |
```

- [ ] **Step 6：修 §11 D1/D2 定稿标注**

替换 D1 推荐默认（`:287` 的 label 片段）`label \`{agent_id, dimension}\`` → `label \`{agent_id, metric}\`（对齐已注册 schema，P2 写者落地）`；并在 D2 推荐默认后追加`（P0 已落地）`：

`sed -n '287p'`（D1）替换尾段为：

```
| D1 | `agent_eval_score` 语义/维度/label | ①run 级写 ②评测集/门禁结论写 ③两者分层 | **②为主，①仅过渡口径**；label 沿用已注册 `{agent_id, metric}`（P0 定稿，语义写者留 P2） |
```

`sed -n '289p'`（D2）替换为：

```
| D2 | 计数归属 | ①去 ledger 留 gateway ②去 gateway 留 ledger | **①**（网关出站为 token 唯一事实源）——**P0 已落地** |
```

- [ ] **Step 7：修附录两行（token 账本 / agent 指标 file:line）**

替换附录中「- token 账本 / 计数：」与「- agent 指标注册：」两行（`sed -n '299,300p'`）为：

```
- token 账本 / 计数：`token_ledger.go:37-70`（Record：cost+span 属性+日志，**无 usage 打点**——C1 后）；`graph/react_llm.go:386`（ledger.Record 调用）；`gateway.go:407-412,468-471`（usage，唯一记账）；`:403,460`（duration）；`api/wiring/agent.go`（`wireTokenLedger` 不注入 metrics）
- agent 指标注册：`prometheus.go:202-216`（executions/duration/step，无 tenant）；`:435-437`（task_completed 五标签含 tenant_id）；`:734-736`（实现落真实 tenant）；`:446-449`（agent_eval_score `{agent_id, metric}` 0 调用）；`agent.go`（`recordAgentKPI` 打点 + `recordFingerprintAndKPI` 指纹）
```

- [ ] **Step 8：全文一致性 + 校验 + commit**

Run: `cd /home/yang/go-projects/stratum-agent-eval-obs-spec && grep -n "dimension\b" docs/superpowers/specs/2026-09-03-agent-evaluation-driven-observability-design.md`
Expected: 仅剩语义中性提及（如 `by-dimension` 措辞处可保留，若无则无输出）；不应再出现 `agent_eval_score{... dimension}` 或「新增 SetAgentEvalScore」字样。
再 `grep -n "SetAgentEvalScore" docs/superpowers/specs/2026-09-03-agent-evaluation-driven-observability-design.md`
Expected: 无输出（已全部改为复用 `RecordAgentEvalScore`）。
文档结构改动 → 按仓库门槛跑 `markdownlint`（若 worktree 有 make target 则 `make fe-lint` 不适用，docs 用 `npx markdownlint-cli2 docs/superpowers/specs/2026-09-03-agent-evaluation-driven-observability-design.md`；无则跳过并说明）。

Commit（在 spec worktree）：

```bash
cd /home/yang/go-projects/stratum-agent-eval-obs-spec
git add docs/superpowers/specs/2026-09-03-agent-evaluation-driven-observability-design.md
git commit -m "docs(spec): align P0 findings with post-fix code reality

C3 勘误为形参命名错位（非 agent_type 值污染，task_kind 镜像 agent_type）；
agent_eval_score label 对齐已注册 {agent_id, metric}（不改 schema，不新增
SetAgentEvalScore）；C2 tenant 来源标注 ExecutionConfig.TenantID；§6.2/§7/
§11 D1/D2/附录 标记 P0 落地状态与口径分工。"
```

---

### Task 4：验收与 PR

**Files:** 无（验证与提交流程）

- [ ] **Step 1：全量质量门禁（代码 worktree）**

```bash
cd <worktree>            # /home/yang/go-projects/stratum-eval-obs-p0（Task 0 创建）
bash scripts/quality/risk-regression-guard.sh --explain   # 只读 explain，确认无命中项需专项测试
go vet ./...
go test -short ./...
make code-quality
make risk-guardrails
```

Expected: 全绿。`go test -short ./...` 无失败；`code-quality` 无新函数超限（`recordAgentKPI` 单职责 ≤ 上限）。

- [ ] **Step 2：完整测试套件（含 race）**

Run: `go test -v -race -timeout 30s ./...`
Expected: 全绿。

- [ ] **Step 3：系统验收（仓库红线）**

在 clean commit 上按 CLAUDE.md「验收红线」经本地专用验收 agent `stratum-e2e-tester`（封装 `stratum-e2e-development` skill，按 `.test/verification.yaml` 分级）完成系统验收，产出结构化报告；报告内 failed/skipped/unreconciled 一律阻断。

- [ ] **Step 4：PR（base 须不落后 origin/main）**

```bash
git push -u origin feat/eval-obs-p0
git fetch origin main && git rev-parse origin/main     # 对比 base
gh pr create --base main --title "fix(observability): agent KPI label & token usage dedup (P0)" \
  --body "What: C1 llm token usage 计数去重归网关；C2 agent_task_completed_total.tenant_id 接线（cfg.TenantID）；C3 recordFingerprintAndKPI 拆分 recordAgentKPI 正名；spec 回写对齐。
Why: spec P0（2026-09-03-agent-evaluation-driven-observability-design），消除 2x 双计与空 tenant 标签，为租户级成本/评测对比铺底。
HowToTest: go test -short ./... + race 全量；stratum-e2e-tester 系统验收报告见 PR 附件。"
```

若 base 落后：先 `git merge origin/main` 本地验证无冲突通过后 push，再等 CI（merge ref 为动态合并，规则见 CLAUDE.md）。

---

## Self-Review

**Spec coverage 检查：**

- §1.3 C1（token 双计）→ Task 1 ✓；C2（tenant 空串）→ Task 2 ✓；C3（形参误导）→ Task 2 + Task 3 措辞对齐 ✓
- §5.2/§5.3/§5.4 `agent_eval_score` label 措辞 `{agent_id, dimension}` → 与代码 `{agent_id, metric}` 不符 → Task 3 修正为对齐已注册 schema（**不新增 SetAgentEvalScore、不改 label**），写者语义留 P2 ✓
- §6.2（C2/C3 修复口径 + executions/task_completed 口径分工）→ Task 2 实现 + Task 3 回写 ✓
- §7 P0 行出口判据 → Task 1-3 达成并回写 ✓
- §11 D1 label 定稿 → Task 3；D2（计数归属 ①）→ Task 1 落地 ✓
- §8 no-false-green / §10 交付项 / §3.3 / §4.3（成本账本与过程证据）：**不在 P0 范围**，属 P1（证据保真：A1-A3/B1-B3）→ 本计划不做，属后续分期 ✓（YAGNI，避免空接线）

**Placeholder scan：** 全部步骤含完整代码/命令/期望输出，无 TBD/TODO；Task 3 的替换均给出 old 定位命令 + new 全文。

**Type consistency：** `NewTokenLedger(logger)`（Task 1）与 wiring/test 全同步；`IncAgentTaskCompleted(agentID, agentType, taskKind, outcome, tenantID)` 在 provider.go/prometheus.go/NoopMetrics/prometheus_test.go/recordAgentKPI/agent_kpi_test.go 中 6 处签名一致；`recordAgentKPI(metrics, agentID, agentType, status, tenantID, result)` 定义与唯一调用点（recordFingerprintAndKPI）+ 单测一致。
