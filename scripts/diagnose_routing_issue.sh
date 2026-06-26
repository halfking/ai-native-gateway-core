#!/bin/bash
# 诊断脚本：71服务器路由和凭据匹配问题排查
# 使用方法：./scripts/diagnose_routing_issue.sh [模型名称]

set -e

MODEL="${1:-minimax-m3}"
DB_URL="${LLM_GATEWAY_DATABASE_URL:-postgresql://postgres:Kaixuan2023@192.168.1.184:5432/llm_gateway_production?sslmode=disable}"

echo "=========================================="
echo "LLM Gateway 路由诊断工具"
echo "=========================================="
echo "目标模型: $MODEL"
echo "数据库: $DB_URL"
echo ""

# 1. 检查最近1小时的请求统计
echo "1. 最近1小时请求统计"
echo "-------------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    COUNT(*) as total_requests,
    COUNT(CASE WHEN error_kind = 'empty_response' THEN 1 END) as empty_response_count,
    ROUND(COUNT(CASE WHEN error_kind = 'empty_response' THEN 1 END)::numeric / NULLIF(COUNT(*), 0)::numeric * 100, 2) as empty_response_pct,
    COUNT(CASE WHEN error_kind = 'no_candidate' THEN 1 END) as no_candidate_count,
    COUNT(CASE WHEN error_kind = 'model_not_found' THEN 1 END) as model_not_found_count,
    COUNT(CASE WHEN success = true THEN 1 END) as success_count,
    COUNT(CASE WHEN success = false THEN 1 END) as failure_count
FROM request_logs 
WHERE ts > NOW() - INTERVAL '1 hour';
SQL

echo ""
echo "2. 检查指定模型的候选凭据（来自 v_routable_credential_models）"
echo "-------------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    c.id as credential_id,
    c.label as credential_label,
    c.status as credential_status,
    c.lifecycle_status,
    c.availability_state,
    c.quota_state,
    c.circuit_state,
    p.id as provider_id,
    p.display_name as provider_name,
    p.enabled as provider_enabled,
    p.manual_disabled as provider_manual_disabled,
    mo.available as offer_available,
    v.is_routable,
    v.unavailable_reason,
    mo.manual_priority,
    mo.routing_tier,
    mo.weight,
    COALESCE(mo.success_rate, 0.9) as success_rate
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN v_routable_credential_models v
    ON v.credential_id = mo.credential_id
    AND v.raw_model_name = mo.raw_model_name
WHERE p.tenant_id = 'default'
  AND (lower(mo.raw_model_name) = lower('$MODEL') 
       OR lower(mo.standardized_name) = lower('$MODEL'))
ORDER BY 
    CASE COALESCE(mo.billing_mode, 'token')
        WHEN 'free' THEN 1
        WHEN 'token_plan' THEN 1
        ELSE 2
    END,
    COALESCE(mo.manual_priority, 99),
    COALESCE(mo.routing_tier, 2),
    COALESCE(mo.weight, 100) DESC;
SQL

echo ""
echo "3. 检查最近50次该模型的请求成功率"
echo "-------------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    c.id as credential_id,
    c.label,
    mo.raw_model_name,
    COUNT(*) as total,
    COUNT(CASE WHEN rl.success THEN 1 END) as success_count,
    ROUND(COUNT(CASE WHEN rl.success THEN 1 END)::numeric / COUNT(*)::numeric, 3) as success_rate,
    COUNT(CASE WHEN rl.error_kind = 'empty_response' THEN 1 END) as empty_response_count
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
JOIN model_offers mo ON mo.credential_id = c.id 
    AND lower(mo.raw_model_name) = lower(rl.outbound_model)
WHERE lower(rl.client_model) = lower('$MODEL')
  AND rl.ts > NOW() - INTERVAL '1 hour'
GROUP BY c.id, c.label, mo.raw_model_name
ORDER BY total DESC
LIMIT 10;
SQL

echo ""
echo "4. 检查 model_probe_state（探测状态）"
echo "-------------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    mps.credential_id,
    c.label as credential_label,
    mps.raw_model_name,
    mps.state,
    mps.probe_count,
    mps.consecutive_failures,
    mps.last_probe_at,
    mps.next_probe_at,
    mps.error_message
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
WHERE lower(mps.raw_model_name) = lower('$MODEL')
ORDER BY mps.last_probe_at DESC NULLS LAST
LIMIT 10;
SQL

echo ""
echo "5. 最近10条该模型的失败请求详情"
echo "-------------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    rl.ts,
    rl.request_id,
    rl.client_model,
    rl.outbound_model,
    rl.credential_id,
    c.label as credential_label,
    rl.error_kind,
    rl.failure_stage,
    rl.failure_detail_code,
    LEFT(rl.response_preview, 100) as response_preview_short
FROM request_logs rl
LEFT JOIN credentials c ON c.id = rl.credential_id
WHERE lower(rl.client_model) = lower('$MODEL')
  AND rl.success = false
  AND rl.ts > NOW() - INTERVAL '1 hour'
ORDER BY rl.ts DESC
LIMIT 10;
SQL

echo ""
echo "6. 检查质量门限过滤（recent_success_rate）"
echo "-------------------------------------------"
psql "$DB_URL" <<SQL
SELECT 
    c.id as credential_id,
    c.label,
    mo.raw_model_name,
    rsr.rate as recent_success_rate,
    rsr.samples as recent_samples,
    CASE 
        WHEN rsr.samples >= 20 AND COALESCE(rsr.rate, 1.0) < 0.3 THEN 'FILTERED_BY_STRICT_GATE'
        WHEN rsr.samples >= 20 AND COALESCE(rsr.rate, 1.0) < 0.0 THEN 'FILTERED_BY_RELAXED_GATE'
        ELSE 'PASSES_QUALITY_GATE'
    END as gate_status
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50) AS rsr
WHERE p.tenant_id = 'default'
  AND (lower(mo.raw_model_name) = lower('$MODEL') 
       OR lower(mo.standardized_name) = lower('$MODEL'))
ORDER BY rsr.rate DESC NULLS LAST;
SQL

echo ""
echo "=========================================="
echo "诊断完成"
echo "=========================================="
echo ""
echo "常见问题修复建议："
echo ""
echo "1. 如果 is_routable = false："
echo "   - 检查 unavailable_reason 字段"
echo "   - 可能需要手动启用凭据或提供商"
echo ""
echo "2. 如果 empty_response_count 很高："
echo "   - 检查是否已部署最新的 empty_response 修复（提交 78de1295）"
echo "   - 检查 response_preview 是否为空"
echo ""
echo "3. 如果 no_candidate 错误："
echo "   - 检查是否所有凭据都被质量门限过滤"
echo "   - 检查 model_probe_state 是否为 broken_confirmed"
echo ""
echo "4. 如果 success_rate < 0.3："
echo "   - 凭据会被严格质量门限过滤"
echo "   - 系统会回退到 0.0 阈值重试"
echo ""
