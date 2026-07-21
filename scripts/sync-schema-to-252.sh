#!/usr/bin/env bash
# ============================================================================
# sync-schema-to-252.sh — Sync schema FROM local/kaixuan-1 TO 252
#
# Schema-only DDL sync (ADD COLUMN, CREATE TABLE, CREATE INDEX).
# No data transfer, no table drops — purely additive schema alignment.
#
# Usage:
#   ./scripts/sync-schema-to-252.sh                       # local → 252
#   ./scripts/sync-schema-to-252.sh --from=kaixuan1       # kaixuan-1 → 252
#   ./scripts/sync-schema-to-252.sh --check               # diff report only
#   ./scripts/sync-schema-to-252.sh --tables=X,Y          # specific tables
#
# Source config: configs/env-<name>.sh
#   - SOURCE_TYPE=docker  → docker exec (local)
#   - SOURCE_TYPE=direct  → psql -h host (kaixuan-1, etc.)
#
# Prereqs:
#   - SSH tunnel to 252 (default localhost:15432 → 172.16.2.210:5432)
#   - env-injector: eval "$(env-injector inject --target=252)"
# ============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
WORK="$(mktemp -d -t schema252-XXXX)"
trap 'rm -rf "$WORK"' EXIT

# ── Colors ────────────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; C='\033[0;36m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}⚠${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}════════════════════════════════════════════════════════════${N}"; echo -e "${B}  $*${N}"; echo -e "${B}════════════════════════════════════════════════════════════${N}"; }

# ── Defaults ─────────────────────────────────────────────────────────────
FROM="local"
TUNNEL_PORT="${TUNNEL_LOCAL_PORT:-15432}"
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=20 -o IdentitiesOnly=yes -o PreferredAuthentications=publickey"

# ── Parse args ────────────────────────────────────────────────────────────
MODE=full
ONLY_TABLES=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --from=*)      FROM="${1#--from=}" ;;
    --check)       MODE=check ;;
    --tables=*)    ONLY_TABLES="${1#--tables=}" ;;
    -h|--help)     sed -n '3,12p' "$0"; exit 0 ;;
    *)             err "unknown option: $1"; exit 1 ;;
  esac
  shift
done

# ── Source configs ───────────────────────────────────────────────────────
ENV_SOURCE="$ROOT_DIR/configs/env-${FROM}.sh"
ENV_TARGET="$ROOT_DIR/configs/env-252.sh"

if [[ -f "$ENV_SOURCE" ]]; then
  set -a; . "$ENV_SOURCE"; set +a
fi

# Source connection (local/kaixuan1)
SOURCE_TYPE="${TARGET_TYPE:-direct}"
SOURCE_DOCKER_CONTAINER="${DOCKER_PG_CONTAINER:-}"
SOURCE_PG_HOST="${PG_HOST:-localhost}"
SOURCE_PG_PORT="${PG_PORT:-5432}"
SOURCE_PG_USER="${PG_USER:-llm_gateway}"
SOURCE_PG_PASS="${PG_PASS:-}"
SOURCE_PG_DB="${PG_DB:-llm_gateway}"

# Target is always 252
if [[ -f "$ENV_TARGET" ]]; then
  set -a; . "$ENV_TARGET"; set +a
  unset DOCKER_HOST
fi

TARGET_PASS="${PG_PASS_252:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}"

# ── Source helpers ────────────────────────────────────────────────────────
src_psql() {
  if [[ "$SOURCE_TYPE" == "docker" ]]; then
    docker exec -e PGPASSWORD="$SOURCE_PG_PASS" "$SOURCE_DOCKER_CONTAINER" \
      psql -U "$SOURCE_PG_USER" -d "$SOURCE_PG_DB" -tAc "$1"
  else
    PGPASSWORD="$SOURCE_PG_PASS" psql -h "$SOURCE_PG_HOST" -p "$SOURCE_PG_PORT" \
      -U "$SOURCE_PG_USER" -d "$SOURCE_PG_DB" -tAc "$1"
  fi
}

src_psql_file() {
  local file="$1"
  if [[ "$SOURCE_TYPE" == "docker" ]]; then
    local bname=$(basename "$file")
    docker cp "$file" "$SOURCE_DOCKER_CONTAINER":/tmp/"$bname"
    docker exec -e PGPASSWORD="$SOURCE_PG_PASS" "$SOURCE_DOCKER_CONTAINER" \
      psql -U "$SOURCE_PG_USER" -d "$SOURCE_PG_DB" -v ON_ERROR_STOP=1 -f "/tmp/$bname"
  else
    PGPASSWORD="$SOURCE_PG_PASS" psql -h "$SOURCE_PG_HOST" -p "$SOURCE_PG_PORT" \
      -U "$SOURCE_PG_USER" -d "$SOURCE_PG_DB" -v ON_ERROR_STOP=1 -f "$file"
  fi
}

src_dump_schema() {
  if [[ "$SOURCE_TYPE" == "docker" ]]; then
    docker exec -e PGPASSWORD="$SOURCE_PG_PASS" "$SOURCE_DOCKER_CONTAINER" \
      pg_dump -U "$SOURCE_PG_USER" -d "$SOURCE_PG_DB" \
      --schema-only --no-owner --no-privileges --format=plain
  else
    PGPASSWORD="$SOURCE_PG_PASS" pg_dump -h "$SOURCE_PG_HOST" -p "$SOURCE_PG_PORT" \
      -U "$SOURCE_PG_USER" -d "$SOURCE_PG_DB" \
      --schema-only --no-owner --no-privileges --format=plain
  fi
}

tgt_dump_schema() {
  PGPASSWORD="$TARGET_PASS" pg_dump -h localhost -p "$TUNNEL_PORT" \
    -U llm_gateway -d llm_gateway \
    --schema-only --no-owner --no-privileges --format=plain
}

tgt_dump_data() {
  PGPASSWORD="$TARGET_PASS" pg_dump -h localhost -p "$TUNNEL_PORT" \
    -U llm_gateway -d llm_gateway \
    --data-only --no-owner --no-privileges --format=plain "$@"
}

# ── Detect tables that should skip data backup ────────────────────────────
detect_hot_tables() {
  tgt_psql "
  SELECT tablename
  FROM pg_tables WHERE schemaname='public'
    AND (tablename LIKE '%_hot' OR tablename LIKE '%_default'
         OR tablename IN (
           SELECT c.relname FROM pg_inherits i
           JOIN pg_class c ON i.inhrelid = c.oid
           JOIN pg_namespace n ON c.relnamespace = n.oid
           WHERE n.nspname = 'public'
         ))
  ORDER BY 1"
}

# ── Target (252) helpers ─────────────────────────────────────────────────
tgt_psql() {
  PGPASSWORD="$TARGET_PASS" psql -h localhost -p "$TUNNEL_PORT" \
    -U llm_gateway -d llm_gateway -tAc "$1"
}

tgt_psql_file() {
  local file="$1"
  PGPASSWORD="$TARGET_PASS" psql -h localhost -p "$TUNNEL_PORT" \
    -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -f "$file"
}

# ============================================================================
# PHASE 0: PRE-FLIGHT
# ============================================================================
phase "PHASE 0: PRE-FLIGHT [from: $FROM → 252]"

if [[ "$SOURCE_TYPE" == "docker" ]]; then
  if ! docker ps --format '{{.Names}}' | grep -q "^${SOURCE_DOCKER_CONTAINER}\$"; then
    err "Source Docker container not running: $SOURCE_DOCKER_CONTAINER"
    exit 1
  fi
  ok "Source: docker://$SOURCE_DOCKER_CONTAINER ($FROM)"
else
  ok "Source: $SOURCE_TYPE $SOURCE_PG_HOST:$SOURCE_PG_PORT/$SOURCE_PG_DB ($FROM)"
fi

info "Connecting to 252 via tunnel (localhost:$TUNNEL_PORT)..."
if ! PGPASSWORD="$TARGET_PASS" psql -h localhost -p "$TUNNEL_PORT" -U llm_gateway -d llm_gateway -tAc "SELECT 1" >/dev/null 2>&1; then
  err "252 unreachable. Start the tunnel:"
  err "  ssh -f -N -p 25022 -L $TUNNEL_PORT:172.16.2.210:5432 root@115.29.212.252"
  exit 1
fi
ok "252 reachable via localhost:$TUNNEL_PORT"

# ============================================================================
# CHECK MODE (diff report only)
# ============================================================================
if [[ "$MODE" == "check" ]]; then
  phase "SCHEMA DIFF ($FROM vs 252)"
  printf "  %-14s %8s %8s\n" "metric" "$FROM" "252"
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
    csrc=$(src_psql "$q")
    ctgt=$(tgt_psql "$q")
    mark=$([ "$csrc" = "$ctgt" ] && echo "✓" || { MISMATCH=$((MISMATCH + 1)); echo "✗"; })
    printf "  %-14s %8s %8s  %s\n" "$label" "$csrc" "$ctgt" "$mark"
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
# PHASE 0.5: BACKUP 252 (before any DDL changes)
# ============================================================================
phase "PHASE 0.5: BACKUP 252"

BACKUP_BASE="${BACKUP_BASE:-$HOME/backups/sync-schema-to-252}"
BACKUP_TS=$(date +%Y%m%d-%H%M%S)
BACKUP_DIR="$BACKUP_BASE/$BACKUP_TS"
mkdir -p "$BACKUP_DIR/tables"
info "Backup path: $BACKUP_DIR"

# Full schema dump of 252 (safety net for all tables)
info "Dumping full schema from 252..."
tgt_dump_schema > "$BACKUP_DIR/252-predump-schema.sql" 2>/dev/null
ok "$(wc -l < "$BACKUP_DIR/252-predump-schema.sql") lines schema dump done"

# Detect and exclude hot/partition tables from data backup
hot_tables=$(detect_hot_tables || true)
skip_table_count=$(echo "$hot_tables" | wc -l | tr -d ' ')
info "Tables skipped for data backup (hot/default/inherit): $skip_table_count"

# Data backup for regular tables via pg_dump --table
regular_tables=$(comm -23 \
  <(tgt_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1") \
  <(echo "$hot_tables") || true)
regular_count=$(echo "$regular_tables" | wc -l | tr -d ' ')
if [[ "$regular_count" -gt 0 ]]; then
  info "Backing up data for $regular_count regular tables..."
  pg_dump_args=()
  while IFS= read -r tbl; do
    [[ -n "$tbl" ]] && pg_dump_args+=(--table="public.$tbl")
  done <<< "$regular_tables"
  PGPASSWORD="$TARGET_PASS" pg_dump -h localhost -p "$TUNNEL_PORT" \
    -U llm_gateway -d llm_gateway \
    --data-only --no-owner --no-privileges --format=plain \
    "${pg_dump_args[@]}" > "$BACKUP_DIR/252-predump-data.sql" 2>/dev/null || \
    warn "data dump had errors (some tables may be empty)"
  ok "$(wc -l < "$BACKUP_DIR/252-predump-data.sql") lines data dump done"
else
  info "No regular tables detected — skipping data backup"
fi

# Per-table schema snapshot for quick individual restore
while IFS= read -r tbl; do
  [[ -n "$tbl" ]] || continue
  PGPASSWORD="$TARGET_PASS" pg_dump -h localhost -p "$TUNNEL_PORT" \
    -U llm_gateway -d llm_gateway \
    --schema-only --no-owner --no-privileges --format=plain \
    --table="public.$tbl" > "$BACKUP_DIR/tables/${tbl}.sql" 2>/dev/null
done < <(tgt_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1")
ok "Per-table schema snapshots done"

# Backup manifest
cat > "$BACKUP_DIR/backup-manifest.txt" <<EOF
sync-schema-to-252 backup
timestamp: $BACKUP_TS
source_from: $FROM
target: 252 (localhost:$TUNNEL_PORT)
regular_tables_with_data: $regular_count
hot_tables_skipped_data: $skip_table_count
contents:
  252-predump-schema.sql   — full schema of all public tables
  252-predump-data.sql     — data for regular tables only (no hot/default/partition children)
  tables/*.sql             — per-table schema snapshots
EOF
ok "Backup complete"

# ============================================================================
# PHASE 1: DDL SYNC (additive only — no drops, no data)
# ============================================================================
phase "PHASE 1: DDL SYNC"

DUMP="$WORK/source_schema.sql"
info "Dumping $FROM schema..."
src_dump_schema > "$DUMP" 2>/dev/null
sed -i.bak '/^\\(restrict|unrestrict)/d' "$DUMP" 2>/dev/null || true
ok "$(wc -l < "$DUMP") lines from $FROM schema dump"

# ── Step 1a: ADD COLUMN IF NOT EXISTS on 252 ─────────────────────────────
info "Step 1a: ADD COLUMN IF NOT EXISTS"
# Export source column definitions to local file
src_psql "
SELECT table_name || E'\t' || column_name || E'\t' ||
       format_type(a.atttypid, a.atttypmod) || E'\t' ||
       is_nullable || E'\t' || COALESCE(column_default, '')
FROM information_schema.columns c
JOIN pg_attribute a
  ON a.attrelid = (c.table_schema || '.' || c.table_name)::regclass
 AND a.attname = c.column_name
WHERE c.table_schema = 'public' AND c.ordinal_position > 0
" > "$WORK/src_columns.txt"

# Copy to /tmp so psql \copy can read it (psql runs locally for target)
cp "$WORK/src_columns.txt" /tmp/src_columns.txt

cat > "$WORK/add_columns.sql" <<'SQL'
DROP TABLE IF EXISTS _src_cols;
CREATE TEMP TABLE _src_cols (
  table_name text, column_name text, data_type text,
  is_nullable text, column_default text
);
\copy _src_cols from '/tmp/src_columns.txt' with (FORMAT csv, DELIMITER E'\t');
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
    FROM _src_cols src
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

# Always run against 252 (target) — \copy reads from local /tmp
tgt_psql_file "$WORK/add_columns.sql" 2>&1 | grep -E 'Step 1a done' || warn "step 1a had errors"
ok "ADD COLUMN done"

# ── Step 1b: CREATE missing tables on 252 ────────────────────────────────
info "Step 1b: CREATE TABLE IF NOT EXISTS"
missing_tables=$(comm -23 \
  <(src_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1") \
  <(tgt_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1"))

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
  if tgt_psql_file "$WORK/${tbl}.sql" >/dev/null 2>&1; then
    ok "  created $tbl"
  else
    err "  failed to create $tbl"
    exit 1
  fi
done
ok "CREATE TABLE done"

# ── Step 1c: CREATE missing indexes on 252 ───────────────────────────────
info "Step 1c: CREATE INDEX IF NOT EXISTS"
tgt_indexes=$(tgt_psql "SELECT indexname FROM pg_indexes WHERE schemaname='public' ORDER BY 1")

python3 - "$DUMP" "$WORK/missing_indexes.sql" "$tgt_indexes" <<'PY' || true
import sys, re
dump, out_path, existing = sys.argv[1], sys.argv[2], sys.argv[3].split()
with open(dump) as f: c = f.read()
out = ['-- Missing indexes from source', 'BEGIN;']
for m in re.finditer(
    r"CREATE\s+(UNIQUE\s+)?INDEX\s+(\w+)\s+ON\s+public\.\w+\s+.*?;", c, re.DOTALL):
    name = m.group(2)
    if name not in existing:
        out.append(m.group(0))
out.append("COMMIT;")
open(out_path, 'w').write('\n'.join(out))
PY

if [ -s "$WORK/missing_indexes.sql" ] && grep -q "CREATE INDEX" "$WORK/missing_indexes.sql"; then
  out=$(tgt_psql_file "$WORK/missing_indexes.sql" 2>&1 || true)
  echo "$out" | tail -3
  if echo "$out" | grep -q "unsupported access method for the index on columnar table"; then
    info "columnar GIN index creation(s) skipped (expected)"
  fi
fi
ok "CREATE INDEX done"

# ============================================================================
# PHASE 2: VERIFICATION
# ============================================================================
phase "PHASE 2: VERIFICATION"

info "252 DB size: $(tgt_psql "SELECT pg_size_pretty(pg_database_size('llm_gateway'));")"
info "Table count:"
tgt_psql "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';"

info "Final schema diff ($FROM vs 252):"
printf "  %-14s %8s %8s\n" "metric" "$FROM" "252"
for m in \
  "tables:SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" \
  "columns:SELECT count(*) FROM information_schema.columns WHERE table_schema='public'" \
  "indexes:SELECT count(*) FROM pg_indexes WHERE schemaname='public'"
do
  label="${m%%:*}"; q="${m#*:}"
  csrc=$(src_psql "$q")
  ctgt=$(tgt_psql "$q")
  mark=$([ "$csrc" = "$ctgt" ] && echo "✓" || echo "✗")
  printf "  %-14s %8s %8s  %s\n" "$label" "$csrc" "$ctgt" "$mark"
done

echo ""
ok "Schema sync complete ($FROM → 252). No data transferred, no tables dropped."
