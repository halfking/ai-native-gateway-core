\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
DROP TRIGGER IF EXISTS enqueue_request_metrics_turn ON public.session_turns_hot;
DROP TRIGGER IF EXISTS enqueue_request_metrics_context ON public.request_context_attrs;
DROP TRIGGER IF EXISTS enqueue_request_metrics_fact ON public.usage_facts;
DROP FUNCTION IF EXISTS public.enqueue_request_metrics_dimension();
DROP FUNCTION IF EXISTS public.enqueue_request_metrics_fact();
DROP TABLE IF EXISTS public.request_metrics_pending;
DROP TABLE IF EXISTS public.request_metrics_projection_state;
DROP TABLE IF EXISTS public.request_metrics_projection;
DELETE FROM public.schema_migrations WHERE version = '983d';
COMMIT;
