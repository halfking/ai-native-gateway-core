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
  "$ROOT_DIR/sql/migrations/startup/659_legacy_promote_atomic_cte.sql"
  "$ROOT_DIR/sql/migrations/startup/651_provider_quality_hot_rollup.sql"
  "$ROOT_DIR/sql/migrations/startup/652_system_monitor_fallback_queue.sql"
  "$ROOT_DIR/sql/migrations/startup/653_archive_credential_model_index_canonical_return.sql"
  "$ROOT_DIR/sql/migrations/startup/654_archive_credential_model_index_detach_drop.sql"
  "$ROOT_DIR/sql/migrations/startup/660_credential_model_weekly_peak_unique.sql"
  "$ROOT_DIR/sql/migrations/startup/661_session_summary_token_ratio_reassert.sql"
  "$ROOT_DIR/sql/migrations/startup/662_feature_distribution_stats.sql"
  "$ROOT_DIR/sql/migrations/startup/663_training_export.sql"
  "$ROOT_DIR/sql/migrations/startup/664_provider_error_details_agg_key_dedup.sql"
  "$ROOT_DIR/deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql"
)

# 2026-09-05 PG log audit follow-up (function clobber guard): 572 and 563
# both CREATE OR REPLACE update_session_summary() and 563's older body
# silently overwrote the 572 fix one second after it applied (397 x 22003
# on the day). Any function defined by more than one sequence file must be
# registered below as an intentional chain, written in EXACT sequence order
# with the intended final definition LAST — a chain whose entries drift out
# of sequence order fails the equality check below. Unregistered duplicates
# abort the deployment before the first file is applied. The scanner strips
# SQL line/block comments and dollar-quoted bodies, so prose or dynamic SQL
# that merely mentions CREATE OR REPLACE cannot trigger it.
intentional_function_chains=(
  # 563 restores the hot trigger with the corrected unbounded-numeric ratio
  # body; 661 re-asserts the same fixed body AFTER 563 and validates the
  # function source. 572 must stay before 563; 661 must stay last.
  'update_session_summary|572_session_summary_large_token_ratio.sql|563_session_summary_trigger_on_hot.sql|661_session_summary_token_ratio_reassert.sql|'
  # 654 supersedes 653's DELETE-based archive body with the columnar-safe
  # DETACH PARTITION + DROP path and must stay the later entry.
  'archive_credential_model_index|653_archive_credential_model_index_canonical_return.sql|654_archive_credential_model_index_detach_drop.sql|'
)
redefined_functions="$(
  for file in "${files[@]}"; do
    base="$(basename "$file")"
    awk '
      BEGIN { indq = 0; inbc = 0 }
      {
        line = $0
        clean = ""
        while (length(line) > 0) {
          if (indq) {
            end = index(line, dqtag)
            if (end == 0) { line = "" }
            else { line = substr(line, end + length(dqtag)); indq = 0 }
          } else if (inbc) {
            end = index(line, "*/")
            if (end == 0) { line = "" }
            else { line = substr(line, end + 2); inbc = 0 }
          } else {
            dq = match(line, /\$[A-Za-z_][A-Za-z0-9_]*\$|\$\$/)
            bc = index(line, "/*")
            lc = index(line, "--")
            if (dq > 0 && (bc == 0 || dq < bc) && (lc == 0 || dq < lc)) {
              clean = clean substr(line, 1, dq - 1)
              tag = substr(line, dq, RLENGTH)
              rest = substr(line, dq + RLENGTH)
              end = index(rest, tag)
              if (end == 0) { indq = 1; line = "" }
              else { line = substr(rest, end + length(tag)) }
            } else if (bc > 0 && (lc == 0 || bc < lc)) {
              clean = clean substr(line, 1, bc - 1)
              line = substr(line, bc + 2)
              end = index(line, "*/")
              if (end == 0) { inbc = 1; line = "" }
              else { line = substr(line, end + 2) }
            } else if (lc > 0) {
              clean = clean substr(line, 1, lc - 1)
              line = ""
            } else {
              clean = clean line
              line = ""
            }
          }
        }
        if (length(clean) > 0) print tolower(clean)
      }
    ' "$file" \
      | grep -oE 'create[[:space:]]+or[[:space:]]+replace[[:space:]]+function[[:space:]]+("?[a-z_][a-z0-9_$]*"?\.)*"?[a-z_][a-z0-9_$]*"?' \
      | sed -E 's/.*function[[:space:]]+//; s/"//g; s/^([a-z_][a-z0-9_$]*\.)+//' \
      | awk -v f="$base" '{ print $0 "\t" f }' || true
  done | awk -F'\t' '
    {
      if (!(($1) in chain)) { chain[$1] = "|"; hits[$1] = 0 }
      if (index(chain[$1], "|" $2 "|") == 0) { hits[$1]++; chain[$1] = chain[$1] $2 "|" }
    }
    END { for (f in chain) if (hits[f] > 1) print f chain[f] }'
)"
guard_violations="$(printf '%s\n' "$redefined_functions" | while IFS= read -r entry; do
  if [[ -z "$entry" ]]; then continue; fi
  known=0
  for rule in "${intentional_function_chains[@]}"; do
    if [[ "$entry" == "$rule" ]]; then known=1; break; fi
  done
  if [[ "$known" == 0 ]]; then printf '%s\n' "${entry%|}"; fi
done)"
if [[ -n "$guard_violations" ]]; then
  printf 'error: sequence redefines the same function from multiple files; register each intentional chain in intentional_function_chains (entries must match sequence order, last entry wins):\n%s\n' "$guard_violations" >&2
  exit 5
fi
if [[ -n "$redefined_functions" ]]; then
  printf 'function clobber guard: multi-file redefinitions match the intentional allowlist:\n'
  printf '%s\n' "$redefined_functions" | sed 's/^/  /; s/|$//'
fi

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
