import { ArrowLeftOutlined } from '@ant-design/icons';
import {
  Alert, Button, Empty, Flex, Modal, Skeleton, Space, Table, Tabs, Tag, Typography, message,
} from 'antd';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import { evaluationApi } from '../api/evaluation.api';
import { AddDraftCaseModal, type AddDraftCaseValues } from '../components/AddDraftCaseModal';
import { EditDraftCaseModal, type EditDraftCaseValues } from '../components/EditDraftCaseModal';
import { GenerateCasesModal, type GenerateCasesValues } from '../components/GenerateCasesModal';
import { SuiteCaseCollapse } from '../components/SuiteCaseCollapse';
import { StatusTag, displayLabel } from '../components/evaluationView';
import { useSuiteDraft } from '../hooks/useSuiteDraft';
import type { EvaluationCase, SuiteDetail, SuiteRevision, SuiteRevisionMeta } from '../model/evaluation';

import { useTenantRole } from '@/modules/iam';
import { extractErrorMessage } from '@/shared/lib';

// SuiteDetailPage 是评测集独立详情页（受 PrivateRoute 保护的 admin 管理 + member
// 只读页）。第一片提供「草稿编辑」与「已发布版本」两个 tab；第三个「评测」tab 由
// 后续切片追加，结构上已预留 items 数组扩展点。
export const SuiteDetailPage = () => {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const { isAdmin } = useTenantRole();
  const canManage = isAdmin;
  const [detail, setDetail] = useState<SuiteDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const generationRef = useRef(0);

  const load = useCallback(async () => {
    const generation = ++generationRef.current;
    setLoading(true); setError('');
    try {
      const next = await evaluationApi.getSuiteDetail(id);
      if (generation === generationRef.current) setDetail(next);
    } catch (err) {
      if (generation === generationRef.current) setError(extractErrorMessage(err) || '加载评测集失败');
    } finally {
      if (generation === generationRef.current) setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    generationRef.current += 1;
    void load();
    return () => { generationRef.current += 1; };
  }, [load]);

  if (loading && !detail) return <Skeleton active />;
  return <div>
    <Button type="link" icon={<ArrowLeftOutlined />} style={{ paddingLeft: 0, marginBottom: 8 }}
      onClick={() => navigate('/evaluations/suites')}>返回评测集列表</Button>
    {error && <Alert type="error" showIcon style={{ marginBottom: 12 }} message={error} action={<Space wrap>
      <Button size="small" onClick={() => void load()}>重试</Button>
      <Button size="small" onClick={() => navigate('/evaluations/suites')}>返回评测集列表</Button>
    </Space>} />}
    {!detail && !error && <Empty description="评测集不存在" />}
    {detail && <>
      <Flex align="center" gap={8} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>{detail.name}</Typography.Title>
        {detail.resource_kind && <Tag>{displayLabel(detail.resource_kind)}</Tag>}
        <StatusTag value={detail.status} />
      </Flex>
      {detail.description && <Typography.Paragraph type="secondary" style={{ marginBottom: 4 }}>{detail.description}</Typography.Paragraph>}
      <Space wrap split={<Typography.Text type="secondary">·</Typography.Text>}>
        <Typography.Text type="secondary">{detail.active_revision_id
          ? `v${detail.active_version_no ?? ''} · ${detail.active_case_count ?? 0} 个启用用例` : '尚未发布'}</Typography.Text>
        <Typography.Text type="secondary">{detail.draft_revision_id
          ? `草稿 ${detail.draft_case_count ?? 0} 个用例` : '无草稿'}</Typography.Text>
        <Typography.Text type="secondary">{detail.created_by ? `创建者：${detail.created_by} · ` : ''}{detail.created_at}</Typography.Text>
      </Space>
      <Tabs style={{ marginTop: 16 }} items={[
        { key: 'draft', label: '草稿编辑', children: detail.draft_revision_id
          ? <DraftEditorTab key={detail.draft_revision_id} suiteId={detail.id} canManage={canManage}
            onDetailChanged={() => void load()} />
          : <LegacyDraftNotice suiteId={detail.id} canManage={canManage}
            hasActive={!!detail.active_revision_id} onStarted={() => void load()} /> },
        { key: 'versions', label: '已发布版本', children: <SuiteVersionsTab suiteId={detail.id}
          activeRevisionId={detail.active_revision_id} /> },
      ]} />
    </>}
  </div>;
};

// DraftEditorTab 在套件存在草稿时挂载（外层按 detail.draft_revision_id 切换），
// 用 useSuiteDraft 承载草稿数据与写动作；写动作成功后刷新 detail 让头部草稿计数
// 与 draft_revision_id 同步。member 只读查看草稿，不渲染操作按钮。
const DraftEditorTab = ({ suiteId, canManage, onDetailChanged }: {
  suiteId: string; canManage: boolean; onDetailChanged: () => void;
}) => {
  const editor = useSuiteDraft({ suiteId, enabled: canManage });
  const [generateOpen, setGenerateOpen] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [editCase, setEditCase] = useState<EvaluationCase | null>(null);
  const fail = (err: unknown, fallback: string) =>
    message.error({ content: extractErrorMessage(err) || fallback, duration: 3 });

  const confirmDeleteCase = (testCase: EvaluationCase) => {
    Modal.confirm({
      title: `删除用例「${testCase.name || '未命名'}」？`,
      content: '删除后草稿将不再包含该用例；已发布版本不受影响。',
      okText: '删除', okButtonProps: { danger: true }, cancelText: '取消',
      onOk: async () => {
        try {
          await editor.deleteCase(testCase.id || '');
          message.success({ content: '已删除草稿用例', duration: 2 });
          onDetailChanged();
        } catch (err) { fail(err, '删除草稿用例失败'); throw err; }
      },
    });
  };

  const confirmPublish = () => {
    Modal.confirm({
      title: '发布当前草稿？',
      content: '发布后草稿归档为不可变版本并自动开启继承当前用例的新草稿；发布前请审阅全部用例与判定配置。',
      okText: '发布', cancelText: '取消',
      onOk: async () => {
        try {
          const published = await editor.publish();
          message.success({ content: `已发布 v${published.version_no ?? 1}，已开启继承草稿`, duration: 2 });
          onDetailChanged();
        } catch (err) { fail(err, '发布草稿失败'); throw err; }
      },
    });
  };

  const saveCase = async (values: EditDraftCaseValues) => {
    if (!editCase?.id) throw new Error('草稿用例不可用');
    try {
      await editor.saveCase(editCase.id, values);
      message.success({ content: '草稿用例已更新', duration: 2 });
    } catch (err) { fail(err, '更新草稿用例失败'); throw err; }
  };

  const onAddCase = async (values: AddDraftCaseValues) => {
    try {
      await editor.addCase(values);
      message.success({ content: '已添加草稿用例', duration: 2 });
      onDetailChanged();
    } catch (err) { fail(err, '添加草稿用例失败'); throw err; }
  };

  const onGenerate = async (values: GenerateCasesValues) => {
    try {
      const result = await editor.generate(values);
      message.success({
        content: `已生成 ${result.generated} 个草稿用例（采样 ${result.samples_found}，拒绝 ${result.rejected.length} 个）`,
        duration: 2,
      });
      onDetailChanged();
    } catch (err) { fail(err, '生成草稿用例失败'); throw err; }
  };

  return <>
    {canManage && <Space style={{ marginBottom: 12 }} wrap>
      <Button onClick={() => setGenerateOpen(true)}>生成用例</Button>
      <Button onClick={() => setAddOpen(true)}>添加用例</Button>
      <Button type="primary" onClick={confirmPublish}>发布</Button>
    </Space>}
    {editor.loading && <Skeleton active />}
    {editor.error && <Alert type="error" showIcon message={editor.error}
      action={<Button size="small" onClick={() => void editor.reload()}>重试</Button>} />}
    {!editor.loading && !editor.error && editor.draft && <SuiteCaseCollapse key={editor.draft.id}
      cases={editor.draft.cases} canManage={canManage}
      onEditCase={canManage ? setEditCase : undefined}
      onDeleteCase={canManage ? (testCase) => confirmDeleteCase(testCase) : undefined}
      emptyText={canManage ? '草稿还没有用例，可生成用例或添加用例。' : '草稿还没有用例。'} />}
    <EditDraftCaseModal open={!!editCase} draft={editCase} onClose={() => setEditCase(null)} onSubmit={saveCase} />
    <GenerateCasesModal open={generateOpen} onClose={() => setGenerateOpen(false)} onSubmit={onGenerate} />
    <AddDraftCaseModal open={addOpen} onClose={() => setAddOpen(false)} onSubmit={onAddCase} />
  </>;
};

// LegacyDraftNotice 渲染在「草稿编辑」tab 下、套件无草稿时（历史套件：S1 前发布版
// 不含 successor draft）。管理员可从当前发布版本继承用例补建草稿；member 仅提示。
const LegacyDraftNotice = ({ suiteId, canManage, hasActive, onStarted }: {
  suiteId: string; canManage: boolean; hasActive: boolean; onStarted: () => void;
}) => {
  const [starting, setStarting] = useState(false);
  const start = async () => {
    setStarting(true);
    try {
      await evaluationApi.startNextDraft(suiteId);
      message.success({ content: '已开启继承草稿', duration: 2 });
      onStarted();
    } catch (err) {
      message.error({ content: extractErrorMessage(err) || '开启继承草稿失败', duration: 3 });
    } finally { setStarting(false); }
  };
  const description = hasActive
    ? '该评测集暂无编辑草稿（历史套件）。管理员可从当前发布版本继承用例开启新草稿。'
    : '该评测集暂无编辑草稿。管理员可开启一个新草稿后再编辑。';
  return <Alert type="info" showIcon message={description} action={canManage
    ? <Button size="small" type="primary" loading={starting} onClick={() => void start()}>从此版本新建草稿</Button>
    : undefined} />;
};

// SuiteVersionsTab 展示轻量版本链（published 在前、draft 最后），点击「查看」按需
// 装载该版本完整用例正文并只读展开。无选中时不展示正文，仅提示选择。
const SuiteVersionsTab = ({ suiteId, activeRevisionId }: { suiteId: string; activeRevisionId?: string }) => {
  const [metas, setMetas] = useState<SuiteRevisionMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [revision, setRevision] = useState<SuiteRevision | null>(null);
  const [revisionLoading, setRevisionLoading] = useState(false);
  const [revisionError, setRevisionError] = useState('');
  const generationRef = useRef(0);
  const revisionGenRef = useRef(0);

  const load = useCallback(async () => {
    const generation = ++generationRef.current;
    setLoading(true); setError('');
    try {
      const rows = await evaluationApi.listSuiteVersions(suiteId);
      if (generation === generationRef.current) setMetas(rows);
    } catch (err) {
      if (generation === generationRef.current) setError(extractErrorMessage(err) || '加载版本列表失败');
    } finally {
      if (generation === generationRef.current) setLoading(false);
    }
  }, [suiteId]);

  useEffect(() => {
    generationRef.current += 1;
    void load();
    return () => { generationRef.current += 1; revisionGenRef.current += 1; };
  }, [load]);

  const openRevision = async (row: SuiteRevisionMeta) => {
    if (revision?.id === row.id) return;
    const generation = ++revisionGenRef.current;
    setRevisionLoading(true); setRevisionError(''); setRevision(null);
    try {
      const loaded = await evaluationApi.getSuiteRevision(suiteId, row.id);
      if (generation === revisionGenRef.current) setRevision(loaded);
    } catch (err) {
      if (generation === revisionGenRef.current) setRevisionError(extractErrorMessage(err) || '加载版本用例失败');
    } finally {
      if (generation === revisionGenRef.current) setRevisionLoading(false);
    }
  };

  return <>
    {error && <Alert type="error" showIcon style={{ marginBottom: 12 }} message={error}
      action={<Button size="small" onClick={() => void load()}>重试</Button>} />}
    <Table<SuiteRevisionMeta> rowKey="id" size="small" loading={loading} dataSource={metas} pagination={false}
      locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有发布版本" /> }} columns={[
        { title: '版本', dataIndex: 'version_no', width: 90, render: (value?: number) => (value ? `v${value}` : '草稿') },
        { title: '状态', dataIndex: 'status', width: 110, render: (value: string) => <StatusTag value={value} /> },
        { title: '启用用例数', dataIndex: 'enabled_case_count', width: 110, render: (value?: number) => value ?? 0 },
        { title: '创建者', dataIndex: 'created_by', width: 120, render: (value?: string) => value || '—' },
        { title: '发布时间', dataIndex: 'published_at', width: 180, render: (value?: string | null) => value || '—' },
        { title: '当前版本', key: 'current', width: 100,
          render: (_, row) => (row.id === activeRevisionId ? <Tag color="green">当前使用</Tag> : null) },
        { title: '操作', key: 'actions', width: 80,
          render: (_, row) => <Button type="link" size="small" onClick={() => void openRevision(row)}>查看</Button> },
      ]} />
    {revisionLoading && <div style={{ marginTop: 12 }}><Skeleton active /></div>}
    {revisionError && <Alert type="error" showIcon style={{ marginTop: 12 }} message={revisionError} />}
    {revision && <div style={{ marginTop: 12 }}>
      <Typography.Title level={5} style={{ marginTop: 0 }}>
        {revision.version_no ? `版本 v${revision.version_no} 用例` : '草稿用例'}
      </Typography.Title>
      <SuiteCaseCollapse key={revision.id} cases={revision.cases} emptyText="该版本没有用例。" />
    </div>}
    {!revision && !revisionLoading && !revisionError && metas.length > 0
      && <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>选择一个版本查看该版本的用例正文。</Typography.Paragraph>}
  </>;
};
