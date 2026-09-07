import { Alert, Button, Flex, Select, Typography } from 'antd';
import { useState } from 'react';

import { ResourceTable } from '../components/ResourceTable';
import { TimelineDrawer } from '../components/TimelineDrawer';
import { kindFilterOptions } from '../components/evaluationView';
import { useEvaluationTimeline } from '../hooks/useEvaluationTimeline';
import { useResourceListPage } from '../hooks/useResourceListPage';
import type { ResourceKind } from '../model/evaluation';

import { useResponsive } from '@/shared/hooks';

export const ResourceListPage = () => {
  const { isMobile } = useResponsive();
  const [kind, setKind] = useState<ResourceKind | undefined>();
  const filtered = !!kind;
  const { resources, loading, error, reload } = useResourceListPage({ resource_kind: kind });
  const timeline = useEvaluationTimeline();

  return <div>
    <Flex justify="space-between" align="end" gap={16} wrap style={{ marginBottom: 12 }}>
      <div><Typography.Title level={4} style={{ margin: 0 }}>被测资源</Typography.Title>
        <Typography.Text type="secondary">Agent / 知识库 / 技能 / MCP 的建档状态与稳定版本</Typography.Text></div>
      <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
        value={kind} onChange={(value?: ResourceKind) => setKind(value)} />
    </Flex>
    {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }}
      action={<Button onClick={() => void reload()}>重试</Button>} />}
    <ResourceTable resources={resources} loading={loading} filtered={filtered}
      onOpen={(row) => void timeline.openTimeline(row)} />
    <TimelineDrawer events={timeline.events} open={timeline.open} loading={timeline.loading} error={timeline.error}
      isMobile={isMobile} onClose={timeline.closeTimeline} />
  </div>;
};
