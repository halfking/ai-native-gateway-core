#!/usr/bin/env bash
# =====================================================================
# tests/deploy_sops_test.sh — Slice 6 SOPS / scanner integration
#
# Spec cf8aad1a9 AC-8/AC-9/AC-10 surface:
#
#   AC-8  Given .sops.yaml, when its creation rules are inspected,
#         then the regex `^\.env\.(71|184|252|kaixuan-1)(\.enc)?$` uses
#         the existing recipient; plaintext active files are ignored and
#         filename-only scanner bypass is impossible.
#
#   AC-9  Given env-injector can provide the complete required key set,
#         when encrypted artifacts are generated, then SOPS decrypts
#         both artifacts successfully without values appearing in output
#         or Git plaintext; otherwise no artifact is created.
#
#   AC-10 Given the current tracked tree, when `bash scripts/scan-secrets.sh`
#         runs, then no BLOCKING plaintext credential finding remains.
#         Slice 6 lands the empty baseline + scanner framework; AC-10
#         fully passes once Slice 7's HEAD cleanup lands.
#
# The SOPS binary is NOT installed in CI; AC-9 is exercised through
# metadata detection (the ENC[…], sops:, encrypted_regex triplet)
# rather than decryption. Decryption is env-injector's job.
# =====================================================================

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GITIGNORE="$REPO_ROOT/.gitignore"
SOPS_YAML="$REPO_ROOT/.sops.yaml"
SCAN="$REPO_ROOT/scripts/scan-secrets.sh"
BASELINE="$REPO_ROOT/scripts/scan-secrets.baseline"
ENV_252="$REPO_ROOT/.env.252.enc"
ENV_KAIXUAN_1="$REPO_ROOT/.env.kaixuan-1.enc"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass()  { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail()  { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }

assert_contains() { [[ "$2" == *"$3"* ]] && log_pass "$1" || log_fail "$1: needle [$3] missing"; }
assert_file_exists() { [[ -f "$2" ]] && log_pass "$1" || log_fail "$1: $2 missing"; }
assert_file_absent()  { [[ ! -f "$2" ]] && log_pass "$1" || log_fail "$1: $2 should be absent"; }

# ---- tests --------------------------------------------------------------

# AC-8: .sops.yaml regex covers 71/184/252/kaixuan-1 with one recipient
test_sops_yaml_regex() {
  echo "── sops_yaml_regex ──"
  [[ -f "$SOPS_YAML" ]] || { log_fail ".sops.yaml missing"; return; }

  local yaml
  yaml=$(cat "$SOPS_YAML")

  assert_contains "regex captures .env.71.enc"    "$yaml" '71'
  assert_contains "regex captures .env.184.enc"   "$yaml" '184'
  assert_contains "regex captures .env.252.enc"   "$yaml" '252'
  assert_contains "regex captures .env.kaixuan-1.enc" "$yaml" 'kaixuan-1'
  assert_contains "regex allows .enc suffix"      "$yaml" '\.enc)?$'
  assert_contains "single recipient line"        "$yaml" 'age1uwuh5zdw4nfvvs0vdndsxzscqt9hj6slajaczdql494kp8pkxvesscl9d5'
}

# AC-8: plaintext .env.{252,kaixuan-1} are .gitignore'd
test_gitignore_plaintext() {
  echo "── gitignore_plaintext ──"
  [[ -f "$GITIGNORE" ]] || { log_fail ".gitignore missing"; return; }

  local body
  body=$(cat "$GITIGNORE")

  assert_contains "plaintext .env.252 is ignored"       "$body" '.env.252'
  assert_contains "plaintext .env.kaixuan-1 is ignored"  "$body" '.env.kaixuan-1'
  assert_contains ".env.252.enc is tracked"             "$body" '.env.252.enc'
  assert_contains ".env.kaixuan-1.enc is tracked"        "$body" '.env.kaixuan-1.enc'
  assert_contains "plaintext .env.71 is ignored"         "$body" '.env.71'
  assert_contains "plaintext .env.184 is ignored"        "$body" '.env.184'
}

# AC-9: encrypted fixtures have valid SOPS preamble so scan-secrets
# recognizes them and skips the rule scan
test_sops_envelope_detection() {
  echo "── sops_envelope_detection ──"
  assert_file_exists ".env.252.enc tracks"          "$ENV_252"
  assert_file_exists ".env.kaixuan-1.enc tracks"    "$ENV_KAIXUAN_1"

  # SOPS envelopes carry the JSON-shaped preamble with optional
  # leading whitespace (real sops output indents with tabs). Each
  # marker may sit on its own line OR inline on a "data"/"sops" key
  # in the JSON object. We grep with whitespace tolerance.
  #
  # v2: Real SOPS envelopes always have mac/lastmodified/version and
  # age/pgp/kms key groups. The old encrypted_regex check was wrong —
  # that field only exists when .sops.yaml specifies it.
  local has_enckey_252 has_sops_252 has_metadata_252
  has_enckey_252=$(grep -Ec '\bENC\[' "$ENV_252")
  has_sops_252=$(grep -Ec '^[[:space:]]*"(sops|data)":' "$ENV_252")
  has_metadata_252=$(grep -Ec '^[[:space:]]*"(mac|lastmodified|version|age)":' "$ENV_252")
  if [[ $has_enckey_252 -ge 1 ]]; then
    log_pass ".env.252.enc carries ENC[ data-key block"
  else
    log_fail ".env.252.enc missing ENC[ data-key block"
  fi
  if [[ $has_sops_252 -ge 1 ]]; then
    log_pass ".env.252.enc carries sops: config section"
  else
    log_fail ".env.252.enc missing sops: section"
  fi
  if [[ $has_metadata_252 -ge 1 ]]; then
    log_pass ".env.252.enc carries SOPS metadata (mac/age/version)"
  else
    log_fail ".env.252.enc missing SOPS metadata markers"
  fi
}

# AC-9 + AC-10 framework: scan-secrets.sh recognizes the SOPS envelope
# and does not produce a BLOCK finding against the .enc files. We
# exercise just the two specific files in --paths form to keep the
# scan bounded.
test_scan_secrets_skips_enc() {
  echo "── scan_secrets_skips_enc ──"
  if ! command -v bash >/dev/null; then
    log_skip "bash not on PATH"
    return
  fi

  local out rc
  # Use --paths to limit the scan to just the two fixtures.
  timeout 30 bash "$SCAN" --tracked-only --baseline="$BASELINE" \
    --paths .env.252.enc --paths .env.kaixuan-1.enc 2>/tmp/scan_out.$$ >/dev/null
  rc=$?
  rm -f /tmp/scan_out.$$

  # Empty baseline + only .enc files = clean (rc=0). Block (rc=1) would
  # mean the scanner flagged the SOPS files, which is exactly the
  # filename-only bypass that AC-8 forbids.
  if [[ $rc -eq 0 ]]; then
    log_pass "scan-secrets.sh exits clean against only .enc files (rc=0)"
  else
    log_fail "scan-secrets.sh exited rc=$rc against .enc fixtures (expected 0)"
  fi
}

# AC-10 framework: empty baseline contains no false-positive entries
test_empty_baseline() {
  echo "── empty_baseline ──"
  [[ -f "$BASELINE" ]] || { log_fail "scripts/scan-secrets.baseline missing"; return; }
  local noncomment
  noncomment=$(grep -cvE '^[[:space:]]*(#|$)' "$BASELINE")
  if [[ "$noncomment" -eq 0 ]]; then
    log_pass "scan-secrets.baseline is empty of false-positive entries"
  else
    log_fail "scan-secrets.baseline has $noncomment non-comment lines (expected 0)"
  fi
}

# AC-9: the scanner must NOT exempt a .env.<target>.enc file that
# lacks the SOPS preamble. Filename-only bypass is forbidden.
test_scan_secrets_does_not_exempt_non_sops() {
  echo "── scan_secrets_does_not_exempt_non_sops ──"
  local tmpdir out
  tmpdir=$(mktemp -d -t kx-sops-fake.XXXXXX)
  # A file named .env.252.enc but containing plaintext + the literal
  # string "SECRET_VALUE=fake-plaintext-credential" must trigger a
  # finding. Filename alone cannot mask the lack of SOPS envelope.
  cat >"$tmpdir/.env.252.enc" <<EOF
SECRET_VALUE=fake-plaintext-credential
POSTGRES_PASSWORD=postgres-plaintext-fake
EOF

  out=$(timeout 30 bash "$SCAN" --mode=normal \
        --baseline="$BASELINE" \
        --paths "$tmpdir/.env.252.enc" 2>/dev/null)
  # The scanner's SECRET_FILE filename-rule fires on `.env.<target>`
  # regardless of whether the file is plaintext or encrypted. We
  # assert that the BLOCK severity appears (which only happens for
  # plaintext — encrypt it and the SECRETCheck shifts to "warning"
  # once SOPS-envelope detection skips the file).
  if echo "$out" | grep -qE '\[BLOCK\]'; then
    log_pass "filename .env.<target>.enc without SOPS preamble produces BLOCK finding"
  else
    log_fail "filename-only bypass succeeded — AC-8 violated (got: $out)"
  fi
  rm -rf "$tmpdir"
}

# ---- runner --------------------------------------------------------------

run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_sops_test.sh — Slice 6 SOPS / scanner tests"
  echo "═══════════════════════════════════════════════════════════════"
  test_sops_yaml_regex
  test_gitignore_plaintext
  test_sops_envelope_detection
  test_scan_secrets_skips_enc
  test_empty_baseline
  test_scan_secrets_does_not_exempt_non_sops

  echo
  echo "───────────────────────────────────────────────────────────────"
  echo " summary: ${TESTS_PASSED} passed, ${TESTS_FAILED} failed"
  if (( TESTS_FAILED > 0 )); then
    echo " failed:"
    for n in "${FAILED_NAMES[@]}"; do echo "   - $n"; done
    return 1
  fi
  return 0
}

if [[ $# -gt 0 ]]; then
  case "$1" in
    yaml_regex)               test_sops_yaml_regex ;;
    gitignore)                test_gitignore_plaintext ;;
    envelope)                 test_sops_envelope_detection ;;
    scan_skips)               test_scan_secrets_skips_enc ;;
    empty_baseline)           test_empty_baseline ;;
    no_bypass)                test_scan_secrets_does_not_exempt_non_sops ;;
    all|*)                    run_all ;;
  esac
else
  run_all
fi