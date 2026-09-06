import { Button, Empty, Space, Table } from 'antd';

import type { SuiteSummary } from '../model/evaluation';

import { StatusTag } from './evaluationView';

// activeVersionLabel 把列表行的当前启用版本转成展示文案：无已发布版本时提示尚未发布。
const activeVersionLabel = (suite: SuiteSummary) => (
  suite.active_version_no ? `v${suite.active_version_no} · ${suite.active_case_count ?? 0} 个启用用例` : '尚未发布'
);

export const SuitesPanel = ({ suites, loading, canManage, onOpen, onDelete, canDelete }: {
  suites: SuiteSummary[]; loading: boolean; canManage: boolean;
  onOpen: (suite: SuiteSummary) => void;
  onDelete: (suite: SuiteSummary) => void; canDelete: (suite: SuiteSummary) => boolean;
}) => (
  <Table<SuiteSummary> size="small" rowKey="id" dataSource={suites} loading={loading} pagination={false}
    locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
      description={canManage ? '套件还是空的' : '套件还是空的（仅管理员可管理）'} /> }}
    columns={[
      { title: '名称', dataIndex: 'name', ellipsis: true },
      { title: '状态', dataIndex: 'status', width: 110, render: (value: string) => <StatusTag value={value} /> },
      { title: '当前版本', width: 190, render: (_: unknown, row: SuiteSummary) => activeVersionLabel(row) },
      { title: '操作', width: 130, render: (_: unknown, row: SuiteSummary) => <Space>
        <Button type="link" size="small" onClick={() => onOpen(row)}>打开</Button>
        {canDelete(row) && <Button type="link" size="small" danger onClick={() => onDelete(row)}>删除</Button>}
      </Space> },
    ]} />
);
