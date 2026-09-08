#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest=""
manifest_hash=""
policy=""
backup_index=""
output_dir=""
yes=false
fresh=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) manifest="${2:-}"; shift 2 ;;
    --manifest-hash) manifest_hash="${2:-}"; shift 2 ;;
    --policy) policy="${2:-}"; shift 2 ;;
    --backup-index) backup_index="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    --yes) yes=true; shift ;;
    --freshness-check) fresh=true; shift ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ "$yes" == true ]] || die "--yes is required"
[[ "$fresh" == true ]] || die "--freshness-check is required"
[[ -r "$manifest" && -r "$policy" && -r "$backup_index" ]] ||
  die "manifest, policy, and backup index must be readable"
[[ -n "$output_dir" ]] || die "--output-dir is required"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-guardrails.sh"
validate_manifest_freshness "$manifest" "$manifest_hash" "$policy"
validate_llm_gateway_protection bootstrap "$manifest"
DATABASE_OWNER_MAP=""
# shellcheck disable=SC1090
source "$policy"
mkdir -p "$output_dir"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-connections.sh"
pg_instance_connections_init
pg_instance_tunnel_open
trap pg_instance_tunnel_close EXIT

owner_for() {
  local database="$1" source_owner="$2" mapping
  for mapping in $DATABASE_OWNER_MAP; do
    [[ "${mapping%%=*}" == "$database" ]] || continue
    printf '%s\n' "${mapping#*=}"
    return
  done
  printf '%s\n' "$source_owner"
}

source_metadata() {
  local side="$1" database="$2"
  local query="SELECT pg_get_userbyid(datdba),datcollate,datctype FROM pg_database WHERE datname=:'db'"
  if [[ "$side" == "local" ]]; then
    printf '%s\n' "$query" |
      pg_instance_local_psql postgres -AtF '|' -v db="$database"
  else
    printf '%s\n' "$query" |
      pg_instance_remote_psql postgres -AtF '|' -v db="$database"
  fi
}

target_role_exists() {
  local side="$1" owner="$2"
  local query="SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=:'owner')"
  if [[ "$side" == "local" ]]; then
    [[ "$(printf '%s\n' "$query" |
      pg_instance_local_psql postgres -At -v owner="$owner")" == "t" ]]
  else
    [[ "$(printf '%s\n' "$query" |
      pg_instance_remote_psql postgres -At -v owner="$owner")" == "t" ]]
  fi
}

backup_for() {
  local side="$1" database="$2"
  awk -F'\t' -v s="$side" -v d="$database" \
    'NR>1 && $1==s && $2==d {print $4 "|" $5; exit}' "$backup_index"
}

bootstrap_one() {
  local source_side="$1" target_side="$2" source_db="$3" target_db="$4"
  local metadata source_owner collate ctype owner backup_record dump expected actual
  pg_instance_database_exists "$target_side" "$target_db" &&
    die "target database already exists; refusing bootstrap: $target_side/$target_db"
  metadata="$(source_metadata "$source_side" "$source_db")"
  IFS='|' read -r source_owner collate ctype <<<"$metadata"
  owner="$(owner_for "$target_db" "$source_owner")"
  target_role_exists "$target_side" "$owner" ||
    die "mapped owner role missing on target: $owner"
  backup_record="$(backup_for "$source_side" "$source_db")"
  [[ -n "$backup_record" ]] || die "backup not found: $source_side/$source_db"
  dump="${backup_record%%|*}"
  expected="${backup_record#*|}"
  actual="$(shasum -a 256 "$dump" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || die "backup checksum mismatch: $dump"

  printf 'creating %s/%s from %s/%s\n' \
    "$target_side" "$target_db" "$source_side" "$source_db" >&2
  pg_instance_create_database "$target_side" "$target_db" "$owner" "$collate" "$ctype"
  pg_instance_restore "$target_side" "$target_db" "$owner" "$dump"
  printf '%s\t%s\t%s\t%s\t%s\n' \
    "$source_side" "$source_db" "$target_side" "$target_db" "$actual" \
    >>"$output_dir/bootstrap-index.tsv"
}

printf 'source_side\tsource_database\ttarget_side\ttarget_database\tbackup_sha256\n' \
  >"$output_dir/bootstrap-index.tsv"
while IFS=$'\t' read -r classification local_db remote_db _mode; do
  [[ "$classification" == \#* || "$classification" == "classification" ]] && continue
  case "$classification" in
    LOCAL_ONLY) bootstrap_one local remote "$local_db" "$local_db" ;;
    REMOTE_ONLY) bootstrap_one remote local "$remote_db" "$remote_db" ;;
  esac
done <"$manifest"
printf 'bootstrap_index=%s\n' "$output_dir/bootstrap-index.tsv"
