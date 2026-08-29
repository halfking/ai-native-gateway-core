--
-- Name: ensure_request_logs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 2026-08-23 (migration 562): switched from columnar to heap. The
        -- body columns (request_body / outbound_body / response_body jsonb)
        -- are TOAST-heavy (~350 KB avg) and the hot→monthly promote path
        -- issues INSERT-then-DELETE-when-retried cycles; columnar blocks
        -- UPDATE/DELETE so the bodies pipeline silently stalled, leaving
        -- request_logs_bodies_hot unbounded. request_logs_archive remains
        -- columnar (it's a read-only tiered store, see archive_request_logs).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
    END IF;
END;
$$;

