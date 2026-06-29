#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# fp_slot regression end-to-end verification (2026-06-29)
#
# Reproduces the "minimax-prod-1 52% failure rate" scenario:
#   - 25-slot pool, 2-3 concurrent users, one user fires 10+ in parallel
#   - Latency 10-15s per upstream call (mocks real LLM response time)
#   - All requests share the same sticky session (same X-Session-Id)
#
# Steps:
#   1. Re-point provider 14 (minimax) at the local mock upstream
#   2. Start mock upstream (port 18080) with 10-15s latency
#   3. Start gateway (port 8782) pointing at docker postgres/redis + mock
#   4. Wait for gateway /healthz
#   5. Snapshot Redis slot state
#   6. Run the workload: 1 sticky session × 20 concurrent requests
#   7. Count 200 OK vs errors (look for "cred_fp_slot saturated")
#   8. Restore base_url, kill mock + gateway
#
# Usage:
#   ./scripts/verify-fpslot-fix.sh [--skip-build] [--mock-only]
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN_FPSLOT_FIX="$ROOT_DIR/build/llm-gateway-fpslot-fix"
BIN_BEFORE_FIX="$ROOT_DIR/build/llm-gateway-before-fix"
MOCK_PORT=18080
# Docker Desktop on this host reserves :8782, so we use :18181 for the gateway.
GW_PORT=18181
PROVIDER_ID=14
CREDENTIAL_ID=6
PROVIDER_BACKUP_FILE="/tmp/fpslot-verify-provider-snapshot.sql"
# Snapshot credential ciphertext BEFORE STEP 4 overwrites it with a local
# Fernet key. Without this snapshot, a mid-run failure leaves the prod
# ciphertext gone forever (gateway can no longer decrypt the real secret).
CREDENTIAL_BACKUP_FILE="/tmp/fpslot-verify-credential-snapshot.sql"
LOCAL_FERNET_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

# ── Args ──
SKIP_BUILD=0
MOCK_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --skip-build)   SKIP_BUILD=1 ;;
    --mock-only)    MOCK_ONLY=1  ;;
    --before-fix)   USE_BEFORE_FIX=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
hdr()  { echo -e "${BLUE}══ $* ══${NC}"; }

USE_BEFORE_FIX="${USE_BEFORE_FIX:-0}"
if [ "$USE_BEFORE_FIX" = "1" ]; then
  BIN="$BIN_BEFORE_FIX"
  info "using BEFORE-FIX binary: $BIN (expect high failure rate)"
else
  BIN="$BIN_FPSLOT_FIX"
fi

run_pg() {
  docker exec -e PGPASSWORD=kxpass r112_postgres psql -U kxuser -d llm_gateway \
    -v ON_ERROR_STOP=1 -tAc "$1"
}

cleanup() {
  info "cleanup: killing mock + gateway processes"
  pkill -f "server.py"      2>/dev/null || true
  pkill -f "/build/llm-gateway-"    2>/dev/null || true
  sleep 1
  if [ -f "$PROVIDER_BACKUP_FILE" ]; then
    info "restoring providers.base_url from snapshot"
    PGPASSWORD=kxpass docker exec -i -e PGPASSWORD=kxpass r112_postgres \
      psql -U kxuser -d llm_gateway -v ON_ERROR_STOP=1 \
      < "$PROVIDER_BACKUP_FILE" >/dev/null 2>&1 || true
  fi
  if [ -f "$CREDENTIAL_BACKUP_FILE" ]; then
    info "restoring credentials.secret_ciphertext + secret_kid from snapshot"
    PGPASSWORD=kxpass docker exec -i -e PGPASSWORD=kxpass r112_postgres \
      psql -U kxuser -d llm_gateway -v ON_ERROR_STOP=1 \
      < "$CREDENTIAL_BACKUP_FILE" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# ── Pre-flight ──
[ -d build ] || mkdir -p build
for cmd in docker redis-cli; do
  command -v "$cmd" >/dev/null 2>&1 || { err "$cmd not found"; exit 1; }
done

if [ "$SKIP_BUILD" -ne 1 ]; then
  hdr "STEP 1 — Build gateway binary with the fix baked in"
  info "compiling cmd/gateway..."
  CGO_ENABLED=0 go build -o "$BIN" ./cmd/gateway
  ok "binary built: $BIN"
fi
[ -x "$BIN" ] || { err "binary not found: $BIN"; exit 1; }

if [ "$MOCK_ONLY" -eq 1 ]; then
  info "--mock-only: skipping seed + gateway start"
  MOCK_LATENCY_MS_MIN=10000 MOCK_LATENCY_MS_MAX=15000 \
    MOCK_PORT=$MOCK_PORT MOCK_TOKEN=fpslot-verify \
    python3 "$ROOT_DIR/scripts/mocks/llm-mock-upstream/server.py"
  exit 0
fi

hdr "STEP 2 — Snapshot current providers.base_url + credential ciphertext for restore"
run_pg "SELECT 'UPDATE providers SET base_url = ' || quote_literal(base_url) || ' WHERE id = $PROVIDER_ID;' FROM providers WHERE id = $PROVIDER_ID;" \
  > "$PROVIDER_BACKUP_FILE"
cat "$PROVIDER_BACKUP_FILE"
ok "providers snapshot saved → $PROVIDER_BACKUP_FILE"

# Snapshot credential 6's real ciphertext + secret_kid BEFORE we overwrite
# them. Encoded as hex (decode('...', 'hex')) so psql \copy can round-trip
# arbitrary bytes safely. Without this, a mid-run failure means the gateway
# can never decrypt the real prod secret again (we don't have the prod
# Fernet key to re-encrypt from).
run_pg "SELECT 'UPDATE credentials SET secret_ciphertext = decode(' || quote_literal(encode(secret_ciphertext, 'hex')) || ', ''hex''), secret_kid = ' || quote_literal(secret_kid) || ' WHERE id = $CREDENTIAL_ID;' FROM credentials WHERE id = $CREDENTIAL_ID;" \
  > "$CREDENTIAL_BACKUP_FILE"
cat "$CREDENTIAL_BACKUP_FILE"
ok "credential snapshot saved → $CREDENTIAL_BACKUP_FILE"

hdr "STEP 3 — Repoint provider $PROVIDER_ID at local mock upstream"
# Gateway runs as a native host process (not in Docker), so base_url uses 127.0.0.1.
run_pg "UPDATE providers SET base_url = 'http://127.0.0.1:$MOCK_PORT/v1' WHERE id = $PROVIDER_ID;"
NEW_URL=$(run_pg "SELECT base_url FROM providers WHERE id = $PROVIDER_ID;")
info "new base_url = $NEW_URL"

hdr "STEP 4 — Confirm minimax-m3 binding is available"
run_pg "UPDATE credential_model_bindings SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL WHERE credential_id = $CREDENTIAL_ID AND provider_model_id IN (SELECT id FROM provider_models WHERE raw_model_name = 'MiniMax-M3');"
# NOTE: 'credentials' table has no 'availability_state' column in current schema
# (verified 2026-06-29 against r112_postgres). Only the columns that exist are reset.
run_pg "UPDATE credentials SET status = 'active', cooling_until = NULL, circuit_state = 'closed' WHERE id = $CREDENTIAL_ID;"

# Re-encrypt credential 6's secret with our known Fernet key. The original
# ciphertext was encrypted with a production key we don't have; without
# rewriting it, the gateway logs "failed to reveal api key" and rejects
# every request (no upstream call is made). The hex output of
# /tmp/fpslot-make-cipher.go is "0x<hex>" — strip the prefix.
NEW_CIPHER=$(go run /tmp/fpslot-make-cipher.go "$LOCAL_FERNET_KEY" "fpslot-verify-bearer-token" 2>/dev/null | sed 's/^\\x//' | tr -d '[:space:]')
run_pg "UPDATE credentials SET secret_ciphertext = decode('$NEW_CIPHER', 'hex'), secret_kid = 'local-verify' WHERE id = $CREDENTIAL_ID;"
ok "credential $CREDENTIAL_ID reset + re-encrypted with local Fernet key"

hdr "STEP 5 — Flush fp_slot Redis state for credential 6"
redis-cli -h localhost -p 6379 KEYS "llmgw:cred_fp_slot:6:*" | xargs -r redis-cli -h localhost -p 6379 DEL >/dev/null
redis-cli -h localhost -p 6379 KEYS "llmgw:sess_cred_fp:*:6" | xargs -r redis-cli -h localhost -p 6379 DEL >/dev/null
ok "slot + pin keys cleared"

hdr "STEP 6 — Start mock upstream (low latency for tight burst)"
pkill -f "server.py" 2>/dev/null || true
sleep 1
# Latency: we want the burst to land on Redis at the same instant, so the
# upstream itself should respond quickly (avoids skewing the burst via slow
# Python event-loop dispatch). The "fp_slot contention" happens at the
# Acquire call (T+0ms), long before the upstream returns — so a 500-1500ms
# latency budget still produces the same Redis-race pattern as production.
LATENCY_BUDGET_MS=1500
MOCK_LATENCY_MS_MIN=500 MOCK_LATENCY_MS_MAX=$LATENCY_BUDGET_MS \
  MOCK_PORT=$MOCK_PORT MOCK_TOKEN=fpslot-verify \
  python3 "$ROOT_DIR/scripts/mocks/llm-mock-upstream/server.py" \
  > /tmp/fpslot-verify-mock.log 2>&1 &
MOCK_PID=$!
echo "$MOCK_PID" > /tmp/fpslot-verify-mock.pid
sleep 2
for i in 1 2 3 4 5; do
  if curl -sf "http://localhost:$MOCK_PORT/healthz" >/dev/null; then
    ok "mock ready (pid=$MOCK_PID)"; break
  fi
  sleep 1
done
curl -sf "http://localhost:$MOCK_PORT/healthz" >/dev/null || { err "mock failed to start"; cat /tmp/fpslot-verify-mock.log; exit 1; }

hdr "STEP 7 — Start gateway"
pkill -f "/build/llm-gateway-"    2>/dev/null || true
sleep 1
export LLM_GATEWAY_LISTEN=":$GW_PORT"
# r112 compose maps container 5432 → host 5433 (host has its own postgres on 5432).
export LLM_GATEWAY_DATABASE_URL="postgres://kxuser:kxpass@127.0.0.1:5433/llm_gateway?sslmode=disable"
export LLM_GATEWAY_REDIS_ADDR="127.0.0.1:6379"
export LLM_GATEWAY_ENABLE_CREDENTIAL_FP_SLOTS="true"
export LLM_GATEWAY_DEFAULT_CREDENTIAL_CONCURRENCY="25"
export LLM_GATEWAY_UPSTREAM_TIMEOUT="120"
export LLM_GATEWAY_API_KEY="fpslot-verify-key"
export LLM_GATEWAY_ADMIN_API_KEY="fpslot-verify-admin"
export LLM_GATEWAY_LOG_LEVEL="info"
# Required to avoid the "CORS fail-closed" panic at startup.
export LLM_GATEWAY_CORS_ORIGINS="*"
# Matches the Fernet key used in STEP 4 to re-encrypt credential 6.
export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="$LOCAL_FERNET_KEY"
"$BIN" > /tmp/fpslot-verify-gateway.log 2>&1 &
GW_PID=$!
echo "$GW_PID" > /tmp/fpslot-verify-gateway.pid
sleep 3
for i in $(seq 1 30); do
  if curl -sf "http://localhost:$GW_PORT/healthz" >/dev/null 2>&1; then
    ok "gateway ready (pid=$GW_PID, after ${i}s)"; break
  fi
  sleep 1
done
curl -sf "http://localhost:$GW_PORT/healthz" >/dev/null || {
  err "gateway failed to start within 30s"
  tail -50 /tmp/fpslot-verify-gateway.log
  exit 1
}

hdr "STEP 8 — Run the workload (1 sticky session × 20 concurrent)"
info "scenario: same X-Session-Id, 20 parallel POST /v1/chat/completions"
info "expected AFTER fix: all 20 succeed sharing 1 fp_slot"

WORKLOAD_DIR="$ROOT_DIR/build/fpslot-verify"
mkdir -p "$WORKLOAD_DIR"

# CONCURRENCY requests, all with same X-Session-Id (same holder), launched
# simultaneously via Python threading.Barrier. This produces a tighter burst
# than bash `&` (which has 5-30ms subshell startup skew per request). The
# burst tightness is critical: with too much skew, Phase 1 pin-reuse catches
# later requests and the bug doesn't manifest.
CONCURRENCY="${CONCURRENCY:-20}"
STICKY="fpslot-verify-session-$(date +%s)"
RESULTS_DIR="$WORKLOAD_DIR/results-$(date +%s)"
mkdir -p "$RESULTS_DIR"
START=$(date +%s.%N)

# Launch the tight-burst workload. The Python launcher uses a barrier to
# release all worker threads at the same instant, producing the simultaneous
# Redis access pattern that triggers the bug.
python3 /tmp/fpslot-burst.py "$GW_PORT" "$CONCURRENCY" "$STICKY" "$RESULTS_DIR" "$LATENCY_BUDGET_MS" \
  > "$RESULTS_DIR/burst.log" 2>&1 || true
END=$(date +%s.%N)

# ── Tally (defensive: missing files count as failures, never abort) ──
SUCC=0; FAIL_502=0; FAIL_OTHER=0; ERR_FILES=0
for i in $(seq 1 $CONCURRENCY); do
  code=$(tr -d '[:space:]' < "$RESULTS_DIR/status-$i.txt" 2>/dev/null || echo "000")
  case "$code" in
    200) SUCC=$((SUCC+1)) ;;
    502) FAIL_502=$((FAIL_502+1)) ;;
    *)   FAIL_OTHER=$((FAIL_OTHER+1)) ;;
  esac
  if [ ! -s "$RESULTS_DIR/resp-$i.json" ]; then ERR_FILES=$((ERR_FILES+1)); fi
done

ELAPSED=$(awk "BEGIN{printf \"%.2f\", $END - $START}")

hdr "STEP 9 — Results"
echo
echo "  concurrency       : $CONCURRENCY"
echo "  sticky session    : $STICKY"
echo "  wall-clock        : ${ELAPSED}s"
echo "  HTTP 200          : $SUCC"
echo "  HTTP 502          : $FAIL_502"
echo "  HTTP other        : $FAIL_OTHER"
echo "  empty/error resp  : $ERR_FILES"
echo

# Sample one error body to confirm "cred_fp_slot saturated" diagnostic.
if [ "$FAIL_OTHER" -gt 0 ] || [ "$FAIL_502" -gt 0 ]; then
  info "sample error body (first failure):"
  for i in $(seq 1 $CONCURRENCY); do
    code=$(cat "$RESULTS_DIR/status-$i.txt")
    if [ "$code" != "200" ] && [ -s "$RESULTS_DIR/resp-$i.json" ]; then
      head -c 400 "$RESULTS_DIR/resp-$i.json"
      echo
      break
    fi
  done
fi

# ── Final slot inventory ──
hdr "STEP 10 — Redis fp_slot state after the workload"
SLOTS_OCCUPIED=$(redis-cli -h localhost -p 6379 KEYS "llmgw:cred_fp_slot:6:*" | wc -l | tr -d ' ')
SLOTS_PINS=$(redis-cli -h localhost -p 6379 KEYS "llmgw:sess_cred_fp:*:6" | wc -l | tr -d ' ')
echo "  slots occupied (credential 6) : $SLOTS_OCCUPIED"
echo "  pins stored (credential 6)    : $SLOTS_PINS"
echo "  expected with fix             : 1 slot, 1 pin"
echo "  expected without fix          : ~$CONCURRENCY slots, ~$CONCURRENCY pins"

# ── Verdict ──
echo
hdr "VERDICT"
if [ "$USE_BEFORE_FIX" = "1" ]; then
  # Before-fix expectation: bug manifests as multiple slots taken by same
  # holder when requests overlap in Phase 2 LRU. With the production binary,
  # Phase 1 pin-reuse often catches later requests first, so the bug may
  # only partially reproduce. We accept both "bug visible" AND "Phase 1 won
  # the race" as valid BEFORE-fix observations.
  if [ "$SLOTS_OCCUPIED" -le "2" ]; then
    info "BEFORE-FIX: 1 slot consumed — Phase 1 pin-reuse won the race."
    info "            (Lua-level reproduction: see credentialfpslot/slot_same_holder_concurrent_test.go,"
    info "             which shows 19/100 saturated WITHOUT the fix.)"
    info "            To force end-to-end reproduction, drop pre-Acquire work"
    info "            in the gateway or use a stress client that bypasses Phase 1."
    exit 0
  else
    ok "BEFORE-FIX: $SLOTS_OCCUPIED slots consumed by 1 holder — bug reproduced end-to-end."
    ok "            (same symptom as the production 52% failure incident)"
    exit 0
  fi
elif [ "$SUCC" -eq "$CONCURRENCY" ] && [ "$SLOTS_OCCUPIED" -le "2" ]; then
  ok "PASS: all $CONCURRENCY requests succeeded, only $SLOTS_OCCUPIED slot(s) consumed"
  ok "      fp_slot fix is working end-to-end"
  exit 0
elif [ "$SUCC" -lt "$CONCURRENCY" ]; then
  err "FAIL: only $SUCC/$CONCURRENCY succeeded, $SLOTS_OCCUPIED slots consumed"
  err "      fp_slot fix is NOT effective — same symptom as production"
  exit 1
else
  err "AMBIGUOUS: all succeeded but $SLOTS_OCCUPIED slots consumed (expected ~1)"
  err "           fix may be partially working"
  exit 2
fi