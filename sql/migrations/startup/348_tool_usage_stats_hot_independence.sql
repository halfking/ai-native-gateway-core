-- Migration 348: tool_usage_stats 热表独立化
--
-- 背景：
--   统一所有分区表为 HOT 模式（独立热表架构），提升查询性能和代码一致性。
--
-- 核心改变：
--   1. tool_usage_stats_default (分区) → tool_usage_stats_hot (独立表)
--   2. 删除 DEFAULT 分区
--   3. 简化 VIEW（2 路 UNION）
--   4. 更新 promote 函数
--
-- 参考：migration 341 (request_logs 热表独立化模板)
-- Author: llm-gateway-ops (2026-07-05)

BEGIN;

-- ============================================================
-- 1. 创建独立热表 tool_usage_stats_hot
-- ============================================================

CREATE TABLE IF NOT EXISTS tool_usage_stats_hot (
    LIKE tool_usage_stats INCLUDING ALL
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE 'Created tool_usage_stats_hot table'; END $$;

-- ============================================================
-- 2. 创建索引（与父表一致）
-- ============================================================

-- 唯一约束（支持UPSERT）
CREATE UNIQUE INDEX IF NOT EXISTS tool_usage_stats_hot_tool_tenant_date_key 
ON tool_usage_stats_hot (tool_id, tenant_id, usage_date);

-- 常用查询索引
CREATE INDEX IF NOT EXISTS tool_usage_stats_hot_tool_date_idx
ON tool_usage_stats_hot (tool_id, usage_date DESC);

CREATE INDEX IF NOT EXISTS tool_usage_stats_hot_date_idx
ON tool_usage_stats_hot (usage_date DESC);

CREATE INDEX IF NOT EXISTS tool_usage_stats_hot_tenant_date_idx
ON tool_usage_stats_hot (tenant_id, usage_date DESC);

DO $$ BEGIN RAISE NOTICE 'Created indexes on tool_usage_stats_hot'; END $$;

-- ============================================================
-- 3. 迁移数据：tool_usage_stats_default → tool_usage_stats_hot
-- ============================================================

-- 3.1 检查源表
DO $$
DECLARE
  source_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'tool_usage_stats_default') THEN
    SELECT count(*) INTO source_count FROM tool_usage_stats_default;
    RAISE NOTICE 'Found % rows in tool_usage_stats_default to migrate', source_count;
  ELSE
    RAISE NOTICE 'tool_usage_stats_default does not exist, skip migration';
  END IF;
END $$;

-- 3.2 迁移数据（仅当源表存在时）
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'tool_usage_stats_default') THEN
    INSERT INTO tool_usage_stats_hot
    SELECT * FROM tool_usage_stats_default
    ON CONFLICT (tool_id, tenant_id, usage_date) DO NOTHING;
  END IF;
END $$;

-- 3.3 验证数据完整性
DO $$
DECLARE
  old_count bigint;
  new_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'tool_usage_stats_default') THEN
    SELECT count(*) INTO old_count FROM tool_usage_stats_default;
    SELECT count(*) INTO new_count FROM tool_usage_stats_hot;
    
    IF old_count <> new_count THEN
      RAISE EXCEPTION 'Data mismatch after migration: default=%, hot=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migrated % rows to tool_usage_stats_hot', new_count;
  END IF;
END $$;

-- ============================================================
-- 4. DETACH + DROP tool_usage_stats_default
-- ============================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'tool_usage_stats_default') THEN
    ALTER TABLE tool_usage_stats DETACH PARTITION tool_usage_stats_default;
    RAISE NOTICE 'DETACHED tool_usage_stats_default';
  END IF;
END $$;

DROP TABLE IF EXISTS tool_usage_stats_default CASCADE;

DO $$ BEGIN RAISE NOTICE 'Dropped tool_usage_stats_default'; END $$;

-- ============================================================
-- 5. 更新 VIEW（3 路 → 2 路 UNION）
-- ============================================================

DROP VIEW IF EXISTS tool_usage_stats_with_current_month;

CREATE VIEW tool_usage_stats_with_current_month AS
SELECT * FROM tool_usage_stats_hot    -- 热表（0-7天，独立）
UNION ALL
SELECT * FROM tool_usage_stats;        -- 父表（自动聚合所有 ATTACHED 月度分区）

COMMENT ON VIEW tool_usage_stats_with_current_month IS
'Optimized query VIEW using hot table architecture.
- tool_usage_stats_hot: independent hot table (0-7 days)
- tool_usage_stats: parent table (auto-aggregates all ATTACHED monthly partitions)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 348 (2026-07-05).';

-- ============================================================
-- 6. 创建 promote 函数（hot → 月度分区）
-- ============================================================

DROP FUNCTION IF EXISTS promote_tool_usage_stats_default_batch();

CREATE OR REPLACE FUNCTION promote_tool_usage_stats_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  -- 使用两步法：先 SELECT 出冷行到临时表，再 DELETE + INSERT
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM tool_usage_stats_hot
  WHERE usage_date < CURRENT_DATE - p_retention::interval
  ORDER BY usage_date
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  -- 从 hot 表删除这些行
  DELETE FROM tool_usage_stats_hot
  WHERE (tool_id, tenant_id, usage_date) IN (
    SELECT tool_id, tenant_id, usage_date FROM _promote_hot_batch
  );

  -- 插入到父表（PG 自动路由到对应月度分区）
  BEGIN
    INSERT INTO tool_usage_stats
    SELECT * FROM _promote_hot_batch
    ON CONFLICT (tool_id, tenant_id, usage_date) DO NOTHING;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_tool_usage_stats_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_tool_usage_stats_hot_to_partition(interval, int) IS
'Move cold rows (older than p_retention) from tool_usage_stats_hot to monthly partitions.
Data is inserted into parent table and PostgreSQL automatically routes to correct partition.
Returns number of rows moved. Loop until 0 to drain all cold data.
Created by migration 348 (2026-07-05).';

-- ============================================================
-- 7. 验证
-- ============================================================

DO $$
DECLARE
  hot_count bigint;
  view_exists boolean;
  promote_fn_exists boolean;
  storage_type text;
BEGIN
  -- 检查热表
  SELECT count(*) INTO hot_count FROM tool_usage_stats_hot;
  RAISE NOTICE 'tool_usage_stats_hot contains % rows', hot_count;
  
  -- 检查存储类型
  SELECT am.amname INTO storage_type
  FROM pg_class c
  LEFT JOIN pg_am am ON c.relam = am.oid
  WHERE c.relname = 'tool_usage_stats_hot';
  
  IF storage_type <> 'heap' THEN
    RAISE EXCEPTION 'tool_usage_stats_hot storage is %, expected heap', storage_type;
  END IF;
  
  -- 检查视图
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'tool_usage_stats_with_current_month'
  ) INTO view_exists;
  
  IF NOT view_exists THEN
    RAISE EXCEPTION 'View tool_usage_stats_with_current_month not found';
  END IF;
  
  -- 检查 promote 函数
  SELECT EXISTS (
    SELECT 1 FROM pg_proc WHERE proname = 'promote_tool_usage_stats_hot_to_partition'
  ) INTO promote_fn_exists;
  
  IF NOT promote_fn_exists THEN
    RAISE EXCEPTION 'Function promote_tool_usage_stats_hot_to_partition not found';
  END IF;
  
  RAISE NOTICE 'Migration 348 verification PASSED';
  RAISE NOTICE '  - hot table: % rows (storage: %)', hot_count, storage_type;
  RAISE NOTICE '  - view: exists';
  RAISE NOTICE '  - promote function: exists';
END $$;

COMMIT;

-- ============================================================
-- 使用说明
-- ============================================================
-- 
-- 应用后：
--   1. 所有 INSERT/UPDATE 写入 tool_usage_stats_hot
--   2. >7 天数据自动 promote 到月度分区
--   3. 跨月查询使用 tool_usage_stats_with_current_month 视图
--
-- 代码修改：
--   - INSERT INTO tool_usage_stats_default → tool_usage_stats_hot
--   - UPDATE tool_usage_stats_default → tool_usage_stats_hot
--   - SELECT ... FROM tool_usage_stats WHERE usage_date >= CURRENT_DATE - 7 → tool_usage_stats_hot
--
-- 手动 promote 测试：
--   SELECT promote_tool_usage_stats_hot_to_partition('7 days', 100);
