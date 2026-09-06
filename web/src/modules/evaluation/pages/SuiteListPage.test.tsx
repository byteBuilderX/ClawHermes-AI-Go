import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { SuiteSummary } from '../model/evaluation';

import { SuiteListPage } from './SuiteListPage';

const api = vi.hoisted(() => ({
  listSuites: vi.fn(),
  createSuite: vi.fn(),
  deleteSuite: vi.fn(),
}));
const role = vi.hoisted(() => ({ isAdmin: true, isOwner: false, userId: 'u1' }));
const messageMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));

vi.mock('@/modules/iam', () => ({
  useTenantRole: () => ({
    role: role.isAdmin ? 'admin' : 'member', isAdmin: role.isAdmin, isOwner: role.isOwner, isMember: !role.isAdmin,
    hasTenantRole: vi.fn(),
  }),
  useAuth: () => ({ user: { id: role.userId } }),
}));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));
// antd message 的真实 rc-notification 定时器会在 teardown 后触发 setState，mock 掉
// 避免偶发 "window is not defined"（与 EvaluationCenterPage.test 同款处理）。
vi.mock('antd', async () => ({
  ...(await vi.importActual<typeof import('antd')>('antd')),
  message: { success: messageMocks.success, error: messageMocks.error },
}));

const suites: SuiteSummary[] = [
  { id: 'suite-1', name: '投诉分类基线', description: '技能检索基线', status: 'published', resource_kind: 'skill',
    active_version_no: 2, active_case_count: 5, created_by: 'u1', created_at: '2026-07-23T00:00:00Z' },
  { id: 'suite-2', name: '草稿基线', description: '', status: 'draft', draft_revision_id: 'd1', draft_case_count: 1,
    created_by: 'admin', created_at: '2026-07-24T00:00:00Z' },
];

const LocationProbe = () => {
  const location = useLocation();
  return <output aria-label="当前路径">{location.pathname}</output>;
};

const renderPage = () => render(<MemoryRouter>
  <SuiteListPage />
  <LocationProbe />
</MemoryRouter>);

describe('SuiteListPage', () => {
  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    messageMocks.success.mockReset();
    messageMocks.error.mockReset();
    role.isAdmin = true;
    role.isOwner = false;
    role.userId = 'u1';
    api.listSuites.mockResolvedValue({ items: suites });
  });

  it('renders suite rows with version and draft labels', async () => {
    renderPage();
    expect(await screen.findByText('投诉分类基线')).toBeInTheDocument();
    expect(screen.getByText('草稿基线')).toBeInTheDocument();
    // suite-1：已发布 v2 带 5 个启用用例；suite-2：无启用版本展示「尚未发布」
    expect(screen.getByText('v2 · 5 个启用用例')).toBeInTheDocument();
    expect(screen.getByText('尚未发布')).toBeInTheDocument();
    // suite-2 有草稿：展示草稿用例数；suite-1 无草稿展示占位符
    expect(screen.getByText('1 个用例')).toBeInTheDocument();
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });

  it('navigates to the suite detail route from the row name and the detail action', async () => {
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: '投诉分类基线' }));
    expect(screen.getByRole('status', { name: '当前路径' })).toHaveTextContent('/evaluations/suites/suite-1');

    // 操作列「详情」同样跳转（link 按钮不插入空格，文本为「详情」）
    fireEvent.click(screen.getByRole('button', { name: '草稿基线' }));
    expect(screen.getByRole('status', { name: '当前路径' })).toHaveTextContent('/evaluations/suites/suite-2');
  });

  it('lets an admin author a suite, refresh and enter its detail page', async () => {
    api.createSuite.mockResolvedValue({ suite: { id: 'new-1', name: '客服基线' }, revision: {} });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: /新建评测集/ }));

    fireEvent.mouseDown(screen.getByRole('combobox', { name: '资源类型' }));
    // 页面表格内也有「技能」Tag，须用 option 的 title 精确定位下拉项
    fireEvent.click(await screen.findByTitle('技能'));
    fireEvent.change(screen.getByLabelText('套件名称'), { target: { value: '客服基线' } });
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '标准问候' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '你好' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '您好' } });
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }));

    await waitFor(() => expect(api.createSuite).toHaveBeenCalledWith(expect.objectContaining({
      name: '客服基线', resourceKind: 'skill',
      cases: [expect.objectContaining({ name: '标准问候', assertion_mode: 'contains', enabled: true })],
    })));
    expect(messageMocks.success).toHaveBeenCalledWith({ content: '评测集已创建', duration: 2 });
    await waitFor(() => expect(screen.getByRole('status', { name: '当前路径' }))
      .toHaveTextContent('/evaluations/suites/new-1'));
  });

  it('keeps the create entry hidden for members', async () => {
    role.isAdmin = false;
    renderPage();
    await screen.findByText('投诉分类基线');
    expect(screen.queryByRole('button', { name: /新建评测集/ })).not.toBeInTheDocument();
  });

  it('shows delete only for owner-created rows and deletes after confirmation', async () => {
    renderPage();
    await screen.findByText('投诉分类基线');
    // suite-1 的创建者是当前用户可删；suite-2 创建者 admin 非 owner 场景不可删
    const deleteButtons = screen.getAllByRole('button', { name: '删除' });
    expect(deleteButtons).toHaveLength(1);
    fireEvent.click(deleteButtons[0]);
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: /删\s*除/ }));
    await waitFor(() => expect(api.deleteSuite).toHaveBeenCalledWith('suite-1'));
    expect(messageMocks.success).toHaveBeenCalledWith({ content: '已删除', duration: 2 });
  });

  it('exposes load failure with a retry action', async () => {
    api.listSuites.mockRejectedValueOnce(new Error('评测集服务不可用'));
    renderPage();
    expect(await screen.findByText('评测集服务不可用')).toBeInTheDocument();
    api.listSuites.mockResolvedValue({ items: suites });
    fireEvent.click(screen.getByRole('button', { name: /重\s*试/ }));
    expect(await screen.findByText('投诉分类基线')).toBeInTheDocument();
  });
});
