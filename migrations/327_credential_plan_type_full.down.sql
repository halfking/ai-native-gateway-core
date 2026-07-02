-- Migration 136 down: 回滚 v_routable_credential_models 到 131+326 综合版本
-- 注意: 不回滚 cmb.plan_type_origin 列和 credentials.plan_type CHECK 约束（这是无损 DDL）

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

    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'::text
        WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'::text
        WHEN c.status <> 'active'::text THEN ('credential_status_' || c.status)::text
        WHEN c.lifecycle_status <> 'active'::text THEN ('lifecycle_' || c.lifecycle_status)::text
        WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'::text
        WHEN c.availability_state = 'cooling'::text THEN 'availability_cooling'::text
        WHEN c.availability_state = 'rate_limited'::text THEN 'availability_rate_limited'::text
        WHEN c.availability_state = 'auth_failed'::text THEN 'availability_auth_failed'::text
        WHEN c.availability_state = 'unreachable'::text THEN 'availability_unreachable'::text
        WHEN c.availability_state = 'suspended'::text THEN 'availability_suspended'::text
        WHEN c.quota_state IN ('permanently_exhausted', 'balance_exhausted') THEN ('quota_' || c.quota_state)::text
        WHEN c.health_status = 'unreachable'::text AND c.health_checked_at > (now() - interval '1 hour') THEN 'recent_probe_unreachable'::text
        WHEN NOT pm.available THEN 'model_unavailable'::text
        WHEN cmb.unavailable_reason = 'manual'::text THEN 'model_manual_disabled'::text
        WHEN NOT cmb.available THEN 'binding_unavailable'::text
        ELSE NULL::text
    END AS unavailable_reason,

    (
        p.enabled
        AND COALESCE(p.manual_disabled, false) = false
        AND c.status = 'active'::text
        AND c.lifecycle_status = 'active'::text
        AND COALESCE(c.manual_disabled, false) = false
        AND c.availability_state = 'ready'::text
        AND c.quota_state <> ALL (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])
        AND pm.available = true
        AND cmb.available = true
        AND cmb.unavailable_reason IS DISTINCT FROM 'manual'::text
        AND COALESCE(c.health_status, 'unknown'::text) IN ('healthy', 'unknown')
    ) AS is_routable
FROM
    public.credential_model_bindings cmb
    JOIN public.credentials c ON c.id = cmb.credential_id
    JOIN public.providers p ON p.id = c.provider_id
    JOIN public.provider_models pm ON pm.id = cmb.provider_model_id;

GRANT SELECT ON v_routable_credential_models TO PUBLIC;
