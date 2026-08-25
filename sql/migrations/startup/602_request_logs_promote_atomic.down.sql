-- Down for 602: restore the pre-fix promote_request_logs_hot_to_partition.
--
-- WARNING: the restored body reintroduces the delete-before-insert loss
-- window fixed by 602 (see the incident note in the up migration). This file
-- exists for the mandatory down-migration convention and emergency rollback
-- only — do NOT apply it to the shared production PG.

\set ON_ERROR_STOP on

BEGIN;

CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
  n bigint := 0;
BEGIN
  CREATE TEMP TABLE _promote_hot_batch ON COMMIT DROP AS
  SELECT * FROM request_logs_hot
  WHERE ts < now() - p_retention
  ORDER BY ts
  LIMIT p_batch_size;

  GET DIAGNOSTICS n = ROW_COUNT;

  IF n = 0 THEN
    RETURN 0;
  END IF;

  DELETE FROM request_logs_hot
  WHERE id IN (SELECT id FROM _promote_hot_batch);

  BEGIN
    INSERT INTO request_logs
    SELECT * FROM _promote_hot_batch;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'promote_request_logs_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
    n := 0;
  END;

  RETURN n;
END;
$$;

COMMIT;
