#!/usr/bin/env bash
# Compare 252 and local data for all owned ordinary public tables.
# Hot/partitioned relations are intentionally excluded: they are runtime data
# and the sync contract copies their schema only.
set -euo pipefail

G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok(){ echo -e "${G}✓${N} $*"; }
warn(){ echo -e "${Y}⚠${N} $*"; }
err(){ echo -e "${R}✗${N} $*" >&2; }
phase(){ echo -e "\n${B}════════════════════════════════════════════════════════════${N}\n${B}  $*${N}\n${B}════════════════════════════════════════════════════════════${N}"; }

ENVS_LOADER="$HOME/workspace/ai-native-tools/envs/loader.sh"
PROJECT="llm-gateway-go"
LOCAL_CONTAINER="llm-gateway-pg"
LOCAL_DB="llm_gateway"
LOCAL_USER="llm_gateway"
WORK_DIR="/tmp/verify-db-data-consistency"
PGOPTIONS="${PGOPTIONS:--c statement_timeout=0}"

mkdir -p "$WORK_DIR"
source "$ENVS_LOADER" --project "$PROJECT" 2>/dev/null
export PG_PASS_252="${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded}"
export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
export PG_PASS_LOCAL="${PG_PASS_LOCAL:-$COMMON_PG_SUPERUSER_PASS}"
source configs/env-252.sh
SRC_DB_USER="$PG_USER"
SRC_DB_NAME="$PG_DB"
source configs/env-local.sh
LOCAL_DB_USER="$PG_USER"
LOCAL_DB_NAME="$PG_DB"

p252(){ PGOPTIONS="$PGOPTIONS" PGPASSWORD="$PG_PASS_252" psql -X -h localhost -p "$TUNNEL_LOCAL_PORT" -U "$SRC_DB_USER" -d "$SRC_DB_NAME" -v ON_ERROR_STOP=1 -tAq -c "$1"; }
ploc(){ docker exec -i -e PGOPTIONS="$PGOPTIONS" -e PGPASSWORD="$PG_PASS_LOCAL" "$LOCAL_CONTAINER" psql -X -U "$LOCAL_DB_USER" -d "$LOCAL_DB_NAME" -v ON_ERROR_STOP=1 -tAq -c "$1"; }

if ! p252 'SELECT 1' >/dev/null 2>&1; then
  ssh -f -N -L "$TUNNEL_LOCAL_PORT:$TUNNEL_REMOTE_TARGET" 252
  sleep 3
fi
if ! p252 'SELECT 1' >/dev/null 2>&1; then err "cannot reach 252 via tunnel"; exit 1; fi
if ! ploc 'SELECT 1' >/dev/null 2>&1; then err "cannot reach local docker PostgreSQL"; exit 1; fi

HOT_SQL="(
  c.relkind = 'p' OR c.relispartition OR pt.partrelid IS NOT NULL OR
  t.tablename LIKE '%_hot' OR t.tablename LIKE '%_2026_%' OR
  t.tablename LIKE '%_2027_%' OR t.tablename LIKE '%_2028_%' OR
  t.tablename LIKE '%_archived' OR t.tablename LIKE '%_archive'
)"
TABLE_SQL="SELECT t.tablename
FROM pg_tables t
JOIN pg_class c ON c.relname=t.tablename
JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname=t.schemaname
LEFT JOIN pg_partitioned_table pt ON pt.partrelid=c.oid
WHERE t.schemaname='public' AND t.tablename NOT LIKE '\_%' ESCAPE '\' AND NOT $HOT_SQL
ORDER BY t.tablename"

phase "DATA AUDIT: ordinary public tables (252 → local)"
if ! p252 "$TABLE_SQL" > "$WORK_DIR/tables252.raw"; then
  err "ordinary-table catalog query failed on 252"; exit 1
fi
if ! ploc "$TABLE_SQL" > "$WORK_DIR/tableslocal.raw"; then
  err "ordinary-table catalog query failed on local"; exit 1
fi
sed '/^$/d' "$WORK_DIR/tables252.raw" | sort > "$WORK_DIR/tables252.txt"
sed '/^$/d' "$WORK_DIR/tableslocal.raw" | sort > "$WORK_DIR/tableslocal.txt"
if [[ ! -s "$WORK_DIR/tables252.txt" || ! -s "$WORK_DIR/tableslocal.txt" ]]; then
  err "ordinary-table catalog returned an empty set; refusing to report consistency"
  exit 1
fi
only252=$(comm -23 "$WORK_DIR/tables252.txt" "$WORK_DIR/tableslocal.txt" || true)
onlylocal=$(comm -13 "$WORK_DIR/tables252.txt" "$WORK_DIR/tableslocal.txt" || true)
FAIL=0
if [[ -n "$only252" ]]; then err "252-only ordinary tables:"; printf '%s\n' "$only252"; FAIL=1; fi
if [[ -n "$onlylocal" ]]; then err "local-only ordinary tables:"; printf '%s\n' "$onlylocal"; FAIL=1; fi

# The digest is order-independent and duplicate-sensitive: count + sum + XOR
# over SHA-like PostgreSQL row hashes. to_jsonb(row) canonicalizes JSON object
# key order and preserves NULLs; table/column structure is checked separately
# by verify-db-consistency.sh.
row_signature(){
  local runner="$1" tbl="$2"
  local sql="SELECT count(*)::bigint || '|' || coalesce(sum(hashtextextended(to_jsonb(x)::text, 0)),0)::numeric || '|' || coalesce(bit_xor(hashtextextended(to_jsonb(x)::text, 0)),0)::bigint FROM public.\"$tbl\" x;"
  if [[ "$runner" == "252" ]]; then p252 "$sql"; else ploc "$sql"; fi
}

printf 'table|252_count|local_count|252_digest|local_digest|status\n' > "$WORK_DIR/data-manifest.tsv"
while IFS= read -r tbl; do
  [[ -z "$tbl" ]] && continue
  s252=$(row_signature 252 "$tbl") || { err "query failed on 252: $tbl"; FAIL=1; continue; }
  sloc=$(row_signature local "$tbl") || { err "query failed on local: $tbl"; FAIL=1; continue; }
  c252=${s252%%|*}; rest=${s252#*|}; sum252=${rest%%|*}; xor252=${rest#*|}
  cloc=${sloc%%|*}; rest=${sloc#*|}; sumloc=${rest%%|*}; xorloc=${rest#*|}
  if [[ "$s252" == "$sloc" ]]; then status=identical; else status=DIFF; FAIL=1; fi
  printf '%s|%s|%s|%s|%s|%s\n' "$tbl" "$c252" "$cloc" "$sum252/$xor252" "$sumloc/$xorloc" "$status" >> "$WORK_DIR/data-manifest.tsv"
done < "$WORK_DIR/tables252.txt"

DIFF_COUNT=$(awk -F'|' '$6=="DIFF"{n++} END{print n+0}' "$WORK_DIR/data-manifest.tsv")
TABLE_COUNT=$(($(wc -l < "$WORK_DIR/tables252.txt") - 0))
if [[ "$DIFF_COUNT" -gt 0 ]]; then
  err "data mismatches: $DIFF_COUNT of $TABLE_COUNT ordinary tables"
  awk -F'|' '$6=="DIFF"{print}' "$WORK_DIR/data-manifest.tsv"
else
  ok "data signatures identical for $TABLE_COUNT ordinary tables"
fi
warn "hot/partition data excluded by contract; structure is checked separately"
echo "manifest: $WORK_DIR/data-manifest.tsv"

if [[ "$FAIL" -ne 0 ]]; then
  err "DATA INCONSISTENT"
  exit 1
fi
ok "DATA CONSISTENT — complete ordinary-table set, count, and content digest match"
