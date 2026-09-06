# RAG 溯源「来源文档」链路修复与落库设计

> 日期：2026-09-03 · 范围：来源文档溯源展示（live + 历史回放）· 状态：草案待 review

## 1. 背景与根因

Stratum 已有两条「来源文档」溯源展示链路，本应让用户核验知识问答依据，但都未真正可用：

- **Agent 知识问答**：后端 SSE done 帧 `sources` 字段的 wire 契约早已是 **camelCase**（`domain.RAGSearchSource` 带 json tag，`internal/agent/domain/agent.go:512-521`；handler 序列化 `agent_exec_handler.go:258-282`；单测锁定 camelCase，`agent_exec_handler_test.go:215`）。但前端 `ChatCitationSource` 类型与 `SourceCardList` 组件整体按 **PascalCase** 读取（`web/src/modules/agent/model/agent.ts:172-185`、`components/SourceCardList.tsx`），且**全链路无字段转换层** → 运行时 `s.DocumentTitle / s.WorkspaceName / s.Snippet / s.HasScore / s.Score` 全部 `undefined` → 卡片标题回落「未知文档」、分数/片段缺失、不可点击预览。前端注释（agent.ts:172-175、198-199）声称「后端无 tag 序列化为 PascalCase」，与现状不符，属过期注释。
- **知识库查询测试面板 `/knowledge/query`**：后端 `rag_handler.go:203-211` 返回 sources 只带 `document_id/content/chunk_index/score`，**缺 `document_title` 与 `workspace`**；前端 `SourceItem` 已按 snake_case 字段就绪（`WorkspaceQueryResult.tsx:54-99`），因缺字段导致标题退化为截断 id、不可点开预览。
- **历史回放缺失**：`chat_messages`（`pkg/storage/postgres/tenant_schema.sql`）无 sources 列，来源卡片只出现在「本次 live SSE done 帧」，会话刷新/重新进入后从 DB 读历史 → 引用丢失。

**同一 done 帧两种风格并存是既有事实**：`sources` 子字段 camelCase（`RAGSearchSource` 带 tag），`noAnswer` 子字段 PascalCase（`domain.NoAnswerInfo` 无 tag，agent.go:446-454，注释明说 PascalCase 且与前端 `NoAnswerInfo` 匹配、工作正常）。本次**只修前端 sources 消费**，不碰 noAnswer。

## 2. 目标与成功标准

让「来源文档卡片 → 点开预览原文」在 agent 对话与知识库查询面板真正可用，且历史会话可回放来源：

1. Agent 知识问答流式完成后：来源卡片显示**文档名 + snippet + 分数**，含 workspace+document 时**可点开原文预览**；无来源不渲染空卡。
2. `/knowledge/query` 面板来源条目显示**文档名**、可点开原文预览。
3. **刷新 / 重新进入会话**后，历史 assistant 消息的来源卡片仍展示（读自 DB）。
4. `go vet && go test -short ./...`、`make fe-lint && make fe-build` 绿；改动涉及的 contract 黄金文件一致；新增前端组件测试绿。

## 3. 数据流（目标态）

```
检索命中 → AgentResult.Sources(domain.RAGSearchSource, camelCase tag)
        → (live) SSE done.sources(camelCase) ──→ 前端 ChatCitationSource(camelCase) → SourceCardList 渲染
        → (落库) AddMessage sources_json 列 → chat_messages
              → (回放) ListMessages 读回 ChatMessage.Sources → GET messages DTO → 前端 SourceCardList 渲染
```

字段映射契约（wire 实际大小写，以 Go 实际 tag 为准）：

| 对象 | wire 风格 | 依据 |
|---|---|---|
| `sources[]`（RAGSearchSource） | camelCase：`workspaceId/workspaceName/chunkId/documentId/documentTitle/snippet/score/hasScore` | agent.go:512-521 |
| `noAnswer`（NoAnswerInfo） | PascalCase：`Reason/RetrievedCount/...` | agent.go:446-454（本次不动） |
| `/knowledge/query` sources | snake_case：`document_id/chunk_index/score/content`（补 `document_title/workspace`） | rag_handler.go:203-211 |

## 4. A｜Agent 来源卡片：前端对齐 camelCase

改动文件（前端）：

- `web/src/modules/agent/model/agent.ts`
  - `ChatCitationSource` interface 字段改 camelCase 并保持可选：`workspaceId/workspaceName/chunkId/documentId/documentTitle/snippet/score/hasScore`。
  - 更正过期注释（约 172-175、198-199）：分别说明 `sources`（camelCase，后端 RAGSearchSource 带 tag）与 `noAnswer`（PascalCase，后端 NoAnswerInfo 无 tag）的 wire 规则**已不同**，不再沿用「同一序列化规则」表述。
- `web/src/modules/agent/components/SourceCardList.tsx`：`s.DocumentTitle/s.DocumentID/s.WorkspaceName/s.ChunkID/s.Snippet/s.HasScore/s.Score` 全部改 camelCase 键。
- 全局 grep 前端对 `ChatCitationSource` 旧 PascalCase 字段（`DocumentTitle/WorkspaceName/DocumentID/ChunkID/HasScore/Snippet/Score`）的引用并统一改齐（含 `ChatMessageList`、`useChatPage`、测试与 story 等）。
- 数据流无需改造：done 帧 sources 是 raw JSON 直达前端，无 zod 中间 parse（`useChatPage.ts:486` 直接 `sources: st.result?.sources ?? []`）。

测试：

- `SourceCardList` 当前零覆盖测试 → 新增渲染测试：
  - 喂 camelCase 数据 → 断言渲染文档名、snippet、分数徽标；含 `workspaceName + documentId` 时点击可开预览。
  - 喂 `[]`/`undefined` → 不渲染。

## 5. B｜知识库查询面板：后端补字段

`api/http/handler/rag_handler.go` query 响应 sources（203-211 的 gin.H 构造）补：

- `document_title`：源文件名（knowledge 侧已对访问白名单过滤后的展示标题）。
- `workspace`：workspace 名称，供 `SourceItem` 判定可预览并传给 `DocPreviewDrawer`。

前端 `QuerySource` schema 与 `SourceItem`（`knowledge.ts`、`WorkspaceQueryResult.tsx`）已就绪，无需改；仅若该响应进入 `api/http/contract_test.go` 黄金文件需同步更新。

> 实现期确认点 1：knowledge 域 `RAGService.Query` 返回 Source 的字段名（`DocumentTitle`/`WorkspaceName`）与 rag_handler 可取到的具体结构；若缺展示所需字段则由 knowledge 侧检索结果补足，不得在 handler 内拼 SQL。

## 6. C｜Sources 落库回放（复刻 `artifacts_json` 先例）

`chat_messages` 已有复杂结构 JSONB 落列并历史回放的成功先例：`steps_json`、`artifacts_json`、`visibility`、`trace_id` 均通过 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 追加，`AddMessage`/`ListMessages`（`internal/agent/infrastructure/persistence/chat_store.go:225/333`）读写，前端 `artifacts` 已正常回放。sources 完全复刻同一路径。

### 6.1 DDL

`pkg/storage/postgres/tenant_schema.sql` 在 `chat_messages` 定义区追加（与 artifacts_json 同款幂等写法）：

```sql
ALTER TABLE chat_messages
    ADD COLUMN IF NOT EXISTS sources_json JSONB NOT NULL DEFAULT '[]';
```

存量历史租户经现有 tenant schema 幂等重放机制升级（`artifacts_json`/`trace_id` 等历史追加列即按此升级存量租户）。**先升级 schema 再发代码**，规避新代码 `SELECT sources_json` 在旧库上 500。

### 6.2 写路径

- `internal/agent/domain`：`ChatMessage` 增字段 `Sources []port.RAGSearchSource`（或复用域内 `RAGSearchSource` 类型），序列化 tag 取 camelCase `json:"sources,omitempty"`（与 SSE live 帧一致，回放/直播同构）。
- `chat_store.go AddMessage`：INSERT 增列 `sources_json`；编码仿 `encodeExecutionArtifacts`（`json.Marshal` → 按仓库规范以 string 传 JSONB）。
- 新写入点：agent 执行完成、把 assistant 结果写入消息处（`AddMessage` 调用方）将 `AgentResult.Sources` 带入构造的 `ChatMessage.Sources`。

> 实现期确认点 2：`AddMessage` 的调用方（agent application 服务落消息处）精确定位，把 `AgentResult.Sources` 接到该处构造消息的字段；注意 `role='agent'` 与 `trace_id` 写入的同一次插入。

### 6.3 读路径

- `chat_store.go ListMessages`：SELECT 增 `sources_json`；scan 后反序列化回 `ChatMessage.Sources`。
- 历史消息读取 API（conversation messages 的 DTO 透出）带出 `sources`；前端加载历史消息时 `ChatMessage.sources` 已存在即可渲染。
- **滚动兼容**：旧消息默认 `'[]'`，前端 `sources ?? []` 容错，无需迁移回填。

### 6.4 兼容性与部署排序

- 消息 JSON 字段 additive（仅新增 `sources` 键），无 proto/DTO 参数契约变化，不新增 HTTP 端点。
- 部署排序：tenant schema 重放（DDL）→ 后端 → 前端。滚动期旧后端不读 `sources_json`（未 SELECT）可工作；新后端读旧库列不存在 → 必须 DDL 先行。

## 7. 测试与验收

- 前端：`SourceCardList` 渲染测试（A）；`make fe-lint && make fe-build`。
- 后端：`go vet && go test -short ./...`；`chat_store` 相关单测覆盖 sources_json 写读往返与默认 `'[]'`；若 rag_handler query 在 contract 黄金文件则同步。
- 端到端（三层验收标准）：
  1. agent 知识问答 live：来源卡片显示名/片段/分数、可点开预览；无来源无空卡。
  2. `/knowledge/query`：来源条目显示文档名、可预览。
  3. 刷新 / 重进会话：历史 assistant 消息来源卡片仍在。

> 命中「功能 / Bug 修复」风险级，实施阶段按 `.test/verification.yaml` 定级走完整验证（前端无头浏览器对账登录态，禁止有头浏览器）。

## 8. 不在本次范围

- 答案内引用角标 `[1][2]`（模型输出句子 ↔ chunk 一一对应）——需改造 `formatSources`/注入引用指令，单列。
- 引用忠实度 / groundedness 校验（evaluation `citation_correct` 由检索层推到答案层），单列。
- `noAnswer` PascalCase → camelCase 统一，不在本次（会破坏当前工作正常的前端匹配）。
- snippet 之外的 enrichment、来源按文档分组去重等增强，不做。

## 9. 决策记录

- **前端对齐 camelCase（而非后端改回 PascalCase）**：后端 `RAGSearchSource` 带 camelCase tag 是既有意图（注释 + 单测锁定），done 帧整体 camelCase、`factCheck` 等 camelCase 消费正常；改前端即修复现有部署，无需后端发版同步；后端整体改 PascalCase 会让 done 帧内部分裂更甚且要求前后端同步发布。
- **sources 落库复刻 artifacts_json 路径**：同一 repo、同一消息 API、同一前端模型字段 `artifacts` 回放已工作，是最小差异的既定成功模式；避免新造消息持久化/读取机制。
- **最小可交付分三段**：live 可用（A+B）为骨架；落库回放（C）为体验闭环；不引入文本引用/核验，避免范围膨胀。
