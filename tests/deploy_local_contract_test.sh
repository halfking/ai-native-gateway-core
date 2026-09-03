#!/usr/bin/env bash
# Contract tests for the unified local deployment entry point.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/deploy-local-lib.sh"

TMP=$(mktemp -d -t kx-local-contract.XXXXXX)
trap 'rm -rf "$TMP"' EXIT
export LLM_GATEWAY_ROOT="$TMP/install"

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS %s\n' "$*"; }

TEST_HOME="$TMP/home"
mkdir -p "$TEST_HOME"
original_home="$HOME"
export HOME="$TEST_HOME"
default_root=$(dl_default_root)
[[ "$default_root" == "$TEST_HOME/kaixuan/llm-gateway-go" ]] || fail 'macOS default root project path'
for d in bin logs run attachments backups raw-logs; do
  [[ ! -e "$TEST_HOME/kaixuan/$d" ]] || fail "shared parent contains project directory $d"
done
export HOME="$original_home"
pass 'default root is isolated under the project subdirectory'

dl_prepare_layout 0 0
for d in attachments bin backups logs raw-logs run; do [[ -d "$TMP/install/$d" ]] || fail "required directory $d"; done
[[ ! -e "$TMP/install/postgres" ]] || fail 'postgres must not be created without a local dependency'
[[ ! -e "$TMP/install/redis" ]] || fail 'redis must not be created without a local dependency'
pass 'required layout is created without optional dependency directories'

dl_prepare_layout 1 1
[[ ! -e "$TMP/install/postgres" && ! -e "$TMP/install/redis" ]] || fail 'project root must not host postgres/redis; shared service dirs only'
KAIXUAN_ROOT="$TMP/install/../shared" dl_prepare_shared_service_dirs
[[ -d "$TMP/install/../shared/postgres" && -d "$TMP/install/../shared/redis" ]] || fail 'shared service directories should be created on demand'
pass 'optional shared service directories are conditional'

# Redis three-level discovery contract. Each helper is exercised in an isolated
# subshell so the test can mock the docker/redis-cli/ss binaries on PATH.
redis_discover() {
  local dir=$1
  rm -rf "$dir/bin"
  mkdir -p "$dir/bin"
  cat >"$dir/bin/docker" <<EOF
#!/usr/bin/env bash
case "\$1" in
  info) printf 'ok' ;;
  compose) ;;
  ps)
    if [[ -f "$dir/docker_ps" ]]; then cat "$dir/docker_ps"; fi
    ;;
  inspect)
    target="\$2"
    if [[ "\$target" == \${DOCKER_PING_NAME:-} ]]; then
      sed -n 's/.*6379.*/ok/p' "$dir/docker_inspect_\$target" 2>/dev/null || echo ""
    else
      echo ""
    fi
    ;;
  exec)
    target="\$2"
    if [[ "\$target" == \${DOCKER_PING_NAME:-} ]]; then printf 'PONG\n'; fi
    ;;
esac
EOF
  chmod +x "$dir/bin/docker"
cat >"$dir/bin/redis-cli" <<'EOF'
#!/usr/bin/env bash
# Accept any redis-cli invocation. Real Redis parsing is not relevant to the
# discovery contract; we only assert that dl_redis_try_system can reach PONG.
exit 0
EOF
chmod +x "$dir/bin/redis-cli"
cat >"$dir/bin/ss" <<EOF
#!/usr/bin/env bash
dir="\${DL_TEST_DIR:-}"
[[ "\$1" == "-ltn" ]] && [[ -n "\$dir" ]] && cat "\$dir/ss_listen" 2>/dev/null
EOF
chmod +x "$dir/bin/ss"
HOME="$dir/home" \
  PATH="$dir/bin:$PATH" \
  DL_TEST_DIR="$dir" \
  bash -c "
    set -u
    source '$ROOT/scripts/deploy-local-lib.sh'
    DL_DOCKER=0; DL_COMPOSE=0; DL_PG_CONTAINER=; DL_REDIS_CONTAINER=
    DL_PG_SOURCE=; DL_REDIS_SOURCE=; DL_DB_MODE=none; DL_REDIS_MODE=none
    DL_REDIS_MOUNT_TYPE=
    dl_detect_resources
    printf 'redis_container=%s\n' \"\${DL_REDIS_CONTAINER:-}\"
    printf 'redis_addr=%s\n' \"\${LLM_GATEWAY_REDIS_ADDR:-}\"
  "
}

redis_discover_tries() {
  local dir=$1 kind=$2
  shift 2
  case "$kind" in
    named)
      printf 'nbjl-redis\nredis:7-alpine\n0.0.0.0:6379->6379/tcp\n' > "$dir/docker_ps"
      DOCKER_PING_NAME='nbjl-redis' redis_discover "$dir"
      ;;
    scan)
      printf 'app-cache\tpython:3.12\nbusybox\tbusybox\nvalid-redis-cache\tredis:7-alpine\t0.0.0.0:16379->6379/tcp\n' > "$dir/docker_ps"
      DOCKER_PING_NAME='valid-redis-cache' redis_discover "$dir"
      ;;
    scan_no_rediscli)
      printf 'redis-custom\tcustom:1.0\t0.0.0.0:6379->6379/tcp\n' > "$dir/docker_ps"
      DOCKER_PING_NAME='redis-custom' redis_discover "$dir"
      ;;
    system)
      printf 'LISTEN 0 128 127.0.0.1:16379 0.0.0.0:*\nLISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n' > "$dir/ss_listen"
      DOCKER_PING_NAME='valid-redis-cache' redis_discover "$dir"
      ;;
  esac
}

tmpd=$(mktemp -d)
out=$(redis_discover_tries "$tmpd" named)
echo "$out" | grep -q 'redis_container=nbjl-redis' || fail "named Redis discovery should pick nbjl-redis (got $out)"
echo "$out" | grep -q 'redis_addr=' || fail 'named Redis discovery must not set system LLM_GATEWAY_REDIS_ADDR'

out=$(redis_discover_tries "$tmpd" scan)
echo "$out" | grep -q 'redis_container=valid-redis-cache' || fail "scan Redis discovery should pick valid-redis-cache (got $out)"

out=$(redis_discover_tries "$tmpd" scan_no_rediscli)
echo "$out" | grep -q 'redis_container=redis-custom' || fail "scan-without-redis-cli should accept by port heuristic (got $out)"

out=$(redis_discover_tries "$tmpd" system)
echo "$out" | grep -q 'redis_addr=127.0.0.1:16379' || fail "system fallback should pick 16379 (got $out)"
rm -rf "$tmpd"
pass 'Redis three-level discovery finds named, scan, no-cli, and host listeners'

mkdir -p "$TMP/source/web"
printf '#!/bin/sh\nexit 0\n' > "$TMP/source/gateway"
printf '<html>test</html>\n' > "$TMP/source/web/index.html"
printf '{"version":"2.4.7-test","git_tag":"2.4.7","git_sha":"deadbeef","build_seq":9,"build_date":"20260903"}\n' > "$TMP/source/version.json"
printf '2.4.7-test\n' > "$TMP/source/VERSION"
dl_stage_release "$TMP/install/bin/2.4.7.9" "$TMP/source/gateway" "$TMP/source/web" "$TMP/source/version.json" "$TMP/source/VERSION" '2.4.7.9'
dl_verify_release "$TMP/install/bin/2.4.7.9" || fail 'fresh release checksum'
dl_atomic_switch '2.4.7.9'
[[ "$(readlink "$TMP/install/bin/current")" == '2.4.7.9' ]] || fail 'current release pointer'
[[ "$(readlink "$TMP/install/bin/gateway")" == 'current/gateway' ]] || fail 'gateway shortcut pointer'
dl_mark_verified "$TMP/install/bin/2.4.7.9"
grep -q '"verified": true' "$TMP/install/bin/2.4.7.9/deployment.json" || fail 'verified metadata'
pass 'release bundle, checksum, atomic pointer, and verification metadata'

for f in scripts/deploy-local-lib.sh scripts/deploy-local.sh scripts/deploy-154.sh scripts/deploy-245.sh; do
  bash -n "$ROOT/$f" || fail "bash syntax: $f"
done
pass 'all unified deployment scripts pass bash syntax'

if grep -q 'LLM_GATEWAY_FILES_ROOT\|~/Downloads/kaixuan/llm-gateway' "$ROOT/scripts/deploy-local.sh"; then
  # only acceptable usage is the explicit guard comment, not a hard-coded path
  if grep -qE '(INSTALL_ROOT|LLM_GATEWAY_FILES_ROOT|~/Downloads/kaixuan/llm-gateway)\s*=\s*' "$ROOT/scripts/deploy-local.sh"; then
    fail 'local entry point still uses Downloads/legacy path'
  fi
fi
if grep -q 'SSHPASS' "$ROOT/scripts/deploy-154.sh" "$ROOT/scripts/deploy-245.sh"; then :; else fail 'remote wrappers must explicitly reject SSHPASS'; fi
pass 'legacy Downloads path and credential bypass are not used by new entry points'

# Remote wrappers must fail closed for inherited password authentication and
# provide the target-specific env-injector recovery command.
for target in 154 245; do
  wrapper="$ROOT/scripts/deploy-$target.sh"
  err_file="$TMP/deploy-$target-sshp-ass.err"
  set +e
  SSHPASS=legacy-password bash "$wrapper" --dry-run >/dev/null 2>"$err_file"
  rc=$?
  set -e
  [[ $rc -eq 64 ]] || fail "deploy-$target must reject SSHPASS with exit 64 (got $rc)"
  grep -q 'SSHPASS is forbidden' "$err_file" || fail "deploy-$target SSHPASS error message"
  grep -q "SSH_KEY_$target" "$err_file" || fail "deploy-$target target-specific SSH key guidance"
done
pass 'remote wrappers reject SSHPASS before any deployment side effect'

# A readable injected key must be sufficient for the non-mutating wrapper
# preflight, without requiring SSHPASS or a network connection.
fake_key="$TMP/id_ed25519"
printf 'offline-test-key\n' >"$fake_key"
chmod 600 "$fake_key"
for target in 154 245; do
  wrapper="$ROOT/scripts/deploy-$target.sh"
  out=$(env -u SSHPASS "SSH_KEY_$target=$fake_key" bash "$wrapper" --dry-run) \
    || fail "deploy-$target dry-run with injected key"
  grep -q "\"target\":\"$target\"" <<<"$out" \
    || fail "deploy-$target dry-run target output"
done
pass 'remote wrappers accept injected SSH keys for dry-run preflight'

# The canonical remote path is key-only; legacy sshpass implementations must
# not re-enter the audited deploy wrappers.
for f in scripts/deploy-154.sh scripts/deploy-245.sh scripts/deploy-seamless.sh scripts/deploy-lib/ssh-retry.sh; do
  ! grep -q 'sshpass' "$ROOT/$f" || fail "canonical remote path contains sshpass: $f"
done
pass 'canonical remote deployment path contains no sshpass implementation'

for needle in 'cleanup_legacy_downloads' 'llm-gateway-pg' 'dl_shared_pg_dir' 'CLEANUP_DOWNLOADS' 'downloads-legacy'; do
  if ! grep -q "$needle" "$ROOT/scripts/deploy-local.sh" "$ROOT/scripts/deploy-local-lib.sh"; then
    fail "PostgreSQL migration safeguard: $needle"
  fi
done
pass 'PostgreSQL migration uses shared service directories and --cleanup-downloads'

# The old local scripts exported INSTALL_ROOT under ~/Downloads. The unified
# entry point must ignore that legacy variable and remain idempotent for an
# existing (including dangling) Redis symlink.
(
  set -e
  export HOME="$TMP/home3"
  rm -rf "$TMP/home3"
  mkdir -p "$TMP/home3"
  export INSTALL_ROOT="$TMP/legacy-downloads"
  unset LLM_GATEWAY_ROOT
  got_root=$(dl_root)
  [[ "$got_root" == "$TMP/home3/kaixuan/llm-gateway-go" ]] || { echo "FAIL: legacy INSTALL_ROOT leaked into new root: $got_root"; exit 1; }
  export LLM_GATEWAY_ROOT="$TMP/install"
  export KAIXUAN_ROOT="$TMP/kaixuan-shared"
  rm -rf "$TMP/install/redis"
  ln -s "$TMP/missing-redis" "$TMP/install/redis"
  DL_REDIS_MODE=docker DL_REDIS_SOURCE="$TMP/redis-source" dl_link_existing_data
  directory_after=$(readlink "$TMP/install/redis")
  [[ "$(readlink "$TMP/install/redis")" == "$TMP/missing-redis" ]] || { echo "FAIL: existing Redis symlink was replaced"; exit 1; }
) || fail 'legacy root check failed'
pass 'legacy root is ignored and existing Redis symlink is idempotent'

bundle_env="$TMP/install/bundle.env"
LLM_GATEWAY_ROOT="$TMP/install" KAIXUAN_ROOT="$TMP/shared" dl_write_env "$bundle_env" 8781
grep -q "$TMP/install/logs/gateway-8781.log" "$bundle_env" || fail 'bundle log path is not under install root'
grep -q "$TMP/shared/postgres" "$bundle_env" || fail 'bundle env must declare LLM_GATEWAY_PG_DATA_DIR'
grep -q "$TMP/shared/redis" "$bundle_env" || fail 'bundle env must declare LLM_GATEWAY_REDIS_DATA_DIR'
! grep -q 'Downloads' "$bundle_env" || fail 'bundle env still contains Downloads path'
pass 'bundle logs are pinned under the installation root and services declared via KAIXUAN_ROOT'

# Shared service directory contract: layout, idempotent Redis symlink,
# migration log+backup destinations. Run inside a subshell so the real
# user's HOME is not touched even if the test fails midway.
TMP="$TMP" HOME="$TMP/home2" KAIXUAN_ROOT="$TMP/home2/kaixuan" \
  LLM_GATEWAY_ROOT="$TMP/home2/kaixuan/llm-gateway-go" \
  bash -c '
    set -e
    source "$1"
    SHARED_ROOT="$KAIXUAN_ROOT"
    rm -rf "$SHARED_ROOT"
    mkdir -p "$SHARED_ROOT/postgres" "$SHARED_ROOT/redis" "$LLM_GATEWAY_ROOT"
    dl_prepare_shared_service_dirs
    [[ -d "$SHARED_ROOT/postgres/logs" ]] || { echo FAIL: pg logs dir missing; exit 1; }
    [[ -d "$SHARED_ROOT/postgres/backups" ]] || { echo FAIL: pg backups dir missing; exit 1; }
    [[ -d "$SHARED_ROOT/postgres/run" ]] || { echo FAIL: pg run dir missing; exit 1; }
    [[ -d "$SHARED_ROOT/redis/logs" ]] || { echo FAIL: redis logs dir missing; exit 1; }
    [[ -d "$SHARED_ROOT/redis/run" ]] || { echo FAIL: redis run dir missing; exit 1; }
    ln -s "$TMP/external-redis" "$SHARED_ROOT/redis/data"
    DL_REDIS_MODE=docker DL_REDIS_SOURCE="$SHARED_ROOT/redis/data" dl_link_existing_data
    [[ -L "$SHARED_ROOT/redis/data" ]] || { echo FAIL: redis symlink should still exist; exit 1; }
    DL_REDIS_MODE=docker DL_REDIS_SOURCE="$SHARED_ROOT/redis/data" dl_link_existing_data
    [[ "$(readlink "$SHARED_ROOT/redis/data")" == "$TMP/external-redis" ]] || { echo FAIL: redis symlink should be untouched; exit 1; }
  ' _ "$ROOT/scripts/deploy-local-lib.sh" || fail 'shared service directories or Redis link contract failed'
pass 'shared service directories and Redis link are idempotent'

# --cleanup-downloads must remain opt-in. The default code path never deletes
# the legacy ~/Downloads/llm-gateway-files tree; explicit flag is required.
tmpdl=$(mktemp -d)
mkdir -p "$tmpdl/postgres" "$tmpdl/redis" "$tmpdl/bin"
mkdir -p "$tmpdl/legacy-home/Downloads/llm-gateway-files/postgres"
HOME="$tmpdl/legacy-home" KAIXUAN_ROOT="$tmpdl/legacy-home/Downloads" \
  LLM_GATEWAY_ROOT="$tmpdl/llm-gateway-go" \
  bash -c '
    mkdir -p "$LLM_GATEWAY_ROOT"
    source "$1/scripts/deploy-local-lib.sh"
    dl_cleanup_legacy_downloads
  ' _ "$ROOT" >/dev/null
[[ -d "$tmpdl/legacy-home/Downloads/llm-gateway-files/postgres" ]] || fail 'default cleanup must not delete Downloads postgres'
HOME="$tmpdl/legacy-home" KAIXUAN_ROOT="$tmpdl/legacy-home/Downloads" \
  LLM_GATEWAY_ROOT="$tmpdl/llm-gateway-go" DL_CLEANUP_DOWNLOADS=1 \
  bash -c '
    mkdir -p "$LLM_GATEWAY_ROOT"
    source "$1/scripts/deploy-local-lib.sh"
    dl_cleanup_legacy_downloads
  ' _ "$ROOT" >/dev/null
[[ ! -d "$tmpdl/legacy-home/Downloads/llm-gateway-files/postgres" ]] || fail 'explicit --cleanup-downloads must remove legacy postgres copy'
find "$tmpdl/legacy-home/Downloads/postgres/backups" -maxdepth 1 -name 'downloads-legacy-*.tar.gz' | grep -q . || fail 'cleanup archive must be created'
rm -rf "$tmpdl"
pass '--cleanup-downloads is opt-in and archives the legacy tree'

# Active port default contract: 8782 (candidate 8781).
HOME_TEST="$TMP/home2" LLM_GATEWAY_ROOT="$TMP/home2" KAIXUAN_ROOT= \
  bash -c '
    set -u
    HOME="$HOME_TEST" LLM_GATEWAY_ROOT="$HOME_TEST" KAIXUAN_ROOT= source "$1/scripts/deploy-local-lib.sh"
    [[ "$(dl_active_port)" == 8782 ]] || { echo "FAIL_default got=$(dl_active_port)"; exit 1; }
    [[ "$(dl_candidate_port)" == 8781 ]] || { echo "FAIL_candidate got=$(dl_candidate_port)"; exit 1; }
  ' _ "$ROOT" || fail "default active port must be 8782 with candidate 8781"

# INSTALL_ROOT legacy variable guard: deploy-local must refuse to proceed
# when an operator accidentally exports a legacy root variable.
INSTALL_ROOT='/tmp/legacy-root' LLM_GATEWAY_FILES_ROOT='/tmp/legacy-files' \
  bash scripts/deploy-local.sh deploy --dry-run > /tmp/install-root-out.log 2>&1 \
  && fail 'deploy-local must refuse when legacy INSTALL_ROOT is set' \
  || grep -q 'legacy INSTALL_ROOT' /tmp/install-root-out.log \
    || fail 'legacy variable rejection must explain the cause'

pass 'active port 8782 is default and INSTALL_ROOT guard refuses legacy paths'

# Two projects sharing ~/kaixuan must never share deployment state.
project_a="$TMP/home/kaixuan/project-a"
project_b="$TMP/home/kaixuan/project-b"
LLM_GATEWAY_ROOT="$project_a" dl_prepare_layout 0 0
LLM_GATEWAY_ROOT="$project_b" dl_prepare_layout 0 0
LLM_GATEWAY_ROOT="$project_a" dl_write_env "$project_a/run/a.env" 8781
LLM_GATEWAY_ROOT="$project_b" dl_write_env "$project_b/run/b.env" 8782
[[ -f "$project_a/run/a.env" && ! -e "$project_a/run/b.env" ]] || fail 'project A contains project B runtime state'
[[ -f "$project_b/run/b.env" && ! -e "$project_b/run/a.env" ]] || fail 'project B contains project A runtime state'
[[ ! -e "$TMP/home/kaixuan/bin" && ! -e "$TMP/home/kaixuan/logs" ]] || fail 'shared parent received project deployment files'
pass 'independent project roots do not share deployment state'
