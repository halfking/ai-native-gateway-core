--
-- Name: ensure_usage_ledger_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_usage_ledger_partition(target_month timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date := date_trunc('month', target_month)::date;
    month_end      date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'usage_ledger_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF usage_ledger
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_usage_ledger_partition: created % as heap', partition_name;
    END IF;
END;
$$;

