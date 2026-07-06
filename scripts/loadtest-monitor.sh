#!/usr/bin/env bash
# Real-time ASCII Monitor - 实时监控 4 mock 的请求/错误率
set -euo pipefail

INTERVAL=${1:-2}  # 刷新间隔秒数

MOCK_PORTS=(19080 19081 19082 19083)
MOCK_NAMES=(mock-A mock-B mock-C mock-D)

# 清屏并移动光标到顶部
clear_screen() {
  printf "\033[2J\033[H"
}

# 绘制进度条
draw_bar() {
  local value=$1
  local max=$2
  local width=${3:-50}
  
  local filled=$(( value * width / max ))
  [[ $filled -gt $width ]] && filled=$width
  [[ $filled -lt 0 ]] && filled=0
  
  printf "["
  for ((i=0; i<filled; i++)); do printf "█"; done
  for ((i=filled; i<width; i++)); do printf "░"; done
  printf "]"
}

# 获取 mock metrics
get_metrics() {
  local port=$1
  curl -sS --max-time 1 "http://localhost:$port/admin/metrics" 2>/dev/null || echo '{}'
}

# 主循环
declare -A PREV_TOTAL PREV_SUCCESS PREV_ERROR

while true; do
  clear_screen
  
  echo "═══════════════════════════════════════════════════════════════"
  echo "  Mock LLM Real-time Monitor  (Refresh: ${INTERVAL}s)"
  echo "  Press Ctrl+C to exit"
  echo "═══════════════════════════════════════════════════════════════"
  echo ""
  
  MAX_RPS=0
  
  # 收集当前数据
  for idx in "${!MOCK_PORTS[@]}"; do
    PORT=${MOCK_PORTS[$idx]}
    NAME=${MOCK_NAMES[$idx]}
    
    METRICS=$(get_metrics $PORT)
    
    if [[ -z "$METRICS" ]] || [[ "$METRICS" == "{}" ]]; then
      echo "$NAME  [OFFLINE]"
      continue
    fi
    
    MODE=$(echo "$METRICS" | jq -r '.mode // "unknown"')
    TOTAL=$(echo "$METRICS" | jq -r '.counters.requests_total // 0')
    SUCCESS=$(echo "$METRICS" | jq -r '.counters.requests_success // 0')
    ERROR=$(echo "$METRICS" | jq -r '.counters.requests_error // 0')
    
    # 计算 RPS (自上次检查)
    PREV_T=${PREV_TOTAL[$PORT]:-0}
    PREV_S=${PREV_SUCCESS[$PORT]:-0}
    PREV_E=${PREV_ERROR[$PORT]:-0}
    
    RPS=$(( (TOTAL - PREV_T) / INTERVAL ))
    RPS_SUCCESS=$(( (SUCCESS - PREV_S) / INTERVAL ))
    RPS_ERROR=$(( (ERROR - PREV_E) / INTERVAL ))
    
    [[ $RPS -gt $MAX_RPS ]] && MAX_RPS=$RPS
    
    # 计算错误率
    if [[ $TOTAL -gt 0 ]]; then
      ERROR_RATE=$(awk "BEGIN {printf \"%.1f\", ($ERROR * 100.0 / $TOTAL)}")
    else
      ERROR_RATE="0.0"
    fi
    
    # 显示
    printf "%-8s " "$NAME"
    draw_bar $RPS ${MAX_RPS:-100} 30
    printf " %3d req/s  (✓ %d  ✗ %d)  err: %s%%  mode: %s\n" \
      $RPS $RPS_SUCCESS $RPS_ERROR "$ERROR_RATE" "$MODE"
    
    # 保存当前值
    PREV_TOTAL[$PORT]=$TOTAL
    PREV_SUCCESS[$PORT]=$SUCCESS
    PREV_ERROR[$PORT]=$ERROR
  done
  
  echo ""
  echo "─────────────────────────────────────────────────────────────"
  echo "Total RPS: $MAX_RPS  |  Time: $(date '+%H:%M:%S')"
  echo ""
  
  sleep $INTERVAL
done
