#!/bin/bash
# =====================================================================
# mock-only PostgreSQL init — runs once on a fresh volume, inside the
# postgres container, before the gateway ever starts.
# =====================================================================
# Sequence (deliberately mirrors scripts/local-r112-migrate.sh, the
# proven bring-up recipe, with one deliberate difference: migration
# failures do NOT abort the stack — they are tallied and printed so a
# failed migration in a non-routing area cannot silently fake-green, but
# also cannot block the bring-up. The real gate is the stack script's
# post-boot verification + the load test itself, and the ledger below
# lands in the run report.)
# =====================================================================
set -uo pipefail

SQL_ROOT="/sql"
SEED_FILE="/seed/mock-only-seed.sql"
FAIL_LOG="/tmp/mock-only-migration-failures.txt"
: > "$FAIL_LOG"

say() { echo "[mock-only-init] $*"; }

# ── 1. extensions (explicit, tolerant — no citus dependency) ────────
say "extensions..."
for ext in btree_gist pg_trgm pgcrypto plpgsql pg_stat_statements pgstattuple vector citus citus_columnar pgcolumnar; do
  psql -U "${POSTGRES_USER:-mocktest}" -d "${POSTGRES_DB:-llm_gateway_mock}" -v ON_ERROR_STOP=0 -qc "CREATE EXTENSION IF NOT EXISTS ${ext};" >/dev/null 2>&1 || true
done
# columnar access-method alias when only pgcolumnar is present (same
# fallback shape as 00-prereqs.sql, inlined so no patching is needed).
psql -U "${POSTGRES_USER:-mocktest}" -d "${POSTGRES_DB:-llm_gateway_mock}" -v ON_ERROR_STOP=1 <<'EOSQL' >/dev/null 2>&1 || true
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_am WHERE amname = 'columnar') THEN
    EXECUTE 'CREATE ACCESS METHOD columnar TYPE TABLE HANDLER pgcolumnar.columnar_handler';
  END IF;
END $$;
EOSQL

# ── 2. baseline schema (columnar → heap, same sed as local-deploy) ──
say "schema 01-schema.sql (columnar→heap)..."
sed 's/SET default_table_access_method = columnar;/SET default_table_access_method = heap;/g' \
  "$SQL_ROOT/schema/01-schema.sql" | psql -U "${POSTGRES_USER:-mocktest}" -d "${POSTGRES_DB:-llm_gateway_mock}" -v ON_ERROR_STOP=1 -q >/dev/null \
  || { say "FATAL: base schema failed"; exit 1; }

# ── 3. base seed (skip __REDACTED_ rows, same awk as local-r112) ────
say "seed 02-seed.sql..."
awk '!/__REDACTED_/' "$SQL_ROOT/schema/02-seed.sql" | psql -U "${POSTGRES_USER:-mocktest}" -d "${POSTGRES_DB:-llm_gateway_mock}" -v ON_ERROR_STOP=1 -q >/dev/null \
  || { say "FATAL: base seed failed"; exit 1; }

# ── 4. hot-table prerequisites (belt-and-suspenders; dump has them) ──
psql -U "${POSTGRES_USER:-mocktest}" -d "${POSTGRES_DB:-llm_gateway_mock}" -v ON_ERROR_STOP=1 -qc "
  CREATE TABLE IF NOT EXISTS _request_logs_hot (LIKE _request_logs INCLUDING ALL);
  CREATE TABLE IF NOT EXISTS usage_ledger_hot  (LIKE usage_ledger  INCLUDING ALL);
  CREATE TABLE IF NOT EXISTS request_wal_hot   (LIKE request_wal   INCLUDING ALL);
" >/dev/null 2>&1 || true

# ── 5. startup migrations (skip list from local-r112-migrate.sh + the
#       pg_cron operator-gated 755; failures tallied, not fatal) ──────
SKIP_MIGRATIONS="002_work_types.sql 004_tuning_signals.sql 005_tuning_proposals.sql 021_tool_registry_and_metatools.sql 029_seed_tool_registry.sql 031_provider_settings.sql 033_credential_model_call_history.sql 036_fp_slot_limit.sql 302_unified_probe_scheduler.sql 304_model_health_dashboard.sql 308_probe_dashboard_state_alignment.sql 310_session_summaries.sql 313_probe_dashboard_followup.sql 315_prompt_injection_detection.sql 316_output_compliance_monitoring.sql 317_partition_credential_model_index.sql 321_cleanup_stale_in_progress.sql 340_create_partition_query_views.sql 342_create_other_table_views.sql 341_hot_table_independence.fix.sql 343_fix_routing_decision_log_columnar.sql 344_usage_ledger_hot_independence.sql 345_request_wal_hot_independence.sql 346_routing_decision_log_hot_independence.sql 347_credential_model_index_hot_independence.sql 348_tool_usage_stats_hot_independence.sql 349_credit_ledger_hot_independence.sql 350_session_analytics_fix.sql 351_session_analytics_tables.sql 353_request_logs_bodies_hot_independence.sql 354_credential_model_index_hot_independence.sql 355_session_analytics_indexes.sql 356_session_health_columns.sql 357_session_analytics_aggregation_views.sql 358_session_ownership.sql 364_prompt_injection_enhanced.sql 365_output_compliance_policy_enhance.sql 755_drop_dead_cleanup_expired_session_turn_logs.sql"

applied=0; skipped=0; failed=0
: > /tmp/mock-only-pending.txt
for mig in $(find "$SQL_ROOT/migrations/startup" -maxdepth 1 -name '*.sql' ! -name '*.down.sql' -type f | sort); do
  echo "$mig" >> /tmp/mock-only-pending.txt
done

# ── convergence loop: order-dependent migrations can fail against a
#    raw dump on pass 1 but replay cleanly once their neighbours landed
#    (verified live: 640_session_turns_protocol_fields). Re-run failures
#    until a pass makes no progress (max 3 passes); only what STILL fails
#    on the final pass lands in the ledger.
for pass in 1 2 3; do
  : > /tmp/mock-only-next.txt
  pass_failed=0
  while IFS= read -r mig; do
    name="$(basename "$mig")"
    case " $SKIP_MIGRATIONS " in *" $name "*) skipped=$((skipped+1)); continue ;; esac
    logtmp=$(mktemp)
    if psql -U "${POSTGRES_USER:-mocktest}" -d "${POSTGRES_DB:-llm_gateway_mock}" -v ON_ERROR_STOP=1 -q -f - < "$mig" >"$logtmp" 2>&1; then
      applied=$((applied+1))
    elif grep -qE 'already exists|duplicate key value violates unique constraint|relation .* already exists|function .* already exists|multiple primary keys|constraint .* already exists' "$logtmp"; then
      skipped=$((skipped+1))
    else
      pass_failed=$((pass_failed+1))
      echo "$mig" >> /tmp/mock-only-next.txt
      if [ "$pass" = "3" ]; then
        { echo "=== $name ==="; tail -5 "$logtmp"; } >> "$FAIL_LOG"
      fi
    fi
    rm -f "$logtmp"
  done < /tmp/mock-only-pending.txt
  say "migration pass $pass: still_failing=$pass_failed"
  mv /tmp/mock-only-next.txt /tmp/mock-only-pending.txt
  [ "$pass_failed" -eq 0 ] && break
done
failed=$(grep -c '^=== ' "$FAIL_LOG" 2>/dev/null || true); failed=${failed:-0}
say "migrations: applied=$applied skipped=$skipped still_failed=$failed"
if [ "$failed" -gt 0 ]; then
  say "FAILED MIGRATIONS LEDGER (also copied to the run report):"
  cat "$FAIL_LOG"
fi

# ── 6. mock-only seed + non-mock scrub ──────────────────────────────
say "mock-only seed + scrub..."
psql -U "${POSTGRES_USER:-mocktest}" -d "${POSTGRES_DB:-llm_gateway_mock}" -v ON_ERROR_STOP=1 -q -f "$SEED_FILE" \
  || { say "FATAL: mock-only seed failed"; exit 1; }

say "done."
