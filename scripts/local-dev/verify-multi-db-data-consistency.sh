#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/verify-multi-db-data-consistency.sh
# Purpose:       Multi-database data audit (ordinary tables only). Per db,
#                compare count/sum/xor digest of each public ordinary table
#                between 252 (source) and local (reference). Hot/partitioned
#                relations are excluded by contract; structure parity is
#                covered by verify-multi-db-consistency.sh.
# Status:        active
# Changelog:
#   2026-09-07  v1.0  Initial multi-db orchestrator.
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
WORK_DIR="/tmp/verify-multi-db-data-consistency"
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

HOT_SQL="(c.relkind='p' OR c.relispartition OR pt.partrelid IS NOT NULL OR t.tablename LIKE '%\\_hot' ESCAPE '\\' OR t.tablename LIKE '%\\_2025\\_%' ESCAPE '\\' OR t.tablename LIKE '%\\_2026\\_%' ESCAPE '\\' OR t.tablename LIKE '%\\_2027\\_%' ESCAPE '\\' OR t.tablename LIKE '%\\_2028\\_%' ESCAPE '\\' OR t.tablename LIKE '%\\_archived' ESCAPE '\\' OR t.tablename LIKE '%\\_archive' ESCAPE '\\')"
TABLE_SQL="SELECT t.tablename FROM pg_tables t JOIN pg_class c ON c.relname=t.tablename JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname=t.schemaname LEFT JOIN pg_partitioned_table pt ON pt.partrelid=c.oid WHERE t.schemaname='public' AND t.tablename NOT LIKE '\\_%' ESCAPE '\\' AND NOT $HOT_SQL ORDER BY t.tablename"
LIST_DBS_SQL="SELECT datname FROM pg_database WHERE datistemplate=false AND datname NOT IN ('postgres') ORDER BY 1"

row_signature() {
  local runner="$1" db="$2" tbl="$3"
  local sql="SELECT count(*)::bigint || '|' || coalesce(sum(hashtextextended(to_jsonb(x)::text, 0)),0)::numeric || '|' || coalesce(bit_xor(hashtextextended(to_jsonb(x)::text, 0)),0)::bigint FROM public.\"$tbl\" x;"
  if [[ "$runner" == "252" ]]; then p252 "$db" "$sql"; else ploc "$db" "$sql"; fi
}

audit_db() {
  local db="$1"
  local outdir="$WORK_DIR/$db"
  mkdir -p "$outdir"
  phase "DATA AUDIT: $db"
  p252 "$db" "$TABLE_SQL" > "$outdir/tables252.raw" || { warn "$db: 252 table list failed"; return 1; }
  ploc "$db" "$TABLE_SQL" > "$outdir/tableslocal.raw" || { warn "$db: local table list failed"; return 1; }
  sed '/^$/d' "$outdir/tables252.raw" | sort > "$outdir/tables252.txt"
  sed '/^$/d' "$outdir/tableslocal.raw" | sort > "$outdir/tableslocal.txt"
  if [[ ! -s "$outdir/tables252.txt" || ! -s "$outdir/tableslocal.txt" ]]; then
    warn "$db: empty ordinary-table set; skipping"; return 0
  fi
  printf 'table|252_count|local_count|252_digest|local_digest|status\n' > "$outdir/data-manifest.tsv"
  local diff=0
  while IFS= read -r tbl; do
    [[ -z "$tbl" ]] && continue
    local s252 sloc status
    s252=$(row_signature 252  "$db" "$tbl") || { warn "$db/$tbl: 252 query failed"; diff=$((diff+1)); continue; }
    sloc=$(row_signature local "$db" "$tbl") || { warn "$db/$tbl: local query failed"; diff=$((diff+1)); continue; }
    if [[ "$s252" == "$sloc" ]]; then status=identical; else status=DIFF; diff=$((diff+1)); fi
    local c252 sum252 xor252 cloc sumloc xorloc rest
    c252=${s252%%|*}; rest=${s252#*|}; sum252=${rest%%|*}; xor252=${rest#*|}
    cloc=${sloc%%|*}; rest=${sloc#*|}; sumloc=${rest%%|*}; xorloc=${rest#*|}
    printf '%s|%s|%s|%s/%s|%s/%s|%s\n' "$tbl" "$c252" "$cloc" "$sum252" "$xor252" "$sumloc" "$xorloc" "$status" >> "$outdir/data-manifest.tsv"
  done < "$outdir/tables252.txt"
  if [[ "$diff" -gt 0 ]]; then err "$db: $diff row-signature diffs (manifest: $outdir/data-manifest.tsv)"; else ok "$db: ordinary-table data identical"; fi
}

main() {
  db252_tunnel_ensure || { err "tunnel to 252 unavailable"; exit 1; }
  p252 postgres "$LIST_DBS_SQL" > "$WORK_DIR/dbs252.txt"
  ploc postgres "$LIST_DBS_SQL" > "$WORK_DIR/dbslocal.txt"
  sed '/^$/d' "$WORK_DIR/dbs252.txt" | sort > "$WORK_DIR/dbs252.sorted"
  sed '/^$/d' "$WORK_DIR/dbslocal.txt" | sort > "$WORK_DIR/dbslocal.sorted"
  comm -12 "$WORK_DIR/dbs252.sorted" "$WORK_DIR/dbslocal.sorted" > "$WORK_DIR/dbs-common.txt"
  info "common dbs: $(wc -l < "$WORK_DIR/dbs-common.txt" | tr -d ' ')"
  local any=0
  while IFS= read -r db; do
    [[ -z "$db" ]] && continue
    if ! audit_db "$db"; then any=1; fi
  done < "$WORK_DIR/dbs-common.txt"
  if [[ "$any" -ne 0 ]]; then err "DATA INCONSISTENT on at least one db"; exit 1; fi
  ok "DATA CONSISTENT across all common dbs (ordinary tables only)"
}

trap db252_tunnel_teardown EXIT
main "$@"
