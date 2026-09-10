--
-- Name: promote_credential_model_index_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_credential_model_index_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- Rows route by bucket, so pre-ensure the months the moved rows will land in.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', bucket) AS month_start
    FROM public.credential_model_index_hot
    WHERE updated_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_credential_model_index_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT bucket, credential_id, raw_model FROM public.credential_model_index_hot
    WHERE updated_at < now() - p_retention
    ORDER BY updated_at, bucket, credential_id, raw_model LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.credential_model_index_hot h USING batch b
    WHERE h.bucket = b.bucket AND h.credential_id = b.credential_id AND h.raw_model = b.raw_model
    RETURNING h.bucket, h.credential_id, h.raw_model, h.canonical_id, h.billing_mode,
      h.unit_price_in_per_1m, h.unit_price_out_per_1m, h.context_window,
      h.success_rate, h.p95_latency_ms, h.active_sessions, h.concurrency_limit,
      h.pressure_ratio, h.score_smart, h.score_speed_first, h.score_cost_first, h.updated_at
  ), inserted AS (
    INSERT INTO public.credential_model_index (
      bucket, credential_id, raw_model, canonical_id, billing_mode,
      unit_price_in_per_1m, unit_price_out_per_1m, context_window,
      success_rate, p95_latency_ms, active_sessions, concurrency_limit,
      pressure_ratio, score_smart, score_speed_first, score_cost_first, updated_at)
    SELECT bucket, credential_id, raw_model, canonical_id, billing_mode,
      unit_price_in_per_1m, unit_price_out_per_1m, context_window,
      success_rate, p95_latency_ms, active_sessions, concurrency_limit,
      pressure_ratio, score_smart, score_speed_first, score_cost_first, updated_at
    FROM moved_rows
    RETURNING credential_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;
