// EvaluationMonitorPanel 「评测指标监控」视图（spec 2026-09-03 §5.2）。
// 数据线：观测线 eval_observations（质量/行为/成本聚合）+ 评测线 eval_runs 最近
// succeeded run（过程基线，process 为 null 表示窗口内无离线评测）。表按样本数
// 降序只展示观测最多的资源（limit 截断用 banner 显式标注，从不暗示全量）。
// 时间窗含端点（后端 >= from AND <= to），前端 to 取 endOf('day') 保证含整天。
import { Alert, Button, DatePicker, Empty, Select, Skeleton, Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import type { Dayjs } from 'dayjs';
import { useEffect, useMemo, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { MonitorFilters, MonitorResourceSummary, ResourceKind } from '../model/evaluation';

import { MonitorResourceDrawer } from './MonitorResourceDrawer';
import { displayLabel } from './evaluationView';

import {
  EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS, EVALUATION_MONITOR_RESOURCE_LIMIT,
  EVALUATION_MONITOR_WINDOW_PRESETS_DAYS,
} from '@/constants';
import { extractErrorMessage } from '@/shared/lib';

const { RangePicker } = DatePicker;

const kindOptions = ['skill', 'agent', 'mcp', 'knowledge'].map((value) => ({ value, label: displayLabel(value) }));
type WindowRange = [Dayjs, Dayjs];

const windowPresets = EVALUATION_MONITOR_WINDOW_PRESETS_DAYS.map((days) => ({
  label: `近 ${days} 天`,
  value: [dayjs().subtract(days - 1, 'day').startOf('day'), dayjs().endOf('day')] as WindowRange,
}));

const defaultWindowRange = (): WindowRange => [
  dayjs().subtract(EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS - 1, 'day').startOf('day'),
  dayjs().endOf('day'),
];

// isoWindow 归一化到整日起点/终点：与后端含端点窗口一致（RFC3339）。
function isoWindow(range: WindowRange): { from: string; to: string } {
  return { from: range[0].startOf('day').toISOString(), to: range[1].endOf('day').toISOString() };
}

const joinNonEmpty = (parts: string[]) => parts.filter(Boolean).join(' · ');

export const EvaluationMonitorPanel = ({ defaultKind, defaultResourceId, isMobile }: {
  defaultKind?: ResourceKind; defaultResourceId?: string; isMobile?: boolean;
}) => {
  const [kind, setKind] = useState<ResourceKind | undefined>(defaultKind);
  const [resourceId, setResourceId] = useState(defaultResourceId ?? '');
  const [range, setRange] = useState<WindowRange>(defaultWindowRange);
  const [rows, setRows] = useState<MonitorResourceSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // 重试计数：失败 Alert 的「重试」递增以重新触发拉取 effect。
  const [tick, setTick] = useState(0);
  const [openRow, setOpenRow] = useState<MonitorResourceSummary | null>(null);
  const window = useMemo(() => isoWindow(range), [range]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    const filters: MonitorFilters = {
      ...(kind ? { resource_kind: kind } : {}),
      ...(resourceId ? { resource_id: resourceId } : {}),
      from: window.from,
      to: window.to,
      limit: EVALUATION_MONITOR_RESOURCE_LIMIT,
    };
    evaluationApi.listMonitorResources(filters)
      .then((page) => { if (!cancelled) setRows(page.items); })
      .catch((err) => { if (!cancelled) setError(extractErrorMessage(err) || '加载监控数据失败'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [kind, resourceId, window.from, window.to, tick]);

  // 端点 1 无 total：limit 条即认为可能截断（spec §7.4），banner 显式说明按样本数排序。
  const truncated = rows.length >= EVALUATION_MONITOR_RESOURCE_LIMIT;
  const scoped = Boolean(kind || resourceId);

  const columns: ColumnsType<MonitorResourceSummary> = [
    { title: '资源', key: 'resource', render: (_, row) => `${displayLabel(row.resource_kind)} ${row.resource_id}` },
    { title: '样本', dataIndex: 'sample_count', width: 80, align: 'right' },
    { title: '质量通过率', dataIndex: 'quality', render: (value: MonitorResourceSummary['quality']) => value.length
      ? value.map((dim) => (
        <Tag key={dim.dimension} style={{ marginInlineEnd: 4 }}>{`${displayLabel(dim.dimension)} ${Math.round(dim.pass_rate * 100)}%`}</Tag>))
      : '—' },
    { title: '行为', key: 'behavior', render: (_, row) => joinNonEmpty([
      `规则命中 ${row.behavior.rule_hits}`,
      row.behavior.retry_count ? `重试 ${row.behavior.retry_count}` : '',
      row.behavior.escalation_count ? `升级 ${row.behavior.escalation_count}` : '',
      row.behavior.abandonment_count ? `放弃 ${row.behavior.abandonment_count}` : '',
    ]) },
    { title: '判定', key: 'verdict', render: (_, row) => {
      const verdict = row.behavior.verdict;
      return `通过 ${verdict.pass} / 待复核 ${verdict.flag} / 阻断 ${verdict.block}`;
    } },
    { title: '成本', key: 'cost', render: (_, row) => {
      const latency = row.cost.avg_latency_ms === null ? '—' : `${Math.round(row.cost.avg_latency_ms)}ms`;
      return joinNonEmpty([latency, `$${row.cost.total_cost_usd.toFixed(2)}`]);
    } },
    { title: '过程通过率', key: 'process', render: (_, row) => row.process
      ? `${Math.round(row.process.process_pass_rate * 100)}%` : '—' },
  ];

  return (
    <div data-testid="evaluation-monitor-panel">
      <Space wrap style={{ marginBottom: 12 }}>
        <RangePicker
          value={range}
          presets={windowPresets}
          allowClear={false}
          format="YYYY-MM-DD"
          onChange={(next) => { if (next && next[0] && next[1]) setRange([next[0].startOf('day'), next[1].endOf('day')]); }}
        />
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindOptions}
          value={kind} onChange={(value: ResourceKind | undefined) => { setKind(value); setResourceId(''); }} />
      </Space>
      {error
        ? <Alert type="error" showIcon message={error} action={<Button size="small" onClick={() => setTick((value) => value + 1)}>重试</Button>} />
        : rows.length === 0
          ? (loading ? <Skeleton active /> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={scoped ? '所选范围在本窗口内暂无评测观测样本' : '该时间窗口内暂无评测观测样本'} />)
          : <>
            {truncated && <Typography.Text type="warning" style={{ display: 'block', marginBottom: 12 }}>
              仅显示观测最多的前 {EVALUATION_MONITOR_RESOURCE_LIMIT} 个资源（按样本数排序）
            </Typography.Text>}
            <Table<MonitorResourceSummary> size="small" rowKey={(row) => `${row.resource_kind}:${row.resource_id}`}
              dataSource={rows} columns={columns} pagination={false} loading={loading}
              onRow={(row) => ({ onClick: () => setOpenRow(row), style: { cursor: 'pointer' } })} />
            {openRow && <MonitorResourceDrawer resource={openRow} open from={window.from} to={window.to}
              isMobile={isMobile} onClose={() => setOpenRow(null)} />}
          </>}
    </div>
  );
};
