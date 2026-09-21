-- Down migration 680: remove the bootstrapped canonical view.
--
-- The wrapper views are intentionally preserved: they are the wrapper chain
-- created by migrations 577/610, and dropping them here would make a later
-- 680 re-bootstrap rebuild the base view instead of reusing it. This down
-- only removes what 680's final stage added.

BEGIN;

DROP VIEW IF EXISTS public.request_logs_with_current_month;

COMMIT;
