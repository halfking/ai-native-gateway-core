-- 372_license_modules.sql
-- License与产品模块授权关联
--
-- 将License（设备绑定 + 有效期）与产品模块（功能授权）打通：
--   license_modules     = 每个License已授权的模块列表
--   license_module_audit = 授权变更审计日志

CREATE TABLE IF NOT EXISTS licenses (
    id               BIGSERIAL PRIMARY KEY,
    license_key      TEXT NOT NULL UNIQUE,
    customer_name    TEXT NOT NULL DEFAULT '',
    customer_email   TEXT NOT NULL DEFAULT '',
    max_devices      INT NOT NULL DEFAULT 2,
    subscription_tier TEXT NOT NULL DEFAULT 'starter',
    features         JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at       TIMESTAMPTZ,
    revoked_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_licenses_key ON licenses (license_key);
CREATE INDEX IF NOT EXISTS idx_licenses_expires ON licenses (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS license_modules (
    id              BIGSERIAL PRIMARY KEY,
    license_id      BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    module_key      TEXT NOT NULL REFERENCES product_modules(key),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    config          JSONB,
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (license_id, module_key)
);
CREATE INDEX IF NOT EXISTS idx_lm_license ON license_modules (license_id);
CREATE INDEX IF NOT EXISTS idx_lm_module ON license_modules (module_key);

CREATE TABLE IF NOT EXISTS license_module_audit (
    id              BIGSERIAL PRIMARY KEY,
    license_key     TEXT NOT NULL,
    module_key      TEXT NOT NULL,
    action          TEXT NOT NULL,
    old_value       JSONB,
    new_value       JSONB,
    actor           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_lma_key ON license_module_audit (license_key, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_lma_module ON license_module_audit (module_key, created_at DESC);
