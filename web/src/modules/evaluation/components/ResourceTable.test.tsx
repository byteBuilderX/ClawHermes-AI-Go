import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { ResourceTable } from './ResourceTable';

const row = {
  id: 'resource-1', resource_id: 'skill-1', resource_kind: 'skill' as const, status: 'active',
  stable_revision_id: 'revision-1', latest_run_status: 'succeeded', safe_summary: { name: '客服技能' },
  created_at: '2026-07-23T00:00:00Z',
};

describe('ResourceTable', () => {
  it('keeps the operational ledger within five columns and opens facts', () => {
    const onOpen = vi.fn();
    render(<ResourceTable resources={[row]} loading={false} filtered={false} onOpen={onOpen} />);
    expect(screen.getAllByRole('columnheader')).toHaveLength(5);
    fireEvent.click(screen.getByRole('button', { name: '查看 skill-1 详情' }));
    expect(onOpen).toHaveBeenCalledWith(row);
  });

  it('splits timeline drawer and evidence page actions when detail navigation is wired', () => {
    const onOpen = vi.fn();
    const onOpenDetail = vi.fn();
    render(<ResourceTable resources={[row]} loading={false} filtered={false} onOpen={onOpen}
      onOpenDetail={onOpenDetail} />);
    fireEvent.click(screen.getByRole('button', { name: '查看 skill-1 时间线' }));
    expect(onOpen).toHaveBeenCalledWith(row);
    fireEvent.click(screen.getByRole('button', { name: '查看 skill-1 详情' }));
    expect(onOpenDetail).toHaveBeenCalledWith(row);
  });

  it('uses distinct Chinese guidance for empty and filtered results', () => {
    const { rerender } = render(<ResourceTable resources={[]} loading={false} filtered={false} onOpen={vi.fn()} />);
    expect(screen.getByText('评测资源还是空的')).toBeInTheDocument();
    rerender(<ResourceTable resources={[]} loading={false} filtered onOpen={vi.fn()} />);
    expect(screen.getByText('没有找到符合条件的评测资源')).toBeInTheDocument();
  });
});
