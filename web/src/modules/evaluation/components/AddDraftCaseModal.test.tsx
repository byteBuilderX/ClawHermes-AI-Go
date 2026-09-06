import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { AddDraftCaseModal, type AddDraftCaseValues } from './AddDraftCaseModal';

describe('AddDraftCaseModal', () => {
  it('submits a single-turn case with default contains assertion and enabled=true', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<AddDraftCaseModal open onClose={vi.fn()} onSubmit={onSubmit} />);

    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '标准问候' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '你好' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '您好' } });
    fireEvent.click(screen.getByRole('button', { name: /添\s*加/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      name: '标准问候', input: '你好', expected_output: '您好', assertion_mode: 'contains', enabled: true,
      judge_spec: undefined, tool_spec: undefined, step_judge: undefined,
    })));
    expect(onSubmit.mock.calls[0][0]).not.toHaveProperty('session');
  });

  it('collects judge_spec when the assertion mode is AI 判定', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<AddDraftCaseModal open onClose={vi.fn()} onSubmit={onSubmit} />);

    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '总结判定' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '帮我总结' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '要点' } });
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '断言方式' }));
    fireEvent.click(await screen.findByText('AI 判定'));
    fireEvent.change(screen.getByLabelText('判定模型'), { target: { value: 'judge-v1' } });
    fireEvent.change(screen.getByLabelText('评分标准'), { target: { value: '总结要点覆盖度' } });
    fireEvent.click(screen.getByRole('button', { name: /添\s*加/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      name: '总结判定', input: '帮我总结', expected_output: '要点',
      assertion_mode: 'judge', judge_spec: { model: 'judge-v1', rubric: '总结要点覆盖度' }, enabled: true,
    })));
  });

  it('collects tool_spec and step_judge from the always-visible process fields', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<AddDraftCaseModal open onClose={vi.fn()} onSubmit={onSubmit} />);

    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '工具链路' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '北京天气' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '晴天' } });
    const mustCall = screen.getByRole('combobox', { name: '必调用工具' });
    fireEvent.mouseDown(mustCall);
    fireEvent.change(mustCall, { target: { value: 'weather' } });
    fireEvent.keyDown(mustCall, { key: 'Enter', code: 'Enter', keyCode: 13 });
    fireEvent.change(screen.getByLabelText('步骤判定标准'), { target: { value: '逐步评分' } });
    fireEvent.click(screen.getByRole('button', { name: /添\s*加/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      name: '工具链路', input: '北京天气', expected_output: '晴天',
      tool_spec: { must_call: ['weather'] }, step_judge: { criteria: '逐步评分' }, enabled: true,
    })));
  });

  it('authors a session script case that omits input and carries the full script', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<AddDraftCaseModal open onClose={vi.fn()} onSubmit={onSubmit} />);

    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '退货退款会话' } });
    fireEvent.click(screen.getByRole('radio', { name: '会话剧本' }));
    await waitFor(() => expect(screen.getByLabelText('会话目标')).toBeInTheDocument());
    expect(screen.queryByLabelText('测试输入')).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('会话目标'), { target: { value: '处理用户的退货退款诉求' } });
    fireEvent.change(screen.getByLabelText('第 1 轮用户消息'), { target: { value: '快递一直没更新' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '已受理退款' } });
    fireEvent.click(screen.getByRole('button', { name: '添 加' }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    const values = onSubmit.mock.calls[0][0] as AddDraftCaseValues;
    expect(values.name).toBe('退货退款会话');
    expect(values.expected_output).toBe('已受理退款');
    expect(values.enabled).toBe(true);
    expect(values.session).toEqual(expect.objectContaining({ goal: '处理用户的退货退款诉求' }));
    expect(values.session?.turns).toEqual([{ user: '快递一直没更新' }]);
    expect(values).not.toHaveProperty('input');
  });
});
