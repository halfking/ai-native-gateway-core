--
-- Name: promote_request_wal_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_request_wal_hot_to_partition(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
  n bigint := 0;
BEGIN
  RAISE NOTICE 'request_wal_hot_to_partition: no timestamp column, skip promote';
  RETURN 0;
END;
$$;

