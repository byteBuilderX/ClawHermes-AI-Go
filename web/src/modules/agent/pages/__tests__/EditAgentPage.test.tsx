import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { Form } from 'antd';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { agentApi } from '../../api/agent.api';
import { useEditAgentPage } from '../../hooks/useEditAgentPage';
import type { Agent, AgentVersionDetail } from '../../model/agent';
import { EditAgentPage } from '../EditAgentPage';

vi.mock('../../hooks/useEditAgentPage', () => ({ useEditAgentPage: vi.fn() }));
vi.mock('../../components/AgentFormSections', () => ({
  AgentFormSections: () => <div>普通 Agent 完整表单</div>,
}));
// B6「详情」Drawer：版本历史 tab 拉列表行，点「详情」按 parentVersionId 拉单版内容。
vi.mock('../../api/agent.api', () => ({
  agentApi: { listVersions: vi.fn(), getVersion: vi.fn(), rollback: vi.fn() },
}));

// F2：页面直接使用 useTenantRole（isAdmin 门控编辑/只读），测试 mock 返回值。
vi.mock('@/modules/iam', () => ({
  useTenantRole: () => ({ role: 'member', isAdmin: false, isOwner: false, isMember: true, hasTenantRole: () => false }),
}));
const { operationProposalApiMock } = vi.hoisted(() => ({
  operationProposalApiMock: { requestEditorAccess: vi.fn() },
}));
vi.mock('@/modules/operation-gate', () => ({ operationProposalApi: operationProposalApiMock }));

const baseHook = {
  loading: false,
  pageLoading: false,
  skills: [],
  mcpTools: [],
  workspaces: [],
  groupedModels: [],
  managementPath: '/agents',
  navigate: vi.fn(),
  onFinish: vi.fn(),
  readOnly: false,
  refreshTick: 0,
  reloadAgent: vi.fn(),
  editorCandidates: [],
  editorCandidatesLoading: false,
};

const renderPage = (name: string, id: string, overrides: Partial<typeof baseHook> = {}) => {
  const agent: Agent = {
    id,
    name,
    description: '',
    type: 'react',
    systemPrompt: '',
    llmModel: '',
    allowedSkills: [],
    mcpToolIds: [],
    knowledgeWorkspaceIds: [],
    memoryScope: 'user',
  };
  const Harness = () => {
    const [form] = Form.useForm();
    vi.mocked(useEditAgentPage).mockReturnValue({
      ...baseHook,
      form,
      id,
      agent,
      ...overrides,
    } as ReturnType<typeof useEditAgentPage>);
    return <EditAgentPage />;
  };
  return render(<Harness />);
};

describe('EditAgentPage', () => {
  beforeEach(() => vi.clearAllMocks());

  it('shows the edit title and form for any agent', () => {
    renderPage('Stratum 平台助手', 'stratum-platform-assistant');

    // 等化后标题恒为「编辑 Agent」，不再区分平台助手与普通 Agent。
    expect(screen.getByText('编辑 Agent')).toBeInTheDocument();
    expect(screen.getByText('普通 Agent 完整表单')).toBeInTheDocument();
  });

  it('member 只读（非白名单）可点击「申请编辑权限」并提交 grant_editor 提案', async () => {
    renderPage('Stratum 平台助手', 'stratum-platform-assistant', { readOnly: true });

    // 按钮须在 Form 外可点：<Form disabled={readOnly}> 通过 DisabledContext 禁用表单内 Button。
    const requestBtn = await screen.findByRole('button', { name: /申请编辑权限/ });
    expect(requestBtn).toBeInTheDocument();
    expect(requestBtn.closest('form')).toBeNull();
    expect(screen.queryByRole('button', { name: /保存修改/ })).not.toBeInTheDocument();

    fireEvent.click(requestBtn);
    await waitFor(() => expect(operationProposalApiMock.requestEditorAccess).toHaveBeenCalledWith(
      'agent', 'stratum-platform-assistant', { resourceName: 'Stratum 平台助手' },
    ));
    expect(await screen.findByText('已提交，等待管理员审批')).toBeInTheDocument();
  });

  it('opens the version detail drawer diffing against the direct parent payload', async () => {
    const versionList = {
      versions: [
        { id: 'v2', versionNo: 2, status: 'published', source: 'manual', contentHash: 'h2', parentVersionId: 'v1',
          createdBy: 'u-1', createdByName: '管理员', createdAt: '2026-07-24T00:00:00Z', publishedAt: '2026-07-24T00:00:00Z',
          isCurrent: true, safeSummary: { name: 'Alpha' } },
        { id: 'v1', versionNo: 1, status: 'deprecated', source: 'manual', contentHash: 'h1', parentVersionId: '',
          createdBy: 'u-2', createdByName: 'Alice', createdAt: '2026-07-23T00:00:00Z', publishedAt: '2026-07-23T00:00:00Z',
          isCurrent: false, safeSummary: { name: 'Alpha' } },
      ],
    };
    // 列表行 + payload 的合成夹具：schema 的 optional().default() 使输出类型字段必填，
    // 逐键补齐（createdByName 等后端 join 现算字段不在单版内容 diff 关注内）。
    const detailOf = (id: string, versionNo: number, parentVersionId: string, payload: Record<string, unknown>): AgentVersionDetail => ({
      id, versionNo, parentVersionId, payload,
      status: 'published', source: 'manual', contentHash: 'h', createdBy: 'u-1', createdByName: '管理员',
      createdAt: '2026-07-24T00:00:00Z', publishedAt: '2026-07-24T00:00:00Z', isCurrent: false,
    });
    const detailByVersion: Record<string, AgentVersionDetail> = {
      // v2 相对直父 v1 改了 description（+迭代上限）。
      v2: detailOf('v2', 2, 'v1', { name: 'Alpha', description: '新描述', llm_model: 'qwen-plus', max_iterations: 8 }),
      v1: detailOf('v1', 1, '', { name: 'Alpha', description: '旧描述', llm_model: 'qwen-plus', max_iterations: 5 }),
    };
    vi.mocked(agentApi.listVersions).mockResolvedValue(versionList.versions);
    vi.mocked(agentApi.getVersion).mockImplementation(
      async (_id: string, versionId: string) => detailByVersion[versionId],
    );
    renderPage('Alpha', 'a1');

    fireEvent.click(screen.getByRole('tab', { name: '版本历史' }));
    // 列表加载出两行，首行 v2（当前生效）的「详情」入口可用。
    await screen.findByText('管理员');
    fireEvent.click((await screen.findAllByRole('button', { name: '详情' }))[0]);

    const drawer = await screen.findByRole('dialog');
    expect(within(drawer).getByText('版本 v2 字段变更')).toBeInTheDocument();
    // 变更前列展示直父 v1 的旧值，变更后列展示 v2 新值。
    expect(within(drawer).getByText('旧描述')).toBeInTheDocument();
    expect(within(drawer).getByText('新描述')).toBeInTheDocument();
    // onViewDetail 先取点击版再取直父 parentVersionId：末次拉取必须是 v1。
    const fetched = vi.mocked(agentApi.getVersion).mock.calls.map((call) => call[1]);
    expect(fetched[fetched.length - 1]).toBe('v1');
  });
});
