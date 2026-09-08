#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/verify-multi-db-consistency.sh
# Purpose:       Multi-database structure audit: enumerate common dbs between
#                252's pg17 docker and local llm-gateway-pg, then for each db
#                report table inventory + column signatures + views + indexes
#                + constraints + sequences + functions (parity with the single
#                verify-db-consistency.sh v1.2 surface). Output is per-db
#                under WORK_DIR/<db>/{tables.txt,cols.txt,...}.
# Status:        active
# Changelog:
#   2026-09-07  v1.0  Initial multi-db orchestrator (no reconcile yet).
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
WORK_DIR="/tmp/verify-multi-db-consistency"
PGOPTIONS="${PGOPTIONS:--c statement_timeout=0}"

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
PG_USER="$SRC_USER"; PG_PASS="$SRC_PASS"
# shellcheck disable=SC1091
source scripts/lib/252-db-tunnel.sh

p252(){ PGOPTIONS="$PGOPTIONS" PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" -X -h 127.0.0.1 -p "$TUNNEL_LOCAL_PORT" -U "$SRC_USER" -d "$1" -v ON_ERROR_STOP=1 -tAq -c "${2:-}"; }
ploc(){ docker exec -i -e PGOPTIONS="$PGOPTIONS" -e PGPASSWORD="$PG_PASS_LOCAL" "$LOCAL_CONTAINER" psql -X -U "$LOCAL_USER" -d "$1" -v ON_ERROR_STOP=1 -tAq -c "${2:-}"; }

LIST_DBS_SQL="SELECT datname FROM pg_database WHERE datistemplate=false AND datname NOT IN ('postgres') ORDER BY 1"
COL_SIG_SQL="SELECT table_name||'|'||column_name||'|'||data_type||'|'||is_nullable||'|'||coalesce(column_default,'') FROM information_schema.columns WHERE table_schema='public' ORDER BY 1,2"
VIEW_SQL="SELECT viewname||'|'||md5(definition) FROM pg_views WHERE schemaname='public' ORDER BY 1"
IDX_SQL="SELECT t.relname||'.'||i.relname||'|'||coalesce(pg_get_indexdef(i.oid),'') FROM pg_class t JOIN pg_index x ON x.indrelid=t.oid JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname='public' AND x.indisprimary=false ORDER BY 1"
CON_SQL="SELECT conrelid::regclass::text||'|'||conname||'|'||contype||'|'||coalesce(pg_get_constraintdef(oid),'') FROM pg_constraint WHERE conrelid::regclass::text LIKE 'public.%' ORDER BY 1,2"
SEQ_SQL="SELECT sequence_name||'|'||last_value FROM information_schema.sequences WHERE sequence_schema='public' ORDER BY 1"
FUNC_SQL="SELECT p.proname||'|'||pg_get_function_identity_arguments(p.oid)||'|'||md5(p.prosrc) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' ORDER BY 1,2"

dump_one() {
  local side="$1" db="$2" outdir="$WORK_DIR/$db"
  mkdir -p "$outdir"
  local runner
  [[ "$side" == "252" ]] && runner=p252 || runner=ploc
  info "[$side] db=$db"
  for dim in cols views idxes cons seqs funcs; do
    local sql_var="${DIM_SQL[$dim]}"
    if ! "$runner" "$db" "$sql_var" > "$outdir/${dim}.${side}.txt" 2>"$outdir/${dim}.${side}.err"; then
      warn "[$side/$db] $dim query failed (see $outdir/${dim}.${side}.err)"
      : > "$outdir/${dim}.${side}.txt"
    fi
    sed '/^$/d' "$outdir/${dim}.${side}.txt" > "$outdir/${dim}.${side}.sorted" || true
  done
}

declare -A DIM_SQL=(
  [cols]="$COL_SIG_SQL"
  [views]="$VIEW_SQL"
  [idxes]="$IDX_SQL"
  [cons]="$CON_SQL"
  [seqs]="$SEQ_SQL"
  [funcs]="$FUNC_SQL"
)

main() {
  db252_tunnel_ensure || { err "tunnel to 252 unavailable"; exit 1; }
  phase "DISCOVER: list databases on each side"
  p252 postgres "$LIST_DBS_SQL" > "$WORK_DIR/dbs252.txt" || { err "252 db list failed"; exit 1; }
  ploc postgres "$LIST_DBS_SQL" > "$WORK_DIR/dbslocal.txt" || { err "local db list failed"; exit 1; }
  sed '/^$/d' "$WORK_DIR/dbs252.txt" | sort > "$WORK_DIR/dbs252.sorted"
  sed '/^$/d' "$WORK_DIR/dbslocal.txt" | sort > "$WORK_DIR/dbslocal.sorted"
  comm -12 "$WORK_DIR/dbs252.sorted" "$WORK_DIR/dbslocal.sorted" > "$WORK_DIR/dbs-common.txt"
  info "common dbs: $(wc -l < "$WORK_DIR/dbs-common.txt" | tr -d ' ')"
  ok "discovery artifacts: $WORK_DIR/dbs-{252,local,common}.txt"
  phase "STRUCTURE AUDIT: per-db dimensional dump"
  while IFS= read -r db; do
    [[ -z "$db" ]] && continue
    dump_one 252  "$db"
    dump_one local "$db"
  done < "$WORK_DIR/dbs-common.txt"
  ok "audit artifacts under: $WORK_DIR/<db>/{cols,views,idxes,cons,seqs,funcs}.{252,local}.sorted"
  info "compare 252 vs local with: for d in $WORK_DIR/*/; do for dim in cols views idxes cons seqs funcs; do diff -u \"\$d\${dim}.252.sorted\" \"\$d\${dim}.local.sorted\" || true; done; done"
}

trap db252_tunnel_teardown EXIT
main "$@"
