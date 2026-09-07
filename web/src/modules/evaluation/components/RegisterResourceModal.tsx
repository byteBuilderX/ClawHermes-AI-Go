import { Alert, Button, Form, Modal, Select, Typography, message } from 'antd';
import { useEffect, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import { registrableResourceKinds } from '../model/evaluation';
import type { RegistrableResourceKind } from '../model/evaluation';

import { displayLabel } from './evaluationView';

import { agentApi } from '@/modules/agent/api/agent.api';
import { knowledgeApi } from '@/modules/knowledge/api/knowledge.api';
import { extractErrorMessage } from '@/shared/lib/errorMessage';

type Kind = RegistrableResourceKind;

interface ResourceOption { value: string; label: string }

interface RegisteredRow {
  kind: Kind;
  resource_id: string;
}

interface RegisterResourceModalProps {
  open: boolean;
  /** URL 深链初值（如 knowledge 详情页「检索质量评测」跳转预填）。 */
  initial?: { kind: Kind; resource_id?: string };
  /** 中心当前已登记的同轨资源（kind+resource_id），用于提示「重新建档刷新稳定版本」。 */
  registered: RegisteredRow[];
  onClose: () => void;
  onRegistered: (kind: Kind, resourceId: string) => void;
  /** 提供时渲染「登记并新建评测」快捷：登记成功后关闭本框并让调用方打开新建评测。 */
  onRegisterThenRun?: (kind: Kind, resourceId: string) => void;
}

// loadCandidates 按类型加载可登记对象：agent=线上 agent（id），knowledge=知识库
// workspace（name 即 ResourceID，与后端 knowledgeEvaluationAdapter 口径一致）。
// 纯 IO 选项加载，不引用对方页面/组件（沿用 evaluation→iam 等跨模块 API 引用先例）。
const loadCandidates = async (kind: Kind): Promise<ResourceOption[]> => {
  if (kind === 'agent') {
    const agents = await agentApi.list();
    return agents.map((agent) => ({ value: agent.id, label: agent.name || agent.id }));
  }
  const workspaces = await knowledgeApi.list();
  return workspaces.map((workspace) => ({ value: workspace.name, label: workspace.name }));
};

export const RegisterResourceModal = ({
  open, initial, registered, onClose, onRegistered, onRegisterThenRun,
}: RegisterResourceModalProps) => {
  const [form] = Form.useForm<{ kind: Kind; resource_id?: string }>();
  const kind = Form.useWatch('kind', form) ?? 'agent';
  const resourceId = Form.useWatch('resource_id', form);
  const [options, setOptions] = useState<ResourceOption[]>([]);
  const [optionLoading, setOptionLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const loadGenerationRef = useRef(0);

  // 每次打开重置表单并套用 URL 初值；agent 为默认类型。
  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue({ kind: initial?.kind ?? 'agent', resource_id: initial?.resource_id });
  }, [open, initial, form]);

  // 类型切换时加载对应可登记对象；只读打开期加载，错误不打断登记入口。
  useEffect(() => {
    if (!open) return;
    const generation = loadGenerationRef.current + 1;
    loadGenerationRef.current = generation;
    setOptionLoading(true);
    setOptions([]);
    loadCandidates(kind)
      .then((next) => { if (loadGenerationRef.current === generation) setOptions(next); })
      .catch((error: unknown) => {
        if (loadGenerationRef.current === generation) {
          message.error({ content: error instanceof Error ? error.message : '加载候选资源失败', duration: 3 });
        }
      })
      .finally(() => { if (loadGenerationRef.current === generation) setOptionLoading(false); });
  }, [open, kind]);

  const alreadyRegistered = registered.some((row) => row.kind === kind && row.resource_id === resourceId);
  const run = async (after: (kind: Kind, resourceId: string) => void) => {
    const values = await form.validateFields();
    if (!values.resource_id) {
      message.error({ content: '请选择要登记的资源', duration: 3 });
      return;
    }
    setSubmitting(true);
    try {
      await evaluationApi.createBaseline(values.kind, values.resource_id);
      message.success({ content: '资源已登记稳定版本', duration: 2 });
      after(values.kind, values.resource_id);
    } catch (error) {
      // 建档失败（如被测 Agent 缺 system_prompt 返回 422）要展示后端 `.error` 中文，
      // 而非 axios 的 "Request failed with status code xxx"。
      message.error({ content: extractErrorMessage(error, '登记失败'), duration: 3 });
    } finally {
      setSubmitting(false);
    }
  };

  return <Modal open={open} title="登记被测资源" onCancel={onClose} footer={[
    <Button key="cancel" disabled={submitting} onClick={onClose}>取消</Button>,
    ...(onRegisterThenRun ? [<Button key="then-run" loading={submitting}
      onClick={() => void run(onRegisterThenRun)}>登记并新建评测</Button>] : []),
    <Button key="submit" type="primary" loading={submitting} onClick={() => void run(onRegistered)}>登记</Button>,
  ]}>
    <Form form={form} layout="vertical">
      <Form.Item label="被测类型" name="kind" rules={[{ required: true }]}>
        <Select aria-label="被测类型" options={registrableResourceKinds.map((value) => (
          { value, label: displayLabel(value) }))} />
      </Form.Item>
      <Form.Item label="资源" name="resource_id">
        <Select aria-label="被测资源" showSearch optionFilterProp="label" placeholder="选择要登记的资源"
          loading={optionLoading} options={options} />
      </Form.Item>
    </Form>
    {alreadyRegistered && resourceId && <Alert type="info" showIcon
      message="该资源已登记稳定版本；再次登记会把其当前发布版本设为新的稳定基线。" />}
    <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>
      建档会固定被测资源当前发布版本；agent 评测会一并锚定其绑定的 skill/mcp/知识库版本。
    </Typography.Paragraph>
  </Modal>;
};
