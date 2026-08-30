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
CLEAN_SCHEMA=false
REPLACE_DATA=false
WORK_DIR="/tmp/pg-table-copy"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
SCHEMA_IMPORT_FAILED=false
DATA_IMPORT_FAILED=false
PGOPTIONS="${PGOPTIONS:-}"

# ── Parse args ────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source)        SOURCE_CONFIG="$2"; shift 2 ;;
    --target)        TARGET_CONFIG="$2"; shift 2 ;;
    --hot-patterns)  HOT_PATTERNS="$2"; shift 2 ;;
    --schema-only)   SCHEMA_ONLY=true; shift ;;
    --data-only)     DATA_ONLY=true; shift ;;
    --clean-schema)  CLEAN_SCHEMA=true; shift ;;
    --replace-data)  REPLACE_DATA=true; shift ;;
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
      echo "  --clean-schema      DROP/CREATE schema objects during import (destructive; opt-in)"
      echo "  --replace-data      TRUNCATE classified normal tables before data import"
      echo "                       (required for exact source→target replacement)"
      echo "  --dry-run           Show what would be done"
      echo "  --verbose           Show detailed progress"
      echo ""
      echo "Environment:"
      echo "  PGOPTIONS          Passed to psql (env var only). Recommended:"
      echo "                       export PGOPTIONS='-c statement_timeout=0'"
      echo "                     252 enforces a 30s server-side statement_timeout,"
      echo "                     which trips count(*) and large COPY streams."
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

# ── Local docker target detection ─────────────────────────────────────────
# On macOS the host may run a Homebrew PostgreSQL on localhost:5432 that shadows
# the docker container's published port. When the target is the local docker
# container (TARGET_TYPE=docker + DOCKER_PG_CONTAINER), route all target
# access through `docker exec -i` instead of `psql -h localhost`.
# This mirrors sync-from-252.sh and avoids hitting the wrong instance
# (see skill pg-sync-252-to-env Q0).
TGT_IS_LOCAL_DOCKER=false
TGT_CONTAINER="${DOCKER_PG_CONTAINER:-}"
if [[ "${TARGET_TYPE:-}" == "docker" && -n "$TGT_CONTAINER" ]]; then
  TGT_IS_LOCAL_DOCKER=true
  # The target env file may inherit a stale DOCKER_HOST from a previously sourced
  # remote config (e.g. env-252.sh sets DOCKER_HOST to an SSH endpoint). Reset it
  # so plain `docker` calls target the host daemon.
  unset DOCKER_HOST
  info "Target is local docker container: $TGT_CONTAINER (bypassing localhost:5432)"
fi

# ── Helper functions ──────────────────────────────────────────────────────

# Run psql on source (respect PGOPTIONS for statement_timeout overrides).
# Note: PGOPTIONS must be an environment variable — passing it on the psql CLI
# line is parsed as a query and produces "syntax error at or near
# statement_timeout". psql honors PGOPTIONS automatically.
src_psql() {
  PGOPTIONS="$PGOPTIONS" PGPASSWORD="$SRC_PASS" psql -h "$SRC_HOST" -p "$SRC_PORT" -U "$SRC_USER" -d "$SRC_DB" -tAq "$@"
}

# Run psql on target. Local docker → docker exec; remote → network psql.
tgt_psql() {
  if $TGT_IS_LOCAL_DOCKER; then
    PGOPTIONS="$PGOPTIONS" PGPASSWORD="$TGT_PASS" docker exec -i \
      -e PGOPTIONS="$PGOPTIONS" -e PGPASSWORD="$TGT_PASS" "$TGT_CONTAINER" \
      psql -U "$TGT_USER" -d "$TGT_DB" -tAq "$@"
  else
    PGOPTIONS="$PGOPTIONS" PGPASSWORD="$TGT_PASS" psql -h "$TGT_HOST" -p "$TGT_PORT" -U "$TGT_USER" -d "$TGT_DB" -tAq "$@"
  fi
}

# Import a SQL file into the target. Caller can pass extra psql args as a
# whitespace-separated string (currently only --single-transaction).
tgt_psql_file() {
  local f="$1"
  local extra_args="${2:-}"
  if $TGT_IS_LOCAL_DOCKER; then
    docker exec -i -e PGOPTIONS="$PGOPTIONS" -e PGPASSWORD="$TGT_PASS" "$TGT_CONTAINER" \
      psql -U "$TGT_USER" -d "$TGT_DB" -v ON_ERROR_STOP=1 $extra_args < "$f"
  else
    PGOPTIONS="$PGOPTIONS" PGPASSWORD="$TGT_PASS" psql -h "$TGT_HOST" -p "$TGT_PORT" -U "$TGT_USER" -d "$TGT_DB" \
      -v ON_ERROR_STOP=1 $extra_args -f "$f"
  fi
}

# Check if table matches any configured hot pattern (shell glob matching).
is_hot_table() {
  local tbl="$1" pat
  IFS=',' read -ra patterns <<< "$HOT_PATTERNS"
  for pat in "${patterns[@]}"; do
    pat=$(echo "$pat" | xargs)
    case "$tbl" in
      $pat) return 0 ;;
    esac
  done
  return 1
}

# Return 0 when a source catalog relation must not receive data. This uses
# pg_inherits/relispartition rather than names alone, so unnamed/default
# partitions are protected even when they do not match HOT_PATTERNS.

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

# A catalog entry without its extension library makes pg_dump fail when it
# reaches columns using the affected type. Refuse destructive sync before the
# target has been backed up successfully.
for ext in vector citus citus_columnar; do
  if [[ "$(tgt_psql -tAc "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname='$ext')")" == "t" ]]; then
    libname="$ext"
    if ! tgt_psql -tAc "LOAD '$libname'" &>/dev/null; then
      err "Target extension $ext is registered but $libname.so is unavailable"
      err "Repair the PG image before backup or destructive synchronization"
      exit 1
    fi
  fi
done

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
       pg_total_relation_size(schemaname || '.' || tablename) AS bytes,
       CASE WHEN c.relkind = 'p' OR c.relispartition OR pt.partrelid IS NOT NULL THEN 't' ELSE 'f' END AS catalog_hot
FROM pg_tables t
JOIN pg_class c ON c.relname=t.tablename
JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname=t.schemaname
LEFT JOIN pg_partitioned_table pt ON pt.partrelid=c.oid
WHERE t.schemaname NOT IN (${EXCLUDE_SQL})
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
  IFS='|' read -r schema tbl size _bytes catalog_hot <<< "$line"
  [[ -z "$schema" ]] && continue
  
  if [[ "$catalog_hot" == "t" ]] || is_hot_table "$tbl"; then
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
  
  # `--clean` is deliberately opt-in: pg_dump emits DROP statements for hot
  # tables/partitions too, which can delete target data even though hot data is
  # not exported. Use --clean-schema only when the target is disposable.
  schema_clean_args=()
  if $CLEAN_SCHEMA; then
    schema_clean_args+=(--clean --if-exists)
    warn "--clean-schema enabled: target schema objects (including hot tables) may be dropped"
  else
    info "Safe schema mode: no DROP statements; existing target data is preserved"
  fi
  dump_err="$WORK_DIR/$TIMESTAMP/schema_dump.err"
  if ! PGOPTIONS="${PGOPTIONS:-}" PGPASSWORD="$SRC_PASS" pg_dump \
    -h "$SRC_HOST" -p "$SRC_PORT" -U "$SRC_USER" -d "$SRC_DB" \
    --schema-only --no-owner --no-privileges \
    "${schema_clean_args[@]}" \
    --exclude-schema='columnar_internal' --exclude-schema='citus' \
    -f "$SCHEMA_FILE" 2>"$dump_err"; then
    err "Schema export failed; see $dump_err"; exit 1
  fi
  if [[ ! -s "$SCHEMA_FILE" ]]; then
    err "Schema export produced an empty file"; exit 1
  fi
  if grep -q "set_config('search_path', '', false)" "$SCHEMA_FILE"; then
    tmp_schema="$SCHEMA_FILE.fixed"
    grep -vE "set_config\('search_path', '', false\)" "$SCHEMA_FILE" > "$tmp_schema"
    mv "$tmp_schema" "$SCHEMA_FILE"
    ok "Stripped pg_dump search_path='' guard before import"
  fi
  SCHEMA_SIZE=$(du -h "$SCHEMA_FILE" | cut -f1)
  SCHEMA_LINES=$(wc -l < "$SCHEMA_FILE")
  ok "Schema exported: $SCHEMA_SIZE ($SCHEMA_LINES lines)"
fi

# ============================================================================
# PHASE 5: DATA EXPORT
# ============================================================================
phase "PHASE 5: DATA EXPORT"

DATA_DIR="$WORK_DIR/$TIMESTAMP/data"
MANIFEST_FILE="$WORK_DIR/$TIMESTAMP/data_manifest.tsv"
mkdir -p "$DATA_DIR"
: > "$MANIFEST_FILE"

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
        printf '%s\t%s\t%s\t%s\n' "$DATA_FILE" "$schema" "$tbl" "$row_count" >> "$MANIFEST_FILE"
        SKIPPED=$((SKIPPED + 1))
      else
        dump_err="$DATA_FILE.err"
        if ! PGOPTIONS="${PGOPTIONS:-}" PGPASSWORD="$SRC_PASS" pg_dump \
          -h "$SRC_HOST" -p "$SRC_PORT" -U "$SRC_USER" -d "$SRC_DB" \
          --data-only --table="$schema.$tbl" --no-owner --no-privileges \
          --disable-triggers -f "$DATA_FILE" 2>"$dump_err"; then
          err "Data export failed for $schema.$tbl; see $dump_err"
          exit 1
        fi
        if [[ ! -s "$DATA_FILE" ]]; then
          err "Data export produced an empty file for non-empty table $schema.$tbl"
          exit 1
        fi
        printf '%s\t%s\t%s\t%s\n' "$DATA_FILE" "$schema" "$tbl" "$row_count" >> "$MANIFEST_FILE"
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
    info "Importing schema to target (with --clean: existing objects are dropped then recreated)..."

    tgt_psql -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TGT_DB' AND pid<>pg_backend_pid();" &>/dev/null || true

    # pg_dump emits `SELECT pg_catalog.set_config('search_path', '', false)` for
    # security. On databases carrying the columnar event trigger
    # (enforce_columnar_trigger -> columnar_insert_only_parents()) — present on
    # BOTH 252 and local — an empty search_path makes the trigger fail to resolve
    # its function and EVERY CREATE/DROP TABLE errors out. All objects in the
    # dump are already schema-qualified, so strip the guard before importing.
    SCHEMA_IMPORT_FILE="$SCHEMA_FILE"
    if grep -q "set_config('search_path', '', false)" "$SCHEMA_FILE" 2>/dev/null; then
      SCHEMA_IMPORT_FILE="$SCHEMA_FILE.fixed"
      grep -vE "set_config\('search_path', '', false\)" "$SCHEMA_FILE" > "$SCHEMA_IMPORT_FILE"
      ok "Stripped pg_dump search_path='' guard (it breaks the columnar event trigger on import)"
    fi

    # --clean mode emits per-object DROP IF EXISTS; "does not exist, skipping"
    # notices are expected. Capture full output for diagnosis, show tail + errors.
    SCHEMA_IMPORT_LOG="$WORK_DIR/$TIMESTAMP/schema_import.log"
    import_exit=0
    tgt_psql_file "$SCHEMA_IMPORT_FILE" > "$SCHEMA_IMPORT_LOG" 2>&1 || import_exit=$?

    # ALWAYS scan the log for real errors. With ON_ERROR_STOP=off psql exits 0
    # even when statements fail, so the exit code alone proves nothing: the
    # 2026-08-26 sync reported "Schema imported" while the DDL had actually
    # failed on the columnar trigger, leaving stale schema behind (this hid
    # migrations 573/610 and was only caught by the 2026-08-31 structure audit).
    SCHEMA_ERRORS=$(grep -cE "(ERROR|FATAL):" "$SCHEMA_IMPORT_LOG" 2>/dev/null || true)
    SCHEMA_ERRORS=${SCHEMA_ERRORS:-0}
    if [ "$import_exit" -ne 0 ] || [ "$SCHEMA_ERRORS" -gt 0 ]; then
      err "Schema import FAILED (psql exit=$import_exit, $SCHEMA_ERRORS error lines):"
      grep -E "(ERROR|FATAL):" "$SCHEMA_IMPORT_LOG" | head -15
      err "Full log: $SCHEMA_IMPORT_LOG"
      SCHEMA_IMPORT_FAILED=true
    else
      ok "Schema imported (0 error lines; 'does not exist, skipping' notices are expected with --clean)"
    fi
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
    PARTIAL=0
    IMPORT_LOG="$WORK_DIR/$TIMESTAMP/data_import.log"

    info "Importing data..."
    echo ""

    if $REPLACE_DATA; then
      if [[ ! -s "$MANIFEST_FILE" ]]; then
        err "--replace-data requires a populated data manifest"; exit 1
      fi
      info "Checking foreign-key safety before replacing normal-table data..."
      owned_tables_sql=""
      while IFS=$'\t' read -r _manifest_file schema tbl _expected_rows; do
        [[ -z "$tbl" ]] && continue
        [[ -n "$owned_tables_sql" ]] && owned_tables_sql+=","
        owned_tables_sql+="'${schema}.${tbl}'"
      done < "$MANIFEST_FILE"
      unsafe_fk=$(tgt_psql -tAc "
        WITH owned(rel) AS (VALUES (${owned_tables_sql}))
        SELECT count(*) FROM pg_constraint c
        JOIN pg_class child ON child.oid=c.conrelid
        JOIN pg_class parent ON parent.oid=c.confrelid
        JOIN pg_namespace n1 ON n1.oid=child.relnamespace
        JOIN pg_namespace n2 ON n2.oid=parent.relnamespace
        WHERE c.contype='f' AND n1.nspname='public' AND n2.nspname='public'
          AND (n1.nspname||'.'||child.relname) NOT IN (SELECT rel FROM owned)
          AND (n2.nspname||'.'||parent.relname) IN (SELECT rel FROM owned);")
      if [[ "${unsafe_fk:-0}" != "0" ]]; then
        err "--replace-data refused: $unsafe_fk FK(s) from tables outside the manifest would make replacement unsafe"
        exit 1
      fi
      truncate_tables_sql=""
      while IFS=$'\t' read -r _manifest_file schema tbl _expected_rows; do
        [[ -z "$tbl" ]] && continue
        [[ -n "$truncate_tables_sql" ]] && truncate_tables_sql+=","
        truncate_tables_sql+="\"$schema\".\"$tbl\""
      done < "$MANIFEST_FILE"
      tgt_psql -c "TRUNCATE TABLE $truncate_tables_sql RESTART IDENTITY;" >> "$IMPORT_LOG" 2>&1 || {
        err "failed to atomically clear classified normal tables"; exit 1;
      }
    else
      warn "append mode: existing target rows are preserved; use --replace-data for exact replacement"
    fi

    while IFS=$'\t' read -r data_file schema tbl expected_rows; do
      [[ -z "$tbl" ]] && continue
      tbl_size=$(du -h "$data_file" | cut -f1)

      if [[ ! -s "$data_file" ]]; then
        dim "  SKIP: $schema.$tbl (empty)"
        SKIPPED=$((SKIPPED + 1))
        continue
      fi

      printf "  %-45s %8s" "$schema.$tbl" "$tbl_size"
      import_err="$WORK_DIR/$TIMESTAMP/${schema}_${tbl}.import.err"
      if tgt_psql_file "$data_file" "--single-transaction" >>"$IMPORT_LOG" 2>"$import_err"; then
        echo -e " ${G}OK${N}"
        IMPORTED=$((IMPORTED + 1))
      else
        echo -e " ${R}FAILED${N}"
        printf "    [stderr] %s\n" "$(head -3 "$import_err" | tr -d '\r' | tr '\n' ' ' | head -c 200)"
        cat "$import_err" >> "$IMPORT_LOG"
        FAILED=$((FAILED + 1))
      fi
      rm -f "$import_err"
    done < "$MANIFEST_FILE"

    echo ""
    if [[ $FAILED -gt 0 || $PARTIAL -gt 0 ]]; then
      err "Data import FAILED: $IMPORTED succeeded, $FAILED failed, $PARTIAL partial, $SKIPPED skipped"
      err "Data import log: $IMPORT_LOG"
      DATA_IMPORT_FAILED=true
    else
      ok "Data imported: $IMPORTED succeeded, $FAILED failed, $PARTIAL partial, $SKIPPED skipped"
    fi
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

if $SCHEMA_IMPORT_FAILED || $DATA_IMPORT_FAILED; then
  if $SCHEMA_IMPORT_FAILED; then
    err "pg-table-copy completed WITH SCHEMA IMPORT ERRORS — target schema may be stale or partial."
    err "Do NOT trust migration-tracking tables copied as data: they can claim migrations"
    err "are applied while the DDL never landed. Fix the errors above and re-run, then"
  fi
  if $DATA_IMPORT_FAILED; then
    err "pg-table-copy completed WITH DATA IMPORT ERRORS — target data is partial or stale."
    err "Fix the errors above and re-run with --replace-data, then"
  fi
  err "verify with scripts/local-dev/verify-db-consistency.sh and scripts/local-dev/verify-db-data-consistency.sh."
  exit 1
fi

ok "pg-table-copy complete!"
