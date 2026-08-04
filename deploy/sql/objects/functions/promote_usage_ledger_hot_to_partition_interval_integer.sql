--
-- Name: promote_usage_ledger_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_usage_ledger_hot_to_partition(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM usage_ledger_hot
  WHERE ts < now() - p_retention
  ORDER BY ts
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM usage_ledger_hot
  WHERE (request_id, ts) IN (SELECT request_id, ts FROM _promote_hot_batch);

  BEGIN
    INSERT INTO usage_ledger
    SELECT * FROM _promote_hot_batch;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_usage_ledger_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

