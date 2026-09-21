#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/verify-migrations-627-631.sh
# Purpose:       Audit-time end-to-end verification of migrations 627-631
#                against an isolated PostgreSQL with FORCE RLS enabled.
#
# Scope:
#   1. Reset isolated PG schema and apply scripts/audit/sql/min-prereqs.sql
#   2. Apply 627, 628, 629, 630, 631 in order
#   3. Verify DDL objects: tables, indexes, policies, views, functions
#   4. Verify 630 FORCE RLS by setting a tenant GUC and confirming visibility
#   5. Down 631..627, verify objects disappear
#   6. Re-apply 627..631, verify all objects re-appear
#   7. Run scripts/verify-migration-checksums.sh and confirm OK
#
# Out of scope:
#   - 01-schema.sql dump (not self-consistent for fresh DB; not needed for
#     627-631 verification because the migrations either CREATE TABLE
#     IF NOT EXISTS or reference objects in min-prereqs).
#
# Usage:
#   bash scripts/audit/verify-migrations-627-631.sh
# -----------------------------------------------------------------------------
set -euo pipefail
PSQL() { bash scripts/audit/psql-isolated.sh "$@"; }

if [[ ! -f /tmp/audit-pg.env ]]; then
    echo "ERROR: /tmp/audit-pg.env missing; run start-isolated-pg.sh first" >&2
    exit 1
fi
set -a
# shellcheck disable=SC1091
source /tmp/audit-pg.env
set +a

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

echo "═══ STEP 0: ensure container ready ═══"
if ! docker exec -e PGPASSWORD="$AUDIT_PG_PASS" kx-citus pg_isready -U "$AUDIT_PG_USER" -d "$AUDIT_PG_DB" >/dev/null 2>&1; then
    echo "ERROR: isolated PG not ready. Run start-isolated-pg.sh first." >&2
    exit 1
fi

echo "═══ STEP 1: reset schema, apply min prereqs ═══"
PSQL -c "DROP TABLE IF EXISTS public.candidate_failure_logs CASCADE;
         DROP TABLE IF EXISTS public.candidate_failure_logs_hot CASCADE;
         DROP TABLE IF EXISTS public.provider_error_aggregator_state CASCADE;
         DROP TABLE IF EXISTS public.credentials CASCADE;
         DROP TABLE IF EXISTS public.providers CASCADE;
         DROP FUNCTION IF EXISTS public.get_current_tenant() CASCADE;
         DROP FUNCTION IF EXISTS public.promote_candidate_failure_logs_hot_to_partition(integer, interval) CASCADE;
         DROP TABLE IF EXISTS public.session_aggregate_outbox CASCADE;
         DROP TABLE IF EXISTS public.audit_attachments_cleanup CASCADE;
         DROP TABLE IF EXISTS public.audit_attachments_filesystem_cleanup CASCADE;" >/dev/null
PSQL -f scripts/audit/sql/min-prereqs.sql >/dev/null
PSQL -c "SELECT 'min-prereqs ok' AS status;"

echo "═══ STEP 2: apply 627-631 in order ═══"
for v in 627 628 629 630 631; do
    fsql=$(ls "sql/migrations/startup/${v}_"*.sql | grep -v '\.down\.sql$' | head -1)
    if PSQL -f "$fsql" >/dev/null 2>&1; then
        echo "  ✓ $(basename "$fsql")"
    else
        echo "  ✗ $(basename "$fsql") FAILED"
        PSQL -f "$fsql" 2>&1 | tail -10 | sed 's/^/    /'
        exit 1
    fi
done

echo "═══ STEP 3: verify DDL objects ═══"
PSQL -c "
SELECT
    'aggregate_id_col' AS obj,
    EXISTS(SELECT 1 FROM information_schema.columns
           WHERE table_schema='public' AND table_name='candidate_failure_logs_hot'
             AND column_name='aggregation_id')::text AS present
UNION ALL SELECT 'unified_view',
    EXISTS(SELECT 1 FROM pg_views WHERE schemaname='public' AND viewname='candidate_failure_logs_unified')::text
UNION ALL SELECT 'audit_attachments_cleanup',
    EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public' AND tablename='audit_attachments_cleanup')::text
UNION ALL SELECT 'session_aggregate_outbox',
    EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public' AND tablename='session_aggregate_outbox')::text
UNION ALL SELECT 'providers_deleted_at',
    EXISTS(SELECT 1 FROM information_schema.columns
           WHERE table_schema='public' AND table_name='providers' AND column_name='deleted_at')::text
UNION ALL SELECT 'credentials_status_check',
    EXISTS(SELECT 1 FROM pg_constraint
           WHERE conname='credentials_status_check' AND conrelid='public.credentials'::regclass)::text
UNION ALL SELECT 'providers_deleted_at_idx',
    EXISTS(SELECT 1 FROM pg_indexes
           WHERE schemaname='public' AND tablename='providers' AND indexname='idx_providers_live')::text
UNION ALL SELECT 'promote_fn_v3',
    EXISTS(SELECT 1 FROM pg_proc WHERE proname='promote_candidate_failure_logs_hot_to_partition')::text
ORDER BY 1;"

echo "═══ STEP 4: verify 630 FORCE RLS by GUC ═══"
PSQL -c "SELECT relname, relrowsecurity, relforcerowsecurity
         FROM pg_class WHERE relname IN ('session_aggregate_outbox','audit_attachments_cleanup')
         ORDER BY relname;"

# Create a non-super-admin role to exercise FORCE RLS. kxuser is a superuser
# (created by kx-citus as the install role) so direct connection from kxuser
# bypasses RLS regardless of the FORCE flag. We must SET ROLE to a
# non-superuser account to see the policy take effect.
PSQL -c "DO \$\$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='audit_tenant') THEN
        CREATE ROLE audit_tenant NOLOGIN;
    END IF;
END \$\$;" >/dev/null
PSQL -c "GRANT USAGE ON SCHEMA public TO audit_tenant;
         GRANT SELECT, INSERT, UPDATE, DELETE ON session_aggregate_outbox TO audit_tenant;
         GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO audit_tenant;" >/dev/null

# Insert as superuser (kxuser). FORCE RLS does not block writes from the
# table owner or a BYPASSRLS role, so this is a normal insert.
PSQL -c "DELETE FROM session_aggregate_outbox WHERE tenant_id IN ('tenant_a','tenant_b');
         INSERT INTO session_aggregate_outbox
         (tenant_id, session_id, partition_date, request_id, update_payload)
         VALUES ('tenant_a', 'sess_1', CURRENT_DATE, 'req_1', '{\"session_id\":\"sess_1\",\"tenant_id\":\"tenant_a\",\"request_id\":\"req_1\"}'::jsonb);"

# Single psql call so SET ROLE and the GUC persist across the three SELECTs.
TMP_RLS_SQL="$(mktemp -t rls-test-XXXX.sql)"
cat > "$TMP_RLS_SQL" <<'EOF'
SET ROLE audit_tenant;
-- 1. tenant_a context: must see 1 row
SELECT set_config('app.current_tenant', 'tenant_a', false) AS set_a;
SELECT count(*) AS tenant_a_visible FROM session_aggregate_outbox;
-- 2. tenant_b context: must see 0 rows under FORCE RLS
SELECT set_config('app.current_tenant', 'tenant_b', false) AS set_b;
SELECT count(*) AS tenant_b_visible FROM session_aggregate_outbox;
-- 3. super_admin role: must see 1 row again (policy bypass)
SELECT set_config('app.current_tenant', '', false), set_config('app.current_role', 'super_admin', false);
SELECT count(*) AS super_admin_visible FROM session_aggregate_outbox;
EOF
PSQL -f "$TMP_RLS_SQL"
rm -f "$TMP_RLS_SQL"

# Cleanup test row + role (back to superuser).
PSQL -c "DELETE FROM session_aggregate_outbox WHERE tenant_id='tenant_a';" >/dev/null
PSQL -c "REVOKE ALL ON session_aggregate_outbox FROM audit_tenant;
         REVOKE USAGE ON SCHEMA public FROM audit_tenant;
         DROP ROLE IF EXISTS audit_tenant;
         DROP ROLE IF EXISTS audit_tenant_role;" >/dev/null 2>&1 || true

echo "═══ STEP 5: down 631..627 ═══"
for v in 631 630 629 628 627; do
    fsql=$(ls "sql/migrations/startup/${v}_"*.down.sql | head -1)
    if [[ -f "$fsql" ]]; then
        if PSQL -f "$fsql" >/dev/null 2>&1; then
            echo "  ✓ down $(basename "$fsql")"
        else
            echo "  ✗ down $(basename "$fsql") FAILED"
            PSQL -f "$fsql" 2>&1 | tail -5 | sed 's/^/    /'
        fi
    else
        echo "  ! $(basename "${fsql%.down.sql}") has no .down.sql — skipping"
    fi
done

PSQL -c "SELECT
    'aggregate_id_col' AS obj,
    EXISTS(SELECT 1 FROM information_schema.columns
           WHERE table_schema='public' AND table_name='candidate_failure_logs_hot'
             AND column_name='aggregation_id')::text AS present
UNION ALL SELECT 'unified_view',
    EXISTS(SELECT 1 FROM pg_views WHERE schemaname='public' AND viewname='candidate_failure_logs_unified')::text
UNION ALL SELECT 'session_aggregate_outbox',
    EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public' AND tablename='session_aggregate_outbox')::text;"

echo "═══ STEP 6: re-apply 627..631 ═══"
for v in 627 628 629 630 631; do
    fsql=$(ls "sql/migrations/startup/${v}_"*.sql | grep -v '\.down\.sql$' | head -1)
    if PSQL -f "$fsql" >/dev/null 2>&1; then
        echo "  ✓ $(basename "$fsql")"
    else
        echo "  ✗ $(basename "$fsql") FAILED"
    fi
done

PSQL -c "SELECT
    'session_aggregate_outbox' AS obj,
    EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public' AND tablename='session_aggregate_outbox')::text AS present
UNION ALL SELECT 'audit_attachments_cleanup',
    EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public' AND tablename='audit_attachments_cleanup')::text
UNION ALL SELECT 'aggregate_id_col',
    EXISTS(SELECT 1 FROM information_schema.columns
           WHERE table_schema='public' AND table_name='candidate_failure_logs_hot'
             AND column_name='aggregation_id')::text
ORDER BY 1;"

echo "═══ STEP 7: verify-migration-checksums.sh ═══"
bash scripts/verify-migration-checksums.sh 2>&1 | tail -10

echo "═══ verify-migrations-627-631.sh complete ═══"
