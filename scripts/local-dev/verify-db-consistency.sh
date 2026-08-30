#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/verify-db-consistency.sh
# Purpose:       Compare the llm-gateway PostgreSQL schema (table inventory +
#                column-level structure) between 252 (test) and the local
#                docker container (llm-gateway-pg), and optionally reconcile
#                schema drift by pushing local feature-table DDL to 252.
# Status:        active
# Changelog:
#   2026-08-31  v1.0  Initial version (table inventory + structure diff,
#                      gated --reconcile mode). Captures the 252<-local
#                      reconciliation lessons from the 2026-08-31 audit.
# -----------------------------------------------------------------------------
# Usage:
#   # Read-only consistency check (sets up + tears down the 252 tunnel):
#   bash scripts/local-dev/verify-db-consistency.sh
#   bash scripts/local-dev/verify-db-consistency.sh --verify
#
#   # Push local feature-table DDL to 252 (requires --yes, gated):
#   bash scripts/local-dev/verify-db-consistency.sh --reconcile agent_discovery proxy_nodes --yes
# -----------------------------------------------------------------------------
# Preconditions:
#   - envs loader at ~/workspace/ai-native-tools/envs/loader.sh
#   - configs/env-252.sh and configs/env-local.sh present
#   - llm-gateway-pg docker container running locally
#   - SSH access to 252 configured (ssh-config-auth or injected SSH_PASS_252)
# -----------------------------------------------------------------------------

set -euo pipefail

# ── Colors ────────────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; C='\033[0;36m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}⚠${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}════════════════════════════════════════════════════════════${N}"; echo -e "${B}  $*${N}"; echo -e "${B}════════════════════════════════════════════════════════════${N}"; }

# ── Config ────────────────────────────────────────────────────────────────
ENVS_LOADER="$HOME/workspace/ai-native-tools/envs/loader.sh"
PROJECT="llm-gateway-go"
LOCAL_CONTAINER="llm-gateway-pg"
LOCAL_DB="llm_gateway"
LOCAL_USER="llm_gateway"
WORK_DIR="/tmp/verify-db-consistency"
MODE="verify"
RECONCILE_TABLES=()
RECONCILE_YES=false

# Hot-table patterns: 252-only tables matching these are EXPECTED drift
# (monthly partitions / hot windows), not a real inconsistency.
HOT_PATTERNS='*_hot *_2026_* *_2027_* *_archived *_archive'

mkdir -p "$WORK_DIR"

# ── Parse args ────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --verify)    MODE="verify"; shift ;;
    --reconcile) MODE="reconcile"; shift
      while [[ $# -gt 0 && "$1" != --* ]]; do RECONCILE_TABLES+=("$1"); shift; done ;;
    --yes)       RECONCILE_YES=true; shift ;;
    -h|--help)
      grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) err "unknown arg: $1"; exit 1 ;;
  esac
done

# ── Load envs (credentials + tunnel target) ───────────────────────────────
load_envs() {
  if [[ ! -f "$ENVS_LOADER" ]]; then err "envs loader not found: $ENVS_LOADER"; exit 1; fi
  # shellcheck disable=SC1090
  source "$ENVS_LOADER" --project "$PROJECT" 2>/dev/null
  export PG_PASS_252="${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded}"
  export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
  # shellcheck disable=SC1090
  source configs/env-252.sh
  # Local docker container uses the SAME superuser password as 252 (synced by
  # recreate-llm-gateway-pg.sh), so we drive local access with PG_PASS_252 and
  # do NOT need configs/env-local.sh (which requires PG_PASS_LOCAL).
}

# ── Tunnel management ──────────────────────────────────────────────────────
TUNNEL_PID=""
ensure_tunnel() {
  # Reuse an existing listener on TUNNEL_LOCAL_PORT if present.
  if PGPASSWORD="$PG_PASS_252" psql -h localhost -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway -tAc 'SELECT 1' >/dev/null 2>&1; then
    info "reusing existing tunnel on localhost:$TUNNEL_LOCAL_PORT"
    return 0
  fi
  ssh -f -N -L "$TUNNEL_LOCAL_PORT:$TUNNEL_REMOTE_TARGET" 252 2>&1
  sleep 3
  if ! PGPASSWORD="$PG_PASS_252" psql -h localhost -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway -tAc 'SELECT 1' >/dev/null 2>&1; then
    err "cannot reach 252 via tunnel (localhost:$TUNNEL_LOCAL_PORT)"; exit 1
  fi
  TUNNEL_PID=$(lsof -tiTCP:"$TUNNEL_LOCAL_PORT" -sTCP:LISTEN 2>/dev/null | head -1)
  ok "tunnel up (localhost:$TUNNEL_LOCAL_PORT -> $TUNNEL_REMOTE_TARGET)"
}

teardown_tunnel() {
  if [[ -n "$TUNNEL_PID" ]]; then
    kill "$TUNNEL_PID" 2>/dev/null && info "tunnel torn down ($TUNNEL_PID)"
  fi
}

# ── Inventory pulls ─────────────────────────────────────────────────────────
pull_inventories() {
  PGPASSWORD="$PG_PASS_252" psql -h localhost -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway -tAc \
    "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename" > "$WORK_DIR/252.txt"
  docker exec -e PGPASSWORD="$PG_PASS_252" "$LOCAL_CONTAINER" psql -U "$LOCAL_USER" -d "$LOCAL_DB" -tAc \
    "SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT LIKE '\_%' ESCAPE '\\' ORDER BY tablename" > "$WORK_DIR/local.txt"
  ok "252 tables: $(wc -l < "$WORK_DIR/252.txt") | local tables (excl _): $(wc -l < "$WORK_DIR/local.txt")"
}

# ── Classification helpers ─────────────────────────────────────────────────
is_hot_pattern() {
  local name="$1" p
  for p in $HOT_PATTERNS; do
    case "$name" in
      $p) return 0 ;;
    esac
  done
  return 1
}

# ── Verify mode ────────────────────────────────────────────────────────────
do_verify() {
  phase "Table inventory diff (252 vs local)"
  local drift=0 expected=0
  local only252 onlylocal
  only252=$(comm -23 "$WORK_DIR/252.txt" "$WORK_DIR/local.txt")
  onlylocal=$(comm -13 "$WORK_DIR/252.txt" "$WORK_DIR/local.txt")

  # Process substitution (not a pipe) so counter updates persist in this shell.
  if [[ -n "$only252" ]]; then
    while IFS= read -r t; do
      if is_hot_pattern "$t"; then warn "252-only (EXPECTED hot/partition): $t"; expected=$((expected+1));
      else err "252-only (REAL DRIFT): $t"; drift=$((drift+1)); fi
    done < <(printf '%s\n' "$only252")
  else
    ok "no 252-only tables"
  fi

  if [[ -z "$onlylocal" ]]; then
    ok "no local-only tables — inventories match"
  else
    while IFS= read -r t; do
      [[ -z "$t" ]] && continue
      err "local-only (REAL DRIFT): $t"; drift=$((drift+1))
    done < <(printf '%s\n' "$onlylocal")
  fi

  # Column-level structure diff for any local-only tables (to aid reconciliation)
  if [[ -n "$onlylocal" ]]; then
    phase "Column-level structure diff for local-only tables"
    while IFS= read -r t; do
      [[ -z "$t" ]] && continue
      PGPASSWORD="$PG_PASS_252" psql -h localhost -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway -tAc \
        "SELECT column_name||':'||data_type||':'||is_nullable FROM information_schema.columns WHERE table_schema='public' AND table_name='$t' ORDER BY ordinal_position" 2>/dev/null > "$WORK_DIR/_c252.txt" || true
      docker exec -e PGPASSWORD="$PG_PASS_252" "$LOCAL_CONTAINER" psql -U "$LOCAL_USER" -d "$LOCAL_DB" -tAc \
        "SELECT column_name||':'||data_type||':'||is_nullable FROM information_schema.columns WHERE table_schema='public' AND table_name='$t' ORDER BY ordinal_position" > "$WORK_DIR/_clocal.txt"
      if diff -q <(sort "$WORK_DIR/_c252.txt") <(sort "$WORK_DIR/_clocal.txt") >/dev/null 2>&1; then
        ok "$t: structure identical on local (252 lacks the table)"
      else
        warn "$t: differs — see diff below"; diff <(sort "$WORK_DIR/_c252.txt") <(sort "$WORK_DIR/_clocal.txt") | head
      fi
    done < <(printf '%s\n' "$onlylocal")
  fi

  phase "Verdict"
  if [[ "$drift" -eq 0 ]]; then
    ok "CONSISTENT — only expected hot/partition tables differ (if any)."
    return 0
  else
    err "INCONSISTENT — $drift real drift table(s) found (see above)."
    return 1
  fi
}

# ── Reconcile mode: push local feature-table DDL to 252 ────────────────────
do_reconcile() {
  if [[ ${#RECONCILE_TABLES[@]} -eq 0 ]]; then err "--reconcile needs at least one table name"; exit 1; fi
  if ! $RECONCILE_YES; then
    err "refusing to apply DDL without --yes (this modifies 252). Re-run with --yes to confirm."
    exit 1
  fi
  phase "Reconcile: push local DDL -> 252 for: ${RECONCILE_TABLES[*]}"

  # Order tables so FK parents precede children (best-effort: keep given order,
  # but dump each table's DDL so FKs resolve if parents are also listed).
  local ddl="$WORK_DIR/reconcile.sql"
  : > "$ddl"
  for t in "${RECONCILE_TABLES[@]}"; do
    # Verify the table exists locally first.
    if ! docker exec -e PGPASSWORD="$PG_PASS_252" "$LOCAL_CONTAINER" psql -U "$LOCAL_USER" -d "$LOCAL_DB" -tAc \
         "SELECT 1 FROM pg_tables WHERE schemaname='public' AND tablename='$t'" | grep -q 1; then
      err "table not found locally: $t — skipping"; continue
    fi
    docker exec -e PGPASSWORD="$PG_PASS_252" "$LOCAL_CONTAINER" psql -U "$LOCAL_USER" -d "$LOCAL_DB" -tAc \
      "SELECT 1" >/dev/null 2>&1
    docker exec -e PGPASSWORD="$PG_PASS_252" "$LOCAL_CONTAINER" \
      pg_dump -U "$LOCAL_USER" -d "$LOCAL_DB" --schema-only --clean --if-exists --no-owner --no-privileges -t "$t" \
      >> "$ddl" 2>"$WORK_DIR/reconcile.err"
  done

  # CRITICAL: pg_dump emits `SET search_path=''` for security. On 252 this breaks
  # the `enforce_columnar_trigger` event trigger (unqualified function lookup fails).
  # Everything in the dump is already schema-qualified as public.*, so strip it.
  grep -vE "set_config\('search_path', '', false\)" "$ddl" > "$ddl.fixed"

  info "applying $(wc -l < "$ddl.fixed") lines of DDL to 252 (localhost:$TUNNEL_LOCAL_PORT)..."
  if PGPASSWORD="$PG_PASS_252" psql -h localhost -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -f "$ddl.fixed" > "$WORK_DIR/reconcile.out" 2>"$WORK_DIR/reconcile.psqlerr"; then
    ok "DDL applied to 252"
  else
    err "DDL apply FAILED — see $WORK_DIR/reconcile.psqlerr"; cat "$WORK_DIR/reconcile.psqlerr"; exit 1
  fi

  info "re-verifying consistency..."
  pull_inventories
  do_verify
}

# ── Main ───────────────────────────────────────────────────────────────────
trap teardown_tunnel EXIT
load_envs
ensure_tunnel
pull_inventories

case "$MODE" in
  verify)   do_verify ;;
  reconcile) do_reconcile ;;
esac
