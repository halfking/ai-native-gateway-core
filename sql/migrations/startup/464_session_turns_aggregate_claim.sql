-- Migration 464: durable Session V2 aggregate idempotency claim
--
-- A session_turns row is inserted before the asynchronous sessions snapshot
-- update. Presence of the row therefore cannot mean "already aggregated".
-- This nullable marker is claimed in the same transaction as the sessions
-- upsert so retries remain safe and failures remain retryable.

BEGIN;

ALTER TABLE public.session_turns
    ADD COLUMN IF NOT EXISTS aggregate_applied_at TIMESTAMPTZ;

COMMENT ON COLUMN public.session_turns.aggregate_applied_at IS
    'Timestamp at which this turn was atomically applied to public.sessions aggregate counters; NULL means pending/retryable.';

COMMIT;
