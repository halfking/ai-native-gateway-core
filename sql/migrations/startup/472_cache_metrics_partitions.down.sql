-- Migration 472 down: drop cache_metrics monthly + default partitions
--
-- Note: this does NOT drop the cache_metrics parent table or its columns
-- (those came from migration 470). It only removes the partitions created
-- by 472. The default partition will be re-created by the next re-run of
-- 472_cache_metrics_partitions.sql.

BEGIN;

DROP TABLE IF EXISTS public.cache_metrics_2026_09 CASCADE;
DROP TABLE IF EXISTS public.cache_metrics_2026_08 CASCADE;
DROP TABLE IF EXISTS public.cache_metrics_default CASCADE;

COMMIT;