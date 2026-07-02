#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# Runtime Test TC6: Quota 耗尽后静默切换
#
# 验证：当一个凭据 quota 耗尽后，网关应该自动切换到其他凭据，
#       客户端不感知失败（200 OK 返回）。
#
# 前置条件：
#   - 网关已部署（含 mig 327/328）
#   - 至少 2 个同 provider 的活跃凭据
#   - 网关健康检查通过
#
# 用法:
#   ./scripts/test_tc6_quota_silent_failover.sh
#
# 环境变量（可选）:
#   GATEWAY_URL    网关地址 (默认: http://localhost:8080)
#   API_KEY        测试 API key (默认: test-key)
#   TEST_MODEL     测试模型 (默认: claude-3-5-sonnet-20241022)
#   DB_CONTAINER   PG 容器名 (默认: r112_postgres)
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# 配置
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
TEST_MODEL="${TEST_MODEL:-claude-3-5-sonnet-20241022}"
DB_CONTAINER="${DB_CONTAINER:-r112_postgres}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
ok()  { echo -e "${GREEN}✓ $*${NC}"; }
err() { echo -e "${RED}✗ $*${NC}" >&2; }
info(){ echo -e "${YELLOW}▶ $*${NC}"; }

# ── Step 1: 健康检查 ──
info "Step 1: 健康检查 $GATEWAY_URL"
HEALTH=$(curl -sS -o /dev/null -w "%{http_code}" "$GATEWAY_URL/health" || echo "000")
if [ "$HEALTH" != "200" ]; then
    err "网关健康检查失败: HTTP $HEALTH"
    exit 1
fi
ok "网关健康"

# ── Step 2: 选择一个活跃凭据进行测试 ──
info "Step 2: 选择测试凭据"
TEST_CRED_ID=$(PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -tAc "
    SELECT c.id FROM credentials c
    JOIN providers p ON p.id = c.provider_id
    WHERE c.status = 'active' 
      AND COALESCE(c.quota_state, 'ok') = 'ok'
      AND p.enabled = true
      AND p.manual_disabled IS NOT TRUE
    ORDER BY c.id
    LIMIT 1
" 2>/dev/null)

if [ -z "$TEST_CRED_ID" ]; then
    err "找不到活跃凭据"
    exit 1
fi
ok "选择凭据 ID=$TEST_CRED_ID"

# ── Step 3: 模拟 quota 耗尽 ──
info "Step 3: 模拟凭据 $TEST_CRED_ID quota 耗尽"
PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -c "
    UPDATE credentials 
    SET quota_state = 'exhausted', 
        quota_recover_at = NOW() + INTERVAL '1 hour'
    WHERE id = $TEST_CRED_ID
" 2>&1 | tail -1
ok "凭据 $TEST_CRED_ID quota 已耗尽"

# ── Step 4: 发送请求并验证静默切换 ──
info "Step 4: 发送请求，验证静默切换"
START_TIME=$(date +%s)
HTTP_CODE=$(curl -sS -o /tmp/tc6_response.json -w "%{http_code}" \
    -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"$TEST_MODEL\",
        \"messages\": [{\"role\": \"user\", \"content\": \"TC6 quota failover test\"}]
    }" \
    --max-time 30 || echo "000")
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

echo "  HTTP 状态: $HTTP_CODE"
echo "  耗时: ${DURATION}s"
echo "  响应: $(head -c 200 /tmp/tc6_response.json)..."

# ── Step 5: 验证结果 ──
info "Step 5: 验证结果"
if [ "$HTTP_CODE" = "200" ]; then
    ok "PASS: 客户端不感知 quota 失败（静默切换成功）"
else
    err "FAIL: HTTP $HTTP_CODE（预期 200）"
fi

if [ "$DURATION" -lt 30 ]; then
    ok "PASS: 响应时间 ${DURATION}s < 30s（无死循环）"
else
    err "FAIL: 响应时间 ${DURATION}s（疑似死循环）"
fi

# ── Step 6: 查询实际使用的凭据 ──
info "Step 6: 查询实际使用的凭据"
USED_CRED=$(PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -tAc "
    SELECT credential_id, status, latency_ms 
    FROM request_logs 
    WHERE created_at > NOW() - INTERVAL '1 minute'
    ORDER BY created_at DESC 
    LIMIT 1
" 2>/dev/null)
echo "  实际使用: $USED_CRED"

if echo "$USED_CRED" | grep -q "^$TEST_CRED_ID|"; then
    err "FAIL: 实际使用了耗尽凭据 $TEST_CRED_ID（预期切换到其他）"
    TEST_RESULT=1
else
    ok "PASS: 网关已切换到其他凭据"
    TEST_RESULT=0
fi

# ── Step 7: 恢复凭据状态 ──
info "Step 7: 恢复凭据状态"
PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -c "
    UPDATE credentials 
    SET quota_state = 'ok', 
        quota_recover_at = NULL
    WHERE id = $TEST_CRED_ID
" 2>&1 | tail -1
ok "凭据 $TEST_CRED_ID 已恢复"

# ── 总结 ──
echo ""
if [ "$TEST_RESULT" = "0" ]; then
    ok "🎉 TC6 通过：Quota 耗尽后静默切换功能正常"
    exit 0
else
    err "💥 TC6 失败：需要排查"
    exit 1
fi
