#!/usr/bin/env bash
# R37 audit pin (2026-09-17): the deploy-local lib must emit a UNIT-SUFFIXED
# LLM_GATEWAY_DB_BOOT_RETRY_SECONDS default.
#
# Background: the gateway parses the variable with time.ParseDuration
# (cmd/gateway/main_helpers.go bootRetryBudgetEnv) — a bare "90" fails with
# "missing unit" and silently falls back to the 20s default, which neutralized
# the layer-2 defense added for incident 2114 (cutover readyz database:null).
# be24c65c9 shipped exactly that bare "90"; this test pins "90s".
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

env_file="$tmpdir/env.out"

# Drive the real dl_write_env (native branch, DL_DOCKER unset) and capture
# the produced docker --env-file.
# shellcheck disable=SC1090
bash -c '
  set -euo pipefail
  source "'"$LIB"'"
  dl_write_env "'"$env_file"'" 8782
' >/dev/null

line="$(grep -E '^LLM_GATEWAY_DB_BOOT_RETRY_SECONDS=' "$env_file" || true)"
if [[ "$line" != "LLM_GATEWAY_DB_BOOT_RETRY_SECONDS=90s" ]]; then
  printf 'FAIL: boot-retry default = %q, want LLM_GATEWAY_DB_BOOT_RETRY_SECONDS=90s\n' "$line" >&2
  exit 1
fi

# Guard against regression to the unit-less default.
if grep -q 'LLM_GATEWAY_DB_BOOT_RETRY_SECONDS:-90}' "$LIB"; then
  echo 'FAIL: lib regressed to the unit-less default ":-90" (time.ParseDuration rejects it)' >&2
  exit 1
fi

echo 'deploy-local-lib boot-retry default test: PASS'
