-- 2026_06_12_pricing_refresh_log.sql
-- Audit table for monthly pricing refresh cron job
-- Run via: psql -U llm_gateway -d llm_gateway -f 2026_06_12_pricing_refresh_log.sql

BEGIN;

CREATE TABLE IF NOT EXISTS pricing_refresh_log (
  id              BIGSERIAL PRIMARY KEY,
  run_id          TEXT NOT NULL,
  run_ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
  trigger         TEXT NOT NULL DEFAULT 'cron',  -- 'cron' | 'manual'
  status          TEXT NOT NULL,                 -- 'success' | 'failed' | 'partial'
  before_summary  JSONB NOT NULL,
  after_summary   JSONB NOT NULL,
  diff_count      INTEGER NOT NULL DEFAULT 0,
  new_offers      INTEGER NOT NULL DEFAULT 0,
  removed_offers  INTEGER NOT NULL DEFAULT 0,
  changed_offers  INTEGER NOT NULL DEFAULT 0,
  artifacts_path  TEXT,
  feishu_sent     BOOLEAN NOT NULL DEFAULT false,
  error_message   TEXT,
  duration_seconds INTEGER,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_pricing_refresh_log_run_ts ON pricing_refresh_log(run_ts DESC);
CREATE INDEX IF NOT EXISTS idx_pricing_refresh_log_status ON pricing_refresh_log(status);

-- Retention: auto-prune after 365 days
-- (cleanup job runs separately; not in this migration)

COMMENT ON TABLE pricing_refresh_log IS 'Audit log for monthly pricing refresh cron job. Each run inserts one row.';
COMMENT ON COLUMN pricing_refresh_log.before_summary IS 'pricing/summary response BEFORE refresh (pricing_plans + cmb state)';
COMMENT ON COLUMN pricing_refresh_log.after_summary IS 'pricing/summary response AFTER refresh';
COMMENT ON COLUMN pricing_refresh_log.diff_count IS 'Total offers changed (new + removed + changed)';
COMMENT ON COLUMN pricing_refresh_log.artifacts_path IS 'PVC path containing fetch.log, tier-pricing.csv, summary_*.json';

COMMIT;
