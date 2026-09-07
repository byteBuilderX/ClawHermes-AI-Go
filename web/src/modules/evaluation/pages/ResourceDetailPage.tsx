import { ArrowLeftOutlined, ArrowRightOutlined } from '@ant-design/icons';
import { Alert, Button, Card, Empty, Flex, message, Select, Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import { evaluationApi } from '../api/evaluation.api';
import { HealthTrendChart, type HealthTrendPoint } from '../components/HealthTrendChart';
import { RegisterResourceModal } from '../components/RegisterResourceModal';
import { ResourceNameCell } from '../components/ResourceNameCell';
import { RevisionReferenceDrawer } from '../components/RevisionReferenceDrawer';
import { TimelinePanel } from '../components/TimelinePanel';
import { StatusTag, displayLabel, runDisplayStatus } from '../components/evaluationView';
import { useResourceDetailPage } from '../hooks/useResourceDetailPage';
import { resourceDisplayName } from '../lib/resourceName';
import type {
  CandidateSummary,
  ExperimentSummary,
  RegistrableResourceKind,
  ResourceKind,
  RevisionSummary,
  RunSummary,
} from '../model/evaluation';
import { registrableResourceKinds, resourceKindSchema } from '../model/evaluation';
import { pickVersionLabel, productEditPath } from '../model/productLinks';

import { useTenantRole } from '@/modules/iam';

const VALID_KINDS = resourceKindSchema.options;
const REGISTRABLE: readonly string[] = registrableResourceKinds;

const timeLabel = (value: string) => new Date(value).toLocaleString('zh-CN', {
  month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
});

export const ResourceDetailPage = () => {
  const params = useParams();
  const navigate = useNavigate();
  const { isAdmin } = useTenantRole();
  const canManage = isAdmin;
  const kind = VALID_KINDS.includes(params.kind as ResourceKind) ? params.kind as ResourceKind : undefined;
  const resourceId = params.id || '';
  const [registerOpen, setRegisterOpen] = useState(false);
  const { resource, runs, candidates, experiments, events, loading, error, reload } =
    useResourceDetailPage({ resourceKind: kind, resourceId });
  const registrable = kind !== undefined && REGISTRABLE.includes(kind);
  // 页头主文案用真名（建档后后端下发 resource_name / safe_summary）；未建档时无名称来源，
  // 退回 URL 中的资源 id 作身份标识（登记 CTA 语境）。真名与 id 不同时把 id 降级为弱化 code。
  const pageTitle = resource ? resourceDisplayName(resource) : resourceId;
  const [revisions, setRevisions] = useState<RevisionSummary[]>([]);
  const [revisionsLoading, setRevisionsLoading] = useState(false);
  const [revisionsError, setRevisionsError] = useState('');
  const [revisionTick, setRevisionTick] = useState(0);
  const [analyze, setAnalyze] = useState<{ revisionId: string; revisionLabel: string } | null>(null);
  // stableRevisionId 供头部「引用分析」直接作用于当前稳定版本；登记后 reload 会把 resource
  // 换成新对象，effect 依赖 resource 使账本在 (重新)建档后自动重取。
  const stableRevisionId = resource?.stable_revision_id;
  const revisionLabelOf = (id: string) => {
    const row = revisions.find((value) => value.id === id);
    return row ? (pickVersionLabel(row) ?? id) : id;
  };

  // 版本引用账本只对已建档资源有意义：resource 存在才拉取该资源 eval 版本表（(0) 端点）。
  useEffect(() => {
    if (!kind || !resource) {
      setRevisions([]);
      return;
    }
    let cancelled = false;
    setRevisionsLoading(true);
    setRevisionsError('');
    evaluationApi.listRevisions(kind, resourceId)
      .then((pageData) => { if (!cancelled) setRevisions(pageData.items); })
      .catch((err) => {
        if (cancelled) return;
        setRevisionsError(err.response?.data?.error || '加载版本账本失败');
      })
      .finally(() => { if (!cancelled) setRevisionsLoading(false); });
    return () => { cancelled = true; };
    // resource 参与依赖：未建档→建档的 transition 由 resource 身份变化触发重取。
  }, [kind, resourceId, resource, revisionTick]);

  return <div>
    <Button type="link" icon={<ArrowLeftOutlined />} style={{ paddingLeft: 0, marginBottom: 8 }}
      onClick={() => navigate('/evaluations/resources')}>返回被测资源</Button>
    {kind && <Flex align="center" gap={8} wrap style={{ marginBottom: 12 }}>
      <Typography.Title level={4} style={{ margin: 0 }}>{pageTitle}</Typography.Title>
      {resource && pageTitle !== resourceId && <Typography.Text code type="secondary">{resourceId}</Typography.Text>}
      <Tag>{displayLabel(kind)}</Tag>
      {resource && <StatusTag value={resource.status} />}
      {resource && <Typography.Text type="secondary">稳定版本 {resource.stable_revision_id || '未建档'}</Typography.Text>}
      {stableRevisionId && <Button size="small" onClick={() => setAnalyze({
        revisionId: stableRevisionId,
        revisionLabel: revisionLabelOf(stableRevisionId),
      })}>引用分析</Button>}
    </Flex>}

    {error && <Alert type="error" showIcon style={{ marginBottom: 12 }} message={error} action={<Space wrap>
      <Button size="small" onClick={() => void reload()}>重试</Button>
      <Button size="small" onClick={() => navigate('/evaluations/resources')}>返回被测资源</Button>
    </Space>} />}
    {!kind && !error && <Empty description="未知的被测资源类型" />}
    {kind && loading && !resource && !error && <Card loading />}

    {kind && resource && <>
      <Flex justify="space-between" align="center" gap={16} wrap style={{ marginBottom: 12 }}>
        <Space wrap>
          <Typography.Text type="secondary">建档于 {timeLabel(resource.created_at)}</Typography.Text>
          {resource.latest_run_status && <Space size={4}><Typography.Text type="secondary">最近运行</Typography.Text>
            <StatusTag value={resource.latest_run_status} /></Space>}
        </Space>
        {productEditPath(kind, resourceId) && <Button onClick={() =>
          navigate(productEditPath(kind, resourceId)!.path)}>
          打开{productEditPath(kind, resourceId)!.label} <ArrowRightOutlined /></Button>}
      </Flex>
      <Flex vertical gap={16}>
        <Card size="small" title="运行与回归">
          {/* key 绑定资源：跨资源导航复用组件实例时强制重挂载，清空残留版本筛选态 */}
          <RunRegressionSection key={`${kind}:${resourceId}`} runs={runs} kind={kind} resourceId={resourceId}
            onOpenRun={(run) => navigate(`/evaluations/runs/${encodeURIComponent(run.id)}`)} />
        </Card>
        <Card size="small" title="版本引用账本">
          <VersionLedgerSection revisions={revisions} loading={revisionsLoading} error={revisionsError}
            stableRevisionId={stableRevisionId} onRetry={() => setRevisionTick((value) => value + 1)}
            onAnalyze={(revisionId, revisionLabel) => setAnalyze({ revisionId, revisionLabel })} />
        </Card>
        <Card size="small" title="版本时间线">
          <TimelinePanel events={events} loading={loading} error={error} />
        </Card>
        <Card size="small" title="候选与实验">
          <CandidateExperimentSection candidates={candidates} experiments={experiments}
            canManage={canManage} />
        </Card>
      </Flex>
    </>}

    {kind && !loading && !resource && !error && <Alert type="info" showIcon
      message={`${displayLabel(kind)}「${resourceId}」尚未在评测中心建档。`}
      description={registrable ? '建档后可在此回看版本↔运行证据账本；登记会为当前产品稳定版本建立评测基线。'
        : '技能/MCP 为历史只读类型，不再提供登记入口，仅可回看旧证据。'}
      action={canManage && registrable ? <Button type="primary" onClick={() => setRegisterOpen(true)}>
        登记该资源</Button> : null} />}
    {/* 未建档 CTA 就地登记，不再跳转 hub；URL 保持当前资源详情，无 ?action= 状态残留 */}
    {registrable && <RegisterResourceModal open={registerOpen}
      initial={{ kind: kind as RegistrableResourceKind, resource_id: resourceId }} registered={[]}
      onClose={() => setRegisterOpen(false)}
      onRegistered={() => { setRegisterOpen(false); reload(); }} />}
    {/* 版本引用账本下钻抽屉：行内版本/头部稳定版本 → 该版本 subject/pinned/candidate/
        experiment 引用与通过率。从详情页进运行详情/自进化工作区，维持既有导航语义。 */}
    {analyze && kind && resource && <RevisionReferenceDrawer open
      resourceKind={kind} resourceId={resourceId} resourceName={resourceDisplayName(resource)}
      revisionId={analyze.revisionId} revisionLabel={analyze.revisionLabel}
      onClose={() => setAnalyze(null)}
      onOpenRun={(runId) => navigate(`/evaluations/runs/${encodeURIComponent(runId)}`)}
      onGoEvolution={() => navigate('/evaluations/evolution')} />}
  </div>;
};

// VersionLedgerSection 版本引用账本：(0) listRevisions 小表陈列该资源 eval 版本。产品
// 版本对照列以 safe_summary.version_label 为主文案（缺值显 —），eval 版本 id 弱化次要行；
// 当前稳定行打 Tag；行「引用分析」开 RevisionReferenceDrawer 查看该版本引用与通过率。
// 只读、无命令副作用；失败独立 alert + 重试，不阻断其余卡。
const VersionLedgerSection = ({ revisions, loading, error, stableRevisionId, onRetry, onAnalyze }: {
  revisions: RevisionSummary[]; loading: boolean; error: string;
  stableRevisionId?: string; onRetry: () => void;
  onAnalyze: (revisionId: string, revisionLabel: string) => void;
}) => {
  if (error) {
    return <Alert type="error" showIcon message={error} action={<Space wrap>
      <Button size="small" onClick={onRetry}>重试</Button>
    </Space>} />;
  }
  const columns: ColumnsType<RevisionSummary> = [
    { title: '产品版本', render: (_: unknown, row: RevisionSummary) =>
      <ResourceNameCell name={pickVersionLabel(row) ?? '—'} resourceId={row.id} /> },
    { title: '来源', dataIndex: 'source', width: 96 },
    { title: '状态', dataIndex: 'status', width: 96, render: (value: string) => <StatusTag value={value} /> },
    { title: '建档时间', dataIndex: 'created_at', width: 150, render: (value: string) => timeLabel(value) },
    { title: '当前稳定', width: 96,
      render: (_: unknown, row: RevisionSummary) => row.id === stableRevisionId
        ? <Tag color="blue">当前稳定</Tag> : '—' },
    { title: '操作', width: 92, render: (_: unknown, row: RevisionSummary) => <Button type="link" size="small"
      onClick={() => onAnalyze(row.id, pickVersionLabel(row) ?? row.id)}>引用分析</Button> },
  ];
  return <Table<RevisionSummary> size="small" rowKey="id" dataSource={revisions} loading={loading}
    pagination={false} columns={columns}
    locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
      description="该资源还没有评测版本（建档后逐版本记录评估证据）" /> }} />;
};

// 运行与回归：本资源离线 run 的 revision 过滤 + 通过率折线 + run 行（点击去运行详情）。
// revision 过滤走服务端（后端 (b) revision_id 契约）：选中某锚定版本即以 revision_id
// 重新取该版本 runs，避免首屏 20 条并集把旧版本 run 挤出列表而误报"假空"。选项来自
// 上游并集 runs（可见窗口内的版本）；清除筛选回到并集视图。组件以 kind:id 作 key，
// 跨资源导航时整体重挂载，残留的版本筛选不串资源。
const RunRegressionSection = ({ runs, kind, resourceId, onOpenRun }: {
  runs: RunSummary[]; kind?: ResourceKind; resourceId: string;
  onOpenRun: (run: RunSummary) => void;
}) => {
  const [revision, setRevision] = useState<string | undefined>();
  const [serverRuns, setServerRuns] = useState<RunSummary[] | null>(null);
  const [filterLoading, setFilterLoading] = useState(false);
  const revisionOptions = useMemo(() => Array.from(new Set(runs.map((run) => run.revision_id))), [runs]);
  // 选中版本期间以服务端结果为准；无版本（含版本加载中）暂空，避免泄漏并集里的其他版本行。
  const visible = revision ? (serverRuns ?? []) : runs;

  useEffect(() => {
    if (!revision || !kind) {
      setServerRuns(null);
      return;
    }
    let cancelled = false;
    setFilterLoading(true);
    evaluationApi.listRuns({ resource_kind: kind, resource_id: resourceId, revision_id: revision })
      .then((page) => { if (!cancelled) setServerRuns(page.items); })
      .catch((err) => {
        if (cancelled) return;
        message.error({ content: err.response?.data?.error || '加载该版本运行记录失败', duration: 3 });
        setRevision(undefined);
        setServerRuns(null);
      })
      .finally(() => { if (!cancelled) setFilterLoading(false); });
    return () => { cancelled = true; };
  }, [revision, kind, resourceId]);

  const changeRevision = (value?: string) => {
    setRevision(value);
    setServerRuns(null);
  };
  // 折线按时间正序绘制（后端 runs 为 created_at DESC）。
  const points: HealthTrendPoint[] = visible.slice().reverse().map((run) => ({
    id: run.id,
    timeLabel: timeLabel(run.created_at),
    fullLabel: `${run.id} · ${resourceDisplayName(run)} · ${run.passed_cases}/${run.total_cases} 通过 · ${timeLabel(run.created_at)}`,
    passRate: run.total_cases > 0 ? run.passed_cases / run.total_cases : null,
    passed: run.passed,
  }));

  const columns: ColumnsType<RunSummary> = [
    { title: '锚定版本', dataIndex: 'revision_id', ellipsis: true },
    { title: '状态', dataIndex: 'status', width: 96,
      render: (_: unknown, row: RunSummary) => <StatusTag value={runDisplayStatus(row.status, row.passed)} /> },
    { title: '通过用例', width: 92, render: (_: unknown, row: RunSummary) =>
      `${row.passed_cases} / ${row.total_cases}` },
    { title: '创建时间', dataIndex: 'created_at', width: 170, render: (value: string) => timeLabel(value) },
    { title: '操作', width: 76, render: (_, row) => <Button type="link" size="small"
      onClick={() => onOpenRun(row)}>详情</Button> },
  ];

  return <Flex vertical gap={12}>
    {revisionOptions.length > 1 && <Select aria-label="版本过滤" allowClear placeholder="全部版本"
      style={{ width: 240, alignSelf: 'flex-end' }} value={revision} onChange={changeRevision}
      options={revisionOptions.map((value) => ({ value, label: value }))} />}
    {points.length > 0
      ? <HealthTrendChart points={points} />
      : visible.length > 0
        ? <Typography.Text type="secondary">该筛选下没有可绘制的运行通过率。</Typography.Text>
        : null}
    <Table<RunSummary> size="small" rowKey="id" dataSource={visible} loading={filterLoading}
      pagination={false} columns={columns}
      locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
        description={revision ? '该版本还没有离线运行记录' : '离线运行记录还是空的'} /> }} />
  </Flex>;
};

// CandidateExperimentSection 只读陈列本资源候选与实验行；推进/评审命令在自进化
// 工作区（/evaluations/evolution）执行，此处给入口，避免在证据页重复命令副作用。
const CandidateExperimentSection = ({ candidates, experiments, canManage }: {
  candidates: CandidateSummary[];
  experiments: ExperimentSummary[];
  canManage: boolean;
}) => {
  const navigate = useNavigate();
  const hasRows = candidates.length + experiments.length > 0;
  if (!hasRows) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该资源还没有候选或金丝雀实验" />;
  }
  const candidateColumns: ColumnsType<CandidateSummary> = [
    { title: '候选版本', dataIndex: 'revision_id', ellipsis: true },
    { title: '父版本', dataIndex: 'parent_revision_id', ellipsis: true },
    { title: '来源', dataIndex: 'source', width: 110 },
    { title: '状态', dataIndex: 'status', width: 96, render: (value: string) => <StatusTag value={value} /> },
    { title: '创建时间', dataIndex: 'created_at', width: 170, render: (value: string) => timeLabel(value) },
  ];
  const experimentColumns: ColumnsType<ExperimentSummary> = [
    { title: '稳定版本', dataIndex: 'stable_revision_id', ellipsis: true },
    { title: '金丝雀版本', dataIndex: 'canary_revision_id', ellipsis: true },
    { title: '阶段', dataIndex: 'stage_percent', width: 90, render: (value: number) => `${value}%` },
    { title: '状态', dataIndex: 'status', width: 96, render: (value: string) => <StatusTag value={value} /> },
    { title: '创建时间', dataIndex: 'created_at', width: 170, render: (value: string) => timeLabel(value) },
  ];
  return <Flex vertical gap={12}>
    {canManage && <Typography.Text type="secondary">管理员可在自进化工作区执行晋级/暂停/回滚等命令。</Typography.Text>}
    {candidates.length > 0 && <>
      <Typography.Text strong>候选版本（{candidates.length}）</Typography.Text>
      <Table<CandidateSummary> size="small" rowKey="id" dataSource={candidates} pagination={false}
        columns={candidateColumns} />
    </>}
    {experiments.length > 0 && <>
      <Typography.Text strong>金丝雀实验（{experiments.length}）</Typography.Text>
      <Table<ExperimentSummary> size="small" rowKey="id" dataSource={experiments} pagination={false}
        columns={experimentColumns} />
    </>}
    <div><Button type="link" size="small" style={{ paddingLeft: 0 }}
      onClick={() => navigate('/evaluations/evolution')}>前往自进化工作区</Button></div>
  </Flex>;
};
