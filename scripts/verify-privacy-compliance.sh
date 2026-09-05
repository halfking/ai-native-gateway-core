#!/usr/bin/env bash
# verify-privacy-compliance.sh — privacy compliance test gate for AUTO route
#
# This script runs privacy-specific tests to ensure no sensitive content
# (prompt text, message content, or reversible features) leaks into:
#   - Database storage (auto_route_selections table)
#   - Logs (ClassificationSignals String() / MarshalJSON())
#   - Telemetry (structured features extraction)
#
# Usage:
#   ./scripts/verify-privacy-compliance.sh              # run all privacy tests
#   ./scripts/verify-privacy-compliance.sh --verbose    # verbose output
#
# Exit codes:
#   0 = all privacy tests passed
#   1 = one or more privacy tests failed
#   2 = usage error

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VERBOSE=false
for arg in "$@"; do
  case "$arg" in
    --verbose|-v) VERBOSE=true ;;
    -h|--help)
      sed -n '2,14p' "$0"
      exit 0
      ;;
    *)
      echo "unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

echo "[privacy-compliance] Running AUTO route privacy tests..."

FAILED=0

# Test 1: Structured features do not leak content
echo "[privacy-compliance] Test 1/3: Structured features content leakage protection"
if [[ "$VERBOSE" == true ]]; then
  go test ./autoroute -run TestStructuredFeaturesNoContentLeakage -v
else
  go test ./autoroute -run TestStructuredFeaturesNoContentLeakage
fi
if [[ $? -ne 0 ]]; then
  echo "[privacy-compliance] FAIL: Structured features leaked content" >&2
  FAILED=$((FAILED + 1))
fi

# Test 2: Content hash is non-reversible
echo "[privacy-compliance] Test 2/3: Content hash non-reversibility"
if [[ "$VERBOSE" == true ]]; then
  go test ./autoroute -run TestContentHashNonReversibility -v
else
  go test ./autoroute -run TestContentHashNonReversibility
fi
if [[ $? -ne 0 ]]; then
  echo "[privacy-compliance] FAIL: Content hash reversibility vulnerability" >&2
  FAILED=$((FAILED + 1))
fi

# Test 3: ClassificationSignals sanitization
echo "[privacy-compliance] Test 3/3: ClassificationSignals log sanitization"
if [[ "$VERBOSE" == true ]]; then
  go test ./autoroute -run TestClassificationSignals -v
else
  go test ./autoroute -run TestClassificationSignals
fi
if [[ $? -ne 0 ]]; then
  echo "[privacy-compliance] FAIL: ClassificationSignals leaked content in logs" >&2
  FAILED=$((FAILED + 1))
fi

# Test 4: Audit log sanitization
echo "[privacy-compliance] Test 4/4: Audit log sanitization"
if [[ "$VERBOSE" == true ]]; then
  go test ./autoroute -run TestSanitizeForAudit -v
else
  go test ./autoroute -run TestSanitizeForAudit
fi
if [[ $? -ne 0 ]]; then
  echo "[privacy-compliance] FAIL: Audit log sanitization failed" >&2
  FAILED=$((FAILED + 1))
fi

if [[ $FAILED -gt 0 ]]; then
  echo "[privacy-compliance] FAIL: $FAILED test(s) failed" >&2
  exit 1
fi

echo "[privacy-compliance] PASS: All privacy tests passed ✅"
echo ""
echo "Privacy guarantee verified:"
echo "  ✅ auto_route_selections stores NO prompt/message content"
echo "  ✅ Structured features are non-reversible (enums, buckets, booleans, hashes)"
echo "  ✅ ClassificationSignals safe for logging (String/JSON sanitized)"
echo "  ✅ Audit logs do not contain sensitive content"
