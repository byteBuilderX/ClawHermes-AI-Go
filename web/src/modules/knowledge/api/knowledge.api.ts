import { z } from 'zod';

import {
  documentPreviewSchema,
  documentSchema,
  queryResultSchema,
  workspaceSchema,
  workspaceStatsSchema,
  workspaceVersionDetailSchema,
  workspaceVersionSchema,
  type CreateWorkspaceInput,
  type DocumentAccessInput,
  type DocumentPreview,
  type KnowledgeDocument,
  type QueryResult,
  type Workspace,
  type WorkspaceStats,
  type WorkspaceVersion,
  type WorkspaceVersionDetail,
} from '../model/knowledge';

import api from '@/services/client';

interface QueryInput {
  question: string;
  workspace: string;
  mode?: string;
  topK?: number;
}

// 上传时同时携带文档级白名单（repeated multipart 字段，空 = 不限制）
export interface IngestInput {
  formData: FormData;
  allowedUserIDs?: string[];
  allowedRoleIDs?: string[];
}

export const knowledgeApi = {
  list: async (): Promise<Workspace[]> => {
    const res = await api.get('/knowledge/workspaces');
    return z.array(workspaceSchema).parse(res.data?.workspaces ?? []);
  },
  create: (data: CreateWorkspaceInput) => api.post('/knowledge/workspaces', data),
  stats: async (name: string): Promise<WorkspaceStats> => {
    const res = await api.get(`/knowledge/workspaces/${name}/stats`);
    return workspaceStatsSchema.parse(res.data ?? {});
  },
  update: (name: string, data: Record<string, unknown>) =>
    api.patch(`/knowledge/workspaces/${name}`, data),
  delete: (name: string) => api.delete(`/knowledge/workspaces/${name}`),
  ingest: ({ formData, allowedUserIDs, allowedRoleIDs }: IngestInput) => {
    const body = new FormData();
    // 复用调用方已拼好的 form 字段，再追加白名单 repeated 字段
    for (const [key, value] of formData.entries()) {
      body.append(key, value);
    }
    for (const id of allowedUserIDs ?? []) body.append('allowed_user_ids', id);
    for (const id of allowedRoleIDs ?? []) body.append('allowed_role_ids', id);
    return api.post('/knowledge/ingest', body, {
      headers: { 'Content-Type': 'multipart/form-data' },
    });
  },
  listDocuments: async (name: string): Promise<KnowledgeDocument[]> => {
    const res = await api.get(`/knowledge/workspaces/${name}/documents`);
    return z.array(documentSchema).parse(res.data?.documents ?? []);
  },
  deleteDocument: (name: string, documentID: string) =>
    api.delete(`/knowledge/workspaces/${encodeURIComponent(name)}/documents/${encodeURIComponent(documentID)}`),
  // 替换文档级白名单（PUT .../access）；返回回显的白名单
  setDocAccess: async (name: string, documentID: string, input: DocumentAccessInput) => {
    const res = await api.put(
      `/knowledge/workspaces/${encodeURIComponent(name)}/documents/${encodeURIComponent(documentID)}/access`,
      {
        allowed_user_ids: input.allowedUserIDs,
        allowed_role_ids: input.allowedRoleIDs,
      },
    );
    return res.data as { allowed_user_ids: string[]; allowed_role_ids: string[] };
  },
  // 引用溯源预览：按 doc_id 重组分块返回原文内容
  previewDocument: async (name: string, documentID: string): Promise<DocumentPreview> => {
    const res = await api.get(
      `/knowledge/workspaces/${encodeURIComponent(name)}/documents/${encodeURIComponent(documentID)}/preview`,
    );
    return documentPreviewSchema.parse(res.data ?? {});
  },
  query: async (data: QueryInput): Promise<QueryResult> => {
    const res = await api.post('/knowledge/query', data);
    return queryResultSchema.parse(res.data ?? {});
  },
  // 工作区产品版本历史（member 可见 GET，admin-only POST rollback）
  listVersions: async (name: string): Promise<WorkspaceVersion[]> => {
    const res = await api.get(`/knowledge/workspaces/${encodeURIComponent(name)}/versions`);
    return z.array(workspaceVersionSchema).parse(res.data?.versions ?? []);
  },
  // 单版本内容：列表元数据 + 整份编辑面 payload（name/description/config 键）。
  // 「详情」Drawer 取点击版与其直父(parentVersionId)两次内容现算字段前后值。
  getVersion: async (name: string, versionId: string): Promise<WorkspaceVersionDetail> => {
    const res = await api.get(`/knowledge/workspaces/${encodeURIComponent(name)}/versions/${encodeURIComponent(versionId)}`);
    return workspaceVersionDetailSchema.parse(res.data ?? {});
  },
  rollback: async (name: string, versionId: string): Promise<void> => {
    await api.post(`/knowledge/workspaces/${encodeURIComponent(name)}/rollback`, { versionId });
  },
};
