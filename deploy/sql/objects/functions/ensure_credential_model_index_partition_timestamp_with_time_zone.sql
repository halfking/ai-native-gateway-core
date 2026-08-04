--
-- Name: ensure_credential_model_index_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_credential_model_index_partition(target_month timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start date := date_trunc('month', target_month)::date;
    month_end   date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'credential_model_index_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF credential_model_index
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'Created partition % for credential_model_index', partition_name;
    END IF;
END;
$$;

