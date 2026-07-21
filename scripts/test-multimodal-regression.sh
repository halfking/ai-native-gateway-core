#!/usr/bin/env bash
# scripts/test-multimodal-regression.sh
#
# One-shot regression for multimodal capability verification (handoff
# doc 2026-07-20). Runs:
#   1. Go tests covering Layer A (request-body detection) and Layer C
#      (probe + admin endpoint contracts).
#   2. Phase 3 dry-run (no real-model spend).
#
# Live Phase 3 execution requires LLM_GATEWAY_API_KEY + running gateway.
# Set PHASE3_LIVE=1 to opt in.
#
# Exit code 0 = all green, non-zero = at least one failure.

set -uo pipefail

RED='\033[31m'; GRN='\033[32m'; YLW='\033[33m'; RST='\033[0m'
pass() { printf "${GRN}✓${RST} %s\n" "$*"; }
fail() { printf "${RED}✗${RST} %s\n" "$*"; FAILED=1; }
info() { printf "${YLW}→${RST} %s\n" "$*"; }

FAILED=0
cd "$(dirname "$0")/.."

info "Phase 2 — Go tests (multimodal-only: Layer A detect, Layer C probe+admin, modelname rules)"
# Scoped to -run patterns that cover the multimodal matrix. We deliberately
# exclude the bg credential_selfcheck_pick tests, which have a separate
# pre-existing failure unrelated to multimodal capability work.
if go test \
     -run 'TestDetectRequestModality|TestE2E_DetectModality_Supplement|TestResolveCandidatesForRequest|TestProbeModality|TestValidModalities|TestUpdateModelModality|TestInferModality' \
     ./domains/streaming/... ./bg/... ./admin/... ./modelname/... -count=1 2>&1 | tail -40; then
  pass "Phase 2 multimodal Go tests passed"
else
  fail "Phase 2 multimodal Go tests FAILED (see above)"
fi

info "Phase 3 dry-run (Phase 3 live execution requires LLM_GATEWAY_API_KEY)"
if bash scripts/multimodal-e2e/run_phase3.sh --dry-run >/tmp/regression-p3.log 2>&1; then
  pass "Phase 3 dry-run passed (see /tmp/regression-p3.log)"
else
  fail "Phase 3 dry-run FAILED"
fi

if [[ "${PHASE3_LIVE:-0}" == "1" ]]; then
  info "Phase 3 LIVE (LLM_GATEWAY_API_KEY set)"
  if [[ -z "${LLM_GATEWAY_API_KEY:-}" ]]; then
    fail "PHASE3_LIVE=1 but LLM_GATEWAY_API_KEY is empty"
  elif bash scripts/multimodal-e2e/run_phase3.sh 2>&1 | tee /tmp/regression-p3-live.log; then
    pass "Phase 3 LIVE passed"
  else
    fail "Phase 3 LIVE FAILED (see /tmp/regression-p3-live.log)"
  fi
fi

echo
if [[ $FAILED -eq 0 ]]; then
  printf "${GRN}═══ REGRESSION PASS ═══${RST}\n"
  exit 0
else
  printf "${RED}═══ REGRESSION FAIL ═══${RST}\n"
  exit 1
fi
