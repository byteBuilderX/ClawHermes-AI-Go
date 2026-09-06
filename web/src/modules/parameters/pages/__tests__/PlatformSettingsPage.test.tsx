import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { parametersApi } from '../../api/parameters.api';
import type { ParameterDefinition } from '../../model/parameters';
import { PlatformSettingsPage } from '../PlatformSettingsPage';

import { PlatformAdminGate } from '@/modules/iam';

vi.mock('../../api/parameters.api', () => ({
  parametersApi: {
    schema: vi.fn(),
    list: vi.fn(),
    update: vi.fn(),
    versions: vi.fn(),
    createDraft: vi.fn(),
    publish: vi.fn(),
    rollback: vi.fn(),
  },
}));

vi.mock('@/modules/llm', () => ({
  llmApi: {
    listModels: vi.fn().mockResolvedValue([]),
    listProviders: vi.fn().mockResolvedValue([]),
  },
}));

// 默认以普通成员（user）身份运行：Gate 内 canEdit=false，读/写控件置灰。
// PlatformAdminGate → usePlatformRole → useAuth，必须 mock 才能解析平台角色。
const authState = vi.hoisted(() => ({ user: { global_role: 'user' } }));
vi.mock('@/modules/iam/components/AuthContext', () => ({ useAuth: () => authState }));

// 平台 scope 定义：非 0 默认值由 List 回填，缺失键 = 0/''/nil 默认（0=unset）。
const defs = (): ParameterDefinition[] => [
  {
    key: 'memory.enrich_temperature',
    scope: 'platform',
    category: '记忆',
    display_name: '记忆丰富温度',
    value_type: 'float',
    default: 0.1,
    description: '',
    optimizable: false,
    sensitive: false,
    visual_hint: { control: 'slider', min: 0, max: 1, step: 0.05 },
  },
  {
    key: 'evaluation.judge.temperature',
    scope: 'platform',
    category: '评测',
    display_name: '评测温度',
    value_type: 'float',
    default: 0,
    description: '',
    optimizable: false,
    sensitive: false,
    visual_hint: { control: 'number', min: 0, max: 1, step: 0.1 },
  },
  {
    key: 'memory.supersede_prompt',
    scope: 'platform',
    category: '记忆',
    display_name: '记忆取代提示词',
    value_type: 'string',
    default: '',
    description: '',
    optimizable: false,
    sensitive: false,
    visual_hint: { control: 'textarea' },
  },
  {
    key: 'memory.enrich_prompt',
    scope: 'platform',
    category: '记忆',
    display_name: '记忆丰富提示词',
    value_type: 'string',
    default: '',
    description: '',
    optimizable: false,
    sensitive: false,
    visual_hint: { control: 'textarea' },
  },
];

const clickTab = (name: string) => {
  fireEvent.click(screen.getByRole('tab', { name }));
};

describe('PlatformSettingsPage', () => {
  it('groups platform params into domain tabs and populates loaded values', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(defs());
    // 平台值必须回填到控件：这是"刷新后编辑参数变空"类回归的防线。
    vi.mocked(parametersApi.list).mockResolvedValue({
      'memory.enrich_prompt': '平台级富化提示词',
      'memory.supersede_prompt': '平台级取代提示词',
      'memory.enrich_temperature': 0.9,
    });

    render(<PlatformSettingsPage />);

    expect(await screen.findByRole('tab', { name: '记忆' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '评测' })).toBeInTheDocument();
    // 默认激活第一个领域 tab,加载值回填到控件。
    expect(await screen.findByDisplayValue('平台级富化提示词')).toBeInTheDocument();
    expect(screen.getByDisplayValue('平台级取代提示词')).toBeInTheDocument();
  });

  it('never renders resource defaults: resource-scope keys belong to the resource layer', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue([
      ...defs(),
      {
        key: 'agent.temperature',
        scope: 'resource',
        category: 'agent',
        display_name: 'Agent 温度',
        value_type: 'float',
        default: 0.7,
        description: '',
        optimizable: true,
        sensitive: false,
        visual_hint: { control: 'slider', min: 0, max: 1, step: 0.1 },
      },
      {
        key: 'memory.long_term_top_k',
        scope: 'resource',
        category: 'memory',
        display_name: '废弃记忆参数',
        value_type: 'int',
        default: 5,
        description: '',
        optimizable: false,
        sensitive: false,
        visual_hint: { control: 'slider', min: 1, max: 20, step: 1 },
      },
      {
        key: 'agent.bindings',
        scope: 'resource',
        category: 'agent',
        display_name: 'Agent 绑定',
        value_type: 'string',
        default: null,
        description: '',
        optimizable: false,
        sensitive: false,
        visual_hint: { control: 'textarea' },
      },
    ]);
    vi.mocked(parametersApi.list).mockResolvedValue({ 'agent.temperature': 0.3 });

    render(<PlatformSettingsPage />);

    expect(await screen.findByRole('tab', { name: '记忆' })).toBeInTheDocument();
    expect(screen.queryByText('资源默认值')).not.toBeInTheDocument();
    expect(screen.queryByText('Agent 温度')).not.toBeInTheDocument();
    expect(screen.queryByText('废弃记忆参数')).not.toBeInTheDocument();
    expect(screen.queryByText('Agent 绑定')).not.toBeInTheDocument();
    expect(screen.queryByText('会影响所有未单独配置的 Agent')).not.toBeInTheDocument();
  });

  it('shows per-tab default hints for missing keys and hides hints for set keys', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(defs());
    // 非 0 默认键被后端回填(已设置),0 默认键缺失
    vi.mocked(parametersApi.list).mockResolvedValue({ 'memory.enrich_temperature': 0.9 });

    render(<PlatformSettingsPage />);

    expect(await screen.findByText('记忆丰富温度')).toBeInTheDocument();
    // List 回填后 hint 消失：antd Form setFieldsValue 的 watch 更新可能滞后
    // 于 schema 渲染一帧（慢机器上明显），同步 queryByText 会命中中间帧。
    await waitFor(() => expect(screen.queryByText('默认：0.1')).not.toBeInTheDocument());

    clickTab('评测');
    expect(screen.getByText('评测温度')).toBeInTheDocument();
    expect(screen.getByText('默认：0（未设置）')).toBeInTheDocument();
  });

  it('shows the non-zero default hint when the key is missing entirely', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(defs());
    vi.mocked(parametersApi.list).mockResolvedValue({});

    render(<PlatformSettingsPage />);

    expect(await screen.findByText('默认：0.1')).toBeInTheDocument();
    clickTab('评测');
    expect(screen.getByText('默认：0（未设置）')).toBeInTheDocument();
  });

  it('shows 未设置（使用定义默认） for string keys with empty default and no prompt viewers', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(defs());
    vi.mocked(parametersApi.list).mockResolvedValue({});

    render(<PlatformSettingsPage />);

    expect(await screen.findByText('记忆取代提示词')).toBeInTheDocument();
    expect(screen.getAllByText('未设置（使用定义默认）')).toHaveLength(2);
    // S2：memory.*_prompt 无内置模板，不再渲染"查看默认提示词"。
    expect(screen.queryByRole('button', { name: '查看默认提示词' })).not.toBeInTheDocument();
  });

  it('renders no hint for toggle keys', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue([
      {
        key: 'rag.enabled',
        scope: 'platform',
        category: '其他',
        display_name: 'RAG 开关',
        value_type: 'bool',
        default: false,
        description: '',
        optimizable: false,
        sensitive: false,
        visual_hint: { control: 'toggle' },
      },
    ]);
    vi.mocked(parametersApi.list).mockResolvedValue({});

    render(<PlatformSettingsPage />);

    expect(await screen.findByRole('tab', { name: '其他' })).toBeInTheDocument();
    expect(screen.getByText('RAG 开关')).toBeInTheDocument();
    // toggle 恒被 List 返回,无缺失场景,不渲染 hint(副标题含"未设置"文案,故精确匹配 hint 文本)
    expect(screen.queryByText('默认：', { exact: false })).not.toBeInTheDocument();
    expect(screen.queryByText('未设置（使用定义默认）')).not.toBeInTheDocument();
  });

  it('saves only the active tab keys (per-domain independent save)', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(defs());
    vi.mocked(parametersApi.list).mockResolvedValue({
      'memory.enrich_temperature': 0.9,
      'evaluation.judge.temperature': 0.5,
    });
    vi.mocked(parametersApi.update).mockResolvedValue({});

    render(<PlatformSettingsPage />);
    await screen.findByText('记忆丰富温度');

    fireEvent.click(screen.getByRole('button', { name: '保存记忆参数' }));
    await waitFor(() =>
      expect(parametersApi.update).toHaveBeenLastCalledWith(
        expect.objectContaining({ 'memory.enrich_temperature': 0.9 }),
      ),
    );
    // 记忆 tab 保存只提交记忆领域 key,不包含评测 key。
    const memoryCalls = vi.mocked(parametersApi.update).mock.calls;
    const memoryCall = memoryCalls[memoryCalls.length - 1][0] as Record<string, unknown>;
    expect(Object.keys(memoryCall).every((k) => k.startsWith('memory.'))).toBe(true);

    clickTab('评测');
    fireEvent.click(screen.getByRole('button', { name: '保存评测参数' }));
    await waitFor(() =>
      expect(parametersApi.update).toHaveBeenLastCalledWith(
        expect.objectContaining({ 'evaluation.judge.temperature': 0.5 }),
      ),
    );
    const evalCalls = vi.mocked(parametersApi.update).mock.calls;
    const evalCall = evalCalls[evalCalls.length - 1][0] as Record<string, unknown>;
    expect(Object.keys(evalCall).every((k) => k.startsWith('evaluation.'))).toBe(true);
  });

  it('submits only keys present in the form (zero write-back for unset keys)', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(defs());
    vi.mocked(parametersApi.list).mockResolvedValue({});
    vi.mocked(parametersApi.update).mockResolvedValue({});

    render(<PlatformSettingsPage />);
    await screen.findByText('记忆丰富温度');

    fireEvent.click(screen.getByRole('button', { name: '保存记忆参数' }));

    await waitFor(() => expect(parametersApi.update).toHaveBeenCalledWith({}));
  });

  it('renders a page-level skeleton while loading and no editable page chrome', async () => {
    vi.mocked(parametersApi.schema).mockImplementation(() => new Promise(() => {}));
    vi.mocked(parametersApi.list).mockImplementation(() => new Promise(() => {}));

    render(<PlatformSettingsPage />);

    // 只渲染明确加载态，不出现"保存X参数"等半成品展示页元素。
    expect(document.querySelector('.ant-skeleton')).toBeTruthy();
    expect(screen.queryByRole('button', { name: '保存记忆参数' })).not.toBeInTheDocument();
  });

  it('submits empty embedding_model value to clear it back to unset (fail-closed)', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue([
      ...defs(),
      {
        key: 'memory.embedding_model',
        scope: 'platform',
        category: '记忆',
        display_name: '记忆嵌入模型',
        value_type: 'string',
        default: '',
        description: '',
        optimizable: false,
        sensitive: false,
        visual_hint: { control: 'embedding_model' },
      },
    ]);
    // 平台值已清空（空串）→ 保存时显式提交空串 = 主动未配置（fail-closed），
    // 不做"等于默认跳过"。
    vi.mocked(parametersApi.list).mockResolvedValue({ 'memory.embedding_model': '' });
    vi.mocked(parametersApi.update).mockResolvedValue({});

    render(<PlatformSettingsPage />);
    await screen.findByText('记忆嵌入模型');

    fireEvent.click(screen.getByRole('button', { name: '保存记忆参数' }));

    await waitFor(() =>
      expect(parametersApi.update).toHaveBeenCalledWith(
        expect.objectContaining({ 'memory.embedding_model': '' }),
      ),
    );
  });

  // —— 版本历史（配置变更审计视图）——

  // 仅后端有分组映射的 category（英文 agent/memory/evaluation/trace）挂版本历史；
  // 中文字段名 category 无映射，不触发版本拉取（既有用例已覆盖）。
  const memoryDefs = (): ParameterDefinition[] => [
    {
      key: 'memory.enrich_temperature',
      scope: 'platform',
      category: 'memory',
      display_name: '记忆丰富温度',
      value_type: 'float',
      default: 0.1,
      description: '',
      optimizable: false,
      sensitive: false,
      visual_hint: { control: 'slider', min: 0, max: 1, step: 0.05 },
    },
  ];
  const version = (
    id: number,
    seq: number,
    status: 'draft' | 'published' | 'archived',
    isCurrent: boolean,
    snapshot: Record<string, unknown>,
    base: number | null,
    message: string,
    createdBy: string,
    // created_by 的可读名（服务端 join）；不传表示旧数据无该字段 → 前端回退 created_by。
    createdByName?: string,
  ) => ({
    id,
    group_key: 'memory',
    version_seq: seq,
    status,
    is_current: isCurrent,
    snapshot,
    base_version_id: base,
    message,
    created_by: createdBy,
    created_by_name: createdByName,
    created_at: '2026-08-20T10:00:00Z',
  });

  it('renders version history as config audit view with current badge and detail drawer', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(memoryDefs());
    vi.mocked(parametersApi.list).mockResolvedValue({ 'memory.enrich_temperature': 0.9 });
    vi.mocked(parametersApi.versions).mockResolvedValue([
      version(2, 2, 'published', true, { 'memory.enrich_temperature': 0.9 }, 1, '调高温度', 'admin-1', '王五'),
      version(1, 1, 'published', false, { 'memory.enrich_temperature': 0.1 }, null, '初始化', 'system'),
    ]);

    render(<PlatformSettingsPage />);

    // 版本历史 = 审计视图标题与行信息（操作者/备注/版本号）。
    expect(await screen.findByText('版本历史（配置变更审计）')).toBeInTheDocument();
    expect(screen.getByText('v2')).toBeInTheDocument();
    expect(screen.getByText('调高温度')).toBeInTheDocument();
    // 操作者优先展示服务端 join 出的可读名 created_by_name，而非原始 actor。
    expect(screen.getByText('王五')).toBeInTheDocument();
    expect(screen.queryByText('admin-1')).not.toBeInTheDocument();
    // backfill 的 system 归因（无 users 命中 → created_by_name 回退原文）经映射显示为"系统"。
    expect(screen.getByText('系统')).toBeInTheDocument();
    // 服务端 is_current=true（production label 指向 v2）→ 当前生效徽标（v2 自身不回滚）。
    expect(screen.getByText('当前生效')).toBeInTheDocument();
    // antd 对双中文按钮自动插空格（"回 滚"），用正则匹配。
    expect(screen.getAllByRole('button', { name: /回\s*滚/ })).toHaveLength(1);

    // 每行都有「详情」→ 共享 Drawer：before = base_version_id(v1) 快照，after = 本版快照。
    fireEvent.click(screen.getAllByRole('button', { name: '详情' })[0]);
    expect(await screen.findByText('版本 v2 字段变更')).toBeInTheDocument();
    // Drawer 字段列用友好名 labelMap（表单 label 同名，打开前 1 份、打开后 2 份）。
    await waitFor(() =>
      expect(screen.getAllByText('记忆丰富温度').length).toBeGreaterThanOrEqual(2),
    );
    expect(screen.getByText('0.1')).toBeInTheDocument();
    expect(screen.getByText('0.9')).toBeInTheDocument();
  });

  it('renders diff of keys present on only one side via the drawer', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(memoryDefs());
    vi.mocked(parametersApi.list).mockResolvedValue({
      'memory.enrich_temperature': 0.9,
      'memory.new_param': 'x',
    });
    vi.mocked(parametersApi.versions).mockResolvedValue([
      version(
        2,
        2,
        'published',
        true,
        { 'memory.enrich_temperature': 0.9, 'memory.new_param': 'x' },
        1,
        '新增参数',
        'admin-1',
      ),
      version(
        1,
        1,
        'published',
        false,
        { 'memory.enrich_temperature': 0.1, 'memory.removed_param': 'old' },
        null,
        '初始化',
        'system',
      ),
    ]);

    render(<PlatformSettingsPage />);
    await screen.findByText('版本历史（配置变更审计）');

    // v2 vs base(v1)：enrich_temperature 值变更(0.1→0.9)、new_param 基线下不存在
    // （变更前 '—'）、removed_param 目标缺失（变更后 '—'）——共享 Drawer 对缺失侧
    // 渲染占位符，无值侧不渲染新增/删除标签。
    fireEvent.click(screen.getAllByRole('button', { name: '详情' })[0]);
    await screen.findByText('版本 v2 字段变更');
    expect(screen.getByText('0.9')).toBeInTheDocument();
    expect(screen.getByText('0.1')).toBeInTheDocument();
    expect(screen.getByText('x')).toBeInTheDocument();
    expect(screen.getByText('old')).toBeInTheDocument();
    // 仅存在单侧的两个字段各渲染一个 '—' 占位。
    expect(screen.getAllByText('—')).toHaveLength(2);
  });

  it('publishes a draft version after confirmation', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(memoryDefs());
    vi.mocked(parametersApi.list).mockResolvedValue({ 'memory.enrich_temperature': 0.9 });
    vi.mocked(parametersApi.versions).mockResolvedValue([
      version(3, 3, 'draft', false, { 'memory.enrich_temperature': 0.7 }, 2, '草稿调温', 'admin-1'),
      version(2, 2, 'published', true, { 'memory.enrich_temperature': 0.9 }, 1, '调高温度', 'admin-1'),
      version(1, 1, 'published', false, { 'memory.enrich_temperature': 0.1 }, null, '初始化', 'system'),
    ]);
    vi.mocked(parametersApi.publish).mockResolvedValue({ 'memory.enrich_temperature': 0.7 });

    render(<PlatformSettingsPage />);
    await screen.findByText('版本历史（配置变更审计）');
    // 初始拉取版本历史；记录基线计数。
    await waitFor(() => expect(parametersApi.versions).toHaveBeenCalled());
    const before = vi.mocked(parametersApi.versions).mock.calls.length;

    // 仅草稿行有"发布"按钮；确认弹窗后调用 publish(groupKey, versionId)。
    fireEvent.click(screen.getByRole('button', { name: /发\s*布/ }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: /发\s*布/ }));

    await waitFor(() => expect(parametersApi.publish).toHaveBeenCalledWith('memory', 3));
    // 发布成功 → 生效快照回传 → refreshTick 递增 → 版本历史重拉（计数增长）。
    await waitFor(() =>
      expect(vi.mocked(parametersApi.versions).mock.calls.length).toBeGreaterThan(before),
    );
  });

  it('rolls back to a historical published version after confirmation', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(memoryDefs());
    vi.mocked(parametersApi.list).mockResolvedValue({ 'memory.enrich_temperature': 0.9 });
    vi.mocked(parametersApi.versions).mockResolvedValue([
      version(2, 2, 'published', true, { 'memory.enrich_temperature': 0.9 }, 1, '调高温度', 'admin-1'),
      version(1, 1, 'published', false, { 'memory.enrich_temperature': 0.5 }, null, '初始化', 'system'),
    ]);
    vi.mocked(parametersApi.rollback).mockResolvedValue({ 'memory.enrich_temperature': 0.5 });

    render(<PlatformSettingsPage />);
    await screen.findByText('版本历史（配置变更审计）');
    // 初始拉取版本历史；记录基线计数。
    await waitFor(() => expect(parametersApi.versions).toHaveBeenCalled());
    const before = vi.mocked(parametersApi.versions).mock.calls.length;

    // v1 非当前生效 → 有回滚按钮；v2 当前生效 → 无回滚按钮。
    fireEvent.click(screen.getByRole('button', { name: /回\s*滚/ }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: /回\s*滚/ }));

    await waitFor(() => expect(parametersApi.rollback).toHaveBeenCalledWith('memory', 1));
    // 回滚不产生新版本，但生效快照变化 → 版本历史重拉（计数增长）。
    await waitFor(() =>
      expect(vi.mocked(parametersApi.versions).mock.calls.length).toBeGreaterThan(before),
    );
  });

  // —— 只读成员：Gate 包 → canEdit=false → 写控件全部置灰 ——

  it('disables the whole platform-parameter form for a plain member', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(defs());
    vi.mocked(parametersApi.list).mockResolvedValue({ 'memory.enrich_temperature': 0.9 });

    render(
      <PlatformAdminGate minRole="system_admin">
        <PlatformSettingsPage />
      </PlatformAdminGate>,
    );
    await screen.findByText('记忆丰富温度');

    // 只读提示条 + 表单级 disabled 级联（保存按钮与控件均置灰）
    expect(screen.getByText('只读模式')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '保存记忆参数' })).toBeDisabled();
  });

  it('disables version publish/rollback for a plain member', async () => {
    vi.mocked(parametersApi.schema).mockResolvedValue(memoryDefs());
    vi.mocked(parametersApi.list).mockResolvedValue({ 'memory.enrich_temperature': 0.9 });
    vi.mocked(parametersApi.versions).mockResolvedValue([
      version(3, 3, 'draft', false, { 'memory.enrich_temperature': 0.7 }, 2, '草稿调温', 'admin-1'),
      version(2, 2, 'published', true, { 'memory.enrich_temperature': 0.9 }, 1, '调高温度', 'admin-1'),
      version(1, 1, 'published', false, { 'memory.enrich_temperature': 0.1 }, null, '初始化', 'system'),
    ]);

    render(
      <PlatformAdminGate minRole="system_admin">
        <PlatformSettingsPage />
      </PlatformAdminGate>,
    );
    await screen.findByText('版本历史（配置变更审计）');

    expect(screen.getByRole('button', { name: /发\s*布/ })).toBeDisabled();
    expect(screen.getByRole('button', { name: /回\s*滚/ })).toBeDisabled();
  });
});
