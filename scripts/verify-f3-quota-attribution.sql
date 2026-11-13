-- F3 智谱 trial 配额归因验证 SQL
-- 目的：验证 e2242e078 修复部署前后，error_kind 从 transient/request_rejected 
--       转变为 upstream_quota_permanent
-- 
-- 使用方法：
--   ssh 211-mesh "docker exec acc-pg17 psql -U postgres -d llm_gateway -f /path/to/this.sql"
-- 或：
--   docker exec acc-pg17 psql -U postgres -d llm_gateway -f /path/to/this.sql

-- ============================================================================
-- 查询 1: 部署前后 error_kind 分布对比
-- ============================================================================
-- 假设部署时间：2026-11-13 12:50:00+08（e2242e078 提交后约 2 分钟）
-- 
-- 预期结果：
--   before_deploy | request_rejected 或 transient | N 条
--   after_deploy  | upstream_quota_permanent      | M 条
-- ============================================================================

\echo '========================================='
\echo '查询 1: 部署前后 error_kind 分布对比'
\echo '========================================='

SELECT 
    CASE 
        WHEN event_at < '2026-11-13 12:50:00+08' THEN 'before_deploy'
        ELSE 'after_deploy'
    END AS deployment_phase,
    error_kind,
    COUNT(*) AS cnt,
    MIN(event_at AT TIME ZONE 'Asia/Shanghai') AS earliest,
    MAX(event_at AT TIME ZONE 'Asia/Shanghai') AS latest
FROM _request_logs
WHERE client_model = 'glm-5.3-flash-trial'
  AND error_message LIKE '%no active%quota%'
  AND event_at BETWEEN '2026-11-13 10:00:00+08' AND '2026-11-13 14:00:00+08'
GROUP BY 1, 2
ORDER BY 1, 2;

-- ============================================================================
-- 查询 2: 所有智谱 trial 配额错误的详细时间线
-- ============================================================================
-- 显示每条记录的时间戳和归因，用于精确定位部署时间点
-- ============================================================================

\echo ''
\echo '========================================='
\echo '查询 2: 详细时间线（前 20 条）'
\echo '========================================='

SELECT 
    event_at AT TIME ZONE 'Asia/Shanghai' AS event_time_beijing,
    request_id,
    error_kind,
    upstream_status_code,
    client_status_code,
    LEFT(error_message, 80) AS error_msg_preview
FROM _request_logs
WHERE client_model = 'glm-5.3-flash-trial'
  AND error_message LIKE '%no active%quota%'
  AND event_at BETWEEN '2026-11-13 10:00:00+08' AND '2026-11-13 14:00:00+08'
ORDER BY event_at
LIMIT 20;

-- ============================================================================
-- 查询 3: 验证 upstream_quota_permanent 记录的完整性
-- ============================================================================
-- 确认修复后的记录具有正确的字段值
-- ============================================================================

\echo ''
\echo '========================================='
\echo '查询 3: upstream_quota_permanent 记录完整性'
\echo '========================================='

SELECT 
    COUNT(*) AS total_count,
    COUNT(CASE WHEN error_kind = 'upstream_quota_permanent' THEN 1 END) AS quota_permanent_count,
    COUNT(CASE WHEN upstream_status_code = 400 THEN 1 END) AS status_400_count,
    COUNT(CASE WHEN client_status_code = 502 THEN 1 END) AS client_502_count,
    COUNT(CASE WHEN client_status_code = 429 THEN 1 END) AS client_429_count,
    COUNT(CASE WHEN success = false THEN 1 END) AS failure_count
FROM _request_logs
WHERE client_model = 'glm-5.3-flash-trial'
  AND error_message LIKE '%no active%quota%'
  AND event_at BETWEEN '2026-11-13 10:00:00+08' AND '2026-11-13 14:00:00+08';

-- ============================================================================
-- 查询 4: 扩展时间窗口检查（最近 24 小时）
-- ============================================================================
-- 确认没有新的 transient 误归因
-- ============================================================================

\echo ''
\echo '========================================='
\echo '查询 4: 最近 24 小时 error_kind 分布'
\echo '========================================='

SELECT 
    error_kind,
    COUNT(*) AS cnt,
    MIN(event_at AT TIME ZONE 'Asia/Shanghai') AS earliest,
    MAX(event_at AT TIME ZONE 'Asia/Shanghai') AS latest
FROM _request_logs
WHERE client_model = 'glm-5.3-flash-trial'
  AND error_message LIKE '%no active%quota%'
  AND event_at > NOW() - INTERVAL '24 hours'
GROUP BY 1
ORDER BY 2 DESC;

-- ============================================================================
-- 查询 5: 采样检查——抽取 3 条代表性记录
-- ============================================================================

\echo ''
\echo '========================================='
\echo '查询 5: 采样记录（各阶段代表）'
\echo '========================================='

-- 修复前最后一条
(SELECT 'before_deploy' AS phase, event_at AT TIME ZONE 'Asia/Shanghai' AS event_time, 
        request_id, error_kind, upstream_status_code, error_message
 FROM _request_logs
 WHERE client_model = 'glm-5.3-flash-trial'
   AND error_message LIKE '%no active%quota%'
   AND event_at < '2026-11-13 12:50:00+08'
 ORDER BY event_at DESC LIMIT 1)
UNION ALL
-- 修复后第一条
(SELECT 'after_deploy_first' AS phase, event_at AT TIME ZONE 'Asia/Shanghai' AS event_time,
        request_id, error_kind, upstream_status_code, error_message
 FROM _request_logs
 WHERE client_model = 'glm-5.3-flash-trial'
   AND error_message LIKE '%no active%quota%'
   AND event_at >= '2026-11-13 12:50:00+08'
 ORDER BY event_at ASC LIMIT 1)
UNION ALL
-- 修复后最新一条
(SELECT 'after_deploy_latest' AS phase, event_at AT TIME ZONE 'Asia/Shanghai' AS event_time,
        request_id, error_kind, upstream_status_code, error_message
 FROM _request_logs
 WHERE client_model = 'glm-5.3-flash-trial'
   AND error_message LIKE '%no active%quota%'
   AND event_at >= '2026-11-13 12:50:00+08'
 ORDER BY event_at DESC LIMIT 1)
ORDER BY event_time;

\echo ''
\echo '========================================='
\echo '验证完成！请检查上述结果。'
\echo '========================================='
