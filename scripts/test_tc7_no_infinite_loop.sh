#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# Runtime Test TC7: 所有候选失败时不死循环
#
# 验证：当所有可用凭据都 quota 耗尽时，请求应在有限时间内返回错误，
#       而非陷入无限死循环。
#
# 前置条件：同 TC6
#
# 用法:
#   ./scripts/test_tc7_no_infinite_loop.sh
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# 配置
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
TEST_MODEL="${TEST_MODEL:-claude-3-5-sonnet-20241022}"
DB_CONTAINER="${DB_CONTAINER:-r112_postgres}"
MAX_DURATION=30  # 秒

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
ok()  { echo -e "${GREEN}✓ $*${NC}"; }
err() { echo -e "${RED}✗ $*${NC}" >&2; }
info(){ echo -e "${YELLOW}▶ $*${NC}"; }

# ── Step 1: 健康检查 ──
info "Step 1: 健康检查"
HEALTH=$(curl -sS -o /dev/null -w "%{http_code}" "$GATEWAY_URL/health" || echo "000")
if [ "$HEALTH" != "200" ]; then
    err "网关健康检查失败: HTTP $HEALTH"
    exit 1
fi
ok "网关健康"

# ── Step 2: 备份当前所有凭据状态 ──
info "Step 2: 备份当前凭据状态"
PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -c "
    CREATE TEMP TABLE backup_credentials AS
    SELECT id, quota_state, quota_recover_at FROM credentials;
" 2>&1 | tail -1
ok "已备份"

# ── Step 3: 标记所有凭据为 exhausted ──
info "Step 3: 标记所有凭据为 quota_exhausted"
PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -c "
    UPDATE credentials 
    SET quota_state = 'exhausted',
        quota_recover_at = NOW() + INTERVAL '24 hours'
    WHERE status = 'active'
" 2>&1 | tail -1
ok "所有活跃凭据已标记为 exhausted"

# ── Step 4: 发送请求并测量耗时 ──
info "Step 4: 发送请求（所有候选都失败），测量耗时"
START_TIME=$(date +%s)
HTTP_CODE=$(curl -sS -o /tmp/tc7_response.json -w "%{http_code}" \
    -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"$TEST_MODEL\",
        \"messages\": [{\"role\": \"user\", \"content\": \"TC7 no infinite loop test\"}]
    }" \
    --max-time 60 || echo "TIMEOUT")
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

echo "  HTTP 状态: $HTTP_CODE"
echo "  耗时: ${DURATION}s"
echo "  响应: $(head -c 300 /tmp/tc7_response.json)..."

# ── Step 5: 验证不死循环 ──
info "Step 5: 验证不死循环"
TEST_RESULT=0

if [ "$DURATION" -lt "$MAX_DURATION" ]; then
    ok "PASS: 耗时 ${DURATION}s < ${MAX_DURATION}s（无死循环）"
else
    err "FAIL: 耗时 ${DURATION}s >= ${MAX_DURATION}s（疑似死循环）"
    TEST_RESULT=1
fi

if [ "$HTTP_CODE" = "429" ] || [ "$HTTP_CODE" = "503" ] || [ "$HTTP_CODE" = "500" ]; then
    ok "PASS: 返回错误码 $HTTP_CODE（quota_exhausted 是预期行为）"
elif [ "$HTTP_CODE" = "000" ] || [ "$HTTP_CODE" = "TIMEOUT" ]; then
    err "FAIL: 连接超时（$HTTP_CODE）"
    TEST_RESULT=1
else
    err "WARN: 返回 $HTTP_CODE（预期 429/503/500）"
fi

# ── Step 6: 验证日志中确实发生了重试 ──
info "Step 6: 验证重试日志"
RETRIES=$(PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -tAc "
    SELECT COUNT(*) FROM candidate_failure_logs
    WHERE created_at > NOW() - INTERVAL '1 minute'
      AND error_kind IN ('quota', 'quota_balance', 'quota_permanent')
" 2>/dev/null || echo "0")
echo "  最近 1 分钟 quota 错误次数: $RETRIES"

if [ "$RETRIES" -gt 0 ]; then
    ok "PASS: 日志记录了 $RETRIES 次 quota 失败（证明确实发生了重试）"
else
    info "INFO: 未找到 quota 失败日志（可能路由在第一候选就检测到）"
fi

# ── Step 7: 恢复凭据状态 ──
info "Step 7: 恢复凭据状态"
PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -c "
    UPDATE credentials c
    SET quota_state = b.quota_state,
        quota_recover_at = b.quota_recover_at
    FROM backup_credentials b
    WHERE c.id = b.id;
" 2>&1 | tail -1
ok "凭据状态已恢复"

# ── 总结 ──
echo ""
if [ "$TEST_RESULT" = "0" ]; then
    ok "🎉 TC7 通过：所有候选失败时无死循环"
    exit 0
else
    err "💥 TC7 失败"
    exit 1
fi
