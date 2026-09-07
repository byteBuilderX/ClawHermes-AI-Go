import { ExperimentOutlined } from '@ant-design/icons';
import { Alert, Button, Empty, Flex, Select, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import { CandidateDrawer } from '../components/CandidateDrawer';
import { CandidateEvaluationModal } from '../components/CandidateEvaluationModal';
import { EvolutionCommandModal, type ExperimentCommandValues, type FeedbackCommandValues,
  type OptimizationCommandValues } from '../components/EvolutionCommandModal';
import { ExperimentDrawer } from '../components/ExperimentDrawer';
import { StatusTag, displayLabel, kindFilterOptions } from '../components/evaluationView';
import { useEvolutionPage } from '../hooks/useEvolutionPage';
import type { CandidateSummary, ExperimentSummary, ResourceKind } from '../model/evaluation';

import { useTenantRole } from '@/modules/iam';
import { useResponsive } from '@/shared/hooks';
import { createIdempotencyKey } from '@/shared/lib/idempotencyKey';

// command 组装状态机命令：expected_state_version 乐观锁 + 单次幂等键，决定语意由
// 页面调用方通过 reason 表达（与 baseline hub 一致）。
const command = (version: number, reason: string) => ({ reason, expected_state_version: version,
  idempotency_key: createIdempotencyKey() });

export const EvolutionPage = () => {
  const { isMobile } = useResponsive();
  const { isAdmin } = useTenantRole();
  const canManage = isAdmin;
  const [kind, setKind] = useState<ResourceKind | undefined>();
  const { candidates, experiments, loading, error, reload } = useEvolutionPage({ resource_kind: kind ?? 'agent,knowledge' });
  const [candidateId, setCandidateId] = useState('');
  const [experimentId, setExperimentId] = useState('');
  const [evolutionOpen, setEvolutionOpen] = useState(false);
  const [candidateEvaluationOpen, setCandidateEvaluationOpen] = useState(false);
  const candidate = candidates.find((item) => item.id === candidateId) || null;
  const experiment = experiments.find((item) => item.id === experimentId) || null;

  // decide 收敛命令副作用：执行 → 刷新列表 → 成功/失败 toast（镜像 hub managedCommand）。
  const decide = async (operation: () => Promise<unknown>, success: string) => {
    if (!canManage) throw new Error('仅租户管理员可执行评测命令');
    try {
      await operation();
      await reload();
      message.success({ content: success, duration: 2 });
    } catch (err) {
      message.error({ content: err instanceof Error ? err.message : '操作失败', duration: 3 });
    }
  };

  const candidateColumns: ColumnsType<CandidateSummary> = [
    { title: '资源', dataIndex: 'resource_id', ellipsis: true },
    { title: '类型', dataIndex: 'resource_kind', width: 92, render: (value: string) => <Tag>{displayLabel(value)}</Tag> },
    { title: '候选版本', dataIndex: 'revision_id', ellipsis: true },
    { title: '来源', dataIndex: 'source', width: 120 },
    { title: '状态', dataIndex: 'status', width: 108, render: (value: string) => <StatusTag value={value} /> },
    { title: '创建时间', dataIndex: 'created_at', width: 176 },
    { title: '操作', width: 76, render: (_, row) => <Button type="link" size="small"
      onClick={() => setCandidateId(row.id)}>详情</Button> },
  ];
  const experimentColumns: ColumnsType<ExperimentSummary> = [
    { title: '资源', dataIndex: 'resource_id', ellipsis: true },
    { title: '类型', dataIndex: 'resource_kind', width: 92, render: (value: string) => <Tag>{displayLabel(value)}</Tag> },
    { title: '稳定版本', dataIndex: 'stable_revision_id', ellipsis: true },
    { title: '候选版本', dataIndex: 'canary_revision_id', ellipsis: true },
    { title: '状态', dataIndex: 'status', width: 108, render: (value: string) => <StatusTag value={value} /> },
    { title: '流量', dataIndex: 'stage_percent', width: 72, render: (value: number) => `${value}%` },
    { title: '创建时间', dataIndex: 'created_at', width: 176 },
    { title: '操作', width: 76, render: (_, row) => <Button type="link" size="small"
      onClick={() => setExperimentId(row.id)}>详情</Button> },
  ];

  return <div>
    <Flex justify="space-between" align="end" gap={16} wrap style={{ marginBottom: 12 }}>
      <div><Typography.Title level={4} style={{ margin: 0 }}>自进化工作区</Typography.Title>
        <Typography.Text type="secondary">从安全差异生成候选，经金丝雀观测证据决定晋级或回滚</Typography.Text></div>
      <Space wrap>
        {canManage && <Button icon={<ExperimentOutlined />} onClick={() => setEvolutionOpen(true)}>进化操作</Button>}
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
          value={kind} onChange={(value?: ResourceKind) => setKind(value)} />
      </Space>
    </Flex>
    {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }}
      action={<Button onClick={() => void reload()}>重试</Button>} />}
    <Tabs items={[
      { key: 'candidates', label: `候选版本 ${candidates.length}`, children: <Table<CandidateSummary> size="small"
        rowKey="id" dataSource={candidates} loading={loading} pagination={false} columns={candidateColumns}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="候选版本还是空的（可先发起自进化优化生成）" /> }} /> },
      { key: 'experiments', label: `金丝雀实验 ${experiments.length}`, children: <Table<ExperimentSummary> size="small"
        rowKey="id" dataSource={experiments} loading={loading} pagination={false} columns={experimentColumns}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="金丝雀实验还是空的（候选通过离线评测后可发起）" /> }} /> },
    ]} />
    <CandidateDrawer candidate={candidate} open={!!candidate} onClose={() => setCandidateId('')}
      canManage={canManage} isMobile={isMobile}
      onReject={(value) => void decide(() => evaluationApi.rejectCandidate(value.id,
        command(value.state_version, '管理员拒绝候选版本')), '候选版本已拒绝')}
      onEvaluate={() => setCandidateEvaluationOpen(true)} />
    <CandidateEvaluationModal open={candidateEvaluationOpen} onClose={() => setCandidateEvaluationOpen(false)}
      resourceKind={candidate?.resource_kind ?? 'agent'}
      onSubmit={async (suiteRevisionId, idempotencyKey) => {
        if (!candidate) throw new Error('候选版本已不可用');
        try {
          await evaluationApi.enqueueRun({ kind: candidate.resource_kind, resource_id: candidate.resource_id,
            revision_id: candidate.revision_id }, suiteRevisionId, idempotencyKey);
          await reload();
          message.success({ content: '候选离线评测已进入运行队列', duration: 2 });
        } catch (err) {
          message.error({ content: err instanceof Error ? err.message : '候选离线评测启动失败', duration: 3 });
          throw err;
        }
      }} />
    <ExperimentDrawer experiment={experiment} open={!!experiment} onClose={() => setExperimentId('')}
      canManage={canManage} isMobile={isMobile}
      onPause={(value) => void decide(() => evaluationApi.pauseExperiment(value.id,
        command(value.state_version, '管理员暂停实验')), '实验已暂停')}
      onPromote={(value) => void decide(() => evaluationApi.promoteExperiment(value.id,
        command(value.state_version, '管理员晋级实验')), '实验已晋级')}
      onRollback={(value) => void decide(() => evaluationApi.rollbackExperiment(value.id,
        command(value.state_version, '管理员回滚实验')), '实验已回滚')} />
    <EvolutionCommandModal open={evolutionOpen} onClose={() => setEvolutionOpen(false)}
      onOptimize={async (values: OptimizationCommandValues) => {
        try {
          await evaluationApi.generateOptimization({
            baseline: { kind: values.resource_kind, resource_id: values.resource_id,
              revision_id: values.stable_revision_id },
            suiteRevisionId: values.suite_revision_id,
            searchSpace: {}, failureSummaries: [values.failure_summary], idempotencyKey: createIdempotencyKey(),
          });
          await reload();
          message.success({ content: '优化候选已生成', duration: 2 });
        } catch (err) {
          message.error({ content: err instanceof Error ? err.message : '生成优化候选失败', duration: 3 });
          throw err;
        }
      }}
      onExperiment={async (values: ExperimentCommandValues) => {
        try {
          const resource = { kind: values.resource_kind, resource_id: values.resource_id };
          await evaluationApi.createExperiment(
            { ...resource, revision_id: values.stable_revision_id },
            { ...resource, revision_id: values.candidate_revision_id },
            values.suite_revision_id,
          );
          await reload();
          message.success({ content: '金丝雀实验已创建', duration: 2 });
        } catch (err) {
          message.error({ content: err instanceof Error ? err.message : '创建金丝雀失败', duration: 3 });
          throw err;
        }
      }}
      onFeedback={async (values: FeedbackCommandValues) => {
        try {
          await evaluationApi.recordFeedback({ resourceKind: values.resource_kind, traceId: values.trace_id,
            resourceId: values.resource_id, score: values.score, outcome: { source: 'manual' },
            idempotencyKey: createIdempotencyKey() });
          await reload();
          message.success({ content: '反馈已记录', duration: 2 });
        } catch (err) {
          message.error({ content: err instanceof Error ? err.message : '记录反馈失败', duration: 3 });
          throw err;
        }
      }} />
  </div>;
};
