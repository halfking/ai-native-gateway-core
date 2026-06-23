-- Migration 038: Adaptive probe scheduling based on failure age.
--
-- Why: The current backoff schedule (model_probe_backoff in migration 011)
-- is fixed at 30s/2m/5m/15m regardless of how long ago the binding failed.
-- For a binding that failed 1 hour ago, we still wait 15 minutes. For a
-- transient spike (30 successful requests after 3 failures), we still wait
-- the same backoff before testing. This caused the minimax-m3 06-23 incident:
-- during the request spike at 07:56-08:08, fp_slot contention caused 27
-- 'no_candidates' errors, and recovery took 60+ seconds because the binding
-- backoff was 5 minutes.
--
-- Goal: Make probe frequency proportional to the time since the last failure.
-- Newer failures need MORE frequent probing (still failing or recently
-- broke). Older failures need LESS frequent probing (transient spike settled
-- long ago, don't waste cycles).
--
-- Algorithm (model_probe_backoff_v2):
--   consecutive_failures  age_since_last_failure  next_retry_interval
--   ────────────────────  ────────────────────────  ───────────────────
--   0                      any                      2 hours (watchdog)
--   1                      < 5 min                  1 min
--   1                      5–30 min                 3 min
--   1                      30–60 min                10 min
--   1                      > 60 min                 30 min
--   2                      < 5 min                  2 min
--   2                      5–30 min                 5 min
--   2                      30–60 min                15 min
--   2                      > 60 min                 45 min
--   3                      any                      60 min (still recovering toward broken)
--
-- The function takes (consecutive_failures INTEGER, last_attempt_at TIMESTAMPTZ)
-- and returns the interval. We use last_attempt_at as the proxy for
-- "age since last failure" because consecutive_failures only increments on
-- failed probes, not on successful requests.
--
-- Additional: Add `next_retry_at` update from passive failure events.
-- When a request fails for a (cred, model) pair that's NOT yet in
-- `broken_confirmed`, the executor writes to candidate_failure_logs with
-- a ts. A new SQL function `model_probe_passive_boost()` adjusts
-- `next_retry_at` so the next probe happens sooner when many failures
-- accumulated recently (e.g., 5 failures in the last minute → boost).
--
-- Spec: 2026-06-23-adaptive-probe-scheduling
-- Mirrors: docs/2026-06-23-adaptive-probe-algorithm.md

-- v2 backoff: age-aware
CREATE OR REPLACE FUNCTION model_probe_backoff_v2(
    consecutive_failures INTEGER,
    last_attempt_at TIMESTAMPTZ
) RETURNS INTERVAL
LANGUAGE SQL
IMMUTABLE
AS $$
    WITH age AS (
        SELECT EXTRACT(EPOCH FROM (NOW() - COALESCE(last_attempt_at, NOW() - INTERVAL '1 hour'))) AS secs
    )
    SELECT CASE
        -- 0 failures → healthy_confirmed watchdog (every 2h)
        WHEN consecutive_failures <= 0 THEN INTERVAL '2 hours'

        -- 3+ failures → still recovering toward broken_confirmed
        WHEN consecutive_failures >= 3 THEN INTERVAL '60 minutes'

        -- 1 failure: ramp up frequency when fresh, taper when stale
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <   300 THEN INTERVAL '1 minute'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '3 minutes'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '10 minutes'
        WHEN consecutive_failures = 1                              THEN INTERVAL '30 minutes'

        -- 2 failures: same pattern but with longer floor
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <   300 THEN INTERVAL '2 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '5 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '15 minutes'
        WHEN consecutive_failures = 2                              THEN INTERVAL '45 minutes'

        -- 4+ failures: very rare, treat like 3+
        ELSE INTERVAL '60 minutes'
    END;
$$;

COMMENT ON FUNCTION model_probe_backoff_v2(INTEGER, TIMESTAMPTZ) IS
'Adaptive backoff: 0 fails = 2h watchdog; 1 fail ramps 1m→30m as the failure ages; 2 fails ramps 2m→45m; 3+ fails = 60m recovering.';

-- Passive-failure boost: when the executor records a candidate failure
-- for a (cred, model) that is NOT broken_confirmed, recompute next_retry_at
-- based on how many failures have happened in the last N minutes.
--
-- The runner calls this from its cycle() so the schedule reflects passive
-- signals, not only the periodic probe outcome.
--
-- Logic:
--   if recent_failures_in_5min >= 3 → schedule next probe in 30 seconds
--   else if recent_failures_in_5min >= 2 → schedule in 1 minute
--   else leave next_retry_at alone (backoff schedule handles it).
--
-- This is the key fix for the minimax-m3 incident: when 5 failures hit in
-- 30 seconds, the runner will try again in 30 seconds instead of waiting
-- 5 minutes for the next backoff tick.
CREATE OR REPLACE FUNCTION model_probe_passive_boost(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_now TIMESTAMPTZ
) RETURNS VOID
LANGUAGE SQL
AS $$
    DECLARE
        recent_count INTEGER;
        new_retry TIMESTAMPTZ;
    BEGIN
        SELECT COUNT(*) INTO recent_count
        FROM candidate_failure_logs
        WHERE credential_id = p_credential_id
          AND raw_model_name = p_raw_model_name
          AND ts > p_now - INTERVAL '5 minutes';

        IF recent_count >= 3 THEN
            new_retry := p_now + INTERVAL '30 seconds';
        ELSIF recent_count >= 2 THEN
            new_retry := p_now + INTERVAL '1 minute';
        ELSE
            -- No boost; leave existing schedule alone.
            RETURN;
        END IF;

        -- Only update if the new retry is sooner than the existing one.
        UPDATE model_probe_state mps
        SET next_retry_at = LEAST(COALESCE(mps.next_retry_at, new_retry), new_retry)
        WHERE mps.credential_id = p_credential_id
          AND mps.raw_model_name = p_raw_model_name
          AND COALESCE(mps.state, 'unknown') <> 'broken_confirmed';
    END;
$$;

COMMENT ON FUNCTION model_probe_passive_boost(BIGINT, TEXT, TIMESTAMPTZ) IS
'When a (cred, model) sees 2+ failures in 5 min via passive signals, pull next_retry_at forward to 30s–1m so the next cycle probes sooner.';

-- Helper view: surface "should this be probed now?" to the runner.
-- Replaces the inline SELECT in cycle() with a query against this view so
-- the WHERE clause is shared with the manual-recovery admin tool.
CREATE OR REPLACE VIEW v_adaptive_probe_targets AS
SELECT
    cmb.id AS binding_id,
    cmb.credential_id,
    pm.raw_model_name,
    COALESCE(mps.consecutive_failures, 0) AS consecutive_failures,
    COALESCE(mps.consecutive_successes, 0) AS consecutive_successes,
    COALESCE(mps.state, 'unknown') AS probe_state,
    mps.last_attempt_at,
    mps.next_retry_at,
    -- Composite score: how urgent is this binding to probe now?
    -- Lower score = probe sooner. Used to ORDER BY in the cycle().
    EXTRACT(EPOCH FROM (NOW() - COALESCE(mps.last_attempt_at, NOW() - INTERVAL '1 hour'))) AS age_secs,
    (
        SELECT COUNT(*)
        FROM candidate_failure_logs cfl
        WHERE cfl.credential_id = cmb.credential_id
          AND cfl.raw_model_name = pm.raw_model_name
          AND cfl.ts > NOW() - INTERVAL '5 minutes'
    ) AS recent_passive_failures
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN model_probe_state mps
       ON mps.credential_id = cmb.credential_id
      AND mps.raw_model_name = pm.raw_model_name
WHERE COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.availability_state, 'ready') NOT IN ('suspended')
  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
  AND COALESCE(p.enabled, FALSE) = TRUE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
  AND COALESCE(mps.state, 'unknown') <> 'broken_confirmed';

COMMENT ON VIEW v_adaptive_probe_targets IS
'Per-(cred, model) row with adaptive scheduling fields (age, recent failures). The model probe runner selects from this view, ordered by urgency.';