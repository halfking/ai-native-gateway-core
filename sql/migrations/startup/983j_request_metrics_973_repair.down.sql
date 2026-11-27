-- Retain the 973 projection, old queue, cursor and widened counters so that
-- rolling back to a 973 gateway never truncates or discards observed usage.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS enqueue_request_metrics_refresh_fact ON public.usage_facts;
DROP TRIGGER IF EXISTS enqueue_request_metrics_context ON public.request_context_attrs;
DROP TRIGGER IF EXISTS enqueue_request_metrics_turn ON public.session_turns_hot;
DROP FUNCTION IF EXISTS public.enqueue_request_metrics_refresh_fact();
DROP FUNCTION IF EXISTS public.enqueue_request_metrics_context();
DROP FUNCTION IF EXISTS public.enqueue_request_metrics_turn();
DROP VIEW IF EXISTS public.request_metrics_pending_events;
DROP VIEW IF EXISTS public.request_metrics_projection;
DROP TABLE IF EXISTS public.request_metrics_refresh_pending;
DROP TABLE IF EXISTS public.request_metrics_repair_state;
DROP INDEX IF EXISTS public.idx_request_metrics_projection_provider;
DROP INDEX IF EXISTS public.idx_request_metrics_projection_session;
DELETE FROM public.schema_migrations WHERE version = '983j';
COMMIT;
