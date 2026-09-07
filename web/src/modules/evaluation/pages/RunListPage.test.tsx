import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { RunSummary } from '../model/evaluation';

import { RunListPage } from './RunListPage';

// 新建评测动作按租户角色门控（isAdmin），页面经 useCreateEvaluation→useTenantRole 读取。
const role = vi.hoisted(() => ({ isAdmin: true }));
vi.mock('@/modules/iam', () => ({ useTenantRole: () => role }));

const api = vi.hoisted(() => ({ listRuns: vi.fn(), listResources: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

const runs: RunSummary[] = [
  { id: 'run-1', resource_id: 'agent-1', revision_id: 'rev-a', status: 'succeeded', resource_kind: 'agent',
    passed: true, total_cases: 4, passed_cases: 4, created_by: 'u1', created_at: '2026-08-01T00:00:00Z' },
];

const LocationProbe = () => {
  const location = useLocation();
  return <output aria-label="当前路径">{location.pathname}</output>;
};
const path = () => screen.getByRole('status', { name: '当前路径' });

const renderList = () => render(
  <MemoryRouter initialEntries={['/evaluations/runs']}>
    <Routes>
      <Route path="/evaluations/runs" element={<RunListPage />} />
    </Routes>
    <LocationProbe />
  </MemoryRouter>,
);

describe('RunListPage', () => {
  beforeEach(() => {
    role.isAdmin = true;
    api.listRuns.mockReset();
    api.listRuns.mockResolvedValue({ items: runs });
    // 「新建评测」目标下拉取 agent/knowledge 被测资源；用例不开表单即可空态通过。
    api.listResources.mockReset();
    api.listResources.mockResolvedValue({ items: [] });
  });

  it('loads and renders offline run rows with anchors and pass counts', async () => {
    renderList();
    expect(await screen.findByText('agent-1')).toBeInTheDocument();
    expect(screen.getByText('rev-a')).toBeInTheDocument();
    expect(screen.getByText('4 / 4')).toBeInTheDocument();
    await waitFor(() => expect(api.listRuns).toHaveBeenCalledWith(
      expect.objectContaining({ resource_kind: 'agent,knowledge' })));
  });

  it('shows the empty state when no run exists', async () => {
    api.listRuns.mockResolvedValue({ items: [] });
    renderList();
    expect(await screen.findByText('离线运行记录还是空的')).toBeInTheDocument();
  });

  it('navigates a run row to its detail route for inspection', async () => {
    renderList();
    fireEvent.click(await screen.findByRole('button', { name: '详情' }));
    await waitFor(() => expect(path()).toHaveTextContent('/evaluations/runs/run-1'));
    // 详情跳页展示体取数由 RunDetailPage 承担，列表自身不再触发 getRun。
    expect(api.listRuns).toHaveBeenCalledTimes(1);
  });

  it('opens the create-evaluation modal from the admin header action', async () => {
    renderList();
    fireEvent.click(await screen.findByRole('button', { name: '新建评测' }));
    // 新建评测就地弹框，不再跳 hub。
    expect(screen.getByRole('dialog', { name: '新建评测' })).toBeInTheDocument();
  });

  it('keeps the create action hidden from members', async () => {
    role.isAdmin = false;
    renderList();
    await screen.findByText('agent-1');
    expect(screen.queryByRole('button', { name: '新建评测' })).not.toBeInTheDocument();
  });
});
