import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { VersionHistory, type VersionRow } from '../VersionHistory';

Object.defineProperty(window, 'matchMedia', { writable: true, value: vi.fn(() => ({
  matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(),
})) });

const rows: VersionRow[] = [
  { id: 'v2', versionNo: 2, status: 'published', isCurrent: true, createdByName: 'Alice', createdBy: 'u1', createdAt: '2026-02-01T00:00:00Z' },
  { id: 'v1', versionNo: 1, status: 'deprecated', createdBy: 'u2', createdAt: '2026-01-01T00:00:00Z', canRollback: true },
];

describe('VersionHistory', () => {
  it('renders current marker, status, operator nickname and version no', () => {
    render(<VersionHistory rows={rows} />);
    expect(screen.getByText('当前生效')).toBeInTheDocument();
    expect(screen.getByText('已发布')).toBeInTheDocument();
    expect(screen.getByText('历史')).toBeInTheDocument();
    // 操作者优先展示昵称 createdByName。
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('v2')).toBeInTheDocument();
  });

  it('falls back to raw createdBy when nickname is missing', () => {
    render(<VersionHistory rows={rows} />);
    expect(screen.getByText('u2')).toBeInTheDocument();
  });

  it('renders rollback entry only when row canRollback and rollback injected', () => {
    const rollback = vi.fn().mockResolvedValue(undefined);
    const { rerender } = render(<VersionHistory rows={rows} />);
    expect(screen.queryByRole('button', { name: '回滚' })).not.toBeInTheDocument();

    rerender(<VersionHistory rows={rows} rollback={rollback} />);
    expect(screen.getByRole('button', { name: '回滚' })).toBeInTheDocument();
  });

  it('confirms rollback via modal then calls injected rollback', async () => {
    const rollback = vi.fn().mockResolvedValue(undefined);
    render(<VersionHistory rows={rows} rollback={rollback} />);
    fireEvent.click(screen.getByRole('button', { name: '回滚' }));
    // antd Modal.confirm 同时渲染 ant-modal-title 与 ant-modal-confirm-title 两份标题。
    expect((await screen.findAllByText('回滚到版本 v1？')).length).toBeGreaterThan(0);

    // antd 中文双字按钮在字符间加字距空格（modal 确认按钮 name 为「回 滚」），用正则匹配。
    const confirmButtons = screen.getAllByRole('button', { name: /回\s*滚/ });
    fireEvent.click(confirmButtons[confirmButtons.length - 1]);
    await waitFor(() => expect(rollback).toHaveBeenCalledWith(rows[1]));
  });

  it('renders detail button for every row only when onViewDetail injected', () => {
    const onViewDetail = vi.fn();
    const { rerender } = render(<VersionHistory rows={rows} />);
    expect(screen.queryByRole('button', { name: '详情' })).not.toBeInTheDocument();

    rerender(<VersionHistory rows={rows} onViewDetail={onViewDetail} />);
    expect(screen.getAllByRole('button', { name: '详情' })).toHaveLength(rows.length);
  });

  it('opens diff drawer with before/after after onViewDetail resolves', async () => {
    const onViewDetail = vi.fn().mockResolvedValue({ before: { name: 'old' }, after: { name: 'new' } });
    render(<VersionHistory rows={rows} onViewDetail={onViewDetail} />);

    fireEvent.click(screen.getAllByRole('button', { name: '详情' })[0]);
    await waitFor(() => expect(onViewDetail).toHaveBeenCalledWith(rows[0]));
    expect(await screen.findByText('版本字段详情')).toBeInTheDocument();
    expect(screen.getByText('old')).toBeInTheDocument();
    expect(screen.getByText('new')).toBeInTheDocument();
  });

  it('keeps drawer closed and clears loading when onViewDetail rejects', async () => {
    const onViewDetail = vi.fn().mockRejectedValue(new Error('boom'));
    render(<VersionHistory rows={rows} onViewDetail={onViewDetail} />);

    fireEvent.click(screen.getAllByRole('button', { name: '详情' })[0]);
    // 抓取失败弹错误提示、不弹 Drawer。
    expect(await screen.findByText('boom')).toBeInTheDocument();
    expect(screen.queryByText('版本字段详情')).not.toBeInTheDocument();
  });
});
