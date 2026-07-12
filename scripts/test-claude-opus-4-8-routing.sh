#!/usr/bin/env bash
# test-claude-opus-4-8-routing.sh
# 测试 claude-opus-4-8 模型的路由配置
#
# 故障场景：
#   - claude-opus-4-8 模型探测可行，但实际请求返回 "No available provider"
#   - 原因：models_canonical 表中缺少模型记录
#
# 测试目标：
#   1. 验证 models_canonical 表中有 claude-opus-4-8 记录
#   2. 验证 model_aliases 表中有正确的 canonical_id
#   3. 验证 provider_models 表中有模型记录
#   4. 验证凭证配置正确
#   5. 验证 v_routable_credential_models 视图有可路由记录

set -euo pipefail

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${BLUE}[test]${NC} $*"; }
ok() { echo -e "${GREEN}[test]✓${NC} $*"; }
warn() { echo -e "${YELLOW}[test]⚠${NC} $*"; }
fail() { echo -e "${RED}[test]✗${NC} $*"; }

# 数据库连接
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-xutaohuang}"
DB_NAME="${DB_NAME:-llm_gateway}"

psql_cmd() {
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c "$1"
}

echo "=== claude-opus-4-8 路由配置测试 ==="
echo ""

# 1. 检查 models_canonical 表
log "1. 检查 models_canonical 表..."
CANONICAL_COUNT=$(psql_cmd "
    SELECT COUNT(*) 
    FROM models_canonical 
    WHERE canonical_name = 'claude-opus-4-8';
")

if [ "$CANONICAL_COUNT" -gt 0 ]; then
    ok "models_canonical 表中有 claude-opus-4-8 记录"
    psql_cmd "
        SELECT id, canonical_name, family, status 
        FROM models_canonical 
        WHERE canonical_name = 'claude-opus-4-8';
    "
else
    fail "models_canonical 表中缺少 claude-opus-4-8 记录"
    echo "  修复方法: 运行 sql/fix-claude-opus-4-8.sql"
fi

echo ""

# 2. 检查 model_aliases 表
log "2. 检查 model_aliases 表..."
ALIAS_INFO=$(psql_cmd "
    SELECT ma.id, ma.canonical_id, ma.raw_name, ma.status, mc.canonical_name
    FROM model_aliases ma
    LEFT JOIN models_canonical mc ON ma.canonical_id = mc.id
    WHERE ma.raw_name IN ('claude-opus-4.8', 'claude-opus-4-8');
")

if [ -n "$ALIAS_INFO" ]; then
    ok "model_aliases 表中有 claude-opus-4.8 记录"
    echo "$ALIAS_INFO"
    
    # 检查 canonical_id 是否正确
    CORRECT_CANONICAL=$(psql_cmd "
        SELECT COUNT(*) 
        FROM model_aliases ma
        JOIN models_canonical mc ON ma.canonical_id = mc.id
        WHERE ma.raw_name IN ('claude-opus-4.8', 'claude-opus-4-8')
          AND mc.canonical_name = 'claude-opus-4-8';
    ")
    
    if [ "$CORRECT_CANONICAL" -gt 0 ]; then
        ok "canonical_id 指向正确的模型"
    else
        fail "canonical_id 指向错误的模型"
        echo "  修复方法: 运行 sql/fix-claude-opus-4-8.sql"
    fi
else
    fail "model_aliases 表中缺少 claude-opus-4.8 记录"
fi

echo ""

# 3. 检查 provider_models 表
log "3. 检查 provider_models 表..."
PM_COUNT=$(psql_cmd "
    SELECT COUNT(*) 
    FROM provider_models 
    WHERE raw_model_name LIKE '%opus-4-8%';
")

if [ "$PM_COUNT" -gt 0 ]; then
    ok "provider_models 表中有 claude-opus-4-8 记录"
    psql_cmd "
        SELECT pm.id, pm.provider_id, pm.raw_model_name, pm.available, 
               p.code as provider_code
        FROM provider_models pm
        JOIN providers p ON pm.provider_id = p.id
        WHERE pm.raw_model_name LIKE '%opus-4-8%';
    "
else
    fail "provider_models 表中缺少 claude-opus-4-8 记录"
    echo "  修复方法: 运行 sql/fix-claude-opus-4-8.sql"
fi

echo ""

# 4. 检查凭证配置
log "4. 检查凭证配置..."
CRED_COUNT=$(psql_cmd "
    SELECT COUNT(*) 
    FROM credentials c
    JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
    JOIN provider_models pm ON cmb.provider_model_id = pm.id
    WHERE pm.raw_model_name LIKE '%opus-4-8%';
")

if [ "$CRED_COUNT" -gt 0 ]; then
    ok "有 $CRED_COUNT 个凭证支持 claude-opus-4-8"
else
    warn "没有凭证支持 claude-opus-4-8"
    echo "  需要添加 Anthropic API Key 凭证"
fi

echo ""

# 5. 检查 v_routable_credential_models 视图
log "5. 检查 v_routable_credential_models 视图..."
ROUTABLE_COUNT=$(psql_cmd "
    SELECT COUNT(*) 
    FROM v_routable_credential_models 
    WHERE raw_model_name LIKE '%opus-4-8%';
")

if [ "$ROUTABLE_COUNT" -gt 0 ]; then
    ok "v_routable_credential_models 视图中有 $ROUTABLE_COUNT 条可路由记录"
else
    warn "v_routable_credential_models 视图中没有 claude-opus-4-8 的可路由记录"
    echo "  原因: 可能没有凭证或凭证不可用"
fi

echo ""

# 6. 模拟路由测试（如果有 Gateway 运行）
log "6. 模拟路由测试..."
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8082}"

if curl -sS --max-time 2 "$GATEWAY_URL/healthz" >/dev/null 2>&1; then
    log "Gateway 运行中，发送测试请求..."
    
    RESPONSE=$(curl -s -X POST "$GATEWAY_URL/v1/chat/completions" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer test-key" \
        -d '{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}],"stream":false}' \
        2>&1)
    
    if echo "$RESPONSE" | grep -q "No available provider"; then
        fail "路由测试失败: No available provider"
        echo "  响应: $RESPONSE"
    elif echo "$RESPONSE" | grep -q "model_not_found"; then
        fail "路由测试失败: model_not_found"
        echo "  响应: $RESPONSE"
    elif echo "$RESPONSE" | grep -q "error"; then
        warn "路由测试有错误"
        echo "  响应: $RESPONSE"
    else
        ok "路由测试成功"
        echo "  响应: $RESPONSE"
    fi
else
    warn "Gateway 未运行，跳过路由测试"
fi

echo ""
echo "=== 测试完成 ==="
