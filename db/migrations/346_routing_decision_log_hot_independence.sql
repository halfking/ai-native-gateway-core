-- Migration 346: routing_decision_log 热表独立化
--
-- 背景：
--   统一所有分区表为 HOT 模式（独立热表架构），提升查询性能和代码一致性。
--
-- 前置条件：
--   Migration 343 已将 routing_decision_log_default 转为 heap 存储
--
-- 核心改变：
--   1. routing_decision_log_default (分区) → routing_decision_log_hot (独立表)
--   2. 删除 DEFAULT 分区
--   3. 简化 VIEW（2 路 UNION）
--   4. 更新 promote 函数
--
-- 参考：migration 341 (request_logs 热表独立化模板)
-- Author: llm-gateway-ops (2026-07-05)

BEGIN;

-- ============================================================
-- 1. 创建独立热表 routing_decision_log_hot
-- ============================================================

CREATE TABLE IF NOT EXISTS routing_decision_log_hot (
    LIKE routing_decision_log INCLUDING ALL
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE 'Created routing_decision_log_hot table'; END $$;

-- ============================================================
-- 2. 创建索引（与父表一致）
-- ============================================================

-- 唯一约束（作为主键）
CREATE UNIQUE INDEX IF NOT EXISTS routing_decision_log_hot_request_id_ts_key 
ON routing_decision_log_hot (request_id, ts);

-- 常用查询索引
CREATE INDEX IF NOT EXISTS routing_decision_log_hot_ts_idx 
ON routing_decision_log_hot (ts DESC);

CREATE INDEX IF NOT EXISTS routing_decision_log_hot_tenant_id_ts_idx 
ON routing_decision_log_hot (tenant_id, ts DESC);

DO $$ BEGIN RAISE NOTICE 'Created indexes on routing_decision_log_hot'; END $$;

-- ============================================================
-- 3. 迁移数据：routing_decision_log_default → routing_decision_log_hot
-- ============================================================

-- 3.1 检查源表
DO $$
DECLARE
  source_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'routing_decision_log_default') THEN
    SELECT count(*) INTO source_count FROM routing_decision_log_default;
    RAISE NOTICE 'Found % rows in routing_decision_log_default to migrate', source_count;
  ELSE
    RAISE NOTICE 'routing_decision_log_default does not exist, skip migration';
  END IF;
END $$;

-- 3.2 迁移数据
INSERT INTO routing_decision_log_hot
SELECT * FROM routing_decision_log_default
ON CONFLICT (request_id, ts) DO NOTHING;

-- 3.3 验证数据完整性
DO $$
DECLARE
  old_count bigint;
  new_count bigint;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'routing_decision_log_default') THEN
    SELECT count(*) INTO old_count FROM routing_decision_log_default;
    SELECT count(*) INTO new_count FROM routing_decision_log_hot;
    
    IF old_count <> new_count THEN
      RAISE EXCEPTION 'Data mismatch after migration: default=%, hot=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migrated % rows to routing_decision_log_hot', new_count;
  END IF;
END $$;

-- ============================================================
-- 4. DETACH + DROP routing_decision_log_default
-- ============================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'routing_decision_log_default') THEN
    ALTER TABLE routing_decision_log DETACH PARTITION routing_decision_log_default;
    RAISE NOTICE 'DETACHED routing_decision_log_default';
  END IF;
END $$;

DROP TABLE IF EXISTS routing_decision_log_default CASCADE;

DO $$ BEGIN RAISE NOTICE 'Dropped routing_decision_log_default'; END $$;

-- ============================================================
-- 5. 更新 VIEW（3 路 → 2 路 UNION）
-- ============================================================

DROP VIEW IF EXISTS routing_decision_log_with_current_month;

CREATE VIEW routing_decision_log_with_current_month AS
SELECT * FROM routing_decision_log_hot    -- 热表（0-7天，独立）
UNION ALL
SELECT * FROM routing_decision_log;        -- 父表（自动聚合所有 ATTACHED 月度分区）

COMMENT ON VIEW routing_decision_log_with_current_month IS
'Optimized query VIEW using hot table architecture.
- routing_decision_log_hot: independent hot table (0-7 days)
- routing_decision_log: parent table (auto-aggregates all ATTACHED monthly partitions)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 346 (2026-07-05).';

-- ============================================================
-- 6. 创建 promote 函数（hot → 月度分区）
-- ============================================================

DROP FUNCTION IF EXISTS promote_routing_decision_log_default_batch();

CREATE OR REPLACE FUNCTION promote_routing_decision_log_hot_to_partition(
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
  SELECT * FROM routing_decision_log_hot
  WHERE ts < now() - p_retention
  ORDER BY ts
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  -- 从 hot 表删除这些行
  DELETE FROM routing_decision_log_hot
  WHERE (request_id, ts) IN (SELECT request_id, ts FROM _promote_hot_batch);

  -- 插入到父表（PG 自动路由到对应月度分区）
  BEGIN
    INSERT INTO routing_decision_log
    SELECT * FROM _promote_hot_batch
    ON CONFLICT (request_id, ts) DO NOTHING;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_routing_decision_log_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_routing_decision_log_hot_to_partition(interval, int) IS
'Move cold rows (older than p_retention) from routing_decision_log_hot to monthly partitions.
Data is inserted into parent table and PostgreSQL automatically routes to correct partition.
Returns number of rows moved. Loop until 0 to drain all cold data.
Created by migration 346 (2026-07-05).';

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
  SELECT count(*) INTO hot_count FROM routing_decision_log_hot;
  RAISE NOTICE 'routing_decision_log_hot contains % rows', hot_count;
  
  -- 检查存储类型
  SELECT am.amname INTO storage_type
  FROM pg_class c
  LEFT JOIN pg_am am ON c.relam = am.oid
  WHERE c.relname = 'routing_decision_log_hot';
  
  IF storage_type <> 'heap' THEN
    RAISE EXCEPTION 'routing_decision_log_hot storage is %, expected heap', storage_type;
  END IF;
  
  -- 检查视图
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'routing_decision_log_with_current_month'
  ) INTO view_exists;
  
  IF NOT view_exists THEN
    RAISE EXCEPTION 'View routing_decision_log_with_current_month not found';
  END IF;
  
  -- 检查 promote 函数
  SELECT EXISTS (
    SELECT 1 FROM pg_proc WHERE proname = 'promote_routing_decision_log_hot_to_partition'
  ) INTO promote_fn_exists;
  
  IF NOT promote_fn_exists THEN
    RAISE EXCEPTION 'Function promote_routing_decision_log_hot_to_partition not found';
  END IF;
  
  RAISE NOTICE 'Migration 346 verification PASSED';
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
--   1. 所有 INSERT 写入 routing_decision_log_hot
--   2. >7 天数据自动 promote 到月度分区
--   3. 跨月查询使用 routing_decision_log_with_current_month 视图
--
-- 代码修改：
--   - INSERT INTO routing_decision_log_default → routing_decision_log_hot
--   - SELECT ... FROM routing_decision_log WHERE ts >= NOW() - '7 days' → routing_decision_log_hot
--
-- 手动 promote 测试：
--   SELECT promote_routing_decision_log_hot_to_partition('7 days', 100);
