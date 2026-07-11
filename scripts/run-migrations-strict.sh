#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mode="run"
baseline_through=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --bootstrap)
      mode="bootstrap"
      ;;
    --baseline-through)
      [[ $# -ge 2 && "$2" =~ ^[0-9]+$ ]] || {
        printf 'error: --baseline-through requires a numeric version prefix\n' >&2
        exit 2
      }
      mode="baseline"
      baseline_through="$2"
      shift
      ;;
    -h|--help)
      cat <<'USAGE'
Usage: DATABASE_URL=... scripts/run-migrations-strict.sh [--bootstrap | --baseline-through VERSION]

Runs only migrations not present in public.repository_schema_migrations. Migration files are
ordered numerically within each migration scope and stop at the first failure.

Use --bootstrap only for an empty database; it applies all repository migrations.
For an existing database with no ledger, use --baseline-through 377 to record the
known historical baseline, then apply only later migrations (currently 378/379).
Baseline mode does not execute the versions it records.
USAGE
      exit 0
      ;;
    *)
      printf 'error: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
  shift
done

: "${DATABASE_URL:?DATABASE_URL must be explicitly set}"
command -v psql >/dev/null 2>&1 || { printf 'error: psql is required\n' >&2; exit 1; }

psql_base=(psql -X -v ON_ERROR_STOP=1 -q "$DATABASE_URL")
"${psql_base[@]}" -c '
CREATE TABLE IF NOT EXISTS public.repository_schema_migrations (
    scope TEXT NOT NULL,
    version TEXT NOT NULL,
    migration_name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, migration_name)
);' >/dev/null

ledger_count=$("${psql_base[@]}" -Atqc 'SELECT count(*) FROM public.repository_schema_migrations')
existing_relations=$("${psql_base[@]}" -Atqc "SELECT count(*) FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relkind IN ('r', 'p', 'v', 'm', 'S', 'f') AND relname <> 'repository_schema_migrations'")
if [[ "$mode" == "run" && "$ledger_count" == "0" && "$existing_relations" == "0" ]]; then
  mode="bootstrap"
fi
if [[ "$mode" == "run" && "$ledger_count" == "0" ]]; then
  printf '%s\n' 'error: repository_schema_migrations is empty; refusing to replay unknown history automatically.' >&2
  printf '%s\n' 'For a new empty database use --bootstrap. For an existing database use --baseline-through <known-version>.' >&2
  exit 3
fi
if [[ "$mode" == "bootstrap" && "$existing_relations" != "0" ]]; then
  printf '%s\n' 'error: --bootstrap requires an empty public schema (except repository_schema_migrations).' >&2
  exit 3
fi
if [[ "$mode" == "baseline" && "$ledger_count" != "0" ]]; then
  printf '%s\n' 'error: --baseline-through can only initialize an empty repository_schema_migrations ledger.' >&2
  exit 3
fi

file_checksum() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    sha256sum "$1" | cut -d ' ' -f 1
  fi
}

migration_files() {
  local scope=$1 file filename version version_number
  while IFS= read -r file; do
    filename=$(basename "$file")
    version=${filename%%_*}
    version_number=${version%%[^0-9]*}
    printf '%s\t%s\t%s\n' "$version_number" "$version" "$file"
  done < <(find "$ROOT_DIR/sql/migrations/$scope" -maxdepth 1 -type f -name '[0-9]*.sql' ! -name '*.down.sql' -print) |
    LC_ALL=C sort -n -k1,1 -k2,2 |
    cut -f3-
}

record_migration() {
  local scope=$1 version=$2 name=$3 checksum=$4
  "${psql_base[@]}" -v scope="$scope" -v version="$version" -v name="$name" -v checksum="$checksum" -c '
INSERT INTO public.repository_schema_migrations (scope, version, migration_name, checksum)
VALUES (:'"'"'scope'"'"', :'"'"'version'"'"', :'"'"'name'"'"', :'"'"'checksum'"'"');' >/dev/null
}

apply_scope() {
  local scope=$1 file filename version version_number checksum stored_checksum migration_list
  migration_list=$(migration_files "$scope")

  while IFS= read -r file; do
    filename=$(basename "$file")
    version=${filename%%_*}
    version_number=${version%%[^0-9]*}
    checksum=$(file_checksum "$file")
    stored_checksum=$("${psql_base[@]}" -Atq -v scope="$scope" -v name="$filename" -c "SELECT checksum FROM public.repository_schema_migrations WHERE scope = :'scope' AND migration_name = :'name';")

    if [[ -n "$stored_checksum" ]]; then
      [[ "$stored_checksum" == "$checksum" ]] || {
        printf 'error: applied migration changed: %s/%s\n' "$scope" "$filename" >&2
        exit 4
      }
      printf 'Skipping applied %s/%s\n' "$scope" "$filename"
      continue
    fi

    if [[ "$mode" == "baseline" && "$version_number" -le "$baseline_through" ]]; then
      printf 'Baselining %s/%s\n' "$scope" "$filename"
      record_migration "$scope" "$version" "$filename" "$checksum"
      continue
    fi

    printf 'Applying %s/%s\n' "$scope" "$filename"
    "${psql_base[@]}" -f "$file"
    record_migration "$scope" "$version" "$filename" "$checksum"
  done <<<"$migration_list"
}

apply_scope startup
apply_scope domain
printf 'Repository migrations completed successfully.\n'
