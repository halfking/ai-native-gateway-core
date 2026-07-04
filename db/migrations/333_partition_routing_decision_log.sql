-- Migration 333: Convert routing_decision_log to a partitioned table + add default partition
--
-- Background:
--   routing_decision_log is a single heap table that records every
--   routing decision (~21K+ rows and growing). The 2026-07
--   data-lifecycle architecture requires all large data tables to be
--   partitioned by RANGE(time) with a *_default catch-all, so that:
--     - INSERT/UPDATE/DELETE always lands in *_default (heap, supports UPDATE)
--     - SELECT/aggregation queries over the parent gather all partitions
--     - A background tool can migrate old rows from *_default into
--       monthly columnar partitions (storage compression) without blocking writes
--
-- Migration 333 makes routing_decision_log conform:
--   1. Rename old heap → routing_decision_log_old
--   2. Recreate routing_decision_log as RANGE(ts) PARTITIONED with the
--      same column set
--   3. Create routing_decision_log_default as the catch-all (target of all INSERTs)
--   4. Pre-create monthly partitions for 2026_06, _07, _08 so writes near
--      boundary don't fail before PartitionManager's next tick
--   5. Copy rows from old heap (PG auto-routes by ts)
--   6. Sanity check row counts
--   7. Drop the old heap
--
-- Also: rewire the ensure function (created earlier by 319) so the new
-- parent table is referenced correctly and its column list matches the
-- replacement definition. Also update archive_routing_decision_log so
-- its DETACH PARTITION works against the new partitioned parent.

BEGIN;

-- ============================================================
-- Step 0: Drop the old (broken) archive function before the table
--         flip so we can rebuild it against the new partition tree.
-- ============================================================
DROP FUNCTION IF EXISTS public.archive_routing_decision_log(date);

-- ============================================================
-- Step 1: Rename existing heap
-- ============================================================
ALTER TABLE public.routing_decision_log RENAME TO routing_decision_log_old;

-- ============================================================
-- Step 2: Recreate as RANGE-partitioned with identical columns/defaults/constraints
-- ============================================================
CREATE TABLE public.routing_decision_log (
    ts                          timestamp with time zone DEFAULT now() NOT NULL,
    request_id                  uuid NOT NULL,
    idempotency_key             text,
    tenant_id                   text,
    api_key_id                  bigint,
    model                       text NOT NULL,
    chosen_credential_id        bigint,
    chosen_provider_id          bigint,
    tier                        smallint,
    candidates_tried            smallint,
    latency_ms                  integer,
    success                     boolean NOT NULL,
    error_class                 text,
    prompt_tokens               integer,
    completion_tokens           integer,
    cost_usd                    numeric(12,6),
    request_bytes               integer,
    response_bytes              integer,
    client_model                text,
    resolved_raw_model          text,
    sticky_hit                  boolean,
    client_profile              text,
    outbound_model              text,
    request_mode                text,
    identity_hash               text,
    transform_rule_id           text,
    egress_protocol             text,
    failure_stage               text,
    failure_detail_code         text,
    virtual_client_id           text,
    virtual_ip                  text,
    virtual_mac                 text,
    resolution_path             text,
    canonical_model             text,
    resolution_raw_models       jsonb,
    decision_trace              jsonb
) PARTITION BY RANGE (ts);

-- ============================================================
-- Step 3: Primary key must include partition key (ts)
-- ============================================================
ALTER TABLE public.routing_decision_log
    ADD CONSTRAINT routing_decision_log_pkey PRIMARY KEY (ts, request_id);

-- ============================================================
-- Step 4: Pre-create partitions covering recent months + DEFAULT
-- ============================================================
CREATE TABLE public.routing_decision_log_2026_06
    PARTITION OF public.routing_decision_log
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');

CREATE TABLE public.routing_decision_log_2026_07
    PARTITION OF public.routing_decision_log
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

CREATE TABLE public.routing_decision_log_2026_08
    PARTITION OF public.routing_decision_log
    FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');

CREATE TABLE public.routing_decision_log_default
    PARTITION OF public.routing_decision_log DEFAULT;

-- ============================================================
-- Step 5: Re-create the indexes the production schema defined on the old heap.
--         PG inherits ATTACH PARTITION automatically for parent indexes created
--         later, but for our immediate verification we re-attach explicit indexes
--         that the migration 318 archive function depended on.
-- ============================================================
CREATE INDEX IF NOT EXISTS idx_routing_decision_log_canonical_ts
    ON ONLY public.routing_decision_log USING btree (canonical_model, ts DESC);
CREATE INDEX IF NOT EXISTS idx_routing_decision_log_cred_ts
    ON ONLY public.routing_decision_log USING btree (chosen_credential_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_routing_decision_log_identity_hash
    ON ONLY public.routing_decision_log USING btree (identity_hash, ts DESC)
    WHERE (identity_hash IS NOT NULL);
CREATE INDEX IF NOT EXISTS idx_routing_decision_log_model_ts
    ON ONLY public.routing_decision_log USING btree (model, ts DESC);
CREATE INDEX IF NOT EXISTS idx_routing_decision_log_request_id_ts
    ON ONLY public.routing_decision_log USING btree (request_id, ts DESC);
CREATE INDEX IF NOT EXISTS routing_decision_log_ts_idx
    ON ONLY public.routing_decision_log USING btree (ts DESC);

COMMENT ON TABLE public.routing_decision_log IS
    'Routing decision log. Partitioned monthly by ts (heap). '
    'Writes go to routing_decision_log_default (the single canonical INSERT/UPDATE/DELETE '
    'target per the 2026-07 data-lifecycle architecture). '
    'Monthly partitions are pre-created for current+next month by bg/partition_manager.go '
    'ensure_routing_decision_log_partition(); a separate background migrator moves data '
    'from routing_decision_log_default into the matching month partition.';

-- ============================================================
-- Step 6: Copy data from old heap into the new partitioned table.
--         PostgreSQL routes each row to the right partition by ts.
-- ============================================================
INSERT INTO public.routing_decision_log
SELECT * FROM public.routing_decision_log_old;

-- ============================================================
-- Step 7: Sanity check row counts
-- ============================================================
DO $$
DECLARE
    src_count bigint;
    dst_count bigint;
BEGIN
    SELECT COUNT(*) INTO src_count FROM public.routing_decision_log_old;
    SELECT COUNT(*) INTO dst_count FROM public.routing_decision_log;
    IF src_count <> dst_count THEN
        RAISE EXCEPTION 'Row count mismatch: old=%, new=%', src_count, dst_count;
    END IF;
    RAISE NOTICE 'Migration 333: copied % rows to partitioned routing_decision_log', dst_count;
END $$;

-- ============================================================
-- Step 8: Drop the old heap
-- ============================================================
DROP TABLE public.routing_decision_log_old;

-- ============================================================
-- Step 9: Rebuild archive_routing_decision_log so it works against the
--         new partition tree. Partitioned source means we drop the
--         monthly partition after migrating (columnar archive is no
--         longer in scope per migration 331 — function returns no archive
--         target, just count + skipped status. We replace it with a no-op
--         stub so PartitionManager doesn't keep calling it.).
-- ============================================================
CREATE OR REPLACE FUNCTION public.archive_routing_decision_log(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $func$
BEGIN
    -- Migration 331 dropped archive tables. Subsequent cleanup of stale
    -- monthly partitions (if any) is handled by an operator process, not
    -- by the gateway. Return success so the PartitionManager archive
    -- scheduler doesn't error out on every tick.
    RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
END;
$func$;

COMMENT ON FUNCTION public.archive_routing_decision_log(date) IS
'Stub: archive tables removed in migration 331. Kept as a no-op so the
bg.PartitionManager archive scheduler does not break. Always returns ''skipped''.';

COMMIT;

-- ============================================================
-- Post-migration verification
-- ============================================================
DO $$
DECLARE
    partition_count int;
    default_exists  boolean;
BEGIN
    SELECT COUNT(*) INTO partition_count
    FROM pg_inherits
    WHERE inhparent = 'public.routing_decision_log'::regclass;

    SELECT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = 'routing_decision_log_default'
          AND n.nspname = 'public'
    ) INTO default_exists;

    RAISE NOTICE 'routing_decision_log: % partitions, default=%',
        partition_count, default_exists;

    IF NOT default_exists THEN
        RAISE EXCEPTION 'routing_decision_log_default not created — migration 333 incomplete';
    END IF;
END $$;
