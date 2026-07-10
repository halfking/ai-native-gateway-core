--
-- Name: v_routable_credential_models; Type: VIEW; Schema: public; Owner: -
--
-- 2026-07-09 audit: 更新以匹配 migration 332 的最新定义
-- 新增 billing_mode/plan_type/plan_type_origin 列 + plan_type 兼容性检查

CREATE VIEW public.v_routable_credential_models AS
 SELECT cmb.id AS binding_id,
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
            WHEN (NOT p.enabled) THEN false
            WHEN COALESCE(p.manual_disabled, false) THEN false
            WHEN COALESCE(c.manual_disabled, false) THEN false
            WHEN (NOT pm.available) THEN false
            WHEN (c.status NOT IN ('active'::text, 'cooling'::text, 'degraded'::text)) THEN false
            WHEN (c.lifecycle_status <> 'active'::text) THEN false
            WHEN (cmb.available IS NOT true) THEN false
            WHEN (c.quota_state = 'periodic_exhausted'::text) THEN false
            WHEN (((c.quota_state = 'exhausted'::text) AND (c.quota_recover_at IS NULL)) OR (c.quota_recover_at > now())) THEN false
            WHEN (((c.availability_state = 'unavailable'::text) AND (c.availability_recover_at IS NULL)) OR (c.availability_recover_at > now())) THEN false
            WHEN ((c.plan_type = ANY (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text])) AND (cmb.billing_mode <> ALL (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text]))) THEN false
            WHEN ((cmb.billing_mode = ANY (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text])) AND (c.plan_type <> ALL (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text]))) THEN false
            ELSE true
        END AS is_routable,
        CASE
            WHEN (NOT p.enabled) THEN 'provider_disabled'::text
            WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'::text
            WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'::text
            WHEN (NOT pm.available) THEN 'model_unavailable'::text
            WHEN (c.status NOT IN ('active'::text, 'cooling'::text, 'degraded'::text)) THEN ('credential_status_'::text || c.status)
            WHEN (c.lifecycle_status <> 'active'::text) THEN ('lifecycle_'::text || c.lifecycle_status)
            WHEN (cmb.available IS NOT true) THEN 'binding_unavailable'::text
            WHEN (c.quota_state = 'periodic_exhausted'::text) THEN 'quota_periodic_exhausted'::text
            WHEN (c.quota_state = 'exhausted'::text) THEN 'quota_exhausted'::text
            WHEN (c.availability_state = 'unavailable'::text) THEN 'availability_unavailable'::text
            WHEN ((c.plan_type = ANY (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text])) AND (cmb.billing_mode <> ALL (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text]))) THEN ('plan_incompatible_cmb_requires_'::text || COALESCE(cmb.billing_mode, 'per_token'::text))
            WHEN ((cmb.billing_mode = ANY (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text])) AND (c.plan_type <> ALL (ARRAY['token_plan'::text, 'code_plan'::text, 'agent_plan'::text]))) THEN ('plan_incompatible_credential_not_'::text || cmb.billing_mode)
            ELSE NULL::text
        END AS unavailable_reason,
    (((((cmb.manual_priority * 100))::numeric + (COALESCE(cmb.success_rate, 0.5) * (50)::numeric)) - (COALESCE(cmb.unit_price_in_per_1m, (0)::numeric) * 0.001)) - ((COALESCE(cmb.p95_latency_ms, 1000))::numeric * 0.01)) AS routing_score
   FROM (((public.credential_model_bindings cmb
     JOIN public.credentials c ON ((c.id = cmb.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));

