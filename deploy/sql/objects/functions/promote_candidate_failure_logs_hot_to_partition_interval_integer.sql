--
-- Name: promote_candidate_failure_logs_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_candidate_failure_logs_hot_to_partition(p_retention interval DEFAULT '24:00:00'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
BEGIN
  RETURN 0;
END;
$$;

