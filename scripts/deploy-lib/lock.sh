#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/lock.sh — local + remote lock primitives
#
# Implements spec §"Locking" (Slice 3 of the hardening plan):
#
#   - Local lock: a target-scoped lock that protects deployment state for
#     one remote target. 154 and 245 intentionally use different lock paths.
#     A separate shared build lock protects checkout mutations. Both use
#     `flock` when available, otherwise an atomic `mkdir` lock, and are
#     released via traps on normal and error exits.
#
#   - Remote lock: an atomic `mkdir` lock directory on the target host
#     holding metadata (target, source OS user, source hostname, local
#     PID, UTC start time, commit SHA, version). No secrets are stored.
#
#   Lock contention fails fast — the second caller exits nonzero BEFORE
#   backup / upload and prints the first lock's owner metadata (no
#   secrets). Locks are released by traps on normal and error exits.
#   Age alone never authorizes stale-lock deletion; only `force-unlock`
#   (executed by the operator) can remove a remote lock.
#
#   This module is the lib-level primitive. The `lock_local` and
#   `lock_remote` functions are invoked from scripts/deploy.sh via
#   `with_local_lock` / `with_remote_lock` wrappers. The wrappers are
#   defined here too so that any orchestrator can reuse them.
# =====================================================================

if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "lock.sh: requires bash" >&2
  # shellcheck disable=SC2317  # only reached when invoked directly (not sourced)
  return 1 2>/dev/null || exit 1
fi

# shellcheck disable=SC2317

# Default local lock directory. Lives under the system temp so multiple
# clones of the repo on the same machine do not collide. Deploy orchestrators
# should set LOCK_LOCAL_TARGET and/or LOCK_LOCAL_DIR before acquiring; when a
# target is provided, the default is isolated per target. The path is resolved
# lazily so a later LOCK_LOCAL_TARGET assignment cannot fall back to the legacy
# unscoped path.
lock_ensure_local_dir() {
  if [[ -n "${LOCK_LOCAL_DIR:-}" ]]; then
    return 0
  fi
  if [[ -n "${LOCK_LOCAL_TARGET:-}" ]]; then
    LOCK_LOCAL_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-deploy-${LOCK_LOCAL_TARGET}.lock"
  else
    # Compatibility for direct library callers that do not identify a target.
    LOCK_LOCAL_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-deploy.lock"
  fi
}

# Flock binary probe. When empty the module falls back to mkdir.
: "${LOCK_FLOCK_BIN:=$(command -v flock 2>/dev/null || true)}"
: "${LOCK_BUILD_FLOCK_BIN:=${LOCK_FLOCK_BIN}}"

lock_ensure_build_dir() {
  : "${LOCK_LOCAL_BUILD_DIR:=${TMPDIR:-/tmp}/kx-llm-gateway-build.lock}"
}

lock_write_metadata() {
  local target=${1:-?}
  printf 'target=%s\n' "$target"
  printf 'source_user=%s\n' "${SOURCE_USER:-$(id -un 2>/dev/null || echo unknown)}"
  printf 'source_host=%s\n' "${SOURCE_HOST:-$(hostname 2>/dev/null || echo unknown)}"
  printf 'pid=%s\n' "$$"
  printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'commit=%s\n' "${SOURCE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
  printf 'version=%s\n' "${SOURCE_VERSION:-$(cat version.json 2>/dev/null | sed -n 's/.*\"version\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p' || echo unknown)}"
}

# Lock acquire: when flock is available we use a nonblocking exclusive
# file-descriptor lock. The fd travels back to the caller via a global
# LOCK_LOCAL_FD variable; release_lock_local closes it.
lock_acquire_local() {
  lock_ensure_local_dir
  if [[ -n "$LOCK_FLOCK_BIN" ]]; then
    # Open read/write without truncating an existing holder's metadata. Only
    # truncate after the non-blocking flock succeeds.
    exec {LOCK_LOCAL_FD}<>"$LOCK_LOCAL_DIR"
    if ! "$LOCK_FLOCK_BIN" -n "$LOCK_LOCAL_FD"; then
      echo "ERROR: local lock held at $LOCK_LOCAL_DIR" >&2
      eval "exec ${LOCK_LOCAL_FD}<&-"
      LOCK_LOCAL_FD=
      return 75  # EX_TEMPFAIL — standard "try again" code
    fi
    : >"$LOCK_LOCAL_DIR"
    lock_write_metadata "${LOCK_LOCAL_TARGET:-?}" >&"$LOCK_LOCAL_FD"
    return 0
  fi

  # mkdir fallback — atomic on POSIX. mkdir returns nonzero if the dir
  # already exists, so a losing caller's race is captured here.
  if ! mkdir "$LOCK_LOCAL_DIR" 2>/dev/null; then
    echo "ERROR: local lock held at $LOCK_LOCAL_DIR" >&2
    if [[ -f "$LOCK_LOCAL_DIR/metadata" ]]; then
      cat "$LOCK_LOCAL_DIR/metadata" >&2
    fi
    return 75
  fi
  mkdir -p "$LOCK_LOCAL_DIR"
  {
    printf 'target=%s\n' "${LOCK_LOCAL_TARGET:-?}"
    printf 'source_user=%s\n' "${SOURCE_USER:-$(id -un 2>/dev/null || echo unknown)}"
    printf 'source_host=%s\n' "${SOURCE_HOST:-$(hostname 2>/dev/null || echo unknown)}"
    printf 'pid=%s\n' "$$"
    printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'commit=%s\n' "${SOURCE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
    printf 'version=%s\n' "${SOURCE_VERSION:-$(cat version.json 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' || echo unknown)}"
  } >"$LOCK_LOCAL_DIR/metadata"
}

lock_release_local() {
  lock_ensure_local_dir
  if [[ -n "${LOCK_LOCAL_FD:-}" ]]; then
    eval "exec ${LOCK_LOCAL_FD}<&-"
    LOCK_LOCAL_FD=
    rm -f "$LOCK_LOCAL_DIR"
    return
  fi
  rm -rf "$LOCK_LOCAL_DIR"
}

# Shared build lock. This is intentionally separate from the per-target lock:
# it serializes only mutations of the shared checkout (version files, web/dist,
# and local staging), while allowing target-specific remote phases to overlap.
lock_acquire_build() {
  lock_ensure_build_dir
  if [[ -n "$LOCK_BUILD_FLOCK_BIN" ]]; then
    exec {LOCK_BUILD_FD}<>"$LOCK_LOCAL_BUILD_DIR"
    if ! "$LOCK_BUILD_FLOCK_BIN" -n "$LOCK_BUILD_FD"; then
      echo "ERROR: shared build lock held at $LOCK_LOCAL_BUILD_DIR (requested target=${LOCK_BUILD_TARGET:-?})" >&2
      eval "exec ${LOCK_BUILD_FD}<&-"
      LOCK_BUILD_FD=
      return 75
    fi
    : >"$LOCK_LOCAL_BUILD_DIR"
    lock_write_metadata "${LOCK_BUILD_TARGET:-build}" >&"$LOCK_BUILD_FD"
    return 0
  fi
  if ! mkdir "$LOCK_LOCAL_BUILD_DIR" 2>/dev/null; then
    echo "ERROR: shared build lock held at $LOCK_LOCAL_BUILD_DIR (requested target=${LOCK_BUILD_TARGET:-?})" >&2
    if [[ -f "$LOCK_LOCAL_BUILD_DIR/metadata" ]]; then
      cat "$LOCK_LOCAL_BUILD_DIR/metadata" >&2
    fi
    return 75
  fi
  lock_write_metadata "${LOCK_BUILD_TARGET:-build}" >"$LOCK_LOCAL_BUILD_DIR/metadata"
}

lock_release_build() {
  lock_ensure_build_dir
  if [[ -n "${LOCK_BUILD_FD:-}" ]]; then
    eval "exec ${LOCK_BUILD_FD}<&-"
    LOCK_BUILD_FD=
    rm -f "$LOCK_LOCAL_BUILD_DIR"
    return
  fi
  rm -rf "$LOCK_LOCAL_BUILD_DIR"
}

lock_meta_value() {
  local file=$1 key=$2
  [[ -f "$file" ]] || return 1
  awk -F= -v k="$key" '$1 == k { sub($1 "=", ""); print; exit }' "$file"
}

lock_pid_is_deploy_process() {
  local pid=$1 cmd
  [[ "$pid" =~ ^[0-9]+$ && "$pid" != "$$" ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  cmd=$(ps -p "$pid" -o command= 2>/dev/null || true)
  [[ "$cmd" == *deploy-seamless* || "$cmd" == *deploy-154.sh* || "$cmd" == *deploy-245.sh* ]]
}

# Probe an advisory flock without modifying the file. Return 0 when held,
# 1 when available, and 2 when probing is impossible.
lock_flock_is_held() {
  local file=$1 bin=${2:-${LOCK_FLOCK_BIN:-}} fd
  [[ -n "$bin" ]] || return 2
  exec {fd}<>"$file" 2>/dev/null || return 2
  if "$bin" -n "$fd" >/dev/null 2>&1; then
    eval "exec ${fd}<&-"
    return 1
  fi
  eval "exec ${fd}<&-"
  return 0
}

# Recover a target local lock under explicit operator force. The target path is
# always derived here; caller-provided LOCK_LOCAL_DIR is intentionally ignored.
# A live PID is terminated only when it is identifiable as a deployment
# process. This supports both mkdir locks (directory/metadata) and flock locks
# (regular file containing metadata).
lock_recover_local() {
  local target=$1 force=${2:-0} flock_bin=${3:-${LOCK_FLOCK_BIN:-}} dir meta holder_target holder_pid
  [[ "$target" == 154 || "$target" == 245 ]] || { echo "ERROR: invalid lock target: $target" >&2; return 2; }
  [[ "$force" == 1 ]] || return 0
  dir="${TMPDIR:-/tmp}/kx-llm-gateway-deploy-${target}.lock"
  [[ -e "$dir" ]] || return 0
  if [[ -d "$dir" ]]; then meta="$dir/metadata"; else meta="$dir"; fi
  holder_target=$(lock_meta_value "$meta" target 2>/dev/null || true)
  if [[ -n "$holder_target" && "$holder_target" != "$target" ]]; then
    echo "ERROR: refusing to force-remove $dir: metadata target=$holder_target (requested $target)" >&2
    return 1
  fi
  if [[ ! -f "$meta" ]]; then
    echo "ERROR: refusing to force-remove $dir: lock metadata is missing" >&2
    return 1
  fi
  holder_pid=$(lock_meta_value "$meta" pid 2>/dev/null || true)
  if [[ ! -d "$dir" ]]; then
    local flock_rc=0
    lock_flock_is_held "$dir" "$flock_bin" || flock_rc=$?
    case $flock_rc in
      0) echo "ERROR: refusing to remove live flock lock $dir" >&2; return 75 ;;
      2) echo "ERROR: cannot verify flock lock state for $dir" >&2; return 75 ;;
    esac
  fi
  if [[ "$holder_pid" =~ ^[0-9]+$ ]] && kill -0 "$holder_pid" 2>/dev/null; then
    if ! lock_pid_is_deploy_process "$holder_pid"; then
      echo "ERROR: refusing to kill live non-deploy PID $holder_pid for $dir" >&2
      return 75
    fi
    echo "[force] terminating stale target-$target deploy PID $holder_pid"
    kill "$holder_pid" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      kill -0 "$holder_pid" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$holder_pid" 2>/dev/null; then
      kill -9 "$holder_pid" 2>/dev/null || true
      sleep 1
    fi
    kill -0 "$holder_pid" 2>/dev/null && { echo "ERROR: PID $holder_pid still owns $dir" >&2; return 75; }
  fi
  rm -rf "$dir"
  echo "[force] rebuilt target lock path: $dir"
}

# Recover the shared build lock only when its recorded owner is no longer
# alive. A live owner is never removed because it may be building for either
# target and deleting it would reintroduce shared-checkout races.
lock_recover_build() {
  local force=${1:-0} flock_bin=${2:-${LOCK_BUILD_FLOCK_BIN:-${LOCK_FLOCK_BIN:-}}} meta holder_pid
  [[ "$force" == 1 ]] || return 0
  lock_ensure_build_dir
  [[ -e "$LOCK_LOCAL_BUILD_DIR" ]] || return 0
  if [[ -d "$LOCK_LOCAL_BUILD_DIR" ]]; then meta="$LOCK_LOCAL_BUILD_DIR/metadata"; else meta="$LOCK_LOCAL_BUILD_DIR"; fi
  holder_pid=$(lock_meta_value "$meta" pid 2>/dev/null || true)
  if [[ ! -f "$meta" ]]; then
    echo "ERROR: refusing to force-remove shared build lock: metadata is missing" >&2
    return 1
  fi
  if [[ ! -d "$LOCK_LOCAL_BUILD_DIR" ]]; then
    local flock_rc=0
    lock_flock_is_held "$LOCK_LOCAL_BUILD_DIR" "$flock_bin" || flock_rc=$?
    case $flock_rc in
      0) echo "ERROR: shared build lock is still held by flock; refusing force removal" >&2; return 75 ;;
      2) echo "ERROR: cannot verify shared build flock state" >&2; return 75 ;;
    esac
  fi
  if [[ "$holder_pid" =~ ^[0-9]+$ ]] && kill -0 "$holder_pid" 2>/dev/null; then
    echo "ERROR: shared build lock is still held by live PID $holder_pid; refusing force removal" >&2
    return 75
  fi
  rm -rf "$LOCK_LOCAL_BUILD_DIR"
  echo "[force] rebuilt shared build lock path: $LOCK_LOCAL_BUILD_DIR"
}

# Remote lock recovery. The remote command emits a sentinel for a missing
# lock; SSH/read failures remain failures and are never treated as absence.
lock_recover_remote() {
  local ssh_cmd=$1 target=$2 lock_path=$3 force=${4:-0}
  local payload holder_target holder_pid
  [[ "$force" == 1 ]] || return 0
  payload=$("$ssh_cmd" "if [ ! -e '$lock_path' ]; then printf '__LOCK_MISSING__'; elif [ ! -f '$lock_path/metadata' ]; then printf '__LOCK_MALFORMED__'; else cat '$lock_path/metadata'; fi") || {
    echo "ERROR: cannot read remote lock on $target; refusing force recovery" >&2
    return 75
  }
  [[ "$payload" == "__LOCK_MISSING__" ]] && return 0
  if [[ "$payload" == "__LOCK_MALFORMED__" ]]; then
    echo "[force] removing malformed remote lock on target $target"
  else
    holder_target=$(printf '%s\n' "$payload" | awk -F= '$1=="target" {sub($1 "=", ""); print; exit}')
    if [[ -z "$holder_target" || "$holder_target" != "$target" ]]; then
      echo "ERROR: remote lock target=${holder_target:-?} does not match requested target=$target" >&2
      return 1
    fi
    # lock_acquire_remote records the source/deployer PID, not a PID on the
    # target host. Never remove the lock while that source deploy is alive.
    holder_pid=$(printf '%s\n' "$payload" | awk -F= '$1=="pid" {sub($1 "=", ""); print; exit}')
    if [[ "$holder_pid" =~ ^[0-9]+$ ]] && kill -0 "$holder_pid" 2>/dev/null; then
      echo "ERROR: remote lock belongs to live source PID $holder_pid; refusing force recovery" >&2
      return 75
    fi
    echo "[force] removing confirmed stale remote lock for target $target"
  fi
  "$ssh_cmd" "rm -rf '$lock_path'" || { echo "ERROR: failed to remove remote lock on $target" >&2; return 75; }
}

# Remote lock — invoked via SSH. The contract is "atomic mkdir"; we do
# not pipe sensitive data into the lock. The caller is responsible for
# staging the ssh command (the tests stub it via PATH).
lock_acquire_remote() {
  local ssh_cmd=$1 target=$2 lock_path=$3 metadata metadata_b64 lock_q metadata_q owner_token owner_q
  owner_token="${LOCK_REMOTE_OWNER_TOKEN:-}"
  if [[ -z "$owner_token" ]]; then
    if command -v openssl >/dev/null 2>&1; then
      owner_token=$(openssl rand -hex 32)
    else
      owner_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
    fi
  fi
  LOCK_REMOTE_OWNER_TOKEN=$owner_token
  metadata=$(cat <<EOF
target=$target
source_user=${SOURCE_USER:-$(id -un 2>/dev/null || echo unknown)}
source_host=${SOURCE_HOST:-$(hostname 2>/dev/null || echo unknown)}
pid=$$
owner_token=$owner_token
started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
commit=${SOURCE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}
version=${SOURCE_VERSION:-unknown}
EOF
)
  metadata_b64=$(printf '%s\n' "$metadata" | base64 | tr -d '\n')
  printf -v lock_q '%q' "$lock_path"
  printf -v metadata_q '%q' "$metadata_b64"
  printf -v owner_q '%q' "$owner_token"
  if ! "$ssh_cmd" "LOCK_PATH=$lock_q METADATA_B64=$metadata_q OWNER_TOKEN=$owner_q bash -s" <<'REMOTE_LOCK'
set -euo pipefail
umask 077
# The lock parent dir (e.g. /var/lib/llm-gateway-go) must exist before the
# atomic mkdir of the lock directory itself. Creating only the parent is
# a benign race — two concurrent callers both succeed, only one wins the
# inner mkdir "$LOCK_PATH"; without this, a missing parent makes mkdir
# fail with ENOENT and surfaces as "initialization failed".
mkdir -p "$(dirname "$LOCK_PATH")"
if ! mkdir "$LOCK_PATH" 2>/dev/null; then
  cat "$LOCK_PATH/metadata" >&2 2>/dev/null || true
  exit 75
fi
cleanup_partial_lock() {
  rm -rf "$LOCK_PATH"
}
trap cleanup_partial_lock EXIT HUP INT TERM
printf '%s' "$METADATA_B64" | base64 -d > "$LOCK_PATH/metadata.tmp"
test -s "$LOCK_PATH/metadata.tmp"
mv "$LOCK_PATH/metadata.tmp" "$LOCK_PATH/metadata"
trap - EXIT HUP INT TERM
REMOTE_LOCK
  then
    echo "ERROR: remote lock held or initialization failed at $lock_path on $target" >&2
    return 75
  fi
}

lock_release_remote() {
  local ssh_cmd=$1 lock_path=$2 owner_token=${LOCK_REMOTE_OWNER_TOKEN:-}
  [[ -n "$owner_token" ]] || {
    echo "ERROR: refusing remote lock release without owner token" >&2
    return 64
  }
  local lock_q owner_q
  printf -v lock_q '%q' "$lock_path"
  printf -v owner_q '%q' "$owner_token"
  "$ssh_cmd" "LOCK_PATH=$lock_q OWNER_TOKEN=$owner_q bash -s" <<'REMOTE_UNLOCK'
set -euo pipefail
[[ -d "$LOCK_PATH" ]] || exit 0
metadata="$LOCK_PATH/metadata"
[[ -f "$metadata" ]] || exit 0
current_token=$(awk -F= '$1=="owner_token" {sub($1 "=", ""); print; exit}' "$metadata")
[[ "$current_token" == "$OWNER_TOKEN" ]] || exit 0
rm -rf "$LOCK_PATH"
REMOTE_UNLOCK
}

# Force-unlock — operator-driven remote lock removal. Only this entry
# point is allowed to bypass the staleness check; age alone is never
# enough.
#
# This is the simplest "remove the lock" primitive. It performs NO
# validation: no target check, no live-PID check, no SSH-read-failure
# detection. The recommended operator entry point is
# `scripts/deploy-lib/unlock-remote.sh <target> --force` which DOES
# validate metadata and fails closed on SSH/read errors.
#
# This function is kept as:
#   - a thin test helper (tests/deploy_lock_test.sh::AC-L6 uses it
#     against a fake ssh) so the lock library owns a canonical
#     "force-remove" primitive that mirrors the real production command,
#   - an escape hatch for orchestrators that have already validated the
#     removal out of band (e.g. recovery scripts that have confirmed
#     the holder is dead via a separate probe).
force_unlock_remote() {
  local ssh_cmd=$1 lock_path=$2
  echo "WARN: removing remote lock at $lock_path on operator request"
  "$ssh_cmd" "rm -rf '$lock_path'"
}

# Wrapper: run body with the local lock held; release on any exit.
# Usage: with_local_lock <target> <body-command...>
with_local_lock() {
  local target=$1; shift
  if [[ -z "${LOCK_LOCAL_DIR:-}" ]]; then
    LOCK_LOCAL_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-deploy-${target}.lock"
  fi
  LOCK_LOCAL_TARGET=$target lock_acquire_local
  trap 'lock_release_local' EXIT
  "$@"
}