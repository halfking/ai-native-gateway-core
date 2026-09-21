-- Migration 656: auto_route_selections independent hot heap and all view.
-- New selections land in the heap for low-latency settlement; historical rows
-- remain in the partitioned parent. Promotion is one atomic data-modifying CTE
-- that only drains settled rows past the retention window (7-day fallback for
-- rows the settle worker never finished; 2026-09-05 audit H-4).
\set ON_ERROR_STOP on
BEGIN;

CREATE TABLE IF NOT EXISTS public.auto_route_selections_hot (
  id BIGINT NOT NULL DEFAULT nextval('public.auto_route_selections_id_seq'::regclass),
  request_id TEXT NOT NULL, session_id TEXT, task_id TEXT, tenant_id VARCHAR(64),
  ts TIMESTAMPTZ NOT NULL DEFAULT NOW(), task_type TEXT NOT NULL, profile TEXT NOT NULL DEFAULT 'smart',
  classifier TEXT NOT NULL DEFAULT 'heuristic', confidence NUMERIC(4,3), canonical_id BIGINT,
  chosen_model TEXT NOT NULL, candidate_rank SMALLINT NOT NULL DEFAULT 1,
  composite_score NUMERIC(6,2), affinity_score NUMERIC(6,2),
  affinity_applied BOOLEAN NOT NULL, explore BOOLEAN NOT NULL, fallback_used BOOLEAN NOT NULL,
  success BOOLEAN, latency_ms INTEGER, cost_usd NUMERIC(14,8), reward NUMERIC(4,3),
  reward_source TEXT, settled_at TIMESTAMPTZ, partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
  experiment_id TEXT, treatment TEXT, assignment_version TEXT, assignment_key_hash TEXT,
  PRIMARY KEY (id, partition_date)
);
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN ts SET DEFAULT NOW();
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN profile SET DEFAULT 'smart';
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN classifier SET DEFAULT 'heuristic';
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN candidate_rank SET DEFAULT 1;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN affinity_applied SET DEFAULT FALSE;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN explore SET DEFAULT FALSE;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN fallback_used SET DEFAULT FALSE;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN partition_date SET DEFAULT CURRENT_DATE;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.auto_route_selections_hot'::regclass AND conname = 'ars_hot_profile_check') THEN
    ALTER TABLE public.auto_route_selections_hot ADD CONSTRAINT ars_hot_profile_check CHECK (profile IN ('', 'smart', 'speed_first', 'cost_first'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.auto_route_selections_hot'::regclass AND conname = 'ars_hot_reward_range') THEN
    ALTER TABLE public.auto_route_selections_hot ADD CONSTRAINT ars_hot_reward_range CHECK (reward IS NULL OR (reward >= 0 AND reward <= 1));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.auto_route_selections_hot'::regclass AND conname = 'ars_hot_reward_source_check') THEN
    ALTER TABLE public.auto_route_selections_hot ADD CONSTRAINT ars_hot_reward_source_check CHECK (reward_source IS NULL OR reward_source IN ('request', 'session'));
  END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ars_hot_request ON public.auto_route_selections_hot (request_id, partition_date);
CREATE INDEX IF NOT EXISTS idx_ars_hot_task_profile_ts ON public.auto_route_selections_hot (task_type, profile, ts DESC);
CREATE INDEX IF NOT EXISTS idx_ars_hot_session ON public.auto_route_selections_hot (session_id, ts DESC) WHERE session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ars_hot_unsettled ON public.auto_route_selections_hot (ts) WHERE settled_at IS NULL;

CREATE OR REPLACE FUNCTION public.ensure_auto_route_selections_partition(p_month date)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  month_start date := date_trunc('month', p_month)::date;
  month_end date := (month_start + interval '1 month')::date;
  part_name text := format('auto_route_selections_%s', to_char(month_start, 'YYYY_MM'));
  default_attached boolean;
BEGIN
  IF p_month IS NULL THEN
    RAISE EXCEPTION 'p_month must not be null';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('ensure_auto_route_selections_partition'));
  IF to_regclass('public.' || part_name) IS NOT NULL THEN
    RETURN;
  END IF;

  SELECT EXISTS (
    SELECT 1
    FROM pg_inherits i
    JOIN pg_class child ON child.oid = i.inhrelid
    JOIN pg_class parent ON parent.oid = i.inhparent
    JOIN pg_namespace n ON n.oid = parent.relnamespace
    WHERE n.nspname = 'public'
      AND parent.relname = 'auto_route_selections'
      AND child.relname = 'auto_route_selections_default'
  ) INTO default_attached;

  IF default_attached THEN
    ALTER TABLE public.auto_route_selections DETACH PARTITION public.auto_route_selections_default;
  END IF;

  EXECUTE format(
    'CREATE TABLE public.%I PARTITION OF public.auto_route_selections FOR VALUES FROM (%L) TO (%L)',
    part_name, month_start, month_end
  );

  IF default_attached THEN
    EXECUTE format(
      'WITH moved_rows AS (
         DELETE FROM public.auto_route_selections_default
         WHERE partition_date >= %L AND partition_date < %L
         RETURNING id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
                   canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
                   explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
                   partition_date, experiment_id, treatment, assignment_version, assignment_key_hash
       )
       INSERT INTO public.%I (
         id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
         canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
         explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
         partition_date, experiment_id, treatment, assignment_version, assignment_key_hash
       )
       SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
              canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
              explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
              partition_date, experiment_id, treatment, assignment_version, assignment_key_hash
       FROM moved_rows
       ON CONFLICT DO NOTHING',
      month_start, month_end, part_name
    );

    ALTER TABLE public.auto_route_selections ATTACH PARTITION public.auto_route_selections_default DEFAULT;
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.promote_auto_route_selections_hot_to_partition(
  p_retention interval DEFAULT interval '8 hours', p_batch_size integer DEFAULT 5000)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  -- 2026-09-05 (audit H-4): only settled rows past the retention window are
  -- eligible, plus a 7-day fallback so an unsettled row can never strand in
  -- hot forever if the settle worker is down longer than the hot window.
  -- The month pre-ensure loop must use the same predicate as the batch CTE.
  FOR month_rec IN SELECT DISTINCT date_trunc('month', partition_date)::date AS month_start FROM public.auto_route_selections_hot
    WHERE (settled_at IS NOT NULL AND ts < statement_timestamp() - p_retention)
       OR ts < statement_timestamp() - interval '7 days'
    ORDER BY 1 LIMIT 12 LOOP
    PERFORM public.ensure_auto_route_selections_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT id, partition_date FROM public.auto_route_selections_hot
    WHERE (settled_at IS NOT NULL AND ts < statement_timestamp() - p_retention)
       OR ts < statement_timestamp() - interval '7 days'
    ORDER BY ts, id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
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
    -- No conflict target: the parent carries both the (id, partition_date) PK
    -- and uq_ars_request (request_id, partition_date). A legacy parent row with
    -- the same request_id but a different id must skip the row, not fail the
    -- whole batch into an endless promote retry loop.
    ON CONFLICT DO NOTHING RETURNING id, partition_date
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

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

INSERT INTO public.schema_migrations (version, description) VALUES ('656', 'auto_route_selections hot heap, all view, ensure/promote') ON CONFLICT (version) DO NOTHING;
COMMIT;
