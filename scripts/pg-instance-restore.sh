#!/usr/bin/env bash
# Restore a verified backup into a NEW database. Never overwrites an existing DB.
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest=""
manifest_hash=""
policy=""
backup_index=""
source_side=""
source_db=""
target_side=""
target_db=""
yes=false
fresh=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) manifest="${2:-}"; shift 2 ;;
    --manifest-hash) manifest_hash="${2:-}"; shift 2 ;;
    --policy) policy="${2:-}"; shift 2 ;;
    --backup-index) backup_index="${2:-}"; shift 2 ;;
    --source-side) source_side="${2:-}"; shift 2 ;;
    --source-database) source_db="${2:-}"; shift 2 ;;
    --target-side) target_side="${2:-}"; shift 2 ;;
    --target-database) target_db="${2:-}"; shift 2 ;;
    --yes) yes=true; shift ;;
    --freshness-check) fresh=true; shift ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ "$yes" == true && "$fresh" == true ]] || die "--yes and --freshness-check are required"
[[ -r "$manifest" && -r "$policy" && -r "$backup_index" ]] ||
  die "manifest, policy, and backup index must be readable"
[[ "$source_side" == "local" || "$source_side" == "remote" ]] ||
  die "--source-side must be local or remote"
[[ "$target_side" == "local" || "$target_side" == "remote" ]] ||
  die "--target-side must be local or remote"
[[ -n "$source_db" && -n "$target_db" ]] ||
  die "--source-database and --target-database are required"
[[ "$target_db" != "llm_gateway" ]] ||
  die "refusing to restore into llm_gateway"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-guardrails.sh"
validate_manifest_freshness "$manifest" "$manifest_hash" "$policy"
DATABASE_OWNER_MAP=""
# shellcheck disable=SC1090
source "$policy"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-connections.sh"
pg_instance_connections_init
pg_instance_tunnel_open
trap pg_instance_tunnel_close EXIT

record="$(awk -F'\t' -v s="$source_side" -v d="$source_db" \
  'NR>1 && $1==s && $2==d {print $3 "|" $4 "|" $5; exit}' "$backup_index")"
[[ -n "$record" ]] || die "backup not found: $source_side/$source_db"
mode="${record%%|*}"
rest="${record#*|}"
dump="${rest%%|*}"
expected="${rest#*|}"
actual="$(shasum -a 256 "$dump" | awk '{print $1}')"
[[ "$actual" == "$expected" ]] || die "backup checksum mismatch: $dump"
[[ "$source_db" != "llm_gateway" || "$mode" == "schema-only" ]] ||
  die "llm_gateway restore is limited to schema-only dumps"

pg_instance_database_exists "$target_side" "$target_db" &&
  die "target already exists; rollback creates a new database only: $target_side/$target_db"

query="SELECT pg_get_userbyid(datdba),datcollate,datctype FROM pg_database WHERE datname=:'db'"
if [[ "$source_side" == "local" ]]; then
  metadata="$(printf '%s\n' "$query" |
    pg_instance_local_psql postgres -AtF '|' -v db="$source_db")"
else
  metadata="$(printf '%s\n' "$query" |
    pg_instance_remote_psql postgres -AtF '|' -v db="$source_db")"
fi
IFS='|' read -r owner collate ctype <<<"$metadata"
for mapping in $DATABASE_OWNER_MAP; do
  [[ "${mapping%%=*}" == "$target_db" ]] || continue
  owner="${mapping#*=}"
done
role_sql="SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=:'owner')"
if [[ "$target_side" == "local" ]]; then
  [[ "$(printf '%s\n' "$role_sql" |
    pg_instance_local_psql postgres -At -v owner="$owner")" == "t" ]] ||
    die "mapped owner role missing: $owner"
else
  [[ "$(printf '%s\n' "$role_sql" |
    pg_instance_remote_psql postgres -At -v owner="$owner")" == "t" ]] ||
    die "mapped owner role missing: $owner"
fi

printf 'restoring %s/%s -> %s/%s from %s\n' \
  "$source_side" "$source_db" "$target_side" "$target_db" "$dump" >&2
pg_instance_create_database "$target_side" "$target_db" "$owner" "$collate" "$ctype"
pg_instance_restore "$target_side" "$target_db" "$owner" "$dump"
printf 'restore_ok=%s/%s\n' "$target_side" "$target_db"
