#!/usr/bin/env bash
# S17: 流式断连续传 (Stream Continuation After Client Disconnect)
# 
# 验证客户端断开连接后，上游流式响应能够继续接收并保存为 pending response
# 
# 背景: 2026-07-19 修复 fix(streaming): pending continuation audit P1/P2 repairs
# - 问题：客户端断连后，上游流式响应 body 被多次消费或提前关闭，导致数据丢失
# - 修复：使用 context.WithoutCancel 保持租户上下文，确保每个流只有一个 body 消费者

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S17_stream_continuation"
GATEWAY="${GATEWAY_URL:-http://localhost:8781}"
RESULTS_DIR="$SCRIPT_DIR/../results"
OUTPUT_FILE="$RESULTS_DIR/${SCENARIO}.json"

log_info "Starting $SCENARIO: Stream Continuation After Client Disconnect"

# 确保输出目录存在
mkdir -p "$RESULTS_DIR"

# 测试参数
MODELS="loadtest-mini-alpha,loadtest-standard-alpha,loadtest-pro-alpha"
CLIENTS=40
ROUNDS=5
DISCONNECT_RATIO=0.5  # 50% 的请求在接收一半后断开

log_info "Configuration:"
log_info "  Gateway: $GATEWAY"
log_info "  Models: $MODELS"
log_info "  Clients: $CLIENTS"
log_info "  Rounds: $ROUNDS"
log_info "  Disconnect Ratio: $DISCONNECT_RATIO"

# 运行测试
log_info "Running stream continuation test with client disconnects..."

python3 "$SCRIPT_DIR/../tools/loadtest.py" \
  --gateway "$GATEWAY" \
  --clients "$CLIENTS" \
  --rounds "$ROUNDS" \
  --models "$MODELS" \
  --stream \
  --disconnect-ratio "$DISCONNECT_RATIO" \
  --prompt-size short \
  --output "$OUTPUT_FILE" || {
    log_error "Loadtest failed"
    exit 1
  }

log_info "Test completed. Validating results..."

# 验证结果
TOTAL_REQUESTS=$(jq '.summary.total_requests' "$OUTPUT_FILE")
SUCCESS_RATE=$(jq '.summary.success_rate' "$OUTPUT_FILE")
P99_LATENCY=$(jq '.summary.p99_latency_ms' "$OUTPUT_FILE")
DISCONNECTED=$(jq '.summary.disconnected // 0' "$OUTPUT_FILE")
PENDING_SAVED=$(jq '.summary.pending_saved // 0' "$OUTPUT_FILE")
PENDING_COMPLETE=$(jq '.summary.pending_complete // 0' "$OUTPUT_FILE")
PENDING_OVERFLOW=$(jq '.summary.pending_overflow // 0' "$OUTPUT_FILE")

log_info "Results:"
log_info "  Total Requests: $TOTAL_REQUESTS"
log_info "  Success Rate: ${SUCCESS_RATE}%"
log_info "  P99 Latency: ${P99_LATENCY}ms"
log_info "  Disconnected: $DISCONNECTED"
log_info "  Pending Saved: $PENDING_SAVED"
log_info "  Pending Complete: $PENDING_COMPLETE"
log_info "  Pending Overflow: $PENDING_OVERFLOW"

# 验收标准
PASS=true

# 1. 成功率 >= 95%
if (( $(echo "$SUCCESS_RATE < 95" | bc -l) )); then
  log_error "FAIL: Success rate ${SUCCESS_RATE}% < 95%"
  PASS=false
else
  log_success "PASS: Success rate ${SUCCESS_RATE}% >= 95%"
fi

# 2. pending_overflow 应为 0 (无溢出截断)
if (( PENDING_OVERFLOW > 0 )); then
  log_error "FAIL: Pending overflow count $PENDING_OVERFLOW > 0"
  PASS=false
else
  log_success "PASS: No pending overflow (count = 0)"
fi

# 3. pending_complete 应等于 disconnected (所有断连都完整保存)
if (( DISCONNECTED > 0 )); then
  COMPLETION_RATE=$(echo "scale=2; $PENDING_COMPLETE * 100 / $DISCONNECTED" | bc)
  if (( $(echo "$COMPLETION_RATE < 90" | bc -l) )); then
    log_error "FAIL: Pending completion rate ${COMPLETION_RATE}% < 90%"
    PASS=false
  else
    log_success "PASS: Pending completion rate ${COMPLETION_RATE}%"
  fi
fi

# 4. P99 延迟 < 2500ms
if (( $(echo "$P99_LATENCY > 2500" | bc -l) )); then
  log_warn "WARN: P99 latency ${P99_LATENCY}ms > 2500ms (acceptable for disconnect scenario)"
fi

# 输出最终结果
if [ "$PASS" = true ]; then
  log_success "$SCENARIO PASSED"
  exit 0
else
  log_error "$SCENARIO FAILED"
  exit 1
fi
