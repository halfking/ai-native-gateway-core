-- Rollback for Migration 398: restore the pre-398 model_offers view shape.
--
-- This removes only the view column introduced by 398. The underlying
-- provider_models.canonical_raw_name column belongs to migration 395 and is
-- intentionally retained.

\set ON_ERROR_STOP on

DROP VIEW IF EXISTS public.model_offers CASCADE;

CREATE VIEW public.model_offers AS
 SELECT cmb.id,
    cmb.credential_id,
    pm.canonical_id,
    pm.raw_model_name,
    cmb.success_rate,
    cmb.p95_latency_ms,
    cmb.available,
    pm.last_seen_at,
    cmb.routing_tier,
    cmb.weight,
    cmb.unit_price_in_per_1m,
    cmb.unit_price_out_per_1m,
    cmb.currency,
    pm.outbound_model_name,
    cmb.cache_read_price_per_1m,
    cmb.cache_write_price_per_1m,
    pm.standardized_name,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.billing_mode,
    cmb.pricing_source,
    cmb.pricing_updated_at,
    cmb.manual_priority,
    cmb.active_sessions,
    cmb.consecutive_failures,
    cmb.admin_protected
   FROM public.credential_model_bindings cmb
   JOIN public.provider_models pm ON pm.id = cmb.provider_model_id;

COMMENT ON VIEW public.model_offers IS
'Unified view of routable model offers restored to the pre-398 column shape.';
