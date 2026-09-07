#!/usr/bin/env bash
# pg-instance-guardrails.sh - Safety checks and validations
set -euo pipefail

# Check if manifest hash is fresh and valid
validate_manifest_freshness() {
  local manifest_file="$1"
  local provided_hash="$2"
  local policy_file="${3:-}"
  
  [[ -r "$manifest_file" ]] || {
    echo "ERROR: manifest file not readable: $manifest_file" >&2
    return 1
  }
  
  local actual_hash
  actual_hash="$(shasum -a 256 "$manifest_file" | awk '{print $1}')"
  
  [[ "$provided_hash" == "$actual_hash" ]] || {
    echo "ERROR: manifest hash mismatch" >&2
    echo "  Expected: $provided_hash" >&2
    echo "  Actual:   $actual_hash" >&2
    echo "  Manifest may have been modified since planning" >&2
    return 1
  }

  local generated_epoch local_file remote_file recorded_local recorded_remote
  local recorded_policy policy_path actual_local actual_remote actual_policy
  generated_epoch="$(awk -F'\t' '$1=="#generated_epoch"{print $2}' "$manifest_file")"
  local_file="$(awk -F'\t' '$1=="#local_inventory_file"{print $2}' "$manifest_file")"
  remote_file="$(awk -F'\t' '$1=="#remote_inventory_file"{print $2}' "$manifest_file")"
  policy_path="$(awk -F'\t' '$1=="#policy_file"{print $2}' "$manifest_file")"
  recorded_local="$(awk -F'\t' '$1=="#local_inventory_sha256"{print $2}' "$manifest_file")"
  recorded_remote="$(awk -F'\t' '$1=="#remote_inventory_sha256"{print $2}' "$manifest_file")"
  recorded_policy="$(awk -F'\t' '$1=="#policy_sha256"{print $2}' "$manifest_file")"
  [[ "$generated_epoch" =~ ^[0-9]+$ && -r "$local_file" && -r "$remote_file" &&
    -r "$policy_path" ]] || {
    echo "ERROR: manifest provenance is missing or unreadable" >&2
    return 1
  }
  actual_local="$(LC_ALL=C sort -u "$local_file" | sed '/^[[:space:]]*$/d' |
    shasum -a 256 | awk '{print $1}')"
  actual_remote="$(LC_ALL=C sort -u "$remote_file" | sed '/^[[:space:]]*$/d' |
    shasum -a 256 | awk '{print $1}')"
  actual_policy="$(shasum -a 256 "$policy_path" | awk '{print $1}')"
  [[ "$actual_local" == "$recorded_local" &&
    "$actual_remote" == "$recorded_remote" &&
    "$actual_policy" == "$recorded_policy" ]] || {
    echo "ERROR: manifest input changed since planning" >&2
    return 1
  }
  local supplied_policy_path=""
  [[ -z "$policy_file" ]] ||
    supplied_policy_path="$(cd "$(dirname "$policy_file")" && pwd)/$(basename "$policy_file")"
  [[ -z "$supplied_policy_path" || "$supplied_policy_path" == "$policy_path" ]] || {
    echo "ERROR: supplied policy differs from manifest policy" >&2
    return 1
  }

  local max_age_hours=1 manifest_age max_age_seconds
  max_age_hours="$(
    # shellcheck disable=SC1090
    source "$policy_path"
    printf '%s' "${MAX_MANIFEST_AGE_HOURS:-1}"
  )"
  manifest_age="$(( $(date +%s) - generated_epoch ))"
  max_age_seconds=$((max_age_hours * 3600))
  
  if [[ $manifest_age -gt $max_age_seconds ]]; then
    echo "ERROR: manifest is too old (${manifest_age}s > ${max_age_seconds}s limit)" >&2
    echo "  Manifest age exceeds MAX_MANIFEST_AGE_HOURS=${max_age_hours}" >&2
    echo "  Must regenerate with current database state" >&2
    return 1
  fi
}

# Validate that operation is safe for llm_gateway databases
validate_llm_gateway_protection() {
  local operation="$1"
  local manifest_file="$2"
  
  case "$operation" in
    apply-data)
      # apply-data must never touch llm_gateway databases with data modes
      if grep -E "llm_gateway.*(SCHEMA_AND_INSERT_ONLY|CREATE_REMOTE_AND_INSERT_ONLY|BOOTSTRAP_LOCAL_FULL)" "$manifest_file" >/dev/null; then
        echo "ERROR: Data operations on llm_gateway databases are permanently prohibited" >&2
        echo "  Operation: $operation" >&2
        echo "  LLM Gateway data must never be modified by sync operations" >&2
        return 1
      fi
      ;;
    bootstrap)
      if awk -F'\t' '
        ($1=="LOCAL_ONLY" && $2=="llm_gateway") ||
        ($1=="REMOTE_ONLY" && $3=="llm_gateway") {found=1}
        END {exit found ? 0 : 1}
      ' "$manifest_file"; then
        echo "ERROR: full bootstrap of llm_gateway is permanently prohibited" >&2
        return 1
      fi
      ;;
    all)
      # 'all' command should skip data operations on llm_gateway but allow schema
      # This is validated during execution, not planning - just ensure SCHEMA_ONLY mode
      if grep -q "llm_gateway" "$manifest_file"; then
        if ! grep -E "llm_gateway.*SCHEMA_ONLY" "$manifest_file" >/dev/null; then
          echo "ERROR: llm_gateway databases must use SCHEMA_ONLY mode for 'all' command" >&2
          echo "  Data operations will be skipped for llm_gateway, schema operations allowed" >&2
          return 1
        fi
      fi
      ;;
    apply-schema)
      # Schema operations on llm_gateway are allowed but must be schema-only
      if grep -q "llm_gateway" "$manifest_file"; then
        if ! grep -E "llm_gateway.*SCHEMA_ONLY" "$manifest_file" >/dev/null; then
          echo "ERROR: llm_gateway databases must use SCHEMA_ONLY mode" >&2
          return 1
        fi
      fi
      ;;
  esac
}

# Check for dangerous operations that require manual confirmation
check_destructive_operations() {
  local operation="$1"
  local manifest_file="$2"
  
  # For now, flag any DROP operations as requiring manual review
  # This is a placeholder - actual schema analysis will be implemented later
  case "$operation" in
    apply-schema|all)
      echo "INFO: Schema operations require manual review of destructive changes" >&2
      echo "  Any DROP INDEX/TABLE/CONSTRAINT operations must be confirmed separately" >&2
      ;;
  esac
}

# Validate database name patterns against exclusion rules
validate_database_names() {
  local database_name="$1"
  local exclude_regex="${EXCLUDE_DB_REGEX:-}"
  
  [[ -n "$exclude_regex" ]] || {
    echo "ERROR: EXCLUDE_DB_REGEX not set" >&2
    return 1
  }
  
  if [[ "$database_name" =~ $exclude_regex ]]; then
    echo "EXCLUDED: $database_name matches exclusion pattern" >&2
    return 1
  fi
  
  # Special protection for llm_gateway_sync_ prefixed databases
  if [[ "$database_name" =~ ^llm_gateway_sync_ ]]; then
    echo "EXCLUDED: $database_name is a sync metadata database" >&2
    return 1
  fi
}

# If called directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  case "${1:-}" in
    validate-freshness)
      [[ -n "${2:-}" && -n "${3:-}" ]] || {
        echo "Usage: $0 validate-freshness <manifest_file> <hash>" >&2
        exit 1
      }
      validate_manifest_freshness "$2" "$3"
      ;;
    validate-llm-protection)
      [[ -n "${2:-}" && -n "${3:-}" ]] || {
        echo "Usage: $0 validate-llm-protection <operation> <manifest_file>" >&2
        exit 1
      }
      validate_llm_gateway_protection "$2" "$3"
      ;;
    validate-db-name)
      [[ -n "${2:-}" ]] || {
        echo "Usage: $0 validate-db-name <database_name>" >&2
        exit 1
      }
      validate_database_names "$2"
      ;;
    *)
      echo "Usage: $0 {validate-freshness|validate-llm-protection|validate-db-name} <args>" >&2
      exit 1
      ;;
  esac
fi