#!/bin/bash
# 快速修复脚本：基于现有redclaw数据库，补充Dashboard登录所需的最小表集

set -e

DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5434}"
DB_NAME="${DB_NAME:-redclaw}"
DB_USER="${DB_USER:-redclaw}"
DB_PASSWORD="${DB_PASSWORD:-redclaw}"

echo "================================================"
echo "LLM Gateway 数据库快速修复"
echo "================================================"
echo ""
echo "数据库: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
echo ""

# 创建认证用户表（用于登录）
echo "[1/3] 创建认证用户表..."
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
-- 创建独立的认证用户表
CREATE TABLE IF NOT EXISTS auth_users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(255) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'user',
    tenant_code VARCHAR(64) NOT NULL DEFAULT 'default',
    email VARCHAR(255),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 插入管理员用户 (username: admin, password: <ADMIN_PASSWORD_REDACTED>)
-- bcrypt hash生成: python3 -c "import bcrypt; print(bcrypt.hashpw(b'<ADMIN_PASSWORD_REDACTED>', bcrypt.gensalt()).decode())"
INSERT INTO auth_users (username, password_hash, role, tenant_code)
VALUES (
    'admin',
    '$2b$12$Qqs0L1OgsNok8IYW4rBe8ekHq.1Nz42/ehUhaTvtl6s182hhIdDTK',
    'super_admin',
    'default'
) ON CONFLICT (username) DO UPDATE SET
    password_hash = EXCLUDED.password_hash,
    role = EXCLUDED.role;

SELECT '✓ auth_users表已创建，admin用户已就绪' as status;
EOSQL

echo ""
echo "[2/3] 确保credentials表存在..."
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
-- credentials表（ensureCredentialColumns需要）
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'credentials') THEN
        CREATE TABLE credentials (
            id BIGSERIAL PRIMARY KEY,
            tenant_id TEXT NOT NULL DEFAULT 'default',
            provider_id INTEGER,
            api_key TEXT NOT NULL,
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            concurrency_limit INTEGER DEFAULT 5,
            concurrency_limit_auto INTEGER,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
        CREATE INDEX idx_credentials_tenant ON credentials(tenant_id);
        RAISE NOTICE '✓ credentials表已创建';
    ELSE
        -- 添加缺失的列
        ALTER TABLE credentials ADD COLUMN IF NOT EXISTS concurrency_limit INTEGER DEFAULT 5;
        ALTER TABLE credentials ADD COLUMN IF NOT EXISTS concurrency_limit_auto INTEGER;
        RAISE NOTICE '✓ credentials表已更新';
    END IF;
END $$;
EOSQL

echo ""
echo "[3/3] 确保applications表符合规范..."
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
-- 重建applications表
DROP TABLE IF EXISTS applications CASCADE;

CREATE TABLE applications (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    code TEXT NOT NULL,
    display_name TEXT NOT NULL,
    owner_user TEXT,
    data_sensitivity TEXT NOT NULL DEFAULT 'internal',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    default_client_profile TEXT,
    allowed_models_json JSONB,
    CONSTRAINT applications_tenant_id_code_key UNIQUE (tenant_id, code)
);

CREATE INDEX idx_applications_tenant_code ON applications(tenant_id, code) WHERE enabled = TRUE;

-- 插入默认应用
INSERT INTO applications (id, tenant_id, code, display_name, owner_user, data_sensitivity, enabled)
VALUES (1, 'default', 'admin', 'Admin Console', 'admin', 'confidential', TRUE);

SELECT '✓ applications表已重建' as status;
EOSQL

echo ""
echo "================================================"
echo "✓ 数据库修复完成"
echo "================================================"
echo ""
echo "验证数据:"
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
SELECT 'auth_users' as table_name, COUNT(*) as row_count FROM auth_users
UNION ALL
SELECT 'applications', COUNT(*) FROM applications
UNION ALL
SELECT 'credentials', COUNT(*) FROM credentials
UNION ALL
SELECT 'tenants', COUNT(*) FROM tenants;
EOSQL

echo ""
echo "现在可以运行: bash scripts/local-deploy.sh restart"
echo ""
