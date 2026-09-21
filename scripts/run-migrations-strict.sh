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
known historical baseline, then apply only later migrations (currently 378+).
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

# Bulk-load the entire ledger into an in-memory associative array keyed by
# "scope|migration_name" → checksum. Previously each migration file issued its
# own SELECT against repository_schema_migrations, which costs ~25ms of psql
# startup per file on top of the SQL roundtrip. With ~430 files that adds up
# to ~10s of pure overhead before any work happens. Reading the whole ledger in
# one query (a few ms even at 10k rows) keeps strict idempotency semantics
# intact while removing the per-file roundtrip.
declare -A LEDGER_CHECKSUMS
while IFS=$'\t' read -r scope checksum migration_name; do
  [[ -n "$scope" && -n "$migration_name" ]] || continue
  LEDGER_CHECKSUMS["$scope|$migration_name"]="$checksum"
done < <("${psql_base[@]}" -Atq -F $'\t' -c \
  'SELECT scope, checksum, migration_name FROM public.repository_schema_migrations')

file_checksum() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    sha256sum "$1" | cut -d ' ' -f 1
  fi
}

# Compute checksums for a list of files in parallel. The strict runner hashes
# ~430 files on every invocation; doing it serially costs ~4.5s of CPU on
# developer laptops. Parallelizing across $(nproc) jobs brings it under 1s
# while remaining deterministic (each output line corresponds to one input).
parallel_checksums() {
  local files=("$@")
  if command -v shasum >/dev/null 2>&1; then
    printf '%s\n' "${files[@]}" | xargs -n1 -P"$(parallel_jobs)" shasum -a 256
  else
    printf '%s\n' "${files[@]}" | xargs -n1 -P"$(parallel_jobs)" sha256sum
  fi
}

# Cap worker count so a beefy box doesn't saturate the disk with concurrent
# reads. 8 is enough to saturate SHA-NI on modern x86_64; on smaller machines
# this falls back to nproc.
parallel_jobs() {
  local n
  if command -v nproc >/dev/null 2>&1; then
    n=$(nproc)
  else
    n=4
  fi
  if (( n > 8 )); then n=8; fi
  printf '%d\n' "$n"
}

migration_files() {
  local scope=$1 file filename version version_number migration_root
  migration_root="$ROOT_DIR/sql/migrations/$scope"
  if [[ "$scope" == "ursm" ]]; then
    migration_root="$ROOT_DIR/sql/migrations"

    while IFS= read -r file; do
      filename=$(basename "$file")
      case "$filename" in
        080-ursm-key-migration-ledger.sql|081-ursm-key-migration-ledger-add-rollback-deadline.sql|082-ursm-key-migration-state-machine.sql|083-ursm-key-migration-add-dual-checkpoint.sql) ;;
        *) continue ;;
      esac
      version=${filename%%-*}
      version_number=${version%%[^0-9]*}
      printf '%s\t%s\t%s\n' "$version_number" "$version" "$file"
    done < <(find "$migration_root" -maxdepth 1 -type f -name '[0-9]*-*.sql' ! -name '*.down.sql' -print)
  else
    while IFS= read -r file; do
      filename=$(basename "$file")
      version=${filename%%_*}
      version_number=${version%%[^0-9]*}
      printf '%s\t%s\t%s\n' "$version_number" "$version" "$file"
    done < <(find "$migration_root" -type f -name '[0-9]*.sql' ! -name '*.down.sql' -print)
  fi |
    LC_ALL=C sort -n -k1,1 -k2,2 |
    cut -f3-
}

record_migration() {
  local scope=$1 version=$2 name=$3 checksum=$4
  "${psql_base[@]}" -c "
INSERT INTO public.repository_schema_migrations (scope, version, migration_name, checksum)
VALUES ('$scope', '$version', '$name', '$checksum');" >/dev/null
}

apply_scope() {
  local scope=$1 file filename version version_number checksum stored_checksum migration_list
  migration_list=$(migration_files "$scope")

  # Slurp the sorted file list into an array so we can compute checksums in
  # parallel across all files in the scope at once. The previous per-file
  # `shasum -a 256` ran serially inside the apply loop, costing ~4.5s for
  # the 430-file startup+domain+ursm corpus. parallel_checksums() (defined
  # above) runs them on $(parallel_jobs) workers, dropping the wall time
  # to ~0.7s while keeping the same SHA-256 output.
  local -a files=()
  while IFS= read -r file; do
    files+=("$file")
  done <<<"$migration_list"
  if (( ${#files[@]} == 0 )); then return; fi

  # checksum_map keys files by basename → "checksum  path" line so we can
  # join against the sorted iteration below.
  local -A checksum_map=()
  local line path sha
  while IFS= read -r line; do
    sha="${line%% *}"
    path="${line#* }"
    checksum_map["$(basename "$path")"]="$sha"
  done < <(parallel_checksums "${files[@]}")

  for file in "${files[@]}"; do
    filename=$(basename "$file")
    if [[ "$scope" == "ursm" ]]; then
      version=${filename%%-*}
    else
      version=${filename%%_*}
    fi
    version_number=${version%%[^0-9]*}
    checksum="${checksum_map[$filename]}"
    stored_checksum="${LEDGER_CHECKSUMS[$scope|$filename]:-}"

    if [[ -n "$stored_checksum" ]]; then
      [[ "$stored_checksum" == "$checksum" ]] || {
        printf 'error: applied migration changed: %s/%s\n' "$scope" "$filename" >&2
        exit 4
      }
      printf 'Skipping applied %s/%s\n' "$scope" "$filename"
      continue
    fi

    # 10# forces base-10: bash otherwise parses leading-zero versions (008,
    # 009, 018, ...) as invalid octal, silently failing the comparison and
    # executing migrations that baseline mode must only record.
    if [[ "$mode" == "baseline" && $((10#$version_number)) -le $((10#$baseline_through)) ]]; then
      printf 'Baselining %s/%s\n' "$scope" "$filename"
      record_migration "$scope" "$version" "$filename" "$checksum"
      continue
    fi

    printf 'Applying %s/%s\n' "$scope" "$filename"
    "${psql_base[@]}" -f "$file"
    record_migration "$scope" "$version" "$filename" "$checksum"
  done
}

apply_scope startup
apply_scope domain
apply_scope ursm
printf 'Repository migrations completed successfully.\n'
