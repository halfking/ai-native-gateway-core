#!/usr/bin/env bash
# Verify dashboard archive migrations 378 and 379.
set -euo pipefail

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  cat <<'USAGE'
Usage: DATABASE_URL=... scripts/verify-migrations.sh

Verifies the session module execution and dashboard access event migration
objects, pooled-writer RLS compatibility, partitions, and transactional writes.
USAGE
  exit 0
fi

if [[ -n "${DATABASE_URL:-}" ]]; then
  PSQL=(psql "${DATABASE_URL}")
else
  required=(DB_HOST DB_PORT DB_NAME DB_USER)
  for var in "${required[@]}"; do
    [[ -n "${!var:-}" ]] || { printf 'error: set DATABASE_URL or %s (and the other DB_* variables)\n' "$var" >&2; exit 2; }
  done
  PSQL=(psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}")
fi
PSQL+=(--no-psqlrc -X -v ON_ERROR_STOP=1 -v VERBOSITY=verbose)

assert_true() {
  local description=$1 sql=$2
  local result
  result=$("${PSQL[@]}" -Atqc "SELECT CASE WHEN (${sql}) THEN 'true' ELSE 'false' END")
  [[ "$result" == true ]] || { printf 'FAIL: %s\n' "$description" >&2; exit 1; }
  printf 'PASS: %s\n' "$description"
}

for relation in session_module_executions_hot session_module_executions dashboard_access_events_hot dashboard_access_events; do
  assert_true "relation ${relation} exists" "to_regclass('public.${relation}') IS NOT NULL"
done
for function_name in ensure_session_module_executions_partition archive_session_module_executions ensure_dashboard_events_partition archive_dashboard_events; do
  assert_true "function ${function_name} exists" "to_regprocedure('public.${function_name}(integer)') IS NOT NULL OR to_regprocedure('public.${function_name}(date)') IS NOT NULL"
done
for relation in session_module_executions_hot session_module_executions dashboard_access_events_hot dashboard_access_events; do
  assert_true "RLS disabled on pooled-writer table ${relation}" "EXISTS (SELECT 1 FROM pg_class WHERE oid='public.${relation}'::regclass AND NOT relrowsecurity)"
  assert_true "withdrawn tenant policy absent on ${relation}" "NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname='public' AND tablename='${relation}' AND policyname LIKE 'tenant_isolation_%')"
done

current_month=$("${PSQL[@]}" -Atqc "SELECT to_char(CURRENT_DATE,'YYYY_MM')")
next_month=$("${PSQL[@]}" -Atqc "SELECT to_char(CURRENT_DATE+INTERVAL '1 month','YYYY_MM')")
for prefix in session_module_executions dashboard_access_events; do
  assert_true "current partition for ${prefix}" "to_regclass('public.${prefix}_${current_month}') IS NOT NULL"
  assert_true "next partition for ${prefix}" "to_regclass('public.${prefix}_${next_month}') IS NOT NULL"
done

run_id="migration_verify_${$}_$(date +%s)_${RANDOM}"
"${PSQL[@]}" -v run_id="$run_id" <<'SQL'
BEGIN;
-- Deliberately omit app.current_tenant: moduleexec and telemetry write through
-- asynchronous pooled connections that cannot rely on a transaction-local GUC.
INSERT INTO public.session_module_executions_hot
 (gw_session_id,tenant_id,module_name,cache_key,status,expires_at)
VALUES (:'run_id','migration_verify_tenant','session_audit',:'run_id','completed',now()+INTERVAL '1 hour');
INSERT INTO public.dashboard_access_events_hot
 (event_id,event_type,tenant_id,api_path,api_method,status_code,response_time_ms)
VALUES (:'run_id','api_access','migration_verify_tenant','/migration-verify','GET',200,1);
SELECT 1 / (count(*) = 1)::int FROM public.session_module_executions_hot WHERE gw_session_id=:'run_id';
SELECT 1 / (count(*) = 1)::int FROM public.dashboard_access_events_hot WHERE event_id=:'run_id';
ROLLBACK;
SQL
printf 'PASS: transactional insert checks (rolled back)\n'
printf 'Migration verification passed.\n'
