import { z } from 'zod';

export const resourceKindSchema = z.enum(['skill', 'agent', 'mcp', 'knowledge']);
export type ResourceKind = z.infer<typeof resourceKindSchema>;

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
export const toolSpecSchema = z.object({
  must_call: z.array(z.string()).optional(),
  must_not_call: z.array(z.string()).optional(),
  order: z.array(z.string()).optional(),
  max_calls: z.number().optional(),
}).optional();
export type ToolSpec = z.infer<typeof toolSpecSchema>;

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

export const suiteSummarySchema = z.object({
  id: z.string(), name: z.string(), description: z.string(), status: z.string(),
  created_by: z.string().optional(), created_at: z.string(),
}).strict();
export const suitePageSchema = page(suiteSummarySchema);
export type SuiteSummary = z.infer<typeof suiteSummarySchema>;
export type SuitePage = z.infer<typeof suitePageSchema>;

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
  resource_kind?: ResourceKind;
  resource_id?: string;
  status?: string;
  cursor?: string;
  limit?: number;
}
