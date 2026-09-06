import { z } from 'zod';

const goSlice = <T extends z.ZodTypeAny>(item: T) => z.preprocess(
  (value) => value ?? [],
  z.array(item),
);

export const workflowNodeTypeSchema = z.enum(['agent', 'skill', 'mcp_tool', 'condition', 'approval']);
export type WorkflowNodeType = z.infer<typeof workflowNodeTypeSchema>;

export const workflowEffectClassSchema = z.enum(['pure', 'idempotent', 'non_idempotent']);

export const workflowRetrySchema = z.object({
  max_attempts: z.number().int().nonnegative().optional().default(0),
  backoff_ms: z.number().int().nonnegative().optional().default(0),
});

// 必须 optional、无 default：历史数据无 position 时保持缺失，
// 由自动布局兜底填充，避免被 (0,0) 污染导致节点堆叠在原点。
export const workflowNodePositionSchema = z.object({ x: z.number(), y: z.number() });

const workflowNodeBase = z.object({
  id: z.string().min(1),
  name: z.string().optional().default(''),
  input_mapping: z.record(z.string()).optional().default({}),
  output_mapping: z.record(z.string()).optional().default({}),
  retry: workflowRetrySchema.optional().default({ max_attempts: 0, backoff_ms: 0 }),
  timeout_ms: z.number().int().nonnegative().optional().default(0),
  position: workflowNodePositionSchema.optional(),
});

export const workflowNodeSchema = z.discriminatedUnion('type', [
  workflowNodeBase.extend({ type: z.literal('agent'), agent_id: z.string().min(1) }),
  workflowNodeBase.extend({
    type: z.literal('skill'),
    agent_id: z.string().min(1),
    skill_id: z.string().min(1),
    skill_revision_id: z.string().min(1),
  }),
  workflowNodeBase.extend({
    type: z.literal('mcp_tool'),
    agent_id: z.string().optional().default(''),
    mcp_server_id: z.string().min(1),
    mcp_tool_name: z.string().min(1),
    effect_class: workflowEffectClassSchema,
  }),
  workflowNodeBase.extend({
    type: z.literal('condition'),
    agent_id: z.string().optional().default(''),
    condition: z.string().min(1),
  }),
  workflowNodeBase.extend({
    type: z.literal('approval'),
    agent_id: z.string().optional().default(''),
  }),
]);
export type WorkflowNode = z.infer<typeof workflowNodeSchema>;

export const workflowEdgeSchema = z.object({
  id: z.string().optional().default(''),
  from: z.string().min(1),
  to: z.string().min(1),
  condition_value: z.boolean().optional(),
  default: z.boolean().optional().default(false),
});
export type WorkflowEdge = z.infer<typeof workflowEdgeSchema>;

export const workflowSpecSchema = z.object({
  nodes: goSlice(workflowNodeSchema),
  edges: goSlice(workflowEdgeSchema),
  max_concurrency: z.number().int().nonnegative().optional().default(0),
});
export type WorkflowSpec = z.infer<typeof workflowSpecSchema>;

export const workflowInputFieldTypeSchema = z.enum([
  'short_text',
  'long_text',
  'number',
  'single_select',
  'multi_select',
  'boolean',
  'date',
]);
export type WorkflowInputFieldType = z.infer<typeof workflowInputFieldTypeSchema>;

export const workflowInputOptionSchema = z.object({ label: z.string().min(1), value: z.string().min(1) });

export const workflowInputFieldSchema = z.object({
  key: z.string().min(1),
  label: z.string().min(1),
  type: workflowInputFieldTypeSchema,
  required: z.boolean().optional().default(false),
  description: z.string().optional().default(''),
  default: z.unknown().optional(),
  options: goSlice(workflowInputOptionSchema).optional().default([]),
});
export type WorkflowInputField = z.infer<typeof workflowInputFieldSchema>;

export const workflowInputSchemaSchema = z.object({
  task_label: z.string().min(1),
  task_description: z.string().optional().default(''),
  fields: goSlice(workflowInputFieldSchema).optional().default([]),
});
export type WorkflowInputSchema = z.infer<typeof workflowInputSchemaSchema>;

export const workflowDefinitionSchema = z.object({
  id: z.string().min(1),
  name: z.string().min(1),
  description: z.string(),
  revision: z.number().int().positive(),
  active_version_id: z.string().optional(),
  spec: workflowSpecSchema,
  input_schema: workflowInputSchemaSchema,
  // P2 白名单：创建人（grant 提案通过后由服务层回填）与可编辑人列表。
  // optional+default 兼容历史数据与旧版本后端（缺字段时按空白名单解析）。
  created_by: z.string().optional().default(''),
  editors: z.array(z.string()).optional().default([]),
  created_at: z.string(),
  updated_at: z.string(),
});
export type WorkflowDefinition = z.infer<typeof workflowDefinitionSchema>;

export const workflowVersionSchema = z.object({
  id: z.string().min(1),
  definition_id: z.string().min(1),
  version: z.number().int().positive(),
  name: z.string().min(1),
  description: z.string(),
  spec: workflowSpecSchema,
  input_schema: workflowInputSchemaSchema,
  created_at: z.string(),
});
export type WorkflowVersion = z.infer<typeof workflowVersionSchema>;

export const workflowSummarySchema = z.object({
  id: z.string().min(1),
  name: z.string().min(1),
  description: z.string(),
  revision: z.number().int().positive(),
  updated_at: z.string(),
});
export type WorkflowSummary = z.infer<typeof workflowSummarySchema>;

export const workflowVersionSummarySchema = z.object({
  id: z.string().min(1),
  definition_id: z.string().min(1),
  version: z.number().int().positive(),
  name: z.string().min(1),
  description: z.string(),
  // 发布者（版本历史「操作者」）：created_by 为原始 actor id；created_by_name 是服务端
  // join public.users 现算的可读名（display_name>github_login>无命中留空），前端展示
  // 优先 name、缺失回退 raw id。optional+default 兼容历史后端（缺字段时按空展示）。
  created_by: z.string().optional().default(''),
  created_by_name: z.string().optional().default(''),
  created_at: z.string(),
});
export type WorkflowVersionSummary = z.infer<typeof workflowVersionSummarySchema>;

export const workflowPageSchema = z.object({
  workflows: goSlice(workflowSummarySchema),
  total: z.number().int().nonnegative(),
  page: z.number().int().positive(),
  page_size: z.number().int().positive(),
});
export type WorkflowPage = z.infer<typeof workflowPageSchema>;

export const workflowVersionPageSchema = z.object({
  versions: goSlice(workflowVersionSummarySchema),
  total: z.number().int().nonnegative(),
  page: z.number().int().positive(),
  page_size: z.number().int().positive(),
});
export type WorkflowVersionPage = z.infer<typeof workflowVersionPageSchema>;

export const validationIssueSchema = z.object({
  field: z.string().optional(),
  path: z.string().optional(),
  node_id: z.string().optional(),
  edge_id: z.string().optional(),
  code: z.string(),
  message: z.string(),
});
export type ValidationIssue = z.infer<typeof validationIssueSchema>;

export type WorkflowDraftPayload = Pick<WorkflowDefinition, 'name' | 'description' | 'spec' | 'input_schema'>;

export const workflowRunStatusSchema = z.enum([
  'queued', 'running', 'completed', 'failed', 'paused', 'pause_requested', 'cancel_requested', 'canceled', 'manual_intervention',
]);
export type WorkflowRunStatus = z.infer<typeof workflowRunStatusSchema>;

export const workflowRunSummarySchema = z.object({
  id: z.string(), definition_id: z.string(), name: z.string(), version_id: z.string(), version: z.number().int(),
  status: workflowRunStatusSchema, created_by: z.string(), created_at: z.string(), updated_at: z.string(),
  started_at: z.string().nullish(), finished_at: z.string().nullish(),
});
export type WorkflowRunSummary = z.infer<typeof workflowRunSummarySchema>;

export const workflowRunSchema = workflowRunSummarySchema.extend({
  snapshot: workflowSpecSchema,
  input: z.record(z.unknown()),
  output: z.string(),
  error_message: z.string().optional(),
  generation: z.number().int().positive(),
  pause_reason: z.string().optional(),
  cancel_reason: z.string().optional(),
  manual_reason: z.string().optional(),
});
export type WorkflowRun = z.infer<typeof workflowRunSchema>;

export const workflowNodeAttemptSchema = z.object({
  id: z.string(), run_id: z.string(), node_id: z.string(), attempt_no: z.number().int(),
  status: z.enum(['pending', 'running', 'ready', 'claimed', 'succeeded', 'failed', 'retry_wait', 'skipped', 'paused', 'canceled', 'manual_intervention']),
  input: z.string(), output_summary: z.string(), error_message: z.string().optional(), trace_id: z.string().optional(),
  fence_token: z.number().int(), run_generation: z.number().int(), error_code: z.string().optional(),
  retry_at: z.string().nullish(), effect_class: workflowEffectClassSchema.optional(), selected_edges: goSlice(z.string()).optional().default([]),
});
export type WorkflowNodeAttempt = z.infer<typeof workflowNodeAttemptSchema>;

const approvalWireSchema = z.object({
  ID: z.string(), RunID: z.string(), NodeID: z.string(), AttemptID: z.string(), RunGeneration: z.number().int(),
  Reason: z.string(), Risk: z.string(), RequestSummary: z.string(), Status: z.enum(['pending', 'approved', 'rejected']),
  DecisionActor: z.string(), DecisionComment: z.string(), DecidedAt: z.string().nullish(),
}).transform((row) => ({
  id: row.ID, run_id: row.RunID, node_id: row.NodeID, attempt_id: row.AttemptID, run_generation: row.RunGeneration,
  reason: row.Reason, risk: row.Risk, request_summary: row.RequestSummary, status: row.Status,
  decision_actor: row.DecisionActor, decision_comment: row.DecisionComment, decided_at: row.DecidedAt,
}));
export const workflowApprovalSchema = approvalWireSchema;
export type WorkflowApproval = z.infer<typeof workflowApprovalSchema>;

const effectIntentWireSchema = z.object({
  ID: z.string(), RunID: z.string(), NodeID: z.string(), AttemptID: z.string(), RunGeneration: z.number().int(),
  EffectClass: workflowEffectClassSchema, IdempotencyKey: z.string(), Status: z.enum(['prepared', 'started', 'succeeded', 'failed', 'unknown']),
  Reason: z.string(), OutputSummary: z.string(),
}).transform((row) => ({
  id: row.ID, run_id: row.RunID, node_id: row.NodeID, attempt_id: row.AttemptID, run_generation: row.RunGeneration,
  effect_class: row.EffectClass, status: row.Status, reason: row.Reason, output_summary: row.OutputSummary,
}));
export const workflowEffectIntentSchema = effectIntentWireSchema;
export type WorkflowEffectIntent = z.infer<typeof workflowEffectIntentSchema>;

export const workflowRunEventSchema = z.object({
  id: z.string(), run_id: z.string(), sequence_no: z.number().int().nonnegative(), event_type: z.string().min(1),
  status: z.string().optional(), node_id: z.string().optional(), attempt_no: z.number().int().optional(),
  summary: z.string().optional(), actor_type: z.string().optional(), actor_id: z.string().optional(),
  data: z.record(z.unknown()).optional().default({}), occurred_at: z.string(),
});
export type WorkflowRunEvent = z.infer<typeof workflowRunEventSchema>;

export const workflowRunPageSchema = z.object({
  runs: goSlice(workflowRunSummarySchema), total: z.number().int().nonnegative(), page: z.number().int().positive(), page_size: z.number().int().positive(),
});
export type WorkflowRunPage = z.infer<typeof workflowRunPageSchema>;

export const workflowRunDetailSchema = z.object({
  run: workflowRunSchema,
  node_attempts: goSlice(workflowNodeAttemptSchema),
  approvals: goSlice(workflowApprovalSchema),
  effect_intents: goSlice(workflowEffectIntentSchema),
  progress: z.object({ completed: z.number().int().nonnegative(), total: z.number().int().nonnegative() }),
  available_actions: goSlice(z.enum(['pause', 'cancel', 'resume', 'mark_succeeded', 'retry', 'terminate'])),
});
export type WorkflowRunDetail = z.infer<typeof workflowRunDetailSchema>;
