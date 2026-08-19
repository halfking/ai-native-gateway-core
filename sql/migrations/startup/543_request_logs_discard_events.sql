-- 543_request_logs_discard_events.sql
-- 2026-08-19: streaming observability. Persist the audit
-- StreamCapture.DiscardEvents JSONB on request_logs so operators can
-- reconstruct every "Partial assistant output was discarded before a
-- streaming retry" event with a single SQL query, joining it against
-- request_id / provider_id / error_kind.
--
-- Discard events are appended by the survival coordinator before every
-- AttemptCommitGate.Discard() (streaming/survival_coordinator.go), by
-- L1 holdback-window failures (streaming/stream_recovery.go), and by the
-- empty-stream content gate (streaming/stream.go). The field is also
-- surfaced through the application log line "survival_attempt_discarded"
-- — operators who lack SQL access can still post-mortem from the
-- gateway.log without the column.
--
-- The column is intentionally nullable: rows written before this
-- migration have NULL.

ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS discard_events JSONB;

ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS discard_events JSONB;

-- GIN index on the hot table supports the high-value "give me all
-- discards where raw_model = X" / "give me all discards where
-- decision_action = Y" query shapes. jsonb_path_ops is the smallest,
-- fastest GIN opclass and supports only @> containment.
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_discard_events_gin
    ON public.request_logs_hot USING GIN (discard_events jsonb_path_ops);

-- BRIN does not support jsonb out of the box (no default operator
-- class). A btree on (ts DESC, discard_events) gives the planner a
-- cheap column-leading sort for the rare full-scan aggregator query
-- (e.g. weekly aggregate over 30 days of partitions). Partial index
-- keeps it small: only rows that ever recorded a discard event enter.
CREATE INDEX IF NOT EXISTS idx_request_logs_discard_events_ts
    ON public.request_logs (ts DESC)
    WHERE discard_events IS NOT NULL;

COMMENT ON COLUMN public.request_logs.discard_events IS
    '2026-08-19: JSONB array of streaming-discard events the survival / recovery / empty-gate paths recorded. Each entry: {reason, buffer_bytes, holdback_held, state, attempt_number, provider_id, raw_model, decision_action, decision_reason, recorded_at}. Mirrors the in-process audit StreamCapture.DiscardEvents so operators can reconstruct partial-discard events without joining application logs.';
COMMENT ON COLUMN public.request_logs_hot.discard_events IS
    '2026-08-19: same as request_logs.discard_events, on the hot table. GIN-indexed for raw_model / decision_action containment queries.';