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
  apply-schema     Additive schema apply (requires --yes, hash, impact, work-dir)
  apply-data       Insert-only data merge (requires --yes, hash, impact, work-dir)
  verify           FK/trigger verification (requires --manifest --policy --output-dir)
  restore          Restore a verified backup into a NEW database
  all              Intentionally not auto-run; prints the required pipeline

Options for plan:
  --local-inventory FILE    Local database list
  --remote-inventory FILE   Remote database list  
  --policy FILE             Sync policy configuration
  --manifest FILE           Output manifest file

Options for inventory:
  --output-dir DIR          Directory for output files

Options for write/verify commands:
  --yes                     Required for all write operations
  --manifest-hash HASH      Required manifest hash for safety
  --freshness-check         Verify manifest freshness
  --impact-matrix FILE      Required by apply-schema/apply-data
  --work-dir DIR            Required by apply-schema/apply-data
  --database NAME           Optional database filter
  --dry-run                 Schema/data preview only
  --llm-ssot-allowlist FILE Hashed public-object allowlist
  --backup-index FILE       Required by restore
  --source-side SIDE        restore: local|remote
  --source-database NAME    restore source dump
  --target-side SIDE        restore: local|remote
  --target-database NAME    NEW target database name

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
yes_flag=false
manifest_hash=""
freshness_check=false
impact_matrix=""
work_dir=""
database_filter=""
dry_run=false
llm_ssot_allowlist=""
llm_ssot_allowlist_hash=""
backup_index=""
source_side=""
source_database=""
target_side=""
target_database=""

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
    --impact-matrix) impact_matrix="${2:-}"; shift 2 ;;
    --work-dir) work_dir="${2:-}"; shift 2 ;;
    --database) database_filter="${2:-}"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    --llm-ssot-allowlist) llm_ssot_allowlist="${2:-}"; shift 2 ;;
    --llm-ssot-allowlist-hash) llm_ssot_allowlist_hash="${2:-}"; shift 2 ;;
    --backup-index) backup_index="${2:-}"; shift 2 ;;
    --source-side) source_side="${2:-}"; shift 2 ;;
    --source-database) source_database="${2:-}"; shift 2 ;;
    --target-side) target_side="${2:-}"; shift 2 ;;
    --target-database) target_database="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

# Validate command
case "$command_name" in
  plan|inventory|backup|apply-schema|apply-data|verify|restore|all)
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
  apply-schema|apply-data)
    [[ "$yes_flag" == true ]] || die "$command_name requires --yes"
    [[ "$freshness_check" == true ]] || die "$command_name requires --freshness-check"
    [[ -n "$manifest_hash" ]] || die "$command_name requires --manifest-hash"
    [[ -r "$manifest_file" && -r "$policy_file" && -r "$impact_matrix" ]] ||
      die "$command_name requires readable --manifest --policy --impact-matrix"
    [[ -n "$work_dir" ]] || die "$command_name requires --work-dir"
    ;;
  verify)
    [[ -r "$manifest_file" && -r "$policy_file" ]] ||
      die "verify requires readable --manifest and --policy"
    [[ -n "$output_dir" ]] || die "verify requires --output-dir"
    ;;
  restore)
    [[ "$yes_flag" == true ]] || die "restore requires --yes"
    [[ "$freshness_check" == true ]] || die "restore requires --freshness-check"
    [[ -n "$manifest_hash" ]] || die "restore requires --manifest-hash"
    [[ -r "$manifest_file" && -r "$policy_file" && -r "$backup_index" ]] ||
      die "restore requires readable --manifest --policy --backup-index"
    ;;
  all)
    echo "ERROR: all is intentionally not auto-run" >&2
    echo "  Use: inventory → plan → impact → backup → bootstrap → apply-schema → apply-data → verify" >&2
    exit 1
    ;;
esac

# Source library functions
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/pg-instance-guardrails.sh"

common_extra=()
[[ "$dry_run" == true ]] && common_extra+=(--dry-run)
[[ -n "$database_filter" ]] && common_extra+=(--database "$database_filter")
schema_extra=("${common_extra[@]}")
if [[ -n "$llm_ssot_allowlist" ]]; then
  schema_extra+=(--llm-ssot-allowlist "$llm_ssot_allowlist")
  [[ -n "$llm_ssot_allowlist_hash" ]] &&
    schema_extra+=(--llm-ssot-allowlist-hash "$llm_ssot_allowlist_hash")
fi

# Execute commands
case "$command_name" in
  backup)
    exec bash "$ROOT/scripts/pg-instance-backup.sh" \
      --manifest "$manifest_file" --manifest-hash "$manifest_hash" \
      --policy "$policy_file" --output-dir "$output_dir" \
      --yes --freshness-check
    ;;
  apply-schema)
    exec bash "$ROOT/scripts/pg-instance-schema-additive.sh" \
      --manifest "$manifest_file" --manifest-hash "$manifest_hash" \
      --policy "$policy_file" --impact-matrix "$impact_matrix" \
      --work-dir "$work_dir" --yes --freshness-check "${schema_extra[@]}"
    ;;
  apply-data)
    exec bash "$ROOT/scripts/pg-instance-data-merge.sh" \
      --manifest "$manifest_file" --manifest-hash "$manifest_hash" \
      --policy "$policy_file" --impact-matrix "$impact_matrix" \
      --work-dir "$work_dir" --yes --freshness-check "${common_extra[@]}"
    ;;
  verify)
    verify_extra=()
    [[ -n "$impact_matrix" ]] && verify_extra+=(--impact-matrix "$impact_matrix")
    exec bash "$ROOT/scripts/pg-instance-verify.sh" \
      --manifest "$manifest_file" --policy "$policy_file" \
      --output-dir "$output_dir" "${verify_extra[@]}"
    ;;
  restore)
    exec bash "$ROOT/scripts/pg-instance-restore.sh" \
      --manifest "$manifest_file" --manifest-hash "$manifest_hash" \
      --policy "$policy_file" --backup-index "$backup_index" \
      --source-side "$source_side" --source-database "$source_database" \
      --target-side "$target_side" --target-database "$target_database" \
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
  plan)
    exec bash "$ROOT/scripts/pg-instance-plan.sh" \
      --local-inventory "$local_inventory" \
      --remote-inventory "$remote_inventory" \
      --policy "$policy_file" --manifest "$manifest_file"
    ;;
esac
die "unhandled command: $command_name"
