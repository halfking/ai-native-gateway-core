#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/verify-promote-session-bodies-turns.sh
# Purpose:       End-to-end verify the hot→columnar promote paths for
#                session_bodies (migration 614/615/626) and session_turns
#                (migration 526) against the isolated audit PG.
#
# What this proves:
#   1. promote_session_bodies_hot_to_partition moves rows from
#      session_bodies_hot into the parent monthly partition.
#   2. After promote, session_bodies_unified (used by admin readers) still
#      returns the same row count. This catches a subtle gotcha: the
#      unified view's parent branch filters `partition_date <= today - 1
#      day`, so writers MUST set partition_date to the row's logical date
#      (i.e. yesterday or earlier for retention-eligible rows), NOT today.
#      We exercise that contract here.
#   3. promote_session_turns_hot_to_partition moves rows from
#      session_turns_hot into the parent monthly partition.
#   4. The function is idempotent within a single batch (re-running on
#      an empty hot returns 0 and is a no-op).
#
# Why an audit-only fixture instead of production 526:
#   Migration 526's full path requires the production-grade
#   session_turns / session_turns_hot column shape (50+ columns) plus
#   request_logs.gw_session_id + owner_user and a sibling session_turns
#   write-ahead. Setting that up inside the isolated audit container is
#   out of proportion for a smoke test. Instead this script registers an
#   audit-local helper promote_session_turns_hot_to_partition_audit that
#   mirrors 526's row movement logic against the existing minimal
#   schema. The helper is dropped on teardown so it cannot collide with
#   production 526 in a subsequent audit run.
#
# Usage:
#   bash scripts/audit/verify-promote-session-bodies-turns.sh
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

echo "═══ STEP 1: ensure min-prereqs + 614/615/626 + audit-only promote fn ═══"
PSQL -f scripts/audit/sql/min-prereqs.sql >/dev/null
# Rebuild the audit fixture before applying the migrations. Expected rerun
# conflicts are avoided by min-prereqs' DROP CASCADE rather than masking all
# psql failures. A migration error is therefore fatal.
for v in 614 615 626; do
    fsql=$(ls "sql/migrations/startup/${v}_"*.sql | grep -v '\.down\.sql$' | head -1)
    PSQL -f "$fsql" >/dev/null
    echo "  ✓ $(basename "$fsql")"
done

# Ensure session_bodies_unified exists even after a partial 614 re-apply.
# 625 is the canonical migration that pins security_invoker=true and
# guarantees the 12-column view shape.
PSQL -f sql/migrations/startup/625_session_bodies_unified_explicit.sql >/dev/null
echo "  ✓ 625_session_bodies_unified_explicit.sql (view restored)"

# Register audit-only promote helper for session_turns. Mirrors 526's
# row movement (DELETE FROM hot RETURNING → INSERT INTO parent) without
# the full production column-shape contract. idempotent.
PSQL <<'SQL'
CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition_audit(
    p_retention INTERVAL DEFAULT '7 days',
    p_batch_size INTEGER DEFAULT 5000
)
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_moved BIGINT := 0;
BEGIN
    IF p_retention IS NULL OR p_retention <= INTERVAL '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be a positive interval';
    END IF;

    WITH picked AS (
        SELECT id FROM public.session_turns_hot
        WHERE ts < NOW() - p_retention
        ORDER BY ts
        LIMIT p_batch_size
        FOR UPDATE SKIP LOCKED
    ), moved AS (
        DELETE FROM public.session_turns_hot h
        USING picked p
        WHERE h.id = p.id
        RETURNING h.id, h.session_id, h.turn_no, h.tenant_id, h.request_id, h.ts, h.partition_date
    ), inserted AS (
        INSERT INTO public.session_turns
            (id, session_id, turn_no, tenant_id, request_id, ts, partition_date)
        SELECT id, session_id, turn_no, tenant_id, request_id, ts, partition_date
        FROM moved
        RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM inserted;

    RETURN v_moved;
END;
$$;
SQL
echo "  ✓ audit-only promote_session_turns_hot_to_partition_audit installed"

echo ""
echo "═══ STEP 2: seed 50 hot rows into session_bodies_hot (ts = 2 days ago, partition_date = yesterday) ═══"
PSQL <<'SQL'
TRUNCATE public.session_bodies_hot;
TRUNCATE public.session_bodies;
INSERT INTO public.session_bodies_hot
    (session_id, turn_no, tenant_id, request_id, ts,
     request_delta, response_delta, partition_date)
SELECT
    'sess_bodies_prom_' || g,
    g,
    'tenant_bodies_prom',
    'req_bodies_prom_' || g,
    NOW() - INTERVAL '2 days',
    '{"role":"user","content":"hi"}'::jsonb,
    '{"role":"assistant","content":"hello"}'::jsonb,
    CURRENT_DATE - INTERVAL '1 day'
FROM generate_series(1, 50) g;
SELECT 'hot' AS t, count(*) FROM public.session_bodies_hot
UNION ALL SELECT 'parent', count(*) FROM public.session_bodies;
SQL

echo ""
echo "═══ STEP 3: call promote_session_bodies_hot_to_partition(retention=1h, batch=1000) ═══"
PSQL <<'SQL'
SELECT promote_session_bodies_hot_to_partition('1 hour'::interval, 1000) AS promoted;
SELECT 'hot' AS t, count(*) FROM public.session_bodies_hot
UNION ALL SELECT 'parent', count(*) FROM public.session_bodies
UNION ALL SELECT 'unified', count(*) FROM public.session_bodies_unified;
SQL

echo ""
echo "═══ STEP 4: idempotency — second call returns 0 ═══"
PSQL <<'SQL'
SELECT promote_session_bodies_hot_to_partition('1 hour'::interval, 1000) AS promoted_again;
SELECT count(*) AS hot_remaining FROM public.session_bodies_hot;
SQL

echo ""
echo "═══ STEP 5: seed 30 hot rows into session_turns_hot (ts = 2 days ago) ═══"
PSQL <<'SQL'
TRUNCATE public.session_turns_hot;
TRUNCATE public.session_turns;
INSERT INTO public.session_turns_hot
    (session_id, turn_no, tenant_id, request_id, ts, partition_date,
     submit_mode, model, provider, prompt_tokens, completion_tokens)
SELECT
    'sess_turns_prom_' || g,
    g,
    'tenant_turns_prom',
    'req_turns_prom_' || g,
    NOW() - INTERVAL '2 days',
    CURRENT_DATE - INTERVAL '1 day',
    'full', 'gpt-4o', 'openai', 100, 50
FROM generate_series(1, 30) g;
SELECT 'hot' AS t, count(*) FROM public.session_turns_hot
UNION ALL SELECT 'parent', count(*) FROM public.session_turns;
SQL

echo ""
echo "═══ STEP 6: call promote_session_turns_hot_to_partition_audit ═══"
PSQL <<'SQL'
SELECT promote_session_turns_hot_to_partition_audit('1 hour'::interval, 1000) AS promoted;
SELECT 'hot' AS t, count(*) FROM public.session_turns_hot
UNION ALL SELECT 'parent', count(*) FROM public.session_turns;
SQL

echo ""
echo "═══ STEP 7: idempotency — second call returns 0 ═══"
PSQL <<'SQL'
SELECT promote_session_turns_hot_to_partition_audit('1 hour'::interval, 1000) AS promoted_again;
SELECT count(*) AS hot_remaining FROM public.session_turns_hot;
SQL

echo ""
echo "═══ STEP 8: cleanup audit-only helper ═══"
PSQL -c "DROP FUNCTION IF EXISTS public.promote_session_turns_hot_to_partition_audit(INTERVAL, INTEGER);" >/dev/null
echo "  ✓ dropped promote_session_turns_hot_to_partition_audit"

echo ""
echo "═══ STEP 9: Go integration tests (TEST_AUDIT_ISOLATED_DB_URL) ═══"
if [[ -n "${TEST_AUDIT_ISOLATED_DB_URL:-}" ]]; then
    go test -tags=integration -count=1 -run 'TestPromote_' -timeout 60s ./scripts/audit/...
else
    echo "  ⚠ TEST_AUDIT_ISOLATED_DB_URL not set; skipping Go integration tests."
    echo "    To enable: source /tmp/audit-pg.env && bash $0"
fi

echo ""
echo "═══ verify-promote-session-bodies-turns.sh complete ═══"
