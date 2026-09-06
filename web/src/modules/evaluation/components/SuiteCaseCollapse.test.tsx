import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { EvaluationCase } from '../model/evaluation';

import { SuiteCaseCollapse } from './SuiteCaseCollapse';

const containsCase: EvaluationCase = {
  id: 'c1', name: '标准问候', input: '你好', expected_output: '您好', assertion_mode: 'contains', enabled: true,
};
const judgeCase: EvaluationCase = {
  id: 'c2', name: '总结判定', input: '帮我总结', expected_output: '要点', assertion_mode: 'judge',
  judge_spec: { model: 'judge-v1', rubric: '总结要点覆盖度' }, enabled: true,
  source_trace_id: 'trace-1', generate_reason: '负样本优先',
};
const sessionCase: EvaluationCase = {
  id: 'c3', name: '退货退款会话', expected_output: '已受理退款', assertion_mode: 'contains', enabled: true,
  session: {
    goal: '处理用户的退货退款诉求',
    turns: [
      { user: '快递一直没更新', probe: '识别到退货意向' },
      { user: '请帮我退款', tool_spec: { must_call: ['refund'], max_calls: 2 } },
    ],
  },
};
const processCase: EvaluationCase = {
  id: 'c4', name: '工具链路', input: '查天气', expected_output: '晴天', assertion_mode: 'contains',
  tool_spec: { must_call: ['weather'], must_not_call: ['delete'], order: ['search', 'weather'], max_calls: 5 },
  step_judge: { criteria: '逐步评分' }, enabled: true,
};

describe('SuiteCaseCollapse', () => {
  it('renders single-turn, session-script and judge/provenance blocks for audit', () => {
    render(<SuiteCaseCollapse cases={[containsCase, sessionCase, judgeCase, processCase]} />);
    expect(screen.getByText('标准问候')).toBeInTheDocument();
    expect(screen.getByText('会话剧本')).toBeInTheDocument();
    expect(screen.getByText('Goal：处理用户的退货退款诉求')).toBeInTheDocument();
    expect(screen.getByText('探针期望：')).toBeInTheDocument();
    expect(screen.getByText('本轮工具断言：')).toBeInTheDocument();
    expect(screen.getByText('必调用:refund；上限:2')).toBeInTheDocument();
    expect(screen.getByText('AI 判定配置')).toBeInTheDocument();
    expect(screen.getByText('模型：judge-v1')).toBeInTheDocument();
    expect(screen.getByText('生成来源')).toBeInTheDocument();
    expect(screen.getByText('Trace：')).toBeInTheDocument();
    expect(screen.getByText('trace-1')).toBeInTheDocument();
    expect(screen.getByText('过程判定配置')).toBeInTheDocument();
    expect(screen.getByText('工具断言：必调用:weather；禁调用:delete；顺序:search>weather；上限:5')).toBeInTheDocument();
    expect(screen.getByText('步骤判定：逐步评分')).toBeInTheDocument();
  });

  it('shows the empty hint when there are no cases and emptyText is given', () => {
    render(<SuiteCaseCollapse cases={[]} emptyText="草稿还没有用例。" />);
    expect(screen.getByText('草稿还没有用例。')).toBeInTheDocument();
  });

  it('hides edit/delete actions for members and exposes them only with callbacks', () => {
    render(<SuiteCaseCollapse cases={[containsCase]} />);
    expect(screen.queryByRole('button', { name: /编\s*辑/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /删\s*除/ })).not.toBeInTheDocument();
  });

  it('invokes edit and delete callbacks without performing api calls itself', () => {
    const onEditCase = vi.fn();
    const onDeleteCase = vi.fn();
    render(<SuiteCaseCollapse cases={[containsCase]} canManage onEditCase={onEditCase} onDeleteCase={onDeleteCase} />);
    fireEvent.click(screen.getByRole('button', { name: /编\s*辑/ }));
    expect(onEditCase).toHaveBeenCalledWith(containsCase);
    fireEvent.click(screen.getByRole('button', { name: /删\s*除/ }));
    expect(onDeleteCase).toHaveBeenCalledWith(containsCase);
  });
});
