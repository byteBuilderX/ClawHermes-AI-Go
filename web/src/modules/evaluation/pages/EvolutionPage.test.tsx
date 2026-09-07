import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { message as antdStaticMessage } from 'antd';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { EvolutionPage } from './EvolutionPage';

const role = vi.hoisted(() => ({ isAdmin: true }));
vi.mock('@/modules/iam', () => ({ useTenantRole: () => role }));

const api = vi.hoisted(() => ({
  listCandidates: vi.fn(),
  listExperiments: vi.fn(),
  listSuites: vi.fn(),
  listSuiteVersions: vi.fn(),
  rejectCandidate: vi.fn(),
  enqueueRun: vi.fn(),
  pauseExperiment: vi.fn(),
  promoteExperiment: vi.fn(),
  rollbackExperiment: vi.fn(),
  generateOptimization: vi.fn(),
  createExperiment: vi.fn(),
  recordFeedback: vi.fn(),
}));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

const proposedCandidate = {
  id: 'cand-1', resource_id: 'agent-1', revision_id: 'cand-rev', parent_revision_id: 'base-rev',
  source: 'optimize', status: 'proposed', resource_kind: 'agent', rank: 1, state_version: 1,
  safe_diff: { changed_fields: [], changes: {}, parent_missing: false },
  created_by: 'u1', created_at: '2026-08-02T00:00:00Z',
};
const runningExperiment = {
  id: 'exp-1', resource_id: 'agent-1', stable_revision_id: 'stable-rev', canary_revision_id: 'cand-rev',
  status: 'running', recommendation: 'promote', resource_kind: 'agent', stage_percent: 25,
  safety_stopped: false, state_version: 1,
  gates: { quality: 'passed', cost: 'passed', latency: 'passed', error_rate: 'passed', security: 'passed' },
  promotion_evidence: { eligible: true, blockers: [],
    gates: { quality: 'passed', cost: 'passed', latency: 'passed', error_rate: 'passed', security: 'passed' } },
  created_by: 'u1', created_at: '2026-08-02T00:00:00Z',
};
const suite = {
  id: 'suite-1', name: '客服套件', description: '', status: 'active', resource_kind: 'agent',
  active_revision_id: 'rev-1', active_version_no: 1, active_case_count: 2,
  created_by: 'u1', created_at: '2026-08-01T00:00:00Z',
};
const publishedVersion = {
  id: 'rev-1', version_no: 1, status: 'published', resource_kind: 'agent',
  published_at: '2026-08-01T00:00:00Z', enabled_case_count: 2,
};

// 复位 antd 静态 message/Modal：message.destroy() 走 React 干净卸载 holder（直接移除
// DOM 会让单例 holder 脱离且后续 message 不可见）；确认弹窗由用例点按钮自行关闭。
const clearTransient = () => {
  antdStaticMessage.destroy();
  document.querySelectorAll('.ant-modal-root').forEach((node) => {
    // 仅移除已关闭（空壳）的 modal root，避免竞态与跨用例文案叠加。
    if (node.childElementCount === 0) node.remove();
  });
};

// antd Modal.confirm 与已开的 Drawer 都暴露 role=dialog，标题还会在 header 与 body
// 双份渲染，故不用文案/role 定位，直接取唯一的 confirm 容器再点它的主按钮。
const clickConfirmOk = async (okName: string | RegExp) => {
  const modalRoot = await waitFor(() => {
    const root = document.querySelector<HTMLElement>('.ant-modal-confirm');
    expect(root).not.toBeNull();
    return root!;
  });
  fireEvent.click(within(modalRoot).getByRole('button', { name: okName }));
};

const openCandidateDrawer = async () => {
  fireEvent.click(await screen.findByRole('button', { name: '详情' }));
  await screen.findByText('来源：optimize');
};

// antd 两级选择：先选「评测集」，随后 SuitePicker 加载 published 版本并默认 active。
const pickSuite = async () => {
  fireEvent.mouseDown(screen.getByRole('combobox', { name: '评测集' }));
  const option = await waitFor(() => {
    const node = Array.from(document.querySelectorAll<HTMLElement>('.ant-select-item-option-content'))
      .find((el) => el.textContent?.includes('客服套件'));
    expect(node).toBeDefined();
    return node!;
  });
  fireEvent.click(option);
};

describe('EvolutionPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    role.isAdmin = true;
    clearTransient();
    api.listCandidates.mockResolvedValue({ items: [] });
    api.listExperiments.mockResolvedValue({ items: [] });
    api.listSuites.mockResolvedValue({ items: [] });
    api.listSuiteVersions.mockResolvedValue([]);
    api.rejectCandidate.mockResolvedValue({});
    api.enqueueRun.mockResolvedValue({ job_id: 'job-1', status: 'queued' });
    api.pauseExperiment.mockResolvedValue({});
    api.promoteExperiment.mockResolvedValue({});
    api.rollbackExperiment.mockResolvedValue({});
    api.generateOptimization.mockResolvedValue({});
    api.createExperiment.mockResolvedValue({});
    api.recordFeedback.mockResolvedValue({});
  });

  it('loads candidates and experiments with the default current-kind aggregation', async () => {
    render(<EvolutionPage />);
    await waitFor(() => expect(api.listCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ resource_kind: 'agent,knowledge' })));
    await waitFor(() => expect(api.listExperiments).toHaveBeenCalledWith(
      expect.objectContaining({ resource_kind: 'agent,knowledge' })));
  });

  it('shows empty states for both tracks', async () => {
    render(<EvolutionPage />);
    expect(await screen.findByText(/候选版本还是空的/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: /金丝雀实验/ }));
    expect(await screen.findByText(/金丝雀实验还是空的/)).toBeInTheDocument();
  });

  it('renders a candidate row from the feed', async () => {
    api.listCandidates.mockResolvedValue({ items: [proposedCandidate] });
    render(<EvolutionPage />);
    expect(await screen.findByText('cand-rev')).toBeInTheDocument();
  });

  it('lets an admin reject a proposed candidate with a state-version command', async () => {
    api.listCandidates.mockResolvedValue({ items: [proposedCandidate] });
    render(<EvolutionPage />);
    await openCandidateDrawer();
    // 抽屉管理区展示评审命令（拒绝 + 候选离线评测）。
    fireEvent.click(screen.getByRole('button', { name: '拒绝候选' }));
    await clickConfirmOk('拒绝候选');
    await waitFor(() => expect(api.rejectCandidate).toHaveBeenCalledWith('cand-1',
      expect.objectContaining({ reason: '管理员拒绝候选版本', expected_state_version: 1,
        idempotency_key: expect.any(String) })));
    // 命令成功触发列表刷新。
    await waitFor(() => expect(api.listCandidates).toHaveBeenCalledTimes(2));
    expect(await screen.findByText('候选版本已拒绝')).toBeInTheDocument();
  });

  it('lets an admin enqueue an offline evaluation for a proposed candidate', async () => {
    api.listCandidates.mockResolvedValue({ items: [proposedCandidate] });
    api.listSuites.mockResolvedValue({ items: [suite] });
    api.listSuiteVersions.mockResolvedValue([publishedVersion]);
    render(<EvolutionPage />);
    await openCandidateDrawer();
    fireEvent.click(screen.getByRole('button', { name: '运行离线评测' }));
    await screen.findByText('运行候选离线评测');
    await pickSuite();
    const ok = await screen.findByRole('button', { name: '开始评测' });
    await waitFor(() => expect(ok).not.toBeDisabled());
    fireEvent.click(ok);
    await waitFor(() => expect(api.enqueueRun).toHaveBeenCalledWith(
      { kind: 'agent', resource_id: 'agent-1', revision_id: 'cand-rev' }, 'rev-1', expect.any(String)));
    expect(await screen.findByText('候选离线评测已进入运行队列')).toBeInTheDocument();
  });

  it('keeps command affordances and the evolution entry hidden from members', async () => {
    role.isAdmin = false;
    api.listCandidates.mockResolvedValue({ items: [proposedCandidate] });
    api.listExperiments.mockResolvedValue({ items: [runningExperiment] });
    render(<EvolutionPage />);
    expect(screen.queryByRole('button', { name: /进化操作/ })).not.toBeInTheDocument();
    await screen.findByText('cand-rev');
    await openCandidateDrawer();
    expect(screen.queryByRole('button', { name: /运行离线评测/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '拒绝候选' })).not.toBeInTheDocument();
  });

  it('lets an admin promote an eligible running experiment after confirmation', async () => {
    api.listCandidates.mockResolvedValue({ items: [] });
    api.listExperiments.mockResolvedValue({ items: [runningExperiment] });
    render(<EvolutionPage />);
    fireEvent.click(screen.getByRole('tab', { name: /金丝雀实验/ }));
    fireEvent.click(await screen.findByRole('button', { name: '详情' }));
    await screen.findByText('系统建议');
    // 门禁全部通过 → 晋级主按钮可用；两字按钮会被 antd 插入空格（晋 级）。
    fireEvent.click(screen.getByRole('button', { name: /^晋\s*级$/ }));
    await clickConfirmOk(/^晋\s*级$/);
    await waitFor(() => expect(api.promoteExperiment).toHaveBeenCalledWith('exp-1',
      expect.objectContaining({ reason: '管理员晋级实验', expected_state_version: 1,
        idempotency_key: expect.any(String) })));
    expect(await screen.findByText('实验已晋级')).toBeInTheDocument();
  });

  it('opens the evolution command modal from the admin header action', async () => {
    render(<EvolutionPage />);
    // 头部按钮带 ExperimentOutlined 图标，access name 会拼入图标，按子串匹配。
    fireEvent.click(screen.getByRole('button', { name: /进化操作/ }));
    expect(await screen.findByRole('tab', { name: '记录反馈' })).toBeInTheDocument();
  });
});
