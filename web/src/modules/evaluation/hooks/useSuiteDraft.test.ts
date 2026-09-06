import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { useSuiteDraft } from './useSuiteDraft';

const api = vi.hoisted(() => ({
  getSuiteDraft: vi.fn(),
  publishSuite: vi.fn(),
  generateSuiteCases: vi.fn(),
  updateDraftCase: vi.fn(),
  deleteDraftCase: vi.fn(),
  addDraftCase: vi.fn(),
  startNextDraft: vi.fn(),
}));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

const draftRev = {
  id: 'draft-3', suite_id: 's1', status: 'draft', resource_kind: 'skill',
  cases: [{ id: 'c1', name: '标准问候', input: '你好', expected_output: '您好', assertion_mode: 'contains', enabled: true }],
};
const successor = { ...draftRev, id: 'draft-4' };
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
};

describe('useSuiteDraft', () => {
  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    api.getSuiteDraft.mockResolvedValue(draftRev);
    api.publishSuite.mockResolvedValue({ ...draftRev, id: 'pub-2', version_no: 2, status: 'published' });
    api.generateSuiteCases.mockResolvedValue({ samples_found: 3, generated: 2, rejected: [] });
    api.updateDraftCase.mockResolvedValue({ ...draftRev.cases[0], name: '新名称' });
    api.addDraftCase.mockResolvedValue({ ...draftRev.cases[0], id: 'c2' });
    api.startNextDraft.mockResolvedValue(successor);
  });

  it('loads the draft on mount', async () => {
    const { result } = renderHook(() => useSuiteDraft({ suiteId: 's1', enabled: true }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(api.getSuiteDraft).toHaveBeenCalledWith('s1');
    expect(result.current.draft?.id).toBe('draft-3');
  });

  it('refuses management commands before calling the api when not enabled', async () => {
    const { result } = renderHook(() => useSuiteDraft({ suiteId: 's1', enabled: false }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await expect(result.current.addCase({
      name: '标准问候', expected_output: '您好', assertion_mode: 'contains', enabled: true,
    })).rejects.toThrow('仅租户管理员可编辑评测集草稿');
    expect(api.addDraftCase).not.toHaveBeenCalled();
  });

  it('publishes, reloads the successor draft and returns the published revision', async () => {
    api.getSuiteDraft.mockResolvedValueOnce(draftRev).mockResolvedValue(successor);
    const { result } = renderHook(() => useSuiteDraft({ suiteId: 's1', enabled: true }));
    await waitFor(() => expect(result.current.draft?.id).toBe('draft-3'));

    const published = await act(async () => result.current.publish());
    expect(api.publishSuite).toHaveBeenCalledWith('s1');
    expect(published?.version_no).toBe(2);
    await waitFor(() => expect(result.current.draft?.id).toBe('draft-4'));
  });

  it('reloads the draft after adding a case', async () => {
    const { result } = renderHook(() => useSuiteDraft({ suiteId: 's1', enabled: true }));
    await waitFor(() => expect(result.current.draft?.id).toBe('draft-3'));
    await act(async () => result.current.addCase({
      name: '新增问候', expected_output: '您好', assertion_mode: 'contains', enabled: true,
    }));
    expect(api.addDraftCase).toHaveBeenCalledWith('s1', expect.objectContaining({ name: '新增问候' }));
    expect(api.getSuiteDraft).toHaveBeenCalledTimes(2);
  });

  it('maps each remaining action onto its api method and reloads', async () => {
    const { result } = renderHook(() => useSuiteDraft({ suiteId: 's1', enabled: true }));
    await waitFor(() => expect(result.current.draft?.id).toBe('draft-3'));

    await act(async () => result.current.saveCase('c1', {
      name: '改名', expectedOutput: '您好', assertionMode: 'contains', enabled: true,
    }));
    expect(api.updateDraftCase).toHaveBeenCalledWith('s1', 'c1', expect.objectContaining({ name: '改名' }));

    await act(async () => result.current.deleteCase('c1'));
    expect(api.deleteDraftCase).toHaveBeenCalledWith('s1', 'c1');

    const generated = await act(async () => result.current.generate({ samplePolicy: 'negative_first', maxCases: 10 }));
    expect(api.generateSuiteCases).toHaveBeenCalledWith('s1', { samplePolicy: 'negative_first', maxCases: 10 });
    expect(generated?.generated).toBe(2);

    await act(async () => result.current.startNextDraft());
    expect(api.startNextDraft).toHaveBeenCalledWith('s1');
    expect(api.getSuiteDraft).toHaveBeenCalledTimes(5);
  });

  it('does not update state after unmounting an in-flight load', async () => {
    const load = deferred<unknown>();
    api.getSuiteDraft.mockReturnValue(load.promise);
    const { result, unmount } = renderHook(() => useSuiteDraft({ suiteId: 's1', enabled: true }));
    unmount();
    await act(async () => { load.resolve({ ...draftRev, id: 'stale' }); });
    expect(result.current.draft).toBeNull();
  });
});
