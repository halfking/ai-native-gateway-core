--
-- Name: ensure_model_probe_runs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_model_probe_runs_partition(target_ts timestamp with time zone) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    partition_name text := 'model_probe_runs_disabled';
BEGIN
    RETURN partition_name;
END;
$$;

