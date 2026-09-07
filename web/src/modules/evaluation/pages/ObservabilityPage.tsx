import { Flex, Select, Tabs, Typography } from 'antd';
import { useState } from 'react';

import { EvaluationMonitorPanel } from '../components/EvaluationMonitorPanel';
import { RuntimeHealthTrendPanel } from '../components/RuntimeHealthTrendPanel';
import { kindFilterOptions } from '../components/evaluationView';
import type { ResourceKind } from '../model/evaluation';

import { useResponsive } from '@/shared/hooks';

export const ObservabilityPage = () => {
  const { isMobile } = useResponsive();
  const [kind, setKind] = useState<ResourceKind | undefined>();

  return <div>
    <Flex justify="space-between" align="end" gap={16} wrap style={{ marginBottom: 12 }}>
      <div><Typography.Title level={4} style={{ margin: 0 }}>在线观测</Typography.Title>
        <Typography.Text type="secondary">离线评测健康趋势与线上运行指标监控</Typography.Text></div>
      <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
        value={kind} onChange={(value?: ResourceKind) => setKind(value)} />
    </Flex>
    <Tabs items={[
      { key: 'health', label: '运行通过率趋势', children: <RuntimeHealthTrendPanel key={`health-${kind ?? 'all'}`}
        defaultKind={kind} /> },
      { key: 'monitor', label: '评测指标监控', children: <EvaluationMonitorPanel key={`monitor-${kind ?? 'all'}`}
        defaultKind={kind} isMobile={isMobile} /> },
    ]} />
  </div>;
};
