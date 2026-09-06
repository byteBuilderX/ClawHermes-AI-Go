import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { VersionDiffDrawer } from '../VersionDiffDrawer';

Object.defineProperty(window, 'matchMedia', { writable: true, value: vi.fn(() => ({
  matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(),
})) });

describe('VersionDiffDrawer', () => {
  it('renders friendly label, raw path and before/after values', () => {
    render(
      <VersionDiffDrawer
        open onClose={() => {}}
        title="版本字段详情"
        fieldLabels={{ 'evaluation.judge.model': '判定模型' }}
        before={{ 'evaluation.judge.model': 'qwen-max' }}
        after={{ 'evaluation.judge.model': 'deepseek-v3' }}
      />,
    );
    expect(screen.getByText('版本字段详情')).toBeInTheDocument();
    expect(screen.getByText('判定模型')).toBeInTheDocument();
    expect(screen.getByText('evaluation.judge.model')).toBeInTheDocument();
    expect(screen.getByText('qwen-max')).toBeInTheDocument();
    expect(screen.getByText('deepseek-v3')).toBeInTheDocument();
  });

  it('pretty-prints nested object values', () => {
    render(
      <VersionDiffDrawer
        open onClose={() => {}}
        before={{ spec: { old: { retry: 1 }, keep: 1 } }}
        after={{ spec: { retry: 2, keep: 1 } }}
      />,
    );
    expect(screen.getByText('spec.old')).toBeInTheDocument();
    expect(screen.getByText(/"retry": 1/)).toBeInTheDocument();
    expect(screen.getByText('spec.retry')).toBeInTheDocument();
  });

  it('shows empty state when snapshots are equal', () => {
    render(<VersionDiffDrawer open onClose={() => {}} before={{ a: 1 }} after={{ a: 1 }} />);
    expect(screen.getByText('该版本相对基线无字段变更')).toBeInTheDocument();
  });
});
