#!/usr/bin/env bash
# scripts/integrity_smoke_test.sh
#
# 2026-07-28: minimal smoke check for the model integrity surface
# (fingerprint_drift baseline + integrity probe planner). It runs only
# against a developer PostgreSQL with the apply-missing-migrations helper
# so it is safe to run on a developer's local 252 PG.
#
# Required env: PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE.
# Optional:   LLM_GATEWAY_INTEGRITY_PROBE_INTERVAL (default 5s) and
#             LLM_GATEWAY_INTEGRITY_FP_DRIFT_INTERVAL (default 5s)
#             let the test verify the rows faster.

set -euo pipefail

: "${PGHOST:?PGHOST required}"
: "${PGPORT:=5432}"
: "${PGUSER:?PGUSER required}"
: "${PGPASSWORD:?PGPASSWORD required}"
: "${PGDATABASE:?PGDATABASE required}"

PSQL="psql -h $PGHOST -p $PGPORT -U $PGUSER -d $PGDATABASE -t -A -v ON_ERROR_STOP=1"

echo "[smoke] schema ensure..."
$PSQL -c "SELECT 1 FROM model_integrity_events LIMIT 0" >/dev/null
$PSQL -c "SELECT 1 FROM integrity_fingerprint_baseline LIMIT 0" >/dev/null
$PSQL -c "SELECT 1 FROM credential_probe_queue LIMIT 0" >/dev/null

echo "[smoke] seed a synthetic integrity event..."
$PSQL <<'SQL'
INSERT INTO model_integrity_events (
    ts, request_id, tenant_id, application_id, api_key_id,
    provider_id, provider_code, credential_id,
    client_model, outbound_model, raw_model_name,
    anomaly_type, severity, expected_value, actual_value, sample, context
) VALUES (
    now(), 'smoke-1', 'default', NULL, NULL,
    NULL, 'smoke-provider', 1,
    'gpt-5.6-luna', 'gpt-5.6-luna', 'gpt-5.6-luna',
    'model_mismatch', 'high', 'gpt-5.6-luna', 'gpt-5.4-mini', 'smoke',
    jsonb_build_object('source', 'smoke_test')
) ON CONFLICT DO NOTHING;
SQL

echo "[smoke] planner should have enqueued a deduped task..."
# Wait briefly for the planner cycle to run.
sleep 6
COUNT=$($PSQL -c "SELECT COUNT(*) FROM credential_probe_queue WHERE dedup_key = 'integrity:1:gpt-5.6-luna' AND source = 'integrity_probe_planner'")
if [[ "$COUNT" -lt 1 ]]; then
  echo "[smoke] FAIL: planner did not enqueue an integrity probe (count=$COUNT)"
  exit 1
fi
echo "[smoke] OK: planner enqueued $COUNT integrity probe task(s)"

echo "[smoke] harvester should have bridged the high event to fault_events..."
COUNT=$($PSQL -c "SELECT COUNT(*) FROM fault_events WHERE rule_id = 'integrity-high:model_mismatch' AND status = 'open'")
if [[ "$COUNT" -lt 1 ]]; then
  echo "[smoke] FAIL: harvester did not bridge the high event (count=$COUNT)"
  exit 1
fi
echo "[smoke] OK: harvester bridged $COUNT fault_event(s)"

echo "[smoke] cleanup synthetic state..."
$PSQL -c "DELETE FROM credential_probe_queue WHERE source = 'integrity_probe_planner' AND dedup_key LIKE 'integrity:%'"
$PSQL -c "DELETE FROM fault_events WHERE rule_id LIKE 'integrity:%' AND created_at > now() - interval '10 minutes'"
$PSQL -c "DELETE FROM model_integrity_events WHERE request_id = 'smoke-1'"

echo "[smoke] PASS"
