#!/bin/bash
# tests/local/scenarios/01-baseline.sh
#
# 01 - 基线验证：所有 kill-switch = OFF（默认），业务应 200 OK 通过率 ≥ 99%。
#
# 期望：
#   - 5 个不同模型各 20 次请求
#   - 总通过率 ≥ 99%
#   - P99 < 800ms
#   - session_cache / session_compression / circuit_degradation 均启用

set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:58781}"
TEST_API_KEY="${TEST_API_KEY:-sk-loc-1234567890abcdef}"

echo "=== Scenario 01: baseline (all kill-switches OFF) ==="
echo "Gateway: $GATEWAY_URL"
echo ""

# 1. 健康检查
echo "[1/4] /healthz ..."
HEALTH=$(curl -sS -m 3 "$GATEWAY_URL/healthz" | head -c 200 || echo "DOWN")
echo "  health: $HEALTH"
echo ""

# 2. 5 个模型各 20 次
MODELS=("minimax-m3" "gpt-5.6-luna" "gpt-5.6-terra" "claude-sonnet-5" "gpt-4o")
echo "[2/4] Functional matrix: 5 models × 20 calls = 100 calls"
TOTAL=0
SUCC=0
declare -A PER_MODEL_TOTAL
declare -A PER_MODEL_SUCC

for model in "${MODELS[@]}"; do
  for i in $(seq 1 20); do
    STATUS=$(curl -sS -o /dev/null -m 10 -w "%{http_code}" \
      -H "Authorization: Bearer $TEST_API_KEY" \
      -H "Content-Type: application/json" \
      -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"hello $i\"}],\"max_tokens\":15}" \
      "$GATEWAY_URL/v1/chat/completions" || echo "000")
    PER_MODEL_TOTAL[$model]=$(( ${PER_MODEL_TOTAL[$model]:-0} + 1 ))
    TOTAL=$((TOTAL + 1))
    if [ "$STATUS" = "200" ]; then
      PER_MODEL_SUCC[$model]=$(( ${PER_MODEL_SUCC[$model]:-0} + 1 ))
      SUCC=$((SUCC + 1))
    fi
  done
done

echo "  per-model success rate:"
for model in "${MODELS[@]}"; do
  t=${PER_MODEL_TOTAL[$model]:-0}
  s=${PER_MODEL_SUCC[$model]:-0}
  printf "    %-25s %d/%d (%.1f%%)\n" "$model" "$s" "$t" "$(echo "scale=1; 100*$s/$t" | bc)"
done

RATE=$(echo "scale=1; 100*$SUCC/$TOTAL" | bc)
echo ""
echo "  overall: $SUCC / $TOTAL = $RATE%"

# 3. P99 latency (粗略，50 samples)
echo ""
echo "[3/4] P99 latency (50 samples on minimax-m3):"
TIMES=()
for i in $(seq 1 50); do
  T=$(curl -sS -o /dev/null -m 10 -w "%{time_total}" \
    -H "Authorization: Bearer $TEST_API_KEY" -H "Content-Type: application/json" \
    -d '{"model":"minimax-m3","messages":[{"role":"user","content":"x"}],"max_tokens":10}' \
    "$GATEWAY_URL/v1/chat/completions")
  TIMES+=("$T")
done

# P99 = 第 49 索引 (排序后)
echo "${TIMES[@]}" | tr ' ' '\n' | grep -v "^$" | sort -g > /tmp/_times.txt
P50=$(sed -n '25p' /tmp/_times.txt)
P99=$(sed -n '50p' /tmp/_times.txt)
MAX=$(tail -1 /tmp/_times.txt)
echo "  p50=${P50}s p99=${P99}s max=${MAX}s"

# 4. 断言
echo ""
echo "[4/4] Assertions:"
FAILS=0
if [ "$(echo "$RATE < 99" | bc)" = "1" ]; then
  echo "  ❌ success rate $RATE% < 99%"
  FAILS=$((FAILS + 1))
else
  echo "  ✅ success rate $RATE% ≥ 99%"
fi
if [ "$(echo "$P99 > 0.800" | bc)" = "1" ]; then
  echo "  ❌ p99=${P99}s > 0.800s"
  FAILS=$((FAILS + 1))
else
  echo "  ✅ p99=${P99}s ≤ 0.800s"
fi

echo ""
if [ "$FAILS" -eq 0 ]; then
  echo "🟢 Scenario 01 PASSED"
  exit 0
else
  echo "🔴 Scenario 01 FAILED ($FAILS assertion(s))"
  exit 1
fi
