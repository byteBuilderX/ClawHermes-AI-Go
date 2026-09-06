import { AppstoreOutlined, ExperimentOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, Button, Drawer, Empty, Flex, Modal, Select, Skeleton, Space, Table, Tabs, Typography, message } from 'antd';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';

import { evaluationApi } from '../api/evaluation.api';
import { CandidateDrawer } from '../components/CandidateDrawer';
import { CandidateEvaluationModal } from '../components/CandidateEvaluationModal';
import { CreateEvaluationModal } from '../components/CreateEvaluationModal';
import { EvaluationMonitorPanel } from '../components/EvaluationMonitorPanel';
import { EvaluationOverview } from '../components/EvaluationOverview';
import { EvolutionCommandModal } from '../components/EvolutionCommandModal';
import { ExperimentDrawer } from '../components/ExperimentDrawer';
import { RegisterResourceModal } from '../components/RegisterResourceModal';
import { ResourceTable } from '../components/ResourceTable';
import ReviewPoolPanel from '../components/ReviewPoolPanel';
import { RunDrawer } from '../components/RunDrawer';
import { RuntimeHealthTrendPanel } from '../components/RuntimeHealthTrendPanel';
import { SuitesPanel } from '../components/SuitesPanel';
import { TimelineDrawer } from '../components/TimelineDrawer';
import { StatusTag, displayLabel, drawerWidth, kindFilterOptions, runDisplayStatus } from '../components/evaluationView';
import { useEvaluationCenter } from '../hooks/useEvaluationCenter';
import { useEvaluationTimeline } from '../hooks/useEvaluationTimeline';
import { resourceKindSchema } from '../model/evaluation';
import type { CandidateSummary, CenterKindFilter, ExperimentSummary,
  RegistrableResourceKind, ResourceKind, RunSummary } from '../model/evaluation';

import { useResponsive } from '@/shared/hooks';
import { extractErrorMessage } from '@/shared/lib';
import { createIdempotencyKey } from '@/shared/lib/idempotencyKey';

const statusOptions = ['active', 'proposed', 'promoted', 'running', 'succeeded', 'failed', 'paused'].map((value) => ({ value, label: displayLabel(value) }));
const toRegistrableKind = (value: ResourceKind | undefined): RegistrableResourceKind | undefined =>
  value === 'agent' || value === 'knowledge' ? value : undefined;
const command = (version: number, reason: string) => ({ reason, expected_state_version: version,
  idempotency_key: createIdempotencyKey() });

export const EvaluationCenterPage = () => {
  const navigate = useNavigate();
  const { isMobile } = useResponsive();
  const [searchParams, setSearchParams] = useSearchParams();
  const parsedKind = resourceKindSchema.safeParse(searchParams.get('kind'));
  const kind = parsedKind.success ? parsedKind.data : undefined;
  const filterResourceId = searchParams.get('resource_id')?.trim() || undefined;
  // 被测收敛后中心默认并列 agent+knowledge 两轨：未显式选 kind 时以 CSV 聚合两轨；
  // 显式选历史单值（skill/mcp）或单轨时以单值只读读回。
  const centerKind: CenterKindFilter = kind ?? 'agent,knowledge';
  const [status, setStatus] = useState<string | undefined>();
  const center = useEvaluationCenter({ resource_kind: centerKind, resource_id: filterResourceId, status });
  const [resourceId, setResourceId] = useState('');
  const [runId, setRunId] = useState('');
  const [candidateId, setCandidateId] = useState('');
  const [experimentId, setExperimentId] = useState('');
  const timeline = useEvaluationTimeline();
  const [createOpen, setCreateOpen] = useState(false);
  const [evolutionOpen, setEvolutionOpen] = useState(false);
  const [candidateEvaluationOpen, setCandidateEvaluationOpen] = useState(false);
  const [registerOpen, setRegisterOpen] = useState(false);
  // URL 深链 / 外部入口预置的登记初值（kind+resource_id），供 RegisterResourceModal 预填。
  const [registerInitial, setRegisterInitial] = useState<{ kind: RegistrableResourceKind; resource_id?: string }>();
  // 「登记并新建评测」流程预选的目标资源，交由 CreateEvaluationModal focus 消费。
  const [createFocus, setCreateFocus] = useState<{ kind: ResourceKind; resource_id: string }>();
  const resource = useMemo(() => center.resources.items.find((item) => item.id === resourceId) || null,
    [center.resources.items, resourceId]);
  const run = center.runs.items.find((item) => item.id === runId) || null;
  const candidate = center.candidates.items.find((item) => item.id === candidateId) || null;
  const experiment = center.experiments.items.find((item) => item.id === experimentId) || null;

  useEffect(() => { if (resourceId && !resource) setResourceId(''); }, [resource, resourceId]);
  const decide = async (action: () => Promise<unknown>, success: string) => {
    try { await action(); message.success({ content: success, duration: 2 }); }
    catch (error) { message.error({ content: error instanceof Error ? error.message : '操作失败', duration: 3 }); }
  };
  // confirmDelete 二次确认后执行删除（RBAC 后端 fail-closed：403 时提取后端
  // {"error":...} 文案展示）。删除成功后 managedCommand 已触发 center.reload()。
  const confirmDelete = (title: string, action: () => Promise<unknown>) => {
    Modal.confirm({
      title,
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          await action();
          message.success({ content: '已删除', duration: 2 });
        } catch (error) {
          message.error({ content: extractErrorMessage(error) || '删除失败', duration: 3 });
          throw error;
        }
      },
    });
  };
  const setKind = (value: ResourceKind | undefined) => {
    const next = new URLSearchParams(searchParams);
    if (value) next.set('kind', value); else next.delete('kind');
    setSearchParams(next);
  };

  // 登记入口支持 URL 深链 ?action=register&kind=<agent|knowledge>&resource_id=<id>（供
  // agent/知识库详情页跳转直达建档）：一次性消费后清除 action 参数避免刷新反复弹窗。
  useEffect(() => {
    if (searchParams.get('action') !== 'register') return;
    const parsed = resourceKindSchema.safeParse(searchParams.get('kind'));
    setRegisterInitial({
      kind: toRegistrableKind(parsed.success ? parsed.data : undefined) ?? 'agent',
      resource_id: searchParams.get('resource_id')?.trim() || undefined,
    });
    setRegisterOpen(true);
    const next = new URLSearchParams(searchParams);
    next.delete('action'); next.delete('kind'); next.delete('resource_id');
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);
  // 已登记的同轨资源（agent/knowledge），供登记框提示「重新建档刷新稳定版本」。
  const registeredRows = useMemo(() => center.resources.items
    .filter((item) => item.resource_kind === 'agent' || item.resource_kind === 'knowledge')
    .map((item) => ({ kind: item.resource_kind as RegistrableResourceKind, resource_id: item.resource_id })),
  [center.resources.items]);
  const closeRegister = () => setRegisterOpen(false);
  // 登记成功回调：刷新列表让新行出现；createBaseline 已弹成功提示。
  const handleRegistered = () => { setRegisterOpen(false); void center.reload(); };
  // 登记并新建评测：登记成功后预选该资源打开新建评测；focus 由 CreateEvaluationModal
  // 在资源列表刷新后消费。center.reload() 内部吞错（错误落 center.error），不阻断流程。
  const handleRegisterThenRun = (kind: RegistrableResourceKind, resourceId: string) => {
    setRegisterOpen(false);
    setCreateFocus({ kind, resource_id: resourceId });
    setCreateOpen(true);
    void center.reload();
  };

  if (center.loading && !center.overview) return <Skeleton active />;
  return <div>
    <Flex justify="space-between" align="end" gap={16} wrap style={{ marginBottom: 12 }}>
      <div><Typography.Title level={4} style={{ margin: 0 }}>评测与进化中心</Typography.Title>
        <Typography.Text type="secondary">在同一记录簿中审阅版本证据与演进决定</Typography.Text></div>
      <Space wrap>
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
          value={kind} onChange={setKind} />
        <Select aria-label="资源状态" allowClear placeholder="资源状态" style={{ width: 132 }} options={statusOptions}
          value={status} onChange={setStatus} />
        {center.canManageEvaluation && <>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => { setCreateFocus(undefined); setCreateOpen(true); }}>新建评测</Button>
          <Button onClick={() => { setRegisterInitial(undefined); setRegisterOpen(true); }}>登记被测资源</Button>
        </>}
      </Space>
    </Flex>
    <EvaluationOverview overview={center.overview} />
    {center.error && <Alert type="error" showIcon message={center.error} action={<Button onClick={center.reload}>重试</Button>} />}
    <ResourceTable resources={center.resources.items} loading={center.loading} filtered={!!kind || !!status} onOpen={(row) => setResourceId(row.id)} />
    <Tabs style={{ marginTop: 16 }} items={[
      { key: 'runs', label: `运行记录 ${center.runs.items.length}`, children: <CompactList rows={center.runs.items}
        empty="运行记录还是空的" onOpen={(row) => setRunId(row.id)}
        canDelete={(row) => center.canDeleteEntity(row.created_by)}
        onDelete={(row) => confirmDelete('删除该运行记录？', () => center.deleteRun(row.id))} /> },
      { key: 'candidates', label: `候选版本 ${center.candidates.items.length}`, children: <>
        {center.canManageEvaluation && <Button icon={<ExperimentOutlined />} style={{ marginBottom: 12 }}
          onClick={() => setEvolutionOpen(true)}>进化操作</Button>}
        <CompactList rows={center.candidates.items} empty="候选版本还是空的" onOpen={(row) => setCandidateId(row.id)}
          canDelete={(row) => center.canDeleteEntity(row.created_by)}
          onDelete={(row) => confirmDelete('删除该候选版本？', () => center.deleteCandidate(row.id))} />
      </> },
      { key: 'experiments', label: `金丝雀实验 ${center.experiments.items.length}`, children: <CompactList rows={center.experiments.items}
        empty="金丝雀实验还是空的" onOpen={(row) => setExperimentId(row.id)}
        canDelete={(row) => center.canDeleteEntity(row.created_by)}
        onDelete={(row) => confirmDelete('删除该金丝雀实验？', () => center.deleteExperiment(row.id))} /> },
      { key: 'suites', label: `套件 ${center.suites.items.length}`, children: <>
        {center.canManageEvaluation && <Button icon={<AppstoreOutlined />} style={{ marginBottom: 12 }}
          onClick={() => navigate('/evaluations/suites')}>管理评测集</Button>}
        <SuitesPanel suites={center.suites.items} loading={center.loading} canManage={center.canManageEvaluation}
          onOpen={(row) => navigate(`/evaluations/suites/${row.id}`)}
          canDelete={(row) => center.canDeleteEntity(row.created_by)}
          onDelete={(row) => confirmDelete(`删除套件「${row.name}」？`, () => center.deleteSuite(row.id))} />
      </> },
      { key: 'health', label: '运行通过率趋势', children: <RuntimeHealthTrendPanel key={`health-${kind ?? 'all'}-${filterResourceId ?? 'none'}`}
        defaultKind={kind} defaultResourceId={filterResourceId} /> },
      { key: 'monitor', label: '监控', children: <EvaluationMonitorPanel key={`monitor-${kind ?? 'all'}-${filterResourceId ?? 'none'}`}
        defaultKind={kind} defaultResourceId={filterResourceId} isMobile={isMobile} /> },
      { key: 'review', label: '人工评审池', children: <ReviewPoolPanel canDelete={(item) => center.canDeleteEntity(item.created_by)} /> },
    ]} />
    <Drawer title="资源详情" open={!!resource} onClose={() => setResourceId('')} width={drawerWidth(isMobile)} destroyOnHidden>
      {resource && <><Typography.Title level={5}>观测事实</Typography.Title>
        <Typography.Paragraph>资源：{resource.resource_id}</Typography.Paragraph>
        <Typography.Paragraph>稳定版本：{resource.stable_revision_id || '尚未建立'}</Typography.Paragraph>
        <StatusTag value={resource.status} /><Typography.Title level={5}>系统建议</Typography.Title>
        <Alert type="info" message="结合运行、候选与实验记录审阅此资源，不展示原始提示词或载荷。" />
        <Button style={{ marginTop: 16 }} onClick={() => void timeline.openTimeline(resource)}>查看时间线</Button></>}
    </Drawer>
    <RunDrawer run={run} open={!!run} onClose={() => setRunId('')} isMobile={isMobile} runs={center.runs.items} />
    <CandidateDrawer candidate={candidate} open={!!candidate} onClose={() => setCandidateId('')}
      canManage={center.canManageEvaluation} isMobile={isMobile} onReject={(value) => void decide(
        () => center.rejectCandidate(value.id, command(value.state_version, '管理员拒绝候选版本')), '候选版本已拒绝')}
      onEvaluate={() => setCandidateEvaluationOpen(true)} />
    <CandidateEvaluationModal open={candidateEvaluationOpen} onClose={() => setCandidateEvaluationOpen(false)}
      resourceKind={candidate?.resource_kind ?? 'agent'}
      onSubmit={async (suiteRevisionId, idempotencyKey) => {
        if (!candidate) throw new Error('候选版本已不可用');
        try {
          await evaluationApi.enqueueRun({ kind: candidate.resource_kind, resource_id: candidate.resource_id,
            revision_id: candidate.revision_id }, suiteRevisionId, idempotencyKey);
          await center.reload();
          message.success({ content: '候选离线评测已进入运行队列', duration: 2 });
        } catch (error) {
          message.error({ content: error instanceof Error ? error.message : '候选离线评测启动失败', duration: 3 });
          throw error;
        }
      }} />
    <ExperimentDrawer experiment={experiment} open={!!experiment} onClose={() => setExperimentId('')}
      canManage={center.canManageEvaluation} isMobile={isMobile}
      onPause={(value) => void decide(() => center.pauseExperiment(value.id, command(value.state_version, '管理员暂停实验')), '实验已暂停')}
      onPromote={(value) => void decide(() => center.promoteExperiment(value.id, command(value.state_version, '管理员晋级实验')), '实验已晋级')}
      onRollback={(value) => void decide(() => center.rollbackExperiment(value.id, command(value.state_version, '管理员回滚实验')), '实验已回滚')} />
    <TimelineDrawer events={timeline.events} open={timeline.open} loading={timeline.loading} error={timeline.error}
      isMobile={isMobile} onClose={timeline.closeTimeline} />
    <RegisterResourceModal open={registerOpen} initial={registerInitial} registered={registeredRows}
      onClose={closeRegister} onRegistered={handleRegistered} onRegisterThenRun={handleRegisterThenRun} />
    <CreateEvaluationModal open={createOpen} resources={center.resources.items} focusResource={createFocus} onClose={() => {
      center.resetCreateEvaluation(); setCreateFocus(undefined); setCreateOpen(false);
    }}
      onSubmit={async (plan) => {
        try {
          await center.createEvaluation(plan);
          message.success({ content: '评测已创建并进入运行队列', duration: 2 });
        } catch (error) {
          message.error({ content: error instanceof Error ? error.message : '创建评测失败', duration: 3 });
          throw error;
        }
      }} />
    <EvolutionCommandModal open={evolutionOpen} onClose={() => setEvolutionOpen(false)}
      onOptimize={async (values) => {
        try {
          await evaluationApi.generateOptimization({
            baseline: { kind: values.resource_kind, resource_id: values.resource_id,
              revision_id: values.stable_revision_id },
            suiteRevisionId: values.suite_revision_id,
            searchSpace: {}, failureSummaries: [values.failure_summary], idempotencyKey: crypto.randomUUID(),
          });
          await center.reload();
          message.success({ content: '优化候选已生成', duration: 2 });
        } catch (error) {
          message.error({ content: error instanceof Error ? error.message : '生成优化候选失败', duration: 3 });
          throw error;
        }
      }}
      onExperiment={async (values) => {
        try {
          const resource = { kind: values.resource_kind, resource_id: values.resource_id };
          await evaluationApi.createExperiment(
            { ...resource, revision_id: values.stable_revision_id },
            { ...resource, revision_id: values.candidate_revision_id },
            values.suite_revision_id,
          );
          await center.reload();
          message.success({ content: '金丝雀实验已创建', duration: 2 });
        } catch (error) {
          message.error({ content: error instanceof Error ? error.message : '创建金丝雀失败', duration: 3 });
          throw error;
        }
      }}
      onFeedback={async (values) => {
        try {
          await evaluationApi.recordFeedback({ traceId: values.trace_id, resourceId: values.resource_id,
            score: values.score, outcome: { source: 'manual' }, idempotencyKey: crypto.randomUUID() });
          await center.reload();
          message.success({ content: '反馈已记录', duration: 2 });
        } catch (error) {
          message.error({ content: error instanceof Error ? error.message : '记录反馈失败', duration: 3 });
          throw error;
        }
      }} />
  </div>;
};

const CompactList = <T extends RunSummary | CandidateSummary | ExperimentSummary>({ rows, empty, onOpen, canDelete, onDelete }: {
  rows: T[]; empty: string; onOpen: (row: T) => void;
  canDelete?: (row: T) => boolean; onDelete?: (row: T) => void;
}) => <Table<T> size="small" rowKey="id" dataSource={rows} pagination={false}
  locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={empty} /> }} columns={[
    { title: '记录', dataIndex: 'id', ellipsis: true },
    { title: '资源', dataIndex: 'resource_id', ellipsis: true },
    { title: '状态', dataIndex: 'status', width: 120, render: (value: string, row) => <StatusTag
      value={'passed' in row ? runDisplayStatus(value, row.passed) : value} /> },
    { title: '操作', width: 120, render: (_, row) => <Space>
      <Button type="link" size="small" onClick={() => onOpen(row)}>详情</Button>
      {canDelete?.(row) && onDelete && <Button type="link" size="small" danger onClick={() => onDelete(row)}>删除</Button>}
    </Space> },
  ]} />;
