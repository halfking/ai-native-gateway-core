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
require_env() { [[ -n "${!1:-}" ]] || die "$1 must be explicitly set (or create $ROOT_DIR/.env.local from .env.local.example)"; }

# Auto-load local defaults so `./scripts/deploy-local.sh [deploy]` works without
# manually sourcing anything: if the required vars are absent and a 0600
# .env.local exists at the repo root, source it. Explicitly exported variables
# always win — the file is only a fallback, never an override.
if [[ -z "${LLM_GATEWAY_DATABASE_URL:-}" && -f "$ROOT_DIR/.env.local" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT_DIR/.env.local" >/dev/null
fi

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
    printf 'export URSM_V2_MODE=%q\n' "${URSM_V2_MODE:-shadow}"
    # Licensing center (ai-native-maintain) + RSA verify key for issued licenses.
    printf 'export LLM_GATEWAY_CENTER_URL=%q\n' "${LLM_GATEWAY_CENTER_URL:-}"
    printf 'export LLM_GATEWAY_LICENSE_PUBLIC_KEY=%q\n' "${LLM_GATEWAY_LICENSE_PUBLIC_KEY:-}"
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
    baseline_ledger
    return 0
  fi
  # Synced-from-252 case: relations exist but repository_schema_migrations was
  # not part of the sync, so the strict runner would refuse (exit 3). Baseline
  # the repo's migrations as applied, then run strictly to apply anything newer.
  local ledger_count
  ledger_count=$(psql -X -Atqc 'SELECT count(*) FROM public.repository_schema_migrations' "$LLM_GATEWAY_DATABASE_URL" 2>/dev/null || echo 0)
  if [[ "$ledger_count" == "0" ]]; then
    printf 'Populated schema with empty migration ledger detected; baselining ledger first.\n'
    baseline_ledger
    return 0
  fi
  # Partial-ledger case: schema was synced from 252 (or another upstream) with
  # only a slice of the migration ledger copied — typically mid-range entries
  # (e.g. 044, 080-083, 330-358, 382+) while the early migrations 000..043 were
  # never recorded. Running strict migrations forward would re-execute every
  # absent migration against the already-populated schema, which trips errors
  # like "column session_id does not exist" (the 252 schema diverged).
  #
  # We can't use --baseline-through here: run-migrations-strict.sh refuses to
  # baseline a non-empty ledger. Instead, INSERT rows for the missing
  # migrations directly into repository_schema_migrations so the strict runner
  # considers them already applied. Detection: lowest recorded startup
  # version > 000 indicates early migrations are missing.
  if needs_partial_ledger_backfill; then
    printf 'Partial migration ledger detected; backfilling missing rows before strict run.\n'
    backfill_partial_ledger
  fi
  if ! DATABASE_URL="$LLM_GATEWAY_DATABASE_URL" "$SCRIPT_DIR/run-migrations-strict.sh"; then
    die "migrations failed; if repository_schema_migrations is empty on a populated database, rerun with --baseline-through <known-version> via scripts/run-migrations-strict.sh directly"
  fi
}

# Returns 0 (true) iff the public.repository_schema_migrations ledger is
# non-empty but missing early startup migrations (lowest recorded startup
# version > 000). This is the signature of a schema synced from another
# instance whose ledger copy was incomplete.
needs_partial_ledger_backfill() {
  local lowest_startup highest_recorded
  lowest_startup=$(psql -X -Atqc \
    "SELECT MIN(version) FROM public.repository_schema_migrations \
     WHERE scope='startup' AND version ~ '^[0-9]+'" \
    "$LLM_GATEWAY_DATABASE_URL" 2>/dev/null || true)
  highest_recorded=$(psql -X -Atqc \
    "SELECT COALESCE(MAX(version), '0') FROM public.repository_schema_migrations \
     WHERE version ~ '^[0-9]+'" \
    "$LLM_GATEWAY_DATABASE_URL" 2>/dev/null || echo 0)
  # Only act when there is a startup ledger entry whose version is strictly
  # greater than 000 (early migrations missing) and the ledger isn't empty.
  [[ -n "$lowest_startup" && "$lowest_startup" != "000" && "$highest_recorded" != "0" ]]
}

# Backfill repository_schema_migrations with rows for any migration file
# in the repo whose version (per the strict runner's parsing rules) is ≤ the
# highest already-recorded version in that scope. Each row records the file's
# actual sha256 (matching run-migrations-strict.sh:file_checksum) so the
# runner's "already applied" check passes on subsequent runs. ON CONFLICT
# DO UPDATE keeps re-runs idempotent and corrects any prior rows that were
# backfilled with empty checksums. Each scope (startup/domain/ursm) is
# processed independently; scopes with no recorded entries are skipped.
backfill_partial_ledger() {
  local scope migration_root scope_highest inserted_before inserted_after sql_file
  for scope in startup domain ursm; do
    migration_root="$ROOT_DIR/sql/migrations/$scope"
    [[ -d "$migration_root" ]] || continue
    scope_highest=$(psql -X -Atqc \
      "SELECT COALESCE(MAX(version), '0') FROM public.repository_schema_migrations \
       WHERE scope='$scope' AND version ~ '^[0-9]+'" \
      "$LLM_GATEWAY_DATABASE_URL" 2>/dev/null || echo 0)
    [[ "$scope_highest" != "0" ]] || continue
    inserted_before=$(psql -X -Atqc \
      "SELECT count(*) FROM public.repository_schema_migrations WHERE scope='$scope'" \
      "$LLM_GATEWAY_DATABASE_URL" 2>/dev/null || echo 0)
    # Generate a temp SQL file then apply it via psql -f. The find pipeline
    # emits one INSERT ... ON CONFLICT DO NOTHING per migration whose
    # parsed version is ≤ the recorded high-water mark, mirroring the
    # strict runner's filename parsing so the backfill lines up.
    #
    # The strict runner decides "already applied" by comparing the stored
    # checksum against the file's current sha256 — so we have to record
    # the *actual* sha256 of every file, not an empty string; otherwise
    # the runner would still try to re-apply the migration and trip the
    # ledger PK or a "column already exists" error.
    sql_file=$(mktemp -t llm-gw-backfill-XXXXXX.sql)
    {
      printf -- '-- auto-generated backfill for scope=%s baselined_through=%s\n' "$scope" "$scope_highest"
      printf 'BEGIN;\n'
      find "$migration_root" -maxdepth 1 -type f -name '[0-9]*.sql' \
        ! -name '*.down.sql' 2>/dev/null | sort | while read -r file; do
        filename=$(basename "$file")
        if [[ "$scope" == "ursm" ]]; then
          version=${filename%%-*}
        else
          version=${filename%%_*}
        fi
        version_number=${version%%[^0-9]*}
        # Skip date-style prefixes whose leading digits parse as huge
        # numbers; the recorded high-water mark is always a small integer
        # (e.g. 602) so they'd never match anyway.
        if [[ ! "$version_number" =~ ^[0-9]+$ ]]; then continue; fi
        if (( 10#$version_number > 10#$scope_highest )); then continue; fi
        # Match run-migrations-strict.sh:file_checksum — shasum -a 256.
        checksum=$(shasum -a 256 "$file" 2>/dev/null | cut -d ' ' -f 1)
        [[ -n "$checksum" ]] || checksum=''
        printf "INSERT INTO public.repository_schema_migrations \
(scope, version, migration_name, checksum) VALUES ('%s','%s','%s','%s') \
ON CONFLICT (scope, migration_name) DO UPDATE SET checksum = EXCLUDED.checksum;\n" \
          "$scope" "$version" "$filename" "$checksum"
      done
      printf 'COMMIT;\n'
    } > "$sql_file"
    if ! psql -X -v ON_ERROR_STOP=1 -q -f "$sql_file" "$LLM_GATEWAY_DATABASE_URL" >/dev/null 2>&1; then
      die "partial-ledger backfill failed for scope=$scope; inspect $sql_file manually"
    fi
    rm -f "$sql_file"
    inserted_after=$(psql -X -Atqc \
      "SELECT count(*) FROM public.repository_schema_migrations WHERE scope='$scope'" \
      "$LLM_GATEWAY_DATABASE_URL" 2>/dev/null || echo 0)
    printf '  scope=%s baselined_through=%s inserted=%d\n' \
      "$scope" "$scope_highest" "$(( inserted_after - inserted_before ))"
  done
}

baseline_ledger() {
  # Record ≤baseline_through as applied (no execution) so later migrations still run.
  local baseline_through
  baseline_through=$(latest_migration_version)
  printf 'Baselining existing schema through version %s; later migrations will be applied.\n' "$baseline_through"
  if ! DATABASE_URL="$LLM_GATEWAY_DATABASE_URL" "$SCRIPT_DIR/run-migrations-strict.sh" --baseline-through "$baseline_through"; then
    die "baseline migration failed"
  fi
  # Re-run in default mode to apply only new migrations (ledger now non-empty).
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

# POST /api/auth/token; prints HTTP status, body goes to $1.
login_status() {
  local body_file="$1" pass="$2"
  curl -sS --max-time 5 -o "$body_file" -w '%{http_code}' \
    -H 'Content-Type: application/json' -X POST "$BASE_URL/api/auth/token" \
    --data "$(node -e 'process.stdout.write(JSON.stringify({username:process.env.LLM_GATEWAY_ADMIN_USER,password:process.argv[1]}))' "$pass")"
}

reset_admin_password_db() {
  # Local-dev self-heal: .env.local generates a fresh random admin password per
  # deploy while the seeded admin row persists with the previous one. Reset the
  # row to the current env password (first-login state) via bcrypt.
  local hash
  hash=$(python3 -c 'import bcrypt,sys; print(bcrypt.hashpw(sys.argv[1].encode(), bcrypt.gensalt()).decode())' "$1") \
    || die "python3 bcrypt unavailable; cannot reset admin password"
  psql -X -Atqc "UPDATE users SET password_hash='$hash', must_change_password=TRUE, enabled=TRUE WHERE username='$LLM_GATEWAY_ADMIN_USER'" \
    "$LLM_GATEWAY_DATABASE_URL" >/dev/null
}

persist_admin_password() {
  # Rewrite the admin password lines in the 0600 ENV_FILE so later bare
  # `verify`/`restart` invocations use the working credential.
  local newpass="$1"
  [[ -f "$ENV_FILE" ]] || return 0
  sed -i.bak \
    -e "s|^export LLM_GATEWAY_ADMIN_PASSWORD=.*|export LLM_GATEWAY_ADMIN_PASSWORD=$newpass|" \
    -e "s|^export LLM_GATEWAY_SEED_ADMIN_PASSWORD=.*|export LLM_GATEWAY_SEED_ADMIN_PASSWORD=$newpass|" \
    "$ENV_FILE" && rm -f "$ENV_FILE.bak"
}

verify_full() {
  need curl; need node
  wait_healthy
  local login_file token code must_change pass
  login_file="$(mktemp)"
  # NOTE: no RETURN trap here — under set -u a lingering RETURN trap fired at
  # the caller's scope ("login_file: unbound variable"). Cleanup is explicit.
  pass="$LLM_GATEWAY_ADMIN_PASSWORD"
  code="$(login_status "$login_file" "$pass")"
  if [[ "$code" != "200" ]]; then
    printf 'admin login returned %s; resetting local admin password to the env value\n' "$code"
    reset_admin_password_db "$pass"
    code="$(login_status "$login_file" "$pass")"
  fi
  [[ "$code" == "200" ]] || die "admin login failed (HTTP $code)"
  token="$(node -e 'const fs=require("fs"); const x=JSON.parse(fs.readFileSync(process.argv[1])); process.stdout.write(x.access_token||"")' "$login_file")"
  [[ -n "$token" ]] || die "login succeeded without an access token"
  # Rule 20 §6.2: a must_change_password admin may only call me/change-password/
  # logout. Complete the mandatory first-login change via the API so the
  # dashboard gate below is reachable, then re-login with the new credential.
  must_change="$(node -e 'const fs=require("fs"); const x=JSON.parse(fs.readFileSync(process.argv[1])); process.stdout.write(String(!!(x.user&&x.user.must_change_password)))' "$login_file")"
  if [[ "$must_change" == "true" ]]; then
    local newpass="Local-$(openssl rand -hex 12)"
    code="$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -X PUT "$BASE_URL/api/auth/change-password" \
      --data "$(node -e 'process.stdout.write(JSON.stringify({old_password:process.argv[1],new_password:process.argv[2]}))' "$pass" "$newpass")")"
    [[ "$code" == "200" ]] || die "first-login password change failed (HTTP $code)"
    persist_admin_password "$newpass"
    export LLM_GATEWAY_ADMIN_PASSWORD="$newpass"
    pass="$newpass"
    code="$(login_status "$login_file" "$pass")"
    [[ "$code" == "200" ]] || die "re-login after password change failed (HTTP $code)"
    token="$(node -e 'const fs=require("fs"); const x=JSON.parse(fs.readFileSync(process.argv[1])); process.stdout.write(x.access_token||"")' "$login_file")"
  fi
  curl -fsS --max-time 5 -H "Authorization: Bearer $token" "$BASE_URL/api/auth/me" >/dev/null
  curl -fsS --max-time 5 -H "Authorization: Bearer $token" "$BASE_URL/api/admin/dashboard/session-overview" >/dev/null
  curl -fsS --max-time 5 -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" "$BASE_URL/metrics" -o "$login_file.metrics" \
    || die "metrics endpoint failed"
  grep -q '^# TYPE' "$login_file.metrics" || die "metrics output is not Prometheus format"
  rm -f "$login_file" "$login_file.metrics"
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
  verify)
    # Prefer the credentials captured at deploy time (ENV_FILE): random
    # secrets generated per-source in .env.local would otherwise mismatch.
    if [[ -f "$ENV_FILE" ]]; then
      # shellcheck disable=SC1090
      source "$ENV_FILE"
    fi
    for name in LLM_GATEWAY_ADMIN_API_KEY LLM_GATEWAY_ADMIN_USER LLM_GATEWAY_ADMIN_PASSWORD; do require_env "$name"; done
    verify_full
    ;;
  logs) exec tail -f "$LOG_FILE" ;;
  *) die "usage: $0 {deploy|start|stop|restart|status|verify|logs}" ;;
esac
