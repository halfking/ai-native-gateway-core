-- Migration 334: Convert credit_ledger to a partitioned table + add default partition
--
-- Background:
--   credit_ledger is a single heap table holding every billing event
--   (topups, consumption, refunds, adjustments). It grows quickly and
--   benefits from the same data-lifecycle architecture as request_logs:
--   - credit_ledger_default holds recent (mutable) rows — heap, supports UPDATE
--   - monthly partitions (older data) are pre-created for current+next month
--   - INSERT/UPDATE/DELETE in Go code always targets credit_ledger_default
--   - A background migrator moves old rows from credit_ledger_default into
--     the matching month partition
--
-- Conversion strategy:
--   1. Rename old heap → credit_ledger_old (preserve data during flip)
--   2. Recreate credit_ledger as RANGE(created_at) PARTITIONED
--      with the same columns and CHECK constraint
--   3. Re-attach the credit_ledger_id_seq to the new partition's id column
--   4. Create credit_ledger_default as the catch-all write target
--   5. Pre-create monthly partitions 2026_06 / _07 / _08
--   6. Copy rows from old heap (PG routes by created_at)
--   7. Sanity check + drop old heap
--   8. Ensure functions re-bound to the new partition tree

BEGIN;

-- ============================================================
-- Step 1: Rename existing heap
-- ============================================================
ALTER TABLE public.credit_ledger RENAME TO credit_ledger_old;

-- ============================================================
-- Step 2: Recreate as RANGE-partitioned (identical columns + CHECK)
-- ============================================================
CREATE TABLE public.credit_ledger (
    id              bigint NOT NULL,
    tenant_id       character varying(64) NOT NULL,
    entry_type      character varying(32) NOT NULL,
    amount          bigint NOT NULL,
    balance_after   bigint NOT NULL,
    ref_type        character varying(32),
    ref_id          character varying(128),
    note            text DEFAULT ''::text NOT NULL,
    created_at      timestamp with time zone DEFAULT now() NOT NULL,
    pool            character varying(32),
    CONSTRAINT credit_ledger_entry_type_check CHECK (
        ((entry_type)::text = ANY (ARRAY[
            ('consume'::character varying)::text,
            ('topup'::character varying)::text,
            ('subscribe'::character varying)::text,
            ('adjust'::character varying)::text,
            ('refund'::character varying)::text
        ]))
    )
) PARTITION BY RANGE (created_at);

-- ============================================================
-- Step 3: Recreate primary key (must include partition key)
-- ============================================================
ALTER TABLE public.credit_ledger
    ADD CONSTRAINT credit_ledger_pkey PRIMARY KEY (id, created_at);

-- ============================================================
-- Step 4: Re-attach the existing sequence
-- ============================================================
ALTER SEQUENCE public.credit_ledger_id_seq OWNED BY public.credit_ledger.id;
ALTER TABLE public.credit_ledger ALTER COLUMN id
    SET DEFAULT nextval('public.credit_ledger_id_seq'::regclass);

-- ============================================================
-- Step 5: Pre-create monthly partitions for current+next month
--         + the _default catch-all that all writes target
-- ============================================================
CREATE TABLE public.credit_ledger_2026_06
    PARTITION OF public.credit_ledger
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');

CREATE TABLE public.credit_ledger_2026_07
    PARTITION OF public.credit_ledger
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

CREATE TABLE public.credit_ledger_2026_08
    PARTITION OF public.credit_ledger
    FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');

CREATE TABLE public.credit_ledger_default
    PARTITION OF public.credit_ledger DEFAULT;

COMMENT ON TABLE public.credit_ledger IS
    'Per-tenant credit transaction log. Partitioned monthly by created_at (heap). '
    'Writes always target credit_ledger_default (heap, supports UPDATE/DELETE). '
    'Monthly partitions are pre-created for current+next month by bg/partition_manager.go '
    'ensure_credit_ledger_partition(); SELECTs over the parent aggregate all partitions.';

-- ============================================================
-- Step 6: Copy data from old heap to new partitioned table
-- ============================================================
INSERT INTO public.credit_ledger
SELECT * FROM public.credit_ledger_old;

-- ============================================================
-- Step 7: Sanity check
-- ============================================================
DO $$
DECLARE
    src_count bigint;
    dst_count bigint;
BEGIN
    SELECT COUNT(*) INTO src_count FROM public.credit_ledger_old;
    SELECT COUNT(*) INTO dst_count FROM public.credit_ledger;
    IF src_count <> dst_count THEN
        RAISE EXCEPTION 'Row count mismatch: old=%, new=%', src_count, dst_count;
    END IF;
    RAISE NOTICE 'Migration 334: copied % rows to partitioned credit_ledger', dst_count;
END $$;

-- ============================================================
-- Step 8: Drop old heap
-- ============================================================
DROP TABLE public.credit_ledger_old;

-- ============================================================
-- Step 9: Ensure ensure_credit_ledger_partition exists (idempotent)
--         Function signature: (timestamp with time zone) RETURNS text
-- ============================================================
CREATE OR REPLACE FUNCTION public.ensure_credit_ledger_partition(target_month timestamp with time zone)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    partition_name text;
    start_date timestamp with time zone;
    end_date   timestamp with time zone;
BEGIN
    start_date := date_trunc('month', target_month);
    end_date   := start_date + interval '1 month';
    partition_name := 'credit_ledger_' || to_char(start_date, 'YYYY_MM');

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE %I PARTITION OF credit_ledger FOR VALUES FROM (%L) TO (%L)',
        partition_name, start_date, end_date
    );

    RAISE NOTICE 'ensure_credit_ledger_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION public.ensure_credit_ledger_partition(timestamp with time zone) IS
'Ensure a monthly credit_ledger partition exists for the given month (heap storage).
Called by bg.PartitionManager on every tick for current + next month.
Idempotent. Added 2026-07-04 in migration 334.';

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
    WHERE inhparent = 'public.credit_ledger'::regclass;

    SELECT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = 'credit_ledger_default'
          AND n.nspname = 'public'
    ) INTO default_exists;

    RAISE NOTICE 'credit_ledger: % partitions, default=%',
        partition_count, default_exists;

    IF NOT default_exists THEN
        RAISE EXCEPTION 'credit_ledger_default not created — migration 334 incomplete';
    END IF;
END $$;
