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
  "645_session_bodies_hot_request_unique_repair.sql" \
  "656_auto_route_selections_hot.sql"; do
  test -f "$ROOT_DIR/sql/migrations/startup/$required"
done

# Keep the migration sequence explicit in the executable so deployment cannot
# silently fall back to numeric directory ordering.
sequence=$(grep -A14 '^files=(' "$SCRIPT")
for required in 655 560 572 606 563 564 644 645 656; do
  printf '%s\n' "$sequence" | grep -q "${required}_" || {
    printf 'missing sequence entry: %s\n' "$required" >&2
    exit 1
  }
done

# 656 has no db.go ensure compensation; the sequence is its only存量 deployment
# path besides the installer fresh-install runner.
if ! grep -q '656_auto_route_selections_hot' "$ROOT_DIR/installer/internal/dbinit/runner.go"; then
  printf 'installer runner is missing 656_auto_route_selections_hot\n' >&2
  exit 1
fi

# 644's CHECK rebuild must be definition-aware: deploying must not re-run a
# validated ADD CONSTRAINT (ACCESS EXCLUSIVE + full scan) when the canonical
# taxonomy is already in place.
if ! grep -q "position('no_eligible_model' in pg_get_constraintdef" \
    "$ROOT_DIR/sql/migrations/startup/644_tuning_views_selfcheck_and_candidate_failure_cache.sql"; then
  printf '644 CHECK rebuild is not definition-aware\n' >&2
  exit 1
fi

printf 'apply-db-revision-sequence contract passed\n'
