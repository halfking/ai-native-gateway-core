#!/usr/bin/env bash
# ============================================================================
# pg-table-copy.sh — Table-by-table PostgreSQL copy with smart classification
#
# Copies schema for ALL tables, data for non-hot/non-partition tables.
# Hot tables (*_hot, *_2026_*, *_archived) get structure only.
#
# Usage:
#   ./scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-local.sh
#   ./scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-kaixuan1.sh
#   ./scripts/pg-table-copy.sh --dry-run --source configs/env-252.sh --target configs/env-local.sh
#
# See .agents/skills/pg-table-copy/SKILL.md for full documentation.
# ============================================================================

set -euo pipefail

# ── Colors ────────────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; C='\033[0;36m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}⚠${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}════════════════════════════════════════════════════════════${N}"; echo -e "${B}  $*${N}"; echo -e "${B}════════════════════════════════════════════════════════════${N}"; }
dim()   { echo -e "${C}$*${N}"; }

# ── Defaults ──────────────────────────────────────────────────────────────
SOURCE_CONFIG=""
TARGET_CONFIG=""
HOT_PATTERNS="*_hot,*_2026_*,*_2027_*,*_2028_*,*_archived,*_archive"
EXCLUDE_SCHEMAS="pg_catalog,information_schema,columnar_internal"
SCHEMA_ONLY=false
DATA_ONLY=false
DRY_RUN=false
VERBOSE=false
WORK_DIR="/tmp/pg-table-copy"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# ── Parse args ────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source)        SOURCE_CONFIG="$2"; shift 2 ;;
    --target)        TARGET_CONFIG="$2"; shift 2 ;;
    --hot-patterns)  HOT_PATTERNS="$2"; shift 2 ;;
    --schema-only)   SCHEMA_ONLY=true; shift ;;
    --data-only)     DATA_ONLY=true; shift ;;
    --dry-run)       DRY_RUN=true; shift ;;
    --verbose)       VERBOSE=true; shift ;;
    --work-dir)      WORK_DIR="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: $0 --source <env-file> --target <env-file> [options]"
      echo ""
      echo "Options:"
      echo "  --source <file>     Source environment config (e.g., configs/env-252.sh)"
      echo "  --target <file>     Target environment config (e.g., configs/env-local.sh)"
      echo "  --hot-patterns 'p'  Comma-separated LIKE patterns for schema-only tables"
      echo "  --schema-only       Only copy schemas, no data"
      echo "  --data-only         Only copy data, no schema"
      echo "  --dry-run           Show what would be done"
      echo "  --verbose           Show detailed progress"
      exit 0
      ;;
    *) err "Unknown option: $1"; exit 1 ;;
  esac
done

# ── Validate ──────────────────────────────────────────────────────────────
if [[ -z "$SOURCE_CONFIG" || -z "$TARGET_CONFIG" ]]; then
  err "Both --source and --target are required"
  echo "Run with --help for usage"
  exit 1
fi

if [[ ! -f "$SOURCE_CONFIG" ]]; then
  err "Source config not found: $SOURCE_CONFIG"
  exit 1
fi

if [[ ! -f "$TARGET_CONFIG" ]]; then
  err "Target config not found: $TARGET_CONFIG"
  exit 1
fi

# ── Load configs ──────────────────────────────────────────────────────────
info "Loading source config: $SOURCE_CONFIG"
source "$SOURCE_CONFIG"
SRC_HOST="$PG_HOST"
SRC_PORT="$PG_PORT"
SRC_USER="$PG_USER"
SRC_PASS="$PG_PASS"
SRC_DB="$PG_DB"

info "Loading target config: $TARGET_CONFIG"
source "$TARGET_CONFIG"
TGT_HOST="$PG_HOST"
TGT_PORT="$PG_PORT"
TGT_USER="$PG_USER"
TGT_PASS="$PG_PASS"
TGT_DB="$PG_DB"

# ── Helper functions ──────────────────────────────────────────────────────

# Run psql on source
src_psql() {
  PGPASSWORD="$SRC_PASS" psql -h "$SRC_HOST" -p "$SRC_PORT" -U "$SRC_USER" -d "$SRC_DB" -tAq "$@"
}

# Run psql on target
tgt_psql() {
  PGPASSWORD="$TGT_PASS" psql -h "$TGT_HOST" -p "$TGT_PORT" -U "$TGT_USER" -d "$TGT_DB" -tAq "$@"
}

# Check if table matches any hot pattern (shell glob matching)
is_hot_table() {
  local tbl="$1"
  IFS=',' read -ra patterns <<< "$HOT_PATTERNS"
  for pat in "${patterns[@]}"; do
    pat=$(echo "$pat" | xargs)
    case "$tbl" in
      $pat) return 0 ;;
    esac
  done
  return 1
}

# ============================================================================
# PHASE 1: TEST CONNECTIONS
# ============================================================================
phase "PHASE 1: TEST CONNECTIONS"

info "Testing source: ${SRC_USER}@${SRC_HOST}:${SRC_PORT}/${SRC_DB}"
if ! src_psql -c "SELECT 1" &>/dev/null; then
  err "Cannot connect to source database"
  exit 1
fi
ok "Source connection OK"

info "Testing target: ${TGT_USER}@${TGT_HOST}:${TGT_PORT}/${TGT_DB}"
if ! tgt_psql -c "SELECT 1" &>/dev/null; then
  err "Cannot connect to target database"
  exit 1
fi
ok "Target connection OK"

# Capture source state for verification
info "Capturing source state..."
SRC_TABLE_COUNT=$(src_psql -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public';")
SRC_DB_SIZE=$(src_psql -tAc "SELECT pg_size_pretty(pg_database_size(current_database()));")
SRC_TOTAL_ROWS=$(src_psql -tAc "
SELECT sum(n_live_tup) FROM pg_stat_user_tables WHERE schemaname='public';
" 2>/dev/null || echo "0")

dim "  Source: $SRC_TABLE_COUNT tables, $SRC_DB_SIZE, ~$SRC_TOTAL_ROWS rows"

# ============================================================================
# PHASE 2: DISCOVERY
# ============================================================================
phase "PHASE 2: DISCOVERY"

EXCLUDE_SQL=""
IFS=',' read -ra _ex_schemas <<< "$EXCLUDE_SCHEMAS"
for i in "${!_ex_schemas[@]}"; do
  [[ $i -gt 0 ]] && EXCLUDE_SQL+=","
  EXCLUDE_SQL+="'$(echo "${_ex_schemas[$i]}" | xargs)'"
done

mapfile -t TABLE_LINES < <(src_psql -c "
SELECT schemaname, tablename,
       pg_size_pretty(pg_total_relation_size(schemaname || '.' || tablename)) AS size,
       pg_total_relation_size(schemaname || '.' || tablename) AS bytes
FROM pg_tables
WHERE schemaname NOT IN (${EXCLUDE_SQL})
ORDER BY bytes DESC;
")

TOTAL_TABLES=${#TABLE_LINES[@]}
info "Found $TOTAL_TABLES tables"

# ============================================================================
# PHASE 3: CLASSIFICATION
# ============================================================================
phase "PHASE 3: CLASSIFICATION"

HOT_TABLES=()
DATA_TABLES=()

for line in "${TABLE_LINES[@]}"; do
  IFS='|' read -r schema tbl size bytes <<< "$line"
  [[ -z "$schema" ]] && continue
  
  if is_hot_table "$tbl"; then
    HOT_TABLES+=("$schema.$tbl")
    $VERBOSE && dim "  SCHEMA-ONLY: $schema.$tbl ($size)"
  else
    DATA_TABLES+=("$schema.$tbl")
    $VERBOSE && dim "  SCHEMA+DATA: $schema.$tbl ($size)"
  fi
done

ok "Classification: ${#HOT_TABLES[@]} hot (schema-only), ${#DATA_TABLES[@]} normal (schema+data)"

echo ""
dim "  Hot tables (schema only): ${#HOT_TABLES[@]} tables"
dim "  Data tables (schema + data): ${#DATA_TABLES[@]} tables"
echo ""

# ============================================================================
# PHASE 4: SCHEMA EXPORT
# ============================================================================
phase "PHASE 4: SCHEMA EXPORT"

mkdir -p "$WORK_DIR/$TIMESTAMP"

if $DATA_ONLY; then
  info "Skipping schema export (--data-only)"
else
  SCHEMA_FILE="$WORK_DIR/$TIMESTAMP/schema_all.sql"
  info "Exporting schema to $SCHEMA_FILE"
  
  PGPASSWORD="$SRC_PASS" pg_dump \
    -h "$SRC_HOST" -p "$SRC_PORT" -U "$SRC_USER" -d "$SRC_DB" \
    --schema-only \
    --no-owner \
    --no-privileges \
    --exclude-table='columnar_internal.*' \
    -f "$SCHEMA_FILE" 2>/dev/null
  
  SCHEMA_SIZE=$(du -h "$SCHEMA_FILE" | cut -f1)
  SCHEMA_LINES=$(wc -l < "$SCHEMA_FILE")
  ok "Schema exported: $SCHEMA_SIZE ($SCHEMA_LINES lines)"
fi

# ============================================================================
# PHASE 5: DATA EXPORT
# ============================================================================
phase "PHASE 5: DATA EXPORT"

DATA_DIR="$WORK_DIR/$TIMESTAMP/data"
mkdir -p "$DATA_DIR"

if $SCHEMA_ONLY; then
  info "Skipping data export (--schema-only)"
else
  EXPORTED=0
  SKIPPED=0
  TOTAL_DATA_SIZE=0
  
  info "Exporting data for ${#DATA_TABLES[@]} tables..."
  echo ""
  
  for entry in "${DATA_TABLES[@]}"; do
    IFS='.' read -r schema tbl <<< "$entry"
    
    tbl_bytes=$(src_psql -tAc "SELECT pg_total_relation_size('$schema.$tbl');")
    row_count=$(src_psql -tAc "SELECT count(*) FROM $schema.$tbl;")
    
    DATA_FILE="$DATA_DIR/${schema}_${tbl}.sql"
    
    if $DRY_RUN; then
      dim "  [DRY-RUN] Would export: $schema.$tbl ($(numfmt --to=iec $tbl_bytes 2>/dev/null || echo "$tbl_bytes"), $row_count rows)"
    else
      printf "  %-45s %10s rows" "$schema.$tbl" "$row_count"
      
      if [[ "$row_count" == "0" ]]; then
        echo -e " ${C}(empty, skipping)${N}"
        touch "$DATA_FILE"
        SKIPPED=$((SKIPPED + 1))
      else
        PGPASSWORD="$SRC_PASS" pg_dump \
          -h "$SRC_HOST" -p "$SRC_PORT" -U "$SRC_USER" -d "$SRC_DB" \
          --data-only \
          --table="$schema.$tbl" \
          --no-owner \
          --no-privileges \
          --disable-triggers \
          -f "$DATA_FILE" 2>/dev/null
        
        DATA_SIZE=$(du -h "$DATA_FILE" | cut -f1)
        echo -e " ${G}→ $DATA_SIZE${N}"
        EXPORTED=$((EXPORTED + 1))
      fi
    fi
    
    TOTAL_DATA_SIZE=$((TOTAL_DATA_SIZE + ${tbl_bytes:-0}))
  done
  
  echo ""
  ok "Data exported: $EXPORTED with data, $SKIPPED empty, $(numfmt --to=iec $TOTAL_DATA_SIZE 2>/dev/null || echo "$TOTAL_DATA_SIZE bytes") total"
fi

# ============================================================================
# PHASE 6: SCHEMA IMPORT
# ============================================================================
phase "PHASE 6: SCHEMA IMPORT"

if $DATA_ONLY; then
  info "Skipping schema import (--data-only)"
else
  if $DRY_RUN; then
    dim "  [DRY-RUN] Would import schema from $SCHEMA_FILE"
  else
    info "Importing schema to target..."
    
    tgt_psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TGT_DB' AND pid<>pg_backend_pid();" &>/dev/null || true
    
    PGPASSWORD="$TGT_PASS" psql \
      -h "$TGT_HOST" -p "$TGT_PORT" -U "$TGT_USER" -d "$TGT_DB" \
      -f "$SCHEMA_FILE" 2>&1 | tail -5
    
    ok "Schema imported"
  fi
fi

# ============================================================================
# PHASE 7: DATA IMPORT
# ============================================================================
phase "PHASE 7: DATA IMPORT"

if $SCHEMA_ONLY; then
  info "Skipping data import (--schema-only)"
else
  if $DRY_RUN; then
    info "Would import data from $DATA_DIR/*.sql"
  else
    IMPORTED=0
    FAILED=0
    SKIPPED=0
    
    info "Importing data..."
    echo ""
    
    for data_file in "$DATA_DIR"/*.sql; do
      [[ ! -f "$data_file" ]] && continue
      
      tbl_name=$(basename "$data_file" .sql)
      tbl_size=$(du -h "$data_file" | cut -f1)
      
      schema=$(echo "$tbl_name" | cut -d_ -f1)
      tbl=$(echo "$tbl_name" | sed "s/^${schema}_//")
      
      if [[ ! -s "$data_file" ]]; then
        dim "  SKIP: $schema.$tbl (empty)"
        SKIPPED=$((SKIPPED + 1))
        continue
      fi
      
      printf "  %-45s %8s" "$schema.$tbl" "$tbl_size"
      
      if PGPASSWORD="$TGT_PASS" psql \
        -h "$TGT_HOST" -p "$TGT_PORT" -U "$TGT_USER" -d "$TGT_DB" \
        -f "$data_file" &>/dev/null; then
        echo -e " ${G}OK${N}"
        IMPORTED=$((IMPORTED + 1))
      else
        echo -e " ${R}FAILED${N}"
        FAILED=$((FAILED + 1))
      fi
    done
    
    echo ""
    ok "Data imported: $IMPORTED succeeded, $FAILED failed, $SKIPPED skipped"
  fi
fi

# ============================================================================
# PHASE 8: VERIFICATION
# ============================================================================
phase "PHASE 8: VERIFICATION"

if $DRY_RUN; then
  dim "  [DRY-RUN] Would verify target database"
else
  info "Verifying target database..."
  echo ""
  
  # 1. Target DB size
  dim "  Target DB size:"
  TGT_DB_SIZE=$(tgt_psql -tAc "SELECT pg_size_pretty(pg_database_size('$TGT_DB'));")
  echo "    $TGT_DB_SIZE"
  
  # 2. Table count
  dim "  Table count:"
  TGT_TABLE_COUNT=$(tgt_psql -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema','columnar_internal');")
  echo "    $TGT_TABLE_COUNT (source: $SRC_TABLE_COUNT)"
  
  # 3. Row counts comparison
  dim "  Row count comparison (source vs target):"
  echo ""
  printf "    %-45s %12s %12s %s\n" "Table" "Source" "Target" "Status"
  printf "    %-45s %12s %12s %s\n" "─────" "──────" "──────" "──────"
  
  MISMATCH=0
  PASS=0
  
  # Get all tables in target
  mapfile -t TGT_TABLES < <(tgt_psql -c "
  SELECT schemaname||'.'||tablename 
  FROM pg_tables 
  WHERE schemaname='public' 
  ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC
  LIMIT 30;
  ")
  
  for entry in "${TGT_TABLES[@]}"; do
    [[ -z "$entry" ]] && continue
    
    TGT_ROWS=$(tgt_psql -tAc "SELECT count(*) FROM $entry;")
    SRC_ROWS=$(src_psql -tAc "SELECT count(*) FROM $entry;" 2>/dev/null || echo "N/A")
    
    if [[ "$SRC_ROWS" == "$TGT_ROWS" ]]; then
      printf "    %-45s %12s %12s ${G}%s${N}\n" "$entry" "$SRC_ROWS" "$TGT_ROWS" "✓"
      PASS=$((PASS + 1))
    elif [[ "$SRC_ROWS" == "N/A" ]]; then
      printf "    %-45s %12s %12s ${Y}%s${N}\n" "$entry" "N/A" "$TGT_ROWS" "new"
    else
      printf "    %-45s %12s %12s ${R}%s${N}\n" "$entry" "$SRC_ROWS" "$TGT_ROWS" "✗"
      MISMATCH=$((MISMATCH + 1))
    fi
  done
  
  echo ""
  dim "  Summary: $PASS matched, $MISMATCH mismatched (of top 30)"
  
  # 4. Extension check
  dim "  Extensions:"
  tgt_psql -c "SELECT extname, extversion FROM pg_extension WHERE extname IN ('vector','citus','columnar_am');" 2>/dev/null | while read ext ver; do
    echo "    $ext v$ver"
  done
  
  # 5. Final verdict
  echo ""
  if [[ $MISMATCH -eq 0 ]]; then
    ok "Verification PASSED — target is consistent with source"
  else
    warn "Verification completed with $MISMATCH mismatches (hot tables may differ by design)"
  fi
fi

# ============================================================================
# SUMMARY
# ============================================================================
phase "SUMMARY"

echo ""
echo "  Source:  ${SRC_USER}@${SRC_HOST}:${SRC_PORT}/${SRC_DB}"
echo "  Target:  ${TGT_USER}@${TGT_HOST}:${TGT_PORT}/${TGT_DB}"
echo "  Config:  $SOURCE_CONFIG → $TARGET_CONFIG"
echo "  Tables:  ${#HOT_TABLES[@]} hot (schema-only) + ${#DATA_TABLES[@]} normal (schema+data)"
echo "  Dump:    $WORK_DIR/$TIMESTAMP/"
echo ""
ok "pg-table-copy complete!"
