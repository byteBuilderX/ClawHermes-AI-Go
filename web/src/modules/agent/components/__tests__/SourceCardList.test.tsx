import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { ChatCitationSource } from '../../model/agent';
import { SourceCardList } from '../SourceCardList';

// SourceCardList 从 '@/modules/knowledge' barrel 引入 DocPreviewDrawer；stub 成空
// 组件避免单测触发真实抽屉的预览网络请求，其它 barrel 导出保持真实（importOriginal）。
vi.mock('@/modules/knowledge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/knowledge')>();
  return { ...actual, DocPreviewDrawer: () => null };
});

const sources: ChatCitationSource[] = [
  {
    workspaceId: 'ws-1',
    workspaceName: '产品知识库',
    chunkId: 'chunk-1',
    documentId: 'doc-1',
    documentTitle: '用户手册.pdf',
    snippet: '退货流程：签收后 7 天内可无理由退换。',
    score: 0.91,
    hasScore: true,
  },
  { chunkId: 'chunk-2', documentId: 'doc-2' },
];

describe('SourceCardList', () => {
  it('渲染文档名、snippet 与分数徽标（camelCase 字段）', () => {
    render(<SourceCardList sources={sources} />);

    expect(screen.getByText('用户手册.pdf')).toBeInTheDocument();
    expect(screen.getByText(/退货流程/)).toBeInTheDocument();
    expect(screen.getByText('91.0%')).toBeInTheDocument();
    // 无 workspaceName/documentTitle 的第二条回落 DocumentID，不渲染 snippet。
    expect(screen.getByText('doc-2')).toBeInTheDocument();
  });

  it('无来源或空数组时不渲染任何卡片', () => {
    const { container } = render(<SourceCardList sources={undefined} />);
    expect(container.innerHTML).toBe('');

    const { container: c2 } = render(<SourceCardList sources={[]} />);
    expect(c2.innerHTML).toBe('');
  });
});

// 注：spec §4 测试清单中的「点击可开预览」断言不放入组件单测——需跨模块 stub
// 真实 DocPreviewDrawer（其打开会发起预览网络请求），属 E2E 边界；由 Task 8
// 验收标准 1（live 来源卡片可点开预览）在无头浏览器中覆盖。
