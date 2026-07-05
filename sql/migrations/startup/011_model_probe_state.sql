-- 011_model_probe_state.sql
-- Per-(credential, model) probe consensus state.
-- State changes require 3 consecutive successes (recovery) or 3
-- consecutive failures (confirmed_broken) before the row's
-- state_change actually flips.  This prevents upstream flakes from
-- flapping routability in and out.
--
-- Backoff schedule: 1min → 5min → 15min → 60min → 60min (capped).
-- We use the consecutive-failure count to pick the backoff bucket:
--   attempts 1  → 1 min
--   attempts 2  → 5 min
--   attempts 3  → 15 min
--   attempts 4+ → 60 min
-- A passing probe resets consecutive_failures to 0 and bumps
-- consecutive_successes by 1.  When consecutive_successes hits 3,
-- the state flips from recovering/healthy_confirmed to healthy_confirmed
-- and the binding becomes routable again.
--
-- Any failure resets consecutive_successes to 0 and bumps
-- consecutive_failures by 1.  When consecutive_failures hits 3,
-- state moves to broken_confirmed (no further effect — binding is
-- already unroutable, we just stop probing).
--
-- Spec: 2026-06-18-model-probe-rounds (v2: consensus + backoff)

CREATE TABLE IF NOT EXISTS model_probe_state (
    credential_id          BIGINT      NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    raw_model_name         TEXT        NOT NULL,
    -- 'unknown' (just discovered failing) | 'recovering' (probing) |
    -- 'healthy_confirmed' (3 ok in a row, routing back on) |
    -- 'broken_confirmed' (3 fails in a row, stop probing)
    state                  TEXT        NOT NULL DEFAULT 'unknown',
    consecutive_successes  INTEGER     NOT NULL DEFAULT 0,
    consecutive_failures   INTEGER     NOT NULL DEFAULT 0,
    total_attempts         INTEGER     NOT NULL DEFAULT 0,
    last_attempt_at        TIMESTAMPTZ,
    next_retry_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Last fully-classified outcome (so the UI can show "last verdict").
    last_status            TEXT,
    last_state_change_at   TIMESTAMPTZ,
    -- The model_probe_runs.id of the run that flipped state (NULL if
    -- never flipped).  Lets the UI deep-link from the state row back
    -- into the per-run history.
    last_state_change_run  BIGINT,
    PRIMARY KEY (credential_id, raw_model_name)
);

CREATE INDEX IF NOT EXISTS idx_mps_due
    ON model_probe_state (next_retry_at)
    WHERE state IN ('unknown', 'recovering');

-- Backoff schedule: 30s → 2m → 5m → 15m (capped). Tuned to be short
-- enough to recover quickly from transient blips while still easing
-- load on a failing upstream. We pass consecutive_failures; returns
-- the next attempt interval. The function is small enough to be
-- inlined; we keep it as a SQL function for testability and so admins
-- can see it in pg_proc.
CREATE OR REPLACE FUNCTION model_probe_backoff(consecutive_failures INTEGER)
    RETURNS INTERVAL
    LANGUAGE SQL
    IMMUTABLE
AS $$
    SELECT CASE
        WHEN consecutive_failures <= 0 THEN INTERVAL '30 seconds'
        WHEN consecutive_failures = 1  THEN INTERVAL '2 minutes'
        WHEN consecutive_failures = 2  THEN INTERVAL '5 minutes'
        ELSE                                  INTERVAL '15 minutes'
    END;
$$;

COMMENT ON TABLE  model_probe_state IS 'Per-(credential, model) probe consensus state. 3 consecutive successes to recover; 3 consecutive failures to confirm-broken.';
COMMENT ON COLUMN model_probe_state.consecutive_successes IS 'Counter; resets to 0 on any failure. State flips to healthy_confirmed when this hits 3.';
COMMENT ON COLUMN model_probe_state.consecutive_failures  IS 'Counter; resets to 0 on any success. Stops probing when this hits 3 (broken_confirmed).';

-- Seed: for any currently-unroutable binding with an existing
-- model_probe_state row, leave it alone. For new ones, the runner
-- inserts on first sight.  No backfill needed — the worker creates
-- rows lazily.
