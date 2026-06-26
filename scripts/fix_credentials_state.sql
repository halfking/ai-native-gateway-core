-- 71服务器凭据状态修复脚本
-- 使用方法: psql $LLM_GATEWAY_DATABASE_URL -f scripts/fix_credentials_state.sql

BEGIN;

-- ============================================
-- 1. 诊断：检查当前状态
-- ============================================

\echo '=========================================='
\echo '1. 当前凭据状态概览'
\echo '=========================================='

SELECT 
    status,
    availability_state,
    circuit_state,
    COUNT(*) as count
FROM credentials 
WHERE status != 'disabled'
GROUP BY status, availability_state, circuit_state
ORDER BY count DESC;

\echo ''
\echo '=========================================='
\echo '2. 被过滤的凭据详情'
\echo '=========================================='

SELECT 
    c.id,
    c.label,
    c.status,
    c.availability_state,
    c.circuit_state,
    c.lifecycle_status,
    c.quota_state,
    c.availability_recover_at,
    c.cooling_until,
    p.display_name as provider_name
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE c.status = 'active'
  AND (c.availability_state != 'ready' 
       OR c.circuit_state = 'open'
       OR c.lifecycle_status != 'active'
       OR c.quota_state != 'ok')
ORDER BY c.id;

\echo ''
\echo '=========================================='
\echo '3. minimax-m3 模型的探测状态'
\echo '=========================================='

SELECT 
    mps.credential_id,
    c.label,
    mps.raw_model_name,
    mps.state,
    mps.consecutive_failures,
    mps.last_probe_at,
    mps.error_message
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
WHERE lower(mps.raw_model_name) = 'minimax-m3'
ORDER BY mps.credential_id;

\echo ''
\echo '=========================================='
\echo '4. 最近1小时的质量统计'
\echo '=========================================='

SELECT 
    c.id as credential_id,
    c.label,
    COUNT(*) as total_requests,
    COUNT(CASE WHEN rl.success THEN 1 END) as success_count,
    ROUND(COUNT(CASE WHEN rl.success THEN 1 END)::numeric / COUNT(*)::numeric, 3) as success_rate,
    COUNT(CASE WHEN rl.error_kind = 'empty_response' THEN 1 END) as empty_response_count
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
WHERE rl.ts > NOW() - INTERVAL '1 hour'
  AND lower(rl.client_model) = 'minimax-m3'
GROUP BY c.id, c.label
ORDER BY total_requests DESC;

-- ============================================
-- 5. 修复：重置异常状态（需要确认后执行）
-- ============================================

\echo ''
\echo '=========================================='
\echo '5. 准备修复操作（请确认后取消注释）'
\echo '=========================================='

-- 5.1 重置 availability_state 为 ready
-- UPDATE credentials 
-- SET availability_state = 'ready',
--     availability_recover_at = NULL
-- WHERE status = 'active'
--   AND availability_state != 'ready';

-- 5.2 关闭所有打开的熔断器
-- UPDATE credentials 
-- SET circuit_state = 'closed',
--     cooling_until = NULL,
--     consecutive_failures = 0
-- WHERE status = 'active'
--   AND circuit_state = 'open';

-- 5.3 重置 quota_state
-- UPDATE credentials 
-- SET quota_state = 'ok',
--     quota_recover_at = NULL
-- WHERE status = 'active'
--   AND quota_state != 'ok';

-- 5.4 重置 lifecycle_status
-- UPDATE credentials 
-- SET lifecycle_status = 'active'
-- WHERE status = 'active'
--   AND lifecycle_status != 'active';

-- 5.5 清除 broken_confirmed 探测状态
-- UPDATE model_probe_state 
-- SET state = 'pending',
--     consecutive_failures = 0,
--     next_probe_at = NOW(),
--     error_message = 'manually reset'
-- WHERE state = 'broken_confirmed'
--   AND lower(raw_model_name) = 'minimax-m3';

-- 5.6 刷新 v_routable_credential_models 视图（如果需要）
-- REFRESH MATERIALIZED VIEW CONCURRENTLY v_routable_credential_models;

\echo ''
\echo '=========================================='
\echo '诊断完成'
\echo '=========================================='
\echo ''
\echo '下一步操作:'
\echo ''
\echo '1. 如果需要修复凭据状态，请编辑此文件，取消注释第5节的SQL语句'
\echo '2. 然后重新运行: psql $LLM_GATEWAY_DATABASE_URL -f scripts/fix_credentials_state.sql'
\echo '3. 修复后重启网关服务: systemctl restart llm-gateway'
\echo '4. 运行测试脚本验证: ./scripts/test_routing_fix.sh'
\echo ''

ROLLBACK; -- 默认回滚，不做任何更改（诊断模式）
