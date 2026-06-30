#!/bin/bash
# LLM Gateway 路由诊断脚本
# 用于诊断为什么特定模型返回0个候选节点

set -e

SERVER="${SERVER:-__HOST_71_IP__}"
PORT="${PORT:-25022}"
USER="${USER:-root}"
# SSHPASS must be set in the environment before running this script.
# Example: SSHPASS='xxx' bash scripts/diagnose-routing.sh <model>
if [ -z "${SSHPASS:-}" ]; then
  echo "ERROR: SSHPASS environment variable is not set. Export it before running." >&2
  exit 1
fi

DB_CONTAINER="${DB_CONTAINER:-llm-gateway-pg-71-replica}"
DB_USER="${DB_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-llm_gateway}"

# 参数检查
if [ $# -lt 1 ]; then
    echo "用法: $0 <模型名称> [profile]"
    echo "示例: $0 minimax-m2.7-quickspeed"
    echo "示例: $0 glm-5.2 default"
    exit 1
fi

MODEL_NAME="$1"
PROFILE="${2:-}"

echo "================================================"
echo "LLM Gateway 路由诊断"
echo "================================================"
echo "模型: $MODEL_NAME"
echo "Profile: ${PROFILE:-<空>}"
echo "服务器: $SERVER"
echo ""

# 步骤1: 检查 model_offers 表中是否有该模型
echo "[1/7] 检查 model_offers 中的记录..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME" << EOSQL
SELECT 
    mo.id,
    mo.credential_id,
    c.provider_id,
    p.catalog_code,
    mo.raw_model_name,
    mo.standardized_name,
    COALESCE(mo.outbound_model_name, mo.raw_model_name) AS effective_name,
    mo.routing_tier,
    c.lifecycle_status
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE lower(mo.raw_model_name) = lower('$MODEL_NAME')
   OR lower(mo.standardized_name) = lower('$MODEL_NAME')
ORDER BY mo.routing_tier, mo.weight DESC;
EOSQL
echo ""

# 步骤2: 检查 v_routable_credential_models 视图
echo "[2/7] 检查路由视图 v_routable_credential_models..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME" << EOSQL
SELECT 
    credential_id,
    raw_model_name,
    is_routable,
    unavailable_reason
FROM v_routable_credential_models
WHERE lower(raw_model_name) = lower('$MODEL_NAME')
ORDER BY credential_id;
EOSQL
echo ""

# 步骤3: 检查 model_probe_state 表
echo "[3/7] 检查模型探测状态..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME" << EOSQL
SELECT 
    credential_id,
    raw_model_name,
    state,
    consecutive_failures,
    last_checked_at,
    last_success_at,
    last_failure_at,
    last_error
FROM model_probe_state
WHERE lower(raw_model_name) = lower('$MODEL_NAME')
ORDER BY credential_id;
EOSQL
echo ""

# 步骤4: 检查 recent_success_rate
echo "[4/7] 检查最近成功率..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME" << EOSQL
-- 检查 recent_success_rate 函数是否存在
SELECT 
    proname,
    pg_get_functiondef(oid) AS definition
FROM pg_proc
WHERE proname = 'recent_success_rate'
LIMIT 1;

-- 如果函数存在，查询每个凭据的成功率
DO \$\$
DECLARE
    cred_row RECORD;
BEGIN
    FOR cred_row IN 
        SELECT DISTINCT mo.credential_id, mo.raw_model_name
        FROM model_offers mo
        WHERE lower(mo.raw_model_name) = lower('$MODEL_NAME')
           OR lower(mo.standardized_name) = lower('$MODEL_NAME')
    LOOP
        RAISE NOTICE 'Credential %, Model %: %', 
            cred_row.credential_id, 
            cred_row.raw_model_name,
            (SELECT row_to_json(r) FROM recent_success_rate(cred_row.credential_id, cred_row.raw_model_name, 50) r);
    END LOOP;
END \$\$;
EOSQL
echo ""

# 步骤5: 检查 credentials 和 providers 的状态
echo "[5/7] 检查凭据和提供商状态..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME" << EOSQL
SELECT 
    c.id AS credential_id,
    p.id AS provider_id,
    p.catalog_code,
    c.lifecycle_status AS cred_lifecycle,
    c.availability_state,
    c.circuit_state,
    c.quota_state,
    c.status AS cred_status,
    p.manual_disabled AS provider_disabled,
    c.manual_disabled AS cred_disabled
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE lower(mo.raw_model_name) = lower('$MODEL_NAME')
   OR lower(mo.standardized_name) = lower('$MODEL_NAME')
ORDER BY c.id;
EOSQL
echo ""

# 步骤6: 检查 model_aliases
echo "[6/7] 检查模型别名..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME" << EOSQL
SELECT 
    ma.raw_name,
    ma.canonical_id,
    mc.canonical_name,
    ma.status,
    ma.client_profiles
FROM model_aliases ma
LEFT JOIN models_canonical mc ON mc.id = ma.canonical_id
WHERE lower(ma.raw_name) = lower('$MODEL_NAME')
   OR EXISTS (
       SELECT 1 FROM models_canonical mc2
       WHERE mc2.id = ma.canonical_id
         AND lower(mc2.canonical_name) = lower('$MODEL_NAME')
   );
EOSQL
echo ""

# 步骤7: 模拟完整的路由查询
echo "[7/7] 模拟完整路由查询（检查为什么返回0）..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME" << EOSQL
-- 完整的路由查询，带过滤条件注释
SELECT 
    c.id::int AS credential_id,
    p.id::int AS provider_id,
    COALESCE(mo.outbound_model_name, mo.raw_model_name) AS model_name,
    -- 过滤条件检查
    (p.tenant_id = 'default') AS tenant_ok,
    (COALESCE(mc.status, 'active') != 'disabled') AS canonical_not_disabled,
    (COALESCE(c.status, 'active') NOT IN ('disabled')) AS cred_not_disabled,
    (v.is_routable = TRUE) AS is_routable,
    (NOT EXISTS (
        SELECT 1 FROM model_probe_state mps
        WHERE mps.credential_id = c.id
          AND mps.raw_model_name = mo.raw_model_name
          AND mps.state = 'broken_confirmed'
    )) AS not_broken_confirmed,
    rsr.samples AS recent_samples,
    rsr.rate AS recent_rate,
    (NOT (rsr.samples >= 20 AND COALESCE(rsr.rate, 1.0) < 0.3)) AS rate_gate_passed,
    -- 模型匹配检查
    (lower(mo.raw_model_name) = lower('$MODEL_NAME')) AS raw_match,
    (lower(mo.standardized_name) = lower('$MODEL_NAME')) AS std_match,
    (EXISTS (
        SELECT 1 FROM model_aliases ma2
        WHERE lower(ma2.raw_name) = lower('$MODEL_NAME')
          AND COALESCE(ma2.status, 'active') = 'active'
          AND (
              (mo.canonical_id IS NOT NULL AND ma2.canonical_id = mo.canonical_id)
              OR (mo.canonical_id IS NULL AND ma2.canonical_id IS NULL)
          )
    )) AS alias_match,
    -- 原始状态
    c.availability_state,
    c.circuit_state,
    v.unavailable_reason
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN v_routable_credential_models v
       ON v.credential_id = mo.credential_id
      AND v.raw_model_name = mo.raw_model_name
LEFT JOIN model_aliases ma
       ON lower(ma.raw_name) = lower(mo.raw_model_name)
      AND COALESCE(ma.status, 'active') = 'active'
LEFT JOIN models_canonical mc ON mc.id = COALESCE(mo.canonical_id, ma.canonical_id)
CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50) AS rsr
WHERE (
    lower(mo.raw_model_name) = lower('$MODEL_NAME')
    OR lower(mo.standardized_name) = lower('$MODEL_NAME')
    OR EXISTS (
        SELECT 1 FROM model_aliases ma2
        WHERE lower(ma2.raw_name) = lower('$MODEL_NAME')
          AND COALESCE(ma2.status, 'active') = 'active'
          AND (
              (mo.canonical_id IS NOT NULL AND ma2.canonical_id = mo.canonical_id)
              OR (mo.canonical_id IS NULL AND ma2.canonical_id IS NULL)
          )
    )
)
ORDER BY c.id;
EOSQL
echo ""

echo "================================================"
echo "诊断完成"
echo "================================================"
echo ""
echo "常见问题排查："
echo ""
echo "1. 如果 is_routable = FALSE："
echo "   - 检查 unavailable_reason 字段"
echo "   - 可能是 manual_disabled 或凭据状态问题"
echo ""
echo "2. 如果 not_broken_confirmed = FALSE："
echo "   - 模型被探测标记为 broken_confirmed"
echo "   - 修复方法："
echo "     UPDATE model_probe_state"
echo "     SET state = 'healthy', consecutive_failures = 0"
echo "     WHERE credential_id = <id> AND raw_model_name = '$MODEL_NAME';"
echo ""
echo "3. 如果 rate_gate_passed = FALSE："
echo "   - 最近成功率 < 30% (samples >= 20)"
echo "   - 需要等待成功请求积累，或手动清除 request_logs 中的失败记录"
echo ""
echo "4. 如果所有匹配字段都是 FALSE："
echo "   - 模型名称不匹配"
echo "   - 检查 model_aliases 表是否有对应别名"
echo ""
