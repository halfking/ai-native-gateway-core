-- Migration 528 down: restore the original batch promote behavior, which
-- only moves rows into the monthly partition and never deletes expired rows.

BEGIN;

CREATE OR REPLACE FUNCTION promote_request_logs_bodies_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
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
  INSERT INTO request_logs_bodies
    (request_id, ts, request_body, outbound_body, response_body)
  SELECT request_id, ts, request_body, outbound_body, response_body
    FROM deleted;

  GET DIAGNOSTICS v_moved = ROW_COUNT;
  RETURN v_moved;
END;
$$;

COMMIT;
