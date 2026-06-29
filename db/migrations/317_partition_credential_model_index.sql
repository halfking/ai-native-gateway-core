-- Migration 317: Convert credential_model_index to a RANGE-partitioned table
--
-- Background:
--   credential_model_index is a 5-minute rollup index refreshed by the gateway.
--   As of 2026-06-30, the table holds ~186k rows (~79 MB) in a single heap table.
--   Other large tables (request_logs, routing_decision_log, request_wal) have
--   already been converted to RANGE-partitioned tables. This migration
--   brings credential_model_index to the same shape so that:
--     1) future partitions can be detached and dropped in O(1)
--     2) per-partition statistics and indexes stay small
--     3) cleanup_old_credential_model_index() and archive_credential_model_index()
--        can be unified with other archive functions
--
-- Strategy:
--   - Rename the existing heap table to credential_model_index_old
--   - Recreate credential_model_index as RANGE (bucket) PARTITIONED, with the
--     same column set + unique index (bucket, credential_id, raw_model).
--     bucket is the leading key so the index is partition-compatible.
--   - Pre-create partitions for 2026_06 / 2026_07 / 2026_08 and a DEFAULT catch-all
--   - Copy all rows in a single INSERT...SELECT (PG routes to the right partition)
--   - Verify row counts, then DROP credential_model_index_old
--
-- Risk:
--   - Lock: ALTER TABLE / INSERT take an ACCESS EXCLUSIVE lock briefly during
--     the rename and during the COPY. Concurrent INSERTs from the gateway
--     rollup worker will queue. Expected outage: < 30s.
--   - No FK references, no view/materialized-view dependencies, no RLS policies
--     (verified on 2026-06-30). Safe to swap.
--   - 79 MB heap → partitioned table; the operation is fully in-place metadata
--     change for the new shell + one full-table INSERT.
--
-- Compatibility:
--   - archive_credential_model_index(month) continues to work: it issues
--     INSERT INTO credential_model_index_archive SELECT * FROM
--     credential_model_index WHERE bucket < cutoff_ts. The DELETE on the
--     partitioned table is now per-partition (no partition-wise DELETE in PG15)
--     but is bounded to rows older than 7d, so the cost is acceptable.
--   - cleanup_old_credential_model_index() (if present) is unaffected.

BEGIN;

-- 1) Rename existing heap
ALTER TABLE public.credential_model_index
    RENAME TO credential_model_index_old;

-- 1a) Drop the UNIQUE constraint (and its backing index) on the old heap so
--     we can re-use the same name on the new partitioned table. We keep the
--     underlying data — the uniqueness guarantee is restored when we
--     re-create the index on the new table after the INSERT.
ALTER TABLE public.credential_model_index_old
    DROP CONSTRAINT IF EXISTS credential_model_index_bucket_cred_model_key;

-- 2) Recreate as RANGE-partitioned with identical columns/defaults/constraints
CREATE TABLE public.credential_model_index (
    bucket                timestamp with time zone NOT NULL,
    credential_id         bigint                   NOT NULL,
    raw_model             text                     NOT NULL,
    canonical_id          integer,
    billing_mode          text,
    unit_price_in_per_1m  numeric(10,4),
    unit_price_out_per_1m numeric(10,4),
    context_window        integer,
    success_rate          numeric(5,4),
    p95_latency_ms        integer,
    active_sessions       integer                  DEFAULT 0,
    concurrency_limit     integer,
    pressure_ratio        numeric(5,4),
    score_smart           numeric(8,4),
    score_speed_first     numeric(8,4),
    score_cost_first      numeric(8,4),
    updated_at            timestamp with time zone DEFAULT now()
) PARTITION BY RANGE (bucket);

-- 3) Re-create unique index (bucket is leading → partition-compatible)
--    On a partitioned table a UNIQUE index automatically creates a
--    matching UNIQUE constraint.
CREATE UNIQUE INDEX credential_model_index_bucket_cred_model_key
    ON public.credential_model_index (bucket, credential_id, raw_model);

-- 4) Add table comment to document the data flow
COMMENT ON TABLE public.credential_model_index IS
    '5-min rollup of per-credential health metrics. Monthly partitions (heap). '
    'Data older than 7 days is archived to credential_model_index_archive (columnar) '
    'by archive_credential_model_index() — see migration 317.';

-- 5) Pre-create partitions covering the data we have (2026-06) plus the
--    current and next month so writes from the rollup worker never hit DEFAULT
CREATE TABLE public.credential_model_index_2026_06
    PARTITION OF public.credential_model_index
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');

CREATE TABLE public.credential_model_index_2026_07
    PARTITION OF public.credential_model_index
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

CREATE TABLE public.credential_model_index_2026_08
    PARTITION OF public.credential_model_index
    FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');

CREATE TABLE public.credential_model_index_default
    PARTITION OF public.credential_model_index DEFAULT;

-- 6) Copy data from old heap into the new partitioned table.
--    PostgreSQL routes each row to the right partition by bucket value.
INSERT INTO public.credential_model_index
SELECT * FROM public.credential_model_index_old;

-- 7) Sanity check: row counts must match.
DO $$
DECLARE
    src_count  bigint;
    dst_count  bigint;
BEGIN
    SELECT COUNT(*) INTO src_count FROM public.credential_model_index_old;
    SELECT COUNT(*) INTO dst_count FROM public.credential_model_index;

    IF src_count <> dst_count THEN
        RAISE EXCEPTION
            'Row count mismatch after migration: old=%, new=%', src_count, dst_count;
    END IF;

    RAISE NOTICE 'Migration 317: copied % rows to partitioned credential_model_index', dst_count;
END $$;

-- 8) Drop the old heap once parity is confirmed
DROP TABLE public.credential_model_index_old;

COMMIT;

-- 9) Post-migration verification
DO $$
DECLARE
    partition_count int;
    total_size      text;
BEGIN
    SELECT COUNT(*) INTO partition_count
    FROM pg_inherits
    WHERE inhparent = 'public.credential_model_index'::regclass;

    SELECT pg_size_pretty(pg_total_relation_size('public.credential_model_index')) INTO total_size;

    RAISE NOTICE 'credential_model_index now has % partitions, total size %',
        partition_count, total_size;
END $$;
