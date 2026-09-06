import { Drawer, Empty, Table, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMemo } from 'react';

import { computeFieldChanges } from '@/shared/lib';

const { Text, Paragraph } = Typography;

export interface VersionDiffDrawerProps {
  open: boolean;
  onClose: () => void;
  /** 详情标题；缺省为「版本字段详情」。 */
  title?: string;
  /** 字段路径 → 友好名；未映射的路径原样展示（如 `evaluation.judge.model`）。 */
  fieldLabels?: Record<string, string>;
  /** 基线内容快照（变更前）。 */
  before: Record<string, unknown>;
  /** 目标版本内容快照（变更后）。 */
  after: Record<string, unknown>;
}

interface DiffRow {
  key: string;
  path: string;
  label: string;
  before?: unknown;
  after?: unknown;
}

/** 值单元格：标量按文本、对象/数组 pretty JSON，超长折叠可展开。 */
const ValueCell = ({ value }: { value?: unknown }) => {
  if (value === undefined) return <Text type="secondary">—</Text>;
  const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
  if (text.length > 140) {
    return (
      <Paragraph style={{ marginBottom: 0 }} ellipsis={{ rows: 8, expandable: true, symbol: '展开' }}>
        {text}
      </Paragraph>
    );
  }
  return <Text style={{ whiteSpace: 'pre-wrap' }}>{text}</Text>;
};

const buildRows = (before: Record<string, unknown>, after: Record<string, unknown>, fieldLabels?: Record<string, string>): DiffRow[] =>
  computeFieldChanges(before, after).map((change) => ({
    key: change.path,
    path: change.path,
    label: fieldLabels?.[change.path] ?? change.path,
    before: change.before,
    after: change.after,
  }));

/** VersionDiffDrawer：展示某版本相对其基线逐字段「变更前 / 变更后」的统一详情。
 * 差异由 computeFieldChanges 现算（不落库、无写路径），组件只做展示。 */
export const VersionDiffDrawer = ({ open, onClose, title, fieldLabels, before, after }: VersionDiffDrawerProps) => {
  const rows = useMemo(() => buildRows(before, after, fieldLabels), [before, after, fieldLabels]);

  const columns: ColumnsType<DiffRow> = [
    {
      title: '字段', dataIndex: 'label', width: 280,
      render: (label: string, row: DiffRow) => (
        <div>
          <Text strong>{label}</Text>
          {row.label !== row.path && (
            <div><Text type="secondary" style={{ fontSize: 12 }}>{row.path}</Text></div>
          )}
        </div>
      ),
    },
    { title: '变更前', dataIndex: 'before', render: (v: unknown) => <ValueCell value={v} /> },
    { title: '变更后', dataIndex: 'after', render: (v: unknown) => <ValueCell value={v} /> },
  ];

  return (
    <Drawer title={title ?? '版本字段详情'} open={open} onClose={onClose} width={760}>
      <Table<DiffRow>
        rowKey="key" size="small" columns={columns} dataSource={rows} pagination={false}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该版本相对基线无字段变更" /> }}
      />
    </Drawer>
  );
};

export default VersionDiffDrawer;
