--
-- Name: promote_tool_usage_stats_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_tool_usage_stats_hot_to_partition(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM tool_usage_stats_hot
  WHERE usage_date < CURRENT_DATE - p_retention::interval
  ORDER BY usage_date
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM tool_usage_stats_hot
  WHERE (tool_id, tenant_id, usage_date) IN (
    SELECT tool_id, tenant_id, usage_date FROM _promote_hot_batch
  );

  BEGIN
    INSERT INTO tool_usage_stats
    SELECT * FROM _promote_hot_batch
    ON CONFLICT (tool_id, tenant_id, usage_date) DO NOTHING;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_tool_usage_stats_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

