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
      kill "$pid" 2>/dev/null || true
      # graceful wait (5s)
      for _ in {1..20}; do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
      # escalation: SIGKILL if still alive
      if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null || true
        for _ in {1..20}; do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
      fi
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

apply_schema_snapshot() {
  # Empty database bootstrap: apply schema snapshot before strict migrations.
  # 01-schema.sql is a pg_dump that contains forward references (functions
  # referring to tables created later in the file). We must use
  # ON_ERROR_STOP=0 with a "real error" filter so the whole snapshot applies
  # without aborting on those forward references.
  local schema_dir="$ROOT_DIR/sql/schema"
  [[ -d "$schema_dir" ]] || die "schema snapshot directory missing: $schema_dir"
  for f in 00-prereqs.sql 01-schema.sql 02-seed.sql; do
    local path="$schema_dir/$f"
    [[ -f "$path" ]] || die "schema snapshot file missing: $path"
    printf 'Applying schema/%s\n' "$f"
    local log="/tmp/llm-gateway-schema-$(basename "$f").log"
    if ! psql -X -v ON_ERROR_STOP=0 -q "$LLM_GATEWAY_DATABASE_URL" -f "$path" >"$log" 2>&1; then
      die "schema/$f failed; see $log"
    fi
    if grep -qiE "error|fatal" "$log" && ! grep -qiE "already exists|duplicate key|exists, skipping|does not exist, skipping" "$log"; then
      die "schema/$f reported fatal errors; see $log"
    fi
  done
}

# Detect empty database (no ledger + no relations) so deploy works on a fresh PG.
database_is_empty() {
  local count
  count=$(psql -X -Atqc "SELECT count(*) FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relkind IN ('r','p','v','m','S','f') AND relname <> 'repository_schema_migrations'" "$LLM_GATEWAY_DATABASE_URL" 2>/dev/null) || return 1
  [[ "$count" == "0" ]]
}

migrate() {
  need psql
  if database_is_empty; then
    printf 'Empty database detected; applying schema snapshot before migrations.\n'
    apply_schema_snapshot
    # After schema snapshot the strict migration runner would refuse because
    # the ledger is empty while relations exist. Bootstrap mode is wrong here
    # because snapshot already created most tables; baseline mode records
    # ≤baseline_through as applied (no execution) so later migrations still run.
    local baseline_through
    baseline_through=$(latest_migration_version)
    printf 'Baselining existing schema through version %s; later migrations will be applied.\n' "$baseline_through"
    if ! DATABASE_URL="$LLM_GATEWAY_DATABASE_URL" "$SCRIPT_DIR/run-migrations-strict.sh" --baseline-through "$baseline_through"; then
      die "baseline migration failed"
    fi
    # Now re-run in default mode to apply only new migrations (ledger non-empty).
    if ! DATABASE_URL="$LLM_GATEWAY_DATABASE_URL" "$SCRIPT_DIR/run-migrations-strict.sh"; then
      die "migrations failed; if repository_schema_migrations is empty on a populated database, rerun with --baseline-through <known-version> via scripts/run-migrations-strict.sh directly"
    fi
    return 0
  fi
  if ! DATABASE_URL="$LLM_GATEWAY_DATABASE_URL" "$SCRIPT_DIR/run-migrations-strict.sh"; then
    die "migrations failed; if repository_schema_migrations is empty on a populated database, rerun with --baseline-through <known-version> via scripts/run-migrations-strict.sh directly"
  fi
}

latest_migration_version() {
  # Highest numeric version prefix across startup/domain/ursm scopes (matches
  # the parsing in run-migrations-strict.sh: leading digits before any '-_').
  find "$ROOT_DIR/sql/migrations" -type f -name '[0-9]*-*.sql' -o -name '[0-9]*_*.sql' 2>/dev/null \
    | while read -r f; do
        base=$(basename "$f")
        case "$base" in *.down.sql) continue;; esac
        # strict's regex for ursm is "[0-9]*-*.sql" and for others "[0-9]*.sql"
        if [[ "$base" == *-* ]]; then
          v=${base%%-*}
        else
          v=${base%%_*}
        fi
        printf '%s\n' "${v%%[^0-9]*}"
      done | sort -n | tail -1
}

start_service() {
  [[ -x "$ROOT_DIR/llm-gateway" ]] || die "backend is not built; run $0 deploy"
  [[ -f "$ENV_FILE" ]] || die "secure environment file is missing; run $0 deploy"
  stop_service
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  nohup "$ROOT_DIR/llm-gateway" >"$LOG_FILE" 2>&1 &
  local pid=$!
  printf '%s\n' "$pid" > "$PID_FILE"
  chmod 0600 "$PID_FILE"
  # Self-check: pid must still be alive shortly after launch. If the binary
  # exits immediately (missing env, port collision, DB unreachable), the
  # PID file would otherwise mislead later steps.
  sleep 0.5
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$PID_FILE"
    die "service exited immediately after start; inspect $LOG_FILE"
  fi
}

# Absolute-deadline health probe. Bounded by ${HEALTH_TIMEOUT:-60}s to keep
# cutover within the 2-minute budget enforced by llm-gateway-deploy-test.
# Uses --max-time so a single curl can never stall beyond its own budget.
wait_healthy() {
  local timeout_s=${HEALTH_TIMEOUT:-60} deadline elapsed
  deadline=$(( $(date +%s) + timeout_s ))
  while :; do
    elapsed=$(( deadline - $(date +%s) ))
    if (( elapsed <= 0 )); then break; fi
    if curl -fsS --max-time 2 "$BASE_URL/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "health check failed after ${timeout_s}s; inspect $LOG_FILE"
}

verify_full() {
  need curl; need node
  wait_healthy
  local login_file token
  login_file="$(mktemp)"
  trap 'rm -f "$login_file"' RETURN
  curl -fsS --max-time 5 -H 'Content-Type: application/json' -X POST "$BASE_URL/api/auth/token" \
    --data "$(node -e 'process.stdout.write(JSON.stringify({username:process.env.LLM_GATEWAY_ADMIN_USER,password:process.env.LLM_GATEWAY_ADMIN_PASSWORD}))')" >"$login_file"
  token="$(node -e 'const fs=require("fs"); const x=JSON.parse(fs.readFileSync(process.argv[1])); process.stdout.write(x.access_token||"")' "$login_file")"
  [[ -n "$token" ]] || die "login succeeded without an access token"
  curl -fsS --max-time 5 -H "Authorization: Bearer $token" "$BASE_URL/api/auth/me" >/dev/null
  curl -fsS --max-time 5 -H "Authorization: Bearer $token" "$BASE_URL/api/admin/dashboard/session-overview" >/dev/null
  curl -fsS --max-time 5 -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" "$BASE_URL/metrics" | grep -q '^# TYPE'
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
  status)
    if [[ -f "$PID_FILE" ]] && kill -0 "$(<"$PID_FILE")" 2>/dev/null; then
      printf 'gateway running pid=%s port=%s url=%s\n' "$(<"$PID_FILE")" "$SERVICE_PORT" "$BASE_URL"
    else
      printf 'gateway not running (pid_file=%s)\n' "$PID_FILE"
      exit 1
    fi
    ;;
  verify) for name in LLM_GATEWAY_ADMIN_API_KEY LLM_GATEWAY_ADMIN_USER LLM_GATEWAY_ADMIN_PASSWORD; do require_env "$name"; done; verify_full ;;
  logs) exec tail -f "$LOG_FILE" ;;
  *) die "usage: $0 {deploy|start|stop|restart|status|verify|logs}" ;;
esac
