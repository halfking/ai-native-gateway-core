#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
local_inventory=""
remote_inventory=""
policy_file=""
manifest_file=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --local-inventory) local_inventory="${2:-}"; shift 2 ;;
    --remote-inventory) remote_inventory="${2:-}"; shift 2 ;;
    --policy) policy_file="${2:-}"; shift 2 ;;
    --manifest) manifest_file="${2:-}"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done
for required in "$local_inventory" "$remote_inventory" "$policy_file"; do
  [[ -n "$required" && -r "$required" ]] || die "required readable file missing: ${required:-unset}"
done
[[ -n "$manifest_file" && -d "$(dirname "$manifest_file")" &&
  -w "$(dirname "$manifest_file")" ]] || die "manifest directory is not writable"

EXCLUDE_DB_REGEX=""
SCHEMA_ONLY_DBS=""
DB_ALIASES=""
LOCAL_ONLY_ALLOWLIST=""
# shellcheck disable=SC1090
source "$policy_file"
[[ -n "$EXCLUDE_DB_REGEX" ]] || die "EXCLUDE_DB_REGEX is required"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
LC_ALL=C sort -u "$local_inventory" | sed '/^[[:space:]]*$/d' >"$tmp/local"
LC_ALL=C sort -u "$remote_inventory" | sed '/^[[:space:]]*$/d' >"$tmp/remote"
: >"$tmp/alias-local"
: >"$tmp/alias-remote"
: >"$tmp/records"

mode_for() {
  local database="$1" item
  for item in $SCHEMA_ONLY_DBS; do
    [[ "$database" == "$item" ]] && { printf 'SCHEMA_ONLY'; return; }
  done
  printf 'SCHEMA_AND_INSERT_ONLY'
}

is_allowlisted() {
  local database="$1" item
  for item in $LOCAL_ONLY_ALLOWLIST; do
    [[ "$database" == "$item" ]] && return 0
  done
  return 1
}

for mapping in $DB_ALIASES; do
  local_name="${mapping%%=*}"
  remote_name="${mapping#*=}"
  [[ "$local_name" != "$mapping" && -n "$local_name" && -n "$remote_name" ]] ||
    die "invalid alias: $mapping"
  if grep -Fxq "$local_name" "$tmp/local" && grep -Fxq "$remote_name" "$tmp/remote"; then
    printf '%s\n' "$local_name" >>"$tmp/alias-local"
    printf '%s\n' "$remote_name" >>"$tmp/alias-remote"
    printf 'ALIAS\t%s\t%s\t%s\n' \
      "$local_name" "$remote_name" "$(mode_for "$local_name")" >>"$tmp/records"
  fi
done

while IFS= read -r database; do
  if [[ "$database" =~ $EXCLUDE_DB_REGEX ]]; then
    printf 'EXCLUDED\t%s\t-\tNAME_POLICY\n' "$database" >>"$tmp/records"
  elif grep -Fxq "$database" "$tmp/alias-local"; then
    continue
  elif grep -Fxq "$database" "$tmp/remote"; then
    printf 'COMMON\t%s\t%s\t%s\n' \
      "$database" "$database" "$(mode_for "$database")" >>"$tmp/records"
  elif is_allowlisted "$database"; then
    printf 'LOCAL_ONLY\t%s\t-\tCREATE_REMOTE_AND_INSERT_ONLY\n' \
      "$database" >>"$tmp/records"
  else
    printf 'EXCLUDED\t%s\t-\tNOT_ALLOWLISTED\n' \
      "$database" >>"$tmp/records"
  fi
done <"$tmp/local"

while IFS= read -r database; do
  if grep -Fxq "$database" "$tmp/alias-remote" ||
    grep -Fxq "$database" "$tmp/local"; then
    continue
  fi
  printf 'REMOTE_ONLY\t-\t%s\tBOOTSTRAP_LOCAL_FULL\n' \
    "$database" >>"$tmp/records"
done <"$tmp/remote"

{
  generated_epoch=0
  for input in "$local_inventory" "$remote_inventory" "$policy_file"; do
    input_epoch="$(stat -f %m "$input" 2>/dev/null || stat -c %Y "$input")"
    (( input_epoch > generated_epoch )) && generated_epoch="$input_epoch"
  done
  local_inventory_path="$(cd "$(dirname "$local_inventory")" && pwd)/$(basename "$local_inventory")"
  remote_inventory_path="$(cd "$(dirname "$remote_inventory")" && pwd)/$(basename "$remote_inventory")"
  policy_path="$(cd "$(dirname "$policy_file")" && pwd)/$(basename "$policy_file")"
  printf '#generated_epoch\t%s\n' "$generated_epoch"
  printf '#local_inventory_file\t%s\n' "$local_inventory_path"
  printf '#local_inventory_sha256\t%s\n' \
    "$(shasum -a 256 "$tmp/local" | awk '{print $1}')"
  printf '#remote_inventory_file\t%s\n' "$remote_inventory_path"
  printf '#remote_inventory_sha256\t%s\n' \
    "$(shasum -a 256 "$tmp/remote" | awk '{print $1}')"
  printf '#policy_file\t%s\n' "$policy_path"
  printf '#policy_sha256\t%s\n' \
    "$(shasum -a 256 "$policy_file" | awk '{print $1}')"
  printf 'classification\tlocal_database\tremote_database\tmode\n'
  LC_ALL=C sort "$tmp/records"
} >"$manifest_file"
printf 'manifest: %s\n' "$manifest_file"
printf 'manifest_hash: %s\n' \
  "$(shasum -a 256 "$manifest_file" | awk '{print $1}')"
