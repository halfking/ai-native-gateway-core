#!/usr/bin/env bash
# dump-seed.sh — Regenerate 02-seed.sql from production DB
# Usage: ./dump-seed.sh
#   Uses LLM_GATEWAY_DATABASE_URL or DATABASE_URL or PG* env vars.
#
# The seed tables are auto-selected via db_init::select_seed_tables (heuristic:
# small + system-config name pattern). See scripts/_lib/db-init-lib.sh.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../../../../scripts/_lib/db-init-lib.sh"

URL=$(db_init::resolve_db_url) || { echo "ERROR: set LLM_GATEWAY_DATABASE_URL" >&2; exit 1; }
db_init::dump_seed "llm-gateway-go" "llm_gateway" "$URL"
