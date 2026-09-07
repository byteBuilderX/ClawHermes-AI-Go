import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import type { TimelineEvent } from '../model/evaluation';

import { TimelinePanel } from './TimelinePanel';

const event: TimelineEvent = {
  id: 'evt-1', kind: 'run', status: 'succeeded', summary: '运行 7c2f 成功：通过 4/4', resource_id: 'agent-1',
  resource_kind: 'agent', created_at: '2026-08-01T00:00:00Z',
};

describe('TimelinePanel', () => {
  it('shows a skeleton while loading', () => {
    render(<TimelinePanel events={[]} loading />);
    expect(document.querySelector('.ant-skeleton')).toBeTruthy();
  });

  it('surfaces a load error instead of the rail', () => {
    render(<TimelinePanel events={[]} error="时间线加载失败" />);
    expect(screen.getByText('时间线加载失败')).toBeInTheDocument();
  });

  it('shows the empty state when nothing happened yet', () => {
    render(<TimelinePanel events={[]} />);
    expect(screen.getByText('时间线还是空的')).toBeInTheDocument();
  });

  it('renders each event as a timeline item with its timestamp', () => {
    render(<TimelinePanel events={[event]} />);
    expect(screen.getByText(event.summary)).toBeInTheDocument();
    expect(screen.getByText(/\d{4}\/\d{1,2}\/\d{1,2}/)).toBeInTheDocument();
  });
});
