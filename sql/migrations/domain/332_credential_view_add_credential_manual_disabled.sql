-- Migration 138: v_routable_credential_models 增加 credential 级别 manual_disabled 检查
-- Date: 2026-07-08
-- Purpose: 修复凭据级 manual_disabled 已被 Go 代码检查但视图遗漏的问题
--
-- Background:
--   Go 代码（db/db.go）的 credential 过滤查询已含
--   AND COALESCE(c.manual_disabled, FALSE) = FALSE，但视图 v_routable_credential_models
--   的 is_routable CASE 块只检查了 p.manual_disabled（provider 级），漏了
--   c.manual_disabled（credential 级）。这是 migration 327/328 重写视图时丢失的。
--
-- Changes:
--   1. is_routable CASE 增加 COALESCE(c.manual_disabled, false) THEN false
--   2. unavailable_reason CASE 增加 'credential_manual_disabled' 标签

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

        -- Credential 级别 manual_disabled 检查 (新增)
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

        -- 套餐兼容性检查
        WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
             AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false
        WHEN cmb.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
             AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false

        ELSE true
    END AS is_routable,

    -- 不可路由原因
    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'

        -- Credential 级别 manual_disabled (新增)
        WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'

        WHEN NOT pm.available THEN 'model_unavailable'

        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status != 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN cmb.available IS NOT true THEN 'binding_unavailable'
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.availability_state = 'unavailable' THEN 'availability_unavailable'

        -- 套餐不兼容
        WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
             AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan')
             THEN 'plan_incompatible_cmb_requires_' || COALESCE(cmb.billing_mode, 'per_token')
        WHEN cmb.billing_mode IN ('token_plan', 'code_plan', 'agent_plan')
             AND c.plan_type NOT IN ('token_plan', 'code_plan', 'agent_plan')
             THEN 'plan_incompatible_credential_not_' || cmb.billing_mode

        ELSE NULL
    END AS unavailable_reason
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id;

COMMENT ON VIEW v_routable_credential_models IS
  '可路由的凭证-模型绑定视图 (plan_type SSOT + provider/model/credential 级别过滤)';

GRANT SELECT ON v_routable_credential_models TO PUBLIC;
