-- 修复 request_logs_bodies 分区问题
-- 问题：当前时间无法找到对应分区
-- 解决方案：创建 default 分区作为兜底

BEGIN;

-- 创建 default 分区（如果不存在）
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

-- 验证修复
DO $$
DECLARE
    test_result INTEGER;
BEGIN
    -- 尝试插入测试数据
    INSERT INTO request_logs_bodies (request_id, ts, request_body)
    VALUES ('fix-test-' || NOW()::TEXT, NOW(), '{"test": true}'::jsonb);
    
    -- 验证插入成功
    SELECT COUNT(*) INTO test_result 
    FROM request_logs_bodies 
    WHERE request_id LIKE 'fix-test-%';
    
    IF test_result > 0 THEN
        RAISE NOTICE 'Insert test passed: % rows', test_result;
        
        -- 清理测试数据
        DELETE FROM request_logs_bodies WHERE request_id LIKE 'fix-test-%';
        RAISE NOTICE 'Test data cleaned up';
    ELSE
        RAISE WARNING 'Insert test failed';
    END IF;
END $$;

COMMIT;

-- 显示分区信息
SELECT 
    tablename,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size
FROM pg_tables 
WHERE schemaname = 'public' 
  AND tablename LIKE 'request_logs_bodies%'
ORDER BY tablename;
