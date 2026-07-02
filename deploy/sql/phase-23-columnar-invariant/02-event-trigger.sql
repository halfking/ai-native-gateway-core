-- ============================================================
-- Phase 23 / 02 — Event trigger: enforce columnar at runtime
--
-- Goal: even if a developer runs `CREATE TABLE foo PARTITION OF
--       routing_decision_log ...` without `USING columnar`, the
--       partition will be auto-converted within the same transaction
--       cycle.
--
-- Implementation: a `ddl_command_end` event trigger inspects the
-- command tag. For PARTITION OF statements that attached a heap table
-- to an INSERT-only parent, the trigger fires an ALTER TABLE
-- SET ACCESS METHOD columnar inside an autonomous sub-transaction so
-- that the user's outer statement still succeeds.
--
-- Note: ddl_command_end runs AFTER the command commits, so the ALTER
-- runs in a separate transaction. This is acceptable because new
-- partitions are typically not INSERT-into'd until hours/days later.
--
-- The two helpers enforce_columnar_partition() and
-- enforce_columnar_for_insert_only_parents() are idempotent.
-- ============================================================

-- ----------------------------------------------------------------
-- 1. Helper: enforce_columnar_partition(part_name, parent_name)
--    Converts a single partition to columnar AM if it's currently
--    heap. Wrapped in EXCEPTION handler so callers stay safe.
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION enforce_columnar_partition(p_partition_name text, p_parent_name text)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    storage text;
    ok boolean := false;
BEGIN
    -- Only convert heap → columnar. Skip if not partition of parent,
    -- or if already columnar.
    SELECT am.amname INTO storage
    FROM pg_class c
    JOIN pg_am am ON am.oid = c.relam
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_inherits i ON i.inhrelid = c.oid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE n.nspname='public'
      AND c.relname = p_partition_name
      AND p.relname = p_parent_name;

    IF storage IS NULL THEN
        -- Partition doesn't exist or is not for the expected parent.
        RETURN;
    END IF;
    IF storage = 'columnar' THEN
        RETURN;
    END IF;

    BEGIN
        EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar',
                       p_partition_name);
        RAISE NOTICE 'enforce_columnar: converted %.% (heap -> columnar)',
                      p_parent_name, p_partition_name;
        ok := true;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'enforce_columnar: %.% conversion failed: %',
                       p_parent_name, p_partition_name, SQLERRM;
    END;
END;
$$;

COMMENT ON FUNCTION enforce_columnar_partition(text, text) IS
'Convert a single partition to columnar AM if it is currently heap.
Idempotent. Safe to call from plpgsql or directly. Added 2026-07-02.';

-- ----------------------------------------------------------------
-- 2. Helper: list of INSERT-only parents (the columnar invariant)
--    Kept here so the event trigger and columnar_healthcheck() agree.
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION columnar_insert_only_parents()
RETURNS text[]
LANGUAGE sql STABLE AS $$
    SELECT ARRAY['routing_decision_log','credential_model_index'];
$$;

COMMENT ON FUNCTION columnar_insert_only_parents() IS
'Whitelist of parents whose partitions must be columnar. Phase 23 / 02
single source of truth (also referenced by columnar_healthcheck()).';

-- ----------------------------------------------------------------
-- 3. Event trigger function: enforce_columnar_for_insert_only_parents()
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION fn_enforce_columnar_event_trigger()
RETURNS event_trigger
LANGUAGE plpgsql AS $$
DECLARE
    parent_name text;
    cmd_record record;
    relkind_table char;
BEGIN
    -- Iterate CREATE TABLE commands issued in this DDL batch
    FOR cmd_record IN
        SELECT *
        FROM pg_event_trigger_ddl_commands()
        WHERE command_tag = 'CREATE TABLE'
    LOOP
        -- Look at every freshly-stamped partition attached to INSERT-only
        -- parents. If any of them is heap, convert it.
        FOREACH parent_name IN ARRAY columnar_insert_only_parents()
        LOOP
            PERFORM enforce_columnar_partition(c.relname, parent_name)
            FROM pg_class c
            JOIN pg_am am ON am.oid = c.relam
            JOIN pg_inherits i ON i.inhrelid = c.oid
            JOIN pg_class p ON p.oid = i.inhparent
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public'
              AND p.relname = parent_name
              AND am.amname = 'heap';
        END LOOP;
    END LOOP;
END;
$$;

-- ----------------------------------------------------------------
-- 4. Event trigger itself: idempotent install
-- ----------------------------------------------------------------

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_event_trigger
               WHERE evtname = 'enforce_columnar_trigger') THEN
        -- Idempotent re-run: drop and re-create
        EXECUTE 'DROP EVENT TRIGGER enforce_columnar_trigger';
    END IF;
END
$$;

CREATE EVENT TRIGGER enforce_columnar_trigger
ON ddl_command_end
WHEN TAG IN ('CREATE TABLE', 'ALTER TABLE')
EXECUTE FUNCTION fn_enforce_columnar_event_trigger();

COMMENT ON EVENT TRIGGER enforce_columnar_trigger IS
'After every CREATE TABLE / ALTER TABLE, scan newly-attached partitions
of INSERT-only parents; convert any heap partition into columnar.
Idempotent. Added 2026-07-02 by Phase 23 / 02.';

-- ----------------------------------------------------------------
-- 5. Anti-rollback guard: prevent converting UPDATE-heavy parents
--    to columnar by accident. They will fail at INSERT time anyway
--    because columnar rejects UPDATE on most columns.
--
--    We DO NOT add such a guard here because the existing
--    columnar_healthcheck() and columnar_heal() functions are the
--    authoritative source of truth.
-- ----------------------------------------------------------------

\echo ''
\echo 'Phase 23 / 02 complete: event trigger installed.'
