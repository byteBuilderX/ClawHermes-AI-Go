import { Form, Input, Modal, Switch } from 'antd';
import { useState } from 'react';

import type { SessionScript } from '../model/evaluation';

import { AssertionModeField, CaseShapeField, JudgeSpecFields, StepJudgeFields, ToolSpecFields,
  type CaseAssertionMode, type CaseShape } from './CaseFields';
import { SessionScriptFields, sessionFromForm, type SessionTurnRow } from './SessionScriptFields';

// AddDraftCaseValues 是「添加草稿用例」提交载荷，与后端 addDraftCase 的
// gen.EvaluationCaseRequest snake_case 字段一一对应；会话剧本 case 携带 session
// 并省略 input（后端对 session 缺失回退单轮）。
export interface AddDraftCaseValues {
  name: string;
  /** 单轮形态必填；会话形态省略。 */
  input?: unknown;
  expected_output: string;
  assertion_mode: CaseAssertionMode;
  enabled: boolean;
  /** 仅 assertion_mode = judge 时携带。 */
  judge_spec?: { model?: string; rubric?: string };
  tool_spec?: { must_call?: string[]; must_not_call?: string[]; order?: string[]; max_calls?: number };
  step_judge?: { criteria?: string };
  session?: SessionScript;
}

// processFieldsToSpec 把表单过程字段映射为 case 载荷：任一工具断言字段存在时生成
// tool_spec（order 字段名与后端 ToolSpec JSON 一致），否则 undefined；step_criteria
// 存在时生成 step_judge。与 EvaluationCenterPage.processFieldsToSpec 同构——后者因
// 并行改造尚未导出，这里复制其映射逻辑；后续切片统一抽取共享位置后删除本副本。
const processFieldsToSpec = (values: {
  must_call?: string[]; must_not_call?: string[]; tool_order?: string[]; max_calls?: number; step_criteria?: string;
}) => ({
  tool_spec: (values.must_call?.length || values.must_not_call?.length || values.tool_order?.length || values.max_calls)
    ? { must_call: values.must_call, must_not_call: values.must_not_call, order: values.tool_order, max_calls: values.max_calls }
    : undefined,
  step_judge: values.step_criteria ? { criteria: values.step_criteria } : undefined,
});

// AddDraftCaseModal 向草稿追加手写用例。区别于编辑态（judge/tool/step 进入草稿即
// 不可改），添加时可配置全部判定配置：AI 判定模型/评分标准、工具序列过程断言与
// 步骤判定。会话剧本 case 由 session 携带完整剧本并省略 input。
export const AddDraftCaseModal = ({ open, onClose, onSubmit }: {
  open: boolean; onClose: () => void; onSubmit: (values: AddDraftCaseValues) => Promise<void>;
}) => {
  const [form] = Form.useForm<{
    case_name: string; input?: string; expected_output: string; assertion_mode: CaseAssertionMode; enabled: boolean;
    case_shape?: CaseShape; judge_model?: string; judge_rubric?: string;
    must_call?: string[]; must_not_call?: string[]; tool_order?: string[]; max_calls?: number; step_criteria?: string;
    session_goal?: string; session_turns?: SessionTurnRow[];
  }>();
  const [loading, setLoading] = useState(false);
  const shape = Form.useWatch('case_shape', form);
  const close = () => { form.resetFields(); onClose(); };
  const submit = async () => {
    const values = await form.validateFields();
    setLoading(true);
    try {
      const judge_spec = values.assertion_mode === 'judge'
        ? { model: values.judge_model, rubric: values.judge_rubric } : undefined;
      const base = {
        name: values.case_name, expected_output: values.expected_output,
        assertion_mode: values.assertion_mode, enabled: values.enabled,
      };
      if (values.case_shape === 'session') {
        const session = sessionFromForm(values);
        if (!session) return;
        await onSubmit({ ...base, judge_spec, ...processFieldsToSpec(values), session });
      } else {
        await onSubmit({ ...base, input: values.input, judge_spec, ...processFieldsToSpec(values) });
      }
      close();
    } catch { /* Parent owns the persistent Chinese error notification; keep the form open. */ }
    finally { setLoading(false); }
  };
  return <Modal title="添加草稿用例" open={open} onCancel={close} onOk={() => void submit()}
    okText="添加" cancelText="取消" confirmLoading={loading} destroyOnHidden>
    <Form form={form} layout="vertical" initialValues={{ assertion_mode: 'contains', enabled: true, case_shape: 'single' }}>
      <Form.Item name="case_name" label="用例名称" rules={[{ required: true, message: '请输入用例名称' }]}><Input aria-label="用例名称" /></Form.Item>
      <CaseShapeField />
      {shape === 'single' && <Form.Item name="input" label="测试输入" rules={[{ required: true, message: '请输入测试输入' }]}><Input.TextArea aria-label="测试输入" /></Form.Item>}
      {shape === 'session' && <SessionScriptFields />}
      <Form.Item name="expected_output" label="期望输出" rules={[{ required: true, message: '请输入期望输出' }]}><Input.TextArea aria-label="期望输出" /></Form.Item>
      <AssertionModeField />
      <JudgeSpecFields />
      <ToolSpecFields />
      <StepJudgeFields />
      <Form.Item name="enabled" label="包含在本版本" valuePropName="checked">
        <Switch aria-label="包含在本版本" />
      </Form.Item>
    </Form>
  </Modal>;
};
