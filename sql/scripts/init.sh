#!/usr/bin/env bash
# init.sh — Bootstrap a fresh llm_gateway database
# Usage:
#   ./init.sh                  # use LLM_GATEWAY_DATABASE_URL or PG* env vars
#   ./init.sh --reset          # DROP public schema first (DESTRUCTIVE)
#
# All logic lives in scripts/_lib/db-init-lib.sh. This wrapper just sources
# the lib and calls the right function.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../../../../scripts/_lib/db-init-lib.sh"

db_init::apply_all "llm-gateway-go" "llm_gateway" "$@"
