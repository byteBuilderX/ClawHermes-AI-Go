ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS eval_state_updated_by;
ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS eval_state_updated_at;
ALTER TABLE public.platform_config_versions DROP COLUMN IF EXISTS eval_state;
