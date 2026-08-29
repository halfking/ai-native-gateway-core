--
-- Name: ensure_next_month_request_wal_partition(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_next_month_request_wal_partition() RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
    partition_name   text := 'request_wal_' || to_char(next_month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
        -- 2026-08-23 (migration 562): switched from columnar to heap.
        -- request_wal is a heap parent; columnar partitions blocked the
        -- hot→monthly promote path. Kept in sync with
        -- ensure_request_wal_partition(timestamptz) (the active call
        -- site). This orphan is retained for backwards compatibility
        -- but now matches the active function's storage policy.
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_wal FOR VALUES FROM (%L) TO (%L)',
            partition_name, next_month_start, next_month_end
        );
    END IF;
END;
$$;

