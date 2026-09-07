import { Alert, Empty, Skeleton, Timeline, Typography } from 'antd';

import type { TimelineEvent } from '../model/evaluation';

import { StatusTag } from './evaluationView';

// 资源时间线的展示体：Drawer（TimelineDrawer）与资源详情页内嵌共用。
export const TimelinePanel = ({ events, loading, error }: {
  events: TimelineEvent[]; loading?: boolean; error?: string;
}) => {
  if (loading) {
    return <Skeleton active paragraph={{ rows: 6 }} />;
  }
  if (error) {
    return <Alert type="error" showIcon message={error} />;
  }
  if (!events.length) {
    return <Empty description="时间线还是空的" />;
  }
  return <Timeline className="evaluation-timeline-rail" items={events.map((event) => ({
    children: <><Typography.Text strong>{event.summary}</Typography.Text><br /><StatusTag value={event.status} />
      <Typography.Text type="secondary"> {new Date(event.created_at).toLocaleString('zh-CN')}</Typography.Text></>,
  }))} />;
};
