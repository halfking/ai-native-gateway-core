#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/apply-db-revision-sequence.sh"

bash -n "$SCRIPT"

for required in \
  "655_session_summaries_schema_reconcile.sql" \
  "560_session_summaries_tenant_uniqueness.sql" \
  "572_session_summary_large_token_ratio.sql" \
  "606_session_summaries_agent_expert_tags.sql" \
  "563_session_summary_trigger_on_hot.sql" \
  "564_session_summary_backfill_safe.sql" \
  "644_tuning_views_selfcheck_and_candidate_failure_cache.sql" \
  "645_session_bodies_hot_request_unique_repair.sql"; do
  test -f "$ROOT_DIR/sql/migrations/startup/$required"
done

# Keep the migration sequence explicit in the executable so deployment cannot
# silently fall back to numeric directory ordering.
sequence=$(grep -A12 '^files=(' "$SCRIPT")
for required in 655 560 572 606 563 564 644 645; do
  printf '%s\n' "$sequence" | grep -q "${required}_" || {
    printf 'missing sequence entry: %s\n' "$required" >&2
    exit 1
  }
done

printf 'apply-db-revision-sequence contract passed\n'
