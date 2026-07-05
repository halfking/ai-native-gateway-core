-- 016_outbound_body.sql
-- v3 Session-level outbound body persistence (2026-06-19)
-- llm-gateway-go T23: 4 new columns on request_logs
--
-- Context:
--   The existing request_logs.request_body stores the CLIENT wire body
--   (what the caller sent). After v3 session-level compression, the GATEWAY
--   may rewrite the body before forwarding (delta-append + optional LLM
--   summary). This migration adds columns to track what was actually sent
--   to the upstream LLM.
--
-- New columns:
--   outbound_body          JSONB  — LLM wire body (may differ from request_body)
--   outbound_msg_count     INT    — message count in outbound_body
--   outbound_token_est     INT    — token estimate (3.5 chars/token heuristic)
--   outbound_msg_hashes    JSONB  — [{index, sha256}] per-message fingerprints
--                                    used by the next request's BuildOutboundMessages
--                                    LCS diff to find the incremental tail
--
-- compression_meta JSONB (existing, v7 013) gains two new application-layer
-- fields (no DDL needed — JSONB is schemaless):
--   summary_marker         TEXT   — "smm_v1:<sha256>" marks a compacted summary
--                                    message so it is skipped by the LCS diff
--   window_triggered       TEXT   — 'sliding_window_token|count|idle'
--
-- compression_strategy TEXT (existing) gains three new valid values (no DDL,
-- TEXT is unconstrained):
--   'sliding_window_token'  — proactive trigger: token threshold exceeded
--   'sliding_window_count'  — proactive trigger: message count exceeded
--   'sliding_window_idle'   — proactive trigger: idle timeout exceeded
--
-- RLS:
--   request_logs already has tenant_isolation_request_logs policy
--   (007_maas_billing.sql:121) that filters whole rows by tenant_id.
--   New columns are automatically covered — no policy rebuild needed.
--
-- Idempotency: ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS

-- ■ 1. New columns
ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS outbound_body       JSONB,
    ADD COLUMN IF NOT EXISTS outbound_msg_count  INT,
    ADD COLUMN IF NOT EXISTS outbound_token_est  INT,
    ADD COLUMN IF NOT EXISTS outbound_msg_hashes JSONB;

-- ■ 2. Indexes
-- L3 DB fallback query for SessionCache.GetOrLoad:
--   SELECT outbound_msg_hashes, outbound_body, compression_meta
--   FROM request_logs
--   WHERE gw_session_id = $1 AND tenant_id = $2 AND outbound_body IS NOT NULL
--   ORDER BY ts DESC LIMIT 1
CREATE INDEX IF NOT EXISTS idx_request_logs_session_outbound
    ON public.request_logs (gw_session_id, ts DESC)
    WHERE gw_session_id IS NOT NULL
      AND outbound_body IS NOT NULL;

-- Stats / alerting: count of requests that triggered proactive window compression
CREATE INDEX IF NOT EXISTS idx_request_logs_outbound_msg_count
    ON public.request_logs (tenant_id, ts DESC)
    WHERE outbound_msg_count IS NOT NULL
      AND outbound_msg_count > 0;

-- ■ 3. Comments
COMMENT ON COLUMN public.request_logs.outbound_body IS
    'v3 (2026-06-19): LLM wire body JSONB — what was actually forwarded to the
     upstream provider. NULL = no session compressor active (outbound == client).
     Differs from request_body when v3 session-level delta-append or proactive
     sliding-window summary rewrote the body before forwarding.';

COMMENT ON COLUMN public.request_logs.outbound_msg_count IS
    'v3 (2026-06-19): Message count inside outbound_body (including system).
     Compare to the client message count in request_body to measure delta.';

COMMENT ON COLUMN public.request_logs.outbound_token_est IS
    'v3 (2026-06-19): Estimated token count for outbound_body using the
     3.5 chars/token heuristic (same as compressor/estimator.go). Used to
     audit sliding-window threshold decisions in request_logs UI.';

COMMENT ON COLUMN public.request_logs.outbound_msg_hashes IS
    'v3 (2026-06-19): Per-message fingerprint array [{index, sha256}] for
     outbound_body messages. The next request with the same gw_session_id
     reads this column to run LCS diff and find the incremental message tail,
     enabling delta-append without full re-send of conversation history.';
