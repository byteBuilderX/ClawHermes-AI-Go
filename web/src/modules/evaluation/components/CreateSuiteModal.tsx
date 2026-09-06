import { Form, Input, Modal, Select, Switch } from 'antd';
import { useState } from 'react';

import { registrableResourceKinds } from '../model/evaluation';
import type { ResourceKind } from '../model/evaluation';

import { AssertionModeField, CaseShapeField, JudgeSpecFields, StepJudgeFields, ToolSpecFields,
  type CaseAssertionMode, type CaseShape } from './CaseFields';
import { SessionScriptFields, type SessionTurnRow } from './SessionScriptFields';
import { displayLabel } from './evaluationView';

// 新建评测集仅限当前被测对象（agent/knowledge）：skill/mcp 已退出被测，历史套件只读。
const resourceOptions = registrableResourceKinds.map((value) => ({ value, label: displayLabel(value) }));

export interface CreateSuiteValues {
  resource_kind: ResourceKind; name: string; description?: string;
  case_name: string; input?: string; expected_output: string;
  assertion_mode: CaseAssertionMode; judge_model?: string; judge_rubric?: string; enabled: boolean;
  must_call?: string[]; must_not_call?: string[]; tool_order?: string[]; max_calls?: number; step_criteria?: string;
  case_shape?: CaseShape; session_goal?: string; session_turns?: SessionTurnRow[];
}

export const CreateSuiteModal = ({ open, onClose, onSubmit }: {
  open: boolean; onClose: () => void; onSubmit: (values: CreateSuiteValues) => Promise<void>;
}) => {
  const [form] = Form.useForm<CreateSuiteValues>();
  const [loading, setLoading] = useState(false);
  // shape 决定渲染单轮测试输入还是会话剧本编辑控件；initialValues 默认单轮，
  // 切到会话剧本由 CaseShapeField 预置一个空轮。
  const shape = Form.useWatch('case_shape', form);
  const close = () => { form.resetFields(); onClose(); };
  const submit = async () => {
    const values = await form.validateFields();
    setLoading(true);
    try { await onSubmit(values); close(); }
    catch { /* Parent owns the persistent Chinese error notification; keep the form open. */ }
    finally { setLoading(false); }
  };
  return <Modal title="新建套件" open={open} onCancel={close} onOk={() => void submit()}
    okText="创建" cancelText="取消" confirmLoading={loading} destroyOnHidden>
    <Form form={form} layout="vertical" initialValues={{ assertion_mode: 'contains', enabled: true, case_shape: 'single' }}>
      <Form.Item name="resource_kind" label="资源类型" rules={[{ required: true, message: '请选择资源类型' }]}>
        <Select aria-label="资源类型" options={resourceOptions} />
      </Form.Item>
      <Form.Item name="name" label="套件名称" rules={[{ required: true, message: '请输入套件名称' }]}><Input aria-label="套件名称" /></Form.Item>
      <Form.Item name="description" label="套件说明"><Input aria-label="套件说明" /></Form.Item>
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
