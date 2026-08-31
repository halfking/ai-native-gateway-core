#!/usr/bin/env bash
# tests/lib/fake-remote-host.sh — offline harness for deploy-seamless.sh
#
# This library stands up a complete fake remote host for the canonical
# deploy-seamless.sh (and its deploy-lib/* siblings). It is intentionally
# designed for the 245 contract (active_port=8781, candidate_port=8782,
# service_name=llmgo-245.service, binary=gateway, env file
# /opt/llm-gateway-go/.env) because 245 has no 252 vhost indirection,
# keeping the state-machine surface area manageable.
#
# Public API:
#
#   fake_host_setup 245                       # create TMPDIR + seed state
#   fake_host_run "<remote-command>"          # cd $H, bash -c with fakebin
#   fake_host_run_deploy 245 [args...]        # runs scripts/deploy-seamless.sh
#   fake_host_run_rollback 245 [args...]
#   fake_host_inject_stop_hang                # is-active keeps reporting active
#   fake_host_inject_probe_fail [port]         # curl healthz on port returns 000
#   fake_host_inject_nginx_fail               # nginx reload fails
#   fake_host_inject_prng_dangling            # plant dangling slots/<port>
#   fake_host_inject_unverified_release       # plant unverified release
#   fake_host_assert ABSENT|EXISTS|EQ [path]
#   fake_host_assert_release_verified <ver>
#   fake_host_assert_current_points_to <ver>
#   fake_host_assert_active_port <port>
#   fake_host_assert_upstream_contains <port>
#   fake_host_assert_slot_links_to <port> <ver>
#   fake_host_assert_exit_code <msg> zero|nonzero <rc>

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
    "$H/etc/nginx" \
    "$H/var/lib/llm-gateway-go" \
    "$H/proc" \
    "$H/tmp" \
    "$H/fx" \
    "$H/stubs" \
    "$H/envs" \
    "$H/web/public" \
    "$H/web/dist" \
    "$H/configs"

  # Stage a per-test scripts/ tree that mirrors the real one for
  # shared deps but substitutes scripts that would otherwise pollute
  # the working tree or talk to the real ops backend.
  local sandbox_scripts="$H/scripts"
  mkdir -p "$sandbox_scripts"
  cp -R "$REPO_ROOT_REAL/scripts/deploy-lib" "$sandbox_scripts/deploy-lib"
  cp "$REPO_ROOT_REAL/scripts/install-blue-green-assets.sh" \
     "$sandbox_scripts/install-blue-green-assets.sh" 2>/dev/null || true
  cp "$REPO_ROOT_REAL/scripts/install-logrotate.sh" \
     "$sandbox_scripts/install-logrotate.sh" 2>/dev/null || true
  cp "$REPO_ROOT_REAL/scripts/configure-journald.sh" \
     "$sandbox_scripts/configure-journald.sh" 2>/dev/null || true
  cp "$REPO_ROOT_REAL/scripts/deploy-seamless.sh" \
     "$sandbox_scripts/deploy-seamless.sh" 2>/dev/null || true
  mkdir -p "$sandbox_scripts/ops"
  cp "$REPO_ROOT_REAL/scripts/ops/ensure-ops-node-env.sh" \
     "$sandbox_scripts/ops/ensure-ops-node-env.sh" 2>/dev/null || true
  cp "$REPO_ROOT_REAL/scripts/ops/sync-245-env-from-154.sh" \
     "$sandbox_scripts/ops/sync-245-env-from-154.sh" 2>/dev/null || true
  cp "$REPO_ROOT_REAL/scripts/ops/sync-admin-password-from-env.sh" \
     "$sandbox_scripts/ops/sync-admin-password-from-env.sh" 2>/dev/null || true
  cat >"$sandbox_scripts/bump-version.sh" <<'BVEOF'
#!/usr/bin/env bash
# tests stub: do NOT mutate version files.
exit 0
BVEOF
  chmod +x "$sandbox_scripts/bump-version.sh"

  # Drop a fake envs-SSOT loader so deploy-seamless.sh's
  # `source $ENVS_ROOT/loader.sh` doesn't error out.
  cat >"$H/envs/loader.sh" <<'ENVLOADER_EOF'
#!/usr/bin/env bash
fkey="${FAKE_SSH_TMPDIR:-$ENVS_ROOT}/fake_ssh_key_245"
[[ -f "$fkey" ]] || : >"$fkey"
export SSH_KEY_245="$fkey"
export SSH_KEY_154="$fkey"
export SSH_KEY_252="$fkey"
return 0 2>/dev/null || exit 0
ENVLOADER_EOF
  chmod +x "$H/envs/loader.sh"
  : >"$H/fake_ssh_key_245"

  fake_host_install_stubs

  # Seed minimal repo root for host_stage_release / bump-version's python3
  # invocations (version.json / VERSION / configs/ / web/dist).
  printf 'v0-legacy\n' >"$H/VERSION"
  printf '{"version":"v0-legacy","build_seq":1866,"git_sha":"abc12345","git_tag":"9.9.9","build_date":"20260101","module":"llm-gateway-go"}\n' >"$H/version.json"
  printf '{"version":"v0-legacy","build_seq":1866,"git_sha":"abc12345","git_tag":"9.9.9","build_date":"20260101","module":"llm-gateway-go"}\n' >"$H/web/public/version.json"
  printf '<html>v0</html>\n' >"$H/web/dist/index.html"
  printf '{"version":"v0-legacy","build_seq":1866,"git_sha":"abc12345","git_tag":"9.9.9","build_date":"20260101","module":"llm-gateway-go"}\n' >"$H/web/dist/version.json"
  printf '# stub sensitive_patterns\n' >"$H/configs/sensitive_patterns.yaml"

  # Mirror sql/ and docs/ from the real repo. Filter startup migrations
  # to dedupe (real repo has duplicate-version files; deploy's
  # _deploy_validate_pending_migration_versions bails).
  if [[ -d "$REPO_ROOT_REAL/sql" ]]; then
    mkdir -p "$H/sql/migrations/startup"
    cp "$REPO_ROOT_REAL/sql/migrations/080-ursm-key-migration-ledger.sql" \
       "$REPO_ROOT_REAL/sql/migrations/081-ursm-key-migration-ledger-add-rollback-deadline.sql" \
       "$H/sql/migrations/" 2>/dev/null || true
    declare -A kx_seen=()
    for f in "$REPO_ROOT_REAL"/sql/migrations/startup/[0-9]*.sql; do
      [[ -f "$f" ]] || continue
      base=$(basename "$f")
      case "$base" in *.down.sql|*.skip|*.bak.skip) continue ;; esac
      ver=$(printf '%s' "$base" | grep -oE '^[0-9]+' || true)
      [[ -n "$ver" ]] || continue
      [[ "$base" =~ ^[0-9]{3}_ ]] || continue
      if [[ -z "${kx_seen[$ver]+x}" ]]; then
        kx_seen[$ver]="$base"
        cp "$f" "$H/sql/migrations/startup/$base"
      fi
    done
  fi
  [[ -d "$REPO_ROOT_REAL/docs" ]] && cp -R "$REPO_ROOT_REAL/docs" "$H/docs"

  # Pre-seed URSM ledger with correct checksums.
  local pql="$H/psqlstate"
  mkdir -p "$pql"
  : >"$pql/reposm.tsv"; : >"$pql/ursm.tsv"
  : >"$pql/checksums.tsv"; : >"$pql/schema_migrations.list"
  for f in "$H"/sql/migrations/080-ursm-key-migration-ledger.sql \
           "$H"/sql/migrations/081-ursm-key-migration-ledger-add-rollback-deadline.sql; do
    [[ -f "$f" ]] || continue
    base=$(basename "$f")
    sum=$(/usr/bin/shasum -a 256 "$f" | awk '{print $1}')
    printf 'ursm\t%s\t%s\t%s\n' "${base%%-*}" "$base" "$sum" >>"$pql/reposm.tsv"
  done

  fake_host_seed_legacy_release "$H/v0-legacy"
  # Env file the deploy chmods to 0600 and the preflight reads DSN from.
  cat >"$H/opt/llm-gateway-go/.env" <<'ENV_EOF'
LLM_GATEWAY_DATABASE_URL=postgres://fake:fake@127.0.0.1:5432/fake
LLM_GATEWAY_ADMIN_USER=fake-admin
LLM_GATEWAY_ADMIN_PASSWORD=fake-password
LLM_GATEWAY_LISTEN=:8781
TRANSPORT_LAYER_IR_ENABLED=true
ENV_EOF
  chmod 0600 "$H/opt/llm-gateway-go/.env"

  # Pre-mark the legacy release as verified.
  mkdir -p "$H/opt/llm-gateway-go/releases/v0-legacy"
  cp "$H/v0-legacy/version.json" "$H/opt/llm-gateway-go/releases/v0-legacy/version.json"
  printf '{"target":"245","version":"v0-legacy","verified":true,"verified_at":"2026-01-01T00:00:00Z"}\n' \
    >"$H/opt/llm-gateway-go/releases/v0-legacy/deployment.json"

  # Seed canonical active state: current → v0-legacy on port 8781.
  ln -sfn "$H/opt/llm-gateway-go/releases/v0-legacy" \
         "$H/opt/llm-gateway-go/current"
  ln -sfn "$H/opt/llm-gateway-go/current/gateway" \
         "$H/opt/llm-gateway-go/gateway"
  printf '8781\n' >"$H/opt/llm-gateway-go/run/active-port"
  printf 'llmgo-245.service\n' >"$H/opt/llm-gateway-go/run/active-service"
  printf 'active\n' >"$H/etc/systemd/system/llmgo-245.service"
  printf 'active\n' >"$H/etc/systemd/system/.unit.llmgo-245.service"
  printf '8781\n' >"$H/etc/systemd/system/.port.llmgo-245.service"
  printf '4242\n' >"$H/etc/systemd/system/.pid.llmgo-245.service'
  mkdir -p "$H/proc/4242"
  ln -sfn "$H/opt/llm-gateway-go/current/gateway" "$H/proc/4242/exe"

  printf 'server 127.0.0.1:%s max_fails=3 fail_timeout=10s;\n' 8781 \
    >"$H/opt/llm-gateway-go/run/active-upstream.conf"

  printf '%s' "$tmp"
}

fake_host_teardown() {
  [[ -n "${H:-}" ]] && rm -rf "$H" 2>/dev/null || true
  unset H FAKE_TARGET FAKE_SSH_TMPDIR HOST_INSTALL_ROOT
}

fake_host_run() {
  local script=$1
  ( cd "$H" && PATH="$H/stubs:$PATH" FAKE_SSH_TMPDIR="$H" bash -c "$script" )
}

fake_host_run_deploy() {
  local target=$1; shift
  : >"$H/deploy.log"
  PATH="$H/stubs:$PATH" \
    HOST_INSTALL_ROOT="$H/opt/llm-gateway-go" \
    FAKE_SSH_TMPDIR="$H" \
    FAKE_TARGET="$target" \
    ENVS_ROOT="$H/envs" \
    TMPDIR="$H/tmp" \
    LLM_GATEWAY_SSH_PORT=25022 \
    PROBE_TIMEOUT_SECS=10 \
    "$H/scripts/deploy-seamless.sh" deploy "$target" "$@" \
    >>"$H/deploy.log" 2>&1
}

fake_host_run_rollback() {
  local target=$1; shift
  : >"$H/rollback.log"
  PATH="$H/stubs:$PATH" \
    HOST_INSTALL_ROOT="$H/opt/llm-gateway-go" \
    FAKE_SSH_TMPDIR="$H" \
    FAKE_TARGET="$target" \
    ENVS_ROOT="$H/envs" \
    TMPDIR="$H/tmp" \
    LLM_GATEWAY_SSH_PORT=25022 \
    "$H/scripts/deploy-seamless.sh" rollback "$target" "$@" \
    >>"$H/rollback.log" 2>&1
}

fake_host_inject_stop_hang() { touch "$H/fx/candidate_stop_hang"; }
fake_host_inject_probe_fail() { touch "$H/fx/port_${1:-8782}_healthz_fail"; }
fake_host_inject_nginx_fail() { touch "$H/fx/nginx_reload_fail"; }
fake_host_inject_prng_dangling() { touch "$H/fx/dangling_slot"; }
fake_host_inject_unverified_release() { touch "$H/fx/extra_unverified"; }
fake_host_clear_injects() { rm -f "$H/fx"/*; }

log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }

fake_host_assert() {
  local msg=$1 shift=2; shift  # drop msg + assert kind
  local kind=$1; shift
  case "$kind" in
    EXISTS) [[ -e "$1" ]] && log_pass "$msg" || log_fail "$msg: missing $1" ;;
    ABSENT) [[ ! -e "$1" ]] && log_pass "$msg" || log_fail "$msg: present $1" ;;
    EQ)     [[ "$(cat "$1" 2>/dev/null)" == "$2" ]] && log_pass "$msg" \
            || log_fail "$msg: file=$1 content=$(cat "$1") want=$2" ;;
    FILE_HAS) grep -qF -- "$2" "$1" 2>/dev/null && log_pass "$msg" \
            || log_fail "$msg: $1 missing [$2]" ;;
    *) log_fail "$msg: unknown assert kind [$kind]" ;;
  esac
}

fake_host_seed_legacy_release() {
  local dir=$1
  mkdir -p "$dir/web"
  printf '#!/usr/bin/env sh\necho v0\n' >"$dir/gateway"
  chmod +x "$dir/gateway"
  printf '<html>v0</html>\n' >"$dir/web/index.html"
  printf '{"version":"v0-legacy","build_seq":0,"git_sha":"cafef00d","build_date":"20260101"}\n' >"$dir/version.json"
  printf 'v0-legacy\n' >"$dir/VERSION"
}

fake_host_assert_release_verified() {
  local release=$1
  if grep -q '"verified":true' \
       "$H/opt/llm-gateway-go/releases/$release/deployment.json"; then
    log_pass "release $release is marked verified"
  else
    log_fail "release $release not verified"
  fi
}

fake_host_assert_current_points_to() {
  local release=$1
  local actual
  actual=$(readlink "$H/opt/llm-gateway-go/current" 2>/dev/null || echo none)
  if [[ "$actual" == *"releases/$release"* ]]; then
    log_pass "current → releases/$release"
  else
    log_fail "current → $actual (wanted releases/$release)"
  fi
}

fake_host_assert_active_port() {
  local want=$1
  local got
  got=$(cat "$H/opt/llm-gateway-go/run/active-port" 2>/dev/null || echo none)
  if [[ "$got" == "$want" ]]; then
    log_pass "active-port = $want"
  else
    log_fail "active-port = $got (wanted $want)"
  fi
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
  if [[ "$actual" == *"releases/$release"* ]]; then
    log_pass "slots/$port → releases/$release"
  else
    log_fail "slots/$port → $actual (wanted releases/$release)"
  fi
}

fake_host_assert_exit_code() {
  local msg=$1 want=$2 got=$3
  if [[ "$want" == "zero" && "$got" -eq 0 ]]; then
    log_pass "$msg (rc=0)"
  elif [[ "$want" == "nonzero" && "$got" -ne 0 ]]; then
    log_pass "$msg (rc=$got ≠ 0)"
  else
    log_fail "$msg (rc=$got, wanted $want)"
  fi
}

# ─── stub bin installer ────────────────────────────────────────────

fake_host_install_stubs() {
  local stubs=$H/stubs

  # ── ssh stub ──────────────────────────────────────────────────────
  # The single seam that turns real ssh calls into chroot-style
  # execution against $FAKE_SSH_TMPDIR. Parses argv to find host
  # and remote script (last arg), rewrites known absolute prefixes
  # (/opt, /etc/llm-gateway-go, /etc/systemd/system, /var/lib/llm-gateway-go,
  #  /var/log/llm-gateway-go, /proc, /tmp), and exec's the rewritten
  # script in bash -c with the fakebin prepended to PATH.
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
[[ -z "$host" ]] && exit 0
[[ -z "$script" ]] && exit 0

h="${FAKE_SSH_TMPDIR:?}"
rewrite() {
  local s="$1"
  s="${s//\/var\/lib\/llm-gateway-go/$h\/var\/lib\/llm-gateway-go}"
  s="${s//\/var\/log\/llm-gateway-go/$h\/var\/log\/llm-gateway-go}"
  s="${s//\/var\/lib\/kx-gateway/$h\/var\/lib\/kx-gateway}"
  s="${s//\/opt\/llm-gateway-go/$h\/opt\/llm-gateway-go}"
  s="${s//\/etc\/llm-gateway-go/$h\/etc\/llm-gateway-go}"
  s="${s//\/etc\/systemd\/system/$h\/etc\/systemd\/system}"
  s="${s//\/proc\//$h\/proc\/}"
  s="${s//\'\/opt\/llm-gateway-go\/\'/\'$h\/opt\/llm-gateway-go\/\'}"
  s="${s//\'\/etc\/llm-gateway-go\/\'/\'$h\/etc\/llm-gateway-go\/\'}"
  printf '%s' "$s"
}
script_r=$(rewrite "$script")
PATH="$h/stubs:$PATH" FAKE_SSH_TMPDIR="$h" bash -c "$script_r"
SSHEOF
  chmod +x "$stubs/ssh"

  # ── systemctl stub ────────────────────────────────────────────────
  cat >"$stubs/systemctl" <<'SYSEOF'
#!/usr/bin/env bash
set -uo pipefail
host="${FAKE_SSH_TMPDIR:?}"
statedir="${FAKE_SSH_TMPDIR:?}/etc/systemd"

is_canary() { [[ "$1" == *"-canary@"* ]]; }
canary_port() {
  local unit=$1
  [[ "$unit" =~ @([0-9]+)\.service$ ]] && printf '%s' "${BASH_REMATCH[1]}"
}
canonical_port() { printf '8781'; }

cmd_show() {
  local prop=$1 unit=$2
  case "$prop" in
    MainPID)
      if [[ -f "$statedir/.unit.$unit" ]] && [[ "$(cat "$statedir/.unit.$unit")" == "active" ]]; then
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
  if [[ -f "$host/fx/candidate_stop_hang" ]] && is_canary "$unit"; then
    printf '%s\n' active >"$statedir/.unit.$unit"
    printf '%s\n' 9999 >"$statedir/.pid.$unit"
    printf '%s\n' "$port" >"$statedir/.port.$unit"
    return 0
  fi
  printf '%s\n' active >"$statedir/.unit.$unit"
  printf '%s\n' $(( 4242 + RANDOM % 1000 )) >"$statedir/.pid.$unit"
  printf '%s\n' "$port" >"$statedir/.port.$unit"
}

cmd_stop() {
  local unit=$1
  printf '%s\n' inactive >"$statedir/.unit.$unit"
}

cmd_is_active() {
  local unit=$1
  if [[ -f "$statedir/.unit.$unit" ]] && [[ "$(cat "$statedir/.unit.$unit")" == "active" ]]; then return 0
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
      action="$a"; shift_args=$(( i+1 )); rest=("${args[@]:shift_args}"); break ;;
  esac
  (( i++ ))
done

case "$action" in
  start)        cmd_start "${rest[0]:-}" ;;
  stop)         cmd_stop  "${rest[0]:-}" ;;
  restart)      cmd_stop "${rest[0]:-}"; cmd_start "${rest[0]:-}" ;;
  is-active)    cmd_is_active "${rest[0]:-}" ;;
  status)       printf '● %s - fake\n' "${rest[0]:-}"; exit 0 ;;
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

  # ── curl stub ─────────────────────────────────────────────────────
  cat >"$stubs/curl" <<'CURLEOF'
#!/usr/bin/env bash
set -uo pipefail
host="${FAKE_SSH_TMPDIR:?}"
statedir="${FAKE_SSH_TMPDIR:?}/etc/systemd"
port=""
url=""
mode_http_code=""
mode_out=""
mode_body="STDOUT"
mode_dash_headers=0
while [[ $# -gt 0 ]]; do
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
    *) if [[ -z "$url" ]]; then url="$1"; port="${1##*:}"; port="${port%%/*}"; fi ;;
  esac
  shift
done

is_maint() { [[ -f "$host/opt/llm-gateway-go/maintenance/UPGRADING" ]]; }
maint_body="系统正在升级 · llm-gateway-go"

code=000; body=""; path="${url#*://}"; path="${path#*/}"

if [[ -f "$host/fx/port_${port}_healthz_fail" ]] && [[ "$path" == *"/healthz" ]]; then
  [[ -n "$mode_http_code" ]] && printf '503\n'
  exit 7
fi

case "$url" in
  https://*)
    if is_maint && [[ "$path" != "healthz" ]]; then
      code=503; body="$maint_body"
      if (( mode_dash_headers )); then
        printf 'HTTP/1.1 503 Service Temporarily Unavailable\r\n'
        printf 'X-LLM-Gateway-Upgrade: in-progress\r\n\r\n'
      fi
    elif [[ -f "$host/opt/llm-gateway-go/run/active-port" ]]; then
      port=$(cat "$host/opt/llm-gateway-go/run/active-port")
      case "$path" in
        healthz|readyz|"") code=200; body="OK" ;;
        version)
          slot="$host/opt/llm-gateway-go/slots/$port"
          if [[ -L "$slot" ]]; then body=$(cat "$slot/version.json" 2>/dev/null) || body="{}"
          else body=$(cat "$host/opt/llm-gateway-go/current/version.json" 2>/dev/null) || body="{}"
          fi ;;
      esac
    fi ;;
  http://127.0.0.1:*)
    case "$path" in
      healthz|readyz|"") code=200; body="OK" ;;
      version)
        slot="$host/opt/llm-gateway-go/slots/$port"
        if [[ -L "$slot" ]]; then body=$(cat "$slot/version.json" 2>/dev/null) || body="{}"
        else body=$(cat "$host/opt/llm-gateway-go/current/version.json" 2>/dev/null) || body="{}"
        fi ;;
      api/auth/token) code=200; body='{"access_token":"fake","api_key":"fake"}' ;;
      api/system/background-tasks) code=200 ;;
    esac ;;
esac

if [[ "$mode_body" == STDOUT ]]; then
  (( mode_dash_headers )) && printf 'HTTP/1.1 %s OK\r\n\r\n' "$code"
  [[ -n "$body" ]] && printf '%s\n' "$body"
  [[ -n "$mode_http_code" ]] && printf '%s\n' "$code"
else
  printf '%s\n' "$body" >"$mode_out"
fi
CURLEOF
  chmod +x "$stubs/curl"

  # ── nginx stub ────────────────────────────────────────────────────
  cat >"$stubs/nginx" <<'NGXEOF'
#!/usr/bin/env bash
[[ -f "${FAKE_SSH_TMPDIR:?}/fx/nginx_reload_fail" && "$1" == "-s" && "$2" == "reload" ]] && exit 1
exit 0
NGXEOF
  chmod +x "$stubs/nginx"

  # ── journalctl stub ────────────────────────────────────────────────
  cat >"$stubs/journalctl" <<'JCEOF'
#!/usr/bin/env bash
if [[ -f "${FAKE_SSH_TMPDIR:?}/fx/pg_disabled_logs" ]]; then
  printf 'postgres disabled: simulated\n'
fi
JCEOF
  chmod +x "$stubs/journalctl"

  # ── ss stub (port occupancy check) ──────────────────────────────────
  cat >"$stubs/ss" <<'SSEOF'
#!/usr/bin/env bash
statedir="${FAKE_SSH_TMPDIR:?}/etc/systemd"
[[ -d "$statedir/.port" ]] || exit 0
ls "$statedir/.port" 2>/dev/null | while read -r p; do
  [[ "$p" == *.owner ]] && continue
  printf 'LISTEN 0 128 *:%s *:*\n' "$p"
done
SSEOF
  chmod +x "$stubs/ss"

  # ── stat stub (GNU -c %Y → BSD -f %m) ────────────────────────────
  cat >"$stubs/stat" <<'STATEOF'
#!/usr/bin/env bash
if [[ "$1" == "-c" && "$2" == "%Y" ]]; then shift 2; exec /usr/bin/stat -f %m "$@"; fi
exec /usr/bin/stat "$@"
STATEOF
  chmod +x "$stubs/stat"

  # ── sha256sum stub ────────────────────────────────────────────────
  cat >"$stubs/sha256sum" <<'SHSEOF'
#!/usr/bin/env bash
if [[ "$1" == "-c" ]]; then
  file="$2"; shift 2; rc=0
  while IFS= read -r line; do
    expect=$(printf '%s' "$line" | awk '{print $1}')
    name=$(printf '%s' "$line" | awk '{print $2}' | sed 's/^\*//')
    actual=$(/usr/bin/shasum -a 256 "$name" 2>/dev/null | awk '{print $1}')
    if [[ "$expect" != "$actual" ]]; then printf '%s: FAILED\n' "$name" >&2; rc=1
    else printf '%s: OK\n' "$name"; fi
  done <"$file"
  exit "$rc"
fi
exec /usr/bin/shasum -a 256 "$@"
SHSEOF
  chmod +x "$stubs/sha256sum"

  # ── chown stub (tolerate root-only failure on macOS) ────────────
  cat >"$stubs/chown" <<'CHOWNEOF'
#!/usr/bin/env bash
exit 0
CHOWNEOF
  chmod +x "$stubs/chown"

  # ── install stub (passthrough to /usr/bin/install) ─────────────────
  cat >"$stubs/install" <<'INSTEOF'
#!/usr/bin/env bash
exec /usr/bin/install "$@"
INSTEOF
  chmod +x "$stubs/install"

  # ── go stub (skip real build; create empty executable) ─────────────
  cat >"$stubs/go" <<'GOEOF'
#!/usr/bin/env bash
out=""; prev=""
for a in "$@"; do
  if [[ "$prev" == "-o" ]]; then out="$a"; fi
  prev="$a"
done
[[ -n "$out" ]] && printf '#!/usr/bin/env sh\necho stub\n' >"$out" && chmod +x "$out"
exit 0
GOEOF
  chmod +x "$stubs/go"

  # ── npm stub (no-op for offline harness) ──────────────────────────
  cat >"$stubs/npm" <<'NPMEOF'
#!/usr/bin/env bash
exit 0
NPMEOF
  chmod +x "$stubs/npm"

  # ── git stub (deterministic identity) ─────────────────────────────
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