import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { useEvaluationCenter } from './useEvaluationCenter';

const auth = vi.hoisted(() => ({ role: 'member' }));
const api = vi.hoisted(() => ({
  getOverview: vi.fn(), listResources: vi.fn(), listSuites: vi.fn(), listRuns: vi.fn(),
  listCandidates: vi.fn(), listExperiments: vi.fn(), getTimeline: vi.fn(), rejectCandidate: vi.fn(),
  pauseExperiment: vi.fn(), promoteExperiment: vi.fn(), rollbackExperiment: vi.fn(),
  createSuite: vi.fn(), publishSuite: vi.fn(), enqueueRun: vi.fn(),
}));
vi.mock('@/modules/iam', () => ({
  useAuth: () => ({ user: { role: auth.role } }),
  useTenantRole: () => ({
    role: auth.role,
    isAdmin: auth.role === 'admin' || auth.role === 'owner',
    isOwner: auth.role === 'owner',
    isMember: auth.role === 'member',
    hasTenantRole: () => auth.role === 'admin' || auth.role === 'owner',
  }),
}));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

const emptyPage = { items: [] };
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
};
describe('useEvaluationCenter', () => {
  beforeEach(() => {
    auth.role = 'member';
    Object.values(api).forEach((mock) => mock.mockReset());
    api.getOverview.mockResolvedValue({ resources: 0, suites: 0, runs: 0, candidates: 0, experiments: 0 });
    api.listResources.mockResolvedValue(emptyPage); api.listSuites.mockResolvedValue(emptyPage);
    api.listRuns.mockResolvedValue(emptyPage); api.listCandidates.mockResolvedValue(emptyPage);
    api.listExperiments.mockResolvedValue(emptyPage);
  });

  it('loads center data and derives management permission from authenticated role', async () => {
    auth.role = 'admin';
    const { result } = renderHook(() => useEvaluationCenter({ resource_kind: 'skill', resource_id: 'skill-1' }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.canManageEvaluation).toBe(true);
    expect(api.listResources).toHaveBeenCalledWith({ resource_kind: 'skill', resource_id: 'skill-1' });
  });

  it('preserves the frozen Chinese API error after failed loading', async () => {
    api.getOverview.mockRejectedValue({ response: { data: { error: '评测资源不存在' } } });
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toBe('评测资源不存在');
  });

  it('clears the previous filter generation when any current collection fails', async () => {
    api.getOverview.mockResolvedValueOnce({ resources: 1, suites: 1, runs: 0, candidates: 0, experiments: 0 });
    api.listResources.mockResolvedValueOnce({ items: [{ resource_kind: 'skill', resource_id: 'skill-old' }] });
    const { result, rerender } = renderHook(({ kind }) => useEvaluationCenter({ resource_kind: kind }), {
      initialProps: { kind: 'skill' as 'skill' | 'agent' },
    });
    await waitFor(() => expect(result.current.resources.items).toHaveLength(1));

    api.getOverview.mockResolvedValue({ resources: 0, suites: 0, runs: 0, candidates: 0, experiments: 0 });
    api.listResources.mockResolvedValue({ items: [] });
    api.listSuites.mockRejectedValueOnce({ response: { data: { error: '评测套件服务不可用' } } });
    rerender({ kind: 'agent' });

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toBe('评测套件服务不可用');
    expect(result.current.overview).toBeNull();
    expect(result.current.resources.items).toEqual([]);
    expect(result.current.suites.items).toEqual([]);
    expect(result.current.runs.items).toEqual([]);
    expect(result.current.candidates.items).toEqual([]);
    expect(result.current.experiments.items).toEqual([]);
  });

  it('does not update state after unmounting an async effect', async () => {
    let resolveOverview!: (value: unknown) => void;
    api.getOverview.mockReturnValue(new Promise((resolve) => { resolveOverview = resolve; }));
    const { result, unmount } = renderHook(() => useEvaluationCenter());
    unmount();
    await act(async () => { resolveOverview({ resources: 1, suites: 0, runs: 0, candidates: 0, experiments: 0 }); });
    expect(result.current.overview).toBeNull();
  });

  it('refuses commands for members before calling the API', async () => {
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    await expect(result.current.rejectCandidate('candidate-1', {
      reason: '拒绝', idempotency_key: 'request-1', expected_state_version: 1,
    })).rejects.toThrow('仅租户管理员可执行评测命令');
    expect(api.rejectCandidate).not.toHaveBeenCalled();
  });

  it('create mode builds a suite, publishes v1 and enqueues the baseline run before refreshing', async () => {
    auth.role = 'admin';
    api.createSuite.mockResolvedValue({ suite: { id: 'suite-1' }, revision: { id: 'draft-revision-1' } });
    api.publishSuite.mockResolvedValue({ id: 'suite-revision-1' });
    api.enqueueRun.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const job = await act(async () => result.current.createEvaluation(createInput()));
    expect(api.createSuite).toHaveBeenCalledWith(expect.objectContaining({ name: '基线评测', resourceKind: 'skill' }));
    expect(api.publishSuite).toHaveBeenCalledWith('suite-1');
    expect(api.enqueueRun).toHaveBeenCalledWith(expect.objectContaining({ resource_id: 'skill-1' }),
      'suite-revision-1', expect.any(String));
    expect(job).toEqual({ job_id: 'job-1', status: 'queued' });
  });

  it('published mode enqueues the existing revision directly without suite writes', async () => {
    auth.role = 'admin';
    api.enqueueRun.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const job = await act(async () => result.current.createEvaluation(publishedInput()));
    expect(api.createSuite).not.toHaveBeenCalled();
    expect(api.publishSuite).not.toHaveBeenCalled();
    expect(api.enqueueRun).toHaveBeenCalledTimes(1);
    expect(api.enqueueRun).toHaveBeenCalledWith(
      { kind: 'skill', resource_id: 'skill-1', revision_id: 'revision-1' }, 'published-revision-2', expect.any(String));
    expect(job).toEqual({ job_id: 'job-1', status: 'queued' });
  });

  it('unpublished mode publishes the draft suite v1 before enqueuing the run', async () => {
    auth.role = 'admin';
    api.publishSuite.mockResolvedValue({ id: 'published-u1' });
    api.enqueueRun.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const job = await act(async () => result.current.createEvaluation(unpublishedInput()));
    expect(api.createSuite).not.toHaveBeenCalled();
    expect(api.publishSuite).toHaveBeenCalledWith('suite-u1');
    expect(api.enqueueRun).toHaveBeenCalledTimes(1);
    expect(api.enqueueRun).toHaveBeenCalledWith(
      { kind: 'skill', resource_id: 'skill-1', revision_id: 'revision-1' }, 'published-u1', expect.any(String));
    expect(job).toEqual({ job_id: 'job-1', status: 'queued' });
  });

  it('refuses createEvaluation for members before any API call', async () => {
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    await expect(result.current.createEvaluation(publishedInput())).rejects.toThrow('仅租户管理员可执行评测命令');
    expect(api.createSuite).not.toHaveBeenCalled();
    expect(api.publishSuite).not.toHaveBeenCalled();
    expect(api.enqueueRun).not.toHaveBeenCalled();
  });

  it('resumes at publish after publish failure without recreating the suite', async () => {
    auth.role = 'admin';
    api.createSuite.mockResolvedValue({ suite: { id: 'suite-1' } });
    api.publishSuite.mockRejectedValueOnce(new Error('发布失败')).mockResolvedValue({ id: 'published-1' });
    api.enqueueRun.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const data = createInput();
    await act(async () => { await result.current.createEvaluation(data).catch(() => undefined); });
    await act(async () => { await result.current.createEvaluation(data); });
    expect(api.createSuite).toHaveBeenCalledTimes(1);
    expect(api.publishSuite).toHaveBeenCalledTimes(2);
    expect(api.enqueueRun).toHaveBeenCalledTimes(1);
  });

  it('resumes enqueue with the same idempotency key after enqueue failure', async () => {
    auth.role = 'admin';
    api.createSuite.mockResolvedValue({ suite: { id: 'suite-1' } });
    api.publishSuite.mockResolvedValue({ id: 'published-1' });
    api.enqueueRun.mockRejectedValueOnce(new Error('入队失败')).mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const data = createInput();
    await act(async () => { await result.current.createEvaluation(data).catch(() => undefined); });
    await act(async () => { await result.current.createEvaluation(data); });
    expect(api.createSuite).toHaveBeenCalledTimes(1);
    expect(api.publishSuite).toHaveBeenCalledTimes(1);
    expect(api.enqueueRun).toHaveBeenCalledTimes(2);
    expect(api.enqueueRun.mock.calls[0][2]).toBe(api.enqueueRun.mock.calls[1][2]);
  });

  it('does not republish the draft suite when retrying an unpublished run after enqueue failure', async () => {
    auth.role = 'admin';
    api.publishSuite.mockResolvedValue({ id: 'published-u1' });
    api.enqueueRun.mockRejectedValueOnce(new Error('入队失败')).mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const data = unpublishedInput();
    await act(async () => { await result.current.createEvaluation(data).catch(() => undefined); });
    await act(async () => { await result.current.createEvaluation(data); });
    expect(api.createSuite).not.toHaveBeenCalled();
    expect(api.publishSuite).toHaveBeenCalledTimes(1);
    expect(api.enqueueRun).toHaveBeenCalledTimes(2);
    expect(api.enqueueRun.mock.calls[0][2]).toBe(api.enqueueRun.mock.calls[1][2]);
  });

  it('coalesces double submit into one create workflow', async () => {
    auth.role = 'admin';
    const create = deferred<any>();
    api.createSuite.mockReturnValue(create.promise);
    const { result } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    let first!: Promise<unknown>; let second!: Promise<unknown>;
    act(() => { first = result.current.createEvaluation(createInput());
      second = result.current.createEvaluation(createInput()); });
    expect(api.createSuite).toHaveBeenCalledTimes(1);
    create.resolve({ suite: { id: 'suite-1' } });
    api.publishSuite.mockResolvedValue({ id: 'published-1' });
    api.enqueueRun.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    await act(async () => { await Promise.all([first, second]); });
  });

  it('ignores a stale filter load that resolves after the latest load', async () => {
    const oldOverview = deferred<any>();
    api.getOverview.mockReturnValueOnce(oldOverview.promise).mockResolvedValueOnce({
      resources: 2, suites: 0, runs: 0, candidates: 0, experiments: 0,
    });
    const { result, rerender } = renderHook(({ id }) => useEvaluationCenter({ resource_id: id }), {
      initialProps: { id: 'resource-a' },
    });
    rerender({ id: 'resource-b' });
    await waitFor(() => expect(result.current.overview?.resources).toBe(2));
    await act(async () => oldOverview.resolve({ resources: 1, suites: 0, runs: 0, candidates: 0, experiments: 0 }));
    expect(result.current.overview?.resources).toBe(2);
  });

  it('keeps the newest command refresh over an overlapping reload and current filters', async () => {
    auth.role = 'admin';
    const { result, rerender } = renderHook(({ id }) => useEvaluationCenter({ resource_id: id }), {
      initialProps: { id: 'resource-a' },
    });
    await waitFor(() => expect(result.current.loading).toBe(false));
    const stale = deferred<any>();
    api.getOverview.mockReturnValueOnce(stale.promise).mockResolvedValue({
      resources: 3, suites: 0, runs: 0, candidates: 0, experiments: 0,
    });
    await act(async () => { void result.current.reload(); rerender({ id: 'resource-b' }); });
    api.rejectCandidate.mockResolvedValue({ id: 'candidate-1' });
    await act(async () => { await result.current.rejectCandidate('candidate-1', {
      reason: '拒绝', idempotency_key: 'request-1', expected_state_version: 1,
    }); });
    await act(async () => stale.resolve({ resources: 1, suites: 0, runs: 0, candidates: 0, experiments: 0 }));
    expect(result.current.overview?.resources).toBe(3);
    expect(api.listResources).toHaveBeenLastCalledWith({ resource_id: 'resource-b' });
  });

  it('skips post-command refresh after unmount', async () => {
    auth.role = 'admin';
    const command = deferred<any>();
    api.rejectCandidate.mockReturnValue(command.promise);
    const { result, unmount } = renderHook(() => useEvaluationCenter());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const queryCalls = [api.getOverview, api.listResources, api.listSuites, api.listRuns,
      api.listCandidates, api.listExperiments].map((mock) => mock.mock.calls.length);
    const pending = result.current.rejectCandidate('candidate-1', {
      reason: '拒绝', idempotency_key: 'request-1', expected_state_version: 1,
    });
    unmount();
    await act(async () => command.resolve({ id: 'candidate-1' }));
    await pending;
    expect([api.getOverview, api.listResources, api.listSuites, api.listRuns,
      api.listCandidates, api.listExperiments].map((mock) => mock.mock.calls.length)).toEqual(queryCalls);
  });
});

const baseResource = { kind: 'skill' as const, resource_id: 'skill-1', revision_id: 'revision-1' };
// create 计划：内联建套件 → publish v1 → enqueue（沿用旧「内联 1 case 即跑」升级语义）。
const createInput = () => ({ mode: 'create' as const, resource: baseResource, name: '基线评测',
  description: '发布前基线', cases: [{ name: '问候', input: '你好', expected_output: '您好',
    assertion_mode: 'contains' as const, enabled: true }] });
// published 计划：直接跑既有 published revision（纯读）。
const publishedInput = () => ({ mode: 'published' as const, resource: baseResource, suiteId: 'suite-p1',
  revisionId: 'published-revision-2' });
// unpublished 计划：先 publish 草稿套件成 v1 再跑。
const unpublishedInput = () => ({ mode: 'unpublished' as const, resource: baseResource, suiteId: 'suite-u1' });
