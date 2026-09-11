--
-- Name: ensure_request_logs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date;
    month_end     date;
    part_name     text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name := 'request_logs_' || to_char(month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-06-24 (migration 043): GIN trgm on client_model so the
        -- /api/logs ?model= ILIKE filter can use a bitmap index scan
        -- instead of a partition Seq Scan once volume grows.
        EXECUTE format(
            'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;

