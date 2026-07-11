-- 376_autoupdate.sql
-- 自动升级模块表
--
-- 支持：
--   1. 发布版本管理（releases）
--   2. 灰度发布规则（gray_release_rules）
--   3. 实例升级日志（upgrade_logs）
--   4. 实例当前版本状态（instance_release_status）

CREATE TABLE IF NOT EXISTS releases (
    id              BIGSERIAL PRIMARY KEY,
    version         TEXT NOT NULL UNIQUE,
    build_seq       INT NOT NULL,
    channel         TEXT NOT NULL DEFAULT 'stable'
        CHECK (channel IN ('stable', 'beta', 'canary')),
    title           TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    changelog       TEXT NOT NULL DEFAULT '',
    image_tag       TEXT NOT NULL DEFAULT '',
    image_digest    TEXT NOT NULL DEFAULT '',
    min_version     TEXT NOT NULL DEFAULT '',
    mandatory       BOOLEAN NOT NULL DEFAULT FALSE,
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_releases_channel_seq ON releases (channel, build_seq DESC);
CREATE INDEX IF NOT EXISTS idx_releases_published ON releases (published_at DESC) WHERE published_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS gray_release_rules (
    id              BIGSERIAL PRIMARY KEY,
    release_id      BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    phase           TEXT NOT NULL
        CHECK (phase IN ('canary', 'batch_1', 'batch_2', 'batch_3', 'full')),
    percent         INT NOT NULL CHECK (percent > 0 AND percent <= 100),
    selectors       JSONB,
    status          TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'scheduled', 'completed', 'cancelled')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_gray_release ON gray_release_rules (release_id);
CREATE INDEX IF NOT EXISTS idx_gray_status ON gray_release_rules (status);

CREATE TABLE IF NOT EXISTS upgrade_logs (
    id              BIGSERIAL PRIMARY KEY,
    instance_id     TEXT NOT NULL,
    old_version     TEXT NOT NULL DEFAULT '',
    new_version     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'downloading', 'ready_to_restart', 'upgrading', 'success', 'failed', 'rolled_back')),
    error_message   TEXT,
    retry_count     INT NOT NULL DEFAULT 0,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_upgrade_logs_instance ON upgrade_logs (instance_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_upgrade_logs_status ON upgrade_logs (status);

CREATE TABLE IF NOT EXISTS instance_release_status (
    instance_id     TEXT PRIMARY KEY,
    release_id      BIGINT REFERENCES releases(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    version         TEXT NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    error           TEXT,
    retry_count     INT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_irs_release ON instance_release_status (release_id);
CREATE INDEX IF NOT EXISTS idx_irs_status ON instance_release_status (status);