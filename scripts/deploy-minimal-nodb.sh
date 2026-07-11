#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${LLM_GATEWAY_ENV_FILE:-/tmp/llm-gateway-minimal.env}"
LOG_FILE="${LLM_GATEWAY_LOG_FILE:-/tmp/llm-gateway-minimal.log}"
PID_FILE="${LLM_GATEWAY_PID_FILE:-/tmp/llm-gateway-minimal.pid}"
SERVICE_PORT="${SERVICE_PORT:-8781}"
: "${LLM_GATEWAY_SECRET_KEY:?LLM_GATEWAY_SECRET_KEY must be explicitly set}"
: "${LLM_GATEWAY_ADMIN_API_KEY:?LLM_GATEWAY_ADMIN_API_KEY must be explicitly set}"

command -v go >/dev/null || { printf 'error: go is required\n' >&2; exit 1; }
command -v npm >/dev/null || { printf 'error: npm is required\n' >&2; exit 1; }
(cd "$ROOT_DIR" && go build -o llm-gateway ./cmd/gateway)
(cd "$ROOT_DIR/web" && npm run build)

umask 077
{
  printf 'export LLM_GATEWAY_DATABASE_URL=%q\n' ''
  printf 'export LLM_GATEWAY_REDIS_ADDR=%q\n' ''
  printf 'export LLM_GATEWAY_LISTEN=%q\n' ":$SERVICE_PORT"
  printf 'export LLM_GATEWAY_SECRET_KEY=%q\n' "$LLM_GATEWAY_SECRET_KEY"
  printf 'export LLM_GATEWAY_ADMIN_API_KEY=%q\n' "$LLM_GATEWAY_ADMIN_API_KEY"
  printf 'export LLM_GATEWAY_CORS_ORIGINS=%q\n' "${LLM_GATEWAY_CORS_ORIGINS:-http://127.0.0.1:${SERVICE_PORT}}"
  printf 'export LLM_GATEWAY_ENV=%q\n' development
} >"$ENV_FILE"
chmod 0600 "$ENV_FILE"
if [[ -f "$PID_FILE" ]] && kill -0 "$(<"$PID_FILE")" 2>/dev/null; then kill "$(<"$PID_FILE")"; fi
# shellcheck disable=SC1090
source "$ENV_FILE"
nohup "$ROOT_DIR/llm-gateway" >"$LOG_FILE" 2>&1 &
printf '%s\n' "$!" >"$PID_FILE"; chmod 0600 "$PID_FILE"
for _ in {1..30}; do curl -fsS "http://127.0.0.1:${SERVICE_PORT}/healthz" >/dev/null && break; sleep 1; done
curl -fsS "http://127.0.0.1:${SERVICE_PORT}/healthz" >/dev/null
code="$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -X POST \
  --data '{"username":"unavailable","password":"unavailable"}' "http://127.0.0.1:${SERVICE_PORT}/api/auth/token")"
[[ "$code" != 2* ]] || { printf 'error: no-DB mode unexpectedly allowed login\n' >&2; exit 1; }
printf 'No-DB mode is running at http://127.0.0.1:%s. Login and database-backed dashboard features are unavailable.\n' "$SERVICE_PORT"
