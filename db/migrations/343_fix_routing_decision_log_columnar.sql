-- Migration 343: routing_decision_log_default columnar 紧急修复
--
-- 问题：
--   Migration 338 已修复该问题（转 heap），但 184 生产环境未应用完整。
--   当前 routing_decision_log_default 是 columnar（只读），无法支持 UPDATE。
--
-- 修复步骤：
--   1. 创建 heap 临时表
--   2. 迁移数据
--   3. 删除 columnar 分区
--   4. 重命名临时表为 _default
--   5. ATTACH 回父表
--
-- 前置检查：
--   SELECT am.amname FROM pg_class c 
--   JOIN pg_am am ON c.relam = am.oid 
--   WHERE c.relname = 'routing_decision_log_default';
--   -- 如果返回 'heap'，跳过本 migration
--
-- Author: llm-gateway-ops (2026-07-05)

BEGIN;

-- ============================================================
-- 1. 前置检查
-- ============================================================
DO $$
DECLARE
    storage_type text;
BEGIN
    SELECT am.amname INTO storage_type
    FROM pg_class c
    LEFT JOIN pg_am am ON c.relam = am.oid
    WHERE c.relname = 'routing_decision_log_default';
    
    IF storage_type = 'heap' THEN
        RAISE NOTICE 'routing_decision_log_default is already heap, skip migration';
        RETURN;
    END IF;
    
    IF storage_type = 'columnar' THEN
        RAISE NOTICE 'Found columnar routing_decision_log_default, proceed with conversion';
    ELSE
        RAISE EXCEPTION 'Unexpected storage type: %', storage_type;
    END IF;
END $$;

-- ============================================================
-- 2. 创建 heap 临时表
-- ============================================================
CREATE TABLE routing_decision_log_default_heap (
    LIKE routing_decision_log INCLUDING ALL
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE 'Created heap temporary table'; END $$;

-- ============================================================
-- 3. 迁移数据（从 columnar 到 heap）
-- ============================================================
INSERT INTO routing_decision_log_default_heap
SELECT * FROM routing_decision_log_default;

DO $$
DECLARE
    old_count bigint;
    new_count bigint;
BEGIN
    SELECT COUNT(*) INTO old_count FROM routing_decision_log_default;
    SELECT COUNT(*) INTO new_count FROM routing_decision_log_default_heap;
    
    IF old_count <> new_count THEN
        RAISE EXCEPTION 'Data mismatch: columnar=%, heap=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migrated % rows from columnar to heap', new_count;
END $$;

-- ============================================================
-- 4. DETACH + DROP columnar 分区
-- ============================================================
ALTER TABLE routing_decision_log DETACH PARTITION routing_decision_log_default;

DO $$ BEGIN RAISE NOTICE 'DETACHED columnar partition'; END $$;

DROP TABLE routing_decision_log_default CASCADE;

DO $$ BEGIN RAISE NOTICE 'DROPPED columnar partition'; END $$;

-- ============================================================
-- 5. 重命名临时表并 ATTACH 回父表
-- ============================================================
ALTER TABLE routing_decision_log_default_heap 
RENAME TO routing_decision_log_default;

DO $$ BEGIN RAISE NOTICE 'Renamed heap table to _default'; END $$;

ALTER TABLE routing_decision_log 
ATTACH PARTITION routing_decision_log_default DEFAULT;

DO $$ BEGIN RAISE NOTICE 'ATTACHED heap partition as DEFAULT'; END $$;

-- ============================================================
-- 6. 验证
-- ============================================================
DO $$
DECLARE
    storage_type text;
    row_count bigint;
BEGIN
    SELECT am.amname INTO storage_type
    FROM pg_class c
    LEFT JOIN pg_am am ON c.relam = am.oid
    WHERE c.relname = 'routing_decision_log_default';
    
    SELECT COUNT(*) INTO row_count FROM routing_decision_log_default;
    
    IF storage_type <> 'heap' THEN
        RAISE EXCEPTION 'Verification failed: storage is %, expected heap', storage_type;
    END IF;
    
    RAISE NOTICE 'Verification PASSED: storage=%, rows=%', storage_type, row_count;
END $$;

COMMIT;

-- ============================================================
-- 回滚说明
-- ============================================================
-- 如果需要回滚到 columnar（不推荐），执行：
--   ALTER TABLE routing_decision_log DETACH PARTITION routing_decision_log_default;
--   DROP TABLE routing_decision_log_default;
--   CREATE TABLE routing_decision_log_default PARTITION OF routing_decision_log DEFAULT USING columnar;
--   -- 注意：columnar 不支持 UPDATE，回滚后写入功能受限
