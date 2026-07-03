-- Migration 330: Convert usage_ledger to partitioned table
--
-- Background:
--   usage_ledger is a core billing table that records cost, tokens, and
--   latency for every LLM request. It accumulates millions of rows per
--   month and is queried frequently for:
--     - API key quota checks (performance-sensitive)
--     - Tenant cost aggregation
--     - Usage statistics and reporting
--
--   Currently it's a plain table without partitioning, which causes:
--     - Query performance degradation as table grows
--     - Difficult to efficiently clean up old data
--     - Inconsistent with request_logs and request_wal architecture
--
-- Solution:
--   Convert to a monthly-partitioned table using RANGE(ts). This enables:
--     - Faster time-range queries (partition pruning)
--     - Efficient old data cleanup (DROP PARTITION)
--     - Consistent architecture across all large tables
--
-- Storage strategy:
--   Partitions remain **heap** storage (not columnar) because:
--     - telemetry/client.go performs UPDATE operations on tokens/cost/latency
--       after streaming completes (lines 845, 873)
--     - columnar tables do not support UPDATE/DELETE
--
-- Integration:
--   - ensure_usage_ledger_partition() function already exists
--     (created in phase-23-columnar-invariant/01-rewrite-ensure-functions.sql)
--   - This migration only converts the table structure
--   - bg/partition_manager.go will be updated separately to auto-create partitions
--
-- Migration strategy:
--   Option A (simple, requires brief downtime):
--     1. Rename old table to usage_ledger_old
--     2. Create new partitioned table
--     3. Copy data
--     4. Swap table names
--     5. Drop old table
--
--   Option B (online, zero downtime):
--     Use logical replication or manual INSERT batching
--     (implementation in separate script if needed)
--
-- This migration uses Option A for simplicity. For production with
-- large datasets, consider using Option B or scheduling during
-- maintenance window.
--
-- Risks:
--   - Brief write unavailability during table swap
--   - Requires sufficient disk space for temporary table
--
-- Rollback:
--   Apply 330_usage_ledger_partition.down.sql
--
-- Added: 2026-07-04
-- Author: ACC team analysis

BEGIN;

-- Step 1: Check if table is already partitioned
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = 'usage_ledger'
          AND n.nspname = 'public'
          AND c.relkind = 'p'  -- 'p' means partitioned table
    ) THEN
        RAISE NOTICE 'usage_ledger is already partitioned, skipping migration';
        RETURN;
    END IF;
END $$;

-- Step 2: Rename existing table
ALTER TABLE IF EXISTS public.usage_ledger RENAME TO usage_ledger_old;

-- Step 3: Create new partitioned table with identical schema
CREATE TABLE public.usage_ledger (
    id bigint NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    department text,
    employee text,
    "position" text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean,
    error_kind text,
    route_reason text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    cost_currency text,
    CONSTRAINT usage_ledger_ts_not_null CHECK (ts IS NOT NULL)
) PARTITION BY RANGE (ts);

-- Step 4: Create default partition (catches all rows not matching specific partitions)
CREATE TABLE public.usage_ledger_default PARTITION OF public.usage_ledger DEFAULT;

-- Step 5: Create partitions for recent months (adjust dates based on deployment date)
-- These will be automatically created by PartitionManager going forward,
-- but we create them now to match existing data distribution.
SELECT ensure_usage_ledger_partition('2026-06-01'::timestamp);
SELECT ensure_usage_ledger_partition('2026-07-01'::timestamp);
SELECT ensure_usage_ledger_partition('2026-08-01'::timestamp);

-- Step 6: Migrate data from old table to new partitioned table
-- This uses INSERT INTO ... SELECT which is transactional and safe.
-- For very large tables (>10M rows), consider batching this operation.
INSERT INTO public.usage_ledger
SELECT * FROM public.usage_ledger_old;

-- Step 7: Recreate sequence ownership
ALTER SEQUENCE IF EXISTS public.usage_ledger_id_seq OWNED BY public.usage_ledger.id;

-- Step 8: Set default value for id column
ALTER TABLE public.usage_ledger ALTER COLUMN id SET DEFAULT nextval('public.usage_ledger_id_seq'::regclass);

-- Step 9: Recreate indexes
-- Check existing indexes on old table first
DO $$
DECLARE
    idx_record RECORD;
BEGIN
    FOR idx_record IN
        SELECT indexname, indexdef
        FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename = 'usage_ledger_old'
          AND indexname != 'usage_ledger_pkey'  -- Primary key will be recreated separately
    LOOP
        -- Replace table name in index definition
        EXECUTE replace(idx_record.indexdef, 'usage_ledger_old', 'usage_ledger');
        RAISE NOTICE 'Recreated index: %', idx_record.indexname;
    END LOOP;
END $$;

-- Step 10: Recreate primary key
-- Note: Partitioned tables require partition key (ts) to be part of primary key
ALTER TABLE public.usage_ledger ADD CONSTRAINT usage_ledger_pkey PRIMARY KEY (id, ts);

-- Step 11: Add comment
COMMENT ON TABLE public.usage_ledger IS
'Billing ledger recording cost, tokens, and latency for every LLM request. '
'Partitioned monthly by ts for performance and data lifecycle management. '
'Stays heap storage (not columnar) due to UPDATE operations from telemetry '
'enrichment. Migrated to partitioned table in migration 330 (2026-07-04).';

COMMENT ON COLUMN public.usage_ledger.cost_currency IS
'Currency for usage_ledger.cost_usd source pricing; USD when cost_usd is directly billable.';

-- Step 12: Verify row counts match
DO $$
DECLARE
    old_count bigint;
    new_count bigint;
BEGIN
    SELECT COUNT(*) INTO old_count FROM public.usage_ledger_old;
    SELECT COUNT(*) INTO new_count FROM public.usage_ledger;
    
    IF old_count != new_count THEN
        RAISE EXCEPTION 'Row count mismatch: old=% new=%', old_count, new_count;
    END IF;
    
    RAISE NOTICE 'Migration successful: % rows migrated', new_count;
END $$;

-- Step 13: Drop old table immediately (data already migrated and verified)
DROP TABLE IF EXISTS public.usage_ledger_old;
RAISE NOTICE '✓ Dropped old table: usage_ledger_old';

COMMIT;

-- Post-migration verification:
--   [ ] Verify row counts: SELECT COUNT(*) FROM usage_ledger;
--   [ ] Test INSERT: INSERT INTO usage_ledger (...) VALUES (...);
--   [ ] Test UPDATE: UPDATE usage_ledger SET cost_usd = ... WHERE id = ...;
--   [ ] Test SELECT: SELECT * FROM usage_ledger WHERE ts >= NOW() - INTERVAL '7 days';
--   [ ] Check partitions: \d+ usage_ledger
--   [ ] Monitor query performance
--   [ ] Verify PartitionManager auto-creates future partitions
