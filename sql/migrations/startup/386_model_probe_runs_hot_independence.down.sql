-- Migration 386 down: 回滚 model_probe_runs 热表独立化
--
-- 操作：
--   1. 删除 promote / drop_old_partitions 函数
--   2. 删除视图 model_probe_runs_with_current_month
--   3. 删除 ensure_model_probe_runs_partition 函数
--   4. 删除热表 model_probe_runs_hot
--   5. 把 model_probe_runs 分区表 detach 所有月度分区
--   6. 把 model_probe_runs 分区表改回普通表（drop partitioned structure）
--   7. 把 model_probe_runs_old 恢复为 model_probe_runs（columnar 访问）
--
-- 注意：rollback 不会恢复任何数据（历史数据保留在 *_old 表中）

BEGIN;

-- 1. 删除 promote 函数
DROP FUNCTION IF EXISTS promote_model_probe_runs_hot_to_partition(interval, int);

-- 2. 删除 drop_old_partitions 函数
DROP FUNCTION IF EXISTS drop_old_model_probe_runs_partitions(int);

-- 3. 删除视图
DROP VIEW IF EXISTS model_probe_runs_with_current_month;

-- 4. 删除 ensure 函数
DROP FUNCTION IF EXISTS ensure_model_probe_runs_partition(timestamp with time zone);

-- 5. 删除热表
DROP TABLE IF EXISTS model_probe_runs_hot;

-- 6. detach 所有月度分区（partitioned → flat）
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT c.relname AS partition_name
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'model_probe_runs'
    LOOP
        EXECUTE format('ALTER TABLE model_probe_runs DETACH PARTITION %I', rec.partition_name);
        RAISE NOTICE 'Detached partition: %', rec.partition_name;
    END LOOP;
END $$;

-- 7. 删除空的 partitioned 父表
DROP TABLE IF EXISTS model_probe_runs;

-- 8. 把 model_probe_runs_old 改回 model_probe_runs（保持 columnar access）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'model_probe_runs_old') THEN
        ALTER TABLE model_probe_runs_old RENAME TO model_probe_runs;
        RAISE NOTICE 'Restored model_probe_runs from model_probe_runs_old';
    ELSE
        RAISE EXCEPTION 'model_probe_runs_old does not exist; cannot rollback';
    END IF;
END $$;

COMMIT;

-- ============================================================
-- 回滚后状态
-- ============================================================
-- model_probe_runs: 普通 columnar 表（恢复原状）
-- model_probe_runs_hot: 已删除
-- 视图/函数: 已删除
-- 数据: 不恢复（保留在 *_old 或 detached 分区中，可手动恢复）
