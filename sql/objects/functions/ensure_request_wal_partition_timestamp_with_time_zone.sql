--
-- Name: ensure_request_wal_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.ensure_request_wal_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$ DECLARE month_start date; month_end date; part_name text; BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name := 'request_wal_' || to_char(month_start, 'YYYY_MM');
 IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name AND relnamespace = 'public'::regnamespace) THEN EXECUTE format('CREATE TABLE %I PARTITION OF request_wal FOR VALUES FROM (%L) TO (%L)', part_name, month_start, month_end); END IF; END; $$;

