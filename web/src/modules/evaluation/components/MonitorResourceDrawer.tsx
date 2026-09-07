// MonitorResourceDrawer 单资源时间趋势下钻（spec 2026-09-03 §5.2）。
// 数据来自 getMonitorTrend：series 按 UTC 日桶，runs 为该资源窗口内 succeeded 评测 run。
// 质量/行为/成本/延迟折线图若某日无该序列样本则以 null 断线（不跨空连线）；
// 全窗口无该项数值时用 noData 文案诚实空态。run 无过程断言用例时通过率恒为 1.0，
// 列出 run 时显式注记，避免被误读为「过程完美」。
import { Alert, Button, Drawer, Empty, Flex, Spin, Table, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import { resourceDisplayName } from '../lib/resourceName';
import type { MonitorResourceSummary, MonitorTrend, RunProcessPoint } from '../model/evaluation';

import { MultiLineTrendChart } from './MultiLineTrendChart';
import type { MultiLineSeries, TrendBucket } from './MultiLineTrendChart';
import { displayLabel, drawerWidth } from './evaluationView';

import { extractErrorMessage } from '@/shared/lib';

// bucketLabel 与后端 date_trunc('day' AT TIME ZONE 'UTC') 的日界一致：bucket_at 为 UTC 零点。
function bucketLabel(iso: string): string {
  const date = new Date(iso);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
}

const canonicalDims = ['faithfulness', 'relevance', 'completeness'];
// qualityDimOrder 固定 judge 三维优先、其余按首现顺序，保证多日维度顺序稳定。
function qualityDimOrder(names: string[]): string[] {
  const known = canonicalDims.filter((name) => names.includes(name));
  return [...known, ...names.filter((name) => !canonicalDims.includes(name))];
}

const colorOf = (index: number) => ['#1677ff', '#52c41a', '#722ed1', '#fa8c16', '#13c2c2', '#eb2f96'][index % 6];
const percentTick = (value: number) => `${Math.round(value * 100)}%`;
const countTick = (value: number) => String(Math.round(value));
const msTick = (value: number) => `${Math.round(value)}ms`;
const usdTick = (value: number) => `$${value.toFixed(2)}`;

function SeriesChartSection({ title, buckets, series, unit, tick, noDataText, dataTestId }: {
  title: string; buckets: TrendBucket[]; series: MultiLineSeries[]; unit: 'percent' | 'number';
  tick: (value: number) => string; noDataText: string; dataTestId: string;
}) {
  const hasValues = series.some((item) => item.values.some((value) => value !== null));
  return (
    <div>
      <Typography.Title level={5} style={{ marginTop: 0 }}>{title}</Typography.Title>
      {hasValues
        ? <MultiLineTrendChart buckets={buckets} series={series} unit={unit} ariaLabel={title}
          yTickLabel={tick} noDataText={noDataText} dataTestId={dataTestId} />
        : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={noDataText} />}
    </div>
  );
}

const runColumns: ColumnsType<RunProcessPoint> = [
  { title: '运行 ID', dataIndex: 'run_id', ellipsis: true },
  { title: '运行时间', dataIndex: 'run_created_at', width: 170,
    render: (value: string) => new Date(value).toLocaleString('zh-CN') },
  { title: '过程通过率', dataIndex: 'process_pass_rate', width: 110,
    render: (value: number) => `${Math.round(value * 100)}%` },
];

function ProcessRunsSection({ runs }: { runs: MonitorTrend['runs'] }) {
  const hasConstant = runs.some((run) => run.process_pass_rate === 1);
  return (
    <div data-testid="monitor-process-runs">
      <Typography.Title level={5}>过程基线</Typography.Title>
      {hasConstant && <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        注：无过程断言用例的 run，过程通过率恒为 1.0
      </Typography.Paragraph>}
      {runs.length === 0
        ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该时间窗口内无离线评测过程基线" />
        : <Table<RunProcessPoint> size="small" rowKey="run_id" dataSource={runs} columns={runColumns} pagination={false} />}
    </div>
  );
}

export const MonitorResourceDrawer = ({ resource, open, from, to, isMobile, onClose }: {
  resource: MonitorResourceSummary; open: boolean; from: string; to: string; isMobile?: boolean; onClose: () => void;
}) => {
  const [trend, setTrend] = useState<MonitorTrend | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setTrend(null);
    setLoading(true);
    setError('');
    evaluationApi.getMonitorTrend({ resource_kind: resource.resource_kind, resource_id: resource.resource_id, from, to })
      .then((data) => { if (!cancelled) setTrend(data); })
      .catch((err) => { if (!cancelled) setError(extractErrorMessage(err) || '加载趋势失败'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [open, resource, from, to, tick]);

  const series = trend?.series ?? [];
  const runs = trend?.runs ?? [];
  const buckets: TrendBucket[] = series.map((point) => ({
    bucketLabel: bucketLabel(point.bucket_at), fullLabel: point.bucket_at,
  }));
  const totals = series.reduce((acc, point) => ({
    samples: acc.samples + point.sample_count,
    tokens: acc.tokens + point.cost.total_tokens,
    cost: acc.cost + point.cost.total_cost_usd,
  }), { samples: 0, tokens: 0, cost: 0 });

  // 质量：仅对实际出现的维度成线；每桶缺该维度样本 → null（断线），不补齐。
  const dims = qualityDimOrder([...new Set(series.flatMap((point) => point.quality.map((q) => q.dimension)))]);
  const qualitySeries: MultiLineSeries[] = dims.map((dim, index) => ({
    name: displayLabel(dim), color: colorOf(index),
    values: series.map((point) => {
      const hit = point.quality.find((q) => q.dimension === dim);
      return hit ? hit.pass_rate : null;
    }),
  }));
  const behaviorSeries: MultiLineSeries[] = [
    { name: '规则命中', color: colorOf(0), values: series.map((point) => point.behavior.rule_hits) },
    { name: '重试', color: colorOf(4), values: series.map((point) => point.behavior.retry_count) },
    { name: '待复核', color: '#faad14', values: series.map((point) => point.behavior.verdict.flag) },
    { name: '阻断', color: '#ff4d4f', values: series.map((point) => point.behavior.verdict.block) },
  ];
  const latencySeries: MultiLineSeries[] = [
    { name: '平均延迟', color: colorOf(0), values: series.map((point) => point.cost.avg_latency_ms) },
    { name: 'P95 延迟', color: colorOf(2), values: series.map((point) => point.cost.p95_latency_ms) },
  ];
  const costSeries: MultiLineSeries[] = [
    { name: '成本 USD', color: colorOf(3), values: series.map((point) => point.cost.total_cost_usd) },
  ];

  return (
    <Drawer open={open} onClose={onClose} width={drawerWidth(isMobile)} destroyOnHidden
      title={`${displayLabel(resource.resource_kind)} ${resourceDisplayName(resource)} · 评测观测趋势`}>
      {error
        ? <Alert type="error" showIcon message={error}
          action={<Button size="small" onClick={() => setTick((value) => value + 1)}>重试</Button>} />
        : !trend
          ? <Flex justify="center" style={{ paddingTop: 48 }}><Spin spinning={loading} /></Flex>
          : <Flex vertical gap={24}>
            <Flex gap={24} wrap>
              <Typography.Text strong>{`总样本 ${totals.samples}`}</Typography.Text>
              <Typography.Text strong>{`总 Token ${totals.tokens.toLocaleString('zh-CN')}`}</Typography.Text>
              <Typography.Text strong>{`总成本 $${totals.cost.toFixed(2)}`}</Typography.Text>
            </Flex>
            <SeriesChartSection title="质量趋势" buckets={buckets} series={qualitySeries} unit="percent"
              tick={percentTick} noDataText="窗口内无质量判定维度样本" dataTestId="monitor-quality-chart" />
            <SeriesChartSection title="行为趋势" buckets={buckets} series={behaviorSeries} unit="number"
              tick={countTick} noDataText="窗口内无行为异常样本" dataTestId="monitor-behavior-chart" />
            <SeriesChartSection title="延迟趋势" buckets={buckets} series={latencySeries} unit="number"
              tick={msTick} noDataText="窗口内无延迟样本" dataTestId="monitor-latency-chart" />
            <SeriesChartSection title="成本趋势" buckets={buckets} series={costSeries} unit="number"
              tick={usdTick} noDataText="窗口内无成本数据" dataTestId="monitor-cost-chart" />
            <ProcessRunsSection runs={runs} />
          </Flex>}
    </Drawer>
  );
};
