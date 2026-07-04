-- Migration 335: Convert tool_usage_stats to a partitioned table + add default partition
--
-- Background:
--   tool_usage_stats is a single heap table aggregating per-tool usage
--   metrics (call counts, latency p50/p95/p99, error rates) keyed by
--   (tool_id, usage_date). It grows with traffic and benefits from the
--   same data-lifecycle architecture as the other large tables:
--   - tool_usage_stats_default holds recent (mutable) rows — heap
--   - monthly partitions (older data) are pre-created for current+next month
--   - INSERT/UPDATE/DELETE in Go code targets tool_usage_stats_default
--   - A background migrator moves old rows from default into monthly partitions
--
-- Conversion strategy:
--   1. Drop RLS on old heap (RLS cannot be attached to a partitioned table
--      directly until all its partitions share the policy) → re-add on new parent
--   2. Rename old heap → tool_usage_stats_old
--   3. Recreate tool_usage_stats as RANGE(usage_date) PARTITIONED
--   4. Pre-create monthly partitions for 2026_06 / _07 / _08 + DEFAULT
--   5. Copy rows (routed by usage_date)
--   6. Drop old heap
--   7. Re-add RLS on the new parent

BEGIN;

-- ============================================================
-- Step 1: Rename old heap
-- ============================================================
ALTER TABLE public.tool_usage_stats RENAME TO tool_usage_stats_old;

-- ============================================================
-- Step 2: Recreate as RANGE-partitioned by usage_date
-- ============================================================
CREATE TABLE public.tool_usage_stats (
    id              bigint NOT NULL,
    tool_id         character varying(128) NOT NULL,
    tenant_id       character varying(64) DEFAULT 'default'::character varying NOT NULL,
    usage_date      date DEFAULT CURRENT_DATE NOT NULL,
    call_count      bigint DEFAULT 0 NOT NULL,
    success_count   bigint DEFAULT 0 NOT NULL,
    error_count     bigint DEFAULT 0 NOT NULL,
    avg_latency_ms  integer DEFAULT 0,
    last_called_at  timestamp with time zone,
    created_at      timestamp with time zone DEFAULT now() NOT NULL,
    updated_at      timestamp with time zone DEFAULT now() NOT NULL
) PARTITION BY RANGE (usage_date);

-- ============================================================
-- Step 3: Recreate primary key (must include partition key)
-- ============================================================
ALTER TABLE public.tool_usage_stats
    ADD CONSTRAINT tool_usage_stats_pkey PRIMARY KEY (id, usage_date);

-- ============================================================
-- Step 4: Re-attach the existing sequence
-- ============================================================
ALTER SEQUENCE public.tool_usage_stats_id_seq OWNED BY public.tool_usage_stats.id;
ALTER TABLE public.tool_usage_stats ALTER COLUMN id
    SET DEFAULT nextval('public.tool_usage_stats_id_seq'::regclass);

-- ============================================================
-- Step 5: Pre-create partitions for current+next month + DEFAULT
-- ============================================================
CREATE TABLE public.tool_usage_stats_2026_06
    PARTITION OF public.tool_usage_stats
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE public.tool_usage_stats_2026_07
    PARTITION OF public.tool_usage_stats
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

CREATE TABLE public.tool_usage_stats_2026_08
    PARTITION OF public.tool_usage_stats
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

CREATE TABLE public.tool_usage_stats_default
    PARTITION OF public.tool_usage_stats DEFAULT;

-- ============================================================
-- Step 6: Re-create index that the production schema defined on the old heap
-- ============================================================
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_tool_date
    ON ONLY public.tool_usage_stats USING btree (tool_id, usage_date DESC);
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_date
    ON ONLY public.tool_usage_stats USING btree (usage_date DESC);

COMMENT ON TABLE public.tool_usage_stats IS
    'Per-tool usage stats (call_count, success/error rates, latency). '
    'Partitioned monthly by usage_date (heap). '
    'Writes go to tool_usage_stats_default (the canonical INSERT/UPDATE/DELETE '
    'target per the 2026-07 data-lifecycle architecture). '
    'Monthly partitions are pre-created for current+next month by bg/partition_manager.go '
    'ensure_tool_usage_stats_partition(); SELECTs over the parent aggregate all partitions.';

-- ============================================================
-- Step 7: Copy data from old heap (PG routes by usage_date)
-- ============================================================
INSERT INTO public.tool_usage_stats
SELECT * FROM public.tool_usage_stats_old;

-- ============================================================
-- Step 8: Sanity check
-- ============================================================
DO $$
DECLARE
    src_count bigint;
    dst_count bigint;
BEGIN
    SELECT COUNT(*) INTO src_count FROM public.tool_usage_stats_old;
    SELECT COUNT(*) INTO dst_count FROM public.tool_usage_stats;
    IF src_count <> dst_count THEN
        RAISE EXCEPTION 'Row count mismatch: old=%, new=%', src_count, dst_count;
    END IF;
    RAISE NOTICE 'Migration 335: copied % rows to partitioned tool_usage_stats', dst_count;
END $$;

-- ============================================================
-- Step 9: Drop old heap
-- ============================================================
DROP TABLE public.tool_usage_stats_old;

-- ============================================================
-- Step 10: Re-enable RLS on the new parent (matches the original policy).
-- ============================================================
ALTER TABLE public.tool_usage_stats ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.tool_usage_stats FORCE ROW LEVEL SECURITY;

-- ============================================================
-- Step 11: ensure_tool_usage_stats_partition (idempotent)
-- ============================================================
CREATE OR REPLACE FUNCTION public.ensure_tool_usage_stats_partition(target_month timestamp with time zone)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    partition_name text;
    start_date date;
    end_date   date;
BEGIN
    start_date := date_trunc('month', target_month)::date;
    end_date   := (start_date + interval '1 month')::date;
    partition_name := 'tool_usage_stats_' || to_char(start_date, 'YYYY_MM');

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE %I PARTITION OF tool_usage_stats FOR VALUES FROM (%L) TO (%L)',
        partition_name, start_date, end_date
    );

    RAISE NOTICE 'ensure_tool_usage_stats_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION public.ensure_tool_usage_stats_partition(timestamp with time zone) IS
'Ensure a monthly tool_usage_stats partition exists for the given month (heap storage).
Called by bg.PartitionManager on every tick for current + next month.
Idempotent. Added 2026-07-04 in migration 335.';

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
    WHERE inhparent = 'public.tool_usage_stats'::regclass;

    SELECT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = 'tool_usage_stats_default'
          AND n.nspname = 'public'
    ) INTO default_exists;

    RAISE NOTICE 'tool_usage_stats: % partitions, default=%',
        partition_count, default_exists;

    IF NOT default_exists THEN
        RAISE EXCEPTION 'tool_usage_stats_default not created — migration 335 incomplete';
    END IF;
END $$;
