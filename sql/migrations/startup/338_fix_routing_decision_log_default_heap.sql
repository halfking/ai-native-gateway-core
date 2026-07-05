-- Migration 338: 修复 routing_decision_log_default 的存储引擎（columnar → heap）
--
-- 问题根因：
--   Migration 333 创建 routing_decision_log 分区表时，将所有分区（包括 *_default）
--   都继承自原 routing_decision_log_old 表，而 routing_decision_log_old 是
--   columnar 表。这导致 routing_decision_log_default 也是 columnar，无法支持
--   UPDATE/DELETE 操作。
--
-- 症状：
--   INSERT INTO routing_decision_log_default (...) 成功
--   UPDATE routing_decision_log_default ... 报错：
--     ERROR: UPDATE and CTID scans not supported for ColumnarScan
--
-- 解决方案：
--   重建 routing_decision_log_default 为 heap 表（支持 UPDATE/DELETE），
--   迁移现有数据，重新 ATTACH 为 DEFAULT 分区。
--
-- 适用场景：
--   本 migration 只修复 routing_decision_log_default。
--   其他表的 *_default 分区在各自的初始 migration 中已正确创建为 heap。

BEGIN;

-- ============================================================
-- Step 1: 检查当前状态
-- ============================================================
DO $$
DECLARE
    current_am text;
BEGIN
    SELECT am.amname INTO current_am
    FROM pg_class c
    JOIN pg_am am ON c.relam = am.oid
    WHERE c.relname = 'routing_decision_log_default';
    
    IF current_am IS NULL THEN
        RAISE EXCEPTION 'routing_decision_log_default not found';
    END IF;
    
    IF current_am = 'heap' THEN
        RAISE NOTICE 'routing_decision_log_default is already heap, skipping migration 338';
        -- 不执行后续步骤（幂等性）
    ELSE
        RAISE NOTICE 'routing_decision_log_default is %, converting to heap...', current_am;
    END IF;
END $$;

-- ============================================================
-- Step 2: DETACH 现有的 DEFAULT 分区
-- ============================================================
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

-- ============================================================
-- Step 3: 重命名现有 DEFAULT 分区为 _old_columnar
-- ============================================================
ALTER TABLE routing_decision_log_default RENAME TO routing_decision_log_default_old_columnar;

-- ============================================================
-- Step 4: 创建新的 heap 版本的 DEFAULT 分区
-- ============================================================
-- 注意：PostgreSQL 不支持在 CREATE TABLE ... PARTITION OF 语句中使用 LIKE
-- 必须分两步：先创建普通表，再 ATTACH 为分区
CREATE TABLE public.routing_decision_log_default (
    LIKE public.routing_decision_log INCLUDING DEFAULTS INCLUDING CONSTRAINTS
);

-- Step 4.1: ATTACH 为 DEFAULT 分区
ALTER TABLE public.routing_decision_log ATTACH PARTITION public.routing_decision_log_default DEFAULT;

-- ============================================================
-- Step 5: 迁移数据（从 columnar 旧表复制到 heap 新表）
-- ============================================================
INSERT INTO public.routing_decision_log_default
SELECT * FROM public.routing_decision_log_default_old_columnar;

-- ============================================================
-- Step 6: 验证行数一致
-- ============================================================
DO $$
DECLARE
    old_count bigint;
    new_count bigint;
BEGIN
    SELECT COUNT(*) INTO old_count FROM public.routing_decision_log_default_old_columnar;
    SELECT COUNT(*) INTO new_count FROM public.routing_decision_log_default;
    
    IF old_count <> new_count THEN
        RAISE EXCEPTION 'Row count mismatch: old=%, new=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migration 338: migrated % rows from columnar to heap', new_count;
END $$;

-- ============================================================
-- Step 7: 删除旧的 columnar 表
-- ============================================================
DROP TABLE public.routing_decision_log_default_old_columnar;

-- ============================================================
-- Step 8: 验证新表是 heap
-- ============================================================
DO $$
DECLARE
    new_am text;
BEGIN
    SELECT am.amname INTO new_am
    FROM pg_class c
    JOIN pg_am am ON c.relam = am.oid
    WHERE c.relname = 'routing_decision_log_default';
    
    IF new_am <> 'heap' THEN
        RAISE EXCEPTION 'routing_decision_log_default is still %, expected heap', new_am;
    END IF;
    
    RAISE NOTICE 'routing_decision_log_default is now heap (supports UPDATE/DELETE)';
END $$;

COMMIT;

-- ============================================================
-- 验证
-- ============================================================
-- 1. 检查存储引擎：
--    SELECT am.amname FROM pg_class c
--    JOIN pg_am am ON c.relam = am.oid
--    WHERE c.relname = 'routing_decision_log_default';
--    预期：heap
--
-- 2. 测试 INSERT：
--    INSERT INTO routing_decision_log_default (request_id, ts, tenant_id, model, success)
--    VALUES (gen_random_uuid(), NOW(), 'test', 'gpt-4', true);
--    预期：成功
--
-- 3. 测试 UPDATE：
--    UPDATE routing_decision_log_default SET prompt_tokens = 100
--    WHERE request_id = '<上一步插入的 UUID>';
--    预期：成功（不再报 columnar 错误）
