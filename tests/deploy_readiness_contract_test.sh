#!/usr/bin/env bash
# Deployment contracts that must hold before a release can receive traffic.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="$ROOT/deploy/k8s/llm-gateway-go-deployment.yaml"
SEAMLESS="$ROOT/scripts/deploy-seamless.sh"
HOST_LIB="$ROOT/scripts/deploy-lib/host.sh"
TARGETS="$ROOT/scripts/deploy-lib/targets.sh"
LOCAL_HOST="$ROOT/scripts/local-host-deploy.sh"
CANARY_154="$ROOT/deploy/llm-gateway-go-canary@.service"
CANARY_245="$ROOT/deploy/llmgo-245-canary@.service"

failures=0
pass() { printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; failures=$((failures + 1)); }
require() {
  local description=$1 pattern=$2 file=$3
  if grep -Eq "$pattern" "$file"; then pass "$description"; else fail "$description"; fi
}

require "Kubernetes liveness remains healthz" 'livenessProbe:' "$MANIFEST"
require "Kubernetes readiness uses readyz" 'readinessProbe:[[:space:]]*$' "$MANIFEST"
readyz_line=$(grep -n 'path: /readyz' "$MANIFEST" | cut -d: -f1 | head -1 || true)
liveness_line=$(grep -n 'path: /healthz' "$MANIFEST" | cut -d: -f1 | head -1 || true)
if [[ -n "$readyz_line" && -n "$liveness_line" && "$readyz_line" -gt "$liveness_line" ]]; then
  pass "Kubernetes readiness path follows liveness and is strict"
else
  fail "Kubernetes readiness path is not strict"
fi
require "Kubernetes release image is immutable" 'image: registry\.itestu\.cn/kx-llm-gateway-go:[0-9]+\.[0-9]+\.[0-9]+-[0-9]+' "$MANIFEST"
if ! grep -Eq 'image: .*:latest' "$MANIFEST"; then pass "Kubernetes manifest does not use latest"; else fail "Kubernetes manifest uses latest"; fi
require "Kubernetes honors gateway drain budget" 'terminationGracePeriodSeconds: 35' "$MANIFEST"

require "Host library exposes strict readyz gate" '^host_wait_readyz\(\)' "$HOST_LIB"
require "Host metadata uses temporary file before rename" 'tmp=.*m\.tmp' "$HOST_LIB"
require "Host metadata atomically replaces final file" 'mv -f.*tmp.*m' "$HOST_LIB"
require "Seamless deployment calls candidate readyz gate" 'candidate_ready_url=.*readyz' "$SEAMLESS"
ready_gate=$(grep -n 'candidate_ready_url' "$SEAMLESS" | cut -d: -f1 | head -1 || true)
verified_gate=$(grep -n 'host_mark_verified' "$SEAMLESS" | cut -d: -f1 | tail -1 || true)
if [[ -n "$ready_gate" && -n "$verified_gate" && "$ready_gate" -lt "$verified_gate" ]]; then
  pass "Readiness gate precedes verified metadata"
else
  fail "Readiness gate does not precede verified metadata"
fi
require "154 rollback contract matches versioned implementation" 'target "154"' "$TARGETS"
if awk '/target_154_contract\(\)/,/^}/' "$TARGETS" | grep -q 'rollback_policy "versioned"'; then
  pass "154 uses versioned rollback contract"
else
  fail "154 rollback contract is inconsistent"
fi
require "Local deploy records prior active bundle" 'previous_version=\$\(lh_active_version\)' "$LOCAL_HOST"
require "Local deploy restores prior bundle after start failure" 'candidate failed to start; restoring previous active bundle' "$LOCAL_HOST"
require "Local deploy requires readyz before verification" 'ready_url="http://127\.0\.0\.1:\$PORT/readyz"' "$LOCAL_HOST"
# 2026-09-23 契约漂移修复（F2 同型第 6 犯）：9cbd60e40 把蓝绿单元模板 boot
# 预算 75s→600s（TimeoutStartSec 同步 700s）时漏改本断言——候选 boot 迁移链
# 在共享 252 PG 争用下实测可超 3 分钟，90s 断言早已与模板和现实脱节。
# 新契约：systemd TimeoutStartSec=700s 必须覆盖 600s 探针窗口（PROBE_TIMEOUT_
# SECS 默认 600），候选最迟在 systemd 击杀前完成就绪判定。
require "154 canary systemd budget covers 600s probe window" '^TimeoutStartSec=700s$' "$CANARY_154"
require "245 canary systemd budget covers 600s probe window" '^TimeoutStartSec=700s$' "$CANARY_245"

if (( failures > 0 )); then
  printf '%s deployment readiness contract(s) failed\n' "$failures" >&2
  exit 1
fi
