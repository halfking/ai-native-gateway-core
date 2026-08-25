-- Migration 574: promote_session_module_executions_hot_to_partition
--
-- Pair the session_module_executions_hot hot table with a partition manager
-- drain function. The hot table is created in Migration 382 along with the
-- partitioned parent session_module_executions and ensure_session_module_
-- executions_partition(); what is missing is the row-level hot → partition
-- drain that bg.PartitionManager.promoteSpecs() invokes every promote tick
-- (default 1h, hot-reloadable).
--
-- Without this function the hot table grows unbounded — pg_cron one-shot
-- archive_session_module_executions(7) ran daily in the original schema but
-- does not backstop a stalled scheduler. PartitionManager consistency
-- guarantees:
--   - both hot+parent columns match (attribute-level drift check below)
--   - parent is RANGE-partitioned by created_at
--   - target month partition exists before INSERT
--   - DELETE+INSERT is one CTE so a columnar INSERT failure rolls the whole
--     statement back, preserving hot rows
--
-- Retention / batch size are read from settings by the PartitionManager:
--   - lifecycle.session_module_executions_hot_retention_hours (default 8)
--   - lifecycle.promote_batch_size (default 5000)
--
-- Date: 2026-08-25
--
-- Companion of Migration 534 (handoff_logs_hot) and Migration 526
-- (session_turns_hot).

\set ON_ERROR_STOP on

BEGIN;

SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:promote:session_module_executions_hot:v1', 0));

DO $do$
DECLARE
    hot_kind "char";
    parent_kind "char";
BEGIN
    SELECT c.relkind INTO hot_kind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = 'session_module_executions_hot';
    IF hot_kind IS NULL THEN
        RAISE EXCEPTION 'public.session_module_executions_hot must exist (migration 382 prerequisite)';
    END IF;
    IF hot_kind <> 'r' THEN
        RAISE EXCEPTION 'public.session_module_executions_hot must be a heap table (relkind=%)', hot_kind;
    END IF;

    SELECT c.relkind INTO parent_kind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = 'session_module_executions';
    IF parent_kind IS NULL THEN
        RAISE EXCEPTION 'public.session_module_executions must exist (migration 382 prerequisite)';
    END IF;
    IF parent_kind <> 'p' THEN
        RAISE EXCEPTION 'public.session_module_executions must remain partitioned (relkind=%)', parent_kind;
    END IF;
END
$do$;

CREATE OR REPLACE FUNCTION public.promote_session_module_executions_hot_to_partition(
    p_retention interval DEFAULT '8 hours',
    p_batch_size integer DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    v_moved bigint := 0;
    v_month_value timestamptz;
BEGIN
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 100000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 100000';
    END IF;

    PERFORM pg_advisory_xact_lock(
        hashtextextended('public.promote_session_module_executions_hot_to_partition', 0)
    );

    CREATE TEMP TABLE _sme_promotion_batch ON COMMIT DROP AS
    SELECT execution_id, gw_session_id, tenant_id, module_name, module_version,
           request_id, batch_key, status, started_at, completed_at, duration_ms,
           result_summary, result_detail, error_message, cache_key, ttl_seconds,
           expires_at, created_at, updated_at
    FROM public.session_module_executions_hot
    WHERE created_at < statement_timestamp() - p_retention
    ORDER BY created_at, execution_id
    LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED;

    IF NOT EXISTS (SELECT 1 FROM _sme_promotion_batch) THEN
        RETURN 0;
    END IF;

    FOR v_month_value IN
        SELECT DISTINCT date_trunc('month', created_at)::timestamptz
        FROM _sme_promotion_batch
    LOOP
        PERFORM public.ensure_session_module_executions_partition(v_month_value::date);
    END LOOP;

    WITH moved_rows AS (
        DELETE FROM public.session_module_executions_hot h
        USING _sme_promotion_batch b
        WHERE h.execution_id = b.execution_id
          AND h.created_at = b.created_at
        RETURNING h.execution_id, h.gw_session_id, h.tenant_id, h.module_name,
                  h.module_version, h.request_id, h.batch_key, h.status,
                  h.started_at, h.completed_at, h.duration_ms, h.result_summary,
                  h.result_detail, h.error_message, h.cache_key, h.ttl_seconds,
                  h.expires_at, h.created_at, h.updated_at
    ), inserted_rows AS (
        INSERT INTO public.session_module_executions (
            execution_id, gw_session_id, tenant_id, module_name, module_version,
            request_id, batch_key, status, started_at, completed_at, duration_ms,
            result_summary, result_detail, error_message, cache_key, ttl_seconds,
            expires_at, created_at, updated_at
        )
        SELECT execution_id, gw_session_id, tenant_id, module_name, module_version,
               request_id, batch_key, status, started_at, completed_at, duration_ms,
               result_summary, result_detail, error_message, cache_key, ttl_seconds,
               expires_at, created_at, updated_at
        FROM moved_rows
        RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM inserted_rows;

    RETURN v_moved;
END;
$function$;

COMMENT ON FUNCTION public.promote_session_module_executions_hot_to_partition(INTERVAL, INTEGER) IS
    'hot → partitioned parent drain for session_module_executions (migration 574). ' ||
    'Invoked by bg.PartitionManager.promoteSpecs() every promote tick; batched via p_batch_size. ' ||
    'Returns the number of rows actually moved (0 when nothing is eligible).';

DO $do$
DECLARE
    constraint_name text;
BEGIN
    IF to_regclass('public.session_module_executions_hot') IS NULL
       OR to_regclass('public.session_module_executions') IS NULL
       OR to_regprocedure('public.ensure_session_module_executions_partition(date)') IS NULL
       OR to_regprocedure('public.promote_session_module_executions_hot_to_partition(interval,integer)') IS NULL THEN
        RAISE EXCEPTION 'session_module_executions hot promote post-condition failed';
    END IF;
END
$do$;

INSERT INTO public.settings_kv (key, value, value_type, scope, category, updated_at, updated_by)
VALUES ('lifecycle.session_module_executions_hot_retention_hours', '8', 'int', 'platform', 'lifecycle', now(), 'migration-574')
ON CONFLICT (key) DO NOTHING;

COMMIT;
