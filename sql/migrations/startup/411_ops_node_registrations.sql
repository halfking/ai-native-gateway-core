-- 411_ops_node_registrations.sql — 运维节点 API 注册记录（licensee + 管理员用户）

CREATE TABLE IF NOT EXISTS ops_node_registrations (
    id              BIGSERIAL PRIMARY KEY,
    instance_id     TEXT NOT NULL UNIQUE,
    region          TEXT NOT NULL,
    license_key     TEXT NOT NULL,
    license_id      BIGINT REFERENCES licenses(id) ON DELETE SET NULL,
    admin_user      TEXT NOT NULL,
    admin_email     TEXT,
    hostname        TEXT,
    ip_address      TEXT,
    version         TEXT,
    build_seq       INT NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat  TIMESTAMPTZ,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_onr_region ON ops_node_registrations (region, status);
CREATE INDEX IF NOT EXISTS idx_onr_license ON ops_node_registrations (license_key);
CREATE INDEX IF NOT EXISTS idx_onr_admin ON ops_node_registrations (admin_user);
