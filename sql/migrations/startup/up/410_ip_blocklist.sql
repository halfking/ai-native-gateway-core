-- 410_ip_blocklist.sql — API 客户端 IP 黑名单（本地表 + Redis 热缓存）

CREATE TABLE IF NOT EXISTS ip_blocklist (
    id              BIGSERIAL PRIMARY KEY,
    ip_or_cidr      TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    scope           TEXT NOT NULL DEFAULT 'global'
        CHECK (scope IN ('global', 'collect', 'ops')),
    source          TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN ('manual', 'auto_attack')),
    enabled         BOOLEAN NOT NULL DEFAULT true,
    expires_at      TIMESTAMPTZ,
    hit_count       BIGINT NOT NULL DEFAULT 0,
    created_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_ip_blocklist_active_entry
    ON ip_blocklist (ip_or_cidr, scope)
    WHERE enabled = true;

CREATE INDEX IF NOT EXISTS idx_ip_blocklist_enabled ON ip_blocklist (enabled, scope);
CREATE INDEX IF NOT EXISTS idx_ip_blocklist_expires ON ip_blocklist (expires_at)
    WHERE expires_at IS NOT NULL;
