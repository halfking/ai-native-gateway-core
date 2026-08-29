-- 2026-08-29: GLM-5.2 频繁降级修复脚本
--
-- 问题描述：
-- sp1 供应商中的 spi-3 credential 的 glm-5.2 模型直连工作正常，但通过网关
-- 频繁被降级。根本原因是降级策略过于激进（80% 失败率、5 样本最小量）对
-- 智谱 API 的 rate_limit 波动不够宽容。
--
-- 修复策略：
-- 1. 为 glm-5.2 binding 设置 admin_protected，防止自动降级
-- 2. 提高 manual_priority，确保在候选池中优先选择
-- 3. 验证上下文窗口配置正确（128K tokens）
-- 4. 清理历史降级状态，恢复可路由性

-- ============================================================================
-- 第一步：识别目标 credential
-- ============================================================================

-- 查看 sp1 供应商中所有 glm-5.2 的 binding 状态
SELECT 
    c.id AS credential_id,
    c.label AS credential_label,
    p.name AS provider_name,
    pm.raw_model_name,
    cmb.available AS binding_available,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.unavailable_recover_at,
    cmb.admin_protected,
    cmb.manual_priority,
    c.availability_state,
    c.quota_state,
    c.health_status
FROM credential_model_bindings cmb
JOIN provider_models pm ON cmb.provider_model_id = pm.id
JOIN credentials c ON cmb.credential_id = c.id
JOIN providers p ON c.provider_id = p.id
WHERE p.name IN ('sp1', '智谱', 'zhipu')  -- 根据实际 provider name 调整
  AND pm.raw_model_name LIKE '%glm-5.2%'
ORDER BY c.id, pm.raw_model_name;

-- ============================================================================
-- 第二步：保护 glm-5.2 binding，防止自动降级
-- ============================================================================

-- 方案 A: 保护特定 credential 的 glm-5.2 (推荐用于 spi-3)
-- 将下面的 123 替换为实际的 credential_id
DO $$
DECLARE
    target_credential_id INT := 123;  -- TODO: 替换为 sp1/spi-3 的实际 credential_id
BEGIN
    UPDATE credential_model_bindings cmb
    SET 
        admin_protected = TRUE,        -- 防止 credentialhealth/checker.go 自动降级
        manual_priority = 100,         -- 提高优先级（默认为 0）
        updated_at = NOW()
    FROM provider_models pm
    WHERE cmb.provider_model_id = pm.id
      AND cmb.credential_id = target_credential_id
      AND pm.raw_model_name = 'glm-5.2';
    
    RAISE NOTICE 'Protected glm-5.2 binding for credential %', target_credential_id;
END $$;

-- 方案 B: 保护所有 sp1 供应商的 glm-5.2 (更广泛)
-- 如果有多个 sp1 credential 都需要保护，使用这个
UPDATE credential_model_bindings cmb
SET 
    admin_protected = TRUE,
    manual_priority = 100,
    updated_at = NOW()
FROM provider_models pm, credentials c, providers p
WHERE cmb.provider_model_id = pm.id
  AND cmb.credential_id = c.id
  AND c.provider_id = p.id
  AND p.name = 'sp1'  -- 或 'zhipu', '智谱'，根据实际情况调整
  AND pm.raw_model_name = 'glm-5.2';

-- ============================================================================
-- 第三步：恢复已降级的 binding
-- ============================================================================

-- 清理历史降级状态，恢复可路由性
-- 注意：仅恢复 continuous_failure 降级，manual 降级保留
UPDATE credential_model_bindings cmb
SET 
    available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL,
    unavailable_recover_at = NULL,
    updated_at = NOW()
FROM provider_models pm, credentials c, providers p
WHERE cmb.provider_model_id = pm.id
  AND cmb.credential_id = c.id
  AND c.provider_id = p.id
  AND p.name = 'sp1'
  AND pm.raw_model_name = 'glm-5.2'
  AND cmb.available = FALSE
  AND cmb.unavailable_reason = 'continuous_failure';

-- 同时清理 node_probe_state 中的失败状态
DELETE FROM node_probe_state nps
USING credentials c, providers p
WHERE nps.credential_id = c.id
  AND c.provider_id = p.id
  AND p.name = 'sp1'
  AND nps.raw_model_name = 'glm-5.2'
  AND nps.last_direct_ok = FALSE;

-- ============================================================================
-- 第四步：验证和优化上下文窗口配置
-- ============================================================================

-- 检查 glm-5.2 的上下文窗口配置
SELECT 
    normalized_name,
    context_window,
    context_window_override,
    COALESCE(context_window_override, context_window) AS effective_window
FROM models_canonical
WHERE normalized_name LIKE '%glm-5.2%';

-- 如果上下文窗口配置不正确，更新为 128K
UPDATE models_canonical
SET 
    context_window_override = 128000,
    updated_at = NOW()
WHERE normalized_name = 'glm-5.2'
  AND (context_window_override IS NULL 
       OR context_window_override < 128000
       OR context_window < 128000);

-- ============================================================================
-- 第五步：调整凭据队列参数（可选，针对高并发场景）
-- ============================================================================

-- 为 glm-5.2 配置合理的队列参数，避免突发流量直接压垮路由
-- 这些参数由 domains/dispatch 的凭据队列调速器消费

-- 查看当前队列配置
SELECT 
    c.id AS credential_id,
    c.label,
    cmb.concurrency_mode,
    cmb.concurrency_limit,
    cmb.rpm_limit,
    cmb.tpm_limit,
    cmb.max_queue_depth,
    cmb.max_queue_wait_ms
FROM credential_model_bindings cmb
JOIN provider_models pm ON cmb.provider_model_id = pm.id
JOIN credentials c ON cmb.credential_id = c.id
JOIN providers p ON c.provider_id = p.id
WHERE p.name = 'sp1'
  AND pm.raw_model_name = 'glm-5.2';

-- 配置示例（根据实际 QPM/RPM 限制调整）
-- 智谱 GLM-5.2 的典型限制：RPM=60, TPM=300K
DO $$
DECLARE
    target_credential_id INT := 123;  -- TODO: 替换为实际 credential_id
BEGIN
    UPDATE credential_model_bindings cmb
    SET 
        concurrency_mode = 'rpm',      -- 使用 RPM 限流模式
        rpm_limit = 60,                -- 每分钟 60 个请求
        max_queue_depth = 100,         -- 队列深度 100 个请求
        max_queue_wait_ms = 5000,      -- 最大等待 5 秒
        updated_at = NOW()
    FROM provider_models pm
    WHERE cmb.provider_model_id = pm.id
      AND cmb.credential_id = target_credential_id
      AND pm.raw_model_name = 'glm-5.2';
    
    RAISE NOTICE 'Updated queue parameters for credential %', target_credential_id;
END $$;

-- ============================================================================
-- 第六步：验证修复效果
-- ============================================================================

-- 检查 glm-5.2 是否在 v_routable_credential_models 中可路由
SELECT 
    binding_id,
    credential_id,
    credential_label,
    raw_model_name,
    is_routable,
    CASE 
        WHEN is_routable THEN 'OK - 可路由'
        ELSE 'BLOCKED - ' || COALESCE(
            CASE
                WHEN NOT is_routable THEN '未通过路由检查'
            END, 
            '未知原因'
        )
    END AS routing_status
FROM v_routable_credential_models
WHERE raw_model_name = 'glm-5.2'
  AND credential_label LIKE '%sp1%'  -- 或 '%spi-3%'
ORDER BY is_routable DESC, credential_id;

-- 检查最近的降级历史（过去 24 小时）
SELECT 
    cmb.credential_id,
    c.label,
    pm.raw_model_name,
    cmb.unavailable_at,
    cmb.unavailable_reason,
    cmb.unavailable_recover_at,
    cmb.available,
    cmb.admin_protected
FROM credential_model_bindings cmb
JOIN provider_models pm ON cmb.provider_model_id = pm.id
JOIN credentials c ON cmb.credential_id = c.id
WHERE pm.raw_model_name = 'glm-5.2'
  AND cmb.unavailable_at > NOW() - INTERVAL '24 hours'
ORDER BY cmb.unavailable_at DESC;

-- ============================================================================
-- 附录：回滚脚本（如果需要撤销更改）
-- ============================================================================

/*
-- 取消 admin_protected 保护
UPDATE credential_model_bindings cmb
SET 
    admin_protected = FALSE,
    manual_priority = 0,
    updated_at = NOW()
FROM provider_models pm, credentials c, providers p
WHERE cmb.provider_model_id = pm.id
  AND cmb.credential_id = c.id
  AND c.provider_id = p.id
  AND p.name = 'sp1'
  AND pm.raw_model_name = 'glm-5.2'
  AND cmb.admin_protected = TRUE;
*/
