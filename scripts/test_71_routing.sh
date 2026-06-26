#!/bin/bash
# 71 服务器路由和请求记录诊断脚本
# 日期: 2026-06-26
# 目的: 诊断路由问题和 empty_response 问题

set -e

echo "=========================================="
echo "71 服务器路由诊断"
echo "日期: $(date '+%Y-%m-%d %H:%M:%S')"
echo "=========================================="
echo ""

# 配置
API_BASE="https://llmgateway.internal.example.com"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-kxuser}"
DB_NAME="${DB_NAME:-llm_gateway}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

function log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

function log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

function log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查是否提供了 API key
if [ -z "$API_KEY" ]; then
    log_warn "未设置 API_KEY 环境变量"
    log_info "使用方法: API_KEY=your_key $0"
    echo ""
fi

echo "1️⃣  测试健康检查"
echo "--------------------------------------"
if curl -s -f "${API_BASE}/health" > /dev/null 2>&1; then
    log_info "健康检查通过: ${API_BASE}/health"
else
    log_error "健康检查失败: ${API_BASE}/health"
fi
echo ""

echo "2️⃣  测试 minimax-m3 路由（已知可用模型）"
echo "--------------------------------------"
if [ -n "$API_KEY" ]; then
    log_info "发送测试请求到 minimax-m3..."
    RESPONSE=$(curl -s -w "\n%{http_code}" "${API_BASE}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -d '{
            "model": "minimax-m3",
            "messages": [{"role": "user", "content": "测试"}],
            "max_tokens": 10,
            "stream": false
        }')
    
    HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    
    echo "HTTP状态码: $HTTP_CODE"
    echo "响应预览:"
    echo "$BODY" | head -c 500
    echo ""
    
    if [ "$HTTP_CODE" = "200" ]; then
        log_info "✅ minimax-m3 路由成功"
    elif echo "$BODY" | grep -q "no_candidate"; then
        log_error "❌ no_candidate 错误 - 路由失败"
    elif echo "$BODY" | grep -q "credential_reveal_failed"; then
        log_error "❌ credential_reveal_failed - 凭据解密失败"
    else
        log_warn "⚠️  未知响应状态"
    fi
else
    log_warn "跳过（需要 API_KEY）"
fi
echo ""

echo "3️⃣  检查数据库中的路由配置"
echo "--------------------------------------"
log_info "检查 minimax-m3 的配置..."
PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t << 'EOSQL'
-- 检查 provider_models
\echo '=== Provider Models for minimax-m3 ==='
SELECT pm.id, p.code AS provider, pm.raw_model_name, pm.outbound_model_name, pm.canonical_id
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name = 'minimax-m3'
ORDER BY p.code;

-- 检查 credential_model_bindings
\echo ''
\echo '=== Credential Model Bindings ==='
SELECT 
    cmb.id,
    c.id AS credential_id,
    p.code AS provider,
    pm.raw_model_name,
    cmb.available,
    c.availability_state,
    c.lifecycle_status
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3'
ORDER BY p.code, c.id;

-- 检查 model_aliases
\echo ''
\echo '=== Model Aliases ==='
SELECT raw_name, canonical_id, status
FROM model_aliases
WHERE raw_name = 'minimax-m3';

-- 检查 canonical_id 一致性
\echo ''
\echo '=== Canonical ID 一致性检查 ==='
SELECT 'model_aliases' AS source, canonical_id 
FROM model_aliases WHERE raw_name = 'minimax-m3'
UNION ALL
SELECT 'provider_models' AS source, canonical_id 
FROM provider_models WHERE raw_model_name = 'minimax-m3' LIMIT 1
UNION ALL
SELECT 'models_canonical' AS source, id AS canonical_id
FROM models_canonical WHERE canonical_name = 'minimax-m3';
EOSQL
echo ""

echo "4️⃣  检查最近的 request_logs"
echo "--------------------------------------"
PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t << 'EOSQL'
-- 最近 1 小时统计
\echo '=== 最近 1 小时统计 ==='
SELECT 
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE success = true) AS success,
    COUNT(*) FILTER (WHERE success = false) AS failed,
    COUNT(*) FILTER (WHERE error_kind = 'empty_response') AS empty_response,
    COUNT(*) FILTER (WHERE request_status = 'no_candidate') AS no_candidate,
    MAX(ts) AS latest_ts
FROM request_logs
WHERE ts > now() - interval '1 hour';

-- 最近 10 条记录
\echo ''
\echo '=== 最近 10 条 request_logs ==='
SELECT 
    to_char(ts, 'HH24:MI:SS') AS time,
    client_model,
    COALESCE(credential_id::text, 'NULL') AS cred_id,
    request_status,
    COALESCE(error_kind, 'NULL') AS error_kind,
    success,
    COALESCE(latency_ms::text, 'NULL') AS latency_ms
FROM request_logs
ORDER BY ts DESC
LIMIT 10;

-- empty_response 详细分析
\echo ''
\echo '=== empty_response 分析（最近 1 小时）==='
SELECT 
    client_model,
    COUNT(*) AS count,
    ARRAY_AGG(DISTINCT request_status) AS statuses,
    ARRAY_AGG(DISTINCT COALESCE(failure_stage, 'NULL')) AS failure_stages
FROM request_logs
WHERE ts > now() - interval '1 hour'
  AND error_kind = 'empty_response'
GROUP BY client_model
ORDER BY count DESC
LIMIT 10;
EOSQL
echo ""

echo "5️⃣  检查 credential_model_index（路由索引）"
echo "--------------------------------------"
PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t << 'EOSQL'
-- 检查 minimax-m3 的索引
\echo '=== minimax-m3 在 credential_model_index 中的记录 ==='
SELECT 
    bucket,
    credential_id,
    raw_model,
    canonical_id,
    success_rate,
    p95_latency_ms,
    active_sessions,
    score_smart
FROM credential_model_index
WHERE raw_model = 'minimax-m3'
ORDER BY bucket DESC
LIMIT 5;

-- 检查最新的索引时间
\echo ''
\echo '=== 最新的索引更新时间 ==='
SELECT 
    raw_model,
    MAX(bucket) AS latest_bucket,
    COUNT(DISTINCT credential_id) AS credential_count
FROM credential_model_index
WHERE bucket > now() - interval '1 hour'
GROUP BY raw_model
ORDER BY latest_bucket DESC
LIMIT 10;
EOSQL
echo ""

echo "6️⃣  测试 provider.GetCandidates 路径"
echo "--------------------------------------"
if [ -n "$API_KEY" ]; then
    log_info "发送测试请求并查看日志..."
    log_info "提示：在另一个终端运行以下命令查看实时日志："
    log_info "docker logs -f <gateway-container-name> 2>&1 | grep -E '(GetCandidates|autoroute|routing)'"
    echo ""
    
    # 发送请求
    curl -s -o /dev/null -w "HTTP %{http_code}\n" "${API_BASE}/v1/chat/completions" \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -d '{
            "model": "minimax-m3",
            "messages": [{"role": "user", "content": "hi"}],
            "max_tokens": 5,
            "stream": false
        }'
    
    echo ""
    log_info "请在 gateway 日志中查找以下内容："
    echo "  - provider.GetCandidates called"
    echo "  - original_model=minimax-m3"
    echo "  - candidate_count"
    echo "  - credential 详情"
else
    log_warn "跳过（需要 API_KEY）"
fi
echo ""

echo "7️⃣  检查凭据状态"
echo "--------------------------------------"
PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t << 'EOSQL'
-- 检查 minimax 相关的凭据
\echo '=== Minimax 相关凭据状态 ==='
SELECT 
    c.id,
    p.code AS provider,
    c.availability_state,
    c.quota_state,
    c.lifecycle_status,
    c.status,
    c.circuit_state,
    COALESCE(c.balance_usd::text, 'NULL') AS balance
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE p.code IN ('minimax', 'nvidia', 'minimax-anthropic')
  AND c.status != 'disabled'
ORDER BY p.code, c.id;

-- 检查是否有 routable 的凭据
\echo ''
\echo '=== v_routable_credential_models 视图 ==='
SELECT 
    credential_id,
    raw_model_name,
    is_routable,
    unavailable_reason
FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3'
ORDER BY credential_id;
EOSQL
echo ""

echo "8️⃣  检查最近的路由决策日志"
echo "--------------------------------------"
PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t << 'EOSQL'
-- 检查 routing_decision_log
\echo '=== 最近的路由决策（minimax-m3）==='
SELECT 
    to_char(ts, 'HH24:MI:SS') AS time,
    request_id,
    COALESCE(chosen_credential_id::text, 'NULL') AS chosen_cred,
    tier,
    candidates_tried,
    latency_ms
FROM routing_decision_log
WHERE model = 'minimax-m3'
  AND ts > now() - interval '1 hour'
ORDER BY ts DESC
LIMIT 10;
EOSQL
echo ""

echo "=========================================="
echo "诊断完成"
echo "=========================================="
echo ""
echo "📋 问题排查清单："
echo ""
echo "1. 路由配置问题："
echo "   - canonical_id 是否一致？"
echo "   - credential_model_bindings 是否 available=true？"
echo "   - v_routable_credential_models 是否显示 is_routable=true？"
echo ""
echo "2. 路由索引问题："
echo "   - credential_model_index 是否有 minimax-m3 的记录？"
echo "   - 索引的 bucket 时间是否新鲜（最近 10 分钟内）？"
echo ""
echo "3. 凭据状态问题："
echo "   - credentials 的 availability_state 是否为 ready？"
echo "   - lifecycle_status 是否为 active？"
echo "   - circuit_state 是否为 closed？"
echo ""
echo "4. empty_response 问题："
echo "   - 是否大量 empty_response？"
echo "   - 具体在哪些模型上？"
echo "   - failure_stage 是什么？"
echo ""
echo "5. 下一步行动："
echo "   - 如果 no_candidate：检查路由索引和凭据状态"
echo "   - 如果 credential_reveal_failed：检查加密密钥配置"
echo "   - 如果 empty_response：可能是之前修复的 bug，需要重新部署"
echo ""
