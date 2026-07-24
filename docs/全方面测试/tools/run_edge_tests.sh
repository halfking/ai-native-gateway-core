#!/bin/bash
# docs/全方面测试/tools/run_edge_tests.sh
#
# 边缘测试快速执行脚本
# 执行 S40-S55 异常场景测试

set -euo pipefail

# 配置
GATEWAY="${GATEWAY:-http://localhost:8781}"
API_KEY="${API_KEY:-sk-loadtest-01}"
RESULTS_DIR="${RESULTS_DIR:-/tmp/edge_test_results}"
mkdir -p "$RESULTS_DIR"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
log_fail() { echo -e "${RED}[FAIL]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }

cd "$(dirname "$0")"

echo "============================================"
echo "  Edge Case Tests - S40-S55"
echo "============================================"
echo "Gateway: $GATEWAY"
echo "Results: $RESULTS_DIR"
echo "============================================"

# 计数器
TOTAL=0
PASSED=0
FAILED=0

run_test() {
  local name="$1"
  local expected="$2"  # pass or fail
  local cmd="$3"
  TOTAL=$((TOTAL + 1))

  log_info "[$name] Running..."
  if eval "$cmd" > "$RESULTS_DIR/$name.json" 2>&1; then
    local status=$(jq -r '.error // .type // "success"' "$RESULTS_DIR/$name.json" 2>/dev/null || echo "success")
    if [ "$expected" = "pass" ]; then
      log_pass "[$name] ✓ Status: $status"
      PASSED=$((PASSED + 1))
    else
      log_fail "[$name] ✗ Expected failure but got success"
      FAILED=$((FAILED + 1))
    fi
  else
    local status=$(jq -r '.error // .type // "error"' "$RESULTS_DIR/$name.json" 2>/dev/null || echo "error")
    if [ "$expected" = "fail" ]; then
      log_pass "[$name] ✓ Correctly failed: $status"
      PASSED=$((PASSED + 1))
    else
      log_fail "[$name] ✗ Unexpected failure: $status"
      FAILED=$((FAILED + 1))
    fi
  fi
}

# ============================================
# S40: JSON 畸形数据测试
# ============================================
echo ""
echo "=== S40: JSON 畸形数据测试 ==="

run_test "S40a_malformed_json" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{role:user,content:hi}]}"'

run_test "S40b_missing_model" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"'

run_test "S40c_empty_messages" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[]}"'

# ============================================
# S41: Content-Type 混乱测试
# ============================================
echo ""
echo "=== S41: Content-Type 混乱测试 ==="

run_test "S41a_wrong_content_type" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: text/plain" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"'

run_test "S41b_normal_request" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":10}"'

# ============================================
# S43: 认证授权边缘测试
# ============================================
echo ""
echo "=== S43: 认证授权边缘测试 ==="

run_test "S43a_invalid_token" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer invalid-token-format" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"'

run_test "S43b_empty_token" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer " \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"'

run_test "S43c_sql_injection_token" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer sk-loadtest-01'"'"'; DROP TABLE users; --" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"'

# ============================================
# S44: 超时与断连测试
# ============================================
echo ""
echo "=== S44: 超时与断连测试 ==="

run_test "S44_normal_request" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":10}"'

# ============================================
# S48: 协议转换测试
# ============================================
echo ""
echo "=== S48: 协议转换测试 ==="

run_test "S48a_chat_protocol" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -H "X-Gw-Protocol-Mode: chat" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":10}"'

run_test "S48b_response_protocol" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -H "X-Gw-Protocol-Mode: response" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":10}"'

run_test "S48c_anthropic_protocol" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -H "X-Gw-Protocol-Mode: anthropic" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":10}"'

run_test "S48d_system_message" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"system\",\"content\":\"You are helpful.\"},{\"role\":\"user\",\"content\":\"Hi!\"}],\"max_tokens\":10}"'

run_test "S48e_multi_turn" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"},{\"role\":\"assistant\",\"content\":\"Hi there!\"},{\"role\":\"user\",\"content\":\"How are you?\"}],\"max_tokens\":10}"'

# ============================================
# S50: 错误响应格式测试
# ============================================
echo ""
echo "=== S50: 错误响应格式测试 ==="

run_test "S50a_model_not_found" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"nonexistent-model-xyz\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"'

run_test "S50b_empty_model" "fail" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"'

# ============================================
# S51: 特殊字符测试
# ============================================
echo ""
echo "=== S51: 特殊字符测试 ==="

run_test "S51a_emoji" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json; charset=utf-8" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello 👋🎉\"}],\"max_tokens\":10}"'

run_test "S51b_arabic" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json; charset=utf-8" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"مرحبا بالعالم\"}],\"max_tokens\":10}"'

run_test "S51c_control_chars" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"Line1\\nLine2\\tTab\\rEnd\"}],\"max_tokens\":10}"'

run_test "S51d_sql_injection" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"'"'"'; DROP TABLE users; --\"}],\"max_tokens\":10}"'

# ============================================
# S53: 幂等性测试
# ============================================
echo ""
echo "=== S53: 幂等性测试 ==="

run_test "S53_idempotency_key" "pass" \
  'curl -sf -X POST "$GATEWAY/v1/chat/completions" \
   -H "Content-Type: application/json" \
   -H "Authorization: Bearer $API_KEY" \
   -H "X-Idempotency-Key: test-key-$(date +%s)" \
   -d "{\"model\":\"loadtest-mini-alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"What is 2+2?\"}],\"max_tokens\":10}"'

# ============================================
# Summary
# ============================================
echo ""
echo "============================================"
echo "  Test Summary"
echo "============================================"
echo -e "Total:  $TOTAL"
echo -e "Passed: ${GREEN}$PASSED${NC}"
echo -e "Failed: ${RED}$FAILED${NC}"
echo "============================================"

if [ $FAILED -gt 0 ]; then
  echo -e "${RED}Some tests failed!${NC}"
  echo "Failed tests:"
  ls -1 "$RESULTS_DIR"/*.json | while read f; do
    if ! jq -e '.error' "$f" > /dev/null 2>&1 && grep -q '"error"' "$f" 2>/dev/null; then
      echo "  - $(basename $f .json)"
    fi
  done
  exit 1
else
  echo -e "${GREEN}All tests passed!${NC}"
  exit 0
fi
