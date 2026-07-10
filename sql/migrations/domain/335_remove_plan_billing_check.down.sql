-- Migration 335 (down): 恢复 plan_type vs billing_mode 校验（不推荐）
-- 
-- Warning: 这个 down migration 会恢复错误的业务逻辑。
--          只在需要完全回滚到 migration 334 之前的状态时使用。

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

    CASE
        WHEN NOT p.enabled THEN false
        WHEN COALESCE(p.manual_disabled, false) THEN false
        WHEN COALESCE(c.manual_disabled, false) THEN false
        WHEN NOT pm.available THEN false
        WHEN c.status <> ALL (ARRAY['active', 'cooling', 'degraded']) THEN false
        WHEN c.lifecycle_status <> 'active' THEN false
        WHEN cmb.available IS NOT TRUE THEN false
        WHEN c.quota_state = 'periodic_exhausted' THEN false
        WHEN c.quota_state = 'exhausted' AND (c.quota_recover_at IS NULL OR c.quota_recover_at > NOW()) THEN false
        WHEN c.availability_state = 'unavailable' AND (c.availability_recover_at IS NULL OR c.availability_recover_at > NOW()) THEN false
        -- 恢复错误的校验
        WHEN (c.plan_type = ANY (ARRAY['token_plan', 'code_plan', 'agent_plan']))
             AND (cmb.billing_mode <> ALL (ARRAY['token_plan', 'code_plan', 'agent_plan'])) THEN false
        WHEN (cmb.billing_mode = ANY (ARRAY['token_plan', 'code_plan', 'agent_plan']))
             AND (c.plan_type <> ALL (ARRAY['token_plan', 'code_plan', 'agent_plan'])) THEN false
        ELSE true
    END AS is_routable,

    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'
        WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'
        WHEN NOT pm.available THEN 'model_unavailable'
        WHEN c.status <> ALL (ARRAY['active', 'cooling', 'degraded']) THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status <> 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN cmb.available IS NOT TRUE THEN 'binding_unavailable'
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.availability_state = 'unavailable' THEN 'availability_unavailable'
        WHEN (c.plan_type = ANY (ARRAY['token_plan', 'code_plan', 'agent_plan']))
             AND (cmb.billing_mode <> ALL (ARRAY['token_plan', 'code_plan', 'agent_plan']))
             THEN 'plan_incompatible_cmb_requires_' || COALESCE(cmb.billing_mode, 'per_token')
        WHEN (cmb.billing_mode = ANY (ARRAY['token_plan', 'code_plan', 'agent_plan']))
             AND (c.plan_type <> ALL (ARRAY['token_plan', 'code_plan', 'agent_plan']))
             THEN 'plan_incompatible_credential_not_' || cmb.billing_mode
        ELSE NULL
    END AS unavailable_reason

FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id;

GRANT SELECT ON v_routable_credential_models TO PUBLIC;

COMMIT;
