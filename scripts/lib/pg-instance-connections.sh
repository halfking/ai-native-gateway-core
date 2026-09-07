#!/usr/bin/env bash
set -euo pipefail
PG_INSTANCE_ROOT="${PG_INSTANCE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

pg_instance_detect_envs_loader() {
  local cursor="$PG_INSTANCE_ROOT"
  while [[ "$cursor" != "/" ]]; do
    if [[ "$(basename "$cursor")" == "ai-native-tools" &&
      -r "$cursor/envs/loader.sh" ]]; then
      printf '%s\n' "$cursor/envs/loader.sh"
      return 0
    fi
    cursor="$(dirname "$cursor")"
  done
  return 1
}

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

pg_instance_local_data_dump() {
  local database="$1" output="$2"
  shift 2
  local exclusions=() table
  for table in "$@"; do
    exclusions+=("--exclude-table-data=$table")
  done
  docker exec -e PGPASSWORD="$LOCAL_PG_PASS" "$LOCAL_PG_CONTAINER" \
    pg_dump -U "$LOCAL_PG_USER" -d "$database" --data-only \
    --column-inserts --rows-per-insert=1000 --on-conflict-do-nothing \
    --no-owner --no-privileges "${exclusions[@]}" >"$output"
  [[ -s "$output" ]] || {
    echo "ERROR: empty data dump: $database" >&2
    return 1
  }
}

pg_instance_remote_apply_file() {
  local database="$1" file="$2"
  PGOPTIONS='-c statement_timeout=0' PGPASSWORD="$REMOTE_PG_PASS" \
    "$REMOTE_PSQL_BIN" -X -v ON_ERROR_STOP=1 --single-transaction \
    -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
    -U "$REMOTE_PG_USER" -d "$database" -f "$file"
}

pg_instance_remote_sequence_floor() {
  local database="$1"
  local sql="$PG_INSTANCE_ROOT/scripts/sql/pg-instance-sequence-floor.sql"
  PGOPTIONS='-c statement_timeout=0' PGPASSWORD="$REMOTE_PG_PASS" \
    "$REMOTE_PSQL_BIN" -X -v ON_ERROR_STOP=1 --single-transaction \
    -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
    -U "$REMOTE_PG_USER" -d "$database" -f "$sql"
}

pg_instance_schema_dump_tables() {
  local side="$1" database="$2" output="$3"
  shift 3
  local tables=() table
  for table in "$@"; do tables+=("--table=$table"); done
  (( ${#tables[@]} > 0 )) || return 0
  if [[ "$side" == "local" ]]; then
    docker exec -e PGPASSWORD="$LOCAL_PG_PASS" "$LOCAL_PG_CONTAINER" \
      pg_dump -Fc --schema-only --no-owner --no-privileges \
      -U "$LOCAL_PG_USER" -d "$database" "${tables[@]}" >"$output"
  else
    PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PG_DUMP_BIN" \
      -Fc --schema-only --no-owner --no-privileges \
      -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
      -U "$REMOTE_PG_USER" -d "$database" "${tables[@]}" >"$output"
  fi
  [[ -s "$output" ]]
}

pg_instance_apply_sql_file() {
  local side="$1" database="$2" file="$3"
  if [[ "$side" == "local" ]]; then
    docker exec -i -e PGPASSWORD="$LOCAL_PG_PASS" "$LOCAL_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 --single-transaction \
      -U "$LOCAL_PG_USER" -d "$database" <"$file"
  else
    PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PSQL_BIN" \
      -X -v ON_ERROR_STOP=1 --single-transaction \
      -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
      -U "$REMOTE_PG_USER" -d "$database" -f "$file"
  fi
}

pg_instance_restore_schema() {
  local side="$1" database="$2" archive="$3" section="$4"
  local event_trigger="${5:-}"
  if [[ -n "$event_trigger" ]]; then
    [[ "$side" == "remote" && "$section" == "pre-data" &&
      "$event_trigger" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || {
      echo "ERROR: event-trigger wrapper only supports remote pre-data" >&2
      return 1
    }
    {
      printf 'BEGIN;\nALTER EVENT TRIGGER %s DISABLE;\n' "$event_trigger"
      # EOF before COMMIT rolls back both DDL and the trigger disable.
      "$REMOTE_PG_RESTORE_BIN" --exit-on-error --section="$section" \
        --no-owner --no-privileges -f - "$archive"
      printf 'ALTER EVENT TRIGGER %s ENABLE;\nCOMMIT;\n' "$event_trigger"
    } | PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PSQL_BIN" \
      -X -v ON_ERROR_STOP=1 -h "$REMOTE_PG_HOST" \
      -p "$REMOTE_PG_TUNNEL_PORT" -U "$REMOTE_PG_USER" -d "$database"
    return
  fi
  if [[ "$side" == "local" ]]; then
    docker exec -i -e PGPASSWORD="$LOCAL_PG_PASS" "$LOCAL_PG_CONTAINER" \
      pg_restore --exit-on-error --single-transaction \
      --section="$section" --no-owner --no-privileges -U "$LOCAL_PG_USER" \
      -d "$database" <"$archive"
  else
    PGPASSWORD="$REMOTE_PG_PASS" "$REMOTE_PG_RESTORE_BIN" \
      --exit-on-error --single-transaction --section="$section" \
      --no-owner --no-privileges \
      -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
      -U "$REMOTE_PG_USER" -d "$database" "$archive"
  fi
}
