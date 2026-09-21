#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/verify-promote-candidate-failure-logs.sh
# Purpose:       End-to-end verify the hot→columnar promote for
#                candidate_failure_logs (migration 628) against the isolated
#                audit PG.
#
# What this proves:
#   1. promote_candidate_failure_logs_hot_to_partition('0s'::interval, N)
#      moves rows from public.candidate_failure_logs_hot into the
#      monthly-columnar parent partition.
#   2. aggregation_id is correctly preserved on the moved rows (this is the
#      exact bug migration 627/628 fixed: pre-fix the columnar side lost
#      aggregation_id and forced the unified view to use
#      `COALESCE(aggregation_id, -id)`).
#   3. The function is idempotent within a single batch (re-running on an
#      empty hot returns 0 and is a no-op).
#
# Out of scope (deferred to a follow-up audit run; requires min-prereqs to
# also create session_bodies_hot / session_turns_hot and their parent
# partitions, which is too large a fixture for this script):
#   - promote_session_bodies_hot_to_partition
#   - promote_session_turns_hot_to_partition
#
# Usage:
#   bash scripts/audit/verify-promote-candidate-failure-logs.sh
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

echo "═══ STEP 1: ensure 627-631 + ensure fn applied ═══"
PSQL -f scripts/audit/sql/min-prereqs.sql >/dev/null
for v in 627 628; do
    fsql=$(ls "sql/migrations/startup/${v}_"*.sql | grep -v '\.down\.sql$' | head -1)
    PSQL -f "$fsql" >/dev/null
    echo "  ✓ $(basename "$fsql")"
done

PSQL -c "SELECT
    'hot_count' AS what, count(*) FROM public.candidate_failure_logs_hot
UNION ALL SELECT 'parent_count', count(*) FROM public.candidate_failure_logs;" 2>&1 | tail -10

echo "═══ STEP 2: insert 50 hot rows (25 with aggregation_id, 25 NULL) ═══"
PSQL <<'SQL'
WITH
  seq AS (
    SELECT generate_series(1, 50) AS i
  )
INSERT INTO public.candidate_failure_logs_hot
    (request_id, ts, tenant_id, credential_id, provider_id, raw_model_name,
     attempt_index, error_kind, error_message, upstream_status_code,
     upstream_response_body, upstream_response_preview, latency_ms,
     retryable, per_attempt_latency_ms, extracted_upstream_status_code,
     diagnosed_error_kind, context, session_id, aggregation_id)
SELECT
    'req_promote_' || i,
    NOW() - (i || ' minutes')::interval,
    'tenant_promote',
    NULL, NULL, 'gpt-4o', 0, 'timeout', 'simulated', NULL, NULL, NULL, 100,
    false, NULL, NULL, NULL, NULL, 'sess_promote_' || i,
    CASE WHEN i % 2 = 0 THEN 1000 + i ELSE NULL END
FROM seq;
SELECT count(*) AS hot_after_seed,
       count(aggregation_id) AS hot_with_agg,
       count(*) FILTER (WHERE aggregation_id IS NULL) AS hot_null_agg
FROM public.candidate_failure_logs_hot;
SQL

echo "═══ STEP 3: call promote (retention=1s, batch=100) ═══"
PSQL <<'SQL'
-- 628 enforces p_retention > 0, so we use 1 second; the hot rows above
-- are ts = NOW() - i minutes, all older than the retention window.
SELECT public.promote_candidate_failure_logs_hot_to_partition('1 second'::interval, 100) AS promoted;
SQL

echo "═══ STEP 4: post-promote verification ═══"
PSQL <<'SQL'
SELECT
    (SELECT count(*) FROM public.candidate_failure_logs_hot) AS hot_remaining,
    (SELECT count(*) FROM public.candidate_failure_logs) AS parent_total,
    (SELECT count(*) FROM public.candidate_failure_logs
     WHERE tenant_id='tenant_promote') AS parent_tenant_total,
    (SELECT count(*) FROM public.candidate_failure_logs
     WHERE tenant_id='tenant_promote' AND aggregation_id IS NOT NULL) AS parent_with_agg,
    (SELECT count(*) FROM public.candidate_failure_logs
     WHERE tenant_id='tenant_promote' AND aggregation_id IS NULL) AS parent_null_agg;
SQL

echo "═══ STEP 5: idempotency — second call returns 0 ═══"
PSQL <<'SQL'
SELECT public.promote_candidate_failure_logs_hot_to_partition('1 second'::interval, 100) AS promoted_again;
SELECT count(*) AS hot_remaining_again FROM public.candidate_failure_logs_hot;
SQL

echo "═══ STEP 6: unified view check ═══"
PSQL <<'SQL'
SELECT
    (SELECT count(*) FROM public.candidate_failure_logs_unified) AS unified_total,
    (SELECT count(*) FROM public.candidate_failure_logs_unified
     WHERE tenant_id='tenant_promote') AS unified_tenant;
SQL

echo "═══ verify-promote-candidate-failure-logs.sh complete ═══"
