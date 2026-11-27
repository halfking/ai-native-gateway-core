\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
DROP TRIGGER IF EXISTS invalidate_request_metrics_board_day ON public.request_metrics_daily_state;
DROP FUNCTION IF EXISTS public.invalidate_request_metrics_board_day();
DROP TABLE IF EXISTS public.request_metrics_board_series;
DROP TABLE IF EXISTS public.request_metrics_board_keys;
DROP TABLE IF EXISTS public.request_metrics_board_providers;
DROP TABLE IF EXISTS public.request_metrics_board_pies;
DROP TABLE IF EXISTS public.request_metrics_board_days;
DELETE FROM public.schema_migrations WHERE version='983l';
COMMIT;
