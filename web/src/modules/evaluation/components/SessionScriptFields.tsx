import { PlusOutlined } from '@ant-design/icons';
import { Button, Divider, Flex, Form, Input, Typography } from 'antd';

import type { SessionScript, SessionTurn, ToolSpecObject } from '../model/evaluation';

import { ToolSpecInputs } from './CaseFields';

// SessionTurnRow 是会话剧本一行的表单扁平形态：user 必填、probe 可选、
// tool_spec 为该轮工具序列过程断言（可选）。Form.List 直接挂在顶层
// session_turns 字段名上，故每行的嵌套字段路径为 session_turns.<i>.user 等。
export interface SessionTurnRow {
  user: string;
  probe?: string;
  tool_spec?: ToolSpecObject;
}

// compactToolSpec 把表单里可能含空数组的 tool_spec 压缩为领域形态：全部空/缺省
// 返回 undefined（该轮不做过程校验），有内容才产出对象，避免把空数组发到后端。
const compactToolSpec = (spec?: ToolSpecObject): ToolSpecObject | undefined => {
  if (!spec) return undefined;
  const clean = (items?: string[]) => (items ?? []).map((item) => item.trim()).filter(Boolean);
  const mustCall = clean(spec.must_call);
  const mustNotCall = clean(spec.must_not_call);
  const order = clean(spec.order);
  if (!mustCall.length && !mustNotCall.length && !order.length && spec.max_calls == null) {
    return undefined;
  }
  const result: ToolSpecObject = {};
  if (mustCall.length) result.must_call = mustCall;
  if (mustNotCall.length) result.must_not_call = mustNotCall;
  if (order.length) result.order = order;
  if (spec.max_calls != null) result.max_calls = spec.max_calls;
  return result;
};

// sessionFromForm 把表单扁平字段组装为 EvalSessionScript 领域形态：goal 与轮次都
// 缺省（形态实际为单轮）时返回 undefined。压缩 tool_spec 与空 user 轮次——结构与
// 后端 EvalSessionScript.Validate 语义一致，authoring 阶段即拦截脏数据。
export const sessionFromForm = (values: {
  session_goal?: string; session_turns?: SessionTurnRow[];
}): SessionScript | undefined => {
  const goal = values.session_goal?.trim();
  if (!goal) return undefined;
  const turns: SessionTurn[] = [];
  for (const row of values.session_turns ?? []) {
    const user = row.user?.trim();
    if (!user) continue;
    const turn: SessionTurn = { user };
    if (row.probe?.trim()) turn.probe = row.probe.trim();
    const toolSpec = compactToolSpec(row.tool_spec);
    if (toolSpec) turn.tool_spec = toolSpec;
    turns.push(turn);
  }
  if (turns.length === 0) return undefined;
  return { goal, turns };
};

// sessionToForm 是编辑回填的逆变换：领域剧本 → 表单扁平行，供 Form.setFieldsValue。
export const sessionToForm = (script: SessionScript): {
  session_goal: string; session_turns: SessionTurnRow[];
} => ({
  session_goal: script.goal,
  session_turns: script.turns.map((turn) => ({
    user: turn.user,
    probe: turn.probe,
    tool_spec: turn.tool_spec,
  })),
});

// SessionScriptFields 是会话剧本（阶段 B §5.4）编辑控件：goal 文本域 + Form.List
// 逐轮 user/probe + 每轮内嵌 tool_spec（用 ToolSpecInputs 落 session_turns.<i>.tool_spec）。
// 只渲染于「用例形态 = 会话剧本」；每轮控件常驻挂载（不用 Collapse），保证编辑回填
// 时 validateFields 能取回预填值而不丢。至少保留一轮，删除末轮禁用。
export const SessionScriptFields = () => <>
  <Form.Item name="session_goal" label="会话目标（Goal）"
    rules={[{ required: true, message: '请输入会话目标' }]}
    extra="描述被测任务终点，作为判定末轮是否达成目标的语义锚点。">
    <Input.TextArea aria-label="会话目标" rows={2} />
  </Form.Item>
  <Divider plain style={{ margin: '4px 0 12px' }}>剧本轮次</Divider>
  <Form.List name="session_turns" rules={[{
    validator: async (_rule, value: unknown) => {
      if (!Array.isArray(value) || value.length === 0) throw new Error('至少需要一轮用户消息');
    },
  }]}>
    {(fields, { add, remove }, { errors }) => (<>
      {fields.map(({ key, name, ...restField }) => (
        <div key={key} style={{ border: '1px solid #f0f0f0', borderRadius: 8, padding: '8px 12px 0', marginBottom: 12 }}>
          <Flex justify="space-between" align="center" style={{ marginBottom: 4 }}>
            <Typography.Text strong>第 {name + 1} 轮</Typography.Text>
            <Button type="text" danger size="small" disabled={fields.length <= 1}
              onClick={() => remove(name)} aria-label={`删除第 ${name + 1} 轮`}>删除</Button>
          </Flex>
          <Form.Item {...restField} name={[name, 'user']} label="用户消息"
            rules={[{ required: true, message: '请输入该轮用户消息' }]}>
            <Input.TextArea aria-label={`第 ${name + 1} 轮用户消息`} rows={2} />
          </Form.Item>
          <Form.Item {...restField} name={[name, 'probe']} label="探针期望（可选）"
            extra="该轮期望/探针锚点，供轨迹判据逐轮参考；可留空。">
            <Input.TextArea aria-label={`第 ${name + 1} 轮探针期望`} rows={1} />
          </Form.Item>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>本轮工具序列过程断言（可选）</Typography.Text>
          <ToolSpecInputs prefix={[name, 'tool_spec']} />
        </div>
      ))}
      <Form.ErrorList errors={errors} />
      <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({ user: '', probe: '' })}
        aria-label="添加一轮" style={{ marginBottom: 8 }}>添加一轮</Button>
    </>)}
  </Form.List>
</>;
