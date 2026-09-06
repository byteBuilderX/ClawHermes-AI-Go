# 版本修改历史增强 Implementation Plan

> **For agentic workers:** 分支 `feat/judge-optimizer-prompt-params`，与提示词平台化同一分支同一 PR 交付。spec：`docs/superpowers/specs/2026-09-04-version-history-operator-diff-design.md`。
> 步骤用 `- [ ]`。精确代码在实现时对目标文件落地，本 plan 锁定文件清单/契约/测试意图。主仓库只读；所有编辑在 worktree `/home/yang/go-projects/stratum-judge-optimizer-prompt-params`。Go pre-commit 全仓 golangci 冷缓存会超时：提交用 scoped lint + `SKIP=golangci-lint`（CI 兜底），仅对改动包跑 golangci 证明干净。

**Goal:** 五个带版本产品面（平台参数/Agent/Knowledge/Skill/Workflow）版本历史：操作者显示 name（workflow 补列持久化+尽力回填），每行「详情」Drawer 展示相对基线的递归字段路径前后值（不落库现算）。

**Architecture:** 共享 diff 纯函数 + 共享 `VersionDiffDrawer` + `VersionHistory` 可选 `onViewDetail` 挂点；每面一个 fetcher 供 before/after；操作者 name 统一「存 id + join `public.users`」语义（解析器先例 `internal/iam/infrastructure/persistence/actor_name_resolver.go`）。

## Global Constraints

- 不改参数契约的 HTTP JSON 事实源仍是 `.proto`：Skill 响应字段改动走 proto → `make proto-gen`；agent/knowledge/params/workflow 响应 DTO 为手写（不走 proto），但全部受 `api/http/contract_test.go` + `testdata/contracts/*.golden.json` 守护，改形状必须刷新 golden。
- tenant DDL 唯一基线 `pkg/storage/postgres/tenant_schema.sql`，幂等重放（`ProvisionAllTenantSchemas` 启动全量）；新列 = CREATE 块补列 + `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`；回填 UPDATE 必须幂等（只动 `created_by = ''`）。禁止在 `pkg/migration/sql/` 复制 tenant DDL。
- 共享组件对外 props 向后兼容：`onViewDetail` 缺省时行为与现状一致。
- diff 基线：平台参数 = vs `base_version_id`；Agent/Knowledge/Skill = vs 直父（parent id）；Workflow = vs `version_no-1`。
- 门禁：新函数圈复杂度 ≤10、认知 ≤15、行 ≤120、嵌套 ≤4；Go 行宽 ≤120；前端组件超 200 行提取。

---

### Task 1: 前端共享层（fieldDiff + VersionDiffDrawer + VersionHistory 挂点）

**Files:**

- Create: `web/src/shared/utils/fieldDiff.ts` + `web/src/shared/utils/fieldDiff.test.ts`
- Create: `web/src/shared/ui/VersionDiffDrawer.tsx`
- Modify: `web/src/shared/ui/VersionHistory.tsx`

**Deliverable:** 独立可测、无消费方破坏。

- `computeFieldChanges(before, after): FieldChange[]`（`{path, before?, after?}`）：递归 JSONPath，键仅一边→增/删；对象/数组且 JSON 不等→递归；数组按 index；深度护栏（≥32 层截断为整体值）；路径 `a.b[2].c`。
- `VersionDiffDrawer`：AntD Drawer；props `{open,onClose,title?,fieldLabels?,before,after}`；表列 字段|变更前|变更后；值 JSON pretty、单值超长可折叠。
- `VersionHistory`：`VersionHistoryProps` 加 `onViewDetail?`；`VersionDetail={title?;fieldLabels?;before:Record<string,unknown>;after:Record<string,unknown>}`；有则操作列加「详情」，内部 state loading/error 后开 Drawer；缺省行为不变。
- Test：fieldDiff 表驱动用例；VersionHistory 渲染（vitest + testing-library，若仓库有该栈；否则挂 make fe-lint + 现有测试模式）。

### Task 2: 平台参数后端 name join

**Files:** Modify `internal/parameters/infrastructure/persistence/platform_repo.go`（ListVersions SQL 加 `LEFT JOIN public.users u ON u.id::text = v.created_by`，select `COALESCE(u.display_name,u.github_login,v.created_by) AS created_by_name`）、`internal/parameters/domain/port/store.go`（`PlatformVersion` 加 `CreatedByName`）、handler 响应透出 `created_by_name`；同步 repo 测试。
**Verify:** 真实 user 出 name；`system`/未知 uuid 原样；`go test -short ./internal/parameters/...`；契约 golden 刷新（先读 `api/http/contract_test.go` 与受平台参数版本影响的 golden，增量改）。

### Task 3: 平台参数前端（name 展示 + 详情接 Drawer）

**Files:** Modify `web/src/modules/parameters/model/parameters.ts`（`PlatformConfigVersion` 加 `created_by_name?`）、`web/src/modules/parameters/components/VersionHistory.tsx`（操作者优先 `created_by_name`（`system`/`api` 沿用 actorLabel 映射）；每行「详情」→ 共享 Drawer：before=`byId[base_version_id]?.snapshot ?? {}`，after=`v.snapshot`，fieldLabels=labelMap；删展开行 diff，`diffSnapshots` 移除或改调共享 util）。
**Depends:** Task 1, 2。

### Task 4: Workflow schema + 后端 operator

**Files:**

- Modify `pkg/storage/postgres/tenant_schema.sql`：`workflow_versions` CREATE 加 `created_by TEXT NOT NULL DEFAULT ''`；加 `ALTER TABLE workflow_versions ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';`；末尾幂等回填 UPDATE（见 spec §4.5，只动 `created_by=''`，audit 关联 operation=publish 最近于版本 created_at，回落 `workflow_definitions.created_by`）。**先读 audit schema 确认列名/`resource_kind`='workflow'、operation='publish'、resource_id=definition_id::text、created_at、actor_id。**
- Modify `internal/workflow/infrastructure/persistence/store.go`：版本 INSERT 加 `created_by` 列与参数。
- Modify `internal/workflow/application/definition_service.go`：publish 路径取 ctx actor 传入 insert。
- Modify list：SQL 补 select `created_by`；`internal/workflow/application/query.go` `VersionSummary` 加 `CreatedBy`/`CreatedByName`；加 ResolveActorNames port（复刻 skill）+ `api/wiring/workflow.go` 注入 iam resolver。
- 契约 golden 刷新。
**Verify:** insert 落 actor；list 出 name；`tenant_schema.sql` 重放幂等（应用两次断言回填一次且稳定）；`go test -short` + 集成；既有 `tenant_schema_safety_test.go` 惯例兼容。

### Task 5: Workflow 前端（operator 行 + onViewDetail）

**Files:** Modify `web/src/modules/workflow/pages/WorkflowDetailPage.tsx`（行补 `createdByName`/`createdBy`；`onViewDetail`：由 list 建 `versionNo→id`，用现有 `workflowApi.getWorkflowVersion(id, versionId)` 取该版与 `version_no-1` 的 `{name,description,spec,input_schema}` 作 after/before）。前端 workflow model 视需要补字段。
**Depends:** Task 1, 4。

### Task 6: Agent（后端 parent + 单版内容接口 + 前端 fetcher）

**Files:**

- Backend: Modify `api/http/handler/agent_dto.go`（`AgentVersionResponse` 加 `parentVersionId`）、`api/http/router.go`（新 `GET /agents/:id/versions/:versionID`）、agent handler + 应用层透传（复用 versioning `GetVersion`）；契约 golden。
- Front: `web/src/modules/agent/model/agent.ts` + api client 加取单版内容；`web/src/modules/agent/pages/EditAgentPage.tsx` `onViewDetail`（after=payload；before=parent payload 或 `{}`）。
**Depends:** Task 1。

### Task 7: Knowledge（同 Task 6）

**Files:** `api/http/handler/rag_dto.go`、`rag_handler.go`、router 新 `GET /knowledge/workspaces/:name/versions/:versionID`；前端 knowledge model/api/`KnowledgeDetailPage.tsx`。契约 golden。
**Depends:** Task 1。

### Task 8: Skill（proto parent_revision_id + 前端 fetcher）

**Files:** 改 `.proto` 中 SkillRevision 响应加 `parent_revision_id`；`make proto-gen`；应用层透传 DB `parent_revision_id`（list SQL/domain 已有）；契约 golden。前端 skill model + `SkillWorkspacePage.tsx` `onViewDetail`（直接用持有的 listRevisions 原数据定位当前版与直父组装 before/after；parent 按 `parentRevisionId`）。
**Depends:** Task 1。

### Task 9: 全量门禁 + 系统验收（延后到 feature A 同分支 PR 前）

- `make check`（proto/契约残留守卫）、`make code-quality`、`go vet && go test -short ./...`、全量 `-race`；前端 `make fe-lint && make fe-build`。
- 与提示词平台化合并态由 `stratum-e2e-tester` 统一系统验收（spec §6），通过后 push + `gh pr create --base main`。
