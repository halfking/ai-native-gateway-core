#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/verify-outbox-reaper.sh
# Purpose:       Run the integration-tagged Go tests for the session
#                aggregate outbox reaper against the isolated audit PG.
#
#                This is a thin wrapper around `go test -tags=integration` so
#                operators can rerun the reaper test suite without remembering
#                the exact incantation.
#
# Usage:
#   bash scripts/audit/start-isolated-pg.sh --recreate
#   bash scripts/audit/verify-outbox-reaper.sh
# -----------------------------------------------------------------------------
set -euo pipefail
if [[ ! -f /tmp/audit-pg.env ]]; then
    echo "ERROR: /tmp/audit-pg.env missing; run start-isolated-pg.sh first" >&2
    exit 1
fi
set -a
# shellcheck disable=SC1091
source /tmp/audit-pg.env
set +a
export TEST_AUDIT_ISOLATED_DB_URL="$AUDIT_PG_DSN"

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

echo "Running reaper integration tests against $TEST_AUDIT_ISOLATED_DB_URL"
go test -tags=integration -count=1 -v -timeout 60s \
    -run 'TestReaper_' \
    ./domains/session/v2/... 2>&1 | tail -80
