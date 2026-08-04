--
-- Name: drop_old_model_probe_runs_partitions(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.drop_old_model_probe_runs_partitions(p_retention_days integer) RETURNS TABLE(dropped_partition text, rows_dropped bigint)
    LANGUAGE plpgsql
    AS $$
BEGIN
    RETURN;
END;
$$;

