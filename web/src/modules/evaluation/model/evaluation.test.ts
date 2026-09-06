import { describe, expect, it } from 'vitest';

import {
  candidatePageSchema,
  dimensionScoreSchema,
  errorResponseSchema,
  evaluationCaseSchema,
  evaluationJobSchema,
  evaluationRunSchema,
  experimentPageSchema,
  optimizationResponseSchema,
  observedTraceEvidenceSchema,
  resourcePageSchema,
  resourceRefSchema,
  timelinePageSchema,
  safeSummarySchema,
  candidateCommandResponseSchema,
  experimentCommandResponseSchema,
  behaviorStatsSchema,
  costStatsSchema,
  monitorResourceSummarySchema,
  monitorResourcesPageSchema,
  monitorTrendSchema,
  suiteRevisionMetaSchema,
  suiteSummarySchema,
} from './evaluation';

describe('evaluation model', () => {
  it('parses and preserves a session script case without stripping turns', () => {
    const parsed = evaluationCaseSchema.parse({
      id: 'c-session', name: '退货会话', expected_output: '已受理退款', assertion_mode: 'contains', enabled: true,
      session: {
        goal: '处理退货退款诉求',
        turns: [
          { user: '快递没到', probe: '识别到退货意向' },
          { user: '请退款', tool_spec: { must_call: ['refund'], max_calls: 2 } },
        ],
      },
    });
    expect(parsed.session).toBeDefined();
    expect(parsed.session?.turns).toHaveLength(2);
    expect(parsed.session?.turns[1].tool_spec?.must_call).toEqual(['refund']);
    expect(parsed.session?.turns[1].tool_spec?.max_calls).toBe(2);
  });

  it('leaves session undefined for single-turn cases', () => {
    const parsed = evaluationCaseSchema.parse({
      name: '单轮', input: '你好', expected_output: '您好', assertion_mode: 'contains', enabled: true,
    });
    expect(parsed.session).toBeUndefined();
  });

  it('parses completed job with result id', () => {
    const job = evaluationJobSchema.parse({ job_id: 'job-1', status: 'succeeded', result_id: 'run-1' });
    expect(job.result_id).toBe('run-1');
  });

  it('parses generated candidate revisions', () => {
    const response = optimizationResponseSchema.parse({
      job: { id: 'optimization-1', status: 'succeeded' },
      candidates: [
        {
          id: 'candidate-record-1',
          optimization_job_id: 'optimization-1',
          revision: { kind: 'skill', resource_id: 'skill-1', revision_id: 'candidate-1' },
          parent_revision_id: 'version-1',
          source: 'parameter_search',
        },
      ],
    });
    expect(response.candidates[0].revision.revision_id).toBe('candidate-1');
  });

  it.each(['skill', 'agent', 'mcp', 'knowledge'] as const)('supports %s resources', (kind) => {
    expect(resourceRefSchema.parse({ kind, resource_id: 'resource-1', revision_id: 'revision-1' }).kind).toBe(kind);
  });

  it('parses safe center summaries and rejects raw candidate payloads', () => {
    const resources = resourcePageSchema.parse({ items: [{
      id: 'resource-1', resource_id: 'skill-1', resource_kind: 'skill', status: 'active',
      safe_summary: { resource_name: '问答技能', changed_fields: ['instructions'] }, created_at: '2026-01-01T00:00:00Z',
    }] });
    const candidate = {
      id: 'candidate-1', resource_id: 'skill-1', revision_id: 'revision-2', parent_revision_id: 'revision-1',
      source: 'optimization', status: 'proposed', resource_kind: 'skill', state_version: 1,
      safe_diff: {
        changed_fields: ['instructions'],
        changes: { instructions: { before: '旧指令', after: '新指令' } },
        parent_missing: false,
      },
      created_at: '2026-01-01T00:00:00Z',
    };
    expect(resources.items[0].safe_summary.resource_name).toBe('问答技能');
    expect(candidatePageSchema.parse({ items: [candidate] }).items[0].safe_diff.changed_fields).toEqual(['instructions']);
    expect(() => candidatePageSchema.parse({ items: [{ ...candidate, payload: { prompt: 'secret' } }] })).toThrow();
  });

  it('parses experiment gates and timeline events', () => {
    const experiments = experimentPageSchema.parse({ items: [{
      id: 'experiment-1', resource_id: 'agent-1', stable_revision_id: 'stable-1', canary_revision_id: 'canary-1',
      status: 'active', recommendation: 'hold', resource_kind: 'agent', stage_percent: 5, safety_stopped: false,
      state_version: 2,
      gates: { quality: 'passed', cost: 'pending', latency: 'passed', error_rate: 'passed', security: 'passed' },
      promotion_evidence: { eligible: false,
        gates: { quality: 'passed', cost: 'pending', latency: 'passed', error_rate: 'passed', security: 'passed' },
        blockers: [{ code: 'insufficient_samples', category: 'sample', message: '样本量不足' }] },
      created_at: '2026-01-01T00:00:00Z',
    }] });
    const timeline = timelinePageSchema.parse({ items: [{
      id: 'event-1', kind: 'run', status: 'succeeded', summary: '评测通过', resource_id: 'agent-1',
      resource_kind: 'agent', created_at: '2026-01-01T00:00:00Z',
    }] });
    expect(experiments.items[0].gates?.security).toBe('passed');
    expect(timeline.items[0].summary).toBe('评测通过');
  });

  it('keeps the frozen error envelope', () => {
    expect(errorResponseSchema.parse({ error: '操作失败' })).toEqual({ error: '操作失败' });
    expect(() => errorResponseSchema.parse({ message: '操作失败' })).toThrow();
  });

  it.each([
    ['skill', { label: '客服技能', nested: { enabled: true, count: 2 } }],
    ['agent', { model_name: 'qwen-plus', tools: ['search', 'calculator'] }],
    ['mcp', { transport: 'stdio', capabilities: { tools: 3, resources: 1 } }],
    ['knowledge', { workspace_name: '产品手册', chunking: { strategy: 'semantic' } }],
  ])('accepts JSON-safe %s adapter summaries with legitimate extension keys', (_kind, summary) => {
    expect(safeSummarySchema.parse(summary)).toEqual(summary);
  });

  it.each([
    { payload: { instructions: 'raw' } },
    { nested: { raw_prompt: 'secret' } },
    { auth: { credentials: { username: 'u' } } },
    { api_key: 'secret' },
    { nested: [{ token: 'secret' }] },
    { retrieved_content: 'document body' },
    { document_content: 'document body' },
    { tool: { arguments: { query: 'private' } } },
    { tool_raw_response: 'private' },
    { encrypted_payload_ref: 'object://secret' },
    { auth: { cookie: 'session=secret' } },
    { auth: { Session: 'secret' } },
    { database: { connectionString: 'postgres://secret' } },
    { tls: { CERT: 'secret' } },
    { tls: { KEY: 'secret' } },
  ])('rejects recursively sensitive or raw summary keys', (summary) => {
    expect(() => safeSummarySchema.parse(summary)).toThrow();
  });

  it('strictly parses candidate and experiment command responses', () => {
    expect(candidateCommandResponseSchema.parse({
      id: 'candidate-1', resource_id: 'skill-1', revision_id: 'revision-2', parent_revision_id: 'revision-1',
      source: 'optimization', status: 'rejected', resource_kind: 'skill', state_version: 2, safe_diff: {
        changed_fields: ['label'], changes: { label: { before: 'old', after: 'new' } }, parent_missing: false,
      }, created_at: '2026-01-01T00:00:00Z',
    }).state_version).toBe(2);
    expect(experimentCommandResponseSchema.parse({
      id: 'experiment-1', resource_kind: 'agent', resource_id: 'agent-1', stable_revision_id: 'stable-1',
      canary_revision_id: 'canary-1', suite_revision_id: 'suite-1', status: 'paused', stage: 5,
      policy: { stages: [5, 20], min_samples: 100, min_observation_minutes: 60, max_cost_regression: 0.1,
        max_latency_regression: 0.2, max_error_rate_increase: 0.01 }, state_version: 3,
      recommendation: 'hold', safety_stopped: false,
    }).state_version).toBe(3);
  });

  it.each(['system_prompt', 'systemPrompt', 'developer-prompt', 'API_TOKEN', 'bearerToken', 'retrieved_chunks']) (
    'rejects unsafe alias %s while allowing safe metadata names', (key) => {
      expect(() => safeSummarySchema.parse({ nested: { [key]: 'raw' } })).toThrow();
      expect(safeSummarySchema.parse({ promptVersion: 'v2', token_count: 12, prompt_hash: 'sha256',
        model_token_limit: 8192 })).toBeTruthy();
    },
  );

  it.each([
    { changed_fields: Array.from({ length: 33 }, (_, index) => `field_${index}`), changes: {}, parent_missing: false },
    { changed_fields: ['label', 'label'], changes: { label: { before: 'a', after: 'b' } }, parent_missing: false },
    { changed_fields: ['label'], changes: { other: { before: 'a', after: 'b' } }, parent_missing: false },
    { changed_fields: ['raw_payload'], changes: { raw_payload: { before: 'a', after: 'b' } }, parent_missing: false },
    { changed_fields: ['system_prompt'], changes: { system_prompt: { before: 'a', after: 'b' } }, parent_missing: false },
  ])('rejects invalid candidate safe diff contracts', (safeDiff) => {
    expect(() => candidateCommandResponseSchema.parse({
      id: 'candidate-1', resource_id: 'skill-1', revision_id: 'revision-2', parent_revision_id: 'revision-1',
      source: 'optimization', status: 'rejected', resource_kind: 'skill', state_version: 2,
      safe_diff: safeDiff, created_at: '2026-01-01T00:00:00Z',
    })).toThrow();
  });

  it.each([
    'api_key=secret', 'API_KEY = secret', 'access_token: secret', 'client_secret = secret',
    'Authorization: Bearer secret', 'authorization = basic abc123',
    'https://example.test?api_key=secret', 'note(api_key=secret)', '{"api_key":"secret"}',
    'prefix?ACCESS_TOKEN=secret', '{"Authorization":"Bearer secret"}',
  ])('rejects sensitive summary value marker %s', (value) => {
    expect(() => safeSummarySchema.parse({ note: value })).toThrow();
  });

  it.each(['token_count=10', 'API key rotation policy', 'authorization guide', 'my_api_key_count=10',
    'my-api_key=metadata', 'api_key_rotation_policy', 'prompt_version=v2'])(
    'allows safe summary wording %s', (value) => {
      expect(safeSummarySchema.parse({ note: value })).toEqual({ note: value });
    },
  );
});

describe('dimensionScoreSchema', () => {
  it('parses a valid dimension', () => {
    const dim = dimensionScoreSchema.parse({ name: 'faithfulness', score: 0.6, passed: true, confidence: 0.9 });
    expect(dim).toEqual({ name: 'faithfulness', score: 0.6, passed: true, confidence: 0.9 });
  });
});

describe('evaluationCaseSchema', () => {
  it('parses tool_spec and step_judge on a case', () => {
    const testCase = evaluationCaseSchema.parse({
      name: '工具链路', input: '查天气', expected_output: '晴天', assertion_mode: 'contains',
      tool_spec: { must_call: ['weather'], must_not_call: ['delete'], order: ['search', 'weather'], max_calls: 5 },
      step_judge: { criteria: '每一步都应给出清晰解释' },
    });
    expect(testCase.tool_spec?.must_call).toEqual(['weather']);
    expect(testCase.tool_spec?.order).toEqual(['search', 'weather']);
    expect(testCase.tool_spec?.max_calls).toBe(5);
    expect(testCase.step_judge?.criteria).toContain('清晰解释');
  });

  it('omits tool_spec and step_judge when the case has none', () => {
    const testCase = evaluationCaseSchema.parse({
      name: '简单', input: 'hi', expected_output: 'hello', assertion_mode: 'exact',
    });
    expect(testCase.tool_spec).toBeUndefined();
    expect(testCase.step_judge).toBeUndefined();
  });
});

describe('evaluationRunSchema', () => {
  it('parses run results with dimensions and failure_reason', () => {
    const run = evaluationRunSchema.parse({
      id: 'r1', resource: { kind: 'skill', resource_id: 's1', revision_id: 'v1' },
      suite_revision_id: 'rev-1', passed: false, total_cases: 1, passed_cases: 0,
      metrics: { version: { suite_revision_id: 'rev-1', platform_seq: 3, resource_version: 'v1' } },
      results: [{ case_id: 'c1', passed: false, process_pass: true, dimensions: [{ name: 'faithfulness', score: 0.3, passed: false }], failure_reason: 'dimension:faithfulness', trace_evidence: { cost_usd: 0.05, latency_ms: 200, success: false, tool_call_count: 3, tool_error_count: 1 } }],
    });
    expect(run.results[0].failure_reason).toBe('dimension:faithfulness');
    expect(run.results[0].dimensions?.[0].score).toBe(0.3);
    expect(run.results[0].trace_evidence?.latency_ms).toBe(200);
  });

  it('parses process_pass, process_failure and the tool sequence on a result', () => {
    const run = evaluationRunSchema.parse({
      id: 'r2', resource: { kind: 'skill', resource_id: 's1', revision_id: 'v1' },
      suite_revision_id: 'rev-1', passed: false, total_cases: 1, passed_cases: 0,
      results: [{
        case_id: 'c1', passed: true, process_pass: false,
        process_failure: 'process:must_not_call:delete',
        tools: [{ tool_name: 'delete', tool_type: 'mcp', step_index: 2, provider_type: 'zhipu',
          capability_id: 'cap-1', arguments: { key: 'value' }, raw_text: '删除一行' }],
      }],
    });
    const result = run.results[0];
    expect(result.process_pass).toBe(false);
    expect(result.process_failure).toBe('process:must_not_call:delete');
    expect(result.tools?.[0].tool_name).toBe('delete');
    expect(result.tools?.[0].arguments).toEqual({ key: 'value' });
    expect(result.tools?.[0].raw_text).toBe('删除一行');
  });
});

describe('observedTraceEvidenceSchema', () => {
  it('parses a valid trace evidence object', () => {
    const ev = observedTraceEvidenceSchema.parse({ cost_usd: 0.05, latency_ms: 200, success: false, tool_call_count: 3, tool_error_count: 1 });
    expect(ev.tool_call_count).toBe(3);
  });
});

describe('evaluation monitor schemas', () => {
  // §4.2 端点 1 样例：quality 仅列实际出现维度；cost 延迟可 null；process 可为对象或 null。
  const summaryRow = {
    resource_kind: 'skill', resource_id: 'skill-a', sample_count: 128,
    quality: [{ dimension: 'faithfulness', pass_rate: 0.92, avg_score: 0.92, avg_confidence: 0.87, samples: 128 }],
    behavior: { rule_hits: 15, retry_count: 3, escalation_count: 1, abandonment_count: 0,
      verdict: { pass: 120, flag: 6, block: 2 } },
    cost: { total_tokens: 154000, total_cost_usd: 0.42, avg_latency_ms: 1800, p95_latency_ms: 5200 },
    process: { process_pass_rate: 0.67, run_id: 'run-9', run_created_at: '2026-09-02T08:00:00Z' },
  };

  it('parses the endpoint 1 resource-row summary with inner behavior and nullable process', () => {
    const page = monitorResourcesPageSchema.parse({ items: [summaryRow], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    expect(page.items[0].quality[0].pass_rate).toBe(0.92);
    expect(page.items[0].behavior.verdict.block).toBe(2);
    expect(page.items[0].cost.p95_latency_ms).toBe(5200);
    expect(page.items[0].process?.run_id).toBe('run-9');
    expect(page.window.from).toBe('2026-08-27T00:00:00Z');
  });

  it('keeps process null when the window has no succeeded run', () => {
    const row = monitorResourceSummarySchema.parse({ ...summaryRow, process: null });
    expect(row.process).toBeNull();
  });

  it('keeps latency null when no latency sample exists', () => {
    const cost = costStatsSchema.parse({ total_tokens: 0, total_cost_usd: 0, avg_latency_ms: null, p95_latency_ms: null });
    expect(cost.avg_latency_ms).toBeNull();
  });

  it('accepts empty quality and empty series/runs as honest empty states', () => {
    expect(monitorResourceSummarySchema.parse({ ...summaryRow, quality: [], process: null }).quality).toEqual([]);
    const trend = monitorTrendSchema.parse({ resource_kind: 'skill', resource_id: 'skill-a', series: [], runs: [] });
    expect(trend.series).toEqual([]);
    expect(trend.runs).toEqual([]);
  });

  it('rejects unknown top-level and nested keys (strict wire contract)', () => {
    expect(() => monitorResourcesPageSchema.parse({ items: [summaryRow], window: { from: 'a', to: 'b' }, next_cursor: 'x' })).toThrow();
    expect(() => behaviorStatsSchema.parse({ rule_hits: 1, retry_count: 0, escalation_count: 0,
      abandonment_count: 0, verdict: { pass: 1, flag: 0, block: 0 }, extra: true })).toThrow();
    expect(() => monitorResourceSummarySchema.parse({ ...summaryRow, resource_kind: 'plugin' })).toThrow();
  });

  it('parses a full-limit row set without a pagination field (truncation is client-inferred)', () => {
    const items = Array.from({ length: 20 }, (_, index) => ({ ...summaryRow, resource_id: `skill-${index}` }));
    const page = monitorResourcesPageSchema.parse({ items, window: { from: 'a', to: 'b' } });
    expect(page.items).toHaveLength(20);
    expect(page.items[0].resource_id).toBe('skill-0');
  });
});

describe('suite management schemas', () => {
  it('parses the enhanced suite list row with active/draft version meta', () => {
    const parsed = suiteSummarySchema.parse({
      id: 'suite-1', name: '投诉基线', description: '', resource_kind: 'skill', status: 'active',
      active_revision_id: 'rev-v3', draft_revision_id: 'rev-draft',
      active_version_no: 3, draft_version_no: 0, active_case_count: 7, draft_case_count: 2,
      created_by: 'user-1', created_at: '2026-09-01T00:00:00Z',
    });
    expect(parsed.active_version_no).toBe(3);
    expect(parsed.active_case_count).toBe(7);
    expect(parsed.resource_kind).toBe('skill');
  });

  it('parses a legacy suite list row that omits the S1-2 additive keys', () => {
    const parsed = suiteSummarySchema.parse({
      id: 'suite-legacy', name: '旧基线', description: '历史遗留', status: 'published',
      created_by: 'user-1', created_at: '2026-08-01T00:00:00Z',
    });
    expect(parsed.active_version_no).toBeUndefined();
    expect(parsed.resource_kind).toBeUndefined();
  });

  it('rejects an unknown key on the strict suite list row', () => {
    expect(() => suiteSummarySchema.parse({
      id: 's', name: 'n', description: '', status: 'published', created_at: '2026-08-01T00:00:00Z', extra: true,
    })).toThrow();
  });

  it('parses the lightweight version chain rows with nullable published_at', () => {
    const published = suiteRevisionMetaSchema.parse({
      id: 'rev-v2', version_no: 2, status: 'published', resource_kind: 'skill',
      created_by: 'user-1', published_at: '2026-08-20T00:00:00Z', enabled_case_count: 7,
    });
    expect(published.version_no).toBe(2);
    expect(published.published_at).toBe('2026-08-20T00:00:00Z');
    const draft = suiteRevisionMetaSchema.parse({
      id: 'rev-draft', status: 'draft', resource_kind: 'skill',
      created_by: 'user-1', enabled_case_count: 2,
    });
    expect(draft.version_no).toBeUndefined();
    expect(draft.published_at).toBeUndefined();
    expect(draft.status).toBe('draft');
  });
});
