#!/usr/bin/env bash
# Write-side helpers. Requires pg-instance-connections.sh to be sourced first.
set -euo pipefail

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
  PGOPTIONS="-c statement_timeout=${DATA_APPLY_TIMEOUT_MS:-1800000} -c lock_timeout=${DATA_LOCK_TIMEOUT_MS:-10000}" \
    PGPASSWORD="$REMOTE_PG_PASS" \
    "$REMOTE_PSQL_BIN" -X -v ON_ERROR_STOP=1 --single-transaction \
    -h "$REMOTE_PG_HOST" -p "$REMOTE_PG_TUNNEL_PORT" \
    -U "$REMOTE_PG_USER" -d "$database" -f "$file"
}

pg_instance_remote_sequence_floor() {
  local database="$1"
  local sql="$PG_INSTANCE_ROOT/scripts/sql/pg-instance-sequence-floor.sql"
  PGOPTIONS="-c statement_timeout=${DATA_APPLY_TIMEOUT_MS:-1800000} -c lock_timeout=${DATA_LOCK_TIMEOUT_MS:-10000}" \
    PGPASSWORD="$REMOTE_PG_PASS" \
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
