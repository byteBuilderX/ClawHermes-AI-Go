# API Development Rules

## Route Registration

所有路由集中注册于 `api/http/router.go`，按域拆分为独立私有函数，禁止在 handler 文件中散落注册。

```go
// router.go 中注册顺序（除 E2E-only 路由外共 17 组）
registerAuth(r, c, requireActive)
registerModelCatalogue(r, c)
registerDashboard(r, c)
registerHealth(r, c)
registerSkills(r, c, requireActive)
registerEvaluations(r, c, requireActive)
registerAgents(r, c, requireActive)
registerResourceChangeProposals(r, c, requireActive)
registerOperationProposals(r, c, requireActive)
registerWorkflows(r, c, requireActive)
registerCollab(r, c, requireActive)
registerScheduledTasks(r, c, requireActive)
registerKnowledge(r, c, requireActive)
registerMCP(r, c, requireActive)
registerMemory(r, c, requireActive)
registerAudit(r, c, requireActive)
registerLLMAdmin(r, c, requireActive)
```

`registerE2ERoutes` 仅在 `STRATUM_E2E_MODE=true` 时注册 `GET /e2e/routes`（只读路由清单，测试态暴露）。`/avatars/:filename` 在配置了 `AvatarDir` 时由 `NewRouter` 直接注册（静态头像文件）。

## Complete Route List

### 无需认证

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 服务健康检查，返回 `{"status":"ok","service":"Stratum"}` |
| GET | `/livez` | 存活探针，返回 `{"status":"ok"}` |
| GET | `/readyz` | 就绪探针（依赖 readiness 检查，未就绪返回 503） |
| GET | `/metrics` | Prometheus scrape 端点 |

### Model Catalogue（JWT + member 角色）

| 方法 | 路径 | Handler |
|------|------|---------|
| GET | `/models` | ListModels（列出可用 LLM 模型） |

### Dashboard（JWT + member 角色）

| 方法 | 路径 | Handler |
|------|------|---------|
| GET | `/dashboard/overview` | Overview（平台装配了 DashboardService 时注册） |

### Auth（GitHub OAuth / 密码登录 / 访客，均无需 tenant 角色）

| 方法 | 路径 | Handler | 条件 / 额外限制 |
|------|------|---------|---------|
| GET | `/auth/github` | GitHubLogin | 配置了 `GITHUB_CLIENT_ID` |
| GET | `/auth/github/callback` | GitHubCallback | 配置了 `GITHUB_CLIENT_ID` + rate limit |
| POST | `/auth/register` | Register（邮箱注册） | rate limit |
| POST | `/auth/password/register` | UsernameRegister | 配置了 `PasswordAuthEnabled` + rate limit |
| POST | `/auth/password/login` | UsernameLogin | 配置了 `PasswordAuthEnabled` + rate limit |
| POST | `/auth/oauth/exchange` | OAuthExchange | rate limit |
| POST | `/auth/guest` | GuestLogin（临时访客） | rate limit |
| POST | `/auth/refresh` | Refresh（刷新 JWT） | rate limit |
| POST | `/auth/logout` | Logout | — |
| GET | `/auth/me` | Me（当前用户信息） | — |
| PATCH | `/auth/me` | UpdateProfile | — |
| POST | `/auth/me/avatar` | UploadAvatar | — |
| POST | `/auth/switch-tenant` | SwitchTenant（切换租户，重新签发 JWT） | — |
| POST | `/auth/create-tenant` | CreateUserTenant（用户创建自己的租户） | — |

### Admin（JWT + global_admin 角色）

| 方法 | 路径 | Handler |
|------|------|---------|
| GET | `/admin/tenants` | ListTenants |
| POST | `/admin/tenants` | CreateTenant |
| GET | `/admin/tenants/:id` | GetTenant |
| PATCH | `/admin/tenants/:id` | UpdateTenant |
| DELETE | `/admin/tenants/:id` | DeleteTenant |
| GET | `/admin/parameters/schema` | Schema（统一参数注册表 schema） |
| GET | `/admin/parameters` | List |
| PUT | `/admin/parameters` | Update |
| GET | `/admin/parameters/versions/:groupKey` | Versions（版本历史） |
| POST | `/admin/parameters/versions/:groupKey` | CreateDraft |
| POST | `/admin/parameters/versions/:groupKey/:versionID/publish` | Publish |
| POST | `/admin/parameters/versions/:groupKey/:versionID/rollback` | Rollback |
| POST | `/admin/memory/dlq/replay` | Replay（memory 失败队列重放，装配了 pipeline 时注册） |
| GET | `/admin/audit/platform/events` | List（平台级审计日志，装配了 PlatformQueryService 时注册） |
| GET | `/admin/audit/platform/events/:id` | Get |

### Tenant（JWT + member 角色）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/tenant/members` | ListMembers | — |
| POST | `/tenant/members/invite` | InviteMember | requireActive |
| POST | `/tenant/join` | JoinTenant | — |
| PATCH | `/tenant/members/:user_id/role` | UpdateMemberRole | — |
| DELETE | `/tenant/members/:user_id` | RemoveMember | — |
| GET | `/tenant/settings` | GetSettings | — |
| PATCH | `/tenant/settings` | UpdateSettings | requireActive |
| DELETE | `/tenant` | DeleteSelf | owner 角色 |
| GET | `/tenant/list` | ListUserTenants | — (仅 JWT，无 member 断言) |

### Skill（JWT + member 角色）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/skills` | GetAllSkills | — |
| POST | `/skills` | CreateSkill | admin + requireActive |
| GET | `/skills/:id` | GetSkill | — |
| GET | `/skills/:id/workspace` | GetSkillWorkspace（版本草稿工作区） | — |
| PATCH | `/skills/:id` | UpdateSkill（编辑，能力/激活/指令合并为单端点） | requireActive（service 层白名单校验） |
| POST | `/skills/:id/rollback` | RollbackSkill（回滚到历史版本） | requireActive（service 层白名单校验） |
| GET | `/skills/:id/revisions` | ListSkillRevisions（版本历史） | — |
| DELETE | `/skills/:id` | DeleteSkill | admin + requireActive |
| PUT | `/skills/:id/editors` | SetSkillEditors | admin + requireActive |

### Agent（JWT + member 角色）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/agents` | GetAllAgents | — |
| POST | `/agents` | CreateAgent | admin + requireActive |
| GET | `/agents/executions` | ListExecutions | — |
| GET | `/agents/executions/:traceID/tool-traces` | ListExecutionToolTraces | — |
| GET | `/agents/executions/:traceID/trace-events` | ListExecutionTraceEvents | — |
| GET | `/agents/tool-approvals` | ListToolApprovals | — |
| POST | `/agents/tool-approvals/:approvalID/decision` | DecideToolApproval | admin + requireActive |
| POST | `/agents/tool-approvals/:approvalID/resume` | ResumeToolApproval | admin + requireActive |
| GET | `/agents/tool-approvals/history` | ListApprovalHistory | requireActive（member 可见，service 层归属校验） |
| GET | `/agents/tool-approvals/:approvalID` | GetApprovalDetail | requireActive（member 可见，service 层归属校验） |
| POST | `/agents/tool-approvals/:approvalID/execute` | ExecuteApproval | admin + requireActive |
| POST | `/agents/tool-approvals/:approvalID/cancel` | CancelToolApproval | requireActive（发起人可取消自己的审批） |
| PUT | `/agents/tool-approvals/:approvalID/assignee` | SetApprovalAssignee | admin + requireActive |
| GET | `/agents/:id` | GetAgent | — |
| POST | `/agents/:id/execute` | ExecuteAgent | requireActive + rate limit |
| POST | `/agents/:id/execute/stream` | ExecuteAgentStream（SSE） | requireActive + rate limit |
| POST | `/agents/:id/executions/:executionID/pause` | PauseExecution | requireActive |
| POST | `/agents/:id/executions/:executionID/resume` | ResumeExecution | requireActive |
| PUT | `/agents/:id` | UpdateAgent | requireActive（service 层 ownership 矩阵校验） |
| PUT | `/agents/:id/editors` | SetAgentEditors | requireActive（SetEditors 内部限 creator/owner） |
| DELETE | `/agents/:id` | DeleteAgent | admin + requireActive |
| POST | `/agents/:id/conversations` | CreateConversation | — |
| GET | `/agents/:id/conversations` | ListConversations | — |

### Conversations（JWT + tenant context，无 member 断言，service 层归属校验）

| 方法 | 路径 | Handler |
|------|------|---------|
| PATCH | `/conversations/:convID` | RenameConversation |
| DELETE | `/conversations/:convID` | DeleteConversation |
| GET | `/conversations/:convID/messages` | ListMessages |
| POST | `/conversations/:convID/messages` | AddMessage |
| GET | `/conversations/:convID/active-execution` | GetActiveExecution（会话刷新恢复） |

### Evaluation（JWT + member 角色；读端点 admin 收口，写端点 requireActive + handler 内角色分流）

`/evaluations` 组下写端点（baseline/suites/runs/optimizations/experiments/candidates/feedback 等）统一为 requireActive，handler 内按角色分流：member 创建 `evaluation_action` 审批返回 202，admin/owner 直接执行。

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/evaluations/overview` | Overview | admin |
| GET | `/evaluations/resources` | ListResources | — |
| GET | `/evaluations/suites` | ListSuites | — |
| GET | `/evaluations/runs` | ListRuns | admin |
| GET | `/evaluations/candidates` | ListCandidates | admin |
| GET | `/evaluations/experiments` | ListExperiments | — |
| GET | `/evaluations/resources/:kind/:id/timeline` | Timeline | — |
| POST | `/evaluations/resources/:kind/:id/baseline` | CreateBaseline | requireActive |
| POST | `/evaluations/suites` | CreateSuite | requireActive |
| POST | `/evaluations/suites/:id/publish` | PublishSuite | requireActive |
| POST | `/evaluations/suites/:id/generate` | GenerateSuiteCases | requireActive |
| GET | `/evaluations/suites/:id/draft` | GetSuiteDraft | admin + requireActive |
| PUT | `/evaluations/suites/:id/draft/cases/:caseId` | UpdateDraftCase | admin + requireActive |
| POST | `/evaluations/runs` | EnqueueRun | requireActive |
| GET | `/evaluations/runs/:id` | GetRun | admin |
| GET | `/evaluations/jobs/:id` | GetJob | admin |
| POST | `/evaluations/optimizations` | GenerateOptimization | requireActive |
| POST | `/evaluations/experiments` | CreateExperiment | requireActive |
| POST | `/evaluations/candidates/:id/reject` | RejectCandidate | requireActive |
| POST | `/evaluations/experiments/:id/pause` | PauseExperiment | requireActive |
| POST | `/evaluations/experiments/:id/promote` | PromoteExperiment | requireActive |
| POST | `/evaluations/experiments/:id/rollback` | RollbackExperiment | requireActive |
| POST | `/evaluations/feedback` | RecordFeedback | requireActive |

评测中心列表端点共用查询参数：`resource_kind`（空=全部；单值=skill/agent/mcp/knowledge；逗号分隔多值=双轨聚合，如默认 `agent,knowledge`）、`resource_id`、`status`、`cursor`/`limit`（keyset 分页；limit 默认 20、上限 100，非法值 400）。`GET /evaluations/runs` 额外支持 `revision_id`：按 run 锚定版本精确过滤（`WHERE ... AND ($7='' OR revision_id=$7)`），资源详情「运行与回归」的版本筛选即走该服务端过滤，Batch 6 (b)。

### Workflow（JWT + member 角色；定义写入与运行控制部分 admin）

| Method | Path | Handler | Extra role/state |
|---|---|---|---|
| GET | `/workflows` | ListDefinitions | member |
| GET | `/workflows/:id` | GetDefinition | member |
| GET | `/workflows/:id/versions` | ListVersions | member |
| GET | `/workflows/:id/versions/:versionID` | GetVersion | member |
| POST | `/workflows` | CreateDefinition | admin + requireActive |
| PUT | `/workflows/:id/draft` | UpdateDefinition | admin + requireActive |
| DELETE | `/workflows/:id` | DeleteDefinition | admin + requireActive |
| POST | `/workflows/:id/validate` | ValidateDefinition | admin + requireActive |
| POST | `/workflows/:id/publish` | PublishDefinition | admin + requireActive |
| POST | `/workflow-runs` | StartRun | member + requireActive |
| GET | `/workflow-runs` | ListRuns | member |
| GET | `/workflow-runs/:id` | GetRun | member |
| GET | `/workflow-runs/:id/events` | GetEvents | member |
| GET | `/workflow-runs/:id/events/stream` | StreamEvents | member |
| POST | `/workflow-runs/:id/cancel` | CancelRun | member + requireActive |
| POST | `/workflow-runs/:id/pause` | PauseRun | admin + requireActive |
| POST | `/workflow-runs/:id/resume` | ResumeRun | admin + requireActive |
| POST | `/workflow-runs/:id/manual-interventions/:effectID/resolve` | ResolveManual | admin + requireActive |
| GET | `/workflow-approvals` | ListApprovals | admin |
| POST | `/workflow-approvals/:id/decision` | DecideApproval | admin + requireActive |

### Resource Change Proposals（JWT + admin 角色）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/resource-change-proposals/:id` | Get | admin + requireActive |
| PATCH | `/resource-change-proposals/:id` | Update | admin + requireActive |
| POST | `/resource-change-proposals/:id/cancel` | Cancel | admin + requireActive |
| POST | `/resource-change-proposals/:id/confirm` | Confirm | admin + requireActive |

### Operation Proposals（JWT + member 角色；管理操作 admin）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/operation-proposals` | List | admin + requireActive |
| GET | `/operation-proposals/:id` | Get | admin + requireActive |
| GET | `/operation-proposals/mine` | ListMine（「我的申请」） | member + requireActive |
| POST | `/operation-proposals/:id/review` | Review | admin + requireActive |
| POST | `/operation-proposals/:id/approve` | Approve | admin + requireActive |
| POST | `/operation-proposals/:id/reject` | Reject | admin + requireActive |
| POST | `/agents/:id/self-modify` | SelfModify | member + requireActive |
| POST | `/agents/:id/request-editor` | RequestEditorAccess（申请 agent 编辑权） | member + requireActive |
| POST | `/skills/:id/request-editor` | RequestEditorAccess（申请 skill 编辑权） | member + requireActive |
| POST | `/knowledge/workspaces/:name/documents/:documentID/request-access` | RequestEditorAccess（申请知识文档查看权） | member + requireActive |

### Collaboration（JWT + member 角色）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/collaborations` | List | member + requireActive |
| POST | `/collaborations` | Create | member + requireActive |
| GET | `/collaborations/:id` | Get | member + requireActive |
| POST | `/collaborations/:id/start` | Start | member + requireActive |
| POST | `/collaborations/:id/cancel` | Cancel | member + requireActive |

### Scheduled Tasks（JWT + member 角色；写入 admin）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/scheduled-tasks` | List | — |
| GET | `/scheduled-tasks/:id` | Get | — |
| POST | `/scheduled-tasks` | Create | admin + requireActive |
| PUT | `/scheduled-tasks/:id` | Update | admin + requireActive |
| DELETE | `/scheduled-tasks/:id` | Delete | admin + requireActive |
| PATCH | `/scheduled-tasks/:id/enabled` | SetEnabled | admin + requireActive |

### Knowledge / RAG（JWT + member 角色）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/knowledge/workspaces` | ListWorkspaces | — |
| GET | `/knowledge/workspaces/:name/stats` | GetWorkspaceStats | — |
| GET | `/knowledge/workspaces/:name/documents` | ListDocuments | — |
| GET | `/knowledge/workspaces/:name/documents/:documentID/preview` | PreviewDocument | requireActive |
| POST | `/knowledge/query` | Query | requireActive |
| POST | `/knowledge/workspaces` | CreateWorkspace | admin + requireActive |
| PATCH | `/knowledge/workspaces/:name` | UpdateWorkspace | admin + requireActive |
| DELETE | `/knowledge/workspaces/:name` | DeleteWorkspace | admin + requireActive |
| PUT | `/knowledge/workspaces/:name/editors` | SetWorkspaceEditors | admin + requireActive |
| DELETE | `/knowledge/workspaces/:name/documents/:documentID` | DeleteDocument | admin + requireActive |
| PUT | `/knowledge/workspaces/:name/documents/:documentID/access` | SetDocumentAccess | admin + requireActive |
| POST | `/knowledge/ingest` | UploadDocument | admin + requireActive（+ MaxUploadBytes BodyLimit） |

### Memory（JWT + member 角色 + requireActive）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| DELETE | `/memory/clear` | ClearMemories | — |
| GET | `/memory` | ListMemories | — |
| POST | `/memory/sessions` | ListSessions | — |
| GET | `/memory/stats` | GetStats | — |
| GET | `/memory/entities` | GetEntities | — |
| GET | `/memory/summary/:session_id` | GetSummary | — |
| DELETE | `/memory/session/:session_id` | ClearSession | — |

记忆管理页为当前用户级视角：`/memory/stats` 返回 `{memory_count, entity_count}`（facts 与 entities 均按 `scope='user' AND status='active'` 统计），`/memory/entities` 返回该用户的实体话题标签分页列表。单条记忆的创建/读取/删除已移除（facts 用户侧只读），清空与会话清理保留。

### MCP（JWT + member 角色，由 `MCPHandler.RegisterRoutes` 动态注册）

MCP 路由在 `api/http/handler/mcp_handler.go` 的 `RegisterRoutes` 中定义，base 中间件为 JWT + tenant context + member 底线。配置写操作（连接/更新/删除配置/设置编辑者/设置工具策略）为 member + requireActive，handler 内角色分流（admin/owner 直接执行；member 创建 `mcp_policy`/`mcp_server` 审批返回 202 pending）；运行时管理操作（读取完整配置/断开/重连）为 admin + requireActive。当前没有通用 HTTP 工具执行路由，Agent 通过内部 `MCPToolExecutor` 调用工具。

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/mcp/servers` | ListServers | — |
| GET | `/mcp/servers/:id` | GetServer | — |
| GET | `/mcp/servers/:id/tools` | ListTools | — |
| GET | `/mcp/tool-policies` | ListToolPolicies | — |
| GET | `/mcp/servers/:id/resources` | ListResources | — |
| GET | `/mcp/status` | GetServerStatus | — |
| GET | `/mcp/quota` | GetQuota | — |
| PUT | `/mcp/tool-policies/:serverId/:toolName` | SetToolPolicy | requireActive（handler 内角色分流） |
| POST | `/mcp/servers` | ConnectServer | requireActive（handler 内角色分流） |
| PUT | `/mcp/servers/:id` | UpdateServer | requireActive（handler 内角色分流） |
| PUT | `/mcp/servers/:id/editors` | SetMCPServerEditors | requireActive（handler 内角色分流） |
| DELETE | `/mcp/servers/:id/config` | DeleteServerConfig | requireActive（handler 内角色分流） |
| GET | `/mcp/servers/:id/config` | GetServerConfig | admin + requireActive |
| DELETE | `/mcp/servers/:id` | DisconnectServer | admin + requireActive |
| POST | `/mcp/servers/:id/reconnect` | ReconnectServer | admin + requireActive |

### Audit（JWT + admin 角色）

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/audit/events` | ListEvents | admin + requireActive |
| GET | `/audit/events/:id` | GetEvent | admin + requireActive |

### LLM Admin（JWT + member 角色；写操作 global admin）

Provider/Model 管理端点挂在 `/admin/providers`、`/admin/models` 下：读取对任意 member 开放，写操作要求 global admin claim（`/admin/providers` 写端点与 `/admin/models/:id/policy` 额外 requireActive）。

| 方法 | 路径 | Handler | 额外权限 |
|------|------|---------|---------|
| GET | `/admin/providers` | ProviderHandler.List | — |
| POST | `/admin/providers` | Create | global admin + requireActive |
| PUT | `/admin/providers/:id` | Update | global admin + requireActive |
| DELETE | `/admin/providers/:id` | Delete | global admin + requireActive |
| POST | `/admin/providers/:id/discover` | Discover | global admin + requireActive |
| POST | `/admin/providers/:id/health` | HealthCheck | global admin + requireActive |
| GET | `/admin/models` | ModelMgmtHandler.List | — |
| GET | `/admin/models/:id` | Get | — |
| PUT | `/admin/models/:id` | Update | global admin |
| PATCH | `/admin/models/:id/policy` | UpdatePolicy | global admin + requireActive |
| PATCH | `/admin/models/:id/toggle` | Toggle | global admin |
| DELETE | `/admin/models/:id` | Delete | global admin |

## Handler Writing Standards

### File Locations

```
api/http/handler/   ← handler 实现（每域一个文件）
api/http/dto/       ← 跨 handler 复用的 Request/Response 结构体（无业务逻辑）
```

部分域仍在 `api/http/handler/*_dto.go` 放置 handler 私有 DTO；修改时遵循所在域的现有位置，不要重复定义同一 wire contract。

### Struct Pattern

```go
type AgentHandler struct {
    svc    *application.AgentService
    logger *zap.Logger
}

func NewAgentHandler(svc *application.AgentService, logger *zap.Logger) *AgentHandler {
    return &AgentHandler{svc: svc, logger: logger}
}
```

### Request/Response

- 共享 DTO 定义于 `api/http/dto/`；handler 私有 DTO 可沿用对应 `*_dto.go`
- 绑定：`c.ShouldBindJSON(&req)`，失败 `c.Error(err)` → ErrorHandler 返回 400
- 错误必须通过 `c.Error(err)` 传给 ErrorHandler，**不要** 在 handler 内直接 `c.JSON` 错误
- 成功：`c.JSON(http.StatusOK, resp)` 或 `c.JSON(http.StatusCreated, resp)`

### HTTP 状态码约定

| HTTP 状态 | 场景 |
|-----------|------|
| 200 | 查询/更新/执行成功 |
| 201 | 创建成功 |
| 202 | member 发起审批动作被接受（D4/D5：评测/MCP 写操作） |
| 400 | 请求参数非法 |
| 401 | 未认证 |
| 403 | 无权限（RequireGlobalAdmin / RequireTenantRole 拒绝）；租户未激活（RequireActiveTenant 拒绝） |
| 404 | 资源不存在（domain.ErrNotFound） |
| 409 | 资源冲突（domain.ErrNameConflict） |
| 500 | 内部错误 |

## Middleware

注册顺序（`NewRouter` 中）：

```
gin.Recovery → BodyLimit → otelgin.Middleware → TraceMiddleware → ErrorHandler → SecurityHeaders → CORSMiddleware → MetricsMiddleware → Routes
```

ErrorHandler 挂在 TraceMiddleware 之后，使 trace 的访问日志能观察到最终响应状态。

| 文件 | 功能 |
|------|------|
| `middleware.go` | `ErrorHandler`（domain error → HTTP）、`CORSMiddleware` |
| `trace.go` | `TraceMiddleware`：OTEL Span 注入，输出结构化访问日志 |
| `metrics.go` | `MetricsMiddleware`：Prometheus HTTP 指标收集 |
| `jwt.go` | `JWTMiddleware`：RS256 验证，Claims 注入 context |
| `inject_tenant.go` | `InjectTenantContext`：从 Claims 提取 tenant_id，切换 pg schema |
| `require_role.go` | `RequireGlobalAdmin()` / `RequireTenantRole(role)` |
| `require_active_tenant.go` | `RequireActiveTenant`：租户状态激活检查（未激活返回 403） |
| `error_mapping.go` | domain sentinel → HTTP status code 映射表 |

## New Endpoint Checklist

1. 在 `api/http/dto/` 或所在域现有 `api/http/handler/*_dto.go` 中定义 Request/Response 结构体（加 binding tag）
2. 在 `api/http/handler/` 对应文件中实现 handler 方法，业务编排交给 application service
3. 在 `api/http/router.go` 对应 `registerXxx` 函数中注册路由（指定正确 middleware 链）
4. domain sentinel 错误在 `api/middleware/error_mapping.go` 中添加映射规则
5. 运行 `go build ./...` 验证编译
6. 按 `api/http/handler/tenant_handler_test.go` 模式编写 handler 测试
7. 若 API 对外，更新 `api/http/testdata/contracts/*.golden.json`
