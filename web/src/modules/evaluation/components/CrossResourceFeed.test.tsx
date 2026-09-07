import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { CandidateSummary, ExperimentSummary, RunSummary } from '../model/evaluation';

import { CrossResourceFeed, buildFeedEntries } from './CrossResourceFeed';

const run: RunSummary = {
  id: 'run-1', resource_id: 'agent-1', revision_id: 'rev-1', status: 'succeeded', resource_kind: 'agent',
  passed: true, total_cases: 4, passed_cases: 4, created_at: '2026-08-02T00:00:00Z',
};
const candidate: CandidateSummary = {
  id: 'cand-1', resource_id: 'agent-1', revision_id: 'rev-2', parent_revision_id: 'rev-1', source: 'optimize',
  status: 'proposed', resource_kind: 'agent', state_version: 1, safe_diff: { changed_fields: [], changes: {}, parent_missing: false },
  created_at: '2026-08-03T00:00:00Z',
};
const experiment: ExperimentSummary = {
  id: 'exp-1', resource_id: 'agent-1', stable_revision_id: 'rev-1', canary_revision_id: 'rev-2', status: 'running',
  recommendation: 'promote', resource_kind: 'agent', stage_percent: 40, safety_stopped: false, state_version: 1,
  promotion_evidence: { eligible: false, gates: { quality: 'passed', cost: 'passed', latency: 'pending',
    error_rate: 'pending', security: 'passed' }, blockers: [] },
  created_at: '2026-08-04T00:00:00Z',
};

describe('buildFeedEntries', () => {
  it('合并三类记录并按 created_at 倒序（实验最新、运行最旧）', () => {
    const entries = buildFeedEntries([run], [candidate], [experiment]);
    expect(entries.map((entry) => entry.key)).toEqual(['experiment:exp-1', 'candidate:cand-1', 'run:run-1']);
    expect(entries[0].kind).toBe('experiment');
    expect(entries[2].kind).toBe('run');
  });
});

describe('CrossResourceFeed', () => {
  it('空记录时给出空态提示', () => {
    render(<CrossResourceFeed runs={[]} candidates={[]} experiments={[]} onOpen={vi.fn()} />);
    expect(screen.getByText('还没有评测活动记录')).toBeInTheDocument();
  });

  it('展示记录的归类标签与状态', () => {
    render(<CrossResourceFeed runs={[run]} candidates={[]} experiments={[]} onOpen={vi.fn()} />);
    expect(screen.getByText(/run-1/)).toBeInTheDocument();
    expect(screen.getByText('运行')).toBeInTheDocument();
    expect(screen.getByText('通过')).toBeInTheDocument();
  });

  it('点「查看」把该条记录交给 onOpen', () => {
    const onOpen = vi.fn();
    render(<CrossResourceFeed runs={[run]} candidates={[candidate]} experiments={[]} onOpen={onOpen} />);
    // candidate（08-03）新于 run（08-02），排首行。
    fireEvent.click(screen.getAllByRole('button', { name: '查看' })[0]);
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: 'cand-1', kind: 'candidate' }));
  });
});
