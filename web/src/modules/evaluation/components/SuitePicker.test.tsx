import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { SuitePicker, type SuitePick } from './SuitePicker';

const api = vi.hoisted(() => ({ listSuites: vi.fn(), listSuiteVersions: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));
const listSuites = api.listSuites;
const listSuiteVersions = api.listSuiteVersions;

const publishedSuite = {
  id: 'suite-1', name: '投诉基线', description: '', status: 'published', resource_kind: 'skill',
  active_revision_id: 'rev-v2', draft_revision_id: 'rev-draft', active_version_no: 2, active_case_count: 5,
  created_by: 'u1', created_at: '2026-09-01T00:00:00Z',
};
const unpublishedSuite = {
  id: 'suite-2', name: '新退货集', description: '', status: 'draft', resource_kind: 'skill',
  draft_revision_id: 'rev-draft2', draft_case_count: 3,
  created_by: 'u1', created_at: '2026-09-02T00:00:00Z',
};
const publishedVersions = [
  { id: 'rev-v1', version_no: 1, status: 'published', resource_kind: 'skill', enabled_case_count: 4 },
  { id: 'rev-v2', version_no: 2, status: 'published', resource_kind: 'skill', enabled_case_count: 5 },
];

const pickText = () => {
  const raw = screen.getByTestId('pick').textContent;
  return raw ? JSON.parse(raw) : null;
};

const Harness = ({ allowUnpublished, onNeedCreate }: { allowUnpublished?: boolean; onNeedCreate?: () => void }) => {
  const [pick, setPick] = useState<SuitePick | null>(null);
  return <div>
    <SuitePicker resourceKind="skill" value={pick} onChange={setPick} canManage allowUnpublished={allowUnpublished} onNeedCreate={onNeedCreate} />
    <div data-testid="pick">{pick ? JSON.stringify(pick) : ''}</div>
  </div>;
};

const openSuiteSelect = async () => {
  fireEvent.mouseDown(await screen.findByRole('combobox', { name: '评测集' }));
};

describe('SuitePicker', () => {
  beforeEach(() => {
    listSuites.mockReset();
    listSuiteVersions.mockReset();
  });

  it('loads suites for the resource kind and defaults the picked version to the active revision', async () => {
    listSuites.mockResolvedValue({ items: [publishedSuite] });
    listSuiteVersions.mockResolvedValue(publishedVersions);
    render(<Harness />);
    await waitFor(() => expect(listSuites).toHaveBeenCalledWith({ resource_kind: 'skill' }));
    await openSuiteSelect();
    const option = await screen.findByText('投诉基线（v2 · 5 个启用用例）');
    fireEvent.click(option);
    await waitFor(() => expect(listSuiteVersions).toHaveBeenCalledWith('suite-1'));
    await waitFor(() => expect(pickText()).toEqual({ suiteId: 'suite-1', revisionId: 'rev-v2' }));
  });

  it('surfaces unpublished suites only under allowUnpublished and returns suiteId without version fetch', async () => {
    listSuites.mockResolvedValue({ items: [publishedSuite, unpublishedSuite] });
    render(<Harness allowUnpublished />);
    await openSuiteSelect();
    expect(await screen.findByText('未发布草稿套件')).toBeInTheDocument();
    fireEvent.click(await screen.findByText('新退货集（未发布 · 3 个用例）'));
    await waitFor(() => expect(pickText()).toEqual({ suiteId: 'suite-2' }));
    expect(screen.getByText('该评测集尚未发布，运行前会先发布为 v1。')).toBeInTheDocument();
    expect(listSuiteVersions).not.toHaveBeenCalled();
  });

  it('hides unpublished suites when allowUnpublished is off', async () => {
    listSuites.mockResolvedValue({ items: [unpublishedSuite] });
    render(<Harness />);
    await openSuiteSelect();
    expect(screen.queryByText('新退货集（未发布 · 3 个用例）')).not.toBeInTheDocument();
  });

  it('renders the create entry on empty state and forwards onNeedCreate', async () => {
    listSuites.mockResolvedValue({ items: [] });
    const onNeedCreate = vi.fn();
    render(<Harness onNeedCreate={onNeedCreate} />);
    const button = await screen.findByRole('button', { name: '新建评测集' });
    fireEvent.click(button);
    expect(onNeedCreate).toHaveBeenCalledTimes(1);
  });
});
