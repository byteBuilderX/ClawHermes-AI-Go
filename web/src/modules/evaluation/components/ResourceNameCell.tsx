import { Typography } from 'antd';

// ResourceNameCell 展示资源主文案 + 弱化资源 id：主文案是真实名称（由调用方经
// resourceDisplayName 归并），resource_id 只作核对用弱化次要行；主文案已是占位
// 或与 id 相同时省略次要行，绝不把裸 id 当主文案。
export const ResourceNameCell = ({ name, resourceId }: { name: string; resourceId?: string }) => (
  <>
    <Typography.Text strong>{name}</Typography.Text>
    {resourceId && resourceId !== name && (
      <Typography.Text type="secondary" style={{ display: 'block', fontSize: 12 }}>{resourceId}</Typography.Text>
    )}
  </>
);
