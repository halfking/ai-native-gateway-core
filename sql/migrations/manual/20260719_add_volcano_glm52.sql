-- ============================================================================
-- Migration: 添加火山引擎 glm-5.2 模型配置
-- Date: 2026-07-19
-- Author: gateway maintainers
-- Purpose: 火山引擎普通版官网已配置 glm-5.2，但网关未拉取到该模型数据
-- ============================================================================

\echo '=== 检查火山引擎提供商配置 ==='

-- 1. 查询火山引擎提供商是否存在
SELECT 
    id, 
    provider_code, 
    provider_name, 
    base_url, 
    protocol,
    enabled,
    manual_disabled
FROM providers 
WHERE provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
ORDER BY provider_code;

\echo ''
\echo '=== 检查火山引擎现有模型配置 ==='

-- 2. 查询火山引擎已有的模型
SELECT 
    p.provider_code,
    pm.id,
    pm.raw_model_name, 
    pm.canonical_name,
    pm.outbound_model_name,
    pm.context_window
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
ORDER BY p.provider_code, pm.raw_model_name;

\echo ''
\echo '=== 添加 glm-5.2 模型（如不存在） ==='

-- 3. 添加 glm-5.2 模型配置
-- 注意：火山引擎后台显示的是 GLM-5.2（大写），需要确认实际API接受的格式
-- 先尝试添加小写版本 glm-5.2，如果失败再添加大写版本 GLM-5.2

-- 3.1 添加小写版本 glm-5.2
INSERT INTO provider_models (
    provider_id, 
    raw_model_name, 
    canonical_name, 
    outbound_model_name,
    context_window,
    created_at,
    updated_at
)
SELECT 
    p.id,
    'glm-5.2',                    -- 小写：标准OpenAI格式
    'glm-5.2',                    -- 标准化名称
    'glm-5.2',                    -- 发送给上游的模型名（待确认）
    512000,                       -- 512K 上下文窗口
    now(),
    now()
FROM providers p
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND p.enabled = TRUE
  AND NOT EXISTS (
      SELECT 1 FROM provider_models pm
      WHERE pm.provider_id = p.id 
        AND pm.raw_model_name = 'glm-5.2'
  );

-- 3.2 添加大写版本 GLM-5.2（如果火山引擎API要求大写）
INSERT INTO provider_models (
    provider_id, 
    raw_model_name, 
    canonical_name, 
    outbound_model_name,
    context_window,
    created_at,
    updated_at
)
SELECT 
    p.id,
    'GLM-5.2',                    -- 大写：火山引擎后台显示格式
    'glm-5.2',                    -- 标准化名称（保持小写）
    'GLM-5.2',                    -- 发送给上游的模型名（大写）
    512000,                       -- 512K 上下文窗口
    now(),
    now()
FROM providers p
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND p.enabled = TRUE
  AND NOT EXISTS (
      SELECT 1 FROM provider_models pm
      WHERE pm.provider_id = p.id 
        AND pm.raw_model_name = 'GLM-5.2'
  );

\echo ''
\echo '=== 检查火山引擎凭证 ==='

-- 4. 查询火山引擎的凭证
SELECT 
    c.id as credential_id,
    c.credential_label,
    c.provider_id,
    p.provider_code,
    c.lifecycle_status,
    c.enabled,
    c.billing_mode
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND c.lifecycle_status = 'active'
ORDER BY c.id;

\echo ''
\echo '=== 为火山引擎凭证绑定 glm-5.2 模型 ==='

-- 5. 为现有火山引擎凭证自动绑定 glm-5.2 和 GLM-5.2
INSERT INTO credential_model_bindings (
    credential_id, 
    provider_model_id, 
    available,
    created_at,
    updated_at
)
SELECT 
    c.id,
    pm.id,
    TRUE,
    now(),
    now()
FROM credentials c
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.provider_id = p.id
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND pm.raw_model_name IN ('glm-5.2', 'GLM-5.2')  -- 绑定两个版本
  AND c.lifecycle_status = 'active'
  AND c.enabled = TRUE
  AND NOT EXISTS (
      SELECT 1 FROM credential_model_bindings cmb
      WHERE cmb.credential_id = c.id 
        AND cmb.provider_model_id = pm.id
  )
ON CONFLICT (credential_id, provider_model_id) DO NOTHING;

\echo ''
\echo '=== 验证配置结果 ==='

-- 6. 验证：显示所有火山引擎 glm-5.2 相关的绑定
SELECT 
    c.id as credential_id,
    c.credential_label,
    p.provider_code,
    pm.raw_model_name,
    pm.canonical_name,
    cmb.available,
    cmb.unavailable_reason,
    cmb.created_at as binding_created_at
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND pm.raw_model_name IN ('glm-5.2', 'GLM-5.2')  -- 查询两个版本
ORDER BY c.id, pm.raw_model_name;

\echo ''
\echo '=== 检查路由视图中的可路由状态 ==='

-- 7. 检查是否在路由视图中可见
SELECT 
    credential_id,
    provider_code,
    raw_model_name,
    canonical_name,
    is_routable,
    unavailable_reason
FROM v_routable_credential_models
WHERE provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND (raw_model_name = 'glm-5.2' OR raw_model_name = 'GLM-5.2')  -- 查询两个版本
ORDER BY credential_id, raw_model_name;

\echo ''
\echo '=== 完成 ==='
