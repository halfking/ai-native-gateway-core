-- Migration 658 rollback: Remove structured feature columns

\set ON_ERROR_STOP on
BEGIN;

-- Drop columns from parent table
ALTER TABLE public.auto_route_selections
  DROP COLUMN IF EXISTS detected_language,
  DROP COLUMN IF EXISTS prompt_length_bucket,
  DROP COLUMN IF EXISTS context_length_bucket,
  DROP COLUMN IF EXISTS turn_count_bucket,
  DROP COLUMN IF EXISTS has_code_indicator,
  DROP COLUMN IF EXISTS has_math_indicator,
  DROP COLUMN IF EXISTS has_table_indicator,
  DROP COLUMN IF EXISTS has_multimedia_indicator,
  DROP COLUMN IF EXISTS intent_category,
  DROP COLUMN IF EXISTS domain_hint,
  DROP COLUMN IF EXISTS complexity_bucket,
  DROP COLUMN IF EXISTS latency_sensitive,
  DROP COLUMN IF EXISTS cost_sensitive,
  DROP COLUMN IF EXISTS feature_version,
  DROP COLUMN IF EXISTS content_hash;

-- Drop columns from hot table
ALTER TABLE public.auto_route_selections_hot
  DROP COLUMN IF EXISTS detected_language,
  DROP COLUMN IF EXISTS prompt_length_bucket,
  DROP COLUMN IF EXISTS context_length_bucket,
  DROP COLUMN IF EXISTS turn_count_bucket,
  DROP COLUMN IF EXISTS has_code_indicator,
  DROP COLUMN IF EXISTS has_math_indicator,
  DROP COLUMN IF EXISTS has_table_indicator,
  DROP COLUMN IF EXISTS has_multimedia_indicator,
  DROP COLUMN IF EXISTS intent_category,
  DROP COLUMN IF EXISTS domain_hint,
  DROP COLUMN IF EXISTS complexity_bucket,
  DROP COLUMN IF EXISTS latency_sensitive,
  DROP COLUMN IF EXISTS cost_sensitive,
  DROP COLUMN IF EXISTS feature_version,
  DROP COLUMN IF EXISTS content_hash;

-- Restore original all view
CREATE OR REPLACE VIEW public.auto_route_selections_all AS
SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash, 'hot'::text AS storage_tier
FROM public.auto_route_selections_hot
UNION ALL
SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash, 'parent'::text AS storage_tier
FROM public.auto_route_selections;

-- Restore original promote function
CREATE OR REPLACE FUNCTION public.promote_auto_route_selections_hot_to_partition(
  p_retention interval DEFAULT interval '8 hours', p_batch_size integer DEFAULT 5000)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN SELECT DISTINCT date_trunc('month', partition_date)::date AS month_start FROM public.auto_route_selections_hot WHERE ts < statement_timestamp() - p_retention ORDER BY 1 LIMIT 12 LOOP
    PERFORM public.ensure_auto_route_selections_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT id, partition_date FROM public.auto_route_selections_hot
    WHERE ts < statement_timestamp() - p_retention ORDER BY ts, id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.auto_route_selections_hot h USING batch b
    WHERE h.id = b.id AND h.partition_date = b.partition_date
    RETURNING h.id, h.request_id, h.session_id, h.task_id, h.tenant_id, h.ts, h.task_type, h.profile,
      h.classifier, h.confidence, h.canonical_id, h.chosen_model, h.candidate_rank, h.composite_score,
      h.affinity_score, h.affinity_applied, h.explore, h.fallback_used, h.success, h.latency_ms,
      h.cost_usd, h.reward, h.reward_source, h.settled_at, h.partition_date, h.experiment_id,
      h.treatment, h.assignment_version, h.assignment_key_hash
  ), inserted AS (
    INSERT INTO public.auto_route_selections (
      id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
      canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
      explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
      partition_date, experiment_id, treatment, assignment_version, assignment_key_hash)
    SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
      canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
      explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
      partition_date, experiment_id, treatment, assignment_version, assignment_key_hash FROM moved_rows
    ON CONFLICT DO NOTHING RETURNING id, partition_date
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '658';
COMMIT;
