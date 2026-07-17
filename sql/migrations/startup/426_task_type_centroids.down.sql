-- 426_task_type_centroids.down.sql
-- Remove the M3 embedding shadow centroid table.

BEGIN;

DROP TABLE IF EXISTS public.task_type_centroids;

COMMIT;
