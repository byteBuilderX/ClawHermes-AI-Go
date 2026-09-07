import { Form, Input, Modal, Radio, Select, Typography, message } from 'antd';
import { useEffect, useRef, useState } from 'react';

import { resourceDisplayName } from '../lib/resourceName';
import type { CreateEvaluationPlan, EvaluationCase, ResourceKind, ResourceRef, ResourceSummary } from '../model/evaluation';

import { AssertionModeField, CaseShapeField, JudgeSpecFields, StepJudgeFields, ToolSpecFields,
  type CaseAssertionMode, type CaseShape } from './CaseFields';
import { SessionScriptFields, sessionFromForm, type SessionTurnRow } from './SessionScriptFields';
import { SuitePicker, type SuitePick } from './SuitePicker';

type CreateMode = 'existing' | 'create';

interface Values {
  resource_id: string;
  name?: string;
  description?: string;
  case_name?: string;
  input?: string;
  expected_output?: string;
  assertion_mode: CaseAssertionMode;
  judge_model?: string;
  judge_rubric?: string;
  must_call?: string[];
  must_not_call?: string[];
  tool_order?: string[];
  max_calls?: number;
  step_criteria?: string;
  case_shape?: CaseShape;
  session_goal?: string;
  session_turns?: SessionTurnRow[];
}

// processFieldsToSpec 把表单过程字段映射为 case 载荷（§6.5）：任一工具断言字段
// 存在时生成 tool_spec，否则 undefined；step_criteria 存在时生成 step_judge。
// 逻辑与 EvaluationCenterPage 原内联实现等价，搬入本文件以便新建态直接产出 case。
const processFieldsToSpec = (values: {
  must_call?: string[]; must_not_call?: string[]; tool_order?: string[]; max_calls?: number; step_criteria?: string;
}) => ({
  tool_spec: (values.must_call?.length || values.must_not_call?.length || values.tool_order?.length || values.max_calls)
    ? { must_call: values.must_call, must_not_call: values.must_not_call, order: values.tool_order, max_calls: values.max_calls }
    : undefined,
  step_judge: values.step_criteria ? { criteria: values.step_criteria } : undefined,
});

// formValuesToCase 把新建态表单映射为单 case（单 case 数组，形状与 createSuite 入参一致）。
// 会话剧本（sessionFromForm 命中）产出 session 并省略 input；单轮产出 input。
const formValuesToCase = (values: Values): EvaluationCase => {
  const session = sessionFromForm(values);
  const assertionMode = values.assertion_mode ?? 'contains';
  return {
    name: values.case_name ?? '',
    expected_output: values.expected_output,
    assertion_mode: assertionMode,
    judge_spec: assertionMode === 'judge' ? { model: values.judge_model, rubric: values.judge_rubric } : undefined,
    enabled: true,
    ...processFieldsToSpec(values),
    ...(session ? { session } : { input: values.input }),
  };
};

const resourceLabel = (item: ResourceSummary) => {
  // 目标资源下拉主文案用真名（resource_name → safe_summary），id 加括弱化核对；
  // 真名缺失时直接以 id 作选项标识（下拉属身份选择控件，非名称单元格）。
  const name = resourceDisplayName(item);
  return name === '—' ? item.resource_id : `${name}（${item.resource_id}）`;
};

export const CreateEvaluationModal = ({ open, resources, focusResource, onClose, onSubmit }: {
  open: boolean; resources: ResourceSummary[]; onClose: () => void; onSubmit: (plan: CreateEvaluationPlan) => void;
  /** 登记并新建评测流程预选的目标资源（kind+resource_id）；列表刷新到位即预选对应行。 */
  focusResource?: { kind: ResourceKind; resource_id: string };
}) => {
  const [form] = Form.useForm<Values>();
  const [mode, setMode] = useState<CreateMode>('existing');
  const [pick, setPick] = useState<SuitePick | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // shape 决定新建态渲染单轮测试输入还是会话剧本编辑控件；initialValues 默认单轮。
  const shape = Form.useWatch('case_shape', form);
  const selectedResourceId = Form.useWatch('resource_id', form);
  // 目标资源仅限当前被测（agent/knowledge）：skill/mcp 退出被测后不再发起新评测，
  // 历史行即便进入资源列表也不应出现在「新建评测」目标下拉。
  const selectableResources = resources.filter((item) => item.stable_revision_id
    && (item.resource_kind === 'agent' || item.resource_kind === 'knowledge'));
  const selectedResource = selectableResources.find((item) => item.id === selectedResourceId) || null;

  // 每次 open 重置内部选择与表单：清空资源选择、评测集 pick 并回到「已有评测集」模式，
  // 避免重新打开时残留上次提交的选择与草稿。
  useEffect(() => {
    if (!open) return;
    form.resetFields();
    setPick(null);
    setMode('existing');
  }, [open, form]);

  // 「登记并新建评测」预选目标资源：focusResource 指定 kind+resource_id，匹配
  // selectableResources 中对应行；resources 由父级登记后刷新，迟到时本 effect 随其
  // 更新自动补预选。仅对同一 focus key 应用一次，避免覆盖用户后续手动切换。
  const focusAppliedKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (!open) return;
    if (!focusResource) { focusAppliedKeyRef.current = null; return; }
    const key = `${focusResource.kind}:${focusResource.resource_id}`;
    if (focusAppliedKeyRef.current === key) return;
    const matched = selectableResources.find((item) =>
      item.resource_kind === focusResource.kind && item.resource_id === focusResource.resource_id);
    if (matched) {
      form.setFieldValue('resource_id', matched.id);
      focusAppliedKeyRef.current = key;
    }
  }, [open, focusResource, selectableResources, form]);

  // 切换目标资源时清空已选评测集，防止 pick 指向其他资源类型下的套件。
  useEffect(() => { setPick(null); }, [selectedResourceId]);

  const submit = async () => {
    const values = await form.validateFields();
    const resource = selectableResources.find((item) => item.id === values.resource_id);
    if (!resource?.stable_revision_id) return;
    const ref: ResourceRef = {
      kind: resource.resource_kind,
      resource_id: resource.resource_id,
      revision_id: resource.stable_revision_id,
    };
    const plan: CreateEvaluationPlan | null = mode === 'create'
      ? { mode: 'create', resource: ref, name: values.name ?? '', description: values.description,
        cases: [formValuesToCase(values)] }
      : pick
        ? (pick.revisionId
          ? { mode: 'published', resource: ref, suiteId: pick.suiteId, revisionId: pick.revisionId }
          : { mode: 'unpublished', resource: ref, suiteId: pick.suiteId })
        : null;
    if (!plan) {
      message.error({ content: '请先选择要运行的评测集', duration: 3 });
      return;
    }
    setSubmitting(true);
    try {
      await onSubmit(plan);
      onClose();
    } catch {
      // 父层已负责持久错误提示；保持打开供修正或重试（与候选评测模态框语义一致）。
    } finally {
      setSubmitting(false);
    }
  };

  return <Modal title="新建评测" open={open} onCancel={onClose} onOk={() => void submit()}
    okText={mode === 'create' ? '创建并运行' : '开始运行'} cancelText="取消" destroyOnHidden confirmLoading={submitting}>
    <Form form={form} layout="vertical" initialValues={{ assertion_mode: 'contains', case_shape: 'single' }}>
      <Form.Item name="resource_id" label="目标资源" rules={[{ required: true, message: '请选择目标资源' }]}>
        <Select aria-label="目标资源" options={selectableResources.map((item) => ({ value: item.id, label: resourceLabel(item) }))} />
      </Form.Item>
      <Radio.Group aria-label="运行模式" value={mode} onChange={(event) => setMode(event.target.value)}
        options={[{ value: 'existing', label: '从已有评测集运行' }, { value: 'create', label: '新建评测集' }]}
        optionType="button" buttonStyle="solid" style={{ marginBottom: 16, width: '100%' }} />
      {mode === 'existing' ? (
        selectedResource ? (
          // key 随资源 id：切换目标资源时整体重载评测集并清空内部选择。
          <SuitePicker key={selectedResource.id} resourceKind={selectedResource.resource_kind} value={pick}
            onChange={setPick} canManage allowUnpublished onNeedCreate={() => setMode('create')} />
        ) : (
          <Typography.Text type="secondary">请先选择目标资源以加载评测集</Typography.Text>
        )
      ) : <>
        <Form.Item name="name" label="评测集名称" rules={[{ required: true, message: '请输入评测集名称' }]}>
          <Input aria-label="评测集名称" />
        </Form.Item>
        <Form.Item name="description" label="评测集说明"><Input aria-label="评测集说明" /></Form.Item>
        <Form.Item name="case_name" label="用例名称" rules={[{ required: true, message: '请输入用例名称' }]}>
          <Input aria-label="用例名称" />
        </Form.Item>
        <CaseShapeField />
        {shape === 'single' && <Form.Item name="input" label="测试输入" rules={[{ required: true, message: '请输入测试输入' }]}>
          <Input.TextArea aria-label="测试输入" />
        </Form.Item>}
        {shape === 'session' && <SessionScriptFields />}
        <Form.Item name="expected_output" label="期望输出" rules={[{ required: true, message: '请输入期望输出' }]}>
          <Input.TextArea aria-label="期望输出" />
        </Form.Item>
        <AssertionModeField />
        <JudgeSpecFields />
        <ToolSpecFields />
        <StepJudgeFields />
      </>}
    </Form>
  </Modal>;
};
