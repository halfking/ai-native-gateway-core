-- 修复 request_logs_bodies 分区问题（修订版）
-- 问题：当前时间无法找到对应分区，且 columnar 存储不支持 DELETE
-- 解决方案：创建 default 分区作为 heap 存储

BEGIN;

-- 创建 default 分区（heap 存储，不使用 columnar）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_tables 
        WHERE schemaname = 'public' 
        AND tablename = 'request_logs_bodies_default'
    ) THEN
        CREATE TABLE request_logs_bodies_default 
        PARTITION OF request_logs_bodies DEFAULT;
        
        RAISE NOTICE 'Created request_logs_bodies_default partition';
    ELSE
        RAISE NOTICE 'request_logs_bodies_default already exists';
    END IF;
END $$;

-- 验证修复：插入到父表，确认路由落到 default 分区，并只从 heap default 分区清理
DO $$
DECLARE
    test_result INTEGER;
    test_request_id TEXT := 'fix-verification-' || extract(epoch from NOW())::TEXT;
BEGIN
    -- 尝试插入测试数据
    INSERT INTO request_logs_bodies (request_id, ts, request_body)
    VALUES (test_request_id, NOW(), '{"test": true}'::jsonb);
    
    -- 验证插入成功
    SELECT COUNT(*) INTO test_result 
    FROM request_logs_bodies_default
    WHERE request_id = test_request_id;
    
    IF test_result > 0 THEN
        RAISE NOTICE 'Insert test PASSED: % rows inserted successfully', test_result;
        DELETE FROM request_logs_bodies_default WHERE request_id = test_request_id;
        RAISE NOTICE 'Verification row cleaned up from request_logs_bodies_default';
    ELSE
        RAISE WARNING 'Insert test FAILED';
    END IF;
END $$;

COMMIT;

-- 显示分区信息
\echo ''
\echo '=== Partition Status ==='
SELECT 
    c.relname as partition_name,
    CASE WHEN am.amname = 'columnar' THEN 'columnar' ELSE 'heap' END as storage,
    pg_size_pretty(pg_total_relation_size(c.oid)) as size
FROM pg_class c
LEFT JOIN pg_am am ON c.relam = am.oid
WHERE c.relname LIKE 'request_logs_bodies%'
  AND c.relkind IN ('r', 'p')
ORDER BY c.relname;
