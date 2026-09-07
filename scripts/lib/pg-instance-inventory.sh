#!/usr/bin/env bash
# pg-instance-inventory.sh - Database inventory collection
set -euo pipefail

PG_INSTANCE_ROOT="${PG_INSTANCE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

detect_envs_loader() {
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

# Source environment configuration
source_env_config() {
  local env_type="$1"
  local envs_loader
  if ! envs_loader="$(detect_envs_loader)"; then
    echo "ERROR: envs loader not found - cannot load credentials" >&2
    return 1
  fi
  # shellcheck disable=SC1090
  source "$envs_loader" --project llm-gateway-go
  case "$env_type" in
    local)
      export PG_PASS_LOCAL="${PG_PASS_LOCAL:-${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded}}"
      set -a
      # shellcheck disable=SC1091
      source "$PG_INSTANCE_ROOT/configs/env-local.sh"
      set +a
      ;;
    remote|252)
      set -a
      # shellcheck disable=SC1091
      source "$PG_INSTANCE_ROOT/configs/env-252.sh"
      set +a
      ;;
    *)
      echo "ERROR: unknown environment type: $env_type" >&2
      return 1
      ;;
  esac
}

# Get database list from PostgreSQL instance
get_database_list() {
  local env_type="$1"
  local output_file="$2"
  
  source_env_config "$env_type"
  
  if [[ "$env_type" == "local" ]]; then
    # Local Docker container
    docker exec -i -e PGPASSWORD="$PG_PASS" "$DOCKER_PG_CONTAINER" \
      psql -X -U "$PG_USER" -d postgres -Atc "
      SELECT datname 
      FROM pg_database 
      WHERE datallowconn AND NOT datistemplate
      ORDER BY datname;
    " > "$output_file"
  else
    # Remote via SSH tunnel - source tunnel functions
    # shellcheck disable=SC1091
    source "$PG_INSTANCE_ROOT/scripts/lib/252-db-tunnel.sh"
    
    # Ensure tunnel is active
    db252_tunnel_ensure || {
      echo "ERROR: Failed to establish SSH tunnel" >&2
      return 1
    }
    
    # Execute query via tunnel
    PGPASSWORD="$PG_PASS" "${PG_PSQL_BIN:-psql}" -X \
      -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d postgres -Atc "
      SELECT datname 
      FROM pg_database 
      WHERE datallowconn AND NOT datistemplate
      ORDER BY datname;
    " > "$output_file"
    
    # Clean up tunnel when done
    db252_tunnel_teardown
  fi
  
  echo "Database inventory written to: $output_file" >&2
}

# Get extended database information (size, owner, etc.)
get_database_info() {
  local env_type="$1"
  local db_name="$2"
  
  source_env_config "$env_type"
  
  if [[ "$env_type" == "local" ]]; then
    docker exec -i "$DOCKER_PG_CONTAINER" psql -X -U "$PG_USER" \
      -d "$db_name" -AtF $'\t' -c "
      SELECT current_database(), pg_database_size(current_database()),
        pg_get_userbyid(d.datdba), d.encoding, d.datcollate, d.datctype
      FROM pg_database d WHERE d.datname = current_database();"
  else
    echo "ERROR: remote info requires managed tunnel context" >&2
    return 1
  fi
}

# If called directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  case "${1:-}" in
    list)
      [[ -n "${2:-}" && -n "${3:-}" ]] || {
        echo "Usage: $0 list <env_type> <output_file>" >&2
        exit 1
      }
      get_database_list "$2" "$3"
      ;;
    info)
      [[ -n "${2:-}" && -n "${3:-}" ]] || {
        echo "Usage: $0 info <env_type> <database>" >&2
        exit 1
      }
      get_database_info "$2" "$3"
      ;;
    *)
      echo "Usage: $0 {list|info} <args>" >&2
      exit 1
      ;;
  esac
fi