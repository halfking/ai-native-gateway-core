--
-- Name: promote_tool_usage_stats_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_tool_usage_stats_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- Rows route by created_at, so pre-ensure those months; the retention
  -- predicate itself stays on usage_date exactly as in migration 348.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.tool_usage_stats_hot
    WHERE usage_date < CURRENT_DATE - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_tool_usage_stats_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT tool_id, tenant_id, usage_date FROM public.tool_usage_stats_hot
    WHERE usage_date < CURRENT_DATE - p_retention
    ORDER BY usage_date, tool_id, tenant_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.tool_usage_stats_hot h USING batch b
    WHERE h.tool_id = b.tool_id AND h.tenant_id = b.tenant_id AND h.usage_date = b.usage_date
    RETURNING h.id, h.tool_id, h.tenant_id, h.usage_date, h.call_count,
      h.success_count, h.error_count, h.avg_latency_ms, h.last_called_at,
      h.created_at, h.updated_at
  ), inserted AS (
    INSERT INTO public.tool_usage_stats (
      id, tool_id, tenant_id, usage_date, call_count,
      success_count, error_count, avg_latency_ms, last_called_at,
      created_at, updated_at)
    SELECT id, tool_id, tenant_id, usage_date, call_count,
      success_count, error_count, avg_latency_ms, last_called_at,
      created_at, updated_at
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;
