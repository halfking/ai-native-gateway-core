#!/usr/bin/env bash
# Apply the small, ordered database repair set shipped with the gateway.
# This is intentionally not a historical migration replay.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${DATABASE_URL:?DATABASE_URL must be explicitly set}"

if command -v psql >/dev/null 2>&1; then
  psql_query() { psql -X -v ON_ERROR_STOP=1 -Atqc "$1" "$DATABASE_URL"; }
  psql_file() { psql -X -v ON_ERROR_STOP=1 "$DATABASE_URL" -f "$1"; }
else
  : "${LLM_GATEWAY_PG_CONTAINER:?psql is unavailable; set LLM_GATEWAY_PG_CONTAINER for Docker execution}"
  : "${LLM_GATEWAY_PG_PASSWORD:?set LLM_GATEWAY_PG_PASSWORD for Docker execution}"
  psql_query() {
    docker exec -e PGPASSWORD="$LLM_GATEWAY_PG_PASSWORD" "$LLM_GATEWAY_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 -U "${LLM_GATEWAY_PG_USER:-llm_gateway}" \
      -d "${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" -Atqc "$1"
  }
  psql_file() {
    docker exec -i -e PGPASSWORD="$LLM_GATEWAY_PG_PASSWORD" "$LLM_GATEWAY_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 -U "${LLM_GATEWAY_PG_USER:-llm_gateway}" \
      -d "${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" -f - < "$1"
  }
fi

# Do not mutate a database whose required base relation is absent. In
# particular, never rename/drop a same-named table from another product.
base_state=$(psql_query "
SELECT CASE
  WHEN to_regclass('public.session_summaries') IS NULL THEN 'missing'
  WHEN EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='session_summaries'
      AND column_name='session_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='session_summaries'
      AND column_name='session_key'
  ) THEN 'reconcile'
  ELSE 'canonical'
END")
if [[ "$base_state" == missing ]]; then
  printf 'error: public.session_summaries is missing; refusing repair sequence\n' >&2
  exit 3
fi
printf 'database schema state: %s\n' "$base_state"

sequence_name="session-summary-and-integrity-2026-09"
# deploy-local.sh already serializes deployments with its build lock. Markers
# are recorded PER FILE ("<sequence>:<basename>") so appending a new migration
# to an already-applied sequence still runs it — a single sequence-wide marker
# silently skipped 656 after it was added to the list (2026-09-05 PG log audit:
# ensure/promote auto_route_selections functions missing on every boot). Each
# underlying migration is idempotent, so re-running is safe.
psql_query "CREATE TABLE IF NOT EXISTS public.gateway_db_revision_sequences (sequence_name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"

# Fixed order: 655 restores the canonical session_summaries columns; 560
# supplies the tenant uniqueness guard; 572 must replace the bounded token
# ratio before 563 performs its backfill; 563/564 then restore the hot trigger
# and safe aggregate backfill; 644/645 finish the independent repairs; 650
# adds the treatment-attribution columns that 656's promote/ensure functions
# and all-view require on the parent; 656 creates the auto_route_selections
# hot heap (no db.go ensure covers it).
#
# 2026-09-05 migration-completion audit (P1): 651/652/653/654 had no Go
# ensure equivalent in db/db.go and were not covered by any track on
# upgraded deployments (only the installer's fresh-install path ran them,
# installer/internal/dbinit/runner.go). 647 and 649 do have Go equivalents
# (db.ensureGoalClientSignalSchema at db/db.go:350 and
# db.ensureRoutingAnalyticsMaterializedViews + routingAnalyticsMVSQL at
# db/db.go:1081/937 respectively), so they stay out of this list.
# 651 (request_logs_hot contract columns + minute-aggregator index),
# 652 (system_monitor_fallback_queue table) and 653/654
# (archive_credential_model_index canonical tuple + DETACH/DROP rewrite)
# are mutually independent of the session_summaries/auto_route_selections
# chain above, so they append safely after 656. 653 precedes 654 because
# 654 supersedes 653's DELETE-based function body with the columnar-safe
# DETACH PARTITION + DROP path (matching the installer's runner order);
# all four are idempotent (IF NOT EXISTS / DROP FUNCTION IF EXISTS +
# CREATE OR REPLACE).
#
# 2026-09-05 PG log audit (deploy gap): V371 (deploy/sql/migrations —
# supplier_errors_hot + monthly columnar partitions + promote/ensure
# functions + unified view + supplier_error_stats) and the
# credential_model_weekly_peak unique bucket index were shipped as repo
# files but no deployment track applied them on upgraded databases, so the
# gateway logged 42P01/42883 on every insert / rollup / partition cron
# (supplier_errors family) and 42P10 on the weekly peak rollup
# (ON CONFLICT target missing). Both are idempotent and safe to re-run;
# they append after 654 like the other independent repairs.
#
# 2026-09-05 evening audit (22003 numeric field overflow): the sequence
# order runs 572 (token-ratio fix) BEFORE 563, but both CREATE OR REPLACE
# the same update_session_summary() — 563's older body re-clobbered the
# fix one second after 572 applied, so every ≥10K-token request failed its
# request-log persist with 22003 (insert + RowsAffected==0 update
# fallback). 661 re-asserts the fixed body AFTER 563 and validates the
# function source; 563's file was also corrected for fresh installs.
# (2026-09-05 evening renumber: the first-cut 657/658 prefixes collided
# with origin/main's 657_durable_llm_tasks / 658_auto_route_structured_features,
# so the weekly-peak repair moved to 660 and the reassert to 661.)
files=(
  "$ROOT_DIR/sql/migrations/startup/655_session_summaries_schema_reconcile.sql"
  "$ROOT_DIR/sql/migrations/startup/560_session_summaries_tenant_uniqueness.sql"
  "$ROOT_DIR/sql/migrations/startup/572_session_summary_large_token_ratio.sql"
  "$ROOT_DIR/sql/migrations/startup/606_session_summaries_agent_expert_tags.sql"
  "$ROOT_DIR/sql/migrations/startup/563_session_summary_trigger_on_hot.sql"
  "$ROOT_DIR/sql/migrations/startup/564_session_summary_backfill_safe.sql"
  "$ROOT_DIR/sql/migrations/startup/644_tuning_views_selfcheck_and_candidate_failure_cache.sql"
  "$ROOT_DIR/sql/migrations/startup/645_session_bodies_hot_request_unique_repair.sql"
  "$ROOT_DIR/sql/migrations/startup/650_auto_route_selection_treatment_attribution.sql"
  "$ROOT_DIR/sql/migrations/startup/656_auto_route_selections_hot.sql"
  "$ROOT_DIR/sql/migrations/startup/651_provider_quality_hot_rollup.sql"
  "$ROOT_DIR/sql/migrations/startup/652_system_monitor_fallback_queue.sql"
  "$ROOT_DIR/sql/migrations/startup/653_archive_credential_model_index_canonical_return.sql"
  "$ROOT_DIR/sql/migrations/startup/654_archive_credential_model_index_detach_drop.sql"
  "$ROOT_DIR/sql/migrations/startup/660_credential_model_weekly_peak_unique.sql"
  "$ROOT_DIR/sql/migrations/startup/661_session_summary_token_ratio_reassert.sql"
  "$ROOT_DIR/deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql"
)
for file in "${files[@]}"; do
  [[ -f "$file" ]] || { printf 'error: missing migration %s\n' "$file" >&2; exit 4; }
  marker="${sequence_name}:$(basename "$file")"
  if [[ "$(psql_query "SELECT 1 FROM public.gateway_db_revision_sequences WHERE sequence_name='${marker}' LIMIT 1")" == 1 ]]; then
    printf 'already applied: %s\n' "${file#"$ROOT_DIR/"}"
    continue
  fi
  printf 'applying %s\n' "${file#"$ROOT_DIR/"}"
  psql_file "$file"
  psql_query "INSERT INTO public.gateway_db_revision_sequences (sequence_name) VALUES ('${marker}') ON CONFLICT (sequence_name) DO NOTHING"
done
# Retire the legacy sequence-wide marker so it cannot mask future appends.
psql_query "DELETE FROM public.gateway_db_revision_sequences WHERE sequence_name='${sequence_name}'"
printf 'database revision sequence completed successfully: %s\n' "$sequence_name"
