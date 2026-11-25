\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
DROP TRIGGER IF EXISTS invalidate_request_metrics_history_day ON public.request_metrics_daily_state;
DROP FUNCTION IF EXISTS public.invalidate_request_metrics_history_day();
DROP TABLE IF EXISTS public.request_metrics_daily_history;
DROP TABLE IF EXISTS public.request_metrics_history_days;
DELETE FROM public.schema_migrations WHERE version='983g';
COMMIT;
