-- Rollback for 854: drop the reconciled advisory-lock key function.
-- ⚠ Dropping it breaks the sessionv2mirror shadow write again (42883);
-- only run when rolling back the Go code as well.

DROP FUNCTION IF EXISTS public.session_turns_advisory_lock_key(TEXT, TEXT);
