// RevisionReferenceDrawer：单 eval 版本「版本↔引用方」账本下钻。
// 并行取 (c) references（deployment + subject_runs/pinned_runs/candidates/experiments，
// 明细数组恒非 nil）与 (d) pass-rate；主账本失败整块 alert + 重试，pass-rate 是次要块，
// 单独失败只塌缩摘要、不吞掉引用明细。pass_rate=null 表示 0 次成功/无数值（诚实空态，
// 不伪 0）。名称一律真名优先：主体/绑定 run 的「所属被测」主文案取资源真名，裸 id 降级
// 弱化次要行。
import { Alert, Button, Drawer, Empty, Flex, Spin, Statistic, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import { resourceDisplayName } from '../lib/resourceName';
import type {
  ResourceKind,
  RevisionCandidateRef,
  RevisionExperimentRef,
  RevisionPassRate,
  RevisionPinnedRun,
  RevisionReferences,
  RunSummary,
} from '../model/evaluation';

import { HealthTrendChart, type HealthTrendPoint } from './HealthTrendChart';
import { ResourceNameCell } from './ResourceNameCell';
import { StatusTag, displayLabel, drawerWidth, runDisplayStatus } from './evaluationView';

import { extractErrorMessage } from '@/shared/lib';

type Props = {
  resourceKind: ResourceKind;
  resourceId: string;
  resourceName: string;
  revisionId: string;
  revisionLabel: string;
  open: boolean;
  isMobile?: boolean;
  onClose: () => void;
  onOpenRun: (runId: string) => void;
  onGoEvolution: () => void;
};

const fmtTime = (value: string) => new Date(value).toLocaleString('zh-CN', {
  month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
});

const statusCell = (status: string, passed: boolean) =>
  <StatusTag value={runDisplayStatus(status, passed)} />;
const casesCell = (passed: number, total: number) => `${passed} / ${total}`;
const detailLink = (runId: string, onOpenRun: (runId: string) => void) =>
  <Button type="link" size="small" onClick={() => onOpenRun(runId)}>详情</Button>;

// runColumns 主体 run 行：运行 ID 是身份列（rowKey/导航），不作名称伪装。
const runColumns = (onOpenRun: (runId: string) => void): ColumnsType<RunSummary> => [
  { title: '运行 ID', dataIndex: 'id', ellipsis: true },
  { title: '状态', width: 96, render: (_: unknown, row: RunSummary) => statusCell(row.status, row.passed) },
  { title: '通过用例', width: 92, render: (_: unknown, row: RunSummary) => casesCell(row.passed_cases, row.total_cases) },
  { title: '运行时间', dataIndex: 'created_at', width: 150, render: (value: string) => fmtTime(value) },
  { title: '操作', width: 76, render: (_: unknown, row: RunSummary) => detailLink(row.id, onOpenRun) },
];

// pinnedColumns 绑定 run：主文案是被 pin 资源真名（可能跨资源），id 弱化次要行。
const pinnedColumns = (onOpenRun: (runId: string) => void): ColumnsType<RevisionPinnedRun> => [
  { title: '运行 ID', dataIndex: 'run_id', ellipsis: true },
  { title: '所属被测', ellipsis: true,
    render: (_: unknown, row: RevisionPinnedRun) =>
      <ResourceNameCell name={resourceDisplayName(row)} resourceId={row.resource_id} /> },
  { title: '状态', width: 96, render: (_: unknown, row: RevisionPinnedRun) => statusCell(row.status, row.passed) },
  { title: '通过用例', width: 92,
    render: (_: unknown, row: RevisionPinnedRun) => casesCell(row.passed_cases, row.total_cases) },
  { title: '操作', width: 76, render: (_: unknown, row: RevisionPinnedRun) => detailLink(row.run_id, onOpenRun) },
];

const candidateColumns: ColumnsType<RevisionCandidateRef> = [
  { title: '角色', dataIndex: 'role', width: 88,
    render: (value: string) => (value === 'baseline' ? '父基线' : '优化候选') },
  { title: '候选版本', dataIndex: 'revision_id', ellipsis: true },
  { title: '父版本', dataIndex: 'parent_revision_id', ellipsis: true },
  { title: '来源', dataIndex: 'source', width: 100 },
  { title: '状态', dataIndex: 'status', width: 96, render: (value: string) => <StatusTag value={value} /> },
  { title: '创建时间', dataIndex: 'created_at', width: 150, render: (value: string) => fmtTime(value) },
];

const experimentColumns: ColumnsType<RevisionExperimentRef> = [
  { title: '角色', dataIndex: 'role', width: 96,
    render: (value: string) => ({ stable: '稳定', canary: '金丝雀', both: '稳定+金丝雀' })[value] ?? value },
  { title: '稳定版本', dataIndex: 'stable_revision_id', ellipsis: true },
  { title: '金丝雀版本', dataIndex: 'canary_revision_id', ellipsis: true },
  { title: '阶段', dataIndex: 'stage_percent', width: 72, render: (value: number) => `${value}%` },
  { title: '状态', dataIndex: 'status', width: 96, render: (value: string) => <StatusTag value={value} /> },
  { title: '建议', dataIndex: 'recommendation', width: 96, render: (value?: string) => value || '—' },
  { title: '创建时间', dataIndex: 'created_at', width: 150, render: (value: string) => fmtTime(value) },
];

export const RevisionReferenceDrawer = (props: Props) => {
  const {
    resourceKind, resourceId, resourceName, revisionId, revisionLabel,
    open, isMobile, onClose, onOpenRun, onGoEvolution,
  } = props;
  const [refs, setRefs] = useState<RevisionReferences | null>(null);
  const [refsLoading, setRefsLoading] = useState(false);
  const [refsError, setRefsError] = useState('');
  const [rate, setRate] = useState<RevisionPassRate | null>(null);
  const [rateLoading, setRateLoading] = useState(false);
  const [rateError, setRateError] = useState('');
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setRefs(null);
    setRefsLoading(true);
    setRefsError('');
    evaluationApi.listRevisionReferences(resourceKind, resourceId, revisionId)
      .then((data) => { if (!cancelled) setRefs(data); })
      .catch((err) => { if (!cancelled) setRefsError(extractErrorMessage(err) || '加载引用账本失败'); })
      .finally(() => { if (!cancelled) setRefsLoading(false); });
    return () => { cancelled = true; };
  }, [open, resourceKind, resourceId, revisionId, tick]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setRate(null);
    setRateLoading(true);
    setRateError('');
    evaluationApi.getRevisionPassRate(resourceKind, resourceId, revisionId)
      .then((data) => { if (!cancelled) setRate(data); })
      .catch((err) => { if (!cancelled) setRateError(extractErrorMessage(err) || '加载通过率失败'); })
      .finally(() => { if (!cancelled) setRateLoading(false); });
    return () => { cancelled = true; };
  }, [open, resourceKind, resourceId, revisionId, tick]);

  const retry = () => setTick((value) => value + 1);
  // 最近 run 折线按时间正序绘制（后端 recent_runs 为 created_at DESC）。
  const points: HealthTrendPoint[] = [...(rate?.recent_runs ?? [])].reverse().map((run) => ({
    id: run.id,
    timeLabel: fmtTime(run.created_at),
    fullLabel: `${run.id} · ${run.passed_cases}/${run.total_cases} 通过 · ${fmtTime(run.created_at)}`,
    passRate: run.total_cases > 0 ? run.passed_cases / run.total_cases : null,
    passed: run.passed,
  }));
  const passText = rate?.pass_rate != null ? `${(rate.pass_rate * 100).toFixed(1)}%` : '—';

  const overview = (data: RevisionReferences) => {
    const dep = data.deployment;
    const inDeployment = dep && (dep.stable_revision_id === revisionId || dep.canary_revision_id === revisionId);
    return <Flex vertical gap={8}>
      <Flex gap={16} wrap>
        <Typography.Text strong>{`主体运行 ${data.subject_runs.length}`}</Typography.Text>
        <Typography.Text strong>{`绑定引用 ${data.pinned_runs.length}`}</Typography.Text>
        <Typography.Text strong>{`优化候选 ${data.candidates.length}`}</Typography.Text>
        <Typography.Text strong>{`金丝雀实验 ${data.experiments.length}`}</Typography.Text>
      </Flex>
      <Flex gap={8} wrap align="center">
        <Typography.Text type="secondary">部署</Typography.Text>
        {dep
          ? <>
            {(dep.role === 'stable' || dep.role === 'both') && <Tag color="blue">{`稳定 ${dep.stable_revision_id}`}
              {dep.stable_revision_id === revisionId && '（本版本）'}</Tag>}
            {(dep.role === 'canary' || dep.role === 'both') && <Tag color="gold">
              {`金丝雀 ${dep.canary_revision_id} · ${dep.canary_percent}%`}
              {dep.canary_revision_id === revisionId && '（本版本）'}</Tag>}
            {dep && !inDeployment && <Typography.Text type="secondary">该版本未参与当前部署</Typography.Text>}
          </>
          : <Tag>无部署记录</Tag>}
      </Flex>
    </Flex>;
  };

  const passRateSection = () => {
    if (rateError) {
      return <Alert type="error" showIcon message={rateError}
        action={<Button size="small" onClick={retry}>重试</Button>} />;
    }
    if (!rate) {
      return <Flex justify="center" style={{ paddingTop: 24 }}><Spin spinning={rateLoading} /></Flex>;
    }
    const explain = rate.total_runs === 0
      ? '该版本还没有离线运行记录'
      : rate.pass_rate == null
        ? '成功 run 为 0，通过率不估值'
        : '按成功 run 的用例通过合计 / 用例合计计算';
    return <Flex vertical gap={12}>
      <Flex gap={32} wrap align="center">
        <Statistic title="通过率" value={passText} valueStyle={{ fontSize: 22 }} />
        <Flex vertical>
          <Typography.Text>{`成功 run ${rate.succeeded_runs} / 共 ${rate.total_runs} · 通过用例 ${rate.passed_cases} / ${rate.total_cases}`}</Typography.Text>
          <Typography.Text type="secondary">{explain}</Typography.Text>
        </Flex>
      </Flex>
      {points.length > 0 && <>
        <Typography.Text strong>最近运行通过率</Typography.Text>
        <HealthTrendChart points={points} />
      </>}
    </Flex>;
  };

  const detailSection = (data: RevisionReferences) => {
    const hasRows = data.subject_runs.length + data.pinned_runs.length
      + data.candidates.length + data.experiments.length > 0;
    if (!hasRows) {
      return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
        description="该版本还没有主体/绑定运行、优化候选或金丝雀实验引用" />;
    }
    const showEvolutionCta = data.candidates.length > 0 || data.experiments.length > 0;
    return <Flex vertical gap={16}>
      {data.subject_runs.length > 0 && <div>
        <Typography.Text strong>{`主体运行（本版本作为被测）（${data.subject_runs.length}）`}</Typography.Text>
        <Table<RunSummary> size="small" rowKey="id" dataSource={data.subject_runs} pagination={false}
          columns={runColumns(onOpenRun)} />
      </div>}
      {data.pinned_runs.length > 0 && <div>
        <Typography.Text strong>{`绑定引用（作为绑定资源被 pin）（${data.pinned_runs.length}）`}</Typography.Text>
        <Table<RevisionPinnedRun> size="small" rowKey="run_id" dataSource={data.pinned_runs} pagination={false}
          columns={pinnedColumns(onOpenRun)} />
      </div>}
      {data.candidates.length > 0 && <div>
        <Typography.Text strong>{`优化候选（${data.candidates.length}）`}</Typography.Text>
        <Table<RevisionCandidateRef> size="small" rowKey="id" dataSource={data.candidates} pagination={false}
          columns={candidateColumns} />
      </div>}
      {data.experiments.length > 0 && <div>
        <Typography.Text strong>{`金丝雀实验（${data.experiments.length}）`}</Typography.Text>
        <Table<RevisionExperimentRef> size="small" rowKey="id" dataSource={data.experiments} pagination={false}
          columns={experimentColumns} />
      </div>}
      {showEvolutionCta && <div><Button type="link" size="small" style={{ paddingLeft: 0 }}
        onClick={onGoEvolution}>前往自进化工作区查看命令记录</Button></div>}
    </Flex>;
  };

  return (
    <Drawer open={open} onClose={onClose} width={drawerWidth(isMobile)} destroyOnHidden
      title={`${displayLabel(resourceKind)} ${resourceName} · 版本 ${revisionLabel} 引用分析`}>
      {refsError
        ? <Alert type="error" showIcon message={refsError}
          action={<Button size="small" onClick={retry}>重试</Button>} />
        : !refs
          ? <Flex justify="center" style={{ paddingTop: 48 }}><Spin spinning={refsLoading} /></Flex>
          : <Flex vertical gap={20}>
            {overview(refs)}
            {passRateSection()}
            {detailSection(refs)}
          </Flex>}
    </Drawer>
  );
};
