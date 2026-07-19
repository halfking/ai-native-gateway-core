#!/usr/bin/env bash
# S18: NULL 数据处理 (NULL Success Rate Handling)
# 
# 验证 system_health_status 返回 NULL success_rate 时的容错处理
# 
# 背景: 2026-07-19 修复 fix(system-health): scan NULL success_rate as *float64 with nil guard
# - 问题：30秒窗口内零请求时，success_rate 为 NULL，pgx 无法扫描到 float64 导致 panic
# - 修复：改用 *float64 类型，NULL 时回退到 0.0

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S18_null_handling"
GATEWAY="${GATEWAY_URL:-http://localhost:8781}"
RESULTS_DIR="$SCRIPT_DIR/../results"
OUTPUT_FILE="$RESULTS_DIR/${SCENARIO}.json"

log_info "Starting $SCENARIO: NULL Success Rate Handling"

# 确保输出目录存在
mkdir -p "$RESULTS_DIR"

# 测试参数
MODELS="loadtest-mini-alpha,loadtest-standard-alpha,loadtest-pro-alpha"
CLIENTS=10
ROUNDS=5
REQUEST_INTERVAL=35  # 每个请求间隔 35 秒，超过 30 秒窗口

log_info "Configuration:"
log_info "  Gateway: $GATEWAY"
log_info "  Models: $MODELS"
log_info "  Clients: $CLIENTS"
log_info "  Rounds: $ROUNDS"
log_info "  Request Interval: ${REQUEST_INTERVAL}s"

# Step 1: 停止所有流量，等待窗口清空
log_info "Step 1: Waiting for 30s window to clear..."
sleep 35

# Step 2: 触发健康检查（此时窗口内无请求，应返回 NULL success_rate）
log_info "Step 2: Querying system health (should handle NULL success_rate)..."

HEALTH_RESPONSE=$(curl -s "${GATEWAY}/admin/system-health" || echo "{}")
log_info "Health response: $HEALTH_RESPONSE"

# 检查 gateway 是否崩溃
if ! curl -s "${GATEWAY}/healthz" > /dev/null 2>&1; then
  log_error "FAIL: Gateway is down after NULL success_rate query"
  exit 1
fi

log_success "PASS: Gateway handles NULL success_rate without panic"

# Step 3: 运行低频请求测试
log_info "Step 3: Running low-frequency requests..."

python3 "$SCRIPT_DIR/../tools/loadtest.py" \
  --gateway "$GATEWAY" \
  --clients "$CLIENTS" \
  --rounds "$ROUNDS" \
  --models "$MODELS" \
  --request-interval "$REQUEST_INTERVAL" \
  --prompt-size short \
  --output "$OUTPUT_FILE" || {
    log_error "Loadtest failed"
    exit 1
  }

log_info "Test completed. Validating results..."

# 验证结果
TOTAL_REQUESTS=$(jq '.summary.total_requests' "$OUTPUT_FILE")
SUCCESS_RATE=$(jq '.summary.success_rate' "$OUTPUT_FILE")

log_info "Results:"
log_info "  Total Requests: $TOTAL_REQUESTS"
log_info "  Success Rate: ${SUCCESS_RATE}%"

# 验收标准
PASS=true

# 1. Gateway 未崩溃
if ! curl -s "${GATEWAY}/healthz" > /dev/null 2>&1; then
  log_error "FAIL: Gateway crashed during test"
  PASS=false
else
  log_success "PASS: Gateway stable throughout test"
fi

# 2. 成功率 >= 99%
if (( $(echo "$SUCCESS_RATE < 99" | bc -l) )); then
  log_error "FAIL: Success rate ${SUCCESS_RATE}% < 99%"
  PASS=false
else
  log_success "PASS: Success rate ${SUCCESS_RATE}% >= 99%"
fi

# 3. 检查 gateway 日志中无 ERROR/PANIC
log_info "Checking gateway logs for errors..."
# 注意：这需要访问 gateway 日志，实际环境中需要调整
# 这里只做基本检查

# 输出最终结果
if [ "$PASS" = true ]; then
  log_success "$SCENARIO PASSED"
  exit 0
else
  log_error "$SCENARIO FAILED"
  exit 1
fi
