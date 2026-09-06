import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { CreateEvaluationPlan, ResourceSummary } from '../model/evaluation';

import { CreateEvaluationModal } from './CreateEvaluationModal';

const api = vi.hoisted(() => ({ listSuites: vi.fn(), listSuiteVersions: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

const agentResource: ResourceSummary = {
  id: 'r1', resource_id: 'agent-1', resource_kind: 'agent', status: 'active', stable_revision_id: 'v1',
  safe_summary: { name: '客服 Agent' }, created_at: '2026-07-23T00:00:00Z',
};
const publishedSuite = {
  id: 'suite-1', name: '投诉基线', description: '', status: 'published', resource_kind: 'agent',
  active_revision_id: 'rev-v2', draft_revision_id: 'rev-draft', active_version_no: 2, active_case_count: 5,
  created_by: 'u1', created_at: '2026-09-01T00:00:00Z',
};
const unpublishedSuite = {
  id: 'suite-2', name: '新退货集', description: '', status: 'draft', resource_kind: 'agent',
  draft_revision_id: 'rev-draft2', draft_case_count: 3,
  created_by: 'u1', created_at: '2026-09-02T00:00:00Z',
};
const publishedVersions = [
  { id: 'rev-v1', version_no: 1, status: 'published', resource_kind: 'agent', enabled_case_count: 4 },
  { id: 'rev-v2', version_no: 2, status: 'published', resource_kind: 'agent', enabled_case_count: 5 },
];

// flushAsync 在 act 内推进微任务与一个宏任务，让事件驱动的异步挂载 effect
// （SuitePicker 拉取评测集/版本链的 mockResolvedValue 微任务）在 act 内落定，
// 避免 setState 落在 act 外触发 "not wrapped in act" 警告。
const flushAsync = async () => { await act(async () => {
  await Promise.resolve();
  await Promise.resolve();
  await new Promise<void>((resolve) => { setTimeout(resolve, 0); });
}); };

const selectResource = async () => {
  fireEvent.mouseDown(screen.getByRole('combobox', { name: '目标资源' }));
  fireEvent.click(await screen.findByText('客服 Agent（agent-1）'));
  await flushAsync();
};

const chooseSuite = async (optionLabel: string) => {
  fireEvent.mouseDown(await screen.findByRole('combobox', { name: '评测集' }));
  fireEvent.click(await screen.findByText(optionLabel));
  await flushAsync();
};

describe('CreateEvaluationModal', () => {
  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    api.listSuites.mockResolvedValue({ items: [] });
    api.listSuiteVersions.mockResolvedValue([]);
  });

  it('submits a published plan when the picked suite has a published revision', async () => {
    api.listSuites.mockResolvedValue({ items: [publishedSuite] });
    api.listSuiteVersions.mockResolvedValue(publishedVersions);
    const onSubmit = vi.fn();
    render(<CreateEvaluationModal open resources={[agentResource]} onClose={vi.fn()} onSubmit={onSubmit} />);

    await selectResource();
    await chooseSuite('投诉基线（v2 · 5 个启用用例）');
    // SuitePicker 加载版本链后默认选中 active revision 并自动 emit 完整 published pick；
    // 等待版本选择控件显示 v2，确保 pick.revisionId 已就绪再提交。
    await waitFor(() => expect(screen.getByText('v2 · 5 个启用用例')).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: '开始运行' }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    const plan = onSubmit.mock.calls[0][0] as CreateEvaluationPlan;
    expect(plan).toEqual({
      mode: 'published',
      resource: { kind: 'agent', resource_id: 'agent-1', revision_id: 'v1' },
      suiteId: 'suite-1',
      revisionId: 'rev-v2',
    });
  });

  it('submits an unpublished plan when the picked suite is an unpublished draft', async () => {
    api.listSuites.mockResolvedValue({ items: [unpublishedSuite] });
    const onSubmit = vi.fn();
    render(<CreateEvaluationModal open resources={[agentResource]} onClose={vi.fn()} onSubmit={onSubmit} />);

    await selectResource();
    await chooseSuite('新退货集（未发布 · 3 个用例）');
    await waitFor(() => expect(screen.getByText('该评测集尚未发布，运行前会先发布为 v1。')).toBeInTheDocument());
    expect(api.listSuiteVersions).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '开始运行' }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    const plan = onSubmit.mock.calls[0][0] as CreateEvaluationPlan;
    expect(plan).toEqual({
      mode: 'unpublished',
      resource: { kind: 'agent', resource_id: 'agent-1', revision_id: 'v1' },
      suiteId: 'suite-2',
    });
  });

  it('submits a create plan whose case maps the authored fields', async () => {
    const onSubmit = vi.fn();
    render(<CreateEvaluationModal open resources={[agentResource]} onClose={vi.fn()} onSubmit={onSubmit} />);

    // 先切到「新建评测集」再选目标资源：避免已有态 SuitePicker 挂载产生异步加载。
    fireEvent.click(screen.getByRole('radio', { name: '新建评测集' }));
    await selectResource();
    fireEvent.change(screen.getByLabelText('评测集名称'), { target: { value: '客服基线' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '标准问候' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '你好' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '您好' } });

    const mustCall = screen.getByRole('combobox', { name: '必调用工具' });
    fireEvent.mouseDown(mustCall);
    fireEvent.change(mustCall, { target: { value: 'weather' } });
    fireEvent.keyDown(mustCall, { key: 'Enter', code: 'Enter', keyCode: 13 });
    fireEvent.change(screen.getByLabelText('步骤判定标准'), { target: { value: '每一步都要说明依据' } });

    fireEvent.click(screen.getByRole('button', { name: '创建并运行' }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    const plan = onSubmit.mock.calls[0][0] as CreateEvaluationPlan;
    expect(plan.mode).toBe('create');
    // 判别联合收窄：tsc 不随 expect 收窄，显式守卫后按 create 变体访问字段。
    if (plan.mode !== 'create') throw new Error('预期 create 计划');
    expect(plan.resource).toEqual({ kind: 'agent', resource_id: 'agent-1', revision_id: 'v1' });
    expect(plan.name).toBe('客服基线');
    const cases = plan.cases;
    expect(cases).toHaveLength(1);
    expect(cases[0]).toEqual(expect.objectContaining({
      name: '标准问候', input: '你好', expected_output: '您好', assertion_mode: 'contains', enabled: true,
      tool_spec: { must_call: ['weather'] },
      step_judge: { criteria: '每一步都要说明依据' },
    }));
  });

  it('keeps the previous session hidden when reopening', async () => {
    const onSubmit = vi.fn();
    const { rerender } = render(
      <CreateEvaluationModal open resources={[agentResource]} onClose={vi.fn()} onSubmit={onSubmit} />,
    );
    fireEvent.click(screen.getByRole('radio', { name: '新建评测集' }));
    await selectResource();
    fireEvent.change(screen.getByLabelText('评测集名称'), { target: { value: '残留名称' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '残留用例' } });

    // 关闭后重开：内部选择与表单重置，模式回到「已有评测集」，不残留上次草稿。
    rerender(<CreateEvaluationModal open={false} resources={[agentResource]} onClose={vi.fn()} onSubmit={onSubmit} />);
    rerender(<CreateEvaluationModal open resources={[agentResource]} onClose={vi.fn()} onSubmit={onSubmit} />);

    expect(screen.getByRole('radio', { name: '从已有评测集运行' })).toBeChecked();
    expect(screen.queryByLabelText('评测集名称')).not.toBeInTheDocument();
  });
});
