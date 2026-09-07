# 前端规范（web/）

## 技术栈

React 18.3 · Vite 6.4 · Ant Design 5.20 · React Router 7 · Axios 1.18

| 目录 | 职责 |
|------|------|
| `app/` | 路由与应用级 layout |
| `modules/<domain>/` | 按 Agent、Skill、Knowledge、Memory 等业务域组织页面、组件、hook、model 与 API |
| `services/` | 共享 HTTP client 与流式请求工具 |
| `shared/hooks/` | 跨域自定义 Hook（`use*` 命名） |
| `shared/lib/` | 跨域纯函数与基础设施辅助 |
| `shared/ui/` | 跨域 UI 组件 |
| `constants/` | 前端共享行为常量 |

## 编码规则

- 普通 API 调用走 `web/src/services/client.ts` 导出的 axios 实例；SSE 流式调用统一走同文件的 `streamApiEvents`
- 错误统一：`message.error({ content: err.response?.data?.error || '操作失败', duration: 3 })`
- 业务域之间不直接导入对方页面；共享逻辑下沉到 `shared/`，域内页面过大时提取到同域 components/hooks
- `useEffect` 依赖必须完整；异步 effect 需要 `let cancelled = false` 清理
- 用 `message` / `Modal.confirm`，禁止 `alert()` / `confirm()`
- 用户可见字符串全部中文；禁止 `console.log` 提交
- 展示资源/实体时主文案用真实名称（`name`/`label`/`resource_name`），禁止把裸 id（`resource_id`/`revision_id`/`server_id` 等）当名称渲染为可见单元格文本；id 只作 rowKey/路由/aria-label 等非可见身份用途，确需原文时用弱化次要行或 tooltip 并配真实名称作主文案；名称缺失/DTO 未带名称时显式占位 `—`，不得用 id 冒充名称
- Token 禁止存 `localStorage`，用 `httpOnly` cookie 或内存 Context

## 行为常量

见 [constants.md](constants.md) 前端部分——禁止页面内硬编码 timeout / pageSize 等。

## 命名约定

- Modal 开关 `createOpen` / `editOpen`
- loading 状态 `createLoading` / `deleteLoading`
- 服务层函数：动词 + 实体名 `createWorkspace`
- Hook 返参直接解构，不加 `state` 前缀
