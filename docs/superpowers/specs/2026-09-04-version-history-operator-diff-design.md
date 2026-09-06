# 版本修改历史增强：操作者按 name + 字段前后值详情 Drawer

> 设计 spec。交付目标：五个带版本历史的产品面（平台参数 / Agent / Knowledge / Skill / Workflow），其版本历史表的「操作者」统一显示可读姓名；每行新增「详情」入口，打开统一 Drawer 展示该版本相对其基线逐字段「变更前 / 变更后」。
> 范围沿用已确认口径：**所有带版本历史的产品面都要**；与 `feat/judge-optimizer-prompt-params`（提示词平台化）**同一分支同一 PR** 交付。

## 1. 背景与动机

用户在平台「版本修改历史」场景提出三点诉求（原话）：

1. 保存/展示操作者用 name（不要一串不可读的 id / uuid / 空）；
2. 保存被修改字段的前后值；
3. 加一个「详情」按钮可以看到前后值。

盘点了仓库真实带版本的产品面（`web/src/shared/ui/VersionHistory.tsx` 被 Agent/Knowledge/Skill/Workflow 复用；平台参数用自带的 `web/src/modules/parameters/components/VersionHistory.tsx`），操作者与快照现状不对称：

| 面 | 后端表 | 操作者现状 | list 是否自带整份内容快照 | 前一版本指针 |
|---|---|---|---|---|
| 平台参数 | public `platform_config_versions` | `created_by` 存 auth.sub uuid 或字面量 `system`，无 name join | **带**（整份 `snapshot` JSONB） | `base_version_id`（指向发布时 production 基线，非直父） |
| Agent | tenant `resource_versions(kind=agent)` | 存 actor sub；list 已 join 出 `createdByName` | **不带**（只 `contentHash`+`safeSummary`）；DB 有整份 `payload` | DB 有 `parent_version_id`，HTTP 未暴露 |
| Knowledge | tenant `resource_versions(kind=knowledge)` | 同 Agent | 同 Agent | 同 Agent |
| Skill | tenant `skill_revisions` | 存 actor sub；list 已 join `createdByName` | **带**（`name/description/instructions` 列即整份内容） | DB 有 `parent_revision_id`，HTTP 未暴露 |
| Workflow | tenant `workflow_versions` | **无任何 operator 列**（旁证 `resource_change_audits.actor_id` / `workflow_definitions.created_by`） | **不带**；完整 `spec_json`+`input_schema_json` 需 per-version 详情接口 | 无；`version_no` 唯一且 +1 递增（`UNIQUE(definition_id,version_no)`） |

name 解析先例：`internal/iam/infrastructure/persistence/actor_name_resolver.go` —— `public.users`，`display_name > github_login > id 原文`，已被 agent/skill/knowledge wiring 注入。

## 2. 决策（用户已确认）

- **操作者语义**：DB 存 actor id（workflow 补列持久化），展示侧 join `public.users` 出 name；agent/knowledge/skill 保持现状，平台参数补 join，workflow 补列 + 持久化 + 尽力回填。改名全平台生效、无冗余。
- **详情/前后值形态**：统一共享「详情 Drawer」；平台参数从自带展开行 diff 迁到同一 Drawer。
- **diff 粒度**：递归字段路径 diff（JSONPath 叶子行：`path | 变更前 | 变更后`），覆盖 workflow `spec_json` / agent 嵌套 `payload`。
- **diff 不落库**：不做独立 diff 存储/表；详情打开时由各面自带的整份快照现算（before/after 一次性对当前版与基线取齐）。除 workflow 加 operator 列外，**无新增写路径、无数据迁移式结构**。
- **diff 基线语义**：平台参数保留「vs 发布基线 base_version_id」；其余四类 = 「vs 直父版本」（agent/knowledge `parent_version_id`、skill `parent_revision_id`、workflow `version_no-1`）。Drawer 只做展示，不强求五者基线统一。
- **Workflow 存量回填为尽力而为**（用户知悉接受）：新版本从 publish 起 100% 记对；存量行经 audit 关联，无法唯一关联者回落 definition 创建者。
- **Skill DTO 走 proto**（用户知悉接受）：改一个响应字段 = 改 `.proto` + `make proto-gen` + 刷新契约 golden。

## 3. 架构

三层共享 + 每面一个 fetcher：

1. **共享 diff 纯函数** `computeFieldChanges(before, after)`（前端，`web/src/shared/utils/fieldDiff.ts`）
   - 递归比较两个 JSON 值；输出 `FieldChange[] = { path: string; before?: unknown; after?: unknown }`。
   - path 形如 `a.b[2].c`；键仅在一边存在 → 增/删；两边均为对象/数组且 JSON 不等 → 递归；数组按下标比较；深度护栏（防超深嵌套失控）。
2. **共享 Drawer** `VersionDiffDrawer`（`web/src/shared/ui/VersionDiffDrawer.tsx`）
   - 纯展示：`{ open, onClose, title?, fieldLabels?, before, after }`。
   - 表格列：字段(path，可用 `fieldLabels` 映射友好名) | 变更前 | 变更后；值以 pretty JSON / 单行展示，超长可折叠。
3. **共享组件挂点**（`web/src/shared/ui/VersionHistory.tsx`）
   - 新增可选 `onViewDetail?: (row: VersionRow) => Promise<VersionDetail>`；`VersionDetail = { title?: string; fieldLabels?: Record<string,string>; before: Record<string,unknown>; after: Record<string,unknown> }`。
   - 提供时在操作列追加「详情」按钮；组件内部管理 loading/错误并打开 `VersionDiffDrawer`。不提供时行为与现状完全一致（向后兼容，4 个使用方逐面接入）。
   - 平台参数组件（自带表格，含 draft→发布 / 回滚操作，无法用共享表）不换表，只把行内展开 diff 删掉、改为同一「详情」按钮接同一 Drawer。

## 4. 每面实现要点

### 4.1 共享层（先落，纯前端可独立单测）

- 新增 `web/src/shared/ui/VersionDiffDrawer.tsx`、`web/src/shared/utils/fieldDiff.ts`。
- 修改 `web/src/shared/ui/VersionHistory.tsx`：`VersionRow` 不变，`VersionHistoryProps` 加 `onViewDetail`；操作列加详情按钮；内部 state 开 Drawer。
- 沿用仓库共享组件目录既有结构；Ant Design `Drawer`/`Table`。

### 4.2 平台参数

- 后端：`internal/parameters/infrastructure/persistence/platform_repo.go::ListVersions` SQL 追加
  `LEFT JOIN public.users u ON u.id::text = v.created_by`，select 出
  `COALESCE(u.display_name, u.github_login, v.created_by) AS created_by_name`（`system`/`api`/未知 uuid 因 LEFT JOIN 未命中 → COALESCE 原样返回）。
- `internal/parameters/domain/port/store.go` `PlatformVersion` 加 `CreatedByName string`；repo scan 填充。
- handler/response 序列化透出 `created_by_name`；契约 golden（`api/http/testdata/contracts/*.golden.json`）同步刷新。
- 前端 `web/src/modules/parameters/model/parameters.ts` `PlatformConfigVersion` 加 `created_by_name?`；`web/src/modules/parameters/components/VersionHistory.tsx`：
  - 操作者列优先显示 `created_by_name`（值为 `system`/`api` 字面量时沿用现有 actorLabel 映射）；
  - 每行加「详情」→ 打开共享 Drawer：`before = base?.snapshot ?? {}`（base 仍按 `base_version_id` 从同一 list 的 `byId` 取）、`after = v.snapshot`、`fieldLabels = labelMap`；
  - 删除原展开行 diff（`diffSnapshots` 迁为对共享 `computeFieldChanges` 的调用或移除）。

### 4.3 Agent / Knowledge

- 后端（versioning 底层共用，分面加路由/handler/DTO）：
  - Agent list DTO `AgentVersionResponse`（`api/http/handler/agent_dto.go`）加 `parentVersionId`；Knowledge `WorkspaceVersionResponse`（`api/http/handler/rag_dto.go`）同。
  - 新增单版本内容接口（复用现有 DB `GetVersion` 语义，仅补 HTTP）：
    - `GET /agents/:id/versions/:versionID` → 返回该版整份 `payload` + `safeSummary` + `parentVersionId` 等；
    - `GET /knowledge/workspaces/:name/versions/:versionID` → 同构。
  - 契约 golden 刷新。
- 前端：
  - agent/knowledge model 加 `parentVersionId?`；api client 加取单版内容方法。
  - 详情页 `onViewDetail`：`after = content(clicked).payload`；`before = content(parentVersionId).payload`（parent 缺失 → `{}`，首版全新增）。

### 4.4 Skill

- 后端：改 `.proto` 中 SkillRevision 响应加 `parent_revision_id`；`make proto-gen` 生成 `api/http/dto/gen/skill.go`；应用层把 DB `parent_revision_id` 填入响应（list SQL 已 select、domain 已有 `ParentRevisionID`，只缺透传）。契约 golden 刷新。
- 前端：skill model 加 `parentRevisionId?`；SkillWorkspacePage `onViewDetail` 直接用持有的 `listRevisions` 原数据（行内已含 name/description/instructions）定位当前版与直父，组装 before/after。

### 4.5 Workflow（唯一 schema 改动）

- `pkg/storage/postgres/tenant_schema.sql`：
  1. `workflow_versions` CREATE 块加列 `created_by TEXT NOT NULL DEFAULT ''`；
  2. 紧随既有 `ALTER TABLE workflow_versions ...` 处加
     `ALTER TABLE workflow_versions ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';`（幂等升级存量租户，`ProvisionAllTenantSchemas` 启动全量重放即生效）；
  3. 末尾幂等回填（只动 `created_by = ''` 行，重放安全）：

     ```sql
     UPDATE workflow_versions v
        SET created_by = COALESCE(
            (SELECT a.actor_id FROM resource_change_audits a
              WHERE a.resource_kind = 'workflow'
                AND a.resource_id = v.definition_id::text
                AND a.operation = 'publish'
                AND a.created_at <= v.created_at
              ORDER BY a.created_at DESC, a.id DESC
              LIMIT 1),
            (SELECT d.created_by FROM workflow_definitions d WHERE d.id = v.definition_id),
            '')
      WHERE v.created_by = '';
     ```

     （审计关联为尽力而为：版本无外键指向 audit，靠 created_at 邻近 + operation=publish 近似；关联不到回落 definition 创建者。新版本由写路径直接记 actor，不受回填影响。）
- 写路径 `internal/workflow/infrastructure/persistence/store.go`：版本 INSERT（CreateNextVersion）加 `created_by` 列，actor 取 ctx auth.sub（与同事务 `insertChangeAudit` 同源）。调用方 `internal/workflow/application/definition_service.go` publish 路径透传。
- 读路径：list SQL 补 select `created_by`；`internal/workflow/application/query.go` `VersionSummary` 加 `CreatedBy`/`CreatedByName`；应用层解析 name（复刻 skill：新增小型 ResolveActorNames port，wiring 注入 iam `PgActorNameResolver`）。
- 前端 `WorkflowDetailPage`：历史行补传 `createdBy`/`createdByName`；`onViewDetail` 用现有 `workflowApi.getWorkflowVersion(id, versionId)`：
  - 由 list 结果建 `versionNo → id` 映射；`after = 该版 {name, description, spec, input_schema}`；前一版 = `version_no-1`（若 ≥1）同样取 `spec/input_schema` 作 `before`，`version_no-1 < 1` 或缺失 → `{}`。
  - Drawer 借此展示 `spec_json.nodes[...]` 级字段路径 diff。

## 5. 数据与写路径变化清单

| 变化 | 位置 |
|---|---|
| `workflow_versions.created_by` 新列 | `tenant_schema.sql`（CREATE + ALTER + 幂等回填）|
| workflow 版本 INSERT 记 actor | `store.go` CreateNextVersion + `definition_service.go` publish |
| workflow list 出 operator | list SQL + `query.go` VersionSummary + Resolver port + wiring |
| 参数 list 出 name | `platform_repo.go` ListVersions LEFT JOIN + `port.PlatformVersion.CreatedByName` |
| agent/knowledge 单版内容接口 + list 出 parent | agent/rag handler+DTO+router+`versioning` 复用 |
| skill list 出 parent | proto + gen + 应用层透传 |
| 共享层新增 | `fieldDiff.ts`、`VersionDiffDrawer.tsx`、`VersionHistory` onViewDetail |
| 共享组件入面 | EditAgentPage / KnowledgeDetailPage / SkillWorkspacePage / WorkflowDetailPage / parameters VersionHistory |

除 workflow 版本 INSERT 与 tenant_schema.sql 外，**没有任何既有写路径被改动**；diff/前后值全程只读现算。

## 6. 测试策略

- 后端：
  - params：`ListVersions` 对真实 user 出 `created_by_name`、对 `system`/未知原样返回（repo 级，fake/集成）。
  - workflow：INSERT 落 actor；list 解析出 name；`tenant_schema.sql` 重放幂等测试 + 回填两分支（audit 可关联 → actor；不可 → definition 创建者）；`tenant_schema_safety_test.go` 既有惯例覆盖新列/语句顺序。
  - agent/knowledge：单版内容接口返回整份 payload + parentVersionId；list DTO 透出 parent。
  - skill：proto-gen 后响应透出 parent_revision_id。
  - 契约 golden 全量刷新（`api/http/contract_test.go` + `testdata/contracts/*.golden.json`）。
- 前端单测：`computeFieldChanges` 递归用例（嵌套改/增/删、数组、深度护栏）；`VersionHistory` 有 `onViewDetail` 才渲染详情按钮、点开开 Drawer 且展示 before/after、loading/失败态；各页面 fetcher 映射。
- 门禁：`make check`（含 proto/契约残留守卫）、`make code-quality`、`go vet && go test -short ./...`、全量 `-race`。
- 系统验收：与提示词平台化功能合并后，由 `stratum-e2e-tester`（`stratum-e2e-development`）统一做 PR 前验收——逐面验证「操作者显示 name + 详情 Drawer 前后值」，workflow 含发布者归因。

## 7. 明确不做（范围护栏）

- 不持久化 diff 行 / 不改 diff 计算为后端。
- 不动 MCP 及其余 resource kind / 不引入新版本面。
- 不改平台参数「vs 基线」语义（仅展示层统一）。
- 不新增回滚/管理 UI、不改既有权限。
- 不为 workflow 旧行引入强一致回填承诺（尽力而为 + 回落 definition 创建者，新行准确）。
- 不重写共享组件对外 props（`onViewDetail` 缺省时行为不变）。
