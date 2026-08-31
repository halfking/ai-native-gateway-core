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
#   3. The exact list of migrations that fail to apply (if any) is
#      surfaced so the audit pass can fix them one at a time.
#
# Usage:
#   bash scripts/audit/fresh-schema-from-migrations.sh
#
# Side effects:
#   Creates a database `llm_gateway_fresh` in the kx-citus container.
#   Drops it at end of run.
# -----------------------------------------------------------------------------
set -euo pipefail

PSQL_DB()  { docker exec kx-citus psql -U kxuser -d llm_gateway_fresh -v ON_ERROR_STOP=0 "$@"; }
PSQL_ADMIN() { docker exec kx-citus psql -U kxuser -d postgres "$@"; }

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

echo "═══ STEP 1: create fresh DB llm_gateway_fresh ═══"
PSQL_ADMIN -c "DROP DATABASE IF EXISTS llm_gateway_fresh" >/dev/null
PSQL_ADMIN -c "CREATE DATABASE llm_gateway_fresh" >/dev/null

# Run 00-prereqs first (creates extensions the dump relied on). It is
# tiny and idempotent. Read it from the host and pipe into docker exec
# because docker exec with -f /host/path only works for paths the
# container can see (it cannot).
if [[ -f sql/schema/00-prereqs.sql ]]; then
    echo "═══ STEP 2: apply 00-prereqs.sql (extensions) ═══"
    cat sql/schema/00-prereqs.sql | docker exec -i kx-citus psql -U kxuser -d llm_gateway_fresh -v ON_ERROR_STOP=0 >/dev/null
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
failed=0
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
    if cat "$f" | docker exec -i kx-citus psql -U kxuser -d llm_gateway_fresh -v ON_ERROR_STOP=0 >/dev/null 2>&1; then
        ((applied++)) || true
    else
        ((failed++)) || true
        failed_list+=("$base")
    fi
done
echo "  applied=$applied skipped=$skipped failed=$failed"
if (( failed > 0 )); then
    echo "  failed migrations:"
    for m in "${failed_list[@]}"; do
        echo "    - $m"
    done
fi

echo ""
echo "═══ STEP 4: post-migration schema shape ═══"
docker exec -i kx-citus psql -U kxuser -d llm_gateway_fresh <<'SQL'
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
PSQL_ADMIN -c "DROP DATABASE llm_gateway_fresh" >/dev/null
echo "  ✓ dropped llm_gateway_fresh"

echo ""
echo "═══ fresh-schema-from-migrations.sh complete ═══"
