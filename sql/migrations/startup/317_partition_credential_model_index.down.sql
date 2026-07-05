-- Migration 317 rollback: revert credential_model_index to a single heap table.
--
-- WARNING: This is a best-effort rollback. After the migration, the partitioned
-- table has accumulated live writes from the rollup worker. Rolling back
-- preserves whatever is currently in the partitioned table, but it does NOT
-- preserve the per-partition granularity of the previous state.
--
-- Use only in an emergency. The expected path is forward-only.

BEGIN;

-- 1) Rename partitioned table aside
ALTER TABLE public.credential_model_index
    RENAME TO credential_model_index_partitioned;

-- 2) Recreate as a regular heap with the same column set
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
    active_sessions       integer,
    concurrency_limit     integer,
    pressure_ratio        numeric(5,4),
    score_smart           numeric(8,4),
    score_speed_first     numeric(8,4),
    score_cost_first      numeric(8,4),
    updated_at            timestamp with time zone
);

CREATE UNIQUE INDEX credential_model_index_bucket_cred_model_key
    ON public.credential_model_index (bucket, credential_id, raw_model);

-- 3) Copy data from the partitioned shell into the heap
INSERT INTO public.credential_model_index
SELECT * FROM public.credential_model_index_partitioned;

-- 4) Drop the partitioned shell
DROP TABLE public.credential_model_index_partitioned CASCADE;

COMMIT;
