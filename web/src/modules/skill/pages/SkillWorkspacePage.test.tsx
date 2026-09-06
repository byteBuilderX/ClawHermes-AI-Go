import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { vi } from 'vitest';

import { SkillWorkspacePage } from './SkillWorkspacePage';

const { skillApiMock, operationProposalApiMock, roleState, evaluationApiMock } = vi.hoisted(() => ({
  skillApiMock: {
    getWorkspace: vi.fn(), updateSkill: vi.fn(), saveDraft: vi.fn(), publishDraft: vi.fn(),
    discardDraft: vi.fn(), listRevisions: vi.fn(), rollback: vi.fn(),
  },
  operationProposalApiMock: { requestEditorAccess: vi.fn() },
  roleState: { isAdmin: true },
  // 评测 tab 挂载 SkillEvaluationPanel，其唯一外部调用是建基线；隔离掉真实 evaluation.api。
  evaluationApiMock: { createBaseline: vi.fn() },
}));

const workspace = {
  skill: { id: 'skill-1', name: '测试 Skill', status: 'published', activeRevisionId: 'revision-1' },
  active: {
    id: 'revision-1', skillId: 'skill-1', status: 'published', revisionNo: 1,
    name: '测试 Skill', description: '用于测试', instructions: '按照步骤完成测试',
    contentHash: 'hash-v1',
  },
  editors: [],
};

vi.mock('../api/skill.api', () => ({ skillApi: skillApiMock }));
// SkillWorkspacePage 静态引入 SkillEvaluationPanel，后者 import '../../evaluation/api/evaluation.api'，
// 与本文件的 '../../evaluation/api/evaluation.api' 解析到同一绝对路径 → 同被此 mock 替换。
vi.mock('../../evaluation/api/evaluation.api', () => ({ evaluationApi: evaluationApiMock }));
vi.mock('@/modules/iam', () => ({
  useTenantRole: () => ({ isAdmin: roleState.isAdmin }),
  useAuth: () => ({ user: { sub: 'user-1' } }),
  useEditorCandidates: () => ({ candidates: [], loading: false }),
}));
vi.mock('@/modules/operation-gate', () => ({ operationProposalApi: operationProposalApiMock }));
Object.defineProperty(window, 'matchMedia', { writable: true, value: vi.fn(() => ({
  matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(),
})) });

const renderWorkspace = () => render(<MemoryRouter initialEntries={['/skills/skill-1/workspace']}><Routes>
  <Route path="/skills/:id/workspace" element={<SkillWorkspacePage />} />
</Routes></MemoryRouter>);

beforeEach(() => {
  vi.clearAllMocks();
  roleState.isAdmin = true;
  skillApiMock.getWorkspace.mockResolvedValue(workspace);
});

it('展示版本化编辑面：指令/可编辑人/版本历史', async () => {
  renderWorkspace();
  expect(await screen.findByRole('tab', { name: '指令' })).toBeInTheDocument();
  expect(screen.getByRole('tab', { name: '可编辑人' })).toBeInTheDocument();
  expect(screen.getByRole('tab', { name: '版本历史' })).toBeInTheDocument();
  expect(screen.queryByRole('tab', { name: 'Revision' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /发布当前 Revision/ })).not.toBeInTheDocument();
});

it('保存草稿：POST /skills/:id/draft 携带当前生效版本基线，成功后出现未发布提示', async () => {
  skillApiMock.saveDraft.mockResolvedValue({ ...workspace, hasDraft: true });
  // 首次加载为无草稿态；保存后重拉工作台，返回草稿态以显示「有未发布的草稿」提示条。
  skillApiMock.getWorkspace
    .mockResolvedValueOnce(workspace)
    .mockResolvedValue({ ...workspace, hasDraft: true });
  renderWorkspace();
  const instructions = await screen.findByLabelText('执行指令');
  fireEvent.change(instructions, { target: { value: '更新后的步骤' } });
  fireEvent.click(screen.getByRole('button', { name: /保存草稿/ }));

  await waitFor(() => expect(skillApiMock.saveDraft).toHaveBeenCalledWith('skill-1', {
    name: '测试 Skill', description: '用于测试', instructions: '更新后的步骤',
    expectedContentHash: 'hash-v1',
  }));
  expect(await screen.findByText(/有未发布的草稿/)).toBeInTheDocument();
});

it('版本历史列出当前生效与历史版本，回滚历史版本需确认', async () => {
  skillApiMock.listRevisions.mockResolvedValue([
    { ...workspace.active, isCurrent: true, createdAt: '2026-02-01T00:00:00Z' },
    {
      ...workspace.active, id: 'revision-0', status: 'deprecated', revisionNo: 1,
      isCurrent: false, createdAt: '2026-01-01T00:00:00Z',
    },
  ]);
  renderWorkspace();
  fireEvent.click(await screen.findByRole('tab', { name: '版本历史' }));

  expect(await screen.findByText('当前生效')).toBeInTheDocument();
  expect(screen.getByText('历史')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: '回滚' }));
  // antd Modal.confirm 同时渲染 ant-modal-title 与 ant-modal-confirm-title 两份标题。
  expect((await screen.findAllByText('回滚到版本 v1？')).length).toBeGreaterThan(0);

  // antd 中文双字按钮在字符间加字距空格（modal 确认按钮 name 为「回 滚」），用正则匹配。
  const confirmButtons = screen.getAllByRole('button', { name: /回\s*滚/ });
  fireEvent.click(confirmButtons[confirmButtons.length - 1]);
  await waitFor(() => expect(skillApiMock.rollback).toHaveBeenCalledWith('skill-1', 'revision-0'));
});

it('member 非白名单只读，可见「申请编辑权限」并提交 grant_editor 提案', async () => {
  roleState.isAdmin = false;
  renderWorkspace();
  const requestBtn = await screen.findByRole('button', { name: /申请编辑权限/ });
  expect(requestBtn).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /保存草稿/ })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /发布/ })).not.toBeInTheDocument();

  fireEvent.click(requestBtn);
  await waitFor(() => expect(operationProposalApiMock.requestEditorAccess).toHaveBeenCalledWith(
    'skill', 'skill-1', { resourceName: '测试 Skill' },
  ));
  expect(await screen.findByText('已提交，等待管理员审批')).toBeInTheDocument();
});

it('版本详情：点「详情」按直父 revision 组装编辑面 diff，展示变更前后值', async () => {
  // 两版：v2(revision-2) 自链 revision-1 直父，仅 description 改动；name/instructions
  // 两版相同 → Drawer 只出 description 一行。列表行已带完整编辑面 + parentRevisionId。
  skillApiMock.listRevisions.mockResolvedValue([
    { id: 'revision-2', skillId: 'skill-1', status: 'published', revisionNo: 2, parentRevisionId: 'revision-1',
      name: '测试 Skill', description: '更新后的描述(新)', instructions: '按照步骤完成测试',
      isCurrent: true, contentHash: 'hash-v2', createdBy: 'user-1', createdByName: '管理员',
      createdAt: '2026-02-01T00:00:00Z' },
    { id: 'revision-1', skillId: 'skill-1', status: 'deprecated', revisionNo: 1, parentRevisionId: '',
      name: '测试 Skill', description: '用于测试的描述(旧)', instructions: '按照步骤完成测试',
      isCurrent: false, contentHash: 'hash-v1', createdBy: 'user-1', createdByName: '管理员',
      createdAt: '2026-01-01T00:00:00Z' },
  ]);
  renderWorkspace();
  fireEvent.click(await screen.findByRole('tab', { name: '版本历史' }));

  await screen.findByText('当前生效');
  // 首行 v2 的「详情」：before = 直父 revision-1 内容，after = v2 内容。
  fireEvent.click((await screen.findAllByRole('button', { name: '详情' }))[0]);

  expect(await screen.findByText('版本 v2 字段变更')).toBeInTheDocument();
  expect(screen.getByText('用于测试的描述(旧)')).toBeInTheDocument();
  expect(screen.getByText('更新后的描述(新)')).toBeInTheDocument();
});

it('admin 打开「评测」tab 可为当前已发布 Skill 建立基线', async () => {
  evaluationApiMock.createBaseline.mockResolvedValue({ kind: 'skill', resource_id: 'skill-1', revision_id: 'revision-1' });
  renderWorkspace();
  fireEvent.click(await screen.findByRole('tab', { name: '评测' }));
  fireEvent.click(await screen.findByRole('button', { name: '建立评测基线并打开中心' }));

  await waitFor(() => expect(evaluationApiMock.createBaseline).toHaveBeenCalledWith('skill', 'skill-1'));
});

it('member 打开「评测」tab 仅见指向中心的只读链接', async () => {
  roleState.isAdmin = false;
  renderWorkspace();
  fireEvent.click(await screen.findByRole('tab', { name: '评测' }));

  const link = await screen.findByRole('link', { name: '打开评测与进化中心' });
  expect(link).toHaveAttribute('href', '/evaluations?kind=skill&resource_id=skill-1');
  expect(screen.queryByRole('button', { name: '建立评测基线并打开中心' })).not.toBeInTheDocument();
});

it('从未发布的 Skill 在评测 tab 提示先发布', async () => {
  skillApiMock.getWorkspace.mockResolvedValueOnce({ ...workspace, skill: { ...workspace.skill, activeRevisionId: undefined } });
  renderWorkspace();
  fireEvent.click(await screen.findByRole('tab', { name: '评测' }));

  expect(await screen.findByText('请先发布 Skill，再进行评测与优化。')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '建立评测基线并打开中心' })).not.toBeInTheDocument();
  expect(screen.queryByRole('link', { name: '打开评测与进化中心' })).not.toBeInTheDocument();
});
