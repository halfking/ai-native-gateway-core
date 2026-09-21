-- Migration 478: auto-route feedback loop — selection log + learned task→model affinity
--
-- Ref: docs/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md
--
-- Background:
--   `model=auto` picks a model per request but nothing measures whether the
--   pick was good. tuning_signals records a per-request quality score, yet its
--   only consumer (bg/feedback_analyzer.go) produces human-approval proposals —
--   there is no automatic path from "this model does well on this task" back
--   into the routing decision. Meanwhile task_default_routing (the hand-written
--   V6 matrix) is not consulted at all on the live DecideV2 path.
--
-- This migration adds the two tables that close the loop:
--
--   1. auto_route_selections — one row per auto match. IDs and numbers ONLY:
--      request_id / session_id / task_id / canonical_id plus decision snapshot
--      and outcome. Deliberately NO prompt text and NO conversation detail.
--      Outcome columns (success/latency/cost/reward) are backfilled by
--      AutoRouteSettleWorker ~2min after the request settles.
--
--   2. task_model_affinity — the learned "best model per task" ranking,
--      recomputed by TaskModelAffinityWorker from settled selections. This is
--      a SOFT weight: it reorders candidates that already passed the hard
--      availability filter. It never revives a banned or unavailable model.
--      Priority order stays: ban > pin > task_default_routing > affinity > base score.
--
-- Small-sample protection lives in the worker, not here, but the columns that
-- make it auditable (sample_count, confidence) are part of the schema so a
-- reviewer can always see why a model ranks where it does.
--
-- Partitioning: auto_route_selections is RANGE-partitioned by partition_date
-- following rule 33 — a DEFAULT partition is created in the same migration so
-- the first INSERT cannot fail (the mistake migration 470 made and 472 fixed),
-- plus explicit monthly partitions for the deploy month and the next one.
-- task_model_affinity is a small non-partitioned current-state table.
--
-- Idempotent: pg_tables / pg_inherits name checks (NOT ::regclass casts, which
--             raise instead of returning NULL on a missing relation). Re-runnable.
--
-- Rollback: 478_auto_route_affinity.down.sql

\set ON_ERROR_STOP on
BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- 1. auto_route_selections — per-match log (IDs + metrics only)
-- ─────────────────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'auto_route_selections'
  ) THEN
    CREATE TABLE public.auto_route_selections (
      id               BIGSERIAL   NOT NULL,
      request_id       TEXT        NOT NULL,
      session_id       TEXT,
      task_id          TEXT,
      tenant_id        VARCHAR(64),
      ts               TIMESTAMPTZ NOT NULL DEFAULT NOW(),

      -- decision snapshot
      task_type        TEXT        NOT NULL,
      profile          TEXT        NOT NULL DEFAULT 'smart',
      classifier       TEXT        NOT NULL DEFAULT 'heuristic',
      confidence       NUMERIC(4,3),
      canonical_id     BIGINT,
      chosen_model     TEXT        NOT NULL,
      candidate_rank   SMALLINT    NOT NULL DEFAULT 1,
      composite_score  NUMERIC(6,2),
      affinity_score   NUMERIC(6,2),
      affinity_applied BOOLEAN     NOT NULL DEFAULT FALSE,
      explore          BOOLEAN     NOT NULL DEFAULT FALSE,
      fallback_used    BOOLEAN     NOT NULL DEFAULT FALSE,

      -- outcome, backfilled by AutoRouteSettleWorker
      success          BOOLEAN,
      latency_ms       INTEGER,
      cost_usd         NUMERIC(14,8),
      reward           NUMERIC(4,3),
      reward_source    TEXT,
      settled_at       TIMESTAMPTZ,

      partition_date   DATE        NOT NULL DEFAULT CURRENT_DATE,
      PRIMARY KEY (id, partition_date)
    ) PARTITION BY RANGE (partition_date);

    COMMENT ON TABLE public.auto_route_selections IS
      'Auto-route: one row per model=auto match. IDs + metrics only, no prompt/conversation content. Outcome backfilled by AutoRouteSettleWorker.';
    COMMENT ON COLUMN public.auto_route_selections.session_id IS
      'Session identifier only (no session detail is stored anywhere in this table).';
    COMMENT ON COLUMN public.auto_route_selections.candidate_rank IS
      '1 = top-scored candidate was chosen; >1 means a pin/default promoted a lower-ranked one.';
    COMMENT ON COLUMN public.auto_route_selections.affinity_score IS
      'Affinity contribution at decision time. Recorded even in shadow mode (affinity_applied=false).';
    COMMENT ON COLUMN public.auto_route_selections.explore IS
      'TRUE when this request was sampled into the explore bucket (affinity deliberately not applied).';
    COMMENT ON COLUMN public.auto_route_selections.reward IS
      'Routing-attributed reward 0..1. See docs/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md §2.2.';
    COMMENT ON COLUMN public.auto_route_selections.reward_source IS
      'request = per-request signals only; session = session health also attributed (model handled >=80% of the session).';

    -- Idempotency for replays: one row per request per day.
    CREATE UNIQUE INDEX uq_ars_request
      ON public.auto_route_selections (request_id, partition_date);

    -- Affinity rollup scan: (task,profile) over a time window.
    CREATE INDEX idx_ars_task_profile_ts
      ON public.auto_route_selections (task_type, profile, ts DESC);

    -- Per-model history + admin drill-down.
    CREATE INDEX idx_ars_canonical_ts
      ON public.auto_route_selections (canonical_id, ts DESC);

    -- Session trace lookup.
    CREATE INDEX idx_ars_session
      ON public.auto_route_selections (session_id, partition_date)
      WHERE session_id IS NOT NULL;

    -- Settle worker: find unsettled rows cheaply.
    CREATE INDEX idx_ars_unsettled
      ON public.auto_route_selections (ts)
      WHERE settled_at IS NULL;

    ALTER TABLE public.auto_route_selections
      ADD CONSTRAINT ars_profile_check
      CHECK (profile IN ('', 'smart', 'speed_first', 'cost_first'));

    ALTER TABLE public.auto_route_selections
      ADD CONSTRAINT ars_reward_range
      CHECK (reward IS NULL OR (reward >= 0 AND reward <= 1));

    ALTER TABLE public.auto_route_selections
      ADD CONSTRAINT ars_reward_source_check
      CHECK (reward_source IS NULL OR reward_source IN ('request', 'session'));
  END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────
-- 2. Partitions — DEFAULT first (rule 33: INSERT must never fail), then monthly
-- ─────────────────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits inh
    JOIN pg_class c ON c.oid = inh.inhrelid
    JOIN pg_class p ON p.oid = inh.inhparent
    WHERE p.relname = 'auto_route_selections'
      AND p.relnamespace = 'public'::regnamespace
      AND c.relname = 'auto_route_selections_default'
  ) THEN
    CREATE TABLE public.auto_route_selections_default
      PARTITION OF public.auto_route_selections DEFAULT;
    COMMENT ON TABLE public.auto_route_selections_default IS
      'Default partition — catches writes before a monthly partition exists (rule 33).';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits inh
    JOIN pg_class c ON c.oid = inh.inhrelid
    JOIN pg_class p ON p.oid = inh.inhparent
    WHERE p.relname = 'auto_route_selections'
      AND p.relnamespace = 'public'::regnamespace
      AND c.relname = 'auto_route_selections_2026_08'
  ) THEN
    CREATE TABLE public.auto_route_selections_2026_08
      PARTITION OF public.auto_route_selections
      FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits inh
    JOIN pg_class c ON c.oid = inh.inhrelid
    JOIN pg_class p ON p.oid = inh.inhparent
    WHERE p.relname = 'auto_route_selections'
      AND p.relnamespace = 'public'::regnamespace
      AND c.relname = 'auto_route_selections_2026_09'
  ) THEN
    CREATE TABLE public.auto_route_selections_2026_09
      PARTITION OF public.auto_route_selections
      FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
  END IF;

  -- HIGH-2 fix: pre-create one rolling month past the deploy window so that
  -- the loop never lands in DEFAULT for an entire month at a time. The DEFAULT
  -- partition is supposed to be a safety net, not a steady-state destination.
  -- Idempotent: re-running on a DB that already has the partition is a no-op.
  -- Owners must also create 478p2_followup_2026_11+ (or equivalent automation)
  -- before each subsequent month rolls over.
  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits inh
    JOIN pg_class c ON c.oid = inh.inhrelid
    JOIN pg_class p ON p.oid = inh.inhparent
    WHERE p.relname = 'auto_route_selections'
      AND p.relnamespace = 'public'::regnamespace
      AND c.relname = 'auto_route_selections_2026_10'
  ) THEN
    CREATE TABLE public.auto_route_selections_2026_10
      PARTITION OF public.auto_route_selections
      FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
  END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────
-- 3. task_model_affinity — the learned best-model-per-task ranking
-- ─────────────────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'task_model_affinity'
  ) THEN
    CREATE TABLE public.task_model_affinity (
      task_type       TEXT         NOT NULL,
      profile         TEXT         NOT NULL DEFAULT '',
      canonical_id    BIGINT       NOT NULL,
      canonical_model TEXT         NOT NULL,
      tenant_id       VARCHAR(64)  NOT NULL DEFAULT '',

      -- observed aggregates over the learning window
      sample_count    INTEGER      NOT NULL DEFAULT 0,
      success_count   INTEGER      NOT NULL DEFAULT 0,
      success_rate    NUMERIC(5,4),
      avg_reward      NUMERIC(4,3),
      ema_reward      NUMERIC(4,3),
      avg_latency_ms  INTEGER,
      avg_cost_usd    NUMERIC(14,8),
      avg_health      NUMERIC(5,2),

      -- derived ranking signal
      affinity        NUMERIC(6,2) NOT NULL DEFAULT 50,
      rank            SMALLINT,
      confidence      NUMERIC(4,3) NOT NULL DEFAULT 0,

      first_seen_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
      last_sampled_at TIMESTAMPTZ,
      updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

      PRIMARY KEY (task_type, profile, canonical_id, tenant_id)
    );

    COMMENT ON TABLE public.task_model_affinity IS
      'Auto-route: learned task→model ranking, recomputed by TaskModelAffinityWorker from settled auto_route_selections. SOFT weight — reorders candidates that already passed the hard availability filter; never revives a banned/unavailable model.';
    COMMENT ON COLUMN public.task_model_affinity.tenant_id IS
      'Empty string = platform-level. Tenant rows are only used once they have enough samples of their own.';
    COMMENT ON COLUMN public.task_model_affinity.affinity IS
      'Bayesian-shrunk score 0..100, neutral 50. Clamped to [10,90] at read time so one dimension cannot dominate.';
    COMMENT ON COLUMN public.task_model_affinity.confidence IS
      'Sample adequacy w = n/(n+K), K=30. Low confidence pulls affinity back toward neutral 50.';
    COMMENT ON COLUMN public.task_model_affinity.ema_reward IS
      'Exponential moving average of avg_reward (alpha 0.15) so old behaviour decays instead of being averaged forever.';
    COMMENT ON COLUMN public.task_model_affinity.last_sampled_at IS
      'Drives staleness decay: rows unsampled for >7 days drift back toward neutral.';

    -- Ranking read path: "best models for this task/profile".
    CREATE INDEX idx_tma_lookup
      ON public.task_model_affinity (task_type, profile, tenant_id, affinity DESC);

    -- Staleness decay scan.
    CREATE INDEX idx_tma_last_sampled
      ON public.task_model_affinity (last_sampled_at);

    ALTER TABLE public.task_model_affinity
      ADD CONSTRAINT tma_profile_check
      CHECK (profile IN ('', 'smart', 'speed_first', 'cost_first'));

    ALTER TABLE public.task_model_affinity
      ADD CONSTRAINT tma_affinity_range
      CHECK (affinity >= 0 AND affinity <= 100);

    ALTER TABLE public.task_model_affinity
      ADD CONSTRAINT tma_confidence_range
      CHECK (confidence >= 0 AND confidence <= 1);
  END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────
-- 4. v_task_model_ranking — admin/debug view of the learned ranking
-- ─────────────────────────────────────────────────────────────────────────
CREATE OR REPLACE VIEW public.v_task_model_ranking AS
SELECT
    tma.task_type,
    tma.profile,
    tma.tenant_id,
    tma.rank,
    tma.canonical_id,
    tma.canonical_model,
    tma.affinity,
    tma.confidence,
    tma.sample_count,
    tma.success_rate,
    tma.avg_reward,
    tma.ema_reward,
    tma.avg_latency_ms,
    tma.avg_cost_usd,
    tma.avg_health,
    tma.last_sampled_at,
    tma.updated_at,
    -- Is this model currently routable at all? Affinity is meaningless if not.
    EXISTS (
        SELECT 1
        FROM credential_model_bindings cmb
        JOIN provider_models pm ON pm.id = cmb.provider_model_id
        WHERE pm.canonical_id = tma.canonical_id
          AND cmb.available IS TRUE
          AND pm.available IS TRUE
    ) AS currently_routable
FROM task_model_affinity tma;

COMMENT ON VIEW public.v_task_model_ranking IS
  'Auto-route: learned task→model ranking joined with live routability. currently_routable=false means the row is historical only.';

COMMIT;
