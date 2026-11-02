-- 928 down: drop probe-lane tables (only safe when writers are flagged off).

BEGIN;
SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '5min';

DROP TABLE IF EXISTS public.request_stats_probe_minute;
DROP TABLE IF EXISTS public.session_turns_probe_hot;

COMMIT;
