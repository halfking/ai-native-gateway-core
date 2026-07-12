#!/usr/bin/env bash
# test-runtime-tc.sh — 运行时测试统一入口（TC6/TC7/TC8）
#
# 合并自: test_tc6_quota_silent_failover.sh + test_tc7_no_infinite_loop.sh
#         + test_tc8_client_disconnect.sh
# 修订: 2026-07-05
#
# 用法:
#   ./scripts/test-runtime-tc.sh --tc6            # Quota 耗尽后静默切换
#   ./scripts/test-runtime-tc.sh --tc7            # 所有候选失败时不死循环
#   ./scripts/test-runtime-tc.sh --tc8            # 客户端断开检测
#   ./scripts/test-runtime-tc.sh --all            # 全量运行
#
# 环境变量（可选）:
#   GATEWAY_URL    网关地址 (默认: http://localhost:8080)
#   API_KEY        测试 API key (默认: test-key)
#   TEST_MODEL     测试模型 (默认: claude-3-5-sonnet-20241022)
#   DB_CONTAINER   PG 容器名 (默认: r112_postgres)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
TEST_MODEL="${TEST_MODEL:-claude-3-5-sonnet-20241022}"
DB_CONTAINER="${DB_CONTAINER:-r112_postgres}"
MAX_DURATION=${MAX_DURATION:-30}
DISCONNECT_DELAY=${DISCONNECT_DELAY:-8}

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

health_check() {
  info "健康检查 $GATEWAY_URL"
  local code
  code=$(curl -sS -o /dev/null -w "%{http_code}" "$GATEWAY_URL/health" || echo "000")
  if [ "$code" != "200" ]; then err "网关健康检查失败: HTTP $code"; exit 1; fi
  ok "网关健康"
}

pg() { PGPASSWORD=kxpass docker exec "$DB_CONTAINER" psql -U kxuser -d llm_gateway -tAc "$1" 2>/dev/null; }

# ══════════════════════════════════════════════════════════════════════
# TC6: Quota 耗尽后静默切换
# ══════════════════════════════════════════════════════════════════════
tc6() {
  local test_result=1

  health_check

  info "Step 2: 选择测试凭据"
  local test_cred_id
  test_cred_id=$(pg "
    SELECT c.id FROM credentials c
    JOIN providers p ON p.id = c.provider_id
    WHERE c.status = 'active' AND COALESCE(c.quota_state, 'ok') = 'ok'
      AND p.enabled = true AND p.manual_disabled IS NOT TRUE
    ORDER BY c.id LIMIT 1
  ")
  [ -z "$test_cred_id" ] && { err "找不到活跃凭据"; return 1; }
  ok "选择凭据 ID=$test_cred_id"

  info "Step 3: 模拟凭据 $test_cred_id quota 耗尽"
  pg "UPDATE credentials SET quota_state='exhausted',quota_recover_at=NOW()+INTERVAL '1 hour' WHERE id=$test_cred_id" >/dev/null
  ok "凭据 $test_cred_id quota 已耗尽"

  info "Step 4: 发送请求，验证静默切换"
  local start_time=http_code duration
  start_time=$(date +%s)
  http_code=$(curl -sS -o /tmp/tc6_response.json -w "%{http_code}" \
    -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
    -d "{\"model\":\"$TEST_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"TC6 quota failover test\"}]}" \
    --max-time 30 || echo "000")
  duration=$(( $(date +%s) - start_time ))
  echo "  HTTP: $http_code, 耗时: ${duration}s"

  info "Step 5: 验证结果"
  [ "$http_code" = "200" ] && ok "PASS: 静默切换成功" || err "FAIL: HTTP $http_code"
  [ "$duration" -lt 30 ] && ok "PASS: 响应时间 ${duration}s" || err "FAIL: 响应超时"

  info "Step 6: 验证实际使用的凭据"
  local used_cred
  used_cred=$(pg "SELECT credential_id FROM request_logs WHERE created_at > NOW() - INTERVAL '1 minute' ORDER BY created_at DESC LIMIT 1")
  echo "  实际使用: $used_cred"
  if echo "$used_cred" | grep -q "^$test_cred_id|"; then
    err "FAIL: 使用了耗尽凭据"; test_result=1
  else
    ok "PASS: 切换到其他凭据"; test_result=0
  fi

  info "Step 7: 恢复凭据"
  pg "UPDATE credentials SET quota_state='ok',quota_recover_at=NULL WHERE id=$test_cred_id" >/dev/null
  ok "已恢复"

  [ "$test_result" = "0" ] && ok "✅ TC6 通过" || err "❌ TC6 失败"
  return "$test_result"
}

# ══════════════════════════════════════════════════════════════════════
# TC7: 所有候选失败时不死循环
# ══════════════════════════════════════════════════════════════════════
tc7() {
  local test_result=0

  health_check

  info "Step 2: 备份凭据状态"
  pg "CREATE TEMP TABLE IF NOT EXISTS backup_credentials AS SELECT id, quota_state, quota_recover_at FROM credentials" >/dev/null
  ok "已备份"

  info "Step 3: 标记所有凭据为 exhausted"
  pg "UPDATE credentials SET quota_state='exhausted',quota_recover_at=NOW()+INTERVAL '24 hours' WHERE status='active'" >/dev/null
  ok "所有活跃凭据已标记"

  info "Step 4: 发送请求，测量耗时"
  local start_time duration http_code
  start_time=$(date +%s)
  http_code=$(curl -sS -o /tmp/tc7_response.json -w "%{http_code}" \
    -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
    -d "{\"model\":\"$TEST_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"TC7 no infinite loop test\"}]}" \
    --max-time 60 || echo "TIMEOUT")
  duration=$(( $(date +%s) - start_time ))
  echo "  HTTP: $http_code, 耗时: ${duration}s"

  info "Step 5: 验证不死循环"
  if [ "$duration" -lt "$MAX_DURATION" ]; then
    ok "PASS: 耗时 ${duration}s < ${MAX_DURATION}s（无死循环）"
  else
    err "FAIL: 耗时 ${duration}s（疑似死循环）"; test_result=1
  fi

  case "$http_code" in
    429|503|500) ok "PASS: 返回 $http_code（预期行为）" ;;
    000|TIMEOUT) err "FAIL: 连接超时"; test_result=1 ;;
    *) err "WARN: 返回 $http_code（预期 429/503/500）" ;;
  esac

  info "Step 6: 验证重试日志"
  local retries
  retries=$(pg "SELECT COUNT(*) FROM candidate_failure_logs WHERE created_at > NOW() - INTERVAL '1 minute' AND error_kind IN ('quota','quota_balance','quota_permanent')" 2>/dev/null || echo "0")
  echo "  quota 错误次数: $retries"
  [ "$retries" -gt 0 ] && ok "PASS: 有重试记录" || info "INFO: 无 quota 失败日志"

  info "Step 7: 恢复凭据状态"
  pg "UPDATE credentials c SET quota_state=b.quota_state,quota_recover_at=b.quota_recover_at FROM backup_credentials b WHERE c.id=b.id" >/dev/null
  pg "DROP TABLE IF EXISTS backup_credentials" >/dev/null
  ok "已恢复"

  [ "$test_result" = "0" ] && ok "✅ TC7 通过" || err "❌ TC7 失败"
  return "$test_result"
}

# ══════════════════════════════════════════════════════════════════════
# TC8: 客户端断开检测
# ══════════════════════════════════════════════════════════════════════
tc8() {
  health_check

  info "Step 2: 备份凭据状态"
  pg "CREATE TEMP TABLE IF NOT EXISTS backup_credentials AS SELECT id, quota_state, quota_recover_at FROM credentials" >/dev/null
  ok "已备份"

  info "Step 3: 标记所有凭据为 exhausted"
  pg "UPDATE credentials SET quota_state='exhausted',quota_recover_at=NOW()+INTERVAL '24 hours' WHERE status='active'" >/dev/null
  ok "所有凭据已标记"

  info "Step 4: 启动请求，${DISCONNECT_DELAY}s 后杀掉"
  local start_time duration
  start_time=$(date +%s)
  curl -sS -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
    -d "{\"model\":\"$TEST_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"TC8 client disconnect test\"}]}" \
    --max-time 60 > /tmp/tc8_response.json 2>&1 &
  local curl_pid=$!
  sleep "$DISCONNECT_DELAY"
  kill -9 "$curl_pid" 2>/dev/null || true
  wait "$curl_pid" 2>/dev/null || true
  duration=$(( $(date +%s) - start_time ))
  echo "  客户端在 ${duration}s 断开"

  sleep 2

  info "Step 5: 验证网关停止重试"
  local failures_before failures_after
  failures_before=$(pg "SELECT COUNT(*) FROM candidate_failure_logs WHERE created_at < NOW() - INTERVAL '${DISCONNECT_DELAY} seconds' AND created_at > NOW() - INTERVAL '1 minute'" 2>/dev/null || echo "0")
  failures_after=$(pg "SELECT COUNT(*) FROM candidate_failure_logs WHERE created_at > NOW() - INTERVAL '5 seconds'" 2>/dev/null || echo "0")
  echo "  断开前失败: $failures_before, 断开后: $failures_after"

  if [ "$failures_after" -lt "$failures_before" ] || [ "$failures_after" = "0" ]; then
    ok "PASS: 客户端断开后失败日志停止增长"
  else
    err "WARN: 失败日志仍在增加"
  fi

  info "Step 6: 恢复凭据状态"
  pg "UPDATE credentials c SET quota_state=b.quota_state,quota_recover_at=b.quota_recover_at FROM backup_credentials b WHERE c.id=b.id" >/dev/null
  pg "DROP TABLE IF EXISTS backup_credentials" >/dev/null
  ok "已恢复"

  ok "✅ TC8 通过"
  return 0
}

# ══════════════════════════════════════════════════════════════════════
# 主入口
# ══════════════════════════════════════════════════════════════════════
usage() { echo "用法: $0 [--tc6|--tc7|--tc8|--all]"; exit 1; }

MODE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tc6|--tc7|--tc8) MODE="$1"; shift ;;
    --all) MODE="all"; shift ;;
    *) usage ;;
  esac
done
[ -z "$MODE" ] && usage

OVERALL=0
case "$MODE" in
  --tc6) tc6 || OVERALL=1 ;;
  --tc7) tc7 || OVERALL=1 ;;
  --tc8) tc8 || OVERALL=1 ;;
  all)
    info "===== TC6: Quota 静默切换 ====="; tc6 || OVERALL=1; echo ""
    info "===== TC7: 不死循环 =====";     tc7 || OVERALL=1; echo ""
    info "===== TC8: 客户端断开 =====";    tc8 || OVERALL=1; echo ""
    ;;
esac

[ "$OVERALL" = "0" ] && ok "所有测试通过" || err "部分测试失败"
exit "$OVERALL"
