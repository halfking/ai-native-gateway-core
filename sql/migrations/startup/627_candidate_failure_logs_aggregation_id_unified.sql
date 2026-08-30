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
-- (candidate_failure_logs), backfill from the sequence for any rows that were
-- promoted under the old definition, and create a `candidate_failure_logs_unified`
-- view that the aggregator reads from (mirroring migration 625's
-- session_bodies_unified pattern).
--
-- This migration is non-reversible for correctness: down does not drop the
-- view nor the column because dropping would re-introduce the missing-bucket
-- bug.

BEGIN;

ALTER TABLE IF EXISTS public.candidate_failure_logs
    ADD COLUMN IF NOT EXISTS aggregation_id bigint;

-- Backfill historical rows that were promoted before this migration. We use
-- a CTE that orders by ts + ctid so the assignment is deterministic and
-- monotonic with respect to insertion time. The sequence is advanced past
-- the watermark so future inserts (none expected, but defensive) cannot
-- collide.
DO $do$
DECLARE
    max_existing bigint;
    next_val bigint;
BEGIN
    SELECT MAX(aggregation_id) INTO max_existing
    FROM public.candidate_failure_logs
    WHERE aggregation_id IS NOT NULL;

    IF max_existing IS NULL THEN
        max_existing := 0;
    END IF;

    -- Only backfill rows that lack an aggregation_id. Lock the table for the
    -- backfill so concurrent aggregator ticks see a consistent state.
    LOCK TABLE public.candidate_failure_logs IN SHARE ROW EXCLUSIVE MODE;

    WITH ordered AS (
        SELECT ctid
        FROM public.candidate_failure_logs
        WHERE aggregation_id IS NULL
        ORDER BY ts ASC, ctid ASC
    ), assigned AS (
        UPDATE public.candidate_failure_logs cfl
        SET aggregation_id = nextval('public.candidate_failure_logs_hot_aggregation_id_seq')
        FROM ordered
        WHERE cfl.ctid = ordered.ctid
        RETURNING cfl.aggregation_id
    )
    SELECT COALESCE(MAX(aggregation_id), 0) INTO next_val FROM assigned;

    IF next_val > max_existing THEN
        -- Already advanced by nextval(); no extra setval needed.
        RAISE NOTICE 'migration 627: backfilled % rows into candidate_failure_logs.aggregation_id', next_val - max_existing;
    END IF;
END
$do$;

CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_aggregation_id
    ON public.candidate_failure_logs (aggregation_id);

-- candidate_failure_logs_unified spans hot + historical partitions.
-- Mirrors session_bodies_unified (migration 625): same column list on both
-- sides, aggregation_id guaranteed non-NULL on every row after the backfill
-- above plus the per-INSERT default already in place on the hot side.
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
    diagnosed_error_kind, session_id, aggregation_id
FROM public.candidate_failure_logs;

-- Make the view invoke the invoker's RLS context (same hardening as 625's
-- session_bodies_unified). The underlying tables already carry RLS policies
-- (tenant_isolation_candidate_failure_logs_hot, tenant_isolation_candidate_failure_logs);
-- setting security_invoker=true ensures the view does not silently bypass them.
ALTER VIEW public.candidate_failure_logs_unified SET SECURITY_INVOKER = true;

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
