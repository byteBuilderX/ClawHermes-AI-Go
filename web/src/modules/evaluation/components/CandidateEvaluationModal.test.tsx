import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { CandidateEvaluationModal } from './CandidateEvaluationModal';

const api = vi.hoisted(() => ({ listSuites: vi.fn(), listSuiteVersions: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));
const listSuites = api.listSuites;
const listSuiteVersions = api.listSuiteVersions;

const skillSuite = {
  id: 'suite-1', name: '投诉基线', description: '', status: 'published', resource_kind: 'skill',
  active_revision_id: 'rev-v2', draft_revision_id: 'rev-draft', active_version_no: 2, active_case_count: 5,
  created_by: 'u1', created_at: '2026-09-01T00:00:00Z',
};
const agentSuite = {
  id: 'suite-2', name: 'Agent 基线', description: '', status: 'published', resource_kind: 'agent',
  active_revision_id: 'rev-a1', active_version_no: 1, active_case_count: 3,
  created_by: 'u1', created_at: '2026-09-02T00:00:00Z',
};
const skillVersions = [
  { id: 'rev-v1', version_no: 1, status: 'published', resource_kind: 'skill', enabled_case_count: 4 },
  { id: 'rev-v2', version_no: 2, status: 'published', resource_kind: 'skill', enabled_case_count: 5 },
];

const selectPublishedSuite = async (suiteLabel: string, suiteId: string) => {
  fireEvent.mouseDown(await screen.findByRole('combobox', { name: '评测集' }));
  fireEvent.click(await screen.findByText(suiteLabel));
  await waitFor(() => expect(listSuiteVersions).toHaveBeenCalledWith(suiteId));
};

describe('CandidateEvaluationModal', () => {
  beforeEach(() => {
    listSuites.mockReset();
    listSuiteVersions.mockReset();
  });

  it('submits the picked published suite revision and closes after success', async () => {
    listSuites.mockResolvedValue({ items: [skillSuite] });
    listSuiteVersions.mockResolvedValue(skillVersions);
    const onClose = vi.fn();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<CandidateEvaluationModal open onClose={onClose} onSubmit={onSubmit} resourceKind="skill" />);

    await waitFor(() => expect(listSuites).toHaveBeenCalledWith({ resource_kind: 'skill' }));
    const confirm = screen.getByRole('button', { name: '开始评测' });
    expect(confirm).toBeDisabled();

    await selectPublishedSuite('投诉基线（v2 · 5 个启用用例）', 'suite-1');
    await waitFor(() => expect(confirm).toBeEnabled());
    fireEvent.click(confirm);

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('rev-v2', expect.any(String)));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it('reuses the idempotency key when a failed enqueue is retried', async () => {
    listSuites.mockResolvedValue({ items: [skillSuite] });
    listSuiteVersions.mockResolvedValue(skillVersions);
    const onSubmit = vi.fn().mockRejectedValueOnce(new Error('响应丢失')).mockResolvedValue(undefined);
    render(<CandidateEvaluationModal open onClose={vi.fn()} onSubmit={onSubmit} resourceKind="skill" />);

    const confirm = screen.getByRole('button', { name: '开始评测' });
    await selectPublishedSuite('投诉基线（v2 · 5 个启用用例）', 'suite-1');
    await waitFor(() => expect(confirm).toBeEnabled());
    fireEvent.click(confirm);
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(confirm).toBeEnabled());
    fireEvent.click(confirm);
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(2));

    expect(onSubmit.mock.calls[0][1]).toEqual(expect.any(String));
    expect(onSubmit.mock.calls[0][1]).toBe(onSubmit.mock.calls[1][1]);
  });

  it('keeps the confirm disabled until a published revision is ready', async () => {
    listSuites.mockResolvedValue({ items: [] });
    render(<CandidateEvaluationModal open onClose={vi.fn()} onSubmit={vi.fn()} resourceKind="skill" />);

    await waitFor(() => expect(listSuites).toHaveBeenCalled());
    expect(screen.getByRole('button', { name: '开始评测' })).toBeDisabled();
  });

  it('reloads suites when the resource kind changes', async () => {
    listSuites.mockImplementation(async ({ resource_kind }: { resource_kind: string }) => ({
      items: resource_kind === 'skill' ? [skillSuite] : [agentSuite],
    }));
    const { rerender } = render(<CandidateEvaluationModal open onClose={vi.fn()}
      onSubmit={vi.fn()} resourceKind="skill" />);
    await waitFor(() => expect(listSuites).toHaveBeenCalledWith({ resource_kind: 'skill' }));

    rerender(<CandidateEvaluationModal open onClose={vi.fn()} onSubmit={vi.fn()} resourceKind="agent" />);
    await waitFor(() => expect(listSuites).toHaveBeenCalledWith({ resource_kind: 'agent' }));
    expect(listSuites).toHaveBeenCalledTimes(2);
  });
});
