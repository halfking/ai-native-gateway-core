\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
DROP TRIGGER IF EXISTS invalidate_request_metrics_history_detail_day ON public.request_metrics_daily_state;
DROP FUNCTION IF EXISTS public.invalidate_request_metrics_history_detail_day();
DROP TABLE IF EXISTS public.request_metrics_daily_tenant_model_history;
DROP TABLE IF EXISTS public.request_metrics_person_rank_packs;
DROP TABLE IF EXISTS public.request_metrics_person_rank_codes;
DROP TABLE IF EXISTS public.request_metrics_history_detail_days;
DELETE FROM public.schema_migrations WHERE version='983i';
COMMIT;
