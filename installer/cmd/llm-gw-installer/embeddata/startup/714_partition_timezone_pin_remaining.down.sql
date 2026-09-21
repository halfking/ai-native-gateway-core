-- Migration 714 down: restore the six functions to their pre-714 bodies
--
-- 与 694/699 down 同定式：全部 CREATE OR REPLACE（回滚运行时 up 侧定义已
-- 存在，裸 CREATE 会 42710）。恢复体 = 475/534/579/580 原始定义（无时区
-- 钉扎；cache_metrics/handoff_logs 恢复 DECLARE 初始化器形态）。714 的
-- ledger 行在此删除。注意：回滚即重新暴露 473 类时区漂移，仅在明确需要
-- 复原历史行为时使用。

BEGIN;

CREATE OR REPLACE FUNCTION public.ensure_session_module_executions_partition(
    target_date date DEFAULT NULL
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    v_date           date;
    v_month_start    date;
    v_month_end      date;
    v_partition_name text;
BEGIN
    v_date        := COALESCE(target_date, CURRENT_DATE);
    v_month_start := DATE_TRUNC('month', v_date)::date;
    v_month_end   := (v_month_start + INTERVAL '1 month')::date;
    v_partition_name := 'session_module_executions_' || TO_CHAR(v_month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_tables
                   WHERE schemaname = 'public' AND tablename = v_partition_name) THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.session_module_executions
             FOR VALUES FROM (%L) TO (%L)',
            v_partition_name, v_month_start, v_month_end
        );

        EXECUTE format(
            'CREATE INDEX idx_%s_session ON public.%I (gw_session_id, module_name)',
            v_partition_name, v_partition_name
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_tenant ON public.%I (tenant_id, created_at DESC)',
            v_partition_name, v_partition_name
        );
    END IF;

    RETURN v_partition_name;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_dashboard_events_partition(
    target_date date DEFAULT NULL
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    v_date           date;
    v_month_start    date;
    v_month_end      date;
    v_partition_name text;
BEGIN
    v_date        := COALESCE(target_date, CURRENT_DATE);
    v_month_start := DATE_TRUNC('month', v_date)::date;
    v_month_end   := (v_month_start + INTERVAL '1 month')::date;
    v_partition_name := 'dashboard_access_events_' || TO_CHAR(v_month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_tables
                   WHERE schemaname = 'public' AND tablename = v_partition_name) THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.dashboard_access_events
             FOR VALUES FROM (%L) TO (%L)',
            v_partition_name, v_month_start, v_month_end
        );

        EXECUTE format(
            'CREATE INDEX idx_%s_tenant ON public.%I (tenant_id, timestamp DESC)',
            v_partition_name, v_partition_name
        );
    END IF;

    RETURN v_partition_name;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_cache_metrics_partition(
    target_date date DEFAULT CURRENT_DATE
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    month_start    date := date_trunc('month', target_date)::date;
    month_end      date := (date_trunc('month', target_date) + interval '1 month')::date;
    partition_name text := 'cache_metrics_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.cache_metrics FOR VALUES FROM (%L) TO (%L)',
        partition_name, month_start, month_end
    );

    RAISE NOTICE 'ensure_cache_metrics_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_handoff_logs_partition(p_month timestamptz)
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    m0 timestamp := date_trunc('month', p_month AT TIME ZONE 'Asia/Shanghai');
    m1 timestamp := m0 + interval '1 month';
    part_name text := 'handoff_logs_' || to_char(p_month AT TIME ZONE 'Asia/Shanghai', 'YYYY_MM');
    existing_parent oid;
    existing_am text;
BEGIN
    SELECT i.inhparent, am.amname INTO existing_parent, existing_am
    FROM pg_inherits i
    JOIN pg_class child ON child.oid = i.inhrelid
    JOIN pg_namespace cn ON cn.oid = child.relnamespace
    LEFT JOIN pg_am am ON am.oid = child.relam
    WHERE cn.nspname = 'public' AND child.relname = part_name;

    IF existing_parent IS NOT NULL THEN
        IF existing_parent <> 'public.handoff_logs'::regclass OR existing_am <> 'columnar' THEN
            RAISE EXCEPTION 'public.% exists but is not an attached columnar handoff partition', part_name;
        END IF;
        RETURN;
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.handoff_logs FOR VALUES FROM (%L) TO (%L) USING columnar',
        part_name, m0, m1
    );
    EXECUTE format('CREATE INDEX %I ON public.%I (created_at, id)', part_name || '_created_at_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (session_id, created_at DESC)', part_name || '_session_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (tenant_id, created_at DESC)', part_name || '_tenant_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (trigger_reason, created_at DESC)', part_name || '_trigger_idx', part_name);
END;
$function$;

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
        PERFORM public.ensure_dashboard_events_partition(v_month_value::date);
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
        USING _dae_promotion_batch b
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

DELETE FROM public.schema_migrations WHERE version = '714';

COMMIT;
