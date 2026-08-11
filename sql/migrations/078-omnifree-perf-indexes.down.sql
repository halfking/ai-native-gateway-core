-- 回滚 078: OmniFree 性能优化索引
-- 创建时间: 2026-08-12

-- 删除索引 (幂等，使用 IF EXISTS)
DROP INDEX IF EXISTS public.idx_quota_preflight_covering;
DROP INDEX IF EXISTS public.idx_quota_window_end_cleanup;
DROP INDEX IF EXISTS public.idx_catalog_active_lookup;
DROP INDEX IF EXISTS public.idx_combo_name_tenant_enabled;

-- 验证删除
DO $$
DECLARE
    idx_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO idx_count
    FROM pg_indexes
    WHERE schemaname = 'public'
      AND indexname IN (
          'idx_quota_preflight_covering',
          'idx_quota_window_end_cleanup',
          'idx_catalog_active_lookup',
          'idx_combo_name_tenant_enabled'
      );
    
    IF idx_count = 0 THEN
        RAISE NOTICE '✅ 所有 OmniFree 性能索引已删除';
    ELSE
        RAISE WARNING '⚠️  仍有 % 个索引未删除', idx_count;
    END IF;
END $$;
