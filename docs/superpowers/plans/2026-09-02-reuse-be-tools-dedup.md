# Reuse BE3 实施计划：contract harness 分叉收编 + findRepoRoot 合一

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. 本改动高度耦合（两个大文件 + 新共享包 + golden 守护），建议**本会话内联执行**（executing-plans），勿并行 subagent。

**Goal:** 把 golden 契约 harness 的约千行双写收编到 `api/http/contracttest` 单一事实源，把 `findRepoRoot` 3 处合一到 `pkg/reporoot`，行为零回归。

**Architecture:** 新建可 import 的 `api/http/contracttest`（无 build tag），容纳契约 stub / fixture / `BuildContainer`；`record-contracts.go` 与 `contract_test.go` 各自删 stub+容器字面量、改 import。recorder 写入抽 `writeGolden` 补文件尾 `\n`，使 regen 与提交态逐字节一致。`findRepoRoot` 合入 `pkg/reporoot.Find()`。

**Tech Stack:** Go（无新依赖）。canonical 语义源 = `api/http/contract_test.go`（CI 守护 golden 真相）。

## Global Constraints

- **canonical stub 集与容器语义 = `api/http/contract_test.go`**（逐方法迁移，test 绿为行为标尺）。
- 除 Task 5b（经用户裁决扩 scope）外，golden 文件**零改动**；Task 5b 仅提交 recorder 对齐后能绿回放的对账 golden。
- recorder 独有路由逻辑、test 独有回放逻辑原地保留（spec「非去重」节）。
- commit message 以 `Co-Authored-By: Claude <noreply@anthropic.com>` 结尾；PR body 以 `🤖 Generated with [Claude Code](https://claude.com/claude-code)` 结尾。
- git mutating 一律单行 `cd /home/yang/go-projects/stratum-be-tools-dedup && git ...`（primary-checkout hook 按 cwd 判定，换行后独立段会误判）。

---

### Task 1: `pkg/reporoot` + 3 调用点合一

**Files:**

- Create: `pkg/reporoot/reporoot.go`
- Modify: `cmd/coverage-gap/main.go:497-508`（删 `findRepoRoot`，改 :78 调用）
- Modify: `cmd/coverage-gap-db/main.go:782-793`（删 `findRepoRoot`，改 :79 调用）
- Modify: `internal/agent/infrastructure/officialdocs/generate/main.go:280-294`（删 `findRepositoryRoot`，改调用点）
- Test: 无（纯移动；`go build ./cmd/...` 编译验证 + 单测在 Task 6）

**Interfaces:**

- Produces: `func Find() string` —— 自 `os.Getwd()` 上溯找含 `go.mod` 的目录；找不到返 `"."`。

- [ ] **Step 1: 新建共享实现**

`pkg/reporoot/reporoot.go`：

```go
// Package reporoot locates the repository root from the current working
// directory by walking up until a directory containing go.mod is found.
package reporoot

import (
	"os"
	"path/filepath"
)

// Find returns the absolute directory that contains go.mod, walking up from
// cwd. It returns "." when no go.mod is found on the way to the filesystem
// root, matching the historical contract of the CLI tools that call it.
func Find() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
```

- [ ] **Step 2: `cmd/coverage-gap/main.go`**

删除 :497-508 `findRepoRoot` 整函数；文件头 import 组加 `"github.com/byteBuilderX/stratum/pkg/reporoot"`（放 stdlib 与 internal 之间？否——按现有分组：third-party 后、internal 前新增一行 `"github.com/byteBuilderX/stratum/pkg/reporoot"`）；:78 `repoRoot := findRepoRoot()` → `repoRoot := reporoot.Find()`。

- [ ] **Step 3: `cmd/coverage-gap-db/main.go`**

同样：删 `findRepoRoot` 整函数（:782-793），加 import，:79 改 `reporoot.Find()`。

- [ ] **Step 4: `internal/agent/infrastructure/officialdocs/generate/main.go`**

删除 `findRepositoryRoot` 整函数（:280-294 含 `os.Getwd`/`filepath` 若成孤立 import 一并清）。调用点改为：

```go
	root := reporoot.Find()
	if root == "." {
		return "", errors.New("repository root with go.mod not found")
	}
```

（保 `(string, error)` 签名与调用方契约。）

- [ ] **Step 5: 编译验证**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && go build ./cmd/coverage-gap/ ./cmd/coverage-gap-db/ ./internal/agent/infrastructure/officialdocs/generate/ && go vet ./pkg/reporoot/`
Expected: PASS（无输出/无错误）。

- [ ] **Step 6: Commit**

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup
git add pkg/reporoot/reporoot.go cmd/coverage-gap/main.go cmd/coverage-gap-db/main.go internal/agent/infrastructure/officialdocs/generate/main.go
git commit -m "$(cat <<'EOF'
refactor(reuse): findRepoRoot 3 处合一到 pkg/reporoot.Find

coverage-gap / coverage-gap-db / officialdocs generate 三份自 cwd 上溯
找 go.mod 的实现合为 pkg/reporoot 单一事实源，三调用点行为保持
（前两者失败返 "."；officialdocs 把失败映射回其 error）。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `api/http/contracttest` 共享 stub 集（canonical 自 test 逐字搬移）

**Files:**

- Create: `api/http/contracttest/stubs.go`
- Create: `api/http/contracttest/container.go`（Task 3）
- Create: `api/http/contracttest/review.go`（fixture + review/observation stub，Task 2 一并）
- Source（逐字搬移源）：`api/http/contract_test.go`

**Interfaces:**

- Consumes: 各域 `internal/<ctx>/domain/port` 接口 + `internal/<ctx>/application` 构造函数（现 contract_test.go 已用，import 照搬）。
- Produces: 未导出 stub 类型集（表 1 canonical 名）+ 未导出 `reviewItem()` + `schedulerStub` + `errStubNotFound`。本包只被 `BuildContainer` 使用，先不导出。

- [ ] **Step 1: 建 `stubs.go` 头 + 逐字搬移 llm/workflow 组**

从 `api/http/contract_test.go` **原样复制**（含注释、空实现风格），迁入 `contracttest/stubs.go`，类型重命名按 spec 表 1：

- :265-293 `contractProviderRepo`（原名同）——不动
- :294-311 `contractModelRepo`——不动
- :312-320 `contractProviderRuntime`——不动
- :321-344 `contractDefRepo`——不动
- :345-362 `contractVersionRepo`——不动
- :363-386 `contractRunStore`——不动
- :387-407 `contractControlRepo`——不动
- :408-415 `contractAgentExecutor`——不动

`stubs.go` 头（import 自 contract_test.go :1-56 剔除 test-only 项后照搬；以 goimports 兜底）：

```go
package contracttest

import (
	"context"
	"time"
)
// …后续 stub 定义沿用 contract_test.go 同名类型体的 import 需求，编译报错逐个补齐。
```

（实际以移动代码的最小 import 集为准，Step 6 `go build` 验证。）

> 搬移规则：**改包名/删 `*testing.T` 无关项之外一律逐字**；同名类型在两文件均已一致（spec 表 1 逐方法核对过），canonical 取 test。

- [ ] **Step 2: 搬移 iam/agent 组**

- :416-436 `contractAdminUserRepo`、:437-461 `contractAdminTenantRepo`、:462-492 `contractTenantRepo`、:493-506 `contractInvitationRepo`、:507-512 `contractDashboardRepo`、:513-535 `contractAgentRepo`、:536-562 `contractOpPropRepo`、:563-575 `contractOpUsageRepo`、:576-581 `contractTenantRole`、:582-610 `contractProposalRepo`、:611-619 `contractProposalAuthorizer` —— 逐字复制到 `stubs.go`。

- [ ] **Step 3: 搬移 eval 组 + fixture 到 `review.go`**

- :625-648 `contractQueryRepo`、:649-679 `contractExperimentRepo`、:680-687 `contractCandidateRepo`、:688-723 `contractObservationRepo` —— 逐字复制（可放 `stubs.go`）。
- :724-742 `contractReviewItem()` → 改名 `reviewItem()`（包内私有；保留原注释），连同 :743-775 `contractReviewRepo`（含 MarkReviewed/CreateCalibrationSample/CreateAttributionEntry/CountPending）放 `review.go`。

`review.go` 头：`package contracttest`，import `"context"`、`"time"`、`"github.com/byteBuilderX/stratum/internal/evaluation/domain"`、`"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"`（以实际为准）。

- [ ] **Step 4: 搬移 audit/sched 到 `container.go` 或 `stubs.go`**

- :776-787 `contractAuditRepo`、:788-792 `schedulerStub`（由 `contractSchedulerStub` 改名，签名为 `func schedulerStub(logger *zap.Logger) *schedapp.Service`，体照搬 :788-792 调 `schedapp.NewService(contractSchedRepo{}, contractSchedRunner{}, contractSchedResolver{}, logger)` —— **核对原签名形参**，:793-817 `contractSchedRepo`、:818-823 `contractSchedRunner`、:824-835 `contractSchedResolver`。
- `errStubNotFound`（contract_test.go :263）随 stub 移入本包（未导出）。

- [ ] **Step 5: 一致性核对**

Run: `diff <(sed -n '265,619p' api/http/contract_test.go) <(sed -n '1,$p' api/http/contracttest/stubs.go | 过滤改名差异)`（人工/脚本复核 spec 表 1 各同名 port 方法集一致；记录改名映射已全量应用）。
Expected: 除 spec 表 1 改名外无方法集差异。

- [ ] **Step 6: 编译**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && go build ./api/http/contracttest/`
Expected: PASS（此处只有未导出符号 + 无导出 API，构建通过即合法；Task 3 之后才有消费方）。

- [ ] **Step 7: Commit**

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup
git add api/http/contracttest/
git commit -m "$(cat <<'EOF'
refactor(reuse): 契约 stub 集迁入 api/http/contracttest 单一事实源

自 contract_test.go 逐字搬移全部契约 stub/fixture，canonical 命名取
test 全称；consumers 待 Task 4/5 改 import 后删除各自副本。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `BuildContainer` canonical 装配

**Files:**

- Modify: `api/http/contracttest/container.go`

**Interfaces:**

- Consumes: Task 2 stub 集；`api/wiring`、`config`、`observability`、`iamtoken`、各域 application。
- Produces: `func BuildContainer(cfg *config.Config, key *rsa.PrivateKey, logger *zap.Logger, metrics *observability.PrometheusMetrics) *wiring.Container`

- [ ] **Step 1: 写 `container.go`**

canonical 以 `scripts/record-contracts.go:73-144 buildDDDContainer` 为骨架（inline 复合字面量风格），施加：canonical 类型名（表 1）、atomic 计数器、**补 `ObservationService` 装配**、去掉「与 contract_test.go 保持同一注入」的同步注释改述。

```go
package contracttest

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/byteBuilderX/stratum/api/wiring"
	"github.com/byteBuilderX/stratum/config"
	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	iamapp "github.com/byteBuilderX/stratum/internal/iam/application"
	iamtoken "github.com/byteBuilderX/stratum/internal/iam/infrastructure/token"
	llmapp "github.com/byteBuilderX/stratum/internal/llmgateway/application"
	"github.com/byteBuilderX/stratum/internal/llmgateway/domain/port"
	"github.com/byteBuilderX/stratum/internal/observability"
	platformapp "github.com/byteBuilderX/stratum/internal/platform/application"
	schedapp "github.com/byteBuilderX/stratum/internal/scheduler/application"
	workflowapp "github.com/byteBuilderX/stratum/internal/workflow/application"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	"go.uber.org/zap"
)

// BuildContainer 装配契约 harness 用的确定性 DDD 容器：全部 stub 注入
// 各域 service，admin/tenant 角色固定（contractTenantRole 恒返 "admin"）。
// 这是 contract_test.go（校验 golden）与 scripts/record-contracts.go
// （录制 golden）共用的唯一事实源。
func BuildContainer(cfg *config.Config, key *rsa.PrivateKey, logger *zap.Logger, metrics *observability.PrometheusMetrics) *wiring.Container {
	var idCounter atomic.Int64
	nextID := func() string { return fmt.Sprintf("contract-%d", idCounter.Add(1)) }
	return &wiring.Container{
		Config: cfg, Logger: logger,
		Platform: &wiring.Platform{
			JWTService: iamtoken.NewJWTService(key), Metrics: metrics,
			DashboardService: platformapp.NewDashboardService(contractDashboardRepo{}),
		},
		LLMGateway: &wiring.LLMGateway{
			ProviderService:  llmapp.NewProviderService(contractProviderRepo{}, contractModelRepo{}, contractProviderRuntime{}),
			ModelMgmtService: llmapp.NewModelMgmtService(contractModelRepo{}),
		},
		Skill: &wiring.Skill{}, MCP: &wiring.MCP{}, Memory: &wiring.Memory{},
		Agent: func() *wiring.Agent {
			gate := agentapp.NewOperationGateService(
				contractOpPropRepo{}, contractOpUsageRepo{}, metrics,
			)
			svc := agentapp.NewAgentService(agentapp.AgentServiceDeps{
				Registry: agentapp.NewRegistry(contractAgentRepo{}, logger),
				Logger:   logger,
				Metrics:  metrics,
			})
			svc.SetOperationGate(gate)
			return &wiring.Agent{
				ProposalService: agentapp.NewResourceChangeProposalService(
					contractProposalRepo{}, contractProposalAuthorizer{}, nil, nil, metrics,
				),
				OperationGateService: gate,
				OperationProposalSvc: agentapp.NewOperationProposalService(
					contractOpPropRepo{}, contractTenantRole{}, metrics,
				),
				Service: svc,
			}
		}(),
		Workflow: &wiring.Workflow{
			DefinitionService: func() *workflowapp.DefinitionService {
				svc := workflowapp.NewDefinitionService(contractDefRepo{}, contractVersionRepo{}, nextID)
				// 所有权矩阵单事实源：契约 harness 固定 admin 角色，注入后
				// admin 的 Update/Publish/Validate 走 OpEdit 放行，Delete 走
				// createdBy==actorID 校验（stub 空 createdBy → 403，预期语义）。
				svc.SetTenantRoleResolver(contractTenantRole{})
				return svc
			}(),
			RunService:     workflowapp.NewRunService(contractVersionRepo{}, contractRunStore{}, contractAgentExecutor{}, nextID),
			ControlService: workflowapp.NewControlService(contractControlRepo{}, nextID),
		},
		Knowledge: &wiring.Knowledge{},
		Evaluation: &wiring.Evaluation{
			SuiteService: evalapp.NewSuiteService(nil), JobService: evalapp.NewJobService(nil, nil, nil),
			QueryService:       evalapp.NewQueryService(contractQueryRepo{}),
			ExperimentService:  evalapp.NewExperimentService(contractExperimentRepo{}),
			CandidateService:   evalapp.NewCandidateCommandService(contractCandidateRepo{}),
			ObservationService: evalapp.NewObservationService(evalapp.ObservationServiceDeps{
				Repo: contractObservationRepo{}, Logger: logger,
			}),
			ReviewService: evalapp.NewReviewService(evalapp.ReviewServiceDeps{
				Repo: contractReviewRepo{}, Logger: logger,
			}),
		},
		IAM: &wiring.IAM{
			AdminService: iamapp.NewAdminService(
				contractAdminTenantRepo{},
				iamapp.WithUserRepo(contractAdminUserRepo{}),
			),
			TenantService:     iamapp.NewTenantService(contractTenantRepo{}, logger),
			InvitationService: iamapp.NewInvitationService(contractInvitationRepo{}),
		},
		Scheduler: &wiring.Scheduler{Service: schedulerStub(logger)},
		Audit:     &wiring.Audit{QueryService: contractAuditRepo{}},
	}
}
```

（import 组按 gofmt/项目分组整理；`observability` 实际路径为 `github.com/byteBuilderX/stratum/pkg/observability`——以 contract_test.go :1-56 的既有 import 为准修正；`context` 若不被本文件用则去掉。）

- [ ] **Step 2: 编译**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && go build ./api/http/contracttest/`
Expected: PASS。未使用 import 用 gofmt -l 提示并清理。

- [ ] **Step 3: Commit**

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup
git add api/http/contracttest/container.go
git commit -m "$(cat <<'EOF'
refactor(reuse): contracttest.BuildContainer canonical DDD 装配

canonical 语义取 contract_test.go（超集，含 ObservationService），结构
沿用 recorder 的 buildDDDContainer inline 风格，nextID 用 atomic 计数器。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `contract_test.go` 改 import（删 stub + 容器字面量）

**Files:**

- Modify: `api/http/contract_test.go`

**Interfaces:**

- Consumes: `contracttest.BuildContainer`
- Produces: 精简后的校验器（stub 零本地定义）

- [ ] **Step 1: 替换容器字面量为单行调用**

把 `dddRouter := apihttp.NewRouter(&wiring.Container{ … })`（:90-162 区）整体替换为：

```go
	dddRouter := apihttp.NewRouter(contracttest.BuildContainer(cfg, key, logger, metrics))
```

删除其前预声明局部变量（:88-99 `contractProviderRepo := …` 等 12 行别名块）。

- [ ] **Step 2: 删除全部 stub 定义**

从 `// ── Stub repositories` 段（:264 起 `var errStubNotFound`）到文件尾，**仅保留** `jsonEquivalent`（:620-624）与 `mustGeneratePEM(t)`（:836-845）。即：删 :264-619、:625-835 两区（stub/audit/sched/fixture），jsonEquivalent 与 mustGeneratePEM(t) 移到文件靠后保留区。
顶部 import 组加 `"github.com/byteBuilderX/stratum/api/http/contracttest"`；删除因 stub 搬移而孤立的 import（`goimports`/`go vet` 提示逐一清；`contract_test.go` 仍需保留 `wiring`？——不再直接引用 `wiring.Container` 后 `api/wiring` import 移除；`iamtoken` 若仅容器用则移除；逐项以编译报错为准）。

- [ ] **Step 3: 校验器行为守护**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && make contract-test`
Expected: `ok github.com/byteBuilderX/stratum/api/http`（与基线一致；golden 未动）。

- [ ] **Step 4: Commit**

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup
git add api/http/contract_test.go
git commit -m "$(cat <<'EOF'
refactor(reuse): contract_test.go 改 import contracttest.BuildContainer

删除本地 stub/容器字面量，DDD 容器改由共享 BuildContainer 装配；校验
golden 行为不变（contract-test 绿）。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `record-contracts.go` 改 import + `writeGolden` 补换行

**Files:**

- Modify: `scripts/record-contracts.go`

**Interfaces:**

- Consumes: `contracttest.BuildContainer`
- Produces: recorder（stub 零本地定义；regen 与提交态逐字节一致）

- [ ] **Step 1: 删 `buildDDDContainer` + 全部 stub**

删除：`buildDDDContainer`（:73-143）、`contractSchedulerStub`（:144-150）、audit/sched stub 块（:151-203）、prov/mod/…/dashboard stub 块（:500-963，含 `contractReviewItem` :797-815）、`errStubNotFound`（:71）。`main()` 里 `ddRouter := apihttp.NewRouter(buildDDDContainer(cfg, key, logger, metrics))` → `contracttest.BuildContainer(cfg, key, logger, metrics)`。

保留：:204-… `isDDDAuthOverride`、:224 `main`、:263-459 各 `record*`/`goldenName`/`resolvePath`/`isReviewRoute`、:60 `Case`、:964 `mustGeneratePEM()`。import 组加 `contracttest`，删孤立 import（`wiring` 若不再引用移除；`iamtoken`/`config`/`observability` 仍被 main 用，保留）。

- [ ] **Step 2: 加 `writeGolden` + 替换 5 处写入**

文件尾部（`mustGeneratePEM` 后）新增：

```go
// writeGolden 写契约 golden 文件，统一补文件尾换行，使录制产物与提交态
// 逐字节一致（regen 零 diff 守护依赖）。
func writeGolden(outPath string, out []byte) {
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		panic(fmt.Errorf("write golden %s: %w", outPath, err))
	}
}
```

把 5 处 `os.WriteFile(outPath, out, 0o644)`（:344-346、:377-379、:422-424、:452-454）与 :494-495 `_ = os.WriteFile(outPath, out, 0o644)` 统一替换为 `writeGolden(outPath, out)`。若该文件已 import `os`（main 里 `os.Args`/`os.MkdirAll` 用）则 `os` 保留；`fmt` 已有。

- [ ] **Step 3: tagged 编译**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && go build -tags=contracts ./scripts/record-contracts.go`
Expected: PASS。

- [ ] **Step 4: 录制确定性守护（regen 零 diff）**

Run:

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup && make record-contracts >/dev/null 2>&1; echo "tracked dirty: $(git status --short api/http/testdata/contracts | grep -v '^??' | wc -l)"; echo "untracked new: $(git status --short api/http/testdata/contracts | grep -c '^??')"
```

Expected: `tracked dirty: 0`；`untracked new: 21`（预存覆盖缺口文件，与基线同集）。

若 `tracked dirty: 0` 成立 → 收编后录制产物与提交态逐字节一致（spec 守护 3 达成）。然后清理：

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup
git status --short api/http/testdata/contracts | grep '^??' | sed 's/^?? //' | xargs -r rm
git status --short api/http/testdata/contracts
```

Expected: 空（无任何 golden 变更）。

- [ ] **Step 5: Commit**

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup
git add scripts/record-contracts.go
git commit -m "$(cat <<'EOF'
refactor(reuse): record-contracts.go 改 import contracttest + writeGolden

删除本地 stub/容器定义改由共享 BuildContainer；录制器输出统一补文件尾
换行，regen 与提交态逐字节一致（已跟踪 golden 零 diff 实测）。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 5b: recorder 鉴权对齐 verifier + golden 对账提交（经用户裁决扩 scope）

> **背景（用户裁决，2026-09-02）**：Task 5 的 guard「已跟踪 golden 零 diff」因**既有** recorder↔提交态漂移无法达成（11 内容 + 6 换行 + 21 未跟踪，改动前即存在，已对照旧 recorder 产物实证）；spec「golden 零改动」与「tracked-dirty:0」互斥。用户选择**扩 scope**：修 recorder 鉴权 → 重录使 11 个内容分歧归零 → 提交 6 换行 + 21 新增 goldens → regen guard 真正归零。verifier（`api/http/contract_test.go`）是 canonical truth，**不得改动**。

**Files:**

- Modify: `scripts/record-contracts.go` 的 `isDDDAuthOverride`
- Regenerate: `make record-contracts`
- Test: `make contract-test`、`go vet ./...`、`go build -tags=contracts ./scripts/record-contracts.go`

**Root cause（controller 已实证）**：recorder 与 verifier 对同一批 `/admin/*` 路由签发不同的 JWT claims：

| 前缀 | recorder（现） | verifier（truth） |
|---|---|---|
| `/admin/providers`、`/admin/models` | `{Role:"admin"}` → router 读 global_role → 403 | `{Role:"admin", GlobalRole:"global_admin", TenantID}` → 200/400 |
| `/admin/users` | `{GlobalRole:"global_admin"}`（缺 TenantID/Role） | 同上全量 |
| `/admin/tenants`、`/admin/admins` | `{GlobalRole:"global_admin"}` | 同上全量 |
| 其余 DDD 前缀（`/tenant/`、`/workflows` 等） | `{Role:"admin", TenantID}` | `{Role:"admin", TenantID}`（一致） |

11 个内容分歧 = 8 个 `/admin/providers|models` + 2 个 `/admin/users`(tenant_members) 写路由（recorder 403/缺字段 vs committed 200）+ `delete_workflows__id`（独立成因，须诊断）。

- [ ] **Step 1: 对齐 `isDDDAuthOverride` 至 verifier claims**

```go
func isDDDAuthOverride(routePath string) (bool, iamport.TokenClaims) {
	adminFull := iamport.TokenClaims{Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin", GlobalRole: "global_admin"}
	adminClaims := iamport.TokenClaims{Sub: "contract-admin", TenantID: "contract-tenant", Role: "admin"}
	switch {
	case strings.HasPrefix(routePath, "/admin/tenants"),
		strings.HasPrefix(routePath, "/admin/providers"),
		strings.HasPrefix(routePath, "/admin/models"),
		strings.HasPrefix(routePath, "/admin/admins"),
		strings.HasPrefix(routePath, "/admin/users"):
		return true, adminFull
	case strings.HasPrefix(routePath, "/tenant/"), strings.HasPrefix(routePath, "/workflows"),
		strings.HasPrefix(routePath, "/workflow-runs"), strings.HasPrefix(routePath, "/workflow-approvals"),
		strings.HasPrefix(routePath, "/operation-proposals"), strings.HasPrefix(routePath, "/scheduled-tasks"),
		strings.HasPrefix(routePath, "/audit"):
		return true, adminClaims
	default:
		return false, iamport.TokenClaims{}
	}
}
```

> 对照 verifier：admin 集合 = {`/admin/tenants`,`/admin/providers`,`/admin/models`,`/admin/admins`,`/admin/users`} → 全量 claims；其余 useDDD → adminClaims。verifier 不把 `/admin/*` 之外的任何 route 签成 global_admin，本函数同理。

- [ ] **Step 2: 重录 + 分类剩余 diff**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && make record-contracts && git status --short`
Expected：8 provider/model + 2 user 内容分歧**归零**（recorder 现与 committed 逐字节一致）；若有残余停下诊断。仍剩 tracked diff：`delete_workflows__id` + 6 个仅缺尾 `\n` 文件；21 个未跟踪新 golden 出现。

诊断 `delete_workflows__id`：对照 recorder 产物与 committed 内容，判断差异成因（命名 / recorder 顺序状态副作用 / 鉴权）。若 recorder 产物是顺序副作用造成、在 fresh container 回放不一致，则**不采纳 recorder 产物**，保留 committed 内容并说明；若 recorder 产物语义正确且回放绿，则采纳随本次提交。

- [ ] **Step 3: 实证 gate —— 全量产物下 contract-test 必须绿**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && go build -tags=contracts ./scripts/record-contracts.go && make contract-test`
Expected: PASS。注意 21 个新 golden 现在在 testdata 目录内，contract_test glob 会**实际回放**它们。凡不能绿回放的新 golden（路径不在 `dddPrefixes` 会走 legacy router 而红，或鉴权不匹配），**不得提交**：从 testdata 删除该文件使其回归「预存缺口」状态，并在报告逐条列出被排除者与原因。已跟踪 content 改动同理必须绿。

- [ ] **Step 4: 提交可绿集合并再次验证确定性**

提交 6 个补尾换行的 tracked golden + 通过 Step 3 的未跟踪新 golden（不含被排除者）。commit 如实说明，不伪绿。提交后再次 `make record-contracts && git status --short`：预期 tracked **零 diff**，被排除者之外无未跟踪残留（被排除者每次 regen 重现，属预存缺口）。

### Task 6: 收尾门禁 + 回归

- [ ] **Step 1: 全量快速验证**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && go vet ./... && go test -short ./...`
Expected: PASS。golden 相关零 diff（`git status --short` 应只剩本 PR 5 个改动文件的增删）。

- [ ] **Step 2: code-quality 门禁**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && make code-quality`
Expected: 无新增超限（新函数为 stub 空实现 / 单行 helper，天然满足）。

- [ ] **Step 3: 复核 PR 变更集边界**

Run: `cd /home/yang/go-projects/stratum-be-tools-dedup && git log --oneline origin/main..HEAD && git diff --stat origin/main...HEAD`
Expected: commits（Task1-5 + Task5b + spec/plan 文档），改动仅限 `pkg/reporoot/`、`api/http/contracttest/`、`api/http/contract_test.go`、`scripts/record-contracts.go`、`cmd/coverage-gap/main.go`、`cmd/coverage-gap-db/main.go`、`internal/.../officialdocs/generate/main.go`、`api/http/testdata/contracts/`（Task 5b 对账提交的 golden，经用户裁决）、`docs/superpowers/`。

- [ ] **Step 4: push + PR（等待 CI 期间查 base 落后）**

```bash
cd /home/yang/go-projects/stratum-be-tools-dedup
git push -u origin feat/reuse-be-tools-dedup
gh pr create --base main --title "refactor(reuse): 收编 contract harness 分叉 + findRepoRoot 合一" --body "…
What: record-contracts.go ↔ contract_test.go 约千行 stub/容器双写收编到 api/http/contracttest；findRepoRoot 3 处合一；recorder 鉴权对齐 verifier（isDDDAuthOverride → global_admin）+ golden 对账提交（Task 5b，经裁决）。
Why: 消除人工双写的漂移源（已现 ObservationService/21 缺口类差异）；录制器补换行 + 对齐鉴权使 regen 与提交态逐字节一致，已跟踪 golden 零 diff。
HowToTest: make contract-test 绿；make record-contracts 后 git status 对已跟踪 golden 零 diff；go build -tags=contracts ./scripts/record-contracts.go 通过。
🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

## Self-Review

1. **Spec 覆盖**：A 收编（Task 2/3/4/5）、writeGolden（Task 5）、B findRepoRoot（Task 1）、误报排除（无 task，spec 已述）、守护（各 task Step + Task 6）——全覆盖。
2. **占位符扫描**：无 TBD/TODO；新代码均给全量；搬移引用给出确切源范围与指令。
3. **类型一致性**：canonical 名在 Task 2/3 映射一致（ProviderRepo/VersionRepo/AdminTenantRepo/ProposalRepo/ExperimentRepo/CandidateRepo/ObservationRepo/ReviewRepo 等）；`BuildContainer` 签名在 Task 3 定义、Task 4/5 调用一致；`Find()` 在 Task 1 定义、Task 1 消费一致。
