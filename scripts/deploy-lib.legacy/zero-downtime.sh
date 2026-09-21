#!/usr/bin/env bash
# Shared blue-green lifecycle primitives for local-host and systemd targets.
# The caller supplies a remote command function (or uses the local shell).
set -euo pipefail

zd_now_ns() { date +%s%N 2>/dev/null || python3 -c 'import time; print(time.time_ns())'; }

zd_render_upstream() {
  local port=$1
  # This fragment is included inside an existing upstream block. Keeping the
  # fragment to one server directive makes nginx reloads small and atomic.
  printf 'server 127.0.0.1:%s max_fails=3 fail_timeout=10s;\n' "$port"
}

# Atomically replace the include fragment after syntax validation. The command
# is deliberately passed in so this function is safe to exercise with a fake
# host in offline tests.
zd_switch_upstream() {
  local run_cmd=$1 fragment=$2 port=$3 nginx_test=${4:-nginx -t} nginx_reload=${5:-nginx -s reload}
  local tmp="${fragment}.tmp.$$"
  local old="${fragment}.bluegreen-old.$$"
  "$run_cmd" "set -eu
    test -f '$fragment' && cp -p '$fragment' '$old' || :
    umask 022
    cat > '$tmp' <<'EOF_UPSTREAM'
$(zd_render_upstream "$port")
EOF_UPSTREAM
    mv -f '$tmp' '$fragment'
    $nginx_test
    $nginx_reload
    rm -f '$old'
  " || {
    "$run_cmd" "if [ -f '$old' ]; then mv -f '$old' '$fragment'; $nginx_test && $nginx_reload; fi; rm -f '$tmp' '$old'" >/dev/null 2>&1 || true
    return 1
  }
}

zd_probe_version() {
  local curl_cmd=$1 url=$2 expected=$3 timeout_s=${4:-5}
  local body
  body=$("$curl_cmd" -fsS --max-time "$timeout_s" "$url") || return 1
  printf '%s' "$body" | EXPECTED_VERSION="$expected" python3 -c '
import json, os, sys
try:
    v=json.load(sys.stdin).get("version", "")
except Exception:
    raise SystemExit(1)
raise SystemExit(0 if v == os.environ["EXPECTED_VERSION"] else 1)
'
}

# Stop a candidate only. The active process is never touched by this helper.
zd_stop_candidate() {
  local run_cmd=$1 unit=$2
  "$run_cmd" "systemctl stop '$unit'" || true
}

# Drain old traffic after the upstream handoff has succeeded. A timeout is a
# bounded safety valve; the candidate remains active even when escalation is
# needed.
zd_drain_old() {
  local run_cmd=$1 unit=$2 health_url=$3 timeout_s=${4:-23}
  "$run_cmd" "systemctl stop '$unit'" || return 1
  "$run_cmd" "deadline=\$((\$(date +%s)+$timeout_s)); while [ \"\$(date +%s)\" -lt \"\$deadline\" ]; do curl -fsS --max-time 1 '$health_url' >/dev/null 2>&1 || exit 0; sleep 1; done; exit 0"
}

# Emit a machine-readable handoff record for deployment evidence.
zd_handoff_record() {
  local old_port=$1 new_port=$2 elapsed_ms=$3 old_version=$4 new_version=$5
  printf '{"old_port":%s,"new_port":%s,"handoff_elapsed_ms":%s,"old_version":"%s","new_version":"%s"}\n' \
    "$old_port" "$new_port" "$elapsed_ms" "$old_version" "$new_version"
}
