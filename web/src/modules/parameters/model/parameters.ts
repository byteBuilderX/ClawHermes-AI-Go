import { z } from 'zod';

// 统一参数注册表的 wire 形状：定义在代码（后端 registry），值在
// platform_settings / agents.parameters。前端只消费 schema 渲染控件 + 读写平台值。

export const visualHintSchema = z
  .object({
    control: z.enum(['slider', 'select', 'toggle', 'textarea', 'number', 'model', 'embedding_model']),
    min: z.number().optional(),
    max: z.number().optional(),
    step: z.number().optional(),
    options: z.array(z.unknown()).optional(),
    unit: z.string().optional(),
  })
  .passthrough();
export type VisualHint = z.infer<typeof visualHintSchema>;

export const parameterDefinitionSchema = z
  .object({
    key: z.string(),
    scope: z.enum(['platform', 'resource']),
    category: z.string().optional().default(''),
    display_name: z.string().optional().default(''),
    description: z.string().optional().default(''),
    value_type: z.enum(['int', 'float', 'bool', 'string']),
    default: z.unknown().optional(),
    visual_hint: visualHintSchema,
    optimizable: z.boolean().optional().default(false),
    sensitive: z.boolean().optional().default(false),
  })
  .passthrough();
export type ParameterDefinition = z.infer<typeof parameterDefinitionSchema>;

// PlatformValues:key → 当前生效的平台层值(0=unset 由后端按定义裁剪)。
export type PlatformValues = Record<string, unknown>;

export interface PlatformSettingsFormValues {
  [key: string]: number | string | boolean | undefined;
}

// PlatformConfigVersion 是分组版本历史的一行（配置变更审计视图）。snapshot 是
// 不可变快照，base_version_id 指向发布时生效的 production 版本（diff 链）。
// is_current 由服务端按 production label 推导（前端不跨组拼快照字符串比对），
// 标记该行就是当前生效版本。
export type PlatformVersionStatus = 'draft' | 'published' | 'archived';

export interface PlatformConfigVersion {
  id: number;
  group_key: string;
  version_seq: number;
  status: PlatformVersionStatus;
  is_current: boolean;
  snapshot: Record<string, unknown>;
  base_version_id: number | null;
  message: string;
  created_by: string;
  // created_by 的可读名（display_name > github_login > 原文），服务端 LEFT JOIN
  // public.users 现算；system/未知 uuid 无命中则回退 created_by 原文。
  created_by_name?: string;
  created_at: string;
}
