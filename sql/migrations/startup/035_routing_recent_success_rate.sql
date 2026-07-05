-- Migration 035: Routing recent success-rate gate + broken_confirmed backfill
-- Created: 2026-06-22
-- Purpose: Stop the routing candidate loader from selecting (credential, model)
--   pairs that are either (a) model_probe_state='broken_confirmed' or (b) have
--   a real recent-N success rate near zero. Closes the 4-defect chain where a
--   0%-success credential (demo-tokenplan / minimax-m3) kept re-entering the
--   candidate pool every ~2 minutes because availability_state was auto-restored
--   to 'ready' by credential_recovery while the per-model probe state and real
--   success rate were never consulted by the router.
--
-- Two parts:
--   (1) Backfill: propagate existing model_probe_state='broken_confirmed' rows
--       into credential_model_bindings.available=FALSE so the historical
--       bindings that were marked broken before the P4 propagation code
--       (2026-06-19) landed finally drop out of the candidate pool.
--   (2) Helper function recent_success_rate(cred_id, raw_model, sample_n)
--       returns (rate, samples) over the most recent sample_n request_logs
--       rows for the (credential, outbound_model) pair. Used by loadCandidatesDB
--       so the last-N gate is a single expression, not repeated sub-SELECTs.
--
-- Idempotent: safe to run repeatedly. Guards never overwrite manual/admin
-- unavailable_reason.

-- (1) Backfill: broken_confirmed → binding available=FALSE.
--     This mirrors bg/model_probe.go applyResult P4 propagation, but runs
--     once to cover every binding that reached broken_confirmed without the
--     P4 code present (the cred-11/minimax-m3 case from 2026-06-17).
UPDATE credential_model_bindings cmb
SET available          = FALSE,
    unavailable_reason = 'model_probe_broken',
    unavailable_at     = NOW()
FROM provider_models pm
WHERE cmb.provider_model_id = pm.id
  AND cmb.available = TRUE
  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
  AND EXISTS (
      SELECT 1 FROM model_probe_state mps
      WHERE mps.credential_id = cmb.credential_id
        AND mps.raw_model_name = pm.raw_model_name
        AND mps.state = 'broken_confirmed'
  );

-- (2) recent_success_rate helper.
--     Returns the success fraction over the most recent sample_n rows in
--     request_logs for the given (credential_id, outbound_model). Uses the
--     idx_request_logs_credential_ts (credential_id, ts DESC) index so the
--     LIMIT scan is a 50-row index descent, not a full-window aggregate.
--     Returns samples=0 / rate=NULL when there are no rows, so callers can
--     apply the min-sample gate (don't exclude cold-start pairs).
DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int);
CREATE FUNCTION recent_success_rate(p_credential_id BIGINT,
                                    p_raw_model    TEXT,
                                    p_sample_n     INT DEFAULT 50)
RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT success
        FROM request_logs
        WHERE credential_id = p_credential_id
          -- case-insensitive: request_logs.outbound_model can differ in case
          -- from model_offers.raw_model_name (e.g. "MiniMax-M3" vs "minimax-m3").
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM recent;
$$;

COMMENT ON FUNCTION recent_success_rate(bigint, text, int) IS
    'Success fraction over the most recent p_sample_n request_logs rows for a (credential, outbound_model) pair. Returns samples=0/rate=NULL when empty. Used by loadCandidatesDB last-N gate.';
