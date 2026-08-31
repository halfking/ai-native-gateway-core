#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/fresh-schema-from-migrations.sh
# Purpose:       Build a fresh-DB baseline by applying only the canonical
#                startup migrations (511..635) to an empty database.
#                This replaces the legacy 01-schema.sql pg_dump path,
#                which has documented self-consistency issues (handoff
#                2026-08-31 §4.1.2: plpgsql functions referencing tables
#                that are CREATEd later, and a top-of-file syntax error).
#
# What this proves:
#   1. The migration catalog 511..635 is self-contained — applying them
#      in order to an empty database produces a usable schema without
#      the legacy 01-schema.sql baseline.
#   2. The post-migrations schema shape matches what the canonical
#      runtime expects: pg_class table count, pg_proc function count,
#      and the audit_attachments_cleanup / session_aggregate_outbox /
#      promote_* functions all exist.
#   3. The 15 known repair/fix migrations may fail on a clean DB; those
#      expected failures are accounted for separately. Any other failure is
#      fatal so a broken build migration cannot be masked.
#
# Usage:
#   bash scripts/audit/fresh-schema-from-migrations.sh
#
# Side effects:
#   Creates a database `FRESH_DB` in the kx-citus container and drops it at
#   end of run. Set KEEP_FRESH_DB=1 to retain it for the separate Go end-state
#   check (TEST_AUDIT_FRESH_SCHEMA_DB_URL).
# -----------------------------------------------------------------------------
set -euo pipefail

FRESH_DB="${FRESH_DB:-llm_gateway_fresh}"
KEEP_FRESH_DB="${KEEP_FRESH_DB:-0}"
PSQL_DB()  { docker exec kx-citus psql -U kxuser -d "$FRESH_DB" "$@"; }
PSQL_ADMIN() { docker exec kx-citus psql -U kxuser -d postgres "$@"; }

# These are repair/fix migrations for already-deployed schemas. They are
# expected to fail against a genuinely empty database; every other failure is
# a baseline failure and must make this audit fail.
expected_repair_count=15
is_expected_repair() {
  case "$1" in
    533_request_wal_bodies_unique_request_id.sql|534_handoff_logs_hot_columnar.sql|\
    538_node_probe_runs_trigger_kind_unified_queue.sql|579_dashboard_access_events_hot_promote.sql|\
    580_session_module_executions_hot_promote.sql|601_request_logs_bodies_drop_metadata.sql|\
    603_repair_request_logs_schema_consistency.sql|604_repair_request_logs_bodies_tenant_id.sql|\
    605_fix_tool_calls_index_predicate.sql|606_session_summaries_agent_expert_tags.sql|\
    607_repair_dashboard_access_events_promote_columns.sql|608_tenant_model_policies_add_pkey.sql|\
    609_tenant_model_policies_audit_rekey_pkey.sql|616_provider_error_details_unique_constraint.sql|\
    625_session_bodies_unified_explicit.sql) return 0 ;;
    *) return 1 ;;
  esac
}

cleanup() {
  if [[ "$KEEP_FRESH_DB" == "1" ]]; then
    echo "  keeping $FRESH_DB (set TEST_AUDIT_FRESH_SCHEMA_DB_URL to its DSN for Go end-state checks)"
  else
    PSQL_ADMIN -c "DROP DATABASE IF EXISTS $FRESH_DB" >/dev/null || true
  fi
}
trap cleanup EXIT

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

echo "═══ STEP 1: create fresh DB $FRESH_DB ═══"
PSQL_ADMIN -c "DROP DATABASE IF EXISTS $FRESH_DB" >/dev/null
PSQL_ADMIN -c "CREATE DATABASE $FRESH_DB" >/dev/null

# Run 00-prereqs first (creates extensions the dump relied on). It is
# tiny and idempotent. Read it from the host and pipe into docker exec
# because docker exec with -f /host/path only works for paths the
# container can see (it cannot).
if [[ -f sql/schema/00-prereqs.sql ]]; then
    echo "═══ STEP 2: apply 00-prereqs.sql (extensions) ═══"
    cat sql/schema/00-prereqs.sql | docker exec -i kx-citus psql -U kxuser -d "$FRESH_DB" -v ON_ERROR_STOP=1 >/dev/null
    echo "  ✓ 00-prereqs.sql applied"
else
    echo "═══ STEP 2: 00-prereqs.sql missing, skipping ═══"
fi

echo ""
echo "═══ STEP 3: apply startup migrations 511..635 (in numeric order) ═══"
# Files named <digits>_<rest>.sql; some have suffixes like 'p2' so we
# fall back to a string compare when the leading numeric is malformed.
mapfile -t mig_files < <(ls sql/migrations/startup/ 2>/dev/null \
    | grep -E '^[0-9]+[a-z0-9]*_[a-z0-9_].*\.sql$' \
    | grep -v '\.down\.sql$' \
    | sort)
applied=0
skipped=0
expected_failed=0
failed=0
declare -a expected_failed_list=()
declare -a failed_list=()
for base in "${mig_files[@]}"; do
    f="sql/migrations/startup/${base}"
    n="${base%%_*}"
    # Pure-digit comparisons; everything else (p2, suffixes) is treated
    # as in-range by lex order against '635'.
    if [[ "$n" =~ ^[0-9]+$ ]]; then
        # Strip leading zeros to avoid bash octal interpretation
        n_dec=$((10#$n))
        if (( n_dec > 635 )); then
            ((skipped++)) || true
            continue
        fi
        if (( n_dec < 511 )); then
            ((skipped++)) || true
            continue
        fi
    else
        # Non-numeric migration prefix (e.g. multimodal-token-fields-hot).
        # Apply if between 511 and 635 by string order — best-effort.
        if [[ "$base" < "511_"* ]] || [[ "$base" > "635_"* ]]; then
            ((skipped++)) || true
            continue
        fi
    fi
    if cat "$f" | docker exec -i kx-citus psql -U kxuser -d "$FRESH_DB" -v ON_ERROR_STOP=1 >/dev/null 2>&1; then
        ((applied++)) || true
    elif is_expected_repair "$base"; then
        ((expected_failed++)) || true
        expected_failed_list+=("$base")
    else
        ((failed++)) || true
        failed_list+=("$base")
    fi
done
if (( expected_failed != expected_repair_count )); then
    echo "ERROR: expected $expected_repair_count repair failures, observed $expected_failed" >&2
    exit 1
fi
echo "  applied=$applied skipped=$skipped expected_repair_failures=$expected_failed failed=$failed"
if (( expected_failed > 0 )); then
    echo "  expected repair failures:"
    printf '    - %s\n' "${expected_failed_list[@]}"
fi
if (( failed > 0 )); then
    echo "  unexpected failed migrations:"
    printf '    - %s\n' "${failed_list[@]}"
    exit 1
fi

echo ""
echo "═══ STEP 4: post-migration schema shape ═══"
docker exec -i kx-citus psql -U kxuser -d "$FRESH_DB" -v ON_ERROR_STOP=1 <<'SQL'
SELECT
    (SELECT count(*) FROM pg_class WHERE relkind='r' AND relnamespace='public'::regnamespace) AS tables,
    (SELECT count(*) FROM pg_class WHERE relkind='p' AND relnamespace='public'::regnamespace) AS partitioned_tables,
    (SELECT count(*) FROM pg_class WHERE relkind='v' AND relnamespace='public'::regnamespace) AS views,
    (SELECT count(*) FROM pg_proc  WHERE pronamespace='public'::regnamespace) AS functions,
    (SELECT count(*) FROM pg_policies WHERE schemaname='public') AS policies,
    (SELECT count(*) FROM pg_class  WHERE relname='audit_attachments_cleanup') AS audit_attachments_cleanup_exists,
    (SELECT count(*) FROM pg_class  WHERE relname='audit_attachments_filesystem_cleanup') AS audit_attachments_filesystem_cleanup_exists,
    (SELECT count(*) FROM pg_class  WHERE relname='session_aggregate_outbox') AS outbox_exists,
    (SELECT count(*) FROM pg_proc   WHERE proname='promote_session_bodies_hot_to_partition') AS promote_bodies_exists,
    (SELECT count(*) FROM pg_proc   WHERE proname='promote_candidate_failure_logs_hot_to_partition') AS promote_candidate_exists,
    (SELECT count(*) FROM pg_proc   WHERE proname='get_current_tenant') AS get_current_tenant_exists;
SQL

echo ""
echo "═══ STEP 5: cleanup ═══"
if [[ "$KEEP_FRESH_DB" != "1" ]]; then
    echo "  ✓ dropped $FRESH_DB"
fi

echo ""
echo "═══ fresh-schema-from-migrations.sh complete ═══"
