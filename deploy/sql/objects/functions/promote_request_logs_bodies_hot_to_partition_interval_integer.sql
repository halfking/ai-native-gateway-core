--
-- Name: promote_request_logs_bodies_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_request_logs_bodies_hot_to_partition(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
  v_moved bigint := 0;
BEGIN
  WITH batch AS (
    SELECT request_id, ts, request_body, outbound_body, response_body
    FROM request_logs_bodies_hot
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size
  ),
  deleted AS (
    DELETE FROM request_logs_bodies_hot
    WHERE request_id IN (SELECT request_id FROM batch)
    RETURNING *
  )
  INSERT INTO request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
  SELECT request_id, ts, request_body, outbound_body, response_body FROM deleted;

  GET DIAGNOSTICS v_moved = ROW_COUNT;
  RETURN v_moved;
END;
$$;

