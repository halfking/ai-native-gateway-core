-- 416_reconcile_node_probe_bindings.sql
-- Purpose: Remove stale failed node-probe bindings from the routing view.
-- Idempotent: YES
-- Rollback: Restore affected credential_model_bindings rows manually after
-- reviewing node_probe_state and the provider credential health.

UPDATE credential_model_bindings cmb
SET available = FALSE,
    unavailable_reason = 'probe_' || COALESCE(nps.last_err_code, 'failed'),
    unavailable_at = COALESCE(nps.last_attempt_at, now()),
    unavailable_recover_at = nps.next_retry_at,
    updated_at = now()
FROM provider_models pm, node_probe_state nps
WHERE cmb.provider_model_id = pm.id
  AND nps.credential_id = cmb.credential_id
  AND nps.raw_model_name = pm.raw_model_name
  AND nps.last_direct_ok = FALSE
  AND nps.consecutive_failures > 0
  AND nps.next_retry_at > now()
  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%';
