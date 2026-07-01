-- Migration: 131_credential_plan_type.sql
-- Description: 为 credentials 添加 plan_type 字段，用于标识凭证的计费套餐类型
-- Date: 2026-07-02
-- Purpose: 优化路由过滤，在路由层面排除 token_plan/code_plan 与不支持此类套餐的模型的不兼容组合

-- ============================================================================
-- 1. 添加 plan_type 字段到 credentials 表
-- ============================================================================

ALTER TABLE credentials 
ADD COLUMN IF NOT EXISTS plan_type TEXT DEFAULT 'token' 
CHECK (plan_type IN ('token', 'token_plan', 'code_plan', 'agent_plan', 'monthly'));

COMMENT ON COLUMN credentials.plan_type IS '凭证的计费套餐类型: token(按量付费), token_plan(令牌包), code_plan(代码包), agent_plan(Agent包), monthly(月付)';

-- ============================================================================
-- 2. 更新现有数据：根据 active_plan_id 推断 plan_type
-- ============================================================================

-- 如果 active_plan_id 不为 NULL，说明是套餐类型
-- 默认设置为 token_plan (可以手动调整为 code_plan 或其他)
UPDATE credentials 
SET plan_type = 'token_plan'
WHERE active_plan_id IS NOT NULL 
  AND plan_type = 'token';

-- ============================================================================
-- 3. 创建索引
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_credentials_plan_type 
ON credentials(plan_type);

-- ============================================================================
-- 4. 重建视图: v_routable_credential_models
-- ============================================================================

DROP VIEW IF EXISTS v_routable_credential_models CASCADE;

CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT
    cmb.id AS binding_id,
    cmb.credential_id,
    pm.raw_model_name,
    cmb.available AS binding_available,
    c.status AS credential_status,
    c.lifecycle_status AS credential_lifecycle_status,
    c.availability_state,
    c.availability_recover_at,
    c.quota_state,
    c.quota_recover_at,
    c.plan_type,
    mo.billing_mode,
    
    -- 核心逻辑: 判断是否可路由
    CASE
        -- 基础条件检查
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN false
        WHEN c.lifecycle_status != 'active' THEN false
        WHEN cmb.available IS NOT true THEN false
        
        -- quota_state 检查 (排除 periodic_exhausted)
        WHEN c.quota_state = 'periodic_exhausted' THEN false
        WHEN c.quota_state = 'exhausted' AND (c.quota_recover_at IS NULL OR c.quota_recover_at > NOW()) THEN false
        
        -- availability_state 检查
        WHEN c.availability_state = 'unavailable' AND (c.availability_recover_at IS NULL OR c.availability_recover_at > NOW()) THEN false
        
        -- 新增: plan_type 与 billing_mode 兼容性检查
        -- 如果 credential 是 token_plan/code_plan，但 model_offer 要求 token/per_token，则不兼容
        WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan') 
             AND mo.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false
        
        -- 反过来：如果 model_offer 要求 token_plan，但 credential 不是 token_plan，也不兼容
        WHEN mo.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
             AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false
        
        ELSE true
    END AS is_routable,
    
    -- 不可路由原因
    CASE
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status != 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN cmb.available IS NOT true THEN 'binding_unavailable'
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.availability_state = 'unavailable' THEN 'availability_unavailable'
        
        -- 新增: 套餐不兼容原因
        WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan') 
             AND mo.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan') 
             THEN 'plan_incompatible_model_requires_' || COALESCE(mo.billing_mode, 'token')
        WHEN mo.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
             AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan')
             THEN 'plan_incompatible_credential_not_' || mo.billing_mode
        
        ELSE NULL
    END AS unavailable_reason

FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN model_offers mo ON mo.credential_id = cmb.credential_id 
    AND mo.raw_model_name = pm.raw_model_name
WHERE c.tenant_id = 'default';

COMMENT ON VIEW v_routable_credential_models IS '可路由的凭证-模型绑定视图（包含 plan_type 兼容性检查）';

-- ============================================================================
-- 验证查询示例
-- ============================================================================

-- 示例查询: 查看 doubao-1-5-pro-32k-250115 的路由过滤结果
-- SELECT credential_id, plan_type, billing_mode, is_routable, unavailable_reason
-- FROM v_routable_credential_models
-- WHERE raw_model_name = 'doubao-1-5-pro-32k-250115';
