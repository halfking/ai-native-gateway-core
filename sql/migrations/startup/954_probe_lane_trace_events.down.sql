-- Roll back the trace writer before removing its probe-lane column.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
ALTER TABLE public.session_turns_probe_hot DROP COLUMN IF EXISTS trace_events;
DELETE FROM public.schema_migrations WHERE version = '954';
COMMIT;
