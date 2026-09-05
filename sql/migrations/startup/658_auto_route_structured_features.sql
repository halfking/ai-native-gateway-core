-- Migration 658: Add structured features to auto_route_selections
--
-- Ref: docs/auto-model-optimization/02-architecture-design.md
--      docs/auto-model-optimization/05-storage-optimization.md
--
-- Background:
--   The AUTO model optimization design requires storing structured features
--   (language, length buckets, complexity indicators) instead of prompt content.
--   These features are:
--   - Low-sensitivity, non-reversible, fixed schema values
--   - Used for ML training and human annotation without content exposure
--   - Versioned to allow schema evolution
--
-- This migration adds structured feature columns to both auto_route_selections
-- parent table and auto_route_selections_hot table. All columns are nullable
-- to allow gradual rollout and backward compatibility.
--
-- Prohibited: This migration does NOT add prompt, messages, response, summary,
-- keywords, truncated text, embeddings, or any reversible content features.

\set ON_ERROR_STOP on
BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- 1. Add structured feature columns to parent table auto_route_selections
-- ─────────────────────────────────────────────────────────────────────────
DO $$
BEGIN
  -- Language enumeration (detected, not stored content)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'detected_language'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN detected_language TEXT;
    COMMENT ON COLUMN public.auto_route_selections.detected_language IS
      'Detected language enum: zh, en, ja, mixed, etc. Non-reversible signal.';
  END IF;

  -- Prompt length bucket (logarithmic bins, not actual content)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'prompt_length_bucket'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN prompt_length_bucket TEXT;
    COMMENT ON COLUMN public.auto_route_selections.prompt_length_bucket IS
      'Length bucket: xs(<500), s(500-2k), m(2k-8k), l(8k-32k), xl(32k-128k), xxl(128k+). Non-reversible.';
  END IF;

  -- Context length bucket
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'context_length_bucket'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN context_length_bucket TEXT;
    COMMENT ON COLUMN public.auto_route_selections.context_length_bucket IS
      'Total context bucket: xs(<2k), s(2k-8k), m(8k-32k), l(32k-128k), xl(128k+). Non-reversible.';
  END IF;

  -- Turn count bucket (conversation rounds)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'turn_count_bucket'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN turn_count_bucket TEXT;
    COMMENT ON COLUMN public.auto_route_selections.turn_count_bucket IS
      'Turn count bucket: single, few(2-5), many(6-20), very_many(20+). Non-reversible.';
  END IF;

  -- Boolean flags for content type (no actual content stored)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'has_code_indicator'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN has_code_indicator BOOLEAN;
    COMMENT ON COLUMN public.auto_route_selections.has_code_indicator IS
      'Boolean flag: code block detected. No code content stored.';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'has_math_indicator'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN has_math_indicator BOOLEAN;
    COMMENT ON COLUMN public.auto_route_selections.has_math_indicator IS
      'Boolean flag: math/formula detected. No formula content stored.';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'has_table_indicator'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN has_table_indicator BOOLEAN;
    COMMENT ON COLUMN public.auto_route_selections.has_table_indicator IS
      'Boolean flag: table/structured data detected. No table content stored.';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'has_multimedia_indicator'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN has_multimedia_indicator BOOLEAN;
    COMMENT ON COLUMN public.auto_route_selections.has_multimedia_indicator IS
      'Boolean flag: image/audio/video parts detected. No media content stored.';
  END IF;

  -- Intent enumeration (high-level classification)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'intent_category'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN intent_category TEXT;
    COMMENT ON COLUMN public.auto_route_selections.intent_category IS
      'Intent enum: question, instruction, conversation, analysis, generation, etc. Non-reversible.';
  END IF;

  -- Domain enumeration
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'domain_hint'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN domain_hint TEXT;
    COMMENT ON COLUMN public.auto_route_selections.domain_hint IS
      'Domain enum: general, technical, business, academic, creative, etc. Non-reversible.';
  END IF;

  -- Complexity bucket
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'complexity_bucket'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN complexity_bucket TEXT;
    COMMENT ON COLUMN public.auto_route_selections.complexity_bucket IS
      'Complexity bucket: trivial, simple, moderate, complex, very_complex. Heuristic estimate.';
  END IF;

  -- User preference signals (latency/cost sensitivity)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'latency_sensitive'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN latency_sensitive BOOLEAN;
    COMMENT ON COLUMN public.auto_route_selections.latency_sensitive IS
      'Boolean flag: request prefers low latency (from profile or explicit signal).';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'cost_sensitive'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN cost_sensitive BOOLEAN;
    COMMENT ON COLUMN public.auto_route_selections.cost_sensitive IS
      'Boolean flag: request prefers low cost (from profile or explicit signal).';
  END IF;

  -- Feature schema version (allows evolution)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'feature_version'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN feature_version TEXT DEFAULT 'v1';
    COMMENT ON COLUMN public.auto_route_selections.feature_version IS
      'Feature schema version: v1, v2, etc. Allows training data evolution.';
  END IF;

  -- Non-reversible content hash (for deduplication, not reconstruction)
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'auto_route_selections'
      AND column_name = 'content_hash'
  ) THEN
    ALTER TABLE public.auto_route_selections
      ADD COLUMN content_hash TEXT;
    COMMENT ON COLUMN public.auto_route_selections.content_hash IS
      'SHA256 hash of prompt content. For dedup only, not reversible.';
  END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────
-- 2. Add same structured feature columns to hot table
-- ─────────────────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'detected_language') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN detected_language TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'prompt_length_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN prompt_length_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'context_length_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN context_length_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'turn_count_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN turn_count_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_code_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_code_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_math_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_math_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_table_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_table_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_multimedia_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_multimedia_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'intent_category') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN intent_category TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'domain_hint') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN domain_hint TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'complexity_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN complexity_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'latency_sensitive') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN latency_sensitive BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'cost_sensitive') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN cost_sensitive BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'feature_version') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN feature_version TEXT DEFAULT 'v1';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'content_hash') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN content_hash TEXT;
  END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────
-- 3. Update auto_route_selections_all view to include structured features
-- ─────────────────────────────────────────────────────────────────────────
CREATE OR REPLACE VIEW public.auto_route_selections_all AS
SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash,
  detected_language, prompt_length_bucket, context_length_bucket, turn_count_bucket,
  has_code_indicator, has_math_indicator, has_table_indicator, has_multimedia_indicator,
  intent_category, domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
  feature_version, content_hash, 'hot'::text AS storage_tier
FROM public.auto_route_selections_hot
UNION ALL
SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash,
  detected_language, prompt_length_bucket, context_length_bucket, turn_count_bucket,
  has_code_indicator, has_math_indicator, has_table_indicator, has_multimedia_indicator,
  intent_category, domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
  feature_version, content_hash, 'parent'::text AS storage_tier
FROM public.auto_route_selections;

COMMENT ON VIEW public.auto_route_selections_all IS
  'Unified view: hot heap + partitioned parent. Includes structured features (v1). No prompt/message content.';

-- ─────────────────────────────────────────────────────────────────────────
-- 4. Update promote function to handle new columns
-- ─────────────────────────────────────────────────────────────────────────
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
      h.treatment, h.assignment_version, h.assignment_key_hash,
      h.detected_language, h.prompt_length_bucket, h.context_length_bucket, h.turn_count_bucket,
      h.has_code_indicator, h.has_math_indicator, h.has_table_indicator, h.has_multimedia_indicator,
      h.intent_category, h.domain_hint, h.complexity_bucket, h.latency_sensitive, h.cost_sensitive,
      h.feature_version, h.content_hash
  ), inserted AS (
    INSERT INTO public.auto_route_selections (
      id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
      canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
      explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
      partition_date, experiment_id, treatment, assignment_version, assignment_key_hash,
      detected_language, prompt_length_bucket, context_length_bucket, turn_count_bucket,
      has_code_indicator, has_math_indicator, has_table_indicator, has_multimedia_indicator,
      intent_category, domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
      feature_version, content_hash)
    SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
      canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
      explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
      partition_date, experiment_id, treatment, assignment_version, assignment_key_hash,
      detected_language, prompt_length_bucket, context_length_bucket, turn_count_bucket,
      has_code_indicator, has_math_indicator, has_table_indicator, has_multimedia_indicator,
      intent_category, domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
      feature_version, content_hash FROM moved_rows
    ON CONFLICT DO NOTHING RETURNING id, partition_date
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

COMMENT ON FUNCTION public.promote_auto_route_selections_hot_to_partition IS
  'Promote cold rows from hot heap to monthly partitions. Updated for structured features v1.';

INSERT INTO public.schema_migrations (version, description) VALUES ('658', 'auto_route_selections structured features v1') ON CONFLICT (version) DO NOTHING;
COMMIT;
