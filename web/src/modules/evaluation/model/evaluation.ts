import { z } from 'zod';

export const resourceKindSchema = z.enum(['skill', 'agent', 'mcp', 'knowledge']);
export type ResourceKind = z.infer<typeof resourceKindSchema>;

// registrableResourceKinds 是「可登记建档/发起评测」的被测类型：被测对象收敛后仅
// agent 与 knowledge 两轨（skill 的评测本质是 agent 的评测，mcp 不再评测）。只用于
// 新建/登记/默认过滤；历史响应中的 skill/mcp 仍经 resourceKindSchema 四值读回。
export const registrableResourceKinds = ['agent', 'knowledge'] as const;
export type RegistrableResourceKind = (typeof registrableResourceKinds)[number];

// CenterKindFilter 是中心列表 resource_kind 过滤串：单值=历史语义（含 skill/mcp
// 只读回溯），'agent,knowledge'=默认两轨聚合；空/undefined=全部。
export type CenterKindFilter = ResourceKind | 'agent,knowledge';

export const resourceRefSchema = z.object({
  kind: resourceKindSchema,
  resource_id: z.string(),
  revision_id: z.string(),
});
export type ResourceRef = z.infer<typeof resourceRefSchema>;

export const judgeSpecSchema = z.object({
  model: z.string().optional(),
  rubric: z.string().optional(),
}).optional();
export type JudgeSpec = z.infer<typeof judgeSpecSchema>;

// toolObservationSchema parses one tool invocation from the run tool sequence
// (§6.5). The backend projects a strict subset of the agent ToolObservation,
// so zod strips any extra fields the wire might carry.
export const toolObservationSchema = z.object({
  tool_name: z.string(),
  tool_type: z.string().optional(),
  step_index: z.number().optional(),
  provider_type: z.string().optional(),
  capability_id: z.string().optional(),
  arguments: z.record(z.string(), z.unknown()).optional(),
  raw_text: z.string().optional(),
});
export type ToolObservation = z.infer<typeof toolObservationSchema>;

// toolSpecSchema is the deterministic tool-call rule (§6.5): must_call /
// must_not_call / order constrain the execution tool sequence, max_calls caps
// total invocations. Empty fields do not participate in validation.
export const toolSpecObjectSchema = z.object({
  must_call: z.array(z.string()).optional(),
  must_not_call: z.array(z.string()).optional(),
  order: z.array(z.string()).optional(),
  max_calls: z.number().optional(),
});
export type ToolSpecObject = z.infer<typeof toolSpecObjectSchema>;

// toolSpecSchema 是 case 级可选工具断言；轮次级 tool_spec 复用 toolSpecObjectSchema
// 以允许 optional 语义落在各自层级（会话 case 的某轮可单独携带过程断言）。
export const toolSpecSchema = toolSpecObjectSchema.optional();
export type ToolSpec = z.infer<typeof toolSpecSchema>;

// sessionTurnSchema 是会话剧本的一轮（阶段 B §5.4）：user 必填、probe 探针可选、
// tool_spec 为该轮工具序列过程断言（可缺省）。
export const sessionTurnSchema = z.object({
  user: z.string(),
  probe: z.string().optional(),
  tool_spec: toolSpecObjectSchema.optional(),
});
export type SessionTurn = z.infer<typeof sessionTurnSchema>;

// sessionScriptSchema 是 EvalSessionScript（阶段 B §5.4）：goal 描述被测任务终点，
// turns 至少一轮、每轮 user 非空（后端 Validate 兜底）。缺省 session 即旧单轮 case。
export const sessionScriptSchema = z.object({
  goal: z.string(),
  turns: z.array(sessionTurnSchema).min(1),
});
export type SessionScript = z.infer<typeof sessionScriptSchema>;

// stepJudgeSchema is the step-level LLM rubric (§6.5): criteria scores the
// tool sequence step by step. Empty criteria falls back to the platform
// default rubric at runtime.
export const stepJudgeSchema = z.object({
  criteria: z.string().optional(),
}).optional();
export type StepJudge = z.infer<typeof stepJudgeSchema>;

export const evaluationCaseSchema = z.object({
  id: z.string().optional(),
  name: z.string().optional().default(''),
  input: z.unknown(),
  expected_output: z.unknown(),
  assertion_mode: z.enum(['exact', 'contains', 'regex', 'judge']),
  judge_spec: judgeSpecSchema,
  tool_spec: toolSpecSchema,
  step_judge: stepJudgeSchema,
  // session 是会话剧本（阶段 B §5.4）；nil/缺省 = 旧单轮 case。字段必须显式声明，
  // 否则 zod 默认 strip 响应里 draft case 的 session，导致会话剧本读回即丢。
  session: sessionScriptSchema.optional(),
  enabled: z.boolean().optional().default(true),
  // Provenance from auto-generation (Phase 3c): which production trace and
  // feedback signal the case was generated from, and why.
  source_trace_id: z.string().optional(),
  feedback_ref: z.string().optional(),
  generate_reason: z.string().optional(),
});
export type EvaluationCase = z.infer<typeof evaluationCaseSchema>;

// generateResultSchema is the outcome of one POST /suites/:id/generate
// pass: how many samples were found, how many became draft cases, and
// which samples were rejected and why.
export const generateResultSchema = z.object({
  samples_found: z.number(),
  generated: z.number(),
  rejected: z.array(z.object({ trace_id: z.string(), reason: z.string() }).strict()),
}).strict();
export type GenerateResult = z.infer<typeof generateResultSchema>;

export const suiteRevisionSchema = z.object({
  id: z.string(),
  suite_id: z.string(),
  version_no: z.number().optional(),
  status: z.string(),
  resource_kind: resourceKindSchema,
  cases: z.array(evaluationCaseSchema),
});
export type SuiteRevision = z.infer<typeof suiteRevisionSchema>;

export const createSuiteResponseSchema = z.object({
  suite: z.object({ id: z.string(), name: z.string(), draft_revision_id: z.string().optional() }),
  revision: suiteRevisionSchema,
});

export const evaluationJobSchema = z.object({
  job_id: z.string(),
  status: z.enum(['queued', 'running', 'succeeded', 'failed', 'cancelled']),
  error_message: z.string().optional(),
  result_id: z.string().optional(),
});
export type EvaluationJob = z.infer<typeof evaluationJobSchema>;

export const ragEvidenceSchema = z.object({
  retrieved_document_ids: z.array(z.string()).optional(),
  relevant_document_ids: z.array(z.string()).optional(),
  recall_at_k: z.number().optional(),
  precision_at_k: z.number().optional(),
  mrr: z.number().optional(),
  ndcg_at_k: z.number().optional(),
});
export type RAGEvidence = z.infer<typeof ragEvidenceSchema>;

export const dimensionScoreSchema = z.object({
  name: z.string(),
  score: z.number(),
  passed: z.boolean(),
  reason: z.string().optional(),
  confidence: z.number().optional(),
});
export type DimensionScore = z.infer<typeof dimensionScoreSchema>;

// observedTraceEvidenceSchema parses the trace-level evidence (spec §6.3
// component drill-down) the backend attaches to a failed case. All fields are
// best-effort and optional: a nil backend TraceEvidence omits the whole key.
export const observedTraceEvidenceSchema = z.object({
  cost_usd: z.number().optional(),
  latency_ms: z.number().optional(),
  success: z.boolean().optional(),
  security_violation: z.boolean().optional(),
  tool_call_count: z.number().optional(),
  tool_error_count: z.number().optional(),
});
export type ObservedTraceEvidence = z.infer<typeof observedTraceEvidenceSchema>;

// runResourceAnchorSchema 解析 run 详情里的「评测资源版本锚定」项（后端从
// context_snapshot.pinned_assignments 平铺展开）：被测主体恒在首位，其后是
// 绑定的 skill/mcp/knowledge 资源及各自锁定的 revision。
export const runResourceAnchorSchema = z.object({
  kind: resourceKindSchema,
  resource_id: z.string(),
  revision_id: z.string(),
}).strict();
export type RunResourceAnchor = z.infer<typeof runResourceAnchorSchema>;

export const evaluationRunSchema = z.object({
  id: z.string(),
  resource: resourceRefSchema,
  suite_revision_id: z.string(),
  anchors: z.array(runResourceAnchorSchema).optional(),
  passed: z.boolean(),
  total_cases: z.number(),
  passed_cases: z.number(),
  metrics: z.record(z.string(), z.unknown()).optional(),
  results: z.array(
    z.object({
      case_id: z.string(),
      passed: z.boolean(),
      message: z.string().optional(),
      error: z.string().optional(),
      actual: z.unknown().optional(),
      trace_id: z.string().optional(),
      tokens: z.number().optional().default(0),
      cost_usd: z.number().optional().default(0),
      duration_ms: z.number().optional().default(0),
      rag_evidence: ragEvidenceSchema.optional(),
      dimensions: z.array(dimensionScoreSchema).optional(),
      failure_reason: z.string().optional(),
      trace_evidence: observedTraceEvidenceSchema.optional(),
      // process_pass/process_failure/tools are the §6.5 process assertions:
      // the backend always emits process_pass (DB NOT NULL DEFAULT true),
      // process_failure only on process failure, tools only when collected.
      process_pass: z.boolean(),
      process_failure: z.string().optional(),
      tools: z.array(toolObservationSchema).optional(),
    }),
  ),
});
export type EvaluationRun = z.infer<typeof evaluationRunSchema>;

export const optimizationCandidateSchema = z.object({
  id: z.string(),
  optimization_job_id: z.string(),
  revision: resourceRefSchema,
  parent_revision_id: z.string(),
  source: z.string(),
  rationale: z.string().optional(),
});
export type OptimizationCandidate = z.infer<typeof optimizationCandidateSchema>;

export const optimizationResponseSchema = z.object({
  job: z.object({ id: z.string(), status: z.string() }).passthrough(),
  candidates: z.array(optimizationCandidateSchema),
});

export const experimentResponseSchema = z.object({
  experiment: z.object({ id: z.string(), status: z.string(), stage: z.number() }).passthrough(),
  deployment: z
    .object({ stable_revision_id: z.string(), canary_revision_id: z.string().optional(), canary_percent: z.number() })
    .passthrough(),
});
export type ExperimentResponse = z.infer<typeof experimentResponseSchema>;

export const errorResponseSchema = z.object({ error: z.string() }).strict();

type JSONValue = string | number | boolean | null | JSONValue[] | { [key: string]: JSONValue };

const SENSITIVE_SUMMARY_KEYS = new Set([
  'payload', 'raw_payload', 'prompt', 'raw_prompt', 'credentials', 'credential', 'api_key', 'apikey', 'token',
  'access_token', 'refresh_token', 'retrieved_content', 'document_content', 'arguments', 'tool_arguments',
  'raw_response', 'tool_raw_response', 'encrypted_payload_ref', 'payload_ref', 'payload_hash', 'content_hash',
  'authorization', 'password', 'secret', 'private_key', 'client_secret', 'cookie', 'session', 'key', 'cert',
  'connection_string',
  'system_prompt', 'developer_prompt', 'api_token', 'bearer_token', 'retrieved_chunks',
]);

const normalizedKey = (key: string) => key
  .replace(/-/g, '_')
  .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
  .replace(/([A-Z]+)([A-Z][a-z])/g, '$1_$2')
  .toLowerCase();
const sensitiveSummaryAssignment = /(^|[^A-Za-z0-9_-])["']?(?:api[_-]?key|access[_-]?token|client[_-]?secret)["']?\s*[:=]\s*["']?\S/i;
const sensitiveSummaryAuthorization = /(^|[^A-Za-z0-9_-])["']?authorization["']?\s*[:=]\s*["']?(?:bearer|basic)\b/i;
const isSensitiveSummaryValue = (value: string) => sensitiveSummaryAssignment.test(value)
  || sensitiveSummaryAuthorization.test(value);
const validateSafeJSON = (value: unknown, path: string[], depth = 0): string | null => {
  if (depth > 6) return `${path.join('.')} exceeds safe summary depth`;
  if (value === null || typeof value === 'boolean') return null;
  if (typeof value === 'number') return Number.isFinite(value) ? null : `${path.join('.')} is not finite`;
  if (typeof value === 'string') {
    if (value.length > 2048) return `${path.join('.')} is too long`;
    return isSensitiveSummaryValue(value) ? `${path.join('.')} contains a sensitive value` : null;
  }
  if (Array.isArray(value)) {
    if (value.length > 64) return `${path.join('.')} has too many items`;
    for (let index = 0; index < value.length; index += 1) {
      const error = validateSafeJSON(value[index], [...path, String(index)], depth + 1);
      if (error) return error;
    }
    return null;
  }
  if (!value || typeof value !== 'object' || Object.getPrototypeOf(value) !== Object.prototype) {
    return `${path.join('.')} is not JSON-safe`;
  }
  const entries = Object.entries(value as Record<string, unknown>);
  if (entries.length > 64) return `${path.join('.')} has too many fields`;
  for (const [key, nested] of entries) {
    if (SENSITIVE_SUMMARY_KEYS.has(normalizedKey(key))) return `${[...path, key].join('.')} is sensitive`;
    const error = validateSafeJSON(nested, [...path, key], depth + 1);
    if (error) return error;
  }
  return null;
};

export const safeSummarySchema = z.unknown().superRefine((value, ctx) => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'safe summary must be an object' });
    return;
  }
  const error = validateSafeJSON(value, ['safe_summary']);
  if (error) ctx.addIssue({ code: z.ZodIssueCode.custom, message: error });
}).transform((value) => value as Record<string, JSONValue>);

const safeDiffValueSchema = z.unknown().superRefine((value, ctx) => {
  const error = validateSafeJSON(value, ['safe_diff']);
  if (error) ctx.addIssue({ code: z.ZodIssueCode.custom, message: error });
}).transform((value) => value as JSONValue | undefined);

export const candidateSafeDiffSchema = z.object({
  changed_fields: z.array(z.string().min(1).max(64)).max(32),
  changes: z.record(z.string().min(1).max(64),
    z.object({ before: safeDiffValueSchema.optional(), after: safeDiffValueSchema.optional() }).strict()),
  parent_missing: z.boolean(),
}).strict().superRefine((diff, ctx) => {
  const fields = diff.changed_fields;
  const keys = Object.keys(diff.changes);
  if (new Set(fields).size !== fields.length) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'changed_fields must be unique' });
  }
  if (keys.length > 32 || [...fields].sort().join('\0') !== [...keys].sort().join('\0')) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'changed_fields must match changes' });
  }
  for (const key of keys) {
    if (SENSITIVE_SUMMARY_KEYS.has(normalizedKey(key))) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, message: `unsafe diff field: ${key}` });
    }
  }
});

const page = <T extends z.ZodTypeAny>(item: T) => z.object({
  items: z.array(item),
  next_cursor: z.string().optional(),
}).strict();

export const centerOverviewSchema = z.object({
  resources: z.number(), suites: z.number(), runs: z.number(), candidates: z.number(), experiments: z.number(),
}).strict();
export type CenterOverview = z.infer<typeof centerOverviewSchema>;

export const resourceSummarySchema = z.object({
  id: z.string(), resource_id: z.string(), status: z.string(), stable_revision_id: z.string().optional(),
  latest_run_status: z.string().optional(), resource_kind: resourceKindSchema,
  safe_summary: safeSummarySchema.default({}), created_at: z.string(),
}).strict();
export type ResourceSummary = z.infer<typeof resourceSummarySchema>;
export const resourcePageSchema = page(resourceSummarySchema);
export type ResourcePage = z.infer<typeof resourcePageSchema>;

// suiteSummarySchema 是评测集列表行（GET /evaluations/suites）。S1-2 起后端在
// 原 suite 字段上叠加当前 active/draft revision 的 kind/版本号/启用 case 数；
// omitempty 使 legacy 套件可缺省这些键，故全部 optional。active_case_count /
// draft_case_count 与 SuiteRevisionMeta.enabled_case_count 同口径（启用 case 数）。
export const suiteSummarySchema = z.object({
  id: z.string(), name: z.string(), description: z.string(), status: z.string(),
  resource_kind: resourceKindSchema.optional(),
  active_revision_id: z.string().optional(),
  draft_revision_id: z.string().optional(),
  active_version_no: z.number().optional(),
  draft_version_no: z.number().optional(),
  active_case_count: z.number().optional(),
  draft_case_count: z.number().optional(),
  created_by: z.string().optional(), created_at: z.string(),
}).strict();
export const suitePageSchema = page(suiteSummarySchema);
export type SuiteSummary = z.infer<typeof suiteSummarySchema>;
export type SuitePage = z.infer<typeof suitePageSchema>;

// suiteDetailSchema 是 GET /suites/:id 顶部元信息，字段集与增强后的列表行一致
// （kind/status 由当前 active/draft revision 聚合而来，无 revision 的 legacy 空套件
// 可能缺省 kind）。
export const suiteDetailSchema = suiteSummarySchema;
export type SuiteDetail = SuiteSummary;

// suiteRevisionMetaSchema 是 GET /suites/:id/versions 的轻量版本行：不含 case 正文。
// version_no 草稿为 0（未分配版本号）；published_at 已发布才有值。
export const suiteRevisionMetaSchema = z.object({
  id: z.string(),
  version_no: z.number().optional(),
  status: z.string(),
  resource_kind: resourceKindSchema.optional(),
  created_by: z.string().optional(),
  published_at: z.string().nullable().optional(),
  enabled_case_count: z.number().optional(),
}).strict();
export type SuiteRevisionMeta = z.infer<typeof suiteRevisionMetaSchema>;

export const runSummarySchema = z.object({
  id: z.string(), resource_id: z.string(), revision_id: z.string(), status: z.string(),
  resource_kind: resourceKindSchema, passed: z.boolean(), total_cases: z.number(), passed_cases: z.number(),
  created_by: z.string().optional(), created_at: z.string(),
}).strict();
export type RunSummary = z.infer<typeof runSummarySchema>;
export const runPageSchema = page(runSummarySchema);
export type RunPage = z.infer<typeof runPageSchema>;

export const candidateSummarySchema = z.object({
  id: z.string(), resource_id: z.string(), revision_id: z.string(), parent_revision_id: z.string(),
  source: z.string(), status: z.string(), resource_kind: resourceKindSchema, rank: z.number().optional(),
  state_version: z.number().int().positive(), safe_diff: candidateSafeDiffSchema,
  created_by: z.string().optional(), created_at: z.string(),
}).strict();
export type CandidateSummary = z.infer<typeof candidateSummarySchema>;
export const candidatePageSchema = page(candidateSummarySchema);
export type CandidatePage = z.infer<typeof candidatePageSchema>;

export const experimentGateSchema = z.enum(['passed', 'failed', 'pending', 'not_applicable']);
const promotionEvidenceSchema = z.object({
  eligible: z.boolean(),
  gates: z.object({ quality: experimentGateSchema, cost: experimentGateSchema, latency: experimentGateSchema,
    error_rate: experimentGateSchema, security: experimentGateSchema }).strict(),
  blockers: z.array(z.object({ code: z.enum(['insufficient_samples', 'insufficient_duration', 'evidence_unavailable',
    'guardrail_violation', 'safety_stop', 'recommendation_hold']), category: z.string(), message: z.string() }).strict()),
}).strict();
export const experimentSummarySchema = z.object({
  id: z.string(), resource_id: z.string(), stable_revision_id: z.string(), canary_revision_id: z.string(),
  status: z.string(), recommendation: z.string(), resource_kind: resourceKindSchema, stage_percent: z.number(),
  safety_stopped: z.boolean(), state_version: z.number().int().positive(), gates: z.object({
    quality: experimentGateSchema, cost: experimentGateSchema, latency: experimentGateSchema,
    error_rate: experimentGateSchema, security: experimentGateSchema,
  }).strict().optional(), promotion_evidence: promotionEvidenceSchema,
  created_by: z.string().optional(), created_at: z.string(),
}).strict();
export type ExperimentSummary = z.infer<typeof experimentSummarySchema>;
export const experimentPageSchema = page(experimentSummarySchema);
export type ExperimentPage = z.infer<typeof experimentPageSchema>;

export const timelineEventSchema = z.object({
  id: z.string(), kind: z.string(), status: z.string(), summary: z.string(), resource_id: z.string(),
  resource_kind: resourceKindSchema, created_at: z.string(),
}).strict();
export type TimelineEvent = z.infer<typeof timelineEventSchema>;
export const timelinePageSchema = page(timelineEventSchema);
export type TimelinePage = z.infer<typeof timelinePageSchema>;

export const evaluationCommandSchema = z.object({
  reason: z.string(), idempotency_key: z.string(), expected_state_version: z.number().int().positive(),
}).strict();
export type EvaluationCommand = z.infer<typeof evaluationCommandSchema>;

export const candidateCommandResponseSchema = candidateSummarySchema;

const promotionPolicySchema = z.object({
  stages: z.array(z.number()), min_samples: z.number(), min_observation_minutes: z.number(),
  max_cost_regression: z.number(), max_latency_regression: z.number(), max_error_rate_increase: z.number(),
}).strict();
export const experimentCommandResponseSchema = z.object({
  id: z.string(), resource_kind: resourceKindSchema, resource_id: z.string(), stable_revision_id: z.string(),
  canary_revision_id: z.string(), suite_revision_id: z.string(), status: z.string(), stage: z.number(),
  policy: promotionPolicySchema, state_version: z.number().int().positive(), recommendation: z.string(),
  safety_stopped: z.boolean(),
}).strict();

export interface EvaluationCenterFilters {
  resource_kind?: CenterKindFilter;
  resource_id?: string;
  revision_id?: string;
  status?: string;
  cursor?: string;
  limit?: number;
}

// CreateEvaluationPlan 是「新建一次评测 run」的三态执行计划（S3 产品决策）：
// resource 恒为带稳定基线 revision 的目标资源行——离线评测跑在已发布稳定版本上，
// 由页面在下发命令前保证 revision 有效。
//  - published：目标评测集已有已发布版本，直接用该 revision 排队 run（纯读，不产生写）。
//  - unpublished：目标评测集只有未发布草稿（从未 publish），先把 draft publish 成 v1 再跑。
//  - create：内联新建含起始 case 的评测集 → publish v1 → 跑（旧「内联 1 case 即跑」升级版，
//     suite 成为详情页可继续补 case/再发布的复用对象）。
// 幂等 / in-flight 去重由 useCreateEvaluation 按计划指纹 + idempotency_key 处理。
export type CreateEvaluationPlan =
  | { mode: 'published'; resource: ResourceRef; suiteId: string; revisionId: string }
  | { mode: 'unpublished'; resource: ResourceRef; suiteId: string }
  | { mode: 'create'; resource: ResourceRef; name: string; description?: string; cases: EvaluationCase[] };

// —— 评测指标监控面板（spec 2026-09-03 §4.2）——

// qualityDimSchema 单 judge 语义维度的窗口聚合；仅在实际观测到样本时出现。
export const qualityDimSchema = z.object({
  dimension: z.string(),
  pass_rate: z.number(),
  avg_score: z.number(),
  avg_confidence: z.number(),
  samples: z.number(),
}).strict();
export type QualityDim = z.infer<typeof qualityDimSchema>;

export const verdictDistributionSchema = z.object({
  pass: z.number(),
  flag: z.number(),
  block: z.number(),
}).strict();
export type VerdictDistribution = z.infer<typeof verdictDistributionSchema>;

export const behaviorStatsSchema = z.object({
  rule_hits: z.number(),
  retry_count: z.number(),
  escalation_count: z.number(),
  abandonment_count: z.number(),
  verdict: verdictDistributionSchema,
}).strict();
export type BehaviorStats = z.infer<typeof behaviorStatsSchema>;

// costStatsSchema 观测线成本；avg/p95 延迟无样本时为 null（不做假装为零）。
export const costStatsSchema = z.object({
  total_tokens: z.number(),
  total_cost_usd: z.number(),
  avg_latency_ms: z.number().nullable(),
  p95_latency_ms: z.number().nullable(),
}).strict();
export type CostStats = z.infer<typeof costStatsSchema>;

// processBaselineSchema 窗口内最近一条 succeeded 评测 run 的过程通过率基线；
// process 为 null 表示该窗口无离线评测（run 未做过程断言时该值恒 1.0，前端须带 run 元信息语境呈现）。
export const processBaselineSchema = z.object({
  process_pass_rate: z.number(),
  run_id: z.string(),
  run_created_at: z.string(),
}).strict();
export type ProcessBaseline = z.infer<typeof processBaselineSchema>;

export const monitorResourceSummarySchema = z.object({
  resource_kind: resourceKindSchema,
  resource_id: z.string(),
  sample_count: z.number(),
  quality: z.array(qualityDimSchema),
  behavior: behaviorStatsSchema,
  cost: costStatsSchema,
  process: processBaselineSchema.nullable(),
}).strict();
export type MonitorResourceSummary = z.infer<typeof monitorResourceSummarySchema>;

export const monitorWindowSchema = z.object({ from: z.string(), to: z.string() }).strict();
export type MonitorWindow = z.infer<typeof monitorWindowSchema>;

// monitorResourcesPageSchema 端点 1；MVP 不分页，故无 next_cursor/truncated 字段，
// 截断由前端以「items.length 达到 limit」推断（Task 7），schema 不为此添加字段。
export const monitorResourcesPageSchema = z.object({
  items: z.array(monitorResourceSummarySchema),
  window: monitorWindowSchema,
}).strict();
export type MonitorResourcesPage = z.infer<typeof monitorResourcesPageSchema>;

export const monitorTrendPointSchema = z.object({
  bucket_at: z.string(),
  sample_count: z.number(),
  quality: z.array(qualityDimSchema),
  behavior: behaviorStatsSchema,
  cost: costStatsSchema,
}).strict();
export type MonitorTrendPoint = z.infer<typeof monitorTrendPointSchema>;

export const runProcessPointSchema = z.object({
  run_id: z.string(),
  process_pass_rate: z.number(),
  run_created_at: z.string(),
}).strict();
export type RunProcessPoint = z.infer<typeof runProcessPointSchema>;

// monitorTrendSchema 端点 2：series 按日桶；runs 为该资源窗口内 succeeded run 过程基线点。
export const monitorTrendSchema = z.object({
  resource_kind: resourceKindSchema,
  resource_id: z.string(),
  series: z.array(monitorTrendPointSchema),
  runs: z.array(runProcessPointSchema),
}).strict();
export type MonitorTrend = z.infer<typeof monitorTrendSchema>;

export interface MonitorFilters {
  resource_kind?: ResourceKind;
  resource_id?: string;
  /** RFC3339；省略由后端兜底近 7 天。 */
  from?: string;
  /** RFC3339。 */
  to?: string;
  limit?: number;
}
