-- Migration 034: Session Reuse Lookup Index (2026-06-26)
-- Purpose: Speed up FindRecentGatewaySession, which queries
--   request_logs by (tenant_id, api_key_id, identity_hash, ts DESC)
--   filtered to gateway-generated sessions (gw_ prefix).
--
-- Query (telemetry/client.go:FindRecentGatewaySession):
--   SELECT gw_session_id FROM request_logs
--   WHERE tenant_id = $1 AND api_key_id = $2
--     AND identity_hash = $3 AND gw_session_id LIKE 'gw\_%'
--     AND ts >= NOW() - ($4 * INTERVAL '1 second')
--   ORDER BY ts DESC LIMIT 1
--
-- Existing idx_request_logs_gw_session_ts indexes
-- (gw_session_id, ts DESC) — does not match this query because the
-- filter keys are (tenant_id, api_key_id, identity_hash).
--
-- This is a partial index that excludes empty/non-gw session rows to
-- keep the index small. Escapes the underscore in 'gw\_%' so the LIKE
-- pattern only matches rows with a real gw_<uuid> session id.

CREATE INDEX IF NOT EXISTS idx_request_logs_session_reuse
    ON request_logs (tenant_id, api_key_id, identity_hash, ts DESC)
    WHERE gw_session_id LIKE 'gw\_%' AND gw_session_id IS NOT NULL;
