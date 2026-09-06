import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { MonitorResourceSummary, MonitorTrend } from '../model/evaluation';

import { MonitorResourceDrawer } from './MonitorResourceDrawer';

const mocks = vi.hoisted(() => ({ getMonitorTrend: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({
  evaluationApi: { getMonitorTrend: mocks.getMonitorTrend },
}));

const resource: MonitorResourceSummary = {
  resource_kind: 'skill', resource_id: 'skill-a', sample_count: 4, quality: [],
  behavior: { rule_hits: 3, retry_count: 1, escalation_count: 0, abandonment_count: 0,
    verdict: { pass: 2, flag: 2, block: 0 } },
  cost: { total_tokens: 12000, total_cost_usd: 0.03, avg_latency_ms: 900, p95_latency_ms: 1200 },
  process: { process_pass_rate: 0.67, run_id: 'run-9', run_created_at: '2026-09-01T08:00:00Z' },
};

// trendTwo 两条日桶：quality 仅出现在观测样本的维度；day2 无延迟样本（avg/p95 null，
// 折线断开而非跨空连点）；token/成本为每日合计，跨桶求和得窗口总量。
const trendTwo: MonitorTrend = {
  resource_kind: 'skill', resource_id: 'skill-a',
  series: [
    { bucket_at: '2026-09-01T00:00:00Z', sample_count: 3,
      quality: [{ dimension: 'faithfulness', pass_rate: 0.92, avg_score: 0.92, avg_confidence: 0.87, samples: 3 }],
      behavior: { rule_hits: 2, retry_count: 1, escalation_count: 0, abandonment_count: 0,
        verdict: { pass: 2, flag: 1, block: 0 } },
      cost: { total_tokens: 10000, total_cost_usd: 0.02, avg_latency_ms: 900, p95_latency_ms: 1200 } },
    { bucket_at: '2026-09-02T00:00:00Z', sample_count: 1,
      quality: [{ dimension: 'faithfulness', pass_rate: 0.67, avg_score: 0.67, avg_confidence: 0.6, samples: 1 },
        { dimension: 'relevance', pass_rate: 1, avg_score: 1, avg_confidence: 0.9, samples: 1 }],
      behavior: { rule_hits: 1, retry_count: 0, escalation_count: 0, abandonment_count: 0,
        verdict: { pass: 0, flag: 1, block: 0 } },
      cost: { total_tokens: 2000, total_cost_usd: 0.01, avg_latency_ms: null, p95_latency_ms: null } },
  ],
  runs: [{ run_id: 'run-9', process_pass_rate: 0.67, run_created_at: '2026-09-01T08:00:00Z' }],
};

const window = { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' };

describe('MonitorResourceDrawer', () => {
  beforeEach(() => { mocks.getMonitorTrend.mockReset(); });

  it('fetches the trend for the resource window and renders summary + section charts', async () => {
    mocks.getMonitorTrend.mockResolvedValue(trendTwo);
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);

    expect(await screen.findByText('质量趋势')).toBeInTheDocument();
    // 单资源下钻携带 kind/id 与面板同窗 from/to。
    expect(mocks.getMonitorTrend).toHaveBeenCalledWith({
      resource_kind: 'skill', resource_id: 'skill-a', from: window.from, to: window.to,
    });
    // 汇总：跨桶求和（sample 3+1、token 10000+2000、usd 0.02+0.01）。
    expect(screen.getByText('总样本 4')).toBeInTheDocument();
    expect(screen.getByText('总成本 $0.03')).toBeInTheDocument();
    // 质量多线按实际维度成线（faithfulness/relevance 图例），null 延迟当日不断 quality 线。
    expect(screen.getByText('faithfulness')).toBeInTheDocument();
    expect(screen.getByText('relevance')).toBeInTheDocument();
    expect(screen.getByText('行为趋势')).toBeInTheDocument();
    expect(screen.getByText('成本趋势')).toBeInTheDocument();
    // 过程基线列出窗口内 succeeded run，process 0.67 不触发恒 1.0 注记。
    expect(screen.getByText('run-9')).toBeInTheDocument();
    expect(screen.getByText('67%')).toBeInTheDocument();
    expect(screen.queryByText(/恒为 1.0/)).not.toBeInTheDocument();
  });

  it('breaks latency lines on null days and renders an honest empty state when all latency is null', async () => {
    mocks.getMonitorTrend.mockResolvedValue({
      ...trendTwo,
      series: trendTwo.series.map((point) => ({ ...point,
        cost: { ...point.cost, avg_latency_ms: null, p95_latency_ms: null } })),
    });
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText('窗口内无延迟样本')).toBeInTheDocument();
  });

  it('shows the process empty state and the always-1.0 caveat for a run without process assertions', async () => {
    mocks.getMonitorTrend.mockResolvedValue({
      ...trendTwo,
      runs: [{ run_id: 'run-fn', process_pass_rate: 1, run_created_at: '2026-09-01T08:00:00Z' }],
    });
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText(/恒为 1.0/)).toBeInTheDocument();
  });

  it('shows the process empty state when the window has no succeeded run', async () => {
    mocks.getMonitorTrend.mockResolvedValue({ ...trendTwo, runs: [] });
    render(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText('该时间窗口内无离线评测过程基线')).toBeInTheDocument();
  });

  it('keeps previous content hidden while closed', async () => {
    mocks.getMonitorTrend.mockResolvedValue(trendTwo);
    const { rerender } = render(<MonitorResourceDrawer resource={resource} open={false}
      from={window.from} to={window.to} onClose={() => {}} />);
    expect(screen.queryByText('质量趋势')).not.toBeInTheDocument();
    expect(mocks.getMonitorTrend).not.toHaveBeenCalled();
    rerender(<MonitorResourceDrawer resource={resource} open from={window.from} to={window.to} onClose={() => {}} />);
    expect(await screen.findByText('质量趋势')).toBeInTheDocument();
    await waitFor(() => expect(mocks.getMonitorTrend).toHaveBeenCalledTimes(1));
  });
});
