import { beforeEach, describe, expect, it, vi } from 'vitest';

import { evaluationApi } from './evaluation.api';

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() }));
vi.mock('@/services/client', () => ({ default: client }));

describe('evaluation center api', () => {
  beforeEach(() => { client.get.mockReset(); client.post.mockReset(); client.put.mockReset(); client.delete.mockReset(); });

  it.each([
    ['getOverview', '/evaluations/overview', undefined, { resources: 0, suites: 0, runs: 0, candidates: 0, experiments: 0 }],
    ['listResources', '/evaluations/resources', { resource_kind: 'agent', status: 'active', cursor: 'next', limit: 20 }, { items: [] }],
    ['listSuites', '/evaluations/suites', { resource_kind: 'skill' }, { items: [] }],
    ['listRuns', '/evaluations/runs', { resource_id: 'resource-1' }, { items: [] }],
    ['listCandidates', '/evaluations/candidates', { status: 'proposed' }, { items: [] }],
    ['listExperiments', '/evaluations/experiments', { status: 'active' }, { items: [] }],
  ] as const)('%s uses the shared client and query params', async (method, path, params, data) => {
    client.get.mockResolvedValue({ data });
    await (evaluationApi[method] as (filters?: unknown) => Promise<unknown>)(params);
    if (params) expect(client.get).toHaveBeenCalledWith(path, { params });
    else expect(client.get).toHaveBeenCalledWith(path);
  });

  it('encodes timeline resource paths and forwards cursors', async () => {
    client.get.mockResolvedValue({ data: { items: [] } });
    await evaluationApi.getTimeline('knowledge', 'space/name', { cursor: 'next', limit: 10 });
    expect(client.get).toHaveBeenCalledWith('/evaluations/resources/knowledge/space%2Fname/timeline', {
      params: { cursor: 'next', limit: 10 },
    });
  });

  it('creates provider-neutral suites with the selected resource kind', async () => {
    client.post.mockResolvedValue({ data: { suite: { id: 'suite-1', name: 'Agent 基线' }, revision: {
      id: 'revision-1', suite_id: 'suite-1', status: 'draft', resource_kind: 'agent', cases: [],
    } } });
    await evaluationApi.createSuite({ name: 'Agent 基线', resourceKind: 'agent', cases: [] });
    expect(client.post).toHaveBeenCalledWith('/evaluations/suites', {
      name: 'Agent 基线', resource_kind: 'agent', cases: [],
    });
  });

  it('records manual feedback against the被测轨 resource kind', async () => {
    client.post.mockResolvedValue({ data: { decision: 'ok' } });
    await evaluationApi.recordFeedback({
      resourceKind: 'agent', traceId: 'trace-1', resourceId: 'agent-1', score: 0.9,
      outcome: { source: 'manual' }, idempotencyKey: 'idem-1',
    });
    expect(client.post).toHaveBeenCalledWith('/evaluations/feedback', {
      trace_id: 'trace-1', resource_kind: 'agent', resource_id: 'agent-1', score: 0.9,
      outcome: { source: 'manual' }, idempotency_key: 'idem-1',
    });
  });

  it('updates a session draft case with the full script and omits single-turn input', async () => {
    client.put.mockResolvedValue({ data: {
      id: 'c4', name: '退货会话', expected_output: '已受理退款', assertion_mode: 'contains', enabled: true,
      session: { goal: '处理退货', turns: [{ user: '快递没到' }] },
    } });
    await evaluationApi.updateDraftCase('s1', 'c4', {
      name: '退货会话', expectedOutput: '已受理退款', assertionMode: 'contains', enabled: true,
      session: { goal: '处理退货', turns: [{ user: '快递没到' }] },
    });
    expect(client.put).toHaveBeenCalledWith('/evaluations/suites/s1/draft/cases/c4', {
      name: '退货会话', expected_output: '已受理退款', assertion_mode: 'contains', enabled: true,
      session: { goal: '处理退货', turns: [{ user: '快递没到' }] },
    });
  });

  it('registers a published resource baseline through the shared client', async () => {
    client.post.mockResolvedValue({ data: { kind: 'skill', resource_id: 'skill/1', revision_id: 'revision-1' } });
    await expect(evaluationApi.createBaseline('skill', 'skill/1')).resolves.toMatchObject({
      resource_id: 'skill/1', revision_id: 'revision-1',
    });
    expect(client.post).toHaveBeenCalledWith('/evaluations/resources/skill/skill%2F1/baseline');
  });

  it('parses serialized promotion evidence from the experiment center endpoint', async () => {
    client.get.mockResolvedValue({ data: { items: [{ id: 'experiment-1', resource_id: 'agent-1',
      stable_revision_id: 'stable-1', canary_revision_id: 'canary-1', status: 'running', recommendation: 'promote',
      resource_kind: 'agent', stage_percent: 100, safety_stopped: false, state_version: 2,
      promotion_evidence: { eligible: true, gates: { quality: 'passed', cost: 'passed', latency: 'passed',
        error_rate: 'passed', security: 'passed' }, blockers: [] }, created_at: '2026-07-23T00:00:00Z' }] } });
    const page = await evaluationApi.listExperiments();
    expect(page.items[0].promotion_evidence.eligible).toBe(true);
  });

  it.each([
    ['rejectCandidate', '/evaluations/candidates/candidate-1/reject'],
    ['pauseExperiment', '/evaluations/experiments/candidate-1/pause'],
    ['promoteExperiment', '/evaluations/experiments/candidate-1/promote'],
    ['rollbackExperiment', '/evaluations/experiments/candidate-1/rollback'],
  ] as const)('%s omits actor identity', async (method, path) => {
    const isCandidate = method === 'rejectCandidate';
    client.post.mockResolvedValue({ data: isCandidate ? {
      id: 'candidate-1', resource_id: 'skill-1', revision_id: 'revision-2', parent_revision_id: 'revision-1',
      source: 'optimization', status: 'rejected', resource_kind: 'skill', state_version: 3,
      safe_diff: { changed_fields: [], changes: {}, parent_missing: false }, created_at: '2026-01-01T00:00:00Z',
    } : {
      id: 'candidate-1', resource_kind: 'skill', resource_id: 'skill-1', stable_revision_id: 'stable-1',
      canary_revision_id: 'canary-1', suite_revision_id: 'suite-1', status: 'paused', stage: 5,
      policy: { stages: [5, 20], min_samples: 100, min_observation_minutes: 60, max_cost_regression: 0.1,
        max_latency_regression: 0.2, max_error_rate_increase: 0.01 }, state_version: 3,
      recommendation: 'hold', safety_stopped: false,
    } });
    await (evaluationApi[method] as (id: string, command: unknown) => Promise<unknown>)('candidate-1', {
      reason: '人工复核', idempotency_key: 'request-1', expected_state_version: 2,
    });
    expect(client.post).toHaveBeenCalledWith(path, {
      reason: '人工复核', idempotency_key: 'request-1', expected_state_version: 2,
    });
    expect(client.post.mock.calls[0][1]).not.toHaveProperty('actor_id');
  });

  it.each(['rejectCandidate', 'pauseExperiment', 'promoteExperiment', 'rollbackExperiment'] as const)(
    '%s rejects unexpected sensitive response fields', async (method) => {
      client.post.mockResolvedValue({ data: { id: 'resource-1', status: 'paused', raw_payload: 'secret' } });
      await expect(evaluationApi[method]('resource-1', {
        reason: '人工复核', idempotency_key: 'request-1', expected_state_version: 2,
      })).rejects.toThrow();
    },
  );

  it('lists monitor resources with the window and limit forwarded as query params', async () => {
    const data = { items: [], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } };
    client.get.mockResolvedValue({ data });
    const page = await evaluationApi.listMonitorResources({ resource_kind: 'skill', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z', limit: 20 });
    expect(client.get).toHaveBeenCalledWith('/evaluations/monitoring/resources', {
      params: { resource_kind: 'skill', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z', limit: 20 },
    });
    expect(page.window.from).toBe('2026-08-27T00:00:00Z');
  });

  it('fetches the per-resource trend through the trend endpoint', async () => {
    const data = { resource_kind: 'skill', resource_id: 'skill-a', series: [], runs: [] };
    client.get.mockResolvedValue({ data });
    const trend = await evaluationApi.getMonitorTrend({ resource_kind: 'skill', resource_id: 'skill-a', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' });
    expect(client.get).toHaveBeenCalledWith('/evaluations/monitoring/resources/trend', {
      params: { resource_kind: 'skill', resource_id: 'skill-a', from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' },
    });
    expect(trend.runs).toEqual([]);
  });

  it('reads the suite detail header through GET /suites/:id', async () => {
    client.get.mockResolvedValue({ data: {
      id: 'suite-1', name: '投诉基线', description: '', resource_kind: 'skill', status: 'active',
      active_revision_id: 'rev-v3', draft_revision_id: 'rev-draft',
      active_version_no: 3, draft_version_no: 0, active_case_count: 7, draft_case_count: 2,
      created_by: 'user-1', created_at: '2026-09-01T00:00:00Z',
    } });
    const detail = await evaluationApi.getSuiteDetail('suite/1');
    expect(client.get).toHaveBeenCalledWith('/evaluations/suites/suite%2F1');
    expect(detail.active_version_no).toBe(3);
    expect(detail.draft_case_count).toBe(2);
  });

  it('lists the lightweight version chain through GET /suites/:id/versions', async () => {
    client.get.mockResolvedValue({ data: [
      { id: 'rev-v2', version_no: 2, status: 'published', resource_kind: 'skill',
        created_by: 'user-1', published_at: '2026-08-20T00:00:00Z', enabled_case_count: 7 },
      { id: 'rev-draft', status: 'draft', resource_kind: 'skill', created_by: 'user-1', enabled_case_count: 2 },
    ] });
    const metas = await evaluationApi.listSuiteVersions('suite-1');
    expect(client.get).toHaveBeenCalledWith('/evaluations/suites/suite-1/versions');
    expect(metas).toHaveLength(2);
    expect(metas[0].version_no).toBe(2);
    expect(metas[1].status).toBe('draft');
  });

  it('loads a full published revision through GET /suites/:id/versions/:revisionId', async () => {
    client.get.mockResolvedValue({ data: { id: 'rev-v2', suite_id: 'suite-1', version_no: 2,
      status: 'published', resource_kind: 'skill', cases: [{ name: '物流', input: '快递没更新',
        expected_output: '物流', assertion_mode: 'contains', enabled: true }] } });
    const revision = await evaluationApi.getSuiteRevision('suite-1', 'rev-v2');
    expect(client.get).toHaveBeenCalledWith('/evaluations/suites/suite-1/versions/rev-v2');
    expect(revision.version_no).toBe(2);
    expect(revision.cases[0].name).toBe('物流');
  });

  it('appends a session draft case through POST /suites/:id/draft/cases', async () => {
    client.post.mockResolvedValue({ data: { id: 'c5', name: '退货会话', expected_output: '已受理退款',
      assertion_mode: 'contains', enabled: true,
      session: { goal: '处理退货', turns: [{ user: '快递没到' }] } } });
    const created = await evaluationApi.addDraftCase('suite-1', {
      name: '退货会话', expected_output: '已受理退款', assertion_mode: 'contains', enabled: true,
      session: { goal: '处理退货', turns: [{ user: '快递没到' }] },
    });
    expect(client.post).toHaveBeenCalledWith('/evaluations/suites/suite-1/draft/cases', {
      name: '退货会话', expected_output: '已受理退款', assertion_mode: 'contains', enabled: true,
      session: { goal: '处理退货', turns: [{ user: '快递没到' }] },
    });
    expect(created.id).toBe('c5');
  });

  it('deletes a draft case through DELETE /suites/:id/draft/cases/:caseId', async () => {
    client.delete.mockResolvedValue({});
    await evaluationApi.deleteDraftCase('suite-1', 'c5');
    expect(client.delete).toHaveBeenCalledWith('/evaluations/suites/suite-1/draft/cases/c5');
  });

  it('starts the next draft through POST /suites/:id/draft', async () => {
    client.post.mockResolvedValue({ data: { id: 'rev-draft', suite_id: 'suite-1', status: 'draft',
      resource_kind: 'skill', cases: [] } });
    const draft = await evaluationApi.startNextDraft('suite-1');
    expect(client.post).toHaveBeenCalledWith('/evaluations/suites/suite-1/draft');
    expect(draft.status).toBe('draft');
  });
});
