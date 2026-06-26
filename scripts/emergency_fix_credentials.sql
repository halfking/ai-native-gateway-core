-- 紧急修复脚本：强制重置所有凭据状态
-- ⚠️  警告：此脚本会强制重置所有凭据，仅在紧急情况下使用
-- 使用方法: psql $LLM_GATEWAY_DATABASE_URL -f scripts/emergency_fix_credentials.sql

BEGIN;

\echo '=========================================='
\echo '⚠️  紧急凭据状态重置'
\echo '=========================================='

-- 记录修复前的状态
CREATE TEMP TABLE credential_state_backup AS
SELECT 
    id,
    label,
    status,
    availability_state,
    circuit_state,
    lifecycle_status,
    quota_state,
    availability_recover_at,
    cooling_until,
    consecutive_failures
FROM credentials
WHERE status = 'active';

\echo '已备份当前凭据状态到临时表'

-- 1. 重置所有 active 凭据的 availability_state
UPDATE credentials 
SET availability_state = 'ready',
    availability_recover_at = NULL
WHERE status = 'active'
  AND availability_state != 'ready';

\echo '✓ 已重置 availability_state'

-- 2. 关闭所有熔断器
UPDATE credentials 
SET circuit_state = 'closed',
    cooling_until = NULL,
    consecutive_failures = 0
WHERE status = 'active'
  AND circuit_state != 'closed';

\echo '✓ 已关闭所有熔断器'

-- 3. 重置 quota_state
UPDATE credentials 
SET quota_state = 'ok',
    quota_recover_at = NULL
WHERE status = 'active'
  AND quota_state != 'ok'
  AND balance_usd > 0; -- 只重置有余额的凭据

\echo '✓ 已重置 quota_state'

-- 4. 重置 lifecycle_status
UPDATE credentials 
SET lifecycle_status = 'active'
WHERE status = 'active'
  AND lifecycle_status != 'active';

\echo '✓ 已重置 lifecycle_status'

-- 5. 清除 minimax-m3 的 broken_confirmed 状态
UPDATE model_probe_state 
SET state = 'pending',
    consecutive_failures = 0,
    next_probe_at = NOW(),
    error_message = 'emergency reset at ' || NOW()
WHERE state = 'broken_confirmed'
  AND lower(raw_model_name) = 'minimax-m3';

\echo '✓ 已清除 broken_confirmed 探测状态'

-- 显示修复摘要
\echo ''
\echo '=========================================='
\echo '修复摘要'
\echo '=========================================='

SELECT 
    '已修复的凭据数量' as metric,
    COUNT(*) as count
FROM credential_state_backup
WHERE availability_state != 'ready'
   OR circuit_state != 'closed'
   OR lifecycle_status != 'active'
   OR (quota_state != 'ok' AND quota_state IS NOT NULL)

UNION ALL

SELECT 
    '当前可用凭据数量',
    COUNT(*)
FROM credentials
WHERE status = 'active'
  AND availability_state = 'ready'
  AND circuit_state = 'closed'
  AND lifecycle_status = 'active'
  AND quota_state = 'ok';

\echo ''
\echo '=========================================='
\echo '验证：检查 minimax-m3 的可路由凭据'
\echo '=========================================='

SELECT 
    c.id,
    c.label,
    v.is_routable,
    v.unavailable_reason,
    mo.manual_priority,
    mo.routing_tier
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
LEFT JOIN v_routable_credential_models v
    ON v.credential_id = mo.credential_id
    AND v.raw_model_name = mo.raw_model_name
WHERE lower(mo.raw_model_name) = 'minimax-m3'
ORDER BY mo.manual_priority, mo.routing_tier;

\echo ''
\echo '=========================================='
\echo '⚠️  确认提交'
\echo '=========================================='
\echo ''
\echo '如果上述修复看起来正确，请手动执行: COMMIT;'
\echo '如果需要撤销修复，请执行: ROLLBACK;'
\echo ''

-- 默认不自动提交，需要手动确认
-- COMMIT;
