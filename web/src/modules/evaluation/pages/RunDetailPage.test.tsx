import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { RunDetailPage } from './RunDetailPage';

const api = vi.hoisted(() => ({ getRun: vi.fn(), listRuns: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

// getRun 详情：EvaluationRun 里 resource 携带资源身份，供详情页 listRuns 回归行。
const runDetail = {
  id: 'run-1',
  resource: { kind: 'agent', resource_id: 'agent-1', revision_id: 'rev-a' },
  suite_revision_id: 'suite-rev-1',
  anchors: [{ kind: 'skill', resource_id: 'skill-1', revision_id: 'sk-v1' }],
  passed: true,
  total_cases: 4,
  passed_cases: 4,
  metrics: { total_tokens: 1200 },
  results: [],
};
const runRow = {
  id: 'run-1', resource_id: 'agent-1', revision_id: 'rev-a', status: 'succeeded', resource_kind: 'agent',
  passed: true, total_cases: 4, passed_cases: 4, created_by: 'u1', created_at: '2026-08-01T00:00:00Z',
};
const otherRow = {
  id: 'run-2', resource_id: 'agent-1', revision_id: 'rev-b', status: 'failed', resource_kind: 'agent',
  passed: false, total_cases: 4, passed_cases: 1, created_by: 'u1', created_at: '2026-08-02T00:00:00Z',
};

const LocationProbe = () => {
  const location = useLocation();
  return <output aria-label="当前路径">{location.pathname}</output>;
};
const path = () => screen.getByRole('status', { name: '当前路径' });

const renderDetail = (runId: string) => render(
  <MemoryRouter initialEntries={[`/evaluations/runs/${runId}`]}>
    <Routes>
      <Route path="/evaluations/runs/:runId" element={<RunDetailPage />} />
      {/* 返回按钮的落点，避免 "No routes matched" 噪音。 */}
      <Route path="/evaluations/runs" element={<div>运行列表占位</div>} />
    </Routes>
    <LocationProbe />
  </MemoryRouter>,
);

describe('RunDetailPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.getRun.mockResolvedValue(runDetail);
    api.listRuns.mockResolvedValue({ items: [runRow, otherRow] });
  });

  it('composes the run header and facts for an existing run', async () => {
    renderDetail('run-1');
    expect(await screen.findByText('agent-1 · 锚定版本 rev-a')).toBeInTheDocument();
    expect(screen.getByText('Agent')).toBeInTheDocument();
    expect(screen.getByText('运行详情')).toBeInTheDocument();
    // 按该运行所属资源取回归行，供版本对比使用。
    await waitFor(() => expect(api.listRuns).toHaveBeenCalledWith(
      { resource_kind: 'agent', resource_id: 'agent-1' }));
    // 展示体默认落在「观测事实」Tab（Tab 标题与正文各一次）。
    expect((await screen.findAllByText('观测事实')).length).toBeGreaterThanOrEqual(1);
  });

  it('falls back to an empty state for a run outside the recent list window', async () => {
    api.listRuns.mockResolvedValue({ items: [otherRow] });
    renderDetail('run-1');
    expect(await screen.findByText(/未找到该运行记录/)).toBeInTheDocument();
  });

  it('surfaces a load error and recovers via retry', async () => {
    api.getRun.mockRejectedValueOnce(new Error('加载失败：boom'));
    renderDetail('run-1');
    expect(await screen.findByText('加载失败：boom')).toBeInTheDocument();
    // AntD 双字默认按钮会插入空格，「重试」实为「重 试」。
    fireEvent.click(screen.getByRole('button', { name: /^重\s*试$/ }));
    expect(await screen.findByText('agent-1 · 锚定版本 rev-a')).toBeInTheDocument();
  });

  it('navigates back to the run list', async () => {
    renderDetail('run-1');
    // 返回按钮带 ArrowLeftOutlined 图标，access name 可能拼入图标 aria-label，按子串匹配。
    fireEvent.click(await screen.findByRole('button', { name: /返回运行列表/ }));
    await waitFor(() => expect(path()).toHaveTextContent('/evaluations/runs'));
  });
});
