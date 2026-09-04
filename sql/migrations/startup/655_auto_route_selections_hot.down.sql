-- Migration 655 rollback: restore parent-table writes without losing hot rows.
-- Copy hot rows into the parent before dropping the hot heap. The INSERT is
-- explicit and conflict-safe; rows remain queryable through the parent.
\set ON_ERROR_STOP on
BEGIN;

DO $$
DECLARE month_rec record;
BEGIN
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', partition_date)::date AS month_start
    FROM public.auto_route_selections_hot
    ORDER BY 1
  LOOP
    PERFORM public.ensure_auto_route_selections_partition(month_rec.month_start);
  END LOOP;
END;
$$;

INSERT INTO public.auto_route_selections (
  id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash)
SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash
FROM public.auto_route_selections_hot
ON CONFLICT DO NOTHING;

DROP VIEW IF EXISTS public.auto_route_selections_all;
DROP FUNCTION IF EXISTS public.promote_auto_route_selections_hot_to_partition(interval, integer);
DROP FUNCTION IF EXISTS public.ensure_auto_route_selections_partition(date);
DROP TABLE IF EXISTS public.auto_route_selections_hot;
DELETE FROM public.schema_migrations WHERE version = '655';
COMMIT;
