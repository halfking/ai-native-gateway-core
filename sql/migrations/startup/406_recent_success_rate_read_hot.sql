-- =============================================================================
-- Migration 406: Point recent_success_rate() at request_logs_hot
-- Created: 2026-07-15
-- Author:  gateway maintainers (glm-5.2 routing skew incident)
--
-- Root cause:
--   recent_success_rate(credential_id, raw_model, sample_n, window_hours)
--   queries the `request_logs` partitioned parent table, but the gateway only
--   ever INSERTs into `request_logs_hot` (telemetry/client.go:661). Rows are
--   promoted from hot → partitioned only after the 7-day retention window
--   (promote_request_logs_hot_to_partition, retention='7 days'). The function
--   uses a 3-HOUR window, so EVERY row it could match still lives in the hot
--   table — the partitioned `request_logs` is empty for that window by design.
--   Result: recent_success_rate() returned (NULL, 0) for every candidate,
--   the live success-rate tiebreaker in loadCandidatesDB (provider/client.go
--   ORDER BY ... COALESCE(rsr.rate, mo.success_rate, 0.9) DESC) was inert,
--   and the router fell back to the static 0.9 column for all candidates —
--   defeating any success-based ordering within a billing_mode rank.
--
--   This is the second half of the glm-5.2 routing skew fix. After migration
--   403 promoted glm-xianyu (100% success) into rank 1, the success-rate
--   tiebreaker is what should have ordered it above the 28.5%-success NIM
--   `endless`. Without this fix, rank-1 members tie at 0.9 and ordering is
--   non-deterministic, so traffic still sticks to whichever row the planner
--   returns first.
--
-- Fix:
--   1. Rewrite recent_success_rate() to read request_logs_hot instead of
--      request_logs. The hot table holds the most recent 7 days, so a
--      3-hour window is always fully covered. (When p_window_hours exceeds
--      the hot retention, the older rows are simply gone — acceptable since
--      the window default is 3h.)
--   2. Add a composite index (credential_id, lower(outbound/client model),
--      ts DESC) on request_logs_hot so the per-candidate LATERAL lookup is an
--      index descent, not a scan. The existing partial indexes key on ts only.
--
-- Idempotency:
--   CREATE OR REPLACE FUNCTION is idempotent. CREATE INDEX IF NOT EXISTS is
--   idempotent. Safe as a startup migration.
--
-- Rollback:
--   DROP INDEX IF EXISTS idx_request_logs_hot_credential_model_ts;
--   then restore the previous function body (reads request_logs) if needed.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 406 recent_success_rate → request_logs_hot ==='

-- ---------------------------------------------------------------------------
-- 1. Pre-flight: show the (broken) current behaviour for a known-busy pair.
--    zhipu-roocode-v2 / glm-5.2 has 374 rows in the last 48h but the function
--    should currently return samples=0 because request_logs is empty for 3h.
-- ---------------------------------------------------------------------------
\echo '--- 1. pre-flight: recent_success_rate on request_logs (expect samples=0) ---'
SELECT recent_success_rate(c.id, 'glm-5.2', 50) AS before
FROM credentials c
WHERE c.label = 'zhipu-roocode-v2'
LIMIT 1;

-- ---------------------------------------------------------------------------
-- 2. Rewrite the function to read request_logs_hot.
--    Signature is unchanged (4-arg, window default 3h) so every caller —
--    notably the 3-arg LATERAL call in provider/client.go:858 that relies on
--    the p_window_hours default — keeps working without a Go change.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION recent_success_rate(
    p_credential_id BIGINT,
    p_raw_model     TEXT,
    p_sample_n      INT DEFAULT 50,
    p_window_hours  INT DEFAULT 3
)
RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT success
        FROM request_logs_hot
        WHERE credential_id = p_credential_id
          -- case-insensitive: request_logs_hot.outbound_model can differ in
          -- case from model_offers.raw_model_name (e.g. "MiniMax-M3" vs
          -- "minimax-m3"). Fall back to client_model when outbound is NULL.
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM recent;
$$;

COMMENT ON FUNCTION recent_success_rate(bigint, text, int, int) IS
    'Success fraction over the most recent p_sample_n rows within p_window_hours for a (credential, model) pair. Reads request_logs_hot (where live rows live; request_logs partition parent only holds >7d-old promoted rows). Returns samples=0/rate=NULL when empty. Used by loadCandidatesDB last-N gate and success-rate tiebreaker.';

-- ---------------------------------------------------------------------------
-- 3. Add a composite index so the LATERAL per-candidate lookup is indexed.
--    The expression index matches the function's WHERE predicate exactly:
--    (credential_id, lower(COALESCE(outbound_model, client_model)), ts DESC).
--    One descent yields the recent-N rows for a candidate without a scan.
--    Estimated size is modest: request_logs_hot holds ~7 days of traffic.
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_credential_model_ts
    ON request_logs_hot (credential_id,
                         lower(COALESCE(outbound_model, client_model)),
                         ts DESC);

-- ---------------------------------------------------------------------------
-- 4. Post-flight: the same busy pair should now return real samples/rate.
-- ---------------------------------------------------------------------------
\echo '--- 4. post-flight: recent_success_rate on request_logs_hot (expect samples>0) ---'
SELECT recent_success_rate(c.id, 'glm-5.2', 50) AS after
FROM credentials c
WHERE c.label = 'zhipu-roocode-v2'
LIMIT 1;

\echo '--- 4b. post-flight: all glm-5.2 candidates with live success rate ---'
SELECT
    p.code  AS provider,
    c.label AS cred,
    rsr.rate,
    rsr.samples
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p   ON p.id = c.provider_id
CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50) AS rsr
WHERE mo.canonical_raw_name = 'glm-5.2'
  AND mo.available = TRUE
ORDER BY COALESCE(rsr.rate, -1) DESC, p.code;

\echo '=== 406 recent_success_rate → request_logs_hot: done ==='
