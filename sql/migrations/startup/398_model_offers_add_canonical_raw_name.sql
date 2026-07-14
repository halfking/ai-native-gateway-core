-- =============================================================================
-- Migration 398: model_offers VIEW add canonical_raw_name
-- Created:     2026-07-14
-- Author:      gateway maintainers
--
-- Background:
--   model_offers is a VIEW (JOIN credential_model_bindings x provider_models).
--   Migration 395 added provider_models.canonical_raw_name but the VIEW's
--   SELECT list was never updated to include it. As a result, Go code that
--   references mo.canonical_raw_name (admin/logs.go, admin/routing.go,
--   admin/credential_monitor.go, admin/credential_success_rate.go,
--   provider/client.go, discovery/alias_sync.go) gets:
--     ERROR:  column "canonical_raw_name" does not exist
--   This is the root cause of the /request-logs HTTP 500 error.
--
-- This migration:
--   1. Snapshot pre-state: confirm the VIEW is still on the old definition.
--   2. DROP + CREATE the model_offers VIEW, adding pm.canonical_raw_name.
--   3. Post-flight: verify the column exists in the new VIEW.
--
-- It is idempotent: DROP VIEW IF EXISTS / CREATE OR REPLACE VIEW would
-- be simpler but we want exact visibility into the definition.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 398 model_offers VIEW add canonical_raw_name ==='

-- ---------------------------------------------------------------------------
-- 1. Snapshot: how many columns does the VIEW currently expose?
-- ---------------------------------------------------------------------------
\echo '--- 1. pre-state: model_offers column count (was 26, after add = 27) ---'
SELECT COUNT(*) AS old_column_count
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'model_offers';

\echo '--- 1. pre-state: explicit column names ---'
SELECT column_name
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'model_offers'
ORDER BY ordinal_position;

-- ---------------------------------------------------------------------------
-- 2. Rebuild the VIEW with canonical_raw_name.
-- ---------------------------------------------------------------------------
\echo '--- 2. rebuilding model_offers VIEW with pm.canonical_raw_name ---'

DROP VIEW IF EXISTS public.model_offers CASCADE;

CREATE VIEW public.model_offers AS
 SELECT cmb.id,
    cmb.credential_id,
    pm.canonical_id,
    pm.raw_model_name,
    pm.canonical_raw_name,
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
   FROM (public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));

COMMENT ON VIEW public.model_offers IS
'Unified view of routable model offers: joins credential_model_bindings
with provider_models. Each row represents one credential-model pairing
that MIGHT be available for routing. The insert/update/delete triggers
on this VIEW are defined in model_offers_insert_trigger(),
model_offers_update_trigger(), and model_offers_delete_trigger().

2026-07-14: Added pm.canonical_raw_name (migration 398) so Go code can
reference mo.canonical_raw_name directly without hitting
"column does not exist".';

-- ---------------------------------------------------------------------------
-- 3. Post-flight: verify the column exists.
-- ---------------------------------------------------------------------------
\echo '--- 3. post-flight: column count after rebuild (expect 27) ---'
SELECT COUNT(*) AS new_column_count
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'model_offers';

\echo '--- 3. post-flight: verify canonical_raw_name is present ---'
SELECT column_name, ordinal_position, data_type
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'model_offers'
  AND column_name = 'canonical_raw_name';

\echo '--- 3. post-flight: quick sanity — SELECT 1 FROM model_offers LIMIT 1 ---'
SELECT 1 AS smoke_test FROM public.model_offers LIMIT 1;

\echo '=== 398 model_offers VIEW add canonical_raw_name: done ==='
