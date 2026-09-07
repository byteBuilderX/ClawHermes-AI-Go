import { Alert, Button, Flex, Select, Typography } from 'antd';
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { RegisterResourceModal } from '../components/RegisterResourceModal';
import { ResourceTable } from '../components/ResourceTable';
import { TimelineDrawer } from '../components/TimelineDrawer';
import { kindFilterOptions } from '../components/evaluationView';
import { useEvaluationTimeline } from '../hooks/useEvaluationTimeline';
import { useResourceListPage } from '../hooks/useResourceListPage';
import type { RegistrableResourceKind, ResourceKind, ResourceSummary } from '../model/evaluation';
import { registrableResourceKinds } from '../model/evaluation';

import { useTenantRole } from '@/modules/iam';
import { useResponsive } from '@/shared/hooks';

// 已登记行 = 当前被测轨（agent/knowledge）的建档资源，供登记框提示「重新建档刷新稳定版本」。
const toRegisteredRows = (resources: ResourceSummary[]): Array<{ kind: RegistrableResourceKind; resource_id: string }> =>
  resources.flatMap((row) => registrableResourceKinds.includes(row.resource_kind as RegistrableResourceKind)
    ? [{ kind: row.resource_kind as RegistrableResourceKind, resource_id: row.resource_id }]
    : []);

export const ResourceListPage = () => {
  const navigate = useNavigate();
  const { isMobile } = useResponsive();
  const { isAdmin } = useTenantRole();
  const [kind, setKind] = useState<ResourceKind | undefined>();
  const [registerOpen, setRegisterOpen] = useState(false);
  const filtered = !!kind;
  const { resources, loading, error, reload } = useResourceListPage({ resource_kind: kind });
  const timeline = useEvaluationTimeline();
  const canManage = isAdmin;

  return <div>
    <Flex justify="space-between" align="end" gap={16} wrap style={{ marginBottom: 12 }}>
      <div><Typography.Title level={4} style={{ margin: 0 }}>被测资源</Typography.Title>
        <Typography.Text type="secondary">Agent / 知识库 / 技能 / MCP 的建档状态与稳定版本</Typography.Text></div>
      <Flex gap={8} align="center" wrap>
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
          value={kind} onChange={(value?: ResourceKind) => setKind(value)} />
        {canManage && <Button type="primary" onClick={() => setRegisterOpen(true)}>登记被测资源</Button>}
      </Flex>
    </Flex>
    {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }}
      action={<Button onClick={() => void reload()}>重试</Button>} />}
    <ResourceTable resources={resources} loading={loading} filtered={filtered}
      onOpen={(row) => void timeline.openTimeline(row)}
      onOpenDetail={(row) => navigate(`/evaluations/resources/${encodeURIComponent(row.resource_kind)}/${encodeURIComponent(row.resource_id)}`)} />
    <TimelineDrawer events={timeline.events} open={timeline.open} loading={timeline.loading} error={timeline.error}
      isMobile={isMobile} onClose={timeline.closeTimeline} />
    <RegisterResourceModal open={registerOpen} registered={toRegisteredRows(resources)} onClose={() => setRegisterOpen(false)}
      onRegistered={() => { setRegisterOpen(false); reload(); }} />
  </div>;
};
