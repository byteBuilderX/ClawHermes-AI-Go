# Reuse BE3：后端工具/测试助手去重（contract harness 分叉收编 + findRepoRoot 合一）

> 目标：消除 golden 契约 harness 在 `scripts/record-contracts.go`（录制器）与 `api/http/contract_test.go`（校验器）之间的约千行手工双写，并把 `findRepoRoot` 的三处副本合为一处共享实现。行为保持，由既有契约测试与录制确定性守护。

## 背景与动机

HTTP 契约 golden 的「录制—校验」两半各自维护一套同构实现：

- `scripts/record-contracts.go`（`//go:build contracts`，`package main`，`make record-contracts` 手动执行）——遍历 Gin 路由、发请求、**写** `api/http/testdata/contracts/*.golden.json`。
- `api/http/contract_test.go`（`package http_test`，CI 常跑的 `contract-test`）——读同一批 golden、重放请求、**校验**响应。

两文件各自内联了同一组 ~28 个领域 port 的 `contract*` stub、逐行同构的 `wiring.Container` 装配，以及确定性 fixture（如 `contractReviewItem`）。recorder 的 stub 用缩写 receiver 名（`contractProvRepo`/`contractAdminUR`/`contractTenantR`…），test 用全称（`contractProviderRepo`/`contractAdminUserRepo`/`contractTenantRepo`…）；同名者（`contractDefRepo`/`contractRunStore`/`contractReviewRepo`/`contractSchedRepo`…）两处逐字一致。recorder 装配注释自述「与 api/http/contract_test.go 的 buildDDDContainer 保持同一注入」——靠人工双写维持，正是 DRY 要消除的漂移源。本次取证确认已存在一处真实差异（test 的 Evaluation 装配含 `ObservationService`，recorder 缺；router 有 `GET /evaluations/observations` 且 5 个 golden 含 observation，录制产物与 test 语义仍一致，见「验证基线」）。

## 取证结论（对原 8 项审计 BE3 快照的修正）

| 审计项 | 实际 | 处理 |
|---|---|---|
| `findRepoRoot` ×2 | 实为 **3 处**：`cmd/coverage-gap/main.go:497`、`cmd/coverage-gap-db/main.go:782`（同体，失败返 `"."`），`internal/agent/infrastructure/officialdocs/generate/main.go:280` `findRepositoryRoot`（第三变体，失败返 error） | 合一为共享 `Find()` |
| `contractReviewItem` ×2 | 只是更大分叉的切片：两文件整段 stub+容器双写 | 全量收编分叉 |
| `dropStale` | **误报**：`llmgateway` 集成测试内为局部闭包、`parameters` 集成测试内为包级助手，同名不同函数，各单点使用 | 不去重 |
| coverage-gap 工具 | 两个独立 CLI，仅 `findRepoRoot` 重复 | 只合一 `findRepoRoot` |

## 范围

### A. contract harness 分叉收编（核心）

新建可 import 的共享包 **`api/http/contracttest`**（`package contracttest`，无 build tag），收编两文件共同的：

1. **stub 集合**（表 1，canonical 命名 = test 全称；已同名者原样保留）
2. **`BuildContainer(cfg, key, logger, metrics) *wiring.Container`**——canonical 取 test 装配（超集，含 `ObservationService`），internal 的 `nextID` 用 `atomic.Int64`（两文件当前语义等价，均自 `contract-1` 起递增）
3. **确定性 fixture** `ReviewItem()`（原两处 `contractReviewItem()`）
4. **`SchedulerStub(logger)`** + sched 三个 stub（原 `contractSchedulerStub` 及 `contractSchedRepo/Runner/Resolver`）
5. `errStubNotFound`（两文件各有一份；随 stub 移入共享，未导出）

**录制器写入确定性修复（随 A 一并做）**：`record-contracts.go` 现有 5 个
`os.WriteFile` 写入点（344/377/422/452/494）均直接写 `json.MarshalIndent` 产物、
**不带文件尾换行**，而提交的 golden 均以 `\n` 结尾 → 每次 regen 都产生 174 文件
「去换行」churn。抽一个私有 helper `writeGolden(outPath string, out []byte)`
（内部 `append(out, '\n')` 后写入，写失败 `panic`，与现有 344-452 各点一致；494
处当前 `_ = os.WriteFile` 吞错，一并归一到 panic），5 处调用替换为 helper。使
regen 与提交态**逐字节一致**（守护简化为 `git status` 干净），golden 在本 PR
零改动（提交态本就带 `\n`，录制器对齐即可）。

消费方改为 import：

- `api/http/contract_test.go` 删除：全部 stub 定义、fixture、`errStubNotFound`、内联容器字面量。保留：`contractCase`、`TestContracts`（含 legacy `api.SetupRouter`、ddd 前缀分发、逐 golden 回放断言）、`jsonEquivalent`、`mustGeneratePEM(t)`。DDD 路由改由 `apihttp.NewRouter(contracttest.BuildContainer(...))` 一行构建。
- `scripts/record-contracts.go` 删除：`buildDDDContainer`、全部 stub、fixture、`errStubNotFound`、`contractSchedulerStub`。保留：`main`、`recordLegacyRoutes`、`recordDDDRoutes`、`recordDDDRoute`、`recordAuthOverride`、`recordAuthRoute`、`recordEvalRoute`、`recordReviewRoute`、`recordSelfModifyRoute`、`recordRoute`、`goldenName`、`resolvePath`、`isReviewRoute`、`isDDDAuthOverride`、`mustGeneratePEM()`、`Case`。DDD 容器改由 `contracttest.BuildContainer(...)` 构建。

**表 1：共享 stub 命名映射（canonical 源 = api/http/contract_test.go）**

| 领域 port | record 缩写 | test 全称（canonical） |
|---|---|---|
| llm Provider repo | `contractProvRepo` | `contractProviderRepo` |
| llm Model repo | `contractModRepo` | `contractModelRepo` |
| llm Provider runtime | `contractProvRuntime` | `contractProviderRuntime` |
| workflow Definition repo | `contractDefRepo`（同名） | `contractDefRepo` |
| workflow Version repo | `contractVerRepo` | `contractVersionRepo` |
| workflow Run store | `contractRunStore`（同名） | `contractRunStore` |
| workflow Control repo | `contractCtrlRepo` | `contractControlRepo` |
| agent executor | `contractAgtExec` | `contractAgentExecutor` |
| iam AdminUser repo | `contractAdminUR` | `contractAdminUserRepo` |
| iam AdminTenant repo | `contractAdminTR` | `contractAdminTenantRepo` |
| iam Tenant repo | `contractTenantR` | `contractTenantRepo` |
| iam Invitation repo | `contractInvR` | `contractInvitationRepo` |
| agent Proposal repo | `contractPropRepo` | `contractProposalRepo` |
| agent ProposalAuthorizer | `contractPropAuthorizer` | `contractProposalAuthorizer` |
| eval Experiment repo | `contractExpRepo` | `contractExperimentRepo` |
| eval Candidate repo | `contractCandRepo` | `contractCandidateRepo` |
| eval Observation repo | （record 无） | `contractObservationRepo` |
| eval Review item fixture | `contractReviewItem` | `contractReviewItem`（→ 导出 `ReviewItem`） |
| eval Review repo | `contractReviewRepo`（同名） | `contractReviewRepo` |
| audit Query repo | `contractAuditRepo`（同名） | `contractAuditRepo` |
| 其余同名共享 | `contractAgentRepo`/`contractOpPropRepo`/`contractOpUsageRepo`/`contractTenantRole`/`contractDashboardRepo`/`contractQueryRepo` | 同名 |
| sched | `contractSchedulerStub` + `contractSchedRepo/Runner/Resolver` | 同名 |

> 校验 tip：取两文件对同名 port 的 stub 逐方法 diff；存在行为差异时以 **test（校验器，CI 守护 golden 真相）** 为准。canonical 名称一律取 test 当前名，使 test 侧 diff 最小、recorder 侧改名适配。

### B. `findRepoRoot` 3 → 1

新建 **`pkg/reporoot`**（`package reporoot`，`func Find() string`），函数体取 `cmd/coverage-gap/main.go:497` 版本（自 cwd 上溯找 `go.mod`；找不到返 `"."`——2/3 现场现状）。

调用点适配（三处行为保持）：

- `cmd/coverage-gap/main.go`：删 `findRepoRoot`，`repoRoot := reporoot.Find()`。
- `cmd/coverage-gap-db/main.go`：同上。
- `internal/agent/infrastructure/officialdocs/generate/main.go`：删 `findRepositoryRoot`，改为
  `root := reporoot.Find(); if root == "." { return "", errors.New("repository root with go.mod not found") }`——与现 `(string, error)` 语义等价（今日成功路径返回绝对目录；失败返回该 error，绝不返 `"."`）。

## 非去重（显式保留，Out of scope）

- `mustGeneratePEM` ×2：record 版 `func mustGeneratePEM() string`（失败 `panic`）、test 版 `func mustGeneratePEM(t *testing.T) string`（`t.Fatal`），各 8 行、签名与失败策略不同、消费方各自私有。刻意不合并。
- `dropStale` ×2：取证为不同函数，各单点使用。不去重。
- 21 条「有路由、无已提交 golden」的既有覆盖缺口（memory/workflow/admin 新端点；regen 会产生 21 个未跟踪 golden）：**预存缺口，不在本 PR 修复**（另见验证基线）。
- recorder 独有路由逻辑（`main`、`record*`、`goldenName`、`resolvePath`、`isReviewRoute`/`isDDDAuthOverride`、`Case`）与 test 独有逻辑（`contractCase`、`TestContracts`、`jsonEquivalent`、`mustGeneratePEM(t)`）保留原地，不共享。

## 验证与守护

**验证基线（实测于 clean worktree）**：`make contract-test` 绿。`make record-contracts` 后 `git status` 显示：174 个已跟踪 golden 仅「文件尾去换行」diff（无任何真实行变更），另生成 21 个未跟踪 golden（预存缺口）。即录制器当前语义保真，且提交态与录制产物**仅差文件尾 `\n`**。

**成功后守护（PR 内本地执行）**：

1. `go vet ./...` 与 `go build -tags=contracts ./scripts/record-contracts.go` 通过（shared 包无 tag，普通路径与 tagged 路径都编译）。
2. `make contract-test` 绿（校验器未变语义）。
3. **录制确定性守护**：`make record-contracts` 后
   - 已跟踪 golden：`git status --short` 必须为 **0 变更**（录制器补 `\n` 后与提交态逐字节一致；证明收编 + `writeGolden` 后录制产物 = 基线产物 + `\n`，无任何语义漂移）；
   - 仅 21 个未跟踪 golden 出现（预存覆盖缺口，与基线同集，未被本改动改变）。
   随后删除 21 个未跟踪文件并 `git checkout --` 已跟踪（如有），PR 不携带任何 golden 改动。
4. 门禁：新包满足 code-quality（圈复杂度 ≤10 等；stub 为一行空实现天然满足）。

## 成功标准

- `api/http/contracttest/` 成为契约 stub 与容器装配的**唯一事实源**；`record-contracts.go` 与 `contract_test.go` 不再各自定义任何 `contract*` stub 或容器字面量。
- `pkg/reporoot` 成为 repo-root 查找的唯一实现，3 个调用点行为保持。
- `make record-contracts` 对已跟踪 golden 产出**零 diff**（录制器 `writeGolden` 补 `\n` 后确定性对齐提交态）。
- 全部守护通过；golden 文件零改动；PR 无行为回归。

## 风险与缓解

- **校验器语义漂移**：stub/容器逐方法迁移后 test 行为必须不变 → 守护 2（contract_test 绿）+ 迁移期逐方法 diff。
- **录制器行为漂移**：canonical 容器取 test 超集，recorder 增益 `ObservationService`。基线已证录制产物对 observation 路由不受该装配影响（见验证基线）；仍以守护 3 的「已跟踪 golden 零变更」实证。
- **build tag/import 环**：`contracttest` 不 import `api/http`（`BuildContainer` 返回 `*wiring.Container`，路由构建留在消费方），无环。recorder 仅在有 `contracts` tag 时编译。
- **未使用 import**：迁移后两消费方 import 需修剪；`goimports`/`go vet` 兜底。
