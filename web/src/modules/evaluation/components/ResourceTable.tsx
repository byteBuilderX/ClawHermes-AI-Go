import { Button, Empty, Space, Table, Tag } from 'antd';

import { resourceDisplayName } from '../lib/resourceName';
import type { ResourceSummary } from '../model/evaluation';

import { ResourceNameCell } from './ResourceNameCell';
import { displayLabel, StatusTag } from './evaluationView';

interface Props {
  resources: ResourceSummary[];
  loading: boolean;
  filtered: boolean;
  /** 时间线抽屉（版本时间线/最近运行证据）。 */
  onOpen: (resource: ResourceSummary) => void;
  /** 提供时动作列拆出「详情」→ 资源证据页（版本账本/登记/产品外链）；缺省退化为单按钮。 */
  onOpenDetail?: (resource: ResourceSummary) => void;
}

export const ResourceTable = ({ resources, loading, filtered, onOpen, onOpenDetail }: Props) => (
  <Table<ResourceSummary>
    size="small" rowKey="id" dataSource={resources} loading={loading} pagination={false}
    locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
      description={filtered ? '没有找到符合条件的评测资源' : '评测资源还是空的'} /> }}
    columns={[
      { title: '资源', key: 'resource', render: (_, row: ResourceSummary) => (
        <ResourceNameCell name={resourceDisplayName(row)} resourceId={row.resource_id} />
      ) },
      { title: '类型', dataIndex: 'resource_kind', width: 100, render: (value: string) => <Tag>{displayLabel(value)}</Tag> },
      { title: '状态', dataIndex: 'status', width: 110, render: (value: string) => <StatusTag value={value} /> },
      { title: '最近运行', dataIndex: 'latest_run_status', width: 120, render: (value?: string) => <StatusTag value={value} /> },
      { title: '操作', width: onOpenDetail ? 150 : 90, fixed: 'right', render: (_, row) =>
        onOpenDetail
          ? <Space size={0}>
              <Button type="link" size="small" aria-label={`查看 ${row.resource_id} 时间线`}
                onClick={() => onOpen(row)}>时间线</Button>
              <Button type="link" size="small" aria-label={`查看 ${row.resource_id} 详情`}
                onClick={() => onOpenDetail(row)}>详情</Button>
            </Space>
          : <Button type="link" size="small" aria-label={`查看 ${row.resource_id} 详情`}
              onClick={() => onOpen(row)}>详情</Button> },
    ]}
    scroll={{ x: 680 }}
  />
);
