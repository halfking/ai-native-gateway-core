-- Phase 2 热度追踪功能数据库准备脚本
-- 在184测试环境执行

-- ============================================================================
-- 1. 检查 request_logs 表结构
-- ============================================================================
\echo '=== 1. 检查 request_logs 表 ==='
\d request_logs

-- 检查关键列
SELECT 
    column_name, 
    data_type, 
    is_nullable,
    column_default
FROM information_schema.columns 
WHERE table_name = 'request_logs' 
  AND column_name IN ('id', 'client_model', 'created_at')
ORDER BY ordinal_position;

-- ============================================================================
-- 2. 检查现有索引
-- ============================================================================
\echo ''
\echo '=== 2. 现有索引 ==='
SELECT 
    schemaname,
    tablename,
    indexname,
    indexdef
FROM pg_indexes 
WHERE tablename = 'request_logs'
ORDER BY indexname;

-- ============================================================================
-- 3. 创建性能索引（如果不存在）
-- ============================================================================
\echo ''
\echo '=== 3. 创建热度查询索引 ==='

-- 检查索引是否存在
DO $$ 
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE indexname = 'idx_request_logs_created_at_model'
    ) THEN
        RAISE NOTICE '创建索引: idx_request_logs_created_at_model...';
        CREATE INDEX CONCURRENTLY idx_request_logs_created_at_model 
        ON request_logs (created_at DESC, client_model)
        WHERE client_model IS NOT NULL;
        RAISE NOTICE '✓ 索引创建成功';
    ELSE
        RAISE NOTICE '✓ 索引已存在: idx_request_logs_created_at_model';
    END IF;
END $$;

-- ============================================================================
-- 4. 数据量统计
-- ============================================================================
\echo ''
\echo '=== 4. 数据量统计 ==='

-- 总数据量
SELECT 
    COUNT(*) as total_rows,
    pg_size_pretty(pg_total_relation_size('request_logs')) as table_size
FROM request_logs;

-- 最近1小时数据
SELECT 
    '最近1小时' as time_range,
    COUNT(*) as total_requests,
    COUNT(DISTINCT client_model) as distinct_models,
    COUNT(*) FILTER (WHERE client_model IS NOT NULL) as with_model,
    COUNT(*) FILTER (WHERE client_model IS NULL) as without_model,
    ROUND(100.0 * COUNT(*) FILTER (WHERE client_model IS NOT NULL) / NULLIF(COUNT(*), 0), 2) as model_coverage_pct
FROM request_logs 
WHERE created_at > NOW() - INTERVAL '1 hour';

-- 最近24小时数据
SELECT 
    '最近24小时' as time_range,
    COUNT(*) as total_requests,
    COUNT(DISTINCT client_model) as distinct_models,
    COUNT(*) FILTER (WHERE client_model IS NOT NULL) as with_model
FROM request_logs 
WHERE created_at > NOW() - INTERVAL '24 hours';

-- ============================================================================
-- 5. 热度查询性能测试
-- ============================================================================
\echo ''
\echo '=== 5. 热度查询性能测试 ==='
\timing on

EXPLAIN ANALYZE
SELECT 
    client_model, 
    COUNT(*) AS request_count
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
  AND client_model IS NOT NULL
  AND client_model != ''
GROUP BY client_model
ORDER BY request_count DESC
LIMIT 100;

\timing off

-- ============================================================================
-- 6. TOP 20 热门模型预览
-- ============================================================================
\echo ''
\echo '=== 6. TOP 20 热门模型（最近1小时） ==='

SELECT 
    ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC) as rank,
    client_model,
    COUNT(*) as request_count,
    CASE 
        WHEN COUNT(*) >= 100 THEN '🔥 热门 (10s探测)'
        WHEN COUNT(*) >= 10 THEN '🌡️  温热 (2m探测)'
        ELSE '❄️  冷门 (10m探测)'
    END as tier,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
  AND client_model IS NOT NULL
  AND client_model != ''
GROUP BY client_model
ORDER BY request_count DESC
LIMIT 20;

-- ============================================================================
-- 7. 热度分布统计
-- ============================================================================
\echo ''
\echo '=== 7. 模型热度分布 ==='

WITH model_stats AS (
    SELECT 
        client_model,
        COUNT(*) as request_count
    FROM request_logs
    WHERE created_at > NOW() - INTERVAL '1 hour'
      AND client_model IS NOT NULL
      AND client_model != ''
    GROUP BY client_model
)
SELECT 
    CASE 
        WHEN request_count >= 100 THEN '热门 (≥100 req/h)'
        WHEN request_count >= 10 THEN '温热 (10-99 req/h)'
        ELSE '冷门 (<10 req/h)'
    END as tier,
    COUNT(*) as model_count,
    SUM(request_count) as total_requests,
    ROUND(100.0 * SUM(request_count) / SUM(SUM(request_count)) OVER (), 2) as request_pct
FROM model_stats
GROUP BY tier
ORDER BY MIN(request_count) DESC;

-- ============================================================================
-- 完成
-- ============================================================================
\echo ''
\echo '=== 数据库准备完成 ==='
\echo ''
\echo '下一步：'
\echo '1. 确认索引已创建（查看上方输出）'
\echo '2. 确认查询耗时 < 500ms'
\echo '3. 启用热度追踪: LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true'
\echo '4. 重启服务并观察日志'
