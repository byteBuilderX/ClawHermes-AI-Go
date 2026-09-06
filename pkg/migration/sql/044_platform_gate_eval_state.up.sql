-- 分层门禁 P1（spec §4.1.1）：平台参数版本携带评测门禁状态（unknown 未评估 /
-- rollback_recommended 待回滚评审 / …）。值域约束由应用层维护，本列 TEXT 放开。
ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS eval_state TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS eval_state_updated_at TIMESTAMPTZ;
ALTER TABLE public.platform_config_versions ADD COLUMN IF NOT EXISTS eval_state_updated_by TEXT NOT NULL DEFAULT '';
