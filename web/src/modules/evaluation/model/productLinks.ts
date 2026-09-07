// productLinks.ts 收拢「评测 → 产品模块」的只读外链与产品版本对照。
// productEditPath 把被测资源引导回产品模块的配置/版本页（不做双写）：promote 会经
// ApplyPublishedRevision 写回产品稳定版本，此处仅查看对应关系。pickVersionLabel 读
// eval 版本 safe_summary.version_label（建档时后端把产品发布版本写入
// resource_revisions.safe_summary）；缺值返回 undefined，展示侧显式占位 —。
import type { ResourceKind } from './evaluation';

export const productEditPath = (kind: ResourceKind, id: string): { path: string; label: string } | null => {
  switch (kind) {
    case 'agent': return { path: `/agents/${encodeURIComponent(id)}/edit`, label: 'Agent 配置页' };
    case 'knowledge': return { path: `/knowledge/${encodeURIComponent(id)}`, label: '知识库版本页' };
    case 'skill': return { path: `/skills/${encodeURIComponent(id)}/workspace`, label: '技能工作台' };
    case 'mcp': return { path: `/mcp/${encodeURIComponent(id)}/edit`, label: 'MCP 配置页' };
    default: return null;
  }
};

export const pickVersionLabel = (row: { safe_summary?: Record<string, unknown> }): string | undefined => {
  const value = row.safe_summary?.version_label;
  return typeof value === 'string' && value.trim() !== '' ? value : undefined;
};
