#!/usr/bin/env bash
# ============================================================================
# pg-schema-diff.sh — Bidirectional PostgreSQL schema comparison
#
# Compares two PostgreSQL databases table-by-table, column-by-column.
# Reports differences in both directions:
#   - Left → Right: tables/columns in SOURCE but missing in TARGET
#   - Right → Left: tables/columns in TARGET but missing in SOURCE
#
# Usage:
#   ./scripts/pg-schema-diff.sh \
#     --source configs/env-252.sh \
#     --target configs/env-local.sh
#
#   ./scripts/pg-schema-diff.sh \
#     --left-env configs/env-252.sh \
#     --right-env configs/env-local.sh \
#     --output diff-report.md
# ============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

# ── Defaults ──────────────────────────────────────────────────────────────
LEFT_CONFIG=""
RIGHT_CONFIG=""
LEFT_LABEL="LEFT"
RIGHT_LABEL="RIGHT"
OUTPUT_FILE=""
SCHEMA="public"
VERBOSE=false

# ── Parse args ────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source|--left-env)   LEFT_CONFIG="$2"; shift 2 ;;
    --target|--right-env)  RIGHT_CONFIG="$2"; shift 2 ;;
    --left-label)   LEFT_LABEL="$2"; shift 2 ;;
    --right-label)  RIGHT_LABEL="$2"; shift 2 ;;
    --schema)       SCHEMA="$2"; shift 2 ;;
    --output)       OUTPUT_FILE="$2"; shift 2 ;;
    --verbose)      VERBOSE=true; shift ;;
    -h|--help)
      echo "Usage: $0 --source <env-file> --target <env-file> [options]"
      echo ""
      echo "Options:"
      echo "  --source <file>     Left (source) environment config"
      echo "  --target <file>     Right (target) environment config"
      echo "  --left-label <str>  Display label for left (default: LEFT)"
      echo "  --right-label <str> Display label for right (default: RIGHT)"
      echo "  --schema <name>     Schema to compare (default: public)"
      echo "  --output <file>     Write report to file"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

if [[ -z "$LEFT_CONFIG" || -z "$RIGHT_CONFIG" ]]; then
  echo "Both --source and --target are required"
  exit 1
fi

# ── Load configs ──────────────────────────────────────────────────────────
source "$LEFT_CONFIG"
L_HOST="$PG_HOST"; L_PORT="$PG_PORT"; L_USER="$PG_USER"; L_PASS="$PG_PASS"; L_DB="$PG_DB"

source "$RIGHT_CONFIG"
R_HOST="$PG_HOST"; R_PORT="$PG_PORT"; R_USER="$PG_USER"; R_PASS="$PG_PASS"; R_DB="$PG_DB"

# ── Helper ────────────────────────────────────────────────────────────────
left_query() {
  PGPASSWORD="$L_PASS" psql -h "$L_HOST" -p "$L_PORT" -U "$L_USER" -d "$L_DB" -tAq "$@"
}
right_query() {
  PGPASSWORD="$R_PASS" psql -h "$R_HOST" -p "$R_PORT" -U "$R_USER" -d "$R_DB" -tAq "$@"
}

# Generate schema fingerprint query
FINGERPRINT_QUERY="
WITH tbl AS (
  SELECT c.relname AS table_name,
         string_agg(a.attname || ':' || format_type(a.atttypid, a.atttypmod),
                    ',' ORDER BY a.attnum) AS cols
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
  WHERE n.nspname = '%s'
    AND c.relkind = 'r'
  GROUP BY c.relname
)
SELECT table_name || '|' || cols FROM tbl ORDER BY table_name;
"

# Get indexes for a table
INDEX_QUERY="
SELECT indexname || '|' || indexdef
FROM pg_indexes
WHERE schemaname = '%s' AND tablename = '%s';
"

# Get constraints
CONSTRAINT_QUERY="
SELECT conname || '|' || pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_namespace n ON n.oid = c.connamespace
WHERE n.nspname = '%s' AND conrelid::regclass::text = '%s.%s';
"

echo "════════════════════════════════════════════════════════════"
echo "  Bidirectional Schema Comparison"
echo "════════════════════════════════════════════════════════════"
echo ""
echo "  $LEFT_LABEL:  ${L_USER}@${L_HOST}:${L_PORT}/${L_DB}"
echo "  $RIGHT_LABEL: ${R_USER}@${R_HOST}:${R_PORT}/${R_DB}"
echo "  Schema:      $SCHEMA"
echo ""

# ── Get table lists ───────────────────────────────────────────────────────
echo "Collecting schema information from $LEFT_LABEL..."
mapfile -t LEFT_TABLES < <(left_query -c "$(printf "$FINGERPRINT_QUERY" "$SCHEMA")")
LEFT_TOTAL=${#LEFT_TABLES[@]}
echo "  Found $LEFT_TOTAL tables"

echo "Collecting schema information from $RIGHT_LABEL..."
mapfile -t RIGHT_TABLES < <(right_query -c "$(printf "$FINGERPRINT_QUERY" "$SCHEMA")")
RIGHT_TOTAL=${#RIGHT_TABLES[@]}
echo "  Found $RIGHT_TOTAL tables"
echo ""

# ── Parse into associative arrays ─────────────────────────────────────────
declare -A LEFT_DATA RIGHT_DATA
for line in "${LEFT_TABLES[@]}"; do
  [[ -z "$line" ]] && continue
  tbl="${line%%|*}"
  LEFT_DATA[$tbl]="${line#*|}"
done
for line in "${RIGHT_TABLES[@]}"; do
  [[ -z "$line" ]] && continue
  tbl="${line%%|*}"
  RIGHT_DATA[$tbl]="${line#*|}"
done

# ── Find missing tables ───────────────────────────────────────────────────
echo "════════════════════════════════════════════════════════════"
echo "  Tables in $LEFT_LABEL but missing in $RIGHT_LABEL"
echo "════════════════════════════════════════════════════════════"
MISSING_IN_RIGHT=()
for tbl in "${!LEFT_DATA[@]}"; do
  if [[ -z "${RIGHT_DATA[$tbl]:-}" ]]; then
    echo "  ✗ $tbl"
    MISSING_IN_RIGHT+=("$tbl")
  fi
done
MISSING_IN_RIGHT_COUNT=${#MISSING_IN_RIGHT[@]}
if [[ $MISSING_IN_RIGHT_COUNT -eq 0 ]]; then
  echo "  (none)"
fi
echo ""

echo "════════════════════════════════════════════════════════════"
echo "  Tables in $RIGHT_LABEL but missing in $LEFT_LABEL"
echo "════════════════════════════════════════════════════════════"
MISSING_IN_LEFT=()
for tbl in "${!RIGHT_DATA[@]}"; do
  if [[ -z "${LEFT_DATA[$tbl]:-}" ]]; then
    echo "  ✗ $tbl"
    MISSING_IN_LEFT+=("$tbl")
  fi
done
MISSING_IN_LEFT_COUNT=${#MISSING_IN_LEFT[@]}
if [[ $MISSING_IN_LEFT_COUNT -eq 0 ]]; then
  echo "  (none)"
fi
echo ""

# ── Find column differences ───────────────────────────────────────────────
echo "════════════════════════════════════════════════════════════"
echo "  Column differences (in tables present on both sides)"
echo "════════════════════════════════════════════════════════════"

SCHEMA_DIFFS=0
SCHEMA_DIFFS_TABLE=""

for tbl in "${!LEFT_DATA[@]}"; do
  [[ -z "${RIGHT_DATA[$tbl]:-}" ]] && continue

  L_COLS="${LEFT_DATA[$tbl]}"
  R_COLS="${RIGHT_DATA[$tbl]}"

  if [[ "$L_COLS" == "$R_COLS" ]]; then
    continue
  fi

  SCHEMA_DIFFS=$((SCHEMA_DIFFS + 1))

  # Parse columns into arrays
  declare -A L_COL_MAP R_COL_MAP
  IFS=',' read -ra L_ARR <<< "$L_COLS"
  IFS=',' read -ra R_ARR <<< "$R_COLS"
  for c in "${L_ARR[@]}"; do L_COL_MAP[${c%%:*}]="${c#*:}"; done
  for c in "${R_ARR[@]}"; do R_COL_MAP[${c%%:*}]="${c#*:}"; done

  echo ""
  echo "  Table: $tbl"

  # Columns only in LEFT
  ONLY_LEFT=()
  for col in "${!L_COL_MAP[@]}"; do
    if [[ -z "${R_COL_MAP[$col]:-}" ]]; then
      ONLY_LEFT+=("$col:${L_COL_MAP[$col]}")
    fi
  done
  if [[ ${#ONLY_LEFT[@]} -gt 0 ]]; then
    echo "    In $LEFT_LABEL only:"
    for c in "${ONLY_LEFT[@]}"; do echo "      + $c"; done
  fi

  # Columns only in RIGHT
  ONLY_RIGHT=()
  for col in "${!R_COL_MAP[@]}"; do
    if [[ -z "${L_COL_MAP[$col]:-}" ]]; then
      ONLY_RIGHT+=("$col:${R_COL_MAP[$col]}")
    fi
  done
  if [[ ${#ONLY_RIGHT[@]} -gt 0 ]]; then
    echo "    In $RIGHT_LABEL only:"
    for c in "${ONLY_RIGHT[@]}"; do echo "      + $c"; done
  fi

  # Columns with type mismatch
  TYPE_DIFFS=()
  for col in "${!L_COL_MAP[@]}"; do
    if [[ -n "${R_COL_MAP[$col]:-}" ]]; then
      if [[ "${L_COL_MAP[$col]}" != "${R_COL_MAP[$col]}" ]]; then
        TYPE_DIFFS+=("$col: ${L_COL_MAP[$col]} → ${R_COL_MAP[$col]}")
      fi
    fi
  done
  if [[ ${#TYPE_DIFFS[@]} -gt 0 ]]; then
    echo "    Type mismatches:"
    for c in "${TYPE_DIFFS[@]}"; do echo "      ! $c"; done
  fi
done

echo ""

# ── Index and constraint differences ──────────────────────────────────────
echo "════════════════════════════════════════════════════════════"
echo "  Index & constraint differences"
echo "════════════════════════════════════════════════════════════"

INDEX_DIFFS=0
for tbl in "${!LEFT_DATA[@]}"; do
  [[ -z "${RIGHT_DATA[$tbl]:-}" ]] && continue

  L_IDX=$(left_query -c "$(printf "$INDEX_QUERY" "$SCHEMA" "$tbl")" 2>/dev/null | sort)
  R_IDX=$(right_query -c "$(printf "$INDEX_QUERY" "$SCHEMA" "$tbl")" 2>/dev/null | sort)

  if [[ "$L_IDX" != "$R_IDX" ]]; then
    INDEX_DIFFS=$((INDEX_DIFFS + 1))
    if $VERBOSE; then
      echo ""
      echo "  Table: $tbl (indexes differ)"
      echo "    $LEFT_LABEL:"
      echo "$L_IDX" | sed 's/^/      /'
      echo "    $RIGHT_LABEL:"
      echo "$R_IDX" | sed 's/^/      /'
    fi
  fi
done

if [[ $INDEX_DIFFS -eq 0 ]]; then
  echo "  All indexes match ✓"
else
  echo "  $INDEX_DIFFS table(s) have index differences (use --verbose to see details)"
fi
echo ""

# ── Summary ───────────────────────────────────────────────────────────────
echo "════════════════════════════════════════════════════════════"
echo "  SUMMARY"
echo "════════════════════════════════════════════════════════════"
echo ""
echo "  Tables: $LEFT_TOTAL in $LEFT_LABEL, $RIGHT_TOTAL in $RIGHT_LABEL"
echo "  Missing in $RIGHT_LABEL: $MISSING_IN_RIGHT_COUNT table(s)"
echo "  Missing in $LEFT_LABEL:  $MISSING_IN_LEFT_COUNT table(s)"
echo "  Schema differences: $SCHEMA_DIFFS table(s)"
echo "  Index differences:  $INDEX_DIFFS table(s)"
echo ""

# ── Write to file if requested ────────────────────────────────────────────
if [[ -n "$OUTPUT_FILE" ]]; then
  {
    echo "# Schema Comparison Report"
    echo ""
    echo "Generated: $(date '+%Y-%m-%d %H:%M:%S')"
    echo ""
    echo "## Configuration"
    echo ""
    echo "- **$LEFT_LABEL**: ${L_USER}@${L_HOST}:${L_PORT}/${L_DB}"
    echo "- **$RIGHT_LABEL**: ${R_USER}@${R_HOST}:${R_PORT}/${R_DB}"
    echo "- **Schema**: $SCHEMA"
    echo ""
    echo "## Summary"
    echo ""
    echo "- Tables: $LEFT_TOTAL in $LEFT_LABEL, $RIGHT_TOTAL in $RIGHT_LABEL"
    echo "- Missing in $RIGHT_LABEL: $MISSING_IN_RIGHT_COUNT table(s)"
    echo "- Missing in $LEFT_LABEL:  $MISSING_IN_LEFT_COUNT table(s)"
    echo "- Schema differences: $SCHEMA_DIFFS table(s)"
    echo "- Index differences:  $INDEX_DIFFS table(s)"
    echo ""
    echo "## Tables missing in $RIGHT_LABEL"
    echo ""
    if [[ $MISSING_IN_RIGHT_COUNT -eq 0 ]]; then
      echo "(none)"
    else
      for tbl in "${MISSING_IN_RIGHT[@]}"; do echo "- \`$tbl\`"; done
    fi
    echo ""
    echo "## Tables missing in $LEFT_LABEL"
    echo ""
    if [[ $MISSING_IN_LEFT_COUNT -eq 0 ]]; then
      echo "(none)"
    else
      for tbl in "${MISSING_IN_LEFT[@]}"; do echo "- \`$tbl\`"; done
    fi
  } > "$OUTPUT_FILE"
  echo "Report written to: $OUTPUT_FILE"
fi

if [[ $MISSING_IN_RIGHT_COUNT -eq 0 && $MISSING_IN_LEFT_COUNT -eq 0 && $SCHEMA_DIFFS -eq 0 ]]; then
  echo "  ✓ Schema is fully consistent"
else
  echo "  ⚠ Schema differences found"
fi
echo ""