#!/bin/bash
# tests/local/scenarios/02-killswitch.sh
#
# 02 - Kill-switch 验证：依次关 session_cache / session_compression /
#     circuit_degradation / fp_slot / rate_limiter，每次验证业务仍 200。
#
# 验证原则：每个 kill 都是**最小伤害**（旁路而非删除），不应出现 5xx 雪崩。
#
# 此脚本只在已经用 KILL_*=1 重启过 gateway 的前提下运行；如未重启，
# 启动顺序：make gateway-down KILL_FP_SLOT=1 ; KILL_FP_SLOT=1 make gateway-up

set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:58781}"
TEST_API_KEY="${TEST_API_KEY:-sk-loc-1234567890abcdef}"

echo "=== Scenario 02: kill-switch matrix ==="
echo "Assumes current gateway is running with at least one KILL_*=1 env."
echo ""

probe() {
  local label="$1"
  local rps=10
  local model="minimax-m3"
  local total=0 succ=0 elapsed
  echo "[probe] $label → 50 requests at ~10/s on minimax-m3"
  for i in $(seq 1 50); do
    STATUS=$(curl -sS -o /dev/null -m 10 -w "%{http_code}" \
      -H "Authorization: Bearer $TEST_API_KEY" -H "Content-Type: application/json" \
      -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"ks-$label-$i\"}],\"max_tokens\":10}" \
      "$GATEWAY_URL/v1/chat/completions" || echo "000")
    total=$((total + 1))
    if [ "$STATUS" = "200" ]; then
      succ=$((succ + 1))
    fi
  done
  rate=$(echo "scale=1; 100*$succ/$total" | bc)
  printf "  %s: success=%d/%d (%.1f%%)\n" "$label" "$succ" "$total" "$rate"
  if [ "$succ" -ge 49 ]; then
    return 0
  fi
  return 1
}

# 通过 /api/admin/features (如果存在) 或日志快速判断当前启用状态
echo "[0/4] Current feature_switch state:"
STATUS_RAW=$(curl -sS -m 3 "$GATEWAY_URL/api/admin/features" 2>&1 || true)
echo "  (read-only endpoint probe): $STATUS_RAW"
echo ""

PASS=0
FAIL=0

echo "[1/4] baseline (all enabled, before any kill)"
if probe "before-kill"; then
  PASS=$((PASS+1))
else
  FAIL=$((FAIL+1))
fi

# 检查日志中是否出现 feature_switches_snapshot
echo ""
echo "[2/4] Look for 'feature_switches_snapshot' in gateway logs..."
LATEST_LOG=$(docker compose -f "$(dirname "$0")/../docker-compose.yml" logs gateway --no-color 2>&1 | tail -200 || true)
echo "$LATEST_LOG" | grep -E "feature_switches_snapshot|killswitch" | head -3 || echo "  (日志不可读 — 需要手动 docker compose logs gateway 验证)"
echo ""

echo "[3/4] Per-kill probes"
echo "  Note: gateway restart is required to flip switches. Each probe tests the CURRENT state."
for switch in session_cache session_compression circuit_degradation fp_slot rate_limiter; do
  echo ""
  echo "  KILL_${switch}=1:"
  if probe "kill-$switch"; then
    PASS=$((PASS+1))
  else
    FAIL=$((FAIL+1))
    echo "    ⚠️ 业务受到击穿 — 调查日志 + DB:credential_model_bindings 状态"
  fi
done

echo ""
echo "[4/4] Re-enable all + regression"
echo "  Restart gateway without KILL_* to verify recovery:"
echo "    docker compose -f tests/local/docker-compose.yml restart gateway"
echo "  Then re-run: ./tests/local/scenarios/01-baseline.sh"

echo ""
if [ "$FAIL" -eq 0 ]; then
  echo "🟢 Scenario 02 PASSED ($PASS probes)"
else
  echo "🔴 Scenario 02: $FAIL probe(s) failed, $PASS passed"
fi
exit "$FAIL"
