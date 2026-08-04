--
-- Name: promote_credit_ledger_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_credit_ledger_hot_to_partition(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM credit_ledger_hot
  WHERE created_at < now() - p_retention
  ORDER BY created_at
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM credit_ledger_hot
  WHERE id IN (SELECT id FROM _promote_hot_batch);

  BEGIN
    INSERT INTO credit_ledger
    SELECT * FROM _promote_hot_batch
    ON CONFLICT (id, created_at) DO NOTHING;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_credit_ledger_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

