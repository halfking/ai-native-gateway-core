-- Migration 468 down: restore v_suspicious_probe_targets to pre-468 shape
-- (drop the admin_protected guard from the binding EXISTS clause).
--
-- Restores the original definition from migration 301 (without the
-- admin_protected exclusion). Safe to re-run.

CREATE OR REPLACE VIEW v_suspicious_probe_targets AS
SELECT
    mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, '') AS outbound_model_name,
    COALESCE(p.base_url, '') AS base_url,
    COALESCE(p.protocol, 'openai-completions') AS protocol,
    mps.marked_suspicious_at,
    mps.next_retry_at,
    mps.consecutive_failures,
    mps.consecutive_successes,
    model_probe_credential_concurrency(mps.credential_id) AS credential_probe_count
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
    AND EXISTS (
        SELECT 1 FROM credential_model_bindings cmb
        WHERE cmb.credential_id = mps.credential_id
          AND cmb.provider_model_id = pm.id
    )
WHERE mps.state = 'suspicious'
  AND mps.next_retry_at <= NOW()
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(p.enabled, FALSE) = TRUE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND model_probe_credential_concurrency(mps.credential_id) < 2
ORDER BY 
    model_probe_credential_concurrency(mps.credential_id) ASC,
    mps.marked_suspicious_at ASC NULLS LAST,
    mps.next_retry_at ASC
LIMIT 100;
