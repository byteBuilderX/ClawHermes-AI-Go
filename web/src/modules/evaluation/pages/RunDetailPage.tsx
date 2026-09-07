import { ArrowLeftOutlined } from '@ant-design/icons';
import { Alert, Button, Card, Empty, Flex, Space, Tag, Typography } from 'antd';
import { useNavigate, useParams } from 'react-router-dom';

import { RunDetailView } from '../components/RunDetailView';
import { displayLabel } from '../components/evaluationView';
import { useRunDetailPage } from '../hooks/useRunDetailPage';
import { resourceDisplayName } from '../lib/resourceName';

import { useResponsive } from '@/shared/hooks';

export const RunDetailPage = () => {
  const navigate = useNavigate();
  const { isMobile } = useResponsive();
  const { runId = '' } = useParams();
  const { run, runs, loading, error, reload } = useRunDetailPage({ runId });

  return <div>
    <Button type="link" icon={<ArrowLeftOutlined />} style={{ paddingLeft: 0, marginBottom: 8 }}
      onClick={() => navigate('/evaluations/runs')}>返回运行列表</Button>
    <Flex align="center" gap={8} wrap style={{ marginBottom: 12 }}>
      <Typography.Title level={4} style={{ margin: 0 }}>运行详情</Typography.Title>
      <Typography.Text code>{runId}</Typography.Text>
      {run && <>
        <Tag>{displayLabel(run.resource_kind)}</Tag>
        <Typography.Text type="secondary">{resourceDisplayName(run)} · 锚定版本 {run.revision_id}</Typography.Text>
      </>}
    </Flex>

    {error && <Alert type="error" showIcon style={{ marginBottom: 12 }} message={error}
      action={<Space wrap>
        <Button size="small" onClick={() => void reload()}>重试</Button>
        <Button size="small" onClick={() => navigate('/evaluations/runs')}>返回运行列表</Button>
      </Space>} />}
    {!error && loading && <Card loading />}
    {!error && !loading && !run && <Empty description="未找到该运行记录，可能已被删除或超出该资源最近列表。" />}
    {!error && run && <RunDetailView run={run} runs={runs} isMobile={isMobile} />}
  </div>;
};
