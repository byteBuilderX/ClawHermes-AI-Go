import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, useLocation, useNavigate } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { CandidateSummary, ExperimentSummary, RunSummary, SuiteSummary } from '../model/evaluation';

import { EvaluationCenterPage } from './EvaluationCenterPage';

const runFixture: RunSummary = {
  id: 'run-1', resource_id: 'agent-1', revision_id: 'agent-v1', status: 'succeeded', resource_kind: 'agent',
  passed: true, total_cases: 4, passed_cases: 4, created_at: '2026-07-23T00:00:00Z',
};
const candidateFixture: CandidateSummary = {
  id: 'cand-1', resource_id: 'agent-1', revision_id: 'canary-1', parent_revision_id: 'agent-v1', source: 'optimize',
  status: 'proposed', resource_kind: 'agent', state_version: 1,
  safe_diff: { changed_fields: [], changes: {}, parent_missing: false }, created_at: '2026-07-23T00:00:00Z',
};
const center = vi.hoisted(() => ({
  overview: { resources: 1, suites: 2, runs: 3, candidates: 1, experiments: 1 },
  resources: { items: [
    { id: 'r1', resource_id: 'skill-1', resource_kind: 'skill', status: 'active', stable_revision_id: 'v1',
      latest_run_status: 'succeeded', safe_summary: { name: '客服技能' }, created_at: '2026-07-23T00:00:00Z' },
    { id: 'r2', resource_id: 'agent-1', resource_kind: 'agent', status: 'active', stable_revision_id: 'agent-v1',
      latest_run_status: 'succeeded', safe_summary: { name: '客服 Agent' }, created_at: '2026-07-23T00:00:00Z' },
    { id: 'r3', resource_id: 'mcp-1', resource_kind: 'mcp', status: 'active', stable_revision_id: 'mcp-v1',
      safe_summary: { name: '检索 MCP' }, created_at: '2026-07-23T00:00:00Z' },
    { id: 'r4', resource_id: 'knowledge-1', resource_kind: 'knowledge', status: 'active', stable_revision_id: 'knowledge-v1',
      safe_summary: { name: '产品知识库' }, created_at: '2026-07-23T00:00:00Z' },
  ] },
  suites: { items: [] as SuiteSummary[] },
  runs: { items: [] as RunSummary[] }, candidates: { items: [] as CandidateSummary[] }, experiments: { items: [] as ExperimentSummary[] },
  loading: false, error: '', canManageEvaluation: true, reload: vi.fn(), rejectCandidate: vi.fn(),
  pauseExperiment: vi.fn(), promoteExperiment: vi.fn(), rollbackExperiment: vi.fn(), createEvaluation: vi.fn(),
  canDeleteEntity: vi.fn(() => true), deleteSuite: vi.fn(), deleteRun: vi.fn(), deleteJob: vi.fn(),
  deleteCandidate: vi.fn(), deleteExperiment: vi.fn(), deleteReviewItem: vi.fn(), deleteFeedback: vi.fn(),
}));
const useCenter = vi.hoisted(() => vi.fn(() => center));
vi.mock('../hooks/useEvaluationCenter', () => ({ useEvaluationCenter: useCenter }));
// 组件创建/操作后调用 message.success/error,antd 的 rc-notification 定时器
// (duration 2-3s)会在测试 teardown 后触发 setState → "window is not defined",
// 造成 vitest 计 1 error 的偶发失败。mock 掉 message,避免真实 notification 定时器。
const messageMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock('antd', async () => ({
  ...(await vi.importActual<typeof import('antd')>('antd')),
  message: { success: messageMocks.success, error: messageMocks.error },
}));
// RegisterResourceModal 打开时会拉取可登记对象：agent=agentApi.list()（/agents），
// knowledge=knowledgeApi.list()（/knowledge/workspaces）。登记流程的候选数据由组件自身
// 单测覆盖；本页测试只需空响应，避免打开登记框触发真实 axios。spread actual 保留模块
// 其余导出，防止其它渲染路径引用缺失字段。
const agentList = vi.hoisted(() => vi.fn(async () => []));
vi.mock('@/modules/agent/api/agent.api', async () => {
  const actual = await vi.importActual<typeof import('@/modules/agent/api/agent.api')>('@/modules/agent/api/agent.api');
  return { ...actual, agentApi: { ...actual.agentApi, list: agentList } };
});
const knowledgeList = vi.hoisted(() => vi.fn(async () => []));
vi.mock('@/modules/knowledge/api/knowledge.api', async () => {
  const actual = await vi.importActual<typeof import('@/modules/knowledge/api/knowledge.api')>('@/modules/knowledge/api/knowledge.api');
  return { ...actual, knowledgeApi: { ...actual.knowledgeApi, list: knowledgeList } };
});

const LocationProbe = () => {
  const location = useLocation();
  const navigate = useNavigate();
  return <>
    <output aria-label="当前查询参数">{location.search}</output>
    <output aria-label="当前路径">{location.pathname}</output>
    <button type="button" onClick={() => navigate(-1)}>返回</button>
  </>;
};

const renderPage = (entry = '/evaluations') => {
  render(
    <MemoryRouter initialEntries={[entry]}>
      <EvaluationCenterPage />
      <LocationProbe />
    </MemoryRouter>,
  );
};
const path = () => screen.getByRole('status', { name: '当前路径' });
const query = () => screen.getByRole('status', { name: '当前查询参数' });

describe('EvaluationCenterPage', () => {
  beforeEach(() => {
    center.canManageEvaluation = true;
    center.suites.items = [];
    center.runs.items = [];
    center.candidates.items = [];
    center.experiments.items = [];
    useCenter.mockClear();
  });

  it('exposes filters, the register entry and the record-book headings for admins', () => {
    renderPage();
    expect(screen.getByRole('combobox', { name: '资源类型' })).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: '资源状态' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /新建评测/ })).toBeInTheDocument();
    // 被测收敛后统一建档入口落在评测中心（skill 工作台入口已移除）。
    expect(screen.getByRole('button', { name: '登记被测资源' })).toBeInTheDocument();
    expect(screen.queryByRole('tab')).not.toBeInTheDocument();
  });

  it('keeps new evaluation and the register entry hidden for members while quick links remain', () => {
    center.canManageEvaluation = false;
    renderPage();
    expect(screen.queryByRole('button', { name: /新建评测/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '登记被测资源' })).not.toBeInTheDocument();
    // 只读成员同样能经由记录簿直达各链路页。
    expect(screen.getByRole('button', { name: '离线运行' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '人工评审池' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '查看 skill-1' })).toBeInTheDocument();
  });

  it('navigates to the resource detail page when a summary row is opened', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: '查看 skill-1' }));
    expect(path()).toHaveTextContent('/evaluations/resources/skill/skill-1');
  });

  it('navigates each quick link to its dedicated page', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: '离线运行' }));
    expect(path()).toHaveTextContent('/evaluations/runs');
    fireEvent.click(screen.getByRole('button', { name: '返回' }));
    fireEvent.click(screen.getByRole('button', { name: '自进化工作区' }));
    expect(path()).toHaveTextContent('/evaluations/evolution');
    fireEvent.click(screen.getByRole('button', { name: '返回' }));
    fireEvent.click(screen.getByRole('button', { name: '在线观测' }));
    expect(path()).toHaveTextContent('/evaluations/observability');
  });

  it('routes suite management through the dedicated suites page (hub keeps a read-only entry)', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: '评测集' }));
    expect(path()).toHaveTextContent('/evaluations/suites');
  });

  it('navigates a run feed entry to the run detail page and candidate entries to the evolution page', () => {
    center.runs.items = [runFixture];
    center.candidates.items = [candidateFixture];
    renderPage();
    expect(screen.getByText(/run-1/)).toBeInTheDocument();
    expect(screen.getByText(/canary-1/)).toBeInTheDocument();
    // 首行记录为 run（相同时间戳下运行先于候选入并），其「查看」直达运行详情。
    fireEvent.click(screen.getAllByRole('button', { name: '查看' })[0]);
    expect(path()).toHaveTextContent('/evaluations/runs/run-1');
  });

  it('shows the feed empty state when there are no records yet', () => {
    renderPage();
    expect(screen.getByText('还没有评测活动记录')).toBeInTheDocument();
  });

  it('creates an inline suite under create mode and forwards the plan to the center hook', async () => {
    center.createEvaluation.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /新建评测/ }));
    fireEvent.click(screen.getByRole('radio', { name: '新建评测集' }));
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '目标资源' }));
    // 被测收敛后「新建评测」目标资源仅 agent/knowledge：skill/mcp 已退出建档不可再发起。
    expect(await screen.findByText('客服 Agent（agent-1）')).toBeInTheDocument();
    expect(screen.getByText('产品知识库（knowledge-1）')).toBeInTheDocument();
    expect(screen.queryByText('检索 MCP（mcp-1）')).not.toBeInTheDocument();
    fireEvent.click(screen.getByText('客服 Agent（agent-1）'));
    fireEvent.change(screen.getByLabelText('评测集名称'), { target: { value: '客服基线评测' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '标准问候' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '你好' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '您好' } });
    fireEvent.click(screen.getByRole('button', { name: '创建并运行' }));
    await waitFor(() => expect(center.createEvaluation).toHaveBeenCalledWith(expect.objectContaining({
      mode: 'create',
      resource: expect.objectContaining({ kind: 'agent', resource_id: 'agent-1', revision_id: 'agent-v1' }),
      name: '客服基线评测',
      cases: [expect.objectContaining({ tool_spec: undefined, step_judge: undefined })],
    })));
  });

  it('maps tool_spec and step_judge onto the created case when process fields are set', async () => {
    center.createEvaluation.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /新建评测/ }));
    fireEvent.click(screen.getByRole('radio', { name: '新建评测集' }));
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '目标资源' }));
    fireEvent.click(await screen.findByText('客服 Agent（agent-1）'));
    fireEvent.change(screen.getByLabelText('评测集名称'), { target: { value: '工具链路评测' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '查天气' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '北京天气' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '晴天' } });
    const mustCall = screen.getByRole('combobox', { name: '必调用工具' });
    fireEvent.mouseDown(mustCall);
    fireEvent.change(mustCall, { target: { value: 'weather' } });
    fireEvent.keyDown(mustCall, { key: 'Enter', code: 'Enter', keyCode: 13 });
    fireEvent.change(screen.getByLabelText('步骤判定标准'), { target: { value: '逐步评分' } });
    fireEvent.click(screen.getByRole('button', { name: '创建并运行' }));
    await waitFor(() => expect(center.createEvaluation).toHaveBeenCalledWith(expect.objectContaining({
      mode: 'create',
      resource: expect.objectContaining({ kind: 'agent', resource_id: 'agent-1' }),
      cases: [expect.objectContaining({
        tool_spec: { must_call: ['weather'] },
        step_judge: { criteria: '逐步评分' },
      })],
    })));
  });

  it('initializes the center from a valid resource deep link', () => {
    renderPage('/evaluations?kind=skill&resource_id=skill-1');
    expect(useCenter).toHaveBeenLastCalledWith({ resource_kind: 'skill', resource_id: 'skill-1', status: undefined });
  });

  it('defaults to the agent+knowledge two tracks when no supported kind is in effect', () => {
    renderPage('/evaluations');
    expect(useCenter).toHaveBeenLastCalledWith({ resource_kind: 'agent,knowledge', resource_id: undefined, status: undefined });
  });

  it('falls back to the agent+knowledge two tracks for an unsupported kind without dropping the resource id', () => {
    renderPage('/evaluations?kind=workflow&resource_id=resource-1');
    expect(useCenter).toHaveBeenLastCalledWith({ resource_kind: 'agent,knowledge', resource_id: 'resource-1', status: undefined });
  });

  it('keeps the resource deep link while changing kind and follows history navigation', async () => {
    renderPage('/evaluations?kind=skill&resource_id=skill-1&view=evidence');
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '资源类型' }));
    const option = await waitFor(() => {
      const item = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option-content'))
        .find((value) => value.textContent === 'Agent');
      expect(item).toBeDefined();
      return item!;
    });
    fireEvent.click(option);
    await waitFor(() => expect(query()).toHaveTextContent('?kind=agent&resource_id=skill-1&view=evidence'));
    expect(useCenter).toHaveBeenLastCalledWith({ resource_kind: 'agent', resource_id: 'skill-1', status: undefined });

    fireEvent.click(screen.getByRole('button', { name: '返回' }));
    await waitFor(() => expect(useCenter).toHaveBeenLastCalledWith({
      resource_kind: 'skill', resource_id: 'skill-1', status: undefined,
    }));
  });

  it('opens the register modal from the toolbar with agent as the default kind', async () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: '登记被测资源' }));
    // 登记框在页面 Tag（Agent）外独立渲染，用 dialog 限定作用域断言类型默认 agent。
    const dialog = await screen.findByRole('dialog', { name: '登记被测资源' });
    expect(within(dialog).getByText('Agent')).toBeInTheDocument();
    // 页面注入 onRegisterThenRun → 出现「登记并新建评测」快捷。
    expect(within(dialog).getByRole('button', { name: '登记并新建评测' })).toBeInTheDocument();
    // AntD 二字按钮自动插空格：主操作「登记」实为「登 记」。
    expect(within(dialog).getByRole('button', { name: /^登\s*记$/ })).toBeInTheDocument();
  });

  it('opens the register modal prefilled from the deep link and consumes the action params', async () => {
    renderPage('/evaluations?action=register&kind=knowledge&resource_id=kb-1');
    // kind=knowledge 与 resource_id=kb-1 预填；知识库候选列表在空响应下正常就绪。
    const dialog = await screen.findByRole('dialog', { name: '登记被测资源' });
    expect(within(dialog).getByText('知识库')).toBeInTheDocument();
    expect(within(dialog).getByText('kb-1')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '登记并新建评测' })).toBeInTheDocument();
    // action/kind/resource_id 一次性消费，避免刷新反复弹窗。
    await waitFor(() => expect(query()).not.toHaveTextContent('action=register'));
  });
});
