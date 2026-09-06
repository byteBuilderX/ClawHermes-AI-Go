import { ArrowLeftOutlined, PlayCircleOutlined } from '@ant-design/icons';
import { Button, Card, Empty, message, Skeleton, Space, Tag, Typography } from 'antd';
import { useCallback, useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import { workflowApi } from '../api/workflow.api';
import { WorkflowReadonlyCanvas } from '../components/WorkflowReadonlyCanvas';
import { useWorkflowResources } from '../hooks/useWorkflowResources';
import type { WorkflowDefinition, WorkflowVersion, WorkflowVersionSummary } from '../model/workflow';

import { useTenantRole } from '@/modules/iam';
import { extractErrorMessage } from '@/shared/lib';
import { VersionHistory, type VersionDetail, type VersionRow } from '@/shared/ui';

const { Paragraph, Title } = Typography;

// WorkflowDetailPage 是普通成员与管理员共用的工作流只读详情页：
// 展示当前生效版本的画布、版本历史；管理员额外获得回退入口。
export const WorkflowDetailPage = () => {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const { isAdmin } = useTenantRole();
  const { skillRevisions } = useWorkflowResources();
  // revision ID → 可读版本名（如「检索（已发布）」），供只读节点详情展示 Skill 版本。
  const skillRevisionLabels = Object.fromEntries(skillRevisions.map((revision) => [revision.value, revision.label]));
  const [definition, setDefinition] = useState<WorkflowDefinition | null>(null);
  const [versions, setVersions] = useState<WorkflowVersionSummary[]>([]);
  const [activeVersion, setActiveVersion] = useState<WorkflowVersion | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    const definition = await workflowApi.getWorkflow(id);
    const page = await workflowApi.listWorkflowVersions(id, { page: 1, pageSize: 100 });
    const activeId = definition.active_version_id || page.versions[0]?.id;
    const active = activeId ? await workflowApi.getWorkflowVersion(id, activeId) : null;
    setDefinition(definition);
    setVersions(page.versions);
    setActiveVersion(active);
  }, [id]);

  useEffect(() => {
    let cancelled = false;
    load().catch((error: unknown) => {
      if (!cancelled) message.error({ content: extractErrorMessage(error, '操作失败'), duration: 3 });
    }).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [load]);

  // 回退后生效指针已改变，重新拉取定义与生效版本；成功提示由共享组件负责。
  const handleRollback = async (row: VersionRow) => {
    await workflowApi.rollbackWorkflow(id, row.id);
    setLoading(true);
    try { await load(); } finally { setLoading(false); }
  };

  // 「详情」素材：baseline = 直父版本 version_no-1（spec §4.5），after/before 各取
  // {name,description,spec,input_schema} 整份快照，由共享 Drawer 现算叶子 JSONPath
  // diff（spec.nodes[...] 级）。前一版不存在（首版）或缺失时 before 传空对象，全部视为新增。
  const handleViewDetail = async (row: VersionRow): Promise<VersionDetail> => {
    const afterVersion = await workflowApi.getWorkflowVersion(id, row.id);
    const snapshot = (v: WorkflowVersion) => ({
      name: v.name,
      description: v.description,
      spec: v.spec,
      input_schema: v.input_schema,
    });
    const prevNo = (row.versionNo ?? 1) - 1;
    const prev = versions.find((version) => version.version === prevNo);
    let before: Record<string, unknown> = {};
    if (prevNo >= 1 && prev) {
      before = snapshot(await workflowApi.getWorkflowVersion(id, prev.id));
    }
    return {
      title: `版本 v${row.versionNo ?? '—'} 字段变更`,
      fieldLabels: { name: '名称', description: '描述', spec: '图定义', input_schema: '输入表单' },
      before,
      after: snapshot(afterVersion),
    };
  };

  if (loading) return <Skeleton active />;
  if (!definition) return <Empty description="没有找到这个工作流" />;

  const activeId = definition.active_version_id || versions[0]?.id;
  const rows: VersionRow[] = versions.map((version) => ({
    id: version.id,
    versionNo: version.version,
    status: 'published',
    isCurrent: version.id === activeId,
    canRollback: isAdmin && version.id !== activeId,
    createdAt: version.created_at,
    createdByName: version.created_by_name,
    createdBy: version.created_by,
  }));

  return <section className="workflow-page-shell workflow-version-page">
    <header className="workflow-version-header">
      <Button aria-label="返回工作流列表" type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate('/workflows')} />
      <div><Title level={3}>{definition.name}</Title><Paragraph>{definition.description || '暂无说明'}</Paragraph></div>
      <Space>
        <Tag color="blue">生效版本 v{activeVersion?.version ?? '—'}</Tag>
        <Button aria-label="运行工作流" type="primary" ghost icon={<PlayCircleOutlined />} onClick={() => navigate(`/workflows/${definition.id}/run`)}>
          运行工作流
        </Button>
      </Space>
    </header>
    {activeVersion
      ? <WorkflowReadonlyCanvas spec={activeVersion.spec} skillRevisionLabels={skillRevisionLabels} />
      : <Empty description="这个工作流还没有已发布版本" />}
    <Card title="版本历史" className="workflow-version-inputs">
      <VersionHistory
        rows={rows}
        loading={loading}
        rollback={isAdmin ? handleRollback : undefined}
        onViewDetail={handleViewDetail}
        infoMessage={isAdmin ? '回滚到历史版本后立即生效，不产生新版本；历史保留可再次回滚。' : undefined}
      />
    </Card>
  </section>;
};
