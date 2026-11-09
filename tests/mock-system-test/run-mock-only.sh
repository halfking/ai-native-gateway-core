#!/usr/bin/env bash
# =====================================================================
# run-mock-only.sh — mock-only stress stack lifecycle (runs ON the host)
# =====================================================================
# Works on the local Docker host and on 211 unchanged: base images are
# probed from what actually exists locally (211 has no docker.io egress,
# so registry-pinned images are used when present).
#
# Usage:
#   bash tests/mock-system-test/run-mock-only.sh up          # build + start + gates
#   bash tests/mock-system-test/run-mock-only.sh gate        # post-boot assertions only
#   bash tests/mock-system-test/run-mock-only.sh load [--quick] [--scenarios S01,...]
#   bash tests/mock-system-test/run-mock-only.sh smoke       # one E2E request
#   bash tests/mock-system-test/run-mock-only.sh down        # stop (keep volumes)
#   bash tests/mock-system-test/run-mock-only.sh destroy     # stop + delete volumes
#   bash tests/mock-system-test/run-mock-only.sh status
#
# Post-boot gates (all must pass before `load` runs):
#   G1 gateway /healthz 200
#   G2 DB: every provider with code NOT LIKE 'mock-vendor-%' is disabled
#   G3 DB: exactly 4 mock vendors, 12 bindings, all routable
#   G4 static-key E2E: /v1/chat/completions returns a mock vendor reply
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.mock-only.yml"
PROJECT="lgw-mock-only"
GATEWAY_HOST_PORT="${MOCK_GATEWAY_PORT:-18781}"
GATEWAY_URL="http://127.0.0.1:${GATEWAY_HOST_PORT}"
STATIC_KEY="mock-only-gateway-key"

if docker compose version >/dev/null 2>&1; then
  COMPOSE=(docker compose -p "$PROJECT" -f "$COMPOSE_FILE")
else
  COMPOSE=(docker-compose -p "$PROJECT" -f "$COMPOSE_FILE")
fi

c_red=$'\033[0;31m'; c_grn=$'\033[0;32m'; c_yel=$'\033[0;33m'; c_off=$'\033[0m'
say() { printf '%s[stack]%s %s\n' "$c_yel" "$c_off" "$*"; }
ok()  { printf '%s[ok]%s %s\n' "$c_grn" "$c_off" "$*"; }
die() { printf '%s[fatal]%s %s\n' "$c_red" "$c_off" "$*" >&2; exit 1; }

# ── per-host image probing (the 211-safe part) ──────────────────────
image_exists() { docker image inspect "$1" >/dev/null 2>&1; }

pick_env() {
  # A .env file next to the compose file wins: compose feeds it to ${VAR}
  # interpolation itself, so probing must not export overriding shell vars.
  if [[ -f "$SCRIPT_DIR/.env" ]]; then
    say "using $SCRIPT_DIR/.env (host-pinned images)"
    grep -v '^#' "$SCRIPT_DIR/.env" | grep -q '^MOCK_GATEWAY_PORT=' || export MOCK_GATEWAY_PORT="$GATEWAY_HOST_PORT"
    return 0
  fi
  # Only export what is not already set — caller overrides win.
  if [[ -z "${MOCK_PG_IMAGE:-}" ]]; then
    for cand in pgvector/pgvector:pg17 kx-citus-pg17:amd64 kx-citus-pg17:13.3.0-vector-arm64 kx-citus-pg17:13.3.0-vector-amd64 registry.kxpms.cn/kx-citus-pg17:13.3.0-vector-amd64; do
      if image_exists "$cand"; then export MOCK_PG_IMAGE="$cand"; break; fi
    done
  fi
  if [[ -z "${MOCK_REDIS_IMAGE:-}" ]]; then
    for cand in redis:7-alpine kx-redis:7.4.9-amd64 redis:7.4.11-alpine; do
      if image_exists "$cand"; then export MOCK_REDIS_IMAGE="$cand"; break; fi
    done
  fi
  if [[ -z "${MOCK_CLIENTS_IMAGE:-}" ]]; then
    for cand in python:3.12-alpine python:3.14-slim python:3.11-slim; do
      if image_exists "$cand"; then export MOCK_CLIENTS_IMAGE="$cand"; break; fi
    done
  fi
  if [[ -z "${MOCK_GO_IMAGE:-}" ]]; then
    for cand in kx-base/golang:1.27-alpine-arm64 kx-base/golang:1.27-alpine-amd64 \
                registry-211.itestu.cn/lang-base/kx-base-golang:1.27-alpine-amd64 \
                golang:1.27-alpine golang:1.27.1-alpine; do
      if image_exists "$cand"; then export MOCK_GO_IMAGE="$cand"; break; fi
    done
  fi
  if [[ -z "${MOCK_RUNTIME_IMAGE:-}" ]]; then
    for cand in registry-211.itestu.cn/lang-base/alpine:3.22-amd64 alpine:3.22 alpine:3.20 alpine:latest; do
      if image_exists "$cand"; then export MOCK_RUNTIME_IMAGE="$cand"; break; fi
    done
  fi
  for v in MOCK_PG_IMAGE MOCK_REDIS_IMAGE MOCK_CLIENTS_IMAGE MOCK_GO_IMAGE MOCK_RUNTIME_IMAGE; do
    [[ -n "${!v:-}" ]] || die "$v could not be resolved: no candidate base image present locally. Export it explicitly."
    say "$v=${!v}"
  done
  [[ -n "${MOCK_GATEWAY_PORT:-}" ]] || export MOCK_GATEWAY_PORT="$GATEWAY_HOST_PORT"
}

stack_up() {
  pick_env
  say "building + starting mock-only stack (project=$PROJECT)..."
  # Tolerate compose's dependency-failure exit: the PG init and gateway
  # ensure chain both converge across restarts, so up failing on first
  # pass must not abort before wait_gateway's self-heal can run.
  "${COMPOSE[@]}" up -d --build || say "compose up returned nonzero — continuing to self-heal loop"
  wait_gateway
}

wait_gateway() {
  say "waiting for gateway (max 300s)..."
  local i
  for i in $(seq 1 60); do
    # Self-heal: the gateway fail-closes at boot when it races the PG
    # init→final-server switch; a restart re-runs its resumable ensure
    # chain now that PG is stable.
    local state
    state=$(docker inspect -f '{{.State.Status}}' lgw-mock-only-gateway 2>/dev/null || echo unknown)
    if [[ "$state" == "exited" ]]; then
      say "gateway exited — restarting (ensure chain is resumable)"
      docker start lgw-mock-only-gateway >/dev/null 2>&1 || true
    fi
    if curl -sf --max-time 3 "$GATEWAY_URL/healthz" >/dev/null 2>&1; then
      ok "gateway healthy (after ~$((i * 5))s)"
      return 0
    fi
    sleep 5
  done
  echo "---- gateway logs (tail) ----"
  docker logs --tail 60 lgw-mock-only-gateway 2>&1 || true
  die "gateway not healthy after 300s"
}

pg_sql() {
  docker exec lgw-mock-only-pg psql -U mocktest -d llm_gateway_mock -tAc "$1"
}

# normalize_after_boot: the gateway re-seeds catalog-provided LOCAL
# providers (whisper-cpp / macos-say …) with placeholder URLs on every
# boot — they are not part of the mock topology and must be disabled.
# Also re-asserts the mock canonical linkage. Idempotent; safe to run
# before every gate/load.
normalize_after_boot() {
  # -i is mandatory: without it the heredoc never reaches psql and the
  # whole normalization silently no-ops (verified live on 211).
  docker exec -i lgw-mock-only-pg psql -U mocktest -d llm_gateway_mock -v ON_ERROR_STOP=1 <<'EOSQL'
UPDATE providers SET enabled = false, manual_disabled = true, updated_at = now()
WHERE code NOT LIKE 'mock-vendor-%'
  AND (enabled = true OR COALESCE(manual_disabled, false) = false);
UPDATE credentials SET status = 'disabled', manual_disabled = true, updated_at = now()
WHERE provider_id NOT IN (SELECT id FROM providers WHERE code LIKE 'mock-vendor-%')
  AND (status <> 'disabled' OR COALESCE(manual_disabled, false) = false);
INSERT INTO models_canonical (canonical_name, display_name, family, modality, context_window, status, source)
SELECT m.name, m.disp, 'mock', 'text', 131072, 'active', 'db'
FROM (VALUES ('mock-model-fast','Mock Fast'),('mock-model-long','Mock Long'),('mock-model-stream','Mock Stream')) AS m(name, disp)
WHERE NOT EXISTS (SELECT 1 FROM models_canonical mc WHERE mc.canonical_name = m.name);
UPDATE provider_models pm SET canonical_id = mc.id
FROM models_canonical mc
WHERE pm.provider_id BETWEEN 95001 AND 95004
  AND pm.raw_model_name = mc.canonical_name
  AND pm.canonical_id IS DISTINCT FROM mc.id;
EOSQL
}

run_gates() {
  wait_gateway
  normalize_after_boot
  local fails=0

  # G1 health + ready
  if curl -sf --max-time 5 "$GATEWAY_URL/healthz" | grep -q ok; then
    ok "G1 /healthz ok"
  else
    echo "G1 FAILED"; fails=$((fails+1))
  fi

  # G2 non-mock providers disabled
  local nonmock_bad
  nonmock_bad=$(pg_sql "SELECT count(*) FROM providers WHERE code NOT LIKE 'mock-vendor-%' AND (enabled = true OR COALESCE(manual_disabled,false) = false);")
  local nonmock_cred_bad
  nonmock_cred_bad=$(pg_sql "SELECT count(*) FROM credentials WHERE provider_id NOT IN (SELECT id FROM providers WHERE code LIKE 'mock-vendor-%') AND (status <> 'disabled' OR COALESCE(manual_disabled,false) = false);")
  if [[ "$nonmock_bad" == "0" && "$nonmock_cred_bad" == "0" ]]; then
    ok "G2 all non-mock providers/credentials disabled (providers_bad=$nonmock_bad creds_bad=$nonmock_cred_bad)"
  else
    echo "G2 FAILED: nonmock providers enabled=$nonmock_bad credentials active=$nonmock_cred_bad"
    fails=$((fails+1))
  fi

  # G3 mock topology counts
  local prov cred bind routable
  prov=$(pg_sql "SELECT count(*) FROM providers WHERE code LIKE 'mock-vendor-%';")
  cred=$(pg_sql "SELECT count(*) FROM credentials WHERE id BETWEEN 95001 AND 95004;")
  bind=$(pg_sql "SELECT count(*) FROM credential_model_bindings WHERE credential_id BETWEEN 95001 AND 95004;")
  routable=$(pg_sql "SELECT count(*) FROM v_routable_credential_models WHERE credential_id BETWEEN 95001 AND 95004 AND is_routable;")
  if [[ "$prov" == "4" && "$cred" == "4" && "$bind" == "12" && "$routable" == "12" ]]; then
    ok "G3 mock topology: providers=4 credentials=4 bindings=12 routable=12"
  else
    echo "G3 FAILED: providers=$prov credentials=$cred bindings=$bind routable=$routable"
    fails=$((fails+1))
  fi

  # G4 static-key E2E
  local body
  body=$(curl -sf --max-time 30 -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $STATIC_KEY" -H 'Content-Type: application/json' \
    -d '{"model":"mock-model-fast","messages":[{"role":"user","content":"gate-g4"}],"max_tokens":32}' || true)
  if echo "$body" | grep -q 'mock-vendor-'; then
    ok "G4 static-key E2E: $(echo "$body" | jq -r '._mock_identity // .id' 2>/dev/null || echo vendor-ok)"
  else
    echo "G4 FAILED: body=$(echo "$body" | head -c 400)"
    fails=$((fails+1))
  fi

  # migration failure ledger surfaced (non-fatal, recorded)
  local ledger
  ledger=$(docker exec lgw-mock-only-pg sh -c 'cat /tmp/mock-only-migration-failures.txt 2>/dev/null || true' || true)
  local ledger_n=0
  [[ -n "$ledger" ]] && ledger_n=$(echo "$ledger" | grep -c '^=== ' || true)
  if [[ "$ledger_n" -gt 0 ]]; then
    say "migration failure ledger: $ledger_n entries (recorded; see loadtest report)"
    echo "$ledger" > "$SCRIPT_DIR/reports/migration-failures.txt" 2>/dev/null || \
      mkdir -p "$SCRIPT_DIR/reports" && echo "$ledger" > "$SCRIPT_DIR/reports/migration-failures.txt"
  else
    ok "migration ledger: 0 failures"
  fi

  if [[ "$fails" -gt 0 ]]; then
    die "$fails gate(s) failed"
  fi
  ok "all gates passed"
}

stack_smoke() {
  local body
  body=$(curl -sf --max-time 30 -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $STATIC_KEY" -H 'Content-Type: application/json' \
    -d '{"model":"mock-model-stream","messages":[{"role":"user","content":"smoke"}],"max_tokens":48,"stream":true}' || true)
  if echo "$body" | grep -q 'data: \[DONE\]'; then
    ok "stream smoke: [DONE] received"
  else
    die "stream smoke failed: $(echo "$body" | head -c 300)"
  fi
}

stack_load() {
  wait_gateway >/dev/null 2>&1 || true
  normalize_after_boot
  local -a args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --quick) args+=(--quick); shift ;;
      --scenarios) args+=(--scenarios "$2"); shift 2 ;;
      *) die "unknown load arg: $1" ;;
    esac
  done
  say "running load test inside clients container..."
  docker exec lgw-mock-only-clients python3 /workspace/mock_only_loadtest.py "${args[@]+"${args[@]}"}"
}

stack_down() { "${COMPOSE[@]}" down --remove-orphans; ok "stack down (volumes kept)"; }
stack_destroy() { "${COMPOSE[@]}" down -v --remove-orphans; ok "stack destroyed (volumes removed)"; }
stack_status() { "${COMPOSE[@]}" ps; }

case "${1:-status}" in
  up)      stack_up; run_gates; stack_smoke ;;
  gate)    run_gates ;;
  smoke)   stack_smoke ;;
  load)    shift; stack_load "$@" ;;
  down)    stack_down ;;
  destroy) stack_destroy ;;
  status)  stack_status ;;
  *)
    sed -n '2,30p' "$0"; exit 1 ;;
esac
