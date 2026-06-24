#!/usr/bin/env bash
# verify.sh — Dry-run: bootstrap a fresh DB and verify schema + seed work
# Usage:
#   ./verify.sh                       # uses LLM_GATEWAY_DATABASE_URL or PG* defaults
#   ./verify.sh --no-timescale        # local dev: skip timescaledb hypertables
#   ./verify.sh --keep                # keep the test DB after success
#
# The expected row counts are auto-derived from the current production DB via
# db_init::auto_expected. To override (e.g. for known prod drift), edit
# the EXPECTED array below.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../../../../scripts/_lib/db-init-lib.sh"

# Optional: override expected counts here. If empty, auto-derived from prod.
# Example:
#   declare -A EXPECTED_OVERRIDE=(
#     [applications]=11
#     [settings_kv]=2
#   )
declare -A EXPECTED_OVERRIDE=()

# Auto-generate EXPECTED from current production (only the seed tables)
# IMPORTANT: use the original (non-_verify) URL here, since the test DB
# doesn't have the tables yet.
URL_FOR_EXPECTED="${LLM_GATEWAY_DATABASE_URL:-${DATABASE_URL:-}}"
if [[ -n "$URL_FOR_EXPECTED" ]]; then
  URL_FOR_EXPECTED=$(echo "$URL_FOR_EXPECTED" | sed -E 's#\?[^#]*$##')
  export PGSSLMODE=disable
fi
declare -A EXPECTED=()
while IFS='=' read -r tbl cnt; do
  [[ -z "$tbl" ]] && continue
  if [[ -n "${EXPECTED_OVERRIDE[$tbl]:-}" ]]; then
    EXPECTED[$tbl]="${EXPECTED_OVERRIDE[$tbl]}"
  else
    EXPECTED[$tbl]="$cnt"
  fi
done < <(db_init::auto_expected "$URL_FOR_EXPECTED" 2>/dev/null || true)

# Fall back to LLM_GATEWAY_DATABASE_URL from resolve_db_url if auto_expected
# couldn't get the prod data.
URL=$(db_init::resolve_db_url) || { echo "ERROR: set LLM_GATEWAY_DATABASE_URL" >&2; exit 1; }
if [[ ${#EXPECTED[@]} -eq 0 ]]; then
  echo "WARN: auto_expected returned 0 tables (no prod DB access?)." >&2
  echo "      Define EXPECTED_OVERRIDE in verify.sh to use hardcoded values." >&2
fi

db_init::verify "llm-gateway-go" "llm_gateway" "$@"
