-- Migration 338 rollback: 恢复 routing_decision_log_default 为 columnar（不推荐）
--
-- 警告：
--   回滚后，UPDATE/DELETE routing_decision_log_default 将再次失败。
--   仅在测试或需要恢复到 migration 338 之前的状态时执行。

BEGIN;

-- 1. DETACH 当前的 heap DEFAULT 分区
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'routing_decision_log' AND c.relname = 'routing_decision_log_default'
    ) THEN
        ALTER TABLE routing_decision_log DETACH PARTITION routing_decision_log_default;
        RAISE NOTICE 'DETACHED routing_decision_log_default';
    END IF;
END $$;

-- 2. 重命名当前 heap 表为 _heap_backup
ALTER TABLE routing_decision_log_default RENAME TO routing_decision_log_default_heap_backup;

-- 3. 重建 columnar 版本的 DEFAULT 分区
CREATE TABLE public.routing_decision_log_default (
    LIKE public.routing_decision_log INCLUDING ALL
)
USING columnar
PARTITION OF public.routing_decision_log DEFAULT;

-- 4. 迁移数据（从 heap 复制到 columnar）
INSERT INTO public.routing_decision_log_default
SELECT * FROM public.routing_decision_log_default_heap_backup;

-- 5. 验证行数
DO $$
DECLARE
    old_count bigint;
    new_count bigint;
BEGIN
    SELECT COUNT(*) INTO old_count FROM public.routing_decision_log_default_heap_backup;
    SELECT COUNT(*) INTO new_count FROM public.routing_decision_log_default;
    
    IF old_count <> new_count THEN
        RAISE EXCEPTION 'Row count mismatch during rollback: old=%, new=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migration 338 rollback: restored % rows to columnar', new_count;
END $$;

-- 6. 删除 heap 备份表
DROP TABLE public.routing_decision_log_default_heap_backup;

COMMIT;
