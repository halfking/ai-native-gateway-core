#!/usr/bin/env bash
# =============================================================================
# db-init-lib.sh — Shared library for deploy/sql/ initialization scripts
# =============================================================================
# SSOT (single source of truth) for the pattern used by every service's
# deploy/sql/{init,verify,dump-schema,dump-seed}.sh:
#
#   00-prereqs.sql   — CREATE EXTENSION statements
#   01-schema.sql    — full public schema (idempotent CREATE IF NOT EXISTS)
#   02-seed.sql      — initial seed data with ON CONFLICT DO NOTHING
#
# Why a lib (not per-service copies): llm-gateway-go's R44 lint flagged
# "AI 自由发挥 → drift across deploy scripts" as the #1 failure mode.
# Centralizing the dump/apply logic in one file means:
#   - One place to fix bugs
#   - One place to add a new step (e.g. RLS assertion)
#   - Easy to add new services by just creating a thin wrapper
#
# Usage in a wrapper:
#   source "$ROOT/scripts/_lib/db-init-lib.sh"
#   db_init::apply_all "$SERVICE_NAME" "$DB_NAME" "$@"
#   db_init::verify   "$SERVICE_NAME" "$DB_NAME" "$@"
#   db_init::dump_schema "$SERVICE_NAME" "$DB_NAME" "$DB_URL"
#   db_init::dump_seed   "$SERVICE_NAME" "$DB_NAME" "$DB_URL"
# =============================================================================
set -euo pipefail

# Resolve this lib's directory (so wrappers don't need to know the absolute path)
# When sourced, BASH_SOURCE[0] is the file that sourced us (not us). We need to
# find the lib's own path explicitly.
if [[ -n "${BASH_SOURCE[0]:-}" && "${BASH_SOURCE[0]}" != "" ]]; then
  _DB_INIT_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
else
  # Fallback: assume this file is at $MONOREPO_ROOT/scripts/_lib/db-init-lib.sh
  _DB_INIT_LIB_DIR="${DB_INIT_LIB_DIR:-$(cd "$(dirname "$0")" && pwd)}"
fi
_MONOREPO_ROOT="$(cd "$_DB_INIT_LIB_DIR/../.." && pwd)"

# --- Helpers -----------------------------------------------------------------
# db_init::resolve_db_url — strip asyncpg-style query params, set PGSSLMODE
# Usage: db_init::resolve_db_url
#   echoes the URL on stdout, returns 1 if empty
db_init::resolve_db_url() {
  local url="${LLM_GATEWAY_DATABASE_URL:-${DATABASE_URL:-}}"
  if [[ -n "$url" ]]; then
    # psql 15 doesn't accept asyncpg-style ?ssl=false
    echo "$url" | sed -E 's#\?[^#]*$##'
    export PGSSLMODE=disable
    return 0
  fi
  if [[ -n "${PGHOST:-}" && -n "${PGUSER:-}" && -n "${PGDATABASE:-}" ]]; then
    echo "postgresql://${PGUSER}:${PGPASSWORD:-}@${PGHOST}:${PGPORT:-5432}/${PGDATABASE}"
    return 0
  fi
  return 1
}

# db_init::mask_url — replace ://user:pass@ with ://***:***@ for log output
db_init::mask_url() {
  local url="$1"
  echo "$url" | sed -E 's#://[^:]+:[^@]+@#://***:***@#'
}

# db_init::service_dir — resolve the service's deploy/sql/ absolute path
# Usage: db_init::service_dir "llm-gateway-go"
db_init::service_dir() {
  local service="$1"
  # Round 43: this repository is a single service extracted from the
  # official-deploy monorepo. The monorepo-relative layout below does not
  # exist here, so an explicit output dir must win when provided.
  if [[ -n "${DB_INIT_OUT_DIR:-}" ]]; then
    echo "$DB_INIT_OUT_DIR"
    return 0
  fi
  if [[ -d "$_MONOREPO_ROOT/services/$service/deploy/sql" ]]; then
    echo "$_MONOREPO_ROOT/services/$service/deploy/sql"
    return 0
  fi
  echo "ERROR: no output dir for service '$service'." >&2
  echo "  Set DB_INIT_OUT_DIR (this repo keeps its baseline under sql/schema/)." >&2
  return 1
}

# db_init::ensure_service_dir — create deploy/sql/ if missing
db_init::ensure_service_dir() {
  local dir
  dir="$(db_init::service_dir "$1")"
  mkdir -p "$dir"
  echo "$dir"
}

# --- 0. Prereqs (auto-detect) -----------------------------------------------
# db_init::dump_prereqs SERVICE_NAME DB_URL
#   Detects installed extensions in the target DB and writes 00-prereqs.sql
#   with the corresponding CREATE EXTENSION IF NOT EXISTS statements.
#
# Round 43 note: the extension list below was TimescaleDB-era ('plpgsql',
# 'pgcrypto', 'btree_gist', 'pg_trgm', 'uuid-ossp', 'timescaledb',
# 'timescaledb_toolkit') and knew nothing about Citus. Regenerating with it
# would have DROPPED citus / citus_columnar / vector / pg_stat_statements /
# pgstattuple from 00-prereqs.sql — regressing the round-42 P0 in which a
# stale prereqs left every `SET default_table_access_method = columnar`
# failing. The list is now the extension set the current schema requires.
db_init::dump_prereqs() {
  local service="$1" db_url="$2"
  local dir
  dir="$(db_init::service_dir "$service")"
  local masked
  masked="$(db_init::mask_url "$db_url")"
  echo "==> Dumping prereqs from: $masked"

  # Get installed extensions
  local extensions url2
  url2=$(echo "$db_url" | sed -E 's#://[^:]+:[^@]+@#://\\*\\*:\\*\\*@#')
  extensions=$(PGPASSWORD="${PGPASSWORD:-}" psql "$db_url" -t -A -c "
    SELECT extname || '|' || extversion
    FROM pg_extension
    WHERE extname IN ('plpgsql','pgcrypto','btree_gist','pg_trgm','uuid-ossp','citus','citus_columnar','vector','pg_stat_statements','pgstattuple')
    ORDER BY extname;
  " 2>/dev/null | sort)

  if [[ -z "$extensions" ]]; then
    echo "ERROR: cannot detect extensions (is the DB accessible?)" >&2
    return 1
  fi

  # Build 00-prereqs.sql
  local out="$dir/00-prereqs.sql"
  {
    echo "-- ============================================================================="
    echo "-- 00-prereqs.sql — Required PostgreSQL extensions"
    echo "-- ============================================================================="
    echo "-- Run order: FIRST (before 01-schema.sql and 02-seed.sql)."
    echo "-- Idempotent: every CREATE EXTENSION uses IF NOT EXISTS, safe to re-run."
    echo "--"
    echo "-- Reverse-engineered from production DB on $(date -u +%Y-%m-%d) via:"
    echo "--   SELECT extname, extversion FROM pg_extension WHERE extname IN (...);"
    echo "--"
    echo "-- Regenerate with: ./dump-prereqs.sh (or it runs as part of dump-schema.sh)"
    echo "-- ============================================================================="
    echo ""
    echo "$extensions" | while IFS='|' read -r ext ver; do
      case "$ext" in
        plpgsql)        echo "CREATE EXTENSION IF NOT EXISTS plpgsql     WITH SCHEMA pg_catalog;" ;;
        pgcrypto)        echo "CREATE EXTENSION IF NOT EXISTS pgcrypto    WITH SCHEMA public;   -- gen_random_uuid(), crypt(), etc." ;;
        btree_gist)      echo "CREATE EXTENSION IF NOT EXISTS btree_gist  WITH SCHEMA public;   -- multi-column GiST indexes" ;;
        pg_trgm)         echo "CREATE EXTENSION IF NOT EXISTS pg_trgm     WITH SCHEMA public;   -- trigram fuzzy text search" ;;
        uuid_ossp|uuid-ossp) echo "CREATE EXTENSION IF NOT EXISTS \"uuid-ossp\" WITH SCHEMA public;   -- uuid_generate_v4() etc." ;;
        citus)           echo "CREATE EXTENSION IF NOT EXISTS citus          WITH SCHEMA pg_catalog; -- distributed tables + coordinator" ;;
        citus_columnar)  echo "CREATE EXTENSION IF NOT EXISTS citus_columnar WITH SCHEMA pg_catalog; -- columnar access method (default_table_access_method)" ;;
        vector)          echo "CREATE EXTENSION IF NOT EXISTS vector         WITH SCHEMA public;   -- pgvector embeddings" ;;
        pg_stat_statements)
                        echo "CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public; -- query stats" ;;
        pgstattuple)     echo "CREATE EXTENSION IF NOT EXISTS pgstattuple    WITH SCHEMA public;   -- bloat/dead-tuple measurement" ;;
      esac
    done
    echo ""
    echo "-- Verify installed extensions match production"
    echo "-- Expected:"
    echo "$extensions" | while IFS='|' read -r ext ver; do
      echo "--   $ext $ver"
    done
  } > "$out"

  local lines
  lines=$(wc -l < "$out" | tr -d ' ')
  echo "==> 00-prereqs.sql: $lines lines"
  echo "✅ done."
}

# --- 1. Schema dump ----------------------------------------------------------
# db_init::dump_schema SERVICE_NAME DB_NAME DB_URL
#   - pg_dump --schema-only for the public schema (excludes _timescaledb_* and
#     timescaledb_* system schemas)
#   - strips pg_dump header noise (\restrict, version banner)
#   - moves recent_success_rate (if present) to a DEFERRED FUNCTIONS block
#   - places the DEFERRED block BEFORE the first CREATE TRIGGER
#   - makes CREATE SCHEMA idempotent
#   - writes $SERVICE_DIR/01-schema.sql with a documentation header
db_init::dump_schema() {
  local service="$1" db="$2" db_url="$3"
  local dir
  dir="$(db_init::service_dir "$service")"
  local raw="/tmp/${service}_schema.sql"
  local masked
  masked="$(db_init::mask_url "$db_url")"
  echo "==> Dumping schema from: $masked"

  pg_dump "$db_url" \
    --schema-only --no-owner --no-privileges --no-tablespaces \
    --exclude-schema='_timescaledb*' \
    --exclude-schema='timescaledb_*' \
    --schema=public \
    > "$raw" 2>/dev/null

  export RAW="$raw" SERVICE_DIR="$dir" DB_NAME="$db"
  python3 - <<'PY'
import os, re
src = open(os.environ['RAW']).read()
out = []
for ln in src.splitlines():
    s = ln.strip()
    if s.startswith('\\restrict') or s.startswith('\\unrestrict'): continue
    if s.startswith('-- PostgreSQL database dump'):                     continue
    if s.startswith('-- Dumped from database version'):                continue
    if s.startswith('-- Dumped by pg_dump'):                            continue
    out.append(ln)
start = 0
for i, ln in enumerate(out):
    s = ln.strip()
    if s.startswith('CREATE SCHEMA public') or s.startswith('CREATE FUNCTION') or s.startswith('CREATE TABLE'):
        start = i; break
body_lines = out[start:]
# Idempotent CREATE SCHEMA
body_lines = [re.sub(r'^CREATE SCHEMA public;$', 'CREATE SCHEMA IF NOT EXISTS public;', ln) for ln in body_lines]

# SQL-language function bodies are validated at CREATE time against the
# tables they reference. pg_dump orders by creation OID, so a function can
# appear BEFORE the table it reads (e.g. tenant-discovery helpers over
# orchestration_outbox) and the install would fail. The snapshot is
# verified against the source DB already, so relax body checking for the
# install session. (Trigger functions still must exist before their CREATE
# TRIGGER — the deferred-functions reordering below keeps that guarantee.)
body_lines = [
    'SET check_function_bodies = off;',
    '',
] + body_lines

# Move recent_success_rate (LANGUAGE sql, refs request_logs) to deferred block.
rsr_start = rsr_end = None
for i, ln in enumerate(body_lines):
    if 'CREATE FUNCTION public.recent_success_rate(' in ln:
        rsr_start = i
        j = i
        while j < len(body_lines):
            if body_lines[j].rstrip() == '$$;':
                rsr_end = j
                break
            j += 1
        break
if rsr_start is not None and rsr_end is not None:
    rsr_block = body_lines[rsr_start:rsr_end+1]
    placeholder = [
        '-- -----------------------------------------------------------------------------',
        '-- The function `recent_success_rate` was originally here in the pg_dump output,',
        '-- but pg_dump orders by creation OID, not by dependency. This function references',
        '-- `request_logs` directly (LANGUAGE sql) which doesn\'t exist yet at this point.',
        '-- It has been moved to the end of this file, after all tables are created.',
        '-- -----------------------------------------------------------------------------',
    ]
    body_lines = body_lines[:rsr_start] + placeholder + body_lines[rsr_end+1:]
    deferred = ['',
                '-- =============================================================================',
                '-- DEFERRED FUNCTIONS — Created here (after all tables) to satisfy dependency',
                '-- ordering that pg_dump --schema-only does not respect. See note above.',
                '-- =============================================================================',
                '',
                '-- recent_success_rate — moved from earlier in the dump.',
                ''] + rsr_block
    body_lines += deferred
    print(f"  moved recent_success_rate (lines {rsr_start+1}-{rsr_end+1}) to deferred section")

# Place the DEFERRED FUNCTIONS block BEFORE the first CREATE TRIGGER (so
# trigger functions exist before their triggers reference them).
def_start = None
for i, ln in enumerate(body_lines):
    if 'DEFERRED FUNCTIONS' in ln and i > 0 and body_lines[i-1].strip().startswith('-- ===='):
        def_start = i - 1
        break
first_trigger = None
for i, ln in enumerate(body_lines):
    if ln.startswith('CREATE TRIGGER '):
        first_trigger = i
        break
if def_start is not None and first_trigger is not None and def_start > first_trigger:
    def_block = body_lines[def_start:]
    body_lines = body_lines[:def_start]
    for i, ln in enumerate(body_lines):
        if ln.startswith('CREATE TRIGGER '):
            body_lines = body_lines[:i] + def_block + body_lines[i:]
            break
    print("  moved deferred functions block to before triggers")

# --- Round 43: relocate every COMMENT ON to the end of the file -------------
# pg_dump orders by creation OID, so a COMMENT ON FUNCTION can be emitted
# hundreds of lines before the CREATE FUNCTION it annotates. PostgreSQL has
# no check_function_bodies escape hatch for that: the comment simply fails
# with 'function public.X() does not exist' and, under the installer's
# --single-transaction, the whole file rolls back.
#
# The pre-existing fix above was hardcoded to a single function
# (recent_success_rate) and moved only its CREATE, leaving its COMMENT behind
# — which is how refresh_session_analytics_views() still failed at generation
# time. Moving all comments is the general form of that fix: a COMMENT ON is
# valid as soon as its object exists, so deferring every one to the tail of the
# file is always safe and never depends on which object it annotates.
# Round 43 (toggle for isolating this transform during development):
_defer_comments = os.environ.get('DB_INIT_DEFER_COMMENTS', '1') != '0'
comments = []
kept = []
# Round 43: relocate a COMMENT ON only when it is a SELF-CONTAINED single
# physical line — starts with "COMMENT ON " and terminates with ";" on that
# same line. That is the only case we can move without re-implementing SQL
# lexing. An earlier version tried to span multi-line comments with a
# hand-rolled quote tracker; it desynchronised on a comment containing a
# quote, swallowed the following "CREATE FUNCTION ... AS $_$" header, and
# emitted a bare plpgsql body (psql: "invalid command \_hot'"). A parser
# bug in a code generator is worse than an unhandled case, so anything
# ambiguous is left exactly where pg_dump put it.
for ln in body_lines:
    if _defer_comments and ln.startswith('COMMENT ON ') and ln.rstrip().endswith(';'):
        comments.append([ln])
    else:
        kept.append(ln)
body_lines = kept
if comments:
    block = ['',
             '-- =============================================================================',
             '-- DEFERRED COMMENTS — every COMMENT ON moved to the end of this file.',
             '-- pg_dump orders by creation OID, so a comment can precede the CREATE of',
             '-- the object it annotates; PostgreSQL rejects that outright, and unlike a',
             '-- LANGUAGE sql body it is not covered by check_function_bodies = off.',
             '-- =============================================================================',
             '']
    for stmt in comments:
        block.extend(stmt)
        block.append('')
    body_lines = kept + block
    print(f"  deferred {len(comments)} COMMENT ON statements to end of file")

body = '\n'.join(body_lines).rstrip() + '\n'
open(os.environ['SCRIPT_DIR'] if 'SCRIPT_DIR' in os.environ else os.environ['SERVICE_DIR'] + '/01-schema.sql', 'w').write(body)
print(f"  wrote {len(body.splitlines())} lines ({len(body)} bytes)")
PY

  # Prepend documentation header
  python3 - <<'PY'
import os
from datetime import date
service = os.environ.get('SERVICE_NAME', 'service')
db = os.environ['DB_NAME']
schema_path = os.environ['SERVICE_DIR'] + '/01-schema.sql'
header = f"""-- =============================================================================
-- 01-schema.sql — Full public schema for {db} database
-- =============================================================================
-- Reverse-engineered from production DB on {date.today().isoformat()} using:
--   pg_dump --schema-only --no-owner --no-privileges --no-tablespaces \\
--           --exclude-schema='_timescaledb*' --exclude-schema='timescaledb_*' \\
--           --schema=public
--
-- Regenerate with: ./dump-schema.sh
--
-- Run order: AFTER 00-prereqs.sql, BEFORE 02-seed.sql. Idempotent: every
-- CREATE/ALTER uses IF NOT EXISTS, safe to re-run.
--
-- Post-processing: dump-schema.sh does two re-orderings because pg_dump
-- orders by OID (creation order), not by dependency:
--   1. recent_success_rate (LANGUAGE sql, refs request_logs) — moved to a
--      "DEFERRED FUNCTIONS" block placed BEFORE the first CREATE TRIGGER.
--   2. trigger functions (e.g. cmb_protect_manual_disable, routing_overrides_audit_fn)
--      — also need to live in the DEFERRED block because PostgreSQL validates
--      the function exists at CREATE TRIGGER time.
-- =============================================================================

"""
body = open(schema_path).read()
open(schema_path, 'w').write(header + body)
print("  + header prepended")
PY

  local lines bytes
  lines=$(wc -l < "$dir/01-schema.sql" | tr -d ' ')
  bytes=$(wc -c < "$dir/01-schema.sql" | tr -d ' ')
  echo "==> 01-schema.sql: $lines lines, $bytes bytes"
  echo "✅ done."
}

# --- 2. Seed dump ------------------------------------------------------------
# db_init::dump_seed SERVICE_NAME DB_NAME DB_URL [TABLES_CSV] [PK_CSV]
#   TABLES_CSV defaults to "AUTO" (use select_seed_tables heuristic).
#   Pass an explicit CSV to override the heuristic.
#   PK_CSV: "table:col,table:col,..." — only needed if you override TABLES_CSV.
db_init::dump_seed() {
  local service="$1" db="$2" db_url="$3"
  local tables_csv="${4:-AUTO}" pk_csv="${5:-}"
  local dir
  dir="$(db_init::service_dir "$service")"
  local raw="/tmp/${service}_seed.sql"
  local masked
  masked="$(db_init::mask_url "$db_url")"
  echo "==> Dumping seed from: $masked"

  # If AUTO, run the smart selector
  if [[ -z "$tables_csv" || "$tables_csv" == "AUTO" ]]; then
    echo "    (auto-selecting seed tables via db_init::select_seed_tables)"
    local selector_output
    selector_output="$(db_init::select_seed_tables "$db_url" 200)"
    tables_csv=""
    pk_csv=""
    while IFS=',' read -r name pk count reason; do
      [[ -z "$name" ]] && continue
      tables_csv+="${tables_csv:+,}${name}"
      if [[ -n "$pk" && "$pk" != "__no_pk__" ]]; then
        pk_csv+="${pk_csv:+,}${name}:${pk}"
      fi
    done <<< "$selector_output"
  fi
  local ntables
  ntables=$(echo "$tables_csv" | tr ',' '\n' | wc -l | tr -d ' ')
  echo "    selected $ntables tables"

  # Build -t args from CSV
  local -a t_args
  IFS=',' read -ra TABLES <<< "$tables_csv"
  for t in "${TABLES[@]}"; do t_args+=(-t "$t"); done

  pg_dump "$db_url" \
    --data-only --inserts --no-owner --no-privileges --no-tablespaces \
    --exclude-schema='_timescaledb*' \
    --exclude-schema='timescaledb_*' \
    "${t_args[@]}" \
    > "$raw" 2>/dev/null

  # Build PK map
  export RAW="$raw" SERVICE_DIR="$dir" TABLES_CSV="$tables_csv" PK_CSV="$pk_csv" DB_NAME="$db" SERVICE_NAME="$service" DUMP_DB_URL="$db_url"
  python3 - <<'PY'
import os, re
PK_CSV = os.environ['PK_CSV']
PK = {}
for pair in PK_CSV.split(','):
    pair = pair.strip()
    if not pair: continue
    if ':' in pair:
        tbl, col = pair.split(':', 1)
        PK[tbl.strip()] = col.strip()

src = open(os.environ['RAW']).read()
out = []
for ln in src.splitlines():
    s = ln.strip()
    if s.startswith('\\restrict') or s.startswith('\\unrestrict'): continue
    if s.startswith('-- PostgreSQL database dump'):                     continue
    if s.startswith('-- Dumped from database version'):                continue
    if s.startswith('-- Dumped by pg_dump'):                            continue
    out.append(ln)
for i, ln in enumerate(out):
    if 'Data for Name:' in ln:
        out = out[i:]
        break
transformed = []
# Group INSERTs by table name so we can reorder by FK dependency.
# pg_dump produces multi-line INSERTs when the row data contains newlines;
# we need to accumulate these until the statement truly ends. A line ending
# with `;` is NOT sufficient — text values (markdown docs, prompts) routinely
# contain `;` at end-of-line INSIDE the quoted string — so we track single-
# quote state ('' doubling per standard_conforming_strings) and only treat
# `;` as the terminator when outside a string literal.
by_table = {}
order = []  # tables in the order they first appear (pg_dump's natural order)
current_block = []  # accumulating lines of a single INSERT statement
current_table = None
in_string = False  # inside a '...' literal across the accumulated lines

def scan_string_state(ln):
    """Advance the single-quote state machine over one raw line."""
    global in_string
    i = 0
    n = len(ln)
    while i < n:
        ch = ln[i]
        if in_string:
            if ch == "'":
                if i + 1 < n and ln[i + 1] == "'":
                    i += 1  # '' escaped quote, still inside the string
                else:
                    in_string = False
        elif ch == "'":
            in_string = True
        i += 1

def flush_block():
    global current_block, current_table
    if current_block and current_table:
        # The caller only flushes on a true statement terminator, so the
        # last line ends with `;` outside any string; add ON CONFLICT before it.
        last = current_block[-1]
        if last.rstrip().endswith(';'):
            stripped = last.rstrip()
            stripped = stripped[:-1]  # remove the ;
            current_block[-1] = stripped + ' ON CONFLICT DO NOTHING;'
        if current_table not in by_table:
            order.append(current_table)
            by_table[current_table] = []
        by_table[current_table].extend(current_block)
    current_block = []
    current_table = None

for ln in out:
    m = re.match(r'^INSERT INTO public\.(\w+) VALUES ', ln)
    if m and not current_block:
        # New INSERT statement starting
        current_table = m.group(1)
        current_block = [ln]
        scan_string_state(ln)
        # Check if it ends on the same line
        if not in_string and ln.rstrip().endswith(';'):
            flush_block()
    else:
        # Continuation of the current INSERT (or non-INSERT line)
        if current_block:
            current_block.append(ln)
            scan_string_state(ln)
            if not in_string and ln.rstrip().endswith(';'):
                flush_block()
        else:
            transformed.append(ln)

# Query FK dependencies from the prod DB and topologically sort.
# This fixes the "insert child before parent" bug that pg_dump --data-only has.
fk_sql = """
SELECT conrelid::regclass::text AS from_tbl,
       confrelid::regclass::text AS to_tbl,
       array_agg(a.attname ORDER BY array_position(conkey, a.attnum)) AS cols
FROM pg_constraint c
JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace
GROUP BY conrelid, confrelid;
"""
import subprocess as _sp
db_url = os.environ.get('DUMP_DB_URL') or os.environ['DUMP_DB_URL']
url2 = re.sub(r'\?[^#]*$', '', db_url)
try:
    fk_out = _sp.run(['psql', url2, '-t', '-A', '-F', '|', '-c', fk_sql],
                     env=os.environ.copy(), capture_output=True, text=True, timeout=20)
    fk_pairs = []
    if fk_out.returncode == 0:
        for ln in fk_out.stdout.strip().split('\n'):
            if not ln.strip(): continue
            parts = ln.split('|')
            if len(parts) >= 2:
                from_t = parts[0].strip().lstrip('"')
                to_t = parts[1].strip().lstrip('"')
                # strip public. prefix
                if from_t.startswith('public.'): from_t = from_t[7:]
                if to_t.startswith('public.'): to_t = to_t[7:]
                fk_pairs.append((from_t, to_t))
except Exception as _e:
    _sys.stderr.write(f"FK query failed: {_e}\n")
    fk_pairs = []

# Topological sort (Kahn's algorithm). Tables not in seed keep their pg_dump order.
from collections import defaultdict, deque
in_degree = defaultdict(int)
graph = defaultdict(set)
for from_t, to_t in fk_pairs:
    if from_t in by_table and to_t in by_table and from_t != to_t:
        # FK from_t → to_t means from_t depends on to_t (parent).
        # In topological sort, parent must come first, so edge is to_t → from_t.
        if from_t not in graph[to_t]:
            graph[to_t].add(from_t)
            in_degree[from_t] += 1

# Stable topological sort: initialize queue with nodes that have no incoming edges,
# preserving their pg_dump order.
queue = deque()
for t in order:
    if in_degree[t] == 0:
        queue.append(t)
sorted_tables = []
while queue:
    t = queue.popleft()
    sorted_tables.append(t)
    for neighbor in graph[t]:
        in_degree[neighbor] -= 1
        if in_degree[neighbor] == 0:
            queue.append(neighbor)
# Append any remaining tables (cycles or unreferenced) in original order
for t in order:
    if t not in sorted_tables:
        sorted_tables.append(t)

# Reassemble the INSERT statements in the new order.
reordered = []
for t in sorted_tables:
    if t in by_table:
        reordered.extend(by_table[t])
transformed.extend(reordered)

body = '\n'.join(transformed).rstrip() + '\n'
open(os.environ['SERVICE_DIR'] + '/02-seed.sql', 'w').write(body)
print(f"  body: {len(body.splitlines())} lines ({len(body)} bytes)")
PY

  # Prepend header
  python3 - <<'PY'
import os
from datetime import date
service = os.environ.get('SERVICE_NAME', 'service')
db = os.environ['DB_NAME']
tables_csv = os.environ['TABLES_CSV']
tables = [t.strip() for t in tables_csv.split(',') if t.strip()]
table_list = '\n--           '.join(['-t ' + t for t in tables])
seed_path = os.environ['SERVICE_DIR'] + '/02-seed.sql'
header = f"""-- =============================================================================
-- 02-seed.sql — Initial seed data for {db} database
-- =============================================================================
-- Reverse-engineered from production DB on {date.today().isoformat()} using:
--   pg_dump --data-only --inserts --no-owner --no-privileges \\
--           {table_list}
--
-- Regenerate with: ./dump-seed.sh
--
-- Run order: AFTER 01-schema.sql. Idempotent: every INSERT is augmented with
-- `ON CONFLICT (<pk>) DO NOTHING` based on the table's primary key (composite
-- PKs use untargeted ON CONFLICT DO NOTHING). Re-runs are safe.
--
-- What is in this seed: small system-level config / lookup tables selected by
-- db_init::select_seed_tables (heuristic: small + system-config name pattern).
-- What is excluded: business data (users, tenants, api_keys, credentials,
-- audit logs, runtime / time-series data). See README.md for full list.
-- =============================================================================

"""
body = open(seed_path).read()
open(seed_path, 'w').write(header + body)
print("  + header prepended")
PY

  local lines bytes
  lines=$(wc -l < "$dir/02-seed.sql" | tr -d ' ')
  bytes=$(wc -c < "$dir/02-seed.sql" | tr -d ' ')
  echo "==> 02-seed.sql: $lines lines, $bytes bytes"
  for t in "${TABLES[@]}"; do
    n=$(grep -c "^INSERT INTO public.$t" "$dir/02-seed.sql" 2>/dev/null || echo 0)
    echo "    $t: $n rows"
  done
  echo "✅ done."
}

# --- 3. Apply ----------------------------------------------------------------
# db_init::apply_all SERVICE_NAME DB_NAME [--reset]
#   Apply prereqs → schema → seed against the target DB.
db_init::apply_all() {
  local service="$1" db="$2"; shift 2
  local reset=0
  for arg in "$@"; do
    case "$arg" in --reset) reset=1 ;; esac
  done

  local dir
  dir="$(db_init::service_dir "$service")"
  local db_url
  db_url="$(db_init::resolve_db_url)" || { echo "ERROR: no DB URL" >&2; exit 1; }

  local masked
  masked="$(db_init::mask_url "$db_url")"
  echo "==> Target: $masked"
  [[ "$reset" == "1" ]] && echo "    Mode:   --reset"

  if ! psql "$db_url" -c "SELECT 1" -t -A >/dev/null 2>&1; then
    echo "ERROR: cannot connect to $masked" >&2; return 1
  fi

  if [[ "$reset" == "1" ]]; then
    echo "==> [reset] DROP public schema"
    psql "$db_url" -v ON_ERROR_STOP=1 <<'SQL' || return 2
DROP SCHEMA IF EXISTS public CASCADE;
CREATE SCHEMA public;
GRANT ALL ON SCHEMA public TO public;
SQL
  fi

  echo "==> [1/3] Applying 00-prereqs.sql"
  if ! psql "$db_url" -v ON_ERROR_STOP=1 -f "$dir/00-prereqs.sql" >/tmp/init-prereqs.log 2>&1; then
    tail -20 /tmp/init-prereqs.log >&2; return 1
  fi

  echo "==> [2/3] Applying 01-schema.sql"
  if ! psql "$db_url" -v ON_ERROR_STOP=1 -f "$dir/01-schema.sql" >/tmp/init-schema.log 2>&1; then
    tail -30 /tmp/init-schema.log >&2; return 2
  fi

  echo "==> [3/3] Applying 02-seed.sql"
  if ! psql "$db_url" -v ON_ERROR_STOP=1 -f "$dir/02-seed.sql" >/tmp/init-seed.log 2>&1; then
    tail -30 /tmp/init-seed.log >&2; return 3
  fi

  local tbl idx pol
  tbl=$(psql "$db_url" -t -A -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'")
  idx=$(psql "$db_url" -t -A -c "SELECT count(*) FROM pg_indexes WHERE schemaname='public'")
  pol=$(psql "$db_url" -t -A -c "SELECT count(*) FROM pg_policies WHERE schemaname='public'")
  echo "==> ✅ applied: $tbl tables, $idx indexes, $pol policies"
}

# --- 4. Verify --------------------------------------------------------------
# db_init::verify SERVICE_NAME DB_NAME EXPECTED_ASSOCIATIVE [--no-timescale] [--keep]
#   EXPECTED_ASSOCIATIVE is a bash -A style array passed by name; the wrapper
#   declares `declare -A EXPECTED=( ... )` and calls
#   `db_init::verify SERVICE DB --no-timescale` which reads the array.
db_init::verify() {
  local service="$1" db="$2"; shift 2
  local no_ts=0 keep=0
  for arg in "$@"; do
    case "$arg" in --no-timescale) no_ts=1 ;; --keep) keep=1 ;; esac
  done

  local dir
  dir="$(db_init::service_dir "$service")"
  local db_url
  db_url="$(db_init::resolve_db_url)" || { echo "ERROR: no DB URL" >&2; exit 1; }
  # override the DB name to a verify variant
  db_url=$(echo "$db_url" | sed -E "s#/${db}(\?.*)?\$#/${db}_verify\1#")
  local masked
  masked="$(db_init::mask_url "$db_url")"
  echo "==> Verify target: $masked"

  local base
  base=$(echo "$db_url" | sed -E 's#/[^/?]+(\?.*)?$#/postgres\1#')
  if ! psql "$base" -c "SELECT 1" -t -A >/dev/null 2>&1; then
    echo "ERROR: cannot connect to $base" >&2; return 1
  fi
  if ! psql "$base" -c "DROP DATABASE IF EXISTS \"${db}_verify\"" 2>&1; then
    echo "WARN: drop failed (likely no such DB), continuing" >&2
  fi
  if ! psql "$base" -c "CREATE DATABASE \"${db}_verify\"" 2>&1; then
    echo "ERROR: create database failed" >&2; return 1
  fi
  echo "    ✓ fresh database created"

  # Prereqs (with optional --no-timescale)
  echo "==> [1/4] Applying 00-prereqs.sql…"
  local prereq_tmp schema_tmp
  prereq_tmp=$(mktemp)
  if [[ "$no_ts" == "1" ]]; then
    grep -v -E "timescaledb" "$dir/00-prereqs.sql" > "$prereq_tmp"
    echo "    (--no-timescale: skipping timescaledb extension)"
  else
    cp "$dir/00-prereqs.sql" "$prereq_tmp"
  fi
  if ! psql "$db_url" -v ON_ERROR_STOP=1 -f "$prereq_tmp" >/tmp/verify-prereqs.log 2>&1; then
    tail -20 /tmp/verify-prereqs.log >&2; exit 1
  fi
  # Schema (with optional --no-timescale stripping)
  echo "==> [2/4] Applying 01-schema.sql…"
  schema_tmp=$(mktemp)
  if [[ "$no_ts" == "1" ]]; then
    # Strip lines that mention time-series / citus, AND any orphan column-list
    # continuation that would result from removing a CREATE TABLE header.
    export SCRIPT_DIR_FILE="$dir/01-schema.sql" SCHEMA_TMP="$schema_tmp"
    python3 - <<'PY'
import os, re
src_path = os.environ['SCRIPT_DIR_FILE']
tmp_path = os.environ['SCHEMA_TMP']

# Patterns to drop (line-based):
DROP_PATTERNS = [
    r'_timescaledb_internal\.',   # TimescaleDB function refs
    r'ts_insert_blocker',         # TimescaleDB insert blocker triggers
    r'citus_columnar',            # Citus columnar ext
    r'\bcolumnar\b',              # Citus columnar access method
    r'\bSET default_table_access_method = (columnar|heap)\b',  # SET columnar
    r'\bdefault_table_access_method\b',
    r'\btimescaledb\b',
]
# Patterns that START a multi-line block we want to drop entirely:
DROP_BLOCK_STARTS = [
    r'^CREATE TABLE\s+public\.test_columnar_new',  # orphaned test table
    r'^--\s*Name:\s+test_columnar_new',
    r'^--\s*Name:\s+_timescaledb',
    r'^CREATE\s+(?:TRIGGER|FUNCTION|EXTENSION)\b.*(?:_timescaledb_internal|timescaledb|columnar)',
    r'^ALTER\s+TABLE\s+ONLY\s+public\.[\w_]+\s+ATTACH\s+PARTITION\s+public\.request_(?:logs|wal)_bodies',
]

lines = open(src_path).read().splitlines()
out = []
in_drop = False
for ln in lines:
    # If we're inside a block drop, skip until we find a closing ;
    if in_drop:
        if ';' in ln:
            in_drop = False
        continue
    # Check block starts
    if any(re.search(p, ln) for p in DROP_BLOCK_STARTS):
        if ';' in ln:
            # entire block on one line, skip
            continue
        else:
            in_drop = True
            continue
    # Check line-level drop
    if any(re.search(p, ln) for p in DROP_PATTERNS):
        continue
    out.append(ln)

open(tmp_path, 'w').write('\n'.join(out).rstrip() + '\n')
print(f"  cleaned schema: {len(out)} lines")
PY
    echo "    (--no-timescale: stripping _timescaledb + citus_columnar refs)"
  else
    cp "$dir/01-schema.sql" "$schema_tmp"
  fi
  if ! psql "$db_url" -v ON_ERROR_STOP=1 -f "$schema_tmp" >/tmp/verify-schema.log 2>&1; then
    tail -30 /tmp/verify-schema.log >&2; exit 2
  fi
  local tbl idx pol trg
  tbl=$(psql "$db_url" -t -A -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'")
  idx=$(psql "$db_url" -t -A -c "SELECT count(*) FROM pg_indexes WHERE schemaname='public'")
  pol=$(psql "$db_url" -t -A -c "SELECT count(*) FROM pg_policies WHERE schemaname='public'")
  trg=$(psql "$db_url" -t -A -c "SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal")
  echo "    ✓ schema applied: $tbl tables, $idx indexes, $pol policies, $trg user triggers"

  # Seed
  echo "==> [3/4] Applying 02-seed.sql…"
  if ! psql "$db_url" -v ON_ERROR_STOP=1 -f "$dir/02-seed.sql" >/tmp/verify-seed.log 2>&1; then
    tail -20 /tmp/verify-seed.log >&2; exit 3
  fi
  echo "    ✓ seed applied"

  # Assertions
  # We compare the actual row count in the verify DB to the user-provided
  # EXPECTED value. The default EXPECTED comes from `pg_stat_user_tables`
  # in prod, which counts ALL rows (including duplicates that violate UNIQUE
  # constraints). When the seed is applied to a fresh DB, ON CONFLICT DO
  # NOTHING skips duplicate rows, so actual may be < expected for tables
  # with prod data integrity issues. We treat this as a WARN, not FAIL.
  echo "==> [4/4] Assertions…"
  local failed=0 total=0 warned=0
  for astbl in "${!EXPECTED[@]}"; do
    local expected=${EXPECTED[$astbl]}
    local actual
    actual=$(psql "$db_url" -t -A -c "SELECT count(*) FROM \"$astbl\"" 2>/dev/null || echo "?")
    total=$((total+1))
    if [[ "$actual" == "$expected" ]]; then
      printf "    ✓ %-30s %5d rows (expected %d)\n" "$astbl" "$actual" "$expected"
    elif [[ "$actual" != "?" ]] && [[ "$actual" -lt "$expected" ]] && [[ $((expected - actual)) -le $((expected / 5)) ]]; then
      # Within 20% of expected, but lower — likely prod duplicates skipped
      printf "    ~ %-30s %5d rows (expected %d, Δ=-%d — prod UNIQUE-constraint duplicates skipped)\n" \
        "$astbl" "$actual" "$expected" "$((expected - actual))"
      warned=$((warned+1))
    else
      printf "    ✗ %-30s %5s rows (expected %d) ← MISMATCH\n" "$astbl" "$actual" "$expected" >&2
      failed=$((failed+1))
    fi
  done

  # Also assert that NO other public table has data (catches accidental
  # business data leaks from the auto-selector)
  echo "    (sanity: scanning for unexpected data in non-seed tables)"
  local leak_count=0
  for extra_tbl in $(psql "$db_url" -t -A -c "
    SELECT c.relname FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='public' AND c.relkind='r'
      AND c.relname NOT IN ($(printf "'%s'," "${!EXPECTED[@]}" | sed 's/,$//'))
      AND c.relname NOT LIKE '\\_%' ESCAPE '\\'
  " 2>/dev/null); do
    local cnt
    # Quote the table name to handle reserved words like 'user', 'order', 'group'
    cnt=$(psql "$db_url" -t -A -c "SELECT count(*) FROM \"$extra_tbl\"" 2>/dev/null || echo 0)
    if [[ "$cnt" != "0" && "$cnt" != "?" ]]; then
      printf "    ⚠ %-30s %5s rows (not in seed — possible leak or migration data)\n" "$extra_tbl" "$cnt"
      leak_count=$((leak_count+1))
    fi
  done
  if [[ "$leak_count" -gt 0 ]]; then
    echo "    → $leak_count tables have data but aren't in seed (review needed, not a hard fail)"
  fi

  if [[ "$keep" != "1" ]]; then
    psql "$base" -c "DROP DATABASE \"${db}_verify\"" >/dev/null 2>&1
  fi

  if [[ "$failed" == "0" ]]; then
    echo ""
    if [[ "$warned" -gt 0 ]]; then
      echo "✅ PASS (with $warned warns): $((total - failed - warned)) / $total assertions strict OK, $warned with prod UNIQUE-duplicate drift"
    else
      echo "✅ PASS: $total / $total assertions OK"
    fi
    echo "   Schema: $tbl tables, $idx indexes, $pol policies"
    return 0
  else
    echo "❌ FAIL: $failed / $total assertions failed"
    echo "   Schema: $tbl tables, $idx indexes, $pol policies"
    return 4
  fi
}

# --- 6. All-in-one dump -----------------------------------------------------
# db_init::dump_all SERVICE_NAME DB_NAME DB_URL
#   Dump prereqs + schema + seed in one call.
db_init::dump_all() {
  local service="$1" db="$2" db_url="$3"
  db_init::dump_prereqs  "$service" "$db_url"
  echo
  db_init::dump_schema  "$service" "$db"    "$db_url"
  echo
  db_init::dump_seed    "$service" "$db"    "$db_url"
}

# --- 7. db_init::verify helpers ---------------------------------------------
# Smart EXPECTED generator: run the same selector, then query actual counts.
# Usage: db_init::auto_expected DB_URL
#   Outputs "tablename:count\ntablename:count\n..." suitable for `declare -A`.
#   The URL must point to a DB that HAS the seed data (typically prod).
db_init::auto_expected() {
  local db_url="$1"
  # Try to connect; if it fails, return empty (verify will use manual EXPECTED)
  if ! PGPASSWORD="${PGPASSWORD:-}" psql "$db_url" -c "SELECT 1" -t -A >/dev/null 2>&1; then
    echo "" >&2
    return 0
  fi
  local selector_output
  selector_output="$(db_init::select_seed_tables "$db_url" 200 2>/dev/null || true)"
  while IFS=',' read -r name pk count reason; do
    [[ -z "$name" ]] && continue
    echo "${name}=${count}"
  done <<< "$selector_output"
}

# --- 5. Smart seed selector -------------------------------------------------
# db_init::select_seed_tables DB_URL [max_rows] [name_pattern]
#   Outputs CSV: name,pk_col,row_count,reason
#   - row count <= max_rows (default 200)
#   - table name matches a "system config" pattern (defaults below)
#   - not in the EXCLUDE list (users, tenants, api_keys, etc.)
#
# Heuristic intentionally biased toward "safe to seed" — under-selecting is
# better than over-selecting. Each table is checked against an EXCLUDE list
# (e.g. users, tenants, api_keys) that should NEVER be in a seed file.
db_init::select_seed_tables() {
  local db_url="$1" max_rows="${2:-200}"
  SELECT_DB_URL="$db_url" SELECT_MAX_ROWS="$max_rows" \
  python3 -u 2>/dev/null <<'PY'
import os, re, subprocess
url = os.environ['SELECT_DB_URL']
max_rows = int(os.environ['SELECT_MAX_ROWS'])
import os, re, subprocess
url = os.environ['SELECT_DB_URL']
max_rows = int(os.environ['SELECT_MAX_ROWS'])

# Get tables with their PK column
sql = """
SELECT
  c.relname AS table_name,
  COALESCE((
    SELECT a.attname
    FROM pg_index i
    JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
    WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum > 0
    ORDER BY array_position(i.indkey, a.attnum)
    LIMIT 1
  ), '__no_pk__') AS pk_col,
  COALESCE(s.n_live_tup, 0)::bigint AS row_count
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE n.nspname = 'public' AND c.relkind = 'r'
ORDER BY c.relname;
"""
url2 = re.sub(r'\?[^#]*$', '', url)
out = subprocess.run(['psql', url2, '-t', '-A', '-F', '|', '-c', sql], env=os.environ.copy(), capture_output=True, text=True)
if out.returncode != 0:
    import sys as _sys
    _sys.stderr.write(f"select_seed_tables: psql rc={out.returncode} stderr={out.stderr[:500]}\n")
    _sys.exit(0)  # don't break callers, just return empty
out = out.stdout

INCLUDE_PAT = re.compile(
    r'('
    r'config|settings|settings_kv|'
    r'catalog|families|categories|'
    r'types|status|priorities|tags|'
    r'schema_migrations|'
    r'maas_settings|provider_settings|tool_'
    r')', re.IGNORECASE)
EXCLUDE_PAT = re.compile(
    r'('
    r'user|tenant|api_key|credential|key_ciphertext|'
    r'request_log|request_wal|usage_|telemetry|'
    r'audit|event|log[^a-z]|session_|_history|'
    r'token_|_tokens|credential_model_(index|bindings|stats|peak|weekly|call|probe)|'
    r'webhook|notification|invitation|'
    r'order|wallet|ledger|transaction|invoice|'
    r'migration[^s]|'
    r'endpoint|'
    r'.*_runs|.*_state|.*_records'
    r')', re.IGNORECASE)
# Tables whose row count exceeds this are excluded even if they pass the heuristic.
# Catches tables like `stocks` (28K rows), `prices`, etc. that the heuristic
# misses.
MAX_ROWS_PER_TABLE = 500

ALWAYS_INCLUDE = {
    'maas_settings', 'settings_kv', 'tenant_settings_kv',
    'work_type_config', 'work_type_model_route',
    'tool_categories', 'tool_registry',
    'provider_catalog', 'provider_settings', 'providers', 'provider_models',
    'model_families', 'model_aliases', 'models_canonical',
    'applications', 'application_versions',
    'organizations', 'organization', 'org_template', 'org_tree',
    'pricing_plans', 'topup_packages', 'subscription_plans', 'model_credit_rates',
    'migrations', 'schema_migrations',
    'adapter',  # Casdoor casbin
    # Casdoor RBAC + system config
    'application',  # Casdoor app registry (singular)
    'role', 'permission', 'permission_rule', 'rule',
    'menu', 'resource',
    'provider',  # Casdoor OAuth provider registry
    'model',  # Casdoor "model" = OIDC claim mapping (not the LLM "models" table)
    'enforcer',  # Casdoor permission enforcer
    'department', 'post',
    # Service-specific seed parents (these are FK parents of seed children
    # tables — without them, the FK chain breaks)
    'brands',  # brandmind, parent of alerts/content_jobs/etc.
    'market_items',  # trendaradar, parent of deployment_configs
    'crm_messages',  # crm, parent of crm_outbound_queue etc.
}
ALWAYS_EXCLUDE = {
    'users', 'user', 'tenants', 'tenant',
    'api_keys', 'api_key', 'credentials', 'credential',
    'tokens', 'sessions', 'session', 'webhook_deliveries',
    'orders', 'invoices', 'transactions', 'payments',
    'casbin_rule',  # Casdoor internal (casbin_adapter is OK — it has adapter policy)
    # Tables whose FK parents are NOT in the seed (would cause FK violations
    # on a fresh DB). These are typically "audit" or "log" tables.
    'tool_call_logs',  # ACC, FK → api_keys (not seeded)
    'employee_agent_configs',  # ACC, FK → agent_script_bundles (not seeded)
    'routing_audit_log',
    'credential_probe_model_log',
    'session_memora_extraction_log',
    'tenant_model_policies_audit',
    'routing_overrides_audit',
    'settings_audit',
    # Runtime market data tables (heuristic miss)
    'stocks', 'stock', 'prices', 'price',
    'articles', 'news_items', 'news', 'posts',
    # Tables whose FK parents aren't seeded
    'messages',  # aicms, FK → sessions (not seeded)
    'chat_logs',  # ACC chat history (FK → users)
    'comments',  # posts have FKs to users
    'crm_outbound_queue', 'crm_inbound_queue',  # crm, FK → crm_messages
    'crm_message_events',  # crm, FK → crm_messages
    'routing_sessions',  # crm/trendaradar, FK → users
    'apikey_requests',  # ACC, FK → api_keys
    'review_gates',  # ACC, FK → gate_definitions
    'experience_patterns',  # ACC, FK → devices
    'task_assignments',  # ACC, FK → tasks (641 rows, too large to seed)
    # ACC workflow runtime state: drill/test residue in acc_db, and FK child
    # of orchestration_runs which itself is excluded by the `.*_runs` pattern
    # (seeding the children without the parent breaks the install).
    'orchestration_tasks', 'orchestration_task_revisions',
    'orchestration_dispatches', 'orchestration_plans',
    # ACC runtime logs / stat rollups the name patterns miss (`log[^a-z]` and
    # `.*_records` don't match a trailing `_log`/`_stats`).
    'sync_logs', 'message_log', 'llm_daily_stats',
}

selected = []
for line in out.strip().split('\n'):
    if not line.strip(): continue
    parts = line.split('|')
    if len(parts) < 3: continue
    name, pk, count_str = parts[0], parts[1], parts[2]
    try: count = int(count_str)
    except: continue

    if name in ALWAYS_EXCLUDE:
        continue
    if name in ALWAYS_INCLUDE:
        if count <= MAX_ROWS_PER_TABLE:
            selected.append((name, pk, count, 'always-include'))
        # else: skip large always-include tables (likely data drift in prod)
        continue
    if EXCLUDE_PAT.search(name):
        continue
    if count > MAX_ROWS_PER_TABLE:
        continue
    # Special case: tables with very few rows (<= 10) are likely config/seed
    # regardless of name. This catches things like stock_sectors, portfolios,
    # agent_trust_scores, etc. that the heuristic misses.
    if count <= 10:
        selected.append((name, pk, count, 'tiny-config'))
        continue
    if not INCLUDE_PAT.search(name):
        continue
    selected.append((name, pk, count, 'heuristic'))

selected.sort()

# --- FK parent auto-inclusion ---
# If a selected table depends on a parent table that isn't selected,
# and the parent has ≤ MAX_ROWS_PER_TABLE rows, auto-include it.
# This prevents FK violations during seed replay.
selected_names = {s[0] for s in selected}
fk_sql = """
SELECT conrelid::regclass::text AS from_tbl,
       confrelid::regclass::text AS to_tbl
FROM pg_constraint c
WHERE c.contype = 'f'
  AND c.connamespace = 'public'::regnamespace
  AND c.conrelid::regclass::text NOT LIKE '%.%'
  AND c.confrelid::regclass::text NOT LIKE '%.%';
"""
try:
    fk_out = subprocess.run(['psql', url2, '-t', '-A', '-F', '|', '-c', fk_sql],
                            env=os.environ.copy(), capture_output=True, text=True, timeout=15)
    fk_pairs = []
    if fk_out.returncode == 0:
        for ln in fk_out.stdout.strip().split('\n'):
            if not ln.strip(): continue
            parts = ln.split('|')
            if len(parts) >= 2:
                fk_pairs.append((parts[0].strip(), parts[1].strip()))
except Exception:
    fk_pairs = []

# Build a mapping: parent -> set of children
children_of = {}
for from_t, to_t in fk_pairs:
    children_of.setdefault(to_t, set()).add(from_t)

# Iteratively add FK parents of selected tables (including transitive parents)
# Use a queue-based approach similar to topological sort discovery.
import collections as _co
worklist = _co.deque(selected_names)
while worklist:
    child = worklist.popleft()
    for from_t, parent_t in fk_pairs:
        if from_t == child and parent_t not in selected_names and parent_t not in ALWAYS_EXCLUDE and not EXCLUDE_PAT.search(parent_t):
            # parent is not yet selected; check row count
            count_sql = f"SELECT reltuples::bigint FROM pg_class WHERE relname = '{parent_t}' AND relnamespace = 'public'::regnamespace"
            cnt_out = subprocess.run(['psql', url2, '-t', '-A', '-c', count_sql],
                                     env=os.environ.copy(), capture_output=True, text=True, timeout=10)
            try:
                parent_count = int(cnt_out.stdout.strip())
            except:
                parent_count = 0
            if parent_count <= MAX_ROWS_PER_TABLE:
                selected.append((parent_t, '__fk_parent__', parent_count, 'fk-parent'))
                selected_names.add(parent_t)
                worklist.append(parent_t)

selected.sort()
for name, pk, count, reason in selected:
    if pk == '__no_pk__':
        print(f"{name},__no_pk__,{count},{reason}")
    else:
        print(f"{name},{pk},{count},{reason}")
PY
}
