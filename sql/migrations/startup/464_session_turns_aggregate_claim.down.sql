-- Roll back Migration 464.

BEGIN;

ALTER TABLE public.session_turns
    DROP COLUMN IF EXISTS aggregate_applied_at;

COMMIT;
