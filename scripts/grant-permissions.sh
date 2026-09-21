#!/bin/bash
# 授予 kxuser 在 public schema 的权限
#
# 使用超级用户执行此脚本

set -e

# 使用 postgres 超级用户（或其他有权限的用户）
ADMIN_DB_URL=${ADMIN_DB_URL:-"postgres://postgres:password@127.0.0.1:5432/llm_gateway?sslmode=disable"}

echo "授予 kxuser 在 public schema 的权限..."

psql "$ADMIN_DB_URL" << 'EOF'
-- 授予 kxuser 在 public schema 的使用权限
GRANT USAGE ON SCHEMA public TO kxuser;

-- 授予 kxuser 在 public schema 中创建对象的权限
GRANT CREATE ON SCHEMA public TO kxuser;

-- 授予 kxuser 对现有表的权限
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO kxuser;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO kxuser;

-- 设置默认权限（未来创建的对象）
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO kxuser;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO kxuser;

-- 验证权限
SELECT 
    nspname AS schema_name,
    rolname AS grantee,
    privilege_type
FROM (
    SELECT 
        n.nspname,
        r.rolname,
        CASE 
            WHEN has_schema_privilege(r.oid, n.oid, 'CREATE') THEN 'CREATE'
            WHEN has_schema_privilege(r.oid, n.oid, 'USAGE') THEN 'USAGE'
        END AS privilege_type
    FROM pg_namespace n
    CROSS JOIN pg_roles r
    WHERE n.nspname = 'public'
      AND r.rolname = 'kxuser'
      AND (has_schema_privilege(r.oid, n.oid, 'CREATE') 
           OR has_schema_privilege(r.oid, n.oid, 'USAGE'))
) sub
ORDER BY privilege_type;

EOF

echo "✓ 权限授予完成"
echo ""
echo "现在可以使用 kxuser 执行 migration 了："
echo "  ./scripts/test-migration-430.sh"
