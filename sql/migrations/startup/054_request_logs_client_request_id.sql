-- Migration 054: request_logs.client_request_id (2026-06-26)
--
-- Bug context: A client (ZCode / Cursor) was observed reusing the same
-- X-Request-Id across retries. Because ChatHandler.ServeHTTP (and the
-- messages / responses / v2-pipeline handlers) had
--
--     requestID := r.Header.Get("X-Request-Id")
--     if requestID == "" { requestID = generateRequestID() }
--
-- the gateway reused the client-supplied ID as the PRIMARY request_id,
-- so each retry of the same logical HTTP request landed in request_logs
-- as a row sharing that ID. Five retries produced five rows, all with
-- success=true and the same outbound model — a misleading audit trail.
--
-- Fix: this migration adds client_request_id so we can preserve the
-- client-supplied X-Request-Id (debug / cross-system tracing) WITHOUT
-- using it as the primary key. The PRIMARY request_id (request_logs.request_id)
-- is now ALWAYS a server-generated UUID, set by the requestid middleware
-- and by the streaming handlers themselves.
--
-- Together with the gateway-side change
-- ("server always generates a new request_id, client ID goes to
-- client_request_id"), the cross-request pollution is fixed:
--   - 5 retries → 5 distinct request_id rows
--   - 5 retries → 5 identical client_request_id values (one per logical
--     client retry) — visible via the new index for debugging
--   - the existing gw_session_id column already correlates retries on
--     the same conversation
--
-- Idempotency: ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS.
-- The index is partial because the column is sparse — most rows will
-- leave it NULL (clients that did not send X-Request-Id).

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS client_request_id TEXT;

CREATE INDEX IF NOT EXISTS idx_request_logs_client_request_id
    ON request_logs (client_request_id, ts DESC)
    WHERE client_request_id IS NOT NULL;