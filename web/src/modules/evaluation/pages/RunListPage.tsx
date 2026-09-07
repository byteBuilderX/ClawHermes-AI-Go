import { Alert, Button, Empty, Flex, Select, Space, Table, Tag, Typography, message } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { CreateEvaluationModal } from '../components/CreateEvaluationModal';
import { StatusTag, displayLabel, kindFilterOptions, runDisplayStatus } from '../components/evaluationView';
import { useCreateEvaluation } from '../hooks/useCreateEvaluation';
import { useRegisteredResources } from '../hooks/useRegisteredResources';
import { useRunsPage } from '../hooks/useRunsPage';
import type { CreateEvaluationPlan, ResourceKind, RunSummary } from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

const runStatusOptions = ['queued', 'running', 'succeeded', 'failed', 'cancelled']
  .map((value) => ({ value, label: displayLabel(value) }));

export const RunListPage = () => {
  const navigate = useNavigate();
  const [kind, setKind] = useState<ResourceKind | undefined>();
  const [status, setStatus] = useState<string | undefined>();
  const [createOpen, setCreateOpen] = useState(false);
  const filtered = !!kind || !!status;
  const { runs, loading, error, reload } = useRunsPage({ resource_kind: kind ?? 'agent,knowledge', status });
  const { resources: registeredResources } = useRegisteredResources();
  const { createEvaluation, canManageEvaluation } = useCreateEvaluation();

  // 提交失败向上抛（保留 Modal 打开供修正/重试），成功后关框刷新运行列表。
  const handleCreate = async (plan: CreateEvaluationPlan) => {
    try {
      await createEvaluation(plan);
      message.success({ content: '评测已创建并进入运行队列', duration: 2 });
      reload();
    } catch (err) {
      message.error({ content: extractErrorMessage(err, '创建评测失败'), duration: 3 });
      throw err;
    }
  };

  const columns: ColumnsType<RunSummary> = [
    { title: '资源', dataIndex: 'resource_id', ellipsis: true },
    { title: '类型', dataIndex: 'resource_kind', width: 92, render: (value: string) => <Tag>{displayLabel(value)}</Tag> },
    { title: '锚定版本', dataIndex: 'revision_id', ellipsis: true },
    { title: '状态', dataIndex: 'status', width: 108,
      render: (_: unknown, row: RunSummary) => <StatusTag value={runDisplayStatus(row.status, row.passed)} /> },
    { title: '通过用例', width: 96, render: (_: unknown, row: RunSummary) =>
      `${row.passed_cases} / ${row.total_cases}` },
    { title: '创建时间', dataIndex: 'created_at', width: 176 },
    { title: '操作', width: 76, render: (_, row) => <Button type="link" size="small"
      onClick={() => navigate(`/evaluations/runs/${encodeURIComponent(row.id)}`)}>详情</Button> },
  ];

  return <div>
    <Flex justify="space-between" align="end" gap={16} wrap style={{ marginBottom: 12 }}>
      <div><Typography.Title level={4} style={{ margin: 0 }}>离线运行</Typography.Title>
        <Typography.Text type="secondary">逐次观察锚定资源版本的离线评测运行证据</Typography.Text></div>
      <Space wrap>
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
          value={kind} onChange={(value?: ResourceKind) => setKind(value)} />
        <Select aria-label="运行状态" allowClear placeholder="运行状态" style={{ width: 132 }} options={runStatusOptions}
          value={status} onChange={setStatus} />
        {canManageEvaluation && <Button type="primary" onClick={() => setCreateOpen(true)}>新建评测</Button>}
      </Space>
    </Flex>
    {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }}
      action={<Button onClick={() => void reload()}>重试</Button>} />}
    <Table<RunSummary> size="small" rowKey="id" dataSource={runs} loading={loading} pagination={false} columns={columns}
      locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
        description={filtered ? '没有找到符合条件的离线运行记录' : '离线运行记录还是空的'} /> }} />
    <CreateEvaluationModal open={createOpen} resources={registeredResources} onClose={() => setCreateOpen(false)}
      onSubmit={(plan) => void handleCreate(plan)} />
  </div>;
};
