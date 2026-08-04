--
-- Name: promote_model_probe_runs_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_model_probe_runs_hot_to_partition(p_retention interval, p_batch_size integer) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
BEGIN
    RETURN 0;
END;
$$;

