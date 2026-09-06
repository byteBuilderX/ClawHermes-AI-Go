# RAG 溯源「来源文档」链路修复与落库回放 —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让「来源文档卡片 → 预览原文」在 agent 对话（live + 历史回放）与 `/knowledge/query` 面板真正可用：前端对齐 camelCase、query 后端补文档名/workspace、assistant 消息来源落库并在重进会话时回放。

**Architecture:** 三段互不耦合的修复。(A) 前端：把 `ChatCitationSource` 的 TS 接口与唯一读取组件 `SourceCardList` 从 PascalCase 对齐到后端 RAGSearchSource 的 **camelCase** wire（SSE done `sources` 与 GET messages `sources` 均为 camelCase，前端无字段转换层，错位导致运行时 `undefined`）。(B) `/knowledge/query` 后端补字段：knowledge `Source` 增加 `DocumentTitle`，Query 尾部用现成的 `documentTitles` 批量回填源文件名，rag_handler 响应补 `document_title`+`workspace`（前端 zod schema 已就绪）。(C) 来源落库：复刻 `artifacts_json` 先例，`chat_messages` 加 `sources_json` 列（tenant_schema.sql 幂等 `ADD COLUMN IF NOT EXISTS`，服务器启动 `BootstrapTenants → ProvisionAllTenantSchemas` 自动升级全部存量租户），`AddMessage`/`ListMessages` 读写，assistant 主答消息带 `result.Sources`，GET messages 透出 `sources`（前端 `chatMessageSchema` 是 `.passthrough()`，`ChatMessage.sources` 已声明，历史回放零前端改动）。

**Tech Stack:** Go 1.25（gin / pgx v5 / pgxmock）、React 18 + AntD 5 + zod + vitest/jsdom。

## Global Constraints

- **工作目录**：本计划全部命令在 `/home/yang/go-projects/stratum-rag-source-citation`（`feat/rag-source-citation` worktree）内执行；禁止 `cd` 出此目录。前端命令在 `web/` 下执行。
- **Git**：不得提交 `main`。提交标题格式 `[type](scope): description`（type ∈ feat|fix|refactor|test|docs|chore）。每条提交附 `Co-Authored-By: Claude <noreply@anthropic.com>`。push/PR 走 CI，禁止在 base 落后最新 `origin/main` 时依赖 CI 结果。
- **后端不改 wire 契约大小写**：`RAGSearchSource` camelCase（agent.go:512-521 带 json tag）、`NoAnswerInfo` PascalCase（无 tag，agent.go:446-454）都是既有事实，本次**只修前端 sources 消费**，不碰 noAnswer。
- **DDL 只进 `pkg/storage/postgres/tenant_schema.sql`**（go:embed + ProvisionTenantSchema 幂等应用，服务器每次启动 `ProvisionAllTenantSchemas` 对所有存量租户重放）。禁止写进 `pkg/migration/sql/`。新列 `ADD COLUMN IF NOT EXISTS` + 安全默认值。
- **pgx v5 写 JSONB**：`json.Marshal` 后以 `string(b)` 传入，禁止直传 struct。
- **领域/分层**：domain 只依赖 stdlib 与 `pkg/constants`；application 不 import infra/存储驱动；infra 实现 port。`application.AgentResult = domain.AgentResult` 别名（application/agent.go:46），故 `result.Sources`（`[]domain.RAGSearchSource`）可直接赋给 `domain.ChatMessage.Sources`，无需转换。
- **质量棘轮**：新函数圈复杂度 ≤10、长度 ≤120 行、嵌套 ≤4；`go vet && go test -short ./...`、`make fe-lint && make fe-build` 绿。
- **验收红线**：PR 前须由 `stratum-e2e-tester` agent（封装 stratum-e2e-development skill）在 clean commit 上完成系统验收；本计划各任务到单测/契约即止，系统 E2E 交验收 agent。

---

### Task 1: 前端对齐 camelCase（model 类型 + SourceCardList + 过期注释）

**Files:**

- Test: `web/src/modules/agent/components/__tests__/SourceCardList.test.tsx`（Create）
- Modify: `web/src/modules/agent/model/agent.ts`（ChatCitationSource 接口 + 两处过期注释）
- Modify: `web/src/modules/agent/components/SourceCardList.tsx`（字段读取）
- Test(regression): `web/src/modules/agent/pages/__tests__/AgentChatMobile.test.tsx`（若含 PascalCase source fixture，Task 2 修）

**Interfaces:**

- Consumes: `domain.RAGSearchSource`（后端 camelCase json tag：`workspaceId/workspaceName/chunkId/documentId/documentTitle/snippet/score,omitempty/hasScore,omitempty`）。
- Produces: `ChatCitationSource` 接口字段变 camelCase 且保持全可选；`SourceCardList` 的 props 不变（`sources?: ChatCitationSource[]`），下游（ChatMessageList 透传）无需改签名。

- [ ] **Step 1: 写失败测试（camelCase fixture）**

  新建 `web/src/modules/agent/components/__tests__/SourceCardList.test.tsx`，镜像 `WorkspaceDetailHeader.test.tsx` 的 import 风格：

```tsx
import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { ChatCitationSource } from '../../model/agent';
import { SourceCardList } from '../SourceCardList';

// SourceCardList 从 '@/modules/knowledge' barrel 引入 DocPreviewDrawer；stub 成空
// 组件避免单测触发真实抽屉的预览网络请求，其它 barrel 导出保持真实（importOriginal）。
vi.mock('@/modules/knowledge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/knowledge')>();
  return { ...actual, DocPreviewDrawer: () => null };
});

const sources: ChatCitationSource[] = [
  {
    workspaceId: 'ws-1',
    workspaceName: '产品知识库',
    chunkId: 'chunk-1',
    documentId: 'doc-1',
    documentTitle: '用户手册.pdf',
    snippet: '退货流程：签收后 7 天内可无理由退换。',
    score: 0.91,
    hasScore: true,
  },
  { chunkId: 'chunk-2', documentId: 'doc-2' },
];

describe('SourceCardList', () => {
  it('渲染文档名、snippet 与分数徽标（camelCase 字段）', () => {
    render(<SourceCardList sources={sources} />);

    expect(screen.getByText('用户手册.pdf')).toBeInTheDocument();
    expect(screen.getByText(/退货流程/)).toBeInTheDocument();
    expect(screen.getByText('91.0%')).toBeInTheDocument();
    // 无 workspaceName/documentTitle 的第二条回落 DocumentID，不渲染 snippet。
    expect(screen.getByText('doc-2')).toBeInTheDocument();
  });

  it('无来源或空数组时不渲染任何卡片', () => {
    const { container } = render(<SourceCardList sources={undefined} />);
    expect(container.innerHTML).toBe('');

    const { container: c2 } = render(<SourceCardList sources={[]} />);
    expect(c2.innerHTML).toBe('');
  });
});

// 注：spec §4 测试清单中的「点击可开预览」断言不放入组件单测——需跨模块 stub
// 真实 DocPreviewDrawer（其打开会发起预览网络请求），属 E2E 边界；由 Task 8
// 验收标准 1（live 来源卡片可点开预览）在无头浏览器中覆盖。
```

- [ ] **Step 2: 运行测试确认红**

  Run: `cd web && npx vitest run src/modules/agent/components/__tests__/SourceCardList.test.tsx`
  Expected: FAIL——组件按 `s.DocumentTitle` 读取，`documentTitle` camelCase 键运行时不存在 → 显示"未知文档"，`getByText('用户手册.pdf')` 抛 `TestingLibraryElementError`。

- [ ] **Step 3: 把 `ChatCitationSource` 接口字段改为 camelCase 并修过期注释**

  编辑 `web/src/modules/agent/model/agent.ts:172-185`（用 Read 确认现文本后整体替换接口与上方注释）：

```ts
// ChatCitationSource is a retrieval provenance entry carried by the SSE done
// payload. Wire JSON is camelCase: the backend serializes domain.RAGSearchSource
// with json tags (workspaceId/workspaceName/chunkId/documentId/documentTitle/
// snippet/score/hasScore), and the same camelCase shape is replayed from the
// persisted chat_messages.sources_json on history load — live and replay render
// identically with no field remap.
export interface ChatCitationSource {
  workspaceId?: string;
  workspaceName?: string;
  chunkId?: string;
  documentId?: string;
  documentTitle?: string;
  snippet?: string;
  score?: number;
  hasScore?: boolean;
}
```

  再修下方 `NoAnswerInfo` 过期注释（约 `agent.ts:198-199`，现文含"与 ChatCitationSource 同一序列化规则"），改为：

```ts
// NoAnswerInfo 是 SSE done payload 的 noAnswer 信号（JSON 字段名 PascalCase：
// 后端 domain.NoAnswerInfo 无 tag）。注意与 sources 的 camelCase 规则不同：
// sources 子字段带 json tag，noAnswer 子字段无 tag，二者并存于同一 done 帧。
```

- [ ] **Step 4: 把 `SourceCardList.tsx` 的字段读取改为 camelCase**

  编辑 `web/src/modules/agent/components/SourceCardList.tsx`：第 20 行 `renderScore`、第 42-50、75-81 行的 PascalCase 键全部改 camelCase，组件逻辑不变：

```tsx
const renderScore = (s: ChatCitationSource) => {
  if (!s.hasScore || typeof s.score !== 'number') return null;
  return (
    <Badge
      count={`${(s.score * 100).toFixed(1)}%`}
      style={{ background: '#52c41a', fontSize: 11 }}
    />
  );
};
```

```tsx
          const title = s.documentTitle || s.documentId || '未知文档';
          const workspaceName = s.workspaceName || '';
          const previewable = Boolean(workspaceName && s.documentId);
          return (
            <div
              key={s.chunkId || s.documentId || i}
              onClick={
                previewable
                  ? () => setPreview({ name: workspaceName, documentID: s.documentId!, title })
                  : undefined
              }
```

```tsx
              {s.snippet && (
                <Paragraph
                  ellipsis={{ rows: 2 }}
                  type="secondary"
                  style={{ margin: 0, fontSize: 12, color: '#8c8c8c' }}
                >
                  {s.snippet}
                </Paragraph>
              )}
```

- [ ] **Step 5: 运行测试确认绿**

  Run: `cd web && npx vitest run src/modules/agent/components/__tests__/SourceCardList.test.tsx`
  Expected: PASS（2 it 全绿）。

- [ ] **Step 6: 前端类型与 lint 通过后提交**

  Run: `cd web && npx tsc -p tsconfig.json --noEmit && cd .. && make fe-lint`
  Expected: 无类型错误、lint 绿。

```bash
git add web/src/modules/agent/model/agent.ts web/src/modules/agent/components/SourceCardList.tsx web/src/modules/agent/components/__tests__/SourceCardList.test.tsx
git commit -m "fix(web): 来源卡片字段对齐 camelCase wire 并补渲染测试

ChatCitationSource/NoAnswerInfo 注释此前声称与后端同一序列化规则，
与 RAGSearchSource 实际 camelCase json tag 不符，为过期注释。

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: 扫描残留 PascalCase 消费并修 fixtures

**Files:**

- Modify: `web/src/modules/agent/pages/__tests__/AgentChatMobile.test.tsx` 及 `tokensave`/`tsc` 上报的任何含旧 PascalCase source fixture 的文件（含 `web/src/modules/agent/components/ChatMessageList.tsx` 若被 tsc 点名）。

**Interfaces:**

- Consumes: Task 1 后的 camelCase `ChatCitationSource`。
- Produces: `web/` 内无任何对旧 `WorkspaceID/WorkspaceName/ChunkID/DocumentID/DocumentTitle/Snippet/HasScore/Score`（作为 ChatCitationSource 字段）的读取或 fixture 字面量。

- [ ] **Step 1: 全仓类型检查，列出残留引用**

  Run: `cd web && npx tsc -p tsconfig.json --noEmit`
  Expected: 若只有 Task 1 的改动，此处应无错误（此前 grep 证实 SourceCardList 是唯一 PascalCase 读取者）。若列出文件（如测试 fixture 用 `{ DocumentTitle: ... }` 赋给 `ChatMessage.sources`），记录每个 `file:line`。

- [ ] **Step 2: 把上报的 PascalCase source fixture 改成 camelCase**

  对 Step 1 列出的每个 `file:line`：把该 ChatCitationSource 字面量的 `DocumentTitle → documentTitle`、`WorkspaceName → workspaceName`、`ChunkID → chunkId`、`DocumentID → documentId`、`HasScore → hasScore`、`Score → score`、`Snippet → snippet` 全量替换；仅字段名，不改键序或值。

  （若 tsc 无报错则跳过本步并如实记录"无残留"。不得臆造不存在的改动。）

- [ ] **Step 3: 类型检查 + 前端单测通过**

  Run: `cd web && npx tsc -p tsconfig.json --noEmit && npx vitest run src/modules/agent`
  Expected: 无类型错误；agent 模块全部单测（含 AgentChatMobile）绿。

- [ ] **Step 4: 提交**

```bash
git add web/src/modules/agent
git commit -m "refactor(web): 清理来源卡片 camelCase 迁移后的残留 fixture

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: `/knowledge/query` 后端补 document_title/workspace

**Files:**

- Modify: `internal/knowledge/application/rag_service.go`（`Source` struct、新增 `applySourceTitles`/`decorateSourceTitles`、`Query` 尾部）
- Test: `internal/knowledge/application/rag_service_sources_test.go`（Create）
- Modify: `api/http/handler/rag_handler.go`（Query 响应 gin.H 补键）

**Interfaces:**

- Consumes: `rs.documentTitles(ctx, tenantID, workspaceID) map[string]string`（rag_service.go:1414 已存在，失败时返回空 map 并 WARN）。
- Produces: `Source` 新增字段 `DocumentTitle string`；`rag_service` 方法 `decorateSourceTitles(ctx, tenantID, workspaceID string, sources []Source)` 与纯函数 `applySourceTitles(sources []Source, titles map[string]string)`；`/knowledge/query` sources 条目新增 snake_case 键 `document_title`、`workspace`（workspace = 请求携带的 workspace 名，即 `req.Workspace`）。

- [ ] **Step 1: 写纯函数失败测试**

  新建 `internal/knowledge/application/rag_service_sources_test.go`：

```go
package application

import "testing"

func TestApplySourceTitles(t *testing.T) {
	cases := []struct {
		name    string
		sources []Source
		titles  map[string]string
		want    []string // 每个 source 期望的 DocumentTitle
	}{
		{
			name: "docID 命中则回填源文件名，未命中保持空",
			sources: []Source{
				{DocumentID: "doc-1"},
				{DocumentID: "doc-2"},
				{DocumentID: "missing"},
			},
			titles: map[string]string{"doc-1": "用户手册.pdf", "doc-2": "q3-report.md"},
			want:   []string{"用户手册.pdf", "q3-report.md", ""},
		},
		{
			name:    "空 titles 不改动任何 source",
			sources: []Source{{DocumentID: "doc-1"}},
			titles:  map[string]string{},
			want:    []string{""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applySourceTitles(tc.sources, tc.titles)
			for i, src := range tc.sources {
				if src.DocumentTitle != tc.want[i] {
					t.Fatalf("source %d: want title %q, got %q", i, tc.want[i], src.DocumentTitle)
				}
			}
		})
	}
}
```

- [ ] **Step 2: 运行确认红**

  Run: `go test ./internal/knowledge/application/ -run TestApplySourceTitles -v`
  Expected: FAIL——`applySourceTitles` undefined（编译失败 `undefined: applySourceTitles`）。

- [ ] **Step 3: `Source` 加字段 + 实现 applySourceTitles/decorateSourceTitles**

  编辑 `internal/knowledge/application/rag_service.go`。先在 `Source` struct（:345-352）补字段：

```go
type Source struct {
	DocumentID    string
	ChunkID       string
	Content       string
	ParentContent string // non-empty when parent chunk was fetched (Parent-Child strategy)
	ChunkIndex    int64
	Score         float32
	// DocumentTitle is the owning document's source file name, backfilled at
	// the end of Query for the /knowledge/query citation cards. Display-only;
	// empty when the doc index read fails (never fails the query).
	DocumentTitle string
}
```

  在同一文件任意位置（建议紧邻 `documentTitles`，:1431 后）新增：

```go
// decorateSourceTitles backfills each query source's display title from the
// workspace document index. Titles are display metadata only: index read
// failures leave them empty rather than failing the query (documentTitles
// logs the warning).
func (rs *RAGService) decorateSourceTitles(ctx context.Context, tenantID, workspaceID string, sources []Source) {
	if len(sources) == 0 {
		return
	}
	applySourceTitles(sources, rs.documentTitles(ctx, tenantID, workspaceID))
}

func applySourceTitles(sources []Source, titles map[string]string) {
	for i := range sources {
		if title, ok := titles[sources[i].DocumentID]; ok {
			sources[i].DocumentTitle = title
		}
	}
}
```

- [ ] **Step 4: 运行确认绿**

  Run: `go test ./internal/knowledge/application/ -run TestApplySourceTitles -v`
  Expected: PASS（2 subtests）。

- [ ] **Step 5: `Query` 尾部调用 decorateSourceTitles**

  编辑 `rag_service.go` `Query`（:481 `rs.expandParentContext(ctx, req, result)` 之后、日志之前）插入：

```go
	rs.expandParentContext(ctx, req, result)

	// 面板来源卡片需要文档名：expandParentContext 可能追加 parent 分块，
	// 故 title 回填放在其后，保证所有来源都被覆盖。
	rs.decorateSourceTitles(ctx, req.TenantID, req.WorkspaceID, result.Sources)
```

- [ ] **Step 6: rag_handler Query 响应补 document_title 与 workspace**

  编辑 `api/http/handler/rag_handler.go`（现 :203-211 的 sources gin.H 构造）：

```go
	sources := make([]gin.H, len(result.Sources))
	for i, src := range result.Sources {
		sources[i] = gin.H{
			"document_id":    src.DocumentID,
			"document_title": src.DocumentTitle,
			"content":        src.Content,
			"chunk_index":    src.ChunkIndex,
			"score":          src.Score,
			// workspace 传请求名：前端 SourceItem 用它判定可预览并传给预览抽屉。
			"workspace": req.Workspace,
		}
	}
```

  `req.Workspace` 即 workspace 名（handler 用 `GetWorkspace(ctx, tenantID, req.Workspace)` 按名解析，见 :167-179）。注意 `Source` 是 knowledge application 类型，`src.DocumentTitle` 在 Step 3 后可用。

- [ ] **Step 7: 后端编译 + 相关测试绿**

  Run: `go build ./... && go vet ./... && go test ./internal/knowledge/... ./api/http/handler/ -run 'TestRAGHandlerQuery|TestQuery|TestApplySourceTitles' -short`
  Expected: 编译通过；现有 Query 测试为粗粒度状态断言（status 200/400/401/500），新增键不破坏；`TestApplySourceTitles` PASS。

- [ ] **Step 8: 提交**

```bash
git add internal/knowledge/application/rag_service.go internal/knowledge/application/rag_service_sources_test.go api/http/handler/rag_handler.go
git commit -m "feat(knowledge): /knowledge/query sources 补 document_title/workspace

knowledge Source 增加 DocumentTitle，Query 尾部用现成 documentTitles 批量
回填源文件名；rag_handler 响应补 document_title 与 workspace 键。前端
querySourceSchema 早已声明这两字段（default ''），面板来源卡片可直接预览。

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: chat_messages 增加 sources_json 列（tenant_schema.sql）

**Files:**

- Modify: `pkg/storage/postgres/tenant_schema.sql`（chat_messages 定义区）

**Interfaces:**

- Produces: `chat_messages` 存在列 `sources_json JSONB NOT NULL DEFAULT '[]'`，供 Task 5 的 INSERT/SELECT 使用。存量租户无需手操：服务器每次启动 `BootstrapTenants → bootstrapTenantSchemas → provisionAll(ProvisionAllTenantSchemas)`（cmd/server/runtime.go:107-142）在 schema 锁下对全部租户幂等重放本文件，`ADD COLUMN IF NOT EXISTS` 自动补列。

- [ ] **Step 1: 在 artifacts_json 之后插入列定义**

  编辑 `pkg/storage/postgres/tenant_schema.sql`。找到（用 Read 定位行号）：

```sql
ALTER TABLE chat_messages
    ADD COLUMN IF NOT EXISTS artifacts_json JSONB NOT NULL DEFAULT '[]';
```

  在其后整段插入（与上一条 ALTER 同缩进、两行一条）：

```sql
-- sources_json 持久化 assistant 回答的 RAG 溯源来源（camelCase JSON，与 live
-- SSE sources 帧同构），供刷新/重进会话时回放；旧行默认 []（无来源，不迁移）。
ALTER TABLE chat_messages
    ADD COLUMN IF NOT EXISTS sources_json JSONB NOT NULL DEFAULT '[]';
```

- [ ] **Step 2: 校验 SQL 幂等语法（白盒）**

  Run: `grep -n "sources_json" pkg/storage/postgres/tenant_schema.sql`
  Expected: 命中且仅命中新增的 1 处 `sources_json`（ADD COLUMN 行 + 注释行）；上下文 `artifacts_json` 的 ADD 语句完整未改。

- [ ] **Step 3: 提交**

```bash
git add pkg/storage/postgres/tenant_schema.sql
git commit -m "feat(storage): chat_messages 增加 sources_json 列

复刻 artifacts_json 先例：tenant-only DDL 唯一基线 tenant_schema.sql 幂等
ADD COLUMN IF NOT EXISTS，存量租户由启动 ProvisionAllTenantSchemas 自动升级。

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: domain.ChatMessage 增加 Sources 字段

**Files:**

- Modify: `internal/agent/domain/agent.go`（ChatMessage struct :129-147）

**Interfaces:**

- Consumes: `domain.RAGSearchSource`（同文件 :512-521，camelCase tags）。
- Produces: `ChatMessage.Sources []RAGSearchSource`，被 Task 6（chat_store 读写）与 Task 7（messageResponse / persistChatMessages）引用。

- [ ] **Step 1: 加字段**

  编辑 `internal/agent/domain/agent.go` `ChatMessage`，在 `Artifacts` 之后、`TraceID` 注释块之前插入：

```go
	Artifacts []ExecutionArtifact
	// Sources are the RAG citation sources an assistant answer was grounded on,
	// persisted to chat_messages.sources_json and replayed on history load.
	// Serialized camelCase via RAGSearchSource json tags — identical to the live
	// SSE done frame's sources, so live and replay render the same cards.
	Sources []RAGSearchSource
	// TraceID links the message to its agent execution trace. Persisted so
```

- [ ] **Step 2: 编译通过**

  Run: `go build ./internal/agent/...`
  Expected: 编译通过（纯新增字段，未破坏现有字面量）。

- [ ] **Step 3: 提交**

```bash
git add internal/agent/domain/agent.go
git commit -m "feat(agent): ChatMessage 增加 Sources 字段

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: chat_store 读写 sources_json + 单测（mock 断言同步）

**Files:**

- Modify: `internal/agent/infrastructure/persistence/chat_store.go`（AddMessage :225-318、ListMessages :333-368、新增 encodeSources/decodeSources）
- Modify: `internal/agent/infrastructure/persistence/chat_store_test.go`（全部 INSERT/SELECT mock 的列与 WithArgs、新增 SourcesRoundTrip 与 decode 测试）

**Interfaces:**

- Consumes: `domain.ChatMessage.Sources []RAGSearchSource`（Task 5）；`domain.RAGSearchSource` camelCase json tag。
- Produces: `encodeSources([]domain.RAGSearchSource) ([]byte, error)`、`decodeSources([]byte) ([]domain.RAGSearchSource, error)`（len0/`null`/空数组 → 空非 nil 切片）。

- [ ] **Step 1: 写 decode/encode 失败测试**

  在 `chat_store_test.go` 末尾追加：

```go
func TestDecodeSources(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantLen int
		wantErr bool
	}{
		{name: "empty", raw: "", wantLen: 0},
		{name: "null is empty slice", raw: "null", wantLen: 0},
		{name: "empty array", raw: "[]", wantLen: 0},
		{name: "camelCase round trip", raw: `[{"workspaceId":"ws-1","workspaceName":"产品库","chunkId":"c-1","documentId":"doc-1","documentTitle":"用户手册.pdf","snippet":"s","score":0.91,"hasScore":true}]`, wantLen: 1},
		{name: "malformed", raw: `{`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeSources([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("want %d sources, got %d (%#v)", tc.wantLen, len(got), got)
			}
			if got == nil {
				t.Fatal("decodeSources must return non-nil slice")
			}
			if tc.name == "camelCase round trip" && got[0].DocumentTitle != "用户手册.pdf" {
				t.Fatalf("want camelCase fields decoded, got %#v", got[0])
			}
		})
	}
}
```

- [ ] **Step 2: 运行确认红**

  Run: `go test ./internal/agent/infrastructure/persistence/ -run TestDecodeSources -v`
  Expected: FAIL——`decodeSources` undefined。

- [ ] **Step 3: 实现 encodeSources/decodeSources**

  编辑 `chat_store.go`，在 `decodeExecutionArtifacts` 之前（或其后）新增：

```go
// encodeSources serializes RAG citation sources into chat_messages.sources_json.
func encodeSources(sources []domain.RAGSearchSource) ([]byte, error) {
	if sources == nil {
		sources = []domain.RAGSearchSource{}
	}
	return json.Marshal(sources)
}

// decodeSources restores persisted citation sources. Empty / null / absent all
// map to an empty non-nil slice so history replay always yields [].
func decodeSources(raw []byte) ([]domain.RAGSearchSource, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return []domain.RAGSearchSource{}, nil
	}
	var sources []domain.RAGSearchSource
	if err := json.Unmarshal(raw, &sources); err != nil {
		return nil, err
	}
	if sources == nil {
		sources = []domain.RAGSearchSource{}
	}
	return sources, nil
}
```

  （`strings`、`json`、`domain` 均已 import，见 chat_store.go:1-23。）

- [ ] **Step 4: 运行确认绿**

  Run: `go test ./internal/agent/infrastructure/persistence/ -run TestDecodeSources -v`
  Expected: PASS（5 subtests）。

- [ ] **Step 5: AddMessage 默认值 + 编码 + INSERT 加列**

  编辑 `chat_store.go` `AddMessage`。在现有 artifacts 默认值/编码块（:229-238）之后追加：

```go
	if msg.Sources == nil {
		msg.Sources = []domain.RAGSearchSource{}
	}
	sourcesJSON, err := encodeSources(msg.Sources)
	if err != nil {
		return fmt.Errorf("chat_store: encode sources: %w", err)
	}
```

  （`sourcesJSON, err :=` 中 `sourcesJSON` 为新变量，同作用域合法复用 `err`。）

  事务内 INSERT（:250-255）改为：

```go
		if err := tx.QueryRow(ctx,
			`INSERT INTO chat_messages (conversation_id, role, content, steps_json, is_error, artifacts_json, sources_json, visibility, trace_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			 RETURNING id, created_at`,
			msg.ConversationID, msg.Role, msg.Content, string(msg.StepsJSON), msg.IsError, string(artifactsJSON), string(sourcesJSON), msg.Visibility, msg.TraceID,
		).Scan(&msg.ID, &msg.CreatedAt); err != nil {
			return err
		}
```

- [ ] **Step 6: ListMessages SELECT + scan + decode**

  编辑 `chat_store.go` `ListMessages`。SELECT（:337）加 `m.sources_json`：

```go
			`SELECT m.id, m.conversation_id, m.role, m.content, m.steps_json, m.is_error, m.created_at, m.artifacts_json, m.sources_json, m.visibility
			 FROM chat_messages m
```

  scan（:350-355）改为：

```go
			var m domain.ChatMessage
			var artifactsJSON, sourcesJSON []byte
			if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content,
				&m.StepsJSON, &m.IsError, &m.CreatedAt, &artifactsJSON, &sourcesJSON, &m.Visibility); err != nil {
				return err
			}
			m.Artifacts, err = decodeExecutionArtifacts(artifactsJSON)
			if err != nil {
				return fmt.Errorf("decode message artifacts: %w", err)
			}
			m.Sources, err = decodeSources(sourcesJSON)
			if err != nil {
				return fmt.Errorf("decode message sources: %w", err)
			}
```

- [ ] **Step 7: 同步既有 pgxmock 断言（列 + WithArgs）**

  编辑 `chat_store_test.go`，逐处更新（INSERT/SELECT 列序 = `artifacts_json` 与 `visibility` 之间插入 `sources_json`）：

  - `TestChatStore_AddMessage`（:177）INSERT WithArgs 加 sources 参（position 7，值 `"[]"`）：
    `WithArgs("conv-1", "user", "hello", string(steps), false, "[]", "[]", "user", "trace-1")`
  - `TestChatStore_AddMessage_nilStepsDefaultsToEmpty`（:210）：
    `WithArgs("conv-1", "user", "hi", "[]", false, "[]", "[]", "user", "trace-2")`
  - `TestChatStore_ArtifactRoundTrip`（:264）INSERT WithArgs：`("conv-1", "assistant", "ok", "[]", false, string(raw), "[]", "user", "")`；SELECT NewRows 列数组（:273）插入 `"sources_json"` 于 `artifacts_json` 与 `visibility` 之间，行（:274）值加 `json.RawMessage("[]")`：
    `NewRows([]string{"id", "conversation_id", "role", "content", "steps_json", "is_error", "created_at", "artifacts_json", "sources_json", "visibility"}).AddRow("m1", "conv-1", "assistant", "ok", json.RawMessage("[]"), false, now, raw, json.RawMessage("[]"), "user")`
  - `TestChatStore_ListMessages`（:230-234）列数组与两行各加 `sources_json`（值 `json.RawMessage("[]")`），行序变为 `..., artifacts_json, sources_json, visibility`。
  - `TestChatStore_HistoricalMessageHydratesEmptyArtifacts`（:330-331）列数组与行各加 `sources_json` 值 `json.RawMessage("[]")`。

- [ ] **Step 8: 新增 SourcesRoundTrip 单测（INSERT/SELECT 往返）**

  在 `chat_store_test.go` 末尾追加（镜像 `TestChatStore_ArtifactRoundTrip` 结构）：

```go
func TestChatStore_SourcesRoundTrip(t *testing.T) {
	store, mock := newChatStoreWithMock(t)
	defer mock.Close()
	now := time.Now()
	sources := []domain.RAGSearchSource{{
		WorkspaceID: "ws-1", WorkspaceName: "产品知识库", ChunkID: "chunk-1",
		DocumentID: "doc-1", DocumentTitle: "用户手册.pdf", Snippet: "s",
		Score: 0.91, HasScore: true,
	}}
	raw, err := encodeSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	msg := &domain.ChatMessage{
		ConversationID: "conv-1", Role: "assistant", Content: "ok",
		Sources: sources, TraceID: "trace-rt",
	}

	expectTenantTx(mock)
	mock.ExpectExec("UPDATE chat_conversations").WithArgs("conv-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO chat_messages").WithArgs("conv-1", "assistant", "ok", "[]", false, "[]", string(raw), "user", "trace-rt").
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow("m1", now))
	mock.ExpectCommit()
	if err := store.AddMessage(context.Background(), "t1", msg); err != nil {
		t.Fatal(err)
	}

	expectTenantTx(mock)
	mock.ExpectQuery("SELECT m.id, m.conversation_id").WithArgs("conv-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "conversation_id", "role", "content", "steps_json", "is_error", "created_at", "artifacts_json", "sources_json", "visibility"}).
			AddRow("m1", "conv-1", "assistant", "ok", json.RawMessage("[]"), false, now, json.RawMessage("[]"), raw, "user"))
	mock.ExpectCommit()
	got, err := store.ListMessages(context.Background(), "t1", "conv-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Sources) != 1 || got[0].Sources[0].DocumentTitle != "用户手册.pdf" || got[0].Sources[0].HasScore != true {
		t.Fatalf("unexpected sources: %#v", got[0].Sources)
	}
}
```

- [ ] **Step 9: 运行 persistence 单测确认全绿**

  Run: `go test ./internal/agent/infrastructure/persistence/ -v`
  Expected: 全部 PASS（含既有 AddMessage/ListMessages/ArtifactRoundTrip/HistoricalMessage 与新增 DecodeSources/SourcesRoundTrip）。任何 mock 断言不匹配会在此步暴露。

- [ ] **Step 10: 提交**

```bash
git add internal/agent/infrastructure/persistence/chat_store.go internal/agent/infrastructure/persistence/chat_store_test.go
git commit -m "feat(agent): chat_messages sources_json 读写往返

AddMessage 落 sources_json、ListMessages 读回并 decodeSources；新增
encode/decode 单测与 INSERT/SELECT pgxmock 往返测试，既有 mock 断言随
列序同步。

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7: 写路径接入 + GET messages 透出 sources

**Files:**

- Modify: `internal/agent/application/agent.go`（persistChatMessages，assistant 主答消息）
- Modify: `api/http/handler/chat_handler.go`（messageResponse 透出 sources）

**Interfaces:**

- Consumes: Task 5 的 `ChatMessage.Sources`；`result *AgentResult`（`application.AgentResult = domain.AgentResult`，agent.go:46），`result.Sources []RAGSearchSource`。
- Produces: 落库的 assistant 主答消息带来源；`GET /conversations/:convID/messages` 每条消息 JSON 含 `sources` 键（camelCase，空即 `[]`）。前端 `chatMessageSchema` 为 `.passthrough()`（agent.ts:267）且 `ChatMessage.sources` 已声明（agent.ts:287）——**前端零改动**，Task 1 的 camelCase 读取在 live 与回放共用同一渲染。

- [ ] **Step 1: persistChatMessages 给 agentMsg 接 result.Sources**

  编辑 `internal/agent/application/agent.go` `persistChatMessages`，assistant 主答消息字面量（:1753-1758）加字段：

```go
	agentMsg := &ChatMessage{
		ConversationID: cfg.ConversationID, Role: "assistant", Content: result.Output,
		UserID: cfg.UserID, AgentID: agentID, MemoryScope: memoryScope,
		SkipOutbox: false, Visibility: domain.ChatMessageVisibilityUser, Artifacts: result.Artifacts,
		Sources: result.Sources, TraceID: cfg.TraceID,
	}
```

  说明：userMsg 与 summaryMsg（internal）刻意**不**带来源——只有 assistant 主答是溯源载体。

- [ ] **Step 2: messageResponse 透出 sources（nil → []）**

  编辑 `api/http/handler/chat_handler.go` `messageResponse`（:225-240）。不要整体重写函数体——先 Read 该函数，**保留现有 keys/steps/artifacts/created_at 处理原样**，只做两处小改：

  1. 在该函数顶部（`steps` 的 nil 默认块附近）加入 sources 的 nil 默认局部变量：

```go
	sources := m.Sources
	if sources == nil {
		sources = []domain.RAGSearchSource{}
	}
```

  1. 在 `return gin.H{ ... }` 字面量中，找到既有 `"artifacts": executionArtifactsResponse(m.Artifacts),`（或等价的 artifacts 键行），在其正下方新增一行：

```go
		"sources":         sources, // camelCase json tag 序列化，与 SSE done 帧同构；DB 读回恒 []，兜 nil 合成消息
```

  其它 keys 一行不改。改后该函数应在逻辑上与以下形状一致（示意，以文件现状为准——不要用本块替换整个函数）：

```go
func messageResponse(m *agent.ChatMessage) gin.H {
	// ...既有 steps nil 默认逻辑保持原样...
	sources := m.Sources
	if sources == nil {
		sources = []domain.RAGSearchSource{}
	}
	return gin.H{
		"id": m.ID, "conversation_id": m.ConversationID, "role": m.Role, "content": m.Content,
		"steps": steps, "artifacts": executionArtifactsResponse(m.Artifacts),
		"sources": sources, "is_error": m.IsError, "created_at": m.CreatedAt,
	}
}
```

  1. `domain.RAGSearchSource` 需要 `domain` 包在作用域内：`grep -rn '"internal/agent/domain"' api/http/handler/ | head -3`，若 chat_handler.go 本身未 import，把 `internal/agent/domain` 加入 import 组，模块前缀复制自 grep 结果的现有写法。

  说明：DB 读回的 `ChatMessage.Sources` 经 `decodeSources` 恒非 nil，会序列化为 `[]`；nil 默认仅兜住未落库的合成消息，避免输出 `"sources": null`。改动是消息 JSON 的 additive 新键，不改参数契约、不新增端点。

- [ ] **Step 3: 编译 + 相关单测**

  Run: `go build ./... && go vet ./... && go test ./internal/agent/... ./api/http/... -short`
  Expected: 全绿。messageResponse 仅新增键，既有 handler 测试若按整体 JSON 相等断言需同步——若无报错则不变；若有，仅把期望对象补 `"sources": []`。

- [ ] **Step 4: 提交**

```bash
git add internal/agent/application/agent.go api/http/handler/chat_handler.go
git commit -m "feat(agent): assistant 主答落库携带来源并透出 GET messages

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 8: 全量验证门禁 + 系统验收交接

**Files:**

- 无代码改动；执行门禁并把系统验收交给验收 agent。

**Interfaces:**

- Consumes: Task 1-7 全部改动，处于 clean commit 链上。

- [ ] **Step 1: 质量与编译门禁**

  Run: `go vet ./... && go test -short ./...`（仓库根）→ Expected: 绿。
  Run: `make fe-lint && make fe-build`（仓库根；Makefile 目标内部进入 web/，CLAUDE.md 规定的 PR 前端门禁）→ Expected: 绿。
  Run: `make code-quality` → Expected: 无新增超限函数（本改动均为小函数/字段）。

- [ ] **Step 2: 契约/后端单测全量**

  Run: `go test -race -timeout 30s ./...`
  Expected: 绿（含 contract_test 与全部 chat_store/rag 测试）。`api/http/testdata/contracts/post_knowledge_query.golden.json` 无引用方（此前 grep 证实），B 改动不触碰 golden；若 CI parity 报 diff 则按 CI 输出同步该文件。

- [ ] **Step 3: DDL 幂等性校验（tenant_schema.sql 真实 provision 测试）**

  `chat_store` 单测为 pgxmock，验证不了 DDL 语法；SQL 正确性交给真库 provision 测试。先 `ls pkg/storage/postgres/*_test.go` 确认是否存在把 `tenant_schema.sql` 真应用到测试库的测试（CLAUDE.md 要求覆盖历史 schema 顺序/幂等，此类测试存在），存在则运行：

  Run: `go test ./pkg/storage/postgres/ -run Provision -short`
  Expected: 绿（新 ALTER 幂等重放不报错、重复 apply 不炸）。若该包无命中测试，如实记录"DDL 由 chat_store mock + 后续验收 DB 链路共同覆盖"，不做手工裸 SQL。

- [ ] **Step 4: 提交 / 推送并开 PR（CI gate）**

  确认分支干净后推送并开 PR：

```bash
git push -u origin feat/rag-source-citation
gh pr create --base main --title "feat(agent/knowledge): RAG 溯源来源文档链路修复与落库回放" --body "$(cat <<'EOF'
What: 三段修复
- A 前端 ChatCitationSource/SourceCardList 对齐 camelCase wire（SSE 与回放同构）。
- B /knowledge/query sources 补 document_title/workspace（knowledge Source 回填 + rag_handler 透出）。
- C assistant 主答来源落库 chat_messages.sources_json，重进会话历史回放。

Why: 「来源文档」链路根因 = 前后端字段大小写错位、query 缺字段、无历史回放。

HowToTest: agent 知识问答 live 显示来源卡片可预览；/knowledge/query 显示文档名；
刷新/重进会话后历史 assistant 来源卡片仍在；无来源不渲染空卡。
EOF
)"
```

  push 触发 CI 后：先 `git fetch origin main` 比较 PR base 是否落后；若落后先把最新 main 合入分支、本地绿后 push（merge commit），再等 CI。

- [ ] **Step 5: 系统验收（交接专用验收 agent）**

  在 clean commit 上调用 `stratum-e2e-tester` agent 完成系统验收（封装 stratum-e2e-development skill，按 `.test/verification.yaml` 风险级选择本地无头 Chromium E2E + `make test-verify-before-pr`）。三层验收标准：
  1. agent 知识问答 live：来源卡片显示文档名/片段/分数、可点开预览；无来源无空卡。
  2. `/knowledge/query`：来源条目显示文档名、可预览。
  3. 刷新/重进会话：历史 assistant 消息来源卡片仍在。

  所有登录测试用无头浏览器；禁止输出 token/cookie/密钥。E2E 报告非 GitHub trusted status，merge_authority=ci——CI 全绿后合并，再用 `git worktree remove ../stratum-rag-source-citation` 清理。

---

## 自审记录（写入时执行）

- **Spec 覆盖**：A(§4)→Task1/2；B(§5)→Task3；C DDL(§6.1)→Task4、写路径(§6.2)→Task5/7、读路径(§6.3)→Task6/7(messageResponse)、滚动兼容默认`[]`→Task6 decodeSources + AddMessage 默认；测试(§7)→Task1(A 渲染)、Task3(B 单测)、Task6(C 往返)；部署排序(§6.4)→Task4 启动 bootstrap 已满足；验收三层→Task8 Step5。非目标(§8)均未纳入。
- **占位扫描**：无 TBD/「参考 Task N」；每个改动带完整代码与命令。唯一宽式步骤（Task2 Step2、Task8 golden）以 tsc/CI 输出为准且明示「不得臆造」，非占位。
- **类型一致性**：sources wire 全链路 camelCase（RAGSearchSource tag ↔ 前端接口 ↔ chat_store 原样存取 → GET 透出 → 同构渲染）；`Source.DocumentTitle` snake→`document_title` 键对齐前端 `querySourceSchema`；JSONB 传 string（pgx 规范）；`result.Sources`（[]domain.RAGSearchSource）直接赋 `ChatMessage.Sources` 同类型。
