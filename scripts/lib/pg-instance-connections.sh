#!/usr/bin/env bash
set -euo pipefail
PG_INSTANCE_ROOT="${PG_INSTANCE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
# shellcheck disable=SC1091
source "$PG_INSTANCE_ROOT/scripts/lib/pg-instance-env.sh"

pg_instance_connections_init() {
  local loader
  loader="$(pg_instance_detect_envs_loader)" || {
    echo "ERROR: envs loader not found" >&2
    return 1
  }
  # shellcheck disable=SC1090
  source "$loader" --project llm-gateway-go

  export PG_PASS_LOCAL="${PG_PASS_LOCAL:-${COMMON_PG_SUPERUSER_PASS:?}}"
  # shellcheck disable=SC1091
  source "$PG_INSTANCE_ROOT/configs/env-local.sh"
  LOCAL_PG_CONTAINER="$DOCKER_PG_CONTAINER"
  LOCAL_PG_USER="$PG_USER"
  LOCAL_PG_PASS="$PG_PASS"

  export PG_PASS_252="${PG_PASS_252:-${COMMON_PG_SUPERUSER_PASS:?}}"
  # shellcheck disable=SC1091
  source "$PG_INSTANCE_ROOT/configs/env-252.sh"
  REMOTE_PG_HOST="$PG_HOST"
  REMOTE_PG_TUNNEL_PORT="$PG_PORT"
  REMOTE_PG_USER="$PG_USER"
  REMOTE_PG_PASS="$PG_PASS"
  REMOTE_PSQL_BIN="$PG_PSQL_BIN"
  REMOTE_PG_DUMP_BIN="$PG_DUMP_BIN"
  REMOTE_PG_RESTORE_BIN="${PG_RESTORE_BIN:-$(dirname "$PG_DUMP_BIN")/pg_restore}"
  # shellcheck disable=SC1091
  source "$PG_INSTANCE_ROOT/scripts/lib/252-db-tunnel.sh"
}

pg_instance_tunnel_open() {
  db252_tunnel_ensure
}

pg_instance_tunnel_close() {
  db252_tunnel_teardown
}
pg_instance_local_psql() {
  local database="$1"
  shift
  docker exec -i -e PGPASSWORD="$LOCAL_PG_PASS" \
    -e PGOPTIONS="${PGOPTIONS:-}" "$LOCAL_PG_CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U "$LOCAL_PG_USER" -d "$database" "$@"
}

pg_instance_remote_psql() {
  local database="$1"
  shift
  PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PSQL_BIN" -X -v ON_ERROR_STOP=1 \
    -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
    -U "$REMOTE_PG_USER" -d "$database" "$@"
}

pg_instance_signature() {
  local side="$1" database="$2" schema_regex="$3" output="$4"
  local sql="$PG_INSTANCE_ROOT/scripts/sql/pg-instance-signature.sql"
  local options="" attempt
  [[ -n "$schema_regex" ]] &&
    options="-c pg_instance.schema_regex=$schema_regex"
  [[ -n "${PG_INSTANCE_EXCLUDE_SCHEMA_REGEX:-}" ]] &&
    options="$options -c pg_instance.exclude_schema_regex=$PG_INSTANCE_EXCLUDE_SCHEMA_REGEX"
  for attempt in 1 2 3; do
    if [[ "$side" == "local" ]]; then
      docker exec -i -e PGOPTIONS="$options" -e PGPASSWORD="$LOCAL_PG_PASS" \
        "$LOCAL_PG_CONTAINER" psql -X -v ON_ERROR_STOP=1 \
        -U "$LOCAL_PG_USER" -d "$database" <"$sql" >"$output" && return 0
    else
      pg_instance_tunnel_open || continue
      PGOPTIONS="$options" PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PSQL_BIN" \
        -X -v ON_ERROR_STOP=1 -h "$REMOTE_PG_HOST" \
        -p "$REMOTE_PG_TUNNEL_PORT" -U "$REMOTE_PG_USER" \
        -d "$database" <"$sql" >"$output" && return 0
    fi
    sleep "$attempt"
  done
  echo "ERROR: signature failed after 3 attempts: $side/$database" >&2
  return 1
}

pg_instance_data_signature() {
  local side="$1" database="$2" schema_regex="$3" output="$4"
  local sql="$PG_INSTANCE_ROOT/scripts/sql/pg-instance-data-signature.sql"
  local options=""
  [[ -n "$schema_regex" ]] &&
    options="-c pg_instance.schema_regex=$schema_regex"
  [[ -n "${PG_INSTANCE_EXCLUDE_SCHEMA_REGEX:-}" ]] &&
    options="$options -c pg_instance.exclude_schema_regex=$PG_INSTANCE_EXCLUDE_SCHEMA_REGEX"
  local attempt
  for attempt in 1 2 3; do
    if [[ "$side" == "local" ]]; then
      docker exec -i -e PGOPTIONS="$options" -e PGPASSWORD="$LOCAL_PG_PASS" \
        "$LOCAL_PG_CONTAINER" psql -X -v ON_ERROR_STOP=1 \
        -U "$LOCAL_PG_USER" -d "$database" <"$sql" >"$output" && return 0
    else
      pg_instance_tunnel_open || continue
      PGOPTIONS="$options" PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PSQL_BIN" \
        -X -v ON_ERROR_STOP=1 -h "$REMOTE_PG_HOST" \
        -p "$REMOTE_PG_TUNNEL_PORT" -U "$REMOTE_PG_USER" \
        -d "$database" <"$sql" >"$output" && return 0
    fi
    sleep "$attempt"
  done
  echo "ERROR: data signature failed after 3 attempts: $side/$database" >&2
  return 1
}

pg_instance_data_contract() {
  local side="$1" database="$2" output="$3"
  local sql="$PG_INSTANCE_ROOT/scripts/sql/pg-instance-data-contract.sql"
  local options=""
  [[ -n "${PG_INSTANCE_EXCLUDE_SCHEMA_REGEX:-}" ]] &&
    options="-c pg_instance.exclude_schema_regex=$PG_INSTANCE_EXCLUDE_SCHEMA_REGEX"
  if [[ "$side" == "local" ]]; then
    docker exec -i -e PGOPTIONS="$options" -e PGPASSWORD="$LOCAL_PG_PASS" "$LOCAL_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 -U "$LOCAL_PG_USER" \
      -d "$database" <"$sql" >"$output"
  else
    PGOPTIONS="$options" PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PSQL_BIN" \
      -X -v ON_ERROR_STOP=1 -h "$REMOTE_PG_HOST" \
      -p "$REMOTE_PG_TUNNEL_PORT" -U "$REMOTE_PG_USER" \
      -d "$database" <"$sql" >"$output"
  fi
}

pg_instance_dump() {
  local side="$1" database="$2" mode="$3" output="$4"
  local args=(-Fc --no-owner --no-privileges)
  [[ "$mode" == "schema-only" ]] && args+=(--schema-only)
  if [[ "$side" == "local" ]]; then
    docker exec -e PGPASSWORD="$LOCAL_PG_PASS" "$LOCAL_PG_CONTAINER" \
      pg_dump -U "$LOCAL_PG_USER" -d "$database" "${args[@]}" >"$output"
  else
    PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PG_DUMP_BIN" \
      -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
      -U "$REMOTE_PG_USER" -d "$database" "${args[@]}" >"$output"
  fi
  [[ -s "$output" ]] || {
    echo "ERROR: empty backup: $side/$database" >&2
    return 1
  }
}

pg_instance_database_exists() {
  local side="$1" database="$2"
  local sql="SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=:'db')"
  if [[ "$side" == "local" ]]; then
    [[ "$(printf '%s\n' "$sql" |
      pg_instance_local_psql postgres -At -v db="$database")" == "t" ]]
  else
    [[ "$(printf '%s\n' "$sql" |
      pg_instance_remote_psql postgres -At -v db="$database")" == "t" ]]
  fi
}

pg_instance_create_database() {
  local side="$1" database="$2" owner="$3" collate="$4" ctype="$5"
  local query create_sql
  query="SELECT format('CREATE DATABASE %I OWNER %I ENCODING ''UTF8'' LC_COLLATE %L LC_CTYPE %L TEMPLATE template0', :'db', :'owner', :'collate', :'ctype')"
  if [[ "$side" == "local" ]]; then
    create_sql="$(printf '%s\n' "$query" | pg_instance_local_psql postgres -At \
      -v db="$database" -v owner="$owner" -v collate="$collate" -v ctype="$ctype")"
    pg_instance_local_psql postgres -c "$create_sql"
  else
    create_sql="$(printf '%s\n' "$query" | pg_instance_remote_psql postgres -At \
      -v db="$database" -v owner="$owner" -v collate="$collate" -v ctype="$ctype")"
    pg_instance_remote_psql postgres -c "$create_sql"
  fi
}

pg_instance_restore() {
  local side="$1" database="$2" owner="$3" dump="$4"
  if [[ "$side" == "local" ]]; then
    docker exec -i -e PGPASSWORD="$LOCAL_PG_PASS" "$LOCAL_PG_CONTAINER" \
      pg_restore --exit-on-error --no-owner --no-privileges --role="$owner" \
      -U "$LOCAL_PG_USER" -d "$database" <"$dump"
  else
    PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PG_RESTORE_BIN" \
      --exit-on-error --no-owner --no-privileges --role="$owner" \
      -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
      -U "$REMOTE_PG_USER" -d "$database" "$dump"
  fi
}

# shellcheck disable=SC1091
source "$PG_INSTANCE_ROOT/scripts/lib/pg-instance-write.sh"
