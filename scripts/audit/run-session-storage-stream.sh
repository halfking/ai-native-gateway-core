#!/usr/bin/env bash
# Disposable loopback services; no production schema, credentials or providers.
set -euo pipefail
cd "$(dirname "$0")/../.."
require_coherence=0
# Keep the array nonempty for macOS Bash 3 with set -u.
test_args=(-p=2)
for arg in "$@"; do
  case "$arg" in
    --require-coherence) require_coherence=1 ;;
    --race) test_args+=(-race) ;;
    *) echo "usage: $0 [--race] [--require-coherence]" >&2; exit 2 ;;
  esac
done
pg_id=""
redis_id=""
cleanup() {
  if [[ -n "$pg_id" ]]; then docker rm -fv "$pg_id" >/dev/null 2>&1 || true; fi
  if [[ -n "$redis_id" ]]; then docker rm -fv "$redis_id" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
docker image inspect postgres:17-alpine redis:7.4.11-alpine >/dev/null
pg_id=$(docker run --pull=never --label codex.audit=session-storage-stream \
  -e POSTGRES_HOST_AUTH_METHOD=trust -p 127.0.0.1::5432 -d postgres:17-alpine)
redis_id=$(docker run --pull=never --label codex.audit=session-storage-stream \
  -p 127.0.0.1::6379 -d redis:7.4.11-alpine redis-server --save '' --appendonly no)
ready=0
for attempt in {1..30}; do
  if docker exec "$pg_id" pg_isready -U postgres >/dev/null 2>&1 && \
     docker exec "$redis_id" redis-cli ping >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then echo 'disposable services did not become ready' >&2; exit 1; fi
pg_addr=$(docker port "$pg_id" 5432/tcp)
redis_addr=$(docker port "$redis_id" 6379/tcp)
export SESSION_AUDIT_REDIS_ADDR="$redis_addr"
export SESSION_AUDIT_REQUIRE_COHERENCE="$require_coherence"
# Prevent inherited settings from redirecting other integration fixtures.
unset TEST_PG_URL TEST_DB_URL TEST_DATABASE_URL TEST_SESSION_V2_DATABASE_URL SESSION_AUDIT_WORKER SESSION_AUDIT_SESSION
export TEST_PG_URL="postgres://postgres@$pg_addr/postgres?sslmode=disable"
echo "Session storage audit: disposable Redis/PG, coherence gate=$require_coherence"
echo 'OBSERVATION tests measure remaining gaps; PASS does not certify coherence.'
go test "${test_args[@]}" -tags=integration -timeout=120s \
  ./security/sanitize ./domains/hooks/compression ./domains/session/v2 ./domains/streaming \
  -run '^TestStorageAudit' -count=1 -v
