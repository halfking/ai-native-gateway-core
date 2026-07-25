--
-- Name: v_routable_credential_models; Type: VIEW; Schema: public; Owner: -
--
-- SSOT reconciled with migration 417_route_excludes_failed_node_probes.sql.
-- The node_probe_state hard gate prevents routing to credentials whose most
-- recent direct probe failed and whose retry window has not yet elapsed.
--

CREATE VIEW public.v_routable_credential_models AS
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
    (
        p.enabled
        AND COALESCE(p.manual_disabled, FALSE) = FALSE
        AND c.status = 'active'
        AND c.lifecycle_status = 'active'
        AND COALESCE(c.manual_disabled, FALSE) = FALSE
        AND c.availability_state = 'ready'
        AND c.quota_state NOT IN ('permanently_exhausted', 'balance_exhausted')
        AND pm.available = TRUE
        AND cmb.available = TRUE
        AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
        AND COALESCE(c.health_status, 'unknown') IN ('healthy', 'unknown')
        AND NOT EXISTS (
            SELECT 1
            FROM node_probe_state nps
            WHERE nps.credential_id = cmb.credential_id
              AND nps.raw_model_name = pm.raw_model_name
              AND nps.last_direct_ok = FALSE
              AND nps.next_retry_at > now()
        )
    ) AS is_routable,
    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, FALSE) THEN 'provider_manual_disabled'
        WHEN c.status <> 'active' THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status <> 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN COALESCE(c.manual_disabled, FALSE) THEN 'credential_manual_disabled'
        WHEN c.availability_state = 'cooling' THEN 'availability_cooling'
        WHEN c.availability_state = 'rate_limited' THEN 'availability_rate_limited'
        WHEN c.availability_state = 'auth_failed' THEN 'availability_auth_failed'
        WHEN c.availability_state = 'unreachable' THEN 'availability_unreachable'
        WHEN c.availability_state = 'suspended' THEN 'availability_suspended'
        WHEN c.quota_state IN ('permanently_exhausted', 'balance_exhausted') THEN 'quota_' || c.quota_state
        WHEN c.health_status = 'unreachable' AND c.health_checked_at > now() - interval '1 hour' THEN 'recent_probe_unreachable'
        WHEN NOT pm.available THEN 'model_unavailable'
        WHEN cmb.unavailable_reason = 'manual' THEN 'model_manual_disabled'
        WHEN NOT cmb.available THEN 'binding_unavailable'
        WHEN EXISTS (
            SELECT 1
            FROM node_probe_state nps
            WHERE nps.credential_id = cmb.credential_id
              AND nps.raw_model_name = pm.raw_model_name
              AND nps.last_direct_ok = FALSE
              AND nps.next_retry_at > now()
        ) THEN 'node_probe_failed'
        ELSE NULL
    END AS unavailable_reason
FROM public.credential_model_bindings cmb
JOIN public.credentials c ON c.id = cmb.credential_id
JOIN public.providers p ON p.id = c.provider_id
JOIN public.provider_models pm ON pm.id = cmb.provider_model_id;

