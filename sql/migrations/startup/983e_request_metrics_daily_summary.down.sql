\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_truncate ON public.request_metrics_projection;
DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_delete ON public.request_metrics_projection;
DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_update ON public.request_metrics_projection;
DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_insert ON public.request_metrics_projection;
DROP FUNCTION IF EXISTS public.invalidate_all_request_metrics_daily();
DROP FUNCTION IF EXISTS public.invalidate_request_metrics_daily();
DROP TABLE IF EXISTS public.request_metrics_daily_session_masks;
DROP TABLE IF EXISTS public.request_metrics_daily_session_codes;
DROP TABLE IF EXISTS public.request_metrics_daily_summary;
DROP TABLE IF EXISTS public.request_metrics_daily_state;
DELETE FROM public.schema_migrations WHERE version='983e';
COMMIT;
