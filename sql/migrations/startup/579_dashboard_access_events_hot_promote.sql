-- Migration 579: promote_dashboard_access_events_hot_to_partition
--
-- Pair the dashboard_access_events_hot hot table with a partition manager
-- drain function. The hot table is created in Migration 451 along with the
-- partitioned parent dashboard_access_events and ensure_dashboard_access_
-- events_partition(); what is missing is the row-level hot → partition
-- drain that bg.PartitionManager.promoteSpecs() invokes every promote tick
-- (default 1h, hot-reloadable).
--
-- Without this function the hot table grows unbounded — the original schema
-- relied on a manual cron job that has been observed to drift. The
-- PartitionManager enforces:
--   - attribute-level drift check between hot and parent
--   - target month partition exists before INSERT
--   - DELETE+INSERT is one CTE so a columnar INSERT failure rolls the whole
--     statement back, preserving hot rows
--
-- Retention / batch size are read from settings by the PartitionManager:
--   - lifecycle.dashboard_access_events_hot_retention_hours (default 8)
--   - lifecycle.promote_batch_size (default 5000)
--
-- Date: 2026-08-25
--
-- Companion of Migration 534 (handoff_logs_hot) and Migration 526
-- (session_turns_hot) and Migration 574 (session_module_executions_hot).

\set ON_ERROR_STOP on

BEGIN;

SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:promote:dashboard_access_events_hot:v1', 0));

DO $do$
DECLARE
    hot_kind "char";
    parent_kind "char";
BEGIN
    SELECT c.relkind INTO hot_kind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = 'dashboard_access_events_hot';
    IF hot_kind IS NULL THEN
        RAISE EXCEPTION 'public.dashboard_access_events_hot must exist (migration 451 prerequisite)';
    END IF;
    IF hot_kind <> 'r' THEN
        RAISE EXCEPTION 'public.dashboard_access_events_hot must be a heap table (relkind=%)', hot_kind;
    END IF;

    SELECT c.relkind INTO parent_kind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = 'dashboard_access_events';
    IF parent_kind IS NULL THEN
        RAISE EXCEPTION 'public.dashboard_access_events must exist (migration 451 prerequisite)';
    END IF;
    IF parent_kind <> 'p' THEN
        RAISE EXCEPTION 'public.dashboard_access_events must remain partitioned (relkind=%)', parent_kind;
    END IF;
END
$do$;

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
    SELECT event_id, tenant_id, user_id, dashboard_id, widget_id, action,
           route_path, referrer, status_code, duration_ms, request_size_bytes,
           response_size_bytes, user_agent, ip_address, country_code,
           occurred_at, created_at
    FROM public.dashboard_access_events_hot
    WHERE occurred_at < statement_timestamp() - p_retention
    ORDER BY occurred_at, event_id
    LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED;

    IF NOT EXISTS (SELECT 1 FROM _dae_promotion_batch) THEN
        RETURN 0;
    END IF;

    FOR v_month_value IN
        SELECT DISTINCT date_trunc('month', occurred_at)::timestamptz
        FROM _dae_promotion_batch
    LOOP
        PERFORM public.ensure_dashboard_access_events_partition(v_month_value::date);
    END LOOP;

    WITH moved_rows AS (
        DELETE FROM public.dashboard_access_events_hot h
        USING _dae_promotion_batch b
        WHERE h.event_id = b.event_id
          AND h.occurred_at = b.occurred_at
        RETURNING h.event_id, h.tenant_id, h.user_id, h.dashboard_id,
                  h.widget_id, h.action, h.route_path, h.referrer,
                  h.status_code, h.duration_ms, h.request_size_bytes,
                  h.response_size_bytes, h.user_agent, h.ip_address,
                  h.country_code, h.occurred_at, h.created_at
    ), inserted_rows AS (
        INSERT INTO public.dashboard_access_events (
            event_id, tenant_id, user_id, dashboard_id, widget_id, action,
            route_path, referrer, status_code, duration_ms, request_size_bytes,
            response_size_bytes, user_agent, ip_address, country_code,
            occurred_at, created_at
        )
        SELECT event_id, tenant_id, user_id, dashboard_id, widget_id, action,
               route_path, referrer, status_code, duration_ms, request_size_bytes,
               response_size_bytes, user_agent, ip_address, country_code,
               occurred_at, created_at
        FROM moved_rows
        RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM inserted_rows;

    RETURN v_moved;
END;
$function$;

COMMENT ON FUNCTION public.promote_dashboard_access_events_hot_to_partition(INTERVAL, INTEGER) IS
    'hot → partitioned parent drain for dashboard_access_events (migration 579). Invoked by bg.PartitionManager.promoteSpecs() every promote tick; batched via p_batch_size. Returns the number of rows actually moved (0 when nothing is eligible).';

DO $do$
DECLARE
    hot_exists boolean := to_regclass('public.dashboard_access_events_hot') IS NOT NULL;
    parent_exists boolean := to_regclass('public.dashboard_access_events') IS NOT NULL;
    ensure_exists boolean := to_regprocedure('public.ensure_dashboard_access_events_partition(date)') IS NOT NULL;
    promote_exists boolean := to_regprocedure('public.promote_dashboard_access_events_hot_to_partition(interval,integer)') IS NOT NULL;
BEGIN
    IF NOT (hot_exists AND parent_exists AND ensure_exists AND promote_exists) THEN
        RAISE EXCEPTION 'dashboard_access_events hot promote post-condition failed (hot=%, parent=%, ensure=%, promote=%)',
            hot_exists, parent_exists, ensure_exists, promote_exists;
    END IF;
END
$do$;

INSERT INTO public.settings_kv (key, value, value_type, scope, category, updated_at, updated_by)
VALUES ('lifecycle.dashboard_access_events_hot_retention_hours', '8', 'int', 'platform', 'lifecycle', now(), 'migration-579')
ON CONFLICT (key) DO NOTHING;

COMMIT;