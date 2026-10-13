-- Migration 861 (2026-10-14): rebuild candidate_failure_logs_with_current_month
-- with an EXPLICIT column list so it (a) absorbs the session_id column that 617
-- later added to both tables, and (b) stops depending on positional UNION of
-- SELECT * — the two tables' column ORDERS diverged (hot carries
-- aggregation_id at position 17 before session_id; the parent carries
-- aggregation_id last), which is exactly why this rebuild must not use SELECT *.
--
-- Background: PostgreSQL views snapshot SELECT *'s column list at creation.
-- Migration 392 created this view when both tables had 20 columns; 617 added
-- session_id to the tables afterwards and the view never picked it up.
-- SELECT session_id FROM the view failed with 42703, making
-- /api/candidate-failures a hard 500 (surfaced by the 2026-10-14 exhaustive
-- API sweep; admin/candidate_failure_handlers.go:193).
--
-- CREATE OR REPLACE VIEW cannot add columns to an existing view, hence
-- DROP + CREATE. Column list order follows candidate_failure_logs_hot.

BEGIN;

\set ON_ERROR_STOP on

DROP VIEW IF EXISTS candidate_failure_logs_with_current_month;
CREATE VIEW candidate_failure_logs_with_current_month AS
SELECT
    id,
    request_id,
    ts,
    tenant_id,
    credential_id,
    provider_id,
    raw_model_name,
    attempt_index,
    error_kind,
    error_message,
    upstream_status_code,
    upstream_response_body,
    upstream_response_preview,
    latency_ms,
    retryable,
    context,
    aggregation_id,
    session_id,
    per_attempt_latency_ms,
    extracted_upstream_status_code,
    diagnosed_error_kind
FROM candidate_failure_logs_hot
UNION ALL
SELECT
    id,
    request_id,
    ts,
    tenant_id,
    credential_id,
    provider_id,
    raw_model_name,
    attempt_index,
    error_kind,
    error_message,
    upstream_status_code,
    upstream_response_body,
    upstream_response_preview,
    latency_ms,
    retryable,
    context,
    aggregation_id,
    session_id,
    per_attempt_latency_ms,
    extracted_upstream_status_code,
    diagnosed_error_kind
FROM candidate_failure_logs;

COMMENT ON VIEW candidate_failure_logs_with_current_month IS
'Optimized query VIEW using hot table architecture.
- candidate_failure_logs_hot: independent hot table (default 24h retention)
- candidate_failure_logs: parent table (auto-aggregates all ATTACHED monthly partitions)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 392 (2026-07-13); rebuilt by 861 (2026-10-14) with an
explicit column list — SELECT * views do not track later ALTERs, and the two
tables column orders diverged (aggregation_id position).';

DO $$ BEGIN RAISE NOTICE 'Rebuilt candidate_failure_logs_with_current_month (861: explicit columns, absorbed session_id)'; END $$;

-- Postcondition: the view must now project session_id.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'candidate_failure_logs_with_current_month'
       AND column_name = 'session_id'
  ) THEN
    RAISE EXCEPTION 'migration 861: candidate_failure_logs_with_current_month still lacks session_id';
  END IF;
END
$$;

COMMIT;
