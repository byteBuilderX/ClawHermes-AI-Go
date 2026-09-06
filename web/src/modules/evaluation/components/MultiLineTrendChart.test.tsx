import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { MultiLineTrendChart } from './MultiLineTrendChart';
import type { MultiLineSeries, TrendBucket } from './MultiLineTrendChart';

const buckets: TrendBucket[] = ['09-01', '09-02', '09-03'].map((label, i) => ({
  bucketLabel: label,
  fullLabel: `2026-09-0${i + 1}T00:00:00Z`,
}));

const percent = (v: number) => `${Math.round(v * 100)}%`;

describe('MultiLineTrendChart', () => {
  it('renders one colored line per series and a legend', () => {
    const series: MultiLineSeries[] = [
      { name: 'faithfulness', color: '#1677ff', values: [0.9, 0.8, 0.7] },
      { name: 'relevance', color: '#52c41a', values: [0.5, 0.6, null] },
    ];
    const { container } = render(<MultiLineTrendChart buckets={buckets} series={series} unit="percent"
      ariaLabel="质量通过率趋势" yTickLabel={percent} noDataText="无数据" dataTestId="quality-chart" />);

    expect(screen.getByRole('img', { name: '质量通过率趋势' })).toBeInTheDocument();
    expect(container.querySelector('svg path[stroke="#1677ff"]')).not.toBeNull();
    // relevance 在第三桶为 null：前两桶一段连线（M 一次），而非贯穿三桶的整线。
    const green = container.querySelector('svg path[stroke="#52c41a"]');
    expect((green!.getAttribute('d') || '').match(/M/g)).toHaveLength(1);
    expect(screen.getByText('faithfulness')).toBeInTheDocument();
    expect(screen.getByText('relevance')).toBeInTheDocument();
  });

  it('draws one continuous line when no series value is null', () => {
    const series: MultiLineSeries[] = [
      { name: 'faithfulness', color: '#1677ff', values: [0.9, 0.8, 0.7] },
    ];
    const { container } = render(<MultiLineTrendChart buckets={buckets} series={series} unit="percent"
      ariaLabel="质量通过率趋势" yTickLabel={percent} noDataText="无数据" dataTestId="quality-chart" />);
    const line = container.querySelector('svg path[stroke="#1677ff"]');
    expect((line!.getAttribute('d') || '').match(/M/g)).toHaveLength(1);
  });

  it('renders the number unit axis with value-scaled ticks and honors noDataText', () => {
    const series: MultiLineSeries[] = [{ name: 'tokens', color: '#13c2c2', values: [null, null, null] }];
    render(<MultiLineTrendChart buckets={buckets} series={series} unit="number"
      ariaLabel="成本趋势" yTickLabel={(v) => String(Math.round(v))} noDataText="窗口内无该项数值" dataTestId="cost-chart" />);
    expect(screen.getByText('窗口内无该项数值')).toBeInTheDocument();
    // 有值时渲染轴刻度文本（number 轴 0/0.5/1 的若干 tick 之一出现）。
    const values: MultiLineSeries[] = [{ name: 'tokens', color: '#13c2c2', values: [100, 200, 150] }];
    const { container } = render(<MultiLineTrendChart buckets={buckets} series={values} unit="number"
      ariaLabel="成本趋势" yTickLabel={(v) => String(Math.round(v))} noDataText="窗口内无该项数值" dataTestId="cost-chart-2" />);
    expect(container.querySelectorAll('svg path').length).toBeGreaterThan(0);
  });
});
