#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016
# Regression coverage for the managed 252 database tunnel contract.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELPER="$REPO_ROOT/scripts/lib/252-db-tunnel.sh"
CONFIG="$REPO_ROOT/configs/env-252.sh"
SYNC_WRAPPER="$REPO_ROOT/scripts/local-host-sync-db.sh"
STRUCTURE_AUDIT="$REPO_ROOT/scripts/local-dev/verify-db-consistency.sh"
DATA_AUDIT="$REPO_ROOT/scripts/local-dev/verify-db-data-consistency.sh"
TABLE_COPY="$REPO_ROOT/scripts/pg-table-copy.sh"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()
log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED + 1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED + 1)); FAILED_NAMES+=("$1"); }

run_helper_case() {
  local scenario="$1" body="$2" tmp out rc
  tmp=$(mktemp -d -t db252-tunnel-test.XXXXXX)
  mkdir -p "$tmp/bin"
  cat > "$tmp/bin/ssh" <<'MOCK'
#!/usr/bin/env bash
if [[ "$*" == *"docker inspect"* ]]; then
  printf '%s\n' "${MOCK_CONTAINER_IP:-10.88.0.79}"
  exit 0
fi
: > "$MOCK_STATE/forwarded"
MOCK
  cat > "$tmp/bin/lsof" <<'MOCK'
#!/usr/bin/env bash
if [[ -f "$MOCK_STATE/occupied" || -f "$MOCK_STATE/forwarded" ]]; then printf '%s\n' "4242"; fi
MOCK
  cat > "$tmp/bin/psql" <<'MOCK'
#!/usr/bin/env bash
count_file="$MOCK_STATE/psql-count"
count=0
[[ -f "$count_file" ]] && count=$(<"$count_file")
count=$((count + 1)); printf '%s' "$count" > "$count_file"
case "${MOCK_PSQL_MODE:-healthy}" in
  healthy) exit 0 ;;
  unavailable) exit 1 ;;
  open-after-forward) [[ -f "$MOCK_STATE/forwarded" ]] && exit 0 || exit 1 ;;
esac
MOCK
  chmod +x "$tmp/bin"/*
  out=$(PATH="$tmp/bin:$PATH" PG_PSQL_BIN="$tmp/bin/psql" MOCK_STATE="$tmp" MOCK_PSQL_MODE="$scenario" MOCK_CONTAINER_IP="${MOCK_CONTAINER_IP:-10.88.0.79}" COMMON_PG_SUPERUSER_PASS=test-password \
    bash -c "source '$CONFIG'; source '$HELPER'; $body" 2>&1)
  rc=$?
  printf '%s\n' "$out" > "$tmp/output"
  printf '%s\n' "$rc" > "$tmp/rc"
  printf '%s\n' "$tmp"
}

test_invalid_container_ip_fails_before_forward() {
  local tmp rc
  tmp=$(MOCK_CONTAINER_IP=not-an-ip run_helper_case unavailable 'db252_tunnel_ensure')
  rc=$(<"$tmp/rc")
  [[ "$rc" -ne 0 && ! -f "$tmp/forwarded" ]] \
    && log_pass "invalid container address fails before forwarding" \
    || log_fail "invalid container address should not open a tunnel"
  rm -rf "$tmp"
}

test_existing_healthy_tunnel_is_reused() {
  local tmp rc
  tmp=$(run_helper_case healthy 'touch "$MOCK_STATE/occupied"; db252_tunnel_ensure; [[ "$DB252_TUNNEL_CREATED" == false ]]')
  rc=$(<"$tmp/rc")
  [[ "$rc" -eq 0 && ! -f "$tmp/forwarded" ]] \
    && log_pass "existing healthy tunnel is reused without SSH" \
    || log_fail "healthy listener should be reused"
  rm -rf "$tmp"
}

test_unhealthy_listener_is_never_replaced() {
  local tmp rc
  tmp=$(run_helper_case unavailable 'touch "$MOCK_STATE/occupied"; db252_tunnel_ensure')
  rc=$(<"$tmp/rc")
  [[ "$rc" -ne 0 && ! -f "$tmp/forwarded" ]] \
    && log_pass "unhealthy existing listener is not replaced" \
    || log_fail "occupied unhealthy port must fail closed"
  rm -rf "$tmp"
}

test_created_tunnel_is_owned_and_cleaned_up() {
  local tmp rc
  tmp=$(run_helper_case open-after-forward '
    db252_tunnel_ensure
    [[ "$DB252_TUNNEL_CREATED" == true && "$DB252_TUNNEL_PID" == 4242 ]]
    kill() { printf "%s\\n" "$*" >> "$MOCK_STATE/kills"; return 0; }
    db252_tunnel_teardown
    grep -qx "4242" "$MOCK_STATE/kills"
  ')
  rc=$(<"$tmp/rc")
  [[ "$rc" -eq 0 ]] \
    && log_pass "helper tracks and tears down only its own listener PID" \
    || log_fail "created tunnel ownership/cleanup contract failed"
  rm -rf "$tmp"
}

test_config_uses_common_password_alias() {
  local out rc
  out=$(COMMON_PG_SUPERUSER_PASS=from-common bash -c "source '$CONFIG'; printf '%s' \"\$PG_PASS\"" 2>&1)
  rc=$?
  [[ "$rc" -eq 0 && "$out" == "from-common" ]] \
    && log_pass "env-252 aliases the common password in memory" \
    || log_fail "env-252 should load the common-password alias"
}

test_config_uses_native_clients_for_host_tunnel() {
  local out rc
  out=$(PG_PSQL_BIN=/usr/bin/psql PG_DUMP_BIN=/usr/bin/pg_dump COMMON_PG_SUPERUSER_PASS=from-common bash -c "source '$CONFIG'; printf '%s|%s' \"\$PG_PSQL_BIN\" \"\$PG_DUMP_BIN\"" 2>&1)
  rc=$?
  [[ "$rc" -eq 0 && "$out" == "/usr/bin/psql|/usr/bin/pg_dump" ]] \
    && log_pass "env-252 accepts explicit native host PostgreSQL clients" \
    || log_fail "env-252 must support PG_PSQL_BIN and PG_DUMP_BIN overrides"
}

test_active_paths_have_no_stale_target_or_password_fallback() {
  local files=("$CONFIG" "$SYNC_WRAPPER" "$TABLE_COPY" "$STRUCTURE_AUDIT" "$DATA_AUDIT")
  if grep -nE '172\.16\.2\.210:5432|4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg' "${files[@]}" >/dev/null 2>&1; then
    log_fail "active sync paths retain stale target, embedded password, or unsafe import mode"
  else
    log_pass "active sync paths contain no stale target/password fallback"
  fi
}

test_table_copy_uses_configured_native_clients() {
  if grep -q 'SRC_PSQL_BIN="${PG_PSQL_BIN:-psql}"' "$TABLE_COPY" \
    && grep -q 'SRC_PG_DUMP_BIN="${PG_DUMP_BIN:-pg_dump}"' "$TABLE_COPY" \
    && grep -q '"$SRC_PG_DUMP_BIN"' "$TABLE_COPY"; then
    log_pass "table copy uses configured native source clients"
  else
    log_fail "table copy must use configured native source clients"
  fi
}

test_wrapper_runs_both_mandatory_audits() {
  if grep -q 'verify-db-consistency.sh --verify' "$SYNC_WRAPPER" \
    && grep -q 'verify-db-data-consistency.sh' "$SYNC_WRAPPER"; then
    log_pass "sync wrapper runs structure and data audits"
  else
    log_fail "sync wrapper must run both mandatory audits"
  fi
}

test_p1_scripts_use_managed_tunnel() {
  local files=(
    "$REPO_ROOT/scripts/partition/bodies-hot-p1-execute.sh"
    "$REPO_ROOT/scripts/partition/bodies-hot-repack.sh"
    "$REPO_ROOT/scripts/partition/bodies-hot-diagnose.sh"
    "$REPO_ROOT/scripts/partition/bodies-hot-repack-rollback.sh"
    "$REPO_ROOT/scripts/partition/index-drift-align.sh"
  )
  local f missing_tunnel stale
  missing_tunnel=0
  for f in "${files[@]}"; do
    grep -q 'scripts/lib/252-db-tunnel.sh' "$f" || missing_tunnel=1
  done
  # NOTE: ERE dot must be escaped as '\.', never '\\' — in single quotes '\\.'
  # matches a literal backslash, so the stale-endpoint guard would never fire.
  if [[ "$missing_tunnel" -eq 0 ]] \
    && ! grep -nE '172\.16\.2\.210|COMMON_PG_SUPERUSER[^_]|(^|[[:space:]])psql([[:space:]]|$).*15432' "${files[@]}" >/dev/null 2>&1; then
    log_pass "P1 partition scripts use managed tunnel without stale endpoint"
  else
    log_fail "P1 partition scripts must use managed tunnel and native clients"
  fi
}

test_diagnose_is_read_only() {
  if ! grep -q 'SELECT 1 FROM pg_extension' "$REPO_ROOT/scripts/partition/bodies-hot-diagnose.sh" \
    || grep -q 'CREATE EXTENSION' "$REPO_ROOT/scripts/partition/bodies-hot-diagnose.sh"; then
    log_fail "bodies-hot diagnosis must not install extensions"
  else
    log_pass "bodies-hot diagnosis remains read-only"
  fi
}

test_routing_fixup_matches_audit_contract() {
  local fixup="$REPO_ROOT/scripts/local-dev/apply-routing-mv-fixup.sh"
  if grep -q 'auto_request_count' "$fixup" \
    && grep -q 'specified_request_count' "$fixup" \
    && grep -q "client_model IS NOT NULL" "$fixup" \
    && grep -q '^BEGIN;' "$fixup" \
    && grep -q '^COMMIT;' "$fixup"; then
    log_pass "routing MV fixup includes canonical audit contract and transaction"
  else
    log_fail "routing MV fixup drifted from migration 632 contract"
  fi
}

test_sync_control_guards_present() {
  if grep -q 'SCHEMA_ONLY=true' "$SYNC_WRAPPER" \
    && grep -q -- '--backup-only cannot be combined' "$SYNC_WRAPPER" \
    && grep -q 'if \$BACKUP_ONLY' "$SYNC_WRAPPER" \
    && grep -q -- '--schema-only and --data-only cannot be used together' "$TABLE_COPY"; then
    log_pass "sync backup-only and mode exclusivity guards are present"
  else
    log_fail "sync control-flow guards are missing"
  fi
}

echo "── managed 252 database tunnel tests ──────────────────────────"
test_invalid_container_ip_fails_before_forward
test_existing_healthy_tunnel_is_reused
test_unhealthy_listener_is_never_replaced
test_created_tunnel_is_owned_and_cleaned_up
test_config_uses_common_password_alias
test_config_uses_native_clients_for_host_tunnel
test_active_paths_have_no_stale_target_or_password_fallback
test_table_copy_uses_configured_native_clients
test_wrapper_runs_both_mandatory_audits
test_p1_scripts_use_managed_tunnel
test_diagnose_is_read_only
test_routing_fixup_matches_audit_contract
test_sync_control_guards_present

echo "────────────────────────────────────────────────────────────────"
echo "summary: $TESTS_PASSED passed, $TESTS_FAILED failed"
if [[ "$TESTS_FAILED" -ne 0 ]]; then
  printf 'failures: %s\n' "${FAILED_NAMES[*]}"
  exit 1
fi
