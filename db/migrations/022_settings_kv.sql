-- Migration 022: settings_kv + tenant_settings_kv
-- 平台级 + 租户级运行时设置存储（Q1: B, Q2: A, Q3: B）
-- 作者: settings-management project
-- 日期: 2026-06-20

-- ============================================================
-- settings_kv: 平台级设置（无 tenant_id 维度）
-- 优先级最高，DB > env > default
-- ============================================================
CREATE TABLE IF NOT EXISTS settings_kv (
    key              VARCHAR(128) PRIMARY KEY,
    value            JSONB NOT NULL,
    value_type       VARCHAR(32) NOT NULL,
    scope            VARCHAR(16) NOT NULL DEFAULT 'platform',
    category         VARCHAR(32) NOT NULL DEFAULT 'general',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by       VARCHAR(64),
    prev_value       JSONB,
    prev_updated_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_settings_kv_category ON settings_kv (category);
CREATE INDEX IF NOT EXISTS idx_settings_kv_scope    ON settings_kv (scope);
CREATE INDEX IF NOT EXISTS idx_settings_kv_updated  ON settings_kv (updated_at DESC);

COMMENT ON TABLE settings_kv IS '平台级运行时设置（Q2: 立即生效）';
COMMENT ON COLUMN settings_kv.prev_value IS '上次的值，用于一键回滚';

-- ============================================================
-- tenant_settings_kv: 租户级设置（Q3: 租户级支持）
-- ============================================================
CREATE TABLE IF NOT EXISTS tenant_settings_kv (
    tenant_id        VARCHAR(64) NOT NULL,
    key              VARCHAR(128) NOT NULL,
    value            JSONB NOT NULL,
    value_type       VARCHAR(32) NOT NULL,
    category         VARCHAR(32) NOT NULL DEFAULT 'general',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by       VARCHAR(64),
    prev_value       JSONB,
    prev_updated_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, key)
);

CREATE INDEX IF NOT EXISTS idx_tenant_settings_kv_tenant ON tenant_settings_kv (tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_settings_kv_category ON tenant_settings_kv (category);

COMMENT ON TABLE tenant_settings_kv IS '租户级运行时设置（Q3）';

-- ============================================================
-- Q4 (C 方案): 迁移 app_settings.rate_limit_* 到 settings_kv
-- 然后删除 app_settings 的死字段（清理）
-- ============================================================
DO $$
DECLARE
    has_app_settings BOOLEAN;
    has_rpm BOOLEAN;
    has_conc BOOLEAN;
    has_tpm BOOLEAN;
    rec RECORD;
BEGIN
    -- 检查 app_settings 表是否存在
    SELECT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'app_settings'
    ) INTO has_app_settings;

    IF has_app_settings THEN
        -- 检查列是否存在
        SELECT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'app_settings'
              AND column_name = 'rate_limit_rpm'
        ) INTO has_rpm;
        SELECT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'app_settings'
              AND column_name = 'rate_limit_concurrent'
        ) INTO has_conc;
        SELECT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'app_settings'
              AND column_name = 'rate_limit_tpm'
        ) INTO has_tpm;

        -- 迁移 rate_limit_rpm
        IF has_rpm THEN
            FOR rec IN SELECT DISTINCT tenant_id, rate_limit_rpm FROM app_settings
                         WHERE rate_limit_rpm IS NOT NULL
            LOOP
                INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by, updated_at)
                VALUES ('default.rate_limit_rpm', to_jsonb(rec.rate_limit_rpm),
                        'int', 'platform', 'rate_limit', 'migration-022', now())
                ON CONFLICT (key) DO NOTHING;
            END LOOP;
            ALTER TABLE app_settings DROP COLUMN IF EXISTS rate_limit_rpm;
        END IF;

        -- 迁移 rate_limit_concurrent
        IF has_conc THEN
            FOR rec IN SELECT DISTINCT tenant_id, rate_limit_concurrent FROM app_settings
                         WHERE rate_limit_concurrent IS NOT NULL
            LOOP
                INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by, updated_at)
                VALUES ('default.rate_limit_concurrent', to_jsonb(rec.rate_limit_concurrent),
                        'int', 'platform', 'rate_limit', 'migration-022', now())
                ON CONFLICT (key) DO NOTHING;
            END LOOP;
            ALTER TABLE app_settings DROP COLUMN IF EXISTS rate_limit_concurrent;
        END IF;

        -- 迁移 rate_limit_tpm
        IF has_tpm THEN
            FOR rec IN SELECT DISTINCT tenant_id, rate_limit_tpm FROM app_settings
                         WHERE rate_limit_tpm IS NOT NULL
            LOOP
                INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by, updated_at)
                VALUES ('default.rate_limit_tpm', to_jsonb(rec.rate_limit_tpm),
                        'int', 'platform', 'rate_limit', 'migration-022', now())
                ON CONFLICT (key) DO NOTHING;
            END LOOP;
            ALTER TABLE app_settings DROP COLUMN IF EXISTS rate_limit_tpm;
        END IF;
    END IF;
END
$$;