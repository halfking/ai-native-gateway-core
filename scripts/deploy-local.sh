#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${LLM_GATEWAY_ENV_FILE:-/tmp/llm-gateway-local.env}"
LOG_FILE="${LLM_GATEWAY_LOG_FILE:-/tmp/llm-gateway.log}"
PID_FILE="${LLM_GATEWAY_PID_FILE:-/tmp/llm-gateway.pid}"
SERVICE_PORT="${SERVICE_PORT:-8781}"
BASE_URL="${BASE_URL:-http://127.0.0.1:${SERVICE_PORT}}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
require_env() { [[ -n "${!1:-}" ]] || die "$1 must be explicitly set"; }

stop_service() {
  if [[ -f "$PID_FILE" ]]; then
    local pid
    pid="$(<"$PID_FILE")"
    if [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid"
      for _ in {1..20}; do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
    fi
    rm -f "$PID_FILE"
  fi
}

write_env() {
  umask 077
  {
    printf 'export LLM_GATEWAY_DATABASE_URL=%q\n' "$LLM_GATEWAY_DATABASE_URL"
    printf 'export LLM_GATEWAY_SECRET_KEY=%q\n' "$LLM_GATEWAY_SECRET_KEY"
    printf 'export LLM_GATEWAY_ADMIN_API_KEY=%q\n' "$LLM_GATEWAY_ADMIN_API_KEY"
    printf 'export LLM_GATEWAY_ADMIN_USER=%q\n' "$LLM_GATEWAY_ADMIN_USER"
    printf 'export LLM_GATEWAY_ADMIN_PASSWORD=%q\n' "$LLM_GATEWAY_ADMIN_PASSWORD"
    printf 'export LLM_GATEWAY_SEED_ADMIN_PASSWORD=%q\n' "$LLM_GATEWAY_ADMIN_PASSWORD"
    printf 'export LLM_GATEWAY_LISTEN=%q\n' ":$SERVICE_PORT"
    printf 'export LLM_GATEWAY_REDIS_ADDR=%q\n' "${LLM_GATEWAY_REDIS_ADDR:-}"
    printf 'export LLM_GATEWAY_CORS_ORIGINS=%q\n' "${LLM_GATEWAY_CORS_ORIGINS:-http://127.0.0.1:${SERVICE_PORT}}"
    printf 'export LLM_GATEWAY_ENV=%q\n' "${LLM_GATEWAY_ENV:-development}"
  } > "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
}

build_all() {
  need go; need npm
  (cd "$ROOT_DIR" && go build -o llm-gateway ./cmd/gateway)
  (cd "$ROOT_DIR/web" && npm run build)
}

migrate() {
  need psql
  DATABASE_URL="$LLM_GATEWAY_DATABASE_URL" "$SCRIPT_DIR/run-migrations-strict.sh"
}

start_service() {
  [[ -x "$ROOT_DIR/llm-gateway" ]] || die "backend is not built; run $0 deploy"
  [[ -f "$ENV_FILE" ]] || die "secure environment file is missing; run $0 deploy"
  stop_service
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  nohup "$ROOT_DIR/llm-gateway" >"$LOG_FILE" 2>&1 &
  printf '%s\n' "$!" > "$PID_FILE"
  chmod 0600 "$PID_FILE"
}

wait_healthy() {
  for _ in {1..60}; do curl -fsS "$BASE_URL/healthz" >/dev/null && return 0; sleep 1; done
  die "health check failed; inspect $LOG_FILE"
}

verify_full() {
  need curl; need node
  wait_healthy
  local login_file token
  login_file="$(mktemp)"
  trap 'rm -f "$login_file"' RETURN
  curl -fsS -H 'Content-Type: application/json' -X POST "$BASE_URL/api/auth/token" \
    --data "$(node -e 'process.stdout.write(JSON.stringify({username:process.env.LLM_GATEWAY_ADMIN_USER,password:process.env.LLM_GATEWAY_ADMIN_PASSWORD}))')" >"$login_file"
  token="$(node -e 'const fs=require("fs"); const x=JSON.parse(fs.readFileSync(process.argv[1])); process.stdout.write(x.access_token||"")' "$login_file")"
  [[ -n "$token" ]] || die "login succeeded without an access token"
  curl -fsS -H "Authorization: Bearer $token" "$BASE_URL/api/auth/me" >/dev/null
  curl -fsS -H "Authorization: Bearer $token" "$BASE_URL/api/admin/dashboard/session-overview" >/dev/null
  curl -fsS -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" "$BASE_URL/metrics" | grep -q '^# TYPE'
  printf 'Deployment gates passed: health, login, auth-me, dashboard, metrics.\n'
}

deploy() {
  for name in LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_ADMIN_API_KEY LLM_GATEWAY_ADMIN_USER LLM_GATEWAY_ADMIN_PASSWORD; do require_env "$name"; done
  write_env
  migrate
  build_all
  start_service
  verify_full
  printf 'Gateway is running at %s; secrets are stored only in %s (0600).\n' "$BASE_URL" "$ENV_FILE"
}

case "${1:-deploy}" in
  deploy) deploy ;;
  start) start_service; wait_healthy ;;
  stop) stop_service ;;
  restart) start_service; wait_healthy ;;
  status) [[ -f "$PID_FILE" ]] && kill -0 "$(<"$PID_FILE")" 2>/dev/null ;;
  verify) for name in LLM_GATEWAY_ADMIN_API_KEY LLM_GATEWAY_ADMIN_USER LLM_GATEWAY_ADMIN_PASSWORD; do require_env "$name"; done; verify_full ;;
  logs) exec tail -f "$LOG_FILE" ;;
  *) die "usage: $0 {deploy|start|stop|restart|status|verify|logs}" ;;
esac
