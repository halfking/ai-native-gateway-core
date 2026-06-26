#!/bin/bash
# Request Logs 诊断脚本 - 仅在生产环境 71 上运行

set -e

echo "=========================================="
echo "Request Logs 诊断 - 生产环境 71"
echo "=========================================="
echo ""

# 配置
DB_CONTAINER="r112_postgres"  # 根据实际容器名修改
DB_USER="kxuser"
DB_NAME="llm_gateway"

echo "1️⃣  检查数据库中的 request_logs"
echo "--------------------------------------"
docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME << 'EOSQL'
-- 最近 24 小时统计
SELECT 
    COUNT(*) AS total_24h,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    COUNT(*) FILTER (WHERE success = false) AS failed_count,
    MIN(ts) AS earliest_ts,
    MAX(ts) AS latest_ts,
    MAX(ts) > now() - interval '5 minutes' AS has_recent_data
FROM request_logs
WHERE ts > now() - interval '24 hours';

-- 最近 10 条记录
\echo ''
\echo '最近 10 条 request_logs:'
SELECT 
    to_char(ts, 'YYYY-MM-DD HH24:MI:SS') AS time,
    client_model,
    COALESCE(credential_id::text, 'NULL') AS cred_id,
    request_status,
    success,
    latency_ms
FROM request_logs
ORDER BY ts DESC
LIMIT 10;

-- 按小时分组统计
\echo ''
\echo '最近 24 小时按小时统计:'
SELECT 
    to_char(date_trunc('hour', ts), 'YYYY-MM-DD HH24:00') AS hour,
    COUNT(*) AS count
FROM request_logs
WHERE ts > now() - interval '24 hours'
GROUP BY date_trunc('hour', ts)
ORDER BY hour DESC;
EOSQL

echo ""
echo "2️⃣  检查 tenant_id 分布"
echo "--------------------------------------"
docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME << 'EOSQL'
SELECT 
    COALESCE(tenant_id::text, 'NULL') AS tenant_id,
    COUNT(*) AS count
FROM request_logs
WHERE ts > now() - interval '24 hours'
GROUP BY tenant_id
ORDER BY count DESC
LIMIT 10;
EOSQL

echo ""
echo "3️⃣  检查 api_keys 和 tenant 关系"
echo "--------------------------------------"
docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME << 'EOSQL'
SELECT 
    ak.id AS api_key_id,
    ak.key_prefix,
    COALESCE(ak.tenant_id::text, 'NULL') AS tenant_id,
    t.name AS tenant_name,
    COUNT(rl.id) AS request_count_24h
FROM api_keys ak
LEFT JOIN tenants t ON t.id = ak.tenant_id
LEFT JOIN request_logs rl ON rl.api_key_id = ak.id AND rl.ts > now() - interval '24 hours'
WHERE ak.status = 'active'
GROUP BY ak.id, ak.key_prefix, ak.tenant_id, t.name
ORDER BY request_count_24h DESC
LIMIT 10;
EOSQL

echo ""
echo "4️⃣  检查当前登录的 admin 用户"
echo "--------------------------------------"
docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME << 'EOSQL'
SELECT 
    id,
    username,
    role,
    COALESCE(tenant_id::text, 'NULL') AS tenant_id,
    status
FROM admin_users
WHERE status = 'active'
ORDER BY role, username;
EOSQL

echo ""
echo "5️⃣  模拟前端 API 查询（最近 24 小时）"
echo "--------------------------------------"
docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME << 'EOSQL'
-- 模拟 super_admin 查询（无 tenant 过滤）
\echo 'Super Admin 视图:'
SELECT COUNT(*) AS count_no_filter
FROM request_logs rl
WHERE rl.ts >= now() - interval '24 hours'
  AND rl.ts <= now();

-- 模拟 tenant_admin 查询（假设 tenant_id=1）
\echo ''
\echo 'Tenant Admin (tenant_id=1) 视图:'
SELECT COUNT(*) AS count_with_tenant_filter
FROM request_logs rl
LEFT JOIN api_keys ak ON ak.id = rl.api_key_id
WHERE rl.ts >= now() - interval '24 hours'
  AND rl.ts <= now()
  AND ak.tenant_id = 1
  AND rl.tenant_id = 1;
EOSQL

echo ""
echo "6️⃣  测试实际的 /api/logs SQL"
echo "--------------------------------------"
docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME << 'EOSQL'
-- 完整的查询（模拟 listLogs）
WITH filtered AS (
    SELECT 
        rl.ts,
        rl.request_id,
        rl.client_model,
        rl.credential_id,
        rl.request_status,
        rl.success
    FROM request_logs rl
    WHERE rl.ts >= now() - interval '24 hours'
      AND rl.ts <= now()
    ORDER BY rl.ts DESC
    LIMIT 10
)
SELECT * FROM filtered;
EOSQL

echo ""
echo "7️⃣  检查 gateway 日志中的 audit 记录"
echo "--------------------------------------"
GATEWAY_CONTAINER=$(docker ps --filter "name=gateway" --format "{{.Names}}" | head -1)
if [ -n "$GATEWAY_CONTAINER" ]; then
    echo "Gateway 容器: $GATEWAY_CONTAINER"
    echo ""
    echo "最近 10 条 audit 记录:"
    docker logs $GATEWAY_CONTAINER 2>&1 | grep "audit: request completed" | tail -10
    echo ""
    echo "最近 10 条 listLogs 调用（如果有）:"
    docker logs $GATEWAY_CONTAINER 2>&1 | grep "listLogs" | tail -10 || echo "未找到 listLogs 日志"
else
    echo "❌ 未找到 gateway 容器"
fi

echo ""
echo "=========================================="
echo "诊断完成！"
echo "=========================================="
echo ""
echo "📋 诊断总结："
echo ""
echo "请检查上述输出，特别关注："
echo "1. 是否有最近 24 小时的数据？"
echo "2. tenant_id 分布如何？"
echo "3. 你的 admin 用户是 super_admin 还是 tenant_admin？"
echo "4. 如果是 tenant_admin，request_logs 的 tenant_id 是否匹配？"
echo ""
echo "下一步："
echo "1. 如果数据库有数据但前端看不到，请测试 API："
echo "   curl https://llmgateway.internal.example.com/api/logs -H 'Authorization: Bearer YOUR_TOKEN'"
echo ""
echo "2. 如果 API 也返回空，检查浏览器 DevTools Network 标签"
echo "   查看实际的请求参数（特别是 from/to 时间范围）"
echo ""
