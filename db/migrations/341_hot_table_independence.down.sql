-- Migration 341 (down): 回滚热表独立化
--
-- 作用：将 request_logs_hot 恢复为 request_logs_default 分区
--       恢复 Migration 337 的架构（DETACH 当月分区 + DEFAULT 分区）
--
-- 执行顺序（重要）：
--   1. 先 DETACH 当月分区（避免后续 INSERT default 报 partition constraint）
--   2. 重建 request_logs_default 分区
--   3. 迁移 hot → default 数据
--   4. 删除 hot 表
--   5. 删除 promote 函数
--   6. 恢复 3 路 UNION VIEW（动态月份）
--   7. 验证

BEGIN;

-- ============================================================
-- Step 0: 先 DETACH 当月分区（关键步骤）
-- ============================================================
-- 如果不先 DETACH 当月分区，后续 INSERT INTO request_logs_default
-- 会因为 partition constraint 错误而失败（PG DEFAULT 分区约束动态）。

DO $$
DECLARE
  current_month text := to_char(now(), 'YYYY_MM');
  partition_name text := 'request_logs_' || current_month;
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_inherits i
    JOIN pg_class c ON i.inhrelid = c.oid
    WHERE c.relname = partition_name
  ) THEN
    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', partition_name);
    RAISE NOTICE 'DETACHED current month partition: %', partition_name;
  ELSE
    RAISE NOTICE 'Current month partition % not ATTACHED (no-op)', partition_name;
  END IF;
END $$;

-- ============================================================
-- Step 1: 重建 request_logs_default 分区
-- ============================================================

CREATE TABLE request_logs_default PARTITION OF request_logs DEFAULT;

DO $$ BEGIN RAISE NOTICE 'Created request_logs_default as DEFAULT partition'; END $$;

-- ============================================================
-- Step 2: 迁移数据：hot → default
-- ============================================================

INSERT INTO request_logs_default
SELECT * FROM request_logs_hot
ON CONFLICT (request_id, ts) DO NOTHING;

-- 验证数据完整性
DO $$
DECLARE
  hot_count bigint;
  default_count bigint;
BEGIN
  SELECT count(*) INTO hot_count FROM request_logs_hot;
  SELECT count(*) INTO default_count FROM request_logs_default;

  IF hot_count <> default_count THEN
    RAISE WARNING 'Row count mismatch: hot=%, default=%', hot_count, default_count;
  ELSE
    RAISE NOTICE 'Data migrated: % rows', default_count;
  END IF;
END $$;

-- ============================================================
-- Step 3: 删除热表（仅在数据迁移完成后）
-- ============================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_hot') THEN
    DROP TABLE request_logs_hot CASCADE;
    RAISE NOTICE 'Dropped request_logs_hot';
  END IF;
END $$;

-- ============================================================
-- Step 4: 恢复旧 VIEW（3 路 UNION，动态月份）
-- ============================================================

DROP VIEW IF EXISTS request_logs_with_current_month;

-- 动态构建 VIEW：当月分区名基于当前月份
DO $$
DECLARE
  current_month text := to_char(now(), 'YYYY_MM');
  partition_name text := 'request_logs_' || current_month;
  view_def text;
BEGIN
  view_def := format(
    'CREATE VIEW request_logs_with_current_month AS
     SELECT * FROM request_logs
     UNION ALL
     SELECT * FROM %I
     UNION ALL
     SELECT * FROM request_logs_default',
    partition_name
  );
  EXECUTE view_def;
  RAISE NOTICE 'VIEW request_logs_with_current_month restored (using partition: %)', partition_name;
END $$;

COMMENT ON VIEW request_logs_with_current_month IS
'Rollback to 3-way UNION VIEW (migration 337 architecture).
Dynamically uses the current month partition for cross-month queries.';

-- ============================================================
-- Step 5: 删除 promote 函数
-- ============================================================

DO $$
BEGIN
  DROP FUNCTION IF EXISTS promote_request_logs_hot_to_partition(interval, int);
  RAISE NOTICE 'Dropped promote_request_logs_hot_to_partition function';
END $$;

-- ============================================================
-- Step 6: 验证回滚完整性
-- ============================================================

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_hot') THEN
    RAISE EXCEPTION 'request_logs_hot still exists after rollback';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_default') THEN
    RAISE EXCEPTION 'request_logs_default was not created';
  END IF;

  RAISE NOTICE '===== Migration 341 ROLLBACK SUCCESSFUL =====';
  RAISE NOTICE 'Architecture restored to migration 337 state';
END $$;

COMMIT;

-- ============================================================
-- 验证查询
-- ============================================================
-- SELECT count(*) FROM request_logs_default;
-- SELECT count(*) FROM request_logs;  -- 应包含所有 ATTACHED 月度分区
-- SELECT * FROM request_logs_with_current_month LIMIT 1;