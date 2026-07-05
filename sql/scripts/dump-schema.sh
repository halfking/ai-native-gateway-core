#!/usr/bin/env bash
# dump-schema.sh — Regenerate 00-prereqs.sql + 01-schema.sql from production DB
# Usage: ./dump-schema.sh
#   Uses LLM_GATEWAY_DATABASE_URL or DATABASE_URL or PG* env vars.
#
# All logic lives in scripts/_lib/db-init-lib.sh (the SSOT). This wrapper
# just sources the lib and calls the right function.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../../../../scripts/_lib/db-init-lib.sh"

URL=$(db_init::resolve_db_url) || { echo "ERROR: set LLM_GATEWAY_DATABASE_URL" >&2; exit 1; }
db_init::dump_prereqs "llm-gateway-go" "$URL"
echo
db_init::dump_schema  "llm-gateway-go" "llm_gateway" "$URL"
