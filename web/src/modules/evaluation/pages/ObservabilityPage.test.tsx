import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { ObservabilityPage } from './ObservabilityPage';

// 两个观测面板均有独立 e2e 级数据加载，页面测试只需关注页壳：Tab 布局与资源类型筛选透传。
vi.mock('../components/RuntimeHealthTrendPanel', () => ({
  RuntimeHealthTrendPanel: ({ defaultKind }: { defaultKind?: string }) => <div>健康趋势桩：{defaultKind ?? '全部'}</div>,
}));
vi.mock('../components/EvaluationMonitorPanel', () => ({
  EvaluationMonitorPanel: ({ defaultKind }: { defaultKind?: string }) => <div>监控桩：{defaultKind ?? '全部'}</div>,
}));

describe('ObservabilityPage', () => {
  it('composes health trend and monitoring as two tabs with an all-kind default', () => {
    render(<ObservabilityPage />);
    expect(screen.getByText('运行通过率趋势')).toBeInTheDocument();
    expect(screen.getByText('评测指标监控')).toBeInTheDocument();
    // 默认第一 Tab 挂载健康趋势桩且无 kind 透传。
    expect(screen.getByText('健康趋势桩：全部')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: '评测指标监控' }));
    expect(screen.getByText('监控桩：全部')).toBeInTheDocument();
  });
});
