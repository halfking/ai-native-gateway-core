\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
DROP TRIGGER IF EXISTS invalidate_request_metrics_tenant_history_day ON public.request_metrics_daily_state;
DROP FUNCTION IF EXISTS public.invalidate_request_metrics_tenant_history_day();
DROP INDEX IF EXISTS public.idx_request_metrics_projection_person_history;
DROP TABLE IF EXISTS public.request_metrics_daily_tenant_history;
DROP TABLE IF EXISTS public.request_metrics_tenant_history_days;
DELETE FROM public.schema_migrations WHERE version='983n';
COMMIT;
