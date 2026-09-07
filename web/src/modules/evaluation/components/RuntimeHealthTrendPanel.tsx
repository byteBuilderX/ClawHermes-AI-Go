// RuntimeHealthTrendPanel 「评测运行通过率趋势」视图。
// 数据源为评测中心 runs 记录（多租户 DB）：展示所选资源最近 N 次离线评测 run 的
// 通过率（passed_cases / total_cases）随运行时间的趋势。
// 注意：本视图展示的是 eval_runs DB 离线通过率，不是 spec §10.1 的 Prom 源「运行态
// 健康分」。spec §10.1 的健康分 = 按资源 × 参数版本 × 时间窗的评分时间序列（rule 命中率
// / judge 均分 / 行为异常率），数据源 eval_rule_hit_total / eval_judge_score /
// eval_behavior_anomaly_total 已注册，属 backlog，可后续接入 Prometheus 时间序列。
// listRuns 按 limit 分页（EVALUATION_TREND_RUN_LIMIT），仅取最近 N 次；后端返回
// next_cursor 时表示存在更早记录，统计行显式标注截断，不暗示全量。
import { Alert, Button, Empty, Flex, Select, Space, Table, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useMemo, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import { resourceDisplayName } from '../lib/resourceName';
import type { ResourceKind, ResourceSummary, RunSummary } from '../model/evaluation';

import { HealthTrendChart } from './HealthTrendChart';
import type { HealthTrendPoint } from './HealthTrendChart';
import { StatusTag, kindFilterOptions, runDisplayStatus } from './evaluationView';

import { EVALUATION_TREND_RUN_LIMIT } from '@/constants';
import { extractErrorMessage } from '@/shared/lib';

// shortTrendTime 生成 x 轴短标签（MM-DD HH:mm），完整时间用于 tooltip/表格。
function shortTrendTime(iso: string): string {
  const date = new Date(iso);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function passRateOf(run: RunSummary): number | null {
  if (!run.total_cases || run.total_cases <= 0) return null;
  return run.passed_cases / run.total_cases;
}

type TrendRow = RunSummary & { pass_rate: number | null };

export const RuntimeHealthTrendPanel = ({ defaultKind, defaultResourceId }: {
  defaultKind?: ResourceKind; defaultResourceId?: string;
}) => {
  const [kind, setKind] = useState<ResourceKind | undefined>(defaultKind);
  const [resourceId, setResourceId] = useState(defaultResourceId ?? '');
  const [resources, setResources] = useState<ResourceSummary[]>([]);
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [resourcesLoading, setResourcesLoading] = useState(true);
  // 初始即给出 defaultKind+defaultResourceId 时，runs 拉取在挂载 effect 中进行，
  // 初始为 true 避免「尚无运行记录」空态闪现。
  const [runsLoading, setRunsLoading] = useState(Boolean(defaultKind && defaultResourceId));
  const [resourcesError, setResourcesError] = useState('');
  const [runsError, setRunsError] = useState('');
  // 资源候选按 EVALUATION_TREND_RUN_LIMIT 截断；next_cursor 存在说明仍有更早资源，展示时标注。
  const [resourcesTruncated, setResourcesTruncated] = useState(false);
  // next_cursor 存在说明该资源运行记录超过本窗口条数，展示时标注截断。
  const [hasMore, setHasMore] = useState(false);
  // 重试计数：失败 Alert 的「重试」按钮递增以重新触发对应 effect。
  const [resourcesTick, setResourcesTick] = useState(0);
  const [runsTick, setRunsTick] = useState(0);

  // kind 变化时刷新资源候选并清空已选资源与旧列表（可能不属于新类型）。
  useEffect(() => {
    let cancelled = false;
    setResourcesLoading(true);
    setResourcesError('');
    setResources([]);
    setResourcesTruncated(false);
    evaluationApi.listResources(kind ? { resource_kind: kind, limit: EVALUATION_TREND_RUN_LIMIT }
      : { limit: EVALUATION_TREND_RUN_LIMIT })
      .then((page) => { if (!cancelled) { setResources(page.items); setResourcesTruncated(Boolean(page.next_cursor)); } })
      .catch((err) => { if (!cancelled) setResourcesError(extractErrorMessage(err) || '加载资源列表失败'); })
      .finally(() => { if (!cancelled) setResourcesLoading(false); });
    return () => { cancelled = true; };
  }, [kind, resourcesTick]);

  // 资源变化时拉取其 run 历史（按创建时间倒序，展示时升序）。effect 开头清空上一
  // 资源 runs，避免图表/表格错配当前选择；fetch 失败只渲染错误态而非「尚无记录」。
  useEffect(() => {
    if (!kind || !resourceId) { setRuns([]); setHasMore(false); setRunsError(''); setRunsLoading(false); return; }
    let cancelled = false;
    setRuns([]);
    setHasMore(false);
    setRunsError('');
    setRunsLoading(true);
    evaluationApi.listRuns({ resource_kind: kind, resource_id: resourceId, limit: EVALUATION_TREND_RUN_LIMIT })
      .then((page) => { if (!cancelled) { setRuns(page.items); setHasMore(Boolean(page.next_cursor)); } })
      .catch((err) => { if (!cancelled) setRunsError(extractErrorMessage(err) || '加载运行记录失败'); })
      .finally(() => { if (!cancelled) setRunsLoading(false); });
    return () => { cancelled = true; };
  }, [kind, resourceId, runsTick]);

  const rows: TrendRow[] = useMemo(() => [...runs]
    .sort((a, b) => a.created_at.localeCompare(b.created_at))
    .map((run) => ({ ...run, pass_rate: passRateOf(run) })), [runs]);
  const points: HealthTrendPoint[] = useMemo(() => rows.map((row) => ({
    id: row.id, timeLabel: shortTrendTime(row.created_at), fullLabel: row.created_at,
    passRate: row.pass_rate, passed: row.passed,
  })), [rows]);
  const scoredRuns = rows.filter((row) => row.pass_rate !== null);
  const avgPassRate = scoredRuns.length
    ? scoredRuns.reduce((acc, row) => acc + (row.pass_rate as number), 0) / scoredRuns.length : null;
  const latest = scoredRuns.length ? scoredRuns[scoredRuns.length - 1].pass_rate as number : null;

  const columns: ColumnsType<TrendRow> = [
    { title: '时间', dataIndex: 'created_at', width: 150, render: (value: string) => (
      new Date(value).toLocaleString('zh-CN')) },
    { title: '运行', dataIndex: 'id', ellipsis: true },
    { title: '资源版本', dataIndex: 'revision_id', ellipsis: true },
    { title: '状态', dataIndex: 'status', width: 110, render: (value: string, row) => (
      <StatusTag value={runDisplayStatus(value, row.passed)} />) },
    { title: '用例', key: 'cases', width: 90, render: (_, row) => `${row.passed_cases}/${row.total_cases}` },
    { title: '通过率', dataIndex: 'pass_rate', width: 90, render: (value: number | null) => (
      value === null ? '-' : `${(value * 100).toFixed(1)}%`) },
  ];

  const percent = (value: number | null) => (value === null ? '-' : `${(value * 100).toFixed(1)}%`);
  const retryResources = () => setResourcesTick((tick) => tick + 1);
  const retryRuns = () => setRunsTick((tick) => tick + 1);

  return (
    <div data-testid="runtime-health-trend-panel">
      <Space wrap style={{ marginBottom: 12 }}>
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
          value={kind} loading={resourcesLoading}
          onChange={(value: ResourceKind | undefined) => { setKind(value); setResourceId(''); }} />
        <Select aria-label="资源" placeholder="选择资源以查看运行通过率趋势" style={{ width: 260 }}
          options={resources.map((item) => {
            // 选项主文案用真名（resource_name → safe_summary），id 加括弱化核对；
            // 真名缺失时直接以 id 作选项标识（下拉属身份选择控件，非名称单元格）。
            const name = resourceDisplayName(item);
            return { value: item.resource_id, label: name === '—' ? item.resource_id : `${name}（${item.resource_id}）` };
          })}
          value={resourceId || undefined} loading={resourcesLoading}
          onChange={(value: string) => setResourceId(value)} />
      </Space>
      {resourcesTruncated && <Typography.Text type="warning" style={{ display: 'block', marginBottom: 12 }}>
        仅显示前 {EVALUATION_TREND_RUN_LIMIT} 个资源（存在更早记录）
      </Typography.Text>}
      {resourcesError && <Alert type="error" showIcon message={resourcesError} style={{ marginBottom: 12 }}
        action={<Button size="small" onClick={retryResources}>重试</Button>} />}
      {!resourceId
        ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="选择资源类型与资源后展示其评测运行通过率趋势" />
        : runsError
          ? <Alert type="error" showIcon message={runsError}
            action={<Button size="small" onClick={retryRuns}>重试</Button>} />
          : rows.length === 0
            ? (runsLoading ? null : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该资源尚无评测运行记录" />)
            : <>
              <Flex gap={24} wrap style={{ marginBottom: 8 }}>
                <Typography.Text strong>评测运行通过率趋势</Typography.Text>
                <Typography.Text type="secondary">本窗口 {rows.length} 次</Typography.Text>
                {hasMore && <Typography.Text type="warning">仅显示最近 {EVALUATION_TREND_RUN_LIMIT} 次运行（存在更早记录）</Typography.Text>}
                <Typography.Text type="secondary">平均通过率 {percent(avgPassRate)}</Typography.Text>
                <Typography.Text type="secondary">最近一次 {percent(latest)}</Typography.Text>
              </Flex>
              <HealthTrendChart points={points} />
              <Typography.Title level={5} style={{ marginTop: 12 }}>运行明细</Typography.Title>
              <Table<TrendRow> size="small" rowKey="id" dataSource={rows} columns={columns} pagination={false}
                loading={runsLoading} locale={{ emptyText: '暂无运行记录' }} />
            </>}
    </div>
  );
};
