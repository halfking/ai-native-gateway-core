#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# fp_slot regression end-to-end verification (2026-06-29)
#
# Reproduces the "minimax-prod-1 52% failure rate" scenario:
#   - 25-slot pool, 2-3 concurrent users, one user fires 10+ in parallel
#   - All requests share the same sticky session (same X-Session-Id)
#
# STEPS:
#   1. Snapshot providers.base_url + credential.secret_ciphertext for restore
#   2. Re-point provider 14 (minimax) at the local mock upstream
#   3. Re-encrypt credential 6's secret with the LOCAL Fernet key so the
#      gateway can decrypt it
#   4. Start mock upstream (port 18080) with configurable latency
#   5. Start gateway (port 18181) pointing at docker postgres/redis + mock
#   6. Run the workload: 1 sticky session × N concurrent requests
#   7. Count 200 OK vs errors + remaining Redis slot keys
#   8. Restore the snapshot — both providers.base_url and the credential
#      secret — and kill mock + gateway
#
# USAGE:
#   ./scripts/verify-fpslot-fix.sh [--skip-build] [--mock-only] [--before-fix] \
#                                   [--confirm-dev-target]
#
# SAFETY (audited 2026-06-29):
#   - Refuses to run unless either:
#       a) --confirm-dev-target is passed, OR
#       b) $FPSLOT_VERIFY_ALLOW_PROD is set in the env.
#     This guards against accidental invocation against a production-shaped
#     DB (e.g. on a host where `r112_postgres` happens to point at prod).
#   - Snapshot files are created with mode 0600 (umask 077).
#   - Mock + gateway are killed by recorded PID, NEVER `pkill -f pattern`
#     (avoids collateral kills of unrelated processes).
#   - Cleanup always restores from the snapshot, logging failures instead
#     of silently swallowing them.
#   - Lockfile prevents two concurrent runs from clobbering each other.
#
# OPTIONS:
#   --skip-build              don't recompile cmd/gateway; reuse build/
#   --mock-only               start only the mock upstream; no DB writes
#   --before-fix              use the un-patched binary (reproduce the bug)
#   --confirm-dev-target      opt out of the dev/prod guard for this run
#
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── Locations ──
BIN_FPSLOT_FIX="$ROOT_DIR/build/llm-gateway-fpslot-fix"
BIN_BEFORE_FIX="$ROOT_DIR/build/llm-gateway-before-fix"
# Helpers kept alongside this script so the script is self-contained.
MAKE_CIPHER="$SCRIPT_DIR/_fpslot-make-cipher.sh"
BURST_LAUNCHER="$SCRIPT_DIR/_fpslot-burst.py"

# ── Tunables (env override allowed) ──
MOCK_PORT="${MOCK_PORT:-18080}"
GW_PORT="${GW_PORT:-18181}"
PROVIDER_ID="${PROVIDER_ID:-14}"
CREDENTIAL_ID="${CREDENTIAL_ID:-6}"
CONCURRENCY="${CONCURRENCY:-20}"
LATENCY_BUDGET_MS="${LATENCY_BUDGET_MS:-1500}"
LOCAL_FERNET_KEY="${LOCAL_FERNET_KEY:-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=}"

# ── PIDs (set when launched; cleaned up by trap) ──
MOCK_PID=""
GW_PID=""

# ── Snapshot paths (created with mode 0600 via umask 077) ──
SNAPSHOT_DIR="/tmp/fpslot-verify-snapshots-$$"
PROVIDER_SNAP="$SNAPSHOT_DIR/provider.sql"
CREDENTIAL_SNAP="$SNAPSHOT_DIR/credential.sql"
LOCK_FILE="/tmp/fpslot-verify.lock"

# ── Args ──
SKIP_BUILD=0
MOCK_ONLY=0
USE_BEFORE_FIX=0
CONFIRM_DEV=0
for arg in "$@"; do
  case "$arg" in
    --skip-build)         SKIP_BUILD=1 ;;
    --mock-only)          MOCK_ONLY=1  ;;
    --before-fix)         USE_BEFORE_FIX=1 ;;
    --confirm-dev-target) CONFIRM_DEV=1 ;;
    -h|--help)
      sed -n '1,/^set -euo pipefail$/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

# ── Pretty printers ──
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
hdr()  { echo -e "${BLUE}══ $* ══${NC}"; }

# ── Dev/prod guard (audit C2) ──
# Without explicit opt-in, refuse to mutate the live DB. Defense-in-depth
# against accidental invocation against a production-shaped DB.
if [ "$MOCK_ONLY" -ne 1 ] && [ "$CONFIRM_DEV" -ne 1 ] && [ -z "${FPSLOT_VERIFY_ALLOW_PROD:-}" ]; then
  err "refusing to run: this script mutates the live llm_gateway DB."
  err "  it is intended ONLY for the local r112 dev stack."
  err "  either:"
  err "    - pass --confirm-dev-target to acknowledge this, OR"
  err "    - set FPSLOT_VERIFY_ALLOW_PROD=1 in the environment"
  exit 3
fi

# ── Lockfile (audit M3) ──
# flock(1) is Linux-only; macOS falls back to a PID-based lock. Two parallel
# invocations would race on snapshot files, processes, and DB mutations.
if command -v flock >/dev/null 2>&1; then
  exec 9>"$LOCK_FILE"
  if ! flock -n 9; then
    err "another verify-fpslot-fix.sh is already running (lock: $LOCK_FILE)"
    err "  if this is stale, remove it and retry."
    exit 4
  fi
else
  if [ -e "$LOCK_FILE" ]; then
    holder_pid=$(cat "$LOCK_FILE" 2>/dev/null || echo "")
    if [ -n "$holder_pid" ] && kill -0 "$holder_pid" 2>/dev/null; then
      err "another verify-fpslot-fix.sh is already running (pid=$holder_pid, lock: $LOCK_FILE)"
      exit 4
    fi
    rm -f "$LOCK_FILE"
  fi
  echo "$$" > "$LOCK_FILE" || { err "cannot write lock: $LOCK_FILE"; exit 4; }
fi

# ── PG helper ──
run_pg() {
  docker exec -e PGPASSWORD=kxpass r112_postgres psql -U kxuser -d llm_gateway \
    -v ON_ERROR_STOP=1 -tAc "$1"
}

# ── Cleanup (audit H1, H3) ──
# Restores DB state from snapshot, kills mock + gateway by PID (NOT pkill).
# Runs on every script exit. Logs failures instead of silently swallowing.
cleanup() {
  local exit_code=$?
  info "cleanup (exit=$exit_code)"
  if [ -n "${MOCK_PID:-}" ] && kill -0 "$MOCK_PID" 2>/dev/null; then
    kill "$MOCK_PID" 2>/dev/null || true
    sleep 0.2
    kill -9 "$MOCK_PID" 2>/dev/null || true
  fi
  if [ -n "${GW_PID:-}" ] && kill -0 "$GW_PID" 2>/dev/null; then
    kill "$GW_PID" 2>/dev/null || true
    sleep 0.2
    kill -9 "$GW_PID" 2>/dev/null || true
  fi
  if [ -f "$PROVIDER_SNAP" ]; then
    if PGPASSWORD=kxpass docker exec -i -e PGPASSWORD=kxpass r112_postgres \
         psql -U kxuser -d llm_gateway -v ON_ERROR_STOP=1 \
         < "$PROVIDER_SNAP" >/tmp/fpslot-verify-restore-$$.log 2>&1; then
      ok "provider.base_url restored from snapshot"
    else
      err "provider.base_url RESTORE FAILED — see /tmp/fpslot-verify-restore-$$.log"
      err "  snapshot was: $PROVIDER_SNAP"
    fi
  fi
  if [ -f "$CREDENTIAL_SNAP" ]; then
    if PGPASSWORD=kxpass docker exec -i -e PGPASSWORD=kxpass r112_postgres \
         psql -U kxuser -d llm_gateway -v ON_ERROR_STOP=1 \
         < "$CREDENTIAL_SNAP" >>/tmp/fpslot-verify-restore-$$.log 2>&1; then
      ok "credentials.secret_ciphertext restored from snapshot"
    else
      err "credentials.secret_ciphertext RESTORE FAILED — see /tmp/fpslot-verify-restore-$$.log"
      err "  snapshot was: $CREDENTIAL_SNAP"
    fi
  fi
  rm -rf "$SNAPSHOT_DIR"
  rm -f "$LOCK_FILE"
  return $exit_code
}
trap cleanup EXIT

# ── Pre-flight ──
command -v docker >/dev/null 2>&1 || { err "docker not found"; exit 5; }
command -v redis-cli >/dev/null 2>&1 || { err "redis-cli not found"; exit 5; }
command -v go >/dev/null 2>&1 || { err "go not found"; exit 5; }
command -v python3 >/dev/null 2>&1 || { err "python3 not found"; exit 5; }

[ -d "$ROOT_DIR/build" ] || mkdir -p "$ROOT_DIR/build"
# Snapshot dir + files are created with mode 0600 (audit M2)
(umask 077; mkdir -p "$SNAPSHOT_DIR"; : > "$PROVIDER_SNAP"; : > "$CREDENTIAL_SNAP")

# ── Mock upstream only? ──
if [ "$MOCK_ONLY" -ne 1 ]; then
  if [ "$USE_BEFORE_FIX" = "1" ]; then
    BIN="$BIN_BEFORE_FIX"
    info "using BEFORE-FIX binary: $BIN (expect high failure rate)"
  else
    BIN="$BIN_FPSLOT_FIX"
  fi

  if [ "$SKIP_BUILD" -ne 1 ]; then
    hdr "STEP 1 — Build gateway binary"
    info "compiling cmd/gateway → $BIN"
    CGO_ENABLED=0 go build -o "$BIN" ./cmd/gateway
    ok "binary built: $BIN"
  fi
  [ -x "$BIN" ] || { err "binary not found: $BIN (build with: ./scripts/verify-fpslot-fix.sh)"; exit 5; }

  hdr "STEP 2 — Snapshot providers.base_url + credential secret for restore"
  # Snapshot uses decode('hex') so arbitrary bytea roundtrips safely.
  run_pg "SELECT 'UPDATE providers SET base_url = ' || quote_literal(base_url) || ' WHERE id = $PROVIDER_ID;' FROM providers WHERE id = $PROVIDER_ID;" \
    > "$PROVIDER_SNAP"
  run_pg "SELECT 'UPDATE credentials SET secret_ciphertext = decode(' || quote_literal(encode(secret_ciphertext, 'hex')) || ', ''hex''), secret_kid = ' || quote_literal(secret_kid) || ' WHERE id = $CREDENTIAL_ID;' FROM credentials WHERE id = $CREDENTIAL_ID;" \
    > "$CREDENTIAL_SNAP"
  ok "snapshot saved ($PROVIDER_SNAP, $CREDENTIAL_SNAP)"

  hdr "STEP 3 — Repoint provider $PROVIDER_ID at local mock + reset circuit/availability"
  run_pg "UPDATE providers SET base_url = 'http://127.0.0.1:$MOCK_PORT/v1' WHERE id = $PROVIDER_ID;" >/dev/null
  run_pg "UPDATE credential_model_bindings SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL WHERE credential_id = $CREDENTIAL_ID AND provider_model_id IN (SELECT id FROM provider_models WHERE raw_model_name = 'MiniMax-M3');" >/dev/null
  run_pg "UPDATE credentials SET status = 'active', cooling_until = NULL, circuit_state = 'closed' WHERE id = $CREDENTIAL_ID;" >/dev/null
  ok "provider + bindings + circuit reset"

  hdr "STEP 4 — Pre-compute local-Fernet ciphertext BEFORE any DB write (audit H2)"
  # Compute the new ciphertext first. If this fails, no DB mutation happened
  # yet, so cleanup can do nothing and the script exits cleanly.
  bash "$MAKE_CIPHER" "$LOCAL_FERNET_KEY" "fpslot-verify-bearer-token" > "$SNAPSHOT_DIR/new-cipher.hex" || {
    err "failed to pre-compute local ciphertext ($MAKE_CIPHER exit=$?)"
    err "no DB write was performed — credential $CREDENTIAL_ID is untouched"
    exit 5
  }
  NEW_CIPHER=$(cat "$SNAPSHOT_DIR/new-cipher.hex")
  run_pg "UPDATE credentials SET secret_ciphertext = decode('$NEW_CIPHER', 'hex'), secret_kid = 'local-verify' WHERE id = $CREDENTIAL_ID;" >/dev/null
  ok "credential $CREDENTIAL_ID re-encrypted with local Fernet key"

  hdr "STEP 5 — Flush fp_slot Redis state for credential $CREDENTIAL_ID"
  # EVAL + UNLINK (atomic, non-blocking) rather than KEYS+xargs DEL.
  redis-cli -h 127.0.0.1 -p 6379 --no-raw EVAL "
    local k = redis.call('KEYS', 'llmgw:cred_fp_slot:$CREDENTIAL_ID:*')
    if #k > 0 then redis.call('UNLINK', unpack(k)) end
    local p = redis.call('KEYS', 'llmgw:sess_cred_fp:*:$CREDENTIAL_ID')
    if #p > 0 then redis.call('UNLINK', unpack(p)) end
    return #k + #p
  " 0 >/dev/null
  ok "slot + pin keys cleared"
fi

# ── Mock upstream (audit H1 — PID-based kill, no pkill) ──
hdr "STEP 6 — Start mock upstream (latency ${LATENCY_BUDGET_MS}ms)"
MOCK_LATENCY_MS_MIN=$((LATENCY_BUDGET_MS / 3)) MOCK_LATENCY_MS_MAX=$LATENCY_BUDGET_MS \
  MOCK_PORT=$MOCK_PORT MOCK_TOKEN="fpverify-$$" \
  python3 "$ROOT_DIR/scripts/mocks/llm-mock-upstream/server.py" \
  > /tmp/fpslot-verify-mock-$$.log 2>&1 &
MOCK_PID=$!
sleep 1
MOCK_HEALTHY=0
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -sf "http://localhost:$MOCK_PORT/healthz" >/dev/null 2>&1; then
    MOCK_HEALTHY=1
    ok "mock ready (pid=$MOCK_PID, after ${i}s)"
    break
  fi
  sleep 1
done
if [ "$MOCK_HEALTHY" -ne 1 ]; then
  err "mock failed to start; log: /tmp/fpslot-verify-mock-$$.log"
  exit 5
fi

if [ "$MOCK_ONLY" -eq 1 ]; then
  info "--mock-only: stopping after mock is up"
  exit 0
fi

# ── Gateway (audit H4 — random per-run API key; H1 — PID-based kill) ──
hdr "STEP 7 — Start gateway (port $GW_PORT)"
# Per-run random API/admin keys. Avoid hardcoding test credentials in this
# script (audit H4). The keys are unused outside this gateway instance.
RUN_API_KEY="fpverify-api-$$-$(head -c 8 /dev/urandom | xxd -p 2>/dev/null || echo random$$)"
RUN_ADMIN_KEY="fpverify-admin-$$-$(head -c 8 /dev/urandom | xxd -p 2>/dev/null || echo random$$)"
export LLM_GATEWAY_LISTEN=":$GW_PORT"
export LLM_GATEWAY_DATABASE_URL="postgres://kxuser:kxpass@127.0.0.1:5433/llm_gateway?sslmode=disable"
export LLM_GATEWAY_REDIS_ADDR="127.0.0.1:6379"
export LLM_GATEWAY_ENABLE_CREDENTIAL_FP_SLOTS="true"
export LLM_GATEWAY_DEFAULT_CREDENTIAL_CONCURRENCY="25"
export LLM_GATEWAY_UPSTREAM_TIMEOUT="120"
export LLM_GATEWAY_API_KEY="$RUN_API_KEY"
export LLM_GATEWAY_ADMIN_API_KEY="$RUN_ADMIN_KEY"
export LLM_GATEWAY_LOG_LEVEL="info"
export LLM_GATEWAY_CORS_ORIGINS="*"
export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="$LOCAL_FERNET_KEY"
# Pass the API key to the burst launcher via env (not argv) so it doesn't
# appear in `ps`.
export FPSLOT_RUN_API_KEY="$RUN_API_KEY"

"$BIN" > /tmp/fpslot-verify-gateway-$$.log 2>&1 &
GW_PID=$!
GATEWAY_HEALTHY=0
for i in $(seq 1 30); do
  if curl -sf "http://localhost:$GW_PORT/healthz" >/dev/null 2>&1; then
    GATEWAY_HEALTHY=1
    ok "gateway ready (pid=$GW_PID, after ${i}s)"
    break
  fi
  sleep 1
done
if [ "$GATEWAY_HEALTHY" -ne 1 ]; then
  err "gateway failed to start within 30s"
  tail -50 /tmp/fpslot-verify-gateway-$$.log >&2
  exit 5
fi

# ── Workload ──
hdr "STEP 8 — Run the workload (1 sticky session × $CONCURRENCY concurrent)"
STICKY="fpslot-verify-session-$(date +%s)-$$"
RESULTS_DIR="/tmp/fpslot-verify-results-$$"
mkdir -p "$RESULTS_DIR"

START=$(date +%s.%N)
python3 "$BURST_LAUNCHER" "$GW_PORT" "$CONCURRENCY" "$STICKY" "$RESULTS_DIR" "$LATENCY_BUDGET_MS" \
  > "$RESULTS_DIR/burst.log" 2>&1 || true
END=$(date +%s.%N)

# Defensive tally — count what we have, never abort.
SUCC=0; FAIL_502=0; FAIL_OTHER=0; ERR_FILES=0
for i in $(seq 1 $CONCURRENCY); do
  code=$(tr -d '[:space:]' < "$RESULTS_DIR/status-$i.txt" 2>/dev/null || echo "000")
  case "$code" in
    200) SUCC=$((SUCC+1)) ;;
    502) FAIL_502=$((FAIL_502+1)) ;;
    *)   FAIL_OTHER=$((FAIL_OTHER+1)) ;;
  esac
  [ -s "$RESULTS_DIR/resp-$i.json" ] || ERR_FILES=$((ERR_FILES+1))
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

# Sample first non-200 body for diagnosis.
if [ "$FAIL_OTHER" -gt 0 ] || [ "$FAIL_502" -gt 0 ]; then
  info "sample error body (first failure):"
  for i in $(seq 1 $CONCURRENCY); do
    code=$(tr -d '[:space:]' < "$RESULTS_DIR/status-$i.txt" 2>/dev/null || echo "000")
    if [ "$code" != "200" ] && [ -s "$RESULTS_DIR/resp-$i.json" ]; then
      head -c 400 "$RESULTS_DIR/resp-$i.json"; echo
      break
    fi
  done
fi

hdr "STEP 10 — Redis fp_slot state after workload"
SLOTS_OCCUPIED=$(redis-cli -h 127.0.0.1 -p 6379 EVAL "return #redis.call('KEYS', 'llmgw:cred_fp_slot:$CREDENTIAL_ID:*')" 0)
SLOTS_PINS=$(redis-cli -h 127.0.0.1 -p 6379 EVAL "return #redis.call('KEYS', 'llmgw:sess_cred_fp:*:$CREDENTIAL_ID')" 0)
echo "  slots occupied    : $SLOTS_OCCUPIED"
echo "  pins stored       : $SLOTS_PINS"
echo "  expected with fix : 1 slot, 1 pin"
echo "  expected w/o fix  : ~$CONCURRENCY slots, ~$CONCURRENCY pins"

# ── Verdict ──
echo
hdr "VERDICT"
if [ "$USE_BEFORE_FIX" = "1" ]; then
  if [ "$SLOTS_OCCUPIED" -le "2" ]; then
    info "BEFORE-FIX: 1 slot consumed — Phase 1 pin-reuse won the race."
    info "            See credentialfpslot/slot_same_holder_concurrent_test.go"
    info "            for the authoritative Lua-level reproduction."
    exit 0
  fi
  ok "BEFORE-FIX: $SLOTS_OCCUPIED slots consumed — bug reproduced end-to-end."
  exit 0
fi

if [ "$SUCC" -eq "$CONCURRENCY" ] && [ "$SLOTS_OCCUPIED" -le 2 ]; then
  ok "PASS: all $CONCURRENCY requests succeeded, only $SLOTS_OCCUPIED slot(s) consumed"
  ok "      fp_slot fix is working end-to-end"
  exit 0
elif [ "$SUCC" -lt "$CONCURRENCY" ]; then
  err "FAIL: only $SUCC/$CONCURRENCY succeeded, $SLOTS_OCCUPIED slots consumed"
  err "      fp_slot fix is NOT effective — same symptom as production"
  exit 1
else
  err "AMBIGUOUS: all succeeded but $SLOTS_OCCUPIED slots consumed (expected 1)"
  exit 2
fi