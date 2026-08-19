BEGIN;

-- 2026-08-19: streaming observability. Persist the audit
-- StreamCapture.DiscardEvents JSONB on request_logs_hot so operators can
-- reconstruct every "Partial assistant output was discarded before a
-- streaming retry" event with a single SQL query, joining it against
-- request_id / provider_id / error_kind. The same column is also added
-- to the partitioned parent (request_logs) for query planner symmetry —
-- PostgreSQL 14+ propagates ADD COLUMN IF NOT EXISTS to children through
-- the catalog, but the partition parent carries the column metadata
-- anyway so future ALTERs inherit cleanly.

ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS discard_events JSONB;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS discard_events JSONB;

-- GIN index supports the high-value "give me all discards where
-- raw_model = X" / "give me all discards where decision_action = Y"
-- query shapes. Use jsonb_path_ops to keep the index small and lookups
-- fast for the @> containment operator (the only operator GIN supports
-- here anyway).
CREATE INDEX IF NOT EXISTS request_logs_hot_discard_events_gin
    ON request_logs_hot USING GIN (discard_events jsonb_path_ops);

-- Plain BRIN index on the partitioned parent is enough for the rare
-- full-scan aggregator query. NOT GIN — GIN on a partition parent of a
-- declarative-partitioned table is propagated through ATTACH/detach and
-- is rarely what you want.
CREATE INDEX IF NOT EXISTS request_logs_discard_events_brin
    ON request_logs USING BRIN (discard_events);

COMMIT;