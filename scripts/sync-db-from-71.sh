#!/usr/bin/env bash

# Sync llm_gateway from server 71 (test-apps-infra) to local r112_postgres.
#
# Server 71 topology:
#   - SSH: __HOST_71_IP__:25022 (key: ~/.ssh/71_id_rsa)
#   - PG: docker container "llm-gateway-pg-71-replica" running directly on host
#   - Credentials inside container: kxuser / kxpass / db=llm_gateway
#   - PG version: 15.3 (same as local)
#   - No k8s layer, no kubectl, no replication privilege needed
#
# Difference from sync-db-from-184.sh:
#   - No k8s (so no `kubectl exec ...` wrapper)
#   - Direct `docker exec ... pg_dump` on the remote host
#   - No replicator / replication privilege required
#   - pg_basebackup would also work but pg_dump is simpler for this topology
#
# Modes:
#   full         Recreate local DB, then stream remote schema+data into it.
#   schema-only  Replace local public schema using remote schema-only dump.
#   data-only    Truncate local public tables (skips hot tables), stream remote data-only dump.
#
# Verification (always runs at the end):
#   - Compare public table counts
#   - Compare key static table counts
#   - For request_logs: warn on hot-table drift instead of failing

set -euo pipefail

MODE="${1:-full}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode"
BACKUP_DIR="$TMP_ROOT/llmgw-db-sync-71-$(date +%Y%m%d-%H%M%S)"

REMOTE_SSH_HOST="${REMOTE_SSH_HOST:-root@__HOST_71_IP__}"
REMOTE_SSH_PORT="${REMOTE_SSH_PORT:-25022}"
REMOTE_SSH_IDENTITY="${REMOTE_SSH_IDENTITY:-$HOME/.ssh/71_id_rsa}"
REMOTE_PG_CONTAINER="${REMOTE_PG_CONTAINER:-llm-gateway-pg-71-replica}"
REMOTE_DB="${REMOTE_DB:-llm_gateway}"
# Use the superuser for export because some tables (e.g. approval_queue)
# have FORCE ROW LEVEL SECURITY and the regular kxuser role gets blocked.
# 71's docker compose sets POSTGRES_USER=llm_gateway with this password.
REMOTE_DB_USER="${REMOTE_DB_USER:-llm_gateway}"
REMOTE_DB_PASS="${REMOTE_DB_PASS:-__REDACTED_DB_PASSWORD__}"
# SSH agent has multiple keys. Without IdentitiesOnly=yes, ssh tries them all
# and may hang if 71's sshd has a slow response to unknown keys.
REMOTE_SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=20 -o IdentitiesOnly=yes -o PreferredAuthentications=publickey"

LOCAL_CONTAINER="${LOCAL_CONTAINER:-r112_postgres}"
LOCAL_ADMIN_DB="${LOCAL_ADMIN_DB:-postgres}"
LOCAL_DB="${LOCAL_DB:-llm_gateway}"
LOCAL_DB_USER="${LOCAL_DB_USER:-kxuser}"
LOCAL_DB_PASS="${LOCAL_DB_PASS:-kxpass}"
# Local superuser (brought over from 184 pg_basebackup). Needed for the
# local pre-sync backup because some tables have FORCE ROW LEVEL SECURITY
# and the regular kxuser role gets blocked by pg_dump.
LOCAL_DB_SUPERUSER="${LOCAL_DB_SUPERUSER:-llm_gateway}"
LOCAL_DB_SUPERPASS="${LOCAL_DB_SUPERPASS:-__REDACTED_DB_PASSWORD__}"

KEY_TABLES=(
  approval_queue
  tool_registry
  tenant_model_policies
  request_logs
)

# Hot tables: written continuously by gateway runtime. Excluded from
# data-only TRUNCATE + remote COPY to avoid log/data-dirty failures.
HOT_TABLES=(
  request_logs
  request_logs_2026_07
  request_logs_2026_08
  request_logs_archive
  request_logs_archive_2026_06
  request_logs_archive_2026_07
  request_logs_default
  request_wal
  request_wal_2026_06
  request_wal_2026_07
  request_wal_bodies
  usage_ledger
  usage_ledger_2026_06
  usage_ledger_2026_07
  usage_ledger_2026_08
  usage_ledger_old
  usage_minute
  armor_judgments
  routing_audit_log
  routing_decision_log
  route_decisions
  candidate_failure_logs
  credential_probe_model_log
  credential_model_call_history
  credential_model_stats_1m
  credential_model_peak_1m
  credential_model_weekly_peak
  credential_quota_usage
  key_rpm_daily
  api_key_model_cost
  api_key_auto_profile
  model_probe_runs
  model_probe_state
  passive_probe_state
  credential_health_checks
  model_offer_events
  price_change_events
  provider_events
  tool_call_events
  tool_usage_stats
  tool_usage_stats_2026_06
  tool_usage_stats_2026_07
  tool_usage_stats_2026_08
  tool_usage_stats_old
  session_audit_records
  session_memora_extraction_log
  session_titles
  token_audit_events
  response_format_anomalies
  model_reconcile_log
  model_discovery_runs
  pricing_refresh_log
  auto_tune_audit
  schema_migration_audit
  background_tasks
  background_tasks_duplicates
  security_audit_log
)

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
err()  { printf "${RED}✗ %s${NC}\n" "$*" >&2; }
ok()   { printf "${GREEN}✓ %s${NC}\n" "$*"; }
info() { printf "${YELLOW}▶ %s${NC}\n" "$*"; }

usage() {
  cat <<'EOF'
Usage:
  ./scripts/sync-db-from-71.sh [full|schema-only|data-only]

Examples:
  ./scripts/sync-db-from-71.sh full
  ./scripts/sync-db-from-71.sh schema-only
  ./scripts/sync-db-from-71.sh data-only

Environment overrides:
  REMOTE_SSH_HOST, REMOTE_SSH_PORT, REMOTE_SSH_IDENTITY
  REMOTE_PG_CONTAINER, REMOTE_DB, REMOTE_DB_USER, REMOTE_DB_PASS
  LOCAL_CONTAINER, LOCAL_ADMIN_DB, LOCAL_DB, LOCAL_DB_USER, LOCAL_DB_PASS
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { err "missing command: $1"; exit 1; }
}

# Strip psql 16+ meta-commands that psql 15.x doesn't understand (in case
# 71 is later upgraded to a newer pg_dump that emits \restrict).
filter_dump_for_legacy_psql() {
  rg -v '^\\(restrict|unrestrict)( |$)' || cat
}

run_local_psql() {
  # Use the local superuser (llm_gateway) by default because RLS-protected
  # tables and CREATE DATABASE require superuser privileges.
  local user="${1:-$LOCAL_DB_SUPERUSER}"
  local pass="${2:-$LOCAL_DB_SUPERPASS}"
  local sql="$3"
  docker exec -e PGPASSWORD="$pass" "$LOCAL_CONTAINER" \
    psql -U "$user" -d "$LOCAL_DB" -v ON_ERROR_STOP=1 -tAc "$sql"
}

run_local_admin_psql() {
  local user="${1:-$LOCAL_DB_SUPERUSER}"
  local pass="${2:-$LOCAL_DB_SUPERPASS}"
  shift; shift
  docker exec -e PGPASSWORD="$pass" "$LOCAL_CONTAINER" \
    psql -U "$user" -d "$LOCAL_ADMIN_DB" -v ON_ERROR_STOP=1 "$@"
}

run_remote_psql() {
  ssh $REMOTE_SSH_OPTS -p "$REMOTE_SSH_PORT" -i "$REMOTE_SSH_IDENTITY" "$REMOTE_SSH_HOST" \
    "docker exec -e PGPASSWORD='$REMOTE_DB_PASS' $REMOTE_PG_CONTAINER psql -U $REMOTE_DB_USER -d $REMOTE_DB -tAc \"$1\""
}

run_remote_dump() {
  local dump_flag="$1"
  ssh $REMOTE_SSH_OPTS -p "$REMOTE_SSH_PORT" -i "$REMOTE_SSH_IDENTITY" "$REMOTE_SSH_HOST" \
    "docker exec -e PGPASSWORD='$REMOTE_DB_PASS' $REMOTE_PG_CONTAINER pg_dump -U $REMOTE_DB_USER -d $REMOTE_DB $dump_flag --no-owner --no-privileges --format=plain"
}

is_hot_table() {
  local table="$1"
  local t
  for t in "${HOT_TABLES[@]}"; do
    [[ "$t" == "$table" ]] && return 0
  done
  return 1
}

backup_local_db() {
  info "backup local database to $BACKUP_DIR"
  mkdir -p "$BACKUP_DIR"
  docker exec -e PGPASSWORD="$LOCAL_DB_SUPERPASS" "$LOCAL_CONTAINER" \
    pg_dump -U "$LOCAL_DB_SUPERUSER" -d "$LOCAL_DB" -Fc -f "/tmp/${LOCAL_DB}-before-sync.dump"
  docker cp "$LOCAL_CONTAINER:/tmp/${LOCAL_DB}-before-sync.dump" \
    "$BACKUP_DIR/${LOCAL_DB}-before-sync.dump"
  ok "local backup saved"
}

recreate_local_db() {
  info "recreate local database $LOCAL_DB"
  run_local_admin_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='${LOCAL_DB}' AND pid <> pg_backend_pid();" \
    -c "DROP DATABASE IF EXISTS ${LOCAL_DB};" \
    -c "CREATE DATABASE ${LOCAL_DB};" >/dev/null
  ok "local database recreated"
}

replace_local_public_schema() {
  info "drop and recreate local public schema"
  run_local_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;" >/dev/null
  ok "local public schema recreated"
}

truncate_local_public_tables() {
  info "truncate local public tables (skipping hot tables)"
  local all_tables keep_tables=""
  all_tables="$(run_local_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" "select tablename from pg_tables where schemaname='public' order by tablename;" | tr -d ' ')"
  for table in $all_tables; do
    if ! is_hot_table "$table"; then
      keep_tables+="public.$table,"
    fi
  done
  if [[ -n "$keep_tables" ]]; then
    keep_tables="${keep_tables%,}"
    run_local_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" "TRUNCATE TABLE $keep_tables RESTART IDENTITY CASCADE;" >/dev/null
  fi
  ok "local public tables truncated (kept ${#HOT_TABLES[@]} hot tables)"
}

build_exclude_flags() {
  local flag=""
  local table
  for table in "${HOT_TABLES[@]}"; do
    flag+=" --exclude-table=$table"
  done
  printf '%s' "$flag"
}

verify_sync() {
  info "verify schema and key data"

  local remote_table_count local_table_count
  remote_table_count="$(run_remote_psql "select count(*) from information_schema.tables where table_schema='public';" | tr -d '[:space:]')"
  local_table_count="$(run_local_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" "select count(*) from information_schema.tables where table_schema='public';" | tr -d '[:space:]')"

  printf "public_tables remote=%s local=%s\n" "$remote_table_count" "$local_table_count"
  if [[ "$remote_table_count" != "$local_table_count" ]]; then
    err "public table count mismatch"
    return 1
  fi

  local had_warning=0
  local table remote_count local_count diff
  for table in "${KEY_TABLES[@]}"; do
    remote_count="$(run_remote_psql "select count(*) from ${table};" | tr -d '[:space:]')"
    local_count="$(run_local_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" "select count(*) from ${table};" | tr -d '[:space:]')"
    printf "%s remote=%s local=%s\n" "$table" "$remote_count" "$local_count"

    if [[ "$table" == "request_logs" ]]; then
      diff=$(( remote_count - local_count ))
      if (( diff < 0 )); then diff=$(( -diff )); fi
      if (( diff > 0 )); then
        had_warning=1
        printf "request_logs_drift=%s\n" "$diff"
      fi
    elif [[ "$remote_count" != "$local_count" ]]; then
      err "table count mismatch for $table"
      return 1
    fi
  done

  local extnames
  extnames="$(run_local_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" "select extname from pg_extension order by extname;" | tr '\n' ' ' | xargs)"
  printf "local_extensions=%s\n" "$extnames"

  if (( had_warning == 1 )); then
    info "verification passed with hot-table drift warning"
  else
    ok "verification passed"
  fi
}

sync_full() {
  # If the local DB doesn't exist yet (first run), skip the backup step
  # but still create the database.
  if run_local_admin_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" \
       -tAc "SELECT 1 FROM pg_database WHERE datname='${LOCAL_DB}'" | rg -q '^1$'; then
    backup_local_db
  else
    info "local database $LOCAL_DB does not exist yet; skipping pre-sync backup"
  fi
  recreate_local_db
  info "stream remote full dump into local"
  run_remote_dump "--clean --if-exists" | filter_dump_for_legacy_psql | docker exec -i -e PGPASSWORD="$LOCAL_DB_SUPERPASS" "$LOCAL_CONTAINER" \
    psql -U "$LOCAL_DB_SUPERUSER" -d "$LOCAL_DB" -v ON_ERROR_STOP=1 >/dev/null
  ok "full sync completed"
}

sync_schema_only() {
  if run_local_admin_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" \
       -tAc "SELECT 1 FROM pg_database WHERE datname='${LOCAL_DB}'" | rg -q '^1$'; then
    backup_local_db
  else
    info "local database $LOCAL_DB does not exist yet; skipping pre-sync backup"
  fi
  recreate_local_db
  info "stream remote schema-only dump into local"
  run_remote_dump "--schema-only --clean --if-exists" | filter_dump_for_legacy_psql | docker exec -i -e PGPASSWORD="$LOCAL_DB_SUPERPASS" "$LOCAL_CONTAINER" \
    psql -U "$LOCAL_DB_SUPERUSER" -d "$LOCAL_DB" -v ON_ERROR_STOP=1 >/dev/null
  ok "schema-only sync completed"
}

sync_data_only() {
  if run_local_admin_psql "$LOCAL_DB_SUPERUSER" "$LOCAL_DB_SUPERPASS" \
       -tAc "SELECT 1 FROM pg_database WHERE datname='${LOCAL_DB}'" | rg -q '^1$'; then
    backup_local_db
  else
    info "local database $LOCAL_DB does not exist yet; skipping pre-sync backup"
  fi
  truncate_local_public_tables
  local exclude_flags
  exclude_flags="$(build_exclude_flags)"
  info "stream remote data-only dump into local (excluding hot tables)"
  # shellcheck disable=SC2086
  run_remote_dump "--data-only --disable-triggers $exclude_flags" | filter_dump_for_legacy_psql | docker exec -i -e PGPASSWORD="$LOCAL_DB_SUPERPASS" "$LOCAL_CONTAINER" \
    psql -U "$LOCAL_DB_SUPERUSER" -d "$LOCAL_DB" -v ON_ERROR_STOP=1 >/dev/null
  ok "data-only sync completed"
}

main() {
  require_cmd docker
  require_cmd ssh
  require_cmd rg

  if ! docker ps --format '{{.Names}}' | grep -q "^${LOCAL_CONTAINER}$"; then
    err "local postgres container not running: $LOCAL_CONTAINER"
    exit 1
  fi

  case "$MODE" in
    full) sync_full ;;
    schema-only) sync_schema_only ;;
    data-only) sync_data_only ;;
    -h|--help|help) usage; exit 0 ;;
    *) err "unknown mode: $MODE"; usage; exit 1 ;;
  esac

  verify_sync
}

main "$@"
