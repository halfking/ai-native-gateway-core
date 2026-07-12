#!/usr/bin/env bash
set -euo pipefail

# E2E 测试环境初始化脚本
# 功能：执行数据库迁移（376-379）、插入测试 license 数据、插入测试 release 数据

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SQL_DIR="$PROJECT_ROOT/sql/migrations/startup"

# 数据库连接参数（从环境变量或默认值）
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5433}"
DB_NAME="${DB_NAME:-llm_gateway_test}"
DB_USER="${DB_USER:-test}"
DB_PASSWORD="${DB_PASSWORD:-test123}"

export PGPASSWORD="$DB_PASSWORD"

echo "==> [1/5] 等待 PostgreSQL 启动..."
for i in {1..30}; do
    if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1" >/dev/null 2>&1; then
        echo "✓ PostgreSQL 已就绪"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "✗ PostgreSQL 启动超时"
        exit 1
    fi
    sleep 1
done

echo ""
echo "==> [2/5] 执行数据库迁移（376-379）..."

migrations=(
    "376_gateway_instances_auth.sql"
    "377_instance_heartbeats_partition.sql"
    "378_add_refresh_token.sql"
    "379_instance_release_status.sql"
)

for migration in "${migrations[@]}"; do
    migration_file="$SQL_DIR/$migration"
    if [ ! -f "$migration_file" ]; then
        echo "✗ 迁移文件不存在: $migration_file"
        exit 1
    fi
    echo "  - 执行 $migration..."
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$migration_file" -v ON_ERROR_STOP=1
done
echo "✓ 数据库迁移完成"

echo ""
echo "==> [3/5] 插入测试 license 数据..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 <<'SQL'
-- 插入测试 license（如果不存在）
INSERT INTO licenses (
    license_key,
    license_type,
    issued_at,
    expires_at,
    features,
    status
) VALUES (
    'test-trial-license-e2e',
    'trial',
    now(),
    now() + interval '30 days',
    '{"max_instances": 3, "max_requests_per_day": 10000}',
    'active'
) ON CONFLICT (license_key) DO NOTHING;

-- 插入测试企业 license（用于注册测试）
INSERT INTO licenses (
    license_key,
    license_type,
    issued_at,
    expires_at,
    features,
    status
) VALUES (
    'test-enterprise-e2e-20260712',
    'enterprise',
    now(),
    now() + interval '365 days',
    '{"max_instances": 100, "max_requests_per_day": 1000000}',
    'active'
) ON CONFLICT (license_key) DO NOTHING;
SQL
echo "✓ 测试 license 数据插入完成"

echo ""
echo "==> [4/5] 插入测试 release 数据（v1.14.0）..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 <<'SQL'
-- 插入测试 release 数据
INSERT INTO releases (
    version,
    build_seq,
    channel,
    title,
    description,
    changelog,
    image_tag,
    image_digest,
    mandatory,
    created_by,
    published_at
) VALUES (
    'v1.14.0',
    140,
    'stable',
    'Gateway v1.14.0 Stable Release',
    'E2E 测试用稳定版本',
    '- Feature: 新增自动升级功能\n- Fix: 修复心跳丢失问题\n- Improve: 性能优化',
    'llm-gateway:v1.14.0',
    'sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef',
    false,
    'e2e-test',
    now()
) ON CONFLICT (version) DO UPDATE SET
    published_at = EXCLUDED.published_at,
    updated_at = now();
SQL
echo "✓ 测试 release 数据插入完成"

echo ""
echo "==> [5/5] 验证数据完整性..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 <<'SQL'
-- 验证表存在
DO $$
DECLARE
    missing_tables TEXT[];
BEGIN
    SELECT ARRAY_AGG(t)
    INTO missing_tables
    FROM unnest(ARRAY[
        'gateway_instances',
        'instance_heartbeats',
        'releases',
        'upgrade_logs',
        'instance_release_status',
        'licenses'
    ]) AS t
    WHERE NOT EXISTS (
        SELECT 1 FROM pg_tables
        WHERE schemaname = 'public' AND tablename = t
    );

    IF array_length(missing_tables, 1) > 0 THEN
        RAISE EXCEPTION '缺少表: %', array_to_string(missing_tables, ', ');
    END IF;
END $$;

-- 验证测试数据
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM licenses WHERE license_key = 'test-trial-license-e2e') THEN
        RAISE EXCEPTION '测试 license 数据未插入';
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM releases WHERE version = 'v1.14.0') THEN
        RAISE EXCEPTION '测试 release 数据未插入';
    END IF;
END $$;
SQL
echo "✓ 数据完整性验证通过"

echo ""
echo "=========================================="
echo "✓ E2E 测试环境初始化完成"
echo "=========================================="
echo "数据库: $DB_HOST:$DB_PORT/$DB_NAME"
echo "测试 license: test-trial-license-e2e, test-enterprise-e2e-20260712"
echo "测试 release: v1.14.0"
echo "=========================================="
