// 展示真名归并 helper：DTO 顶层 resource_name 优先，其次 safe_summary 内的
// name / label / resource_name；都没有则返回显式占位「—」。绝不回退到返回裸
// resource_id/revision_id 冒充名称（前端规范：id 只作 rowKey/路由/aria 等非可见
// 身份用途）。resourceDisplayName 只做纯取值，不依赖 antd，便于各渲染点组合。
export const resourceDisplayName = (
  row: {
    resource_name?: string;
    safe_summary?: Record<string, unknown>;
  },
): string => {
  const candidates = [
    row.resource_name,
    pickString(row.safe_summary?.name),
    pickString(row.safe_summary?.label),
    pickString(row.safe_summary?.resource_name),
  ];
  return candidates.find((value) => value !== undefined && value.trim() !== '')?.trim() || '—';
};

const pickString = (value: unknown): string | undefined => (typeof value === 'string' ? value : undefined);
