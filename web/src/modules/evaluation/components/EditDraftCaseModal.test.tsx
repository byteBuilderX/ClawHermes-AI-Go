import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { EvaluationCase } from '../model/evaluation';

import { EditDraftCaseModal } from './EditDraftCaseModal';

const containsCase: EvaluationCase = {
  id: 'c1', name: '标准问候', input: '你好', expected_output: '您好',
  assertion_mode: 'contains', enabled: true,
};

describe('EditDraftCaseModal', () => {
  it('pre-fills from the draft and submits camel-cased values on save', async () => {
    const onClose = vi.fn();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<EditDraftCaseModal open draft={containsCase} onClose={onClose} onSubmit={onSubmit} />);

    expect(screen.getByLabelText('用例名称')).toHaveValue('标准问候');
    expect(screen.getByLabelText('测试输入')).toHaveValue('你好');
    expect(screen.getByLabelText('期望输出')).toHaveValue('您好');
    expect(screen.queryByText(/不可修改/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /保\s*存/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({
      name: '标准问候', input: '你好', expectedOutput: '您好', assertionMode: 'contains', enabled: true,
    }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it('pre-fills and saves a session draft case with the full script preserved', async () => {
    const onClose = vi.fn();
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const sessionCase: EvaluationCase = {
      id: 'c4', name: '退货退款会话', expected_output: '已受理退款', assertion_mode: 'contains', enabled: true,
      session: {
        goal: '处理用户的退货退款诉求',
        turns: [
          { user: '快递一直没更新', probe: '识别到退货意向' },
          { user: '请帮我退款', tool_spec: { must_call: ['refund'], max_calls: 2 } },
        ],
      },
    };
    render(<EditDraftCaseModal open draft={sessionCase} onClose={onClose} onSubmit={onSubmit} />);

    expect(screen.getByLabelText('会话目标')).toHaveValue('处理用户的退货退款诉求');
    expect(screen.getByLabelText('第 1 轮用户消息')).toHaveValue('快递一直没更新');
    expect(screen.getByLabelText('第 1 轮探针期望')).toHaveValue('识别到退货意向');
    expect(screen.queryByLabelText('测试输入')).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('会话目标'), { target: { value: '升级为优先处理' } });
    fireEvent.click(screen.getByRole('button', { name: /保\s*存/ }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      name: '退货退款会话', expectedOutput: '已受理退款', assertionMode: 'contains', enabled: true,
      session: expect.objectContaining({ goal: '升级为优先处理' }),
    })));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it('explains that the judge spec is immutable when editing a judge case', () => {
    const judgeCase: EvaluationCase = { ...containsCase, name: '总结判定', assertion_mode: 'judge' };
    render(<EditDraftCaseModal open draft={judgeCase} onClose={vi.fn()} onSubmit={vi.fn()} />);
    expect(screen.getByText(/工具序列与步骤判定在 case 进入草稿时确定/)).toBeInTheDocument();
  });

  it('warns that tool_spec and step_judge are immutable for a process-configured contains case', () => {
    const processCase: EvaluationCase = {
      ...containsCase, name: '工具链路', assertion_mode: 'contains',
      tool_spec: { must_call: ['weather'], max_calls: 5 }, step_judge: { criteria: '逐步评分' },
    };
    render(<EditDraftCaseModal open draft={processCase} onClose={vi.fn()} onSubmit={vi.fn()} />);
    expect(screen.getByText(/AI 判定、工具序列与步骤判定/)).toBeInTheDocument();
  });
});
