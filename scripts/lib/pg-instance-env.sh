#!/usr/bin/env bash
# Shared env-loader discovery for instance-sync scripts.
set -euo pipefail
PG_INSTANCE_ROOT="${PG_INSTANCE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

pg_instance_detect_envs_loader() {
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
