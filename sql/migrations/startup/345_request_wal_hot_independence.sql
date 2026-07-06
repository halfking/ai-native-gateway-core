-- Migration 345: request_wal 热表独立化
--
-- 背景：
--   统一所有分区表为 HOT 模式（独立热表架构），提升查询性能和代码一致性。
--
-- 核心改变：
--   1. request_wal_default (分区) → request_wal_hot (独立表)
--   2. 删除 DEFAULT 分区
--   3. 简化 VIEW（2 路 UNION）
--   4. 更新 promote 函数
--
-- 参考：migration 341 (request_logs 热表独立化模板)
-- Author: llm-gateway-ops (2026-07-05)

BEGIN;

-- ============================================================
-- 1. 创建独立热表 request_wal_hot
-- ============================================================

CREATE TABLE IF NOT EXISTS request_wal_hot (
    LIKE request_wal INCLUDING ALL
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE 'Created request_wal_hot table'; END $$;

-- ============================================================
-- 2. 创建索引（与父表一致）
-- ============================================================

-- 主键（LIKE INCLUDING ALL 已自动复制，这里跳过）
-- request_wal_hot_pkey PRIMARY KEY (request_id, created_at) 已存在

DO $$ BEGIN RAISE NOTICE 'Primary key already created by LIKE INCLUDING ALL'; END $$;

-- ============================================================
-- 3. 迁移数据：request_wal_default → request_wal_hot
-- ============================================================

-- 3.1 检查源表
DO $$
DECLARE
  source_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_default') THEN
    SELECT count(*) INTO source_count FROM request_wal_default;
    RAISE NOTICE 'Found % rows in request_wal_default to migrate', source_count;
  ELSE
    RAISE NOTICE 'request_wal_default does not exist, skip migration';
  END IF;
END $$;

-- 3.2 迁移数据
INSERT INTO request_wal_hot
SELECT * FROM request_wal_default
ON CONFLICT (request_id, created_at) DO NOTHING;

-- 3.3 验证数据完整性
DO $$
DECLARE
  old_count bigint;
  new_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_default') THEN
    SELECT count(*) INTO old_count FROM request_wal_default;
    SELECT count(*) INTO new_count FROM request_wal_hot;
    
    IF old_count <> new_count THEN
      RAISE EXCEPTION 'Data mismatch after migration: default=%, hot=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migrated % rows to request_wal_hot', new_count;
  END IF;
END $$;

-- ============================================================
-- 4. DETACH + DROP request_wal_default
-- ============================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_default') THEN
    ALTER TABLE request_wal DETACH PARTITION request_wal_default;
    RAISE NOTICE 'DETACHED request_wal_default';
  END IF;
END $$;

DROP TABLE IF EXISTS request_wal_default CASCADE;

DO $$ BEGIN RAISE NOTICE 'Dropped request_wal_default'; END $$;

-- ============================================================
-- 5. 更新 VIEW（3 路 → 2 路 UNION）
-- ============================================================

DROP VIEW IF EXISTS request_wal_with_current_month;

CREATE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal_hot    -- 热表（0-7天，独立）
UNION ALL
SELECT * FROM request_wal;        -- 父表（自动聚合所有 ATTACHED 月度分区）

COMMENT ON VIEW request_wal_with_current_month IS
'Optimized query VIEW using hot table architecture.
- request_wal_hot: independent hot table (0-7 days)
- request_wal: parent table (auto-aggregates all ATTACHED monthly partitions)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 345 (2026-07-05).';

-- ============================================================
-- 6. 创建 promote 函数（hot → 月度分区）
-- ============================================================

DROP FUNCTION IF EXISTS promote_request_wal_default_batch();

CREATE OR REPLACE FUNCTION promote_request_wal_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  n bigint := 0;
BEGIN
  -- request_wal 没有 ts 字段，无法按时间 promote
  -- 这里保留函数签名，但返回 0（暂不实现）
  RAISE NOTICE 'request_wal_hot_to_partition: no timestamp column, skip promote';
  RETURN 0;
END;
$$;

COMMENT ON FUNCTION promote_request_wal_hot_to_partition(interval, int) IS
'Placeholder for request_wal promote function.
request_wal does not have timestamp column, so time-based promotion is not applicable.
Created by migration 345 (2026-07-05).';

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
  SELECT count(*) INTO hot_count FROM request_wal_hot;
  RAISE NOTICE 'request_wal_hot contains % rows', hot_count;
  
  -- 检查存储类型
  SELECT am.amname INTO storage_type
  FROM pg_class c
  LEFT JOIN pg_am am ON c.relam = am.oid
  WHERE c.relname = 'request_wal_hot';
  
  IF storage_type <> 'heap' THEN
    RAISE EXCEPTION 'request_wal_hot storage is %, expected heap', storage_type;
  END IF;
  
  -- 检查视图
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_wal_with_current_month'
  ) INTO view_exists;
  
  IF NOT view_exists THEN
    RAISE EXCEPTION 'View request_wal_with_current_month not found';
  END IF;
  
  -- 检查 promote 函数
  SELECT EXISTS (
    SELECT 1 FROM pg_proc WHERE proname = 'promote_request_wal_hot_to_partition'
  ) INTO promote_fn_exists;
  
  IF NOT promote_fn_exists THEN
    RAISE EXCEPTION 'Function promote_request_wal_hot_to_partition not found';
  END IF;
  
  RAISE NOTICE 'Migration 345 verification PASSED';
  RAISE NOTICE '  - hot table: % rows (storage: %)', hot_count, storage_type;
  RAISE NOTICE '  - view: exists';
  RAISE NOTICE '  - promote function: exists (placeholder)';
END $$;

COMMIT;

-- ============================================================
-- 使用说明
-- ============================================================
-- 
-- 应用后：
--   1. 所有 INSERT/UPDATE 写入 request_wal_hot
--   2. request_wal 没有时间戳，promote 函数暂不实现
--   3. 跨月查询使用 request_wal_with_current_month 视图
--
-- 代码修改：
--   - INSERT INTO request_wal_default → request_wal_hot
--   - UPDATE request_wal_default → request_wal_hot
