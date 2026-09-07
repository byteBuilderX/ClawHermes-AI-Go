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
  listRevisions: vi.fn(),
  listRevisionReferences: vi.fn(),
  getRevisionPassRate: vi.fn(),
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
// 版本引用账本 fixtures：revision 行携带产品版本对照 version_label（建档时写入
// resource_revisions.safe_summary）；references 覆盖 deployment/subject/pinned/candidate/
// experiment 四类引用，供抽屉测试校验。
const revisionV1 = {
  id: 'v1', resource_id: 'skill-1', resource_kind: 'skill', source: 'register',
  status: 'active', safe_summary: { version_label: '1.0.0' }, created_at: '2026-07-23T00:00:00Z',
};
const revisionV2 = {
  id: 'v2', resource_id: 'skill-1', resource_kind: 'skill', source: 'optimize',
  status: 'active', safe_summary: { version_label: '1.1.0' }, created_at: '2026-07-23T02:00:00Z',
};
const referencesData = {
  deployment: { role: 'stable', stable_revision_id: 'v1', canary_percent: 0 },
  subject_runs: [runV1],
  pinned_runs: [{
    run_id: 'run-pin', resource_kind: 'agent', resource_id: 'agent-5', resource_name: '线上客服A',
    status: 'succeeded', passed: true, total_cases: 3, passed_cases: 3, created_at: '2026-07-23T01:00:00Z',
  }],
  candidates: [{
    id: 'cand-ref', revision_id: 'v1', parent_revision_id: 'v0', role: 'candidate',
    source: 'optimize', status: 'proposed', created_at: '2026-07-23T01:00:00Z',
  }],
  experiments: [{
    id: 'exp-ref', role: 'stable', stable_revision_id: 'v1', canary_revision_id: 'v2',
    status: 'running', stage_percent: 25, recommendation: 'promote', created_at: '2026-07-23T01:00:00Z',
  }],
};
const emptyReferences = { deployment: null, subject_runs: [], pinned_runs: [], candidates: [], experiments: [] };
const passRateData = {
  succeeded_runs: 2, total_runs: 3, passed_cases: 6, total_cases: 9, pass_rate: 0.6666667,
  recent_runs: [runV1, runV2],
};
const emptyPassRate = {
  succeeded_runs: 0, total_runs: 0, passed_cases: 0, total_cases: 0, pass_rate: null, recent_runs: [],
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
    api.listRevisions.mockResolvedValue({ items: [] });
    api.listRevisionReferences.mockResolvedValue(emptyReferences);
    api.getRevisionPassRate.mockResolvedValue(emptyPassRate);
    api.listAgents.mockResolvedValue([]);
    api.listWorkspaces.mockResolvedValue([]);
  });

  it('composes header, regression, timeline and candidate sections for a registered resource', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.getTimeline.mockResolvedValue({ items: [timelineEvent] });
    renderDetail('/evaluations/resources/skill/skill-1');

    expect(await screen.findByText('客服技能')).toBeInTheDocument();
    // 页头主文案是真实名称，裸资源 id 降级为弱化 code 供核对。
    expect(screen.getByText('skill-1')).toBeInTheDocument();
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

  it('lists the version ledger with product version labels and marks the current stable', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.listRevisions.mockResolvedValue({ items: [revisionV1, revisionV2] });
    renderDetail('/evaluations/resources/skill/skill-1');

    // 账本按 listRevisions 装载；产品版本列以 version_label 为主文案，id 弱化次要行。
    expect(await screen.findByText('1.0.0')).toBeInTheDocument();
    expect(screen.getByText('1.1.0')).toBeInTheDocument();
    expect(api.listRevisions).toHaveBeenCalledWith('skill', 'skill-1');
    // 当前稳定版本行打「当前稳定」标，非稳定行不误导为占位 —。
    // 当前稳定版本行打「当前稳定」标（cell role 避开同名列标题 th）；非稳定行不误导为占位 —。
    expect(screen.getByRole('cell', { name: '当前稳定' })).toBeInTheDocument();
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
    // 每行都提供「引用分析」入口；头部分析按钮作用于当前稳定版本。
    expect(screen.getAllByRole('button', { name: '引用分析' }).length).toBeGreaterThanOrEqual(2);
  });

  it('shows an honest empty ledger for a registered resource without eval revisions', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    renderDetail('/evaluations/resources/skill/skill-1');
    expect(await screen.findByText('该资源还没有评测版本（建档后逐版本记录评估证据）')).toBeInTheDocument();
  });

  it('opens the revision ledger drawer and shows deployment, pass-rate and grouped references', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.listRevisions.mockResolvedValue({ items: [revisionV1, revisionV2] });
    api.listRevisionReferences.mockResolvedValue(referencesData);
    api.getRevisionPassRate.mockResolvedValue(passRateData);
    renderDetail('/evaluations/resources/skill/skill-1');

    // 先等账本装载，保证头部「引用分析」作用于当前稳定版本 v1 的产品标签 1.0.0。
    expect(await screen.findByText('1.0.0')).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole('button', { name: '引用分析' })[0]);

    expect(await screen.findByText(/版本 1.0.0 引用分析/)).toBeInTheDocument();
    expect(screen.getByText('主体运行 1')).toBeInTheDocument();
    expect(screen.getByText('绑定引用 1')).toBeInTheDocument();
    expect(screen.getByText('优化候选 1')).toBeInTheDocument();
    expect(screen.getByText('金丝雀实验 1')).toBeInTheDocument();
    // 该版本正是当前部署稳定版本 → 部署标签打「本版本」，否则诚实标注未参与。
    expect(screen.getByText('稳定 v1（本版本）')).toBeInTheDocument();
    // 通过率摘要：(d) pass_rate 成功 run 用例聚合，mini 折线复用最近 run。
    expect(screen.getByText('66.7%')).toBeInTheDocument();
    expect(screen.getByText(/成功 run 2 \/ 共 3/)).toBeInTheDocument();
    expect(screen.getByText('最近运行通过率')).toBeInTheDocument();
    // 引用明细：主体 run → 运行详情页；candidate/experiment 提供自进化工作区入口。
    expect(screen.getByText('线上客服A')).toBeInTheDocument();
    expect(screen.getByText('前往自进化工作区查看命令记录')).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole('button', { name: '详情' })[0]);
    await waitFor(() => expect(path()).toHaveTextContent('/evaluations/runs/run-1'));
    expect(api.listRevisionReferences).toHaveBeenCalledWith('skill', 'skill-1', 'v1');
    expect(api.getRevisionPassRate).toHaveBeenCalledWith('skill', 'skill-1', 'v1');
  });

  it('surfaces a ledger load error with a retry action', async () => {
    api.listResources.mockResolvedValue({ items: [skillRow] });
    api.listRevisions.mockRejectedValue(new Error('boom'));
    renderDetail('/evaluations/resources/skill/skill-1');
    expect(await screen.findByText(/加载版本账本失败/)).toBeInTheDocument();
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
