-- Migration 335: 移除视图中错误的 plan_type vs billing_mode 校验
-- Date: 2026-07-10
-- Author: System Fix
--
-- Problem:
--   Migration 327/328/332 在 v_routable_credential_models 视图中添加了
--   plan_type vs billing_mode 的强制一致性校验，导致：
--     1. claude-fable-5 等模型被标记为 plan_incompatible（credential.plan_type=token_plan
--        但 cmb.billing_mode=per_token）
--     2. 业务逻辑错误：billing_mode 是我们从供应商**采购**的计费方式，
--        plan_type 是我们向客户**销售**的计费方式，两者本来就应该可以独立！
--
-- Root Cause:
--   Migration 327 试图让 plan_type 成为 SSOT 并派生 billing_mode，这违背了
--   采购-销售分离的商业逻辑。我们可以从供应商那里按量采购（per_token），
--   但以套餐方式（token_plan）卖给客户，这是完全合理的商业模式。
--
-- Fix:
--   重建 v_routable_credential_models 视图，基于 migration 332 的完整定义，
--   仅移除 line 57-61 的 plan_type vs billing_mode 校验逻辑（is_routable CASE）
--   和 line 84-89 的对应 unavailable_reason。
--
-- Impact:
--   - claude-fable-5, mimo-v2.5-pro 等模型恢复可路由
--   - 不再限制采购模式和销售模式的组合
--   - 保留所有其他路由安全检查

BEGIN;

DROP VIEW IF EXISTS v_routable_credential_models CASCADE;

CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT
    cmb.id AS binding_id,
    cmb.credential_id,
    cmb.provider_model_id,
    c.tenant_id,
    p.id AS provider_id,
    c.label AS credential_label,
    pm.raw_model_name,
    pm.canonical_id,
    cmb.billing_mode,
    c.plan_type,
    cmb.plan_type_origin,

    -- 核心逻辑: 判断是否可路由
    CASE
        -- Provider 级别检查
        WHEN NOT p.enabled THEN false
        WHEN COALESCE(p.manual_disabled, false) THEN false

        -- Credential 级别 manual_disabled 检查
        WHEN COALESCE(c.manual_disabled, false) THEN false

        -- Provider model 级别检查
        WHEN NOT pm.available THEN false

        -- Credential 基础条件检查
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN false
        WHEN c.lifecycle_status != 'active' THEN false
        WHEN cmb.available IS NOT true THEN false

        -- quota_state 检查 (排除 periodic_exhausted, 考虑恢复时间)
        WHEN c.quota_state = 'periodic_exhausted' THEN false
        WHEN c.quota_state = 'exhausted'
             AND (c.quota_recover_at IS NULL OR c.quota_recover_at > NOW()) THEN false

        -- availability_state 检查 (考虑恢复时间)
        WHEN c.availability_state = 'unavailable'
             AND (c.availability_recover_at IS NULL OR c.availability_recover_at > NOW()) THEN false

        -- *** 移除了错误的套餐兼容性检查 (line 57-61 in migration 332) ***
        -- 原始逻辑（已删除）：
        -- WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
        --      AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false
        -- WHEN cmb.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
        --      AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false

        ELSE true
    END AS is_routable,

    -- 不可路由原因
    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'

        -- Credential 级别 manual_disabled
        WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'

        WHEN NOT pm.available THEN 'model_unavailable'

        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status != 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN cmb.available IS NOT true THEN 'binding_unavailable'
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.availability_state = 'unavailable' THEN 'availability_unavailable'

        -- *** 移除了套餐不兼容原因 (line 84-89 in migration 332) ***
        -- 原始逻辑（已删除）：
        -- WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
        --      AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan')
        --      THEN 'plan_incompatible_cmb_requires_' || COALESCE(cmb.billing_mode, 'per_token')
        -- WHEN cmb.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
        --      AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan')
        --      THEN 'plan_incompatible_credential_not_' || cmb.billing_mode

        ELSE NULL
    END AS unavailable_reason
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id;

COMMENT ON VIEW v_routable_credential_models IS
  '可路由的凭证-模型绑定视图 (billing_mode 和 plan_type 独立，不强制一致)';

GRANT SELECT ON v_routable_credential_models TO PUBLIC;

-- 触发自动路由刷新
SELECT pg_notify('auto_route_refresh', 'manual:335_remove_plan_billing_check');

COMMIT;
