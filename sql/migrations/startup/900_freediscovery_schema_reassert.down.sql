-- Migration 900 down: 移除再投递的 FreeDiscovery 数据模型。
-- ⚠ 若迁移后已产生真实模板/任务/结果数据，本脚本会连带删除 —— 回滚即回到
-- free-discovery 503（未接线）形态。
-- 不删 get_current_tenant() / omnifree_touch_updated_at()（075 OmniFree 的表仍依赖）。

BEGIN;

DROP TABLE IF EXISTS public.discovery_results CASCADE;
DROP TABLE IF EXISTS public.discovery_tasks CASCADE;
DROP TABLE IF EXISTS public.provider_templates CASCADE;

ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS source_type;
ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS discovery_task_id;
ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS last_synced_at;
ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS upstream_metadata;

COMMIT;
