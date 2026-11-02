-- Down for 930: drop task_events.
BEGIN;
DROP TABLE IF EXISTS public.task_events;
DELETE FROM public.schema_migrations WHERE version = '930';
COMMIT;
