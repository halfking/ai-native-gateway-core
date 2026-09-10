--
-- Name: promote_routing_decision_log_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_routing_decision_log_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', ts) AS month_start
    FROM public.routing_decision_log_hot
    WHERE ts < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_routing_decision_log_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT request_id, ts FROM public.routing_decision_log_hot
    WHERE ts < now() - p_retention
    ORDER BY ts, request_id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.routing_decision_log_hot h USING batch b
    WHERE h.request_id = b.request_id AND h.ts = b.ts
    RETURNING h.ts, h.request_id, h.idempotency_key, h.tenant_id, h.api_key_id,
      h.model, h.chosen_credential_id, h.chosen_provider_id, h.tier,
      h.candidates_tried, h.latency_ms, h.success, h.error_class,
      h.prompt_tokens, h.completion_tokens, h.cost_usd, h.request_bytes,
      h.response_bytes, h.client_model, h.resolved_raw_model, h.sticky_hit,
      h.client_profile, h.outbound_model, h.request_mode, h.identity_hash,
      h.transform_rule_id, h.egress_protocol, h.failure_stage,
      h.failure_detail_code, h.virtual_client_id, h.virtual_ip, h.virtual_mac,
      h.resolution_path, h.canonical_model, h.resolution_raw_models, h.decision_trace
  ), inserted AS (
    INSERT INTO public.routing_decision_log (
      ts, request_id, idempotency_key, tenant_id, api_key_id,
      model, chosen_credential_id, chosen_provider_id, tier,
      candidates_tried, latency_ms, success, error_class,
      prompt_tokens, completion_tokens, cost_usd, request_bytes,
      response_bytes, client_model, resolved_raw_model, sticky_hit,
      client_profile, outbound_model, request_mode, identity_hash,
      transform_rule_id, egress_protocol, failure_stage,
      failure_detail_code, virtual_client_id, virtual_ip, virtual_mac,
      resolution_path, canonical_model, resolution_raw_models, decision_trace)
    SELECT ts, request_id, idempotency_key, tenant_id, api_key_id,
      model, chosen_credential_id, chosen_provider_id, tier,
      candidates_tried, latency_ms, success, error_class,
      prompt_tokens, completion_tokens, cost_usd, request_bytes,
      response_bytes, client_model, resolved_raw_model, sticky_hit,
      client_profile, outbound_model, request_mode, identity_hash,
      transform_rule_id, egress_protocol, failure_stage,
      failure_detail_code, virtual_client_id, virtual_ip, virtual_mac,
      resolution_path, canonical_model, resolution_raw_models, decision_trace
    FROM moved_rows
    RETURNING request_id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;
