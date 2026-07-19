#!/usr/bin/env bash
# 只读预检：不启动或重建容器，不同步数据库，不写入测试凭据。

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8781}"
PG_CONTAINER="${PG_CONTAINER:-r112_postgres}"
REDIS_CONTAINER="${REDIS_CONTAINER:-r112_redis}"
GATEWAY_CONTAINER="${GATEWAY_CONTAINER:-r112_gateway}"

failures=0
check() {
    local label="$1"
    shift
    if "$@"; then
        printf 'PASS %s\n' "$label"
    else
        printf 'FAIL %s\n' "$label" >&2
        failures=$((failures + 1))
    fi
}

printf 'Routing test preflight (read-only)\n'
printf 'Repository: %s\n' "$ROOT"
printf 'Gateway: %s\n\n' "$GATEWAY_URL"

check "gateway health" curl --fail --silent --show-error "${GATEWAY_URL}/healthz"
check "PostgreSQL 17 container" docker exec "$PG_CONTAINER" psql -U kxuser -d postgres -Atqc "SHOW server_version_num" 
check "Redis container" docker exec "$REDIS_CONTAINER" redis-cli ping
check "gateway container" docker inspect --format '{{.State.Running}}' "$GATEWAY_CONTAINER"
check "existing mock tooling" test -x "$ROOT/docs/全方面测试/tools/start_suppliers.sh"
check "existing seed data" test -f "$ROOT/docs/全方面测试/data/seed.sql"
check "existing mock credentials" test -f "$ROOT/sql/scripts/04-loadtest-mock-credentials.sql"
check "schema synchronization script" test -x "$ROOT/scripts/sync-252-schema-only.sh"
check "routing client builds" go build ./cmd/routing-test-client

if (( failures > 0 )); then
    printf '\nPreflight failed: %d check(s). No state was changed.\n' "$failures" >&2
    exit 1
fi

printf '\nPreflight passed. Continue with the documented, explicitly approved setup steps.\n'
