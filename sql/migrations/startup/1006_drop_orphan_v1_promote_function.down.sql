-- Migration 1006 down: restore the 528 shape of the promote function so the
-- pre-1006 lineage (528 applied, 1006 not) keeps replaying byte-identically.
-- The body below is the 528 up-side definition (expired-first cleanup), kept
-- verbatim from 528_request_logs_bodies_expired_hot_cleanup.sql.

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

CREATE OR REPLACE FUNCTION promote_request_logs_bodies_hot_to_partition(
  p_retention interval DEFAULT '7 days',
  p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
  v_processed bigint := 0;
  v_ttl_days int := 7;
BEGIN
  SELECT CASE jsonb_typeof(value)
           WHEN 'number' THEN value::text::int
           WHEN 'string' THEN trim(both '"' from value::text)::int
           ELSE 7
         END
    INTO v_ttl_days
    FROM settings_kv
   WHERE key = 'lifecycle.request_logs_bodies_ttl_days'
     AND scope = 'platform'
   LIMIT 1;

  v_ttl_days := GREATEST(COALESCE(v_ttl_days, 7), 1);

  WITH expired_batch AS (
    SELECT request_id
      FROM request_logs_bodies_hot
     WHERE ts < now() - make_interval(days => v_ttl_days)
       AND ts < now() - p_retention
     ORDER BY ts
     LIMIT p_batch_size
  ),
  expired_deleted AS (
    DELETE FROM request_logs_bodies_hot
     WHERE request_id IN (SELECT request_id FROM expired_batch)
    RETURNING request_id
  )
  SELECT count(*) INTO v_processed FROM expired_deleted;

  IF v_processed > 0 THEN
    RETURN v_processed;
  END IF;

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

  GET DIAGNOSTICS v_processed = ROW_COUNT;
  RETURN v_processed;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '1006';

COMMIT;
