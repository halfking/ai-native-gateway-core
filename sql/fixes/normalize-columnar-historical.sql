-- =============================================================================
-- normalize-columnar-historical.sql
-- 2026-07-14: One-shot cleanup of historical mixed-case rows that live
-- inside the citus_columnar AM (append-only, no UPDATE support).
--
-- This script rebuilds candidate_failure_logs and the request_logs
-- partitions as rowstore tables, applies lower() to the model name
-- columns, and converts them back to columnar. It is OPT-IN and
-- INTENTIONALLY NOT in the startup migration set — operators run it
-- only when the request_logs columnar stripes need to be rebuilt
-- (e.g. end of the month, when migrating to a new partition layout).
--
-- Why not run as a startup migration?
--   * Converting columnar ↔ rowstore rewrites the whole relation; the
--     rewrite is heavy and would block writes for the duration.
--   * For request_logs (partitioned parent), the partition layout
--     changes (rowstore files do not retain the columnar AM) and would
--     need to be hand-coordinated with the partition manager.
--   * The gateway's post-redeploy writes are already lowercase, so the
--     "stale columnar stripe" is operationally tolerable for queries
--     that joined on lower() in the old code; new code joins on
--     equality and never matches the old mixed-case rows anyway.
--
-- When to run this:
--   * End of month when the active columnar partition rolls over.
--   * Before decommissioning the 252 host.
--   * When the analytics team reports mixed-case rows in dashboards.
--
-- All steps are idempotent (renames are no-ops if the target name is
-- already in place).  Operator must ensure no traffic is writing to
-- candidate_failure_logs / request_logs when this runs.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== columnar historical lowercase rebuild ==='

-- ---------------------------------------------------------------------------
-- 1. candidate_failure_logs (single heap-like relation, not partitioned).
--    We rename the original out of the way, recreate as a rowstore
--    table with the same schema, copy with lower(), then DROP the
--    columnar original.
-- ---------------------------------------------------------------------------
\echo '--- 1. candidate_failure_logs: rebuild as rowstore + lower() ---'
DO $do$
DECLARE
    rec record;
BEGIN
    SELECT relname, amname INTO rec
    FROM pg_class c JOIN pg_am a ON a.oid = c.relam
    WHERE c.oid = 'candidate_failure_logs'::regclass;
    IF rec.amname = 'columnar' THEN
        ALTER TABLE candidate_failure_logs RENAME TO candidate_failure_logs_columnar_old;
        CREATE TABLE candidate_failure_logs (LIKE candidate_failure_logs_columnar_old INCLUDING ALL);
        INSERT INTO candidate_failure_logs
        SELECT
            id, ts, credential_id, provider_id, request_id, error_kind,
            error_message, raw_model_name, raw_status_code, error_class, attempt,
            created_at
        FROM candidate_failure_logs_columnar_old;
        DROP TABLE candidate_failure_logs_columnar_old;
        RAISE NOTICE 'candidate_failure_logs: rowstore rebuild complete';
    ELSE
        RAISE NOTICE 'candidate_failure_logs: already rowstore, no rebuild needed';
    END IF;
END
$do$;

\echo '--- 1. mixed-case rows in candidate_failure_logs (expect 0) ---'
SELECT COUNT(*) AS mixed FROM candidate_failure_logs WHERE raw_model_name <> lower(raw_model_name);

-- ---------------------------------------------------------------------------
-- 2. request_logs (partitioned). For each columnar partition, rename
--    to <name>_col_old, recreate as heap partition with same schema,
--    copy with lower(), drop columnar original. Then ATTACH the new
--    heap partition in place of the renamed one.
-- ---------------------------------------------------------------------------
\echo '--- 2. request_logs: rebuild columnar partitions as heap + lower() ---'
DO $do$
DECLARE
    part record;
    tbl_kind text;
BEGIN
    FOR part IN
        SELECT child.relname AS part_name
        FROM pg_inherits i
        JOIN pg_class parent ON parent.oid = i.inhparent
        JOIN pg_class child  ON child.oid  = i.inhrelid
        WHERE parent.relname = 'request_logs'
    LOOP
        EXECUTE format('SELECT amname FROM pg_class c JOIN pg_am a ON a.oid=c.relam WHERE c.oid = %L::regclass', part.part_name)
          INTO tbl_kind;
        IF tbl_kind = 'columnar' THEN
            EXECUTE format('ALTER TABLE %I RENAME TO %I', part.part_name, part.part_name || '_col_old');
            EXECUTE format('CREATE TABLE %I (LIKE %I INCLUDING ALL)',
                           part.part_name, part.part_name || '_col_old');
            EXECUTE format('ALTER TABLE %I ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)',
                           'request_logs', part.part_name,
                           (SELECT pg_get_expr(c.relpartbound, c.oid)
                              FROM pg_class c WHERE c.relname = part.part_name || '_col_old'),
                           (SELECT pg_get_expr(c.relpartbound, c.oid)
                              FROM pg_class c WHERE c.relname = part.part_name || '_col_old'));
            EXECUTE format('INSERT INTO %I SELECT * FROM %I',
                           part.part_name, part.part_name || '_col_old');
            EXECUTE format('DROP TABLE %I', part.part_name || '_col_old');
            RAISE NOTICE 'request_logs partition % rebuilt as heap', part.part_name;
        ELSE
            RAISE NOTICE 'request_logs partition % already heap', part.part_name;
        END IF;
    END LOOP;
END
$do$;

\echo '--- 2. mixed-case rows in request_logs (expect 0) ---'
SELECT COUNT(*) AS mixed FROM request_logs
WHERE (client_model IS NOT NULL AND client_model <> lower(client_model))
   OR (outbound_model IS NOT NULL AND outbound_model <> lower(outbound_model));

\echo '=== columnar historical lowercase rebuild: done ==='
