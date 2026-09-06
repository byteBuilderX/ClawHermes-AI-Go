import { z } from 'zod';

import { resourceChangeProposalArtifactSchema } from './proposal';

import type { ExecuteAgentRequest as GenExecuteAgentRequest } from '@/services/gen/agent';

export const agentSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    description: z.string().optional().default(''),
    type: z.string().optional().default('react'),
    systemPrompt: z.string().optional().default(''),
    llmModel: z.string().optional().default(''),
    maxIterations: z.number().optional(),
    maxContextTokens: z.number().optional(),
    temperature: z.number().optional(),
    max_tokens: z.number().optional(),
    reasoning_effort: z.string().optional(),
    allowedSkills: z.array(z.string()).nullish().transform((v) => v ?? []),
    mcpToolIds: z.array(z.string()).nullish().transform((v) => v ?? []),
    knowledgeWorkspaceIds: z.array(z.string()).nullish().transform((v) => v ?? []),
    memoryScope: z.string().optional().default('user'),
    // stratum_delegate 子 Agent 派发：delegateEnabled 缺失按 false（存量默认关闭，
    // 委托是显式能力，避免未评估风险的 Agent 静默获得子 Agent 派发能力）；
    // 深度/默认步数 0=unset → 运行时回落全局默认（pkg/constants/agent.go）。
    delegateEnabled: z.boolean().optional().default(false),
    delegateMaxDepth: z.number().optional(),
    delegateDefaultMaxSteps: z.number().optional(),
    created_at: z.string().optional(),
    updated_at: z.string().optional(),
  })
  .passthrough();
export interface Agent {
  id: string;
  name: string;
  description: string;
  type: string;
  systemPrompt: string;
  llmModel: string;
  maxIterations?: number;
  maxContextTokens?: number;
  temperature?: number;
  max_tokens?: number;
  reasoning_effort?: string;
  allowedSkills: string[];
  mcpToolIds: string[];
  knowledgeWorkspaceIds: string[];
  memoryScope: string;
  delegateEnabled?: boolean;
  delegateMaxDepth?: number;
  delegateDefaultMaxSteps?: number;
  created_at?: string;
  updated_at?: string;
  [key: string]: unknown;
}

// agentVersionSchema: 通用产品版本历史（resource_versions）单行。createdByName
// 由后端解析昵称（display_name > github_login > actor_id），前端回退 createdBy。
export const agentVersionSchema = z
  .object({
    id: z.string(),
    versionNo: z.number().optional(),
    status: z.string(),
    source: z.string().optional().default('manual'),
    contentHash: z.string().optional().default(''),
    // parentVersionId：直父版本 ID（创建时自链的前一最高 revision 行）；空串=首版。
    // 「详情」Drawer 以直父整份 payload 为 before 基线现算字段前后值。
    parentVersionId: z.string().optional().default(''),
    createdBy: z.string().optional().default(''),
    createdByName: z.string().optional().default(''),
    createdAt: z.string().optional().default(''),
    publishedAt: z.string().optional().default(''),
    isCurrent: z.boolean().optional().default(false),
    safeSummary: z.record(z.unknown()).optional(),
  })
  .passthrough();
export type AgentVersion = z.infer<typeof agentVersionSchema>;

// agentVersionDetailSchema：单版本内容接口（GET /agents/:id/versions/:versionID）
// 在列表行字段上补充整份编辑面 payload（snake_case 快照键）。extend 后显式再
// passthrough，未知键仍放行。
export const agentVersionDetailSchema = agentVersionSchema
  .extend({ payload: z.record(z.unknown()).optional().default({}) })
  .passthrough();
export type AgentVersionDetail = z.infer<typeof agentVersionDetailSchema>;

export interface AgentFormValues {
  name: string;
  description?: string;
  systemPrompt?: string;
  llmModel: string;
  maxIterations: number;
  maxContextTokens: number;
  // 采样参数(agents.parameters JSONB,merge 语义:0=unset 不落库)
  temperature?: number;
  max_tokens?: number;
  reasoning_effort?: string;
  allowedSkills?: string[];
  mcpToolIds?: string[];
  knowledgeWorkspaceIds?: string[];
  memoryScope?: string;
  // stratum_delegate 子 Agent 派发配置；数值 0=unset → 运行时回落全局默认。
  delegateEnabled?: boolean;
  delegateMaxDepth?: number;
  delegateDefaultMaxSteps?: number;
  editors?: string[];
}

export const conversationSchema = z
  .object({
    id: z.string(),
    name: z.string().optional().default(''),
    agent_id: z.string().optional(),
    created_at: z.string().optional(),
    updated_at: z.string().optional(),
  })
  .passthrough();
export type Conversation = z.infer<typeof conversationSchema>;

export const chatStepSchema = z
  .object({
    type: z.string().optional(),
    tool: z.string().optional(),
    input: z.unknown().optional(),
    output: z.unknown().optional(),
    thought: z.string().optional(),
    duration_ms: z.number().optional(),
  })
  .passthrough();
export type ChatStep = z.infer<typeof chatStepSchema>;

export const citationSchema = z.object({
  documentId: z.string(),
  title: z.string(),
  productVersion: z.string(),
  section: z.string(),
  url: z.string(),
  excerpt: z.string(),
});
export type Citation = z.infer<typeof citationSchema>;

export const diagnosticFactSchema = z.object({
  area: z.string(),
  objectId: z.string().optional(),
  statement: z.string(),
  source: z.string(),
  observedAt: z.string(),
});

export const evidenceGapSchema = z.object({
  area: z.string().optional(),
  source: z.string().optional(),
  code: z.string(),
});

export const diagnosticStepSchema = z.object({
  tool: z.string(),
  outcome: z.string(),
  errorCode: z.string().optional(),
  latencyMs: z.number(),
});

export const diagnosticReportSchema = z.object({
  facts: z.array(diagnosticFactSchema).nullish().transform((v) => v ?? []),
  inferences: z.array(z.string()).nullish().transform((v) => v ?? []),
  evidenceGaps: z.array(evidenceGapSchema).nullish().transform((v) => v ?? []),
  recommendedActions: z.array(z.string()).nullish().transform((v) => v ?? []),
  citations: z.array(citationSchema).nullish().transform((v) => v ?? []),
  steps: z.array(diagnosticStepSchema).nullish().transform((v) => v ?? []),
});
export type DiagnosticReport = z.infer<typeof diagnosticReportSchema>;

export const executionArtifactSchema = z.object({
  type: z.string(),
  profileVersion: z.string().optional(),
  citations: z.array(citationSchema).nullish().transform((v) => v ?? []),
  diagnosticReport: diagnosticReportSchema.nullish().transform((v) => v ?? undefined),
  resourceChangeProposal: resourceChangeProposalArtifactSchema.nullish().transform((v) => v ?? undefined),
});
export type ExecutionArtifact = z.infer<typeof executionArtifactSchema>;

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

// NoAnswerReason 与后端 pkg/constants 的 NoAnswerReason 枚举逐值对齐
// （跨 context 单一事实源，值进入响应契约与指标 label）。
export const noAnswerReasons = [
  'no_sources',
  'threshold_filtered',
  'access_restricted',
  'insufficient_evidence',
  'unsupported_mode',
] as const;
export type NoAnswerReason = (typeof noAnswerReasons)[number];

// NoAnswerInfo 是 SSE done payload 的 noAnswer 信号（JSON 字段名 PascalCase：
// 后端 domain.NoAnswerInfo 无 tag）。注意与 sources 的 camelCase 规则不同：
// sources 子字段带 json tag，noAnswer 子字段无 tag，二者并存于同一 done 帧。
// nil=有答案（omitempty 不输出键）；非 nil=无答案且 reason 说明原因。
export interface NoAnswerInfo {
  Reason: NoAnswerReason;
  RetrievedCount?: number;
  FilteredCount?: number;
  BestScore?: number;
  Retried?: boolean;
  RewrittenQuery?: string;
  Detail?: string;
}

// ToolReferenceClassification 是工具引用声称的五态对账枚举（与后端
// factcheck/reconcile.go 分类常量逐值对齐）。verified=核验通过（折叠不展示）；
// verification_failed=引用真实但工具确凿失败/从未发出；outcome_unknown=结果
// 不确定（advisory）；invalid_reference=引用无法对应任何工具调用；
// unverified=含操作声称但未带引用的软标记（单独走 unverifiedClaims）。
export type ToolReferenceClassification =
  | 'verified'
  | 'verification_failed'
  | 'outcome_unknown'
  | 'invalid_reference'
  | 'unverified';

// ToolReferenceVerdict 是单个 <tool_ref:ID> 声称的对账判定（SSE done 帧
// factCheck.toolReferences 项，camelCase 与 Go json tag 对齐）。
export interface ToolReferenceVerdict {
  claimText?: string;
  toolName?: string;
  toolCallId?: string;
  reference?: string;
  status?: string;
  outcome?: string;
  classification: ToolReferenceClassification;
  risk: number;
}

// ClaimVerdict 是单个 claim 的 LLM-as-Judge 判定（factCheck.claims 项）。
export interface ClaimVerdict {
  text: string;
  verdict: string;
  risk: number;
}

// FactCheckReport 是幻觉防护的展示型报告（advisory，只展示）。checked 标记本次
// 确实校验；isValid=false 表示存在核验失败/无效引用；toolReferences 是对账条目
// （verified 折叠）；unverifiedClaims 含操作声称但未带引用的句子。SSE done 帧
// factCheck 键透出，校验关/旧后端时缺省。
export interface FactCheckReport {
  checked: boolean;
  claims: ClaimVerdict[];
  isValid: boolean;
  riskPoints: number;
  toolReferences?: ToolReferenceVerdict[];
  unverifiedCount?: number;
  unverifiedClaims?: string[];
}

export const chatMessageSchema = z
  .object({
    id: z.string().optional(),
    role: z.string(),
    content: z.string().optional().default(''),
    created_at: z.string().optional(),
    steps: z.array(chatStepSchema).optional(),
    artifacts: z.array(executionArtifactSchema).nullish().transform((v) => v ?? []),
    interrupted: z.boolean().optional(),
  })
  .passthrough();
/** 后端 TaskSnapshot 的 JSON 形态（camelCase 与 Go 对齐） */
export interface TaskSnapshot {
  goal: string;
  currentPhase: string;
  completedSteps: string[];
  nextAction: string;
  status: 'active' | 'completed' | 'abandoned';
  failures?: number;
}
export interface ChatMessage {
  id?: string;
  role: string;
  content: string;
  created_at?: string;
  steps?: ChatStep[];
  artifacts?: ExecutionArtifact[];
  interrupted?: boolean;
  /** 审批终态续跑(取消/被拒)成功收尾的持久痕迹：该工具未执行 */
  approvalRejected?: boolean;
  sources?: ChatCitationSource[];
  /** 无答案结构化信号（nil/缺失=有答案或旧后端）；用于渲染拒答提示 */
  noAnswer?: NoAnswerInfo;
  /** 跨会话目标进度摘要（stratum_task_snapshot 透出）；无则 undefined */
  taskSnapshot?: TaskSnapshot;
  /** 幻觉防护对账报告（advisory，只展示；校验关/旧后端缺省） */
  factCheck?: FactCheckReport;
  [key: string]: unknown;
}

// query/context/variables 来自 proto 契约(gen);conversation_id 是 wire-only 字段
// (后端 handler.ExecuteAgentRequest 绑定并用于会话连续性,dto 契约无此字段,parity 冻结)。
// execution_id 同为 wire-only:断线续发时 SSE 首帧下发恢复键,前端保存并在断线
// 重发请求中原样带回,供后端 resumeFromCheckpoint 定位检查点续跑。
export interface ExecuteAgentPayload extends GenExecuteAgentRequest {
  conversation_id?: string;
  execution_id?: string;
}

export interface AgentExecutionResult {
  output?: string;
  steps?: ChatStep[];
  artifacts?: ExecutionArtifact[];
  sources?: ChatCitationSource[];
  /** 无答案结构化信号：nil/缺失=有答案（omitempty），渲染拒答提示用 */
  noAnswer?: NoAnswerInfo;
  /** 幻觉防护对账报告（advisory，校验关/旧后端缺省） */
  factCheck?: FactCheckReport;
  error?: string;
  metadata?: Record<string, unknown>;  // SSE done 白名单透出（thoughtsJSON/toolCallsJSON/stratum_task_snapshot）
  [key: string]: unknown;
}

export interface AgentExecutionFailure {
  message: string;
  code?: string;
  status?: number;
}


export interface StreamCallbacks {
  onToken: (token: string) => void;
  onDone: (data: AgentExecutionResult) => void;
	onError: (err: Error) => void;
	// 同一轮 LLM 消息可能含多个需审批工具：SSE approval_required 帧一帧携带
	// approvals 数组，批量渲染审批卡并等待全部终态统一续跑。
	onApprovalsRequired: (approvals: ToolApproval[]) => void;
	// 首帧恢复键(断线续接协议):SSE 首帧下发 execution_id,捕获后断线重发时
	// 原样带回;仅存内存供消费方读取,不持久化。
	onExecutionId?: (executionId: string) => void;
	// 委托进度帧(SSE delegate_status):running 时子 agent 正在执行、finished 时
	// 已结束;非终止帧,不影响主流(断线重发仍由 execution_id 恢复)。
	onDelegateEvent?: (evt: DelegateEventPayload) => void;
}

// 委托子 agent 进度事件(SSE delegate_status 帧 payload,后端白名单直通)。
export interface DelegateEventPayload {
	delegate_status: 'running' | 'finished';
	result_status?: 'success' | 'partial' | 'failed';
	delegate_id?: string;
	goal?: string;
	summary?: string;
	tokens_used?: number;
}

// 委托子 agent 进度渲染态(由 DelegateEventPayload 经 ChatStreamContext 收敛)。
// conversationId 用于跨会话隔离:渲染端仅当与当前会话一致时才展示,避免 A 会话的
// running/failed banner 泄漏到 B 会话。finished 且非 failed 时上游直接清空(最终
// 回答自然呈现),仅 failed 保留失败 Tag 直至终态清理。
export type DelegateStatusView =
  | { status: 'running'; goal?: string; delegateId?: string; conversationId?: string | null }
  | {
      status: 'finished';
      resultStatus: 'failed';
      goal?: string;
      delegateId?: string;
      summary?: string;
      conversationId?: string | null;
    }
  | null;

export interface ToolApproval {
	approvalId: string;
	agentId?: string;
	toolName: string;
	serverId: string;
	riskLevel: string;
	status: 'pending' | 'approved' | 'rejected' | 'expired' | 'unknown_outcome' | 'authorization_denied' | 'cancelled' | 'voided' | 'invalidated' | string;
	expiresAt?: string;
	invalidationReason?: string;
	// 审批归属会话：ListPending 透出（member 已按自己过滤）、SSE 等待审批帧由发起
	// 会话页补附；对话页卡片按此过滤当前会话，审批工作台复用。
	conversationId?: string;
	// 审批发起人：ListPending 透出（user_id），SSE 等待审批帧由发起会话页补附
	// user.sub；对话页"取消"按钮据此仅对发起人本人展示。
	userId?: string;
}

// 会话"进行中执行"视图（后端 GET /conversations/:convID/active-execution）。
// status: running | paused | waiting_approval；waiting_approval 时附审批数组
// approvals（每项 approval_status 区分"已批准待续跑"与"仍待审批"）；顶层
// approval_id/approval_status 镜像首条，兼容旧前端单审批读取。无活跃执行时后端
// 返回 404，API 层统一折叠为 null（非 404 错误向上抛，禁止当作无执行）。
export interface ActiveExecution {
	executionId: string;
	agentId: string;
	status: string;
	approvalId?: string;
	approvalStatus?: string;
	// 多审批：整轮全部审批的状态快照（approval_id + approval_status）。
	approvals?: { approvalId: string; approvalStatus?: string }[];
	userQuery?: string;
	updatedAt?: string;
}

export interface ToolApprovalResumeResult {
  status: 'completed';
  output: string;
  steps: number;
  tokensUsed: number;
}
