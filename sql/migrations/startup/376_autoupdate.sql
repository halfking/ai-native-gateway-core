-- 376_autoupdate.sql
-- 自动升级模块表

-- 发布版本表
CREATE TABLE IF NOT EXISTS releases (
    id              BIGSERIAL PRIMARY KEY,
    version         TEXT NOT NULL UNIQUE,
    build_seq       INT NOT NULL,
    channel         TEXT NOT NULL DEFAULT 'stable'
        CHECK (channel IN ('stable', 'beta', 'canary')),
    title           TEXT NOT NULL,
    description     TEXT,
    changelog       TEXT,
    image_tag       TEXT NOT NULL,
    image_digest    TEXT,
    min_version     TEXT,
    mandatory       BOOLEAN NOT NULL DEFAULT FALSE,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_releases_version ON releases (version);
CREATE INDEX IF NOT EXISTS idx_releases_channel ON releases (channel, build_seq DESC);
CREATE INDEX IF NOT EXISTS idx_releases_published ON releases (published_at DESC)
    WHERE published_at IS NOT NULL;

-- 灰度发布规则表
CREATE TABLE IF NOT EXISTS gray_release_rules (
    id              BIGSERIAL PRIMARY KEY,
    release_id      BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    phase           TEXT NOT NULL
        CHECK (phase IN ('canary', 'batch_1', 'batch_2', 'batch_3', 'full')),
    percent         INT NOT NULL CHECK (percent >= 0 AND percent <= 100),
    selectors       JSONB,
    status          TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'paused', 'completed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_gray_rules_release ON gray_release_rules (release_id);
CREATE INDEX IF NOT EXISTS idx_gray_rules_status ON gray_release_rules (status, created_at DESC);

-- 升级日志表
CREATE TABLE IF NOT EXISTS upgrade_logs (
    id              BIGSERIAL PRIMARY KEY,
    instance_id     TEXT NOT NULL,
    old_version     TEXT NOT NULL,
    new_version     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'downloading', 'ready_to_restart', 'upgrading', 'success', 'failed', 'rolled_back')),
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    error_message   TEXT,
    retry_count     INT NOT NULL DEFAULT 0,
    duration_ms     INT
);

CREATE INDEX IF NOT EXISTS idx_upgrade_logs_instance ON upgrade_logs (instance_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_upgrade_logs_status ON upgrade_logs (status, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_upgrade_logs_failed ON upgrade_logs (started_at DESC)
    WHERE status = 'failed';

-- 实例发布状态表（跟踪每个实例的当前升级状态）
CREATE TABLE IF NOT EXISTS instance_release_status (
    release_id      BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    instance_id     TEXT NOT NULL PRIMARY KEY,
    status          TEXT NOT NULL,
    version         TEXT NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    error           TEXT,
    retry_count     INT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_instance_status_release ON instance_release_status (release_id);
CREATE INDEX IF NOT EXISTS idx_instance_status_status ON instance_release_status (status);
