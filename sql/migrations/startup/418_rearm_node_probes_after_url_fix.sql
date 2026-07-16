-- 418_rearm_node_probes_after_url_fix.sql
-- Purpose: Re-run failed node probes after correcting shared upstream URL
-- construction. This is intentionally limited to non-manual bindings.
-- Idempotent: YES
-- Rollback: Restore each affected node_probe_state row from its audit history.

UPDATE node_probe_state nps
SET consecutive_failures = 0,
    next_retry_at = now() + interval '5 seconds',
    next_retry_seconds = 5,
    paused = FALSE,
    in_flight_until = NULL,
    last_err_code = NULL,
    last_err_detail = NULL,
    updated_at = now()
WHERE nps.last_direct_ok = FALSE
  AND nps.next_retry_at > now()
  AND EXISTS (
      SELECT 1
      FROM credential_model_bindings cmb
      JOIN provider_models pm ON pm.id = cmb.provider_model_id
      WHERE cmb.credential_id = nps.credential_id
        AND pm.raw_model_name = nps.raw_model_name
        AND COALESCE(cmb.admin_protected, FALSE) = FALSE
        AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
  );
