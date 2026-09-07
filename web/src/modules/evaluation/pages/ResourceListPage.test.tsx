import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { ResourceSummary } from '../model/evaluation';

import { ResourceListPage } from './ResourceListPage';

// 「登记被测资源」与进详情按钮按租户角色门控（isAdmin）。
const role = vi.hoisted(() => ({ isAdmin: true }));
vi.mock('@/modules/iam', () => ({ useTenantRole: () => role }));

const api = vi.hoisted(() => ({
  listResources: vi.fn(),
  getTimeline: vi.fn(),
  listAgents: vi.fn(),
  listWorkspaces: vi.fn(),
}));
vi.mock('../api/evaluation.api', () => ({
  evaluationApi: { listResources: api.listResources, getTimeline: api.getTimeline },
}));
// RegisterResourceModal 打开时会按类型拉取可登记候选，mock 以隔离真实模块请求。
vi.mock('@/modules/agent/api/agent.api', () => ({ agentApi: { list: api.listAgents } }));
vi.mock('@/modules/knowledge/api/knowledge.api', () => ({ knowledgeApi: { list: api.listWorkspaces } }));

const resources: ResourceSummary[] = [{
  id: 'r1', resource_id: 'agent-1', status: 'active', stable_revision_id: 'rev-a', resource_kind: 'agent',
  latest_run_status: 'succeeded', safe_summary: { name: '客服 Agent' }, created_at: '2026-08-01T00:00:00Z',
}];

const LocationProbe = () => {
  const location = useLocation();
  return <output aria-label="当前路径">{location.pathname}</output>;
};
const path = () => screen.getByRole('status', { name: '当前路径' });

const renderList = () => render(
  <MemoryRouter initialEntries={['/evaluations/resources']}>
    <Routes>
      <Route path="/evaluations/resources" element={<ResourceListPage />} />
    </Routes>
    <LocationProbe />
  </MemoryRouter>,
);

describe('ResourceListPage', () => {
  beforeEach(() => {
    role.isAdmin = true;
    api.listResources.mockReset();
    api.listResources.mockResolvedValue({ items: resources });
    api.getTimeline.mockReset();
    api.getTimeline.mockResolvedValue({ items: [] });
    api.listAgents.mockReset();
    api.listAgents.mockResolvedValue([]);
    api.listWorkspaces.mockReset();
    api.listWorkspaces.mockResolvedValue([]);
  });

  it('loads and renders registered resource rows', async () => {
    renderList();
    expect(await screen.findByText('客服 Agent')).toBeInTheDocument();
    expect(screen.getByText('agent-1')).toBeInTheDocument();
  });

  it('shows the empty state when nothing is registered', async () => {
    api.listResources.mockResolvedValue({ items: [] });
    renderList();
    expect(await screen.findByText('评测资源还是空的')).toBeInTheDocument();
  });

  it('opens the evidence timeline for a resource row', async () => {
    renderList();
    await screen.findByText('客服 Agent');
    fireEvent.click(screen.getByRole('button', { name: '查看 agent-1 时间线' }));
    await waitFor(() => expect(api.getTimeline).toHaveBeenCalledWith('agent', 'agent-1'));
  });

  it('routes a resource row to its evidence detail page', async () => {
    renderList();
    await screen.findByText('客服 Agent');
    fireEvent.click(screen.getByRole('button', { name: '查看 agent-1 详情' }));
    await waitFor(() => expect(path()).toHaveTextContent('/evaluations/resources/agent/agent-1'));
  });

  it('opens the register-resource modal from the admin header action', async () => {
    renderList();
    fireEvent.click(await screen.findByRole('button', { name: '登记被测资源' }));
    // 登记被测资源就地弹框；默认类型 agent 触发候选加载。
    await waitFor(() => expect(api.listAgents).toHaveBeenCalled());
    expect(screen.getByRole('dialog', { name: '登记被测资源' })).toBeInTheDocument();
  });

  it('keeps the register action hidden from members', async () => {
    role.isAdmin = false;
    renderList();
    await screen.findByText('客服 Agent');
    expect(screen.queryByRole('button', { name: '登记被测资源' })).not.toBeInTheDocument();
  });
});
