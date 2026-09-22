#!/usr/bin/env bash
# tests/stress/scripts/runner.sh
#
# Stress-test lifecycle: start 4 mock upstreams + 1 test gateway, run
# scenarios, tear down. All supplier / model names are NON-REAL
# (mock-alpha / mock-stress-*).
#
# Usage:
#   ./tests/stress/scripts/runner.sh start    # only start the harness
#   ./tests/stress/scripts/runner.sh stop     # tear down
#   ./tests/stress/scripts/runner.sh status   # show status
#   ./tests/stress/scripts/runner.sh logs     # tail all logs
#
# Logs: tests/stress/logs/*.log
# Pids: tests/stress/logs/*.pid

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
STRESS="$SCRIPT_DIR/.."
LOG_DIR="$STRESS/logs"
mkdir -p "$LOG_DIR"

STRESS_MOCK="${STRESS_MOCK:-/tmp/stress-mock}"
STRESS_GATEWAY="${STRESS_GATEWAY:-/tmp/stress-gateway}"
GATEWAY_PORT="${GATEWAY_PORT:-18901}"
MOCK_PORTS=(18101 18102 18103 18104)
MOCK_NAMES=(alpha beta gamma delta)

# ANSI helpers
c_red=$'\033[0;31m'; c_grn=$'\033[0;32m'; c_yel=$'\033[0;33m'
c_blu=$'\033[0;34m'; c_dim=$'\033[2m'; c_off=$'\033[0m'
log()  { printf '%s[runner]%s %s\n' "$c_blu" "$c_off" "$*"; }
ok()   { printf '%s[ok]%s %s\n' "$c_grn" "$c_off" "$*"; }
warn() { printf '%s[warn]%s %s\n' "$c_yel" "$c_off" "$*"; }
err()  { printf '%s[err]%s %s\n' "$c_red" "$c_off" "$*" >&2; }

start_mocks() {
  log "Starting 4 mock upstreams..."
  for i in 0 1 2 3; do
    local name="mock-${MOCK_NAMES[$i]}"
    local port="${MOCK_PORTS[$i]}"
    local logfile="$LOG_DIR/mock-$name.log"
    local pidfile="$LOG_DIR/mock-$name.pid"
    if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
      ok "  $name (port $port) already running pid=$(cat "$pidfile")"
      continue
    fi
    "$STRESS_MOCK" -name="$name" -port="$port" \
      -latency-min=20ms -latency-max=80ms \
      > "$logfile" 2>&1 &
    echo $! > "$pidfile"
    ok "  $name (port $port) pid=$!"
  done
  sleep 2
  for i in 0 1 2 3; do
    local name="mock-${MOCK_NAMES[$i]}"
    local port="${MOCK_PORTS[$i]}"
    if curl -sf --max-time 1 "http://127.0.0.1:$port/healthz" >/dev/null; then
      ok "  $name healthy"
    else
      err "  $name unreachable on :$port"
    fi
  done
}

start_gateway() {
  local logfile="$LOG_DIR/gateway.log"
  local pidfile="$LOG_DIR/gateway.pid"
  if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
    ok "test-gateway already running pid=$(cat "$pidfile")"
    return
  fi
  "$STRESS_GATEWAY" -port="$GATEWAY_PORT" -upstream-timeout=25s \
    > "$logfile" 2>&1 &
  echo $! > "$pidfile"
  sleep 1
  if curl -sf "http://127.0.0.1:$GATEWAY_PORT/healthz" >/dev/null; then
    ok "test-gateway listening :$GATEWAY_PORT pid=$(cat "$pidfile")"
  else
    err "test-gateway failed to start; see $logfile"
    return 1
  fi
}

stop_all() {
  log "Stopping harness..."
  for f in "$LOG_DIR"/*.pid; do
    [[ -f "$f" ]] || continue
    local pid; pid=$(cat "$f")
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      sleep 0.2
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$f"
  done
  ok "harness stopped"
}

show_status() {
  printf '%-30s %-8s %-8s %s\n' "name" "pid" "port" "status"
  for i in 0 1 2 3; do
    local name="mock-${MOCK_NAMES[$i]}"
    local port="${MOCK_PORTS[$i]}"
    local pidfile="$LOG_DIR/mock-$name.pid"
    local pid="—"
    [[ -f "$pidfile" ]] && pid=$(cat "$pidfile")
    local ok="down"
    if curl -sf --max-time 1 "http://127.0.0.1:$port/healthz" >/dev/null; then
      ok="up"
    fi
    printf '%-30s %-8s %-8s %s\n' "$name" "$pid" "$port" "$ok"
  done
  local gw_pid="—"
  [[ -f "$LOG_DIR/gateway.pid" ]] && gw_pid=$(cat "$LOG_DIR/gateway.pid")
  local gw_ok="down"
  curl -sf --max-time 1 "http://127.0.0.1:$GATEWAY_PORT/healthz" >/dev/null && gw_ok="up"
  printf '%-30s %-8s %-8s %s\n' "test-gateway" "$gw_pid" "$GATEWAY_PORT" "$gw_ok"
}

case "${1:-status}" in
  start)
    start_mocks
    start_gateway
    show_status
    ;;
  stop)
    stop_all
    ;;
  restart)
    stop_all
    sleep 1
    start_mocks
    start_gateway
    show_status
    ;;
  status)
    show_status
    ;;
  logs)
    ls -la "$LOG_DIR"/*.log 2>/dev/null
    tail -n 50 "$LOG_DIR"/*.log
    ;;
  *)
    cat <<USAGE
Usage: $0 {start|stop|restart|status|logs}
USAGE
    exit 1
    ;;
esac