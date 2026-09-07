import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { RegisterResourceModal } from './RegisterResourceModal';

const api = vi.hoisted(() => ({
  createBaseline: vi.fn(),
  listAgents: vi.fn(),
  listWorkspaces: vi.fn(),
}));
vi.mock('@/modules/agent/api/agent.api', () => ({ agentApi: { list: api.listAgents } }));
vi.mock('@/modules/knowledge/api/knowledge.api', () => ({ knowledgeApi: { list: api.listWorkspaces } }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: { createBaseline: api.createBaseline } }));
const messageMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock('antd', async () => ({
  ...(await vi.importActual<typeof import('antd')>('antd')),
  message: { success: messageMocks.success, error: messageMocks.error },
}));

// flushAsync 在 act 内推进微任务与一个宏任务：登记框打开/切类型时的候选加载
// （mockResolvedValue 微任务 → setOptions/setOptionLoading）在 act 内落定，避免
// setState 落在 act 外触发 "not wrapped in act" 警告。
const flushAsync = async () => { await act(async () => {
  await Promise.resolve();
  await Promise.resolve();
  await new Promise<void>((resolve) => { setTimeout(resolve, 0); });
}); };

// pickOption 打开指定下拉后点选文本，与仓库既有 AntD Select 交互一致。下拉虚拟列表
// 可能同时存在隐藏克隆项，故用 findAllByText 取渲染序最后一项点击。
const pickOption = async (comboName: string, optionText: string) => {
  fireEvent.mouseDown(screen.getByRole('combobox', { name: comboName }));
  const matches = await screen.findAllByText(optionText);
  fireEvent.click(matches[matches.length - 1]);
  await flushAsync();
};

// AntD 为二字按钮自动在字符间插入空格（登记 → 登 记），用锚定正则匹配。
const registerButton = () => screen.getByRole('button', { name: /^登\s*记$/ });

const agentFixtures = [
  { id: 'agent-1', name: '客服 Agent' },
  { id: 'agent-2', name: '' },
];
const workspaceFixtures = [{ name: '产品知识库' }, { name: '检索评测库' }];

describe('RegisterResourceModal', () => {
  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    Object.values(messageMocks).forEach((mock) => mock.mockReset());
    api.createBaseline.mockResolvedValue(undefined);
    api.listAgents.mockResolvedValue(agentFixtures);
    api.listWorkspaces.mockResolvedValue(workspaceFixtures);
  });

  it('loads agent candidates by default and submits a baseline for the picked agent', async () => {
    const onRegistered = vi.fn();
    const onClose = vi.fn();
    render(<RegisterResourceModal open registered={[]} onClose={onClose} onRegistered={onRegistered} />);
    // 默认被测类型 agent，加载 /agents 候选（无 URL 初值）。
    expect(screen.getByText('Agent')).toBeInTheDocument();
    await waitFor(() => expect(api.listAgents).toHaveBeenCalledTimes(1));
    expect(api.listWorkspaces).not.toHaveBeenCalled();

    await pickOption('被测资源', '客服 Agent');
    fireEvent.click(registerButton());

    await waitFor(() => expect(api.createBaseline).toHaveBeenCalledWith('agent', 'agent-1'));
    expect(onRegistered).toHaveBeenCalledWith('agent', 'agent-1');
    expect(messageMocks.success).toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled(); // 关闭由页面 onRegistered 回调负责
  });

  it('switches to knowledge workspaces and registers by workspace name', async () => {
    const onRegistered = vi.fn();
    render(<RegisterResourceModal open registered={[]} onClose={vi.fn()} onRegistered={onRegistered} />);
    await pickOption('被测类型', '知识库');

    await waitFor(() => expect(api.listWorkspaces).toHaveBeenCalledTimes(1));
    await pickOption('被测资源', '产品知识库');
    fireEvent.click(registerButton());

    await waitFor(() => expect(api.createBaseline).toHaveBeenCalledWith('knowledge', '产品知识库'));
    expect(onRegistered).toHaveBeenCalledWith('knowledge', '产品知识库');
  });

  it('surfaces a backend failure and keeps the form open for correction', async () => {
    api.createBaseline.mockRejectedValue(new Error('该被测对象不再支持建档'));
    const onRegistered = vi.fn();
    const onClose = vi.fn();
    render(<RegisterResourceModal open registered={[]} onClose={onClose} onRegistered={onRegistered} />);
    await pickOption('被测资源', '客服 Agent');
    fireEvent.click(registerButton());

    await waitFor(() => expect(messageMocks.error).toHaveBeenCalledWith(
      expect.objectContaining({ content: '该被测对象不再支持建档' })));
    expect(onRegistered).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(api.createBaseline).toHaveBeenCalledTimes(1);
  });

  it('shows the backend .error Chinese message when baseline fails with an API payload (e.g. 被测 Agent 缺 system_prompt)', async () => {
    // 建档 500→4xx 后，后端返回 {error: 中文, code}；toast 必须展示该中文，
    // 而不是 axios 的 "Request failed with status code 422"。
    const backendMessage = '该被测 Agent 尚未配置系统提示词，无法建档。请先在 Agent 配置中填写系统提示词后再登记';
    api.createBaseline.mockRejectedValue({ response: { data: { error: backendMessage, code: 'AGENT_SYSTEM_PROMPT_REQUIRED' } } });
    const onRegistered = vi.fn();
    render(<RegisterResourceModal open registered={[]} onClose={vi.fn()} onRegistered={onRegistered} />);
    await pickOption('被测资源', '客服 Agent');
    fireEvent.click(registerButton());

    await waitFor(() => expect(messageMocks.error).toHaveBeenCalledWith(
      expect.objectContaining({ content: backendMessage })));
    expect(onRegistered).not.toHaveBeenCalled();
    expect(api.createBaseline).toHaveBeenCalledTimes(1);
  });

  it('prefills from the deep link and offers register-then-run with an already-registered hint', async () => {
    const onRegisterThenRun = vi.fn();
    const onRegistered = vi.fn();
    render(<RegisterResourceModal open registered={[{ kind: 'knowledge', resource_id: 'kb-1' }]}
      onClose={vi.fn()} onRegistered={onRegistered} onRegisterThenRun={onRegisterThenRun}
      initial={{ kind: 'knowledge', resource_id: 'kb-1' }} />);
    await flushAsync();
    await waitFor(() => expect(api.listWorkspaces).toHaveBeenCalledTimes(1));

    // URL 初值：类型=知识库、资源=kb-1 已预选。
    expect(screen.getByText('知识库')).toBeInTheDocument();
    expect(screen.getByText('kb-1')).toBeInTheDocument();
    // 已登记提示：再次登记会把当前发布版本设为新的稳定基线。
    expect(screen.getByText(/已登记稳定版本/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '登记并新建评测' }));
    await waitFor(() => expect(api.createBaseline).toHaveBeenCalledWith('knowledge', 'kb-1'));
    expect(onRegisterThenRun).toHaveBeenCalledWith('knowledge', 'kb-1');
    expect(onRegistered).not.toHaveBeenCalled();
  });
});
