-- Migration 043: client_model trigram index for ?model= ILIKE filter
--
-- Background (2026-06-24 baseline, see
--   docs/llm-gateway-go/perf/2026-06-24-request-logs-baseline.md):
-- EXPLAIN on `WHERE rl.client_model ILIKE '%gpt%'` showed a Seq Scan on
-- request_logs_2026_06. The /api/logs `?model=` filter expands to a
-- 3-way OR EXISTS all using ILIKE '%..%' (admin/logs.go:332-352) and the
-- `OR rl.client_model ILIKE $N` arm is the only one without any index
-- today (search_text already has a per-partition GIN trgm; canonical_name
-- joins via models_canonical).
--
-- Current scale is tiny (24h = 3,249 rows), so the Seq Scan takes ~3 ms
-- and this index is NOT urgent. It is added as forward-looking insurance
-- so that once 24h volume grows past ~100k rows the ?model= filter keeps
-- using a GIN trgm index instead of degrading to a full scan.
--
-- Partitioning constraints (why this looks the way it does):
--   * request_logs is a PostgreSQL native PARTITION BY RANGE (ts) table
--     (NOT a TimescaleDB hypertable) with monthly partitions named
--     request_logs_YYYY_MM. See deploy/sql/01-schema.sql:1626.
--   * CREATE INDEX CONCURRENTLY is NOT allowed inside a transaction /
--     DO block. The DO block below therefore uses plain CREATE INDEX
--     (non-concurrent). At the current partition sizes (<1 s per
--     partition) this is acceptable. If a future partition is large,
--     create the per-partition index manually with CONCURRENTLY first
--     and then re-run this migration (the IF NOT EXISTS guards make it
--     idempotent) to pick up the ATTACH step only.
--   * A partitioned parent index has no storage of its own; each child
--     partition index must be ATTACHed to it so that planner treats them
--     as one logical index.
--
-- Idempotent: safe to re-run. Roll forward only; no data changes.

-- 1. Parent (partitioned) index shell — no storage, just the catalog entry.
CREATE INDEX IF NOT EXISTS idx_request_logs_client_model_trgm
    ON ONLY public.request_logs USING gin (client_model gin_trgm_ops);

-- 2. Per-partition GIN index + ATTACH to the parent for every existing
--    partition (current month + already-created future months + default).
DO $$
DECLARE
    part text;
    child_idx text;
BEGIN
    FOR part IN
        SELECT inhrelid::regclass::text
        FROM pg_inherits
        WHERE inhparent = 'request_logs'::regclass
        ORDER BY 1
    LOOP
        -- Per-partition index name mirrors the existing trgm pattern:
        -- idx_request_logs_search_trgm_YYYY_MM  ->  idx_<part>_client_model_trgm
        child_idx := 'idx_' || part || '_client_model_trgm';
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS %I ON %I USING gin (client_model gin_trgm_ops)',
            child_idx, part
        );
        -- ATTACH is idempotent on PG13+: re-attaching an already-attached
        -- child index raises 'already attached', so guard with a check.
        IF NOT EXISTS (
            SELECT 1 FROM pg_inherits
            WHERE inhparent = 'idx_request_logs_client_model_trgm'::regclass
              AND inhrelid = (child_idx)::regclass
        ) THEN
            EXECUTE format(
                'ALTER INDEX public.idx_request_logs_client_model_trgm ATTACH PARTITION %I',
                child_idx
            );
        END IF;
    END LOOP;
END$$;

-- 3. Teach the partition-creation function to also build this index on
--    every NEW monthly partition, so the index stays complete as time
--    advances. Mirrors the existing idx_%s_search_trgm line.
CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamptz DEFAULT now())
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    month_start   date := date_trunc('month', target_ts)::date;
    month_end     date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name     text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-06-24 (migration 043): GIN trgm on client_model so the
        -- /api/logs ?model= ILIKE filter can use a bitmap index scan
        -- instead of a partition Seq Scan once volume grows.
        EXECUTE format(
            'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;
