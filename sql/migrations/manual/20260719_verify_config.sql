-- ============================================================================
-- 验证脚本: 智谱AI和火山引擎的配置与错误响应
-- Date: 2026-07-19
-- Purpose: 执行分析文档中的验证SQL，确认问题根因
-- ============================================================================

\echo '=========================================='
\echo '验证1: 智谱AI最近的错误响应'
\echo '=========================================='
\echo ''

SELECT 
    request_id,
    credential_id,
    client_model,
    error_kind,
    upstream_status_code,
    upstream_response_preview,
    created_at
FROM request_logs
WHERE provider_code = 'zhipuai'
  AND request_status = 'failure'
  AND created_at > now() - interval '24 hours'
ORDER BY created_at DESC
LIMIT 20;

\echo ''
\echo '=========================================='
\echo '验证2: 智谱AI的模型配置'
\echo '=========================================='
\echo ''

SELECT 
    p.provider_code,
    pm.id as model_id,
    pm.raw_model_name,
    pm.canonical_name,
    pm.outbound_model_name,
    pm.context_window,
    COUNT(cmb.id) as binding_count,
    COUNT(CASE WHEN cmb.available = TRUE THEN 1 END) as available_count,
    COUNT(CASE WHEN cmb.available = FALSE THEN 1 END) as unavailable_count
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
LEFT JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
WHERE p.provider_code = 'zhipuai'
GROUP BY p.provider_code, pm.id, pm.raw_model_name, pm.canonical_name, pm.outbound_model_name, pm.context_window
ORDER BY pm.raw_model_name;

\echo ''
\echo '=========================================='
\echo '验证3: 火山引擎的模型配置'
\echo '=========================================='
\echo ''

SELECT 
    p.provider_code,
    pm.id as model_id,
    pm.raw_model_name,
    pm.canonical_name,
    pm.outbound_model_name,
    pm.context_window,
    COUNT(cmb.id) as binding_count,
    COUNT(CASE WHEN cmb.available = TRUE THEN 1 END) as available_count
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
LEFT JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
GROUP BY p.provider_code, pm.id, pm.raw_model_name, pm.canonical_name, pm.outbound_model_name, pm.context_window
ORDER BY p.provider_code, pm.raw_model_name;

\echo ''
\echo '=========================================='
\echo '验证4: 火山引擎是否有GLM-5.2（大写）'
\echo '=========================================='
\echo ''

SELECT 
    p.provider_code,
    pm.raw_model_name,
    pm.canonical_name,
    pm.outbound_model_name
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND (
    LOWER(pm.raw_model_name) LIKE '%glm%'
    OR LOWER(pm.canonical_name) LIKE '%glm%'
    OR LOWER(pm.outbound_model_name) LIKE '%glm%'
  )
ORDER BY pm.raw_model_name;

\echo ''
\echo '=========================================='
\echo '验证5: 智谱AI的凭证状态'
\echo '=========================================='
\echo ''

SELECT 
    c.id as credential_id,
    c.credential_label,
    c.lifecycle_status,
    c.availability_state,
    c.billing_mode,
    c.enabled,
    COUNT(cmb.id) as model_count,
    COUNT(CASE WHEN cmb.available = TRUE THEN 1 END) as available_models,
    STRING_AGG(DISTINCT pm.raw_model_name, ', ') as models
FROM credentials c
JOIN providers p ON p.id = c.provider_id
LEFT JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
LEFT JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE p.provider_code = 'zhipuai'
  AND c.lifecycle_status = 'active'
GROUP BY c.id, c.credential_label, c.lifecycle_status, c.availability_state, c.billing_mode, c.enabled
ORDER BY c.id;

\echo ''
\echo '=========================================='
\echo '验证6: 智谱AI超限相关的错误（如果有）'
\echo '=========================================='
\echo ''

SELECT 
    error_kind,
    upstream_status_code,
    COUNT(*) as error_count,
    MAX(created_at) as last_seen,
    STRING_AGG(DISTINCT SUBSTRING(upstream_response_preview, 1, 100), ' | ') as sample_responses
FROM request_logs
WHERE provider_code = 'zhipuai'
  AND request_status = 'failure'
  AND created_at > now() - interval '7 days'
GROUP BY error_kind, upstream_status_code
ORDER BY error_count DESC
LIMIT 20;

\echo ''
\echo '=========================================='
\echo '验证7: 降级模式触发情况（查询unavailable但仍被使用的记录）'
\echo '=========================================='
\echo ''

-- 查询最近有unavailable_reason但仍产生请求的凭证
WITH unavailable_creds AS (
    SELECT DISTINCT 
        cmb.credential_id,
        pm.raw_model_name,
        cmb.unavailable_reason,
        cmb.unavailable_at
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
    WHERE cmb.available = FALSE
      AND cmb.unavailable_at > now() - interval '1 hour'
)
SELECT 
    uc.credential_id,
    uc.raw_model_name,
    uc.unavailable_reason,
    uc.unavailable_at,
    COUNT(rl.id) as requests_after_unavailable,
    MAX(rl.created_at) as last_request_at,
    STRING_AGG(DISTINCT rl.error_kind, ', ') as error_kinds
FROM unavailable_creds uc
LEFT JOIN request_logs rl 
    ON rl.credential_id = uc.credential_id
    AND rl.created_at > uc.unavailable_at
WHERE rl.id IS NOT NULL
GROUP BY uc.credential_id, uc.raw_model_name, uc.unavailable_reason, uc.unavailable_at
ORDER BY requests_after_unavailable DESC
LIMIT 10;

\echo ''
\echo '=========================================='
\echo '验证完成'
\echo '=========================================='
