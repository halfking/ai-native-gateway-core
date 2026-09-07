#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest=""
manifest_hash=""
policy=""
impact=""
work_dir=""
yes=false
fresh=false
dry_run=false
database_filter=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) manifest="${2:-}"; shift 2 ;;
    --manifest-hash) manifest_hash="${2:-}"; shift 2 ;;
    --policy) policy="${2:-}"; shift 2 ;;
    --impact-matrix) impact="${2:-}"; shift 2 ;;
    --work-dir) work_dir="${2:-}"; shift 2 ;;
    --yes) yes=true; shift ;;
    --freshness-check) fresh=true; shift ;;
    --dry-run) dry_run=true; shift ;;
    --database) database_filter="${2:-}"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ "$yes" == true ]] || die "--yes is required"
[[ "$fresh" == true ]] || die "--freshness-check is required"
[[ -r "$manifest" && -r "$policy" && -r "$impact" ]] ||
  die "manifest, policy, and impact matrix must be readable"
[[ -n "$work_dir" ]] || die "--work-dir is required"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-guardrails.sh"
validate_manifest_freshness "$manifest" "$manifest_hash" "$policy"
validate_llm_gateway_protection apply-data "$manifest"
EXCLUDE_SCHEMA_REGEX=""
# shellcheck disable=SC1090
source "$policy"
export PG_INSTANCE_EXCLUDE_SCHEMA_REGEX="$EXCLUDE_SCHEMA_REGEX"
mkdir -p "$work_dir"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-connections.sh"
pg_instance_connections_init
if [[ "$dry_run" == false ]]; then
  if ! docker inspect -f '{{.State.Running}}' "$LOCAL_PG_CONTAINER" |
    grep -qx true; then
    die "local PostgreSQL container is not running: $LOCAL_PG_CONTAINER"
  elif ! docker exec "$LOCAL_PG_CONTAINER" pg_dump --help |
    grep -Fq -- '--on-conflict-do-nothing'; then
    die "local pg_dump lacks --on-conflict-do-nothing support"
  fi
fi
pg_instance_tunnel_open
trap pg_instance_tunnel_close EXIT

keyless_nonempty_list() {
  local database="$1" output="$2"
  local sql="$ROOT/scripts/sql/pg-instance-keyless-nonempty.sql"
  PGOPTIONS="-c pg_instance.exclude_schema_regex=$EXCLUDE_SCHEMA_REGEX" \
    pg_instance_local_psql "$database" <"$sql" | sed '/^$/d' >"$output"
}

report="$work_dir/data-merge.tsv"
printf 'local_database\tremote_database\tstatus\tdump_bytes\n' >"$report"
while IFS=$'\t' read -r classification local_db remote_db mode; do
  [[ "$classification" == \#* || "$classification" == "classification" ]] && continue
  [[ "$classification" == "COMMON" || "$classification" == "ALIAS" ]] || continue
  [[ -z "$database_filter" || "$local_db" == "$database_filter" ]] || continue
  if [[ "$mode" == "SCHEMA_ONLY" || "$local_db" == "llm_gateway" ]]; then
    printf '%s\t%s\tSKIPPED_SCHEMA_ONLY\t0\n' "$local_db" "$remote_db" >>"$report"
    continue
  fi
  local_contract="$work_dir/$local_db.local-contract"
  remote_contract="$work_dir/$local_db.remote-contract"
  pg_instance_data_contract local "$local_db" "$local_contract"
  pg_instance_data_contract remote "$remote_db" "$remote_contract"
  if ! cmp -s "$local_contract" "$remote_contract"; then
    printf '%s\t%s\tSKIPPED_SCHEMA_DRIFT\t0\n' "$local_db" "$remote_db" >>"$report"
    continue
  fi
  keyless_file="$work_dir/$local_db.keyless"
  keyless_nonempty_list "$local_db" "$keyless_file"
  exclusions=()
  if [[ -s "$keyless_file" ]]; then
    local_data="$work_dir/$local_db.local-data"
    remote_data="$work_dir/$local_db.remote-data"
    pg_instance_data_signature local "$local_db" "" "$local_data"
    pg_instance_data_signature remote "$remote_db" "" "$remote_data"
    keyless_drift=false
    while IFS= read -r table; do
      local_row="$(awk -F'|' -v name="$table" '$1==name {print; exit}' "$local_data")"
      remote_row="$(awk -F'|' -v name="$table" '$1==name {print; exit}' "$remote_data")"
      if [[ -z "$local_row" || "$local_row" != "$remote_row" ]]; then
        keyless_drift=true
        break
      fi
      exclusions+=("$table")
    done <"$keyless_file"
    if [[ "$keyless_drift" == true ]]; then
      printf '%s\t%s\tBLOCKED_KEYLESS_DRIFT\t0\n' \
        "$local_db" "$remote_db" >>"$report"
      continue
    fi
  fi
  if [[ "$dry_run" == true ]]; then
    printf '%s\t%s\tELIGIBLE_INSERT_ONLY\t0\n' "$local_db" "$remote_db" >>"$report"
    continue
  fi

  raw="$work_dir/$local_db.raw.sql"
  safe="$work_dir/$local_db.insert-only.sql"
  printf 'exporting insert-only data: %s -> %s\n' "$local_db" "$remote_db" >&2
  pg_instance_local_data_dump "$local_db" "$raw" "${exclusions[@]}"
  awk '!/^SELECT pg_catalog\.setval\(/' "$raw" >"$safe"
  if grep -Eni \
    '^(UPDATE|DELETE|TRUNCATE|DROP)[[:space:]].*;[[:space:]]*$|session_replication_role' \
    "$safe" >/dev/null; then
    die "forbidden statement generated for $local_db"
  fi
  pg_instance_remote_apply_file "$remote_db" "$safe"
  pg_instance_remote_sequence_floor "$remote_db"
  printf '%s\t%s\tAPPLIED_INSERT_ONLY\t%s\n' \
    "$local_db" "$remote_db" "$(wc -c <"$safe" | tr -d ' ')" >>"$report"
done <"$manifest"
printf 'data_merge_report=%s\n' "$report"
