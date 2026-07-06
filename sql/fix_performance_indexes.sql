# 性能优化 SQL 修复脚本

-- 修复 Migration 355: 日期索引表达式问题
-- 问题: date_trunc('day', ts) 不是 IMMUTABLE 函数
-- 解决: 使用 CAST(ts AS date) 或 (ts::date)

-- 1. 删除失败的索引（如果存在）
DROP INDEX IF EXISTS idx_request_logs_ts_day;

-- 2. 使用兼容的表达式重建
CREATE INDEX idx_request_logs_ts_day 
    ON request_logs ((ts::date));

COMMENT ON INDEX idx_request_logs_ts_day IS '按日聚合查询（活动趋势）- 使用 ts::date 表达式';

-- 3. 验证索引创建成功
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE indexname = 'idx_request_logs_ts_day'
    ) THEN
        RAISE NOTICE '✓ 索引 idx_request_logs_ts_day 创建成功';
    ELSE
        RAISE EXCEPTION '✗ 索引 idx_request_logs_ts_day 创建失败';
    END IF;
END $$;

-- 4. 查询示例（验证索引使用）
-- EXPLAIN ANALYZE 
-- SELECT ts::date AS day, COUNT(*) AS requests
-- FROM request_logs
-- WHERE tenant_id = 'tenant_abc'
--   AND ts >= NOW() - INTERVAL '7 days'
-- GROUP BY day
-- ORDER BY day;

-- 预期: Index Scan using idx_request_logs_ts_day
