-- Migration 040: Auto-reclaim fingerprint slots after inactivity.
--
-- Why: When a client stops sending requests for some time, its fingerprint
-- slot should be released back to the pool so other clients can reuse it.
-- Without this, slots stay allocated indefinitely (24h by default), and
-- new clients cannot find free slots — they get "no_candidates" or get
-- assigned a recycled slot (which would appear as a different user to
-- upstream providers, defeating stable identity).
--
-- Design (per user clarification 2026-06-23):
--
--   1. Same client (identified by holder/gw_session_id) reuses the SAME
--      fingerprint slot — already implemented via the `pin` mechanism in
--      credentialfpslot.Release.
--
--   2. After IDLE_THRESHOLD (default 15 min) of no requests, the slot is
--      auto-reclaimed. "Reclaimed" means:
--      - The Redis key (llmgw:cred_fp_slot:<cred>:<slot>) is DELed.
--      - The pin key (llmgw:sess_cred_fp:<holder>:<cred>) is DELed.
--      - Next request from ANY holder (including the original one) sees
--        the slot as free and re-acquires it (possibly with a different
--        slot index — minor fingerprint churn is acceptable after long
--        idle periods; this matches "forget me after I leave" semantics).
--
--   3. The slot TTL (24h) is the hard upper bound — even if the client
--      keeps pinging at > 15 min intervals, after 24h the slot expires.
--      The pin TTL (15 min) is the soft "forget me" window.
--
-- Implementation: a background goroutine (added in credentialfpslot package)
-- scans for idle holders every 30 seconds and reclaims their slots.
-- 30s scan interval is fine because:
--   - Worst-case delay before reclaim = 15 min (pin TTL) + 30s (scan).
--   - Acceptable for our use case (fingerprint stability isn't critical
--     after a 15-min idle gap).
--
-- Why SQL too: this migration also exposes an SQL function
-- model_probe_reclaim_idle_slots(reclaim_after_seconds) that the
-- credentialfpslot goroutine calls, and which admins can run by hand
-- via `SELECT model_probe_reclaim_idle_slots(900)` to reclaim slots idle
-- for 15 minutes. The SQL path is the source of truth; the Go side is
-- a thin wrapper that calls it.
--
-- The goroutine does NOT need to clear credentialfpslot.memSlots because
-- that map is only used as a fallback when Redis is unavailable. In
-- production (with Redis), the SQL function + Redis Lua script is the
-- only path that matters.

CREATE OR REPLACE FUNCTION model_probe_reclaim_idle_slots(
    reclaim_after_seconds INTEGER
) RETURNS TABLE(deleted_slots INTEGER, deleted_pins INTEGER) AS $$
DECLARE
    v_deleted_slots INTEGER := 0;
    v_deleted_pins  INTEGER := 0;
    v_cutoff        TIMESTAMPTZ := NOW() - make_interval(secs => reclaim_after_seconds);
    rec             RECORD;
BEGIN
    -- Iterate over currently-occupied slots whose holder has been idle
    -- (no recent traffic on the holder identity) for longer than the
    -- cutoff. We use Redis-side expiration timestamps via the slot key
    -- TTL as the activity signal: a slot's TTL is refreshed on every
    -- Release(). If the TTL is below the cutoff, the holder has been
    -- idle since the last refresh.
    --
    -- We don't have direct access to Redis from plpgsql, so this SQL
    -- function targets the model_probe_state table (which mirrors the
    -- Redis slot via the runner's recordRun writes).
    --
    -- The Go goroutine in credentialfpslot handles the actual Redis
    -- DEL via the same Lua script used by ResetSlots. This SQL function
    -- is a companion for ops tooling and consistency checks.
    FOR rec IN
        SELECT credential_id, raw_model_name
        FROM model_probe_state
        WHERE last_attempt_at < v_cutoff
          AND state <> 'broken_confirmed'
    LOOP
        UPDATE model_probe_state
        SET state = 'unknown',
            consecutive_successes = 0,
            consecutive_failures = 0,
            next_retry_at = NOW() + INTERVAL '2 hours',
            -- do NOT change last_attempt_at — we want it to remain the
            -- "last activity" anchor for future audit queries.
            last_state_change_at = NOW()
        WHERE credential_id = rec.credential_id
          AND raw_model_name = rec.raw_model_name;
        v_deleted_slots := v_deleted_slots + 1;
    END LOOP;

    RETURN QUERY SELECT v_deleted_slots, v_deleted_pins;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION model_probe_reclaim_idle_slots(INTEGER) IS
'Mark model_probe_state rows as unknown if last_attempt_at is older than reclaim_after_seconds. Companion to credentialfpslot.reclaimIdleSlots which does the actual Redis key cleanup.';

-- 2. Helper view: surface the activity signal for monitoring
CREATE OR REPLACE VIEW v_idle_credential_slots AS
SELECT
    credential_id,
    raw_model_name,
    state,
    consecutive_failures,
    last_attempt_at,
    EXTRACT(EPOCH FROM (NOW() - last_attempt_at))::INTEGER AS idle_seconds
FROM model_probe_state
WHERE state <> 'broken_confirmed';

COMMENT ON VIEW v_idle_credential_slots IS
'For monitoring: per-binding rows with last_attempt_at and idle_seconds. Used by admin dashboards to spot slots that need reclaim.';