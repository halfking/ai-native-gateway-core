-- Migration 036: Add time window to recent_success_rate
-- Created: 2026-06-23
-- Purpose: Replace fixed 50-sample window with sliding time window (3 hours, max 50)
--   to prevent old failures from blocking recovery after fixes.
--
-- Problem:
--   Resource leak caused 54% failure rate. After fix deployed, old failures
--   still in the 50-sample window block routing with < 50% threshold.
--
-- Solution:
--   Only consider requests from last 3 hours. This allows system to recover
--   naturally as old failures age out of the window.

-- Drop old version
DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int);

-- Create new version with time window
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
        FROM request_logs
        WHERE credential_id = p_credential_id
          -- case-insensitive: request_logs.outbound_model can differ in case
          -- from model_offers.raw_model_name (e.g. "MiniMax-M3" vs "minimax-m3").
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
          -- 2026-06-23: add time window to allow recovery
          AND ts > NOW() - (p_window_hours || ' hours')::interval
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM recent;
$$;

COMMENT ON FUNCTION recent_success_rate(bigint, text, int, int) IS
    'Success fraction over the most recent p_sample_n request_logs rows within p_window_hours for a (credential, outbound_model) pair. Returns samples=0/rate=NULL when empty. Time window allows system to recover after fixes by aging out old failures.';
