import { Button, Form, Input, InputNumber, Modal, Select, Tabs, Typography } from 'antd';
import { useEffect, useState } from 'react';

import { registrableResourceKinds } from '../model/evaluation';
import type { ResourceKind } from '../model/evaluation';

import { SuitePicker, type SuitePick } from './SuitePicker';
import { displayLabel } from './evaluationView';

// 演化命令（生成优化/金丝雀实验）面向当前被测对象：skill/mcp 已退出被测，不再对
// 历史类型发起优化与实验（历史 candidate/experiment 记录仅只读浏览）。
const resourceOptions = registrableResourceKinds.map((value) => ({ value, label: displayLabel(value) }));

export interface OptimizationCommandValues {
  resource_kind: ResourceKind;
  resource_id: string;
  stable_revision_id: string;
  suite_revision_id: string;
  failure_summary: string;
}

export interface ExperimentCommandValues {
  resource_kind: ResourceKind;
  resource_id: string;
  stable_revision_id: string;
  candidate_revision_id: string;
  suite_revision_id: string;
}

export interface FeedbackCommandValues {
  trace_id: string;
  resource_id: string;
  score: number;
}

export const EvolutionCommandModal = ({ open, onClose, onOptimize, onExperiment, onFeedback }: {
  open: boolean;
  onClose: () => void;
  onOptimize: (values: OptimizationCommandValues) => Promise<void>;
  onExperiment: (values: ExperimentCommandValues) => Promise<void>;
  onFeedback: (values: FeedbackCommandValues) => Promise<void>;
}) => {
  const [optimizationForm] = Form.useForm<OptimizationCommandValues>();
  const [experimentForm] = Form.useForm<ExperimentCommandValues>();
  const [feedbackForm] = Form.useForm<FeedbackCommandValues>();
  const [loading, setLoading] = useState(false);
  const [optimizationPick, setOptimizationPick] = useState<SuitePick | null>(null);
  const [experimentPick, setExperimentPick] = useState<SuitePick | null>(null);
  const optimizationKind = Form.useWatch('resource_kind', optimizationForm) as ResourceKind | undefined;
  const experimentKind = Form.useWatch('resource_kind', experimentForm) as ResourceKind | undefined;

  // 每次打开重置表单与套件选择，避免重开残留上一次命令的输入。
  useEffect(() => {
    if (!open) return;
    optimizationForm.resetFields();
    experimentForm.resetFields();
    feedbackForm.resetFields();
    setOptimizationPick(null);
    setExperimentPick(null);
  }, [open, optimizationForm, experimentForm, feedbackForm]);

  const submit = async <T,>(form: ReturnType<typeof Form.useForm<T>>[0], action: (values: T) => Promise<void>,
    enrich: (values: T) => T) => {
    const values = await form.validateFields();
    setLoading(true);
    try {
      await action(enrich(values));
      form.resetFields();
      onClose();
    } catch {
      // The page owns the persistent error notification; keep the form open for correction.
    } finally {
      setLoading(false);
    }
  };

  const resourceFields = <>
    <Form.Item name="resource_kind" label="资源类型" rules={[{ required: true, message: '请选择资源类型' }]}>
      <Select aria-label="资源类型" options={resourceOptions} />
    </Form.Item>
    <Form.Item name="resource_id" label="资源 ID" rules={[{ required: true, message: '请输入资源 ID' }]}>
      <Input aria-label="资源 ID" />
    </Form.Item>
  </>;

  const suiteSection = (kind: ResourceKind | undefined, pick: SuitePick | null,
    onChange: (pick: SuitePick | null) => void) => (
    <Form.Item label="评测套件" required extra="演化命令必须运行在评测套件已发布的版本上。">
      {kind
        ? <SuitePicker resourceKind={kind} value={pick} onChange={onChange} />
        : <Typography.Text type="secondary">请先选择资源类型，再选择已发布评测套件。</Typography.Text>}
    </Form.Item>
  );

  return <Modal title="进化操作" open={open} onCancel={onClose} footer={null} destroyOnHidden width={640}>
    <Tabs items={[
      { key: 'optimization', label: '生成优化候选', children: <Form form={optimizationForm} layout="vertical">
        {resourceFields}
        <Form.Item name="stable_revision_id" label="稳定 Revision ID"
          rules={[{ required: true, message: '请输入稳定 Revision ID' }]}>
          <Input aria-label="稳定 Revision ID" />
        </Form.Item>
        {suiteSection(optimizationKind, optimizationPick, setOptimizationPick)}
        <Form.Item name="failure_summary" label="失败摘要" rules={[{ required: true, message: '请输入失败摘要' }]}>
          <Input.TextArea aria-label="失败摘要" rows={3} />
        </Form.Item>
        <Button type="primary" loading={loading} disabled={!optimizationPick?.revisionId}
          onClick={() => void submit(optimizationForm, onOptimize,
            (values) => ({ ...values, suite_revision_id: optimizationPick?.revisionId ?? '' }))}>生成候选</Button>
      </Form> },
      { key: 'experiment', label: '创建金丝雀', children: <Form form={experimentForm} layout="vertical">
        {resourceFields}
        <Form.Item name="stable_revision_id" label="稳定 Revision ID"
          rules={[{ required: true, message: '请输入稳定 Revision ID' }]}>
          <Input aria-label="稳定 Revision ID" />
        </Form.Item>
        <Form.Item name="candidate_revision_id" label="候选 Revision ID"
          rules={[{ required: true, message: '请输入候选 Revision ID' }]}>
          <Input aria-label="候选 Revision ID" />
        </Form.Item>
        {suiteSection(experimentKind, experimentPick, setExperimentPick)}
        <Button type="primary" loading={loading} disabled={!experimentPick?.revisionId}
          onClick={() => void submit(experimentForm, onExperiment,
            (values) => ({ ...values, suite_revision_id: experimentPick?.revisionId ?? '' }))}>创建金丝雀</Button>
      </Form> },
      { key: 'feedback', label: '记录反馈', children: <Form form={feedbackForm} layout="vertical">
        <Form.Item name="trace_id" label="Trace ID" rules={[{ required: true, message: '请输入 Trace ID' }]}>
          <Input aria-label="Trace ID" />
        </Form.Item>
        <Form.Item name="resource_id" label="资源 ID" rules={[{ required: true, message: '请输入资源 ID' }]}>
          <Input aria-label="反馈资源 ID" />
        </Form.Item>
        <Form.Item name="score" label="分数" rules={[{ required: true, message: '请输入分数' }]}>
          <InputNumber aria-label="分数" min={0} max={1} step={0.1} style={{ width: '100%' }} />
        </Form.Item>
        <Button type="primary" loading={loading}
          onClick={() => void submit(feedbackForm, onFeedback, (values) => values)}>提交反馈</Button>
      </Form> },
    ]} />
  </Modal>;
};
