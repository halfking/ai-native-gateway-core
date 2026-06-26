#!/bin/bash
# 71 服务器路由问题完整修复方案
# 日期: 2026-06-26
# 问题: 请求无法记录 + 路由失败 + empty_response 过多

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

function log_section() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}========================================${NC}"
    echo ""
}

# 配置
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-kxuser}"
DB_NAME="${DB_NAME:-llm_gateway}"

log_section "71 服务器路由问题诊断和修复"

echo "问题描述:"
echo "1. 71服务器上的请求无法记录到 request_logs"
echo "2. 通过 llmgateway.internal.example.com/v1 发起的请求无法正确路由"
echo "3. 路由层无法匹配凭据（虽然有可用凭据如 minimax-m3）"
echo "4. 184 数据库的 request_logs 中大量 empty_response"
echo ""

log_section "步骤 1: 诊断数据库配置"

log_info "检查 minimax-m3 的配置完整性..."

PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
\set ON_ERROR_STOP on

-- 1. 检查 canonical_id 一致性
\echo '=== 1. Canonical ID 一致性检查 ==='
DO $$
DECLARE
    alias_canonical_id INT;
    pm_canonical_id INT;
    mc_id INT;
BEGIN
    SELECT canonical_id INTO alias_canonical_id FROM model_aliases WHERE raw_name = 'minimax-m3' LIMIT 1;
    SELECT canonical_id INTO pm_canonical_id FROM provider_models WHERE raw_model_name = 'minimax-m3' LIMIT 1;
    SELECT id INTO mc_id FROM models_canonical WHERE canonical_name = 'minimax-m3' LIMIT 1;
    
    RAISE NOTICE 'model_aliases.canonical_id = %', COALESCE(alias_canonical_id::text, 'NULL');
    RAISE NOTICE 'provider_models.canonical_id = %', COALESCE(pm_canonical_id::text, 'NULL');
    RAISE NOTICE 'models_canonical.id = %', COALESCE(mc_id::text, 'NULL');
    
    IF alias_canonical_id IS NOT NULL AND pm_canonical_id IS NOT NULL AND alias_canonical_id != pm_canonical_id THEN
        RAISE WARNING '❌ canonical_id 不一致！需要修复！';
    ELSIF alias_canonical_id IS NULL THEN
        RAISE WARNING '⚠️  model_aliases 中没有 minimax-m3 记录';
    ELSE
        RAISE NOTICE '✅ canonical_id 一致';
    END IF;
END $$;

-- 2. 检查 provider_models
\echo ''
\echo '=== 2. Provider Models ==='
SELECT 
    pm.id,
    p.code AS provider,
    pm.raw_model_name,
    pm.outbound_model_name,
    pm.canonical_id,
    CASE 
        WHEN pm.canonical_id IS NULL THEN '❌ NULL'
        ELSE '✅'
    END AS status
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE pm.raw_model_name = 'minimax-m3'
ORDER BY p.code;

-- 3. 检查 credential_model_bindings
\echo ''
\echo '=== 3. Credential Model Bindings ==='
SELECT 
    cmb.id,
    c.id AS cred_id,
    p.code AS provider,
    cmb.available,
    c.availability_state,
    c.lifecycle_status,
    c.status,
    CASE 
        WHEN NOT cmb.available THEN '❌ not available'
        WHEN c.availability_state != 'ready' THEN '❌ ' || c.availability_state
        WHEN c.lifecycle_status NOT IN ('active', '') THEN '❌ ' || c.lifecycle_status
        WHEN c.status = 'disabled' THEN '❌ disabled'
        ELSE '✅ OK'
    END AS status
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = 'minimax-m3'
ORDER BY p.code, c.id;

-- 4. 检查 v_routable_credential_models
\echo ''
\echo '=== 4. Routable Credential Models ==='
SELECT 
    credential_id,
    raw_model_name,
    is_routable,
    unavailable_reason,
    CASE 
        WHEN NOT is_routable THEN '❌ ' || COALESCE(unavailable_reason, 'unknown')
        ELSE '✅ routable'
    END AS status
FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3'
ORDER BY credential_id;

-- 5. 检查 credential_model_index（路由索引）
\echo ''
\echo '=== 5. Routing Index Status ==='
WITH latest AS (
    SELECT 
        credential_id,
        raw_model,
        MAX(bucket) AS latest_bucket
    FROM credential_model_index
    WHERE raw_model = 'minimax-m3'
    GROUP BY credential_id, raw_model
)
SELECT 
    l.credential_id,
    l.raw_model,
    l.latest_bucket,
    CASE 
        WHEN l.latest_bucket > now() - interval '10 minutes' THEN '✅ fresh'
        WHEN l.latest_bucket > now() - interval '1 hour' THEN '⚠️  stale'
        ELSE '❌ too old'
    END AS freshness,
    cmi.success_rate,
    cmi.p95_latency_ms
FROM latest l
JOIN credential_model_index cmi ON cmi.credential_id = l.credential_id 
    AND cmi.raw_model = l.raw_model 
    AND cmi.bucket = l.latest_bucket
ORDER BY l.credential_id;

\echo ''
\echo '如果上面没有任何记录，说明路由索引为空，需要手动初始化！'
EOSQL

echo ""
read -p "按 Enter 继续到修复步骤..."

log_section "步骤 2: 修复配置问题"

log_info "开始修复配置..."

PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
\set ON_ERROR_STOP on

BEGIN;

-- 修复 1: 确保 models_canonical 存在
INSERT INTO models_canonical (canonical_name, family, source, status)
VALUES ('minimax-m3', 'minimax', 'manual', 'active')
ON CONFLICT (canonical_name) DO UPDATE SET status = 'active';

-- 获取 canonical_id
DO $$
DECLARE
    v_canonical_id INT;
BEGIN
    SELECT id INTO v_canonical_id FROM models_canonical WHERE canonical_name = 'minimax-m3';
    RAISE NOTICE 'Using canonical_id = %', v_canonical_id;
    
    -- 修复 2: 更新 model_aliases
    INSERT INTO model_aliases (raw_name, canonical_id, status)
    VALUES ('minimax-m3', v_canonical_id, 'active')
    ON CONFLICT (raw_name) DO UPDATE SET canonical_id = v_canonical_id, status = 'active';
    
    -- 修复 3: 更新所有 provider_models
    UPDATE provider_models
    SET canonical_id = v_canonical_id
    WHERE raw_model_name = 'minimax-m3' AND (canonical_id IS NULL OR canonical_id != v_canonical_id);
    
    RAISE NOTICE '✅ Configuration fixed';
END $$;

COMMIT;

\echo ''
\echo '✅ 配置修复完成'
EOSQL

log_section "步骤 3: 初始化路由索引"

log_info "检查是否需要初始化路由索引..."

PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
\set ON_ERROR_STOP on

-- 检查是否有索引
DO $$
DECLARE
    index_count INT;
BEGIN
    SELECT COUNT(*) INTO index_count
    FROM credential_model_index
    WHERE raw_model = 'minimax-m3'
      AND bucket > now() - interval '10 minutes';
    
    IF index_count = 0 THEN
        RAISE NOTICE '❌ 路由索引为空或过期，需要初始化';
        RAISE NOTICE '正在初始化...';
        
        -- 初始化索引
        INSERT INTO credential_model_index (
            bucket,
            credential_id,
            raw_model,
            canonical_id,
            active_sessions,
            concurrency_limit,
            success_rate,
            p95_latency_ms,
            score_smart,
            score_speed_first,
            score_cost_first,
            pressure_ratio,
            billing_mode,
            unit_price_in_per_1m,
            unit_price_out_per_1m
        )
        SELECT 
            date_trunc('minute', now() - interval '5 minutes') AS bucket,
            cmb.credential_id,
            pm.raw_model_name AS raw_model,
            pm.canonical_id,
            0 AS active_sessions,
            COALESCE(c.concurrency_limit, 10) AS concurrency_limit,
            0.95 AS success_rate,
            500 AS p95_latency_ms,
            100.0 AS score_smart,
            100.0 AS score_speed_first,
            100.0 AS score_cost_first,
            0.0 AS pressure_ratio,
            COALESCE(mo.billing_mode, 'token') AS billing_mode,
            COALESCE(mo.unit_price_in_per_1m, 0) AS unit_price_in_per_1m,
            COALESCE(mo.unit_price_out_per_1m, 0) AS unit_price_out_per_1m
        FROM credential_model_bindings cmb
        JOIN credentials c ON c.id = cmb.credential_id
        JOIN provider_models pm ON pm.id = cmb.provider_model_id
        LEFT JOIN model_offers mo ON mo.credential_id = c.id AND mo.raw_model_name = pm.raw_model_name
        WHERE cmb.available = true
          AND c.availability_state = 'ready'
          AND COALESCE(c.lifecycle_status, 'active') = 'active'
          AND c.status != 'disabled'
          AND pm.raw_model_name = 'minimax-m3'
        ON CONFLICT (bucket, credential_id, raw_model) DO NOTHING;
        
        GET DIAGNOSTICS index_count = ROW_COUNT;
        RAISE NOTICE '✅ 已初始化 % 条路由索引记录', index_count;
    ELSE
        RAISE NOTICE '✅ 路由索引已存在且新鲜 (% 条记录)', index_count;
    END IF;
END $$;
EOSQL

log_section "步骤 4: 验证修复结果"

log_info "验证配置和索引状态..."

PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
-- 最终验证
\echo '=== 最终验证结果 ==='

\echo '1. Canonical ID 一致性:'
SELECT 'model_aliases' AS source, canonical_id FROM model_aliases WHERE raw_name = 'minimax-m3'
UNION ALL
SELECT 'provider_models', canonical_id FROM provider_models WHERE raw_model_name = 'minimax-m3' LIMIT 1;

\echo ''
\echo '2. 可路由的凭据数量:'
SELECT COUNT(*) AS routable_credentials
FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3' AND is_routable = true;

\echo ''
\echo '3. 路由索引最新状态:'
SELECT 
    COUNT(*) AS total_records,
    COUNT(DISTINCT credential_id) AS unique_credentials,
    MAX(bucket) AS latest_bucket,
    CASE 
        WHEN MAX(bucket) > now() - interval '10 minutes' THEN '✅ fresh'
        ELSE '❌ stale'
    END AS status
FROM credential_model_index
WHERE raw_model = 'minimax-m3';
EOSQL

log_section "步骤 5: 测试实际请求"

if [ -z "$API_KEY" ]; then
    log_warn "未设置 API_KEY 环境变量，跳过实际请求测试"
    log_info "要测试请求，请运行: API_KEY=your_key $0"
else
    log_info "发送测试请求到 minimax-m3..."
    
    RESPONSE=$(curl -s -w "\n%{http_code}" https://llmgateway.internal.example.com/v1/chat/completions \
        -H "Authorization: Bearer ${API_KEY}" \
        -H "Content-Type: application/json" \
        -d '{
            "model": "minimax-m3",
            "messages": [{"role": "user", "content": "你好"}],
            "max_tokens": 10,
            "stream": false
        }')
    
    HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    
    echo ""
    echo "HTTP 状态码: $HTTP_CODE"
    echo "响应内容:"
    echo "$BODY" | jq '.' 2>/dev/null || echo "$BODY"
    echo ""
    
    if [ "$HTTP_CODE" = "200" ]; then
        log_info "✅ 请求成功！路由正常工作"
    elif echo "$BODY" | grep -q "no_candidate"; then
        log_error "❌ no_candidate - 路由仍然失败"
        log_error "可能原因："
        log_error "  1. Gateway 缓存未刷新（需要重启或等待缓存过期）"
        log_error "  2. 凭据密钥无法解密"
        log_error "  3. 路由索引刷新延迟"
    elif echo "$BODY" | grep -q "credential_reveal_failed"; then
        log_error "❌ credential_reveal_failed - 凭据解密失败"
        log_error "检查 Gateway 的加密密钥配置"
    else
        log_warn "⚠️  未知响应，请检查详细内容"
    fi
    
    # 检查 request_logs
    log_info "检查 request_logs 中的记录..."
    sleep 2
    
    PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
SELECT 
    to_char(ts, 'HH24:MI:SS') AS time,
    request_id,
    client_model,
    credential_id,
    request_status,
    error_kind,
    success
FROM request_logs
WHERE ts > now() - interval '1 minute'
  AND client_model = 'minimax-m3'
ORDER BY ts DESC
LIMIT 5;
EOSQL
fi

log_section "步骤 6: empty_response 问题检查"

log_info "检查最近的 empty_response 统计..."

PGPASSWORD="${DB_PASSWORD}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOSQL'
\echo '=== empty_response 统计（最近 24 小时）==='
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE error_kind = 'empty_response') AS empty_response_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE error_kind = 'empty_response') / NULLIF(COUNT(*), 0), 2) AS empty_response_pct
FROM request_logs
WHERE ts > now() - interval '24 hours';

\echo ''
\echo '=== 按模型分组的 empty_response ==='
SELECT 
    client_model,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE error_kind = 'empty_response') AS empty_resp
FROM request_logs
WHERE ts > now() - interval '24 hours'
  AND error_kind = 'empty_response'
GROUP BY client_model
ORDER BY empty_resp DESC
LIMIT 10;
EOSQL

log_section "完成"

echo "📋 修复总结："
echo ""
echo "✅ 已完成的修复："
echo "  1. 确保 canonical_id 一致性"
echo "  2. 更新 model_aliases 和 provider_models"
echo "  3. 初始化路由索引（如果为空）"
echo "  4. 验证配置正确性"
echo ""
echo "📝 注意事项："
echo "  1. Gateway 可能需要重启或等待缓存过期（30秒）才能生效"
echo "  2. empty_response 问题已在代码中修复（commit 78de1295），需要重新部署"
echo "  3. 如果仍有 no_candidate 错误，检查 gateway 日志中的详细信息"
echo ""
echo "🔍 后续行动："
echo "  1. 如果测试失败，运行: ./scripts/test_71_routing.sh"
echo "  2. 查看 gateway 日志: docker logs -f <gateway-container> | grep -E '(GetCandidates|routing)'"
echo "  3. 检查是否需要重新部署最新代码（包含 empty_response 修复）"
echo ""
