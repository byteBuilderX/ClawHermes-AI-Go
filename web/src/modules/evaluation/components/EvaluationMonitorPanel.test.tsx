import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { MonitorResourceSummary } from '../model/evaluation';

import { EvaluationMonitorPanel } from './EvaluationMonitorPanel';

import { EVALUATION_MONITOR_RESOURCE_LIMIT } from '@/constants';

const mocks = vi.hoisted(() => ({ listMonitorResources: vi.fn(), getMonitorTrend: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({
  evaluationApi: { listMonitorResources: mocks.listMonitorResources, getMonitorTrend: mocks.getMonitorTrend },
}));

const rowA: MonitorResourceSummary = {
  resource_kind: 'skill', resource_id: 'skill-a', sample_count: 128,
  quality: [{ dimension: 'faithfulness', pass_rate: 0.92, avg_score: 0.92, avg_confidence: 0.87, samples: 128 }],
  behavior: { rule_hits: 15, retry_count: 3, escalation_count: 1, abandonment_count: 0,
    verdict: { pass: 120, flag: 6, block: 2 } },
  cost: { total_tokens: 154000, total_cost_usd: 0.42, avg_latency_ms: 1800, p95_latency_ms: 5200 },
  process: { process_pass_rate: 0.67, run_id: 'run-9', run_created_at: '2026-09-02T08:00:00Z' },
};
const rowB: MonitorResourceSummary = {
  resource_kind: 'agent', resource_id: 'agent-x', sample_count: 3, quality: [],
  behavior: { rule_hits: 0, retry_count: 0, escalation_count: 0, abandonment_count: 0,
    verdict: { pass: 3, flag: 0, block: 0 } },
  cost: { total_tokens: 900, total_cost_usd: 0.003, avg_latency_ms: null, p95_latency_ms: null },
  process: null,
};
const emptyPage = { items: [], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } };

describe('EvaluationMonitorPanel', () => {
  beforeEach(() => { mocks.listMonitorResources.mockReset(); mocks.getMonitorTrend.mockReset(); });

  it('loads the top resource rows scoped by the default kind and resource', async () => {
    mocks.listMonitorResources.mockResolvedValue({ items: [rowA, rowB], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" defaultResourceId="skill-a" />);
    expect(await screen.findByText('技能 skill-a')).toBeInTheDocument();
    expect(mocks.listMonitorResources).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'skill', resource_id: 'skill-a', limit: EVALUATION_MONITOR_RESOURCE_LIMIT,
      from: expect.any(String), to: expect.any(String),
    }));
    // 过程/成本/判定列如实呈现：process null → 「—」，不假装 0%。「—」在空 quality/process
    // 等单元格可多次出现，用 getAllByText 断言存在（不得 getByText 期望唯一）。
    expect(screen.getByText('通过 120 / 待复核 6 / 阻断 2')).toBeInTheDocument();
    expect(screen.getByText('faithfulness 92%')).toBeInTheDocument();
    expect(screen.getByText(/1800ms/)).toBeInTheDocument();
    expect(screen.getByText('67%')).toBeInTheDocument();
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });

  it('opens the per-resource drawer and refetches its trend on row click', async () => {
    mocks.listMonitorResources.mockResolvedValue({ items: [rowA], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    mocks.getMonitorTrend.mockResolvedValue({ resource_kind: 'skill', resource_id: 'skill-a', series: [], runs: [] });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    fireEvent.click(await screen.findByText('技能 skill-a'));
    await waitFor(() => expect(mocks.getMonitorTrend).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'skill', resource_id: 'skill-a',
    })));
    expect(await screen.findByText('质量趋势')).toBeInTheDocument();
  });

  it('shows an honest empty state when the window has no observation', async () => {
    mocks.listMonitorResources.mockResolvedValue(emptyPage);
    render(<EvaluationMonitorPanel />);
    expect(await screen.findByText('该时间窗口内暂无评测观测样本')).toBeInTheDocument();
  });

  it('renders the error alert and refetches on retry', async () => {
    mocks.listMonitorResources.mockRejectedValueOnce(new Error('加载监控数据失败'));
    mocks.listMonitorResources.mockResolvedValueOnce({ items: [rowA], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    fireEvent.click(await screen.findByRole('button', { name: /重\s*试/ }));
    expect(await screen.findByText('技能 skill-a')).toBeInTheDocument();
    expect(mocks.listMonitorResources).toHaveBeenCalledTimes(2);
  });

  it('marks the row list truncated when the limit is reached', async () => {
    const items = Array.from({ length: EVALUATION_MONITOR_RESOURCE_LIMIT }, (_, index) => ({
      ...rowA, resource_id: `skill-${index}` }));
    mocks.listMonitorResources.mockResolvedValue({ items, window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    expect(await screen.findByText(/仅显示观测最多的前 20 个资源/)).toBeInTheDocument();
  });

  it('refetches with an earlier window when a RangePicker preset is chosen', async () => {
    mocks.listMonitorResources.mockResolvedValue({ items: [rowA], window: { from: '2026-08-27T00:00:00Z', to: '2026-09-03T00:00:00Z' } });
    render(<EvaluationMonitorPanel defaultKind="skill" />);
    await screen.findByText('技能 skill-a');
    const firstCall = mocks.listMonitorResources.mock.calls[0][0] as { from: string; to: string; limit: number };

    const panel = screen.getByTestId('evaluation-monitor-panel');
    const input = panel.querySelector('.ant-picker input');
    expect(input).not.toBeNull();
    fireEvent.focus(input as Element);
    fireEvent.click(input as Element);
    fireEvent.click(await screen.findByText('近 14 天'));

    await waitFor(() => expect(mocks.listMonitorResources).toHaveBeenCalledTimes(2));
    const secondCall = mocks.listMonitorResources.mock.calls[1][0] as { from: string; to: string; limit: number };
    // 「近 14 天」窗口起点更早、终点与近 7 天同（同为今天），避免依赖宿主 TZ 断言具体值。
    expect(new Date(secondCall.from).getTime()).toBeLessThan(new Date(firstCall.from).getTime());
    expect(secondCall.to).toBe(firstCall.to);
    expect(secondCall.limit).toBe(EVALUATION_MONITOR_RESOURCE_LIMIT);
  });
});
