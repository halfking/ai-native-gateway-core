BEGIN;

-- FreeDiscovery Phase 1: 回滚脚本
-- 用途: 回滚 084-freediscovery-schema.sql 的所有更改

-- 删除新表 (级联删除外键 / 触发器 / 策略)
DROP TABLE IF EXISTS public.discovery_results CASCADE;
DROP TABLE IF EXISTS public.discovery_tasks CASCADE;
DROP TABLE IF EXISTS public.provider_templates CASCADE;

-- 移除 free_resource_catalog 扩展列
ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS source_type;
ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS discovery_task_id;
ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS last_synced_at;
ALTER TABLE public.free_resource_catalog DROP COLUMN IF EXISTS upstream_metadata;

-- 不删除 get_current_tenant() / omnifree_touch_updated_at():
-- 075 (OmniFree) 的表仍依赖它们, 回滚本迁移不应破坏既有功能.

DO $$
BEGIN
    RAISE NOTICE '✅ FreeDiscovery 数据模型回滚完成';
END $$;

COMMIT;
