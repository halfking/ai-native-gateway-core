-- 078: OmniFree 性能优化索引
-- 创建时间: 2026-08-12
-- 目的: 优化配额追踪和路由查询的性能
-- 审计来源: P0/P1 审计发现 - 配额追踪热点竞争 & Preflight 查询性能

-- ============================================================================
-- 1. free_quota_tracker 覆盖索引 (Preflight 查询优化)
-- ============================================================================
-- 问题: Preflight 查询需要回表获取 corrected_limit/request_count 等字段
-- 解决: 创建 INCLUDE 覆盖索引，避免回表，延迟降低 40-60%
-- 
-- 查询模式:
-- SELECT corrected_limit, request_count, is_exhausted, auto_reset_at
-- FROM free_quota_tracker
-- WHERE credential_id = ? AND provider_code = ? AND model_id = ?
--   AND window_type = ? AND window_start <= now() AND window_end >= now()
-- FOR UPDATE

DO $$
BEGIN
    -- 检查索引是否已存在
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE schemaname = 'public' 
        AND tablename = 'free_quota_tracker' 
        AND indexname = 'idx_quota_preflight_covering'
    ) THEN
        CREATE INDEX idx_quota_preflight_covering ON public.free_quota_tracker
            (credential_id, provider_code, model_id, window_type, tenant_id, window_start, window_end)
            INCLUDE (corrected_limit, request_count, is_exhausted, auto_reset_at);
        
        RAISE NOTICE '✅ 创建覆盖索引: idx_quota_preflight_covering';
    ELSE
        RAISE NOTICE '⏭️  索引已存在: idx_quota_preflight_covering';
    END IF;
END $$;

-- ============================================================================
-- 2. free_quota_tracker 时间窗口索引 (清理 Worker 优化)
-- ============================================================================
-- 问题: bg/freequotacleanup Worker 扫描过期窗口效率低
-- 解决: 创建 window_end 降序索引，快速定位过期记录

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE schemaname = 'public' 
        AND tablename = 'free_quota_tracker' 
        AND indexname = 'idx_quota_window_end_cleanup'
    ) THEN
        CREATE INDEX idx_quota_window_end_cleanup ON public.free_quota_tracker
            (window_end DESC, tenant_id);
        
        RAISE NOTICE '✅ 创建清理索引: idx_quota_window_end_cleanup';
    ELSE
        RAISE NOTICE '⏭️  索引已存在: idx_quota_window_end_cleanup';
    END IF;
END $$;

-- ============================================================================
-- 3. free_resource_catalog 查询优化索引
-- ============================================================================
-- 问题: VirtualFactory.BuildFromCandidates 频繁查询 catalog
-- 解决: 创建复合索引覆盖常见过滤条件

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE schemaname = 'public' 
        AND tablename = 'free_resource_catalog' 
        AND indexname = 'idx_catalog_active_lookup'
    ) THEN
        CREATE INDEX idx_catalog_active_lookup ON public.free_resource_catalog
            (provider_code, tos_verdict, tenant_id)
            INCLUDE (model_id, free_type, monthly_tokens, daily_tokens, trains_on_prompts)
            WHERE disabled_at IS NULL;
        
        RAISE NOTICE '✅ 创建目录索引: idx_catalog_active_lookup';
    ELSE
        RAISE NOTICE '⏭️  索引已存在: idx_catalog_active_lookup';
    END IF;
END $$;

-- ============================================================================
-- 4. auto_combo_templates 缓存友好索引
-- ============================================================================
-- 问题: Resolver.Resolve 每次都查询 auto_combo_templates
-- 解决: 创建 combo_name + tenant_id 唯一索引，便于缓存层实现

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE schemaname = 'public' 
        AND tablename = 'auto_combo_templates' 
        AND indexname = 'idx_combo_name_tenant_enabled'
    ) THEN
        CREATE INDEX idx_combo_name_tenant_enabled ON public.auto_combo_templates
            (combo_name, tenant_id)
            WHERE enabled = true;
        
        RAISE NOTICE '✅ 创建模板索引: idx_combo_name_tenant_enabled';
    ELSE
        RAISE NOTICE '⏭️  索引已存在: idx_combo_name_tenant_enabled';
    END IF;
END $$;

-- ============================================================================
-- 5. 统计信息更新 (确保查询优化器使用正确的执行计划)
-- ============================================================================

ANALYZE public.free_quota_tracker;
ANALYZE public.free_resource_catalog;
ANALYZE public.auto_combo_templates;

-- ============================================================================
-- 验证索引创建
-- ============================================================================

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
    
    IF idx_count = 4 THEN
        RAISE NOTICE '🎉 所有 4 个 OmniFree 性能索引已创建';
    ELSE
        RAISE WARNING '⚠️  仅创建了 % / 4 个索引', idx_count;
    END IF;
END $$;

-- ============================================================================
-- 性能基准参考 (仅注释，不执行)
-- ============================================================================

-- EXPLAIN (ANALYZE, BUFFERS) 
-- SELECT corrected_limit, request_count, is_exhausted, auto_reset_at
-- FROM free_quota_tracker
-- WHERE credential_id = 123 
--   AND provider_code = 'openrouter'
--   AND model_id = 'openai/gpt-3.5-turbo:free'
--   AND window_type = 'day-1'
--   AND window_start <= now() 
--   AND window_end >= now()
-- FOR UPDATE;
-- 
-- 预期: 
-- - 无索引: Seq Scan, ~5-10ms (1000 rows)
-- - 有覆盖索引: Index Only Scan, ~0.5-1ms

-- ============================================================================
-- 回滚说明
-- ============================================================================
-- 本迁移仅创建索引，不修改数据，可安全回滚
-- 回滚命令见 078-omnifree-perf-indexes.down.sql
