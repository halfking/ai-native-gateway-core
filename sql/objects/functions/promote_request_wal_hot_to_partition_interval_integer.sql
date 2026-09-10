--
-- Name: promote_request_wal_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_request_wal_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.request_wal_hot
    WHERE created_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_request_wal_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, created_at FROM public.request_wal_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.request_wal_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.created_at = b.created_at
    RETURNING h.request_id, h.tenant_id, h.gw_session_id, h.status, h.stage,
      h.client_model, h.upstream_provider_id, h.upstream_credential_id,
      h.completion_tokens, h.prompt_tokens, h.created_at, h.completed_at,
      h.upstream_request_at, h.upstream_response_at, h.error,
      h.compression_strategy, h.compression_meta
  ), inserted AS (
    INSERT INTO public.request_wal (
      request_id, tenant_id, gw_session_id, status, stage,
      client_model, upstream_provider_id, upstream_credential_id,
      completion_tokens, prompt_tokens, created_at, completed_at,
      upstream_request_at, upstream_response_at, error,
      compression_strategy, compression_meta)
    SELECT request_id, tenant_id, gw_session_id, status, stage,
      client_model, upstream_provider_id, upstream_credential_id,
      completion_tokens, prompt_tokens, created_at, completed_at,
      upstream_request_at, upstream_response_at, error,
      compression_strategy, compression_meta
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;
