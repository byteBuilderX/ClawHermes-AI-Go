import { HistoryOutlined } from '@ant-design/icons';
import { Button, Modal } from 'antd';

import { knowledgeApi } from '../api/knowledge.api';
import { DocAccessModal } from '../components/DocAccessModal';
import { DocPreviewDrawer } from '../components/DocPreviewDrawer';
import { WorkspaceConfigForm } from '../components/WorkspaceConfigForm';
import { WorkspaceDetailHeader } from '../components/WorkspaceDetailHeader';
import { WorkspaceDetailSkeleton } from '../components/WorkspaceDetailSkeleton';
import { WorkspaceDocumentsTable } from '../components/WorkspaceDocumentsTable';
import { WorkspaceQueryPanel } from '../components/WorkspaceQueryPanel';
import { WorkspaceStatsCard } from '../components/WorkspaceStatsCard';
import { WorkspaceUploadZone } from '../components/WorkspaceUploadZone';
import { useKnowledgeDetailPage } from '../hooks/useKnowledgeDetailPage';

import { VersionHistory, type VersionDetail, type VersionRow } from '@/shared/ui';

// 版本 payload（domain 快照 .Map() 的 name/description/config 键）的中文标签；
// config 内子键为 PascalCase（无 json tag）且不在此表 → 由 Drawer 回落到原文段。
const KNOWLEDGE_WORKSPACE_FIELD_LABELS: Record<string, string> = {
  name: '名称',
  description: '描述',
  config: 'RAG 配置',
};

export const KnowledgeDetailPage = () => {
  const {
    name,
    navigate,
    isAdmin,
    canEdit,
    canRequestEditor,
    stats,
    statsLoading,
    configForm,
    configLoading,
    uploadLoading,
    queryForm,
    queryLoading,
    queryResult,
    documents,
    documentsLoading,
    deletingDocumentID,
    handleConfigSave,
    handleDescriptionSave,
    handleNameSave,
    handleUpload,
    handleQuery,
    handleDeleteDocument,
    userCandidates,
    userCandidatesLoading,
    roleCandidates,
    editOpen,
    setEditOpen,
    editDoc,
    accessLoading,
    accessForm,
    handleOpenAccess,
    handleSetDocAccess,
    previewDoc,
    setPreviewDoc,
    handlePreviewDocument,
    versions,
    versionsOpen,
    setVersionsOpen,
    versionsLoading,
    openVersions,
    rollbackVersion,
    undoEdits,
  } = useKnowledgeDetailPage();

  // 「详情」素材：after = 点击版整份 payload；before = 直父(parentVersionId) payload。
  // 首版/父缺失（record 为空）→ before 空对象，全部视为新增。payload 为
  // name/description/config 键，由共享 Drawer 递归现算叶子 diff（spec §4.3）。
  const handleViewDetail = async (row: VersionRow): Promise<VersionDetail> => {
    const after = await knowledgeApi.getVersion(name, row.id);
    let before: Record<string, unknown> = {};
    if (after.parentVersionId) {
      const parent = await knowledgeApi.getVersion(name, after.parentVersionId);
      before = parent.payload ?? {};
    }
    return {
      title: `版本 v${row.versionNo ?? '—'} 字段变更`,
      fieldLabels: KNOWLEDGE_WORKSPACE_FIELD_LABELS,
      before,
      after: after.payload ?? {},
    };
  };

  if (statsLoading && !stats) {
    return <WorkspaceDetailSkeleton />;
  }

  return (
    <div>
      <WorkspaceDetailHeader
        name={name}
        description={stats?.description}
        onBack={() => navigate('/knowledge')}
        onDescriptionSave={canEdit ? handleDescriptionSave : undefined}
        onNameSave={canEdit ? handleNameSave : undefined}
        canRequestEditor={canRequestEditor}
      />

      <WorkspaceStatsCard stats={stats ?? undefined} docCount={documents.length || undefined} />

      {isAdmin && (
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
          <Button icon={<HistoryOutlined />} onClick={openVersions}>
            版本历史
          </Button>
        </div>
      )}

      {canEdit && (
        <WorkspaceConfigForm
          form={configForm}
          loading={configLoading}
          onSubmit={handleConfigSave}
          onUndo={undoEdits}
        />
      )}

      {canEdit && (
        <WorkspaceUploadZone
          loading={uploadLoading}
          userCandidates={userCandidates}
          userCandidatesLoading={userCandidatesLoading}
          roleCandidates={roleCandidates}
          onUpload={handleUpload}
        />
      )}

      <WorkspaceDocumentsTable
        documents={documents}
        loading={documentsLoading}
        isAdmin={isAdmin}
        deletingDocumentID={deletingDocumentID}
        onDelete={handleDeleteDocument}
        onPreview={handlePreviewDocument}
        onSetAccess={isAdmin ? handleOpenAccess : undefined}
        workspaceName={name}
      />

      <WorkspaceQueryPanel
        form={queryForm}
        loading={queryLoading}
        result={queryResult}
        onSubmit={handleQuery}
      />

      {isAdmin && (
        <DocAccessModal
          open={editOpen}
          loading={accessLoading}
          form={accessForm}
          documentTitle={editDoc?.source || editDoc?.id || ''}
          userCandidates={userCandidates}
          userCandidatesLoading={userCandidatesLoading}
          roleCandidates={roleCandidates}
          onClose={() => setEditOpen(false)}
          onSubmit={handleSetDocAccess}
        />
      )}

      <DocPreviewDrawer
        open={Boolean(previewDoc)}
        name={name}
        documentID={previewDoc?.id ?? ''}
        documentTitle={previewDoc?.source}
        onClose={() => setPreviewDoc(null)}
      />

      {isAdmin && (
        <Modal
          title="版本历史"
          open={versionsOpen}
          onCancel={() => setVersionsOpen(false)}
          footer={null}
          width={760}
        >
          <VersionHistory
            rows={versions.map((v) => ({
              id: v.id,
              versionNo: v.versionNo,
              status: v.status,
              isCurrent: v.isCurrent,
              createdByName: v.createdByName,
              createdBy: v.createdBy,
              createdAt: v.createdAt,
              canRollback: v.status === 'deprecated' && isAdmin,
              summary: v.safeSummary,
            }))}
            loading={versionsLoading}
            rollback={rollbackVersion}
            onViewDetail={handleViewDetail}
          />
        </Modal>
      )}
    </div>
  );
};

export default KnowledgeDetailPage;
