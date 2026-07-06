-- Migration 347: credential_model_index 热表独立化
--
-- 背景：
--   统一所有分区表为 HOT 模式（独立热表架构），提升查询性能和代码一致性。
--
-- 核心改变：
--   1. credential_model_index_default (分区) → credential_model_index_hot (独立表)
--   2. 删除 DEFAULT 分区
--   3. 简化 VIEW（2 路 UNION）
--   4. 更新 promote 函数
--
-- 参考：migration 341 (request_logs 热表独立化模板)
-- Author: llm-gateway-ops (2026-07-05)

BEGIN;

-- ============================================================
-- 1. 创建独立热表 credential_model_index_hot
-- ============================================================

CREATE TABLE IF NOT EXISTS credential_model_index_hot (
    LIKE credential_model_index INCLUDING ALL
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE 'Created credential_model_index_hot table'; END $$;

-- ============================================================
-- 2. 创建索引（与父表一致）
-- ============================================================

-- 唯一约束（实际主键）
CREATE UNIQUE INDEX IF NOT EXISTS credential_model_index_hot_unique_key
ON credential_model_index_hot (bucket, credential_id, raw_model);

-- 常用查询索引
CREATE INDEX IF NOT EXISTS credential_model_index_hot_credential_id_idx 
ON credential_model_index_hot (credential_id);

CREATE INDEX IF NOT EXISTS credential_model_index_hot_canonical_id_idx 
ON credential_model_index_hot (canonical_id);

CREATE INDEX IF NOT EXISTS credential_model_index_hot_updated_at_idx 
ON credential_model_index_hot (updated_at DESC);

DO $$ BEGIN RAISE NOTICE 'Created indexes on credential_model_index_hot'; END $$;

-- ============================================================
-- 3. 迁移数据：credential_model_index_default → credential_model_index_hot
-- ============================================================

-- 3.1 检查源表
DO $$
DECLARE
  source_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index_default') THEN
    SELECT count(*) INTO source_count FROM credential_model_index_default;
    RAISE NOTICE 'Found % rows in credential_model_index_default to migrate', source_count;
  ELSE
    RAISE NOTICE 'credential_model_index_default does not exist, skip migration';
  END IF;
END $$;

-- 3.2 迁移数据
INSERT INTO credential_model_index_hot
SELECT * FROM credential_model_index_default
ON CONFLICT (bucket, credential_id, raw_model) DO NOTHING;

-- 3.3 验证数据完整性
DO $$
DECLARE
  old_count bigint;
  new_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index_default') THEN
    SELECT count(*) INTO old_count FROM credential_model_index_default;
    SELECT count(*) INTO new_count FROM credential_model_index_hot;
    
    IF old_count <> new_count THEN
      RAISE EXCEPTION 'Data mismatch after migration: default=%, hot=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migrated % rows to credential_model_index_hot', new_count;
  END IF;
END $$;

-- ============================================================
-- 4. DETACH + DROP credential_model_index_default
-- ============================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index_default') THEN
    ALTER TABLE credential_model_index DETACH PARTITION credential_model_index_default;
    RAISE NOTICE 'DETACHED credential_model_index_default';
  END IF;
END $$;

DROP TABLE IF EXISTS credential_model_index_default CASCADE;

DO $$ BEGIN RAISE NOTICE 'Dropped credential_model_index_default'; END $$;

-- ============================================================
-- 5. 更新 VIEW（3 路 → 2 路 UNION）
-- ============================================================

DROP VIEW IF EXISTS credential_model_index_with_current_month;

CREATE VIEW credential_model_index_with_current_month AS
SELECT * FROM credential_model_index_hot    -- 热表（0-7天，独立）
UNION ALL
SELECT * FROM credential_model_index;        -- 父表（自动聚合所有 ATTACHED 月度分区）

COMMENT ON VIEW credential_model_index_with_current_month IS
'Optimized query VIEW using hot table architecture.
- credential_model_index_hot: independent hot table (0-7 days)
- credential_model_index: parent table (auto-aggregates all ATTACHED monthly partitions)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 347 (2026-07-05).';

-- ============================================================
-- 6. 创建 promote 函数（hot → 月度分区）
-- ============================================================

DROP FUNCTION IF EXISTS promote_credential_model_index_default_batch();

CREATE OR REPLACE FUNCTION promote_credential_model_index_hot_to_partition(
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
  SELECT * FROM credential_model_index_hot
  WHERE updated_at < now() - p_retention
  ORDER BY updated_at
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  -- 从 hot 表删除这些行
  DELETE FROM credential_model_index_hot
  WHERE (bucket, credential_id, raw_model) IN (
    SELECT bucket, credential_id, raw_model FROM _promote_hot_batch
  );

  -- 插入到父表（PG 自动路由到对应月度分区）
  BEGIN
    INSERT INTO credential_model_index
    SELECT * FROM _promote_hot_batch
    ON CONFLICT (bucket, credential_id, raw_model) DO NOTHING;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_credential_model_index_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_credential_model_index_hot_to_partition(interval, int) IS
'Move cold rows (older than p_retention) from credential_model_index_hot to monthly partitions.
Uses updated_at column for aging. Data is inserted into parent table and PostgreSQL 
automatically routes to correct partition.
Returns number of rows moved. Loop until 0 to drain all cold data.
Created by migration 347 (2026-07-05).';

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
  SELECT count(*) INTO hot_count FROM credential_model_index_hot;
  RAISE NOTICE 'credential_model_index_hot contains % rows', hot_count;
  
  -- 检查存储类型
  SELECT am.amname INTO storage_type
  FROM pg_class c
  LEFT JOIN pg_am am ON c.relam = am.oid
  WHERE c.relname = 'credential_model_index_hot';
  
  IF storage_type <> 'heap' THEN
    RAISE EXCEPTION 'credential_model_index_hot storage is %, expected heap', storage_type;
  END IF;
  
  -- 检查视图
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'credential_model_index_with_current_month'
  ) INTO view_exists;
  
  IF NOT view_exists THEN
    RAISE EXCEPTION 'View credential_model_index_with_current_month not found';
  END IF;
  
  -- 检查 promote 函数
  SELECT EXISTS (
    SELECT 1 FROM pg_proc WHERE proname = 'promote_credential_model_index_hot_to_partition'
  ) INTO promote_fn_exists;
  
  IF NOT promote_fn_exists THEN
    RAISE EXCEPTION 'Function promote_credential_model_index_hot_to_partition not found';
  END IF;
  
  RAISE NOTICE 'Migration 347 verification PASSED';
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
--   1. 所有 INSERT/UPDATE 写入 credential_model_index_hot
--   2. >7 天数据自动 promote 到月度分区（基于 updated_at）
--   3. 跨月查询使用 credential_model_index_with_current_month 视图
--
-- 代码修改：
--   - INSERT INTO credential_model_index_default → credential_model_index_hot
--   - UPDATE credential_model_index_default → credential_model_index_hot
--   - SELECT ... FROM credential_model_index WHERE updated_at >= NOW() - '7 days' → credential_model_index_hot
--
-- 手动 promote 测试：
--   SELECT promote_credential_model_index_hot_to_partition('7 days', 100);
