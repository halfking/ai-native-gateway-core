-- Migration 470 down: remove cache_metrics table

BEGIN;

DROP TABLE IF EXISTS public.cache_metrics CASCADE;

COMMIT;
