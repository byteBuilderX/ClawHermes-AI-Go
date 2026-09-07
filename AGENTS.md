<!-- generated; do not edit directly -->
<!-- source: docs/agent/instructions.md + docs/agent/templates/agents-prefix.md -->

> Codex entry: this generated `AGENTS.md` applies to the repository root.

More deeply nested `AGENTS.md` files may add narrower rules for their subtree; direct user instructions remain higher priority.

---

# Stratum project instructions

## Default principle

正确 > 清晰 > 速度。有疑问先问，不默默猜测。先读完相关文件、接口和调用链，声明假设，选择最小正确解，只做任务相关修改；冲突必须选定一种方案，禁止静默混合。先定义成功标准，测试应验证业务意图；所有跳过、不确定和部分失败都必须暴露。路由、重试和状态机等控制逻辑必须硬编码，AI 只做语言任务。

## Knowledge input and evidence

架构设计、技术方案、重大重构，以及 Agent、Memory、Workflow、安全或数据治理任务，必须同时检查：

1. 仓库代码、测试、ADR 和真实运行证据；
2. `obsidian` MCP 中相关的已验证/evergreen 技术观点、案例、踩坑和变更记录；
3. 官方文档、标准、原始论文或上游源码中的当前外部证据。

仓库事实以代码、测试和运行结果为准。Obsidian 是只读长期知识输入，`provisional` 内容只能作为未核验线索；搜索摘要不能作为关键证据。来源冲突时记录版本、范围和反例，不得静默选择。知识写回是独立蒸馏任务。完整协议必须通过已配置的 `obsidian` MCP 读取 vault-relative 资源 `99-系统/知识输入与证据检索协议.md`。

## Technology and directory map

- 后端使用 Go 1.25.12（以 `go.mod` 为准）。入口 `cmd/server/main.go` 通过 `api/wiring.BuildContainer` 构图；HTTP 路由、handler、DTO 位于 `api/http/`，middleware 位于 `api/middleware/`（`body_limit`、`rate_limit`、`public_error`、`require_default_tenant`、`system_role_check` 等），组合根位于 `api/wiring/`。
- 业务上下文位于 `internal/<ctx>/{domain,application,infrastructure}`。当前上下文为 `agent`、`audit`、`collab`、`evaluation`、`iam`、`knowledge`、`llmgateway`、`mcp`、`memory`、`parameters`、`platform`、`scheduler`、`skill`、`workflow`。
- 通用基础设施位于 `pkg/`：`constants`、`crypto`、`dag`、`httpclient`、`jsonschema`、`messaging`、`migration`、`observability`、`postgres`、`reqctx`、`safetext`、`storage/{milvus,postgres,redis,filestore,objectstore,tenantnaming}`、`tenantdb`、`textchunk`、`timeutil`、`tokenutil`。`pkg/vector` 仅兼容旧 import，新代码使用 `pkg/storage/milvus`。
- 关键后端依赖：Gin v1.9.1、NATS v1.51.0（JetStream）、Milvus SDK v2.4.2、pgx v5.9.2、go-redis v9.7.3、golang-jwt v5.3.1、OTEL v1.42.0、Zap v1.27.1、minio-go v7、unidoc/unioffice、modelcontextprotocol/go-sdk、robfig/cron、bufbuild/protocompile。
- 前端位于 `web/`，使用 React 18.3、Vite 6.4、Ant Design 5.20、React Router 7.18.2、Axios 1.18、TypeScript。代码按 `web/src/modules/` 业务域组织（`agent`、`approvals`、`audit`、`collab`、`dashboard`、`evaluation`、`iam`、`knowledge`、`llm`、`mcp`、`memory`、`operation-gate`、`parameters`、`scheduled-task`、`skill`、`workflow`），共享 API 客户端是 `web/src/services/client.ts`。
- 部署资源位于 `k8s/`、`helm/`、`grafana/`；模块的细节以本文件末尾索引为准。

## Remote environment

远端生产集群部署在阿里云 ECS，用于排查告警和运维诊断：

- **SSH 连接**：`ssh root@101.200.181.141`（已免密登录）
- **集群类型**：单节点 k3s v1.36.2
- **节点 IP**：172.20.139.203（内网 Pod 无法访问 localhost 时使用）
- **Prometheus**：`kube-prometheus-stack`（helm release: `kps`），namespace `monitoring`

**规则**：

- **只读操作**（无需确认）：`kubectl get/describe/logs/top`、`curl` health/metrics、`psql` SELECT、Prometheus 查询等
- **写入操作**（必须先获用户许可）：`kubectl apply/edit/patch/delete/set image/scale/rollout`、`helm install/upgrade/rollback`、`docker build+push` 后更新部署、修改 ingress/service/configmap/secret、重启 pod、数据库 DDL/DML 等。**E2E 验证优先使用本地 Docker，只有明确确认后才部署到远端**

## Architecture decisions

- PostgreSQL 采用多租户 schema 隔离；事务内通过 `SET LOCAL search_path` 切换，统一走 `pkg/tenantdb`。
- JWT 使用 RS256，网关可验证且无需共享签名密钥。
- 消息采用 NATS JetStream；持久化 subject 使用 `domain.action` 形式。
- GraphRAG 向量检索采用 Milvus；不要以 pgvector 平行实现同一能力。
- Harness 顺序启动组件、逆序关闭依赖，避免生命周期竞争。
- LLMGateway 屏蔽 Qwen、Zhipu 等 OpenAI-compatible provider 差异，业务层不直接绑定 provider。
- 业务数据按当前用例硬删除；审计记录遵循其独立保留策略。请求和启动路径不得擅自执行不可逆清理。

## Multi-tenant DDL and repository rules

- 编号迁移 `pkg/migration/sql/NNN_*.sql` 只操作 public schema，禁止引用 tenant-only 表。Tenant-only DDL 的唯一基线是 `pkg/storage/postgres/tenant_schema.sql`，由租户 provision 流程幂等应用；禁止在 `pkg/migration/sql/` 复制 tenant DDL。
- 新表和索引用 `IF NOT EXISTS`；新列必须紧跟 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 以升级历史租户。新增 `NOT NULL` 列须有安全默认值，或按 nullable → 回填 → 约束迁移。依赖新列的索引、约束和查询必须排在 backfill 后，并覆盖历史 schema 顺序测试。
- INSERT 与目标 DDL 必须逐列核对，尤其是无 DEFAULT 的 NOT NULL 列。数据库写入必须验证事务回滚和失败传播。`golang-migrate force <version>` 只用于把版本标为 clean，再由 `Up()` 继续；禁止手改 `schema_migrations`。
- 所有访问 tenant-scoped 表的 repository 方法必须通过 `execTenant(ctx, tenantID, fn)`，禁止直接调用 `r.pool.Exec/Query`；对应 port 方法必须显式包含 `tenantID string`。
- 删除租户向量数据必须调用 Milvus delete-by-filter，禁止 DropCollection；collection 命名遵循 `pkg/storage/milvus` 的现有实现。
- 功能替代旧存储时，同时删除旧表 DDL 和全部 Go 引用，并在 tenant schema 中添加兼容的清理语句处理存量租户；先确认代码引用为零，破坏性迁移必须单独审查和验证。
- 在 `execTenant` 外使用 `SET search_path` 时，连接释放前必须执行 `RESET search_path`；启动路径 SQL 必须用 `public.table_name` 等 schema-qualified 名称，禁止依赖连接残留状态。

## DDD layering and cross-context dependencies

- 依赖方向是 `handler → application → domain/port`；infrastructure 实现 port，由 `api/wiring/Container` 集中装配和逆序关闭。
- `pkg/` 不 import `internal/`；`domain/` 仅依赖 stdlib 和 `pkg/constants`；`application/` 不 import pgx、Redis、NATS 或 Gin；handler 不 import infrastructure 或存储驱动。
- 跨 context 接口定义在消费方 `domain/port/`，provider 由 infrastructure 实现，`api/wiring/` 只做薄 ACL 适配；禁止 import 兄弟 context 的 application 或 infrastructure。跨租户能力使用请求时 `Resolver(ctx, tenantID)` 延迟解析。
- DTO 只定义结构和 binding；handler 只做 bind、获取 tenant、调用 service、render，并用 `c.Error(err)` 交给统一错误中间件；application 负责编排、事务、鉴权和领域事件；domain 维护实体、不变量和算法；infrastructure 负责 IO 和错误翻译。
- wiring 禁止散写裸 SQL；表访问移到 infrastructure repository，事务和编排移到 application service。错误按 domain `Err*` → infrastructure 翻译 → application 编排 → middleware 映射 HTTP；冻结响应体 `{"error":"..."}` 兼容性。

## Git workflow

禁止在 `main` 分支直接提交或推送。必须使用仓库入口从最新 `origin/main` 创建隔离 worktree，禁止用原生 branch/worktree 命令绕过：

```bash
bash scripts/new-worktree.sh ../stratum-<feature> feat/<feature>
cd ../stratum-<feature>
git push -u origin feat/<feature>
gh pr create --base main
```

CI 全绿后合并，再用 `git worktree remove ../stratum-<feature>` 清理。Commit/PR 标题格式为 `[type](scope): description`，type 使用 `feat|fix|refactor|perf|test|docs|chore|ci`；PR 描述包含 What、Why、HowToTest。

push 触发 CI 后的等待期间，必须先检查 PR base 是否落后于最新 `origin/main`（`git fetch origin main` 后比较 base commit）。若落后：先把最新 main 合入分支，本地验证无冲突且测试通过后 push（merge commit 关联提交者），再继续等 CI。禁止在 base 落后状态下依赖当前 CI 结果或合并 PR——CI 的 merge ref 是 head+base 的动态合并，base 前进会改变实际合并结果。

## Development and end-to-end verification

- 编码前运行 `bash scripts/quality/risk-regression-guard.sh --explain`。后端快速验证：`go vet && go test -short ./...`；PR 前：`go test -v -race -timeout 30s ./...`。前端 PR 前：`make fe-lint && make fe-build`。依赖服务可用 `make infra-up`。
- Go 代码质量采用增量棘轮：新函数必须满足圈复杂度 ≤10、认知复杂度 ≤15、函数长度 ≤120 行、最大嵌套 ≤4；存量超限函数不得恶化。参数数 >6、文件长度 >800 和重复代码候选当前仅告警。运行 `make code-quality` 检查；基线只能通过 `make code-quality-baseline` 显式刷新，并与代码改动一同审查，禁止为通过门禁隐式更新。
- AI 生成测试前必须先读同域优质测试模板，复用 mock 和断言风格。代码是主、测试是行为契约；冲突时依据产品意图判断改实现或改测试，禁止为过测扭曲实现。
- API 兼容性由 `api/http/contract_test.go` 和 `api/http/testdata/contracts/*.golden.json` 守护。业务逻辑目标覆盖率 ≥80%，外部依赖须 mock，完整套件使用 `-race`。
- HTTP JSON 参数契约的唯一事实源是 `proto/` 下的 .proto 文件；前后端类型由 `protoc-gen-ginstruct` 生成（`api/http/dto/gen/`、`web/src/services/gen/`，不入 git）。改参数契约 = 改 proto 后 `make proto-gen`；绕过 make 直敲 `go test` 且未生成时 import 编译失败，属预期约束（与"生成物不入 git"配套）。仓库级残留由 `scripts/quality/dto-residue-guard.sh` 守卫（挂在 `make check`）。

## End-to-end testing and acceptance

### 测试门槛原则

- 只改字段 / 只改单个小 bug / 常量值调整 → 最小验证：unit + contract（`go test -short ./...` + contract tests）。
- 其余所有改动——功能、Bug 修复、前后端联调、数据库链路、Agent/Skill/MCP/Memory/Knowledge/IAM 能力改动——必须完整测试（`make test-verify-before-pr`），按 `.test/verification.yaml` 风险级升级：R2→e2e-short，R3→+e2e-soak，R4→+release-soak。
- 纯文案/措辞文档改动（不改文档结构、不改参数契约、不改变生成物）→ 最小验证：`markdownlint` + `make agent-instructions-check`（验证生成一致）。
- 文档结构性改动（新增/重排章节、改变 instructions.md 与 CLAUDE.md/AGENTS.md 的生成关系、修改 .proto 契约）→ 完整测试（`make test-verify-before-pr`），按 `.test/verification.yaml` 风险级升级。

### 验证执行方

- 系统验收由专用测试 agent `stratum-e2e-tester` 执行——它封装 `stratum-e2e-development` skill，定义见 `.claude/agents/stratum-e2e-tester.md`（本地 agent 定义，不入库，仅本仓库开发机可用）。
- 测试编写/设计/覆盖分析用 `agent-skills:test-engineer`。
- `stratum-e2e-development` 仍是 Claude Code 与 Codex 共用的唯一测试和验收 Skill。`browser_e2e_authority: local`、`merge_authority: ci`、`deployment_authority: release_pipeline` 分别管理本地浏览器、非浏览器 PR CI 和发布验证；local report 只是 developer audit assertion，不是 GitHub trusted status。

### 验收红线

- 创建 PR 前必须在 clean commit 上通过 `stratum-e2e-development` skill 完成系统验收；skill 内部按 `.test/verification.yaml` 自动选择本地无头 Chromium short 并运行 `make test-verify-before-pr`，R3 自动执行 `STATEFUL_E2E_PROFILE=test STATEFUL_E2E_DURATION_SEC=600 STATEFUL_E2E_PACKS=all` soak，R4 显式发布意图追加 `make e2e-system-release-soak` 的 3600 秒 release profile。`make test-verify-before-pr` 只是 skill 的落点之一，禁止绕过 skill 直接跑 make 或手工拼装 E2E 替代系统验收。
- 所有登录测试和验证流程必须使用无头浏览器（Playwright headless）；禁止为测试或验证启动有头浏览器。纯 API/单元测试不属于登录测试，但涉及登录态恢复时必须通过无头浏览器完成。
- 浏览器操作以 HTTP 和测试数据库凭据证据对账，但禁止输出 token、cookie、密钥、密码或原始 API key。
- failed/skipped/unreconciled capability、清理失败、残留实体或 stale report 都必须阻断；临时 Playwright、纯 API 或手工场景不能替代系统验收。

## Backend conventions

- Go 行宽 ≤120；import 按 stdlib、third-party、internal 分组。错误逐层用 `fmt.Errorf("operation: %w", err)` 包装；日志只用 Zap，禁止 `fmt.Print`。
- timeout、TTL、分页、topK、chunkSize、poolSize、retry 等行为数字禁止内联：跨包放 `pkg/constants/<domain>.go`，包内共享放 `internal/<pkg>/defaults.go`，单文件放本文件 `const` 块；名称包含 `Default`/`Max`/`Min` 或单位语义。
- 外部依赖必须有超时预算、有限重试、熔断/隔离和确定性关闭。瞬态错误指数退避基准 100ms、上限 10s；流式 LLM 不用 flat timeout，使用 header/idle timeout 和外层执行预算。
- 修改 port 后立即搜索并同步所有 test mock/stub。新增 tenant repository 时同时保证 `execTenant`、port 的 `tenantID` 和测试 mock。
- pgx v5 向 JSONB 写自定义 Go struct 时，先 `json.Marshal`，再传 `string(b)`；禁止直接传 struct 或 `pgtype.JSONB{}`。
- `context.WithTimeout` 必须在每次循环迭代内创建并及时 cancel；独立 IO 应有界并发，所有 goroutine 用 WaitGroup 跟踪，错误/停止路径 cancel 后必须 wait。
- 替换有状态连接/client/worker 时创建新实例、原子写回并关闭旧资源。共享 client 指针须在锁内捕获后使用，避免检查后被 `Close` 置空。超时后仍可能产出资源的 buffered channel 必须排水并关闭迟到资源。

## Code quality

门禁（圈复杂度 ≤10、认知复杂度 ≤15、行数 ≤120、嵌套 ≤4）是底线，以下原则从源头控制复杂度，而非事后拆分凑数：

### 决策阶梯（写码前必爬）

动手前从上往下取第一个成立的档位；先理解问题再爬梯——读完任务和触及的代码、trace 真实调用链，禁止跳过阅读直接开写：

1. 需要建吗？(YAGNI) —— 不需要就跳过。
2. 本仓库已有？复用现成 helper/util/pattern，禁止重写。
3. 标准库能搞定？用标准库（Go stdlib / React 内置 API）。
4. 平台原生能力有吗？用原生（浏览器原生控件、OS/数据库原生特性），优于自写。
5. 已装依赖里有？用依赖，禁止重复造轮子；确需新依赖时先过开源优先评估（license 兼容、社区活跃度、维护状态）。
6. 能一行完成？就写一行。
7. 以上都不行，才写能工作的最小实现。

允许懒的是「解决方案」，不允许懒的是：理解问题、信任边界校验、防数据丢失的错误处理、安全、无障碍、明确要求的功能。懒代码无检查不算完成：非平凡逻辑必须留一个可运行自检（assert 或小测试文件），一行代码免测。

### Functions

- **单一职责**：一个函数只做一件事，函数名能准确描述全部行为。做两件事的函数天然超复杂度和行数。
- **early return 消灭 else**：异常/边界先 return，主逻辑保持在左边界，嵌套自然 ≤2。
- **flag 参数拆函数**：`func Do(verbose bool)` → `func Do()` + `func DoVerbose()`。bool 参数增加圈复杂度和调用方心智负担。
- **纯函数优先**：无副作用、输出仅依赖输入的函数易测试、易推理。IO 和副作用推到调用链边缘。

### Types and interfaces

- **领域类型替代原始类型**：`userID string` → `type UserID string`。防止参数错位，语义自文档。
- **小接口（1-3 方法）**：接口越大，抽象越弱。消费方定义接口，实现方不预判。
- **具体类型作为函数返回值，接口作为参数**：return struct, accept interface.

### Error handling

- **错误逐层 wrap、绝不吞没**：`fmt.Errorf("operation: %w", err)`。忽略 error 或在 error 分支中 fallthrough 是 bug。
- **error 是最后一个返回值**：`(Result, error)` 而非 `(error, Result)`。
- **panic 仅用于初始化阶段的不可恢复错误**（如 missing config），业务逻辑用 error 返回。

### Testing

- **表驱动测试**：Go 测试结构统一 —— 定义 cases slice，`t.Run(tc.name, ...)` 迭代。禁止复制粘贴测试函数。
- **mock 外部依赖，不 mock 领域逻辑**：mock repository/port，不 mock entity/service。
- **测试意图即文档**：用例名描述行为（"returns error when user not found"），不描述步骤（"calls FindUser then checks error"）。

### Naming and structure

- **短作用域用短名**：`i`, `ctx`, `err`, `ok`；导出符号用描述性名称：`UserRepository`, `FindByTenant`。
- **注释解释 WHY，不翻译 WHAT**：`// 修复 #42: pgx 不传 *float64` 优于 `// 设置 price 为 nil`。
- **文件内自上而下阅读顺序**：导出的 public API 在前，私有实现在后。调用方先于被调用方。
- **import 分组**：stdlib → third-party → internal，组间空行分隔。

## Frontend conventions

- 所有普通 API 调用走 `web/src/services/client.ts` 的唯一 Axios 实例；流式请求也复用其 base URL、认证状态和统一错误约定，禁止新增平行客户端。
- 行为常量集中在 `web/src/constants/`，使用全大写下划线和 `_MS`、`_SEC`、`_SIZE` 等单位后缀；页面不得硬编码网络、分页、MCP、Skill 或 Memory 行为数字。
- 错误通知统一为 `message.error({ content: err.response?.data?.error || '操作失败', duration: 3 })`；成功通知使用 `message.success({ content: '操作成功', duration: 2 })` 或等价的 `duration <= 2` 形式。使用 `message` 和 `Modal.confirm`，禁止 `alert()`、`confirm()` 和提交 `console.log`。
- 页面不得跨 `pages/` 导入；组件超过 200 行应提取 hook、component 或纯函数。`useEffect` 依赖完整，异步 effect 使用 cancelled 标志清理。
- 用户可见字符串使用中文。Bearer token 不得存入 localStorage/Web Storage；使用 HttpOnly cookie 或内存 Context。
- 展示资源/实体时主文案用真实名称（产品元数据 `name`/`label`/`resource_name`），禁止把裸 id（`resource_id`、`revision_id`、`server_id` 等）当名称渲染成可见单元格文本；id 只作 rowKey/路由参数/aria-label 等非可见身份用途，确需展示原文时用弱化次要行或 tooltip 并配真实名称作主文案。名称缺失/DTO 未带名称时显式占位 `—`，不得用 id 冒充名称。
- Modal 状态命名：`createOpen/editOpen`；loading 命名：`createLoading/deleteLoading`；service 命名：`动词+实体名` 如 `createWorkspace`；Hook 返回值直接解构，不加 `state` 前缀。

## Logging and security

- 使用 `observability.NewLogger(env)`；production 输出 JSON，其他环境输出 console。事件命名 `layer.operation`；请求链路记录 request/trace/tenant/user，LLM 与 ReAct 只记录 model、provider、token 数、step、tool 和 latency 等必要元数据。
- DEBUG 只用于开发；正常路径 INFO；可预期 4xx/重试 WARN；5xx 和外部调用失败 ERROR。禁止记录 password、token、API key、PII 或原始上游响应体。
- 密钥通过 Vault/AWS Secrets Manager 管理，禁止入 Git；禁止修改 `config/config.go` 的默认值；生产覆盖走 `helm/values-prod.yaml`；传输使用 TLS 1.2+，静态敏感数据使用 AES-256；前端 `.env` 禁止提交密钥。
- bearer credential 不得进入 URL、Web Storage、通用请求日志或下游错误正文。认证 token 必须单次消费，状态转换必须原子。

## Risk regression harness

高风险改动必须逐项检查以下七条原则：

1. 授权、租户状态和外部依赖查询失败时必须 fail closed，禁止默认角色或默认放行。
2. bearer credential 不得进入 URL、Web Storage、通用请求日志或下游错误正文。
3. tenant-scoped 操作必须显式携带并校验 tenant ID，数据库访问必须经过租户边界封装。
4. 请求和启动路径禁止自动执行 DropCollection、不可逆的破坏性清理或无法审计的数据修复。
5. 持久化失败必须向上传播，失败状态写回失败也必须暴露。
6. 替换连接、client 或 worker 时必须关闭旧资源并等待 goroutine 退出。
7. 涉及认证、租户、迁移、消息、向量库或外部依赖时，必须添加对应失败路径与真实链路验证。

开工先运行 `bash scripts/quality/risk-regression-guard.sh --explain`，提交前运行 `make risk-guardrails`。IAM/OAuth、租户 DDL、日志/错误边界、资源关闭、readiness、部署或供应链改动必须执行命中的专项测试。数据库变更验证 DDL、回滚、历史 schema 顺序和失败传播；外部依赖验证预算、有限重试、隔离和关闭；secret scan 覆盖 tracked worktree。守卫必须传播失败，禁止吞错、伪成功或降级绕过。

自动报告只能作为待复核线索；仅修复由当前代码、测试和运行证据确认仍成立的缺陷。`tmp/risk-consolidated/reports/latest.md` 是本地复核索引，代码、测试、运行证据和阻断式守卫始终是事实源。

## Product design rules

产品交互规格与实体约束详见 `docs/agent/product.md`。

## Layered context index

- 项目事实：`docs/agent/project.md`。
- 架构与后端：`docs/agent/architecture.md`、`docs/agent/backend-go.md`、`docs/agent/constants.md`、`docs/agent/migration-tenant.md`。
- 模块规则：`docs/agent/api.md`、`docs/agent/agent.md`、`docs/agent/agent-chat-flow.md`、`docs/agent/milvus.md`、`docs/agent/nats.md`、`docs/agent/memory-facts.md`。
- 前端与产品：`docs/agent/frontend.md`、`docs/agent/product.md`。
- 可观测、部署和知识：`docs/agent/observability.md`、`docs/agent/deployment-architecture.md`、`docs/agent/knowledge-workspace.md`。
