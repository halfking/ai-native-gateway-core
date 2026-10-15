#!/usr/bin/env bash
# Magpie P0 post-deploy observation (P0-D / P0-C / P0-A+ / P0-B).
# Run ONLY after owner authorizes deploy. Does not deploy.
#
# Usage:
#   TARGET=local|154|245 LOG_GLOB='...' BIN=... WINDOW_MIN=30 \
#     bash scripts/observe-magpie-p0-postdeploy.sh
#
# Exit: 0 = fingerprints + log probes collected (not "全绿");
#       1 = missing binary / unreadable logs / fingerprint miss.
#
# Bash 3.2-safe (no declare -A / ${var^^}). Prefer python byte search over
# strings|grep -q (pipefail+SIGPIPE → 假 MISS).
set -euo pipefail

TARGET="${TARGET:-local}"
WINDOW_MIN="${WINDOW_MIN:-30}"
# When run via `ssh … bash -s`, $0 is not the script path — ROOT only used
# for local baseline JSON; remote deploy-observe scp's BASELINE_JSON explicitly.
ROOT="$(cd "$(dirname "$0")/.." 2>/dev/null && pwd)" || ROOT=""

resolve_bin() {
  # 154/245 release 二进制名是 llm-gateway-go；本地 host-first / 245 部分轮次叫 gateway。
  local cand
  for cand in "$@"; do
    if [[ -n "$cand" && -r "$cand" ]]; then
      echo "$cand"
      return 0
    fi
  done
  return 1
}

case "$TARGET" in
  local)
    BIN="${BIN:-}"
    if [[ -z "$BIN" ]]; then
      BIN="$(resolve_bin \
        "${LLM_GATEWAY_ROOT:-$HOME/kaixuan/llm-gateway-go-v2}/run/gateway" \
        "$HOME/kaixuan/llm-gateway-go-v2/run/gateway" \
        "$HOME/kaixuan/llm-gateway-go/bin/gateway" \
        "$HOME/kaixuan/llm-gateway-go/bin/current/gateway" \
        "$HOME/kaixuan/llm-gateway-go/run/gateway" \
        /opt/llm-gateway-go/current/llm-gateway-go \
        /opt/llm-gateway-go/current/gateway)" || BIN=""
    fi
    LOG_GLOB="${LOG_GLOB:-}"
    READY_URLS=(http://127.0.0.1:8781/readyz http://127.0.0.1:8782/readyz)
    ;;
  154|245)
    if [[ -z "${BIN:-}" ]]; then
      BIN="$(resolve_bin \
        /opt/llm-gateway-go/current/llm-gateway-go \
        /opt/llm-gateway-go/current/gateway)" || BIN=""
    fi
    # Prefer canary log matching active port when LOG_GLOB unset.
    if [[ -z "${LOG_GLOB:-}" ]]; then
      active_port=""
      if [[ -r /opt/llm-gateway-go/run/active-upstream.conf ]]; then
        active_port="$(grep -oE '878[12]' /opt/llm-gateway-go/run/active-upstream.conf | head -1 || true)"
      fi
      if [[ -n "$active_port" && -r "/opt/llm-gateway-go/logs/gateway-canary-${active_port}.log" ]]; then
        LOG_GLOB="/opt/llm-gateway-go/logs/gateway-canary-${active_port}.log"
      fi
    fi
    READY_URLS=("http://127.0.0.1:8781/readyz" "http://127.0.0.1:8782/readyz")
    ;;
  *)
    echo "unknown TARGET=$TARGET (local|154|245)" >&2
    exit 1
    ;;
esac

fail=0
echo "=== magpie P0 observe target=$TARGET window=${WINDOW_MIN}m ==="

# --- fingerprints (rodata byte search; NOT macOS `strings|grep -q`) ---
bin_has_needle() {
  local bin="$1" needle="$2"
  python3 - "$bin" "$needle" <<'PY'
import sys
data = open(sys.argv[1], "rb").read()
sys.exit(0 if sys.argv[2].encode() in data else 1)
PY
}

if [[ -z "${BIN}" || ! -r "$BIN" ]]; then
  echo "FAIL: binary not found (set BIN=). looked for llm-gateway-go|gateway under /opt/llm-gateway-go/current/." >&2
  fail=1
else
  echo "BIN=$BIN"
  for needle in \
    'probeOne single-flight' \
    'llmgw:probe_one:' \
    'anthropicSameCandidateStripRetry=false' \
    'retry_after_seconds'; do
    if bin_has_needle "$BIN" "$needle"; then
      echo "OK fingerprint: $needle"
    else
      echo "MISS fingerprint: $needle" >&2
      # survival 字段在 P0-A+ 已上线 release 亦应在场；缺失=fail
      fail=1
    fi
  done
fi

# --- readyz (port may be 8781 or 8782) ---
ready_ok=0
active_ready_port=""
for u in "${READY_URLS[@]}"; do
  if curl -fsS -m 3 "$u" >/tmp/magpie_p0_readyz.json 2>/dev/null; then
    echo "OK readyz $u → $(tr -d '\n' </tmp/magpie_p0_readyz.json | head -c 200)"
    ready_ok=1
    active_ready_port="$(echo "$u" | grep -oE '878[12]' | head -1 || true)"
    break
  fi
done
[[ "$ready_ok" -eq 1 ]] || echo "WARN: no readyz on 8781/8782 (service down or wrong host)"

# --- log counters (W2 / single-flight / quotaHeld) ---
count_pat() {
  # grep 零命中 exit 1；pipefail 下必须临时关掉，否则整脚本/或 `|| echo 0` 双打。
  local pat="$1" n
  if [[ -n "${LOG_GLOB:-}" ]]; then
    set +o pipefail
    # shellcheck disable=SC2086
    n=$(grep -a -h -E "$pat" $LOG_GLOB 2>/dev/null | wc -l | tr -d ' ')
    set -o pipefail
    echo "${n:-0}"
  elif [[ "$TARGET" == "local" ]]; then
    echo "n/a-set-LOG_GLOB"
  else
    # Blue-green canary units — NOT bare llm-gateway-go (154 实测无此 unit).
    if [[ "${JOURNAL:-0}" == "1" ]]; then
      local units="llm-gateway-go-canary@8782 llm-gateway-go-canary@8781 llm-gateway-go"
      if [[ -n "$active_ready_port" ]]; then
        units="llm-gateway-go-canary@${active_ready_port} $units"
      fi
      local u total=0 chunk
      for u in $units; do
        set +o pipefail
        chunk=$(journalctl -u "$u" --since "${WINDOW_MIN} min ago" --no-pager 2>/dev/null \
          | grep -a -E "$pat" | wc -l | tr -d ' ')
        set -o pipefail
        total=$((total + ${chunk:-0}))
      done
      echo "$total"
    else
      echo "n/a-set-LOG_GLOB-or-JOURNAL=1"
    fi
  fi
}

echo "--- log counters (last ${WINDOW_MIN}m if JOURNAL=1 / LOG_GLOB set) ---"
[[ -n "${LOG_GLOB:-}" ]] && echo "LOG_GLOB=$LOG_GLOB"
for pat in \
  'ProbeSync' \
  'probeOne single-flight' \
  'quotaHeld — skip' \
  'quotaHeld' \
  'PenaltyNone|client_bug|json.word|unknown parameter' \
  'retry_after_seconds' \
  'applying learned param strips|paramrefusal'; do
  n="$(count_pat "$pat")"
  echo "count[$pat]=$n"
done

# Optional pre-deploy baseline diff (docs/audit/magpie-p0-154-baseline-*.json).
BASELINE_JSON="${BASELINE_JSON:-}"
if [[ -z "$BASELINE_JSON" && -n "$ROOT" && -f "$ROOT/docs/audit/magpie-p0-154-baseline-20261014.json" && "$TARGET" == "154" ]]; then
  BASELINE_JSON="$ROOT/docs/audit/magpie-p0-154-baseline-20261014.json"
fi
if [[ -n "$BASELINE_JSON" && -r "$BASELINE_JSON" ]]; then
  echo "--- baseline compare ($BASELINE_JSON) ---"
  python3 - "$BASELINE_JSON" "${BIN:-}" <<'PY' || true
import json, sys
base = json.load(open(sys.argv[1]))
bin_path = sys.argv[2] if len(sys.argv) > 2 else ""
data = open(bin_path, "rb").read() if bin_path else b""
print("baseline release=", base.get("release"), "port=", base.get("active_port"))
print("baseline bin=", (base.get("bin") or ""))
fps = base.get("fingerprints") or {}
for needle, was in fps.items():
    now = (needle.encode() in data) if data else None
    if now is None:
        print("fp[%s]: before=%s after=n/a" % (needle, was))
        continue
    mark = "CHANGED" if bool(was) != bool(now) else "same"
    print("fp[%s]: before=%s after=%s (%s)" % (needle, was, now, mark))
    if needle != "executor: quotaHeld" and was is False and now is True:
        print("  OK expected: new P0 fingerprint landed")
    if needle != "executor: quotaHeld" and was is False and now is False:
        print("  WARN: still MISS — release may not include WIP")
print("baseline counts=", base.get("counts"))
print("(log count delta is manual: compare count[*] above vs baseline.counts)")
PY
fi

cat <<'EOF'

--- manual checks (not auto-asserted; do not claim 全绿 from this script) ---
W2 P0-D: in a full-outage window, upstream hits / client requests ≤ 1.2;
         prefer "probeOne single-flight" / "quotaHeld — skip" over raw ProbeSync storms.
E2 P0-C: replay r1027 json-word 400 → breaker must NOT open that credential;
         other models on same cred remain selectable.
survival P0-A+: terminal frame has retry_after_seconds + attempts;
         HTTP Retry-After from applySurvivalRetryAfterHeader.
E1 P0-B: /api/admin/request-anomalies shows recovered strips;
         2nd request logs "applying learned param strips".
See docs/audit/2026-10-14-magpie-p0批判审计与收口.md §授权后观测 runbook.
EOF

exit "$fail"
