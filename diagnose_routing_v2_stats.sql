-- ============================================================================
-- routing-v2 统计数据诊断脚本
-- ============================================================================
-- 用途：检查为什么 routing-v2 页面有请求数据但没有统计数据
-- 使用：psql $DATABASE_URL -f diagnose_routing_v2_stats.sql

\echo '========================================='
\echo '1. 检查 request_logs 最近写入情况'
\echo '========================================='
SELECT 
    COUNT(*) as total_rows,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_count,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as explicit_count,
    MAX(ts) as latest_ts,
    MIN(ts) as earliest_ts,
    NOW() - MAX(ts) as time_since_last
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour';

\echo ''
\echo '========================================='
\echo '2. 检查最近 24 小时的数据分布'
\echo '========================================='
SELECT 
    DATE_TRUNC('hour', ts) as hour,
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_requests,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as explicit_requests
FROM request_logs
WHERE ts > NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour DESC
LIMIT 10;

\echo ''
\echo '========================================='
\echo '3. 检查 auto_decision 字段状态'
\echo '========================================='
SELECT 
    COUNT(*) as total_auto_requests,
    COUNT(*) FILTER (WHERE auto_decision IS NOT NULL) as with_auto_decision,
    COUNT(*) FILTER (WHERE auto_decision IS NULL) as null_auto_decision,
    ROUND(100.0 * COUNT(*) FILTER (WHERE auto_decision IS NOT NULL) / NULLIF(COUNT(*), 0), 2) as fill_rate_pct
FROM request_logs
WHERE ts > NOW() - INTERVAL '24 hours'
  AND is_auto_request = TRUE;

\echo ''
\echo '========================================='
\echo '4. 检查 usage_source 字段状态'
\echo '========================================='
SELECT 
    usage_source,
    COUNT(*) as count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
GROUP BY usage_source
ORDER BY count DESC;

\echo ''
\echo '========================================='
\echo '5. 检查必需索引是否存在'
\echo '========================================='
SELECT 
    indexname,
    indexdef
FROM pg_indexes
WHERE tablename = 'request_logs'
  AND (
    indexname LIKE '%explicit%'
    OR indexname LIKE '%auto%'
    OR indexname LIKE '%ts%'
  )
ORDER BY indexname;

\echo ''
\echo '========================================='
\echo '6. 测试 7 天统计查询（与后端一致）'
\echo '========================================='
SELECT 
    COALESCE(NULLIF(task_type, ''), 
             CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) AS task_key,
    COUNT(*) as count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  )
GROUP BY task_key
ORDER BY count DESC;

\echo ''
\echo '========================================='
\echo '7. 检查 audit 统计（总请求数）'
\echo '========================================='
SELECT
  COUNT(*) as total_requests,
  COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0) as successes,
  COALESCE(SUM(CASE WHEN is_auto_request THEN 1 ELSE 0 END), 0) as total_auto,
  COALESCE(SUM(CASE WHEN NOT is_auto_request THEN 1 ELSE 0 END), 0) as total_specified,
  ROUND(100.0 * COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0) / NULLIF(COUNT(*), 0), 2) as success_rate_pct
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  );

\echo ''
\echo '========================================='
\echo '8. 检查模型分布（热力图数据源）'
\echo '========================================='
SELECT 
    COALESCE(NULLIF(outbound_model, ''), client_model) AS model,
    COUNT(*) as count
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  )
GROUP BY model
ORDER BY count DESC
LIMIT 10;

\echo ''
\echo '========================================='
\echo '9. 检查最近的错误记录'
\echo '========================================='
SELECT 
    ts,
    request_id,
    is_auto_request,
    client_model,
    success,
    CASE 
        WHEN auto_decision IS NULL THEN 'NULL'
        ELSE 'has_value'
    END as auto_decision_status
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND success = FALSE
ORDER BY ts DESC
LIMIT 5;

\echo ''
\echo '========================================='
\echo '10. 查询性能测试（EXPLAIN ANALYZE）'
\echo '========================================='
EXPLAIN ANALYZE
SELECT 
    COALESCE(NULLIF(outbound_model, ''), client_model) AS row_key,
    COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) AS col_key,
    COUNT(*)::float8 AS val
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
  AND COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) IS NOT NULL
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  )
GROUP BY row_key, col_key;

\echo ''
\echo '========================================='
\echo '诊断完成！'
\echo '========================================='
