import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ResourceDetailPage } from './ResourceDetailPage';

const role = vi.hoisted(() => ({ isAdmin: true }));
vi.mock('@/modules/iam', () => ({ useTenantRole: () => role }));

const api = vi.hoisted(() => ({
  listResources: vi.fn(),
  listRuns: vi.fn(),
  listCandidates: vi.fn(),
  listExperiments: vi.fn(),
  getTimeline: vi.fn(),
  listAgents: vi.fn(),
  listWorkspaces: vi.fn(),
}));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));
// 未建档 CTA 就地弹 RegisterResourceModal，打开时按类型拉取候选，mock 隔离真实模块。
vi.mock('@/modules/agent/api/agent.api', () => ({ agentApi: { list: api.listAgents } }));
vi.mock('@/modules/knowledge/api/knowledge.api', () => ({ knowledgeApi: { list: api.listWorkspaces } }));

const skillRow = {
  id: 'row-1', resource_id: 'skill-1', status: 'active', stable_revision_id: 'v1',
  latest_run_status: 'succeeded', resource_kind: 'skill', safe_summary: { name: '客服技能' },
  created_at: '2026-07-23T00:00:00Z',
};
const runV1 = {
  id: 'run-1', resource_id: 'skill-1', revision_id: 'v1', status: 'succeeded', resource_kind: 'skill',
  passed: true, total_cases: 4, passed_cases: 4, created_at: '2026-07-23T00:00:00Z',
};
const runV2 = {
  id: 'run-2', resource_id: 'skill-1', revision_id: 'v2', status: 'succeeded', resource_kind: 'skill',
  passed: false, total_cases: 4, passed_cases: 2, created_at: '2026-07-23T02:00:00Z',
};
const candidate = {
  id: 'cand-1', resource_id: 'skill-1', revision_id: 'canary-1', parent_revision_id: 'v1', source: 'optimize',
  status: 'proposed', resource_kind: 'skill', state_version: 1,
  safe_diff: { changed_fields: [], changes: {}, parent_missing: false }, created_at: '2026-07-23T00:00:00Z',
};
const experiment = {
  id: 'exp-1', resource_id: 'skill-1', stable_revision_id: 'v1', canary_revision_id: 'canary-1',
  status: 'running', recommendation: 'promote', resource_kind: 'skill', stage_percent: 25,
  safety_stopped: false, state_version: 1,
  gates: { quality: 'passed', cost: 'passed', latency: 'pending', error_rate: 'passed', security: 'passed' },
  promotion_evidence: { eligible: false, blockers: [],
    gates: { quality: 'passed', cost: 'passed', latency: 'pending', error_rate: 'passed', security: 'passed' } },
  created_at: '2026-07-23T00:00:00Z',
};
const timelineEvent = {
  id: 'evt-1', kind: 'evaluation_run', status: 'passed', summary: '运行 run-1 通过', resource_id: 'skill-1',
  resource_kind: 'skill', created_at: '2026-07-23T00:00:00Z',
};

const LocationProbe = () => {
  const location = useLocation();
  return <>
    <output aria-label="当前查询参数">{location.search}</output>
    <output aria-label="当前路径">{location.pathname}</output>
  </>;
};
const path = () => screen.getByRole('status', { name: '当前路径' });
const query = () => screen.getByRole('status', { name: '当前查询参数' });

const renderDetail = (entry: string) => render(
  <MemoryRouter initialEntries={[entry]}>
    <Routes>
      <Route path="/evaluations/resources/:kind/:id" element={<ResourceDetailPage />} />
    </Routes>
    <LocationProbe />
  </MemoryRouter>,
);

describe('ResourceDetailPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    role.isAdmin = true;
    api.listResources.mockResolvedValue({ items: [] });
    api.listRuns.mockResolvedValue({ items: [] });
    api.listCandidates.mockResolvedValue({ items: [] });
    api.listExperiments.mockResolvedValue({ items: [] });
    api.getTimeline.mockResolvedValue({ items: [] });
    api.listAgents.mockResolvedValue([]);
    api.listWorkspaces.mockResolvedValue([]);
  });

  it('composes header, regression, timeline and candidate sections for a registered resource', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.getTimeline.mockResolvedValue({ items: [timelineEvent] });
    renderDetail('/evaluations/resources/skill/skill-1');

    expect(await screen.findByText('skill-1')).toBeInTheDocument();
    expect(screen.getByText('技能')).toBeInTheDocument();
    expect(screen.getByText('进行中')).toBeInTheDocument();
    expect(screen.getByText('稳定版本 v1')).toBeInTheDocument();
    expect(screen.getByText('运行与回归')).toBeInTheDocument();
    expect(screen.getByText('版本时间线')).toBeInTheDocument();
    expect(screen.getByText('候选与实验')).toBeInTheDocument();
    // 内嵌 TimelinePanel 渲染时间线事件。
    expect(await screen.findByText('运行 run-1 通过')).toBeInTheDocument();
  });

  it('filters the regression runs by an anchored revision through the server revision_id contract', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    // 上游并集先返回双版本；选中版本后按 revision_id 重取该版本权威行。
    api.listRuns.mockImplementation(async (params?: { revision_id?: string }) => ({
      items: params?.revision_id
        ? [runV1, runV2].filter((run) => run.revision_id === params.revision_id)
        : [runV1, runV2],
    }));
    renderDetail('/evaluations/resources/skill/skill-1');

    expect(await screen.findAllByRole('button', { name: '详情' })).toHaveLength(2);
    expect(screen.getByTestId('health-trend-chart')).toBeInTheDocument();

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '版本过滤' }));
    const option = await waitFor(() => {
      const item = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option-content'))
        .find((value) => value.textContent === 'v2');
      expect(item).toBeDefined();
      return item!;
    });
    fireEvent.click(option);
    // 后端 (b)：选中版本以 revision_id 服务端过滤，而非在首屏并集内做内存收窄。
    await waitFor(() => expect(api.listRuns).toHaveBeenCalledWith(
      expect.objectContaining({ revision_id: 'v2' })));
    await waitFor(() => expect(screen.getAllByRole('button', { name: '详情' })).toHaveLength(1));
  });

  it('navigates a run row to the run detail route', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.listRuns.mockResolvedValue({ items: [runV1] });
    renderDetail('/evaluations/resources/skill/skill-1');
    fireEvent.click(await screen.findByRole('button', { name: '详情' }));
    await waitFor(() => expect(path()).toHaveTextContent('/evaluations/runs/run-1'));
  });

  it('lists candidates and experiments and links to the evolution workspace', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.listCandidates.mockResolvedValue({ items: [candidate] });
    api.listExperiments.mockResolvedValue({ items: [experiment] });
    renderDetail('/evaluations/resources/skill/skill-1');

    expect(await screen.findByText('候选版本（1）')).toBeInTheDocument();
    expect(screen.getByText('金丝雀实验（1）')).toBeInTheDocument();
    expect(screen.getAllByText('canary-1').length).toBeGreaterThan(0);
    expect(screen.getByText('25%')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '前往自进化工作区' }));
    await waitFor(() => expect(path()).toHaveTextContent('/evaluations/evolution'));
  });

  it('offers admin a register CTA that opens a local modal for an unregistered agent', async () => {
    renderDetail('/evaluations/resources/agent/agent-9');
    expect(await screen.findByText(/尚未在评测中心建档/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '登记该资源' }));
    // 就地弹窗登记，不再跳 hub：URL 保持资源详情且无 ?action= 状态残留。
    await waitFor(() => expect(api.listAgents).toHaveBeenCalled());
    expect(screen.getByRole('dialog', { name: '登记被测资源' })).toBeInTheDocument();
    expect(path()).toHaveTextContent('/evaluations/resources/agent/agent-9');
    expect(query()).not.toHaveTextContent('action=register');
  });

  it('keeps the register CTA and command hint hidden from members', async () => {
    role.isAdmin = false;
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.listCandidates.mockResolvedValue({ items: [candidate] });
    renderDetail('/evaluations/resources/skill/skill-1');
    expect(await screen.findByText('候选版本（1）')).toBeInTheDocument();
    expect(screen.queryByText(/管理员可在自进化工作区/)).not.toBeInTheDocument();
  });

  it('renders a historical-only notice without register for unregistered skill/mcp kinds', async () => {
    renderDetail('/evaluations/resources/skill/legacy-skill');
    expect(await screen.findByText(/尚未在评测中心建档/)).toBeInTheDocument();
    expect(screen.getByText(/历史只读类型/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '登记该资源' })).not.toBeInTheDocument();
  });

  it('opens the product module page for a registered resource', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    renderDetail('/evaluations/resources/skill/skill-1');
    fireEvent.click(await screen.findByRole('button', { name: /打开技能工作台/ }));
    await waitFor(() => expect(path()).toHaveTextContent('/skills/skill-1/workspace'));
  });

  it('guards an unknown resource kind without fetching', async () => {
    renderDetail('/evaluations/resources/alien/x');
    expect(await screen.findByText('未知的被测资源类型')).toBeInTheDocument();
    expect(api.listResources).not.toHaveBeenCalled();
  });

  it('surfaces a load error with a retry action', async () => {
    api.listResources.mockRejectedValue(new Error('加载失败：boom'));
    renderDetail('/evaluations/resources/skill/skill-1');
    expect(await screen.findByText('加载失败：boom')).toBeInTheDocument();
    // AntD 双字默认按钮会插入空格，「重试」实为「重 试」。
    expect(screen.getByRole('button', { name: /^重\s*试$/ })).toBeInTheDocument();
  });
});
