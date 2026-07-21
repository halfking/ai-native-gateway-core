#!/usr/bin/env bash
# ============================================================================
# sync-from-252.sh — Unified DB sync from 252 to local (schema + cold data)
#
# Replaces: sync-252-schema-only.sh, pg-table-copy.sh (252→local use case),
#           pg-bidirectional-sync.sh, sync-kaixuan1-schema.sh
#
# What it does:
#   - Sync schema (DDL) for ALL public tables from 252
#   - Sync data for COLD tables only (non-partition, non-hot, non-archive)
#   - Hot/partition/archive tables: schema only, data preserved locally
#
# Usage:
#   ./scripts/sync-from-252.sh                  # full sync (schema + cold data)
#   ./scripts/sync-from-252.sh --schema-only    # DDL only, skip data
#   ./scripts/sync-from-252.sh --check          # diff report only, no changes
#   ./scripts/sync-from-252.sh --tables=X,Y     # specific tables only
#
# Prereqs:
#   - SSH tunnel to 252 (default localhost:15432 → 172.16.2.210:5432)
#   - Local PG container (default llm-gateway-pg) running
#   - env-injector: eval "$(env-injector inject --target=252)"
#     or PG_PASS_252 set manually
# ============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
WORK="$(mktemp -d -t sync252-XXXX)"
trap 'rm -rf "$WORK"' EXIT

# ── Colors ────────────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; C='\033[0;36m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}⚠${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}════════════════════════════════════════════════════════════${N}"; echo -e "${B}  $*${N}"; echo -e "${B}════════════════════════════════════════════════════════════${N}"; }

# ── Defaults ─────────────────────────────────────────────────────────────
LOCAL_CONTAINER="${LOCAL_CONTAINER:-llm-gateway-pg}"
LOCAL_USER="${LOCAL_USER:-llm_gateway}"
LOCAL_DB="${LOCAL_DB:-llm_gateway}"

TUNNEL_PORT="${TUNNEL_LOCAL_PORT:-15432}"

# Hot/partition patterns — these tables get schema only, no data
HOT_PATTERNS="*_hot,*_[0-9][0-9][0-9][0-9]_[0-9][0-9],*_archive,*_archived,*_default,*_old"
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=20 -o IdentitiesOnly=yes -o PreferredAuthentications=publickey"

# ── Parse args ────────────────────────────────────────────────────────────
MODE=full
ONLY_TABLES=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --schema-only) MODE=schema ;;
    --check)       MODE=check ;;
    --tables=*)    ONLY_TABLES="${1#--tables=}" ;;
    -h|--help)     sed -n '3,15p' "$0"; exit 0 ;;
    *)             err "unknown option: $1"; exit 1 ;;
  esac
  shift
done

# ── Source env configs ───────────────────────────────────────────────────
ENV_252="$ROOT_DIR/configs/env-252.sh"
ENV_LOCAL="$ROOT_DIR/configs/env-local.sh"

if [[ -f "$ENV_252" ]]; then
  set -a; . "$ENV_252"; set +a
  unset DOCKER_HOST
fi

REMOTE_PASS="${PG_PASS_252:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}"

if [[ -f "$ENV_LOCAL" ]]; then
  LOCAL_PASS="${LOCAL_PG_PASS:-llm_gateway_db_pass_2026_secure}"
fi

# ── Helpers ──────────────────────────────────────────────────────────────
remote_psql() {
  PGPASSWORD="$REMOTE_PASS" psql -h localhost -p "$TUNNEL_PORT" -U llm_gateway -d llm_gateway -tAc "$1"
}

local_psql() {
  docker exec -e PGPASSWORD="$LOCAL_PASS" "$LOCAL_CONTAINER" \
    psql -U "$LOCAL_USER" -d "$LOCAL_DB" -tAc "$1"
}

local_psql_file() {
  docker exec -e PGPASSWORD="$LOCAL_PASS" "$LOCAL_CONTAINER" \
    psql -U "$LOCAL_USER" -d "$LOCAL_DB" -v ON_ERROR_STOP=1 -f "$1"
}

local_run_sql() {
  docker exec -e PGPASSWORD="$LOCAL_PASS" "$LOCAL_CONTAINER" \
    psql -U "$LOCAL_USER" -d "$LOCAL_DB" -v ON_ERROR_STOP=off -c "$1"
}

# Check if a table is partition/hot (schema-only, no data)
is_partition_or_hot() {
  local tbl="$1"
  IFS=',' read -ra patterns <<< "$HOT_PATTERNS"
  for pat in "${patterns[@]}"; do
    pat=$(echo "$pat" | xargs)
    case "$tbl" in
      $pat) return 0 ;;
    esac
  done
  # Also check pg_inherits — partition children get schema only
  local is_part
  is_part=$(local_psql "SELECT 1 FROM pg_inherits i JOIN pg_class c ON i.inhrelid=c.oid WHERE c.relname='$tbl' LIMIT 1" 2>/dev/null || echo "0")
  [[ "$is_part" == "1" ]] && return 0
  return 1
}

# ============================================================================
# PHASE 0: PRE-FLIGHT
# ============================================================================
phase "PHASE 0: PRE-FLIGHT"

if ! docker ps --format '{{.Names}}' | grep -q "^${LOCAL_CONTAINER}\$"; then
  err "Local PG container not running: $LOCAL_CONTAINER"
  exit 1
fi
ok "Local container: $LOCAL_CONTAINER"

info "Connecting to 252 via tunnel (localhost:$TUNNEL_PORT)..."
if ! PGPASSWORD="$REMOTE_PASS" psql -h localhost -p "$TUNNEL_PORT" -U llm_gateway -d llm_gateway -tAc "SELECT 1" >/dev/null 2>&1; then
  err "252 unreachable. Start the tunnel:"
  err "  ssh -f -N -p 25022 -L $TUNNEL_PORT:172.16.2.210:5432 root@115.29.212.252"
  exit 1
fi
ok "252 reachable via localhost:$TUNNEL_PORT"

# ============================================================================
# PHASE 1: CHECK (diff report only)
# ============================================================================
if [[ "$MODE" == "check" ]]; then
  phase "PHASE 1: SCHEMA DIFF (252 vs local)"
  printf "  %-14s %8s %8s\n" "metric" "252" "local"
  MISMATCH=0
  for m in \
    "tables:SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" \
    "columns:SELECT count(*) FROM information_schema.columns WHERE table_schema='public'" \
    "indexes:SELECT count(*) FROM pg_indexes WHERE schemaname='public'" \
    "views:SELECT count(*) FROM pg_views WHERE schemaname='public'" \
    "functions:SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON p.pronamespace=n.oid WHERE n.nspname='public'" \
    "triggers:SELECT count(*) FROM information_schema.triggers WHERE event_object_schema='public'"
  do
    label="${m%%:*}"; q="${m#*:}"
    c252=$(remote_psql "$q")
    clocal=$(local_psql "$q")
    mark=$([ "$c252" = "$clocal" ] && echo "✓" || { MISMATCH=$((MISMATCH + 1)); echo "✗"; })
    printf "  %-14s %8s %8s  %s\n" "$label" "$c252" "$clocal" "$mark"
  done
  echo ""
  if [[ $MISMATCH -eq 0 ]]; then
    ok "Schema is in sync"
  else
    warn "$MISMATCH metric(s) differ — run without --check to sync"
  fi
  exit 0
fi

# ============================================================================
# PHASE 1: SCHEMA SYNC (DDL)
# ============================================================================
phase "PHASE 1: SCHEMA SYNC"

DUMP="$WORK/252_schema.sql"
info "Dumping 252 schema..."
ssh $SSH_OPTS -p "${REMOTE_SSH_PORT:-25022}" -i "${REMOTE_SSH_IDENTITY:-$HOME/.ssh/56_id_rsa}" \
  "${REMOTE_SSH_HOST:-root@115.29.212.252}" \
  "docker exec ${REMOTE_DB_CONTAINER:-pg-252-pg17} pg_dump -U llm_gateway -d llm_gateway \
   --schema-only --no-owner --no-privileges --format=plain" > "$DUMP" 2>/dev/null
sed -i.bak '/^\\(restrict|unrestrict)/d' "$DUMP" 2>/dev/null || true
ok "$(wc -l < "$DUMP") lines from 252 schema dump"

# ── Step 1a: ADD COLUMN for missing columns ──────────────────────────────
info "Step 1a: ADD COLUMN IF NOT EXISTS"
remote_psql "
SELECT table_name || E'\t' || column_name || E'\t' ||
       format_type(a.atttypid, a.atttypmod) || E'\t' ||
       is_nullable || E'\t' || COALESCE(column_default, '')
FROM information_schema.columns c
JOIN pg_attribute a
  ON a.attrelid = (c.table_schema || '.' || c.table_name)::regclass
 AND a.attname = c.column_name
WHERE c.table_schema = 'public' AND c.ordinal_position > 0
" > "$WORK/252_columns.txt"
docker cp "$WORK/252_columns.txt" "$LOCAL_CONTAINER":/tmp/252_columns.txt

cat > "$WORK/add_columns.sql" <<'SQL'
DROP TABLE IF EXISTS _252_cols;
CREATE TEMP TABLE _252_cols (
  table_name text, column_name text, data_type text,
  is_nullable text, column_default text
);
\copy _252_cols from '/tmp/252_columns.txt' with (FORMAT csv, DELIMITER E'\t');
DO $$
DECLARE stmt text; applied int := 0; failed int := 0; err_msg text;
BEGIN
  FOR stmt IN
    SELECT format(
      'ALTER TABLE public.%I ADD COLUMN IF NOT EXISTS %I %s%s%s;',
      table_name, column_name, data_type,
      CASE WHEN is_nullable='NO' AND (column_default IS NULL OR column_default='')
           THEN ' NOT NULL' ELSE '' END,
      CASE
        WHEN column_default IS NULL OR column_default='' THEN ''
        WHEN column_default LIKE '%::%' OR column_default LIKE '%now()%'
        THEN format(' DEFAULT %s', column_default)
        ELSE format(' DEFAULT %L', trim(both chr(39) from column_default))
      END
    )
    FROM _252_cols src
    WHERE NOT EXISTS (
      SELECT 1 FROM information_schema.columns dst
      WHERE dst.table_schema='public' AND dst.table_name=src.table_name
        AND dst.column_name=src.column_name
    )
  LOOP
    BEGIN EXECUTE stmt; applied := applied + 1;
    EXCEPTION WHEN OTHERS THEN
      err_msg := SQLERRM;
      IF err_msg NOT LIKE '%does not exist%' AND err_msg NOT LIKE '%cannot add column to a partition%'
         AND err_msg NOT LIKE '%already exists%' THEN
        failed := failed + 1;
        RAISE NOTICE 'FAIL: % | %', err_msg, substring(stmt from 1 for 80);
      END IF;
    END;
  END LOOP;
  RAISE NOTICE 'Step 1a done: applied=% failed=%', applied, failed;
END $$;
SQL
docker cp "$WORK/add_columns.sql" "$LOCAL_CONTAINER":/tmp/add_columns.sql
local_psql_file "/tmp/add_columns.sql" 2>&1 | grep -E 'Step 1a done' || warn "step 1a had errors"
ok "ADD COLUMN done"

# ── Step 1b: CREATE missing tables ──────────────────────────────────────
info "Step 1b: CREATE TABLE IF NOT EXISTS"
missing_tables=$(comm -23 \
  <(remote_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1") \
  <(local_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1"))

for tbl in $missing_tables; do
  if [ -n "$ONLY_TABLES" ] && [[ ",$ONLY_TABLES," != *",$tbl,"* ]]; then
    continue
  fi
  info "  -> creating $tbl"
  python3 - "$DUMP" "$tbl" "$WORK/${tbl}.sql" <<'PY'
import sys
dump, tbl, outpath = sys.argv[1], sys.argv[2], sys.argv[3]
with open(dump) as f: c = f.read()
start = c.find(f"Name: {tbl}; Type: TABLE")
if start < 0:
    sys.stderr.write(f"WARN: {tbl} not in dump\n")
    sys.exit(0)
pre = c.rfind("\n--\n", 0, start)
nxt = c.find("\n--\n-- Name:", start + 100)
seg = c[pre+1: nxt if nxt > 0 else len(c)]
with open(outpath, 'w') as g: g.write(seg)
PY
  docker cp "$WORK/${tbl}.sql" "$LOCAL_CONTAINER":/tmp/${tbl}.sql
  if local_psql_file "/tmp/${tbl}.sql" >/dev/null; then
    ok "  created $tbl"
  else
    err "  failed to create $tbl"
    exit 1
  fi
done
ok "CREATE TABLE done"

# ── Step 1c: CREATE missing indexes ─────────────────────────────────────
info "Step 1c: CREATE INDEX IF NOT EXISTS"
local_indexes=$(local_psql "SELECT indexname FROM pg_indexes WHERE schemaname='public' ORDER BY 1")

python3 - "$DUMP" "$WORK/missing_indexes.sql" "$local_indexes" <<'PY' || true
import sys, re
dump, out_path, existing = sys.argv[1], sys.argv[2], sys.argv[3].split()
with open(dump) as f: c = f.read()
out = ['-- Missing indexes from 252', 'BEGIN;']
for m in re.finditer(
    r"CREATE\s+(UNIQUE\s+)?INDEX\s+(\w+)\s+ON\s+public\.\w+\s+.*?;", c, re.DOTALL):
    name = m.group(2)
    if name not in existing:
        out.append(m.group(0))
out.append("COMMIT;")
open(out_path, 'w').write('\n'.join(out))
PY

if [ -s "$WORK/missing_indexes.sql" ] && grep -q "CREATE INDEX" "$WORK/missing_indexes.sql"; then
  docker cp "$WORK/missing_indexes.sql" "$LOCAL_CONTAINER":/tmp/missing_indexes.sql
  out=$(local_psql_file /tmp/missing_indexes.sql 2>&1 || true)
  echo "$out" | tail -3
  if echo "$out" | grep -q "unsupported access method for the index on columnar table"; then
    info "columnar GIN index creation(s) skipped (expected)"
  fi
fi
ok "CREATE INDEX done"

# ============================================================================
# PHASE 2: DATA SYNC (cold tables only)
# ============================================================================
if [[ "$MODE" == "schema" ]]; then
  info "Skipping data sync (--schema-only)"
else
  phase "PHASE 2: DATA SYNC (cold tables only)"

  info "Classifying tables..."
  ALL_TABLES=$(remote_psql "
    SELECT tablename FROM pg_tables
    WHERE schemaname='public'
    ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC
  ")

  HOT_LIST=()
  COLD_LIST=()
  for tbl in $ALL_TABLES; do
    [[ -z "$tbl" ]] && continue
    if [ -n "$ONLY_TABLES" ] && [[ ",$ONLY_TABLES," != *",$tbl,"* ]]; then
      continue
    fi
    if is_partition_or_hot "$tbl"; then
      HOT_LIST+=("$tbl")
    else
      COLD_LIST+=("$tbl")
    fi
  done

  info "${#COLD_LIST[@]} cold tables (schema + data), ${#HOT_LIST[@]} hot/partition (schema only)"

  # ── Export cold table data ────────────────────────────────────────────
  DATA_DIR="$WORK/data"
  mkdir -p "$DATA_DIR"
  EXPORTED=0; SKIPPED=0

  for tbl in "${COLD_LIST[@]}"; do
    row_count=$(remote_psql "SELECT count(*) FROM public.$tbl;")
    printf "  %-45s %10s rows" "$tbl" "$row_count"
    if [[ "$row_count" == "0" ]]; then
      echo -e " ${C}(empty, skip)${N}"
      SKIPPED=$((SKIPPED + 1))
      continue
    fi
    PGPASSWORD="$REMOTE_PASS" pg_dump \
      -h localhost -p "$TUNNEL_PORT" -U llm_gateway -d llm_gateway \
      --data-only --table="public.$tbl" \
      --no-owner --no-privileges --disable-triggers \
      -f "$DATA_DIR/${tbl}.sql" 2>/dev/null
    size=$(du -h "$DATA_DIR/${tbl}.sql" | cut -f1)
    echo -e " ${G}→ $size${N}"
    EXPORTED=$((EXPORTED + 1))
  done
  ok "Exported: $EXPORTED with data, $SKIPPED empty"

  # ── Import cold table data ────────────────────────────────────────────
  IMPORTED=0; FAILED=0
  for data_file in "$DATA_DIR"/*.sql; do
    [[ ! -f "$data_file" ]] && continue
    tbl_name=$(basename "$data_file" .sql)
    tbl_size=$(du -h "$data_file" | cut -f1)
    [[ ! -s "$data_file" ]] && continue

    printf "  %-45s %8s" "$tbl_name" "$tbl_size"
    if docker exec -i -e PGPASSWORD="$LOCAL_PASS" "$LOCAL_CONTAINER" \
         psql -U "$LOCAL_USER" -d "$LOCAL_DB" -v ON_ERROR_STOP=off < "$data_file" &>/dev/null; then
      echo -e " ${G}OK${N}"
      IMPORTED=$((IMPORTED + 1))
    else
      echo -e " ${R}FAILED${N}"
      FAILED=$((FAILED + 1))
    fi
  done
  ok "Imported: $IMPORTED OK, $FAILED failed"
fi

# ============================================================================
# PHASE 3: VERIFICATION
# ============================================================================
phase "PHASE 3: VERIFICATION"

info "Target DB size: $(local_psql "SELECT pg_size_pretty(pg_database_size('$LOCAL_DB'));")"
info "Table count:"
local_psql "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';"

info "Final schema diff (252 vs local):"
printf "  %-14s %8s %8s\n" "metric" "252" "local"
for m in \
  "tables:SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" \
  "columns:SELECT count(*) FROM information_schema.columns WHERE table_schema='public'" \
  "indexes:SELECT count(*) FROM pg_indexes WHERE schemaname='public'"
do
  label="${m%%:*}"; q="${m#*:}"
  c252=$(remote_psql "$q")
  clocal=$(local_psql "$q")
  mark=$([ "$c252" = "$clocal" ] && echo "✓" || echo "✗")
  printf "  %-14s %8s %8s  %s\n" "$label" "$c252" "$clocal" "$mark"
done

echo ""
ok "Sync complete. Hot/partition table data on local preserved."
