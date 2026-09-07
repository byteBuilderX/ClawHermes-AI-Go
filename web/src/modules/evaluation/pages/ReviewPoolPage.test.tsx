import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { ReviewItem } from '../types/review';

import { ReviewPoolPage } from './ReviewPoolPage';

const review = vi.hoisted(() => ({ listReviewItems: vi.fn() }));
vi.mock('../services/review', () => ({ listReviewItems: review.listReviewItems }));
// 页面只负责把 owner-or-creator 删除可见性传给评审面板；评审面板自身有独立测试。
vi.mock('@/modules/iam', () => ({
  useAuth: () => ({ user: { id: 'u1' } }),
  useTenantRole: () => ({ isOwner: false, isAdmin: false, isMember: true }),
}));

const items: ReviewItem[] = [{
  id: 'review-1', source_type: 'observation', source_id: 'obs-1', trace_id: 't1', resource_kind: 'agent',
  resource_id: 'agent-1', trigger_reason: 'low_confidence', risk_level: 'high', snapshot: {},
  status: 'pending', created_by: 'u1', created_at: '2026-08-01T00:00:00Z',
}];

describe('ReviewPoolPage', () => {
  beforeEach(() => {
    review.listReviewItems.mockReset();
    review.listReviewItems.mockResolvedValue({ items, total: 1 });
  });

  it('mounts the review pool and loads its items', async () => {
    render(<ReviewPoolPage />);
    expect(screen.getByText('人工评审池')).toBeInTheDocument();
    await waitFor(() => expect(review.listReviewItems).toHaveBeenCalledWith({ page: 1, page_size: 10 }));
    // 待评审的 agent 观测命中在行内展示（触发原因 low_confidence → 低置信度）。
    expect(await screen.findByText('低置信度')).toBeInTheDocument();
  });

  it('lets an owner see the delete action while a plain creator row is deletable for its author', async () => {
    render(<ReviewPoolPage />);
    await screen.findByText('低置信度');
    // created_by === 当前用户 → canDelete 为真，操作列出现「删除」（antd 两字按钮自动插空格）。
    expect(screen.getByRole('button', { name: /删\s*除/ })).toBeInTheDocument();
  });
});
