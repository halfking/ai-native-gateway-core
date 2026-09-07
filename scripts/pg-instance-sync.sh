#!/usr/bin/env bash
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 2; }
usage() {
  cat <<'EOF'
Usage:
  pg-instance-sync.sh <command> [options]

Commands:
  inventory        List databases from local/remote instances (read-only)
  plan             Create deterministic sync manifest (read-only)
  backup           Create backup before sync (requires --yes)
  apply-schema     Apply schema changes (requires --yes, --manifest-hash, --freshness-check)
  apply-data       Apply data sync (requires --yes, --manifest-hash, --freshness-check)
  verify           Verify sync results (read-only)
  all              Execute full sync pipeline (requires --yes)

Options for plan:
  --local-inventory FILE    Local database list
  --remote-inventory FILE   Remote database list  
  --policy FILE             Sync policy configuration
  --manifest FILE           Output manifest file

Options for inventory:
  --output-dir DIR          Directory for output files

Options for write commands:
  --yes                     Required for all write operations
  --manifest-hash HASH      Required manifest hash for safety
  --freshness-check         Verify manifest freshness

Examples:
  pg-instance-sync.sh plan --local-inventory local.tsv --remote-inventory remote.tsv --policy policy.conf --manifest manifest.tsv
  pg-instance-sync.sh inventory --output-dir /tmp/pg-sync-$(date +%Y%m%d)
  pg-instance-sync.sh apply-schema --yes --manifest manifest.tsv --manifest-hash abc123 --freshness-check

Default behavior: plan and inventory are read-only, all others require explicit confirmation.
EOF
}

[[ $# -gt 0 ]] || { usage; exit 2; }

# Handle help first
if [[ "$1" == "-h" || "$1" == "--help" ]]; then
  usage
  exit 0
fi

command_name="$1"
shift

# Command-specific variables
local_inventory=""
remote_inventory=""
policy_file=""
manifest_file=""
output_dir=""
# Parsed now so the public CLI remains stable while writer commands fail closed.
# shellcheck disable=SC2034
yes_flag=false
# shellcheck disable=SC2034
manifest_hash=""
# shellcheck disable=SC2034
freshness_check=false

# Writer flags are accepted for forward-compatible fail-closed commands.
# shellcheck disable=SC2034
while [[ $# -gt 0 ]]; do
  case "$1" in
    --local-inventory) local_inventory="${2:-}"; shift 2 ;;
    --remote-inventory) remote_inventory="${2:-}"; shift 2 ;;
    --policy) policy_file="${2:-}"; shift 2 ;;
    --manifest) manifest_file="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    --yes) yes_flag=true; shift ;;
    --manifest-hash) manifest_hash="${2:-}"; shift 2 ;;
    --freshness-check) freshness_check=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

# Validate command
case "$command_name" in
  plan|inventory|backup|apply-schema|apply-data|verify|all)
    # Valid commands
    ;;
  *)
    die "unsupported command: $command_name"
    ;;
esac
# Command validation only - execution handled later
case "$command_name" in
  plan)
    # Plan command validation
    for required in "$local_inventory" "$remote_inventory" "$policy_file"; do
      [[ -n "$required" && -r "$required" ]] || die "required readable file missing: ${required:-unset}"
    done
    [[ -n "$manifest_file" && -d "$(dirname "$manifest_file")" &&
      -w "$(dirname "$manifest_file")" ]] || die "manifest directory is not writable"
    ;;
  inventory)
    # Inventory is read-only, no --yes required
    [[ -n "$output_dir" ]] || die "inventory command requires --output-dir"
    ;;
  backup)
    [[ "$yes_flag" == true ]] || die "backup requires --yes"
    [[ "$freshness_check" == true ]] || die "backup requires --freshness-check"
    [[ -n "$manifest_hash" ]] || die "backup requires --manifest-hash"
    [[ -r "$manifest_file" ]] || die "backup requires readable --manifest"
    [[ -r "$policy_file" ]] || die "backup requires readable --policy"
    [[ -n "$output_dir" ]] || die "backup requires --output-dir"
    ;;
  apply-schema|apply-data|all)
    # These commands are not implemented - fail immediately.
    echo "ERROR: $command_name command not implemented" >&2
    exit 1
    ;;
  verify)
    # Verify is read-only, no special requirements
    ;;
esac

# Source library functions
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-guardrails.sh"

# Execute commands
case "$command_name" in
  backup)
    exec bash "$ROOT/scripts/pg-instance-backup.sh" \
      --manifest "$manifest_file" --manifest-hash "$manifest_hash" \
      --policy "$policy_file" --output-dir "$output_dir" \
      --yes --freshness-check
    ;;
  inventory)
    echo "Collecting database inventory..." >&2
    # shellcheck disable=SC1091
    source "$ROOT/scripts/lib/pg-instance-inventory.sh"
    
    # Create timestamped output directory
    mkdir -p "$output_dir"
    
    local_inv_file="$output_dir/local-inventory.tsv"
    remote_inv_file="$output_dir/remote-inventory.tsv"
    
    echo "Collecting local inventory..." >&2
    get_database_list "local" "$local_inv_file"
    echo "Local databases written to: $local_inv_file" >&2
    
    echo "Collecting remote inventory..." >&2
    get_database_list "remote" "$remote_inv_file" 
    echo "Remote databases written to: $remote_inv_file" >&2
    
    # Output results for caller
    echo "local_inventory=$local_inv_file"
    echo "remote_inventory=$remote_inv_file"
    exit 0
    ;;
    
  verify)
    echo "ERROR: verify command not implemented" >&2
    exit 1
    ;;
    
esac

# Execute plan command (existing implementation)
if [[ "$command_name" == "plan" ]]; then

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

# Check LOCAL_ONLY_ALLOWLIST for local-only databases
is_allowlisted() {
  local database="$1"
  for item in $LOCAL_ONLY_ALLOWLIST; do
    [[ "$database" == "$item" ]] && return 0
  done
  return 1
}

while IFS= read -r database; do
  if [[ "$database" =~ $EXCLUDE_DB_REGEX ]]; then
    printf 'EXCLUDED\t%s\t-\tNAME_POLICY\n' "$database" >>"$tmp/records"
  elif grep -Fxq "$database" "$tmp/alias-local"; then
    continue
  elif grep -Fxq "$database" "$tmp/remote"; then
    printf 'COMMON\t%s\t%s\t%s\n' \
      "$database" "$database" "$(mode_for "$database")" >>"$tmp/records"
  else
    # Local-only database - check allowlist
    if is_allowlisted "$database"; then
      printf 'LOCAL_ONLY\t%s\t-\tCREATE_REMOTE_AND_INSERT_ONLY\n' \
        "$database" >>"$tmp/records"
    else
      printf 'EXCLUDED\t%s\t-\tNOT_ALLOWLISTED\n' \
        "$database" >>"$tmp/records"
    fi
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

fi  # End of plan command execution
