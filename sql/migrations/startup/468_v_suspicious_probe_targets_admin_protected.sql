-- Migration 468: v_suspicious_probe_targets — exclude admin-protected bindings
--
-- Manual offers registered via the free-pool/template admin endpoints are
-- flagged admin_protected=TRUE. Batch/auto refresh must not update those
-- records, and the suspicious probe must not pick them as targets either
-- (otherwise it spends upstream calls probing a manually-added model that
-- no auto path is allowed to touch).
--
-- This re-defines the view with an extra guard in the EXISTS binding clause:
--   AND COALESCE(cmb.admin_protected, FALSE) = FALSE
--
-- Safe to re-run: CREATE OR REPLACE VIEW is idempotent.

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
          AND COALESCE(cmb.admin_protected, FALSE) = FALSE
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
    -- 优先探测并发数少的凭据
    model_probe_credential_concurrency(mps.credential_id) ASC,
    -- 然后按等待时间排序
    mps.marked_suspicious_at ASC NULLS LAST,
    mps.next_retry_at ASC
LIMIT 100;

COMMENT ON VIEW v_suspicious_probe_targets IS 
    '待探测的 suspicious 状态模型列表，已过滤凭据并发限制（< 2）与 admin_protected 手工记录，按优先级排序';
