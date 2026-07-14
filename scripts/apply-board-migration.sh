#!/usr/bin/env bash
# Apply migration 394 (request_stats_minute) for dashboard board feature.
# Usage:
#   ./scripts/apply-board-migration.sh
#   PG_URL=postgres://... ./scripts/apply-board-migration.sh

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATION="$ROOT_DIR/sql/migrations/startup/394_request_stats_minute.sql"

PG_URL="${PG_URL:-${LLM_GATEWAY_DATABASE_URL:-}}"

if [[ -z "$PG_URL" ]]; then
  echo "Set PG_URL or LLM_GATEWAY_DATABASE_URL" >&2
  exit 1
fi

if [[ ! -f "$MIGRATION" ]]; then
  echo "Migration not found: $MIGRATION" >&2
  exit 1
fi

echo "Applying 394_request_stats_minute.sql ..."
psql "$PG_URL" -v ON_ERROR_STOP=1 -f "$MIGRATION"
echo "Done. Restart gateway to enable accumulator + rollup worker."
