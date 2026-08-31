#!/usr/bin/env bash
# tests/lib/fake-remote-host.sh — offline harness for deploy-seamless.sh
#
# Provides:
#   * A per-test fake host directory $H mirroring the 245 target layout
#     (/opt/llm-gateway-go, /etc/systemd/system, /var/lib/llm-gateway-go).
#   * fakebin stubs for the commands the deploy script invokes via SSH
#     (systemctl, curl, nginx, ss, journalctl, psql, python3, stat,
#      sha256sum, chown, install, plus orchestrator-side go/npm/git).
#   * ssh rewrite layer: known absolute production paths are redirected
#     to $H, so the chroot-style fake host stays the source of truth.
#
# The harness is offline (no network, no real targets). It is designed
# for the 245 contract; 154/252 vhost indirection is out of scope.
#
# Public API:
#   fake_host_setup 245                       # create $H
#   fake_host_run "<remote-command>"          # bash -c in $H
#   fake_host_run_deploy 245 [args...]        # runs deploy-seamless.sh
#   fake_host_run_rollback 245 [args...]
#   fake_host_inject_stop_hang                 # is-active stays active
#   fake_host_inject_probe_fail [port]         # curl <port>/healthz fails
#   fake_host_inject_nginx_fail                # nginx reload fails
#   fake_host_inject_prng_dangling
#   fake_host_inject_unverified_release
#   fake_host_clear_injects
#   fake_host_assert msg EXISTS|ABSENT path
#   fake_host_assert_current_points_to release
#   fake_host_assert_active_port port
#   fake_host_assert_upstream_contains port
#   fake_host_assert_slot_links_to port release
#   fake_host_assert_exit_code msg zero|nonzero rc
#   fake_host_teardown

set -uo pipefail

REPO_ROOT_REAL="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export REPO_ROOT_REAL

fake_host_setup() {
  local target="${1:-245}"
  local tmp
  tmp=$(mktemp -d -t kx-bgsm.XXXXXX)
  H="$tmp"
  export H FAKE_TARGET="$target"
  export FAKE_SSH_TMPDIR="$tmp"
  export HOST_INSTALL_ROOT="$tmp/opt/llm-gateway-go"

  mkdir -p \
    "$H/opt/llm-gateway-go/releases" \
    "$H/opt/llm-gateway-go/run" \
    "$H/opt/llm-gateway-go/slots" \
    "$H/opt/llm-gateway-go/maintenance" \
    "$H/opt/llm-gateway-go/logs" \
    "$H/etc/systemd/system" \
    "$H/etc/llm-gateway-go" \
    "$H/var/lib/llm-gateway-go" \
    "$H/proc" \
    "$H/tmp" \
    "$H/fx" \
    "$H/stubs" \
    "$H/scripts" \
    "$H/scripts/ops" \
    "$H/envs" \
    "$H/web/public" \
    "$H/web/dist" \
    "$H/configs"

  # Per-test scripts/ tree. Reuses real deploy-lib + ops scripts so the
  # test exercises the same code paths, but stubs bump-version.sh so the
  # test never mutates version.json in the working tree.
  cp -R "$REPO_ROOT_REAL/scripts/deploy-lib" "$H/scripts/deploy-lib"
  for s in install-blue-green-assets install-logrotate configure-journald deploy-seamless; do
    [[ -f "$REPO_ROOT_REAL/scripts/${s}.sh" ]] && \
      cp "$REPO_ROOT_REAL/scripts/${s}.sh" "$H/scripts/${s}.sh" 2>/dev/null
  done
  for s in ensure-ops-node-env sync-245-env-from-154 sync-admin-password-from-env; do
    [[ -f "$REPO_ROOT_REAL/scripts/ops/${s}.sh" ]] && \
      cp "$REPO_ROOT_REAL/scripts/ops/${s}.sh" "$H/scripts/ops/${s}.sh" 2>/dev/null
  done
  cat >"$H/scripts/bump-version.sh" <<'BVEND'
#!/usr/bin/env bash
exit 0
BVEND
  chmod +x "$H/scripts/bump-version.sh"

  # Fake envs-SSOT loader — exports a non-existent SSH key file.
  cat >"$H/envs/loader.sh" <<'LDEND'
#!/usr/bin/env bash
fkey="${FAKE_SSH_TMPDIR:-$ENVS_ROOT}/fake_ssh_key_245"
[ -f "$fkey" ] || : >"$fkey"
export SSH_KEY_245="$fkey"
export SSH_KEY_154="$fkey"
export SSH_KEY_252="$fkey"
return 0 2>/dev/null || exit 0
LDEND
  chmod +x "$H/envs/loader.sh"
  : >"$H/fake_ssh_key_245"

  # Mirrored sql/ + docs/ for migration ledger checks. Dedupes
  # duplicate-version files (real repo has them).
  if [ -d "$REPO_ROOT_REAL/sql" ]; then
    mkdir -p "$H/sql/migrations/startup"
    for f in "$REPO_ROOT_REAL/sql/migrations/080-ursm-key-migration-ledger.sql" \
             "$REPO_ROOT_REAL/sql/migrations/081-ursm-key-migration-ledger-add-rollback-deadline.sql"; do
      [ -f "$f" ] && cp "$f" "$H/sql/migrations/"
    done
    declare -A kx_seen=()
    for f in "$REPO_ROOT_REAL"/sql/migrations/startup/[0-9]*.sql; do
      [ -f "$f" ] || continue
      base=$(basename "$f")
      case "$base" in
        *.down.sql|*.skip|*.bak.skip) continue ;;
      esac
      ver=$(printf '%s' "$base" | grep -oE '^[0-9]+' || true)
      [ -n "$ver" ] || continue
      echo "$base" | grep -qE '^[0-9]{3}_' || continue
      if [ -z "${kx_seen[$ver]+x}" ]; then
        kx_seen[$ver]="$base"
        cp "$f" "$H/sql/migrations/startup/$base"
      fi
    done
  fi
  [ -d "$REPO_ROOT_REAL/docs" ] && cp -R "$REPO_ROOT_REAL/docs" "$H/docs"

  # Pre-seed URSM ledger with correct checksums (deploy's reconcile
  # compares the local file's sha256 against the stored one).
  mkdir -p "$H/psqlstate"
  : >"$H/psqlstate/reposm.tsv"
  : >"$H/psqlstate/ursm.tsv"
  : >"$H/psqlstate/checksums.tsv"
  : >"$H/psqlstate/schema_migrations.list"
  for f in "$H"/sql/migrations/080-ursm-key-migration-ledger.sql \
           "$H"/sql/migrations/081-ursm-key-migration-ledger-add-rollback-deadline.sql; do
    [ -f "$f" ] || continue
    base=$(basename "$f")
    ver="${base%%-*}"
    sum=$(/usr/bin/shasum -a 256 "$f" | awk '{print $1}')
    printf 'ursm\t%s\t%s\t%s\n' "$ver" "$base" "$sum" >>"$H/psqlstate/reposm.tsv"
  done

  # Seed minimal repo root for the orchestrator's local file reads.
  printf 'v0-legacy\n' >"$H/VERSION"
  printf '{"version":"v0-legacy","build_seq":1866,"git_sha":"abc12345","git_tag":"9.9.9","build_date":"20260101","module":"llm-gateway-go"}\n' >"$H/version.json"
  printf '{"version":"v0-legacy","build_seq":1866,"git_sha":"abc12345","git_tag":"9.9.9","build_date":"20260101","module":"llm-gateway-go"}\n' >"$H/web/public/version.json"
  printf '<html>v0</html>\n' >"$H/web/dist/index.html"
  printf '{"version":"v0-legacy","build_seq":1866,"git_sha":"abc12345","git_tag":"9.9.9","build_date":"20260101","module":"llm-gateway-go"}\n' >"$H/web/dist/version.json"
  printf '# stub sensitive_patterns\n' >"$H/configs/sensitive_patterns.yaml"

  # Seed the v0-legacy release.
  local rel="$H/opt/llm-gateway-go/releases/v0-legacy"
  mkdir -p "$rel/web"
  printf '#!/usr/bin/env sh\necho v0\n' >"$rel/gateway"
  chmod +x "$rel/gateway"
  printf '<html>v0</html>\n' >"$rel/web/index.html"
  printf '{"version":"v0-legacy","build_seq":0,"git_sha":"cafef00d","build_date":"20260101"}\n' >"$rel/version.json"
  printf 'v0-legacy\n' >"$rel/VERSION"
  (
    cd "$rel"
    /usr/bin/shasum -a 256 gateway version.json VERSION > SHA256SUMS
  )
  printf '{"target":"245","version":"v0-legacy","verified":true,"verified_at":"2026-01-01T00:00:00Z"}\n' \
    >"$rel/deployment.json"

  # .env for the deploy script's preflight.
  cat >"$H/opt/llm-gateway-go/.env" <<'ENVEND'
LLM_GATEWAY_DATABASE_URL=postgres://fake:fake@127.0.0.1:5432/fake
LLM_GATEWAY_ADMIN_USER=fake-admin
LLM_GATEWAY_ADMIN_PASSWORD=fake-password
LLM_GATEWAY_LISTEN=:8781
TRANSPORT_LAYER_IR_ENABLED=true
ENVEND
  chmod 0600 "$H/opt/llm-gateway-go/.env"

  # Canonical active state.
  ln -sfn "$rel" "$H/opt/llm-gateway-go/current"
  ln -sfn "$H/opt/llm-gateway-go/current/gateway" "$H/opt/llm-gateway-go/gateway"
  printf '8781\n' >"$H/opt/llm-gateway-go/run/active-port"
  printf 'llmgo-245.service\n' >"$H/opt/llm-gateway-go/run/active-service"
  printf 'active\n' >"$H/etc/systemd/system/llmgo-245.service"
  printf 'active\n' >"$H/etc/systemd/system/.unit.llmgo-245.service"
  printf '8781\n' >"$H/etc/systemd/system/.port.llmgo-245.service"
  printf '4242\n' >"$H/etc/systemd/system/.pid.llmgo-245.service"
  mkdir -p "$H/proc/4242"
  ln -sfn "$H/opt/llm-gateway-go/current/gateway" "$H/proc/4242/exe"

  printf 'server 127.0.0.1:8781 max_fails=3 fail_timeout=10s;\n' \
    >"$H/opt/llm-gateway-go/run/active-upstream.conf"

  fake_host_install_stubs
  printf '%s' "$tmp"
}

fake_host_teardown() {
  if [ -n "${H:-}" ]; then rm -rf "$H" 2>/dev/null || true; fi
  unset H FAKE_TARGET FAKE_SSH_TMPDIR HOST_INSTALL_ROOT
}

fake_host_run() {
  local script=$1
  ( cd "$H" && PATH="$H/stubs:$PATH" FAKE_SSH_TMPDIR="$H" bash -c "$script" )
}

fake_host_run_deploy() {
  local target=$1; shift
  : >"$H/deploy.log"
  PATH="$H/stubs:$PATH" HOST_INSTALL_ROOT="$H/opt/llm-gateway-go" \
    FAKE_SSH_TMPDIR="$H" FAKE_TARGET="$target" ENVS_ROOT="$H/envs" \
    TMPDIR="$H/tmp" LLM_GATEWAY_SSH_PORT=25022 PROBE_TIMEOUT_SECS=10 \
    "$H/scripts/deploy-seamless.sh" deploy "$target" "$@" \
    >>"$H/deploy.log" 2>&1
}

fake_host_run_rollback() {
  local target=$1; shift
  : >"$H/rollback.log"
  PATH="$H/stubs:$PATH" HOST_INSTALL_ROOT="$H/opt/llm-gateway-go" \
    FAKE_SSH_TMPDIR="$H" FAKE_TARGET="$target" ENVS_ROOT="$H/envs" \
    TMPDIR="$H/tmp" LLM_GATEWAY_SSH_PORT=25022 \
    "$H/scripts/deploy-seamless.sh" rollback "$target" "$@" \
    >>"$H/rollback.log" 2>&1
}

fake_host_inject_stop_hang()         { touch "$H/fx/candidate_stop_hang"; }
fake_host_inject_probe_fail()         { touch "$H/fx/port_${1:-8782}_healthz_fail"; }
fake_host_inject_nginx_fail()         { touch "$H/fx/nginx_reload_fail"; }
fake_host_inject_prng_dangling()      { touch "$H/fx/dangling_slot"; }
fake_host_inject_unverified_release() { touch "$H/fx/extra_unverified"; }
fake_host_clear_injects()             { rm -f "$H/fx"/*; }

log_pass() {
  printf '  \033[0;32mPASS\033[0m %s\n' "$1"
  TESTS_PASSED=$((TESTS_PASSED + 1))
}
log_fail() {
  printf '  \033[0;31mFAIL\033[0m %s\n' "$1"
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILED_NAMES+=("$1")
}

fake_host_assert() {
  local msg=$1
  local kind=$2
  shift 2
  case "$kind" in
    EXISTS) if [ -e "$1" ]; then log_pass "$msg"
            else log_fail "$msg (missing $1)"; fi ;;
    ABSENT) if [ ! -e "$1" ]; then log_pass "$msg"
            else log_fail "$msg (present $1)"; fi ;;
    EQ)     if [ "$(cat "$1" 2>/dev/null)" = "$2" ]; then log_pass "$msg"
            else log_fail "$msg (file=$1 content=$(cat "$1") want=$2)"; fi ;;
    *)      log_fail "$msg (unknown assert kind: $kind)" ;;
  esac
}

fake_host_assert_current_points_to() {
  local release=$1
  local actual
  actual=$(readlink "$H/opt/llm-gateway-go/current" 2>/dev/null || echo none)
  case "$actual" in
    *releases/$release*) log_pass "current -> releases/$release" ;;
    *) log_fail "current -> $actual (wanted releases/$release)" ;;
  esac
}

fake_host_assert_active_port() {
  local want=$1
  local got
  got=$(cat "$H/opt/llm-gateway-go/run/active-port" 2>/dev/null || echo none)
  if [ "$got" = "$want" ]; then log_pass "active-port = $want"
  else log_fail "active-port = $got (wanted $want)"; fi
}

fake_host_assert_upstream_contains() {
  local want_port=$1
  if grep -q "server 127.0.0.1:$want_port" \
       "$H/opt/llm-gateway-go/run/active-upstream.conf" 2>/dev/null; then
    log_pass "upstream fragment serves 127.0.0.1:$want_port"
  else
    log_fail "upstream fragment does not contain 127.0.0.1:$want_port"
  fi
}

fake_host_assert_slot_links_to() {
  local port=$1 release=$2
  local actual
  actual=$(readlink "$H/opt/llm-gateway-go/slots/$port" 2>/dev/null || echo none)
  case "$actual" in
    *releases/$release*) log_pass "slots/$port -> releases/$release" ;;
    *) log_fail "slots/$port -> $actual (wanted releases/$release)" ;;
  esac
}

fake_host_assert_exit_code() {
  local msg=$1 want=$2 got=$3
  if [ "$want" = zero ] && [ "$got" -eq 0 ]; then
    log_pass "$msg (rc=0)"
  elif [ "$want" = nonzero ] && [ "$got" -ne 0 ]; then
    log_pass "$msg (rc=$got != 0)"
  else
    log_fail "$msg (rc=$got, wanted $want)"
  fi
}

# ─── fakebin stub installation ──────────────────────────────────────

fake_host_install_stubs() {
  local stubs=$H/stubs
  mkdir -p "$stubs"

  # ssh — the single seam. Parses argv to find host + last-arg script,
  # rewrites known absolute production paths into $FAKE_SSH_TMPDIR, then
  # bash -c the rewritten script with the fakebin prepended.
  cat >"$stubs/ssh" <<'SSHEOF'
#!/usr/bin/env bash
set -uo pipefail
opts=()
host=""
script=""
i=0
args=("$@")
while (( i < ${#args[@]} )); do
  a="${args[$i]}"
  case "$a" in
    -o) opts+=("$a"); (( i += 2 )) ;;
    -o=*) opts+=("$a"); (( i++ )) ;;
    -i|-p|-W|-l|-S|-J) (( i += 2 )) ;;
    -*) (( i++ )) ;;
    *) host="$a"; (( i++ )); script="${args[*]:$i}"; break ;;
  esac
done
[ -z "$host" ] && exit 0
[ -z "$script" ] && exit 0
h="${FAKE_SSH_TMPDIR:?}"
# Path rewrite (longest first; single + double quoted).
script="${script//\/var\/lib\/llm-gateway-go/$h\/var\/lib\/llm-gateway-go}"
script="${script//\/var\/log\/llm-gateway-go/$h\/var\/log\/llm-gateway-go}"
script="${script//\/var\/lib\/kx-gateway/$h\/var\/lib\/kx-gateway}"
script="${script//\/opt\/llm-gateway-go/$h\/opt\/llm-gateway-go}"
script="${script//\/etc\/llm-gateway-go/$h\/etc\/llm-gateway-go}"
script="${script//\/etc\/systemd\/system/$h\/etc\/systemd\/system}"
script="${script//\/proc\//$h\/proc\/}"
PATH="$h/stubs:$PATH" FAKE_SSH_TMPDIR="$h" bash -c "$script"
SSHEOF
  chmod +x "$stubs/ssh"

  # systemctl — minimal state machine.
  cat >"$stubs/systemctl" <<'SYSEOF'
#!/usr/bin/env bash
set -uo pipefail
host="${FAKE_SSH_TMPDIR:?}"
statedir="${FAKE_SSH_TMPDIR:?}/etc/systemd/system"
is_canary() { [ "${1#*-canary@}" != "$1" ]; }
canary_port() {
  local u=$1
  # Extract the port (e.g. 8782) from "name@PORT.service". Works on
  # both BSD and GNU sed.
  local tmp="${u%.*}"
  tmp="${tmp##*@}"
  printf '%s' "$tmp"
}
canonical_port() { printf '8781'; }
cmd_show() {
  local prop=$1 unit=$2
  case "$prop" in
    MainPID)
      if [ -f "$statedir/.unit.$unit" ] && [ "$(cat "$statedir/.unit.$unit")" = active ]; then
        printf 'MainPID=%s\n' "$(cat "$statedir/.pid.$unit" 2>/dev/null || echo 0)"
      else
        printf 'MainPID=0\n'
      fi ;;
    StandardOutput|StandardError)
      if is_canary "$unit"; then printf 'StandardOutput=journal\n'
      else printf 'StandardOutput=append:/var/log/llm-gateway-go/gateway.%s.log\n' "$(printf '%s' "$prop" | tr '[:upper:]' '[:lower:]')"
      fi ;;
    *) printf '%s=\n' "$prop" ;;
  esac
}
cmd_start() {
  local unit=$1
  local port
  if is_canary "$unit"; then port=$(canary_port "$unit")
  else port=$(canonical_port); fi
  if [ -f "$host/fx/candidate_stop_hang" ] && is_canary "$unit"; then
    printf 'active\n' >"$statedir/.unit.$unit"
    printf '9999\n' >"$statedir/.pid.$unit"
    printf '%s\n' "$port" >"$statedir/.port.$unit"
    return 0
  fi
  printf 'active\n' >"$statedir/.unit.$unit"
  printf '%s\n' $(( 4242 + RANDOM % 1000 )) >"$statedir/.pid.$unit"
  printf '%s\n' "$port" >"$statedir/.port.$unit"
}
cmd_stop() {
  # The stop-hang failure-injection keeps the unit "active" even after
  # a stop request, simulating a hung unit that the deploy's 45s
  # deadline loop must catch. We synthesize the active state file
  # (which the deploy's `start` would have created) so the subsequent
  # is-active poll sees "active" until the deadline fires.
  if [ -f "$host/fx/candidate_stop_hang" ] && is_canary "$1"; then
    local port
    port=$(canary_port "$1")
    if [ -z "$port" ]; then port=$(canonical_port); fi
    printf 'active\n' >"$statedir/.unit.$1"
    printf '9999\n' >"$statedir/.pid.$1" 2>/dev/null
    printf '%s\n' "$port" >"$statedir/.port.$1" 2>/dev/null
    return 0
  fi
  printf 'inactive\n' >"$statedir/.unit.$1"
}
cmd_is_active() {
  if [ -f "$statedir/.unit.$1" ] && [ "$(cat "$statedir/.unit.$1")" = active ]; then return 0
  else return 3; fi
}
quiet=0
i=0
args=("$@")
while (( i < ${#args[@]} )); do
  a="${args[$i]}"
  case "$a" in
    --quiet) quiet=1 ;;
    --value|--property) break ;;
  esac
  (( i++ ))
done
action=""
rest=()
i=0
while (( i < ${#args[@]} )); do
  a="${args[$i]}"
  case "$a" in
    start|stop|restart|status|reload|show|is-active|daemon-reload|enable|disable|mask|unmask|list-units|reset-failed|condrestart|try-restart)
      action="$a"
      rest=("${args[@]:$((i+1))}")
      break ;;
  esac
  (( i++ ))
done
case "$action" in
  # systemctl accepts flags before/after the action (e.g.
  # `systemctl is-active --quiet unit.service`). Skip leading flags
  # in rest[] to find the actual unit argument.
  start|stop|restart|is-active|status)
    unit=""
    for r in "${rest[@]}"; do
      case "$r" in
        --*) continue ;;
        *) unit="$r"; break ;;
      esac
    done
    case "$action" in
      start)     cmd_start "${unit:-}" ;;
      stop)      cmd_stop "${unit:-}" ;;
      restart)   cmd_stop "${unit:-}"; cmd_start "${unit:-}" ;;
      is-active) cmd_is_active "${unit:-}" ;;
      status)    printf 'stub %s\n' "${unit:-}"; exit 0 ;;
    esac ;;
  daemon-reload) : ;;
  show)
    unit=""; prop=""
    for r in "${rest[@]}"; do
      case "$r" in
        --property=*) prop="${r#--property=}" ;;
        --property) : ;;
        *) unit="$r" ;;
      esac
    done
    cmd_show "$prop" "$unit" ;;
  *) exit 0 ;;
esac
SYSEOF
  chmod +x "$stubs/systemctl"

  # curl — gateway(per port) + nginx(443 → fragment) + maintenance page.
  cat >"$stubs/curl" <<'CURLEOF'
#!/usr/bin/env bash
set -uo pipefail
host="${FAKE_SSH_TMPDIR:?}"
statedir="${FAKE_SSH_TMPDIR:?}/etc/systemd/system"
port=""
url=""
mode_http_code=""
mode_out=""
mode_body="STDOUT"
mode_dash_headers=0
while [ $# -gt 0 ]; do
  case "$1" in
    -sS|-fsS|-kfsS|-ksS|-f|-s) shift ;;
    --max-time|-m|--connect-timeout) shift 2 ;;
    --resolve) shift 2 ;;
    -D) mode_dash_headers=1; shift 2 ;;
    -D-) mode_dash_headers=1; shift ;;
    -o) mode_out="$2"; mode_body="FILE:$2"; shift 2 ;;
    -w) mode_http_code="$2"; shift 2 ;;
    -*) shift ;;
    http://127.0.0.1:*|https://127.0.0.1:*|https://*:*)
      url="$1"; port="${1#*://}"; port="${port%%/*}"; port="${port##*:}" ;;
    *) if [ -z "$url" ]; then url="$1"; port="${1##*:}"; port="${port%%/*}"; fi ;;
  esac
  shift
done
code=000
body=""
path="${url#*://}"; path="${path#*/}"
if [ -f "$host/fx/port_${port}_healthz_fail" ] && [ "$path" = healthz ]; then
  [ -n "$mode_http_code" ] && printf '503\n'
  exit 7
fi
case "$url" in
  https://*)
    if [ -f "$host/opt/llm-gateway-go/maintenance/UPGRADING" ] && [ "$path" != healthz ]; then
      code=503
      body="system upgrading"
      if (( mode_dash_headers )); then
        printf 'HTTP/1.1 503 Service Temporarily Unavailable\r\n'
        printf 'X-LLM-Gateway-Upgrade: in-progress\r\n\r\n'
      fi
    elif [ -f "$host/opt/llm-gateway-go/run/active-port" ]; then
      port=$(cat "$host/opt/llm-gateway-go/run/active-port")
      case "$path" in
        healthz|readyz|"") code=200; body="OK" ;;
        version)
          slot="$host/opt/llm-gateway-go/slots/$port"
          if [ -L "$slot" ]; then body=$(cat "$slot/version.json" 2>/dev/null) || body="{}"
          else body=$(cat "$host/opt/llm-gateway-go/current/version.json" 2>/dev/null) || body="{}"
          fi ;;
      esac
    fi ;;
  http://127.0.0.1:*)
    case "$path" in
      healthz|readyz|"") code=200; body="OK" ;;
      version)
        slot="$host/opt/llm-gateway-go/slots/$port"
        if [ -L "$slot" ]; then body=$(cat "$slot/version.json" 2>/dev/null) || body="{}"
        else body=$(cat "$host/opt/llm-gateway-go/current/version.json" 2>/dev/null) || body="{}"
        fi ;;
      api/auth/token) code=200; body='{"access_token":"fake","api_key":"fake"}' ;;
      api/system/background-tasks) code=200 ;;
    esac ;;
esac
if [ "$mode_body" = STDOUT ]; then
  (( mode_dash_headers )) && printf 'HTTP/1.1 %s OK\r\n\r\n' "$code"
  [ -n "$body" ] && printf '%s\n' "$body"
  [ -n "$mode_http_code" ] && printf '%s\n' "$code"
else
  printf '%s\n' "$body" >"$mode_out"
fi
CURLEOF
  chmod +x "$stubs/curl"

  # nginx, journalctl, ss, stat, sha256sum, chown, install
  cat >"$stubs/nginx" <<'NGXEOF'
#!/usr/bin/env bash
[ -f "${FAKE_SSH_TMPDIR:?}/fx/nginx_reload_fail" ] && [ "$1" = "-s" ] && [ "$2" = "reload" ] && exit 1
exit 0
NGXEOF
  chmod +x "$stubs/nginx"

  cat >"$stubs/journalctl" <<'JCEOF'
#!/usr/bin/env bash
[ -f "${FAKE_SSH_TMPDIR:?}/fx/pg_disabled_logs" ] && printf 'postgres disabled\n'
JCEOF
  chmod +x "$stubs/journalctl"

  cat >"$stubs/ss" <<'SSEOF'
#!/usr/bin/env bash
statedir="${FAKE_SSH_TMPDIR:?}/etc/systemd/system"
[ -d "$statedir/.port" ] || exit 0
for p in "$statedir"/.port/*; do
  [ -d "$p" ] || continue
  printf 'LISTEN 0 128 *:%s *:*\n' "$(basename "$p")"
done
SSEOF
  chmod +x "$stubs/ss"

  cat >"$stubs/stat" <<'STATEOF'
#!/usr/bin/env bash
if [ "$1" = "-c" ] && [ "$2" = "%Y" ]; then shift 2; exec /usr/bin/stat -f %m "$@"; fi
exec /usr/bin/stat "$@"
STATEOF
  chmod +x "$stubs/stat"

  cat >"$stubs/sha256sum" <<'SHSEOF'
#!/usr/bin/env bash
if [ "$1" = "-c" ]; then
  file="$2"; shift 2; rc=0
  while IFS= read -r line; do
    expect=$(printf '%s' "$line" | awk '{print $1}')
    name=$(printf '%s' "$line" | awk '{print $2}' | sed 's/^\*//')
    actual=$(/usr/bin/shasum -a 256 "$name" 2>/dev/null | awk '{print $1}')
    if [ "$expect" != "$actual" ]; then printf '%s: FAILED\n' "$name" >&2; rc=1
    else printf '%s: OK\n' "$name"; fi
  done <"$file"
  exit "$rc"
fi
exec /usr/bin/shasum -a 256 "$@"
SHSEOF
  chmod +x "$stubs/sha256sum"

  cat >"$stubs/chown" <<'CHOWNEOF'
#!/usr/bin/env bash
exit 0
CHOWNEOF
  chmod +x "$stubs/chown"

  cat >"$stubs/install" <<'INSTEOF'
#!/usr/bin/env bash
exec /usr/bin/install "$@"
INSTEOF
  chmod +x "$stubs/install"

  # psql stub — minimal SQL responder for the deploy-script's gate
  # queries. Uses the psqlstate TSV files written by fake_host_setup.
  cat >"$stubs/psql" <<'PSQLEOF'
#!/usr/bin/env bash
set -uo pipefail
statedir="${FAKE_SSH_TMPDIR:?}/psqlstate"
mkdir -p "$statedir"
DB="postgres://fake:fake@127.0.0.1:5432/fake"
q=""
file=""
while [ $# -gt 0 ]; do
  case "$1" in
    -v|-t|-A|-q) shift 2 ;;
    -c) shift; q="$1"; shift ;;
    -f) shift; file="$1"; shift ;;
    *) shift ;;
  esac
done
[ -n "$file" ] && exit 0
emit() { printf '%s\n' "$1"; }
# history gate
if echo "$q" | grep -q "schema_migrations GROUP BY version"; then emit "0|0|1|present"; exit 0; fi
# state transitions gate
if echo "$q" | grep -q "request_state_transitions"; then emit "1|2|1"; exit 0; fi
# to_regclass ledger
if echo "$q" | grep -q "pg_class WHERE oid"; then emit "1"; exit 0; fi
# checksum ledger SELECT (parsed via simple grep for version=)
if echo "$q" | grep -q "SELECT checksum FROM llm_gateway_migration_checksums"; then
  ver=$(echo "$q" | sed -n "s/.*version *= *'\([^']*\)'.*/\1/p" | head -1)
  if [ -n "$ver" ]; then
    awk -v v="$ver" '$1 == v {print $3}' "$statedir/checksums.tsv" 2>/dev/null
  fi
  exit 0
fi
# schema_migrations reads
if echo "$q" | grep -q "SELECT version FROM schema_migrations"; then
  sort -n "$statedir/schema_migrations.list" 2>/dev/null
  exit 0
fi
if echo "$q" | grep -q "SELECT 1 FROM schema_migrations WHERE version"; then
  ver=$(echo "$q" | sed -n "s/.*version = '\([^']*\)'.*/\1/p" | head -1)
  if grep -qx "$ver" "$statedir/schema_migrations.list" 2>/dev/null; then emit "1"; fi
  exit 0
fi
# schema_migrations INSERT (BEGIN; INSERT...; COMMIT;): extract VALUES
if echo "$q" | grep -q "INSERT INTO schema_migrations"; then
  ver=$(printf '%s' "$q" | awk -F"'" '/INSERT INTO schema_migrations/ {for(i=2;i<=NF;i+=2) if($i ~ /^[0-9]+$/) {print $i; exit}}')
  echo "$ver" >>"$statedir/schema_migrations.list"
  exit 0
fi
# llm_gateway_migration_checksums INSERT
if echo "$q" | grep -q "INSERT INTO llm_gateway_migration_checksums"; then
  ver=$(printf '%s' "$q" | awk -F"'" '/VALUES/ {for(i=2;i<=NF;i+=2) if($i ~ /^[0-9]+$/) {print $i; exit}}')
  base=$(printf '%s' "$q" | awk -F"'" '/VALUES/ {for(i=2;i<=NF;i+=2) if($i ~ /\.sql$/) {print $i; exit}}')
  sum=$(printf '%s' "$q" | awk -F"'" '/VALUES/ {for(i=2;i<=NF;i+=2) if(length($i)==64 && $i ~ /^[0-9a-f]+$/) {print $i; exit}}')
  printf '%s\t%s\t%s\n' "$ver" "$base" "$sum" >>"$statedir/checksums.tsv"
  exit 0
fi
# repository_schema_migrations URSM SELECT
if echo "$q" | grep -q "SELECT checksum FROM public.repository_schema_migrations"; then
  name=$(printf '%s' "$q" | awk -F"'" '/migration_name/ {for(i=2;i<=NF;i+=2) if($i ~ /\.sql$/) {print $i; exit}}')
  if [ -n "$name" ]; then
    awk -v n="$name" '$3 == n {print $4; exit}' "$statedir/reposm.tsv" 2>/dev/null
  fi
  exit 0
fi
if echo "$q" | grep -q "INSERT INTO public.repository_schema_migrations"; then
  name=$(printf '%s' "$q" | awk -F"'" '/VALUES/ {for(i=2;i<=NF;i+=2) if($i ~ /\.sql$/) {print $i; exit}}')
  sum=$(printf '%s' "$q" | awk -F"'" '/VALUES/ {for(i=2;i<=NF;i+=2) if(length($i)==64 && $i ~ /^[0-9a-f]+$/) {print $i; exit}}')
  printf 'ursm\tany\t%s\t%s\n' "$name" "$sum" >>"$statedir/reposm.tsv"
  exit 0
fi
if echo "$q" | grep -q "CREATE TABLE IF NOT EXISTS public.repository_schema_migrations"; then
  touch "$statedir/reposm.tsv"
  exit 0
fi
# default: 1 (POST_CONDITION default; ledger presence)
emit "1"
PSQLEOF
  chmod +x "$stubs/psql"

  # Orchestrator-side stubs
  cat >"$stubs/go" <<'GOEOF'
#!/usr/bin/env bash
out=""; prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
[ -n "$out" ] && printf '#!/usr/bin/env sh\necho stub\n' >"$out" && chmod +x "$out"
exit 0
GOEOF
  chmod +x "$stubs/go"

  cat >"$stubs/npm" <<'NPMEOF'
#!/usr/bin/env bash
exit 0
NPMEOF
  chmod +x "$stubs/npm"

  cat >"$stubs/git" <<'GITEOF'
#!/usr/bin/env bash
case "$1" in
  rev-parse)
    case "${2:-}" in
      --short=8|--short|-short) printf 'abc12345\n'; exit 0 ;;
      *) printf 'abc1234567deadbeef\n'; exit 0 ;;
    esac ;;
  tag) printf 'v9.9.9\n'; exit 0 ;;
  *) printf 'fake\n'; exit 0 ;;
esac
GITEOF
  chmod +x "$stubs/git"
}