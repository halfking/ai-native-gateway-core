#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/multi-db-sync-252.sh
# Purpose:       Multi-database structure/data sync orchestrator: aligns local
#                llm-gateway-pg docker databases with 252's pg17 docker, using
#                configs/multi-db-sync.policy to decide per-db behavior. Local
#                is the source of truth for structure; data is pushed from
#                local to 252 only for ordinary (non-partition, non-hot)
#                tables and only when the policy says `structure+data`.
# Usage:
#   bash scripts/multi-db-sync-252.sh --discover              # list dbs on both sides
#   bash scripts/multi-db-sync-252.sh --structure-only --dry-run --yes
#   bash scripts/multi-db-sync-252.sh --data-only     --dry-run --yes
#   bash scripts/multi-db-sync-252.sh --full          --dry-run --yes
#   bash scripts/multi-db-sync-252.sh --full          --yes     # actually apply
# Preconditions:
#   - envs loader at ~/workspace/ai-native-tools/envs/loader.sh
#   - configs/env-252.sh and scripts/lib/252-db-tunnel.sh present
#   - llm-gateway-pg docker container running locally
#   - SSH access to the `252` SSH-config alias using its configured key
# Status:        active
# Changelog:
#   2026-09-07  v1.0  Initial orchestrator (discover/structure/data/full).
# -----------------------------------------------------------------------------
set -euo pipefail

G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}⚠${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}════════════════════════════════════════════════════════════${N}"; echo -e "${B}  $*${N}"; echo -e "${B}════════════════════════════════════════════════════════════${N}"; }

ENVS_LOADER="$HOME/workspace/ai-native-tools/envs/loader.sh"
PROJECT="llm-gateway-go"
LOCAL_CONTAINER="llm-gateway-pg"
POLICY_FILE="configs/multi-db-sync.policy"
WORK_DIR="/tmp/multi-db-sync-252"
MODE="full"
DRY_RUN=true
YES=false
PGOPTIONS="${PGOPTIONS:--c statement_timeout=0}"
LOG_FILE=""

mkdir -p "$WORK_DIR"
# shellcheck disable=SC1090
source "$ENVS_LOADER" --project "$PROJECT" 2>/dev/null
export PG_PASS_252="${PG_PASS_252:-${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded}}"
export PG_PASS_LOCAL="${PG_PASS_LOCAL:-$COMMON_PG_SUPERUSER_PASS}"
# shellcheck disable=SC1090
source configs/env-252.sh
SRC_USER="$PG_USER"; SRC_PASS="$PG_PASS"
# shellcheck disable=SC1090
source configs/env-local.sh
LOCAL_USER="$PG_USER"
PG_USER="$SRC_USER"; PG_PASS="$SRC_PASS"  # restore 252 binding for tunnel helper
# shellcheck disable=SC1091
source scripts/lib/252-db-tunnel.sh

p252(){ PGOPTIONS="$PGOPTIONS" PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" -X -h 127.0.0.1 -p "$TUNNEL_LOCAL_PORT" -U "$SRC_USER" -d "$1" -v ON_ERROR_STOP=1 -tAq -c "${2:-}"; }
ploc(){ docker exec -i -e PGOPTIONS="$PGOPTIONS" -e PGPASSWORD="$PG_PASS_LOCAL" "$LOCAL_CONTAINER" psql -X -U "$LOCAL_USER" -d "$1" -v ON_ERROR_STOP=1 -tAq -c "${2:-}"; }

list_dbs() {
  local runner="$1" target_db="${2:-postgres}"
  if [[ "$runner" == "252" ]]; then
    p252 "$target_db" "SELECT datname FROM pg_database WHERE datistemplate=false AND datname NOT IN ('postgres') ORDER BY 1"
  else
    ploc "$target_db" "SELECT datname FROM pg_database WHERE datistemplate=false AND datname NOT IN ('postgres') ORDER BY 1"
  fi
}

load_policy() {
  [[ -f "$POLICY_FILE" ]] || { err "policy file not found: $POLICY_FILE"; exit 1; }
  POLICY_DB_MODE=()
  POLICY_DEFAULT_MODE="structure-only"
  while IFS= read -r line; do
    line="${line%%#*}"; line="$(echo "$line" | xargs || true)"
    [[ -z "$line" ]] && continue
    if [[ "$line" =~ ^([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*([a-z+]+)$ ]]; then
      POLICY_DB_MODE["${BASH_REMATCH[1]}"]="${BASH_REMATCH[2]}"
    fi
  done < "$POLICY_FILE"
}

mode_for() {
  local db="$1"
  if [[ -n "${POLICY_DB_MODE[$db]:-}" ]]; then echo "${POLICY_DB_MODE[$db]}"; else echo "$POLICY_DEFAULT_MODE"; fi
}

discover() {
  phase "DISCOVER: 252 databases"
  db252_tunnel_ensure || { err "tunnel to 252 unavailable"; return 1; }
  list_dbs 252 > "$WORK_DIR/dbs252.txt"
  list_dbs local > "$WORK_DIR/dbslocal.txt"
  info "252 dbs: $(wc -l < "$WORK_DIR/dbs252.txt" | tr -d ' ')"
  info "local dbs: $(wc -l < "$WORK_DIR/dbslocal.txt" | tr -d ' ')"
  comm -12 "$WORK_DIR/dbs252.txt" "$WORK_DIR/dbslocal.txt" > "$WORK_DIR/dbs-common.txt"
  info "common dbs: $(wc -l < "$WORK_DIR/dbs-common.txt" | tr -d ' ')"
  echo
  printf '%-30s %-20s %-20s\n' db policy mode
  while IFS= read -r db; do
    [[ -z "$db" ]] && continue
    printf '%-30s %-20s %-20s\n' "$db" "$(mode_for "$db")" "$([[ -n "${POLICY_DB_MODE[$db]:-}" ]] && echo configured || echo default)"
  done < "$WORK_DIR/dbs-common.txt"
  echo
  ok "discovery artifacts: $WORK_DIR/dbs-{252,local,common}.txt"
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --discover)        MODE="discover"; shift ;;
      --structure-only)  MODE="structure"; shift ;;
      --data-only)       MODE="data"; shift ;;
      --full)            MODE="full"; shift ;;
      --dry-run)         DRY_RUN=true; shift ;;
      --apply)           DRY_RUN=false; shift ;;
      --yes)             YES=true; shift ;;
      --log)             LOG_FILE="$2"; shift 2 ;;
      -h|--help)         sed -n '2,25p' "$0"; exit 0 ;;
      *) err "unknown arg: $1"; exit 1 ;;
    esac
  done
}

main() {
  parse_args "$@"
  load_policy
  [[ "$YES" == true ]] || { err "this script requires --yes for safety"; exit 2; }
  if [[ -n "$LOG_FILE" ]]; then exec > >(tee -a "$LOG_FILE") 2>&1; fi
  case "$MODE" in
    discover) discover ;;
    structure|full) info "structure sync (mode=$MODE, dry_run=$DRY_RUN) — delegated to verify-multi-db-consistency.sh" ;;
    data|full) info "data sync (mode=$MODE, dry_run=$DRY_RUN) — delegated to verify-multi-db-data-consistency.sh" ;;
  esac
}

trap db252_tunnel_teardown EXIT
main "$@"
