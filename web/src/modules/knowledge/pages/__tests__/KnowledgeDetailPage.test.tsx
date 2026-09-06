import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { knowledgeApi } from '../../api/knowledge.api';
import { useKnowledgeDetailPage } from '../../hooks/useKnowledgeDetailPage';
import type { WorkspaceVersion, WorkspaceVersionDetail } from '../../model/knowledge';
import { KnowledgeDetailPage } from '../KnowledgeDetailPage';

vi.mock('../../hooks/useKnowledgeDetailPage', () => ({ useKnowledgeDetailPage: vi.fn() }));
// B7「详情」Drawer：版本历史 Modal 拉列表行，点「详情」按 parentVersionId 拉单版内容。
vi.mock('../../api/knowledge.api', () => ({
  knowledgeApi: { getVersion: vi.fn(), listVersions: vi.fn(), rollback: vi.fn() },
}));
// 隔离本测试目标（页面把 onViewDetail 接进共享 VersionHistory）：其余展示组件置空，
// 避免引入各自的数据依赖与副作用。
vi.mock('../../components/DocAccessModal', () => ({ DocAccessModal: () => null }));
vi.mock('../../components/DocPreviewDrawer', () => ({ DocPreviewDrawer: () => null }));
vi.mock('../../components/WorkspaceConfigForm', () => ({ WorkspaceConfigForm: () => null }));
vi.mock('../../components/WorkspaceDetailHeader', () => ({ WorkspaceDetailHeader: () => null }));
vi.mock('../../components/WorkspaceDetailSkeleton', () => ({ WorkspaceDetailSkeleton: () => null }));
vi.mock('../../components/WorkspaceDocumentsTable', () => ({ WorkspaceDocumentsTable: () => null }));
vi.mock('../../components/WorkspaceQueryPanel', () => ({ WorkspaceQueryPanel: () => null }));
vi.mock('../../components/WorkspaceStatsCard', () => ({ WorkspaceStatsCard: () => null }));
vi.mock('../../components/WorkspaceUploadZone', () => ({ WorkspaceUploadZone: () => null }));

const baseHook = {
  name: 'kb',
  navigate: vi.fn(),
  isAdmin: true,
  canEdit: false,
  canRequestEditor: false,
  stats: { config: {} },
  statsLoading: false,
  configForm: undefined,
  configLoading: false,
  uploadLoading: false,
  queryForm: undefined,
  queryLoading: false,
  queryResult: undefined,
  documents: [],
  documentsLoading: false,
  deletingDocumentID: undefined,
  handleConfigSave: vi.fn(),
  handleDescriptionSave: vi.fn(),
  handleNameSave: vi.fn(),
  handleUpload: vi.fn(),
  handleQuery: vi.fn(),
  handleDeleteDocument: vi.fn(),
  userCandidates: [],
  userCandidatesLoading: false,
  roleCandidates: [],
  editOpen: false,
  setEditOpen: vi.fn(),
  editDoc: undefined,
  accessLoading: false,
  accessForm: undefined,
  handleOpenAccess: vi.fn(),
  handleSetDocAccess: vi.fn(),
  previewDoc: undefined,
  setPreviewDoc: vi.fn(),
  handlePreviewDocument: vi.fn(),
  versions: [] as WorkspaceVersion[],
  versionsOpen: true,
  setVersionsOpen: vi.fn(),
  versionsLoading: false,
  openVersions: vi.fn(),
  rollbackVersion: vi.fn(),
  undoEdits: vi.fn(),
};

const renderPage = (overrides: Partial<typeof baseHook> = {}) => {
  // mock 工厂返回类型与 hook 真实返回类型不必完全重叠，经 unknown 桥接（vitest mock）。
  vi.mocked(useKnowledgeDetailPage).mockReturnValue({
    ...baseHook,
    ...overrides,
  } as unknown as ReturnType<typeof useKnowledgeDetailPage>);
  return render(<KnowledgeDetailPage />);
};

describe('KnowledgeDetailPage', () => {
  beforeEach(() => vi.clearAllMocks());

  it('opens the version detail drawer diffing against the direct parent payload', async () => {
    // schema 的 optional().default() 使输出类型字段必填，逐键补齐列表行字段。
    const versionList: WorkspaceVersion[] = [
      { id: 'v2', versionNo: 2, status: 'published', source: 'manual', contentHash: 'h2', parentVersionId: 'v1',
        isCurrent: true, createdBy: 'u-1', createdByName: '管理员',
        createdAt: '2026-07-24T00:00:00Z', publishedAt: '2026-07-24T00:00:00Z', safeSummary: { name: 'kb' } },
      { id: 'v1', versionNo: 1, status: 'deprecated', source: 'manual', contentHash: 'h1', parentVersionId: '',
        isCurrent: false, createdBy: 'u-2', createdByName: 'Alice',
        createdAt: '2026-07-23T00:00:00Z', publishedAt: '2026-07-23T00:00:00Z', safeSummary: { name: 'kb' } },
    ];
    // payload 合成夹具：schema 的 optional().default() 使输出类型字段必填，逐键补齐
    // （createdByName 等后端 join 现算字段不在单版内容 diff 关注内）。
    const detailOf = (id: string, versionNo: number, parentVersionId: string, payload: Record<string, unknown>): WorkspaceVersionDetail => ({
      id, versionNo, parentVersionId, payload,
      status: 'published', source: 'manual', contentHash: 'h', createdBy: 'u-1', createdByName: '管理员',
      createdAt: '2026-07-24T00:00:00Z', publishedAt: '2026-07-24T00:00:00Z', isCurrent: false, safeSummary: {},
    });
    const detailByVersion: Record<string, WorkspaceVersionDetail> = {
      // v2 相对直父 v1 改了 description（+TopK）。
      v2: detailOf('v2', 2, 'v1', { name: 'kb', description: '新描述', config: { TopK: 8 } }),
      v1: detailOf('v1', 1, '', { name: 'kb', description: '旧描述', config: { TopK: 5 } }),
    };
    vi.mocked(knowledgeApi.getVersion).mockImplementation(
      async (_name: string, versionId: string) => detailByVersion[versionId],
    );
    renderPage({ versions: versionList });

    // Modal 内共享 VersionHistory 两行，点首行 v2 的「详情」。
    await screen.findByText('管理员');
    fireEvent.click((await screen.findAllByRole('button', { name: '详情' }))[0]);

    // Drawer 标题 + description 变更前后值（父 v1 旧值 vs v2 新值）。
    expect(await screen.findByText('版本 v2 字段变更')).toBeInTheDocument();
    expect(screen.getByText('旧描述')).toBeInTheDocument();
    expect(screen.getByText('新描述')).toBeInTheDocument();
    // onViewDetail 先取点击版再取直父 parentVersionId：末次拉取必须是 v1。
    await waitFor(() => {
      const fetched = vi.mocked(knowledgeApi.getVersion).mock.calls.map((call) => call[1]);
      expect(fetched[fetched.length - 1]).toBe('v1');
    });
  });
});
