#!/usr/bin/env bash
# Offline regression checks for credential-bearing operational scripts.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MONITOR="$REPO_ROOT/scripts/monitor-245-checkpoint.sh"
MIGRATE="$REPO_ROOT/scripts/migrate-session-dim-154.sh"
LOCAL_TEST="$REPO_ROOT/scripts/local-deploy-test.sh"
DETAIL_API="$REPO_ROOT/scripts/test-request-detail-apis.sh"

passed=0
failed=0
pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; passed=$((passed + 1)); }
fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; failed=$((failed + 1)); }

assert_contains() {
  local label=$1 file=$2 needle=$3
  grep -Fq -- "$needle" "$file" && pass "$label" || fail "$label"
}

assert_not_contains() {
  local label=$1 file=$2 needle=$3
  grep -Fq -- "$needle" "$file" && fail "$label" || pass "$label"
}

test_monitor_contract() {
  echo "── monitor_245_contract ──"
  assert_contains "monitor enables fail-closed shell flags" "$MONITOR" 'set -euo pipefail'
  assert_contains "monitor requires dedicated DB password" "$MONITOR" 'LLM_GATEWAY_245_DB_PASSWORD must be set'
  assert_contains "monitor sends password and query through stdin" "$MONITOR" "printf '%s\\n%s\\n' \"\$LLM_GATEWAY_245_DB_PASSWORD\" \"\$query\""
  assert_contains "monitor reads password remotely" "$MONITOR" 'IFS= read -r PGPASSWORD'
  assert_contains "monitor uses ON_ERROR_STOP" "$MONITOR" 'psql -X -v ON_ERROR_STOP=1'
  assert_not_contains "monitor has no sshpass" "$MONITOR" 'sshpass'
}

test_migration_contract() {
  echo "── migration_154_contract ──"
  assert_contains "migration enables fail-closed shell flags" "$MIGRATE" 'set -euo pipefail'
  assert_contains "migration requires explicit confirmation" "$MIGRATE" 'LLM_GATEWAY_MIGRATION_CONFIRM_154=apply'
  assert_contains "migration supports dry-run" "$MIGRATE" '--dry-run'
  assert_contains "migration uses batch SSH" "$MIGRATE" 'BatchMode=yes'
  assert_contains "migration passes DB password by stdin" "$MIGRATE" "printf '%s\\n' \"\$LLM_GATEWAY_154_DB_PASSWORD\""
  assert_not_contains "migration has no sshpass" "$MIGRATE" 'sshpass'

  local out rc
  if out=$(bash "$MIGRATE" --dry-run 2>&1); then
    rc=0
  else
    rc=$?
  fi
  if [[ $rc -eq 0 && "$out" == *'[DRY-RUN]'* ]]; then
    pass "migration dry-run needs no remote credential or connection"
  else
    fail "migration dry-run must succeed offline"
  fi

  if out=$(env -u LLM_GATEWAY_154_DB_PASSWORD -u LLM_GATEWAY_MIGRATION_CONFIRM_154 bash "$MIGRATE" 2>&1); then
    rc=0
  else
    rc=$?
  fi
  if [[ $rc -ne 0 && "$out" == *'LLM_GATEWAY_154_DB_PASSWORD must be set'* ]]; then
    pass "migration refuses apply without DB password"
  else
    fail "migration must fail closed without DB password"
  fi
}

test_local_test_contract() {
  echo "── local_deploy_test_contract ──"
  assert_contains "local test requires explicit insecure-default opt-in" "$LOCAL_TEST" 'LLM_GATEWAY_LOCAL_TEST_ALLOW_INSECURE_DEFAULTS'
  assert_contains "local test validates before full execution" "$LOCAL_TEST" 'full)'
  assert_contains "local test validates configuration" "$LOCAL_TEST" 'require_local_test_config'
  assert_contains "Docker path uses a temporary Compose file" "$LOCAL_TEST" 'llm-gateway-local-test-compose.'
  assert_contains "Docker env file is owner-readable only" "$LOCAL_TEST" 'chmod 0600 "$TEST_ENV_FILE" "$TEST_COMPOSE_FILE"'
  assert_contains "Docker path exports validated PostgreSQL values" "$LOCAL_TEST" 'export POSTGRES_PASSWORD="$LLM_GATEWAY_LOCAL_TEST_PG_PASSWORD"'
  assert_contains "Docker temporary files are removed on exit" "$LOCAL_TEST" 'rm -f "$TEST_COMPOSE_FILE"'
  assert_not_contains "local test does not print password-bearing PG command" "$LOCAL_TEST" 'PGPASSWORD=kxpass docker exec'
}

test_request_detail_contract() {
  echo "── request_detail_contract ──"
  assert_contains "detail test defaults to localhost" "$DETAIL_API" 'http://127.0.0.1:8781'
  assert_contains "detail test requires remote opt-in" "$DETAIL_API" 'LLM_GATEWAY_REQUEST_DETAIL_ALLOW_REMOTE=1'
  assert_contains "detail test accepts existing token" "$DETAIL_API" 'TOKEN="${TOKEN:-}"'
  assert_contains "detail test builds login JSON with jq" "$DETAIL_API" 'jq -n --arg username'
  assert_contains "detail test uses curl timeouts" "$DETAIL_API" '--connect-timeout 5 --max-time 30'
  production_url="https://llm.kxpms$(printf '.cn')"
  assert_not_contains "detail test has no hard-coded production URL" "$DETAIL_API" "$production_url"
  assert_not_contains "detail test has no default password" "$DETAIL_API" '__REDACTED_SSH_PASSWORD__'
  assert_not_contains "detail test does not print token prefix" "$DETAIL_API" 'token: ${TOKEN:0:20}'
}

echo "═══════════════════════════════════════════════════════════════"
echo " credential_script_contract_test.sh"
echo "═══════════════════════════════════════════════════════════════"
test_monitor_contract
test_migration_contract
test_local_test_contract
test_request_detail_contract

echo
echo "summary: $passed passed, $failed failed"
(( failed == 0 ))
