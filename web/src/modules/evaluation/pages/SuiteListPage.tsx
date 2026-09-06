import { PlusOutlined } from '@ant-design/icons';
import { Alert, Button, Empty, Flex, Modal, Space, Table, Tag, Typography, message } from 'antd';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { evaluationApi } from '../api/evaluation.api';
import { CreateSuiteModal, type CreateSuiteValues } from '../components/CreateSuiteModal';
import { sessionFromForm } from '../components/SessionScriptFields';
import { displayLabel, StatusTag } from '../components/evaluationView';
import type { SuiteSummary } from '../model/evaluation';

import { useAuth, useTenantRole } from '@/modules/iam';
import { extractErrorMessage } from '@/shared/lib';

// processFieldsToSpec 把表单过程字段映射为 case 载荷（§6.5）：任一工具断言
// 字段存在时生成 tool_spec，否则 undefined；step_criteria 存在时生成 step_judge。
const processFieldsToSpec = (values: {
  must_call?: string[]; must_not_call?: string[]; tool_order?: string[]; max_calls?: number; step_criteria?: string;
}) => ({
  tool_spec: (values.must_call?.length || values.must_not_call?.length || values.tool_order?.length || values.max_calls)
    ? { must_call: values.must_call, must_not_call: values.must_not_call, order: values.tool_order, max_calls: values.max_calls }
    : undefined,
  step_judge: values.step_criteria ? { criteria: values.step_criteria } : undefined,
});

// activeVersionLabel / draftCaseLabel 把列表行的版本号与用例计数转成展示文案。
const activeVersionLabel = (suite: SuiteSummary) => (
  suite.active_version_no ? `v${suite.active_version_no} · ${suite.active_case_count ?? 0} 个启用用例` : '尚未发布'
);
const draftCaseLabel = (suite: SuiteSummary) => (
  suite.draft_revision_id ? `${suite.draft_case_count ?? 0} 个用例` : '—'
);

export const SuiteListPage = () => {
  const navigate = useNavigate();
  const { user } = useAuth();
  const { isAdmin, isOwner } = useTenantRole();
  const canManage = isAdmin;
  // 删除可见性：owner 恒可删；创建者可删；其余不可删（与后端 fail-closed 一致）。
  const canDelete = (createdBy?: string) => isOwner || (!!createdBy && createdBy === user?.id);
  const [items, setItems] = useState<SuiteSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const mountedRef = useRef(true);
  const requestGenerationRef = useRef(0);

  const load = useCallback(async () => {
    if (!mountedRef.current) return;
    const generation = requestGenerationRef.current + 1;
    requestGenerationRef.current = generation;
    setLoading(true);
    setError('');
    try {
      const page = await evaluationApi.listSuites();
      if (mountedRef.current && generation === requestGenerationRef.current) setItems(page.items);
    } catch (err) {
      if (mountedRef.current && generation === requestGenerationRef.current) {
        setError(extractErrorMessage(err) || '加载评测集失败');
      }
    } finally {
      if (mountedRef.current && generation === requestGenerationRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; requestGenerationRef.current += 1; };
  }, []);

  useEffect(() => { void load(); }, [load]);

  const openDetail = (id: string) => navigate(`/evaluations/suites/${id}`);

  // 新建评测集：成功后提示、刷新并进入详情页继续维护用例。CreateSuiteModal 负责
  // 失败时保持表单打开（父级抛错阻止其 close），成功路径在这里不抛错。
  const createSuite = async (values: CreateSuiteValues) => {
    try {
      const session = sessionFromForm(values);
      const created = await evaluationApi.createSuite({
        name: values.name,
        description: values.description || undefined,
        resourceKind: values.resource_kind,
        cases: [{
          name: values.case_name, expected_output: values.expected_output,
          assertion_mode: values.assertion_mode,
          judge_spec: values.assertion_mode === 'judge'
            ? { model: values.judge_model, rubric: values.judge_rubric } : undefined,
          enabled: values.enabled, ...processFieldsToSpec(values),
          ...(session ? { session } : { input: values.input }),
        }],
      });
      message.success({ content: '评测集已创建', duration: 2 });
      await load();
      navigate(`/evaluations/suites/${created.suite.id}`);
    } catch (error) {
      message.error({ content: extractErrorMessage(error) || '创建评测集失败', duration: 3 });
      throw error;
    }
  };

  const confirmDelete = (row: SuiteSummary) => {
    Modal.confirm({
      title: `删除评测集「${row.name}」？`,
      okText: '删除', okButtonProps: { danger: true }, cancelText: '取消',
      onOk: async () => {
        try {
          await evaluationApi.deleteSuite(row.id);
          await load();
          message.success({ content: '已删除', duration: 2 });
        } catch (error) {
          message.error({ content: extractErrorMessage(error) || '删除失败', duration: 3 });
          throw error;
        }
      },
    });
  };

  return <div>
    <Flex justify="space-between" align="end" gap={16} wrap style={{ marginBottom: 12 }}>
      <div><Typography.Title level={4} style={{ margin: 0 }}>评测集</Typography.Title>
        <Typography.Text type="secondary">集中维护评测用例草稿与发布版本</Typography.Text></div>
      {canManage && <Button type="primary" icon={<PlusOutlined />}
        onClick={() => setCreateOpen(true)}>新建评测集</Button>}
    </Flex>
    {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }}
      action={<Button onClick={() => void load()}>重试</Button>} />}
    <Table<SuiteSummary> size="small" rowKey="id" dataSource={items} loading={loading} pagination={false}
      locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
        description={canManage ? '还没有评测集，新建后即可编写用例并发布' : '评测集还是空的'} /> }}
      columns={[
        { title: '名称', dataIndex: 'name', ellipsis: true,
          render: (value: string, row: SuiteSummary) => <Button type="link" size="small" style={{ padding: 0 }}
            onClick={() => openDetail(row.id)}>{value}</Button> },
        { title: '类型', dataIndex: 'resource_kind', width: 100,
          render: (_: unknown, row: SuiteSummary) => (row.resource_kind ? <Tag>{displayLabel(row.resource_kind)}</Tag> : '—') },
        { title: '状态', dataIndex: 'status', width: 110, render: (value: string) => <StatusTag value={value} /> },
        { title: '当前版本', width: 200, render: (_: unknown, row: SuiteSummary) => activeVersionLabel(row) },
        { title: '草稿', width: 100, render: (_: unknown, row: SuiteSummary) => draftCaseLabel(row) },
        { title: '创建时间', dataIndex: 'created_at', width: 180 },
        { title: '创建者', dataIndex: 'created_by', width: 120, render: (value?: string) => value || '—' },
        { title: '操作', width: 140, render: (_: unknown, row: SuiteSummary) => <Space>
          <Button type="link" size="small" onClick={() => openDetail(row.id)}>详情</Button>
          {canDelete(row.created_by) && <Button type="link" size="small" danger
            onClick={() => confirmDelete(row)}>删除</Button>}
        </Space> },
      ]} />
    <CreateSuiteModal open={createOpen} onClose={() => setCreateOpen(false)} onSubmit={createSuite} />
  </div>;
};
