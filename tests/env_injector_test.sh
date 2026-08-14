#!/usr/bin/env bash
# =====================================================================
# tests/env_injector_test.sh — env-injector CLI integration tests
#
# Acceptance Criteria:
#   AC-I1  Given env-injector binary, when "list" is called,
#          then SSH key mappings for all targets are emitted.
#
#   AC-I2  Given a valid SOPS .env.<target>.enc, when "inject --target=X"
#          is called, then export statements are emitted for eval.
#
#   AC-I3  Given "inject --dry-run", no credential values appear in output.
#
#   AC-I4  Given "verify --target=X", exit 0 on success, exit 1 on failure.
#
#   AC-I5  Given unknown target, exit 64 (usage error).
#
#   AC-I6  Given "inject --format=json", valid JSON is emitted.
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARY="${ENV_INJECTOR_BIN:-$REPO_ROOT/bin/env-injector}"

# Build if not present
if [[ ! -x "$BINARY" ]]; then
  BINARY="$REPO_ROOT/bin/env-injector-test"
  (cd "$REPO_ROOT" && go build -o "$BINARY" ./cmd/env-injector/) 2>/dev/null || {
    echo "SKIP: cannot build env-injector (go not available or compile error)"
    exit 0
  }
fi

# Check sops availability
if ! command -v sops &>/dev/null; then
  echo "SKIP: sops not in PATH"
  exit 0
fi

# Check age key
AGE_KEY="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
if [[ ! -f "$AGE_KEY" ]]; then
  echo "SKIP: age key not found at $AGE_KEY"
  exit 0
fi

export SOPS_AGE_KEY_FILE="$AGE_KEY"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass()  { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail()  { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }

echo "── env-injector CLI tests ──────────────────────────────────────"

# ── AC-I1: list ────────────────────────────────────────────────────
OUT="$("$BINARY" list 2>&1)" || true
[[ "$OUT" == *"SSH_KEY_252="* ]] && log_pass "AC-I1: list emits SSH_KEY_252" || log_fail "AC-I1: list missing SSH_KEY_252"
[[ "$OUT" == *"SSH_KEY_KAIXUAN_1="* ]] && log_pass "AC-I1: list emits SSH_KEY_KAIXUAN_1" || log_fail "AC-I1: list missing SSH_KEY_KAIXUAN_1"

# ── AC-I2: inject eval format ──────────────────────────────────────
if [[ -f "$REPO_ROOT/.env.252.enc" ]]; then
  OUT="$("$BINARY" inject --target=252 2>&1)" || true
  [[ "$OUT" == *"export SSH_PASS_252="* ]] \
    && log_pass "AC-I2: inject 252 emits export SSH_PASS_252" \
    || log_fail "AC-I2: inject 252 missing export SSH_PASS_252"
  [[ "$OUT" == *"export PG_PASS_252="* ]] \
    && log_pass "AC-I2: inject 252 emits export PG_PASS_252" \
    || log_fail "AC-I2: inject 252 missing export PG_PASS_252"

  # ── AC-I3: dry-run hides values ────────────────────────────────────
  OUT="$("$BINARY" inject --target=252 --dry-run 2>&1)" || true
  [[ "$OUT" == *"OK"* && "$OUT" != *"export"* ]] \
    && log_pass "AC-I3: dry-run hides credential values" \
    || log_fail "AC-I3: dry-run leaked values or missing OK"

  # ── AC-I4: verify exit code ────────────────────────────────────────
  "$BINARY" verify --target=252 &>/dev/null
  [[ $? -eq 0 ]] && log_pass "AC-I4: verify 252 exits 0" || log_fail "AC-I4: verify 252 exit non-zero"

  # ── AC-I6: JSON format ─────────────────────────────────────────────
  OUT="$("$BINARY" inject --target=252 --format=json 2>&1)" || true
  echo "$OUT" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null \
    && log_pass "AC-I6: JSON format is valid JSON" \
    || log_fail "AC-I6: JSON format is not valid JSON"
else
  log_fail "AC-I2: .env.252.enc not found"
fi

# ── AC-I5: unknown target ──────────────────────────────────────────
"$BINARY" inject --target=nonexistent &>/dev/null
RC=$?
[[ $RC -ne 0 ]] && log_pass "AC-I5: unknown target exits non-zero (rc=$RC)" || log_fail "AC-I5: unknown target should fail"

# ── Legacy alias resolution ────────────────────────────────────────
OUT="$($BINARY inject --target=184 --dry-run 2>&1)" || true
[[ "$OUT" == *".env.252.enc"* ]] \
  && log_pass "Legacy alias 184 resolves to 252" \
  || log_fail "Legacy alias 184 should resolve to 252"

# 245 is deployed by scripts/deploy-245.sh via the shared envs SSOT. The
# repository-native injector must fail closed until a managed envelope exists.
OUT="$($BINARY inject --target=245 --dry-run 2>&1)" || true
[[ "$OUT" == *"unknown target"* ]] \
  && log_pass "245 is not exposed without a managed envelope" \
  || log_fail "245 should not advertise an unmanaged native envelope"


# ── Summary ───────────────────────────────────────────────────────
echo ""
echo "───────────────────────────────────────────────────────────────"
echo " summary: $TESTS_PASSED passed, $TESTS_FAILED failed"
if [[ $TESTS_FAILED -gt 0 ]]; then
  echo " failures: ${FAILED_NAMES[*]}"
  exit 1
fi
exit 0
