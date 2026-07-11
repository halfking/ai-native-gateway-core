#!/usr/bin/env bash
set -euo pipefail

usage() { printf 'Usage: %s --source ENV --target ENV [--schema NAME] [--left-label NAME] [--right-label NAME] [--output FILE] [--verbose]\n' "$0"; }
die() { printf 'error: %s\n' "$*" >&2; exit 2; }
LEFT_CONFIG=''; RIGHT_CONFIG=''; LEFT_LABEL='LEFT'; RIGHT_LABEL='RIGHT'; SCHEMA='public'; OUTPUT_FILE=''; VERBOSE=0
while (($#)); do
  case "$1" in
    --source|--left-env) (($# >= 2)) || die "$1 requires a value"; LEFT_CONFIG="$2"; shift 2 ;;
    --target|--right-env) (($# >= 2)) || die "$1 requires a value"; RIGHT_CONFIG="$2"; shift 2 ;;
    --left-label) LEFT_LABEL="$2"; shift 2 ;;
    --right-label) RIGHT_LABEL="$2"; shift 2 ;;
    --schema) SCHEMA="$2"; shift 2 ;;
    --output) OUTPUT_FILE="$2"; shift 2 ;;
    --verbose) VERBOSE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ -n "$LEFT_CONFIG" && -n "$RIGHT_CONFIG" ]] || { usage >&2; exit 2; }
[[ -r "$LEFT_CONFIG" && -r "$RIGHT_CONFIG" ]] || die 'environment file is not readable'
command -v psql >/dev/null 2>&1 || die 'psql is required'

load_config() {
  local file="$1"
  unset PG_HOST PG_PORT PG_USER PG_PASS PG_DB
  # shellcheck disable=SC1090
  source "$file"
  : "${PG_HOST:?PG_HOST missing in $file}" "${PG_PORT:?PG_PORT missing in $file}" "${PG_USER:?PG_USER missing in $file}" "${PG_PASS:?PG_PASS missing in $file}" "${PG_DB:?PG_DB missing in $file}"
  printf '%s\037%s\037%s\037%s\037%s\n' "$PG_HOST" "$PG_PORT" "$PG_USER" "$PG_PASS" "$PG_DB"
}
IFS=$'\037' read -r L_HOST L_PORT L_USER L_PASS L_DB < <(load_config "$LEFT_CONFIG")
IFS=$'\037' read -r R_HOST R_PORT R_USER R_PASS R_DB < <(load_config "$RIGHT_CONFIG")

read -r -d '' CATALOG_SQL <<'SQL' || true
WITH tables AS (
  SELECT c.oid, n.nspname, c.relname
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname = :'schema' AND c.relkind IN ('r','p')
), records AS (
  SELECT 'TABLE' AS kind, t.relname AS object_name, '' AS sub_name, '' AS definition FROM tables t
  UNION ALL
  SELECT 'COLUMN', t.relname, a.attname,
         format_type(a.atttypid,a.atttypmod)||'|nullable='||(NOT a.attnotnull)::text||'|default='||COALESCE(pg_get_expr(d.adbin,d.adrelid),'')
  FROM tables t JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum>0 AND NOT a.attisdropped
  LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
  UNION ALL
  SELECT 'INDEX', t.relname, i.relname, pg_get_indexdef(i.oid)
  FROM tables t JOIN pg_index x ON x.indrelid=t.oid JOIN pg_class i ON i.oid=x.indexrelid
  UNION ALL
  SELECT 'CONSTRAINT', t.relname, c.conname, pg_get_constraintdef(c.oid,true)
  FROM tables t JOIN pg_constraint c ON c.conrelid=t.oid
)
SELECT kind||'|'||object_name||'|'||sub_name||'|'||definition FROM records ORDER BY 1;
SQL
query_side() {
  local host="$1" port="$2" user="$3" pass="$4" db="$5" output="$6"
  PGPASSWORD="$pass" psql -X -v ON_ERROR_STOP=1 -v schema="$SCHEMA" -h "$host" -p "$port" -U "$user" -d "$db" -Atq -c "$CATALOG_SQL" | LC_ALL=C sort >"$output"
}
tmpdir="$(mktemp -d)"; trap 'rm -rf "$tmpdir"' EXIT
query_side "$L_HOST" "$L_PORT" "$L_USER" "$L_PASS" "$L_DB" "$tmpdir/left"
query_side "$R_HOST" "$R_PORT" "$R_USER" "$R_PASS" "$R_DB" "$tmpdir/right"

report="$tmpdir/report"
{
  printf 'Schema comparison: %s <-> %s (schema %s)\n' "$LEFT_LABEL" "$RIGHT_LABEL" "$SCHEMA"
  if cmp -s "$tmpdir/left" "$tmpdir/right"; then
    printf 'No differences found.\n'
  else
    printf '\nOnly or changed on %s (-), only or changed on %s (+):\n' "$LEFT_LABEL" "$RIGHT_LABEL"
    diff -u --label "$LEFT_LABEL" --label "$RIGHT_LABEL" "$tmpdir/left" "$tmpdir/right" || true
  fi
} >"$report"
cat "$report"
if [[ -n "$OUTPUT_FILE" ]]; then umask 077; cp "$report" "$OUTPUT_FILE"; chmod 0600 "$OUTPUT_FILE"; fi
cmp -s "$tmpdir/left" "$tmpdir/right" && exit 0
exit 1
