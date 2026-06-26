#!/usr/bin/env bash

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8782}"
PATH_SUFFIX="${PATH_SUFFIX:-/v1/chat?q=hello&model=gpt-4o}"
TENANT_ID="${TENANT_ID:-t1}"
SESSION_ID="${SESSION_ID:-s1}"
API_KEY="${API_KEY:-local-test}"

echo "== Local Routing Load Test =="
echo "BASE_URL   : $BASE_URL"
echo "PATH_SUFFIX: $PATH_SUFFIX"
echo "TENANT_ID  : $TENANT_ID"
echo "SESSION_ID : $SESSION_ID"
echo

if ! command -v npx >/dev/null 2>&1; then
  echo "npx not found" >&2
  exit 1
fi

echo "-- health check --"
curl -sf "$BASE_URL/healthz" >/dev/null
echo "healthz ok"
echo

echo "-- sample request --"
curl -s "$BASE_URL$PATH_SUFFIX" \
  -H "X-Tenant-ID: $TENANT_ID" \
  -H "X-Session-ID: $SESSION_ID" \
  -H "X-API-Key: $API_KEY"
echo
echo

echo "-- run 1: medium concurrency (50 conn / 20s) --"
npx autocannon -c 50 -d 20 -m GET "$BASE_URL$PATH_SUFFIX" \
  -H "X-Tenant-ID: $TENANT_ID" \
  -H "X-Session-ID: $SESSION_ID" \
  -H "X-API-Key: $API_KEY"
echo

echo "-- run 2: high concurrency (200 conn / 30s) --"
npx autocannon -c 200 -d 30 -m GET "$BASE_URL$PATH_SUFFIX" \
  -H "X-Tenant-ID: $TENANT_ID" \
  -H "X-Session-ID: $SESSION_ID" \
  -H "X-API-Key: $API_KEY"
