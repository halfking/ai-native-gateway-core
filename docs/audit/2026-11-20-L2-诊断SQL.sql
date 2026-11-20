-- L2 诊断：为什么凭据级探针 v2 cycleAll 跳过凭据 56
-- 
-- 背景：主档记录 last_probe_at 停在 2026-11-13（7 天没跑过）
-- 目标：对照 cycleAll SQL（bg/credential_probe_v2.go:439-469）逐条检查

-- ========== 第一步：查询凭据 56 的完整状态 ==========
SELECT 
    c.id,
    c.status,
    c.lifecycle_status,
    c.manual_disabled,
    c.quota_state,
    c.availability_state,
    c.availability_recover_at,
    c.default_probe_model,
    c.last_probe_at,
    c.last_used_at,
    c.health_status,
    c.last_probe_success,
    c.probe_consecutive_failures,
    p.id AS provider_id,
    p.enabled AS provider_enabled,
    p.manual_disabled AS provider_manual_disabled,
    p.base_url,
    p.protocol,
    p.catalog_code
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE c.id = 56;

-- ========== 第二步：检查 cycleAll 的各项过滤条件 ==========
-- 条件 1: c.status = 'active'
SELECT 'status' AS check_name, 
       c.status AS actual_value,
       (c.status = 'active') AS passes
FROM credentials c WHERE c.id = 56;

-- 条件 2: c.lifecycle_status = 'active'
SELECT 'lifecycle_status' AS check_name,
       c.lifecycle_status AS actual_value,
       (c.lifecycle_status = 'active') AS passes
FROM credentials c WHERE c.id = 56;

-- 条件 3: COALESCE(c.manual_disabled, FALSE) = FALSE
SELECT 'manual_disabled' AS check_name,
       COALESCE(c.manual_disabled, FALSE) AS actual_value,
       (COALESCE(c.manual_disabled, FALSE) = FALSE) AS passes
FROM credentials c WHERE c.id = 56;

-- 条件 4: COALESCE(p.manual_disabled, FALSE) = FALSE
SELECT 'provider_manual_disabled' AS check_name,
       COALESCE(p.manual_disabled, FALSE) AS actual_value,
       (COALESCE(p.manual_disabled, FALSE) = FALSE) AS passes
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE c.id = 56;

-- 条件 5: COALESCE(p.enabled, FALSE) = TRUE
SELECT 'provider_enabled' AS check_name,
       COALESCE(p.enabled, FALSE) AS actual_value,
       (COALESCE(p.enabled, FALSE) = TRUE) AS passes
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE c.id = 56;

-- 条件 6: COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
SELECT 'quota_state' AS check_name,
       COALESCE(c.quota_state, 'ok') AS actual_value,
       (COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')) AS passes
FROM credentials c WHERE c.id = 56;

-- 条件 7: c.availability_state <> 'suspended'
SELECT 'availability_state' AS check_name,
       c.availability_state AS actual_value,
       (c.availability_state <> 'suspended' OR c.availability_state IS NULL) AS passes
FROM credentials c WHERE c.id = 56;

-- 条件 8: (c.availability_recover_at IS NULL OR c.availability_recover_at <= now())
SELECT 'availability_recover_at' AS check_name,
       c.availability_recover_at AS actual_value,
       now() AS current_time,
       (c.availability_recover_at IS NULL OR c.availability_recover_at <= now()) AS passes
FROM credentials c WHERE c.id = 56;

-- 条件 9: COALESCE(c.default_probe_model, '') <> ''
SELECT 'default_probe_model' AS check_name,
       COALESCE(c.default_probe_model, '') AS actual_value,
       (COALESCE(c.default_probe_model, '') <> '') AS passes
FROM credentials c WHERE c.id = 56;

-- ========== 第三步：检查 skipProbeOnRecentTrafficSuccess 条件 ==========
-- 这个函数在 cycleAll 的行循环中被调用（bg/credential_probe_v2.go:504）
SELECT 
    'skipProbeOnRecentTrafficSuccess' AS check_name,
    (
        COALESCE(c.last_used_at, to_timestamp(0)) > now() - INTERVAL '30 minutes'
        AND c.health_status IN ('healthy', 'unknown')
        AND COALESCE(c.last_probe_success, TRUE)
    ) OR (
        c.health_status = 'healthy'
        AND COALESCE(c.last_probe_success, FALSE)
        AND NOT EXISTS (
            -- 简化版失败证据检查（真实版本在 credentialFailureEvidenceSQL 中）
            SELECT 1 FROM credential_model_bindings cmb
            WHERE cmb.credential_id = c.id
              AND cmb.available = FALSE
              AND cmb.unavailable_at > now() - INTERVAL '30 minutes'
        )
    ) AS would_skip,
    c.last_used_at,
    c.health_status,
    c.last_probe_success
FROM credentials c
WHERE c.id = 56;

-- ========== 第四步：组合条件验证（模拟 cycleAll 的完整 WHERE 子句）==========
SELECT 
    'overall_eligibility' AS check_name,
    c.id,
    CASE 
        WHEN c.status <> 'active' THEN 'FAILED: status not active'
        WHEN c.lifecycle_status <> 'active' THEN 'FAILED: lifecycle_status not active'
        WHEN COALESCE(c.manual_disabled, FALSE) = TRUE THEN 'FAILED: manual_disabled'
        WHEN COALESCE(p.manual_disabled, FALSE) = TRUE THEN 'FAILED: provider manual_disabled'
        WHEN COALESCE(p.enabled, FALSE) = FALSE THEN 'FAILED: provider not enabled'
        WHEN COALESCE(c.quota_state, 'ok') IN ('permanently_exhausted', 'balance_exhausted') 
            THEN 'FAILED: quota_state exhausted'
        WHEN c.availability_state = 'suspended' THEN 'FAILED: availability_state suspended'
        WHEN c.availability_recover_at IS NOT NULL AND c.availability_recover_at > now() 
            THEN 'FAILED: availability_recover_at in future'
        WHEN COALESCE(c.default_probe_model, '') = '' THEN 'FAILED: no default_probe_model'
        ELSE 'PASSES all cycleAll WHERE conditions'
    END AS result
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE c.id = 56;

-- ========== 第五步：检查绑定面状态（辅助信息）==========
SELECT 
    cmb.provider_model_id,
    pm.raw_model_name,
    cmb.available,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.unavailable_recover_at
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.credential_id = 56
ORDER BY cmb.unavailable_at DESC NULLS LAST
LIMIT 10;
