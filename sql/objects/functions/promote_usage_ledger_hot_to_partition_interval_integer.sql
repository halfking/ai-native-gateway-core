--
-- Name: promote_usage_ledger_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_usage_ledger_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- The month pre-ensure loop must use the same predicate as the batch CTE.
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.usage_ledger_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_usage_ledger_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, ts FROM public.usage_ledger_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.usage_ledger_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.ts = b.ts
    RETURNING h.request_id, h.ts, h.tenant_id, h.application_id, h.api_key_id,
      h.end_user_id, h.credential_id, h.provider_id, h.canonical_id, h.raw_model_name,
      h.prompt_tokens, h.completion_tokens, h.cache_read_tokens, h.cache_write_tokens,
      h.total_tokens, h.cost_usd, h.latency_ms, h.success, h.error_kind
  ), inserted AS (
    INSERT INTO public.usage_ledger (
      request_id, ts, tenant_id, application_id, api_key_id,
      end_user_id, credential_id, provider_id, canonical_id, raw_model_name,
      prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
      total_tokens, cost_usd, latency_ms, success, error_kind)
    SELECT request_id, ts, tenant_id, application_id, api_key_id,
      end_user_id, credential_id, provider_id, canonical_id, raw_model_name,
      prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
      total_tokens, cost_usd, latency_ms, success, error_kind
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;
