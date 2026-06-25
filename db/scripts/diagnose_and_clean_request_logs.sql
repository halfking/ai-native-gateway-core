-- Request Logs 重复记录诊断和清理脚本
-- Date: 2026-06-26
-- 
-- 这个脚本帮助你诊断和清理 request_logs 表中的重复记录问题

-- ============================================================================
-- 第一部分：诊断查询
-- ============================================================================

-- 1. 检查重复记录的总体情况
SELECT 
    COUNT(DISTINCT request_id) as unique_requests,
    COUNT(*) as total_rows,
    COUNT(*) - COUNT(DISTINCT request_id) as duplicate_rows,
    ROUND(100.0 * (COUNT(*) - COUNT(DISTINCT request_id)) / COUNT(*), 2) as duplicate_percentage
FROM request_logs
WHERE ts > now() - interval '7 days';

-- 2. 找出重复次数最多的 request_id
SELECT 
    request_id,
    COUNT(*) as duplicate_count,
    MIN(ts) as first_ts,
    MAX(ts) as last_ts,
    MAX(ts) - MIN(ts) as time_span,
    ARRAY_AGG(DISTINCT request_status ORDER BY request_status) as statuses,
    ARRAY_AGG(DISTINCT success ORDER BY success) as success_values
FROM request_logs
WHERE ts > now() - interval '7 days'
GROUP BY request_id
HAVING COUNT(*) > 1
ORDER BY COUNT(*) DESC
LIMIT 20;

-- 3. 按会话 ID 分组查看重复情况（区分同一会话多次请求 vs 同一请求重复）
SELECT 
    gw_session_id,
    COUNT(DISTINCT request_id) as unique_requests,
    COUNT(*) as total_rows,
    STRING_AGG(DISTINCT request_id, ', ') as request_ids
FROM request_logs
WHERE ts > now() - interval '7 days'
  AND gw_session_id IS NOT NULL
GROUP BY gw_session_id
HAVING COUNT(*) > COUNT(DISTINCT request_id)
ORDER BY COUNT(*) - COUNT(DISTINCT request_id) DESC
LIMIT 10;

-- 4. 检查 'in_progress' 状态的记录分布
SELECT 
    request_status,
    success,
    COUNT(*) as count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage
FROM request_logs
WHERE ts > now() - interval '7 days'
GROUP BY request_status, success
ORDER BY count DESC;

-- 5. 查看最近的重复记录详情（前10组）
WITH duplicate_requests AS (
    SELECT request_id
    FROM request_logs
    WHERE ts > now() - interval '1 day'
    GROUP BY request_id
    HAVING COUNT(*) > 1
    LIMIT 10
)
SELECT 
    rl.request_id,
    rl.ts,
    rl.request_status,
    rl.success,
    rl.error_kind,
    rl.client_model,
    rl.gw_session_id,
    rl.latency_ms,
    rl.prompt_tokens,
    rl.completion_tokens
FROM request_logs rl
INNER JOIN duplicate_requests dr ON rl.request_id = dr.request_id
ORDER BY rl.request_id, rl.ts;

-- ============================================================================
-- 第二部分：清理操作（请谨慎执行）
-- ============================================================================

-- 6. 预览将要删除的记录（不会真正删除，只是查看）
WITH duplicates AS (
    SELECT 
        request_id,
        MIN(ts) as earliest_ts,
        COUNT(*) as dup_count
    FROM request_logs
    WHERE ts > now() - interval '7 days'
    GROUP BY request_id
    HAVING COUNT(*) > 1
)
SELECT 
    rl.request_id,
    rl.ts,
    rl.request_status,
    rl.success,
    d.earliest_ts,
    d.dup_count,
    CASE 
        WHEN rl.ts = d.earliest_ts THEN 'KEEP'
        ELSE 'DELETE'
    END as action
FROM request_logs rl
INNER JOIN duplicates d ON rl.request_id = d.request_id
ORDER BY rl.request_id, rl.ts;

-- 7. 清理重复记录（保留最早的记录）
-- 警告：这会真正删除数据！请先在测试环境验证！
-- 取消注释下面的 BEGIN/COMMIT 来执行
/*
BEGIN;

WITH duplicates AS (
    SELECT 
        request_id,
        MIN(ts) as earliest_ts
    FROM request_logs
    WHERE ts > now() - interval '7 days'
    GROUP BY request_id
    HAVING COUNT(*) > 1
)
DELETE FROM request_logs rl
USING duplicates d
WHERE rl.request_id = d.request_id
  AND rl.ts > d.earliest_ts;

-- 检查删除结果
SELECT COUNT(*) as deleted_rows FROM request_logs WHERE false;

COMMIT;
*/

-- 8. 清理重复记录的安全版本（分批执行，每次最多1000条）
-- 这个版本更安全，可以多次运行直到没有重复记录
/*
WITH duplicates AS (
    SELECT 
        request_id,
        MIN(ts) as earliest_ts
    FROM request_logs
    WHERE ts > now() - interval '7 days'
    GROUP BY request_id
    HAVING COUNT(*) > 1
    LIMIT 100  -- 一次处理100个重复的 request_id
),
to_delete AS (
    SELECT rl.id
    FROM request_logs rl
    INNER JOIN duplicates d ON rl.request_id = d.request_id
    WHERE rl.ts > d.earliest_ts
    LIMIT 1000  -- 一次最多删除1000行
)
DELETE FROM request_logs
WHERE id IN (SELECT id FROM to_delete);
*/

-- ============================================================================
-- 第三部分：验证修复效果
-- ============================================================================

-- 9. 验证修复后的状态分布
SELECT 
    DATE_TRUNC('hour', ts) as hour,
    request_status,
    COUNT(*) as count
FROM request_logs
WHERE ts > now() - interval '24 hours'
GROUP BY DATE_TRUNC('hour', ts), request_status
ORDER BY hour DESC, count DESC;

-- 10. 检查是否还有重复记录
SELECT 
    CASE 
        WHEN COUNT(DISTINCT request_id) = COUNT(*) 
        THEN '✓ No duplicates found'
        ELSE '✗ Duplicates still exist: ' || (COUNT(*) - COUNT(DISTINCT request_id))::text || ' rows'
    END as status
FROM request_logs
WHERE ts > now() - interval '1 day';

-- 11. 验证成功请求的状态更新是否正常
SELECT 
    CASE 
        WHEN COUNT(*) = 0 
        THEN '✓ All success=true requests have status=success'
        ELSE '✗ Found ' || COUNT(*)::text || ' success=true with status!=success'
    END as status
FROM request_logs
WHERE ts > now() - interval '1 day'
  AND success = true
  AND request_status != 'success';

-- 12. 检查索引是否存在
SELECT 
    indexname,
    indexdef
FROM pg_indexes
WHERE tablename = 'request_logs'
  AND (indexname LIKE '%request_id%' OR indexname LIKE '%unique%')
ORDER BY indexname;
