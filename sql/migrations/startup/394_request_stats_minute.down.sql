BEGIN;

DROP TABLE IF EXISTS public.request_stats_error_drill_minute;
DROP TABLE IF EXISTS public.request_stats_dim_minute;
DROP TABLE IF EXISTS public.request_stats_minute;
DROP TABLE IF EXISTS public.request_stats_rollup_cursor;

COMMIT;
