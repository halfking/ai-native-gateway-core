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
# silently fall back to numeric directory ordering. Do not use a fixed line
# window here: the sequence is intentionally append-only and has grown beyond
# the original 40-line contract fixture.
sequence=$(awk '/^files=\(/{inside=1} inside{print} inside && /^\)/{exit}' "$SCRIPT")
# R16 (2026-09-12): the 693/699-class channel gaps happened because entries
# newer than V371 were outside this self-test — required list now runs to the
# current top of the startup track. 665 and 687-692 are intentional sequence
# gaps (out-of-band ledger entries / installer-only channel; documented in the
# sequence comments), so they stay excluded here by design.
for required in 655 560 572 606 563 564 644 645 650 651 652 653 654 656 659 660 661 662 663 664 V371 \
                666 667 668 669 670 671 672 673 674 675 676 677 678 679 680 681 682 683 684 685 \
                686 693 694 695 696 697 698 699 700 701 703; do
  printf '%s\n' "$sequence" | grep -q "${required}_" || {
    printf 'missing sequence entry: %s\n' "$required" >&2
    exit 1
  }
done

# Directory-driven channel invariant (2026-09-14 audit F-P2-1): the highest-
# numbered startup migration file MUST be present in the channel files=(
# ...) array. The trailing-sequence guard in migration_700_test.go is a
# hardcoded point-fix (700/701/703); without this check a parallel line can
# land 704_NNN.sql with full installer sync but never register the channel
# entry — the exact 693/699/701/703 recurrence shape — and every gate stays
# green while upgraded databases never reach 704.
top_startup=$(ls "$ROOT_DIR"/sql/migrations/startup/*.sql 2>/dev/null \
  | grep -v '\.down\.sql$' \
  | sed -E 's#.*/([0-9]{3})_.*#\1#' | sort -n | tail -1)
if [[ -n "$top_startup" ]]; then
  printf '%s\n' "$sequence" | grep -q "${top_startup}_" || {
    printf 'startup migration %s exists but is missing from the channel files=(...) array\n' "$top_startup" >&2
    exit 1
  }
fi

# Execute the REAL clobber guard (2026-09-14 audit F-P0-1 root cause):
# grep-spot-checks stayed green while the pre-flight guard exited 5 on the
# unregistered 703 chain. Extract the guard's code block (redefined_functions
# scan + guard_violations check, ending at the guard_violations loop's
# closing `done)")` line) and evaluate it verbatim against the same arrays —
# no re-implementation that could drift from deploy-time behaviour. If the
# extraction anchors ever drift, the unset-variable failures below fail this
# test loudly rather than silently skipping the guard.
guard_block="$(awk '
  /^redefined_functions="\$\($/ {inside=1}
  inside {print}
  inside && /^done\)"$/ {exit}
' "$SCRIPT")"
if [[ -z "$guard_block" ]]; then
  printf 'could not extract clobber guard block from %s\n' "$SCRIPT" >&2
  exit 1
fi
# The guard reads the files=() and intentional_function_chains=() arrays;
# evaluate the same definitions the deploy script uses (files block captured
# above, chains extracted with the same anchors).
eval "$sequence"
chains_block="$(awk '/^intentional_function_chains=\(/{inside=1} inside{print} inside && /^\)$/{exit}' "$SCRIPT")"
[[ -n "$chains_block" ]] || { printf 'could not extract intentional_function_chains\n' >&2; exit 1; }
eval "$chains_block"
eval "$guard_block"
if [[ -n "${guard_violations:-}" ]]; then
  printf 'clobber guard violations (deploy would exit 5):\n%s\n' "$guard_violations" >&2
  exit 5
fi

# Function clobber guard (2026-09-05 PG log audit): 572→563 silently
# re-clobbered update_session_summary(). Every multi-file function chain must
# stay registered in intentional_function_chains or deployment aborts.
for chain in \
  'update_session_summary|572_session_summary_large_token_ratio.sql|563_session_summary_trigger_on_hot.sql|661_session_summary_token_ratio_reassert.sql|' \
  'archive_credential_model_index|653_archive_credential_model_index_canonical_return.sql|654_archive_credential_model_index_detach_drop.sql|'; do
  grep -qF -- "'$chain'" "$SCRIPT" || {
    printf 'missing intentional function chain registration: %s\n' "$chain" >&2
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
