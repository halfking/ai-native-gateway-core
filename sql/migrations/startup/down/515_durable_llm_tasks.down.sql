-- Roll back Migration 515. Projection and event children must be removed first.
BEGIN;

DROP TABLE IF EXISTS public.durable_pending_outbox;
DROP TABLE IF EXISTS public.durable_llm_task_events;
DROP TABLE IF EXISTS public.durable_llm_tasks;
DROP FUNCTION IF EXISTS public.reject_durable_task_event_mutation();

COMMIT;
