import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { ResourceSummary } from '../model/evaluation';

import { ResourceListPage } from './ResourceListPage';

const api = vi.hoisted(() => ({ listResources: vi.fn(), getTimeline: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

const resources: ResourceSummary[] = [{
  id: 'r1', resource_id: 'agent-1', status: 'active', stable_revision_id: 'rev-a', resource_kind: 'agent',
  latest_run_status: 'succeeded', safe_summary: { name: '客服 Agent' }, created_at: '2026-08-01T00:00:00Z',
}];

describe('ResourceListPage', () => {
  beforeEach(() => {
    api.listResources.mockReset();
    api.listResources.mockResolvedValue({ items: resources });
    api.getTimeline.mockReset();
    api.getTimeline.mockResolvedValue({ items: [] });
  });

  it('loads and renders registered resource rows', async () => {
    render(<ResourceListPage />);
    expect(await screen.findByText('客服 Agent')).toBeInTheDocument();
    expect(screen.getByText('agent-1')).toBeInTheDocument();
  });

  it('shows the empty state when nothing is registered', async () => {
    api.listResources.mockResolvedValue({ items: [] });
    render(<ResourceListPage />);
    expect(await screen.findByText('评测资源还是空的')).toBeInTheDocument();
  });

  it('opens the evidence timeline for a resource row', async () => {
    render(<ResourceListPage />);
    await screen.findByText('客服 Agent');
    (screen.getByRole('button', { name: '查看 agent-1' })).click();
    await waitFor(() => expect(api.getTimeline).toHaveBeenCalledWith('agent', 'agent-1'));
  });
});
