import { PlusOutlined } from '@ant-design/icons';
import { Alert, Button, Card, Flex, Select, Skeleton, Space, Typography, message } from 'antd';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';

import { CreateEvaluationModal } from '../components/CreateEvaluationModal';
import { CrossResourceFeed } from '../components/CrossResourceFeed';
import { EvaluationOverview } from '../components/EvaluationOverview';
import { RegisterResourceModal } from '../components/RegisterResourceModal';
import { ResourceTable } from '../components/ResourceTable';
import { displayLabel, kindFilterOptions } from '../components/evaluationView';
import { useEvaluationCenter } from '../hooks/useEvaluationCenter';
import { resourceKindSchema } from '../model/evaluation';
import type { CenterKindFilter, RegistrableResourceKind, ResourceKind } from '../model/evaluation';

const statusOptions = ['active', 'proposed', 'promoted', 'running', 'succeeded', 'failed', 'paused'].map((value) => ({ value, label: displayLabel(value) }));
const toRegistrableKind = (value: ResourceKind | undefined): RegistrableResourceKind | undefined =>
  value === 'agent' || value === 'knowledge' ? value : undefined;

// 记录簿快速入口：链路页从 hub 直达（菜单之外的补充导航）。
const QUICK_LINKS = [
  { path: '/evaluations/runs', label: '离线运行' },
  { path: '/evaluations/evolution', label: '自进化工作区' },
  { path: '/evaluations/resources', label: '被测资源' },
  { path: '/evaluations/observability', label: '在线观测' },
  { path: '/evaluations/review', label: '人工评审池' },
  { path: '/evaluations/suites', label: '评测集' },
] as const;

export const EvaluationCenterPage = () => {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const parsedKind = resourceKindSchema.safeParse(searchParams.get('kind'));
  const kind = parsedKind.success ? parsedKind.data : undefined;
  const filterResourceId = searchParams.get('resource_id')?.trim() || undefined;
  // 被测收敛后中心默认并列 agent+knowledge 两轨：未显式选 kind 时以 CSV 聚合两轨；
  // 显式选历史单值（skill/mcp）或单轨时以单值只读读回。
  const centerKind: CenterKindFilter = kind ?? 'agent,knowledge';
  const [status, setStatus] = useState<string | undefined>();
  const center = useEvaluationCenter({ resource_kind: centerKind, resource_id: filterResourceId, status });
  const [createOpen, setCreateOpen] = useState(false);
  const [registerOpen, setRegisterOpen] = useState(false);
  // URL 深链 / 外部入口预置的登记初值（kind+resource_id），供 RegisterResourceModal 预填。
  const [registerInitial, setRegisterInitial] = useState<{ kind: RegistrableResourceKind; resource_id?: string }>();
  // 「登记并新建评测」流程预选的目标资源，交由 CreateEvaluationModal focus 消费。
  const [createFocus, setCreateFocus] = useState<{ kind: ResourceKind; resource_id: string }>();

  const setKind = (value: ResourceKind | undefined) => {
    const next = new URLSearchParams(searchParams);
    if (value) next.set('kind', value); else next.delete('kind');
    setSearchParams(next);
  };

  // 登记入口支持 URL 深链 ?action=register&kind=<agent|knowledge>&resource_id=<id>（供
  // agent/知识库详情页跳转直达建档）：一次性消费后清除 action 参数避免刷新反复弹窗。
  useEffect(() => {
    if (searchParams.get('action') !== 'register') return;
    const parsed = resourceKindSchema.safeParse(searchParams.get('kind'));
    setRegisterInitial({
      kind: toRegistrableKind(parsed.success ? parsed.data : undefined) ?? 'agent',
      resource_id: searchParams.get('resource_id')?.trim() || undefined,
    });
    setRegisterOpen(true);
    const next = new URLSearchParams(searchParams);
    next.delete('action'); next.delete('kind'); next.delete('resource_id');
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);
  // 已登记的同轨资源（agent/knowledge），供登记框提示「重新建档刷新稳定版本」。
  const registeredRows = useMemo(() => center.resources.items
    .filter((item) => item.resource_kind === 'agent' || item.resource_kind === 'knowledge')
    .map((item) => ({ kind: item.resource_kind as RegistrableResourceKind, resource_id: item.resource_id })),
  [center.resources.items]);
  // 登记成功回调：刷新列表让新行出现；createBaseline 已弹成功提示。
  const handleRegistered = () => { setRegisterOpen(false); void center.reload(); };
  // 登记并新建评测：登记成功后预选该资源打开新建评测；focus 由 CreateEvaluationModal
  // 在资源列表刷新后消费。center.reload() 内部吞错（错误落 center.error），不阻断流程。
  const handleRegisterThenRun = (kind: RegistrableResourceKind, resourceId: string) => {
    setRegisterOpen(false);
    setCreateFocus({ kind, resource_id: resourceId });
    setCreateOpen(true);
    void center.reload();
  };

  if (center.loading && !center.overview) return <Skeleton active />;
  return <Flex vertical gap={16}>
    <Flex justify="space-between" align="end" gap={16} wrap>
      <div><Typography.Title level={4} style={{ margin: 0 }}>评测与进化中心</Typography.Title>
        <Typography.Text type="secondary">在同一记录簿中审阅版本证据与演进决定</Typography.Text></div>
      <Space wrap>
        <Select aria-label="资源类型" allowClear placeholder="资源类型" style={{ width: 132 }} options={kindFilterOptions}
          value={kind} onChange={setKind} />
        <Select aria-label="资源状态" allowClear placeholder="资源状态" style={{ width: 132 }} options={statusOptions}
          value={status} onChange={setStatus} />
        {center.canManageEvaluation && <>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => { setCreateFocus(undefined); setCreateOpen(true); }}>新建评测</Button>
          <Button onClick={() => { setRegisterInitial(undefined); setRegisterOpen(true); }}>登记被测资源</Button>
        </>}
      </Space>
    </Flex>
    <EvaluationOverview overview={center.overview} />
    {center.error && <Alert type="error" showIcon message={center.error} action={<Button onClick={center.reload}>重试</Button>} />}
    <Card size="small" title="快捷入口">
      <Space wrap>{QUICK_LINKS.map((link) => <Button key={link.path} onClick={() => navigate(link.path)}>{link.label}</Button>)}</Space>
    </Card>
    <Card size="small" title="最近记录" extra={<Button type="link" onClick={() => navigate('/evaluations/runs')}>全部运行</Button>}>
      <CrossResourceFeed runs={center.runs.items} candidates={center.candidates.items}
        experiments={center.experiments.items} loading={center.loading} onOpen={(entry) => {
          // 运行有独立详情页直达；候选/实验入口落自进化工作区（该页承载推进决定）。
          if (entry.kind === 'run') navigate(`/evaluations/runs/${encodeURIComponent(entry.id)}`);
          else navigate('/evaluations/evolution');
        }} />
    </Card>
    <Card size="small" title="被测资源" extra={<Button type="link" onClick={() => navigate('/evaluations/resources')}>资源列表</Button>}>
      <ResourceTable resources={center.resources.items} loading={center.loading}
        filtered={!!kind || !!filterResourceId || !!status}
        onOpen={(row) => navigate(`/evaluations/resources/${row.resource_kind}/${encodeURIComponent(row.resource_id)}`)} />
    </Card>
    <RegisterResourceModal open={registerOpen} initial={registerInitial} registered={registeredRows}
      onClose={() => setRegisterOpen(false)} onRegistered={handleRegistered} onRegisterThenRun={handleRegisterThenRun} />
    <CreateEvaluationModal open={createOpen} resources={center.resources.items} focusResource={createFocus} onClose={() => {
      center.resetCreateEvaluation(); setCreateFocus(undefined); setCreateOpen(false);
    }}
      onSubmit={async (plan) => {
        try {
          await center.createEvaluation(plan);
          message.success({ content: '评测已创建并进入运行队列', duration: 2 });
        } catch (error) {
          message.error({ content: error instanceof Error ? error.message : '创建评测失败', duration: 3 });
          throw error;
        }
      }} />
  </Flex>;
};
