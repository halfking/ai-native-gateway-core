-- Migration 607: repair promote_dashboard_access_events_hot_to_partition
--
-- Migration 579 installed this promote function with a column projection
-- that does not match public.dashboard_access_events_hot (created by
-- Migration 383): the batch SELECT, DELETE RETURNING and INSERT all listed
-- a schema the table never had, so every PartitionManager promote tick
-- fails with SQLSTATE 42703 and the hot table keeps growing unbounded —
-- the exact problem 579 was meant to fix.
--
-- 579 is already recorded in repository_schema_migrations on deployed
-- databases, and the strict runner refuses to re-apply (or accept edits
-- to) an applied migration file, so the corrected body ships here as
-- CREATE OR REPLACE. Fresh bootstraps run 579 first and this repair
-- immediately replaces its body; no promote tick can fire in between.
--
-- Corrected contract (23 columns, verified against the 2026-08-25 252
-- production column dump — docs/audits/2026-08-25-252-pg-hot-columns-raw.txt):
--   - drain eligibility / ordering / month bucketing on created_at, the
--     RANGE partition key of dashboard_access_events (Migration 383)
--   - monthly partitions ensured via ensure_dashboard_events_partition(date)
--   - DELETE+INSERT stay one CTE: a parent INSERT failure rolls the whole
--     statement back, preserving hot rows
--
-- The lifecycle.dashboard_access_events_hot_retention_hours setting and
-- the feature-level down path remain owned by migration 579; this repair
-- only replaces the function body and its comment.
--
-- Date: 2026-08-27

\set ON_ERROR_STOP on

BEGIN;

SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:promote:dashboard_access_events_hot:v1', 0));

CREATE OR REPLACE FUNCTION public.promote_dashboard_access_events_hot_to_partition(
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
        hashtextextended('public.promote_dashboard_access_events_hot_to_partition', 0)
    );

    CREATE TEMP TABLE _dae_promotion_batch ON COMMIT DROP AS
    SELECT event_id, event_type, timestamp, tenant_id, user_id, user_role,
           session_id, api_path, api_method, api_version, query_params,
           status_code, response_time_ms, cache_hit, data_size, error_code,
           error_message, client_ip, user_agent, referer, db_query_time_ms,
           cache_query_time_ms, created_at
    FROM public.dashboard_access_events_hot
    WHERE created_at < statement_timestamp() - p_retention
    ORDER BY created_at, event_id
    LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED;

    IF NOT EXISTS (SELECT 1 FROM _dae_promotion_batch) THEN
        RETURN 0;
    END IF;

    FOR v_month_value IN
        SELECT DISTINCT date_trunc('month', created_at)::timestamptz
        FROM _dae_promotion_batch
    LOOP
        PERFORM public.ensure_dashboard_events_partition(v_month_value::date);
    END LOOP;

    WITH moved_rows AS (
        DELETE FROM public.dashboard_access_events_hot h
        USING _dae_promotion_batch b
        WHERE h.event_id = b.event_id
          AND h.created_at = b.created_at
        RETURNING h.event_id, h.event_type, h.timestamp, h.tenant_id,
                  h.user_id, h.user_role, h.session_id, h.api_path,
                  h.api_method, h.api_version, h.query_params, h.status_code,
                  h.response_time_ms, h.cache_hit, h.data_size, h.error_code,
                  h.error_message, h.client_ip, h.user_agent, h.referer,
                  h.db_query_time_ms, h.cache_query_time_ms, h.created_at
    ), inserted_rows AS (
        INSERT INTO public.dashboard_access_events (
            event_id, event_type, timestamp, tenant_id, user_id, user_role,
            session_id, api_path, api_method, api_version, query_params,
            status_code, response_time_ms, cache_hit, data_size, error_code,
            error_message, client_ip, user_agent, referer, db_query_time_ms,
            cache_query_time_ms, created_at
        )
        SELECT event_id, event_type, timestamp, tenant_id, user_id, user_role,
               session_id, api_path, api_method, api_version, query_params,
               status_code, response_time_ms, cache_hit, data_size, error_code,
               error_message, client_ip, user_agent, referer, db_query_time_ms,
               cache_query_time_ms, created_at
        FROM moved_rows
        RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM inserted_rows;

    RETURN v_moved;
END;
$function$;

COMMENT ON FUNCTION public.promote_dashboard_access_events_hot_to_partition(INTERVAL, INTEGER) IS
    'hot → partitioned parent drain for dashboard_access_events (migration 607 repair of 579). Invoked by bg.PartitionManager.promoteSpecs() every promote tick; batched via p_batch_size. Returns the number of rows actually moved (0 when nothing is eligible).';

DO $do$
DECLARE
    hot_exists boolean := to_regclass('public.dashboard_access_events_hot') IS NOT NULL;
    parent_exists boolean := to_regclass('public.dashboard_access_events') IS NOT NULL;
    ensure_exists boolean := to_regprocedure('public.ensure_dashboard_events_partition(date)') IS NOT NULL;
    promote_exists boolean := to_regprocedure('public.promote_dashboard_access_events_hot_to_partition(interval,integer)') IS NOT NULL;
BEGIN
    IF NOT (hot_exists AND parent_exists AND ensure_exists AND promote_exists) THEN
        RAISE EXCEPTION 'dashboard_access_events hot promote repair post-condition failed (hot=%, parent=%, ensure=%, promote=%)',
            hot_exists, parent_exists, ensure_exists, promote_exists;
    END IF;
END
$do$;

COMMIT;
