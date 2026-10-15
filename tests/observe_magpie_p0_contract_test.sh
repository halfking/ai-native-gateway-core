#!/usr/bin/env bash
# observe_magpie_p0_contract_test.sh —— magpie P0 观测/部署硬门契约。
#
# 守的是 2026-10-14 154 只读烟测踩到的三坑（假 FAIL / 中途死）：
#   1. 二进制双名 llm-gateway-go|gateway
#   2. canary@port 日志/unit（禁裸 llm-gateway-go 作为唯一 journal）
#   3. pipefail + grep 零命中不得打死脚本；禁 declare -A / strings|grep -q
#   4. deploy-observe 无 MAGPIE_P0_DEPLOY_AUTH=1 → exit 2
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OBS="$ROOT/scripts/observe-magpie-p0-postdeploy.sh"
DEP="$ROOT/scripts/deploy-observe-magpie-p0.sh"

pass() { printf 'PASS: %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

[[ -f "$OBS" ]] || fail "missing $OBS"
[[ -f "$DEP" ]] || fail "missing $DEP"
[[ -x "$OBS" ]] || fail "$OBS not executable"
[[ -x "$DEP" ]] || fail "$DEP not executable"

# ── 1. 双二进制名 ───────────────────────────────────────────────
grep -q 'llm-gateway-go' "$OBS" || fail "observe must resolve llm-gateway-go (154 name)"
grep -q '/opt/llm-gateway-go/current/gateway' "$OBS" || fail "observe must also resolve gateway (245 name)"
# 默认不得只钉 gateway（会被 154 假 FAIL）
if grep -qE '^[[:space:]]*BIN="\$\{BIN:-/opt/llm-gateway-go/current/gateway\}"' "$OBS"; then
  fail "observe still defaults BIN only to .../gateway (154 FileNotFound)"
fi
pass "binary dual-name candidates"

# ── 2. canary 日志 / unit ───────────────────────────────────────
grep -q 'gateway-canary-' "$OBS" || fail "observe must prefer gateway-canary-\$port.log"
grep -q 'llm-gateway-go-canary@' "$OBS" || fail "observe journal must use canary@port units"
pass "canary log/unit"

# ── 3. 反假绿量具 ───────────────────────────────────────────────
# 只钉可执行形态（行首空白后 declare -A）；注释里的「no declare -A」不得误红。
if grep -qE '^[[:space:]]*declare[[:space:]]+-A' "$OBS"; then
  fail "observe must not use declare -A (macOS bash 3.2)"
fi
if grep -qE '^[[:space:]]*[^#[:space:]].*strings[[:space:]]*\|[[:space:]]*grep' "$OBS"; then
  fail "observe must not use strings|grep (pipefail+SIGPIPE 假 MISS)"
fi
grep -q 'set +o pipefail' "$OBS" || fail "observe count_pat must disable pipefail around grep|wc"
grep -q 'bin_has_needle\|python3' "$OBS" || fail "observe fingerprints must use python byte search"
pass "no declare -A / no strings|grep / pipefail-safe counts"

# ── 4. deploy 硬门 ───────────────────────────────────────────────
grep -q 'MAGPIE_P0_DEPLOY_AUTH' "$DEP" || fail "deploy-observe missing auth gate"
set +e
out="$(MAGPIE_P0_DEPLOY_AUTH= bash "$DEP" 2>&1)"
rc=$?
set -e
[[ "$rc" -eq 2 ]] || fail "deploy-observe without auth want exit 2, got $rc"
echo "$out" | grep -q 'MAGPIE_P0_DEPLOY_AUTH=1' || fail "refuse message must name MAGPIE_P0_DEPLOY_AUTH=1"
pass "deploy refuse exit 2"

# ── 5. 行为：临时 BIN 含三指纹 → OBS=0；缺指纹 → OBS=1 且脚本跑完 count ───────────────────────────────────────────────
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# minimal fake "binary" with needles
printf 'probeOne single-flight\nllmgw:probe_one:\nanthropicSameCandidateStripRetry=false\nretry_after_seconds\n' > "$tmp/gateway"
chmod +x "$tmp/gateway"
set +e
TARGET=local BIN="$tmp/gateway" bash "$OBS" >"$tmp/ok.out" 2>&1
rc=$?
set -e
[[ "$rc" -eq 0 ]] || fail "observe with all fingerprints want 0, got $rc; $(tail -5 "$tmp/ok.out")"
grep -q 'OK fingerprint: probeOne single-flight' "$tmp/ok.out" || fail "missing OK fingerprint line"
pass "behavior: fingerprints OK → exit 0"

printf 'executor: quotaHeld only\n' > "$tmp/emptybin"
chmod +x "$tmp/emptybin"
# empty log so count_pat hits zero matches (pipefail trap)
: >"$tmp/empty.log"
set +e
TARGET=local BIN="$tmp/emptybin" LOG_GLOB="$tmp/empty.log" bash "$OBS" >"$tmp/miss.out" 2>&1
rc=$?
set -e
[[ "$rc" -eq 1 ]] || fail "observe with MISS fingerprints want 1, got $rc"
grep -q 'MISS fingerprint: probeOne single-flight' "$tmp/miss.out" || fail "want MISS line"
grep -q 'count\[ProbeSync\]=0' "$tmp/miss.out" || fail "zero-match counts must print (not abort mid-script)"
pass "behavior: MISS + zero counts still complete"

echo "ALL PASS: observe_magpie_p0_contract_test"
