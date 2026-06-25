-- fix_routing_issue.sql
-- 修复路由问题：创建或修复 v_routable_credential_models 视图
-- 
-- 问题分析：
-- provider/client.go:696 行的查询依赖 v.is_routable = TRUE
-- 如果视图不存在或返回 NULL/FALSE，所有节点都会被过滤掉
--
-- 解决方案：
-- 1. 检查视图是否存在
-- 2. 如果不存在，创建一个正确的视图
-- 3. 视图应该基于 credential_model_bindings 和 model_offers 表

-- 步骤 1: 删除旧视图（如果存在）
DROP VIEW IF EXISTS v_routable_credential_models CASCADE;

-- 步骤 2: 创建新视图
-- 该视图整合了 credential_model_bindings 和 model_offers 的可路由状态
-- is_routable = TRUE 的条件：
--   1. provider 启用 (p.enabled = TRUE)
--   2. provider 未手动禁用 (p.manual_disabled = FALSE 或 NULL)
--   3. credential 状态为 active
--   4. credential 生命周期状态为 active  
--   5. credential 可用性状态为 ready
--   6. credential 配额状态为 ok
--   7. credential 未手动禁用 (c.manual_disabled = FALSE 或 NULL)
--   8. model_offer 可用 (mo.available = TRUE)
--   9. model_offer 未被手动标记为不可用
--   10. credential_model_binding (如果存在) 未被标记为不可用

CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT 
    p.id AS provider_id,
    p.tenant_id,
    c.id AS credential_id,
    mo.raw_model_name,
    -- is_routable: 综合判断该 (credential, model) 组合是否可路由
    (
        p.enabled IS TRUE
        AND COALESCE(p.manual_disabled, FALSE) = FALSE
        AND COALESCE(c.status, 'active') = 'active'
        AND COALESCE(c.lifecycle_status, 'active') = 'active'
        AND COALESCE(c.availability_state, 'ready') = 'ready'
        AND COALESCE(c.quota_state, 'ok') = 'ok'
        AND COALESCE(c.manual_disabled, FALSE) = FALSE
        AND mo.available IS TRUE
        AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
        AND COALESCE(mo.admin_protected, FALSE) = FALSE
        -- 如果存在 credential_model_binding，也要检查其状态
        AND (
            cmb.credential_id IS NULL  -- 没有 binding 记录，视为可用
            OR (
                cmb.available IS TRUE
                AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
                AND COALESCE(cmb.admin_protected, FALSE) = FALSE
            )
        )
    ) AS is_routable,
    -- unavailable_reason: 如果不可路由，说明原因
    CASE 
        WHEN p.enabled IS NOT TRUE THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, FALSE) = TRUE THEN 'provider_manual_disabled'
        WHEN COALESCE(c.status, 'active') != 'active' THEN 'credential_status_' || COALESCE(c.status, 'unknown')
        WHEN COALESCE(c.lifecycle_status, 'active') != 'active' THEN 'lifecycle_' || COALESCE(c.lifecycle_status, 'unknown')
        WHEN COALESCE(c.availability_state, 'ready') != 'ready' THEN 'availability_' || COALESCE(c.availability_state, 'unknown')
        WHEN COALESCE(c.quota_state, 'ok') != 'ok' THEN 'quota_' || COALESCE(c.quota_state, 'unknown')
        WHEN COALESCE(c.manual_disabled, FALSE) = TRUE THEN 'credential_manual_disabled'
        WHEN mo.available IS NOT TRUE THEN 'offer_unavailable'
        WHEN COALESCE(mo.unavailable_reason, '') LIKE 'manual%' THEN mo.unavailable_reason
        WHEN COALESCE(mo.admin_protected, FALSE) = TRUE THEN 'offer_admin_protected'
        WHEN cmb.credential_id IS NOT NULL AND cmb.available IS NOT TRUE THEN 'binding_unavailable'
        WHEN cmb.credential_id IS NOT NULL AND COALESCE(cmb.unavailable_reason, '') LIKE 'manual%' THEN cmb.unavailable_reason
        WHEN cmb.credential_id IS NOT NULL AND COALESCE(cmb.admin_protected, FALSE) = TRUE THEN 'binding_admin_protected'
        ELSE NULL
    END AS unavailable_reason
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN credential_model_bindings cmb ON cmb.credential_id = c.id 
    AND EXISTS (
        SELECT 1 FROM provider_models pm 
        WHERE pm.id = cmb.provider_model_id 
        AND pm.raw_model_name = mo.raw_model_name
    );

-- 步骤 3: 验证视图
-- 检查有多少可路由的节点
SELECT 
    COUNT(*) as total_records,
    COUNT(*) FILTER (WHERE is_routable = TRUE) as routable_count,
    COUNT(*) FILTER (WHERE is_routable = FALSE) as not_routable_count,
    COUNT(DISTINCT credential_id) as unique_credentials,
    COUNT(DISTINCT raw_model_name) as unique_models
FROM v_routable_credential_models
WHERE tenant_id = 'default';

-- 步骤 4: 显示不可路由节点的原因分布
SELECT 
    unavailable_reason,
    COUNT(*) as count
FROM v_routable_credential_models
WHERE tenant_id = 'default' 
AND is_routable = FALSE
GROUP BY unavailable_reason
ORDER BY count DESC
LIMIT 20;

-- 步骤 5: 显示一些可路由的样本数据
SELECT 
    provider_id,
    credential_id,
    raw_model_name,
    is_routable,
    unavailable_reason
FROM v_routable_credential_models
WHERE tenant_id = 'default'
ORDER BY is_routable DESC, provider_id, credential_id
LIMIT 20;
