import { render, screen } from '@testing-library/react';
import { Form } from 'antd';
import { describe, expect, it } from 'vitest';

import { SessionScriptFields, sessionFromForm, sessionToForm } from './SessionScriptFields';

describe('sessionFromForm', () => {
  it('compacts an untouched per-turn tool_spec into undefined', () => {
    const script = sessionFromForm({
      session_goal: '处理退货退款',
      session_turns: [
        { user: '快递没到', probe: '识别到退货意向', tool_spec: { must_call: [], order: [], max_calls: undefined } },
        { user: '请退款' },
      ],
    });
    expect(script).toEqual({
      goal: '处理退货退款',
      turns: [{ user: '快递没到', probe: '识别到退货意向' }, { user: '请退款' }],
    });
  });

  it('drops empty user rows and returns undefined without a goal or any turn', () => {
    expect(sessionFromForm({ session_goal: '  ', session_turns: [{ user: '残留' }] })).toBeUndefined();
    expect(sessionFromForm({ session_goal: '目标', session_turns: [{ user: '   ' }] })).toBeUndefined();
  });

  it('round-trips a domain script through the form shape unchanged', () => {
    const script = {
      goal: '处理退货退款诉求',
      turns: [
        { user: '快递没到', probe: '识别到退货意向' },
        { user: '请退款', tool_spec: { must_call: ['refund'], max_calls: 2 } },
      ],
    };
    expect(sessionFromForm(sessionToForm(script))).toEqual(script);
  });
});

describe('SessionScriptFields', () => {
  it('renders the goal input and keeps a single seeded turn deletable', () => {
    render(<Form initialValues={{ session_turns: [{}] }}><SessionScriptFields /></Form>);
    expect(screen.getByLabelText('会话目标')).toBeInTheDocument();
    expect(screen.getByLabelText('第 1 轮用户消息')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /添加一轮/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /删除第 1 轮/ })).toBeDisabled();
  });
});
