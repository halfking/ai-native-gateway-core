#!/usr/bin/env bash
#
# ╔══════════════════════════════════════════════════════════════════════════╗
# ║  DEPRECATED — Use scripts/sync-from-252.sh instead                     ║
# ║  This script handled schema-only sync. The replacement also handles    ║
# ║  cold table data sync and partition detection in a single invocation.  ║
# ╚══════════════════════════════════════════════════════════════════════════╝
#
# sync-252-schema-only.sh — Sync only the SCHEMA (DDL) from 252 to local.
#
# Use case:
#   You want to bring local PG (Docker, llm-gateway-pg) up to 252's schema
#   without overwriting any HOT or partition data.
#
# Approach:
#   - pg_dump --schema-only to get a DDL snapshot of 252 (no row data)
#   - Extract missing columns -> ADD COLUMN IF NOT EXISTS on local
#   - Extract missing tables   -> CREATE TABLE (only for tables not in local)
#   - Extract missing indexes  -> CREATE INDEX IF NOT EXISTS
#   - Extract missing PK       -> ALTER TABLE ADD CONSTRAINT
#   - Business data in hot + partitioned tables is left untouched
#
# Usage:
#   ./scripts/sync-252-schema-only.sh            # default: apply sync
#   ./scripts/sync-252-schema-only.sh --check    # only show diff counts
#   ./scripts/sync-252-schema-only.sh --tables=foo,bar  # sync only specific tables
#
# Prereqs:
#   - SSH tunnel to 252 is up (default localhost:15432 -> 115.29.212.252)
#   - Local PG is reachable via docker (default llm-gateway-pg container)
#
# Connection defaults match configs/env-252.sh and the dev compose.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

# ── Defaults matching configs/env-252.sh ───────────────────────
LOCAL_CONTAINER="${LOCAL_CONTAINER:-llm-gateway-pg}"
LOCAL_USER="${LOCAL_USER:-llm_gateway}"
LOCAL_PASS="${LOCAL_PASS:-llm_gateway_db_pass_2026_secure}"
LOCAL_DB="${LOCAL_DB:-llm_gateway}"

ENV_252="$ROOT_DIR/configs/env-252.sh"
if [ -f "$ENV_252" ]; then
  # Source 252 env but reset DOCKER_HOST so local docker still targets the host
  set -a
  # shellcheck disable=SC1090
  . "$ENV_252"
  set +a
  unset DOCKER_HOST
fi
TUNNEL_PORT="${TUNNEL_LOCAL_PORT:-15432}"
REMOTE_HOST="${REMOTE_SSH_HOST:-root@115.29.212.252}"
REMOTE_PORT="${REMOTE_SSH_PORT:-25022}"
REMOTE_KEY="${REMOTE_SSH_IDENTITY:-$HOME/.ssh/56_id_rsa}"
REMOTE_CONTAINER="${REMOTE_DB_CONTAINER:-pg-252-pg17}"

SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=20 -o IdentitiesOnly=yes -o PreferredAuthentications=publickey"
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; N='\033[0m'

MODE=apply
ONLY_TABLES=""
case "${1:-}" in
  --check)   MODE=check ;;
  --tables=*) ONLY_TABLES="${1#--tables=}" ;;
  -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
  "")        : ;;
  *)         echo "unknown option: $1"; exit 1 ;;
esac

ok()   { printf "${G}✓${N} %s\n" "$*"; }
info() { printf "${Y}▶${N} %s\n" "$*"; }
err()  { printf "${R}✗${N} %s\n" "$*" >&2; }

if ! docker ps --format '{{.Names}}' | grep -q "^${LOCAL_CONTAINER}\$"; then
  err "Local PG container not running: $LOCAL_CONTAINER"
  exit 1
fi

# ── Remote helpers (run on 252 via SSH) ──────────────────────
remote_dump() {
  ssh $SSH_OPTS -p "$REMOTE_PORT" -i "$REMOTE_KEY" "$REMOTE_HOST" \
    "docker exec $REMOTE_CONTAINER pg_dump -U llm_gateway -d llm_gateway \
     --schema-only --no-owner --no-privileges --format=plain"
}
remote_psql() {
  ssh $SSH_OPTS -p "$REMOTE_PORT" -i "$REMOTE_KEY" "$REMOTE_HOST" \
    "docker exec $REMOTE_CONTAINER psql -U llm_gateway -d llm_gateway -tAc \"$1\""
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

# ── Pre-flight: 252 reachable ─────────────────────────────
info "Pre-flight: connecting to 252 via SSH tunnel (localhost:$TUNNEL_PORT)"
: "${PG_PASS:?PG_PASS must be set for the 252 tunnel}"
if ! PGPASSWORD="$PG_PASS" \
     psql -h localhost -p "$TUNNEL_PORT" -U llm_gateway -d llm_gateway \
     -tAc "SELECT 1" >/dev/null 2>&1; then
  err "Cannot reach 252. Start the tunnel first:"
  err "    ssh -f -N -p $REMOTE_PORT -L $TUNNEL_PORT:172.16.2.210:5432 $REMOTE_HOST"
  exit 1
fi
ok "252 reachable"

# ── Quick count check mode ─────────────────────────────
if [ "$MODE" = "check" ]; then
  info "Schema diff (252 vs local)"
  printf "  %-14s %8s %8s\n" "metric" "252" "local"
  for m in \
    "tables:SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" \
    "columns:SELECT count(*) FROM information_schema.columns WHERE table_schema='public'" \
    "indexes:SELECT count(*) FROM pg_indexes WHERE schemaname='public'" \
    "views:SELECT count(*) FROM pg_views WHERE schemaname='public'" \
    "functions:SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON p.pronamespace=n.oid WHERE n.nspname='public'" \
    "rls_policies:SELECT count(*) FROM pg_policy" \
    "triggers:SELECT count(*) FROM information_schema.triggers WHERE event_object_schema='public'"
  do
    label="${m%%:*}"; q="${m#*:}"
    c252=$(PGPASSWORD="$PG_PASS" psql -h localhost -p "$TUNNEL_PORT" -U llm_gateway -d llm_gateway -tAc "$q")
    clocal=$(local_psql "$q")
    mark=$([ "$c252" = "$clocal" ] && echo "✓" || echo "✗")
    printf "  %-14s %8s %8s  %s\n" "$label" "$c252" "$clocal" "$mark"
  done
  exit 0
fi

# ── Apply mode ─────────────────────────────────────────
WORK="$(mktemp -d -t sync252-XXXX)"; trap 'rm -rf "$WORK"' EXIT
DUMP="$WORK/252_schema.sql"

info "Step 1: dumping 252 schema → $DUMP"
remote_dump > "$DUMP" 2>/dev/null
# Strip pg_dump 17+ meta-commands (\restrict / \unrestrict) not understood by 17.10 client
sed -i.bak '/^\\(restrict|unrestrict)/d' "$DUMP" 2>/dev/null || true
ok "$(wc -l < $DUMP) lines from 252 schema dump"

# ── Step 2: ADD COLUMN IF NOT EXISTS for missing columns ──────
info "Step 2: ADD COLUMN for missing columns"
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

cat > "$WORK/step2.sql" <<'SQL'
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
  RAISE NOTICE 'Step 2 done: applied=% failed=%', applied, failed;
END $$;
SQL
docker cp "$WORK/step2.sql" "$LOCAL_CONTAINER":/tmp/step2.sql
local_psql_file "/tmp/step2.sql" 2>&1 | grep -E 'Step 2 done' || err "step2 ran with errors"
ok "ADD COLUMN phase done"

# ── Step 3: CREATE missing tables ─────────────────────────
info "Step 3: CREATE TABLE for missing tables"
missing_tables=$(comm -23 \
  <(remote_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1") \
  <(local_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1"))

for tbl in $missing_tables; do
  if [ -n "$ONLY_TABLES" ] && [[ ",$ONLY_TABLES," != *",$tbl,"* ]]; then
    info "  skip $tbl (not in --tables list)"
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
nxt  = c.find("\n--\n-- Name:", start + 100)
seg  = c[pre+1: nxt if nxt > 0 else len(c)]
with open(outpath, 'w') as g: g.write(seg)
PY
  docker cp "$WORK/${tbl}.sql" "$LOCAL_CONTAINER":/tmp/${tbl}.sql
  if local_psql_file "/tmp/${tbl}.sql" >/dev/null; then
    ok "  created $tbl"
  else
    err "  failed to create $tbl; aborting before further schema changes"
    exit 1
  fi
done

# ── Step 4: CREATE missing indexes ─────────────────────────
info "Step 4: CREATE INDEX for missing indexes"
local_indexes=$(local_psql "SELECT indexname FROM pg_indexes WHERE schemaname='public' ORDER BY 1")

python3 - "$DUMP" "$WORK/missing_indexes.sql" "$local_indexes" <<'PY' || true
import sys, re
dump, out_path, existing = sys.argv[1], sys.argv[2], sys.argv[3].split()
with open(dump) as f: c = f.read()
out = ['-- Missing indexes from 252', 'BEGIN;']
# Match only `public.<table>` targets so cross-schema indexes (e.g. maintain.*)
# in the 252 dump are NOT misclassified as "missing public index".
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
  # `local_psql_file` uses ON_ERROR_STOP=1, so a single failing CREATE INDEX
  # (e.g. columnar GIN — see skill Q13) aborts psql with exit 3 and, under
  # `set -e`, kills the subshell before we can branch. Capture+tolerate.
  out=$(local_psql_file /tmp/missing_indexes.sql 2>&1 || true)
  echo "$out" | tail -5
  # Filter expected Q13 errors: columnar access method does not support GIN
  # (e.g. quality_flags/tool_calls GIN indexes on request_logs_* partitions).
  # Such failures leave "unsupported access method for the index on columnar table"
  # inside the surrounding BEGIN/COMMIT; we report them as warnings, not failures.
  if echo "$out" | grep -q "unsupported access method for the index on columnar table"; then
    info "WARN: columnar GIN index creation(s) skipped (expected, see skill Q13)"
  fi
  unexpected=$(echo "$out" \
    | grep -vE "unsupported access method for the index on columnar table|BEGIN|COMMIT|NOTICE:|^$" || true) || true
  if [ -n "$unexpected" ]; then
    err "index creation phase failed with unexpected errors"
    echo "$unexpected" >&2
    exit 1
  fi
  ok "index creation phase done"
else
  ok "no missing CREATE INDEX"
fi

# ── Step 5: Add PK for newly created tables ────────────────
info "Step 5: ADD CONSTRAINT PK for newly created tables"
for entry in routing_health_checks:pk compression_bench_results:pk routing_health_checks:check_id_unique; do
  tbl="${entry%:*}"; ctype="${entry#*:}"
  case "$ctype" in
    pk)
      cname="${tbl}_pkey"
      if ! local_psql "SELECT 1 FROM pg_constraint WHERE conname='$cname'" | grep -q 1; then
        local_run_sql "ALTER TABLE ONLY public.$tbl ADD CONSTRAINT $cname PRIMARY KEY (id);" >/dev/null \
          && ok "  added PK $cname"
      fi
      ;;
    check_id_unique)
      idx="routing_health_checks_check_id_unique_per_entity"
      if ! local_psql "SELECT 1 FROM pg_indexes WHERE indexname='$idx'" | grep -q 1; then
        local_run_sql "CREATE UNIQUE INDEX $idx ON public.routing_health_checks (check_id, entity_type, entity_id);" >/dev/null \
          && ok "  added unique index $idx"
      fi
      ;;
  esac
done

# ── Final summary ─────────────────────────────────────
info "Final schema diff (252 vs local)"
printf "  %-14s %8s %8s\n" "metric" "252" "local"
for m in \
  "tables:SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" \
  "columns:SELECT count(*) FROM information_schema.columns WHERE table_schema='public'" \
  "indexes:SELECT count(*) FROM pg_indexes WHERE schemaname='public'" \
  "views:SELECT count(*) FROM pg_views WHERE schemaname='public'" \
  "functions:SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON p.pronamespace=n.oid WHERE n.nspname='public'" \
  "rls_policies:SELECT count(*) FROM pg_policy" \
  "triggers:SELECT count(*) FROM information_schema.triggers WHERE event_object_schema='public'"
do
  label="${m%%:*}"; q="${m#*:}"
  c252=$(PGPASSWORD="$PG_PASS" psql -h localhost -p "$TUNNEL_PORT" -U llm_gateway -d llm_gateway -tAc "$q")
  clocal=$(local_psql "$q")
  mark=$([ "$c252" = "$clocal" ] && echo "✓" || echo "✗")
  printf "  %-14s %8s %8s  %s\n" "$label" "$c252" "$clocal" "$mark"
done

ok "Sync done. Hot + partition tables data on local is preserved."
