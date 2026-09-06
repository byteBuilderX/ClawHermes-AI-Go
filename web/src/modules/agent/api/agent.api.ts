import { z } from 'zod';

import {
  agentSchema,
  agentVersionDetailSchema,
  agentVersionSchema,
  chatMessageSchema,
  conversationSchema,
  type ActiveExecution,
  type Agent,
  type AgentFormValues,
  type AgentVersion,
  type AgentVersionDetail,
  type ChatMessage,
  type Conversation,
  type ExecuteAgentPayload,
	type StreamCallbacks,
	type ToolApproval,
	type ToolApprovalResumeResult,
} from '../model/agent';

import {
  AGENT_STREAM_RECONNECT_BASE_MS,
  AGENT_STREAM_RECONNECT_MAX_ATTEMPTS,
  AGENT_STREAM_RECONNECT_MAX_MS,
  DEFAULT_PAGE_SIZE,
} from '@/constants';
import api, { StreamRequestError, streamApiEvents } from '@/services/client';

export const agentApi = {
  list: async (): Promise<Agent[]> => {
    const res = await api.get('/agents');
    return z.array(agentSchema).parse(res.data?.agents ?? []);
  },
  get: async (id: string): Promise<Agent> => {
    const res = await api.get(`/agents/${id}`);
    return agentSchema.parse(res.data);
  },
  create: (data: AgentFormValues) => api.post('/agents', data),
  update: (id: string, data: AgentFormValues) => api.put(`/agents/${id}`, data),
  // P2：白名单（可编辑人）独立于普通更新体管理——PUT /agents/:id 不接受 editors，
  // 单独 PUT /agents/:id/editors 持久化（handler SetAgentEditors，owner/creator 可调）。
  setEditors: (id: string, editorIds: string[]) => api.put(`/agents/${id}/editors`, { editorIds }),
  delete: (id: string) => api.delete(`/agents/${id}`),
  // 通用产品版本历史（resource_versions）：当前生效版本标记 isCurrent。
  listVersions: async (id: string): Promise<AgentVersion[]> => {
    const res = await api.get(`/agents/${id}/versions`);
    return z.array(agentVersionSchema).parse(res.data?.versions ?? []);
  },
  // 单版本内容：列表元数据 + 整份 payload（snake_case 快照键）。「详情」Drawer 取
  // 点击版与其直父(parentVersionId)两次内容现算字段前后值；首版父为空。
  getVersion: async (id: string, versionId: string): Promise<AgentVersionDetail> => {
    const res = await api.get(`/agents/${id}/versions/${versionId}`);
    return agentVersionDetailSchema.parse(res.data ?? {});
  },
  // rollback: 将生效指针指回历史已发布版本，立即生效、不产生新版本；返回重建后的 agent。
  rollback: async (id: string, versionId: string): Promise<Agent> => {
    const res = await api.post(`/agents/${id}/rollback`, { versionId });
    return agentSchema.parse(res.data);
  },
  // M6：同步执行（列表页"运行"按钮）后端无全局 deadline（逐步骤超时 + maxSteps
  // 上限兜底），HTTP 层不设超时，避免长执行被误杀；只保留接口级 + 迭代限制。
  execute: (id: string, payload: ExecuteAgentPayload) =>
    api.post(`/agents/${id}/execute`, payload, { timeout: 0 }),
	executions: (page = 1, pageSize = DEFAULT_PAGE_SIZE) =>
    api.get('/agents/executions', { params: { page, page_size: pageSize } }),
	listToolApprovals: async (): Promise<ToolApproval[]> => {
		const res = await api.get('/agents/tool-approvals');
		return (res.data?.approvals ?? []).map((row: Record<string, unknown>) => ({
			approvalId: String(row.id || ''), agentId: String(row.agent_id || ''), toolName: String(row.tool_name || ''),
			serverId: String(row.server_id || ''), riskLevel: String(row.risk_level || ''), status: String(row.status || ''), expiresAt: String(row.expires_at || ''),
			invalidationReason: row.invalidation_reason ? String(row.invalidation_reason) : undefined,
			conversationId: row.conversation_id ? String(row.conversation_id) : undefined,
			userId: row.user_id ? String(row.user_id) : undefined,
		}));
	},
	decideToolApproval: (id: string, decision: 'approved' | 'rejected', reason = '') => api.post(`/agents/tool-approvals/${id}/decision`, { decision, reason }),
	cancelToolApproval: (id: string) => api.post(`/agents/tool-approvals/${id}/cancel`),
	// M6：工作台手动兜底入口；后端逐步骤超时 + maxSteps 上限，HTTP 层不设超时。
	resumeToolApproval: async (id: string): Promise<ToolApprovalResumeResult> => {
		const res = await api.post(`/agents/tool-approvals/${id}/resume`, undefined, { timeout: 0 });
		return res.data as ToolApprovalResumeResult;
	},
	pauseExecution: (agentId: string, executionId: string) =>
		api.post(`/agents/${agentId}/executions/${executionId}/pause`),
	resumeExecution: (agentId: string, executionId: string, payload: ExecuteAgentPayload) =>
		api.post(`/agents/${agentId}/executions/${executionId}/resume`, payload, { timeout: 0 }),
	// 会话"进行中执行"视图：404（含不存在/无活跃/越权 fail-closed 哨兵）→ null；
	// 非 404（DB 抖动等）必须向上抛，禁止当作"无执行"发起全新执行（SECURITY-MEDIUM-1）。
	getActiveExecution: async (convId: string): Promise<ActiveExecution | null> => {
		try {
			const res = await api.get<{
				status?: string; execution_id?: string; agent_id?: string;
				approval_id?: string; approval_status?: string; user_query?: string; updated_at?: string;
				approvals?: { approval_id?: string; approval_status?: string }[];
			}>(`/conversations/${convId}/active-execution`);
			if (!res.data || res.data.status === 'none') return null;
			return {
				executionId: res.data.execution_id || '',
				agentId: res.data.agent_id || '',
				status: res.data.status || '',
				approvalId: res.data.approval_id || undefined,
				approvalStatus: res.data.approval_status || undefined,
				approvals: Array.isArray(res.data.approvals)
					? res.data.approvals.map((a) => ({
							approvalId: String(a.approval_id || ''),
							approvalStatus: a.approval_status ? String(a.approval_status) : undefined,
						}))
					: undefined,
				userQuery: res.data.user_query || undefined,
				updatedAt: res.data.updated_at || undefined,
			};
		} catch (err) {
			if ((err as { response?: { status?: number } }).response?.status === 404) return null;
			throw err;
		}
	},
};

export const conversationApi = {
  list: async (agentId: string): Promise<Conversation[]> => {
    const res = await api.get(`/agents/${agentId}/conversations`);
    return z.array(conversationSchema).parse(res.data?.conversations ?? []);
  },
  create: async (agentId: string, name?: string): Promise<Conversation> => {
    const res = await api.post(`/agents/${agentId}/conversations`, name ? { name } : {});
    return conversationSchema.parse(res.data);
  },
  rename: (convId: string, name: string) => api.patch(`/conversations/${convId}`, { name }),
  delete: (convId: string) => api.delete(`/conversations/${convId}`),
  messages: async (convId: string): Promise<ChatMessage[]> => {
    const res = await api.get(`/conversations/${convId}/messages`);
    return z.array(chatMessageSchema).parse(res.data?.messages ?? []);
  },
  addMessage: (convId: string, role: string, content: string) =>
    api.post(`/conversations/${convId}/messages`, { role, content }),
};

// sessionStorage 刷新快路径(非凭据,不进入 URL):与后端 active-execution(权威)
// 互补,仅覆盖同页刷新/组件重挂载;done/终态错误清除,防同 query 新请求误恢复。
const execIdStorageKey = (conversationId: string) => `chat:execId:${conversationId}`;
const readSessionExec = (conversationId: string, query: string): string | undefined => {
  try {
    const raw = sessionStorage.getItem(execIdStorageKey(conversationId));
    if (!raw) return undefined;
    const entry = JSON.parse(raw) as { query?: string; executionId?: string };
    // query 配对:不同 query 的新请求不沿用旧 execution_id,避免误续跑已完成执行。
    return entry.query === query && entry.executionId ? entry.executionId : undefined;
  } catch {
    return undefined;
  }
};
const writeSessionExec = (conversationId: string, query: string, executionId: string): void => {
  try {
    sessionStorage.setItem(execIdStorageKey(conversationId), JSON.stringify({ query, executionId }));
  } catch {
    // sessionStorage 不可用(隐私模式等)时降级为仅内存恢复,不影响主链路。
  }
};
const clearSessionExec = (conversationId: string): void => {
  try {
    sessionStorage.removeItem(execIdStorageKey(conversationId));
  } catch {
    // 清理失败不影响主链路(done 已到达,不依赖该 key)。
  }
};

export const executeAgentStream = (
  id: string,
  payload: ExecuteAgentPayload,
	{ onToken, onDone, onError, onApprovalsRequired, onExecutionId, onDelegateEvent }: StreamCallbacks,
): AbortController => {
  // 自愈连接器:断点续接协议的服务端协作端。SSE 首帧(无条件、先于任何 token
  // 帧)下发 execution_id 作为恢复键,断线(网络/5xx)以指数退避携带同一
  // execution_id + 原 query/conversation_id 重发,服务端 resumeFromCheckpoint
  // 从上次检查点续跑、只流新增 token,前端累积 = 完整答案(无重复)。
  // done/error 帧/approval/4xx/用户 cancel/超最大重试次数为终止条件。
  // 刷新连续性:execution_id 写 sessionStorage(query 配对,非凭据、不进 URL,
  // 仅同页刷新快路径);权威来源是后端 active-execution,刷新恢复由
  // useChatPage.getActiveExecution 显式携带,此处 query-match 注入只兜底组件
  // 重挂载;流到达 done/终态错误时清除,避免同 query 的新请求误恢复已结束执行。
  const outer = new AbortController();
  let executionId: string | undefined;
  let completed = false;
  let attempt = 0;
  let delay = AGENT_STREAM_RECONNECT_BASE_MS;
  let current: AbortController | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;

  outer.signal.addEventListener('abort', () => {
    current?.abort();
    if (timer) clearTimeout(timer);
  });

  const isDisposed = () => completed || outer.signal.aborted;

  const scheduleReconnect = () => {
    if (isDisposed()) return;
    attempt += 1;
    if (attempt > AGENT_STREAM_RECONNECT_MAX_ATTEMPTS) {
      completed = true;
      if (payload.conversation_id) clearSessionExec(payload.conversation_id);
      onError(new Error(`Stream reconnected ${AGENT_STREAM_RECONNECT_MAX_ATTEMPTS} times without completion`));
      return;
    }
    const wait = delay;
    delay = Math.min(delay * 2, AGENT_STREAM_RECONNECT_MAX_MS);
    timer = setTimeout(connect, wait);
  };

  const connect = () => {
    if (isDisposed()) return;
    // 断线重发必须带已捕获的 execution_id:后端沿用同一执行供 resume 定位
    // checkpoint;首连若调用方未显式带 execution_id(刷新恢复已由 useChatPage
    // 显式携带),则尝试 sessionStorage 中 query 配对的恢复键兜底(query-match)。
    const storedExecId =
      !executionId && !payload.execution_id && payload.conversation_id
        ? readSessionExec(payload.conversation_id, payload.query || '')
        : undefined;
    current = streamApiEvents(
      `/agents/${id}/execute/stream`,
      executionId || storedExecId ? { ...payload, execution_id: executionId || storedExecId } : payload,
      {
        onEvent: (evt) => {
          const event = evt as { execution_id?: string; error?: string; code?: string; done?: boolean; token?: unknown; status?: string; approvalId?: string; toolName?: string; serverId?: string; riskLevel?: string; approvals?: { approvalId?: string; toolName?: string; serverId?: string; riskLevel?: string }[]; delegate_status?: string; result_status?: string; delegate_id?: string; goal?: string; summary?: string; tokens_used?: number };
          if (event.execution_id) {
            executionId = event.execution_id;
            onExecutionId?.(event.execution_id);
            // 刷新连续性快路径:首帧恢复键写入 sessionStorage(非凭据、不进 URL)。
            if (payload.conversation_id) writeSessionExec(payload.conversation_id, payload.query || '', event.execution_id);
            return true; // 恢复键首帧,非终止,继续接收 token
          }
          if (event.delegate_status) {
            onDelegateEvent?.({ // 委托进度帧:非终止,只透出渲染层
              delegate_status: event.delegate_status as 'running' | 'finished',
              result_status: event.result_status as 'success' | 'partial' | 'failed' | undefined,
              delegate_id: event.delegate_id,
              goal: event.goal,
              summary: event.summary,
              tokens_used: event.tokens_used,
            });
            return true;
          }
          if (event.status === 'waiting_approval') {
            // 批量审批统一一帧：approvals 数组含全部待审批工具；旧后端无数组时
            // 回退顶层镜像单条。仍然 completed=true; return false 一帧终止。
            completed = true;
            const raw = Array.isArray(event.approvals) && event.approvals.length > 0
              ? event.approvals
              : event.approvalId
                ? [{ approvalId: event.approvalId, toolName: event.toolName, serverId: event.serverId, riskLevel: event.riskLevel }]
                : [];
            const approvals = raw.map((it) => ({
              approvalId: String(it.approvalId || ''),
              toolName: String(it.toolName || ''),
              serverId: String(it.serverId || ''),
              riskLevel: String(it.riskLevel || 'unclassified'),
              status: 'pending' as const,
            }));
            if (approvals.length > 0) onApprovalsRequired(approvals);
            return false;
          }
          if (event.error) {
            completed = true;
            if (payload.conversation_id) clearSessionExec(payload.conversation_id);
            onError(new StreamRequestError(event.error, undefined, event.code));
            return false;
          }
          if (event.done) {
            completed = true;
            if (payload.conversation_id) clearSessionExec(payload.conversation_id);
            onDone(event);
            return false;
          }
          if (event.token != null) {
            delay = AGENT_STREAM_RECONNECT_BASE_MS; // 收到数据流:退避重置
            onToken(String(event.token));
          }
          return true;
        },
        onClose: () => {
          if (!completed && !isDisposed()) scheduleReconnect();
        },
        onError: (err) => {
          if (isDisposed()) return;
          // 4xx 是客户端/协议错误,重发无意义,直接终止;网络断线与 5xx 退避重发。
          if (err instanceof StreamRequestError && err.status != null && err.status >= 400 && err.status < 500) {
            completed = true;
            if (payload.conversation_id) clearSessionExec(payload.conversation_id);
            onError(err);
            return;
          }
          scheduleReconnect();
        },
      },
    );
  };

  connect();
  return outer;
};
