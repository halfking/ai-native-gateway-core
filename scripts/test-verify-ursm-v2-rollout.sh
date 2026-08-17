#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/shadow.prom" <<'METRICS'
llm_gateway_ursm_v2_shadow_records_total{result="recorded"} 42
llm_gateway_ursm_v2_shadow_records_total{result="skipped"} 0
llm_gateway_ursm_v2_shadow_records_total{result="failed"} 0
ursm_shadow_diff_total{type="identical"} 42
ursm_shadow_diff_total{type="availability"} 0
ursm_shadow_diff_total{type="order"} 0
ursm_shadow_diff_total{type="top1"} 0
ursm_shadow_diff_total{type="error"} 0
ursm_shadow_diff_total{type="not_ready"} 0
METRICS
bash "$SCRIPT_DIR/verify-ursm-v2-rollout.sh" --stage shadow --metrics-file "$tmp/shadow.prom" >/dev/null

cat >"$tmp/legacy-labels.prom" <<'METRICS'
llm_gateway_ursm_v2_shadow_records_total{result="recorded"} 42
llm_gateway_ursm_v2_shadow_records_total{result="skipped"} 0
llm_gateway_ursm_v2_shadow_records_total{result="failed"} 0
ursm_shadow_diff_total{type="identical"} 42
ursm_shadow_diff_total{type="availability_mismatch"} 0
ursm_shadow_diff_total{type="order_mismatch"} 0
ursm_shadow_diff_total{type="top1"} 0
ursm_shadow_diff_total{type="error"} 0
ursm_shadow_diff_total{type="not_ready"} 0
METRICS
if bash "$SCRIPT_DIR/verify-ursm-v2-rollout.sh" --stage shadow --metrics-file "$tmp/legacy-labels.prom" >/dev/null 2>&1; then
  printf '%s\n' 'legacy diff labels must not satisfy the shadow contract' >&2
  exit 1
fi

cat >"$tmp/missing-outcome.prom" <<'METRICS'
llm_gateway_ursm_v2_shadow_records_total{result="recorded"} 42
ursm_shadow_diff_total{type="identical"} 42
ursm_shadow_diff_total{type="availability"} 0
ursm_shadow_diff_total{type="order"} 0
ursm_shadow_diff_total{type="top1"} 0
ursm_shadow_diff_total{type="error"} 0
ursm_shadow_diff_total{type="not_ready"} 0
METRICS
if bash "$SCRIPT_DIR/verify-ursm-v2-rollout.sh" --stage shadow --metrics-file "$tmp/missing-outcome.prom" >/dev/null 2>&1; then
  printf '%s\n' 'missing outcome evidence must fail shadow validation' >&2
  exit 1
fi

cat >"$tmp/canary.prom" <<'METRICS'
routing_state_source_total{source="canary"} 10
routing_state_source_total{source="fallback"} 0
llm_gateway_ursm_v2_shadow_records_total{result="recorded"} 10
llm_gateway_ursm_v2_shadow_records_total{result="failed"} 0
METRICS
bash "$SCRIPT_DIR/verify-ursm-v2-rollout.sh" --stage canary --metrics-file "$tmp/canary.prom" >/dev/null

cat >"$tmp/canary-fallback.prom" <<'METRICS'
routing_state_source_total{source="canary"} 10
routing_state_source_total{source="fallback"} 2
llm_gateway_ursm_v2_shadow_records_total{result="recorded"} 10
llm_gateway_ursm_v2_shadow_records_total{result="failed"} 0
METRICS
if bash "$SCRIPT_DIR/verify-ursm-v2-rollout.sh" --stage canary --metrics-file "$tmp/canary-fallback.prom" >/dev/null 2>&1; then
  printf '%s\n' 'nonzero canary fallback must fail validation' >&2
  exit 1
fi

cat >"$tmp/shadow-failed.prom" <<'METRICS'
llm_gateway_ursm_v2_shadow_records_total{result="recorded"} 42
llm_gateway_ursm_v2_shadow_records_total{result="skipped"} 0
llm_gateway_ursm_v2_shadow_records_total{result="failed"} 1
ursm_shadow_diff_total{type="identical"} 42
ursm_shadow_diff_total{type="availability"} 0
ursm_shadow_diff_total{type="order"} 0
ursm_shadow_diff_total{type="top1"} 0
ursm_shadow_diff_total{type="error"} 0
ursm_shadow_diff_total{type="not_ready"} 0
METRICS
if bash "$SCRIPT_DIR/verify-ursm-v2-rollout.sh" --stage shadow --metrics-file "$tmp/shadow-failed.prom" >/dev/null 2>&1; then
  printf '%s\n' 'nonzero shadow sidecar failure must fail validation' >&2
  exit 1
fi

printf '%s\n' 'verify-ursm-v2-rollout self-test passed.'
