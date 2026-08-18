#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin"
cat >"$TMP/bin/psql" <<'PSQL'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${PSQL_LOG:?}"
case " $* " in
  *"SELECT count(*) FROM public.repository_schema_migrations"*) printf '1\n' ;;
  *"SELECT count(*) FROM pg_class"*) printf '1\n' ;;
  *"SELECT checksum FROM public.repository_schema_migrations"*) printf '\n' ;;
esac
PSQL
chmod +x "$TMP/bin/psql"

PATH="$TMP/bin:$PATH" PSQL_LOG="$TMP/psql.log" DATABASE_URL='postgres://runner-test' \
  "$ROOT/scripts/run-migrations-strict.sh" >"$TMP/runner.out"

grep -Fx 'Applying ursm/080-ursm-key-migration-ledger.sql' "$TMP/runner.out" >/dev/null
grep -Fx 'Applying ursm/081-ursm-key-migration-ledger-add-rollback-deadline.sql' "$TMP/runner.out" >/dev/null
grep -F -- '-v scope=ursm -v version=080 -v name=080-ursm-key-migration-ledger.sql' "$TMP/psql.log" >/dev/null
grep -F -- '-v scope=ursm -v version=081 -v name=081-ursm-key-migration-ledger-add-rollback-deadline.sql' "$TMP/psql.log" >/dev/null

OUT="$TMP/bundle"
DRY_RUN=1 "$ROOT/scripts/build-db-release-bundle.sh" runner-test --out "$OUT" >/dev/null
grep -F -- '-- Source: sql/migrations/080-ursm-key-migration-ledger.sql' "$OUT/db/03-current-upgrade.sql" >/dev/null
grep -F -- '-- Source: sql/migrations/081-ursm-key-migration-ledger-add-rollback-deadline.sql' "$OUT/db/03-current-upgrade.sql" >/dev/null

printf 'ursm root migration runner regression tests: passed\n'
