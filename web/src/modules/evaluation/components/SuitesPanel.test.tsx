import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { SuiteSummary } from '../model/evaluation';

import { SuitesPanel } from './SuitesPanel';

const suites: SuiteSummary[] = [
  { id: 's1', name: '投诉分类基线', description: '技能检索基线', status: 'published', resource_kind: 'skill',
    active_version_no: 2, active_case_count: 5, created_at: '2026-07-23T00:00:00Z' },
  { id: 's2', name: '未发布草稿', description: '', status: 'draft', draft_revision_id: 'd1', draft_case_count: 1,
    created_at: '2026-07-24T00:00:00Z' },
];

const renderPanel = (overrides: Partial<Parameters<typeof SuitesPanel>[0]> = {}) => render(<SuitesPanel
  suites={suites} loading={false} canManage={false} onOpen={vi.fn()} onDelete={vi.fn()} canDelete={() => false}
  {...overrides} />);

describe('SuitesPanel', () => {
  it('lets any role open every suite row into the suite detail route', () => {
    const onOpen = vi.fn();
    renderPanel({ canManage: false, onOpen });

    const openButtons = screen.getAllByRole('button', { name: '打开' });
    expect(openButtons).toHaveLength(suites.length);
    fireEvent.click(openButtons[0]);
    expect(onOpen).toHaveBeenCalledWith(suites[0]);
    fireEvent.click(openButtons[1]);
    expect(onOpen).toHaveBeenCalledWith(suites[1]);
  });

  it('renders the current published version and prompts when nothing is published yet', () => {
    renderPanel();
    expect(screen.getByText('v2 · 5 个启用用例')).toBeInTheDocument();
    expect(screen.getByText('尚未发布')).toBeInTheDocument();
  });

  it('hides management entry for members and keeps the read-only empty hint', () => {
    renderPanel({ suites: [] });
    expect(screen.queryByRole('button', { name: /新建套件|管理评测集/ })).not.toBeInTheDocument();
    expect(screen.getByText('套件还是空的（仅管理员可管理）')).toBeInTheDocument();
  });

  it('shows delete only for rows the caller authorizes and fires onDelete', () => {
    const onDelete = vi.fn();
    renderPanel({ canManage: false, onDelete, canDelete: (suite) => suite.id === 's1' });

    // 仅 s1 授权删除，s2 不显示删除按钮（type="link" 按钮不插入空格，文本为「删除」）
    const deleteButtons = screen.getAllByRole('button', { name: '删除' });
    expect(deleteButtons).toHaveLength(1);
    fireEvent.click(deleteButtons[0]);
    expect(onDelete).toHaveBeenCalledWith(suites[0]);
  });
});
