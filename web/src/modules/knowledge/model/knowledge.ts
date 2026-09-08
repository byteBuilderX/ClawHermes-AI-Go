import { z } from 'zod';

export const workspaceConfigSchema = z
  .object({
    embedding_model: z.string().optional().default(''),
    chunking_strategy: z.string().optional().default('structure_recursive'),
    chunk_size: z.number().optional(),
    chunk_overlap: z.number().optional(),
    query_mode: z.string().optional(),
    top_k: z.number().optional(),
    reranking: z.string().optional(),
    rerank_model: z.string().optional(),
    judge_model: z.string().optional(),
    // 评分指令附加段（空 = 后端用内置评分 prompt；与 pkg/constants
    // MaxRerankScoringInstructionsRunes / MaxJudgeScoringInstructionsRunes 同步）。
    rerank_scoring_instructions: z.string().optional(),
    judge_scoring_instructions: z.string().optional(),
    score_threshold: z.number().optional(),
    rerank_top_k: z.number().optional(),
  })
  .passthrough();
export type WorkspaceConfig = z.infer<typeof workspaceConfigSchema>;

// 通用产品版本历史（resource_versions）单行：字段与后端 WorkspaceVersionResponse
// 对齐（前端共用 VersionHistory 组件）。createdByName 由后端解析昵称，前端回退
// createdBy。safeSummary 是版本摘要快照（snake_case 键，canonical JSON 可哈希）。
export const workspaceVersionSchema = z
  .object({
    id: z.string(),
    versionNo: z.number().optional(),
    status: z.string(),
    source: z.string().optional().default(''),
    contentHash: z.string().optional().default(''),
    // ParentVersionID 指向直父版本（首版为空串）；「详情」Drawer 以父版本整份 payload
    // 为 before 基线现算字段前后值。见 spec §4.3。
    parentVersionId: z.string().optional().default(''),
    createdBy: z.string().optional().default(''),
    createdByName: z.string().optional().default(''),
    createdAt: z.string().optional().default(''),
    publishedAt: z.string().optional().default(''),
    isCurrent: z.boolean().optional().default(false),
    safeSummary: z.record(z.unknown()).optional().default({}),
  })
  .passthrough();
export type WorkspaceVersion = z.infer<typeof workspaceVersionSchema>;

// 单版本内容：列表字段 + 整份编辑面 payload（name/description/config 键，config 内
// PascalCase 子键）。「详情」Drawer 取点击版与其直父版两次内容递归 diff 叶子字段。
export const workspaceVersionDetailSchema = workspaceVersionSchema
  .extend({ payload: z.record(z.unknown()).optional().default({}) })
  .passthrough();
export type WorkspaceVersionDetail = z.infer<typeof workspaceVersionDetailSchema>;

export const workspaceSchema = z
  .object({
    id: z.string().optional(),
    name: z.string(),
    description: z.string().optional().default(''),
    config: workspaceConfigSchema.optional(),
  })
  .passthrough();
export type Workspace = z.infer<typeof workspaceSchema>;

export const workspaceStatsSchema = z
  .object({
    description: z.string().optional().default(''),
    config: workspaceConfigSchema.optional(),
    stats: z
      .object({
        row_count: z.number().optional(),
        doc_count: z.number().optional(),
      })
      .passthrough()
      .optional(),
    // 白名单 member 集合（详情页据此回显「申请编辑权限」按钮状态；nil→[] 兜底）
    editors: z.array(z.string()).optional().default([]),
  })
  .passthrough();
export type WorkspaceStats = z.infer<typeof workspaceStatsSchema>;

export const documentSchema = z
  .object({
    id: z.string().default(''),
    source: z.string().default(''),
    content_hash: z.string().default(''),
    ingest_status: z.string().default('completed'),
    ingest_error: z.string().default(''),
    processed_chunks: z.number().default(0),
    total_chunks: z.number().default(0),
    created_at: z.string().nullable().optional(),
    ingest_started_at: z.string().nullable().optional(),
    ingest_finished_at: z.string().nullable().optional(),
    // 文档级访问白名单（仅 admin/owner 回显，member 恒为空数组）
    allowed_user_ids: z.array(z.string()).optional().default([]),
    allowed_role_ids: z.array(z.string()).optional().default([]),
    // 当前 viewer 是否不在该文档白名单内（后端按 viewer 计算：admin/owner/creator
    // 与平台库恒 false）。true 时前端渲染锁定态 + 申请查看入口；内容仍由后端拒绝。
    restricted: z.boolean().optional().default(false),
  })
  .passthrough();
export type KnowledgeDocument = z.infer<typeof documentSchema>;

export const querySourceSchema = z
  .object({
    document_id: z.string().optional().default(''),
    score: z.number().optional().default(0),
    content: z.string().optional().default(''),
    // P1.1 之后 query 响应带文档名与所在 workspace，用于来源卡片跳转预览
    document_title: z.string().optional().default(''),
    // parent_content: 命中 chunk 所在整节原文(Parent-Child 策略才有值)，
    // 供来源卡片就地展开"查看上下文"
    parent_content: z.string().optional().default(''),
    workspace: z.string().optional().default(''),
  })
  .passthrough();
export type QuerySource = z.infer<typeof querySourceSchema>;

export const chunkSegmentSchema = z
  .object({
    chunk_id: z.string().optional().default(''),
    index: z.number().optional().default(0),
    content: z.string().optional().default(''),
    parent_content: z.string().optional().default(''),
  })
  .passthrough();
export type ChunkSegment = z.infer<typeof chunkSegmentSchema>;

// 原文未存储（knowledge_docs.content 已 DROP），预览由分块重组而来。
export const documentPreviewSchema = z
  .object({
    workspace: z.string().optional().default(''),
    document_id: z.string().optional().default(''),
    document_title: z.string().optional().default(''),
    chunk_count: z.number().optional().default(0),
    segments: z.array(chunkSegmentSchema).optional().default([]),
  })
  .passthrough();
export type DocumentPreview = z.infer<typeof documentPreviewSchema>;

// 文档级访问白名单设置入参（PUT /knowledge/workspaces/:name/documents/:documentID/access）
export interface DocumentAccessInput {
  allowedUserIDs: string[];
  allowedRoleIDs: string[];
}

// 权限弹窗表单值（hooks 与组件共用的类型定义，避免 hooks 依赖 components）
export interface DocAccessValues {
  allowedUserIDs?: string[];
  allowedRoleIDs?: string[];
}

// NoAnswerReason 与后端 pkg/constants 的 NoAnswerReason 枚举逐值对齐
// （跨 context 单一事实源，值进入响应契约与指标 label）。
export const noAnswerReasons = [
  'no_sources',
  'threshold_filtered',
  'access_restricted',
  'insufficient_evidence',
  'unsupported_mode',
] as const;
export type NoAnswerReason = (typeof noAnswerReasons)[number];

// 无答案结构化信号（snake_case 对齐 /knowledge/query 响应契约）。
export interface NoAnswerInfo {
  reason: NoAnswerReason;
  retrieved_count: number;
  filtered_count: number;
  best_score: number;
  retried: boolean;
  rewritten_query: string;
  detail: string;
}

export const noAnswerInfoSchema = z.object({
  reason: z.enum(noAnswerReasons),
  retrieved_count: z.number().optional().default(0),
  filtered_count: z.number().optional().default(0),
  best_score: z.number().optional().default(0),
  retried: z.boolean().optional().default(false),
  rewritten_query: z.string().optional().default(''),
  detail: z.string().optional().default(''),
});
export type ParsedNoAnswerInfo = z.infer<typeof noAnswerInfoSchema>;

export const queryResultSchema = z
  .object({
    answer: z.string().optional().default(''),
    sources: z.array(querySourceSchema).optional().default([]),
    // 无答案信号：旧后端无此键，null 兼容（.nullable().optional()）。
    no_answer: noAnswerInfoSchema.nullable().optional(),
    best_score: z.number().optional().default(0),
    candidate_count: z.number().optional().default(0),
  })
  .passthrough();
export type QueryResult = z.infer<typeof queryResultSchema>;

export interface CreateWorkspaceInput {
  name: string;
  description: string;
  config: {
    embedding_model: string;
    chunking_strategy: string;
    chunk_size: number;
    chunk_overlap: number;
    query_mode: string;
    top_k: number;
  };
  editors?: string[];
}
