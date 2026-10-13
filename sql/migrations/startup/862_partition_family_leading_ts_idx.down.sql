-- Down migration for 862: drop the two parent-level leading-ts indexes.
-- Partition child indexes drop with them (parent index DROP cascades).
-- Safe to re-run: IF EXISTS.

DROP INDEX IF EXISTS public.idx_session_turns_ts;
DROP INDEX IF EXISTS public.idx_request_logs_ts;
