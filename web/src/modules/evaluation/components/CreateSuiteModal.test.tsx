import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { CreateSuiteModal } from './CreateSuiteModal';

describe('CreateSuiteModal', () => {
  it('submits the authored case with contains assertion by default', async () => {
    const onClose = vi.fn();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<CreateSuiteModal open onClose={onClose} onSubmit={onSubmit} />);

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '资源类型' }));
    fireEvent.click(await screen.findByText('技能'));
    fireEvent.change(screen.getByLabelText('套件名称'), { target: { value: '投诉分类基线' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '物流' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '快递没更新' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '物流' } });
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'skill', name: '投诉分类基线', case_name: '物流',
      input: '快递没更新', expected_output: '物流', assertion_mode: 'contains', enabled: true,
    })));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it('collects tool_spec and step_judge from the always-visible process fields', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<CreateSuiteModal open onClose={vi.fn()} onSubmit={onSubmit} />);

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '资源类型' }));
    fireEvent.click(await screen.findByText('技能'));
    fireEvent.change(screen.getByLabelText('套件名称'), { target: { value: '工具链路基线' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '查天气' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '北京天气' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '晴天' } });

    const mustCall = screen.getByRole('combobox', { name: '必调用工具' });
    fireEvent.mouseDown(mustCall);
    fireEvent.change(mustCall, { target: { value: 'weather' } });
    fireEvent.keyDown(mustCall, { key: 'Enter', code: 'Enter', keyCode: 13 });
    fireEvent.change(screen.getByLabelText('最大调用次数'), { target: { value: '5' } });
    fireEvent.change(screen.getByLabelText('步骤判定标准'), { target: { value: '每一步都要说明依据' } });

    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'skill', name: '工具链路基线', case_name: '查天气',
      input: '北京天气', expected_output: '晴天', assertion_mode: 'contains', enabled: true,
      must_call: ['weather'], max_calls: 5, step_criteria: '每一步都要说明依据',
    })));
  });

  it('authors a session script case and hides the single-turn input', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<CreateSuiteModal open onClose={vi.fn()} onSubmit={onSubmit} />);

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '资源类型' }));
    fireEvent.click(await screen.findByText('技能'));
    fireEvent.change(screen.getByLabelText('套件名称'), { target: { value: '售后会话基线' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '退货退款' } });
    fireEvent.click(screen.getByRole('radio', { name: '会话剧本' }));
    await waitFor(() => expect(screen.getByLabelText('会话目标')).toBeInTheDocument());
    expect(screen.queryByLabelText('测试输入')).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('会话目标'), { target: { value: '处理用户的退货退款诉求' } });
    fireEvent.change(screen.getByLabelText('第 1 轮用户消息'), { target: { value: '快递一直没更新' } });
    fireEvent.change(screen.getByLabelText('第 1 轮探针期望'), { target: { value: '识别到退货意向' } });
    fireEvent.click(screen.getByRole('button', { name: /添加一轮/ }));
    fireEvent.change(screen.getByLabelText('第 2 轮用户消息'), { target: { value: '请帮我退款' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '已受理退款' } });
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    const values = onSubmit.mock.calls[0][0] as {
      session_goal: string; session_turns: Array<{ user: string; probe?: string }>;
    };
    expect(values.session_goal).toBe('处理用户的退货退款诉求');
    expect(values.session_turns.map((turn) => turn.user)).toEqual(['快递一直没更新', '请帮我退款']);
    expect(values.session_turns[0].probe).toBe('识别到退货意向');
    expect(values.session_turns[0].user).toBe('快递一直没更新');
  });

  it('collects the judge model and rubric when assertion is AI 判定', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<CreateSuiteModal open onClose={vi.fn()} onSubmit={onSubmit} />);

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '资源类型' }));
    fireEvent.click(await screen.findByText('技能'));
    fireEvent.change(screen.getByLabelText('套件名称'), { target: { value: '投诉分类基线' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '总结判定' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '帮我总结' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '要点' } });
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '断言方式' }));
    fireEvent.click(await screen.findByText('AI 判定'));
    fireEvent.change(screen.getByLabelText('判定模型'), { target: { value: 'judge-v1' } });
    fireEvent.change(screen.getByLabelText('评分标准'), { target: { value: '总结要点覆盖度' } });
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'skill', name: '投诉分类基线', case_name: '总结判定',
      input: '帮我总结', expected_output: '要点',
      assertion_mode: 'judge', judge_model: 'judge-v1', judge_rubric: '总结要点覆盖度', enabled: true,
    })));
  });
});
