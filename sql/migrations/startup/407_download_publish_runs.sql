-- 407_download_publish_runs.sql — audit log for offline download release publishing

CREATE TABLE IF NOT EXISTS download_publish_runs (
    id              BIGSERIAL PRIMARY KEY,
    release_version TEXT NOT NULL,
    build_seq       INT NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending',
    artifact_count  INT NOT NULL DEFAULT 0,
    test_passed     BOOLEAN NOT NULL DEFAULT FALSE,
    log_summary     TEXT,
    created_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_download_publish_runs_created
    ON download_publish_runs (created_at DESC);
