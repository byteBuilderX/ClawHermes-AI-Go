import { z } from 'zod';

import type { CreateSkillRequest as CreateSkillPayload } from '@/services/gen/skill';

const jsonObjectSchema = z.record(z.unknown());

export const skillSchema = z.object({
  id: z.string(), name: z.string(), description: z.string().optional().default(''),
  status: z.string().optional().default('published'), activeRevisionId: z.string().optional(),
  created_at: z.string().optional(), updated_at: z.string().optional(),
  // isSystem: 系统内置 skill（ID 前缀 builtin:）的资源属性，仅标识用途，不参与
  // 权限/控制特判——编辑、白名单、删除与普通 skill 走同一套权限体系。
  isSystem: z.boolean().optional().default(false),
}).passthrough();
export type Skill = z.infer<typeof skillSchema>;
export type SkillConfig = never;
export type SkillType = never;

export const skillProductSchema = skillSchema;
export type SkillProduct = Skill;

// skillRevisionSchema: 版本化编辑面，保留 name/description/instructions 三字段
//（skill 模型收敛，删除了 capability/activation_contract）。isCurrent 仅版本
// 历史列表填充，标记当前生效版本。列表行携带完整编辑面内容，「详情」Drawer
// 以直父(parentRevisionId)整份内容为 before 基线现算字段前后值。
export const skillRevisionSchema = z.object({
  id: z.string(), skillId: z.string(), revisionNo: z.number().optional(), status: z.string(),
  name: z.string().default(''), description: z.string().default(''),
  instructions: z.string().default(''), publishChecks: jsonObjectSchema.optional(),
  contentHash: z.string().optional().default(''),
  isCurrent: z.boolean().optional().default(false),
  createdBy: z.string().optional().default(''),
  createdByName: z.string().optional().default(''),
  createdAt: z.string().optional().default(''),
  // ParentRevisionID 指向直父版本（首版为空串）；Drawer 以父版本内容为变更前基线。
  parentRevisionId: z.string().optional().default(''),
}).passthrough();
export type SkillRevision = z.infer<typeof skillRevisionSchema>;
export type SkillVersion = SkillRevision;

export const skillWorkspaceSchema = z.object({
  skill: skillProductSchema,
  // active 是当前生效版本;存量未发布 skill 首次保存前为空。
  active: skillRevisionSchema,
  editors: z.array(z.string()).default([]),
  // hasDraft 标记存在未发布草稿(skills.draft_revision_id 非空);前端据此
  // 展示草稿提示条并开启发布入口。
  hasDraft: z.boolean().optional().default(false),
}).passthrough();
export type SkillWorkspace = z.infer<typeof skillWorkspaceSchema>;

export interface SkillFormValues {
  name: string;
  description: string;
  instructions: string;
  editors?: string[];
}

export type { CreateSkillPayload };

export const buildCreateSkillPayload = (values: SkillFormValues): CreateSkillPayload => ({
  name: values.name, description: values.description, instructions: values.instructions,
  editors: values.editors || [],
});
