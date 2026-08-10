-- Migration 478 down: remove the auto-route feedback loop tables.
--
-- Note: dropping task_model_affinity discards learned rankings. The loop can
-- relearn from auto_route_selections, but that table is dropped here too, so
-- this is a full data loss for the feature. To disable the feature WITHOUT
-- losing history, set AUTO_AFFINITY_MODE=off instead of running this.
--
-- Partitions are dropped implicitly by CASCADE on the parent.

\set ON_ERROR_STOP on
BEGIN;

DROP VIEW  IF EXISTS public.v_task_model_ranking;
DROP TABLE IF EXISTS public.task_model_affinity CASCADE;
DROP TABLE IF EXISTS public.auto_route_selections CASCADE;

COMMIT;
