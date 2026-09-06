import { Alert, Form, Input, Modal, Switch } from 'antd';
import { useEffect, useState } from 'react';

import type { EvaluationCase, SessionScript } from '../model/evaluation';

import { AssertionModeField, CaseShapeField, type CaseAssertionMode, type CaseShape } from './CaseFields';
import { SessionScriptFields, sessionFromForm, sessionToForm } from './SessionScriptFields';

export interface EditDraftCaseValues {
  name: string;
  /** 单轮形态必填；会话形态省略（后端 UpdateDraftCase 写 null）。 */
  input?: string;
  expectedOutput: string;
  assertionMode: CaseAssertionMode;
  enabled: boolean;
  /** 会话剧本形态携带完整剧本；单轮形态缺省。 */
  session?: SessionScript;
}

const toEditable = (value: unknown) => (typeof value === 'string' ? value : JSON.stringify(value ?? ''));

// EditDraftCaseModal 编辑草稿用例。会话剧本 case（draft.session 非空）默认处于
// 会话形态：允许改 goal/轮次（含每轮内嵌 tool_spec）与 expected_output/断言方式，
// 并通过 session 字段回写完整剧本，避免会话内容被 update 误清。用例形态可互转：
// 切到单轮后 input 必填、session 被省略（后端写 '{}' 语义回退单轮）。
export const EditDraftCaseModal = ({ open, draft, onClose, onSubmit }: {
  open: boolean; draft: EvaluationCase | null; onClose: () => void;
  onSubmit: (values: EditDraftCaseValues) => Promise<void>;
}) => {
  const [form] = Form.useForm<{
    name: string; input?: string; expected_output: string; assertion_mode: CaseAssertionMode; enabled: boolean;
    case_shape?: CaseShape; session_goal?: string;
  }>();
  const [loading, setLoading] = useState(false);
  const mode = Form.useWatch('assertion_mode', form);
  const shape = Form.useWatch('case_shape', form);

  useEffect(() => {
    if (!open || !draft) return;
    const sessionShape = !!draft.session;
    form.setFieldsValue({
      name: draft.name ?? '',
      input: sessionShape ? undefined : toEditable(draft.input),
      expected_output: toEditable(draft.expected_output),
      assertion_mode: draft.assertion_mode,
      enabled: draft.enabled ?? true,
      case_shape: sessionShape ? 'session' : 'single',
      ...(sessionShape && draft.session ? sessionToForm(draft.session) : {}),
    });
  }, [open, draft, form]);

  const close = () => { form.resetFields(); onClose(); };
  const submit = async () => {
    const values = await form.validateFields();
    setLoading(true);
    try {
      if (values.case_shape === 'session') {
        const session = sessionFromForm(values);
        if (!session) return;
        await onSubmit({
          name: values.name, expectedOutput: values.expected_output,
          assertionMode: values.assertion_mode, enabled: values.enabled, session,
        });
      } else {
        await onSubmit({
          name: values.name, input: values.input, expectedOutput: values.expected_output,
          assertionMode: values.assertion_mode, enabled: values.enabled,
        });
      }
      close();
    } catch { /* Parent owns the persistent Chinese error notification; keep the form open. */ }
    finally { setLoading(false); }
  };
  return <Modal title="编辑草稿用例" open={open} onCancel={close} onOk={() => void submit()}
    okText="保存" cancelText="取消" confirmLoading={loading} destroyOnHidden>
    <Form form={form} layout="vertical">
      <Form.Item name="name" label="用例名称" rules={[{ required: true, message: '请输入用例名称' }]}><Input aria-label="用例名称" /></Form.Item>
      <CaseShapeField />
      {shape === 'single' && <Form.Item name="input" label="测试输入" rules={[{ required: true, message: '请输入测试输入' }]}><Input.TextArea aria-label="测试输入" /></Form.Item>}
      {shape === 'session' && <SessionScriptFields />}
      <Form.Item name="expected_output" label="期望输出" rules={[{ required: true, message: '请输入期望输出' }]}><Input.TextArea aria-label="期望输出" /></Form.Item>
      <AssertionModeField />
      {(mode === 'judge' || draft?.tool_spec || draft?.step_judge) && <Alert type="info" showIcon
        style={{ marginBottom: 16 }}
        message="AI 判定、工具序列与步骤判定在 case 进入草稿时确定，编辑不可修改。" />}
      <Form.Item name="enabled" label="包含在本版本" valuePropName="checked">
        <Switch aria-label="包含在本版本" />
      </Form.Item>
    </Form>
  </Modal>;
};
