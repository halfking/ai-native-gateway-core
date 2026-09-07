#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest=""
manifest_hash=""
policy=""
output_dir=""
yes=false
fresh=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) manifest="${2:-}"; shift 2 ;;
    --manifest-hash) manifest_hash="${2:-}"; shift 2 ;;
    --policy) policy="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    --yes) yes=true; shift ;;
    --freshness-check) fresh=true; shift ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ "$yes" == true ]] || die "--yes is required"
[[ "$fresh" == true ]] || die "--freshness-check is required"
[[ -r "$manifest" ]] || die "readable --manifest is required"
[[ -r "$policy" ]] || die "readable --policy is required"
[[ -n "$output_dir" ]] || die "--output-dir is required"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-guardrails.sh"
validate_manifest_freshness "$manifest" "$manifest_hash" "$policy"
mkdir -p "$output_dir"

# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-connections.sh"
pg_instance_connections_init
pg_instance_tunnel_open
trap pg_instance_tunnel_close EXIT

backup_index="$output_dir/backup-index.tsv"
printf 'side\tdatabase\tmode\tfile\tsha256\tbytes\n' >"$backup_index"

backup_one() {
  local side="$1" database="$2" mode="$3"
  local file="$output_dir/$side-$database.dump"
  printf 'backing up %s/%s (%s)\n' "$side" "$database" "$mode" >&2
  pg_instance_dump "$side" "$database" "$mode" "$file"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$side" "$database" "$mode" "$file" \
    "$(shasum -a 256 "$file" | awk '{print $1}')" \
    "$(wc -c <"$file" | tr -d ' ')" >>"$backup_index"
}

while IFS=$'\t' read -r classification local_db remote_db mode; do
  [[ "$classification" == \#* || "$classification" == "classification" ]] && continue
  case "$classification" in
    COMMON|ALIAS)
      dump_mode="full"
      [[ "$mode" == "SCHEMA_ONLY" ]] && dump_mode="schema-only"
      backup_one local "$local_db" "$dump_mode"
      backup_one remote "$remote_db" "$dump_mode"
      ;;
    LOCAL_ONLY) backup_one local "$local_db" full ;;
    REMOTE_ONLY) backup_one remote "$remote_db" full ;;
    EXCLUDED) : ;;
    *) die "unsupported classification: $classification" ;;
  esac
done <"$manifest"

cp "$manifest" "$output_dir/manifest.tsv"
printf '%s  manifest.tsv\n' "$manifest_hash" >"$output_dir/manifest.sha256"
printf 'backup_index=%s\n' "$backup_index"
