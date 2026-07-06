#!/usr/bin/env bash
# Mock State Orchestrator - 控制 mock 状态的统一接口
set -euo pipefail

MOCK_ENDPOINTS="${MOCK_ENDPOINTS:-http://localhost:19080,http://localhost:19081,http://localhost:19082,http://localhost:19083}"

usage() {
  cat <<EOF
用法:
  $0 set <mock_url> <mode> [ttl_seconds]     # 设置单个 mock 状态
  $0 reset <mock_url>                         # 重置单个 mock
  $0 get <mock_url>                           # 查看单个 mock 状态
  $0 metrics <mock_url>                       # 查看单个 mock 计数器
  $0 set-all <mode> [ttl_seconds]            # 设置所有 mock 状态
  $0 reset-all                                # 重置所有 mock
  $0 health-all                               # 查看所有 mock 健康

模式: healthy slow rate_limited quota_exceeded auth_error server_error timeout connection_refused broken_stream flaky

示例:
  $0 set http://localhost:19080 slow 30
  $0 set-all healthy
  $0 reset-all
EOF
  exit 1
}

set_state() {
  local url=$1
  local mode=$2
  local ttl=${3:-0}
  
  curl -sS -X POST "$url/admin/state" \
    -H 'Content-Type: application/json' \
    -d "{\"mode\":\"$mode\",\"ttl_seconds\":$ttl}" | jq -c '{status, mode: .new_state.mode}'
}

reset_mock() {
  local url=$1
  curl -sS -X POST "$url/admin/reset" | jq -c '{status, message}'
}

get_state() {
  local url=$1
  curl -sS "$url/admin/state" | jq -c '{mode, since, counters}'
}

get_metrics() {
  local url=$1
  curl -sS "$url/admin/metrics" | jq -c '{token, mode, counters}'
}

set_all() {
  local mode=$1
  local ttl=${2:-0}
  IFS=',' read -ra URLS <<< "$MOCK_ENDPOINTS"
  for url in "${URLS[@]}"; do
    echo -n "$url → "
    set_state "$url" "$mode" "$ttl"
  done
}

reset_all() {
  IFS=',' read -ra URLS <<< "$MOCK_ENDPOINTS"
  for url in "${URLS[@]}"; do
    echo -n "$url → "
    reset_mock "$url"
  done
}

health_all() {
  IFS=',' read -ra URLS <<< "$MOCK_ENDPOINTS"
  for url in "${URLS[@]}"; do
    echo -n "$url → "
    curl -sS --max-time 2 "$url/healthz" 2>&1 | jq -c '{token, mode, status}' || echo "FAIL"
  done
}

CMD=${1:-}
case "$CMD" in
  set)
    [[ $# -ge 3 ]] || usage
    set_state "$2" "$3" "${4:-0}"
    ;;
  reset)
    [[ $# -ge 2 ]] || usage
    reset_mock "$2"
    ;;
  get)
    [[ $# -ge 2 ]] || usage
    get_state "$2"
    ;;
  metrics)
    [[ $# -ge 2 ]] || usage
    get_metrics "$2"
    ;;
  set-all)
    [[ $# -ge 2 ]] || usage
    set_all "$2" "${3:-0}"
    ;;
  reset-all)
    reset_all
    ;;
  health-all)
    health_all
    ;;
  *)
    usage
    ;;
esac
