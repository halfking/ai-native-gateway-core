-- Migration 705: repair request_logs promote 断链 — re-attach the detached
-- monthly partition shells left behind by migration 337.
--
-- Root cause chain (local census 2026-09-14, llm-gateway-pg):
--   1. Migration 337 DETACHed request_logs_2026_07..2026_12 so the DEFAULT
--      partition could receive writes of that era without 23514.
--   2. ensure_request_logs_partition (still the 694 body) checks only
--      "pg_class WHERE relname = part_name" — a DETACHED shell still lives in
--      pg_class, so every ensure tick (and the pre-ensure loop inside
--      promote_request_logs_hot_to_partition, 602/688) saw "already exists"
--      and never re-attached. The shells stayed 0-row standalones.
--   3. promote INSERTs INTO the parent route by ts; with no attached monthly
--      partition every promoted row fell into request_logs_default, which has
--      no TTL and no pruning: 609 MB / 255,084 rows (all 2026-09) piled up
--      while request_logs_2026_07..2026_10 sat empty (352-368 kB of indexes).
--      Parent and shells also drifted: the shells lack the 3 columns added
--      after the detach (billed_despite_cancellation / request_depth /
--      is_terminal), so a bare ATTACH fails on column mismatch.
--
-- Fix, in three parts:
--   a. sync_partition_columns(parent, partition): adds parent columns missing
--      on a would-be partition (nullable, defaults preserved) so ATTACH can
--      succeed after schema drift.
--   b. repair_request_logs_detached_partitions(): the one-shot repair. Detach
--      the DEFAULT partition first, then per standalone shell: sync columns,
--      ATTACH, and only then drain that month's rows from the (now
--      standalone) default through the PARENT so they route into the freshly
--      attached partition. Draining strictly after attach means a failed
--      shell never strands rows outside every partition — they simply stay in
--      the default, which is re-attached at the end. Per-shell failures are
--      WARNed and skipped, never fatal, and the function is re-runnable.
--   c. ensure_request_logs_partition rewritten: attached-aware (pg_inherits,
--      not pg_class), re-attaches drifted shells, and self-heals the 473-class
--      "rows already landed in default for month M, partition M cannot be
--      created" deadlock (23514 on CREATE) with the same detach → create →
--      drain → re-attach dance. Heap only, matching 689/694 policy: monthlies
--      must stay UPDATE/DELETE-capable because the hot→monthly promote chain
--      and row-level TTL paths rely on it (columnar partitions are
--      append-only, established by migration 562).
--
-- Read-path impact: request_logs_with_current_month is
-- "request_logs_hot UNION ALL request_logs (parent)"; moved rows stay visible
-- through the parent branch. claimSessionFinalSuccess only ever touches
-- request_logs_hot, so heap monthlies are untouched by it.
--
-- Idempotent: yes (guards on attach state; drained rows leave the default, so
-- re-runs drain 0). Down: 705_request_logs_reattach_detached_partitions.down.sql
-- restores the pre-705 ensure body — the data stays attached (re-detaching
-- would reopen the 断链; emergency rewind only).

\set ON_ERROR_STOP on

BEGIN;
SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:request-logs-repair-705', 0));

--
-- Name: sync_partition_columns(regclass, regclass); Type: FUNCTION
-- Generic repair utility: add every parent column missing on the partition
-- (matching type/typmod/default/not-null as of this call). Returns the number
-- of columns added. Extra/renamed columns on the partition are NOT touched —
-- ATTACH will reject real drift loudly rather than silently dropping data.
--
CREATE OR REPLACE FUNCTION public.sync_partition_columns(p_parent regclass, p_partition regclass) RETURNS integer
    LANGUAGE plpgsql
    AS $$
DECLARE
    r     record;
    added int := 0;
BEGIN
    FOR r IN
        SELECT a.attname,
               format_type(a.atttypid, a.atttypmod) AS col_type,
               a.attnotnull,
               pg_get_expr(d.adbin, d.adrelid) AS col_default
        FROM pg_attribute a
        LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
        WHERE a.attrelid = p_parent
          AND a.attnum > 0
          AND NOT a.attisdropped
          AND NOT EXISTS (
              SELECT 1 FROM pg_attribute c
              WHERE c.attrelid = p_partition
                AND c.attname = a.attname
                AND c.attnum > 0
                AND NOT c.attisdropped)
    LOOP
        EXECUTE format(
            'ALTER TABLE %s ADD COLUMN %I %s%s%s',
            p_partition, r.attname, r.col_type,
            CASE WHEN r.col_default IS NOT NULL THEN ' DEFAULT ' || r.col_default ELSE '' END,
            CASE WHEN r.attnotnull THEN ' NOT NULL' ELSE '' END);
        added := added + 1;
    END LOOP;
    RETURN added;
END;
$$;

--
-- Name: repair_request_logs_detached_partitions(); Type: FUNCTION
-- Returns the number of rows drained from request_logs_default into monthly
-- partitions. Month bounds are derived from each shell's YYYY_MM name in
-- Asia/Shanghai (687/694 convention).
--
CREATE OR REPLACE FUNCTION public.repair_request_logs_detached_partitions() RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    parent_oid   regclass := 'public.request_logs'::regclass;
    default_name text     := 'request_logs_default';
    default_oid  regclass;
    was_attached boolean := false;
    shell        record;
    month_start  date;
    month_end    date;
    drained      bigint;
    total        bigint := 0;
BEGIN
    -- 687/694 convention: partition bounds and the ::timestamptz drain
    -- predicate both read the session TimeZone — pin Shanghai so a UTC
    -- session cannot create 08:00+08 bounds.
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    PERFORM pg_advisory_xact_lock(hashtextextended('llm-gateway:repair-request-logs-detached', 0));

    SELECT c.oid INTO default_oid
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = default_name AND c.relkind = 'r';

    -- Step 1: detach the DEFAULT partition so shell ATTACHes skip the
    -- default-constraint validation scan entirely. The txn holds ACCESS
    -- EXCLUSIVE on the parent until COMMIT, so concurrent promote/tick
    -- statements queue rather than error into the no-default window.
    IF default_oid IS NOT NULL THEN
        was_attached := EXISTS (
            SELECT 1 FROM pg_inherits
            WHERE inhrelid = default_oid
              AND inhparent = parent_oid);
        IF was_attached THEN
            EXECUTE format('ALTER TABLE public.request_logs DETACH PARTITION public.%I', default_name);
        END IF;
    END IF;

    -- Step 2: per standalone shell — sync columns, ATTACH, then drain that
    -- month out of the standalone default through the PARENT (routing, so
    -- column order can never drift positionally).
    FOR shell IN
        SELECT c.relname, c.oid
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind = 'r'
          AND c.relname ~ '^request_logs_[0-9]{4}_[0-9]{2}$'
          AND NOT EXISTS (
              SELECT 1 FROM pg_inherits i
              WHERE i.inhrelid = c.oid
                AND i.inhparent = parent_oid)
        ORDER BY c.relname
    LOOP
        month_start := to_date(substring(shell.relname from '[0-9]{4}_[0-9]{2}$'), 'YYYY_MM');
        month_end   := (date_trunc('month', month_start::timestamp) + interval '1 month')::date;

        BEGIN
            PERFORM public.sync_partition_columns(parent_oid, shell.oid);
            EXECUTE format(
                'ALTER TABLE public.request_logs ATTACH PARTITION public.%I FOR VALUES FROM (%L) TO (%L)',
                shell.relname, month_start, month_end);
        EXCEPTION WHEN OTHERS THEN
            -- Leave this month in the default; ensure()'s re-attach branch
            -- and a re-run of this repair will retry it. Nothing was drained,
            -- so no row is stranded outside every partition.
            RAISE WARNING 'repair_request_logs_detached_partitions: could not re-attach % (%); month rows remain in request_logs_default',
                shell.relname, SQLERRM;
            CONTINUE;
        END;

        -- Attached: now drain this month from the standalone default. Any
        -- failure here propagates (loud) — the txn rolls back to the safe
        -- pre-repair state rather than half-attaching.
        IF default_oid IS NOT NULL THEN
            LOOP
                WITH batch AS (
                    SELECT ctid
                    FROM public.request_logs_default
                    WHERE ts >= month_start::timestamptz
                      AND ts <  month_end::timestamptz
                    ORDER BY ctid
                    LIMIT 50000
                    FOR UPDATE SKIP LOCKED
                ),
                moved AS (
                    DELETE FROM public.request_logs_default d
                    WHERE d.ctid IN (SELECT ctid FROM batch)
                    RETURNING d.*
                )
                INSERT INTO public.request_logs
                SELECT * FROM moved;

                GET DIAGNOSTICS drained = ROW_COUNT;
                total := total + drained;
                EXIT WHEN drained = 0;
            END LOOP;
        END IF;
        RAISE NOTICE 'repair_request_logs_detached_partitions: attached %', shell.relname;
    END LOOP;

    -- Step 3: put the default back (only if this call detached it — a
    -- default that arrived standalone stays untouched). Remaining rows are
    -- exactly the months without an attached partition, so the overlap
    -- validation passes.
    IF default_oid IS NOT NULL AND was_attached THEN
        EXECUTE format('ALTER TABLE public.request_logs ATTACH PARTITION public.%I DEFAULT', default_name);
    END IF;

    RETURN total;
END;
$$;

--
-- Name: ensure_request_logs_partition(timestamptz); Type: FUNCTION
-- 705 rewrite. Differences from the 694 body:
--   * "exists" means ATTACHED (pg_inherits), not merely present in pg_class;
--   * a legacy detached shell is column-synced and re-attached (warning +
--     fall-through to default routing if it cannot be attached);
--   * a fresh CREATE that fails because the default already holds rows for
--     the month (473-class gap) self-heals: detach default → create → drain
--     that month → re-attach default;
--   * advisory lock so concurrent ensure/promote ticks cannot race on CREATE;
--   * still heap-only with the per-partition GIN trgm indexes.
--
CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date;
    month_end     date;
    part_name     text;
    default_oid   regclass;
    was_attached  boolean;
    drained       bigint;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end   := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name   := 'request_logs_' || to_char(month_start, 'YYYY_MM');

    IF EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.request_logs'::regclass
          AND c.relname = part_name
          AND c.relnamespace = 'public'::regnamespace
    ) THEN
        RETURN;
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended('llm-gateway:ensure-request-logs-partition', 0));

    -- Re-check under the lock.
    IF EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.request_logs'::regclass
          AND c.relname = part_name
          AND c.relnamespace = 'public'::regnamespace
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = part_name
          AND c.relkind = 'r'
    ) THEN
        -- Legacy detached shell: sync + re-attach. On failure warn and let
        -- inserts keep routing to the default (today's status quo) instead of
        -- failing the caller's promote tick.
        BEGIN
            PERFORM public.sync_partition_columns('public.request_logs'::regclass, part_name::regclass);
            EXECUTE format(
                'ALTER TABLE public.request_logs ATTACH PARTITION public.%I FOR VALUES FROM (%L) TO (%L)',
                part_name, month_start, month_end);
            RAISE NOTICE 'ensure_request_logs_partition: re-attached legacy shell %', part_name;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'ensure_request_logs_partition: could not re-attach % (%); inserts for this month keep routing to request_logs_default',
                part_name, SQLERRM;
        END;
        RETURN;
    END IF;

    -- Fresh partition. If the default already holds rows for this month
    -- (rows landed before any attached partition existed), CREATE fails with
    -- 23514 ("updated partition constraint for default ... would be
    -- violated"). Self-heal: detach default → create → drain that month →
    -- re-attach. The parent is not under concurrent DDL (we hold the ensure
    -- advisory lock; promote INSERTs for this month route into the new
    -- partition, and out-of-month inserts are practically nonexistent).
    BEGIN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end);
    EXCEPTION
        -- 23514 only: the default-constraint violation. Everything else
        -- propagates to the caller (602 contract: loud).
        WHEN check_violation THEN
            SELECT c.oid INTO default_oid
            FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public' AND c.relname = 'request_logs_default' AND c.relkind = 'r';

            IF default_oid IS NULL OR NOT EXISTS (
                SELECT 1 FROM pg_inherits
                WHERE inhrelid = default_oid
                  AND inhparent = 'public.request_logs'::regclass
            ) THEN
                RAISE;  -- not the default-constraint case; rethrow
            END IF;

            EXECUTE 'ALTER TABLE public.request_logs DETACH PARTITION public.request_logs_default';
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
                part_name, month_start, month_end);

            drained := 0;
            LOOP
                WITH batch AS (
                    SELECT ctid
                    FROM public.request_logs_default
                    WHERE ts >= month_start::timestamptz
                      AND ts <  month_end::timestamptz
                    ORDER BY ctid
                    LIMIT 50000
                    FOR UPDATE SKIP LOCKED
                ),
                moved AS (
                    DELETE FROM public.request_logs_default d
                    WHERE d.ctid IN (SELECT ctid FROM batch)
                    RETURNING d.*
                )
                INSERT INTO public.request_logs
                SELECT * FROM moved;

                GET DIAGNOSTICS drained = ROW_COUNT;
                EXIT WHEN drained = 0;
            END LOOP;

            EXECUTE 'ALTER TABLE public.request_logs ATTACH PARTITION public.request_logs_default DEFAULT';
            RAISE NOTICE 'ensure_request_logs_partition: healed default-gap for % (drained rows into new partition)', part_name;
    END;

    EXECUTE format(
        'CREATE INDEX IF NOT EXISTS idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
        part_name, part_name);
    -- 2026-06-24 (migration 043): GIN trgm on client_model so the
    -- /api/logs ?model= ILIKE filter can use a bitmap index scan
    -- instead of a partition Seq Scan once volume grows.
    EXECUTE format(
        'CREATE INDEX IF NOT EXISTS idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
        part_name, part_name);
END;
$$;

-- One-shot repair of this database's detached shells. Safe to repeat.
SELECT public.repair_request_logs_detached_partitions();

-- 双账本自登记(695/701/703/704 定式,2026-09-14 审计):升级通道库此前只落
-- gateway_db_revision_sequences :705 标记、无 schema_migrations 行,账本对账
-- 审机会误报漂移。幂等:重跑安全。
INSERT INTO public.schema_migrations (version, description)
VALUES ('705', 're-attach request_logs monthly partition shells detached by 337; rewrite ensure_request_logs_partition (attached-aware + default-gap self-heal)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

-- POST_CONDITION: SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
--   WHERE n.nspname='public' AND c.relname ~ '^request_logs_[0-9]{4}_[0-9]{2}$' AND c.relkind='r'
--   AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid AND i.inhparent='public.request_logs'::regclass)
--   returns 0 (no standalone shells left), and
--   SELECT pg_get_expr(relpartbound, oid) FROM pg_class WHERE relname='request_logs_default'
--   shows a DEFAULT bound (default re-attached).
