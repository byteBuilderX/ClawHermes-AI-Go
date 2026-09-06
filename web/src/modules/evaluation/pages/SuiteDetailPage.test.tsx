import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { SuiteDetailPage } from './SuiteDetailPage';

const role = vi.hoisted(() => ({ isAdmin: true }));
vi.mock('@/modules/iam', () => ({ useTenantRole: () => role }));

const api = vi.hoisted(() => ({
  getSuiteDetail: vi.fn(),
  getSuiteDraft: vi.fn(),
  listSuiteVersions: vi.fn(),
  getSuiteRevision: vi.fn(),
  publishSuite: vi.fn(),
  addDraftCase: vi.fn(),
  deleteDraftCase: vi.fn(),
  generateSuiteCases: vi.fn(),
  updateDraftCase: vi.fn(),
  startNextDraft: vi.fn(),
}));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));

// 页面成功后调用 message.success/error，mock 掉避免 rc-notification 定时器在
// teardown 后触发 setState（与 EvaluationCenterPage.test 同一手法）。
const messageMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock('antd', async () => ({
  ...(await vi.importActual<typeof import('antd')>('antd')),
  message: { success: messageMocks.success, error: messageMocks.error },
}));

const draftCase = { id: 'c1', name: '标准问候', input: '你好', expected_output: '您好', assertion_mode: 'contains', enabled: true };
const judgeCase = {
  id: 'c2', name: '总结判定', input: '帮我总结', expected_output: '要点', assertion_mode: 'judge',
  judge_spec: { model: 'judge-v1', rubric: '覆盖度' }, enabled: true, source_trace_id: 'trace-1',
  generate_reason: '负样本优先',
};
const detail = {
  id: 's1', name: '投诉分类基线', description: '技能检索基线', status: 'draft', resource_kind: 'skill',
  active_revision_id: 'rev-2', draft_revision_id: 'draft-3', active_version_no: 2, active_case_count: 3,
  draft_case_count: 2, created_by: 'u-1', created_at: '2026-07-23T00:00:00Z',
};
const legacyDetail = {
  id: 's1', name: '投诉分类基线', description: '技能检索基线', status: 'published', resource_kind: 'skill',
  active_revision_id: 'rev-2', active_version_no: 2, active_case_count: 3,
  created_by: 'u-1', created_at: '2026-07-23T00:00:00Z',
};
const draftRev = {
  id: 'draft-3', suite_id: 's1', status: 'draft', resource_kind: 'skill', cases: [draftCase, judgeCase],
};
const versionRows = [
  { id: 'rev-2', version_no: 2, status: 'published', resource_kind: 'skill', created_by: 'u-1',
    published_at: '2026-07-24T00:00:00Z', enabled_case_count: 3 },
  { id: 'rev-1', version_no: 1, status: 'published', resource_kind: 'skill', created_by: 'u-1',
    published_at: '2026-07-23T00:00:00Z', enabled_case_count: 1 },
];
const revision2 = {
  id: 'rev-2', suite_id: 's1', version_no: 2, status: 'published', resource_kind: 'skill',
  cases: [{ id: 'v2-c1', name: '发布版问候', input: '在吗', expected_output: '在的', assertion_mode: 'contains', enabled: true }],
};

const renderPage = () => render(
  <MemoryRouter initialEntries={['/evaluations/suites/s1']}><Routes>
    <Route path="/evaluations/suites/:id" element={<SuiteDetailPage />} />
  </Routes></MemoryRouter>,
);

describe('SuiteDetailPage', () => {
  beforeEach(() => {
    role.isAdmin = true;
    Object.values(api).forEach((mock) => mock.mockReset());
    messageMocks.success.mockClear();
    messageMocks.error.mockClear();
    api.getSuiteDetail.mockResolvedValue(detail);
    api.getSuiteDraft.mockResolvedValue(draftRev);
    api.listSuiteVersions.mockResolvedValue(versionRows);
    api.getSuiteRevision.mockResolvedValue(revision2);
    api.publishSuite.mockResolvedValue({ id: 'rev-3', suite_id: 's1', version_no: 3, status: 'published', resource_kind: 'skill', cases: [] });
    api.startNextDraft.mockResolvedValue({ ...draftRev, id: 'draft-3' });
  });

  it('renders header meta, tabs and the admin draft editor with loaded cases', async () => {
    renderPage();
    expect(await screen.findByText('投诉分类基线')).toBeInTheDocument();
    expect(screen.getByText('技能')).toBeInTheDocument();
    expect(screen.getByText('v2 · 3 个启用用例')).toBeInTheDocument();
    expect(screen.getByText('草稿 2 个用例')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '草稿编辑' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '已发布版本' })).toBeInTheDocument();

    expect(api.getSuiteDraft).toHaveBeenCalledWith('s1');
    expect(await screen.findByText('标准问候')).toBeInTheDocument();
    expect(screen.getByText('总结判定')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '生成用例' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '添加用例' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '发 布' })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /编\s*辑/ })).toHaveLength(2);
  });

  it('keeps the draft read-only for members without authoring actions', async () => {
    role.isAdmin = false;
    renderPage();
    expect(await screen.findByText('标准问候')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '生成用例' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '添加用例' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '发 布' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /编\s*辑/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /删\s*除/ })).not.toBeInTheDocument();
  });

  it('publishes the draft and refreshes detail afterwards', async () => {
    renderPage();
    expect(await screen.findByText('标准问候')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '发 布' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: '发 布' }));
    await waitFor(() => expect(api.publishSuite).toHaveBeenCalledWith('s1'));
    await waitFor(() => expect(messageMocks.success).toHaveBeenCalledWith({
      content: '已发布 v3，已开启继承草稿', duration: 2,
    }));
    await waitFor(() => expect(api.getSuiteDetail).toHaveBeenCalledTimes(2));
  });

  it('adds a hand-authored case through the add modal', async () => {
    renderPage();
    expect(await screen.findByText('标准问候')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '添加用例' }));
    fireEvent.change(screen.getByLabelText('用例名称'), { target: { value: '新增问候' } });
    fireEvent.change(screen.getByLabelText('测试输入'), { target: { value: '在吗' } });
    fireEvent.change(screen.getByLabelText('期望输出'), { target: { value: '在的' } });
    const addDialog = await screen.findByRole('dialog');
    fireEvent.click(within(addDialog).getByRole('button', { name: '添 加' }));
    await waitFor(() => expect(api.addDraftCase).toHaveBeenCalledWith('s1', expect.objectContaining({
      name: '新增问候', input: '在吗', expected_output: '在的', assertion_mode: 'contains', enabled: true,
      tool_spec: undefined, step_judge: undefined,
    })));
    expect(messageMocks.success).toHaveBeenCalledWith({ content: '已添加草稿用例', duration: 2 });
  });

  it('deletes a draft case only after confirmation', async () => {
    renderPage();
    expect(await screen.findByText('标准问候')).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole('button', { name: /删\s*除/ })[0]);
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: '删 除' }));
    await waitFor(() => expect(api.deleteDraftCase).toHaveBeenCalledWith('s1', 'c1'));
    expect(messageMocks.success).toHaveBeenCalledWith({ content: '已删除草稿用例', duration: 2 });
  });

  it('lists published versions and loads a revision body on demand', async () => {
    renderPage();
    expect(await screen.findByText('投诉分类基线')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: '已发布版本' }));
    expect(await screen.findByText('v2')).toBeInTheDocument();
    expect(screen.getByText('当前使用')).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole('button', { name: /查\s*看/ })[0]);
    await waitFor(() => expect(api.getSuiteRevision).toHaveBeenCalledWith('s1', 'rev-2'));
    expect(await screen.findByText('发布版问候')).toBeInTheDocument();
  });

  it('offers the legacy inherit action to admins and transitions into the draft editor', async () => {
    api.getSuiteDetail.mockResolvedValueOnce(legacyDetail).mockResolvedValue(detail);
    renderPage();
    expect(await screen.findByText(/暂无编辑草稿/)).toBeInTheDocument();
    const startButton = screen.getByRole('button', { name: '从此版本新建草稿' });
    fireEvent.click(startButton);
    await waitFor(() => expect(api.startNextDraft).toHaveBeenCalledWith('s1'));
    expect(messageMocks.success).toHaveBeenCalledWith({ content: '已开启继承草稿', duration: 2 });
    expect(await screen.findByText('标准问候')).toBeInTheDocument();
    expect(api.getSuiteDetail).toHaveBeenCalledTimes(2);
  });

  it('shows legacy notice to members without the inherit action', async () => {
    role.isAdmin = false;
    api.getSuiteDetail.mockResolvedValue(legacyDetail);
    renderPage();
    expect(await screen.findByText(/暂无编辑草稿/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '从此版本新建草稿' })).not.toBeInTheDocument();
    expect(api.getSuiteDraft).not.toHaveBeenCalled();
  });

  it('surfaces the load error with a way back to the suite list', async () => {
    api.getSuiteDetail.mockRejectedValue({ response: { data: { error: '评测集不存在' } } });
    renderPage();
    expect(await screen.findByText('评测集不存在')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /返回评测集列表/ }).length).toBeGreaterThan(0);
  });
});
