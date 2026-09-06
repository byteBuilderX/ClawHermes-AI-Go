import { ArrowLeftOutlined, LockOutlined } from '@ant-design/icons';
import { Button, Form, Skeleton, Tabs, Typography, message } from 'antd';
import { useEffect, useState } from 'react';

import { agentApi } from '../api/agent.api';
import { AgentFormSections } from '../components/AgentFormSections';
import { useEditAgentPage } from '../hooks/useEditAgentPage';
import type { AgentVersion } from '../model/agent';

import { AGENT_DEFAULT_MAX_CONTEXT_TOKENS, AGENT_DEFAULT_MAX_ITERATIONS } from '@/constants';
import { useTenantRole } from '@/modules/iam';
import { RequestEditorButton } from '@/shared/components';
import { extractErrorMessage } from '@/shared/lib';
import { VersionHistory, type VersionDetail, type VersionRow } from '@/shared/ui';

const { Title, Text } = Typography;

// 版本 payload 字段（domain/agent_version.go AgentVersionSnapshot.Map() snake_case
// 键）的中文标签；未列出的键（含 parameters 下 memory.* 点号子键）由 Drawer 回落到
// 原文路径段。
const AGENT_VERSION_FIELD_LABELS: Record<string, string> = {
  name: '名称',
  description: '描述',
  system_prompt: '系统提示词',
  llm_model: '模型',
  max_iterations: '最大迭代',
  max_context_tokens: '上下文上限',
  memory_scope: '记忆范围',
  temperature: '温度',
  reasoning_effort: '推理强度',
  max_tokens: '最大输出',
  parameters: '记忆参数',
  allowed_skills: '可用技能',
  mcp_tool_ids: 'MCP 工具',
  knowledge_workspace_ids: '知识库',
  delegate_enabled: '委托开关',
  delegate_max_depth: '委托深度',
  delegate_default_max_steps: '委托默认步数',
};

export const EditAgentPage = () => {
  const {
    agent, form, loading, pageLoading, skills, mcpTools, workspaces, groupedModels,
    navigate, managementPath, onFinish, readOnly, refreshTick, reloadAgent,
    editorCandidates, editorCandidatesLoading,
  } = useEditAgentPage();
  const { isAdmin } = useTenantRole();
  const [activeTab, setActiveTab] = useState('config');
  const [versions, setVersions] = useState<AgentVersion[]>([]);
  const [versionsLoading, setVersionsLoading] = useState(false);

  // 版本历史：进入该 tab 才加载；保存/回滚后 bump refreshTick 触发重拉。
  // 依赖用原始 agentId 而非整个 agent 对象，避免 reloadAgent 换新对象导致重复拉取。
  const agentId = agent?.id;
  useEffect(() => {
    if (activeTab !== 'versions' || !agentId) return;
    let cancelled = false;
    setVersionsLoading(true);
    agentApi.listVersions(agentId).then((rows) => { if (!cancelled) setVersions(rows ?? []); })
      .catch((err) => { if (!cancelled) message.error({ content: extractErrorMessage(err, '加载版本历史失败'), duration: 3 }); })
      .finally(() => { if (!cancelled) setVersionsLoading(false); });
    return () => { cancelled = true; };
  }, [activeTab, agentId, refreshTick]);

  // 回滚会改变当前 agent 配置：原地重拉 agent 回填表单并重载版本历史。
  const handleRollback = async (row: VersionRow) => {
    if (!agent) return;
    await agentApi.rollback(agent.id, row.id);
    await reloadAgent();
  };

  // 「详情」素材：after = 点击版整份 payload；before = 直父(parentVersionId) payload。
  // 首版/父缺失（record 为空）→ before 空对象，全部视为新增。payload 为 snake_case
  // 编辑面快照，由共享 Drawer 递归 JSONPath 现算叶子 diff（spec §4.3）。
  const handleViewDetail = async (row: VersionRow): Promise<VersionDetail> => {
    if (!agent) return { title: '', before: {}, after: {} };
    const after = await agentApi.getVersion(agent.id, row.id);
    let before: Record<string, unknown> = {};
    if (after.parentVersionId) {
      const parent = await agentApi.getVersion(agent.id, after.parentVersionId);
      before = parent.payload ?? {};
    }
    return {
      title: `版本 v${row.versionNo ?? '—'} 字段变更`,
      fieldLabels: AGENT_VERSION_FIELD_LABELS,
      before,
      after: after.payload ?? {},
    };
  };

  if (pageLoading) {
    return (
      <div className="responsive-form-page">
        <Skeleton active paragraph={{ rows: 1 }} style={{ marginBottom: 24 }} />
        <div
          style={{
            background: '#fff',
            borderRadius: 12,
            border: '1px solid #f0f0f0',
            padding: 24,
            marginBottom: 16,
          }}
        >
          <Skeleton active paragraph={{ rows: 3 }} />
        </div>
        <div
          style={{
            background: '#fff',
            borderRadius: 12,
            border: '1px solid #f0f0f0',
            padding: 24,
          }}
        >
          <Skeleton active paragraph={{ rows: 4 }} />
        </div>
      </div>
    );
  }

  return (
    <div className="responsive-form-page">
      <div className="responsive-detail-header" style={{ marginBottom: 24 }}>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate(managementPath)} type="text">
          返回
        </Button>
        <div>
          <Title level={4} style={{ margin: 0 }}>
            {readOnly ? '查看 Agent 配置' : '编辑 Agent'}
          </Title>
          <Text type="secondary" style={{ fontSize: 13 }}>
            {readOnly ? '只读查看，如需修改请申请编辑权限' : '修改 Agent 配置'}
          </Text>
        </div>
      </div>

      <Tabs activeKey={activeTab} onChange={setActiveTab} items={[
        { key: 'config', label: '配置', children: (
          <>
            <Form
              form={form}
              layout="vertical"
              onFinish={onFinish}
              disabled={readOnly}
              initialValues={{
                maxIterations: AGENT_DEFAULT_MAX_ITERATIONS,
                maxContextTokens: AGENT_DEFAULT_MAX_CONTEXT_TOKENS,
                allowedSkills: [],
                memoryScope: 'user',
              }}
            >
              <AgentFormSections
                skills={skills}
                mcpTools={mcpTools}
                workspaces={workspaces}
                groupedModels={groupedModels}
                // P2：可编辑人（白名单）管理仅 admin/owner 可见；readOnly 时表单 disabled。
                showEditors={isAdmin}
                editorCandidates={editorCandidates}
                editorCandidatesLoading={editorCandidatesLoading}
              />

              {!readOnly && (
                <div className="responsive-form-actions" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
                  <Button onClick={() => navigate(managementPath)}>取消</Button>
                  <Button type="primary" htmlType="submit" loading={loading}>
                    保存修改
                  </Button>
                </div>
              )}
            </Form>
            {/* 申请编辑权限按钮必须放在 Form 外：<Form disabled={readOnly}> 通过 DisabledContext
                禁用表单内所有 antd 组件（含 Button），member 只读时须可点申请。 */}
            {readOnly && (
              <div className="responsive-form-actions" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
                <RequestEditorButton
                  resourceType="agent"
                  resourceId={agent?.id ?? ''}
                  options={{ resourceName: agent?.name ?? '' }}
                  buttonProps={{ type: 'primary', icon: <LockOutlined /> }}
                />
              </div>
            )}
          </>
        ) },
        { key: 'versions', label: '版本历史', children: (
          <VersionHistory
            rows={(versions ?? []).map((v) => ({
              id: v.id, versionNo: v.versionNo, status: v.status, isCurrent: v.isCurrent,
              createdByName: v.createdByName, createdBy: v.createdBy, createdAt: v.createdAt,
              canRollback: v.status === 'deprecated' && !readOnly,
            }))}
            loading={versionsLoading}
            rollback={handleRollback}
            onViewDetail={handleViewDetail}
            infoMessage="保存即产生新版本并立即生效；历史版本可回滚，回滚不产生新版本。"
          />
        ) },
      ]} />
    </div>
  );
};
