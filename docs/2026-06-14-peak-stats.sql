-- 2026-06-14-peak-stats.sql
-- Per-credential-model peak concurrency tracking + weekly aggregation + auto-tune audit.
-- Apply via:
--   kubectl -n pms-test exec -i deploy/postgres-deployment -- psql -U kxuser -d kaixuan < 2026-06-14-peak-stats.sql

-- 1. Per-minute peak concurrent requests (TimescaleDB hypertable)
CREATE TABLE IF NOT EXISTS credential_model_peak_1m (
    bucket          TIMESTAMPTZ NOT NULL,
    credential_id   BIGINT      NOT NULL,
    raw_model       TEXT        NOT NULL DEFAULT '',
    peak_concurrent INTEGER     NOT NULL DEFAULT 0,
    avg_concurrent  NUMERIC(8,2) NOT NULL DEFAULT 0,
    sample_count    INTEGER     NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, credential_id, raw_model)
);

-- If TimescaleDB extension is available, make it a hypertable.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable(
            'credential_model_peak_1m',
            'bucket',
            chunk_time_interval => INTERVAL '7 days',
            if_not_exists => TRUE
        );
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_peak_1m_cred_bucket
    ON credential_model_peak_1m (credential_id, bucket DESC);

CREATE INDEX IF NOT EXISTS idx_peak_1m_model_bucket
    ON credential_model_peak_1m (raw_model, bucket DESC);

COMMENT ON TABLE credential_model_peak_1m IS
    'Per-minute peak concurrency per credential-model pair (used by auto-tune)';

-- 2. Weekly peak aggregation
CREATE TABLE IF NOT EXISTS credential_model_weekly_peak (
    week_start      TIMESTAMPTZ NOT NULL,                -- Monday 00:00 UTC
    credential_id   BIGINT      NOT NULL,
    raw_model       TEXT        NOT NULL DEFAULT '',
    peak_concurrent INTEGER     NOT NULL DEFAULT 0,
    p95_concurrent  NUMERIC(8,2) NOT NULL DEFAULT 0,
    avg_concurrent  NUMERIC(8,2) NOT NULL DEFAULT 0,
    total_requests  BIGINT      NOT NULL DEFAULT 0,
    sample_days     INTEGER     NOT NULL DEFAULT 0,
    current_limit   INTEGER     NOT NULL DEFAULT 0,
    suggested_limit INTEGER,
    suggestion_reason TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (week_start, credential_id, raw_model)
);

CREATE INDEX IF NOT EXISTS idx_weekly_peak_cred
    ON credential_model_weekly_peak (credential_id, week_start DESC);

COMMENT ON TABLE credential_model_weekly_peak IS
    'Weekly aggregated peak concurrency for auto-tune suggestions';

-- 3. Auto-tune audit log
CREATE TABLE IF NOT EXISTS auto_tune_audit (
    id              BIGSERIAL   PRIMARY KEY,
    credential_id   BIGINT      NOT NULL,
    raw_model       TEXT        NOT NULL DEFAULT '',
    action          TEXT        NOT NULL,  -- 'suggest', 'preview', 'apply', 'reject'
    old_limit       INTEGER,
    new_limit       INTEGER,
    reason          TEXT,
    peak_concurrent INTEGER,
    p95_concurrent  NUMERIC(8,2),
    week_start      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_by      TEXT                    -- 'auto' or admin username
);

CREATE INDEX IF NOT EXISTS idx_auto_tune_cred
    ON auto_tune_audit (credential_id, created_at DESC);

COMMENT ON TABLE auto_tune_audit IS
    'Audit log for concurrency limit auto-tune actions (24h preview + auto-apply)';
