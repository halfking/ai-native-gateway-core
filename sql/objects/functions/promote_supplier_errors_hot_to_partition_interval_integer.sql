--
-- Name: promote_supplier_errors_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_supplier_errors_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0;
BEGIN
  -- 703: group months under Asia/Shanghai so boundary rows land in the
  -- same month group the 699-pinned ensure_supplier_errors_partition
  -- target was created for (V371 deploy-track body, F13 closure).
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- Ensure target partitions exist for the cold rows' months; the month
  -- pre-ensure subquery must use the same predicate as the batch CTE.
  PERFORM ensure_supplier_errors_partition(m)
  FROM (
    SELECT DISTINCT date_trunc('month', occurred_at) AS m
    FROM supplier_errors_hot
    WHERE occurred_at < statement_timestamp() - p_retention
    LIMIT 12
  ) months;
  -- 2026-09-05 audit D-2#2 atomic single data-modifying CTE: FOR UPDATE
  -- SKIP LOCKED -> DELETE RETURNING (explicit columns, no schema-drift
  -- bitwise mismatch) -> INSERT; any failure rolls back the whole batch.
  WITH batch AS (
    SELECT id FROM supplier_errors_hot
    WHERE occurred_at < statement_timestamp() - p_retention
    ORDER BY occurred_at, id
    LIMIT p_batch_size
    FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM supplier_errors_hot h USING batch b
    WHERE h.id = b.id
    RETURNING h.id, h.occurred_at, h.request_id, h.trace_id, h.tenant_id,
      h.session_id, h.provider_id, h.supplier, h.credential_id,
      h.model, h.attempt_seq, h.error_type, h.error_code,
      h.http_status, h.error_message, h.is_retryable, h.stage,
      h.latency_ms, h.affected_users, h.request_metadata
  ), inserted AS (
    INSERT INTO supplier_errors (
      id, occurred_at, request_id, trace_id, tenant_id,
      session_id, provider_id, supplier, credential_id,
      model, attempt_seq, error_type, error_code,
      http_status, error_message, is_retryable, stage,
      latency_ms, affected_users, request_metadata)
    SELECT id, occurred_at, request_id, trace_id, tenant_id,
      session_id, provider_id, supplier, credential_id,
      model, attempt_seq, error_type, error_code,
      http_status, error_message, is_retryable, stage,
      latency_ms, affected_users, request_metadata
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;
