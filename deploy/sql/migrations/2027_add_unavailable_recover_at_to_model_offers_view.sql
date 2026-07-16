-- Migration: Add unavailable_recover_at to model_offers view
-- Date: 2026-07-16
-- Issue: RestoreOnSuccess fails with "column unavailable_recover_at does not exist"
-- Root cause: model_offers view missing cmb.unavailable_recover_at column

-- Drop and recreate view with unavailable_recover_at column
DROP VIEW IF EXISTS model_offers;

CREATE VIEW model_offers AS
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
    cmb.unavailable_recover_at,  -- Added: allows RestoreOnSuccess to clear recovery timestamp
    cmb.billing_mode,
    cmb.pricing_source,
    cmb.pricing_updated_at,
    cmb.manual_priority,
    cmb.active_sessions,
    cmb.consecutive_failures,
    cmb.admin_protected
   FROM credential_model_bindings cmb
     JOIN provider_models pm ON pm.id = cmb.provider_model_id;
