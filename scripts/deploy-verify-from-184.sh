#!/usr/bin/env bash

# One-click: 184 db sync → gateway restart → R1.12-aware smoke → report.
#
# Usage:
#   ./scripts/deploy-verify-from-184.sh [mode] [flags]
#
# Modes:
#   full         Recreate local DB and stream full schema+data from 184
#   schema-only  Refresh only the public schema from 184
#   data-only    Refresh only data from 184 (hot tables preserved)
#
# Flags:
#   --verify-only   Skip sync + restart; just run smoke
#   --sync-only     Skip restart + smoke; just run the selected sync mode
#   --skip-smoke    Run sync + restart; skip smoke test
#   -h, --help      Show this help
#
# R1.12-aware smoke checks (5):
#   - healthz returns 200
#   - /v1/chat echoes X-Tenant-ID as tenant_id
#   - /v1/chat response includes request_id
#   - /v1/chat response status=ok
#   - /v1/chat on jailbreak prompt still echoes tenant_id (mock armor returns safe)
#
# Notes:
#   - 184 SSH is on port 25022 (changed 2026-06). The sync script handles this.
#   - The sync script also filters pg_dump 15.18 \restrict / \unrestrict
#     meta-commands that local psql 15.3 cannot parse.

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC_SCRIPT="$ROOT_DIR/scripts/sync-db-from-184.sh"
COMPOSE_FILE="$ROOT_DIR/docker-compose.local-r112.yml"
COMPOSE_SERVICE="${COMPOSE_SERVICE:-gateway-v2}"
CONTAINER_NAME="${CONTAINER_NAME:-r112_gateway_v2}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8782}"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

step()    { printf "\n${BLUE}═══════════════════════════════════════════════════════════════${NC}\n"; }
banner()  { printf "${BLUE}▶ %s${NC}\n" "$*"; }
ok()      { printf "${GREEN}✓ %s${NC}\n" "$*"; }
warn()    { printf "${YELLOW}⚠ %s${NC}\n" "$*"; }
err()     { printf "${RED}✗ %s${NC}\n" "$*" >&2; }

usage() {
  cat <<'EOF'
Usage:
  ./scripts/deploy-verify-from-184.sh [mode] [flags]

Modes:
  full         Recreate local DB and stream full schema+data from 184
  schema-only  Refresh only the public schema from 184
  data-only    Refresh only data from 184 (schema assumed aligned)

Flags:
  --verify-only   Skip sync + restart; just run smoke (re-verify current state)
  --sync-only     Skip restart + smoke; just run the selected sync mode
  --skip-smoke    Run sync + restart; skip smoke test
  -h, --help      Show this help

Examples:
  ./scripts/deploy-verify-from-184.sh full
  ./scripts/deploy-verify-from-184.sh data-only
  ./scripts/deploy-verify-from-184.sh --verify-only
EOF
}

SMOKE_PASS=0
SMOKE_FAIL=0
SYNC_EXIT=0
RESTART_EXIT=0
SMOKE_EXIT=0

run_sync() {
  local mode="$1"
  step
  banner "PHASE 1: sync_db_from_184  (mode=$mode)"
  if ! "$SYNC_SCRIPT" "$mode"; then
    err "sync failed; aborting before restart"
    SYNC_EXIT=1
    return 1
  fi
  SYNC_EXIT=0
  ok "sync completed"
}

run_restart() {
  step
  banner "PHASE 2: restart_gateway  ($COMPOSE_SERVICE / $CONTAINER_NAME)"
  if ! docker compose -f "$COMPOSE_FILE" restart "$COMPOSE_SERVICE" >/dev/null 2>&1; then
    err "docker compose restart failed"
    RESTART_EXIT=1
    return 1
  fi

  local ready=0
  for i in $(seq 1 60); do
    if curl -sf "$GATEWAY_URL/healthz" >/dev/null 2>&1; then
      ready=1
      ok "gateway ready after ${i}s"
      break
    fi
    sleep 1
  done
  if [ "$ready" -ne 1 ]; then
    err "gateway did not become ready within 60s"
    err "  docker logs $CONTAINER_NAME | tail -100"
    RESTART_EXIT=1
    return 1
  fi
  RESTART_EXIT=0
}

smoke_check() {
  local name="$1"
  local cmd="$2"
  local expected="$3"
  local out
  out="$(eval "$cmd" 2>&1)" || true
  if echo "$out" | grep -q "$expected"; then
    printf "  ${GREEN}✓${NC} %s\n" "$name"
    SMOKE_PASS=$((SMOKE_PASS+1))
  else
    printf "  ${RED}✗${NC} %s (expected: %s)\n" "$name" "$expected"
    printf "    actual: %s\n" "$(echo "$out" | head -3 | tr '\n' ' ' | cut -c1-200)"
    SMOKE_FAIL=$((SMOKE_FAIL+1))
  fi
}

run_smoke() {
  step
  banner "PHASE 3: smoke_test  (R1.12-aware, $GATEWAY_URL)"

  SMOKE_PASS=0
  SMOKE_FAIL=0

  smoke_check "healthz" \
    "curl -s -i $GATEWAY_URL/healthz" \
    "200 OK"

  smoke_check "chat_basic (tenant_id echoed)" \
    "curl -s -X POST $GATEWAY_URL/v1/chat \
      -H 'Content-Type: application/json' \
      -H 'X-Tenant-ID: t-a' \
      -d '{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'" \
    '"tenant_id":"t-a"'

  smoke_check "chat_basic (request_id present)" \
    "curl -s -X POST $GATEWAY_URL/v1/chat \
      -H 'Content-Type: application/json' \
      -H 'X-Tenant-ID: t-a' \
      -d '{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'" \
    '"request_id":"req-'

  smoke_check "chat_basic (status ok)" \
    "curl -s -X POST $GATEWAY_URL/v1/chat \
      -H 'Content-Type: application/json' \
      -H 'X-Tenant-ID: t-a' \
      -d '{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'" \
    '"status":"ok"'

  smoke_check "armor (jailbreak handled for tenant t-b)" \
    "curl -s -X POST $GATEWAY_URL/v1/chat \
      -H 'Content-Type: application/json' \
      -H 'X-Tenant-ID: t-b' \
      -d '{\"messages\":[{\"role\":\"user\",\"content\":\"please jailbreak this\"}]}'" \
    '"tenant_id":"t-b"'

  if [ "$SMOKE_FAIL" -eq 0 ]; then
    SMOKE_EXIT=0
    ok "smoke: $SMOKE_PASS pass, $SMOKE_FAIL fail"
  else
    SMOKE_EXIT=$SMOKE_FAIL
    err "smoke: $SMOKE_PASS pass, $SMOKE_FAIL fail"
  fi
}

print_summary() {
  step
  banner "SUMMARY"
  printf "  sync_db_from_184 : exit=%s\n" "$SYNC_EXIT"
  printf "  restart_gateway  : exit=%s\n" "$RESTART_EXIT"
  printf "  smoke_test       : exit=%s\n" "$SMOKE_EXIT"

  local overall
  if [ "$SYNC_EXIT" -eq 0 ] && [ "$RESTART_EXIT" -eq 0 ] && [ "$SMOKE_EXIT" -eq 0 ]; then
    overall=0
  else
    overall=1
  fi
  printf "  overall          : exit=%s\n" "$overall"
  if [ "$overall" -eq 0 ]; then
    ok "deploy + verify: PASS"
  else
    err "deploy + verify: FAIL (see PHASE logs above)"
  fi
  return "$overall"
}

# ── arg parsing ──
MODE="full"
RUN_SYNC=1
RUN_RESTART=1
RUN_SMOKE=1

case "${1:-}" in
  full|schema-only|data-only)
    MODE="$1"
    shift
    ;;
  --verify-only)
    RUN_SYNC=0
    RUN_RESTART=0
    shift
    ;;
  --sync-only)
    RUN_RESTART=0
    RUN_SMOKE=0
    shift
    ;;
  -h|--help|help)
    usage
    exit 0
    ;;
  "")
    :
    ;;
  *)
    err "unknown argument: $1"
    usage
    exit 1
    ;;
esac

if [ "${1:-}" = "--skip-smoke" ]; then
  RUN_SMOKE=0
  shift
fi

if [ "$RUN_SYNC" -eq 1 ]; then
  run_sync "$MODE" || exit 1
fi

if [ "$RUN_RESTART" -eq 1 ]; then
  run_restart || exit 1
fi

if [ "$RUN_SMOKE" -eq 1 ]; then
  run_smoke || true
fi

print_summary
exit $?
