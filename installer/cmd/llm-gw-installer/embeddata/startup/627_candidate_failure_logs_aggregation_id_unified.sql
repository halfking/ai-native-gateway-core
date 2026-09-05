-- Migration 627: carry aggregation_id across the hot-to-partition promote.
-- audit-data-closure-A (2026-08-31): the ProviderErrorAggregator watermark is
-- MAX(aggregation_id) seen so far. Migration 624 promoted rows from
-- candidate_failure_logs_hot to candidate_failure_logs (columnar monthly
-- partitions) WITHOUT projecting aggregation_id. Once a row is in the columnar
-- partition it is no longer visible to the aggregator's
-- `WHERE aggregation_id > watermark` predicate because the column is missing.
-- The watermark stays correct, but the aggregator never replays the historical
-- partition, so any bucket that was promoted *between* two aggregator ticks
-- before this migration is permanently missing from provider_error_details.
--
-- Fix: add aggregation_id to the parent partitioned table
-- (candidate_failure_logs), synthesize it for historical rows in the unified
-- view (columnar partitions are append-only — UPDATE is not supported, so we
-- cannot backfill via nextval like migration 622 did on the heap hot table),
-- seed the aggregator watermark to bigint-min so the first post-deploy tick
-- replays every historical bucket, and create a `candidate_failure_logs_unified`
-- view that the aggregator reads from (mirroring migration 625's
-- session_bodies_unified pattern).
--
-- The synthesized historical aggregation_id is `(-c.id)` for any historical
-- row whose aggregation_id is NULL. Negation guarantees the synthesized value
-- is always strictly less than any real aggregation_id from
-- candidate_failure_logs_hot_aggregation_id_seq (which only emits positive
-- values). c.id is the BIGSERIAL assigned at hot-insert time and remains
-- stable for the row's lifetime, so the synthesized value is deterministic and
-- monotonic with respect to insertion order — exactly the property the
-- aggregator watermark requires. After the first post-deploy tick, the
-- watermark advances to MAX(aggregation_id) across hot + historical, which is
-- a positive sequence value; subsequent ticks correctly filter new rows with
-- `aggregation_id > last_source_id`.
--
-- Forward direction (post-627): migration 628's rewritten promote carries the
-- real aggregation_id into the destination insert, so new columnar rows are
-- NOT NULL going forward — the synthesis path is exercised only by rows that
-- were already in columnar partitions at the time this migration ran.
--
-- This migration is non-reversible for correctness: down does not drop the
-- view nor the column because dropping would re-introduce the missing-bucket
-- bug. The watermark seed is a one-time deployment-state change and is NOT
-- reverted by the down migration (operators rolling back must manually
-- re-seed the watermark per the audit handoff).

BEGIN;

ALTER TABLE IF EXISTS public.candidate_failure_logs
    ADD COLUMN IF NOT EXISTS aggregation_id bigint;

-- Defensive: a small number of deployments may already have aggregation_id
-- populated (e.g. a previous failed 627 attempt that got past the ADD COLUMN
-- but failed at the backfill step). Leave those rows alone. The view-side
-- COALESCE only fires for historical NULL rows; the ADD COLUMN above is
-- idempotent and does not touch existing values.

CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_aggregation_id
    ON public.candidate_failure_logs (aggregation_id);

-- candidate_failure_logs_unified spans hot + historical partitions.
-- Mirrors session_bodies_unified (migration 625): same column list on both
-- sides. The aggregation_id projection differs by side:
--   - hot: the real per-row aggregation_id allocated by
--     candidate_failure_logs_hot_aggregation_id_seq (positive, monotonic).
--   - historical: COALESCE(aggregation_id, -id). Columnar partitions are
--     append-only (Citus ColumnarScan) and cannot accept UPDATE, so we cannot
--     backfill a real aggregation_id for pre-628 rows. The synthesized value
--     is always negative (id is BIGSERIAL, never 0 in practice because the
--     sequence was attached to the original table), is stable for the row's
--     lifetime, and never collides with the sequence's positive range.
-- Forward rows (post-628 promote) carry the real aggregation_id and do not
-- exercise the COALESCE path.
CREATE OR REPLACE VIEW public.candidate_failure_logs_unified AS
SELECT
    'hot'::text AS source,
    id, request_id, ts, tenant_id, credential_id, provider_id, raw_model_name,
    attempt_index, error_kind, error_message, upstream_status_code,
    upstream_response_body, upstream_response_preview, latency_ms, retryable,
    context, per_attempt_latency_ms, extracted_upstream_status_code,
    diagnosed_error_kind, session_id, aggregation_id
FROM public.candidate_failure_logs_hot
UNION ALL
SELECT
    'historical'::text AS source,
    id, request_id, ts, tenant_id, credential_id, provider_id, raw_model_name,
    attempt_index, error_kind, error_message, upstream_status_code,
    upstream_response_body, upstream_response_preview, latency_ms, retryable,
    context, per_attempt_latency_ms, extracted_upstream_status_code,
    diagnosed_error_kind, session_id,
    COALESCE(aggregation_id, -id) AS aggregation_id
FROM public.candidate_failure_logs;

-- Make the view invoke the invoker's RLS context (same hardening as 625's
-- session_bodies_unified). The underlying tables already carry RLS policies
-- (tenant_isolation_candidate_failure_logs_hot, tenant_isolation_candidate_failure_logs);
-- setting security_invoker=true ensures the view does not silently bypass them.
-- Use the parenthesized SET (security_invoker = true) syntax for compatibility
-- with PostgreSQL 14/15 (PG 15+ also accepts the shorthand
-- `SET SECURITY_INVOKER = true` but env 154's Citus build rejects the
-- shorthand — match 625/626's option-list form).
ALTER VIEW public.candidate_failure_logs_unified SET (security_invoker = true);

-- Seed the aggregator watermark to bigint-min so the first post-deploy tick
-- replays every historical bucket. The view's COALESCE returns negative values
-- for historical NULL rows; without this seed the watermark (default 0) would
-- filter them all out via `aggregation_id > watermark`. After the first tick,
-- the watermark advances to MAX(aggregation_id) across hot + historical — a
-- strictly positive sequence value — and subsequent ticks correctly process
-- only newly inserted rows.
--
-- The hot sequence's value range (positive bigints, 1..9223372036854775807)
-- and the historical synthesized range (negative bigints, -1..-9223372036854775807)
-- never overlap, so the COALESCE contract is preserved even when a deployment
-- has many years of historical data.
DO $do$
BEGIN
    IF to_regclass('public.provider_error_aggregator_state') IS NOT NULL THEN
        UPDATE public.provider_error_aggregator_state
        SET last_source_id = -9223372036854775807,
            updated_at = NOW()
        WHERE id = 1
          AND last_source_id > -9223372036854775807;
        RAISE NOTICE 'migration 627: seeded provider_error_aggregator_state.last_source_id to bigint-min (one-time historical replay)';
    ELSE
        RAISE NOTICE 'migration 627: provider_error_aggregator_state missing; skipping watermark seed (forward-compatible with fresh installs)';
    END IF;
END
$do$;

DO $do$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'candidate_failure_logs'
          AND column_name = 'aggregation_id'
    ) THEN
        RAISE EXCEPTION 'migration 627 post-condition failed: aggregation_id missing on candidate_failure_logs';
    END IF;
    IF to_regclass('public.candidate_failure_logs_unified') IS NULL THEN
        RAISE EXCEPTION 'migration 627 post-condition failed: candidate_failure_logs_unified view not created';
    END IF;
END
$do$;

COMMIT;
