#!/usr/bin/env bash
# repair-252-migration-ledger.sh — audit/repair 252 migration history
#
# Default mode is read-only. --apply requires explicit approval and performs:
#   1. a pg_dump backup of schema_migrations and its checksum ledger;
#   2. transactionally deduplicating byte-for-byte identical history rows;
#   3. restoring a unique version constraint;
#   4. initializing the checksum ledger for applied migrations at/after the
#      reconciliation boundary, using the remote description to resolve any
#      same-number historical filenames.
#
# Usage:
#   bash scripts/repair-252-migration-ledger.sh
#   bash scripts/repair-252-migration-ledger.sh --apply
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

SSH_HOST="${LLM_GATEWAY_252_SSH:-root@115.29.212.252}"
SSH_PORT="${LLM_GATEWAY_252_SSH_PORT:-25022}"
SSH_KEY="${SSH_KEY_252:-${SSH_KEY_FILE:-$HOME/.ssh/id_ed25519}}"
DB_CONTAINER="${DB_CONTAINER_252:-pg-252-pg17}"
DB_USER="${DB_USER_252:-llm_gateway}"
DB_NAME="${DB_NAME_252:-llm_gateway}"
LEDGER_FROM="${DB_LEDGER_RECONCILE_FROM:-412}"
LEDGER_TO="${DB_LEDGER_RECONCILE_TO:-}"
BACKUP_DIR="${MIGRATION_REPAIR_BACKUP_DIR:-$REPO_ROOT/build/252-migration-backups}"
APPLY=0

usage() {
  printf '%s\n' \
    "Usage: $0 [--apply] [--ledger-from N] [--ledger-to N] [--backup-dir DIR]" \
    "  default       read-only preflight" \
    "  --apply       backup first, then repair in one database transaction" \
    "  --ledger-from only reconcile applied numeric versions >= N (default: $LEDGER_FROM)" \
    "  --ledger-to   only reconcile applied numeric versions <= N (default: no upper bound)"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --ledger-from)
      [[ $# -ge 2 && "$2" =~ ^[0-9]+$ ]] || { echo "--ledger-from requires an integer" >&2; exit 64; }
      LEDGER_FROM="$2"; shift 2 ;;
    --ledger-to)
      [[ $# -ge 2 && "$2" =~ ^[0-9]+$ ]] || { echo "--ledger-to requires an integer" >&2; exit 64; }
      LEDGER_TO="$2"; shift 2 ;;
    --backup-dir)
      [[ $# -ge 2 && -n "$2" ]] || { echo "--backup-dir requires a path" >&2; exit 64; }
      BACKUP_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 64 ;;
  esac
done

if [[ -n "$LEDGER_TO" ]] && (( LEDGER_TO < LEDGER_FROM )); then
  echo "--ledger-to must be greater than or equal to --ledger-from" >&2
  exit 64
fi

SSH_OPTS=(-i "$SSH_KEY" -p "$SSH_PORT" -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)
SSH=(ssh "${SSH_OPTS[@]}" "$SSH_HOST")

log() { printf '[252-ledger] %s\n' "$*" >&2; }
fail() { printf '[252-ledger] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -f "$SSH_KEY" ]] || fail "SSH key not found: $SSH_KEY"
command -v ssh >/dev/null 2>&1 || fail "ssh is required"

remote_psql() {
  local sql=$1 format=${2:-unaligned}
  local flags=(-X -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME")
  [[ "$format" == "unaligned" ]] && flags+=(-A -t)
  "${SSH[@]}" "docker exec -i '$DB_CONTAINER' psql ${flags[*]}" <<<"$sql"
}

remote_exec() {
  "${SSH[@]}" "$@"
}

# Build version -> candidate filenames. A candidate is active unless it is a
# down/skip file or its header explicitly says it supersedes/deprecates it.
declare -A CANDIDATES=()
load_candidates() {
  local f base ver
  shopt -s nullglob
  for f in "$REPO_ROOT"/sql/migrations/startup/[0-9]*.sql; do
    [[ -f "$f" ]] || continue
    base=${f##*/}
    [[ "$base" =~ ^[0-9]{3}_ ]] || continue
    [[ "$base" != *.down.sql && "$base" != *.skip && "$base" != *.bak.skip ]] || continue
    head -15 "$f" | grep -qiE 'SUPERSEDED|superceded|DEPRECATED|deprecated' && continue
    ver=${base%%_*}
    if [[ -n "${CANDIDATES[$ver]+x}" ]]; then
      CANDIDATES[$ver]+=$'\n'"$base"
    else
      CANDIDATES[$ver]="$base"
    fi
  done
  shopt -u nullglob
}

canonical_for() {
  local ver=$1
  local description=$2
  local candidates=${CANDIDATES[$ver]-}
  local candidate suffix match="" count=0
  case "$ver:$description" in
    461:request_wal_hot_request_id_unique) description="request_wal_hot_unique_request_id" ;;
  esac
  [[ -n "$candidates" ]] || return 1
  while IFS= read -r candidate; do
    [[ -n "$candidate" ]] || continue
    count=$((count + 1))
    suffix=${candidate#"${ver}"_}
    suffix=${suffix%.sql}
    if [[ "$suffix" == "$description" || "${candidate%.sql}" == "$description" ]]; then
      match=$candidate
    fi
  done <<<"$candidates"
  if (( count == 1 )); then
    [[ -n "$match" ]] || return 2
    printf '%s\n' "$match"
    return 0
  fi
  [[ -n "$match" ]] || return 2
  printf '%s\n' "$match"
}

migration_checksum() {
  local file="$REPO_ROOT/sql/migrations/startup/$1"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    sha256sum "$file" | awk '{print $1}'
  fi
}

sql_quote() {
  local value=$1
  value=${value//\'/\'\'}
  printf "'%s'" "$value"
}

load_candidates

log "target=$SSH_HOST:$SSH_PORT container=$DB_CONTAINER database=$DB_NAME ledger_from=$LEDGER_FROM ledger_to=${LEDGER_TO:-unbounded}"
log "checking SSH and PostgreSQL identity"
remote_exec "docker exec '$DB_CONTAINER' psql -X -v ON_ERROR_STOP=1 -U '$DB_USER' -d '$DB_NAME' -Atc \"SELECT current_database() || '|' || current_user\"" >/dev/null \
  || fail "remote PostgreSQL identity check failed"

schema_status=$(remote_psql "SELECT CASE WHEN to_regclass('public.schema_migrations') IS NULL THEN 'missing' ELSE 'present' END")
[[ "$schema_status" == "present" ]] || fail "public.schema_migrations is missing"

column_status=$(remote_psql "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='schema_migrations' AND column_name IN ('version','description','applied_at')")
[[ "$column_status" == "3" ]] || fail "schema_migrations does not have the expected columns"

duplicate_rows=$(remote_psql "SELECT COALESCE(sum(n-1),0) FROM (SELECT version,count(*) n FROM public.schema_migrations GROUP BY version HAVING count(*) > 1) s")
inconsistent_versions=$(remote_psql "SELECT count(*) FROM public.schema_migrations a JOIN public.schema_migrations b ON a.version=b.version AND a.ctid < b.ctid WHERE ROW(a.description,a.applied_at) IS DISTINCT FROM ROW(b.description,b.applied_at)")
constraint_status=$(remote_psql "SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey) WHERE c.conrelid='public.schema_migrations'::regclass AND c.contype IN ('p','u') AND a.attname='version') THEN 'present' ELSE 'missing' END")
ledger_status=$(remote_psql "SELECT CASE WHEN to_regclass('public.llm_gateway_migration_checksums') IS NULL THEN 'missing' ELSE 'present' END")

log "duplicate_rows=$duplicate_rows inconsistent_duplicate_pairs=$inconsistent_versions version_unique_constraint=$constraint_status checksum_ledger=$ledger_status"
(( inconsistent_versions == 0 )) || fail "duplicate versions have different description/applied_at; refusing to guess"

applied_filter="version::int >= $LEDGER_FROM"
if [[ -n "$LEDGER_TO" ]]; then
  applied_filter+=" AND version::int <= $LEDGER_TO"
fi
applied_rows=$(remote_psql "SELECT version || '|' || COALESCE(description,'') FROM public.schema_migrations WHERE version ~ '^[0-9]+$' AND $applied_filter ORDER BY version::int")

# Resolve all applied versions at/after the boundary before any write. This is
# also the dry-run proof that same-number files (431/432, etc.) are unambiguous.
declare -a EXPECTED_SQL=()
resolved_count=0
repair_tag="\$repair\$"
while IFS='|' read -r ver description; do
  [[ -n "$ver" ]] || continue
  if ! file=$(canonical_for "$ver" "$description"); then
    candidates=${CANDIDATES[$ver]-<none>}
    fail "cannot resolve applied version $ver description='$description' to one canonical local file (candidates: ${candidates//$'\n'/, })"
  fi
  checksum=$(migration_checksum "$file")
  EXPECTED_SQL+=("INSERT INTO public.llm_gateway_migration_checksums(version,migration_name,checksum) VALUES ($(sql_quote "$ver"),$(sql_quote "$file"),$(sql_quote "$checksum")) ON CONFLICT (version) DO NOTHING;")
  EXPECTED_SQL+=("DO ${repair_tag} BEGIN IF EXISTS (SELECT 1 FROM public.llm_gateway_migration_checksums WHERE version=$(sql_quote "$ver") AND (migration_name <> $(sql_quote "$file") OR checksum <> $(sql_quote "$checksum"))) THEN RAISE EXCEPTION 'checksum ledger mismatch for version ${ver}'; END IF; END ${repair_tag};")
  resolved_count=$((resolved_count + 1))
  log "resolved $ver -> $file sha256=${checksum:0:12}..."
done <<<"$applied_rows"

if (( duplicate_rows > 0 )) || [[ "$constraint_status" != "present" || "$ledger_status" != "present" ]]; then
  if (( APPLY == 0 )); then
    log "read-only preflight complete: repair is required before normal deployment"
    exit 2
  fi
fi

if (( APPLY == 0 )); then
  log "read-only preflight passed; no database changes made"
  exit 0
fi

mkdir -p "$BACKUP_DIR"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_file="$BACKUP_DIR/schema-migrations-252-$timestamp.sql"
log "creating backup before write: $backup_file"
remote_exec "docker exec '$DB_CONTAINER' pg_dump -U '$DB_USER' -d '$DB_NAME' --no-owner --no-privileges --table=public.schema_migrations --table=public.llm_gateway_migration_checksums 2>/dev/null || docker exec '$DB_CONTAINER' pg_dump -U '$DB_USER' -d '$DB_NAME' --no-owner --no-privileges --table=public.schema_migrations" >"$backup_file"
[[ -s "$backup_file" ]] || fail "backup is empty"
backup_sha=$(shasum -a 256 "$backup_file" | awk '{print $1}')
printf '%s  %s\n' "$backup_sha" "$(basename "$backup_file")" >"$backup_file.sha256"
log "backup sha256=$backup_sha"

repair_sql=$(mktemp)
trap 'rm -f "$repair_sql"' EXIT
{
  cat <<'SQL'
BEGIN;
SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:schema_migrations:repair', 0));
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM public.schema_migrations a
    JOIN public.schema_migrations b
      ON a.version=b.version AND a.ctid < b.ctid
    WHERE ROW(a.description,a.applied_at) IS DISTINCT FROM ROW(b.description,b.applied_at)
  ) THEN
    RAISE EXCEPTION 'schema_migrations contains inconsistent duplicate rows';
  END IF;
END $$;
DELETE FROM public.schema_migrations a
USING public.schema_migrations b
WHERE a.version=b.version AND a.ctid > b.ctid;
DO $$
DECLARE
  has_version_key boolean;
  has_other_primary boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)
    WHERE c.conrelid='public.schema_migrations'::regclass
      AND c.contype IN ('p','u') AND a.attname='version'
  ) INTO has_version_key;
  SELECT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='public.schema_migrations'::regclass AND contype='p'
      AND conkey <> ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid='public.schema_migrations'::regclass AND attname='version')::smallint]
  ) INTO has_other_primary;
  IF has_other_primary THEN
    RAISE EXCEPTION 'schema_migrations has an unexpected primary key';
  END IF;
  IF NOT has_version_key THEN
    ALTER TABLE public.schema_migrations ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);
  END IF;
END $$;
CREATE TABLE IF NOT EXISTS public.llm_gateway_migration_checksums (
  version TEXT PRIMARY KEY,
  migration_name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
SQL
  printf '%s\n' "${EXPECTED_SQL[@]}"
  cat <<'SQL'
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.schema_migrations GROUP BY version HAVING count(*) > 1) THEN
    RAISE EXCEPTION 'schema_migrations still contains duplicate versions';
  END IF;
END $$;
COMMIT;
SQL
} >"$repair_sql"

log "applying transactional repair"
"${SSH[@]}" "docker exec -i '$DB_CONTAINER' psql -X -v ON_ERROR_STOP=1 -U '$DB_USER' -d '$DB_NAME'" <"$repair_sql"
log "post-repair verification"
post_duplicates=$(remote_psql "SELECT count(*) FROM (SELECT version FROM public.schema_migrations GROUP BY version HAVING count(*) > 1) d")
post_key=$(remote_psql "SELECT count(*) FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey) WHERE c.conrelid='public.schema_migrations'::regclass AND c.contype IN ('p','u') AND a.attname='version'")
post_legacy=$(remote_psql "SELECT count(*) FROM public.schema_migrations WHERE version !~ '^[0-9]+$'")
post_ledger=$(remote_psql "SELECT count(*) FROM public.llm_gateway_migration_checksums")
[[ "$post_duplicates" == "0" && "$post_key" == "1" ]] || fail "post-repair invariant failed: duplicates=$post_duplicates key=$post_key"
log "verified duplicates=$post_duplicates version_key=$post_key legacy_rows=$post_legacy checksum_rows=$post_ledger resolved_numeric_rows=$resolved_count"
log "repair complete; backup=$backup_file"
