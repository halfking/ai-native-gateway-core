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
# deploy-local.sh already serializes deployments with its build lock. The
# marker makes standalone retries cheap; each underlying migration is idempotent.
psql_query "CREATE TABLE IF NOT EXISTS public.gateway_db_revision_sequences (sequence_name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"
if [[ "$(psql_query "SELECT 1 FROM public.gateway_db_revision_sequences WHERE sequence_name='${sequence_name}' LIMIT 1")" == 1 ]]; then
  printf 'database revision sequence already applied: %s\n' "$sequence_name"
  exit 0
fi

# Fixed order: 655 restores the canonical session_summaries columns; 560
# supplies the tenant uniqueness guard; 572 must replace the bounded token
# ratio before 563 performs its backfill; 563/564 then restore the hot trigger
# and safe aggregate backfill; 644/645 finish the independent repairs; 656
# creates the auto_route_selections hot heap (no db.go ensure covers it).
files=(
  "$ROOT_DIR/sql/migrations/startup/655_session_summaries_schema_reconcile.sql"
  "$ROOT_DIR/sql/migrations/startup/560_session_summaries_tenant_uniqueness.sql"
  "$ROOT_DIR/sql/migrations/startup/572_session_summary_large_token_ratio.sql"
  "$ROOT_DIR/sql/migrations/startup/606_session_summaries_agent_expert_tags.sql"
  "$ROOT_DIR/sql/migrations/startup/563_session_summary_trigger_on_hot.sql"
  "$ROOT_DIR/sql/migrations/startup/564_session_summary_backfill_safe.sql"
  "$ROOT_DIR/sql/migrations/startup/644_tuning_views_selfcheck_and_candidate_failure_cache.sql"
  "$ROOT_DIR/sql/migrations/startup/645_session_bodies_hot_request_unique_repair.sql"
  "$ROOT_DIR/sql/migrations/startup/656_auto_route_selections_hot.sql"
)
for file in "${files[@]}"; do
  [[ -f "$file" ]] || { printf 'error: missing migration %s\n' "$file" >&2; exit 4; }
  printf 'applying %s\n' "${file#"$ROOT_DIR/"}"
  psql_file "$file"
done
psql_query "INSERT INTO public.gateway_db_revision_sequences (sequence_name) VALUES ('${sequence_name}') ON CONFLICT (sequence_name) DO NOTHING"
printf 'database revision sequence completed successfully: %s\n' "$sequence_name"
