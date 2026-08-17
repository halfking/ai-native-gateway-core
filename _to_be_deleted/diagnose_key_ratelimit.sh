#!/bin/bash
# 诊断 API Key 限速问题
# 使用方法: ./diagnose_key_ratelimit.sh sk-1vH6C2I9pywyvUXa

set -euo pipefail

KEY_PREFIX="${1:-sk-1vH6C2I9pywyvUXa}"

echo "=== API Key 限速诊断工具 ==="
echo "查询 Key Prefix: $KEY_PREFIX"
echo ""

# 尝试从环境变量获取数据库连接
DB_URL="${LLM_GATEWAY_DATABASE_URL:-${DATABASE_URL:-}}"

if [ -z "$DB_URL" ]; then
    echo "错误: 未设置数据库连接环境变量"
    echo "请设置 LLM_GATEWAY_DATABASE_URL 或 DATABASE_URL"
    echo ""
    echo "示例:"
    echo "  export LLM_GATEWAY_DATABASE_URL='postgres://user:pass@host:port/dbname'"
    exit 1
fi

echo "1. 查询 API Key 配置"
echo "----------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    id,
    key_prefix,
    rate_limit_rpm,
    rate_limit_concurrent,
    rate_limit_tpm,
    key_tier,
    status,
    enabled,
    application_id,
    tenant_id,
    owner_user,
    last_used_at,
    created_at
FROM api_keys 
WHERE key_prefix = '$KEY_PREFIX'
LIMIT 1;
SQL

echo ""
echo "2. 查询该 Key 的最近请求记录"
echo "----------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    id,
    request_id,
    created_at,
    error_kind,
    failure_stage,
    provider_name,
    credential_id,
    status_code
FROM request_logs_2026_07
WHERE api_key_id = (SELECT id FROM api_keys WHERE key_prefix = '$KEY_PREFIX' LIMIT 1)
  AND created_at > NOW() - INTERVAL '1 hour'
  AND error_kind IS NOT NULL
ORDER BY created_at DESC
LIMIT 10;
SQL

echo ""
echo "3. 查询该 Key 使用的 minimax 凭证状态"
echo "----------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    c.id as credential_id,
    c.display_name,
    c.enabled,
    c.availability_status,
    c.availability_recover_at,
    c.breaker_state,
    c.breaker_opened_at,
    c.concurrent_limit,
    c.rpm_limit,
    p.name as provider_name,
    p.vendor_name
FROM credentials c
JOIN providers p ON c.provider_id = p.id
WHERE p.vendor_name LIKE '%minimax%'
  AND c.enabled = true
ORDER BY c.id;
SQL

echo ""
echo "4. 检查全局限速器配置"
echo "----------------------------------------"
psql "$DB_URL" <<SQL
SELECT key, value, description
FROM app_settings
WHERE key LIKE '%rate_limit%'
ORDER BY key;
SQL

echo ""
echo "=== 诊断建议 ==="
echo ""
echo "1. 如果 rate_limit_rpm/concurrent/tpm 为 NULL，则使用 tier 默认值"
echo "   - system: 300 RPM, 50 concurrent"
echo "   - production: 60 RPM, 20 concurrent"
echo "   - default: 12 RPM, 6 concurrent"
echo "   - applicant: 6 RPM, 2 concurrent"
echo ""
echo "2. 如果值为 0，表示明确设置为无限制"
echo ""
echo "3. 如果 'User API Key Rate limit exceeded' 错误来自 MiniMax API，"
echo "   可能是 MiniMax 供应商侧对您提供的凭证进行了限速"
echo ""
echo "4. 检查 credentials 表中 minimax 凭证的 concurrent_limit 和 rpm_limit"
echo "   这些是供应商凭证级别的限制"
