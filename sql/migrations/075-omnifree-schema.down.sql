BEGIN;

-- OmniFree Phase 1: 回滚脚本
-- 用途: 回滚 075-omnifree-schema.sql 的所有更改

-- 删除函数
DROP FUNCTION IF EXISTS public.omnifree_touch_updated_at();
DROP FUNCTION IF EXISTS public.fn_quota_preflight_check(BIGINT, TEXT, TEXT, FLOAT);
DROP FUNCTION IF EXISTS public.fn_quota_preflight_check(BIGINT, TEXT, TEXT, INT, FLOAT, TEXT);
DROP FUNCTION IF EXISTS public.fn_compute_deduped_quota(TEXT, TEXT[]);
DROP FUNCTION IF EXISTS public.fn_compute_deduped_quota(TEXT);
DROP FUNCTION IF EXISTS public.fn_compute_deduped_quota(BIGINT, TEXT[]);

-- 删除视图
DROP VIEW IF EXISTS v_free_resource_summary;

-- 删除表 (级联删除外键约束)
DROP TABLE IF EXISTS public.keyless_providers CASCADE;
DROP TABLE IF EXISTS public.auto_combo_templates CASCADE;
DROP TABLE IF EXISTS public.free_quota_tracker CASCADE;
DROP TABLE IF EXISTS public.free_resource_catalog CASCADE;

-- 移除扩展列
ALTER TABLE public.provider_catalog DROP COLUMN IF EXISTS has_free_tier;
ALTER TABLE public.provider_catalog DROP COLUMN IF EXISTS free_tier_notes;
ALTER TABLE public.provider_catalog DROP COLUMN IF EXISTS official_free_docs_url;

DO $$ 
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='credentials') THEN
        ALTER TABLE public.credentials DROP COLUMN IF EXISTS is_free_tier;
        ALTER TABLE public.credentials DROP COLUMN IF EXISTS free_quota_window_type;
        ALTER TABLE public.credentials DROP COLUMN IF EXISTS free_quota_limit;
    END IF;
END $$;

-- model_offers view 无扩展列，跳过

-- 完成消息
DO $$
BEGIN
    RAISE NOTICE '✅ OmniFree 数据模型回滚完成';
END $$;

COMMIT;
